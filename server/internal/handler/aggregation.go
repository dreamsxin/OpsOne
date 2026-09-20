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

// 归桶维度取值
const (
	dimSource   = "source"
	dimSeverity = "severity"
	dimTitle    = "title"
	dimLabel    = "label:" // label:<键名>
)

// bucketKeyOf 按策略维度算出告警所属的桶。
// 缺失的标签用 <无> 占位，保证「缺这个标签」的告警自己归一类，而不是和别人混在一起。
func bucketKeyOf(alert model.Alert, dimensions []string) (string, map[string]string) {
	labels := map[string]string{}
	if alert.Labels != "" {
		_ = json.Unmarshal([]byte(alert.Labels), &labels)
	}

	parts := make([]string, 0, len(dimensions))
	detail := make(map[string]string, len(dimensions))
	for _, dim := range dimensions {
		var value string
		switch {
		case dim == dimSource:
			value = alert.SourceName
		case dim == dimSeverity:
			value = alert.Severity
		case dim == dimTitle:
			value = alert.Title
		case strings.HasPrefix(dim, dimLabel):
			key := strings.TrimPrefix(dim, dimLabel)
			value = labels[key]
		}
		if value == "" {
			value = "<无>"
		}
		parts = append(parts, dim+"="+value)
		detail[dim] = value
	}
	return strings.Join(parts, " | "), detail
}

func parseDimensions(raw string) []string {
	items := make([]string, 0, 4)
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			items = append(items, part)
		}
	}
	return items
}

func validDimension(dim string) bool {
	switch dim {
	case dimSource, dimSeverity, dimTitle:
		return true
	}
	return strings.HasPrefix(dim, dimLabel) && len(dim) > len(dimLabel)
}

// policyMatches 判断告警是否落在策略的处理范围内
func policyMatches(policy model.AggregationPolicy, alert model.Alert) bool {
	if policy.MatchSeverity == "" {
		return true
	}
	for _, item := range strings.Split(policy.MatchSeverity, ",") {
		if strings.TrimSpace(item) == alert.Severity {
			return true
		}
	}
	return false
}

// ---------- 归桶 ----------

type bucketView struct {
	Key         string            `json:"key"`
	Dimensions  map[string]string `json:"dimensions"`
	AlertCount  int               `json:"alertCount"` // 桶内告警条数
	TotalCount  int               `json:"totalCount"` // 桶内告警的累计出现次数
	Suppressed  int               `json:"suppressed"` // 其中因聚合被抑制通知的条数
	Severities  map[string]int    `json:"severities"`
	SampleIDs   []uint            `json:"sampleIds"`
	SampleTitle string            `json:"sampleTitle"`
	FirstSeenAt time.Time         `json:"firstSeenAt"`
	LastSeenAt  time.Time         `json:"lastSeenAt"`
	Grouped     bool              `json:"grouped"` // 是否达到 MinCount 成桶
}

// aggregateAlerts 按策略把告警归桶。只看窗口内、未恢复的告警。
func (h *Handler) aggregateAlerts(policy model.AggregationPolicy) ([]bucketView, int, error) {
	dimensions := parseDimensions(policy.Dimensions)
	if len(dimensions) == 0 {
		return nil, 0, fmt.Errorf("策略没有配置归桶维度")
	}

	since := time.Now().Add(-time.Duration(policy.WindowMinutes) * time.Minute)
	var alerts []model.Alert
	err := h.DB.Where("status <> ? AND last_seen_at >= ?", "resolved", since).
		Order("id desc").Find(&alerts).Error
	if err != nil {
		return nil, 0, err
	}

	buckets := map[string]*bucketView{}
	matched := 0
	for _, alert := range alerts {
		if !policyMatches(policy, alert) {
			continue
		}
		matched++

		key, detail := bucketKeyOf(alert, dimensions)
		bucket, ok := buckets[key]
		if !ok {
			bucket = &bucketView{
				Key: key, Dimensions: detail, Severities: map[string]int{},
				SampleTitle: alert.Title, FirstSeenAt: alert.FirstSeenAt, LastSeenAt: alert.LastSeenAt,
			}
			buckets[key] = bucket
		}
		bucket.AlertCount++
		bucket.TotalCount += alert.Count
		bucket.Severities[alert.Severity]++
		if alert.SuppressedBy != "" {
			bucket.Suppressed++
		}
		if len(bucket.SampleIDs) < 5 {
			bucket.SampleIDs = append(bucket.SampleIDs, alert.ID)
		}
		if alert.FirstSeenAt.Before(bucket.FirstSeenAt) {
			bucket.FirstSeenAt = alert.FirstSeenAt
		}
		if alert.LastSeenAt.After(bucket.LastSeenAt) {
			bucket.LastSeenAt = alert.LastSeenAt
		}
	}

	list := make([]bucketView, 0, len(buckets))
	for _, bucket := range buckets {
		bucket.Grouped = bucket.AlertCount >= policy.MinCount
		list = append(list, *bucket)
	}
	// 桶大的排前面，方便先看噪音最多的那一类
	sort.Slice(list, func(i, j int) bool {
		if list[i].AlertCount != list[j].AlertCount {
			return list[i].AlertCount > list[j].AlertCount
		}
		return list[i].Key < list[j].Key
	})
	return list, matched, nil
}

// ---------- 通知抑制 ----------

// suppressedByAggregation 判断这条新告警是否该被聚合策略抑制通知。
//
// 规则：按 Priority 找到第一条命中的、开了抑制的策略；如果同一个桶里在窗口内
// 已经有别的告警（说明首条已经通知过），这条就只入库不通知。
// 返回策略名与是否抑制。
func (h *Handler) suppressedByAggregation(alert model.Alert) (string, bool) {
	var policies []model.AggregationPolicy
	err := h.DB.Where("enabled = ? AND suppress_notify = ?", true, true).
		Order("priority asc, id asc").Find(&policies).Error
	if err != nil || len(policies) == 0 {
		return "", false
	}

	for _, policy := range policies {
		if !policyMatches(policy, alert) {
			continue
		}
		dimensions := parseDimensions(policy.Dimensions)
		if len(dimensions) == 0 {
			continue
		}

		key, _ := bucketKeyOf(alert, dimensions)
		since := time.Now().Add(-time.Duration(policy.WindowMinutes) * time.Minute)

		var peers []model.Alert
		if err := h.DB.Where("id <> ? AND status <> ? AND last_seen_at >= ?",
			alert.ID, "resolved", since).Find(&peers).Error; err != nil {
			return "", false
		}
		for _, peer := range peers {
			peerKey, _ := bucketKeyOf(peer, dimensions)
			// 同桶里已有别的告警，且那条不是被抑制的（说明通知已经发过）
			if peerKey == key && peer.SuppressedBy == "" {
				return policy.Name, true
			}
		}
		// 第一条命中的策略决定结果，不再往后看
		return "", false
	}
	return "", false
}

// ---------- 接口 ----------

type aggregationReq struct {
	Name           string `json:"name" binding:"required"`
	Dimensions     string `json:"dimensions" binding:"required"`
	MatchSeverity  string `json:"matchSeverity"`
	WindowMinutes  int    `json:"windowMinutes"`
	MinCount       int    `json:"minCount"`
	SuppressNotify *bool  `json:"suppressNotify"`
	Priority       int    `json:"priority"`
	Enabled        *bool  `json:"enabled"`
	Remark         string `json:"remark"`
}

func (req *aggregationReq) normalize() error {
	dims := parseDimensions(req.Dimensions)
	if len(dims) == 0 {
		return fmt.Errorf("请至少选择一个归桶维度")
	}
	if len(dims) > 5 {
		return fmt.Errorf("归桶维度最多 5 个，再多桶会碎成单条")
	}
	for _, dim := range dims {
		if !validDimension(dim) {
			return fmt.Errorf("不支持的维度: %s（可用 source / severity / title / label:键名）", dim)
		}
	}
	req.Dimensions = strings.Join(dims, ",")

	for _, item := range strings.Split(req.MatchSeverity, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if item != "critical" && item != "warning" && item != "info" {
			return fmt.Errorf("级别只能是 critical / warning / info")
		}
	}

	if req.WindowMinutes <= 0 {
		req.WindowMinutes = 60
	}
	if req.WindowMinutes > 7*24*60 {
		return fmt.Errorf("窗口最长 7 天")
	}
	if req.MinCount <= 0 {
		req.MinCount = 2
	}
	if req.Priority <= 0 {
		req.Priority = 100
	}
	return nil
}

func (h *Handler) ListAggregationPolicies(c *gin.Context) {
	var list []model.AggregationPolicy
	if err := h.DB.Order("priority asc, id asc").Find(&list).Error; err != nil {
		response.Error(c, "查询聚合策略失败")
		return
	}
	response.OK(c, list)
}

// ListAggregationDimensions 可用维度清单：固定维度 + 现有告警里出现过的标签键
func (h *Handler) ListAggregationDimensions(c *gin.Context) {
	items := []gin.H{
		{"key": dimSource, "label": "接入源"},
		{"key": dimSeverity, "label": "告警级别"},
		{"key": dimTitle, "label": "告警标题"},
	}

	var alerts []model.Alert
	h.DB.Select("labels").Where("labels <> ''").Limit(500).Find(&alerts)
	seen := map[string]int{}
	for _, alert := range alerts {
		labels := map[string]string{}
		if json.Unmarshal([]byte(alert.Labels), &labels) != nil {
			continue
		}
		for key := range labels {
			seen[key]++
		}
	}
	keys := make([]string, 0, len(seen))
	for key := range seen {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		items = append(items, gin.H{
			"key":   dimLabel + key,
			"label": "标签 " + key,
			"count": seen[key],
		})
	}
	response.OK(c, items)
}

func (h *Handler) CreateAggregationPolicy(c *gin.Context) {
	var req aggregationReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "名称与归桶维度为必填项")
		return
	}
	if err := req.normalize(); err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	item := model.AggregationPolicy{
		Name: req.Name, Dimensions: req.Dimensions, MatchSeverity: req.MatchSeverity,
		WindowMinutes: req.WindowMinutes, MinCount: req.MinCount,
		Priority: req.Priority, Enabled: true, Remark: req.Remark,
		CreatedBy: middleware.CurrentUser(c).ID,
	}
	if req.SuppressNotify != nil {
		item.SuppressNotify = *req.SuppressNotify
	}
	if req.Enabled != nil {
		item.Enabled = *req.Enabled
	}

	// 同 Probe/Certificate：带 default 的字段在 Create 后会被回填成库默认值
	wantSuppress, wantEnabled := item.SuppressNotify, item.Enabled
	if err := h.DB.Create(&item).Error; err != nil {
		response.Error(c, "创建失败")
		return
	}
	item.SuppressNotify, item.Enabled = wantSuppress, wantEnabled
	h.DB.Model(&model.AggregationPolicy{}).Where("id = ?", item.ID).Updates(map[string]any{
		"suppress_notify": wantSuppress, "enabled": wantEnabled,
	})
	response.OK(c, item)
}

func (h *Handler) UpdateAggregationPolicy(c *gin.Context) {
	var item model.AggregationPolicy
	if err := h.DB.First(&item, idParam(c)).Error; err != nil {
		response.NotFound(c, "策略不存在")
		return
	}

	var req aggregationReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "参数校验失败")
		return
	}
	if err := req.normalize(); err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	item.Name, item.Dimensions, item.MatchSeverity = req.Name, req.Dimensions, req.MatchSeverity
	item.WindowMinutes, item.MinCount = req.WindowMinutes, req.MinCount
	item.Priority, item.Remark = req.Priority, req.Remark
	if req.SuppressNotify != nil {
		item.SuppressNotify = *req.SuppressNotify
	}
	if req.Enabled != nil {
		item.Enabled = *req.Enabled
	}
	if err := h.DB.Save(&item).Error; err != nil {
		response.Error(c, "更新失败")
		return
	}
	// Save 对零值字段同样会被 default 影响，这两个开关显式补写
	h.DB.Model(&model.AggregationPolicy{}).Where("id = ?", item.ID).Updates(map[string]any{
		"suppress_notify": item.SuppressNotify, "enabled": item.Enabled,
	})
	response.OK(c, item)
}

func (h *Handler) DeleteAggregationPolicy(c *gin.Context) {
	if err := h.DB.Delete(&model.AggregationPolicy{}, idParam(c)).Error; err != nil {
		response.Error(c, "删除失败")
		return
	}
	response.OK(c, nil)
}

// PreviewAggregation 归桶预览：拿当前未恢复的告警按策略算一遍
func (h *Handler) PreviewAggregation(c *gin.Context) {
	var item model.AggregationPolicy
	if err := h.DB.First(&item, idParam(c)).Error; err != nil {
		response.NotFound(c, "策略不存在")
		return
	}

	buckets, matched, err := h.aggregateAlerts(item)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	grouped, groupedAlerts := 0, 0
	for _, bucket := range buckets {
		if bucket.Grouped {
			grouped++
			groupedAlerts += bucket.AlertCount
		}
	}
	response.OK(c, gin.H{
		"policy": item, "buckets": buckets,
		"matchedAlerts": matched, "bucketCount": len(buckets),
		"groupedBuckets": grouped, "groupedAlerts": groupedAlerts,
		"detail": fmt.Sprintf("窗口内命中 %d 条未恢复告警，归成 %d 个桶，其中 %d 个达到 %d 条成桶",
			matched, len(buckets), grouped, item.MinCount),
	})
}

// DetectAggregationOverlaps 重叠检测：找出会同时命中同一条告警的启用策略。
//
// 抑制通知时只有优先级最高的那条生效，所以重叠本身不会重复抑制，
// 但会让人误判「到底哪条策略在起作用」，这里把冲突点摆出来。
func (h *Handler) DetectAggregationOverlaps(c *gin.Context) {
	var policies []model.AggregationPolicy
	if err := h.DB.Where("enabled = ?", true).Order("priority asc, id asc").Find(&policies).Error; err != nil {
		response.Error(c, "查询聚合策略失败")
		return
	}
	if len(policies) < 2 {
		response.OK(c, gin.H{"overlaps": []gin.H{}, "detail": "启用中的策略少于两条，不存在重叠"})
		return
	}

	var alerts []model.Alert
	h.DB.Where("status <> ?", "resolved").Order("id desc").Limit(500).Find(&alerts)

	type pairKey struct{ a, b uint }
	counts := map[pairKey]int{}
	samples := map[pairKey]string{}
	for _, alert := range alerts {
		hit := make([]model.AggregationPolicy, 0, 2)
		for _, policy := range policies {
			if policyMatches(policy, alert) {
				hit = append(hit, policy)
			}
		}
		for i := 0; i < len(hit); i++ {
			for j := i + 1; j < len(hit); j++ {
				key := pairKey{hit[i].ID, hit[j].ID}
				counts[key]++
				if samples[key] == "" {
					samples[key] = alert.Title
				}
			}
		}
	}

	nameOf := map[uint]model.AggregationPolicy{}
	for _, policy := range policies {
		nameOf[policy.ID] = policy
	}
	overlaps := make([]gin.H, 0, len(counts))
	for key, count := range counts {
		first, second := nameOf[key.a], nameOf[key.b]
		effective := first
		if second.Priority < first.Priority {
			effective = second
		}
		overlaps = append(overlaps, gin.H{
			"policyA": first.Name, "priorityA": first.Priority,
			"policyB": second.Name, "priorityB": second.Priority,
			"alerts": count, "sample": samples[key],
			"effective": effective.Name,
		})
	}
	sort.Slice(overlaps, func(i, j int) bool {
		return overlaps[i]["alerts"].(int) > overlaps[j]["alerts"].(int)
	})

	detail := "没有重叠"
	if len(overlaps) > 0 {
		detail = "存在重叠：抑制通知时只有优先级小的那条生效"
	}
	response.OK(c, gin.H{"overlaps": overlaps, "detail": detail})
}
