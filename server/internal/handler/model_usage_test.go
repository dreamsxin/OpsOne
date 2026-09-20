package handler

import (
	"encoding/csv"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"ops-platform/server/internal/model"
)

func TestBucketKey(t *testing.T) {
	at := time.Date(2026, 9, 21, 14, 37, 12, 0, time.Local)
	if got := bucketKey(at, "day"); got != "2026-09-21" {
		t.Fatalf("day 桶不对: %s", got)
	}
	if got := bucketKey(at, "hour"); got != "2026-09-21 14:00" {
		t.Fatalf("hour 桶不对: %s", got)
	}
	// 跨天边界：23:59 与次日 00:01 必须落在不同的天桶
	late := time.Date(2026, 9, 21, 23, 59, 59, 0, time.Local)
	early := time.Date(2026, 9, 22, 0, 0, 1, 0, time.Local)
	if bucketKey(late, "day") == bucketKey(early, "day") {
		t.Fatal("跨天的两条被算进同一个桶")
	}
}

func TestBuildUsageTrendFillsEmptyBuckets(t *testing.T) {
	day := func(d, h int) time.Time {
		return time.Date(2026, 9, d, h, 30, 0, 0, time.Local)
	}
	rows := []usageRow{
		{CreatedAt: day(19, 10), TotalToken: 100, Cost: 0.1, LatencyMs: 200, CallStatus: "success"},
		{CreatedAt: day(19, 11), TotalToken: 50, Cost: 0.05, LatencyMs: 400, CallStatus: "success"},
		// 失败的那条不参与平均耗时：它多半是连接失败的 0，会把均值拉垮
		{CreatedAt: day(19, 12), TotalToken: 0, Cost: 0, LatencyMs: 0, CallStatus: "failed"},
		// 20 日没有任何调用，21 日又有
		{CreatedAt: day(21, 9), TotalToken: 20, Cost: 0.02, LatencyMs: 100, CallStatus: "success"},
	}

	trend := buildUsageTrend(rows, "day", day(19, 0), day(21, 23))
	if len(trend) != 3 {
		t.Fatalf("应该铺满 3 天，实际 %d: %+v", len(trend), trend)
	}
	if trend[0].Label != "2026-09-19" || trend[0].Calls != 3 || trend[0].Failed != 1 {
		t.Fatalf("19 日汇总不对: %+v", trend[0])
	}
	if trend[0].Tokens != 150 || trend[0].Cost != 0.15 {
		t.Fatalf("19 日 token/成本不对: %+v", trend[0])
	}
	// (200+400)/2 = 300，失败那条的 0 不能进来
	if trend[0].Latency != 300 {
		t.Fatalf("平均耗时应排除失败调用: %+v", trend[0].Latency)
	}
	// 中间没调用的那天必须是 0 值的桶，而不是直接消失
	if trend[1].Label != "2026-09-20" || trend[1].Calls != 0 {
		t.Fatalf("空桶没补上: %+v", trend[1])
	}
	if trend[2].Label != "2026-09-21" || trend[2].Calls != 1 {
		t.Fatalf("21 日汇总不对: %+v", trend[2])
	}
}

func TestBuildUsageTrendHourBucket(t *testing.T) {
	base := time.Date(2026, 9, 21, 8, 0, 0, 0, time.Local)
	rows := []usageRow{
		{CreatedAt: base.Add(5 * time.Minute), TotalToken: 10, Cost: 0.01, LatencyMs: 100, CallStatus: "success"},
		{CreatedAt: base.Add(55 * time.Minute), TotalToken: 10, Cost: 0.01, LatencyMs: 300, CallStatus: "success"},
		{CreatedAt: base.Add(65 * time.Minute), TotalToken: 5, Cost: 0.005, LatencyMs: 200, CallStatus: "success"},
	}
	trend := buildUsageTrend(rows, "hour", base, base.Add(2*time.Hour))
	if len(trend) != 3 {
		t.Fatalf("应该有 3 个小时桶，实际 %d: %+v", len(trend), trend)
	}
	if trend[0].Label != "2026-09-21 08:00" || trend[0].Calls != 2 {
		t.Fatalf("08 点桶不对: %+v", trend[0])
	}
	if trend[1].Label != "2026-09-21 09:00" || trend[1].Calls != 1 {
		t.Fatalf("09 点桶不对: %+v", trend[1])
	}
}

// seedUsageCalls 造一批跨天、跨模型、跨人的流水
func seedUsageCalls(t *testing.T, h *Handler) {
	t.Helper()
	day := func(d, hour int) time.Time {
		return time.Date(2026, 9, d, hour, 0, 0, 0, time.Local)
	}
	rows := []model.ModelCall{
		{Alias: "chat", UpstreamName: "主力", UpstreamID: 1, Provider: "deepseek", Model: "m1",
			Caller: "api", Username: "alice", PromptTokens: 100, CompletionTokens: 50,
			TotalTokens: 150, Cost: 0.3, LatencyMs: 200, CallStatus: "success", CreatedAt: day(19, 10)},
		{Alias: "chat", UpstreamName: "主力", UpstreamID: 1, Provider: "deepseek", Model: "m1",
			Caller: "api", Username: "alice", PromptTokens: 200, CompletionTokens: 100,
			TotalTokens: 300, Cost: 0.6, LatencyMs: 400, CallStatus: "success", CreatedAt: day(19, 11)},
		{Alias: "chat", UpstreamName: "备用", UpstreamID: 2, Provider: "qwen", Model: "m2",
			Caller: "api", Username: "bob", CallStatus: "failed", ErrorMsg: "上游 500",
			Retried: true, CreatedAt: day(20, 9)},
		{Alias: "code", UpstreamName: "备用", UpstreamID: 2, Provider: "qwen", Model: "m2",
			Caller: "console", Username: "bob", PromptTokens: 20, CompletionTokens: 10,
			TotalTokens: 30, Cost: 0.05, LatencyMs: 150, CallStatus: "success",
			UsageMissing: false, CreatedAt: day(21, 15)},
		{Alias: "code", UpstreamName: "备用", UpstreamID: 2, Provider: "qwen", Model: "m2",
			Caller: "check", Username: "admin", CallStatus: "success", UsageMissing: true,
			LatencyMs: 50, CreatedAt: day(21, 16)},
	}
	for i := range rows {
		if err := h.DB.Create(&rows[i]).Error; err != nil {
			t.Fatalf("写入流水失败: %v", err)
		}
	}
}

func newUsageTestEngine(t *testing.T) (*Handler, *gin.Engine) {
	t.Helper()
	h, _ := newModelTestHandler(t)
	engine := gin.New()
	engine.Use(func(c *gin.Context) {
		c.Set("ctx_user", &model.User{ID: 1, Username: "tester"})
		c.Next()
	})
	engine.GET("/ai/usage", h.ModelUsage)
	engine.GET("/ai/calls/export", h.ExportModelCalls)
	return h, engine
}

func TestModelUsageSummaryAndRanks(t *testing.T) {
	h, engine := newUsageTestEngine(t)
	seedUsageCalls(t, h)

	body := getModelJSON(t, engine, "/ai/usage?start=2026-09-19&end=2026-09-21&bucket=day")
	data, _ := body["data"].(map[string]any)
	summary, _ := data["summary"].(map[string]any)

	if summary["calls"] != float64(5) || summary["failed"] != float64(1) {
		t.Fatalf("总数不对: %+v", summary)
	}
	// 成功率 4/5 = 80
	if summary["successRate"] != float64(80) {
		t.Fatalf("成功率不对: %+v", summary["successRate"])
	}
	if summary["tokens"] != float64(480) || summary["promptTokens"] != float64(320) {
		t.Fatalf("token 汇总不对: %+v", summary)
	}
	if summary["cost"] != 0.95 {
		t.Fatalf("成本汇总不对: %+v", summary["cost"])
	}
	// 平均耗时只算成功的：(200+400+150+50)/4 = 200
	if summary["avgLatencyMs"] != float64(200) {
		t.Fatalf("平均耗时不对: %+v", summary["avgLatencyMs"])
	}
	if summary["usageMissing"] != float64(1) || summary["retried"] != float64(1) {
		t.Fatalf("usage 缺失/重试计数不对: %+v", summary)
	}

	trend, _ := data["trend"].([]any)
	if len(trend) != 3 {
		t.Fatalf("趋势应该铺满 3 天: %d", len(trend))
	}

	// 排名按成本从高到低
	byAlias, _ := data["byAlias"].([]any)
	first, _ := byAlias[0].(map[string]any)
	if first["name"] != "chat" || first["cost"] != 0.9 || first["calls"] != float64(3) {
		t.Fatalf("按模型排名不对: %+v", first)
	}
	byUser, _ := data["byUser"].([]any)
	topUser, _ := byUser[0].(map[string]any)
	if topUser["name"] != "alice" || topUser["cost"] != 0.9 {
		t.Fatalf("按人排名不对: %+v", topUser)
	}
	byUpstream, _ := data["byUpstream"].([]any)
	topUpstream, _ := byUpstream[0].(map[string]any)
	if topUpstream["name"] != "主力" {
		t.Fatalf("按上游排名不对: %+v", topUpstream)
	}
}

func TestModelUsageFilters(t *testing.T) {
	h, engine := newUsageTestEngine(t)
	seedUsageCalls(t, h)

	// 按 Alias
	data, _ := getModelJSON(t, engine, "/ai/usage?alias=code")["data"].(map[string]any)
	summary, _ := data["summary"].(map[string]any)
	if summary["calls"] != float64(2) || summary["cost"] != 0.05 {
		t.Fatalf("按 alias 筛选不对: %+v", summary)
	}
	// 按来源：探活单独看得见
	data, _ = getModelJSON(t, engine, "/ai/usage?caller=check")["data"].(map[string]any)
	summary, _ = data["summary"].(map[string]any)
	if summary["calls"] != float64(1) || summary["usageMissing"] != float64(1) {
		t.Fatalf("按来源筛选不对: %+v", summary)
	}
	// 按上游
	data, _ = getModelJSON(t, engine, "/ai/usage?upstreamId=1")["data"].(map[string]any)
	summary, _ = data["summary"].(map[string]any)
	if summary["calls"] != float64(2) {
		t.Fatalf("按上游筛选不对: %+v", summary)
	}
	// 按调用人（模糊）
	data, _ = getModelJSON(t, engine, "/ai/usage?username=bo")["data"].(map[string]any)
	summary, _ = data["summary"].(map[string]any)
	if summary["calls"] != float64(2) {
		t.Fatalf("按调用人筛选不对: %+v", summary)
	}
	// 时间范围：只要 19 号那天
	data, _ = getModelJSON(t, engine, "/ai/usage?start=2026-09-19&end=2026-09-19")["data"].(map[string]any)
	summary, _ = data["summary"].(map[string]any)
	if summary["calls"] != float64(2) || summary["cost"] != 0.9 {
		t.Fatalf("按时间范围筛选不对: %+v", summary)
	}
	// 历史区间应为空，且不能因为没数据就报错
	data, _ = getModelJSON(t, engine, "/ai/usage?start=2000-01-01&end=2000-01-02")["data"].(map[string]any)
	summary, _ = data["summary"].(map[string]any)
	if summary["calls"] != float64(0) || summary["successRate"] != float64(0) {
		t.Fatalf("空区间应该是 0 而不是报错: %+v", summary)
	}

	// 非法参数要被拦住
	body := getModelJSON(t, engine, "/ai/usage?bucket=week")
	if body["code"] == float64(0) || !strings.Contains(body["msg"].(string), "bucket") {
		t.Fatalf("非法 bucket 应该被拒: %+v", body)
	}
	body = getModelJSON(t, engine, "/ai/usage?upstreamId=abc")
	if body["code"] == float64(0) {
		t.Fatalf("非法 upstreamId 应该被拒: %+v", body)
	}
}

func TestExportModelCalls(t *testing.T) {
	h, engine := newUsageTestEngine(t)
	seedUsageCalls(t, h)

	req := httptest.NewRequest(http.MethodGet, "/ai/calls/export?alias=chat", nil)
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("导出状态码 %d", rec.Code)
	}
	raw := rec.Body.Bytes()
	if !strings.HasPrefix(string(raw), string(utf8BOM)) {
		t.Fatal("导出缺少 UTF-8 BOM，Excel 会乱码")
	}
	if got := rec.Header().Get("X-Export-Truncated"); got != "false" {
		t.Fatalf("截断标记不对: %q", got)
	}

	reader := csv.NewReader(strings.NewReader(strings.TrimPrefix(string(raw), string(utf8BOM))))
	records, err := reader.ReadAll()
	if err != nil {
		t.Fatalf("CSV 解析失败: %v", err)
	}
	// 表头 + chat 的 3 条
	if len(records) != 4 {
		t.Fatalf("行数不对: %d", len(records))
	}
	header := records[0]
	if header[2] != "模型(Alias)" || header[12] != "成本(元)" {
		t.Fatalf("表头不对: %v", header)
	}
	// 导出里绝不能出现提示词或回复正文的列
	joined := strings.Join(header, ",")
	if strings.Contains(joined, "提示词") || strings.Contains(joined, "回复") {
		t.Fatalf("导出不该带上正文列: %v", header)
	}
	if records[1][2] != "chat" {
		t.Fatalf("导出内容不对: %v", records[1])
	}
}
