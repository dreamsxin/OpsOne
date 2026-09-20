package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"ops-platform/server/internal/config"
	"ops-platform/server/internal/model"
)

func TestModelUpstreamReqNormalize(t *testing.T) {
	// 补默认值：provider / weight / timeout；地址去空白与尾斜杠
	req := modelUpstreamReq{
		Name: " 主力 ", Alias: " chat ", Model: " deepseek-chat ",
		BaseURL: "  https://api.deepseek.com/v1/  ",
	}
	if err := req.normalize(); err != nil {
		t.Fatalf("合法请求被拒: %v", err)
	}
	if req.BaseURL != "https://api.deepseek.com/v1" {
		t.Fatalf("地址没归一化: %q", req.BaseURL)
	}
	if req.Provider != "openai" || req.Weight != 10 || req.TimeoutSec != 60 {
		t.Fatalf("默认值不对: %+v", req)
	}

	price := -1.0
	cases := []struct {
		name string
		req  modelUpstreamReq
		want string
	}{
		{"没名字", modelUpstreamReq{Alias: "a", Model: "m", BaseURL: "http://x"}, "名称"},
		{"没 alias", modelUpstreamReq{Name: "n", Model: "m", BaseURL: "http://x"}, "Alias"},
		{"alias 带斜杠", modelUpstreamReq{Name: "n", Alias: "a/b", Model: "m",
			BaseURL: "http://x"}, "不要带空格或斜杠"},
		{"没上游模型名", modelUpstreamReq{Name: "n", Alias: "a", BaseURL: "http://x"}, "上游模型名"},
		{"地址没协议", modelUpstreamReq{Name: "n", Alias: "a", Model: "m",
			BaseURL: "api.deepseek.com"}, "http:// 或 https://"},
		{"把调用路径粘进来", modelUpstreamReq{Name: "n", Alias: "a", Model: "m",
			BaseURL: "https://x/v1/chat/completions"}, "不要带 /chat/completions"},
		{"权重越界", modelUpstreamReq{Name: "n", Alias: "a", Model: "m",
			BaseURL: "http://x", Weight: 5000}, "权重"},
		{"超时越界", modelUpstreamReq{Name: "n", Alias: "a", Model: "m",
			BaseURL: "http://x", TimeoutSec: 1}, "超时"},
		{"负单价", modelUpstreamReq{Name: "n", Alias: "a", Model: "m",
			BaseURL: "http://x", InputPrice: &price}, "负数"},
	}
	for _, tc := range cases {
		local := tc.req
		err := local.normalize()
		if err == nil {
			t.Fatalf("%s 应该报错", tc.name)
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s 的报错是 %q，期望包含 %q", tc.name, err, tc.want)
		}
	}
}

func TestUpstreamCost(t *testing.T) {
	upstream := model.ModelUpstream{InputPrice: 1, OutputPrice: 2}
	cost := upstreamCost(&upstream, upstreamUsage{PromptTokens: 1500, CompletionTokens: 500})
	// 1500/1000*1 + 500/1000*2 = 2.5
	if cost != 2.5 {
		t.Fatalf("成本算错: %v", cost)
	}
	// 单价极小时不能被舍成 0：0.001 元/千 token、100 token = 0.0001 元
	cheap := model.ModelUpstream{InputPrice: 0.001}
	if got := upstreamCost(&cheap, upstreamUsage{PromptTokens: 100}); got != 0.0001 {
		t.Fatalf("小额成本被舍掉了: %v", got)
	}
	// 没登记单价就是 0，不去猜
	free := model.ModelUpstream{}
	if got := upstreamCost(&free, upstreamUsage{PromptTokens: 9999}); got != 0 {
		t.Fatalf("没单价却算出了成本: %v", got)
	}
}

func TestChatRequestNormalize(t *testing.T) {
	req := chatRequest{Model: "chat", Messages: []chatMessage{{Content: "hi"}}}
	if err := req.normalize(); err != nil {
		t.Fatalf("合法请求被拒: %v", err)
	}
	if req.Messages[0].Role != "user" {
		t.Fatalf("role 没有兜默认值: %+v", req.Messages[0])
	}

	bad := chatRequest{Model: "chat"}
	if err := bad.normalize(); err == nil || !strings.Contains(err.Error(), "messages") {
		t.Fatalf("空 messages 应该被拒: %v", err)
	}
	bad = chatRequest{Model: "chat", Messages: []chatMessage{{Role: "tool", Content: "x"}}}
	if err := bad.normalize(); err == nil || !strings.Contains(err.Error(), "role") {
		t.Fatalf("不支持的 role 应该被拒: %v", err)
	}
	bad = chatRequest{Messages: []chatMessage{{Content: "x"}}}
	if err := bad.normalize(); err == nil || !strings.Contains(err.Error(), "Alias") {
		t.Fatalf("没指定模型应该被拒: %v", err)
	}
	temp := 3.0
	bad = chatRequest{Model: "chat", Messages: []chatMessage{{Content: "x"}}, Temperature: &temp}
	if err := bad.normalize(); err == nil || !strings.Contains(err.Error(), "temperature") {
		t.Fatalf("temperature 越界应该被拒: %v", err)
	}
}

// fakeOpenAI 一个 OpenAI 兼容的假上游，用来单测转发与计量逻辑
type fakeOpenAI struct {
	srv *httptest.Server
	// hits 收到的 /chat/completions 次数
	hits atomic.Int64
	// models /models 返回的模型名
	models []string
	// status 非 0 时 /chat/completions 直接返回这个状态码
	status int
	// omitUsage 不返回 usage 字段（有的兼容实现就是不给）
	omitUsage bool
	// emptyChoices 返回 200 但没有 choices（出错时给 200 的实现）
	emptyChoices bool
}

func newFakeOpenAI(t *testing.T, fake *fakeOpenAI) *fakeOpenAI {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		items := make([]map[string]any, 0, len(fake.models))
		for _, name := range fake.models {
			items = append(items, map[string]any{"id": name, "object": "model"})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": items})
	})
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		fake.hits.Add(1)
		if fake.status != 0 {
			w.WriteHeader(fake.status)
			_, _ = w.Write([]byte(`{"error":{"message":"上游说不行","type":"invalid_request_error"}}`))
			return
		}
		var body struct {
			Model    string        `json:"model"`
			Stream   bool          `json:"stream"`
			Messages []chatMessage `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if fake.emptyChoices {
			_, _ = w.Write([]byte(`{"choices":[],"error":{"message":"内容被策略拦截"}}`))
			return
		}
		payload := map[string]any{
			"choices": []map[string]any{{
				"message":       map[string]string{"role": "assistant", "content": "收到：" + body.Model},
				"finish_reason": "stop",
			}},
		}
		if !fake.omitUsage {
			payload["usage"] = map[string]int{
				"prompt_tokens": 12, "completion_tokens": 8, "total_tokens": 20,
			}
		}
		_ = json.NewEncoder(w).Encode(payload)
	})
	fake.srv = httptest.NewServer(mux)
	t.Cleanup(fake.srv.Close)
	return fake
}

func (f *fakeOpenAI) baseURL() string { return f.srv.URL + "/v1" }

// newModelTestHandler 内存库 + 一个带登录用户的 gin 引擎
func newModelTestHandler(t *testing.T) (*Handler, *gin.Engine) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	g, err := gorm.Open(sqlite.Open("file::memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开内存库失败: %v", err)
	}
	if err := g.AutoMigrate(&model.ModelUpstream{}, &model.ModelCall{}); err != nil {
		t.Fatalf("建表失败: %v", err)
	}
	h := New(g, &config.Config{})

	engine := gin.New()
	engine.Use(func(c *gin.Context) {
		// 生产上这是 Auth 中间件塞进去的，测试里手工造一个
		c.Set("ctx_user", &model.User{ID: 7, Username: "tester"})
		c.Next()
	})
	engine.POST("/ai/chat/completions", h.ChatCompletion)
	engine.GET("/ai/calls", h.ListModelCalls)
	engine.GET("/ai/upstreams", h.ListModelUpstreams)
	return h, engine
}

func postModelJSON(t *testing.T, engine *gin.Engine, path, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	var decoded map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("响应不是 JSON: %s", rec.Body.String())
	}
	return rec.Code, decoded
}

func getModelJSON(t *testing.T, engine *gin.Engine, path string) map[string]any {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	var decoded map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("响应不是 JSON: %s", rec.Body.String())
	}
	return decoded
}

// createUpstream 写一条上游。
//
// Enabled 带 gorm:"default:true"，直接 Create 一条 Enabled=false 的记录会被数据库
// 默认值回填成 true（生产代码 CreateModelUpstream 专门为此写回了一次），
// 测试里也得照做，否则「停用的上游不参与挑选」这类用例根本没验到东西。
func createUpstream(t *testing.T, h *Handler, upstream model.ModelUpstream) model.ModelUpstream {
	t.Helper()
	enabled := upstream.Enabled
	if err := h.DB.Create(&upstream).Error; err != nil {
		t.Fatalf("写入上游失败: %v", err)
	}
	if !enabled {
		if err := h.DB.Model(&model.ModelUpstream{}).Where("id = ?", upstream.ID).
			Updates(map[string]any{"enabled": false}).Error; err != nil {
			t.Fatalf("写回 enabled 失败: %v", err)
		}
		upstream.Enabled = false
	}
	return upstream
}

func TestCheckModelUpstream(t *testing.T) {
	fake := newFakeOpenAI(t, &fakeOpenAI{models: []string{"deepseek-chat", "deepseek-coder"}})
	h, _ := newModelTestHandler(t)

	upstream := model.ModelUpstream{
		Name: "主力", Alias: "chat", Provider: "deepseek", BaseURL: fake.baseURL(),
		Model: "deepseek-chat", Weight: 20, TimeoutSec: 30, Enabled: true, ModelStatus: "unknown",
	}
	if err := h.DB.Create(&upstream).Error; err != nil {
		t.Fatalf("写入上游失败: %v", err)
	}

	got := h.checkModelUpstream(&upstream, &model.User{ID: 7, Username: "tester"})
	if got["status"] != "healthy" || got["modelListed"] != true {
		t.Fatalf("检查结果不对: %+v", got)
	}
	var saved model.ModelUpstream
	h.DB.First(&saved, upstream.ID)
	if saved.ModelStatus != "healthy" || !saved.ModelListed || saved.LastCheckAt == nil {
		t.Fatalf("检查结果没回填: %+v", saved)
	}

	// 模型名写错时不算失败，但要明确提示列表里没有它
	wrong := model.ModelUpstream{
		Name: "写错了", Alias: "chat", BaseURL: fake.baseURL(), Model: "gpt-4o",
		TimeoutSec: 30, Enabled: true,
	}
	h.DB.Create(&wrong)
	got = h.checkModelUpstream(&wrong, &model.User{ID: 7, Username: "tester"})
	if got["status"] != "healthy" || got["modelListed"] != false {
		t.Fatalf("模型名写错时的结果不对: %+v", got)
	}
	if detail, _ := got["detail"].(string); !strings.Contains(detail, "没有 gpt-4o") {
		t.Fatalf("没提示模型名对不上: %q", detail)
	}
}

func TestCheckModelUpstreamUnauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"Incorrect API key provided","type":"invalid_request_error"}}`))
	}))
	defer srv.Close()

	h, _ := newModelTestHandler(t)
	upstream := model.ModelUpstream{
		Name: "密钥错了", Alias: "chat", BaseURL: srv.URL + "/v1", Model: "m",
		TimeoutSec: 30, Enabled: true,
	}
	h.DB.Create(&upstream)

	got := h.checkModelUpstream(&upstream, &model.User{ID: 7, Username: "tester"})
	if got["status"] != "error" {
		t.Fatalf("应该判为 error: %+v", got)
	}
	// 上游的原话要带出来，否则用户不知道是密钥问题还是网络问题
	detail, _ := got["detail"].(string)
	if !strings.Contains(detail, "Incorrect API key") || !strings.Contains(detail, "401") {
		t.Fatalf("没带上上游原话: %q", detail)
	}
	var saved model.ModelUpstream
	h.DB.First(&saved, upstream.ID)
	if saved.ModelStatus != "error" || !strings.Contains(saved.LastError, "Incorrect API key") {
		t.Fatalf("失败原因没回填: %+v", saved)
	}
}

// TestCheckModelUpstreamListOkButCallRejected 守住这轮真机验证暴露出来的坑：
// llama.cpp server 的 /models 不校验密钥 —— 密钥写错也能列出模型。
// 只探列表会给出「可用」的假结论，所以检查必须再真打一次最小调用。
func TestCheckModelUpstreamListOkButCallRejected(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		// 列表不校验密钥，照样给结果
		_, _ = w.Write([]byte(`{"data":[{"id":"local-model"}]}`))
	})
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"invalid api key","type":"authentication_error"}}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	h, _ := newModelTestHandler(t)
	upstream := model.ModelUpstream{
		Name: "列表能通但调用不通", Alias: "chat", BaseURL: srv.URL + "/v1",
		Model: "local-model", TimeoutSec: 30, Enabled: true,
	}
	h.DB.Create(&upstream)

	got := h.checkModelUpstream(&upstream, &model.User{ID: 7, Username: "tester"})
	if got["status"] != "error" {
		t.Fatalf("列表通但调用 401，应该判为 error: %+v", got)
	}
	detail, _ := got["detail"].(string)
	if !strings.Contains(detail, "试调用失败") || !strings.Contains(detail, "invalid api key") {
		t.Fatalf("要说清是试调用那一步失败并带上上游原话: %q", detail)
	}
	// 探活也要留痕，否则「谁把额度打掉了」对不上账
	var call model.ModelCall
	h.DB.Order("id desc").First(&call)
	if call.Caller != "check" || call.CallStatus != "failed" {
		t.Fatalf("探活流水不对: %+v", call)
	}
}

func TestCheckModelUpstreamRecordsProbeCall(t *testing.T) {
	fake := newFakeOpenAI(t, &fakeOpenAI{models: []string{"m"}})
	h, _ := newModelTestHandler(t)
	upstream := model.ModelUpstream{
		Name: "正常上游", Alias: "chat", BaseURL: fake.baseURL(), Model: "m",
		TimeoutSec: 30, Enabled: true, InputPrice: 1, OutputPrice: 1,
	}
	h.DB.Create(&upstream)

	got := h.checkModelUpstream(&upstream, &model.User{ID: 7, Username: "tester"})
	if got["status"] != "healthy" {
		t.Fatalf("应该判为 healthy: %+v", got)
	}
	// 探活会真花一次（极小额），所以必须落流水、计入成本
	var call model.ModelCall
	h.DB.Order("id desc").First(&call)
	if call.Caller != "check" || call.CallStatus != "success" || call.TotalTokens == 0 {
		t.Fatalf("探活流水不对: %+v", call)
	}
	if call.Cost <= 0 {
		t.Fatalf("探活的成本也要算进去: %+v", call.Cost)
	}
}

func TestChatCompletionRecordsUsageAndCost(t *testing.T) {
	fake := newFakeOpenAI(t, &fakeOpenAI{models: []string{"deepseek-chat"}})
	h, engine := newModelTestHandler(t)
	h.DB.Create(&model.ModelUpstream{
		Name: "主力", Alias: "chat", Provider: "deepseek", BaseURL: fake.baseURL(),
		Model: "deepseek-chat", Weight: 20, TimeoutSec: 30, Enabled: true,
		InputPrice: 1, OutputPrice: 2, ModelStatus: "healthy",
	})

	code, body := postModelJSON(t, engine, "/ai/chat/completions",
		`{"model":"chat","messages":[{"role":"user","content":"你好"}]}`)
	if code != http.StatusOK || body["code"] != float64(0) {
		t.Fatalf("调用失败: %d %+v", code, body)
	}
	data, _ := body["data"].(map[string]any)
	if content, _ := data["content"].(string); !strings.Contains(content, "deepseek-chat") {
		t.Fatalf("回复内容不对: %+v", data)
	}
	// 对上游报的是上游模型名，而不是平台的 Alias
	if data["model"] != "deepseek-chat" || data["alias"] != "chat" {
		t.Fatalf("模型名/Alias 不对: %+v", data)
	}
	usage, _ := data["usage"].(map[string]any)
	if usage["promptTokens"] != float64(12) || usage["totalTokens"] != float64(20) {
		t.Fatalf("usage 没透出: %+v", usage)
	}
	// 12/1000*1 + 8/1000*2 = 0.028
	if data["cost"] != 0.028 {
		t.Fatalf("成本算错: %+v", data["cost"])
	}

	var calls []model.ModelCall
	h.DB.Order("id asc").Find(&calls)
	if len(calls) != 1 {
		t.Fatalf("应该只有一条流水，实际 %d", len(calls))
	}
	call := calls[0]
	if call.CallStatus != "success" || call.TotalTokens != 20 || call.Cost != 0.028 {
		t.Fatalf("流水内容不对: %+v", call)
	}
	if call.Username != "tester" || call.Caller != "api" || call.Retried {
		t.Fatalf("流水上下文不对: %+v", call)
	}
	if call.UsageMissing {
		t.Fatal("上游给了 usage，不该标成缺失")
	}
}

func TestChatCompletionFailsOverByWeight(t *testing.T) {
	broken := newFakeOpenAI(t, &fakeOpenAI{status: http.StatusInternalServerError})
	good := newFakeOpenAI(t, &fakeOpenAI{models: []string{"backup-model"}})
	h, engine := newModelTestHandler(t)

	h.DB.Create(&model.ModelUpstream{
		Name: "主力", Alias: "chat", BaseURL: broken.baseURL(), Model: "main-model",
		Weight: 20, TimeoutSec: 10, Enabled: true,
	})
	h.DB.Create(&model.ModelUpstream{
		Name: "备用", Alias: "chat", BaseURL: good.baseURL(), Model: "backup-model",
		Weight: 10, TimeoutSec: 10, Enabled: true, InputPrice: 0.5, OutputPrice: 0.5,
	})
	// 停用的上游不参与，哪怕权重最高
	createUpstream(t, h, model.ModelUpstream{
		Name: "停用的", Alias: "chat", BaseURL: broken.baseURL(), Model: "disabled-model",
		Weight: 99, TimeoutSec: 10, Enabled: false,
	})

	code, body := postModelJSON(t, engine, "/ai/chat/completions",
		`{"model":"chat","messages":[{"role":"user","content":"hi"}]}`)
	if code != http.StatusOK || body["code"] != float64(0) {
		t.Fatalf("应该切到备用并成功: %d %+v", code, body)
	}
	data, _ := body["data"].(map[string]any)
	if data["upstreamName"] != "备用" {
		t.Fatalf("没切到备用: %+v", data)
	}
	if broken.hits.Load() != 1 {
		t.Fatalf("主力应该只试一次，实际 %d", broken.hits.Load())
	}

	// 失败的那次也要落流水，否则「为什么慢了」查不出来
	var calls []model.ModelCall
	h.DB.Order("id asc").Find(&calls)
	if len(calls) != 2 {
		t.Fatalf("应该有两条流水（1 失败 + 1 成功），实际 %d", len(calls))
	}
	if calls[0].CallStatus != "failed" || calls[0].Retried {
		t.Fatalf("第一条应该是失败且非重试: %+v", calls[0])
	}
	if !strings.Contains(calls[0].ErrorMsg, "上游说不行") || !strings.Contains(calls[0].ErrorMsg, "500") {
		t.Fatalf("失败原因没带上上游原话: %q", calls[0].ErrorMsg)
	}
	if calls[1].CallStatus != "success" || !calls[1].Retried {
		t.Fatalf("第二条应该是成功且标记为重试: %+v", calls[1])
	}
}

func TestChatCompletionAllUpstreamsFail(t *testing.T) {
	broken := newFakeOpenAI(t, &fakeOpenAI{status: http.StatusTooManyRequests})
	h, engine := newModelTestHandler(t)
	h.DB.Create(&model.ModelUpstream{
		Name: "唯一一条", Alias: "chat", BaseURL: broken.baseURL(), Model: "m",
		Weight: 10, TimeoutSec: 10, Enabled: true,
	})

	_, body := postModelJSON(t, engine, "/ai/chat/completions",
		`{"model":"chat","messages":[{"role":"user","content":"hi"}]}`)
	if body["code"] == float64(0) {
		t.Fatalf("全部失败时不该返回成功: %+v", body)
	}
	msg, _ := body["msg"].(string)
	// 报错要带上每个上游的原话，而不是一句「调用失败」
	if !strings.Contains(msg, "唯一一条") || !strings.Contains(msg, "上游说不行") {
		t.Fatalf("报错不够具体: %q", msg)
	}
}

func TestChatCompletionUnknownAlias(t *testing.T) {
	h, engine := newModelTestHandler(t)
	_ = h
	_, body := postModelJSON(t, engine, "/ai/chat/completions",
		`{"model":"nope","messages":[{"role":"user","content":"hi"}]}`)
	msg, _ := body["msg"].(string)
	if body["code"] == float64(0) || !strings.Contains(msg, "没有可用的上游") {
		t.Fatalf("未知 Alias 应该明确报错: %+v", body)
	}
}

func TestChatCompletionUsageMissingAndEmptyChoices(t *testing.T) {
	noUsage := newFakeOpenAI(t, &fakeOpenAI{omitUsage: true})
	h, engine := newModelTestHandler(t)
	h.DB.Create(&model.ModelUpstream{
		Name: "不给 usage", Alias: "chat", BaseURL: noUsage.baseURL(), Model: "m",
		Weight: 10, TimeoutSec: 10, Enabled: true, InputPrice: 1, OutputPrice: 1,
	})

	_, body := postModelJSON(t, engine, "/ai/chat/completions",
		`{"model":"chat","messages":[{"role":"user","content":"hi"}]}`)
	data, _ := body["data"].(map[string]any)
	usage, _ := data["usage"].(map[string]any)
	if usage["missing"] != true {
		t.Fatalf("应该标记 usage 缺失: %+v", usage)
	}
	if data["cost"] != float64(0) {
		t.Fatalf("没有 usage 就不该算出成本: %+v", data["cost"])
	}
	var call model.ModelCall
	h.DB.Order("id desc").First(&call)
	if !call.UsageMissing || call.TotalTokens != 0 {
		t.Fatalf("流水没记下 usage 缺失: %+v", call)
	}

	// 200 + 空 choices 要当失败处理，不能把空回复当成功
	empty := newFakeOpenAI(t, &fakeOpenAI{emptyChoices: true})
	h.DB.Create(&model.ModelUpstream{
		Name: "空回复", Alias: "empty", BaseURL: empty.baseURL(), Model: "m",
		Weight: 10, TimeoutSec: 10, Enabled: true,
	})
	_, body = postModelJSON(t, engine, "/ai/chat/completions",
		`{"model":"empty","messages":[{"role":"user","content":"hi"}]}`)
	if body["code"] == float64(0) {
		t.Fatalf("空 choices 应该算失败: %+v", body)
	}
	msg, _ := body["msg"].(string)
	if !strings.Contains(msg, "内容被策略拦截") {
		t.Fatalf("没带上上游给的原因: %q", msg)
	}
}

func TestChatCompletionConsoleTestPicksGivenUpstream(t *testing.T) {
	fake := newFakeOpenAI(t, &fakeOpenAI{})
	h, engine := newModelTestHandler(t)
	// 停用的上游也要能单独试，否则「先试通再启用」做不到
	disabled := createUpstream(t, h, model.ModelUpstream{
		Name: "待验证", Alias: "chat", BaseURL: fake.baseURL(), Model: "m",
		Weight: 10, TimeoutSec: 10, Enabled: false,
	})

	_, body := postModelJSON(t, engine, "/ai/chat/completions",
		`{"upstreamId":`+itoa(disabled.ID)+`,"messages":[{"role":"user","content":"hi"}]}`)
	if body["code"] != float64(0) {
		t.Fatalf("指定上游试调用失败: %+v", body)
	}
	var call model.ModelCall
	h.DB.Order("id desc").First(&call)
	if call.Caller != "console" {
		t.Fatalf("界面试调用应该记成 console: %+v", call)
	}
}

func TestListModelCallsSummary(t *testing.T) {
	h, engine := newModelTestHandler(t)
	base := time.Now().Add(-time.Hour)
	rows := []model.ModelCall{
		{Alias: "chat", UpstreamName: "主力", Caller: "api", Username: "tester",
			PromptTokens: 10, CompletionTokens: 10, TotalTokens: 20, Cost: 0.02,
			LatencyMs: 100, CallStatus: "success", CreatedAt: base},
		{Alias: "chat", UpstreamName: "主力", Caller: "api", Username: "tester",
			TotalTokens: 0, CallStatus: "failed", ErrorMsg: "上游 500",
			LatencyMs: 300, CreatedAt: base.Add(time.Minute)},
		{Alias: "code", UpstreamName: "备用", Caller: "console", Username: "other",
			PromptTokens: 5, CompletionTokens: 5, TotalTokens: 10, Cost: 0.01,
			UsageMissing: true, LatencyMs: 200, CallStatus: "success",
			CreatedAt: base.Add(2 * time.Minute)},
	}
	for i := range rows {
		if err := h.DB.Create(&rows[i]).Error; err != nil {
			t.Fatalf("写入流水失败: %v", err)
		}
	}

	body := getModelJSON(t, engine, "/ai/calls?page=1&pageSize=10")
	data, _ := body["data"].(map[string]any)
	if data["total"] != float64(3) {
		t.Fatalf("总数不对: %+v", data["total"])
	}
	summary, _ := data["summary"].(map[string]any)
	if summary["calls"] != float64(3) || summary["failed"] != float64(1) {
		t.Fatalf("汇总不对: %+v", summary)
	}
	if summary["tokens"] != float64(30) || summary["cost"] != 0.03 {
		t.Fatalf("token/成本汇总不对: %+v", summary)
	}
	if summary["avgLatencyMs"] != float64(200) {
		t.Fatalf("平均耗时不对: %+v", summary["avgLatencyMs"])
	}
	if summary["usageMissing"] != float64(1) {
		t.Fatalf("usage 缺失计数不对: %+v", summary["usageMissing"])
	}

	// 汇总必须跟着筛选走，而不是只算当前页
	body = getModelJSON(t, engine, "/ai/calls?page=1&pageSize=10&alias=chat")
	data, _ = body["data"].(map[string]any)
	summary, _ = data["summary"].(map[string]any)
	if data["total"] != float64(2) || summary["tokens"] != float64(20) {
		t.Fatalf("按 alias 筛选后的汇总不对: %+v / %+v", data["total"], summary)
	}
	body = getModelJSON(t, engine, "/ai/calls?page=1&pageSize=10&status=failed")
	data, _ = body["data"].(map[string]any)
	if data["total"] != float64(1) {
		t.Fatalf("按状态筛选不对: %+v", data["total"])
	}
	body = getModelJSON(t, engine, "/ai/calls?page=1&pageSize=10&caller=console")
	data, _ = body["data"].(map[string]any)
	if data["total"] != float64(1) {
		t.Fatalf("按来源筛选不对: %+v", data["total"])
	}
	body = getModelJSON(t, engine, "/ai/calls?page=1&pageSize=10&start=2000-01-01&end=2000-01-02")
	data, _ = body["data"].(map[string]any)
	if data["total"] != float64(0) {
		t.Fatalf("历史时间范围应该为空: %+v", data["total"])
	}
}

func TestListModelUpstreamsGroupsPools(t *testing.T) {
	h, engine := newModelTestHandler(t)
	h.DB.Create(&model.ModelUpstream{Name: "主力", Alias: "chat", BaseURL: "http://a/v1",
		Model: "m1", Weight: 20, Enabled: true, ModelStatus: "healthy"})
	h.DB.Create(&model.ModelUpstream{Name: "备用", Alias: "chat", BaseURL: "http://b/v1",
		Model: "m2", Weight: 10, Enabled: true, ModelStatus: "error"})
	createUpstream(t, h, model.ModelUpstream{Name: "代码", Alias: "code", BaseURL: "http://c/v1",
		Model: "m3", Weight: 10, Enabled: false, ModelStatus: "healthy"})

	body := getModelJSON(t, engine, "/ai/upstreams")
	data, _ := body["data"].(map[string]any)
	list, _ := data["list"].([]any)
	if len(list) != 3 {
		t.Fatalf("上游数量不对: %d", len(list))
	}
	// 密钥绝不能出接口
	if raw, _ := json.Marshal(list); strings.Contains(string(raw), "apiKey") {
		t.Fatalf("响应里出现了 apiKey 字段: %s", raw)
	}
	pools, _ := data["pools"].([]any)
	if len(pools) != 2 {
		t.Fatalf("池子数量不对: %+v", pools)
	}
	first, _ := pools[0].(map[string]any)
	if first["alias"] != "chat" || first["total"] != float64(2) || first["healthy"] != float64(1) {
		t.Fatalf("chat 池统计不对: %+v", first)
	}
	second, _ := pools[1].(map[string]any)
	// 停用的上游不算进「可用」，否则页面会让人误以为这个 Alias 能调
	if second["alias"] != "code" || second["enabled"] != float64(0) || second["healthy"] != float64(0) {
		t.Fatalf("code 池统计不对: %+v", second)
	}
}

func itoa(v uint) string {
	return strconv.FormatUint(uint64(v), 10)
}
