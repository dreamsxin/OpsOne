package handler

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"ops-platform/server/internal/middleware"
	"ops-platform/server/internal/model"
	"ops-platform/server/internal/response"
)

// 告警静默 / 维护窗口。
//
// 只拦外发通知，不拦入库：维护窗口里该看见的还是要看见，只是不半夜叫人。
// 被拦下的告警会记上是哪条静默拦的（alerts.silenced_by / silence_id），
// 「为什么没收到通知」在告警详情里能直接回答。
//
// 判定发生在两处：
//  1. ingestAlert 产生新告警时 —— 拦住渠道外发与站内严重告警消息；
//  2. 值班升级扫描时 —— 每次都按当下时间重新判定，窗口结束后自动恢复叫人。

const (
	silenceKindSilence     = "silence"
	silenceKindMaintenance = "maintenance"
)

// ---------- 匹配 ----------

// silenceMatches 判断一条告警是否落在这条静默里。
// 条件之间是「与」：填了的都必须命中，没填的不限。
func silenceMatches(s model.AlertSilence, alert model.Alert, at time.Time) bool {
	if !s.Enabled {
		return false
	}
	if s.EndedAt != nil && !at.Before(*s.EndedAt) {
		return false // 已被提前结束
	}
	if at.Before(s.StartAt) || !at.Before(s.EndAt) {
		return false // 不在窗口内（右开区间：EndAt 到点即失效）
	}
	if s.MatchAll {
		return true
	}

	if sev := strings.TrimSpace(s.MatchSeverity); sev != "" {
		if !csvContains(sev, alert.Severity) {
			return false
		}
	}
	if src := strings.TrimSpace(s.MatchSource); src != "" {
		if !csvContains(src, alert.SourceName) {
			return false
		}
	}
	if kw := strings.TrimSpace(s.MatchTitle); kw != "" {
		if !strings.Contains(alert.Title, kw) {
			return false
		}
	}
	if raw := strings.TrimSpace(s.MatchLabels); raw != "" && raw != "{}" {
		want := map[string]string{}
		if err := json.Unmarshal([]byte(raw), &want); err != nil {
			return false // 条件本身坏了就别乱拦，宁可把通知发出去
		}
		labels := map[string]string{}
		if alert.Labels != "" {
			_ = json.Unmarshal([]byte(alert.Labels), &labels)
		}
		for k, v := range want {
			if labels[k] != v {
				return false
			}
		}
	}
	return true
}

// csvContains 逗号分隔列表里是否有这个值
func csvContains(csv, value string) bool {
	if value == "" {
		return false
	}
	for _, item := range strings.Split(csv, ",") {
		if strings.TrimSpace(item) == value {
			return true
		}
	}
	return false
}

// matchSilence 找出当下拦住这条告警的静默（按 ID 升序取第一条命中的）。
// 只读，不改计数，供值班升级这类「每次都要重新判定」的地方使用。
func (h *Handler) matchSilence(alert model.Alert, at time.Time) (*model.AlertSilence, bool) {
	var list []model.AlertSilence
	// 时间窗与提前结束都在 Go 侧判，SQL 只做粗筛（enabled + 窗口未结束）
	err := h.DB.Where("enabled = ? AND end_at > ?", true, at).Order("id asc").Find(&list).Error
	if err != nil {
		log.Printf("[silence] 加载静默失败: %v", err)
		return nil, false
	}
	for i := range list {
		if silenceMatches(list[i], alert, at) {
			return &list[i], true
		}
	}
	return nil, false
}

// silencedForAlert 新告警入库后判定是否静默；命中则记账并在告警上留痕。
// 返回是否被静默。
func (h *Handler) silencedForAlert(alert *model.Alert) bool {
	now := time.Now()
	hit, ok := h.matchSilence(*alert, now)
	if !ok {
		return false
	}

	alert.SilencedBy = hit.Name
	alert.SilenceID = hit.ID
	if err := h.DB.Model(alert).Updates(map[string]any{
		"silenced_by": hit.Name, "silence_id": hit.ID,
	}).Error; err != nil {
		log.Printf("[silence] 标记告警 %d 失败: %v", alert.ID, err)
	}
	// 命中计数用表达式自增，避免并发下相互覆盖
	if err := h.DB.Model(&model.AlertSilence{}).Where("id = ?", hit.ID).Updates(map[string]any{
		"hit_count":   gorm.Expr("hit_count + 1"),
		"last_hit_at": &now,
	}).Error; err != nil {
		log.Printf("[silence] 更新静默 %d 命中计数失败: %v", hit.ID, err)
	}
	log.Printf("[silence] 告警 %d(%s) 命中「%s」，只入库不外发", alert.ID, alert.Title, hit.Name)
	return true
}

// ---------- 状态 ----------

// silenceStatus 计算展示用状态：生效中 / 未开始 / 已过期 / 已提前结束 / 已停用
func silenceStatus(s model.AlertSilence, at time.Time) string {
	if !s.Enabled {
		return "disabled"
	}
	if s.EndedAt != nil && !at.Before(*s.EndedAt) {
		return "ended"
	}
	if at.Before(s.StartAt) {
		return "pending"
	}
	if !at.Before(s.EndAt) {
		return "expired"
	}
	return "active"
}

type silenceView struct {
	model.AlertSilence
	Status string `json:"status"`
	// RemainSeconds 生效中时距结束还有多少秒，其它状态为 0
	RemainSeconds int64 `json:"remainSeconds"`
}

func toSilenceView(s model.AlertSilence, at time.Time) silenceView {
	v := silenceView{AlertSilence: s, Status: silenceStatus(s, at)}
	if v.Status == "active" {
		v.RemainSeconds = int64(s.EndAt.Sub(at).Seconds())
	}
	return v
}

// ---------- 请求体 ----------

type silenceReq struct {
	Name          string `json:"name"`
	Kind          string `json:"kind"`
	MatchSeverity string `json:"matchSeverity"`
	MatchLabels   string `json:"matchLabels"`
	MatchTitle    string `json:"matchTitle"`
	MatchSource   string `json:"matchSource"`
	MatchAll      bool   `json:"matchAll"`
	StartAt       string `json:"startAt"`
	EndAt         string `json:"endAt"`
	Reason        string `json:"reason"`
	Enabled       *bool  `json:"enabled"`
}

type silenceParsed struct {
	silenceReq
	start time.Time
	end   time.Time
}

// normalize 校验并归一化。刻意严格：静默配错了是「该响的没响」，比多响几次危险。
func (r *silenceReq) normalize() (*silenceParsed, error) {
	out := &silenceParsed{silenceReq: *r}
	out.Name = strings.TrimSpace(r.Name)
	if out.Name == "" {
		return nil, fmt.Errorf("名称必填")
	}
	if len(out.Name) > 64 {
		return nil, fmt.Errorf("名称最长 64 个字符")
	}
	out.Kind = strings.TrimSpace(r.Kind)
	if out.Kind != silenceKindMaintenance {
		out.Kind = silenceKindSilence
	}

	out.MatchSeverity = normalizeSeverityList(r.MatchSeverity)
	out.MatchSource = strings.TrimSpace(r.MatchSource)
	out.MatchTitle = strings.TrimSpace(r.MatchTitle)
	out.MatchLabels = strings.TrimSpace(r.MatchLabels)
	if out.MatchLabels != "" && out.MatchLabels != "{}" {
		probe := map[string]string{}
		if err := json.Unmarshal([]byte(out.MatchLabels), &probe); err != nil {
			return nil, fmt.Errorf("标签条件必须是 {\"key\":\"value\"} 形式的 JSON 对象")
		}
		if len(probe) == 0 {
			out.MatchLabels = ""
		}
	}

	hasCond := out.MatchSeverity != "" || out.MatchSource != "" || out.MatchTitle != "" ||
		(out.MatchLabels != "" && out.MatchLabels != "{}")
	if !hasCond && !out.MatchAll {
		return nil, fmt.Errorf("至少要填一个匹配条件；确实要静默全部告警请显式勾选「匹配全部告警」")
	}

	start, err := parseSilenceTime(r.StartAt)
	if err != nil {
		return nil, fmt.Errorf("开始时间无效: %w", err)
	}
	end, err := parseSilenceTime(r.EndAt)
	if err != nil {
		return nil, fmt.Errorf("结束时间无效: %w", err)
	}
	if !end.After(start) {
		return nil, fmt.Errorf("结束时间必须晚于开始时间")
	}
	if end.Sub(start) > 30*24*time.Hour {
		return nil, fmt.Errorf("单条静默最长 30 天，长期屏蔽请改告警规则而不是一直静默")
	}
	out.start, out.end = start, end
	return out, nil
}

// parseSilenceTime 接受 RFC3339 或 "2006-01-02 15:04:05"（按服务器本地时区）
func parseSilenceTime(raw string) (time.Time, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return time.Time{}, fmt.Errorf("不能为空")
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	if t, err := time.ParseInLocation("2006-01-02 15:04:05", s, time.Local); err == nil {
		return t, nil
	}
	if t, err := time.ParseInLocation("2006-01-02 15:04", s, time.Local); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("格式应为 2006-01-02 15:04:05 或 RFC3339")
}

// normalizeSeverityList 过滤非法级别，保留原顺序
func normalizeSeverityList(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	allow := map[string]bool{"critical": true, "warning": true, "info": true}
	out := make([]string, 0, 3)
	for _, item := range strings.Split(raw, ",") {
		s := strings.TrimSpace(item)
		if allow[s] {
			out = append(out, s)
		}
	}
	return strings.Join(out, ",")
}

// ---------- 接口 ----------

// ListAlertSilences 列表，支持按状态与类型筛选
func (h *Handler) ListAlertSilences(c *gin.Context) {
	var list []model.AlertSilence
	q := h.DB.Model(&model.AlertSilence{})
	if kind := strings.TrimSpace(c.Query("kind")); kind != "" {
		q = q.Where("kind = ?", kind)
	}
	if err := q.Order("id desc").Find(&list).Error; err != nil {
		response.Error(c, "查询静默失败")
		return
	}

	now := time.Now()
	wantStatus := strings.TrimSpace(c.Query("status"))
	views := make([]silenceView, 0, len(list))
	summary := map[string]int{"active": 0, "pending": 0, "expired": 0, "ended": 0, "disabled": 0}
	for _, item := range list {
		v := toSilenceView(item, now)
		summary[v.Status]++
		if wantStatus != "" && v.Status != wantStatus {
			continue
		}
		views = append(views, v)
	}
	response.OK(c, gin.H{"list": views, "total": len(views), "summary": summary})
}

// CreateAlertSilence 新建
func (h *Handler) CreateAlertSilence(c *gin.Context) {
	var req silenceReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "参数错误")
		return
	}
	parsed, err := req.normalize()
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	item := model.AlertSilence{
		Name: parsed.Name, Kind: parsed.Kind,
		MatchSeverity: parsed.MatchSeverity, MatchLabels: parsed.MatchLabels,
		MatchTitle: parsed.MatchTitle, MatchSource: parsed.MatchSource,
		MatchAll: parsed.MatchAll,
		StartAt:  parsed.start, EndAt: parsed.end,
		Reason: parsed.Reason,
	}
	// 不传 enabled 时默认启用。模型上没有 gorm default，这一行就是最终结果
	wantEnabled := parsed.Enabled == nil || *parsed.Enabled
	item.Enabled = wantEnabled
	if operator := middleware.CurrentUser(c); operator != nil {
		item.CreatedBy = operator.ID
		item.CreatorName = operator.Username
	}
	if err := h.DB.Create(&item).Error; err != nil {
		response.Error(c, "保存静默失败")
		return
	}
	if !wantEnabled {
		h.DB.Model(&model.AlertSilence{}).Where("id = ?", item.ID).Update("enabled", false)
		item.Enabled = false
	}
	response.OK(c, toSilenceView(item, time.Now()))
}

// UpdateAlertSilence 编辑
func (h *Handler) UpdateAlertSilence(c *gin.Context) {
	var item model.AlertSilence
	if err := h.DB.First(&item, c.Param("id")).Error; err != nil {
		response.NotFound(c, "静默不存在")
		return
	}
	var req silenceReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "参数错误")
		return
	}
	parsed, err := req.normalize()
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	updates := map[string]any{
		"name": parsed.Name, "kind": parsed.Kind,
		"match_severity": parsed.MatchSeverity, "match_labels": parsed.MatchLabels,
		"match_title": parsed.MatchTitle, "match_source": parsed.MatchSource,
		"match_all": parsed.MatchAll,
		"start_at":  parsed.start, "end_at": parsed.end,
		"reason": parsed.Reason,
	}
	if parsed.Enabled != nil {
		updates["enabled"] = *parsed.Enabled
	}
	// 重新编辑视为重新启用：把提前结束的痕迹清掉，否则改完还是不生效
	updates["ended_at"] = nil
	updates["ended_by"] = ""
	if err := h.DB.Model(&item).Updates(updates).Error; err != nil {
		response.Error(c, "更新静默失败")
		return
	}
	h.DB.First(&item, item.ID)
	response.OK(c, toSilenceView(item, time.Now()))
}

// EndAlertSilence 提前结束（维护提前做完时用）
func (h *Handler) EndAlertSilence(c *gin.Context) {
	var item model.AlertSilence
	if err := h.DB.First(&item, c.Param("id")).Error; err != nil {
		response.NotFound(c, "静默不存在")
		return
	}
	now := time.Now()
	if item.EndedAt != nil {
		response.BadRequest(c, "这条静默已经结束过了")
		return
	}
	who := ""
	if operator := middleware.CurrentUser(c); operator != nil {
		who = operator.Username
	}
	if err := h.DB.Model(&item).Updates(map[string]any{"ended_at": &now, "ended_by": who}).Error; err != nil {
		response.Error(c, "结束静默失败")
		return
	}
	h.DB.First(&item, item.ID)
	response.OK(c, toSilenceView(item, now))
}

// DeleteAlertSilence 删除。已经拦过告警的不允许删，否则告警详情里的「被谁拦的」就查不到了
func (h *Handler) DeleteAlertSilence(c *gin.Context) {
	var item model.AlertSilence
	if err := h.DB.First(&item, c.Param("id")).Error; err != nil {
		response.NotFound(c, "静默不存在")
		return
	}
	if item.HitCount > 0 {
		response.BadRequest(c, "这条静默已经拦下过告警，删掉会让那些告警查不到原因；请改用「提前结束」或停用")
		return
	}
	if err := h.DB.Delete(&item).Error; err != nil {
		response.Error(c, "删除静默失败")
		return
	}
	response.OK(c, gin.H{"id": item.ID})
}

// ListAlertSilenceHits 这条静默拦下过哪些告警
func (h *Handler) ListAlertSilenceHits(c *gin.Context) {
	var item model.AlertSilence
	if err := h.DB.First(&item, c.Param("id")).Error; err != nil {
		response.NotFound(c, "静默不存在")
		return
	}
	var alerts []model.Alert
	if err := h.DB.Where("silence_id = ?", item.ID).
		Order("id desc").Limit(200).Find(&alerts).Error; err != nil {
		response.Error(c, "查询命中告警失败")
		return
	}
	response.OK(c, gin.H{
		"silence": toSilenceView(item, time.Now()),
		"list":    alerts,
		"total":   len(alerts),
	})
}

// PreviewAlertSilence 试算：按当前条件看看最近的活跃告警里会拦掉哪些。
// 目的是让人在按下保存之前知道自己写的条件到底覆盖多大范围。
func (h *Handler) PreviewAlertSilence(c *gin.Context) {
	var req silenceReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "参数错误")
		return
	}
	parsed, err := req.normalize()
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	var alerts []model.Alert
	if err := h.DB.Where("status <> ?", "resolved").
		Order("id desc").Limit(500).Find(&alerts).Error; err != nil {
		response.Error(c, "查询告警失败")
		return
	}

	// 试算只看匹配条件，不看时间窗（否则未来的窗口永远试算不出东西）
	probe := model.AlertSilence{
		Enabled: true, MatchSeverity: parsed.MatchSeverity, MatchLabels: parsed.MatchLabels,
		MatchTitle: parsed.MatchTitle, MatchSource: parsed.MatchSource, MatchAll: parsed.MatchAll,
		StartAt: time.Now().Add(-time.Second), EndAt: time.Now().Add(time.Second),
	}
	now := time.Now()
	samples := make([]gin.H, 0, 20)
	matched := 0
	for _, a := range alerts {
		if !silenceMatches(probe, a, now) {
			continue
		}
		matched++
		if len(samples) < 20 {
			samples = append(samples, gin.H{
				"id": a.ID, "title": a.Title, "severity": a.Severity,
				"sourceName": a.SourceName, "labels": a.Labels, "lastSeenAt": a.LastSeenAt,
			})
		}
	}
	response.OK(c, gin.H{
		"scanned": len(alerts), "matched": matched, "samples": samples,
		"window": gin.H{"startAt": parsed.start, "endAt": parsed.end},
	})
}
