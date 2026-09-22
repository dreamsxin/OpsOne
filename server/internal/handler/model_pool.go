package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"ops-platform/server/internal/middleware"
	"ops-platform/server/internal/model"
	"ops-platform/server/internal/response"
)

// 模型资源池：把多个供应商的模型收在平台后面，调用方只认一个 Alias。
//
// 平台在这里是一个网关，不是一个壳：调用真的经平台转发到上游，用量与成本按
// 上游返回的 usage 落库。只支持 OpenAI 兼容的 /chat/completions，非流式。
//
// 为什么不做流式：SSE 要维持长连接，而且分片响应里拿不到完整 usage
// （很多供应商只在最后一帧给、有的干脆不给），用量统计就成了估算。
// 需要流式的场景应该让调用方直连上游，平台不挡这条路。

const (
	// modelCheckTimeout 连通性检查的超时。检查只拉一次模型列表，不该等太久
	modelCheckTimeout = 15 * time.Second
	// modelMaxAttempts 一次请求最多尝试几个上游（含第一个）
	modelMaxAttempts = 3
	// modelMaxMessages / modelMaxPromptBytes 入口侧的粗保护，避免把网关当文件上传口
	modelMaxMessages    = 64
	modelMaxPromptBytes = 128 << 10
)

// ---------- 上游维护 ----------

type modelUpstreamReq struct {
	Name        string   `json:"name"`
	Alias       string   `json:"alias"`
	Provider    string   `json:"provider"`
	BaseURL     string   `json:"baseUrl"`
	APIKey      string   `json:"apiKey"`
	Model       string   `json:"model"`
	Weight      int      `json:"weight"`
	TimeoutSec  int      `json:"timeoutSec"`
	InputPrice  *float64 `json:"inputPrice"`
	OutputPrice *float64 `json:"outputPrice"`
	Enabled     *bool    `json:"enabled"`
	Remark      string   `json:"remark"`
}

func (r *modelUpstreamReq) normalize() error {
	r.Name = strings.TrimSpace(r.Name)
	r.Alias = strings.TrimSpace(r.Alias)
	r.Provider = strings.ToLower(strings.TrimSpace(r.Provider))
	r.Model = strings.TrimSpace(r.Model)
	r.APIKey = strings.TrimSpace(r.APIKey)
	// 先去空白再去尾斜杠：顺序反了的话 " http://x/ " 里的斜杠会被空白挡住
	r.BaseURL = strings.TrimRight(strings.TrimSpace(r.BaseURL), "/")

	if r.Name == "" {
		return fmt.Errorf("名称不能为空")
	}
	if r.Alias == "" {
		return fmt.Errorf("对外模型名（Alias）不能为空")
	}
	if strings.ContainsAny(r.Alias, " \t/") {
		return fmt.Errorf("Alias 里不要带空格或斜杠，调用方要把它当模型名传")
	}
	if r.Model == "" {
		return fmt.Errorf("上游模型名不能为空")
	}
	if r.BaseURL == "" {
		return fmt.Errorf("地址不能为空")
	}
	if !strings.HasPrefix(r.BaseURL, "http://") && !strings.HasPrefix(r.BaseURL, "https://") {
		return fmt.Errorf("地址必须以 http:// 或 https:// 开头")
	}
	// 常见误填：把完整的调用路径粘进来了
	if strings.HasSuffix(r.BaseURL, "/chat/completions") {
		return fmt.Errorf("只填到 /v1（如 https://api.deepseek.com/v1），不要带 /chat/completions")
	}
	if r.Provider == "" {
		r.Provider = "openai"
	}
	if r.Weight == 0 {
		r.Weight = 10
	}
	if r.Weight < 1 || r.Weight > 1000 {
		return fmt.Errorf("权重需在 1 到 1000 之间")
	}
	if r.TimeoutSec == 0 {
		r.TimeoutSec = 60
	}
	if r.TimeoutSec < 5 || r.TimeoutSec > 600 {
		return fmt.Errorf("超时需在 5 到 600 秒之间")
	}
	if r.InputPrice != nil && *r.InputPrice < 0 {
		return fmt.Errorf("输入单价不能是负数")
	}
	if r.OutputPrice != nil && *r.OutputPrice < 0 {
		return fmt.Errorf("输出单价不能是负数")
	}
	return nil
}

func (h *Handler) ListModelUpstreams(c *gin.Context) {
	var list []model.ModelUpstream
	q := h.DB.Order("alias asc, weight desc, id asc")
	if alias := strings.TrimSpace(c.Query("alias")); alias != "" {
		q = q.Where("alias = ?", alias)
	}
	if err := q.Find(&list).Error; err != nil {
		response.Error(c, "查询上游失败")
		return
	}
	// 池子视图：同 Alias 的上游算作一组，界面上要能一眼看出哪个 Alias 没有可用上游
	type poolStat struct {
		Alias   string `json:"alias"`
		Total   int    `json:"total"`
		Enabled int    `json:"enabled"`
		Healthy int    `json:"healthy"`
	}
	order := make([]string, 0, 8)
	stats := map[string]*poolStat{}
	for _, item := range list {
		stat, ok := stats[item.Alias]
		if !ok {
			stat = &poolStat{Alias: item.Alias}
			stats[item.Alias] = stat
			order = append(order, item.Alias)
		}
		stat.Total++
		if item.Enabled {
			stat.Enabled++
			if item.ModelStatus == "healthy" {
				stat.Healthy++
			}
		}
	}
	pools := make([]*poolStat, 0, len(order))
	for _, alias := range order {
		pools = append(pools, stats[alias])
	}
	response.OK(c, gin.H{"list": list, "pools": pools})
}

func (h *Handler) CreateModelUpstream(c *gin.Context) {
	var req modelUpstreamReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "参数错误")
		return
	}
	if err := req.normalize(); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	if req.APIKey == "" {
		// 本地部署的 Ollama / vLLM 常常不校验密钥，允许留空但说清楚
		req.APIKey = ""
	}

	upstream := model.ModelUpstream{
		Name: req.Name, Alias: req.Alias, Provider: req.Provider, BaseURL: req.BaseURL,
		APIKey: h.sealSecret(req.APIKey), Model: req.Model, Weight: req.Weight, TimeoutSec: req.TimeoutSec,
		ModelStatus: "unknown", Remark: req.Remark,
		CreatedBy: middleware.CurrentUser(c).ID,
	}
	if req.InputPrice != nil {
		upstream.InputPrice = *req.InputPrice
	}
	if req.OutputPrice != nil {
		upstream.OutputPrice = *req.OutputPrice
	}
	wantEnabled := true
	if req.Enabled != nil {
		wantEnabled = *req.Enabled
	}
	upstream.Enabled = wantEnabled

	if err := h.DB.Create(&upstream).Error; err != nil {
		response.Error(c, "创建失败")
		return
	}
	// enabled 带 gorm default，Create 后会被回填成 true，显式关掉的要写回去
	if !wantEnabled {
		h.DB.Model(&model.ModelUpstream{}).Where("id = ?", upstream.ID).
			Updates(map[string]any{"enabled": false})
		upstream.Enabled = false
	}

	// 建完立刻探一次，让人马上知道地址与密钥对不对
	check := h.checkModelUpstream(&upstream, middleware.CurrentUser(c))
	h.DB.First(&upstream, upstream.ID)
	response.OK(c, gin.H{"upstream": upstream, "check": check})
}

func (h *Handler) UpdateModelUpstream(c *gin.Context) {
	id := idParam(c)
	var upstream model.ModelUpstream
	if err := h.DB.First(&upstream, id).Error; err != nil {
		response.NotFound(c, "上游不存在")
		return
	}
	var req modelUpstreamReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "参数错误")
		return
	}
	if err := req.normalize(); err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	updates := map[string]any{
		"name": req.Name, "alias": req.Alias, "provider": req.Provider,
		"base_url": req.BaseURL, "model": req.Model, "weight": req.Weight,
		"timeout_sec": req.TimeoutSec, "remark": req.Remark,
	}
	if req.APIKey != "" {
		updates["api_key"] = h.sealSecret(req.APIKey) // 留空表示不修改
	}
	if req.InputPrice != nil {
		updates["input_price"] = *req.InputPrice
	}
	if req.OutputPrice != nil {
		updates["output_price"] = *req.OutputPrice
	}
	if req.Enabled != nil {
		updates["enabled"] = *req.Enabled
	}
	if err := h.DB.Model(&model.ModelUpstream{}).Where("id = ?", upstream.ID).
		Updates(updates).Error; err != nil {
		response.Error(c, "更新失败")
		return
	}
	h.DB.First(&upstream, upstream.ID)
	response.OK(c, upstream)
}

func (h *Handler) DeleteModelUpstream(c *gin.Context) {
	id := idParam(c)
	if err := h.DB.Delete(&model.ModelUpstream{}, id).Error; err != nil {
		response.Error(c, "删除失败")
		return
	}
	// 调用流水不跟着删：用量与成本是要长期回看的，上游没了流水也得留着
	response.OK(c, nil)
}

func (h *Handler) CheckModelUpstream(c *gin.Context) {
	id := idParam(c)
	var upstream model.ModelUpstream
	if err := h.DB.First(&upstream, id).Error; err != nil {
		response.NotFound(c, "上游不存在")
		return
	}
	response.OK(c, h.checkModelUpstream(&upstream, middleware.CurrentUser(c)))
}

// openUpstream 返回一份「Key 已解密」的上游副本。
//
// 刻意返回副本而不是原地改：原对象常常还要拿去写状态回库（last_error / model_status），
// 原地把密文换成明文，一不小心就会被 Save 写回去，等于自己把加密撤了。
func (h *Handler) openUpstream(upstream *model.ModelUpstream) (*model.ModelUpstream, error) {
	copied := *upstream
	key, err := h.openSecret("模型上游 Key", copied.APIKey)
	if err != nil {
		return nil, err
	}
	copied.APIKey = key
	return &copied, nil
}

// checkModelUpstream 探活：先拉模型列表，再打一次最小的真实调用。
//
// 为什么非要真调一次：/models 在不少实现上是不校验密钥的（llama.cpp server 就是，
// 实测密钥写错照样能列出模型），只探 /models 会给出「可用」的假结论，
// 等真用的时候才 401。所以第二步用 max_tokens=1 打一次 ping ——
// 代价是每次检查在付费供应商那里会产生一次极小额调用，这一次也照实落进流水。
func (h *Handler) checkModelUpstream(upstream *model.ModelUpstream, operator *model.User) gin.H {
	now := time.Now()
	fail := func(detail string) gin.H {
		h.DB.Model(&model.ModelUpstream{}).Where("id = ?", upstream.ID).Updates(map[string]any{
			"model_status": "error", "last_error": truncate(detail, 480), "last_check_at": &now,
		})
		return gin.H{"status": "error", "detail": detail}
	}

	// Key 在库里是密文，这里解一次，后面两步都用解开的副本
	upstream, err := h.openUpstream(upstream)
	if err != nil {
		return fail(err.Error())
	}

	ctx, cancel := context.WithTimeout(context.Background(), modelCheckTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, upstream.BaseURL+"/models", nil)
	if err != nil {
		return fail("请求构造失败: " + err.Error())
	}
	if upstream.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+upstream.APIKey)
	}

	started := time.Now()
	// 走统一出口：公网厂商与自建 vLLM 都有人用，命中 bypass 的自建地址自动直连
	resp, err := h.egressClient(modelCheckTimeout).Do(req)
	if err != nil {
		return fail("连接失败: " + err.Error())
	}
	defer resp.Body.Close()
	listLatency := time.Since(started).Milliseconds()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 400 {
		return fail(fmt.Sprintf("拉模型列表失败，上游返回 %d: %s",
			resp.StatusCode, openAIErrText(raw)))
	}

	var listed struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	names := make([]string, 0, 8)
	if err := json.Unmarshal(raw, &listed); err == nil {
		for _, item := range listed.Data {
			names = append(names, item.ID)
		}
	}
	found := false
	for _, name := range names {
		if name == upstream.Model {
			found = true
			break
		}
	}

	// 第二步：真打一次最小调用，密钥与模型名到这一步才算验过
	probe := &chatRequest{
		Messages:  []chatMessage{{Role: "user", Content: "ping"}},
		MaxTokens: 1,
	}
	result, callErr := h.callUpstream(ctx, upstream, probe)
	record := model.ModelCall{
		UpstreamID: upstream.ID, UpstreamName: upstream.Name, Alias: upstream.Alias,
		Provider: upstream.Provider, Model: upstream.Model, Caller: "check",
	}
	if operator != nil {
		record.UserID, record.Username = operator.ID, operator.Username
	}
	if callErr != nil {
		record.CallStatus = "failed"
		record.ErrorMsg = truncate(callErr.Error(), 480)
		if err := h.DB.Create(&record).Error; err != nil {
			log.Printf("[ai] 探活流水落库失败: %v", err)
		}
		return fail("列表能拉到，但试调用失败: " + callErr.Error())
	}
	record.CallStatus = "success"
	record.PromptTokens = result.Usage.PromptTokens
	record.CompletionTokens = result.Usage.CompletionTokens
	record.TotalTokens = result.Usage.TotalTokens
	record.UsageMissing = result.UsageMissing
	record.Cost = upstreamCost(upstream, result.Usage)
	record.LatencyMs = result.LatencyMs
	if err := h.DB.Create(&record).Error; err != nil {
		log.Printf("[ai] 探活流水落库失败: %v", err)
	}

	detail := fmt.Sprintf("可用：列表 %d ms、试调用 %d ms", listLatency, result.LatencyMs)
	switch {
	case found:
		detail += fmt.Sprintf("，模型列表里有 %s（共 %d 个）", upstream.Model, len(names))
	case len(names) > 0:
		// 调用都通了，说明模型名是好的，只是上游没在列表里列出来
		detail += fmt.Sprintf("，列出的 %d 个模型里没有 %s，但试调用是通的",
			len(names), upstream.Model)
	default:
		detail += "，上游没有返回模型列表（有的实现不提供 /models）"
	}

	h.DB.Model(&model.ModelUpstream{}).Where("id = ?", upstream.ID).Updates(map[string]any{
		"model_status": "healthy", "model_listed": found, "latency_ms": result.LatencyMs,
		"last_error": "", "last_check_at": &now,
	})
	return gin.H{
		"status": "healthy", "modelListed": found, "latencyMs": result.LatencyMs,
		"modelCount": len(names), "detail": detail,
	}
}

// openAIErrText 从 OpenAI 风格的错误体里取 message，取不到就原样截一段。
// 各家兼容实现的错误体五花八门，别自己编错误信息，照实带出来最有用。
func openAIErrText(raw []byte) string {
	var envelope struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    any    `json:"code"`
		} `json:"error"`
		Message string `json:"message"`
	}
	if json.Unmarshal(raw, &envelope) == nil {
		if envelope.Error.Message != "" {
			if envelope.Error.Type != "" {
				return envelope.Error.Message + "（" + envelope.Error.Type + "）"
			}
			return envelope.Error.Message
		}
		if envelope.Message != "" {
			return envelope.Message
		}
	}
	text := strings.TrimSpace(string(raw))
	if text == "" {
		return "上游没有返回错误详情"
	}
	return truncate(text, 300)
}

// ---------- 调用 ----------

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	// Model 传 Alias；为了让 OpenAI 的 SDK 能直接用，字段名就叫 model
	Model       string        `json:"model"`
	Messages    []chatMessage `json:"messages"`
	Temperature *float64      `json:"temperature"`
	MaxTokens   int           `json:"maxTokens"`
	// UpstreamID 只在界面上试调用时用：指定试哪一条，不走挑选逻辑
	UpstreamID uint `json:"upstreamId"`
	// Caller 调用来源，只认 console（界面上试调用）；其他值一律记成 api，
	// 免得调用方把自己标成别的来源让用量统计失真
	Caller string `json:"caller"`
}

func (r *chatRequest) normalize() error {
	r.Model = strings.TrimSpace(r.Model)
	if len(r.Messages) == 0 {
		return fmt.Errorf("messages 不能为空")
	}
	if len(r.Messages) > modelMaxMessages {
		return fmt.Errorf("一次最多 %d 条消息", modelMaxMessages)
	}
	total := 0
	for i := range r.Messages {
		r.Messages[i].Role = strings.TrimSpace(r.Messages[i].Role)
		if r.Messages[i].Role == "" {
			r.Messages[i].Role = "user"
		}
		switch r.Messages[i].Role {
		case "system", "user", "assistant":
		default:
			return fmt.Errorf("role 只支持 system / user / assistant，收到 %s", r.Messages[i].Role)
		}
		if strings.TrimSpace(r.Messages[i].Content) == "" {
			return fmt.Errorf("第 %d 条消息内容为空", i+1)
		}
		total += len(r.Messages[i].Content)
	}
	if total > modelMaxPromptBytes {
		return fmt.Errorf("提示词总长超过 %d KB，平台侧拒绝", modelMaxPromptBytes>>10)
	}
	if r.Temperature != nil && (*r.Temperature < 0 || *r.Temperature > 2) {
		return fmt.Errorf("temperature 需在 0 到 2 之间")
	}
	if r.MaxTokens < 0 || r.MaxTokens > 32768 {
		return fmt.Errorf("maxTokens 需在 0 到 32768 之间")
	}
	if r.UpstreamID == 0 && r.Model == "" {
		return fmt.Errorf("请指定模型（Alias）")
	}
	return nil
}

// upstreamUsage 上游返回的 token 用量
type upstreamUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// callResult 一次上游调用的结果
type callResult struct {
	Content      string
	Usage        upstreamUsage
	UsageMissing bool
	FinishReason string
	LatencyMs    int64
}

// callUpstream 向一个上游发一次非流式 chat 请求
func (h *Handler) callUpstream(ctx context.Context, upstream *model.ModelUpstream, req *chatRequest) (*callResult, error) {
	payload := map[string]any{
		"model":    upstream.Model, // 对上游要报它自己的模型名，而不是平台的 Alias
		"messages": req.Messages,
		"stream":   false,
	}
	if req.Temperature != nil {
		payload["temperature"] = *req.Temperature
	}
	if req.MaxTokens > 0 {
		payload["max_tokens"] = req.MaxTokens
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	timeout := time.Duration(upstream.TimeoutSec) * time.Second
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(callCtx, http.MethodPost,
		upstream.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if upstream.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+upstream.APIKey)
	}

	started := time.Now()
	resp, err := h.egressClient(timeout).Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("请求失败: %w", err)
	}
	defer resp.Body.Close()
	latency := time.Since(started).Milliseconds()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("读取响应失败: %w", err)
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("上游返回 %d: %s", resp.StatusCode, openAIErrText(raw))
	}

	var decoded struct {
		Choices []struct {
			Message      chatMessage `json:"message"`
			FinishReason string      `json:"finish_reason"`
		} `json:"choices"`
		Usage *upstreamUsage `json:"usage"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, fmt.Errorf("响应不是合法 JSON: %s", truncate(string(raw), 200))
	}
	if len(decoded.Choices) == 0 {
		// 有的兼容实现出错时给 200 + 空 choices，这种要当失败处理
		return nil, fmt.Errorf("上游没有返回任何回复内容: %s", openAIErrText(raw))
	}

	result := &callResult{
		Content:      decoded.Choices[0].Message.Content,
		FinishReason: decoded.Choices[0].FinishReason,
		LatencyMs:    latency,
	}
	if decoded.Usage != nil {
		result.Usage = *decoded.Usage
		if result.Usage.TotalTokens == 0 {
			result.Usage.TotalTokens = result.Usage.PromptTokens + result.Usage.CompletionTokens
		}
	} else {
		result.UsageMissing = true
	}
	return result, nil
}

// upstreamCost 按登记单价算成本（元）。单价为 0 就是 0，不去猜。
func upstreamCost(upstream *model.ModelUpstream, usage upstreamUsage) float64 {
	cost := float64(usage.PromptTokens)/1000*upstream.InputPrice +
		float64(usage.CompletionTokens)/1000*upstream.OutputPrice
	// 留 6 位小数：单价常是 0.001 元/千 token 这种量级，四舍五入到分会全变成 0
	return math.Round(cost*1e6) / 1e6
}

// pickUpstreams 挑出候选上游，按权重从大到小；同权重按 id。
//
// 没做「按流量比例分配」的加权轮询：内网自用的量级下，稳定优先于均衡 ——
// 权重在这里的含义是「优先级」，主用挂了才切备用，行为可预期、事故好复盘。
func (h *Handler) pickUpstreams(alias string, upstreamID uint) ([]model.ModelUpstream, error) {
	var list []model.ModelUpstream
	q := h.DB.Where("enabled = ?", true)
	if upstreamID > 0 {
		// 界面上指定试某一条时，连 enabled 都不看：就是要验证这一条
		q = h.DB.Where("id = ?", upstreamID)
	} else {
		q = q.Where("alias = ?", alias)
	}
	if err := q.Order("weight desc, id asc").Find(&list).Error; err != nil {
		return nil, fmt.Errorf("查询上游失败")
	}
	if len(list) == 0 {
		if upstreamID > 0 {
			return nil, fmt.Errorf("上游不存在")
		}
		return nil, fmt.Errorf("模型 %s 没有可用的上游（检查 Alias 拼写与上游是否启用）", alias)
	}
	return list, nil
}

// chatOutcome 一次成功派发的结果
type chatOutcome struct {
	Upstream model.ModelUpstream
	Result   *callResult
	Record   model.ModelCall
	// Attempts 试过的每个上游及其结果，失败的也在里面
	Attempts []gin.H
}

// dispatchChat 把请求派发到池子里的上游，并把用量落库。
//
// 网关接口与 Agent 运行共用这一条路径：否则 Agent 的调用不进流水，
// 用量与成本页就会少算一块 —— 那是最难查的账。
func (h *Handler) dispatchChat(ctx context.Context, req *chatRequest, caller string,
	operator *model.User, clientIP string) (*chatOutcome, error) {
	candidates, err := h.pickUpstreams(req.Model, req.UpstreamID)
	if err != nil {
		return nil, err
	}

	attempts := make([]gin.H, 0, modelMaxAttempts)
	failures := make([]string, 0, modelMaxAttempts)
	for i, upstream := range candidates {
		if i >= modelMaxAttempts {
			break
		}
		// Key 解不开就当这一次调用失败，照常落一条失败流水并试下一个上游 ——
		// 静默跳过会让「池子里明明有上游却谁都没被调用」变成一个查不出来的现象
		var result *callResult
		opened, callErr := h.openUpstream(&upstream)
		if callErr == nil {
			result, callErr = h.callUpstream(ctx, opened, req)
		}

		record := model.ModelCall{
			UpstreamID: upstream.ID, UpstreamName: upstream.Name, Alias: upstream.Alias,
			Provider: upstream.Provider, Model: upstream.Model, Caller: caller,
			ClientIP: clientIP, Retried: i > 0,
		}
		if operator != nil {
			record.UserID, record.Username = operator.ID, operator.Username
		}
		if callErr != nil {
			record.CallStatus = "failed"
			record.ErrorMsg = truncate(callErr.Error(), 480)
			if err := h.DB.Create(&record).Error; err != nil {
				log.Printf("[ai] 失败流水落库失败: %v", err)
			}
			attempts = append(attempts, gin.H{
				"upstreamId": upstream.ID, "upstreamName": upstream.Name,
				"status": "failed", "error": callErr.Error(),
			})
			failures = append(failures, upstream.Name+": "+callErr.Error())
			continue
		}

		record.CallStatus = "success"
		record.PromptTokens = result.Usage.PromptTokens
		record.CompletionTokens = result.Usage.CompletionTokens
		record.TotalTokens = result.Usage.TotalTokens
		record.UsageMissing = result.UsageMissing
		record.Cost = upstreamCost(&upstream, result.Usage)
		record.LatencyMs = result.LatencyMs
		if err := h.DB.Create(&record).Error; err != nil {
			// 落库失败不该影响调用方拿到回复，但要能在日志里查到
			log.Printf("[ai] 调用流水落库失败: %v", err)
		}
		attempts = append(attempts, gin.H{
			"upstreamId": upstream.ID, "upstreamName": upstream.Name, "status": "success",
		})
		return &chatOutcome{
			Upstream: upstream, Result: result, Record: record, Attempts: attempts,
		}, nil
	}

	// 全军覆没：把每个上游的原话都带上，否则调用方只知道「失败了」
	return nil, fmt.Errorf("上游全部调用失败 —— %s", strings.Join(failures, "；"))
}

// ChatCompletion 网关入口：把请求转发到池子里的上游，并把用量落库。
func (h *Handler) ChatCompletion(c *gin.Context) {
	var req chatRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "参数错误")
		return
	}
	if err := req.normalize(); err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	caller := "api"
	if req.UpstreamID > 0 || strings.TrimSpace(req.Caller) == "console" {
		caller = "console"
	}
	outcome, err := h.dispatchChat(c.Request.Context(), &req, caller,
		middleware.CurrentUser(c), c.ClientIP())
	if err != nil {
		// 挑不到上游是配置问题（400），上游全挂是运行时失败（500）
		if strings.Contains(err.Error(), "上游全部调用失败") {
			response.Error(c, err.Error())
		} else {
			response.BadRequest(c, err.Error())
		}
		return
	}

	response.OK(c, gin.H{
		"content":      outcome.Result.Content,
		"alias":        outcome.Upstream.Alias,
		"model":        outcome.Upstream.Model,
		"provider":     outcome.Upstream.Provider,
		"upstreamId":   outcome.Upstream.ID,
		"upstreamName": outcome.Upstream.Name,
		"finishReason": outcome.Result.FinishReason,
		"usage": gin.H{
			"promptTokens":     outcome.Result.Usage.PromptTokens,
			"completionTokens": outcome.Result.Usage.CompletionTokens,
			"totalTokens":      outcome.Result.Usage.TotalTokens,
			"missing":          outcome.Result.UsageMissing,
		},
		"cost":      outcome.Record.Cost,
		"latencyMs": outcome.Result.LatencyMs,
		"callId":    outcome.Record.ID,
		"attempts":  outcome.Attempts,
	})
}

// ---------- 调用流水 ----------

func (h *Handler) ListModelCalls(c *gin.Context) {
	page, size := pageParams(c)
	q := h.DB.Model(&model.ModelCall{})
	if alias := strings.TrimSpace(c.Query("alias")); alias != "" {
		q = q.Where("alias = ?", alias)
	}
	if status := strings.TrimSpace(c.Query("status")); status != "" {
		q = q.Where("call_status = ?", status)
	}
	if caller := strings.TrimSpace(c.Query("caller")); caller != "" {
		q = q.Where("caller = ?", caller)
	}
	if username := strings.TrimSpace(c.Query("username")); username != "" {
		q = q.Where("username LIKE ?", "%"+username+"%")
	}
	if raw := strings.TrimSpace(c.Query("upstreamId")); raw != "" {
		id, err := strconv.Atoi(raw)
		if err != nil || id <= 0 {
			response.BadRequest(c, "upstreamId 不合法")
			return
		}
		q = q.Where("upstream_id = ?", id)
	}
	start, err := parseAuditTime(c.Query("start"), false)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	end, err := parseAuditTime(c.Query("end"), true)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	if start != nil {
		q = q.Where("created_at >= ?", *start)
	}
	if end != nil {
		q = q.Where("created_at <= ?", *end)
	}
	q = q.Session(&gorm.Session{})

	var total int64
	if err := q.Count(&total).Error; err != nil {
		response.Error(c, "查询流水失败")
		return
	}
	var rows []model.ModelCall
	if err := q.Order("id desc").Offset((page - 1) * size).Limit(size).
		Find(&rows).Error; err != nil {
		response.Error(c, "查询流水失败")
		return
	}

	// 汇总按当前筛选条件算，而不是只算当前这一页
	var summary struct {
		Calls       int64
		Failed      int64
		Tokens      int64
		Cost        float64
		AvgLatency  float64
		MissingRows int64
	}
	q.Select("COUNT(*) as calls, COALESCE(SUM(total_tokens),0) as tokens, " +
		"COALESCE(SUM(cost),0) as cost, COALESCE(AVG(latency_ms),0) as avg_latency, " +
		"COALESCE(SUM(CASE WHEN call_status = 'failed' THEN 1 ELSE 0 END),0) as failed, " +
		"COALESCE(SUM(CASE WHEN usage_missing THEN 1 ELSE 0 END),0) as missing_rows").
		Scan(&summary)

	response.OK(c, gin.H{
		"list": rows, "total": total, "page": page, "pageSize": size,
		"summary": gin.H{
			"calls": summary.Calls, "failed": summary.Failed,
			"tokens": summary.Tokens, "cost": math.Round(summary.Cost*1e6) / 1e6,
			"avgLatencyMs": math.Round(summary.AvgLatency),
			"usageMissing": summary.MissingRows,
		},
	})
}
