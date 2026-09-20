package handler

import (
	"encoding/csv"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"ops-platform/server/internal/model"
	"ops-platform/server/internal/response"
)

// 用量与成本：把 model_calls 这张流水表按时间、模型、上游、调用人切开。
//
// 统计口径写在这里，界面上也照实说明：
//   - 成本是按上游登记的单价算出来的估值，不是供应商账单（平台不对账）
//   - token 只认上游返回的 usage；上游没给的那些调用 token 记 0，单独计数
//   - 只统计走网关的调用；谁绕开平台直连供应商，这里看不到
//
// 趋势分桶在 Go 里做而不是写 SQL 的 strftime：时间在库里的存法与时区处理
// 各驱动不一样（sqlite 更是重灾区），放在 Go 里既准确又能单测。

// usageMaxRows 趋势分桶一次最多扫多少行，超了如实告知而不是悄悄少算
const usageMaxRows = 200000

// modelUsageBucket 一个时间桶的用量
type modelUsageBucket struct {
	// Label 桶的开始时刻，day 桶形如 2026-09-21，hour 桶形如 2026-09-21 14:00
	Label   string  `json:"label"`
	Calls   int64   `json:"calls"`
	Failed  int64   `json:"failed"`
	Tokens  int64   `json:"tokens"`
	Cost    float64 `json:"cost"`
	Latency int64   `json:"avgLatencyMs"`
}

// usageRow 分桶只需要这几列，别把整行捞出来
type usageRow struct {
	CreatedAt  time.Time
	TotalToken int64
	Cost       float64
	LatencyMs  int64
	CallStatus string
}

// bucketKey 按粒度算出桶标签。跨天/跨小时的边界由 time 包处理，不自己拼字符串。
func bucketKey(at time.Time, bucket string) string {
	local := at.Local()
	if bucket == "hour" {
		return local.Format("2006-01-02 15:00")
	}
	return local.Format("2006-01-02")
}

// buildUsageTrend 把流水按桶汇总，并补齐中间没有调用的空桶。
//
// 补空桶是为了让折线图不至于把「周末没人用」画成一条直线连过去 ——
// 看趋势的人需要看见那个凹陷。
func buildUsageTrend(rows []usageRow, bucket string, start, end time.Time) []modelUsageBucket {
	type acc struct {
		calls, failed, tokens, latencySum, latencyCount int64
		cost                                            float64
	}
	buckets := map[string]*acc{}
	for _, row := range rows {
		key := bucketKey(row.CreatedAt, bucket)
		item, ok := buckets[key]
		if !ok {
			item = &acc{}
			buckets[key] = item
		}
		item.calls++
		if row.CallStatus == "failed" {
			item.failed++
		}
		item.tokens += row.TotalToken
		item.cost += row.Cost
		// 失败的调用耗时没有可比性（大多是连接失败的 0），不进平均
		if row.CallStatus == "success" {
			item.latencySum += row.LatencyMs
			item.latencyCount++
		}
	}

	// 时间轴按桶粒度铺满，哪怕某个桶一次调用都没有
	labels := make([]string, 0, len(buckets)+8)
	seen := map[string]bool{}
	step := 24 * time.Hour
	cursor := start.Local().Truncate(time.Hour)
	if bucket == "hour" {
		step = time.Hour
	} else {
		cursor = time.Date(start.Local().Year(), start.Local().Month(), start.Local().Day(),
			0, 0, 0, 0, time.Local)
	}
	for !cursor.After(end) {
		key := bucketKey(cursor, bucket)
		if !seen[key] {
			labels = append(labels, key)
			seen[key] = true
		}
		cursor = cursor.Add(step)
	}
	// 落在时间轴之外的桶（比如筛选条件给了开区间）也别丢
	extra := make([]string, 0, 4)
	for key := range buckets {
		if !seen[key] {
			extra = append(extra, key)
		}
	}
	sort.Strings(extra)
	labels = append(labels, extra...)
	sort.Strings(labels)

	trend := make([]modelUsageBucket, 0, len(labels))
	for _, label := range labels {
		item := buckets[label]
		if item == nil {
			trend = append(trend, modelUsageBucket{Label: label})
			continue
		}
		point := modelUsageBucket{
			Label: label, Calls: item.calls, Failed: item.failed,
			Tokens: item.tokens, Cost: math.Round(item.cost*1e6) / 1e6,
		}
		if item.latencyCount > 0 {
			point.Latency = item.latencySum / item.latencyCount
		}
		trend = append(trend, point)
	}
	return trend
}

// modelUsageRank 一个维度上的排名项
type modelUsageRank struct {
	Name         string  `json:"name"`
	Calls        int64   `json:"calls"`
	Failed       int64   `json:"failed"`
	Tokens       int64   `json:"tokens"`
	Cost         float64 `json:"cost"`
	AvgLatencyMs int64   `json:"avgLatencyMs"`
}

// modelUsageQuery 按筛选条件拼出流水查询。
//
// 返回 Session 后的实例：同一个 q 会被 Count / 分组 / 取行多次复用，
// 不这么做的话条件会一次次叠上去（GORM 的老坑）。
func (h *Handler) modelUsageQuery(c *gin.Context) (*gorm.DB, *time.Time, *time.Time, error) {
	q := h.DB.Model(&model.ModelCall{})
	if alias := strings.TrimSpace(c.Query("alias")); alias != "" {
		q = q.Where("alias = ?", alias)
	}
	if caller := strings.TrimSpace(c.Query("caller")); caller != "" {
		q = q.Where("caller = ?", caller)
	}
	if status := strings.TrimSpace(c.Query("status")); status != "" {
		q = q.Where("call_status = ?", status)
	}
	if username := strings.TrimSpace(c.Query("username")); username != "" {
		q = q.Where("username LIKE ?", "%"+username+"%")
	}
	if raw := strings.TrimSpace(c.Query("upstreamId")); raw != "" {
		id, err := strconv.Atoi(raw)
		if err != nil || id <= 0 {
			return nil, nil, nil, fmt.Errorf("upstreamId 不合法")
		}
		q = q.Where("upstream_id = ?", id)
	}

	start, err := parseAuditTime(c.Query("start"), false)
	if err != nil {
		return nil, nil, nil, err
	}
	end, err := parseAuditTime(c.Query("end"), true)
	if err != nil {
		return nil, nil, nil, err
	}
	if start != nil {
		q = q.Where("created_at >= ?", *start)
	}
	if end != nil {
		q = q.Where("created_at <= ?", *end)
	}
	return q.Session(&gorm.Session{}), start, end, nil
}

// usageRank 按某一列分组排名，按成本从高到低（成本都是 0 时按调用次数）
func usageRank(q *gorm.DB, column string, limit int) []modelUsageRank {
	var rows []struct {
		Name       string
		Calls      int64
		Failed     int64
		Tokens     int64
		Cost       float64
		AvgLatency float64
	}
	q.Select(column + " as name, COUNT(*) as calls, " +
		"COALESCE(SUM(CASE WHEN call_status = 'failed' THEN 1 ELSE 0 END),0) as failed, " +
		"COALESCE(SUM(total_tokens),0) as tokens, COALESCE(SUM(cost),0) as cost, " +
		"COALESCE(AVG(CASE WHEN call_status = 'success' THEN latency_ms END),0) as avg_latency").
		Group(column).Order("cost desc, calls desc").Limit(limit).Scan(&rows)

	list := make([]modelUsageRank, 0, len(rows))
	for _, row := range rows {
		name := row.Name
		if strings.TrimSpace(name) == "" {
			name = "(空)"
		}
		list = append(list, modelUsageRank{
			Name: name, Calls: row.Calls, Failed: row.Failed, Tokens: row.Tokens,
			Cost: math.Round(row.Cost*1e6) / 1e6, AvgLatencyMs: int64(math.Round(row.AvgLatency)),
		})
	}
	return list
}

// ModelUsage 用量与成本总览
func (h *Handler) ModelUsage(c *gin.Context) {
	bucket := strings.ToLower(strings.TrimSpace(c.DefaultQuery("bucket", "day")))
	if bucket != "day" && bucket != "hour" {
		response.BadRequest(c, "bucket 只支持 day 或 hour")
		return
	}

	q, start, end, err := h.modelUsageQuery(c)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	var summary struct {
		Calls            int64
		Failed           int64
		PromptTokens     int64
		CompletionTokens int64
		Tokens           int64
		Cost             float64
		AvgLatency       float64
		MaxLatency       int64
		UsageMissing     int64
		Retried          int64
	}
	if err := q.Select(
		"COUNT(*) as calls, " +
			"COALESCE(SUM(CASE WHEN call_status = 'failed' THEN 1 ELSE 0 END),0) as failed, " +
			"COALESCE(SUM(prompt_tokens),0) as prompt_tokens, " +
			"COALESCE(SUM(completion_tokens),0) as completion_tokens, " +
			"COALESCE(SUM(total_tokens),0) as tokens, COALESCE(SUM(cost),0) as cost, " +
			"COALESCE(AVG(CASE WHEN call_status = 'success' THEN latency_ms END),0) as avg_latency, " +
			"COALESCE(MAX(latency_ms),0) as max_latency, " +
			"COALESCE(SUM(CASE WHEN usage_missing THEN 1 ELSE 0 END),0) as usage_missing, " +
			"COALESCE(SUM(CASE WHEN retried THEN 1 ELSE 0 END),0) as retried").
		Scan(&summary).Error; err != nil {
		response.Error(c, "统计失败")
		return
	}

	// 趋势：只取分桶需要的列
	var rows []usageRow
	if err := q.Select("created_at, total_tokens as total_token, cost, latency_ms, call_status").
		Order("created_at asc").Limit(usageMaxRows + 1).Scan(&rows).Error; err != nil {
		response.Error(c, "统计失败")
		return
	}
	truncated := false
	if len(rows) > usageMaxRows {
		rows = rows[:usageMaxRows]
		truncated = true
	}

	// 时间轴：没给范围时按数据自身的首尾铺
	trendStart, trendEnd := time.Now(), time.Now()
	if len(rows) > 0 {
		trendStart, trendEnd = rows[0].CreatedAt, rows[len(rows)-1].CreatedAt
	}
	if start != nil {
		trendStart = *start
	}
	if end != nil && end.Before(time.Now()) {
		trendEnd = *end
	}

	successRate := float64(0)
	if summary.Calls > 0 {
		successRate = math.Round(float64(summary.Calls-summary.Failed)/float64(summary.Calls)*10000) / 100
	}

	response.OK(c, gin.H{
		"summary": gin.H{
			"calls": summary.Calls, "failed": summary.Failed, "successRate": successRate,
			"promptTokens": summary.PromptTokens, "completionTokens": summary.CompletionTokens,
			"tokens": summary.Tokens, "cost": math.Round(summary.Cost*1e6) / 1e6,
			"avgLatencyMs": int64(math.Round(summary.AvgLatency)),
			"maxLatencyMs": summary.MaxLatency,
			"usageMissing": summary.UsageMissing, "retried": summary.Retried,
		},
		"trend":       buildUsageTrend(rows, bucket, trendStart, trendEnd),
		"byAlias":     usageRank(q, "alias", 20),
		"byUpstream":  usageRank(q, "upstream_name", 20),
		"byUser":      usageRank(q, "username", 20),
		"bucket":      bucket,
		"rowsScanned": len(rows),
		"truncated":   truncated,
	})
}

// modelCallExportMaxRows 导出上限，和其它导出保持一致
const modelCallExportMaxRows = 10000

// ExportModelCalls 按当前筛选条件导出调用流水。
//
// 导出的是元数据（谁、什么时候、哪个模型、多少 token、多少钱），
// 依然不含提示词与回复正文 —— 那是业务数据，见 docs/SECURITY.md 21。
func (h *Handler) ExportModelCalls(c *gin.Context) {
	q, _, _, err := h.modelUsageQuery(c)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	var rows []model.ModelCall
	if err := q.Order("id desc").Limit(modelCallExportMaxRows + 1).Find(&rows).Error; err != nil {
		response.Error(c, "导出失败")
		return
	}
	truncated := false
	if len(rows) > modelCallExportMaxRows {
		rows = rows[:modelCallExportMaxRows]
		truncated = true
	}

	filename := "model-calls-" + time.Now().Format("20060102-150405") + ".csv"
	c.Header("Content-Disposition", "attachment; filename="+filename)
	c.Header("Content-Type", "text/csv; charset=utf-8")
	if truncated {
		// 让调用方知道这不是全部，而不是以为就这么多
		c.Header("X-Export-Truncated", "true")
	} else {
		c.Header("X-Export-Truncated", "false")
	}
	// Excel 不认没有 BOM 的 UTF-8
	_, _ = c.Writer.Write(utf8BOM)

	writer := csv.NewWriter(c.Writer)
	defer writer.Flush()
	_ = writer.Write([]string{
		"ID", "时间", "模型(Alias)", "上游", "供应商", "上游模型", "来源",
		"调用人", "来源IP", "输入token", "输出token", "合计token",
		"成本(元)", "耗时(ms)", "状态", "是否切换重试", "usage缺失", "错误",
	})
	for _, row := range rows {
		_ = writer.Write([]string{
			strconv.FormatUint(uint64(row.ID), 10),
			row.CreatedAt.Format("2006-01-02 15:04:05"),
			row.Alias, row.UpstreamName, row.Provider, row.Model, row.Caller,
			row.Username, row.ClientIP,
			strconv.Itoa(row.PromptTokens), strconv.Itoa(row.CompletionTokens),
			strconv.Itoa(row.TotalTokens),
			strconv.FormatFloat(row.Cost, 'f', 6, 64),
			strconv.FormatInt(row.LatencyMs, 10), row.CallStatus,
			boolText(row.Retried), boolText(row.UsageMissing), row.ErrorMsg,
		})
	}
}

func boolText(v bool) string {
	if v {
		return "是"
	}
	return "否"
}
