package handler

import (
	"time"

	"github.com/gin-gonic/gin"

	"ops-platform/server/internal/model"
	"ops-platform/server/internal/response"
)

// 值班大屏：一屏投出去给值班看的东西。
//
// 与「告警态势」的分工：那一页是**运营统计**（按天、按源、按级别的分布，用来复盘
// 「这周哪里在响」）；这一页是**当下**（现在有什么没人管、哪条快超时、哪些关键面出问题）。
// 两者数据源相同但问题不同，所以刻意不互相抄。
//
// 三条设计原则：
//   - 只聚合，不产生任何新数据。每一块都指向一个已经在跑的模块。
//   - 没有采集的东西不给数字（与安全概览第 36/39 节同一口径）。
//   - **一屏能看完**。大屏上塞二十个数字等于没有大屏，所以只留「现在要不要动手」
//     这一类，历史趋势只留一条 24 小时的线。

// WallboardData 大屏数据
func (h *Handler) Wallboard(c *gin.Context) {
	now := time.Now()
	last24h := now.Add(-24 * time.Hour)

	countOf := func(dest any, where string, args ...any) int64 {
		var n int64
		q := h.DB.Model(dest)
		if where != "" {
			q = q.Where(where, args...)
		}
		q.Count(&n)
		return n
	}

	// ---- 告警：正在响的 ----
	firing := gin.H{
		"total":    countOf(&model.Alert{}, "status = ?", "firing"),
		"critical": countOf(&model.Alert{}, "status = ? AND severity = ?", "firing", "critical"),
		"warning":  countOf(&model.Alert{}, "status = ? AND severity = ?", "firing", "warning"),
		// 被静默或聚合抑制的：仍然在响，只是没外发。大屏上必须看得到，
		// 否则「告警数为 0」会被读成「一切正常」
		"suppressed": countOf(&model.Alert{}, "status = ? AND suppressed_by <> ''", "firing"),
		"new24h":     countOf(&model.Alert{}, "created_at >= ?", last24h),
	}

	// ---- 事件工单：SLA 是这一屏最该看的 ----
	targets := h.slaTargets()
	var openEvents []model.Event
	h.DB.Where("status <> ?", "resolved").Order("id desc").Limit(200).Find(&openEvents)

	events := gin.H{"open": len(openEvents)}
	var breached, atRisk, unassigned int
	type urgentItem struct {
		ID       uint   `json:"id"`
		Title    string `json:"title"`
		Severity string `json:"severity"`
		Status   string `json:"status"`
		Assignee string `json:"assignee"`
		// SLAState na | pending | risk | met | breached
		SLAState string `json:"slaState"`
		// RemainSeconds 距 SLA 还有多少秒，负数表示已超
		RemainSeconds int64 `json:"remainSeconds"`
		// Reason SLA 那边算出来的人话，直接用 —— 大屏上不重新组织一遍说法
		Reason string `json:"reason"`
	}
	urgent := make([]urgentItem, 0, 8)
	for _, event := range openEvents {
		respond, _ := eventSLA(event, targets, now)
		if event.Assignee == "" {
			unassigned++
		}
		switch respond.State {
		case "breached":
			breached++
		case "risk":
			atRisk++
		}
		if respond.State == "breached" || respond.State == "risk" {
			urgent = append(urgent, urgentItem{
				ID: event.ID, Title: event.Title, Severity: event.Severity,
				Status: event.Status, Assignee: event.Assignee,
				SLAState: respond.State, RemainSeconds: respond.RemainSeconds,
				Reason: respond.Reason,
			})
		}
	}
	// 超时最久的排最前；只留 8 条 —— 大屏上列二十条没人看得完
	for i := 0; i < len(urgent); i++ {
		for j := i + 1; j < len(urgent); j++ {
			if urgent[j].RemainSeconds < urgent[i].RemainSeconds {
				urgent[i], urgent[j] = urgent[j], urgent[i]
			}
		}
	}
	if len(urgent) > 8 {
		urgent = urgent[:8]
	}
	events["breached"] = breached
	events["atRisk"] = atRisk
	events["unassigned"] = unassigned
	events["urgent"] = urgent

	// ---- 关键资源面 ----
	hosts := gin.H{
		"total":   countOf(&model.Host{}, ""),
		"offline": countOf(&model.Host{}, "status = ?", "offline"),
		"unknown": countOf(&model.Host{}, "status = ? OR status = ''", "unknown"),
	}
	probes := gin.H{
		"total": countOf(&model.Probe{}, "enabled = ?", true),
		"down":  countOf(&model.Probe{}, "enabled = ? AND last_status = ?", true, "down"),
	}
	certs := gin.H{
		"expiring": countOf(&model.Certificate{}, "enabled = ? AND status = ?", true, "expiring"),
		"expired":  countOf(&model.Certificate{}, "enabled = ? AND status = ?", true, "expired"),
	}
	domains := gin.H{
		"expiring": countOf(&model.Domain{}, "enabled = ? AND expire_status = ?", true, "expiring"),
		"expired":  countOf(&model.Domain{}, "enabled = ? AND expire_status = ?", true, "expired"),
		"dnsDrift": countOf(&model.Domain{}, "enabled = ? AND dns_status = ?", true, "drift"),
	}
	logs := gin.H{
		"hit":        countOf(&model.HostLogTarget{}, "enabled = ? AND last_status = ?", true, "hit"),
		"unreadable": countOf(&model.HostLogTarget{}, "enabled = ? AND last_status IN ?", true, []string{"missing", "denied", "failed"}),
	}
	security := gin.H{
		"open":     countOf(&model.SecurityEvent{}, "status IN ?", []string{secStatusNew, secStatusInvestigating, secStatusConfirmed}),
		"critical": countOf(&model.SecurityEvent{}, "severity = ? AND status IN ?", "critical", []string{secStatusNew, secStatusInvestigating, secStatusConfirmed}),
	}

	// ---- 通知投递：告警发不出去是最隐蔽的一种故障 ----
	notify := gin.H{
		"failed24h":  countOf(&model.NotifyRecord{}, "status = ? AND created_at >= ?", "failed", last24h),
		"success24h": countOf(&model.NotifyRecord{}, "status = ? AND created_at >= ?", "success", last24h),
	}

	// ---- 当前值班人 ----
	response.OK(c, gin.H{
		"at":         now.Format(time.RFC3339),
		"firing":     firing,
		"events":     events,
		"hosts":      hosts,
		"probes":     probes,
		"certs":      certs,
		"domains":    domains,
		"hostLogs":   logs,
		"security":   security,
		"notify":     notify,
		"trend":      h.alertTrend24h(last24h),
		"onCall":     h.wallboardOnCall(now),
		"slaTargets": targets,
		"notes": []string{
			"这一屏回答「现在要不要动手」。按天按源的分布在「告警态势」页 —— 那是复盘用的，不是值班用的",
			"「被抑制」的告警仍然在响，只是没外发（静默或聚合抑制）。它必须出现在这里 —— " +
				"否则告警数为 0 会被读成「一切正常」",
			"SLA 的「响应」口径：指派不算响应，要状态离开待处理或写下第一条处置记录才算",
			"通知投递失败数放在这里是刻意的：告警发不出去是最隐蔽的一种故障 —— " +
				"平台看着在告警，实际没人收到",
			"只聚合、不产生新数据；平台没采集的东西（流量、阻断包数）这里一个数字都没有",
		},
	})
}

// alertTrend24h 最近 24 小时每小时新增多少条告警。
// 用 substr 取到小时，与安全概览的日趋势同一套朴素写法（跨库可用）。
func (h *Handler) alertTrend24h(since time.Time) []gin.H {
	type row struct {
		Hour  string
		Total int64
	}
	var rows []row
	h.DB.Model(&model.Alert{}).
		Select("substr(created_at, 1, 13) as hour, count(*) as total").
		Where("created_at >= ?", since).
		Group("hour").Order("hour asc").Find(&rows)

	out := make([]gin.H, 0, len(rows))
	for _, r := range rows {
		out = append(out, gin.H{"hour": r.Hour, "total": r.Total})
	}
	return out
}

// wallboardOnCall 当前生效的值班表与它们的当班人。
//
// 只读已有的值班表配置，不重新实现排班算法 —— 那是值班模块的事。
// 值班表停用或没有成员时如实说明，不显示成「无人值班」的空白。
func (h *Handler) wallboardOnCall(now time.Time) []gin.H {
	var schedules []model.OnCallSchedule
	h.DB.Where("enabled = ?", true).Order("id asc").Limit(10).Find(&schedules)

	out := make([]gin.H, 0, len(schedules))
	for _, schedule := range schedules {
		item := gin.H{"id": schedule.ID, "name": schedule.Name}
		// 逐级问「这一级当班的是谁」，复用值班模块的排班算法，不在这里重算。
		// 只看前三级：大屏上列更多层级没有意义
		names := make([]string, 0, 3)
		for level := 0; level < 3; level++ {
			person := h.resolveOnCall(schedule, now, level)
			if person == nil {
				break
			}
			names = append(names, person.UserName)
		}
		if len(names) == 0 {
			item["note"] = "这张值班表没有排出当班人（成员为空或轮换配置有问题）"
			out = append(out, item)
			continue
		}
		item["levels"] = names
		item["current"] = names[0]
		out = append(out, item)
	}
	return out
}
