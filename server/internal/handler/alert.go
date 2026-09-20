package handler

import (
	"crypto/sha1"
	"encoding/hex"
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

// alertPayload 通用告警入参。既支持单条对象，也支持 {"alerts":[...]} 批量。
type alertPayload struct {
	Title       string            `json:"title"`
	Summary     string            `json:"summary"`
	Severity    string            `json:"severity"`
	Value       string            `json:"value"`
	Status      string            `json:"status"` // firing | resolved，缺省按 firing
	Fingerprint string            `json:"fingerprint"`
	Labels      map[string]string `json:"labels"`
}

type alertEnvelope struct {
	alertPayload
	Alerts []alertPayload `json:"alerts"`
}

// ReceiveAlert 告警接入入口，走 Token 鉴权，不需要登录态
func (h *Handler) ReceiveAlert(c *gin.Context) {
	token := c.Param("token")
	var source model.AlertSource
	if err := h.DB.Where("token = ?", token).First(&source).Error; err != nil {
		response.NotFound(c, "接入源不存在")
		return
	}
	if !source.Enabled {
		response.Forbidden(c, "接入源已停用")
		return
	}

	var envelope alertEnvelope
	if err := c.ShouldBindJSON(&envelope); err != nil {
		response.BadRequest(c, "请求体必须是 JSON: "+err.Error())
		return
	}

	items := envelope.Alerts
	if len(items) == 0 {
		items = []alertPayload{envelope.alertPayload}
	}

	accepted := 0
	for _, item := range items {
		if strings.TrimSpace(item.Title) == "" {
			continue
		}
		h.ingestAlert(&source, item)
		accepted++
	}
	if accepted == 0 {
		response.BadRequest(c, "没有可用告警，title 为必填字段")
		return
	}

	now := time.Now()
	h.DB.Model(&source).Updates(map[string]any{
		"received_count": source.ReceivedCount + accepted,
		"last_seen_at":   &now,
	})

	response.OK(c, gin.H{"accepted": accepted})
}

// ingestAlert 落库单条告警：同指纹的未恢复告警只累加次数，新告警触发通知派发
func (h *Handler) ingestAlert(source *model.AlertSource, item alertPayload) {
	labels, _ := json.Marshal(item.Labels)
	fingerprint := item.Fingerprint
	if fingerprint == "" {
		fingerprint = alertFingerprint(item.Title, item.Labels)
	}

	now := time.Now()
	resolved := item.Status == "resolved"

	var existing model.Alert
	err := h.DB.Where("fingerprint = ? AND status <> ?", fingerprint, "resolved").
		Order("id desc").First(&existing).Error

	if err == nil {
		updates := map[string]any{"last_seen_at": now, "count": existing.Count + 1}
		if resolved {
			updates["status"] = "resolved"
			updates["resolved_at"] = &now
		}
		h.DB.Model(&existing).Updates(updates)
		return
	}

	// 恢复通知没有对应的活跃告警时不制造新记录，避免噪声
	if resolved {
		return
	}

	alert := model.Alert{
		SourceID: source.ID, SourceName: source.Name, Fingerprint: fingerprint,
		Title: item.Title, Summary: item.Summary, Severity: normalizeSeverity(item.Severity),
		Status: "firing", Labels: string(labels), Value: item.Value, Count: 1,
		FirstSeenAt: now, LastSeenAt: now,
	}
	if err := h.DB.Create(&alert).Error; err != nil {
		return
	}

	// 派发放到后台，避免拖慢接入方的请求
	go h.dispatchAlert(alert)
	if alert.Severity == "critical" {
		go h.notifyCriticalAlert(alert)
	}
}

// alertFingerprint 用标题与标签生成稳定指纹
func alertFingerprint(title string, labels map[string]string) string {
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var sb strings.Builder
	sb.WriteString(title)
	for _, k := range keys {
		sb.WriteString("|" + k + "=" + labels[k])
	}
	sum := sha1.Sum([]byte(sb.String()))
	return hex.EncodeToString(sum[:])
}

func normalizeSeverity(s string) string {
	switch strings.ToLower(s) {
	case "critical", "crit", "fatal", "p0":
		return "critical"
	case "warning", "warn", "p1":
		return "warning"
	default:
		return "info"
	}
}

// ListAlerts 告警列表
func (h *Handler) ListAlerts(c *gin.Context) {
	page, size := pageParams(c)
	q := h.DB.Model(&model.Alert{})

	if status := c.Query("status"); status != "" {
		q = q.Where("status = ?", status)
	}
	if severity := c.Query("severity"); severity != "" {
		q = q.Where("severity = ?", severity)
	}
	if kw := strings.TrimSpace(c.Query("keyword")); kw != "" {
		like := "%" + kw + "%"
		q = q.Where("title LIKE ? OR summary LIKE ? OR labels LIKE ?", like, like, like)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		response.Error(c, "查询告警失败")
		return
	}
	var list []model.Alert
	if err := q.Order("id desc").Offset((page - 1) * size).Limit(size).Find(&list).Error; err != nil {
		response.Error(c, "查询告警失败")
		return
	}
	response.OKPage(c, list, total, page, size)
}

// AlertStats 告警概览，供列表页顶部展示
func (h *Handler) AlertStats(c *gin.Context) {
	count := func(where ...any) int64 {
		var n int64
		q := h.DB.Model(&model.Alert{})
		if len(where) > 0 {
			q = q.Where(where[0], where[1:]...)
		}
		q.Count(&n)
		return n
	}

	response.OK(c, gin.H{
		"total":    count(),
		"firing":   count("status = ?", "firing"),
		"acked":    count("status = ?", "acked"),
		"resolved": count("status = ?", "resolved"),
		"critical": count("status <> ? AND severity = ?", "resolved", "critical"),
		"warning":  count("status <> ? AND severity = ?", "resolved", "warning"),
	})
}

// GetAlert 告警详情，附带该告警的通知投递记录
func (h *Handler) GetAlert(c *gin.Context) {
	var alert model.Alert
	if err := h.DB.First(&alert, idParam(c)).Error; err != nil {
		response.NotFound(c, "告警不存在")
		return
	}
	var records []model.NotifyRecord
	h.DB.Where("alert_id = ?", alert.ID).Order("id desc").Find(&records)

	response.OK(c, gin.H{"alert": alert, "records": records})
}

type alertHandleReq struct {
	Note string `json:"note"`
}

// AckAlert 确认告警
func (h *Handler) AckAlert(c *gin.Context) {
	var alert model.Alert
	if err := h.DB.First(&alert, idParam(c)).Error; err != nil {
		response.NotFound(c, "告警不存在")
		return
	}
	if alert.Status == "resolved" {
		response.BadRequest(c, "已恢复的告警无需确认")
		return
	}

	var req alertHandleReq
	_ = c.ShouldBindJSON(&req)

	now := time.Now()
	user := middleware.CurrentUser(c)
	err := h.DB.Model(&alert).Updates(map[string]any{
		"status": "acked", "ack_by": user.Username, "ack_at": &now,
		"handle_note": truncate(req.Note, 240),
	}).Error
	if err != nil {
		response.Error(c, "确认失败")
		return
	}
	response.OK(c, nil)
}

// ResolveAlert 人工恢复告警
func (h *Handler) ResolveAlert(c *gin.Context) {
	var alert model.Alert
	if err := h.DB.First(&alert, idParam(c)).Error; err != nil {
		response.NotFound(c, "告警不存在")
		return
	}
	var req alertHandleReq
	_ = c.ShouldBindJSON(&req)

	now := time.Now()
	note := alert.HandleNote
	if req.Note != "" {
		note = truncate(req.Note, 240)
	}
	err := h.DB.Model(&alert).Updates(map[string]any{
		"status": "resolved", "resolved_at": &now, "handle_note": note,
	}).Error
	if err != nil {
		response.Error(c, "恢复失败")
		return
	}
	response.OK(c, nil)
}

// ---------- 告警接入源 ----------

type alertSourceReq struct {
	Name    string `json:"name" binding:"required"`
	Remark  string `json:"remark"`
	Enabled *bool  `json:"enabled"`
}

func (h *Handler) ListAlertSources(c *gin.Context) {
	var list []model.AlertSource
	if err := h.DB.Order("id asc").Find(&list).Error; err != nil {
		response.Error(c, "查询接入源失败")
		return
	}
	response.OK(c, list)
}

func (h *Handler) CreateAlertSource(c *gin.Context) {
	var req alertSourceReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "接入源名称不能为空")
		return
	}

	source := model.AlertSource{
		Name: req.Name, Remark: req.Remark, Enabled: true,
		Token: newAlertToken(req.Name),
	}
	if req.Enabled != nil {
		source.Enabled = *req.Enabled
	}
	if err := h.DB.Create(&source).Error; err != nil {
		response.Error(c, "接入源创建失败")
		return
	}
	response.OK(c, source)
}

func (h *Handler) UpdateAlertSource(c *gin.Context) {
	var source model.AlertSource
	if err := h.DB.First(&source, idParam(c)).Error; err != nil {
		response.NotFound(c, "接入源不存在")
		return
	}
	var req alertSourceReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "参数校验失败")
		return
	}

	source.Name, source.Remark = req.Name, req.Remark
	if req.Enabled != nil {
		source.Enabled = *req.Enabled
	}
	if err := h.DB.Save(&source).Error; err != nil {
		response.Error(c, "接入源更新失败")
		return
	}
	response.OK(c, source)
}

// RotateAlertSourceToken 重置 Token，旧地址立即失效
func (h *Handler) RotateAlertSourceToken(c *gin.Context) {
	var source model.AlertSource
	if err := h.DB.First(&source, idParam(c)).Error; err != nil {
		response.NotFound(c, "接入源不存在")
		return
	}
	source.Token = newAlertToken(source.Name)
	if err := h.DB.Save(&source).Error; err != nil {
		response.Error(c, "Token 重置失败")
		return
	}
	response.OK(c, source)
}

func (h *Handler) DeleteAlertSource(c *gin.Context) {
	if err := h.DB.Delete(&model.AlertSource{}, idParam(c)).Error; err != nil {
		response.Error(c, "接入源删除失败")
		return
	}
	response.OK(c, nil)
}

// newAlertToken 用名称与当前时间生成不可猜测的 Token
func newAlertToken(name string) string {
	seed := fmt.Sprintf("%s|%d", name, time.Now().UnixNano())
	sum := sha1.Sum([]byte(seed))
	return hex.EncodeToString(sum[:])
}
