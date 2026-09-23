package handler

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"ops-platform/server/internal/middleware"
	"ops-platform/server/internal/model"
	"ops-platform/server/internal/response"
	"ops-platform/server/internal/sshx"
)

// defaultExecConcurrency 并发缺省值，实际取值来自配置项 exec.concurrency，
// 避免一次下发几百台把本机文件描述符打满
const defaultExecConcurrency = 10

// execOverallCap 一次下发的整体上限。
//
// 原来手动下发**没有整体超时**：只有单台超时，而整体墙钟是
// `ceil(台数/并发) × 单台超时`。200 台 × 600 秒最坏能让一个同步 HTTP 请求挂三小时，
// 中间任何反向代理的 proxy_read_timeout 都会先断，而作业还在跑 ——
// 表现是「页面报错了但命令其实执行了」，这是最糟的一种不确定。
// 现在按台数算出预期上限并卡在 1 小时：超了就按超时收尾，记录写 canceled 而不是 finished。
const execOverallCap = time.Hour

// execCancelRegistry 正在跑的作业 → 取消函数。
//
// 以前没有「停止这次下发」：唯一的中断方式是关浏览器（请求 ctx 被取消），
// 而那样别人看到「有人往生产刷了个错命令」时束手无策，记录里也照样写成 finished。
type execCancelRegistry struct {
	mu    sync.Mutex
	items map[uint]context.CancelFunc
}

func newExecCancelRegistry() *execCancelRegistry {
	return &execCancelRegistry{items: map[uint]context.CancelFunc{}}
}

func (r *execCancelRegistry) add(jobID uint, cancel context.CancelFunc) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.items[jobID] = cancel
}

func (r *execCancelRegistry) remove(jobID uint) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.items, jobID)
}

// cancel 取消指定作业。返回 false 表示这个作业不在本进程里跑
// （已经结束，或者是另一个实例发起的 —— 取消是进程内的，这一点写在页面上）
func (r *execCancelRegistry) cancel(jobID uint) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	cancel, ok := r.items[jobID]
	if !ok {
		return false
	}
	cancel()
	delete(r.items, jobID)
	return true
}

// overallTimeout 按台数与并发度推出整体上限
func (h *Handler) overallTimeout(hostCount, perHost int) time.Duration {
	concurrency := h.configInt(CfgExecConcurr, defaultExecConcurrency)
	if concurrency < 1 {
		concurrency = 1
	}
	rounds := (hostCount + concurrency - 1) / concurrency
	// +60 秒给建连、落库这些固定开销留余量
	total := time.Duration(rounds*perHost+60) * time.Second
	if total > execOverallCap {
		return execOverallCap
	}
	return total
}

// ExecRequest 一次批量执行的入参，手动下发与定时任务共用
type ExecRequest struct {
	Name      string
	Command   string
	Timeout   int
	HostIDs   []uint
	UserID    uint
	Operator  string
	Source    string // manual | script | cron
	CronJobID uint
	// ConfirmProd 调用方已显式确认「要往生产主机上下发」。
	// 目标里有生产主机而这里是 false 时，下发会被闸门拒绝（见 exec_guard.go）。
	ConfirmProd bool
	// ClientIP 仅用于闸门流水留痕，调度器触发时为空
	ClientIP string
}

// RunOnHosts 在指定主机上并发执行命令并落库，同步等待全部主机结束。
//
// 所有下发路径（批量执行 / 脚本下发 / 定时任务立即执行与调度触发）都走这里，
// 下发闸门也挂在这里——见 exec_guard.go 的说明。
func (h *Handler) RunOnHosts(ctx context.Context, req ExecRequest) (*model.ExecJob, error) {
	if req.Command == "" {
		return nil, errors.New("命令不能为空")
	}
	timeout := req.Timeout
	if timeout <= 0 || timeout > 600 {
		timeout = 60
	}

	var hosts []model.Host
	if err := h.DB.Where("id IN ?", req.HostIDs).Find(&hosts).Error; err != nil {
		return nil, err
	}
	if len(hosts) == 0 {
		return nil, errors.New("目标主机不存在")
	}

	// 下发闸门：命令规则与生产确认。被拦下的尝试不会产生执行记录，所以单独留痕。
	decision := h.inspectExec(req.Command, hosts, req.ConfirmProd)
	h.recordExecGuard(req, hosts, decision)
	if decision.Status == "blocked" {
		return nil, errors.New(decision.Reason)
	}

	name := req.Name
	if name == "" {
		name = "批量执行"
	}
	source := req.Source
	if source == "" {
		source = "manual"
	}

	job := model.ExecJob{
		Name: name, Command: req.Command, Timeout: timeout, Status: "running",
		Source: source, CronJobID: req.CronJobID,
		CreatedBy: req.UserID, Operator: req.Operator,
		Total: len(hosts), StartedAt: time.Now(),
		TargetIDs:  uintsJSON(req.HostIDs),
		RiskStatus: decision.Status, RiskHits: hitsJSON(decision.Hits),
		ProdCount: len(decision.ProdHosts), ProdConfirmed: req.ConfirmProd,
	}
	if err := h.DB.Create(&job).Error; err != nil {
		return nil, err
	}

	// 整体超时 + 可取消：两者共用一个 ctx，取消注册表让别人也能停掉这次下发
	runCtx, cancel := context.WithTimeout(ctx, h.overallTimeout(len(hosts), timeout))
	defer cancel()
	h.execCancels.add(job.ID, cancel)
	defer h.execCancels.remove(job.ID)

	results := h.fanOut(runCtx, hosts, req.Command, timeout, job.ID)
	for _, r := range results {
		if r.Status == "success" {
			job.SuccessNum++
		} else {
			job.FailedNum++
		}
	}

	finished := time.Now()
	job.FinishedAt = &finished
	job.Status = "finished"
	// 被取消或整体超时的作业不能写成 finished —— 那会让「这次是被打断的」
	// 在记录上完全看不出来，而后面有人按这条记录判断「命令已经全部执行过」
	if runCtx.Err() != nil {
		job.Status = "canceled"
		if job.CanceledBy == "" {
			job.CanceledBy = "整体超时或被中断"
		}
	}
	h.DB.Model(&job).Select("status", "success_num", "failed_num", "finished_at", "canceled_by").Updates(job)

	if len(results) > 0 {
		h.DB.Create(&results)
	}
	job.Results = results
	return &job, nil
}

type execReq struct {
	Name    string `json:"name"`
	Command string `json:"command" binding:"required"`
	HostIDs []uint `json:"hostIds" binding:"required,min=1"`
	Timeout int    `json:"timeout"`
	// ConfirmProd 目标含生产主机时必须为 true，否则后端拒绝下发
	ConfirmProd bool `json:"confirmProd"`
}

// RunExecJob 手动批量下发命令，同步返回全部结果
func (h *Handler) RunExecJob(c *gin.Context) {
	var req execReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "命令与目标主机不能为空")
		return
	}

	user := middleware.CurrentUser(c)
	// 只允许下发到数据权限内的主机，越权的 ID 直接剔除
	allowed := h.filterVisibleHostIDs(user, req.HostIDs)
	if len(allowed) == 0 {
		response.Forbidden(c, "目标主机不在你的数据权限范围内")
		return
	}
	// 生产主机要单独的权限码：原来只靠前端回传的 confirmProd，直接调接口就能绕过
	if !h.ensureProdPerm(c, allowed) {
		return
	}

	job, err := h.RunOnHosts(c.Request.Context(), ExecRequest{
		Name: req.Name, Command: req.Command, Timeout: req.Timeout, HostIDs: allowed,
		UserID: user.ID, Operator: user.Username, Source: "manual",
		ConfirmProd: req.ConfirmProd, ClientIP: c.ClientIP(),
	})
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.OK(c, job)
}

func (h *Handler) fanOut(ctx context.Context, hosts []model.Host, command string, timeout int, jobID uint) []model.ExecResult {
	results := make([]model.ExecResult, len(hosts))
	sem := make(chan struct{}, h.configInt(CfgExecConcurr, defaultExecConcurrency))
	var wg sync.WaitGroup

	for i := range hosts {
		wg.Add(1)
		go func(idx int, host model.Host) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			item := model.ExecResult{
				JobID: jobID, HostID: host.ID, HostName: host.Name, Address: host.Address,
				Status: "failed", ExitCode: -1,
			}

			runCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
			defer cancel()
			res := sshx.Run(runCtx, h.target(&host), command)

			item.Status, item.ExitCode = res.Status, res.ExitCode
			item.Stdout, item.Stderr, item.CostMs = res.Stdout, res.Stderr, res.CostMs
			results[idx] = item
		}(i, hosts[i])
	}
	wg.Wait()
	return results
}

// ListExecJobs 执行历史，可按来源或定时任务过滤
func (h *Handler) ListExecJobs(c *gin.Context) {
	page, size := pageParams(c)
	q := h.DB.Model(&model.ExecJob{})
	if source := c.Query("source"); source != "" {
		q = q.Where("source = ?", source)
	}
	if cronID := c.Query("cronJobId"); cronID != "" {
		q = q.Where("cron_job_id = ?", cronID)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		response.Error(c, "查询执行历史失败")
		return
	}
	var list []model.ExecJob
	if err := q.Order("id desc").Offset((page - 1) * size).Limit(size).Find(&list).Error; err != nil {
		response.Error(c, "查询执行历史失败")
		return
	}
	response.OKPage(c, list, total, page, size)
}

// GetExecJob 执行详情含每台主机输出
func (h *Handler) GetExecJob(c *gin.Context) {
	var job model.ExecJob
	if err := h.DB.Preload("Results").First(&job, idParam(c)).Error; err != nil {
		response.NotFound(c, "作业不存在")
		return
	}
	response.OK(c, job)
}
