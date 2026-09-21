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
	// traceRespLimit 读 Jaeger 响应的上限。一条大 trace 几万个 span 也就几十兆
	traceRespLimit = 32 << 20
	// traceDefaultLimit 列表默认取多少条 trace
	traceDefaultLimit = 20
	// traceMaxLimit 列表条数上限
	traceMaxLimit = 200
	// traceSpanMaxCount 单条 trace 最多返回多少个 span 给界面。
	// 上万 span 的 trace 画瀑布图没意义，截断并告知比卡死浏览器好
	traceSpanMaxCount = 800
)

// ---------- 与 Jaeger 通信 ----------

// jaegerRequest 调一次 Jaeger Query 的 HTTP API（/api/... 那套，UI 用的就是它）
func jaegerRequest(source model.TraceSource, path string, query url.Values, out any) error {
	base := strings.TrimRight(strings.TrimSpace(source.BaseURL), "/")
	if base == "" {
		return fmt.Errorf("数据源未配置地址")
	}
	target := base + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}

	req, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if source.HeaderKey != "" {
		req.Header.Set(source.HeaderKey, source.HeaderValue)
	}

	timeout := time.Duration(source.TimeoutSec) * time.Second
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, traceRespLimit))
	if err != nil {
		return err
	}

	// Jaeger 的错误体是 {"data":null,"errors":[{"code":..,"msg":".."}]}，
	// 而且**有时带着 HTTP 200**（比如 traceID 不存在），所以两边都得看
	var envelope struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Code int    `json:"code"`
			Msg  string `json:"msg"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return fmt.Errorf("返回不是 Jaeger 格式（HTTP %d）: %s",
			resp.StatusCode, truncate(strings.TrimSpace(string(body)), 160))
	}
	if len(envelope.Errors) > 0 {
		first := envelope.Errors[0]
		return fmt.Errorf("Jaeger 返回错误 %d: %s", first.Code, first.Msg)
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("Jaeger 返回 HTTP %d", resp.StatusCode)
	}
	if out == nil || len(envelope.Data) == 0 {
		return nil
	}
	return json.Unmarshal(envelope.Data, out)
}

// ---------- Jaeger 原始结构 ----------

type jaegerKeyValue struct {
	Key   string `json:"key"`
	Type  string `json:"type"`
	Value any    `json:"value"`
}

type jaegerSpan struct {
	TraceID       string `json:"traceID"`
	SpanID        string `json:"spanID"`
	OperationName string `json:"operationName"`
	// StartTime / Duration 单位都是微秒
	StartTime  int64            `json:"startTime"`
	Duration   int64            `json:"duration"`
	ProcessID  string           `json:"processID"`
	Tags       []jaegerKeyValue `json:"tags"`
	References []struct {
		RefType string `json:"refType"`
		SpanID  string `json:"spanID"`
	} `json:"references"`
}

type jaegerTrace struct {
	TraceID   string       `json:"traceID"`
	Spans     []jaegerSpan `json:"spans"`
	Processes map[string]struct {
		ServiceName string           `json:"serviceName"`
		Tags        []jaegerKeyValue `json:"tags"`
	} `json:"processes"`
}

// ---------- 整形（纯函数） ----------

// traceSummary trace 列表里的一行
type traceSummary struct {
	TraceID string `json:"traceId"`
	// RootService / RootOperation 入口 span 的服务与操作名
	RootService   string `json:"rootService"`
	RootOperation string `json:"rootOperation"`
	SpanCount     int    `json:"spanCount"`
	ServiceCount  int    `json:"serviceCount"`
	// DurationMs 整条 trace 的跨度（最早开始到最晚结束）
	DurationMs float64 `json:"durationMs"`
	StartAt    int64   `json:"startAt"` // 毫秒时间戳
	// ErrorCount 出错的 span 数量，一眼看出这条链路有没有问题
	ErrorCount int      `json:"errorCount"`
	Services   []string `json:"services"`
}

// spanView 瀑布图上的一个 span
type spanView struct {
	SpanID    string `json:"spanId"`
	ParentID  string `json:"parentId"`
	Service   string `json:"service"`
	Operation string `json:"operation"`
	// OffsetMs 相对整条 trace 起点的偏移，界面按它画左边距
	OffsetMs   float64           `json:"offsetMs"`
	DurationMs float64           `json:"durationMs"`
	Depth      int               `json:"depth"`
	Error      bool              `json:"error"`
	Tags       map[string]string `json:"tags"`
}

// spanError 判断一个 span 算不算出错。
//
// 三种约定都认：OpenTracing 的 error=true、HTTP 语义里 5xx、
// 以及 OTel 的 otel.status_code=ERROR。
func spanError(tags map[string]string) bool {
	if tags["error"] == "true" {
		return true
	}
	if strings.EqualFold(tags["otel.status_code"], "ERROR") {
		return true
	}
	for _, key := range []string{"http.status_code", "http.response.status_code"} {
		if code, err := strconv.Atoi(tags[key]); err == nil && code >= 500 {
			return true
		}
	}
	return false
}

// flattenTags 把 Jaeger 的 kv 列表拍成 map，值统一转字符串
func flattenTags(items []jaegerKeyValue) map[string]string {
	tags := make(map[string]string, len(items))
	for _, item := range items {
		switch value := item.Value.(type) {
		case string:
			tags[item.Key] = value
		case bool:
			tags[item.Key] = strconv.FormatBool(value)
		case float64:
			// 整数别显示成 1.000000
			if value == float64(int64(value)) {
				tags[item.Key] = strconv.FormatInt(int64(value), 10)
			} else {
				tags[item.Key] = strconv.FormatFloat(value, 'f', -1, 64)
			}
		case nil:
			tags[item.Key] = ""
		default:
			tags[item.Key] = fmt.Sprint(value)
		}
	}
	return tags
}

// summarizeTrace 把一条 trace 压成列表里的一行
func summarizeTrace(trace jaegerTrace) traceSummary {
	out := traceSummary{TraceID: trace.TraceID, SpanCount: len(trace.Spans), Services: []string{}}
	if len(trace.Spans) == 0 {
		return out
	}

	serviceOf := func(processID string) string {
		if p, ok := trace.Processes[processID]; ok {
			return p.ServiceName
		}
		return processID
	}

	ids := make(map[string]struct{}, len(trace.Spans))
	for _, span := range trace.Spans {
		ids[span.SpanID] = struct{}{}
	}

	var minStart, maxEnd int64
	services := map[string]struct{}{}
	root := jaegerSpan{}
	for i, span := range trace.Spans {
		if i == 0 || span.StartTime < minStart {
			minStart = span.StartTime
		}
		if end := span.StartTime + span.Duration; i == 0 || end > maxEnd {
			maxEnd = end
		}
		services[serviceOf(span.ProcessID)] = struct{}{}
		if spanError(flattenTags(span.Tags)) {
			out.ErrorCount++
		}
		// 入口 span：没有父 span（或父不在本 trace 里），取最早的那个
		if parentOf(span, ids) == "" {
			if root.SpanID == "" || span.StartTime < root.StartTime {
				root = span
			}
		}
	}
	if root.SpanID == "" {
		// 全都有父（数据不完整）时退化成最早的 span
		root = trace.Spans[0]
		for _, span := range trace.Spans {
			if span.StartTime < root.StartTime {
				root = span
			}
		}
	}

	out.RootService, out.RootOperation = serviceOf(root.ProcessID), root.OperationName
	out.DurationMs = float64(maxEnd-minStart) / 1000
	out.StartAt = minStart / 1000
	out.ServiceCount = len(services)
	for name := range services {
		out.Services = append(out.Services, name)
	}
	sort.Strings(out.Services)
	return out
}

// parentOf 取父 span ID。只认 CHILD_OF，且父必须在同一条 trace 里
func parentOf(span jaegerSpan, ids map[string]struct{}) string {
	for _, ref := range span.References {
		if ref.RefType != "CHILD_OF" {
			continue
		}
		if _, ok := ids[ref.SpanID]; ok {
			return ref.SpanID
		}
	}
	return ""
}

// buildSpanTree 把 span 摆成瀑布图顺序：按父子关系深度优先，同层按开始时间。
// 返回值已经带好 depth 与相对偏移，前端只管画条。
func buildSpanTree(trace jaegerTrace) (views []spanView, truncated bool) {
	views = make([]spanView, 0, len(trace.Spans))
	if len(trace.Spans) == 0 {
		return views, false
	}

	serviceOf := func(processID string) string {
		if p, ok := trace.Processes[processID]; ok {
			return p.ServiceName
		}
		return processID
	}
	ids := make(map[string]struct{}, len(trace.Spans))
	for _, span := range trace.Spans {
		ids[span.SpanID] = struct{}{}
	}

	minStart := trace.Spans[0].StartTime
	for _, span := range trace.Spans {
		if span.StartTime < minStart {
			minStart = span.StartTime
		}
	}

	children := map[string][]jaegerSpan{}
	roots := make([]jaegerSpan, 0, 1)
	for _, span := range trace.Spans {
		parent := parentOf(span, ids)
		if parent == "" {
			roots = append(roots, span)
			continue
		}
		children[parent] = append(children[parent], span)
	}
	if len(roots) == 0 {
		// 没有入口 span（采样丢了父）时全部按平铺处理，别把数据藏起来
		roots = trace.Spans
		children = map[string][]jaegerSpan{}
	}
	byStart := func(list []jaegerSpan) {
		sort.SliceStable(list, func(i, j int) bool { return list[i].StartTime < list[j].StartTime })
	}
	byStart(roots)
	for key := range children {
		byStart(children[key])
	}

	// 显式栈做深度优先，避免深链路把调用栈压爆。
	// 用「祖先链」而不是全局 visited 判重：Jaeger 里同一个 spanID 出现两次是常态
	// （客户端与服务端共享 span ID，或同一段被重复上报），那种情况两条都要画出来，
	// 只有「自己成了自己的祖先」才是真环，必须断开。
	type frame struct {
		span   jaegerSpan
		parent *frame
		depth  int
	}
	isAncestor := func(f *frame, spanID string) bool {
		for cur := f; cur != nil; cur = cur.parent {
			if cur.span.SpanID == spanID {
				return true
			}
		}
		return false
	}
	stack := make([]*frame, 0, len(trace.Spans))
	for i := len(roots) - 1; i >= 0; i-- {
		stack = append(stack, &frame{span: roots[i], depth: 0})
	}
	for len(stack) > 0 {
		current := stack[len(stack)-1]
		stack = stack[:len(stack)-1]

		parentID := ""
		if current.parent != nil {
			parentID = current.parent.span.SpanID
		}
		tags := flattenTags(current.span.Tags)
		views = append(views, spanView{
			SpanID: current.span.SpanID, ParentID: parentID,
			Service: serviceOf(current.span.ProcessID), Operation: current.span.OperationName,
			OffsetMs:   float64(current.span.StartTime-minStart) / 1000,
			DurationMs: float64(current.span.Duration) / 1000,
			Depth:      current.depth, Error: spanError(tags), Tags: tags,
		})
		if len(views) >= traceSpanMaxCount {
			return views, len(trace.Spans) > traceSpanMaxCount
		}
		kids := children[current.span.SpanID]
		for i := len(kids) - 1; i >= 0; i-- {
			if isAncestor(current, kids[i].SpanID) {
				continue // 环形引用（脏数据），断开这一条边
			}
			stack = append(stack, &frame{span: kids[i], parent: current, depth: current.depth + 1})
		}
	}
	return views, false
}

// ---------- 数据源 ----------

type traceSourceReq struct {
	Name        string `json:"name"`
	BaseURL     string `json:"baseUrl"`
	HeaderKey   string `json:"headerKey"`
	HeaderValue string `json:"headerValue"`
	TimeoutSec  int    `json:"timeoutSec"`
	IsDefault   *bool  `json:"isDefault"`
	Enabled     *bool  `json:"enabled"`
	Remark      string `json:"remark"`
}

func (r *traceSourceReq) normalize() error {
	r.Name = strings.TrimSpace(r.Name)
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
	if strings.Contains(r.BaseURL, "/api/traces") || strings.Contains(r.BaseURL, "/api/services") {
		return fmt.Errorf("只填根地址（如 http://jaeger:16686），不要带 /api/...")
	}
	if r.TimeoutSec == 0 {
		r.TimeoutSec = 20
	}
	if r.TimeoutSec < 1 || r.TimeoutSec > 120 {
		return fmt.Errorf("超时需在 1 到 120 秒之间")
	}
	return nil
}

func (h *Handler) ListTraceSources(c *gin.Context) {
	var list []model.TraceSource
	if err := h.DB.Order("id asc").Find(&list).Error; err != nil {
		response.Error(c, "查询数据源失败")
		return
	}
	response.OK(c, list)
}

func (h *Handler) CreateTraceSource(c *gin.Context) {
	var req traceSourceReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "参数错误")
		return
	}
	if err := req.normalize(); err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	source := model.TraceSource{
		Name: req.Name, Type: "jaeger", BaseURL: req.BaseURL,
		HeaderKey: req.HeaderKey, HeaderValue: h.sealSecret(req.HeaderValue),
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
	if !wantEnabled {
		h.DB.Model(&model.TraceSource{}).Where("id = ?", source.ID).
			Updates(map[string]any{"enabled": false})
		source.Enabled = false
	}
	h.ensureSingleDefaultTraceSource(source)

	check := h.checkTraceSource(&source)
	h.DB.First(&source, source.ID)
	response.OK(c, gin.H{"source": source, "check": check})
}

func (h *Handler) UpdateTraceSource(c *gin.Context) {
	var source model.TraceSource
	if err := h.DB.First(&source, idParam(c)).Error; err != nil {
		response.NotFound(c, "数据源不存在")
		return
	}
	var req traceSourceReq
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
		updates["header_value"] = h.sealSecret(req.HeaderValue) // 留空表示不修改
	}
	if req.Enabled != nil {
		updates["enabled"] = *req.Enabled
	}
	if req.IsDefault != nil {
		updates["is_default"] = *req.IsDefault
	}
	if err := h.DB.Model(&model.TraceSource{}).Where("id = ?", source.ID).
		Updates(updates).Error; err != nil {
		response.Error(c, "更新失败")
		return
	}
	h.DB.First(&source, source.ID)
	h.ensureSingleDefaultTraceSource(source)
	response.OK(c, source)
}

func (h *Handler) DeleteTraceSource(c *gin.Context) {
	if err := h.DB.Delete(&model.TraceSource{}, idParam(c)).Error; err != nil {
		response.Error(c, "删除失败")
		return
	}
	response.OK(c, nil)
}

func (h *Handler) ensureSingleDefaultTraceSource(source model.TraceSource) {
	if !source.IsDefault {
		return
	}
	h.DB.Model(&model.TraceSource{}).
		Where("id <> ? AND is_default = ?", source.ID, true).
		Update("is_default", false)
}

// CheckTraceSource 手动探一次连通性
func (h *Handler) CheckTraceSource(c *gin.Context) {
	source, ok := h.requireTraceSource(c, c.Param("id"))
	if !ok {
		return
	}
	response.OK(c, h.checkTraceSource(source))
}

// checkTraceSource 用服务列表做连通性判断：它便宜，而且正是查询的第一步
func (h *Handler) checkTraceSource(source *model.TraceSource) gin.H {
	now := time.Now()
	services := []string{}
	// 新建后立刻探活传进来的是刚落库的密文，这里再解一次（对明文是原样返回）
	opened, err := h.openSecret("链路源请求头", source.HeaderValue)
	if err != nil {
		h.DB.Model(&model.TraceSource{}).Where("id = ?", source.ID).Updates(map[string]any{
			"status": "error", "last_error": truncate(err.Error(), 480), "last_check_at": &now,
		})
		return gin.H{"status": "error", "detail": err.Error()}
	}
	probe := *source
	probe.HeaderValue = opened
	if err := jaegerRequest(probe, "/api/services", nil, &services); err != nil {
		h.DB.Model(&model.TraceSource{}).Where("id = ?", source.ID).Updates(map[string]any{
			"status": "error", "last_error": truncate(err.Error(), 480), "last_check_at": &now,
		})
		return gin.H{"status": "error", "detail": err.Error()}
	}

	h.DB.Model(&model.TraceSource{}).Where("id = ?", source.ID).Updates(map[string]any{
		"status": "healthy", "service_count": len(services),
		"last_error": "", "last_check_at": &now,
	})
	detail := fmt.Sprintf("可用，上报过 %d 个服务", len(services))
	if len(services) == 0 {
		detail = "接口通了，但一个服务都没有 —— 可能还没有应用把链路数据发进来"
	}
	return gin.H{"status": "healthy", "serviceCount": len(services), "detail": detail}
}

func (h *Handler) requireTraceSource(c *gin.Context, raw string) (*model.TraceSource, bool) {
	id, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || id <= 0 {
		response.BadRequest(c, "请选择数据源")
		return nil, false
	}
	var source model.TraceSource
	if err := h.DB.First(&source, id).Error; err != nil {
		response.NotFound(c, "数据源不存在")
		return nil, false
	}
	if !source.Enabled {
		response.BadRequest(c, "数据源已停用")
		return nil, false
	}
	// 鉴权头在库里是密文，统一在这个漏斗里解开
	header, err := h.openSecret("链路源请求头", source.HeaderValue)
	if err != nil {
		response.BadRequest(c, err.Error())
		return nil, false
	}
	source.HeaderValue = header
	return &source, true
}

// ---------- 查询 ----------

// ListTraceServices 服务列表；带 service 参数时返回该服务的操作名
func (h *Handler) ListTraceServices(c *gin.Context) {
	source, ok := h.requireTraceSource(c, c.Query("sourceId"))
	if !ok {
		return
	}
	path := "/api/services"
	if service := strings.TrimSpace(c.Query("service")); service != "" {
		path = "/api/services/" + url.PathEscape(service) + "/operations"
	}
	values := []string{}
	if err := jaegerRequest(*source, path, nil, &values); err != nil {
		response.Error(c, "读取失败: "+err.Error())
		return
	}
	sort.Strings(values)
	response.OK(c, gin.H{"values": values, "total": len(values)})
}

// SearchTraces 按服务/操作/耗时/时间范围搜 trace
func (h *Handler) SearchTraces(c *gin.Context) {
	source, ok := h.requireTraceSource(c, c.Query("sourceId"))
	if !ok {
		return
	}
	service := strings.TrimSpace(c.Query("service"))
	if service == "" {
		response.BadRequest(c, "请选择服务")
		return
	}
	start, end, err := logTimeRange(c) // 与日志查询共用时间解析
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	limit := traceDefaultLimit
	if raw := strings.TrimSpace(c.Query("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > traceMaxLimit {
			response.BadRequest(c, fmt.Sprintf("条数需在 1 到 %d 之间", traceMaxLimit))
			return
		}
		limit = parsed
	}

	query := url.Values{}
	query.Set("service", service)
	if operation := strings.TrimSpace(c.Query("operation")); operation != "" {
		query.Set("operation", operation)
	}
	// Jaeger 的时间参数是微秒
	query.Set("start", strconv.FormatInt(start.UnixMicro(), 10))
	query.Set("end", strconv.FormatInt(end.UnixMicro(), 10))
	query.Set("limit", strconv.Itoa(limit))
	if raw := strings.TrimSpace(c.Query("minDurationMs")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 0 {
			response.BadRequest(c, "最小耗时必须是非负整数毫秒")
			return
		}
		if parsed > 0 {
			query.Set("minDuration", strconv.Itoa(parsed)+"ms")
		}
	}
	if tags := strings.TrimSpace(c.Query("tags")); tags != "" {
		// 原样转发，Jaeger 要的是 {"k":"v"} 形式的 JSON
		if !json.Valid([]byte(tags)) {
			response.BadRequest(c, `标签条件必须是 JSON 对象，例如 {"http.method":"POST"}`)
			return
		}
		query.Set("tags", tags)
	}

	started := time.Now()
	var traces []jaegerTrace
	if err := jaegerRequest(*source, "/api/traces", query, &traces); err != nil {
		response.BadRequest(c, "查询失败: "+err.Error())
		return
	}

	items := make([]traceSummary, 0, len(traces))
	for _, trace := range traces {
		items = append(items, summarizeTrace(trace))
	}
	// Jaeger 返回顺序不稳定，统一按开始时间倒序：排查时先看最近的
	sort.SliceStable(items, func(i, j int) bool { return items[i].StartAt > items[j].StartAt })
	if c.Query("errorOnly") == "true" {
		filtered := make([]traceSummary, 0, len(items))
		for _, item := range items {
			if item.ErrorCount > 0 {
				filtered = append(filtered, item)
			}
		}
		items = filtered
	}

	response.OK(c, gin.H{
		"items": items, "total": len(items), "limit": limit,
		"start": start.Format(time.RFC3339), "end": end.Format(time.RFC3339),
		"costMs": time.Since(started).Milliseconds(),
	})
}

// GetTrace 取单条 trace，返回瀑布图用的 span 序列
func (h *Handler) GetTrace(c *gin.Context) {
	source, ok := h.requireTraceSource(c, c.Query("sourceId"))
	if !ok {
		return
	}
	traceID := strings.TrimSpace(c.Param("traceId"))
	if traceID == "" {
		response.BadRequest(c, "TraceID 不能为空")
		return
	}

	started := time.Now()
	var traces []jaegerTrace
	if err := jaegerRequest(*source, "/api/traces/"+url.PathEscape(traceID), nil, &traces); err != nil {
		response.BadRequest(c, "查询失败: "+err.Error())
		return
	}
	if len(traces) == 0 {
		response.NotFound(c, "这个 TraceID 查不到数据：可能已过保留期，或没被采样")
		return
	}

	summary := summarizeTrace(traces[0])
	spans, truncated := buildSpanTree(traces[0])
	response.OK(c, gin.H{
		"summary": summary, "spans": spans, "truncated": truncated,
		"costMs": time.Since(started).Milliseconds(),
	})
}
