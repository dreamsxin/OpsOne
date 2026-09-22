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

// openChannel 返回一份「签名密钥与鉴权头已解密」的渠道副本。
//
// 副本而不是原地改：这个结构体后面还会被当成展示对象用，原地换成明文
// 就有可能顺着某条路径被写回库里。
func (h *Handler) openChannel(channel model.NotifyChannel) (model.NotifyChannel, error) {
	secret, err := h.openSecret("通知渠道签名密钥", channel.Secret)
	if err != nil {
		return channel, err
	}
	header, err := h.openSecret("通知渠道鉴权头", channel.HeaderValue)
	if err != nil {
		return channel, err
	}
	channel.Secret, channel.HeaderValue = secret, header
	return channel, nil
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

	// 密钥解不开就落一条失败流水：这条链路是后台异步跑的，没人在等返回值，
	// 只有投递记录能让人看见「告警没发出去，原因是密钥不可用」
	opened, err := h.openChannel(channel)
	if err != nil {
		record.Status = "failed"
		record.ErrorMsg = truncate(err.Error(), 240)
		record.CostMs = time.Since(start).Milliseconds()
		_ = h.DB.Create(&record).Error
		return
	}
	channel = opened

	if channel.Type == "email" {
		if err := h.sendMail(channel, alertMailVars(alert)); err != nil {
			record.Status = "failed"
			record.ErrorMsg = truncate(err.Error(), 240)
		}
		record.CostMs = time.Since(start).Milliseconds()
		_ = h.DB.Create(&record).Error
		return
	}

	if isIMChannel(channel.Type) {
		// 有配模板就按模板发，没有就按原来的硬编码格式 —— 模板是可选的覆盖
		status, err := h.postIM(channel, h.renderIMBody(channel, alert))
		record.HTTPStatus = status
		record.CostMs = time.Since(start).Milliseconds()
		if err != nil {
			record.Status = "failed"
			record.ErrorMsg = truncate(err.Error(), 240)
		}
		_ = h.DB.Create(&record).Error
		return
	}

	payload, warn := h.renderWebhookBody(channel, alert)
	status, err := postJSON(channel, payload)
	record.HTTPStatus = status
	record.CostMs = time.Since(start).Milliseconds()
	if err != nil {
		record.Status = "failed"
		record.ErrorMsg = truncate(err.Error(), 240)
	} else if warn != "" {
		// 发出去了，但模板没生效。这种情况必须在流水里说清楚：
		// 否则使用者只会发现「模板像是没起作用」，查不到原因
		record.ErrorMsg = truncate(warn, 240)
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
	Name         string `json:"name" binding:"required"`
	Type         string `json:"type"`
	URL          string `json:"url"`
	HeaderKey    string `json:"headerKey"`
	HeaderValue  string `json:"headerValue"`
	Recipients   string `json:"recipients"`
	TemplateCode string `json:"templateCode"`
	Secret       string `json:"secret"`
	MentionList  string `json:"mentionList"`
	MentionAll   *bool  `json:"mentionAll"`
	Remark       string `json:"remark"`
	Enabled      *bool  `json:"enabled"`
}

// validateChannel 类型相关的必填校验。
// 抽出来是因为原先只有新建时校验，编辑时可以把 webhook 的 URL 清空存进去。
func validateChannel(channelType string, req channelReq) error {
	switch channelType {
	case "webhook":
		if strings.TrimSpace(req.URL) == "" {
			return fmt.Errorf("webhook 渠道必须填写 URL")
		}
	case "email":
		if strings.TrimSpace(req.Recipients) == "" {
			return fmt.Errorf("email 渠道必须填写收件人")
		}
	case channelWecom, channelDingTalk, channelFeishu:
		if strings.TrimSpace(req.URL) == "" {
			return fmt.Errorf("%s 渠道必须填写机器人 Webhook 地址", imChannelLabel(channelType))
		}
		if channelType == channelFeishu && strings.TrimSpace(req.MentionList) != "" {
			return fmt.Errorf("飞书只支持 @所有人，请清空 @ 名单后勾选「@所有人」")
		}
	case "silent":
		// 只落记录，什么都不用填
	default:
		return fmt.Errorf("不支持的渠道类型 %q", channelType)
	}
	return nil
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
	if err := validateChannel(channelType, req); err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	channel := model.NotifyChannel{
		Name: req.Name, Type: channelType, URL: req.URL,
		HeaderKey: req.HeaderKey, HeaderValue: h.sealSecret(req.HeaderValue),
		Recipients: req.Recipients, TemplateCode: req.TemplateCode,
		Secret: h.sealSecret(req.Secret), MentionList: req.MentionList,
		Remark: req.Remark, Enabled: true,
	}
	if req.MentionAll != nil {
		channel.MentionAll = *req.MentionAll
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

	channelType := normalizeChannelType(req.Type)
	if err := validateChannel(channelType, req); err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	channel.Name, channel.Type = req.Name, channelType
	channel.URL, channel.HeaderKey, channel.Remark = req.URL, req.HeaderKey, req.Remark
	channel.Recipients, channel.TemplateCode = req.Recipients, req.TemplateCode
	channel.MentionList = req.MentionList
	if req.HeaderValue != "" {
		channel.HeaderValue = h.sealSecret(req.HeaderValue) // 留空表示不修改
	}
	if req.Secret != "" {
		channel.Secret = h.sealSecret(req.Secret) // 同上：签名密钥留空表示沿用旧值
	}
	if req.MentionAll != nil {
		channel.MentionAll = *req.MentionAll
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
	opened, err := h.openChannel(channel)
	if err != nil {
		response.OK(c, gin.H{"ok": false, "detail": err.Error()})
		return
	}
	channel = opened

	if channel.Type == "email" {
		start := time.Now()
		vars := sampleAlertVars()
		vars["title"] = "OpsOne 测试邮件"
		vars["summary"] = "这是一封用于验证 SMTP 配置与模板渲染的测试邮件"
		if err := h.sendMail(channel, vars); err != nil {
			response.OK(c, gin.H{
				"ok": false, "detail": err.Error(), "costMs": time.Since(start).Milliseconds(),
			})
			return
		}
		response.OK(c, gin.H{
			"ok": true, "detail": "已投递到 SMTP 服务器", "costMs": time.Since(start).Milliseconds(),
		})
		return
	}

	if isIMChannel(channel.Type) {
		start := time.Now()
		sample := model.Alert{
			Title: "OpsOne 测试告警", Summary: "这是一条用于验证群机器人连通性的样例消息",
			Severity: "info", Status: "firing", SourceName: "channel-test",
			Labels: `{"test":"true"}`, LastSeenAt: time.Now(),
		}
		status, err := h.postIM(channel, imAlertText(sample))
		if err != nil {
			response.OK(c, gin.H{
				"ok": false, "httpStatus": status,
				"detail": err.Error(), "costMs": time.Since(start).Milliseconds(),
			})
			return
		}
		response.OK(c, gin.H{
			"ok": true, "httpStatus": status, "detail": "群里应当已经收到消息",
			"costMs": time.Since(start).Milliseconds(),
		})
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

// normalizeChannelType 已知类型原样返回，其余交给 validateChannel 拒绝。
//
// 原先这里把未知类型静默改成 webhook——类型多了之后这是个坑：
// 把 dingtalk 写错成 dingding 会变成 webhook，报文发过去对端根本不认。
func normalizeChannelType(t string) string {
	return strings.TrimSpace(t)
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
