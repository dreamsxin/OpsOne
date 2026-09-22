package handler

import (
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"ops-platform/server/internal/middleware"
	"ops-platform/server/internal/model"
	"ops-platform/server/internal/response"
)

// 事件复盘。
//
// 设计前提：复盘最费时间的不是写字，而是「翻当时发生了什么」。所以这里把两件事做实：
//  1. 里程碑时间给建议值（从告警与处置时间线算），人只需要确认或改，MTTA/MTTR 自动出；
//  2. 证据自动汇聚（通知投递、叫人记录、静默命中、下发拦截、主机指标、拨测记录），
//     复盘时不用再去七个页面之间来回翻。
//
// 归档有门槛：根因、影响面、恢复时间必须填，且至少有一条改进项 —— 否则复盘只是个走过场的
// 文本框。归档后字段锁定，要改必须显式「重新打开」，重新打开会记进事件时间线。

var reviewStatusLabels = map[string]string{
	"draft": "草稿", "reviewing": "评审中", "archived": "已归档",
}

var actionItemStatusLabels = map[string]string{
	"open": "待开始", "doing": "进行中", "done": "已完成", "dropped": "不做了",
}

var actionItemKindLabels = map[string]string{
	"prevent": "防复发", "detect": "提升发现能力", "mitigate": "加快止血", "process": "流程改进",
}

// ---------- 里程碑建议值 ----------

// milestoneHint 一个建议时间 + 它是从哪条记录算出来的。
// 说清来源，人才敢用；算不出来就如实说算不出来。
type milestoneHint struct {
	At     *time.Time `json:"at"`
	Source string     `json:"source"`
}

// suggestMilestones 从关联告警与事件本身推导里程碑建议值。
// 不写库，只作为前端的「建议」展示；用户保存的才是准数。
func (h *Handler) suggestMilestones(event model.Event, alerts []model.Alert) map[string]milestoneHint {
	out := map[string]milestoneHint{}

	var happened, detected, responded *time.Time
	happenedFrom, detectedFrom, respondedFrom := "", "", ""
	for _, alert := range alerts {
		first := alert.FirstSeenAt
		if detected == nil || first.Before(*detected) {
			at := first
			detected = &at
			detectedFrom = fmt.Sprintf("告警 #%d 首次出现", alert.ID)
			// 平台只知道告警什么时候响的，不知道故障什么时候真正开始，
			// 所以故障开始默认按「被发现」同值给，等人往前修正
			happened = &at
			happenedFrom = fmt.Sprintf("按告警 #%d 首次出现填（平台推不出故障真正开始的时刻，请按实际往前改）", alert.ID)
		}
		if alert.AckAt != nil && (responded == nil || alert.AckAt.Before(*responded)) {
			at := *alert.AckAt
			responded = &at
			respondedFrom = fmt.Sprintf("告警 #%d 被 %s 确认", alert.ID, alert.AckBy)
		}
	}

	// 没有告警确认记录时，退一步用事件自己的响应时间（状态离开待处理 / 第一条处置记录），
	// 再退一步用建单时间。
	//
	// 这里**不能**退到「事件指派时间」：SLA 的口径是指派不算响应（把单子转给别人不等于
	// 开始处理），复盘里再把指派当响应，同一个词就有了两套算法，MTTA 会比 SLA 宽。
	if responded == nil && event.RespondedAt != nil {
		at := *event.RespondedAt
		responded = &at
		respondedFrom = "事件开始被处理（状态离开待处理或写下第一条处置记录）"
	}
	if responded == nil {
		at := event.CreatedAt
		responded = &at
		respondedFrom = "事件建单（既没有告警确认记录，也没人在这张单上动过手，请按实际改）"
	}
	if happened == nil {
		at := event.CreatedAt
		happened = &at
		happenedFrom = "事件建单（没有关联告警）"
	}
	if detected == nil {
		at := event.CreatedAt
		detected = &at
		detectedFrom = "事件建单（没有关联告警）"
	}

	out["happenedAt"] = milestoneHint{At: happened, Source: happenedFrom}
	out["detectedAt"] = milestoneHint{At: detected, Source: detectedFrom}
	out["respondedAt"] = milestoneHint{At: responded, Source: respondedFrom}
	if event.ResolvedAt != nil {
		out["recoveredAt"] = milestoneHint{At: event.ResolvedAt, Source: "事件被 " + event.ResolvedBy + " 标记已解决"}
	} else {
		out["recoveredAt"] = milestoneHint{Source: "事件还没标记已解决，算不出恢复时间"}
	}
	out["mitigatedAt"] = milestoneHint{Source: "止血时间平台推不出来，只能人填"}
	return out
}

// reviewDurations 按里程碑算出耗时指标。缺哪个时间就把对应指标留空，不用 0 冒充。
func reviewDurations(review model.EventReview) gin.H {
	minutes := func(from, to *time.Time) any {
		if from == nil || to == nil || to.Before(*from) {
			return nil
		}
		return int64(to.Sub(*from).Minutes())
	}
	return gin.H{
		// 发现耗时：故障开始到被发现
		"detectMinutes": minutes(review.HappenedAt, review.DetectedAt),
		// MTTA：被发现到有人响应
		"ackMinutes": minutes(review.DetectedAt, review.RespondedAt),
		// 止血耗时：响应到止血完成
		"mitigateMinutes": minutes(review.RespondedAt, review.MitigatedAt),
		// MTTR：故障开始到完全恢复
		"recoverMinutes": minutes(review.HappenedAt, review.RecoveredAt),
	}
}

func reviewView(review model.EventReview) gin.H {
	return gin.H{
		"id": review.ID, "eventId": review.EventID,
		"status": review.Status, "statusLabel": reviewStatusLabels[review.Status],
		"owner":       review.Owner,
		"happenedAt":  review.HappenedAt,
		"detectedAt":  review.DetectedAt,
		"respondedAt": review.RespondedAt,
		"mitigatedAt": review.MitigatedAt,
		"recoveredAt": review.RecoveredAt,
		"impact":      review.Impact, "rootCause": review.RootCause,
		"trigger": review.Trigger, "detectGap": review.DetectGap,
		"mitigation": review.Mitigation, "lesson": review.Lesson,
		"archivedAt": review.ArchivedAt, "archivedBy": review.ArchivedBy,
		"createdAt": review.CreatedAt, "updatedAt": review.UpdatedAt,
		"durations": reviewDurations(review),
	}
}

func actionItemView(item model.EventActionItem) gin.H {
	overdue := false
	if item.DueDate != nil && item.Status != "done" && item.Status != "dropped" {
		overdue = item.DueDate.Before(time.Now())
	}
	return gin.H{
		"id": item.ID, "reviewId": item.ReviewID, "eventId": item.EventID,
		"title": item.Title, "detail": item.Detail,
		"kind": item.Kind, "kindLabel": actionItemKindLabels[item.Kind],
		"owner": item.Owner, "dueDate": item.DueDate,
		"status": item.Status, "statusLabel": actionItemStatusLabels[item.Status],
		"doneAt": item.DoneAt, "doneNote": item.DoneNote, "overdue": overdue,
		"lastRemindAt":  item.LastRemindAt,
		"createdByName": item.CreatedByName, "createdAt": item.CreatedAt,
	}
}

// ---------- 自动建草稿 ----------

// ensureReviewDraft 事件被标记已解决时自动建一份复盘草稿并预填里程碑建议值。
// 已经有复盘的不动。返回是否新建。
func (h *Handler) ensureReviewDraft(event model.Event, operator string) bool {
	var exist model.EventReview
	if err := h.DB.Where("event_id = ?", event.ID).First(&exist).Error; err == nil {
		return false
	}

	var alerts []model.Alert
	if ids := eventAlertIDs(event); len(ids) > 0 {
		h.DB.Where("id IN ?", ids).Find(&alerts)
	}
	hints := h.suggestMilestones(event, alerts)

	owner := event.Assignee
	if owner == "" {
		owner = operator
	}
	review := model.EventReview{
		EventID: event.ID, Status: "draft", Owner: owner,
		HappenedAt:  hints["happenedAt"].At,
		DetectedAt:  hints["detectedAt"].At,
		RespondedAt: hints["respondedAt"].At,
		RecoveredAt: hints["recoveredAt"].At,
	}
	if err := h.DB.Create(&review).Error; err != nil {
		log.Printf("[review] 事件 #%d 自动建复盘草稿失败: %v", event.ID, err)
		return false
	}
	h.appendEventLog(event.ID, "review", "已解决，自动创建复盘草稿，待复盘人 "+owner, operator)
	h.notifyReviewOwner(review, event, operator)
	return true
}

func (h *Handler) notifyReviewOwner(review model.EventReview, event model.Event, operator string) {
	if review.Owner == "" {
		return
	}
	var user model.User
	if err := h.DB.Where("username = ?", review.Owner).First(&user).Error; err != nil {
		return
	}
	h.DB.Create(&model.Message{
		UserID: user.ID, Type: "review", Level: event.Severity, RefID: event.ID,
		Title:   "待复盘：" + truncate(event.Title, 200),
		Content: fmt.Sprintf("事件 #%d 已由 %s 标记解决，请到「监控告警 → 事件复盘」补齐根因、影响面与改进项", event.ID, operator),
	})
}

// ---------- 复盘读写 ----------

// GetEventReview 事件的复盘详情。没有复盘时返回 review=null 并给出里程碑建议值，
// 让前端可以直接展示「一键用建议值建复盘」。
func (h *Handler) GetEventReview(c *gin.Context) {
	var event model.Event
	if err := h.DB.First(&event, idParam(c)).Error; err != nil {
		response.NotFound(c, "事件不存在")
		return
	}

	var alerts []model.Alert
	if ids := eventAlertIDs(event); len(ids) > 0 {
		h.DB.Where("id IN ?", ids).Find(&alerts)
	}

	var review model.EventReview
	has := h.DB.Where("event_id = ?", event.ID).First(&review).Error == nil

	items := make([]gin.H, 0)
	if has {
		var rows []model.EventActionItem
		h.DB.Where("review_id = ?", review.ID).Order("id asc").Find(&rows)
		for _, row := range rows {
			items = append(items, actionItemView(row))
		}
	}

	body := gin.H{
		"event":       eventView(event),
		"suggestions": h.suggestMilestones(event, alerts),
		"actionItems": items,
	}
	if has {
		body["review"] = reviewView(review)
	} else {
		body["review"] = nil
	}
	response.OK(c, body)
}

type reviewSaveReq struct {
	Owner       string     `json:"owner"`
	HappenedAt  *time.Time `json:"happenedAt"`
	DetectedAt  *time.Time `json:"detectedAt"`
	RespondedAt *time.Time `json:"respondedAt"`
	MitigatedAt *time.Time `json:"mitigatedAt"`
	RecoveredAt *time.Time `json:"recoveredAt"`
	Impact      string     `json:"impact"`
	RootCause   string     `json:"rootCause"`
	Trigger     string     `json:"trigger"`
	DetectGap   string     `json:"detectGap"`
	Mitigation  string     `json:"mitigation"`
	Lesson      string     `json:"lesson"`
	// Status 只允许在 draft / reviewing 之间切换，归档走单独接口
	Status string `json:"status"`
}

// SaveEventReview 新建或更新复盘（upsert）。已归档的必须先重新打开。
func (h *Handler) SaveEventReview(c *gin.Context) {
	var event model.Event
	if err := h.DB.First(&event, idParam(c)).Error; err != nil {
		response.NotFound(c, "事件不存在")
		return
	}

	var req reviewSaveReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "参数格式有误")
		return
	}
	status := strings.TrimSpace(req.Status)
	if status == "" {
		status = "draft"
	}
	if status != "draft" && status != "reviewing" {
		response.BadRequest(c, "保存时状态只能是 draft 或 reviewing，归档请用归档接口")
		return
	}

	operator := middleware.CurrentUser(c)
	if req.Owner != "" {
		var target model.User
		if err := h.DB.Where("username = ?", req.Owner).First(&target).Error; err != nil {
			response.BadRequest(c, "复盘负责人不存在: "+req.Owner)
			return
		}
	}

	var review model.EventReview
	exist := h.DB.Where("event_id = ?", event.ID).First(&review).Error == nil
	if exist && review.Status == "archived" {
		response.BadRequest(c, "复盘已归档，要修改请先「重新打开」")
		return
	}

	review.EventID = event.ID
	review.Status = status
	review.Owner = req.Owner
	review.HappenedAt, review.DetectedAt = req.HappenedAt, req.DetectedAt
	review.RespondedAt, review.MitigatedAt = req.RespondedAt, req.MitigatedAt
	review.RecoveredAt = req.RecoveredAt
	review.Impact, review.RootCause = req.Impact, req.RootCause
	review.Trigger, review.DetectGap = req.Trigger, req.DetectGap
	review.Mitigation, review.Lesson = req.Mitigation, req.Lesson

	if exist {
		if err := h.DB.Save(&review).Error; err != nil {
			response.Error(c, "保存复盘失败")
			return
		}
		h.appendEventLog(event.ID, "review", "更新复盘（"+reviewStatusLabels[status]+"）", operator.Username)
	} else {
		review.CreatedBy = operator.ID
		if err := h.DB.Create(&review).Error; err != nil {
			response.Error(c, "创建复盘失败")
			return
		}
		h.appendEventLog(event.ID, "review", "创建复盘（"+reviewStatusLabels[status]+"）", operator.Username)
	}
	response.OK(c, reviewView(review))
}

// ArchiveEventReview 归档。有门槛：根因、影响面、恢复时间必填，且至少一条改进项，
// 否则复盘就只是个走过场的文本框。
func (h *Handler) ArchiveEventReview(c *gin.Context) {
	var event model.Event
	if err := h.DB.First(&event, idParam(c)).Error; err != nil {
		response.NotFound(c, "事件不存在")
		return
	}
	var review model.EventReview
	if err := h.DB.Where("event_id = ?", event.ID).First(&review).Error; err != nil {
		response.NotFound(c, "该事件还没有复盘")
		return
	}
	if review.Status == "archived" {
		response.BadRequest(c, "复盘已经归档了")
		return
	}

	missing := make([]string, 0, 4)
	if strings.TrimSpace(review.RootCause) == "" {
		missing = append(missing, "根因")
	}
	if strings.TrimSpace(review.Impact) == "" {
		missing = append(missing, "影响面")
	}
	if review.RecoveredAt == nil {
		missing = append(missing, "恢复时间")
	}
	var itemCount int64
	h.DB.Model(&model.EventActionItem{}).Where("review_id = ?", review.ID).Count(&itemCount)
	if itemCount == 0 {
		missing = append(missing, "至少一条改进项")
	}
	if len(missing) > 0 {
		response.BadRequest(c, "归档前请补齐："+strings.Join(missing, "、"))
		return
	}

	operator := middleware.CurrentUser(c).Username
	now := time.Now()
	if err := h.DB.Model(&model.EventReview{}).Where("id = ?", review.ID).Updates(map[string]any{
		"status": "archived", "archived_at": &now, "archived_by": operator,
	}).Error; err != nil {
		response.Error(c, "归档失败")
		return
	}
	h.appendEventLog(event.ID, "review",
		fmt.Sprintf("复盘归档，%d 条改进项进入跟踪", itemCount), operator)
	response.OK(c, gin.H{"status": "archived", "actionItems": itemCount})
}

type reviewReopenReq struct {
	Reason string `json:"reason" binding:"required"`
}

// ReopenEventReview 重新打开已归档的复盘。必须给理由，理由记进事件时间线。
func (h *Handler) ReopenEventReview(c *gin.Context) {
	var event model.Event
	if err := h.DB.First(&event, idParam(c)).Error; err != nil {
		response.NotFound(c, "事件不存在")
		return
	}
	var review model.EventReview
	if err := h.DB.Where("event_id = ?", event.ID).First(&review).Error; err != nil {
		response.NotFound(c, "该事件还没有复盘")
		return
	}
	if review.Status != "archived" {
		response.BadRequest(c, "只有已归档的复盘才需要重新打开")
		return
	}
	var req reviewReopenReq
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.Reason) == "" {
		response.BadRequest(c, "请说明重新打开的原因")
		return
	}

	operator := middleware.CurrentUser(c).Username
	if err := h.DB.Model(&model.EventReview{}).Where("id = ?", review.ID).Updates(map[string]any{
		"status": "reviewing", "archived_at": nil, "archived_by": "",
	}).Error; err != nil {
		response.Error(c, "重新打开失败")
		return
	}
	h.appendEventLog(event.ID, "review", "复盘重新打开："+req.Reason, operator)
	response.OK(c, gin.H{"status": "reviewing"})
}

// ---------- 证据汇聚 ----------

// GetEventEvidence 把复盘要翻的记录一次性汇总出来。
//
// 时间窗 = 最早告警首次出现（或事件建单）到事件解决（或现在），前后各放宽 10 分钟。
// 每一类都如实说明「取自哪张表、用什么条件筛的」，取不到就返回空数组而不是编。
func (h *Handler) GetEventEvidence(c *gin.Context) {
	var event model.Event
	if err := h.DB.First(&event, idParam(c)).Error; err != nil {
		response.NotFound(c, "事件不存在")
		return
	}

	ids := eventAlertIDs(event)
	var alerts []model.Alert
	if len(ids) > 0 {
		h.DB.Where("id IN ?", ids).Find(&alerts)
	}

	from := event.CreatedAt
	for _, alert := range alerts {
		if alert.FirstSeenAt.Before(from) {
			from = alert.FirstSeenAt
		}
	}
	to := time.Now()
	if event.ResolvedAt != nil {
		to = *event.ResolvedAt
	}
	from = from.Add(-10 * time.Minute)
	to = to.Add(10 * time.Minute)

	// 通知投递：这次到底有没有发出去
	notifies := make([]model.NotifyRecord, 0)
	if len(ids) > 0 {
		h.DB.Where("alert_id IN ?", ids).Order("id desc").Limit(200).Find(&notifies)
	}

	// 叫人记录：有没有叫到人
	escalations := make([]model.AlertEscalation, 0)
	if len(ids) > 0 {
		h.DB.Where("alert_id IN ?", ids).Order("id asc").Find(&escalations)
	}

	// 静默与抑制：为什么当时没人收到通知
	silenceIDs := map[uint]bool{}
	suppressed := make([]gin.H, 0)
	for _, alert := range alerts {
		if alert.SilenceID > 0 {
			silenceIDs[alert.SilenceID] = true
		}
		if alert.SuppressedBy != "" {
			suppressed = append(suppressed, gin.H{
				"alertId": alert.ID, "title": alert.Title, "suppressedBy": alert.SuppressedBy,
			})
		}
	}
	silences := make([]model.AlertSilence, 0)
	if len(silenceIDs) > 0 {
		h.DB.Where("id IN ?", keysOf(silenceIDs)).Find(&silences)
	}

	// 时间窗内的下发拦截：有没有人在故障期间被规则挡住
	guards := make([]model.ExecGuardLog, 0)
	h.DB.Where("created_at BETWEEN ? AND ?", from, to).
		Order("id desc").Limit(100).Find(&guards)

	// 相关主机在时间窗内的指标：从告警标签反查主机
	context := h.eventContext(alerts)
	hostNames := make([]string, 0, 4)
	if hosts, ok := context["hosts"].([]gin.H); ok {
		for _, host := range hosts {
			if name, ok := host["name"].(string); ok && name != "" {
				hostNames = append(hostNames, name)
			}
		}
	}
	metrics := make([]model.HostMetric, 0)
	if len(hostNames) > 0 {
		var hostIDs []uint
		h.DB.Model(&model.Host{}).Where("name IN ? OR address IN ?", hostNames, hostNames).
			Pluck("id", &hostIDs)
		if len(hostIDs) > 0 {
			h.DB.Where("host_id IN ? AND created_at BETWEEN ? AND ?", hostIDs, from, to).
				Order("id desc").Limit(200).Find(&metrics)
		}
	}

	// 相关拨测在时间窗内的记录
	probeRecords := make([]model.ProbeRecord, 0)
	if probes, ok := context["probes"].([]model.Probe); ok && len(probes) > 0 {
		probeIDs := make([]uint, 0, len(probes))
		for _, probe := range probes {
			probeIDs = append(probeIDs, probe.ID)
		}
		h.DB.Where("probe_id IN ? AND created_at BETWEEN ? AND ?", probeIDs, from, to).
			Order("id desc").Limit(200).Find(&probeRecords)
	}

	response.OK(c, gin.H{
		"window": gin.H{
			"from": from, "to": to,
			"note": "时间窗 = 最早告警首次出现到事件解决，前后各放宽 10 分钟",
		},
		"notifyRecords": notifies,
		"escalations":   escalations,
		"silences":      silences,
		"suppressed":    suppressed,
		"execGuardLogs": guards,
		"hostMetrics":   metrics,
		"probeRecords":  probeRecords,
		"sources": []string{
			"通知投递取自 notify_records（按关联告警 ID）",
			"叫人记录取自 alert_escalations（按关联告警 ID）",
			"静默取自告警上的 silence_id 反查 alert_silences",
			"下发拦截取自 exec_guard_logs（按时间窗，不限于本事件）",
			"主机指标取自 host_metrics（告警标签能对上的主机 + 时间窗）",
			"拨测记录取自 probe_records（告警标签里的 probeId + 时间窗）",
		},
	})
}

// ---------- 改进项 ----------

type actionItemReq struct {
	Title   string     `json:"title" binding:"required"`
	Detail  string     `json:"detail"`
	Kind    string     `json:"kind"`
	Owner   string     `json:"owner" binding:"required"`
	DueDate *time.Time `json:"dueDate"`
	Status  string     `json:"status"`
}

func (r *actionItemReq) normalize(h *Handler) error {
	r.Title = strings.TrimSpace(r.Title)
	if r.Title == "" {
		return fmt.Errorf("改进项标题不能为空")
	}
	if r.Kind == "" {
		r.Kind = "prevent"
	}
	if _, ok := actionItemKindLabels[r.Kind]; !ok {
		return fmt.Errorf("改进项类型只能是 prevent / detect / mitigate / process")
	}
	if r.Status == "" {
		r.Status = "open"
	}
	if _, ok := actionItemStatusLabels[r.Status]; !ok {
		return fmt.Errorf("改进项状态只能是 open / doing / done / dropped")
	}
	var user model.User
	if err := h.DB.Where("username = ?", r.Owner).First(&user).Error; err != nil {
		return fmt.Errorf("改进项负责人不存在: %s", r.Owner)
	}
	if user.Status != 1 {
		return fmt.Errorf("账号 %s 已被禁用，不能作为改进项负责人", r.Owner)
	}
	return nil
}

// CreateActionItem 给某个事件的复盘加一条改进项。复盘不存在就先建草稿，
// 避免「想先记一条待办却卡在必须先建复盘」。
func (h *Handler) CreateActionItem(c *gin.Context) {
	var event model.Event
	if err := h.DB.First(&event, idParam(c)).Error; err != nil {
		response.NotFound(c, "事件不存在")
		return
	}
	var req actionItemReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "请填写改进项标题与负责人")
		return
	}
	if err := req.normalize(h); err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	operator := middleware.CurrentUser(c)
	var review model.EventReview
	if err := h.DB.Where("event_id = ?", event.ID).First(&review).Error; err != nil {
		review = model.EventReview{EventID: event.ID, Status: "draft", Owner: event.Assignee, CreatedBy: operator.ID}
		if err := h.DB.Create(&review).Error; err != nil {
			response.Error(c, "创建复盘草稿失败")
			return
		}
		h.appendEventLog(event.ID, "review", "添加改进项时自动创建复盘草稿", operator.Username)
	}
	if review.Status == "archived" {
		response.BadRequest(c, "复盘已归档，要加改进项请先「重新打开」")
		return
	}

	item := model.EventActionItem{
		ReviewID: review.ID, EventID: event.ID,
		Title: truncate(req.Title, 200), Detail: req.Detail, Kind: req.Kind,
		Owner: req.Owner, DueDate: req.DueDate, Status: req.Status,
		CreatedByName: operator.Username, CreatedBy: operator.ID,
	}
	if req.Status == "done" {
		now := time.Now()
		item.DoneAt = &now
	}
	if err := h.DB.Create(&item).Error; err != nil {
		response.Error(c, "创建改进项失败")
		return
	}
	h.appendEventLog(event.ID, "review", "新增改进项："+item.Title+"（负责人 "+item.Owner+"）", operator.Username)
	h.notifyActionOwner(item, "改进项指派给你", operator.Username)
	response.OK(c, actionItemView(item))
}

// UpdateActionItem 改进项维护。完成时间由状态驱动，不让前端自己传。
func (h *Handler) UpdateActionItem(c *gin.Context) {
	var item model.EventActionItem
	if err := h.DB.First(&item, idParam(c)).Error; err != nil {
		response.NotFound(c, "改进项不存在")
		return
	}
	var req actionItemReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "请填写改进项标题与负责人")
		return
	}
	if err := req.normalize(h); err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	updates := map[string]any{
		"title": truncate(req.Title, 200), "detail": req.Detail, "kind": req.Kind,
		"owner": req.Owner, "due_date": req.DueDate, "status": req.Status,
	}
	switch {
	case req.Status == "done" && item.Status != "done":
		now := time.Now()
		updates["done_at"] = &now
	case req.Status != "done" && item.Status == "done":
		// 从已完成退回去，完成时间要清掉，否则详情页会显示「进行中但已完成于 xx」
		updates["done_at"], updates["done_note"] = nil, ""
	}
	if err := h.DB.Model(&model.EventActionItem{}).Where("id = ?", item.ID).Updates(updates).Error; err != nil {
		response.Error(c, "更新失败")
		return
	}

	operator := middleware.CurrentUser(c).Username
	h.appendEventLog(item.EventID, "review", "更新改进项："+truncate(req.Title, 120), operator)
	if req.Owner != item.Owner {
		item.Owner = req.Owner
		h.notifyActionOwner(item, "改进项转给你", operator)
	}
	response.OK(c, nil)
}

type actionDoneReq struct {
	Note string `json:"note"`
}

// FinishActionItem 负责人点「完成」的快捷入口：只改状态与完成说明。
func (h *Handler) FinishActionItem(c *gin.Context) {
	var item model.EventActionItem
	if err := h.DB.First(&item, idParam(c)).Error; err != nil {
		response.NotFound(c, "改进项不存在")
		return
	}
	if item.Status == "done" {
		response.BadRequest(c, "该改进项已经完成了")
		return
	}
	var req actionDoneReq
	_ = c.ShouldBindJSON(&req)

	operator := middleware.CurrentUser(c).Username
	now := time.Now()
	if err := h.DB.Model(&model.EventActionItem{}).Where("id = ?", item.ID).Updates(map[string]any{
		"status": "done", "done_at": &now, "done_note": truncate(req.Note, 250),
	}).Error; err != nil {
		response.Error(c, "更新失败")
		return
	}
	content := "改进项完成：" + truncate(item.Title, 120)
	if req.Note != "" {
		content += "（" + truncate(req.Note, 120) + "）"
	}
	h.appendEventLog(item.EventID, "review", content, operator)
	response.OK(c, nil)
}

func (h *Handler) DeleteActionItem(c *gin.Context) {
	var item model.EventActionItem
	if err := h.DB.First(&item, idParam(c)).Error; err != nil {
		response.NotFound(c, "改进项不存在")
		return
	}
	var review model.EventReview
	if err := h.DB.First(&review, item.ReviewID).Error; err == nil && review.Status == "archived" {
		response.BadRequest(c, "复盘已归档，改进项不能删；不做了请把状态改成「不做了」留痕")
		return
	}
	if err := h.DB.Delete(&model.EventActionItem{}, item.ID).Error; err != nil {
		response.Error(c, "删除失败")
		return
	}
	h.appendEventLog(item.EventID, "review", "删除改进项："+truncate(item.Title, 120),
		middleware.CurrentUser(c).Username)
	response.OK(c, nil)
}

func (h *Handler) notifyActionOwner(item model.EventActionItem, title, operator string) {
	var user model.User
	if err := h.DB.Where("username = ?", item.Owner).First(&user).Error; err != nil {
		return
	}
	due := "未设截止日期"
	if item.DueDate != nil {
		due = "截止 " + item.DueDate.Format("2006-01-02")
	}
	h.DB.Create(&model.Message{
		UserID: user.ID, Type: "review", Level: "info", RefID: item.EventID,
		Title:   title + "：" + truncate(item.Title, 180),
		Content: fmt.Sprintf("来自事件 #%d 的复盘，由 %s 指派，%s", item.EventID, operator, due),
	})
}

// ---------- 复盘看板 ----------

// ListReviews 复盘看板。除了已有复盘的，还要把「已解决/已关闭但还没归档复盘」的事件列出来，
// 否则复盘这件事永远不知道自己漏了什么。
func (h *Handler) ListReviews(c *gin.Context) {
	page, size := pageParams(c)
	status := c.Query("status")
	owner := c.Query("owner")

	type row struct {
		model.EventReview
		EventTitle    string `gorm:"column:event_title"`
		EventSeverity string `gorm:"column:event_severity"`
		EventStatus   string `gorm:"column:event_status"`
	}

	// pending 是虚拟状态：事件已经解决/关闭，但没有复盘记录
	if status == "pending" {
		q := h.DB.Model(&model.Event{}).
			Where("status IN ?", []string{"resolved", "closed"}).
			Where("id NOT IN (?)", h.DB.Model(&model.EventReview{}).Select("event_id"))
		if owner != "" {
			q = q.Where("assignee = ?", owner)
		}
		var total int64
		q.Count(&total)
		var events []model.Event
		q.Order("id desc").Offset((page - 1) * size).Limit(size).Find(&events)
		views := make([]gin.H, 0, len(events))
		for _, event := range events {
			views = append(views, gin.H{
				"eventId": event.ID, "eventTitle": event.Title,
				"eventSeverity": event.Severity, "eventStatus": event.Status,
				"status": "pending", "statusLabel": "待复盘",
				"owner": event.Assignee, "resolvedAt": event.ResolvedAt,
				"openItems": 0, "totalItems": 0, "overdueItems": 0,
			})
		}
		response.OKPage(c, views, total, page, size)
		return
	}

	// 计数与取数分开建查询：带 Select 的 query 上直接 Count，GORM 会把别名列一起塞进
	// count 语句里导致 SQL 报错
	filter := func(q *gorm.DB) *gorm.DB {
		if status != "" {
			q = q.Where("event_reviews.status = ?", status)
		}
		if owner != "" {
			q = q.Where("event_reviews.owner = ?", owner)
		}
		if kw := strings.TrimSpace(c.Query("keyword")); kw != "" {
			like := "%" + kw + "%"
			q = q.Where("events.title LIKE ? OR event_reviews.root_cause LIKE ?", like, like)
		}
		return q
	}

	var total int64
	if err := filter(h.DB.Model(&model.EventReview{}).
		Joins("LEFT JOIN events ON events.id = event_reviews.event_id")).
		Count(&total).Error; err != nil {
		response.Error(c, "查询复盘失败")
		return
	}
	var rows []row
	if err := filter(h.DB.Model(&model.EventReview{}).
		Select("event_reviews.*, events.title as event_title, events.severity as event_severity, events.status as event_status").
		Joins("LEFT JOIN events ON events.id = event_reviews.event_id")).
		Order("event_reviews.id desc").Offset((page - 1) * size).Limit(size).
		Find(&rows).Error; err != nil {
		response.Error(c, "查询复盘失败")
		return
	}

	// 一次查出所有改进项的计数，避免 N+1
	reviewIDs := make([]uint, 0, len(rows))
	for _, item := range rows {
		reviewIDs = append(reviewIDs, item.ID)
	}
	type counter struct {
		ReviewID uint
		Total    int
		Open     int
		Overdue  int
	}
	counters := map[uint]counter{}
	if len(reviewIDs) > 0 {
		var items []model.EventActionItem
		h.DB.Where("review_id IN ?", reviewIDs).Find(&items)
		now := time.Now()
		for _, item := range items {
			cur := counters[item.ReviewID]
			cur.ReviewID = item.ReviewID
			cur.Total++
			if item.Status != "done" && item.Status != "dropped" {
				cur.Open++
				if item.DueDate != nil && item.DueDate.Before(now) {
					cur.Overdue++
				}
			}
			counters[item.ReviewID] = cur
		}
	}

	views := make([]gin.H, 0, len(rows))
	for _, item := range rows {
		view := reviewView(item.EventReview)
		view["eventTitle"] = item.EventTitle
		view["eventSeverity"] = item.EventSeverity
		view["eventStatus"] = item.EventStatus
		view["totalItems"] = counters[item.ID].Total
		view["openItems"] = counters[item.ID].Open
		view["overdueItems"] = counters[item.ID].Overdue
		views = append(views, view)
	}
	response.OKPage(c, views, total, page, size)
}

func (h *Handler) ReviewStats(c *gin.Context) {
	user := middleware.CurrentUser(c)
	countReview := func(status string) int64 {
		var n int64
		h.DB.Model(&model.EventReview{}).Where("status = ?", status).Count(&n)
		return n
	}
	var pending int64
	h.DB.Model(&model.Event{}).Where("status IN ?", []string{"resolved", "closed"}).
		Where("id NOT IN (?)", h.DB.Model(&model.EventReview{}).Select("event_id")).Count(&pending)

	var openItems, overdueItems, mineItems int64
	h.DB.Model(&model.EventActionItem{}).Where("status IN ?", []string{"open", "doing"}).Count(&openItems)
	h.DB.Model(&model.EventActionItem{}).Where("status IN ? AND due_date IS NOT NULL AND due_date < ?",
		[]string{"open", "doing"}, time.Now()).Count(&overdueItems)
	h.DB.Model(&model.EventActionItem{}).Where("owner = ? AND status IN ?",
		user.Username, []string{"open", "doing"}).Count(&mineItems)

	// 已归档复盘的平均 MTTR，只统计算得出来的那些，并如实给出样本数
	var archived []model.EventReview
	h.DB.Where("status = ?", "archived").Find(&archived)
	var sum, samples int64
	for _, review := range archived {
		if review.HappenedAt != nil && review.RecoveredAt != nil && review.RecoveredAt.After(*review.HappenedAt) {
			sum += int64(review.RecoveredAt.Sub(*review.HappenedAt).Minutes())
			samples++
		}
	}
	avgRecover := any(nil)
	if samples > 0 {
		avgRecover = sum / samples
	}

	response.OK(c, gin.H{
		"pending": pending, "draft": countReview("draft"),
		"reviewing": countReview("reviewing"), "archived": countReview("archived"),
		"openItems": openItems, "overdueItems": overdueItems, "mineItems": mineItems,
		"avgRecoverMinutes": avgRecover, "recoverSamples": samples,
	})
}

// ListActionItems 改进项跟踪：跨事件看所有待办，支持只看我的、只看逾期的
func (h *Handler) ListActionItems(c *gin.Context) {
	page, size := pageParams(c)
	q := h.DB.Model(&model.EventActionItem{})
	if status := c.Query("status"); status != "" {
		q = q.Where("status = ?", status)
	}
	if owner := c.Query("owner"); owner != "" {
		q = q.Where("owner = ?", owner)
	}
	if kind := c.Query("kind"); kind != "" {
		q = q.Where("kind = ?", kind)
	}
	if c.Query("overdue") == "1" {
		q = q.Where("status IN ? AND due_date IS NOT NULL AND due_date < ?",
			[]string{"open", "doing"}, time.Now())
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		response.Error(c, "查询改进项失败")
		return
	}
	var rows []model.EventActionItem
	if err := q.Order("id desc").Offset((page - 1) * size).Limit(size).Find(&rows).Error; err != nil {
		response.Error(c, "查询改进项失败")
		return
	}

	// 带上事件标题，跟踪表里才知道这条待办是哪次故障来的
	eventIDs := map[uint]bool{}
	for _, row := range rows {
		eventIDs[row.EventID] = true
	}
	titles := map[uint]string{}
	if len(eventIDs) > 0 {
		var events []model.Event
		h.DB.Where("id IN ?", keysOf(eventIDs)).Find(&events)
		for _, event := range events {
			titles[event.ID] = event.Title
		}
	}

	views := make([]gin.H, 0, len(rows))
	for _, row := range rows {
		view := actionItemView(row)
		view["eventTitle"] = titles[row.EventID]
		views = append(views, view)
	}
	response.OKPage(c, views, total, page, size)
}

// ---------- 导出 ----------

// ExportEventReview 导出 Markdown。复盘要能贴进周报/群里，不然写完就烂在系统里。
func (h *Handler) ExportEventReview(c *gin.Context) {
	var event model.Event
	if err := h.DB.First(&event, idParam(c)).Error; err != nil {
		response.NotFound(c, "事件不存在")
		return
	}
	var review model.EventReview
	if err := h.DB.Where("event_id = ?", event.ID).First(&review).Error; err != nil {
		response.NotFound(c, "该事件还没有复盘")
		return
	}
	var items []model.EventActionItem
	h.DB.Where("review_id = ?", review.ID).Order("id asc").Find(&items)
	var logs []model.EventLog
	h.DB.Where("event_id = ?", event.ID).Order("id asc").Find(&logs)

	fmtTime := func(t *time.Time) string {
		if t == nil {
			return "未填"
		}
		return t.Format("2006-01-02 15:04:05")
	}
	fmtMinutes := func(v any) string {
		if v == nil {
			return "-"
		}
		return fmt.Sprintf("%v 分钟", v)
	}
	durations := reviewDurations(review)

	var b strings.Builder
	fmt.Fprintf(&b, "# 事件复盘 #%d %s\n\n", event.ID, event.Title)
	fmt.Fprintf(&b, "- 级别：%s\n- 事件状态：%s\n- 复盘状态：%s\n- 复盘负责人：%s\n\n",
		event.Severity, eventStatusLabels[event.Status], reviewStatusLabels[review.Status], review.Owner)

	b.WriteString("## 时间线\n\n")
	fmt.Fprintf(&b, "- 故障开始：%s\n", fmtTime(review.HappenedAt))
	fmt.Fprintf(&b, "- 被发现：%s（发现耗时 %s）\n", fmtTime(review.DetectedAt), fmtMinutes(durations["detectMinutes"]))
	fmt.Fprintf(&b, "- 开始响应：%s（响应耗时 %s）\n", fmtTime(review.RespondedAt), fmtMinutes(durations["ackMinutes"]))
	fmt.Fprintf(&b, "- 止血完成：%s（止血耗时 %s）\n", fmtTime(review.MitigatedAt), fmtMinutes(durations["mitigateMinutes"]))
	fmt.Fprintf(&b, "- 完全恢复：%s（总时长 %s）\n\n", fmtTime(review.RecoveredAt), fmtMinutes(durations["recoverMinutes"]))

	section := func(title, body string) {
		fmt.Fprintf(&b, "## %s\n\n%s\n\n", title, strings.TrimSpace(orNotFilled(body)))
	}
	section("影响面", review.Impact)
	section("根因", review.RootCause)
	section("诱因", review.Trigger)
	section("为什么没更早发现", review.DetectGap)
	section("止血过程", review.Mitigation)
	section("经验教训", review.Lesson)

	b.WriteString("## 改进项\n\n")
	if len(items) == 0 {
		b.WriteString("（无）\n\n")
	} else {
		for _, item := range items {
			due := "无截止"
			if item.DueDate != nil {
				due = item.DueDate.Format("2006-01-02")
			}
			mark := " "
			if item.Status == "done" {
				mark = "x"
			}
			fmt.Fprintf(&b, "- [%s] %s（%s / %s / %s / %s）\n", mark, item.Title,
				actionItemKindLabels[item.Kind], item.Owner, due, actionItemStatusLabels[item.Status])
			if item.Detail != "" {
				fmt.Fprintf(&b, "  - %s\n", item.Detail)
			}
		}
		b.WriteString("\n")
	}

	b.WriteString("## 处置时间线（系统记录）\n\n")
	for _, entry := range logs {
		fmt.Fprintf(&b, "- %s `%s` %s：%s\n",
			entry.CreatedAt.Format("2006-01-02 15:04:05"), entry.Action, entry.Operator, entry.Content)
	}

	response.OK(c, gin.H{
		"filename": fmt.Sprintf("event-%d-review.md", event.ID),
		"markdown": b.String(),
	})
}

func orNotFilled(s string) string {
	if strings.TrimSpace(s) == "" {
		return "（未填）"
	}
	return s
}

// ---------- 逾期催办 ----------

// RemindOverdueActionItems 定时任务：给逾期未完成的改进项负责人发站内消息，一天最多一条。
func (h *Handler) RemindOverdueActionItems() {
	now := time.Now()
	var items []model.EventActionItem
	if err := h.DB.Where("status IN ? AND due_date IS NOT NULL AND due_date < ?",
		[]string{"open", "doing"}, now).Find(&items).Error; err != nil {
		log.Printf("[review] 改进项逾期检查失败: %v", err)
		return
	}

	// 同一个人的多条逾期项合成一条消息，避免一次塞满消息中心
	byOwner := map[string][]model.EventActionItem{}
	for _, item := range items {
		if item.LastRemindAt != nil && now.Sub(*item.LastRemindAt) < 24*time.Hour {
			continue
		}
		byOwner[item.Owner] = append(byOwner[item.Owner], item)
	}

	reminded, total := 0, 0
	owners := make([]string, 0, len(byOwner))
	for owner := range byOwner {
		owners = append(owners, owner)
	}
	sort.Strings(owners)
	for _, owner := range owners {
		pending := byOwner[owner]
		var user model.User
		if err := h.DB.Where("username = ?", owner).First(&user).Error; err != nil {
			log.Printf("[review] 改进项负责人 %s 不存在，%d 条逾期项没能催办", owner, len(pending))
			continue
		}
		lines := make([]string, 0, len(pending))
		ids := make([]uint, 0, len(pending))
		for _, item := range pending {
			lines = append(lines, fmt.Sprintf("· %s（事件 #%d，截止 %s）",
				item.Title, item.EventID, item.DueDate.Format("2006-01-02")))
			ids = append(ids, item.ID)
		}
		h.DB.Create(&model.Message{
			UserID: user.ID, Type: "review", Level: "warning", RefID: pending[0].EventID,
			Title:   fmt.Sprintf("有 %d 条复盘改进项已逾期", len(pending)),
			Content: strings.Join(lines, "\n") + "\n请到「监控告警 → 事件复盘 → 改进项跟踪」更新进度",
		})
		h.DB.Model(&model.EventActionItem{}).Where("id IN ?", ids).
			Update("last_remind_at", &now)
		reminded++
		total += len(pending)
	}

	h.markFixedRun("review", fmt.Sprintf("逾期改进项 %d 条，催办 %d 人", total, reminded))
	if reminded > 0 {
		log.Printf("[review] 改进项逾期催办完成: %d 人、%d 条", reminded, total)
	}
}
