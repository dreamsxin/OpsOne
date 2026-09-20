package handler

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"ops-platform/server/internal/middleware"
	"ops-platform/server/internal/model"
	"ops-platform/server/internal/response"
)

const (
	// metricExprMaxLen PromQL 长度上限。再长基本是粘错了东西
	metricExprMaxLen = 4000
	// metricRespLimit 读 Prometheus 响应的上限。一次查出几十兆的结果对谁都没好处
	metricRespLimit = 16 << 20
	// metricMaxPoints 范围查询的点数上限，和 Prometheus 自己的 11000 对齐
	metricMaxPoints = 11000
	// metricMaxSeries 返回给界面的曲线条数上限，超出截断并如实告知
	metricMaxSeries = 60
)

// ---------- 与 Prometheus 通信 ----------

// promEnvelope Prometheus HTTP API 的统一信封
type promEnvelope struct {
	Status    string          `json:"status"` // success | error
	Data      json.RawMessage `json:"data"`
	ErrorType string          `json:"errorType"`
	Error     string          `json:"error"`
	Warnings  []string        `json:"warnings"`
}

// promResultData 查询结果。resultType: vector | matrix | scalar | string
type promResultData struct {
	ResultType string `json:"resultType"`
	Result     []struct {
		Metric map[string]string `json:"metric"`
		// Value 即时查询：[时间戳, "值"]
		Value []any `json:"value"`
		// Values 范围查询：[[时间戳, "值"], ...]
		Values [][]any `json:"values"`
	} `json:"result"`
}

// promRequest 调一次 Prometheus 的 HTTP API。
//
// 错误信息一律带上 Prometheus 自己的 error 字段：PromQL 写错时它说得最清楚
// （比如「parse error: unexpected character」带列号），包装一层只会丢信息。
func promRequest(source model.MetricSource, path string, query url.Values, out any) ([]string, error) {
	base := strings.TrimRight(strings.TrimSpace(source.BaseURL), "/")
	if base == "" {
		return nil, fmt.Errorf("数据源未配置地址")
	}
	target := base + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}

	req, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if source.HeaderKey != "" {
		req.Header.Set(source.HeaderKey, source.HeaderValue)
	}

	timeout := time.Duration(source.TimeoutSec) * time.Second
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, metricRespLimit))
	if err != nil {
		return nil, err
	}

	var envelope promEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		// 地址填成了别的服务时会走到这里，带上原文比「解析失败」有用
		return nil, fmt.Errorf("返回不是 Prometheus 格式（HTTP %d）: %s",
			resp.StatusCode, truncate(strings.TrimSpace(string(body)), 160))
	}
	if envelope.Status != "success" {
		detail := envelope.Error
		if detail == "" {
			detail = fmt.Sprintf("HTTP %d", resp.StatusCode)
		}
		if envelope.ErrorType != "" {
			return envelope.Warnings, fmt.Errorf("%s: %s", envelope.ErrorType, detail)
		}
		return envelope.Warnings, fmt.Errorf("%s", detail)
	}
	if out != nil && len(envelope.Data) > 0 {
		if err := json.Unmarshal(envelope.Data, out); err != nil {
			return envelope.Warnings, err
		}
	}
	return envelope.Warnings, nil
}

// ---------- 结果整形（纯函数，便于单测） ----------

// metricSeries 一条曲线：标签 + 采样点
type metricSeries struct {
	// Name 用来在图例里显示，形如 up{instance="a",job="b"}
	Name   string            `json:"name"`
	Labels map[string]string `json:"labels"`
	Points []metricPoint     `json:"points"`
	// Value 即时查询时的当前值（字符串原样保留，避免大整数精度丢失）
	Value string `json:"value"`
}

type metricPoint struct {
	// At 毫秒时间戳，前端直接喂给 echarts
	At    int64  `json:"at"`
	Value string `json:"value"`
}

// seriesName 按 Prometheus 习惯拼出 name{k="v",...}，标签按键排序保证稳定
func seriesName(labels map[string]string) string {
	name := labels["__name__"]
	parts := make([]string, 0, len(labels))
	for key, value := range labels {
		if key == "__name__" {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s=%q", key, value))
	}
	sort.Strings(parts)
	if len(parts) == 0 {
		if name == "" {
			return "{}"
		}
		return name
	}
	return name + "{" + strings.Join(parts, ",") + "}"
}

// samplePoint 把 [时间戳, "值"] 转成结构体。时间戳是秒（可能带小数）
func samplePoint(raw []any) (metricPoint, bool) {
	if len(raw) < 2 {
		return metricPoint{}, false
	}
	seconds, ok := raw[0].(float64)
	if !ok {
		return metricPoint{}, false
	}
	value, ok := raw[1].(string)
	if !ok {
		return metricPoint{}, false
	}
	return metricPoint{At: int64(seconds * 1000), Value: value}, true
}

// shapeResult 把 Prometheus 结果拍成界面用的曲线数组。
// truncated 表示曲线条数超过上限被截断了。
func shapeResult(data promResultData) (series []metricSeries, truncated bool) {
	series = make([]metricSeries, 0, len(data.Result))
	for _, item := range data.Result {
		one := metricSeries{
			Name: seriesName(item.Metric), Labels: item.Metric,
			Points: make([]metricPoint, 0, len(item.Values)),
		}
		if one.Labels == nil {
			one.Labels = map[string]string{}
		}
		if point, ok := samplePoint(item.Value); ok {
			one.Value = point.Value
			one.Points = append(one.Points, point)
		}
		for _, raw := range item.Values {
			if point, ok := samplePoint(raw); ok {
				one.Points = append(one.Points, point)
			}
		}
		// 范围查询把最后一个点当作当前值，便于表格里一起展示
		if one.Value == "" && len(one.Points) > 0 {
			one.Value = one.Points[len(one.Points)-1].Value
		}
		series = append(series, one)
		if len(series) >= metricMaxSeries {
			truncated = len(data.Result) > metricMaxSeries
			break
		}
	}
	return series, truncated
}

// resolveStep 决定范围查询的步长。
//
// 界面上只让选时间范围不让填 step：步长填小了会直接把 Prometheus 打爆
// （11000 点上限），这里按范围自动算，并保证不超过点数上限。
func resolveStep(rangeSec int64, wantStep int64) (int64, error) {
	if rangeSec <= 0 {
		return 0, fmt.Errorf("结束时间必须晚于开始时间")
	}
	step := wantStep
	if step <= 0 {
		// 目标大约 240 个点，够画图又不至于太密
		step = rangeSec / 240
	}
	if step < 1 {
		step = 1
	}
	// 点数超限就按上限反推步长，而不是直接报错——用户要的是「看趋势」
	if rangeSec/step > metricMaxPoints {
		step = rangeSec / metricMaxPoints
		if rangeSec%metricMaxPoints != 0 {
			step++
		}
	}
	return step, nil
}

// parseMetricTime 解析界面传来的时间：支持 Unix 秒与 RFC3339
func parseMetricTime(raw string) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, fmt.Errorf("时间不能为空")
	}
	if seconds, err := strconv.ParseInt(raw, 10, 64); err == nil {
		return time.Unix(seconds, 0), nil
	}
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, fmt.Errorf("时间格式应为 Unix 秒或 RFC3339，收到 %q", raw)
	}
	return parsed, nil
}

// ---------- 数据源 ----------

type metricSourceReq struct {
	Name        string `json:"name"`
	BaseURL     string `json:"baseUrl"`
	HeaderKey   string `json:"headerKey"`
	HeaderValue string `json:"headerValue"`
	TimeoutSec  int    `json:"timeoutSec"`
	IsDefault   *bool  `json:"isDefault"`
	Enabled     *bool  `json:"enabled"`
	Remark      string `json:"remark"`
}

func (r *metricSourceReq) normalize() error {
	r.Name = strings.TrimSpace(r.Name)
	// 先去空白再去尾斜杠：顺序反了的话 " http://x/ " 里的斜杠会被空白挡住
	r.BaseURL = strings.TrimRight(strings.TrimSpace(r.BaseURL), "/")
	if r.Name == "" {
		return fmt.Errorf("名称不能为空")
	}
	if r.BaseURL == "" {
		return fmt.Errorf("地址不能为空")
	}
	if !strings.HasPrefix(r.BaseURL, "http://") && !strings.HasPrefix(r.BaseURL, "https://") {
		return fmt.Errorf("地址必须以 http:// 或 https:// 开头")
	}
	// 常见误填：把 /api/v1/query 也粘进来了
	if strings.Contains(r.BaseURL, "/api/v1") {
		return fmt.Errorf("只填根地址（如 http://prom:9090），不要带 /api/v1")
	}
	if r.TimeoutSec == 0 {
		r.TimeoutSec = 15
	}
	if r.TimeoutSec < 1 || r.TimeoutSec > 120 {
		return fmt.Errorf("超时需在 1 到 120 秒之间")
	}
	return nil
}

func (h *Handler) ListMetricSources(c *gin.Context) {
	var list []model.MetricSource
	if err := h.DB.Order("id asc").Find(&list).Error; err != nil {
		response.Error(c, "查询数据源失败")
		return
	}
	response.OK(c, list)
}

func (h *Handler) CreateMetricSource(c *gin.Context) {
	var req metricSourceReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "参数错误")
		return
	}
	if err := req.normalize(); err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	source := model.MetricSource{
		Name: req.Name, Type: "prometheus", BaseURL: req.BaseURL,
		HeaderKey: req.HeaderKey, HeaderValue: req.HeaderValue,
		TimeoutSec: req.TimeoutSec, Status: "unknown", Remark: req.Remark,
		CreatedBy: middleware.CurrentUser(c).ID,
	}
	wantEnabled := true
	if req.Enabled != nil {
		wantEnabled = *req.Enabled
	}
	source.Enabled = wantEnabled
	if req.IsDefault != nil {
		source.IsDefault = *req.IsDefault
	}
	if err := h.DB.Create(&source).Error; err != nil {
		response.Error(c, "创建失败")
		return
	}
	// enabled 带 gorm default，Create 后会被回填成 true，显式关掉的要写回去
	if !wantEnabled {
		h.DB.Model(&model.MetricSource{}).Where("id = ?", source.ID).
			Updates(map[string]any{"enabled": false})
		source.Enabled = false
	}
	h.ensureSingleDefaultSource(source)

	// 建完立刻探一次，让人马上知道地址与鉴权对不对
	check := h.checkMetricSource(&source)
	h.DB.First(&source, source.ID)
	response.OK(c, gin.H{"source": source, "check": check})
}

func (h *Handler) UpdateMetricSource(c *gin.Context) {
	var source model.MetricSource
	if err := h.DB.First(&source, idParam(c)).Error; err != nil {
		response.NotFound(c, "数据源不存在")
		return
	}
	var req metricSourceReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "参数错误")
		return
	}
	if err := req.normalize(); err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	updates := map[string]any{
		"name": req.Name, "base_url": req.BaseURL, "header_key": req.HeaderKey,
		"timeout_sec": req.TimeoutSec, "remark": req.Remark,
	}
	if req.HeaderValue != "" {
		updates["header_value"] = req.HeaderValue // 留空表示不修改
	}
	if req.Enabled != nil {
		updates["enabled"] = *req.Enabled
	}
	if req.IsDefault != nil {
		updates["is_default"] = *req.IsDefault
	}
	if err := h.DB.Model(&model.MetricSource{}).Where("id = ?", source.ID).
		Updates(updates).Error; err != nil {
		response.Error(c, "更新失败")
		return
	}
	h.DB.First(&source, source.ID)
	h.ensureSingleDefaultSource(source)
	response.OK(c, source)
}

func (h *Handler) DeleteMetricSource(c *gin.Context) {
	id := idParam(c)
	var count int64
	h.DB.Model(&model.SavedMetricQuery{}).Where("source_id = ?", id).Count(&count)
	if count > 0 {
		response.BadRequest(c, fmt.Sprintf("还有 %d 条常用查询绑在这个数据源上，请先处理", count))
		return
	}
	if err := h.DB.Delete(&model.MetricSource{}, id).Error; err != nil {
		response.Error(c, "删除失败")
		return
	}
	response.OK(c, nil)
}

// ensureSingleDefaultSource 默认数据源只留一个
func (h *Handler) ensureSingleDefaultSource(source model.MetricSource) {
	if !source.IsDefault {
		return
	}
	h.DB.Model(&model.MetricSource{}).
		Where("id <> ? AND is_default = ?", source.ID, true).
		Update("is_default", false)
}

// CheckMetricSource 手动探一次连通性
func (h *Handler) CheckMetricSource(c *gin.Context) {
	source, ok := h.requireMetricSource(c, c.Param("id"))
	if !ok {
		return
	}
	response.OK(c, h.checkMetricSource(source))
}

// checkMetricSource 读 buildinfo 与 tsdb 状态，回写状态。
// 这两个接口只读且便宜，比随便跑一条 PromQL 更适合做连通性判断。
func (h *Handler) checkMetricSource(source *model.MetricSource) gin.H {
	now := time.Now()
	fail := func(detail string) gin.H {
		h.DB.Model(&model.MetricSource{}).Where("id = ?", source.ID).Updates(map[string]any{
			"status": "error", "last_error": truncate(detail, 480), "last_check_at": &now,
		})
		return gin.H{"status": "error", "detail": detail}
	}

	var build struct {
		Version string `json:"version"`
	}
	if _, err := promRequest(*source, "/api/v1/status/buildinfo", nil, &build); err != nil {
		return fail(err.Error())
	}

	// 序列数只是给人一个「这个源里有多少东西」的量感，取不到不算失败。
	// Prometheus 放在 headStats.numSeries，VictoriaMetrics 放在 totalSeries，两种都认。
	var stats struct {
		HeadStats struct {
			NumSeries int64 `json:"numSeries"`
		} `json:"headStats"`
		TotalSeries int64 `json:"totalSeries"`
	}
	_, statsErr := promRequest(*source, "/api/v1/status/tsdb", nil, &stats)
	seriesCount := stats.HeadStats.NumSeries
	if seriesCount == 0 {
		seriesCount = stats.TotalSeries
	}

	h.DB.Model(&model.MetricSource{}).Where("id = ?", source.ID).Updates(map[string]any{
		"status": "healthy", "version": build.Version,
		"series_count": seriesCount,
		"last_error":   "", "last_check_at": &now,
	})
	detail := "Prometheus API " + build.Version
	if statsErr == nil {
		detail += fmt.Sprintf("，约 %d 条序列", seriesCount)
	} else {
		detail += "（读不到 tsdb 状态：" + statsErr.Error() + "）"
	}
	return gin.H{
		"status": "healthy", "version": build.Version,
		"seriesCount": seriesCount, "detail": detail,
	}
}

// requireMetricSource 取数据源，顺带挡掉停用的
func (h *Handler) requireMetricSource(c *gin.Context, raw string) (*model.MetricSource, bool) {
	id, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || id <= 0 {
		response.BadRequest(c, "请选择数据源")
		return nil, false
	}
	var source model.MetricSource
	if err := h.DB.First(&source, id).Error; err != nil {
		response.NotFound(c, "数据源不存在")
		return nil, false
	}
	if !source.Enabled {
		response.BadRequest(c, "数据源已停用")
		return nil, false
	}
	return &source, true
}

// ---------- 查询 ----------

// requireExpr 取并校验 PromQL
func requireExpr(c *gin.Context) (string, bool) {
	expr := strings.TrimSpace(c.Query("query"))
	if expr == "" {
		response.BadRequest(c, "查询表达式不能为空")
		return "", false
	}
	if len(expr) > metricExprMaxLen {
		response.BadRequest(c, fmt.Sprintf("表达式超过 %d 字符上限", metricExprMaxLen))
		return "", false
	}
	return expr, true
}

// QueryMetricInstant 即时查询：某个时刻的值，结果按表格展示
func (h *Handler) QueryMetricInstant(c *gin.Context) {
	source, ok := h.requireMetricSource(c, c.Query("sourceId"))
	if !ok {
		return
	}
	expr, ok := requireExpr(c)
	if !ok {
		return
	}

	query := url.Values{}
	query.Set("query", expr)
	at := time.Now()
	if raw := strings.TrimSpace(c.Query("time")); raw != "" {
		parsed, err := parseMetricTime(raw)
		if err != nil {
			response.BadRequest(c, err.Error())
			return
		}
		at = parsed
	}
	query.Set("time", strconv.FormatInt(at.Unix(), 10))

	started := time.Now()
	var data promResultData
	warnings, err := promRequest(*source, "/api/v1/query", query, &data)
	if err != nil {
		response.BadRequest(c, "查询失败: "+err.Error())
		return
	}
	series, truncated := shapeResult(data)
	response.OK(c, gin.H{
		"resultType": data.ResultType, "series": series,
		"total": len(data.Result), "truncated": truncated,
		"warnings": warnings, "costMs": time.Since(started).Milliseconds(),
		"queriedAt": at.Format(time.RFC3339),
	})
}

// QueryMetricRange 范围查询：一段时间的曲线，步长按范围自动算
func (h *Handler) QueryMetricRange(c *gin.Context) {
	source, ok := h.requireMetricSource(c, c.Query("sourceId"))
	if !ok {
		return
	}
	expr, ok := requireExpr(c)
	if !ok {
		return
	}

	end := time.Now()
	if raw := strings.TrimSpace(c.Query("end")); raw != "" {
		parsed, err := parseMetricTime(raw)
		if err != nil {
			response.BadRequest(c, err.Error())
			return
		}
		end = parsed
	}
	start := end.Add(-time.Hour)
	if raw := strings.TrimSpace(c.Query("start")); raw != "" {
		parsed, err := parseMetricTime(raw)
		if err != nil {
			response.BadRequest(c, err.Error())
			return
		}
		start = parsed
	}

	wantStep := int64(0)
	if raw := strings.TrimSpace(c.Query("step")); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed <= 0 {
			response.BadRequest(c, "步长必须是正整数秒")
			return
		}
		wantStep = parsed
	}
	step, err := resolveStep(int64(end.Sub(start).Seconds()), wantStep)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	query := url.Values{}
	query.Set("query", expr)
	query.Set("start", strconv.FormatInt(start.Unix(), 10))
	query.Set("end", strconv.FormatInt(end.Unix(), 10))
	query.Set("step", strconv.FormatInt(step, 10))

	started := time.Now()
	var data promResultData
	warnings, err := promRequest(*source, "/api/v1/query_range", query, &data)
	if err != nil {
		response.BadRequest(c, "查询失败: "+err.Error())
		return
	}
	series, truncated := shapeResult(data)
	response.OK(c, gin.H{
		"resultType": data.ResultType, "series": series,
		"total": len(data.Result), "truncated": truncated,
		"warnings": warnings, "costMs": time.Since(started).Milliseconds(),
		"start": start.Format(time.RFC3339), "end": end.Format(time.RFC3339), "step": step,
	})
}

// ListMetricNames 指标名补全。keyword 为空时返回前若干个，供界面下拉。
func (h *Handler) ListMetricNames(c *gin.Context) {
	source, ok := h.requireMetricSource(c, c.Query("sourceId"))
	if !ok {
		return
	}
	var names []string
	if _, err := promRequest(*source, "/api/v1/label/__name__/values", nil, &names); err != nil {
		response.Error(c, "读取指标名失败: "+err.Error())
		return
	}

	keyword := strings.ToLower(strings.TrimSpace(c.Query("keyword")))
	out := make([]string, 0, 200)
	for _, name := range names {
		if keyword != "" && !strings.Contains(strings.ToLower(name), keyword) {
			continue
		}
		out = append(out, name)
		if len(out) >= 200 {
			break
		}
	}
	response.OK(c, gin.H{"names": out, "total": len(names), "truncated": len(out) >= 200})
}

// ---------- 常用查询 ----------

type savedQueryReq struct {
	Name      string `json:"name"`
	SourceID  uint   `json:"sourceId"`
	Expr      string `json:"expr"`
	RangeMode *bool  `json:"rangeMode"`
	Remark    string `json:"remark"`
}

func (h *Handler) ListSavedMetricQueries(c *gin.Context) {
	var list []model.SavedMetricQuery
	if err := h.DB.Order("id asc").Find(&list).Error; err != nil {
		response.Error(c, "查询常用查询失败")
		return
	}
	response.OK(c, list)
}

func (h *Handler) CreateSavedMetricQuery(c *gin.Context) {
	var req savedQueryReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "参数错误")
		return
	}
	req.Name, req.Expr = strings.TrimSpace(req.Name), strings.TrimSpace(req.Expr)
	if req.Name == "" || req.Expr == "" {
		response.BadRequest(c, "名称与表达式都不能为空")
		return
	}
	if len(req.Expr) > metricExprMaxLen {
		response.BadRequest(c, fmt.Sprintf("表达式超过 %d 字符上限", metricExprMaxLen))
		return
	}

	item := model.SavedMetricQuery{
		Name: req.Name, SourceID: req.SourceID, Expr: req.Expr,
		Remark: req.Remark, CreatedBy: middleware.CurrentUser(c).ID,
	}
	wantRange := true
	if req.RangeMode != nil {
		wantRange = *req.RangeMode
	}
	item.RangeMode = wantRange
	if err := h.DB.Create(&item).Error; err != nil {
		response.Error(c, "保存失败")
		return
	}
	if !wantRange {
		h.DB.Model(&model.SavedMetricQuery{}).Where("id = ?", item.ID).
			Updates(map[string]any{"range_mode": false})
		item.RangeMode = false
	}
	response.OK(c, item)
}

func (h *Handler) DeleteSavedMetricQuery(c *gin.Context) {
	if err := h.DB.Delete(&model.SavedMetricQuery{}, idParam(c)).Error; err != nil {
		response.Error(c, "删除失败")
		return
	}
	response.OK(c, nil)
}
