package handler

import (
	"context"
	"encoding/json"
	"log"
	"time"

	"github.com/gin-gonic/gin"

	"ops-platform/server/internal/middleware"
	"ops-platform/server/internal/model"
	"ops-platform/server/internal/response"
	"ops-platform/server/internal/scheduler"
)

// cronJobView 接口返回结构，把 HostIDs 从 JSON 字符串还原成数组
type cronJobView struct {
	model.CronJob
	HostIDs []uint `json:"hostIds"`
}

func toCronView(job model.CronJob) cronJobView {
	view := cronJobView{CronJob: job}
	view.HostIDs = parseHostIDs(job.HostIDs)
	return view
}

func parseHostIDs(raw string) []uint {
	ids := make([]uint, 0)
	if raw == "" {
		return ids
	}
	if err := json.Unmarshal([]byte(raw), &ids); err != nil {
		log.Printf("[scheduler] 主机列表解析失败: %v", err)
	}
	return ids
}

type cronJobReq struct {
	Name    string `json:"name" binding:"required"`
	Spec    string `json:"spec" binding:"required"`
	Command string `json:"command" binding:"required"`
	HostIDs []uint `json:"hostIds" binding:"required,min=1"`
	Timeout int    `json:"timeout"`
	Enabled *bool  `json:"enabled"`
}

// ListCronJobs 定时任务列表
func (h *Handler) ListCronJobs(c *gin.Context) {
	page, size := pageParams(c)
	q := h.DB.Model(&model.CronJob{})
	if kw := c.Query("keyword"); kw != "" {
		q = q.Where("name LIKE ?", "%"+kw+"%")
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		response.Error(c, "查询定时任务失败")
		return
	}
	var list []model.CronJob
	if err := q.Order("id desc").Offset((page - 1) * size).Limit(size).Find(&list).Error; err != nil {
		response.Error(c, "查询定时任务失败")
		return
	}

	views := make([]cronJobView, 0, len(list))
	for _, job := range list {
		views = append(views, toCronView(job))
	}
	response.OKPage(c, views, total, page, size)
}

// CreateCronJob 新建定时任务
func (h *Handler) CreateCronJob(c *gin.Context) {
	var req cronJobReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "任务名称、cron 表达式、命令与目标主机均为必填")
		return
	}
	if err := scheduler.ValidateSpec(req.Spec); err != nil {
		response.BadRequest(c, "cron 表达式非法: "+err.Error())
		return
	}

	user := middleware.CurrentUser(c)
	if len(h.filterVisibleHostIDs(user, req.HostIDs)) != len(req.HostIDs) {
		response.Forbidden(c, "目标主机中包含你无权访问的主机")
		return
	}

	hostIDs, err := json.Marshal(req.HostIDs)
	if err != nil {
		response.Error(c, "主机列表序列化失败")
		return
	}

	job := model.CronJob{
		Name: req.Name, Spec: req.Spec, Command: req.Command, HostIDs: string(hostIDs),
		Timeout: normalizeTimeout(req.Timeout), Enabled: true,
		CreatedBy: user.ID, Operator: user.Username,
	}
	if req.Enabled != nil {
		job.Enabled = *req.Enabled
	}
	if err := h.DB.Create(&job).Error; err != nil {
		response.Error(c, "任务创建失败")
		return
	}
	if err := h.Sched.Sync(job.ID); err != nil {
		response.Error(c, "任务已保存但注册调度失败: "+err.Error())
		return
	}
	response.OK(c, toCronView(job))
}

// UpdateCronJob 编辑定时任务
func (h *Handler) UpdateCronJob(c *gin.Context) {
	var job model.CronJob
	if err := h.DB.First(&job, idParam(c)).Error; err != nil {
		response.NotFound(c, "任务不存在")
		return
	}
	var req cronJobReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "参数校验失败")
		return
	}
	if err := scheduler.ValidateSpec(req.Spec); err != nil {
		response.BadRequest(c, "cron 表达式非法: "+err.Error())
		return
	}

	hostIDs, err := json.Marshal(req.HostIDs)
	if err != nil {
		response.Error(c, "主机列表序列化失败")
		return
	}

	job.Name, job.Spec, job.Command = req.Name, req.Spec, req.Command
	job.HostIDs = string(hostIDs)
	job.Timeout = normalizeTimeout(req.Timeout)
	if req.Enabled != nil {
		job.Enabled = *req.Enabled
	}
	if err := h.DB.Save(&job).Error; err != nil {
		response.Error(c, "任务更新失败")
		return
	}
	if err := h.Sched.Sync(job.ID); err != nil {
		response.Error(c, "任务已保存但注册调度失败: "+err.Error())
		return
	}
	response.OK(c, toCronView(job))
}

// DeleteCronJob 删除定时任务，历史执行记录保留
func (h *Handler) DeleteCronJob(c *gin.Context) {
	id := idParam(c)
	if err := h.DB.Delete(&model.CronJob{}, id).Error; err != nil {
		response.Error(c, "任务删除失败")
		return
	}
	h.Sched.Remove(id)
	response.OK(c, nil)
}

// RunCronJobNow 立即执行一次，不影响既有调度计划
func (h *Handler) RunCronJobNow(c *gin.Context) {
	var job model.CronJob
	if err := h.DB.First(&job, idParam(c)).Error; err != nil {
		response.NotFound(c, "任务不存在")
		return
	}

	execJob, err := h.RunOnHosts(c.Request.Context(), ExecRequest{
		Name: job.Name, Command: job.Command, Timeout: job.Timeout,
		HostIDs: parseHostIDs(job.HostIDs), UserID: job.CreatedBy,
		Operator: middleware.CurrentUser(c).Username, Source: "cron", CronJobID: job.ID,
	})
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	h.finishCronRun(&job, execJob)
	response.OK(c, execJob)
}

// ExecuteCronJob 由调度器触发，注入到 scheduler.New
func (h *Handler) ExecuteCronJob(job model.CronJob) {
	hostIDs := parseHostIDs(job.HostIDs)
	if len(hostIDs) == 0 {
		log.Printf("[scheduler] 任务 %d(%s) 没有目标主机，跳过", job.ID, job.Name)
		return
	}

	// 整体超时留出建连与收尾余量，避免单次触发无限占用调度线程
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(job.Timeout+60)*time.Second)
	defer cancel()

	execJob, err := h.RunOnHosts(ctx, ExecRequest{
		Name: job.Name, Command: job.Command, Timeout: job.Timeout, HostIDs: hostIDs,
		UserID: job.CreatedBy, Operator: "scheduler", Source: "cron", CronJobID: job.ID,
	})
	if err != nil {
		log.Printf("[scheduler] 任务 %d(%s) 执行失败: %v", job.ID, job.Name, err)
		now := time.Now()
		h.DB.Model(&model.CronJob{ID: job.ID}).Updates(map[string]any{
			"last_status": "failed", "last_run_at": &now, "run_count": job.RunCount + 1,
		})
		return
	}
	h.finishCronRun(&job, execJob)
}

// finishCronRun 回写任务的最近执行状态
func (h *Handler) finishCronRun(job *model.CronJob, execJob *model.ExecJob) {
	status := "partial"
	switch {
	case execJob.FailedNum == 0:
		status = "success"
	case execJob.SuccessNum == 0:
		status = "failed"
	}

	now := time.Now()
	err := h.DB.Model(&model.CronJob{ID: job.ID}).Updates(map[string]any{
		"last_status": status,
		"last_run_at": &now,
		"last_job_id": execJob.ID,
		"run_count":   job.RunCount + 1,
	}).Error
	if err != nil {
		log.Printf("[scheduler] 任务 %d 状态回写失败: %v", job.ID, err)
	}
}

func normalizeTimeout(t int) int {
	if t <= 0 || t > 600 {
		return 60
	}
	return t
}
