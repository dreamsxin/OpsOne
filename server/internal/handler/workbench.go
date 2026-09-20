package handler

import (
	"log"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"ops-platform/server/internal/middleware"
	"ops-platform/server/internal/model"
	"ops-platform/server/internal/response"
)

// PersonalWorkbench 个人工作台：待办、我负责的资源、我最近的动作
func (h *Handler) PersonalWorkbench(c *gin.Context) {
	user := middleware.CurrentUser(c)

	var unreadMsg, myCronJobs, myHosts int64
	h.DB.Model(&model.Message{}).Where("user_id = ? AND read = ?", user.ID, false).Count(&unreadMsg)
	h.DB.Model(&model.CronJob{}).Where("created_by = ?", user.ID).Count(&myCronJobs)
	h.DB.Model(&model.Host{}).Where("created_by = ?", user.ID).Count(&myHosts)

	// 待处理告警只统计当前用户数据范围内能看到的主机之外的全局告警，
	// 告警暂未与部门绑定，这里给出全局未恢复数量
	var firingAlerts, criticalAlerts int64
	h.DB.Model(&model.Alert{}).Where("status <> ?", "resolved").Count(&firingAlerts)
	h.DB.Model(&model.Alert{}).
		Where("status <> ? AND severity = ?", "resolved", "critical").Count(&criticalAlerts)

	var recentJobs []model.ExecJob
	h.DB.Where("created_by = ?", user.ID).Order("id desc").Limit(5).Find(&recentJobs)

	var recentSessions []model.Session
	h.DB.Where("user_id = ?", user.ID).Order("id desc").Limit(5).Find(&recentSessions)

	var pendingCron []model.CronJob
	h.DB.Where("created_by = ? AND enabled = ?", user.ID, true).
		Order("last_run_at desc").Limit(5).Find(&pendingCron)

	response.OK(c, gin.H{
		"profile": gin.H{
			"username": user.Username, "nickname": user.Nickname,
			"deptId": user.DeptID, "lastLoginAt": user.LastLoginAt,
		},
		"todo": gin.H{
			"unreadMessages": unreadMsg,
			"firingAlerts":   firingAlerts,
			"criticalAlerts": criticalAlerts,
		},
		"mine": gin.H{
			"hosts":    myHosts,
			"cronJobs": myCronJobs,
		},
		"recentJobs":     recentJobs,
		"recentSessions": recentSessions,
		"cronJobList":    pendingCron,
	})
}

// MyResources 我的资源：当前用户数据范围内的资源汇总
func (h *Handler) MyResources(c *gin.Context) {
	user := middleware.CurrentUser(c)
	scope := h.resolveScope(user)

	count := func(conds ...any) int64 {

		var n int64
		q := h.applyHostScope(h.DB.Model(&model.Host{}), user)
		if len(conds) > 0 {
			q = q.Where(conds[0], conds[1:]...)
		}
		q.Count(&n)
		return n
	}

	type groupRow struct {
		Name  string `json:"name"`
		Count int64  `json:"count"`
	}
	var byEnv, byDept []groupRow
	h.applyHostScope(h.DB.Model(&model.Host{}), user).
		Select("env as name, count(*) as count").Group("env").Scan(&byEnv)
	h.applyHostScope(h.DB.Model(&model.Host{}), user).
		Select("dept_id as name, count(*) as count").Group("dept_id").Scan(&byDept)

	// 把部门 ID 换成名称，未归属显示为「未归属」
	deptNames := map[string]string{}
	var depts []model.Department
	h.DB.Find(&depts)
	for _, dept := range depts {
		deptNames[strconv.FormatUint(uint64(dept.ID), 10)] = dept.Name
	}

	for i := range byDept {
		if byDept[i].Name == "0" {
			byDept[i].Name = "未归属"
			continue
		}
		if name, ok := deptNames[byDept[i].Name]; ok {
			byDept[i].Name = name
		} else {
			byDept[i].Name = "#" + byDept[i].Name
		}
	}

	var hosts []model.Host
	h.applyHostScope(h.DB.Model(&model.Host{}), user).
		Order("status asc, id desc").Limit(10).Find(&hosts)

	var myCron int64
	h.DB.Model(&model.CronJob{}).Where("created_by = ?", user.ID).Count(&myCron)

	response.OK(c, gin.H{
		"scope": scope,
		"hostStats": gin.H{
			"total":   count(),
			"online":  count("status = ?", "online"),
			"offline": count("status = ?", "offline"),
			"unknown": count("status = ?", "unknown"),
			"prod":    count("env = ?", "prod"),
		},
		"byEnv":      byEnv,
		"byDept":     byDept,
		"hosts":      hosts,
		"myCronJobs": myCron,
	})
}

// MyActivity 我的活动：本人的写操作流水
func (h *Handler) MyActivity(c *gin.Context) {
	user := middleware.CurrentUser(c)
	page, size := pageParams(c)

	q := h.DB.Model(&model.AuditLog{}).Where("user_id = ?", user.ID)
	var total int64
	if err := q.Count(&total).Error; err != nil {
		response.Error(c, "查询操作流水失败")
		return
	}
	var list []model.AuditLog
	if err := q.Order("id desc").Offset((page - 1) * size).Limit(size).Find(&list).Error; err != nil {
		response.Error(c, "查询操作流水失败")
		return
	}
	response.OKPage(c, list, total, page, size)
}

// MySessions 我的终端会话
func (h *Handler) MySessions(c *gin.Context) {
	user := middleware.CurrentUser(c)
	page, size := pageParams(c)

	q := h.DB.Model(&model.Session{}).Where("user_id = ?", user.ID)
	var total int64
	if err := q.Count(&total).Error; err != nil {
		response.Error(c, "查询会话失败")
		return
	}
	var list []model.Session
	if err := q.Order("id desc").Offset((page - 1) * size).Limit(size).Find(&list).Error; err != nil {
		response.Error(c, "查询会话失败")
		return
	}
	response.OKPage(c, list, total, page, size)
}

// MyExecJobs 我发起的批量执行
func (h *Handler) MyExecJobs(c *gin.Context) {
	user := middleware.CurrentUser(c)
	page, size := pageParams(c)

	q := h.DB.Model(&model.ExecJob{}).Where("created_by = ?", user.ID)
	var total int64
	if err := q.Count(&total).Error; err != nil {
		response.Error(c, "查询执行记录失败")
		return
	}
	var list []model.ExecJob
	if err := q.Order("id desc").Offset((page - 1) * size).Limit(size).Find(&list).Error; err != nil {
		response.Error(c, "查询执行记录失败")
		return
	}
	response.OKPage(c, list, total, page, size)
}

// CleanupRecordings 按配置清理过期会话录像，0 表示永久保留。
// 启动时执行一次，避免录像目录无限增长。
func (h *Handler) CleanupRecordings() {
	keepDays := h.configInt(CfgRecordKeepDay, 0)
	if keepDays <= 0 {
		return
	}

	deadline := time.Now().AddDate(0, 0, -keepDays)
	entries, err := os.ReadDir(h.Cfg.RecordDir)
	if err != nil {
		return // 目录还不存在属正常情况
	}

	removed := 0
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".cast" {
			continue
		}
		info, err := entry.Info()
		if err != nil || info.ModTime().After(deadline) {
			continue
		}
		path := filepath.Join(h.Cfg.RecordDir, entry.Name())
		if err := os.Remove(path); err == nil {
			removed++
			// 录像已删除，清空数据库中的路径，回放接口会提示已丢失
			h.DB.Model(&model.Session{}).Where("record_path = ?", path).Update("record_path", "")
		}
	}
	if removed > 0 {
		log.Printf("[cleanup] 已清理 %d 个超过 %d 天的会话录像", removed, keepDays)
	}
}
