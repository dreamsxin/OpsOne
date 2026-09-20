package handler

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"ops-platform/server/internal/middleware"
	"ops-platform/server/internal/model"
	"ops-platform/server/internal/response"
)

// 事件状态流转：open -> processing -> resolved，任何状态都可以直接 closed（判定为无需处理）
var eventStatusFlow = map[string][]string{
	"open":       {"processing", "resolved", "closed"},
	"processing": {"resolved", "closed"},
	"resolved":   {"processing", "closed"},
	"closed":     {"open"},
}

var eventStatusLabels = map[string]string{
	"open": "待处理", "processing": "处理中", "resolved": "已解决", "closed": "已关闭",
}

func eventAlertIDs(event model.Event) []uint {
	ids := []uint{}
	if event.AlertIDs != "" {
		_ = json.Unmarshal([]byte(event.AlertIDs), &ids)
	}
	return ids
}

// eventView 列表与详情共用的输出结构，把 AlertIDs 展开成数组
func eventView(event model.Event) gin.H {
	return gin.H{
		"id": event.ID, "title": event.Title, "severity": event.Severity,
		"status": event.Status, "statusLabel": eventStatusLabels[event.Status],
		"summary": event.Summary, "alertIds": eventAlertIDs(event),
		"origin": event.Origin, "originNote": event.OriginNote,
		"assignee": event.Assignee, "assignedBy": event.AssignedBy, "assignedAt": event.AssignedAt,
		"createdByName": event.CreatedByName, "lastActivityAt": event.LastActivityAt,
		"resolvedAt": event.ResolvedAt, "resolvedBy": event.ResolvedBy,
		"createdAt": event.CreatedAt,
	}
}

// appendEventLog 追加一条时间线记录并刷新事件的最近活动时间
func (h *Handler) appendEventLog(eventID uint, action, content, operator string) {
	h.DB.Create(&model.EventLog{
		EventID: eventID, Action: action,
		Content: truncate(content, 490), Operator: operator,
	})
	h.DB.Model(&model.Event{}).Where("id = ?", eventID).
		Update("last_activity_at", time.Now())
}

// ---------- 建单 ----------

type eventCreateReq struct {
	Title    string `json:"title"`
	Severity string `json:"severity"`
	Summary  string `json:"summary"`
	AlertIDs []uint `json:"alertIds"`
	Assignee string `json:"assignee"`
}

// highestSeverity 取一组告警里最高的级别，作为事件默认级别
func highestSeverity(alerts []model.Alert) string {
	rank := map[string]int{"info": 1, "warning": 2, "critical": 3}
	best := "info"
	for _, alert := range alerts {
		if rank[alert.Severity] > rank[best] {
			best = alert.Severity
		}
	}
	return best
}

// loadAlertsByIDs 按 ID 取告警，任何一个不存在就报错，避免建出挂空告警的事件
func (h *Handler) loadAlertsByIDs(ids []uint) ([]model.Alert, error) {
	if len(ids) == 0 {
		return nil, fmt.Errorf("请至少选择一条告警")
	}
	if len(ids) > 200 {
		return nil, fmt.Errorf("一个事件最多关联 200 条告警")
	}
	var alerts []model.Alert
	if err := h.DB.Where("id IN ?", ids).Find(&alerts).Error; err != nil {
		return nil, fmt.Errorf("查询告警失败")
	}
	if len(alerts) != len(ids) {
		return nil, fmt.Errorf("有 %d 条告警不存在", len(ids)-len(alerts))
	}
	return alerts, nil
}

func (h *Handler) createEvent(c *gin.Context, req eventCreateReq, origin, originNote string) {
	alerts, err := h.loadAlertsByIDs(req.AlertIDs)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	user := middleware.CurrentUser(c)
	title := strings.TrimSpace(req.Title)
	if title == "" {
		title = alerts[0].Title
		if len(alerts) > 1 {
			title = fmt.Sprintf("%s 等 %d 条告警", alerts[0].Title, len(alerts))
		}
	}
	severity := normalizeSeverity(req.Severity)
	if req.Severity == "" {
		severity = highestSeverity(alerts)
	}

	now := time.Now()
	event := model.Event{
		Title: truncate(title, 250), Severity: severity, Status: "open",
		Summary: req.Summary, AlertIDs: marshalIDs(req.AlertIDs),
		Origin: origin, OriginNote: truncate(originNote, 250),
		CreatedByName: user.Username, LastActivityAt: now, CreatedBy: user.ID,
	}
	if req.Assignee != "" {
		event.Assignee = req.Assignee
		event.AssignedBy = user.Username
		event.AssignedAt = &now
	}
	if err := h.DB.Create(&event).Error; err != nil {
		response.Error(c, "创建事件失败")
		return
	}

	detail := fmt.Sprintf("由 %d 条告警建单", len(alerts))
	if originNote != "" {
		detail += "（" + originNote + "）"
	}
	h.appendEventLog(event.ID, "create", detail, user.Username)
	if event.Assignee != "" {
		h.appendEventLog(event.ID, "assign", "指派给 "+event.Assignee, user.Username)
		h.notifyEventAssignee(event, user.Username)
	}
	response.OK(c, eventView(event))
}

// CreateEvent 手动选告警建单
func (h *Handler) CreateEvent(c *gin.Context) {
	var req eventCreateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "参数格式有误")
		return
	}
	h.createEvent(c, req, "manual", "")
}

type eventFromBucketReq struct {
	PolicyID  uint   `json:"policyId" binding:"required"`
	BucketKey string `json:"bucketKey" binding:"required"`
	Assignee  string `json:"assignee"`
	Title     string `json:"title"`
	Summary   string `json:"summary"`
}

// CreateEventFromBucket 把聚合策略算出的某个桶整体建成一个事件。
// 桶是实时算的，这里重新算一遍再取告警，避免用前端传来的过期列表。
func (h *Handler) CreateEventFromBucket(c *gin.Context) {
	var req eventFromBucketReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "请提供聚合策略与桶标识")
		return
	}

	var policy model.AggregationPolicy
	if err := h.DB.First(&policy, req.PolicyID).Error; err != nil {
		response.NotFound(c, "聚合策略不存在")
		return
	}

	dimensions := parseDimensions(policy.Dimensions)
	if len(dimensions) == 0 {
		response.BadRequest(c, "该策略没有配置归桶维度")
		return
	}

	since := time.Now().Add(-time.Duration(policy.WindowMinutes) * time.Minute)
	var alerts []model.Alert
	if err := h.DB.Where("status <> ? AND last_seen_at >= ?", "resolved", since).
		Find(&alerts).Error; err != nil {
		response.Error(c, "查询告警失败")
		return
	}

	ids := make([]uint, 0, 8)
	for _, alert := range alerts {
		if !policyMatches(policy, alert) {
			continue
		}
		if key, _ := bucketKeyOf(alert, dimensions); key == req.BucketKey {
			ids = append(ids, alert.ID)
		}
	}
	if len(ids) == 0 {
		response.BadRequest(c, "该桶当前没有未恢复的告警，可能已经恢复或超出窗口")
		return
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })

	h.createEvent(c, eventCreateReq{
		Title: req.Title, Summary: req.Summary, AlertIDs: ids, Assignee: req.Assignee,
	}, "bucket", policy.Name+" / "+req.BucketKey)
}

// notifyEventAssignee 指派时给负责人发一条站内消息
func (h *Handler) notifyEventAssignee(event model.Event, operator string) {
	var user model.User
	if err := h.DB.Where("username = ?", event.Assignee).First(&user).Error; err != nil {
		return
	}
	h.DB.Create(&model.Message{
		UserID: user.ID, Type: "event", Level: event.Severity, RefID: event.ID,
		Title:   "事件指派给你：" + truncate(event.Title, 200),
		Content: fmt.Sprintf("由 %s 指派，级别 %s，请到「监控告警 → 事件中心」处理", operator, event.Severity),
	})
}

// ---------- 查询 ----------

func (h *Handler) ListEvents(c *gin.Context) {
	page, size := pageParams(c)
	q := h.DB.Model(&model.Event{})
	if status := c.Query("status"); status != "" {
		q = q.Where("status = ?", status)
	}
	if severity := c.Query("severity"); severity != "" {
		q = q.Where("severity = ?", severity)
	}
	if assignee := c.Query("assignee"); assignee != "" {
		q = q.Where("assignee = ?", assignee)
	}
	if kw := strings.TrimSpace(c.Query("keyword")); kw != "" {
		like := "%" + kw + "%"
		q = q.Where("title LIKE ? OR summary LIKE ?", like, like)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		response.Error(c, "查询事件失败")
		return
	}
	var list []model.Event
	if err := q.Order("id desc").Offset((page - 1) * size).Limit(size).Find(&list).Error; err != nil {
		response.Error(c, "查询事件失败")
		return
	}

	views := make([]gin.H, 0, len(list))
	for _, item := range list {
		views = append(views, eventView(item))
	}
	response.OKPage(c, views, total, page, size)
}

func (h *Handler) EventStats(c *gin.Context) {
	count := func(where string, args ...any) int64 {
		var n int64
		q := h.DB.Model(&model.Event{})
		if where != "" {
			q = q.Where(where, args...)
		}
		q.Count(&n)
		return n
	}
	user := middleware.CurrentUser(c)
	todayStart := time.Now().Truncate(24 * time.Hour)

	response.OK(c, gin.H{
		"total":      count(""),
		"open":       count("status = ?", "open"),
		"processing": count("status = ?", "processing"),
		"resolved":   count("status = ?", "resolved"),
		"mine":       count("assignee = ? AND status IN ?", user.Username, []string{"open", "processing"}),
		"unassigned": count("assignee = '' AND status IN ?", []string{"open", "processing"}),
		"today":      count("created_at >= ?", todayStart),
	})
}

// GetEvent 事件详情：基本信息 + 处置时间线 + 关联告警 + 诊断上下文
func (h *Handler) GetEvent(c *gin.Context) {
	var event model.Event
	if err := h.DB.First(&event, idParam(c)).Error; err != nil {
		response.NotFound(c, "事件不存在")
		return
	}

	ids := eventAlertIDs(event)
	var alerts []model.Alert
	if len(ids) > 0 {
		h.DB.Where("id IN ?", ids).Order("id desc").Find(&alerts)
	}

	var logs []model.EventLog
	h.DB.Where("event_id = ?", event.ID).Order("id asc").Find(&logs)

	response.OK(c, gin.H{
		"event":   eventView(event),
		"alerts":  alerts,
		"logs":    logs,
		"context": h.eventContext(alerts),
	})
}

// eventContext 从关联告警的标签反查平台里的对象，给出「现在是什么状态」。
//
// 只认平台自己能解释的标签：host / domain / target / probeId / certId / ruleId。
// 认不出来的标签原样列出，不猜。
func (h *Handler) eventContext(alerts []model.Alert) gin.H {
	hosts := map[string]bool{}
	probeIDs := map[uint]bool{}
	certIDs := map[uint]bool{}
	ruleIDs := map[uint]bool{}
	others := map[string]string{}

	for _, alert := range alerts {
		labels := map[string]string{}
		if alert.Labels != "" {
			_ = json.Unmarshal([]byte(alert.Labels), &labels)
		}
		for key, value := range labels {
			switch key {
			case "host", "domain", "target", "address":
				hosts[value] = true
			case "probeId":
				if id := parseUint(value); id > 0 {
					probeIDs[id] = true
				}
			case "certId":
				if id := parseUint(value); id > 0 {
					certIDs[id] = true
				}
			case "ruleId":
				if id := parseUint(value); id > 0 {
					ruleIDs[id] = true
				}
			case "module", "metric", "type", "port":
				// 这些是分类信息，不指向具体对象
			default:
				others[key] = value
			}
		}
	}

	hostViews := make([]gin.H, 0, 4)
	if len(hosts) > 0 {
		names := make([]string, 0, len(hosts))
		for name := range hosts {
			names = append(names, name)
		}
		var matched []model.Host
		h.DB.Where("name IN ? OR address IN ?", names, names).Find(&matched)
		for _, host := range matched {
			hostViews = append(hostViews, gin.H{
				"id": host.ID, "name": host.Name, "address": host.Address,
				"env": host.Env, "status": host.Status, "checkedAt": host.CheckedAt,
			})
		}
		// 标签里出现过但资产里没有的目标，也要如实列出来
		for _, name := range names {
			found := false
			for _, host := range matched {
				if host.Name == name || host.Address == name {
					found = true
					break
				}
			}
			if !found {
				hostViews = append(hostViews, gin.H{"name": name, "status": "未纳管"})
			}
		}
	}

	probes := make([]model.Probe, 0)
	if len(probeIDs) > 0 {
		h.DB.Where("id IN ?", keysOf(probeIDs)).Find(&probes)
	}
	certs := make([]model.Certificate, 0)
	if len(certIDs) > 0 {
		h.DB.Where("id IN ?", keysOf(certIDs)).Find(&certs)
	}
	rules := make([]model.AlertRule, 0)
	if len(ruleIDs) > 0 {
		h.DB.Where("id IN ?", keysOf(ruleIDs)).Find(&rules)
	}

	return gin.H{
		"hosts": hostViews, "probes": probes,
		"certificates": certs, "rules": rules, "otherLabels": others,
	}
}

func keysOf(m map[uint]bool) []uint {
	ids := make([]uint, 0, len(m))
	for id := range m {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

func parseUint(raw string) uint {
	var n uint64
	if _, err := fmt.Sscanf(strings.TrimSpace(raw), "%d", &n); err != nil {
		return 0
	}
	return uint(n)
}

// ---------- 处置 ----------

type eventAssignReq struct {
	Assignee string `json:"assignee" binding:"required"`
	Note     string `json:"note"`
}

func (h *Handler) AssignEvent(c *gin.Context) {
	var event model.Event
	if err := h.DB.First(&event, idParam(c)).Error; err != nil {
		response.NotFound(c, "事件不存在")
		return
	}

	var req eventAssignReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "请指定负责人")
		return
	}

	var target model.User
	if err := h.DB.Where("username = ?", req.Assignee).First(&target).Error; err != nil {
		response.BadRequest(c, "负责人不存在: "+req.Assignee)
		return
	}
	if target.Status != 1 {
		response.BadRequest(c, "该账号已被禁用，不能作为负责人")
		return
	}

	operator := middleware.CurrentUser(c).Username
	now := time.Now()
	h.DB.Model(&model.Event{}).Where("id = ?", event.ID).Updates(map[string]any{
		"assignee": req.Assignee, "assigned_by": operator, "assigned_at": &now,
	})

	content := "指派给 " + req.Assignee
	if event.Assignee != "" && event.Assignee != req.Assignee {
		content = fmt.Sprintf("负责人 %s -> %s", event.Assignee, req.Assignee)
	}
	if req.Note != "" {
		content += "：" + req.Note
	}
	h.appendEventLog(event.ID, "assign", content, operator)

	event.Assignee = req.Assignee
	h.notifyEventAssignee(event, operator)
	response.OK(c, gin.H{"assignee": req.Assignee})
}

type eventNoteReq struct {
	Content string `json:"content" binding:"required"`
}

func (h *Handler) AddEventNote(c *gin.Context) {
	var event model.Event
	if err := h.DB.First(&event, idParam(c)).Error; err != nil {
		response.NotFound(c, "事件不存在")
		return
	}

	var req eventNoteReq
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.Content) == "" {
		response.BadRequest(c, "处置记录内容不能为空")
		return
	}
	h.appendEventLog(event.ID, "note", req.Content, middleware.CurrentUser(c).Username)
	response.OK(c, nil)
}

type eventStatusReq struct {
	Status string `json:"status" binding:"required"`
	Note   string `json:"note"`
	// ResolveAlerts 置为已解决时是否同时恢复关联告警
	ResolveAlerts bool `json:"resolveAlerts"`
}

func (h *Handler) UpdateEventStatus(c *gin.Context) {
	var event model.Event
	if err := h.DB.First(&event, idParam(c)).Error; err != nil {
		response.NotFound(c, "事件不存在")
		return
	}

	var req eventStatusReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "请指定目标状态")
		return
	}
	if _, ok := eventStatusLabels[req.Status]; !ok {
		response.BadRequest(c, "状态只能是 open / processing / resolved / closed")
		return
	}

	allowed := false
	for _, next := range eventStatusFlow[event.Status] {
		if next == req.Status {
			allowed = true
			break
		}
	}
	if !allowed {
		response.BadRequest(c, fmt.Sprintf("不允许从「%s」直接变为「%s」",
			eventStatusLabels[event.Status], eventStatusLabels[req.Status]))
		return
	}

	operator := middleware.CurrentUser(c).Username
	now := time.Now()
	updates := map[string]any{"status": req.Status}
	switch req.Status {
	case "resolved":
		updates["resolved_at"], updates["resolved_by"] = &now, operator
	case "open", "processing":
		// 重新打开时清掉解决信息，避免详情页显示已解决又是处理中
		updates["resolved_at"], updates["resolved_by"] = nil, ""
	}
	h.DB.Model(&model.Event{}).Where("id = ?", event.ID).Updates(updates)

	content := fmt.Sprintf("状态 %s -> %s", eventStatusLabels[event.Status], eventStatusLabels[req.Status])
	if req.Note != "" {
		content += "：" + req.Note
	}

	resolvedAlerts := 0
	if req.Status == "resolved" && req.ResolveAlerts {
		ids := eventAlertIDs(event)
		if len(ids) > 0 {
			result := h.DB.Model(&model.Alert{}).
				Where("id IN ? AND status <> ?", ids, "resolved").
				Updates(map[string]any{
					"status": "resolved", "resolved_at": &now,
					"handle_note": "事件 #" + fmt.Sprint(event.ID) + " 处置完成",
				})
			resolvedAlerts = int(result.RowsAffected)
			content += fmt.Sprintf("，同时恢复 %d 条关联告警", resolvedAlerts)
		}
	}
	h.appendEventLog(event.ID, "status", content, operator)

	response.OK(c, gin.H{"status": req.Status, "resolvedAlerts": resolvedAlerts})
}

func (h *Handler) DeleteEvent(c *gin.Context) {
	var event model.Event
	if err := h.DB.First(&event, idParam(c)).Error; err != nil {
		response.NotFound(c, "事件不存在")
		return
	}
	// 时间线跟着事件删掉；关联告警不动
	h.DB.Where("event_id = ?", event.ID).Delete(&model.EventLog{})
	if err := h.DB.Delete(&model.Event{}, event.ID).Error; err != nil {
		response.Error(c, "删除失败")
		return
	}
	response.OK(c, nil)
}
