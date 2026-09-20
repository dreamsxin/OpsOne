package handler

import (
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"ops-platform/server/internal/model"
	"ops-platform/server/internal/response"
)

// bucket 时间序列的一个点
type bucket struct {
	Time     string `json:"time"`
	Critical int    `json:"critical"`
	Warning  int    `json:"warning"`
	Info     int    `json:"info"`
}

type nameCount struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// AlertSituation 告警态势：趋势 + 分布 + Top 标签 + 处理时效。
// range 支持 24h / 7d / 30d，缺省 24h。
func (h *Handler) AlertSituation(c *gin.Context) {
	rangeKey := c.DefaultQuery("range", "24h")
	since, step, layout := situationWindow(rangeKey)

	var alerts []model.Alert
	if err := h.DB.Where("first_seen_at >= ?", since).Order("first_seen_at asc").Find(&alerts).Error; err != nil {
		response.Error(c, "查询告警失败")
		return
	}

	response.OK(c, gin.H{
		"range":       rangeKey,
		"since":       since.Format(time.RFC3339),
		"total":       len(alerts),
		"trend":       buildTrend(alerts, since, step, layout),
		"bySeverity":  countBy(alerts, func(a model.Alert) string { return a.Severity }),
		"byStatus":    countBy(alerts, func(a model.Alert) string { return a.Status }),
		"bySource":    countBy(alerts, func(a model.Alert) string { return orUnknown(a.SourceName) }),
		"topLabels":   topLabels(alerts, 8),
		"handleStats": handleStats(alerts),
	})
}

// situationWindow 返回起始时间、分桶步长与桶标签格式
func situationWindow(rangeKey string) (time.Time, time.Duration, string) {
	now := time.Now()
	switch rangeKey {
	case "7d":
		return now.Add(-7 * 24 * time.Hour), 24 * time.Hour, "01-02"
	case "30d":
		return now.Add(-30 * 24 * time.Hour), 24 * time.Hour, "01-02"
	default:
		return now.Add(-24 * time.Hour), time.Hour, "15:04"
	}
}

// buildTrend 把告警按首次出现时间分桶，空桶也保留以便画出连续折线
func buildTrend(alerts []model.Alert, since time.Time, step time.Duration, layout string) []bucket {
	start := since.Truncate(step)
	count := int(time.Since(start)/step) + 1
	if count < 1 {
		count = 1
	}

	buckets := make([]bucket, count)
	for i := range buckets {
		buckets[i].Time = start.Add(time.Duration(i) * step).Format(layout)
	}

	for _, alert := range alerts {
		idx := int(alert.FirstSeenAt.Sub(start) / step)
		if idx < 0 || idx >= count {
			continue
		}
		switch alert.Severity {
		case "critical":
			buckets[idx].Critical++
		case "warning":
			buckets[idx].Warning++
		default:
			buckets[idx].Info++
		}
	}
	return buckets
}

func countBy(alerts []model.Alert, key func(model.Alert) string) []nameCount {
	counter := map[string]int{}
	for _, alert := range alerts {
		counter[key(alert)]++
	}
	return sortedCounts(counter, 0)
}

// topLabels 统计标签键值对出现次数，用于快速定位「哪台机器/哪个服务在报」
func topLabels(alerts []model.Alert, limit int) []nameCount {
	counter := map[string]int{}
	for _, alert := range alerts {
		if alert.Labels == "" {
			continue
		}
		labels := map[string]string{}
		if json.Unmarshal([]byte(alert.Labels), &labels) != nil {
			continue
		}
		for k, v := range labels {
			counter[k+"="+v]++
		}
	}
	return sortedCounts(counter, limit)
}

func sortedCounts(counter map[string]int, limit int) []nameCount {
	list := make([]nameCount, 0, len(counter))
	for name, count := range counter {
		list = append(list, nameCount{Name: name, Count: count})
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].Count == list[j].Count {
			return list[i].Name < list[j].Name
		}
		return list[i].Count > list[j].Count
	})
	if limit > 0 && len(list) > limit {
		list = list[:limit]
	}
	return list
}

// handleStats 平均确认时长与平均恢复时长，只统计有对应时间戳的样本
func handleStats(alerts []model.Alert) gin.H {
	var ackTotal, resolveTotal time.Duration
	var ackNum, resolveNum int

	for _, alert := range alerts {
		if alert.AckAt != nil {
			ackTotal += alert.AckAt.Sub(alert.FirstSeenAt)
			ackNum++
		}
		if alert.ResolvedAt != nil {
			resolveTotal += alert.ResolvedAt.Sub(alert.FirstSeenAt)
			resolveNum++
		}
	}

	avg := func(total time.Duration, n int) int64 {
		if n == 0 {
			return 0
		}
		return int64(total / time.Duration(n) / time.Second)
	}

	return gin.H{
		"ackedNum":      ackNum,
		"resolvedNum":   resolveNum,
		"avgAckSec":     avg(ackTotal, ackNum),
		"avgResolveSec": avg(resolveTotal, resolveNum),
	}
}

func orUnknown(s string) string {
	if strings.TrimSpace(s) == "" {
		return "未知来源"
	}
	return s
}
