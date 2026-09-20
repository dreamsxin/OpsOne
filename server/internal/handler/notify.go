package handler

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"ops-platform/server/internal/model"
	"ops-platform/server/internal/response"
)

// notifyTimeout 单次外发的超时
const notifyTimeout = 10 * time.Second

// ---------- 通知派发 ----------

// dispatchAlert 为新告警选路并投递。按 Priority 升序取第一条命中的路由，
// 都没命中时回落到 IsDefault 路由；没有兜底路由则只记录日志。
func (h *Handler) dispatchAlert(alert model.Alert) {
	var routes []model.NotifyRoute
	if err := h.DB.Where("enabled = ?", true).Order("priority asc, id asc").Find(&routes).Error; err != nil {
		log.Printf("[notify] 加载路由失败: %v", err)
		return
	}

	labels := map[string]string{}
	if alert.Labels != "" {
		_ = json.Unmarshal([]byte(alert.Labels), &labels)
	}

	var matched *model.NotifyRoute
	var fallback *model.NotifyRoute
	for i := range routes {
		route := routes[i]
		if route.IsDefault && fallback == nil {
			fallback = &routes[i]
		}
		if routeMatches(route, alert, labels) {
			matched = &routes[i]
			break
		}
	}
	if matched == nil {
		matched = fallback
	}
	if matched == nil {
		log.Printf("[notify] 告警 %d 未命中任何路由且无兜底路由，跳过通知", alert.ID)
		return
	}

	channelIDs := parseIDList(matched.ChannelIDs)
	if len(channelIDs) == 0 {
		log.Printf("[notify] 路由 %d(%s) 没有配置渠道", matched.ID, matched.Name)
		return
	}

	var channels []model.NotifyChannel
	if err := h.DB.Where("id IN ? AND enabled = ?", channelIDs, true).Find(&channels).Error; err != nil {
		log.Printf("[notify] 加载渠道失败: %v", err)
		return
	}
	for i := range channels {
		h.sendToChannel(alert, matched, channels[i])
	}
}

// routeMatches 级别命中（为空即不限）且标签条件全部命中
func routeMatches(route model.NotifyRoute, alert model.Alert, labels map[string]string) bool {
	if route.IsDefault {
		return false // 兜底路由只在没有其他命中时使用
	}

	if sev := strings.TrimSpace(route.MatchSeverity); sev != "" {
		hit := false
		for _, item := range strings.Split(sev, ",") {
			if strings.TrimSpace(item) == alert.Severity {
				hit = true
				break
			}
		}
		if !hit {
			return false
		}
	}

	if raw := strings.TrimSpace(route.MatchLabels); raw != "" && raw != "{}" {
		want := map[string]string{}
		if err := json.Unmarshal([]byte(raw), &want); err != nil {
			return false
		}
		for k, v := range want {
			if labels[k] != v {
				return false
			}
		}
	}
	return true
}

// sendToChannel 实际外发并落投递流水
func (h *Handler) sendToChannel(alert model.Alert, route *model.NotifyRoute, channel model.NotifyChannel) {
	record := model.NotifyRecord{
		AlertID: alert.ID, AlertTitle: truncate(alert.Title, 240),
		RouteID: route.ID, RouteName: route.Name,
		ChannelID: channel.ID, ChannelName: channel.Name,
		Status: "success",
	}

	start := time.Now()
	if channel.Type == "silent" {
		record.CostMs = time.Since(start).Milliseconds()
		_ = h.DB.Create(&record).Error
		return
	}

	status, err := postJSON(channel, alertMessage(alert))
	record.HTTPStatus = status
	record.CostMs = time.Since(start).Milliseconds()
	if err != nil {
		record.Status = "failed"
		record.ErrorMsg = truncate(err.Error(), 240)
	}
	_ = h.DB.Create(&record).Error
}

// alertMessage 外发报文，字段保持稳定以便接收端解析
func alertMessage(alert model.Alert) map[string]any {
	labels := map[string]string{}
	if alert.Labels != "" {
		_ = json.Unmarshal([]byte(alert.Labels), &labels)
	}
	return map[string]any{
		"alertId":     alert.ID,
		"title":       alert.Title,
		"summary":     alert.Summary,
		"severity":    alert.Severity,
		"status":      alert.Status,
		"value":       alert.Value,
		"source":      alert.SourceName,
		"labels":      labels,
		"count":       alert.Count,
		"firstSeenAt": alert.FirstSeenAt.Format(time.RFC3339),
		"lastSeenAt":  alert.LastSeenAt.Format(time.RFC3339),
	}
}

func postJSON(channel model.NotifyChannel, payload map[string]any) (int, error) {
	if channel.URL == "" {
		return 0, fmt.Errorf("渠道未配置 URL")
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return 0, err
	}

	req, err := http.NewRequest(http.MethodPost, channel.URL, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	if channel.HeaderKey != "" {
		req.Header.Set(channel.HeaderKey, channel.HeaderValue)
	}

	client := &http.Client{Timeout: notifyTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		return resp.StatusCode, fmt.Errorf("接收端返回 HTTP %d", resp.StatusCode)
	}
	return resp.StatusCode, nil
}

func parseIDList(raw string) []uint {
	ids := make([]uint, 0)
	if raw == "" {
		return ids
	}
	_ = json.Unmarshal([]byte(raw), &ids)
	return ids
}

// ---------- 通知渠道 ----------

type channelReq struct {
	Name        string `json:"name" binding:"required"`
	Type        string `json:"type"`
	URL         string `json:"url"`
	HeaderKey   string `json:"headerKey"`
	HeaderValue string `json:"headerValue"`
	Remark      string `json:"remark"`
	Enabled     *bool  `json:"enabled"`
}

func (h *Handler) ListNotifyChannels(c *gin.Context) {
	var list []model.NotifyChannel
	if err := h.DB.Order("id asc").Find(&list).Error; err != nil {
		response.Error(c, "查询通知渠道失败")
		return
	}
	response.OK(c, list)
}

func (h *Handler) CreateNotifyChannel(c *gin.Context) {
	var req channelReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "渠道名称不能为空")
		return
	}
	channelType := normalizeChannelType(req.Type)
	if channelType == "webhook" && req.URL == "" {
		response.BadRequest(c, "webhook 渠道必须填写 URL")
		return
	}

	channel := model.NotifyChannel{
		Name: req.Name, Type: channelType, URL: req.URL,
		HeaderKey: req.HeaderKey, HeaderValue: req.HeaderValue,
		Remark: req.Remark, Enabled: true,
	}
	if req.Enabled != nil {
		channel.Enabled = *req.Enabled
	}
	if err := h.DB.Create(&channel).Error; err != nil {
		response.Error(c, "渠道创建失败")
		return
	}
	response.OK(c, channel)
}

func (h *Handler) UpdateNotifyChannel(c *gin.Context) {
	var channel model.NotifyChannel
	if err := h.DB.First(&channel, idParam(c)).Error; err != nil {
		response.NotFound(c, "渠道不存在")
		return
	}
	var req channelReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "参数校验失败")
		return
	}

	channel.Name, channel.Type = req.Name, normalizeChannelType(req.Type)
	channel.URL, channel.HeaderKey, channel.Remark = req.URL, req.HeaderKey, req.Remark
	if req.HeaderValue != "" {
		channel.HeaderValue = req.HeaderValue // 留空表示不修改
	}
	if req.Enabled != nil {
		channel.Enabled = *req.Enabled
	}
	if err := h.DB.Save(&channel).Error; err != nil {
		response.Error(c, "渠道更新失败")
		return
	}
	response.OK(c, channel)
}

func (h *Handler) DeleteNotifyChannel(c *gin.Context) {
	id := idParam(c)
	var routes []model.NotifyRoute
	h.DB.Find(&routes)
	for _, route := range routes {
		for _, cid := range parseIDList(route.ChannelIDs) {
			if cid == id {
				response.BadRequest(c, fmt.Sprintf("渠道被路由「%s」引用，请先解除引用", route.Name))
				return
			}
		}
	}

	if err := h.DB.Delete(&model.NotifyChannel{}, id).Error; err != nil {
		response.Error(c, "渠道删除失败")
		return
	}
	response.OK(c, nil)
}

// TestNotifyChannel 用样例告警试发一次，验证地址与鉴权头
func (h *Handler) TestNotifyChannel(c *gin.Context) {
	var channel model.NotifyChannel
	if err := h.DB.First(&channel, idParam(c)).Error; err != nil {
		response.NotFound(c, "渠道不存在")
		return
	}
	if channel.Type == "silent" {
		response.OK(c, gin.H{"ok": true, "detail": "站内渠道无需外发"})
		return
	}

	sample := map[string]any{
		"alertId": 0, "title": "OpsOne 测试告警", "summary": "这是一条用于验证渠道连通性的样例消息",
		"severity": "info", "status": "firing", "source": "channel-test",
		"labels": map[string]string{"test": "true"},
	}

	start := time.Now()
	status, err := postJSON(channel, sample)
	if err != nil {
		response.OK(c, gin.H{
			"ok": false, "httpStatus": status,
			"detail": err.Error(), "costMs": time.Since(start).Milliseconds(),
		})
		return
	}
	response.OK(c, gin.H{
		"ok": true, "httpStatus": status, "costMs": time.Since(start).Milliseconds(),
	})
}

func normalizeChannelType(t string) string {
	if t == "silent" {
		return "silent"
	}
	return "webhook"
}

// ---------- 通知路由 ----------

type routeView struct {
	model.NotifyRoute
	ChannelIDs []uint `json:"channelIds"`
}

type routeReq struct {
	Name          string `json:"name" binding:"required"`
	Priority      int    `json:"priority"`
	MatchSeverity string `json:"matchSeverity"`
	MatchLabels   string `json:"matchLabels"`
	ChannelIDs    []uint `json:"channelIds"`
	IsDefault     *bool  `json:"isDefault"`
	Enabled       *bool  `json:"enabled"`
}

func (h *Handler) ListNotifyRoutes(c *gin.Context) {
	var list []model.NotifyRoute
	if err := h.DB.Order("priority asc, id asc").Find(&list).Error; err != nil {
		response.Error(c, "查询通知路由失败")
		return
	}
	views := make([]routeView, 0, len(list))
	for _, route := range list {
		views = append(views, routeView{NotifyRoute: route, ChannelIDs: parseIDList(route.ChannelIDs)})
	}
	response.OK(c, views)
}

func (h *Handler) CreateNotifyRoute(c *gin.Context) {
	var req routeReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "路由名称不能为空")
		return
	}
	if err := validateMatchLabels(req.MatchLabels); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	if len(req.ChannelIDs) == 0 {
		response.BadRequest(c, "请至少选择一个通知渠道")
		return
	}

	ids, _ := json.Marshal(req.ChannelIDs)
	route := model.NotifyRoute{
		Name: req.Name, Priority: normalizePriority(req.Priority),
		MatchSeverity: req.MatchSeverity, MatchLabels: req.MatchLabels,
		ChannelIDs: string(ids), Enabled: true,
	}
	if req.IsDefault != nil {
		route.IsDefault = *req.IsDefault
	}
	if req.Enabled != nil {
		route.Enabled = *req.Enabled
	}
	if err := h.DB.Create(&route).Error; err != nil {
		response.Error(c, "路由创建失败")
		return
	}
	h.ensureSingleDefault(&route)
	response.OK(c, routeView{NotifyRoute: route, ChannelIDs: req.ChannelIDs})
}

func (h *Handler) UpdateNotifyRoute(c *gin.Context) {
	var route model.NotifyRoute
	if err := h.DB.First(&route, idParam(c)).Error; err != nil {
		response.NotFound(c, "路由不存在")
		return
	}
	var req routeReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "参数校验失败")
		return
	}
	if err := validateMatchLabels(req.MatchLabels); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	if len(req.ChannelIDs) == 0 {
		response.BadRequest(c, "请至少选择一个通知渠道")
		return
	}

	ids, _ := json.Marshal(req.ChannelIDs)
	route.Name, route.Priority = req.Name, normalizePriority(req.Priority)
	route.MatchSeverity, route.MatchLabels = req.MatchSeverity, req.MatchLabels
	route.ChannelIDs = string(ids)
	if req.IsDefault != nil {
		route.IsDefault = *req.IsDefault
	}
	if req.Enabled != nil {
		route.Enabled = *req.Enabled
	}
	if err := h.DB.Save(&route).Error; err != nil {
		response.Error(c, "路由更新失败")
		return
	}
	h.ensureSingleDefault(&route)
	response.OK(c, routeView{NotifyRoute: route, ChannelIDs: req.ChannelIDs})
}

func (h *Handler) DeleteNotifyRoute(c *gin.Context) {
	if err := h.DB.Delete(&model.NotifyRoute{}, idParam(c)).Error; err != nil {
		response.Error(c, "路由删除失败")
		return
	}
	response.OK(c, nil)
}

// TestNotifyRoute 用一条样例告警做选路预演，不真正外发
func (h *Handler) TestNotifyRoute(c *gin.Context) {
	var req struct {
		Severity string            `json:"severity"`
		Labels   map[string]string `json:"labels"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "参数校验失败")
		return
	}

	sample := model.Alert{Severity: normalizeSeverity(req.Severity)}
	var routes []model.NotifyRoute
	h.DB.Where("enabled = ?", true).Order("priority asc, id asc").Find(&routes)

	for i := range routes {
		if routeMatches(routes[i], sample, req.Labels) {
			response.OK(c, gin.H{
				"matched": true, "routeId": routes[i].ID, "routeName": routes[i].Name,
				"channelIds": parseIDList(routes[i].ChannelIDs), "fallback": false,
			})
			return
		}
	}
	for i := range routes {
		if routes[i].IsDefault {
			response.OK(c, gin.H{
				"matched": true, "routeId": routes[i].ID, "routeName": routes[i].Name,
				"channelIds": parseIDList(routes[i].ChannelIDs), "fallback": true,
			})
			return
		}
	}
	response.OK(c, gin.H{"matched": false})
}

// ensureSingleDefault 兜底路由只允许一条
func (h *Handler) ensureSingleDefault(route *model.NotifyRoute) {
	if !route.IsDefault {
		return
	}
	h.DB.Model(&model.NotifyRoute{}).
		Where("id <> ? AND is_default = ?", route.ID, true).
		Update("is_default", false)
}

func validateMatchLabels(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	target := map[string]string{}
	if err := json.Unmarshal([]byte(raw), &target); err != nil {
		return fmt.Errorf("标签匹配条件必须是 JSON 对象，例如 {\"env\":\"prod\"}")
	}
	return nil
}

func normalizePriority(p int) int {
	if p <= 0 || p > 9999 {
		return 100
	}
	return p
}

// ---------- 通知记录 ----------

func (h *Handler) ListNotifyRecords(c *gin.Context) {
	page, size := pageParams(c)
	q := h.DB.Model(&model.NotifyRecord{})
	if status := c.Query("status"); status != "" {
		q = q.Where("status = ?", status)
	}
	if channelID := c.Query("channelId"); channelID != "" {
		q = q.Where("channel_id = ?", channelID)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		response.Error(c, "查询通知记录失败")
		return
	}
	var list []model.NotifyRecord
	if err := q.Order("id desc").Offset((page - 1) * size).Limit(size).Find(&list).Error; err != nil {
		response.Error(c, "查询通知记录失败")
		return
	}
	response.OKPage(c, list, total, page, size)
}
