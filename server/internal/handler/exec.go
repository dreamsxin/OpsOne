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
		RiskStatus: decision.Status, RiskHits: hitsJSON(decision.Hits),
		ProdCount: len(decision.ProdHosts), ProdConfirmed: req.ConfirmProd,
	}
	if err := h.DB.Create(&job).Error; err != nil {
		return nil, err
	}

	results := h.fanOut(ctx, hosts, req.Command, timeout, job.ID)
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
	h.DB.Model(&job).Select("status", "success_num", "failed_num", "finished_at").Updates(job)

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
