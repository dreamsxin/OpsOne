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
	// logQueryMaxLen LogQL 长度上限
	logQueryMaxLen = 4000
	// logRespLimit 读 Loki 响应的上限
	logRespLimit = 32 << 20
	// logDefaultLimit 默认取多少条日志
	logDefaultLimit = 200
	// logMaxLimit 单次最多取多少条。再多界面滚不动，也没人真去翻
	logMaxLimit = 5000
	// logLineMaxLen 单行日志超过这个长度就截断，防止一行 JSON 把页面撑爆
	logLineMaxLen = 8000
)

// ---------- 与 Loki 通信 ----------

// lokiRequest 调一次 Loki 的 HTTP API。
//
// Loki 的成功响应是 Prometheus 风格的信封，但**报错经常是纯文本**
// （比如 LogQL 语法错就直接返回一行 parse error），所以不能只按 JSON 解。
func lokiRequest(source model.LogSource, path string, query url.Values, out any) error {
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
	// 多租户 Loki 必须带租户头，单机模式带了也无害
	if tenant := strings.TrimSpace(source.Tenant); tenant != "" {
		req.Header.Set("X-Scope-OrgID", tenant)
	}

	timeout := time.Duration(source.TimeoutSec) * time.Second
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, logRespLimit))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 400 {
		// Loki 的错误正文通常就是给人看的一行话，原样带出去最有用
		detail := strings.TrimSpace(string(body))
		if detail == "" {
			return fmt.Errorf("Loki 返回 HTTP %d", resp.StatusCode)
		}
		return fmt.Errorf("Loki 返回 HTTP %d: %s", resp.StatusCode, truncate(detail, 300))
	}
	if out == nil {
		return nil
	}
	var envelope struct {
		Status string          `json:"status"`
		Data   json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return fmt.Errorf("返回不是 Loki 格式（HTTP %d）: %s",
			resp.StatusCode, truncate(strings.TrimSpace(string(body)), 160))
	}
	if envelope.Status != "" && envelope.Status != "success" {
		return fmt.Errorf("Loki 返回 status=%s", envelope.Status)
	}
	if len(envelope.Data) == 0 {
		return nil
	}
	return json.Unmarshal(envelope.Data, out)
}

// ---------- 结果整形（纯函数） ----------

// lokiStreamData query_range 返回的日志流。resultType 为 streams
type lokiStreamData struct {
	ResultType string `json:"resultType"`
	Result     []struct {
		Stream map[string]string `json:"stream"`
		// Values 每项是 ["<纳秒时间戳>", "日志行"]。
		// 这里用 []any 而不是 []string：聚合表达式返回的 matrix 里时间戳是数字，
		// 若声明成 string 会在解码阶段直接报 json 错，拿不到 resultType 也就没法
		// 给出「这是指标不是日志」这种有用提示。
		Values [][]any `json:"values"`
	} `json:"result"`
	Stats struct {
		Summary struct {
			TotalLinesProcessed int64   `json:"totalLinesProcessed"`
			TotalBytesProcessed int64   `json:"totalBytesProcessed"`
			ExecTime            float64 `json:"execTime"`
		} `json:"summary"`
	} `json:"stats"`
}

// logRow 界面上的一行日志
type logRow struct {
	// At 毫秒时间戳，前端格式化用
	At int64 `json:"at"`
	// Nano 原始纳秒时间戳，同一毫秒内多条日志靠它区分顺序
	Nano   string            `json:"nano"`
	Line   string            `json:"line"`
	Labels map[string]string `json:"labels"`
	// Stream 标签拼出来的一句话，界面上折叠显示
	Stream string `json:"stream"`
	// Truncated 这一行太长被截断了
	Truncated bool `json:"truncated"`
}

// streamLabel 把流标签拼成 k="v" 形式，按键排序保证稳定
func streamLabel(labels map[string]string) string {
	parts := make([]string, 0, len(labels))
	for key, value := range labels {
		parts = append(parts, fmt.Sprintf("%s=%q", key, value))
	}
	sort.Strings(parts)
	return "{" + strings.Join(parts, ",") + "}"
}

// shapeLogRows 把按流分组的结果拍平成按时间排序的行。
//
// Loki 是每个流各自有序，合起来并不有序；界面要的是「一条时间线」，
// 所以这里做一次归并排序（newestFirst 与请求的 direction 对齐）。
//
// truncated 的判定包含「正好等于 limit」：Loki 是在服务端按 limit 截的，
// 拿到满额结果就说明很可能还有更多，得让人知道要收窄条件。
func shapeLogRows(data lokiStreamData, newestFirst bool, limit int) (rows []logRow, truncated bool) {
	rows = make([]logRow, 0, 64)
	for _, stream := range data.Result {
		label := streamLabel(stream.Stream)
		for _, pair := range stream.Values {
			if len(pair) < 2 {
				continue
			}
			nanoStr, ok := pair[0].(string)
			if !ok {
				continue
			}
			line, ok := pair[1].(string)
			if !ok {
				continue
			}
			nano, err := strconv.ParseInt(nanoStr, 10, 64)
			if err != nil {
				continue
			}
			cut := false
			if len(line) > logLineMaxLen {
				line, cut = line[:logLineMaxLen], true
			}
			rows = append(rows, logRow{
				At: nano / 1e6, Nano: nanoStr, Line: line,
				Labels: stream.Stream, Stream: label, Truncated: cut,
			})
		}
	}

	sort.SliceStable(rows, func(i, j int) bool {
		left, right := rows[i].Nano, rows[j].Nano
		if len(left) != len(right) {
			// 纳秒时间戳等长时按字典序即时间序，长度不同先比长度
			if newestFirst {
				return len(left) > len(right)
			}
			return len(left) < len(right)
		}
		if newestFirst {
			return left > right
		}
		return left < right
	})

	if limit > 0 && len(rows) >= limit {
		return rows[:limit], true
	}
	return rows, false
}

// ---------- 数据源 ----------

type logSourceReq struct {
	Name        string `json:"name"`
	BaseURL     string `json:"baseUrl"`
	Tenant      string `json:"tenant"`
	HeaderKey   string `json:"headerKey"`
	HeaderValue string `json:"headerValue"`
	TimeoutSec  int    `json:"timeoutSec"`
	IsDefault   *bool  `json:"isDefault"`
	Enabled     *bool  `json:"enabled"`
	Remark      string `json:"remark"`
}

func (r *logSourceReq) normalize() error {
	r.Name = strings.TrimSpace(r.Name)
	r.BaseURL = strings.TrimRight(strings.TrimSpace(r.BaseURL), "/")
	r.Tenant = strings.TrimSpace(r.Tenant)
	if r.Name == "" {
		return fmt.Errorf("名称不能为空")
	}
	if r.BaseURL == "" {
		return fmt.Errorf("地址不能为空")
	}
	if !strings.HasPrefix(r.BaseURL, "http://") && !strings.HasPrefix(r.BaseURL, "https://") {
		return fmt.Errorf("地址必须以 http:// 或 https:// 开头")
	}
	// 常见误填：把查询路径也粘进来了
	if strings.Contains(r.BaseURL, "/loki/api") {
		return fmt.Errorf("只填根地址（如 http://loki:3100），不要带 /loki/api/...")
	}
	if r.TimeoutSec == 0 {
		r.TimeoutSec = 30
	}
	if r.TimeoutSec < 1 || r.TimeoutSec > 120 {
		return fmt.Errorf("超时需在 1 到 120 秒之间")
	}
	return nil
}

func (h *Handler) ListLogSources(c *gin.Context) {
	var list []model.LogSource
	if err := h.DB.Order("id asc").Find(&list).Error; err != nil {
		response.Error(c, "查询数据源失败")
		return
	}
	response.OK(c, list)
}

func (h *Handler) CreateLogSource(c *gin.Context) {
	var req logSourceReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "参数错误")
		return
	}
	if err := req.normalize(); err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	source := model.LogSource{
		Name: req.Name, Type: "loki", BaseURL: req.BaseURL, Tenant: req.Tenant,
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
	if !wantEnabled {
		h.DB.Model(&model.LogSource{}).Where("id = ?", source.ID).
			Updates(map[string]any{"enabled": false})
		source.Enabled = false
	}
	h.ensureSingleDefaultLogSource(source)

	check := h.checkLogSource(&source)
	h.DB.First(&source, source.ID)
	response.OK(c, gin.H{"source": source, "check": check})
}

func (h *Handler) UpdateLogSource(c *gin.Context) {
	var source model.LogSource
	if err := h.DB.First(&source, idParam(c)).Error; err != nil {
		response.NotFound(c, "数据源不存在")
		return
	}
	var req logSourceReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "参数错误")
		return
	}
	if err := req.normalize(); err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	updates := map[string]any{
		"name": req.Name, "base_url": req.BaseURL, "tenant": req.Tenant,
		"header_key": req.HeaderKey, "timeout_sec": req.TimeoutSec, "remark": req.Remark,
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
	if err := h.DB.Model(&model.LogSource{}).Where("id = ?", source.ID).
		Updates(updates).Error; err != nil {
		response.Error(c, "更新失败")
		return
	}
	h.DB.First(&source, source.ID)
	h.ensureSingleDefaultLogSource(source)
	response.OK(c, source)
}

func (h *Handler) DeleteLogSource(c *gin.Context) {
	if err := h.DB.Delete(&model.LogSource{}, idParam(c)).Error; err != nil {
		response.Error(c, "删除失败")
		return
	}
	response.OK(c, nil)
}

func (h *Handler) ensureSingleDefaultLogSource(source model.LogSource) {
	if !source.IsDefault {
		return
	}
	h.DB.Model(&model.LogSource{}).
		Where("id <> ? AND is_default = ?", source.ID, true).
		Update("is_default", false)
}

// CheckLogSource 手动探一次连通性
func (h *Handler) CheckLogSource(c *gin.Context) {
	source, ok := h.requireLogSource(c, c.Param("id"))
	if !ok {
		return
	}
	response.OK(c, h.checkLogSource(source))
}

// checkLogSource 用 labels 接口做连通性判断。
//
// 不用 /ready：它只说进程活着，说明不了「能不能查到日志」，而标签接口
// 正是查询链路的第一步，顺便还能告诉人这个源里有多少个标签。
func (h *Handler) checkLogSource(source *model.LogSource) gin.H {
	now := time.Now()
	var labels []string
	query := url.Values{}
	query.Set("start", strconv.FormatInt(now.Add(-time.Hour).UnixNano(), 10))
	query.Set("end", strconv.FormatInt(now.UnixNano(), 10))

	if err := lokiRequest(*source, "/loki/api/v1/labels", query, &labels); err != nil {
		h.DB.Model(&model.LogSource{}).Where("id = ?", source.ID).Updates(map[string]any{
			"status": "error", "last_error": truncate(err.Error(), 480), "last_check_at": &now,
		})
		return gin.H{"status": "error", "detail": err.Error()}
	}

	h.DB.Model(&model.LogSource{}).Where("id = ?", source.ID).Updates(map[string]any{
		"status": "healthy", "label_count": len(labels),
		"last_error": "", "last_check_at": &now,
	})
	detail := fmt.Sprintf("可用，最近一小时有 %d 个标签", len(labels))
	if len(labels) == 0 {
		detail = "接口通了，但最近一小时没有任何标签 —— 可能是还没有日志写进来"
	}
	return gin.H{"status": "healthy", "labelCount": len(labels), "detail": detail}
}

func (h *Handler) requireLogSource(c *gin.Context, raw string) (*model.LogSource, bool) {
	id, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || id <= 0 {
		response.BadRequest(c, "请选择数据源")
		return nil, false
	}
	var source model.LogSource
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

// logTimeRange 解析时间范围，默认最近 1 小时
func logTimeRange(c *gin.Context) (start, end time.Time, err error) {
	end = time.Now()
	if raw := strings.TrimSpace(c.Query("end")); raw != "" {
		if end, err = parseMetricTime(raw); err != nil {
			return
		}
	}
	start = end.Add(-time.Hour)
	if raw := strings.TrimSpace(c.Query("start")); raw != "" {
		if start, err = parseMetricTime(raw); err != nil {
			return
		}
	}
	if !end.After(start) {
		err = fmt.Errorf("结束时间必须晚于开始时间")
	}
	return
}

// QueryLogs 查日志。direction=backward（默认）表示先看最新的。
func (h *Handler) QueryLogs(c *gin.Context) {
	source, ok := h.requireLogSource(c, c.Query("sourceId"))
	if !ok {
		return
	}
	expr := strings.TrimSpace(c.Query("query"))
	if expr == "" {
		response.BadRequest(c, "查询表达式不能为空")
		return
	}
	if len(expr) > logQueryMaxLen {
		response.BadRequest(c, fmt.Sprintf("表达式超过 %d 字符上限", logQueryMaxLen))
		return
	}
	start, end, err := logTimeRange(c)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	limit := logDefaultLimit
	if raw := strings.TrimSpace(c.Query("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > logMaxLimit {
			response.BadRequest(c, fmt.Sprintf("条数需在 1 到 %d 之间", logMaxLimit))
			return
		}
		limit = parsed
	}
	direction := "backward"
	if raw := strings.TrimSpace(c.Query("direction")); raw != "" {
		if raw != "backward" && raw != "forward" {
			response.BadRequest(c, "方向只能是 backward 或 forward")
			return
		}
		direction = raw
	}

	query := url.Values{}
	query.Set("query", expr)
	query.Set("start", strconv.FormatInt(start.UnixNano(), 10))
	query.Set("end", strconv.FormatInt(end.UnixNano(), 10))
	query.Set("limit", strconv.Itoa(limit))
	query.Set("direction", direction)

	started := time.Now()
	var data lokiStreamData
	if err := lokiRequest(*source, "/loki/api/v1/query_range", query, &data); err != nil {
		response.BadRequest(c, "查询失败: "+err.Error())
		return
	}

	// 指标型 LogQL（count_over_time 之类）返回的是 matrix，本页面只做日志行
	if data.ResultType != "" && data.ResultType != "streams" {
		response.BadRequest(c, fmt.Sprintf(
			"这条表达式返回的是 %s（指标），日志页只展示日志行；聚合统计请用「指标查询」页或去掉聚合函数",
			data.ResultType))
		return
	}

	rows, truncated := shapeLogRows(data, direction == "backward", limit)
	response.OK(c, gin.H{
		"rows": rows, "total": len(rows), "truncated": truncated,
		"streams": len(data.Result), "limit": limit, "direction": direction,
		"start": start.Format(time.RFC3339), "end": end.Format(time.RFC3339),
		"costMs":         time.Since(started).Milliseconds(),
		"linesProcessed": data.Stats.Summary.TotalLinesProcessed,
		"bytesProcessed": data.Stats.Summary.TotalBytesProcessed,
	})
}

// ListLogLabels 列标签名，或某个标签的取值（带 label 参数时）
func (h *Handler) ListLogLabels(c *gin.Context) {
	source, ok := h.requireLogSource(c, c.Query("sourceId"))
	if !ok {
		return
	}
	start, end, err := logTimeRange(c)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	query := url.Values{}
	query.Set("start", strconv.FormatInt(start.UnixNano(), 10))
	query.Set("end", strconv.FormatInt(end.UnixNano(), 10))

	path := "/loki/api/v1/labels"
	if label := strings.TrimSpace(c.Query("label")); label != "" {
		// 标签名来自用户输入，转义后拼进路径，避免越权访问别的接口
		path = "/loki/api/v1/label/" + url.PathEscape(label) + "/values"
	}
	// Loki 对未知标签会回一个没有 data 字段的成功响应，这里保证返回空数组而不是 null
	values := []string{}
	if err := lokiRequest(*source, path, query, &values); err != nil {
		response.Error(c, "读取标签失败: "+err.Error())
		return
	}
	sort.Strings(values)
	if len(values) > 500 {
		response.OK(c, gin.H{"values": values[:500], "total": len(values), "truncated": true})
		return
	}
	response.OK(c, gin.H{"values": values, "total": len(values), "truncated": false})
}
