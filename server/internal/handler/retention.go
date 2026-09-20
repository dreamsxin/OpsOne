package handler

import (
	"fmt"
	"log"
	"os"
	"time"

	"github.com/gin-gonic/gin"

	"ops-platform/server/internal/middleware"
	"ops-platform/server/internal/model"
	"ops-platform/server/internal/response"
)

// 留存配置键。0 表示永久保留。
const (
	CfgRetentionExecJob      = "retention.exec_job_days"
	CfgRetentionProbeRecord  = "retention.probe_record_days"
	CfgRetentionExposure     = "retention.exposure_scan_days"
	CfgRetentionHostMetric   = "retention.host_metric_days"
	CfgRetentionNotifyRecord = "retention.notify_record_days"
	CfgRetentionAuditLog     = "retention.audit_log_days"
	CfgRetentionKubeChange   = "retention.kube_change_days"
	CfgRetentionSession      = "retention.session_days"
	CfgRetentionAlert        = "retention.alert_days"
)

// retentionTarget 一类可清理的数据
type retentionTarget struct {
	Key       string `json:"key"`   // 配置键
	Label     string `json:"label"` // 中文名
	Days      int    `json:"days"`  // 保留天数，0 表示永久
	Total     int64  `json:"total"` // 当前总行数
	Expired   int64  `json:"expired"`
	Cascade   string `json:"cascade"` // 连带删除的内容，空表示无
	Note      string `json:"note"`
	Irreverse bool   `json:"irreversible"` // 是否属于「删了就没法追溯」的数据
}

// retentionSpec 每类数据怎么数、怎么删。
//
// 统一在这里定义，避免清理逻辑散落在各模块里各写一遍（拨测记录原先就是硬编码 7 天）。
type retentionSpec struct {
	key       string
	label     string
	cascade   string
	note      string
	irreverse bool
	// count 返回总行数与超期行数
	count func(h *Handler, deadline time.Time) (int64, int64)
	// purge 删除超期数据，返回删除的主记录数
	purge func(h *Handler, deadline time.Time) int64
}

var retentionSpecs = []retentionSpec{
	{
		key: CfgRetentionExecJob, label: "执行记录",
		cascade: "逐台执行结果", note: "批量执行、脚本下发、定时任务的作业记录",
		count: func(h *Handler, deadline time.Time) (int64, int64) {
			var total, expired int64
			h.DB.Model(&model.ExecJob{}).Count(&total)
			h.DB.Model(&model.ExecJob{}).Where("started_at < ?", deadline).Count(&expired)
			return total, expired
		},
		purge: func(h *Handler, deadline time.Time) int64 {
			var ids []uint
			h.DB.Model(&model.ExecJob{}).Where("started_at < ?", deadline).Pluck("id", &ids)
			if len(ids) == 0 {
				return 0
			}
			h.DB.Where("job_id IN ?", ids).Delete(&model.ExecResult{})
			return h.DB.Where("id IN ?", ids).Delete(&model.ExecJob{}).RowsAffected
		},
	},
	{
		key: CfgRetentionProbeRecord, label: "拨测记录",
		note: "每个拨测每次执行一条，增长最快",
		count: func(h *Handler, deadline time.Time) (int64, int64) {
			var total, expired int64
			h.DB.Model(&model.ProbeRecord{}).Count(&total)
			h.DB.Model(&model.ProbeRecord{}).Where("created_at < ?", deadline).Count(&expired)
			return total, expired
		},
		purge: func(h *Handler, deadline time.Time) int64 {
			return h.DB.Where("created_at < ?", deadline).Delete(&model.ProbeRecord{}).RowsAffected
		},
	},
	{
		key: CfgRetentionExposure, label: "暴露面扫描记录",
		note: "每个目标每次扫描一条，留着才能回答「这个端口什么时候开的」",
		count: func(h *Handler, deadline time.Time) (int64, int64) {
			var total, expired int64
			h.DB.Model(&model.ExposureScan{}).Count(&total)
			h.DB.Model(&model.ExposureScan{}).Where("created_at < ?", deadline).Count(&expired)
			return total, expired
		},
		purge: func(h *Handler, deadline time.Time) int64 {
			return h.DB.Where("created_at < ?", deadline).Delete(&model.ExposureScan{}).RowsAffected
		},
	},
	{
		key: CfgRetentionHostMetric, label: "主机指标",
		note: "每台主机每次采集一条，台数多时增长最快",
		count: func(h *Handler, deadline time.Time) (int64, int64) {
			var total, expired int64
			h.DB.Model(&model.HostMetric{}).Count(&total)
			h.DB.Model(&model.HostMetric{}).Where("created_at < ?", deadline).Count(&expired)
			return total, expired
		},
		purge: func(h *Handler, deadline time.Time) int64 {
			return h.DB.Where("created_at < ?", deadline).Delete(&model.HostMetric{}).RowsAffected
		},
	},
	{
		key: CfgRetentionNotifyRecord, label: "通知投递记录",
		note: "告警投递到各渠道的流水，平台健康的失败统计依赖最近 24 小时",
		count: func(h *Handler, deadline time.Time) (int64, int64) {
			var total, expired int64
			h.DB.Model(&model.NotifyRecord{}).Count(&total)
			h.DB.Model(&model.NotifyRecord{}).Where("created_at < ?", deadline).Count(&expired)
			return total, expired
		},
		purge: func(h *Handler, deadline time.Time) int64 {
			return h.DB.Where("created_at < ?", deadline).Delete(&model.NotifyRecord{}).RowsAffected
		},
	},
	{
		key: CfgRetentionAuditLog, label: "操作审计",
		note: "所有写操作的留痕", irreverse: true,
		count: func(h *Handler, deadline time.Time) (int64, int64) {
			var total, expired int64
			h.DB.Model(&model.AuditLog{}).Count(&total)
			h.DB.Model(&model.AuditLog{}).Where("created_at < ?", deadline).Count(&expired)
			return total, expired
		},
		purge: func(h *Handler, deadline time.Time) int64 {
			return h.DB.Where("created_at < ?", deadline).Delete(&model.AuditLog{}).RowsAffected
		},
	},
	{
		key: CfgRetentionKubeChange, label: "集群改动留痕",
		note: "对集群 apply / 改副本数的记录，含提交的 YAML 原文", irreverse: true,
		count: func(h *Handler, deadline time.Time) (int64, int64) {
			var total, expired int64
			h.DB.Model(&model.KubeChangeLog{}).Count(&total)
			h.DB.Model(&model.KubeChangeLog{}).Where("created_at < ?", deadline).Count(&expired)
			return total, expired
		},
		purge: func(h *Handler, deadline time.Time) int64 {
			return h.DB.Where("created_at < ?", deadline).Delete(&model.KubeChangeLog{}).RowsAffected
		},
	},
	{
		key: CfgRetentionSession, label: "会话记录",
		cascade: "命令明细与录像文件", note: "Web 终端会话流水", irreverse: true,
		count: func(h *Handler, deadline time.Time) (int64, int64) {
			var total, expired int64
			h.DB.Model(&model.Session{}).Count(&total)
			h.DB.Model(&model.Session{}).Where("started_at < ?", deadline).Count(&expired)
			return total, expired
		},
		purge: func(h *Handler, deadline time.Time) int64 {
			var sessions []model.Session
			h.DB.Where("started_at < ?", deadline).Find(&sessions)
			if len(sessions) == 0 {
				return 0
			}
			ids := make([]uint, 0, len(sessions))
			for _, session := range sessions {
				ids = append(ids, session.ID)
				// 录像文件跟着会话走，删库不删文件会留一堆孤儿文件
				if session.RecordPath != "" {
					if err := os.Remove(session.RecordPath); err != nil && !os.IsNotExist(err) {
						log.Printf("[retention] 删除录像失败 %s: %v", session.RecordPath, err)
					}
				}
			}
			h.DB.Where("session_id IN ?", ids).Delete(&model.SessionCommand{})
			return h.DB.Where("id IN ?", ids).Delete(&model.Session{}).RowsAffected
		},
	},
	{
		key: CfgRetentionAlert, label: "已恢复告警",
		cascade: "值班升级记录",
		note:    "只清理 status=resolved 的告警，未恢复的一律不动",
		count: func(h *Handler, deadline time.Time) (int64, int64) {
			var total, expired int64
			h.DB.Model(&model.Alert{}).Where("status = ?", "resolved").Count(&total)
			h.DB.Model(&model.Alert{}).
				Where("status = ? AND last_seen_at < ?", "resolved", deadline).Count(&expired)
			return total, expired
		},
		purge: func(h *Handler, deadline time.Time) int64 {
			// 先把要删的告警 ID 取出来，连带清掉它们的升级记录，避免留下指不到告警的孤儿
			var ids []uint
			h.DB.Model(&model.Alert{}).
				Where("status = ? AND last_seen_at < ?", "resolved", deadline).Pluck("id", &ids)
			if len(ids) == 0 {
				return 0
			}
			h.DB.Where("alert_id IN ?", ids).Delete(&model.AlertEscalation{})
			return h.DB.Where("id IN ?", ids).Delete(&model.Alert{}).RowsAffected
		},
	},
}

// retentionOverview 汇总每类数据的当前状态
func (h *Handler) retentionOverview() []retentionTarget {
	now := time.Now()
	targets := make([]retentionTarget, 0, len(retentionSpecs))
	for _, spec := range retentionSpecs {
		days := h.configInt(spec.key, 0)
		target := retentionTarget{
			Key: spec.key, Label: spec.label, Days: days,
			Cascade: spec.cascade, Note: spec.note, Irreverse: spec.irreverse,
		}
		if days > 0 {
			deadline := now.AddDate(0, 0, -days)
			target.Total, target.Expired = spec.count(h, deadline)
		} else {
			// 永久保留时只报总量，超期数没有意义
			target.Total, _ = spec.count(h, now)
		}
		targets = append(targets, target)
	}
	return targets
}

// RetentionStatus 留存策略与当前数据量
func (h *Handler) RetentionStatus(c *gin.Context) {
	targets := h.retentionOverview()

	var expiredTotal int64
	disabled := 0
	for _, target := range targets {
		expiredTotal += target.Expired
		if target.Days <= 0 {
			disabled++
		}
	}

	at, info := h.lastFixedRun("retention")
	lastRun := gin.H{"at": nil, "info": "本次启动后还没执行过（记录只存内存）"}
	if !at.IsZero() {
		lastRun = gin.H{"at": at, "info": info}
	}

	response.OK(c, gin.H{
		"targets": targets, "expiredTotal": expiredTotal,
		"permanentCount": disabled, "spec": h.Cfg.RetentionSpec,
		"lastRun": lastRun,
		"detail": fmt.Sprintf("当前可清理 %d 行；%d 类设置为永久保留。保留天数在「系统配置」的 retention 分组里改。",
			expiredTotal, disabled),
	})
}

// runRetention 执行（或试算）清理，返回每类的删除行数
func (h *Handler) runRetention(dryRun bool, operator string) gin.H {
	now := time.Now()
	items := make([]gin.H, 0, len(retentionSpecs))
	var affected int64

	for _, spec := range retentionSpecs {
		days := h.configInt(spec.key, 0)
		if days <= 0 {
			items = append(items, gin.H{
				"key": spec.key, "label": spec.label, "days": 0,
				"deleted": 0, "skipped": true, "reason": "设置为永久保留",
			})
			continue
		}

		deadline := now.AddDate(0, 0, -days)
		_, expired := spec.count(h, deadline)
		deleted := expired
		if !dryRun && expired > 0 {
			deleted = spec.purge(h, deadline)
		}
		affected += deleted
		items = append(items, gin.H{
			"key": spec.key, "label": spec.label, "days": days,
			"deleted": deleted, "skipped": false,
			"deadline": deadline.Format("2006-01-02 15:04:05"),
		})
	}

	action := "清理"
	if dryRun {
		action = "试算"
	}
	detail := fmt.Sprintf("%s完成，共 %d 行", action, affected)
	log.Printf("[retention] %s（操作人 %s）：%s", action, operator, detail)
	if !dryRun {
		h.markFixedRun("retention", detail)
	}
	return gin.H{"dryRun": dryRun, "affected": affected, "items": items, "detail": detail}
}

// RunRetentionCleanup 手动执行清理，dryRun=true 时只试算不删除
func (h *Handler) RunRetentionCleanup(c *gin.Context) {
	dryRun := c.Query("dryRun") == "true"
	response.OK(c, h.runRetention(dryRun, middleware.CurrentUser(c).Username))
}

// RunRetentionForSchedule 定时清理入口
func (h *Handler) RunRetentionForSchedule() {
	h.runRetention(false, "scheduler")
}
