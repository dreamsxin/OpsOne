package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"ops-platform/server/internal/config"
	"ops-platform/server/internal/model"
)

func TestAgentConfigReqNormalize(t *testing.T) {
	req := agentConfigReq{
		Name: " 告警归因 ", Alias: " chat ", DataSource: "alert",
		PromptTemplate: "下面是告警：\n{{.context}}\n请给出可能原因。{{.input}}",
	}
	if err := req.normalize(); err != nil {
		t.Fatalf("合法配置被拒: %v", err)
	}
	if req.MaxItems != 20 || req.MaxTokens != 800 {
		t.Fatalf("默认值不对: %+v", req)
	}

	temp := 5.0
	cases := []struct {
		name string
		req  agentConfigReq
		want string
	}{
		{"没名字", agentConfigReq{Alias: "chat", PromptTemplate: "x"}, "名称"},
		{"没选模型", agentConfigReq{Name: "a", PromptTemplate: "x"}, "Alias"},
		{"模板为空", agentConfigReq{Name: "a", Alias: "chat"}, "提示词模板不能为空"},
		{"数据来源不支持", agentConfigReq{Name: "a", Alias: "chat", DataSource: "kube",
			PromptTemplate: "{{.context}}"}, "不支持"},
		{"模板语法错", agentConfigReq{Name: "a", Alias: "chat",
			PromptTemplate: "{{.context"}, "语法"},
		// 最容易犯的错：取了数据却没放进提示词，模型什么都看不到
		{"取了数据但模板里没用", agentConfigReq{Name: "a", Alias: "chat", DataSource: "alert",
			PromptTemplate: "请分析一下"}, "{{.context}}"},
		{"条数越界", agentConfigReq{Name: "a", Alias: "chat", PromptTemplate: "x",
			MaxItems: 999}, "上下文条数"},
		{"回复上限越界", agentConfigReq{Name: "a", Alias: "chat", PromptTemplate: "x",
			MaxTokens: 10}, "回复长度上限"},
		{"temperature 越界", agentConfigReq{Name: "a", Alias: "chat", PromptTemplate: "x",
			Temperature: &temp}, "temperature"},
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

func TestRenderAgentPrompt(t *testing.T) {
	got, err := renderAgentPrompt("数据：{{.context}} / 补充：{{.input}}", "CPU 95%", "重点看磁盘")
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	if got != "数据：CPU 95% / 补充：重点看磁盘" {
		t.Fatalf("渲染结果不对: %q", got)
	}
	// 超长提示词要截断并留下痕迹，而不是原样发出去
	long := strings.Repeat("x", agentPromptMaxChars+100)
	got, err = renderAgentPrompt("{{.context}}", long, "")
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	if !strings.HasSuffix(got, "（提示词过长已截断）") {
		t.Fatalf("超长提示词没被截断: len=%d", len(got))
	}
	if _, err := renderAgentPrompt("{{.nope", "", ""); err == nil {
		t.Fatal("坏模板应该报错")
	}
}

// newAgentTestHandler 内存库，带上 Agent 需要读的那些表
func newAgentTestHandler(t *testing.T) (*Handler, *gin.Engine) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	g, err := gorm.Open(sqlite.Open("file::memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开内存库失败: %v", err)
	}
	if err := g.AutoMigrate(&model.ModelUpstream{}, &model.ModelCall{},
		&model.AgentConfig{}, &model.AgentRun{}, &model.Alert{}, &model.Host{},
		&model.HostMetric{}, &model.ExecJob{}, &model.ExecResult{},
		&model.Session{}, &model.SessionCommand{}); err != nil {
		t.Fatalf("建表失败: %v", err)
	}
	h := New(g, &config.Config{})

	engine := gin.New()
	engine.Use(func(c *gin.Context) {
		c.Set("ctx_user", &model.User{ID: 3, Username: "opsuser"})
		c.Next()
	})
	engine.POST("/ai/agents/:id/run", h.RunAgent)
	engine.GET("/ai/agent-runs", h.ListAgentRuns)
	engine.GET("/ai/agent-runs/:id", h.GetAgentRun)
	return h, engine
}

func TestBuildAgentContextAlert(t *testing.T) {
	h, _ := newAgentTestHandler(t)
	now := time.Now()
	rows := []model.Alert{
		{Title: "web-01 CPU 高", Summary: "CPU 95%", Severity: "critical", Status: "firing",
			Value: "95%", Count: 3, Labels: `{"host":"web-01"}`, LastSeenAt: now},
		{Title: "db-01 磁盘将满", Summary: "/ 92%", Severity: "warning", Status: "acked",
			Count: 1, LastSeenAt: now.Add(-time.Minute)},
		// 已恢复的不该进上下文：分析当前问题时它只是噪音
		{Title: "旧告警", Summary: "已恢复", Severity: "critical", Status: "resolved",
			Count: 1, LastSeenAt: now.Add(-time.Hour)},
	}
	for i := range rows {
		if err := h.DB.Create(&rows[i]).Error; err != nil {
			t.Fatalf("写入告警失败: %v", err)
		}
	}

	agent := &model.AgentConfig{DataSource: "alert", MaxItems: 20}
	ctxData, err := h.buildAgentContext(agent, 0, "")
	if err != nil {
		t.Fatalf("拼上下文失败: %v", err)
	}
	if ctxData.Items != 2 {
		t.Fatalf("应该只取未恢复的 2 条，实际 %d", ctxData.Items)
	}
	if strings.Contains(ctxData.Text, "旧告警") {
		t.Fatal("已恢复的告警不该进上下文")
	}
	if !strings.Contains(ctxData.Text, "web-01 CPU 高") || !strings.Contains(ctxData.Text, "累计3次") {
		t.Fatalf("上下文缺少关键信息: %s", ctxData.Text)
	}
	if !strings.Contains(ctxData.Text, "host=web-01") {
		t.Fatalf("标签没带上: %s", ctxData.Text)
	}

	// 按级别筛
	ctxData, err = h.buildAgentContext(agent, 0, "warning")
	if err != nil {
		t.Fatalf("拼上下文失败: %v", err)
	}
	if ctxData.Items != 1 || !strings.Contains(ctxData.Text, "db-01") {
		t.Fatalf("按级别筛选不对: %+v", ctxData)
	}

	// 条数上限：撞到上限要标记 truncated，否则看不出还有更多数据没进去
	agent.MaxItems = 1
	ctxData, err = h.buildAgentContext(agent, 0, "")
	if err != nil {
		t.Fatalf("拼上下文失败: %v", err)
	}
	if ctxData.Items != 1 || !ctxData.Truncated {
		t.Fatalf("上限截断没标记: %+v", ctxData)
	}
}

func TestBuildAgentContextHostMetricAndExecJob(t *testing.T) {
	h, _ := newAgentTestHandler(t)

	host := model.Host{Name: "web-01", Address: "10.0.0.1"}
	h.DB.Create(&host)
	h.DB.Create(&model.HostMetric{HostID: host.ID, CPUPercent: 91.5, MemPercent: 70.2,
		DiskMaxPercent: 88.8, DiskMaxMount: "/", Load1: 7.5, CPUCores: 4,
		ProcCount: 210, TCPConn: 88, Status: "ok", CreatedAt: time.Now()})

	agent := &model.AgentConfig{DataSource: "host_metric", MaxItems: 10}
	ctxData, err := h.buildAgentContext(agent, host.ID, "")
	if err != nil {
		t.Fatalf("拼主机指标失败: %v", err)
	}
	if !strings.Contains(ctxData.Text, "web-01") || !strings.Contains(ctxData.Text, "91.5") {
		t.Fatalf("主机指标上下文不对: %s", ctxData.Text)
	}
	if !strings.Contains(ctxData.Text, "4 核") {
		t.Fatalf("核数没给出来，模型没法判断 load 高不高: %s", ctxData.Text)
	}
	// 没有采集数据时要明确报错，而不是给一个空上下文让模型瞎编
	if _, err := h.buildAgentContext(agent, 999, ""); err == nil {
		t.Fatal("主机不存在应该报错")
	}
	empty := model.Host{Name: "new-host", Address: "10.0.0.9"}
	h.DB.Create(&empty)
	if _, err := h.buildAgentContext(agent, empty.ID, ""); err == nil ||
		!strings.Contains(err.Error(), "还没有采集到指标") {
		t.Fatalf("没有指标时应该明确报错: %v", err)
	}

	job := model.ExecJob{Name: "重启服务", Command: "systemctl restart nginx",
		Status: "failed", Total: 3, SuccessNum: 1, FailedNum: 2}
	h.DB.Create(&job)
	h.DB.Create(&model.ExecResult{JobID: job.ID, HostName: "ok-01", Address: "10.0.0.2",
		Status: "success", ExitCode: 0, Stdout: "done", CostMs: 120})
	h.DB.Create(&model.ExecResult{JobID: job.ID, HostName: "bad-01", Address: "10.0.0.3",
		Status: "failed", ExitCode: 1, Stderr: "Unit nginx.service not found\nsee logs",
		CostMs: 80})

	agent = &model.AgentConfig{DataSource: "exec_job", MaxItems: 10}
	ctxData, err = h.buildAgentContext(agent, job.ID, "")
	if err != nil {
		t.Fatalf("拼执行结果失败: %v", err)
	}
	if ctxData.Items != 2 {
		t.Fatalf("结果条数不对: %d", ctxData.Items)
	}
	// 失败的排前面：要分析的正是它们
	badIdx := strings.Index(ctxData.Text, "bad-01")
	okIdx := strings.Index(ctxData.Text, "ok-01")
	if badIdx < 0 || okIdx < 0 || badIdx > okIdx {
		t.Fatalf("失败结果应该排在前面: %s", ctxData.Text)
	}
	// 多行 stderr 要压成一行，否则提示词会被换行撑散
	if !strings.Contains(ctxData.Text, "Unit nginx.service not found / see logs") {
		t.Fatalf("stderr 没被压成一行: %s", ctxData.Text)
	}
}

func TestBuildAgentContextSessionCommand(t *testing.T) {
	h, _ := newAgentTestHandler(t)
	session := model.Session{Username: "alice", LoginUser: "root", HostName: "web-01",
		Address: "10.0.0.1", Status: "closed", StartedAt: time.Now()}
	h.DB.Create(&session)
	h.DB.Create(&model.SessionCommand{SessionID: session.ID, Command: "whoami",
		Risk: "normal", OffsetMs: 1200})
	h.DB.Create(&model.SessionCommand{SessionID: session.ID, Command: "rm -rf /tmp/x",
		Risk: "blocked", RuleDesc: "禁止 rm -rf", OffsetMs: 3400})

	agent := &model.AgentConfig{DataSource: "session_command", MaxItems: 50}
	ctxData, err := h.buildAgentContext(agent, session.ID, "")
	if err != nil {
		t.Fatalf("拼会话命令失败: %v", err)
	}
	if ctxData.Items != 2 {
		t.Fatalf("命令条数不对: %d", ctxData.Items)
	}
	if !strings.Contains(ctxData.Text, "alice") || !strings.Contains(ctxData.Text, "root") {
		t.Fatalf("会话上下文缺少操作人信息: %s", ctxData.Text)
	}
	// 风险与命中规则要带上，这是判断「这次操作危不危险」的关键
	if !strings.Contains(ctxData.Text, "blocked") || !strings.Contains(ctxData.Text, "禁止 rm -rf") {
		t.Fatalf("风险信息没带上: %s", ctxData.Text)
	}
	if !strings.Contains(ctxData.Text, "[+1.2s]") {
		t.Fatalf("时间偏移没带上: %s", ctxData.Text)
	}
}

func TestRunAgentEndToEnd(t *testing.T) {
	fake := newFakeOpenAI(t, &fakeOpenAI{models: []string{"m"}})
	h, engine := newAgentTestHandler(t)
	h.DB.Create(&model.ModelUpstream{Name: "主力", Alias: "chat", BaseURL: fake.baseURL(),
		Model: "m", Weight: 10, TimeoutSec: 30, Enabled: true, InputPrice: 1, OutputPrice: 1})
	h.DB.Create(&model.Alert{Title: "web-01 CPU 高", Summary: "CPU 95%", Severity: "critical",
		Status: "firing", Count: 2, LastSeenAt: time.Now()})
	agent := model.AgentConfig{
		Name: "告警归因", Alias: "chat", DataSource: "alert", MaxItems: 10,
		SystemPrompt: "你是运维专家", PromptTemplate: "告警：\n{{.context}}\n补充：{{.input}}",
		MaxTokens: 256, Enabled: true,
	}
	h.DB.Create(&agent)

	code, body := postModelJSON(t, engine, "/ai/agents/"+itoa(agent.ID)+"/run",
		`{"input":"只看 CPU"}`)
	if code != http.StatusOK || body["code"] != float64(0) {
		t.Fatalf("运行失败: %d %+v", code, body)
	}
	data, _ := body["data"].(map[string]any)
	prompt, _ := data["prompt"].(string)
	// 上下文与补充说明都要真的进提示词
	if !strings.Contains(prompt, "web-01 CPU 高") || !strings.Contains(prompt, "只看 CPU") {
		t.Fatalf("提示词没带上下文或补充: %q", prompt)
	}

	var run model.AgentRun
	if err := h.DB.Order("id desc").First(&run).Error; err != nil {
		t.Fatalf("没有运行记录: %v", err)
	}
	if run.RunStatus != "success" || run.Output == "" {
		t.Fatalf("运行记录不对: %+v", run)
	}
	if run.ContextItems != 1 || run.ContextChars == 0 {
		t.Fatalf("上下文统计不对: %+v", run)
	}
	if run.TotalTokens != 20 || run.Cost == 0 || run.LatencyMs < 0 {
		t.Fatalf("用量没记进运行记录: %+v", run)
	}
	if run.Username != "opsuser" {
		t.Fatalf("运行人不对: %+v", run)
	}

	// Agent 的调用必须进模型调用流水，否则用量与成本页会少算一块
	var call model.ModelCall
	if err := h.DB.Order("id desc").First(&call).Error; err != nil {
		t.Fatalf("没有调用流水: %v", err)
	}
	if call.Caller != "agent" || call.CallStatus != "success" {
		t.Fatalf("流水来源不对: %+v", call)
	}
	if run.CallID != call.ID {
		t.Fatalf("运行记录没指向流水: run.CallID=%d call.ID=%d", run.CallID, call.ID)
	}

	// system 提示词要作为 system 消息发出去（假上游把收到的消息回显在内容里不方便断言，
	// 这里退一步确认运行成功且 prompt 不含 system 文本，避免把两者拼在一起发）
	if strings.Contains(prompt, "你是运维专家") {
		t.Fatal("system 提示词不该被拼进 user 消息")
	}

	// 列表不返回正文（结论动辄几千字），详情才给
	listBody := getModelJSON(t, engine, "/ai/agent-runs?page=1&pageSize=10")
	listData, _ := listBody["data"].(map[string]any)
	list, _ := listData["list"].([]any)
	if len(list) != 1 {
		t.Fatalf("运行记录列表条数不对: %d", len(list))
	}
	first, _ := list[0].(map[string]any)
	if first["output"] != "" {
		t.Fatalf("列表不该带正文: %+v", first["output"])
	}
	detail := getModelJSON(t, engine, "/ai/agent-runs/"+itoa(run.ID))
	detailData, _ := detail["data"].(map[string]any)
	if detailData["output"] == "" {
		t.Fatal("详情应该带正文")
	}
}

func TestRunAgentRecordsContextFailure(t *testing.T) {
	fake := newFakeOpenAI(t, &fakeOpenAI{})
	h, engine := newAgentTestHandler(t)
	h.DB.Create(&model.ModelUpstream{Name: "主力", Alias: "chat", BaseURL: fake.baseURL(),
		Model: "m", Weight: 10, TimeoutSec: 30, Enabled: true})
	agent := model.AgentConfig{
		Name: "主机体检", Alias: "chat", DataSource: "host_metric", MaxItems: 10,
		PromptTemplate: "{{.context}}", MaxTokens: 128, Enabled: true,
	}
	h.DB.Create(&agent)

	// 需要目标却没传：要拦在前面，并且不该产生任何模型调用
	_, body := postModelJSON(t, engine, "/ai/agents/"+itoa(agent.ID)+"/run", `{}`)
	if body["code"] == float64(0) {
		t.Fatalf("缺目标应该被拒: %+v", body)
	}
	if msg, _ := body["msg"].(string); !strings.Contains(msg, "需要指定目标") {
		t.Fatalf("报错不明确: %q", msg)
	}

	// 目标不存在：失败也要落一条运行记录，否则「跑了没结果」查不到
	_, body = postModelJSON(t, engine, "/ai/agents/"+itoa(agent.ID)+"/run", `{"targetId":999}`)
	if body["code"] == float64(0) {
		t.Fatalf("目标不存在应该被拒: %+v", body)
	}
	var run model.AgentRun
	if err := h.DB.Order("id desc").First(&run).Error; err != nil {
		t.Fatalf("失败也该有运行记录: %v", err)
	}
	if run.RunStatus != "failed" || !strings.Contains(run.ErrorMsg, "主机不存在") {
		t.Fatalf("失败记录内容不对: %+v", run)
	}
	var calls int64
	h.DB.Model(&model.ModelCall{}).Count(&calls)
	if calls != 0 {
		t.Fatalf("上下文都没拼成，不该调模型: %d 条流水", calls)
	}
	if fake.hits.Load() != 0 {
		t.Fatalf("上游被调用了 %d 次", fake.hits.Load())
	}
}

func TestRunAgentDisabled(t *testing.T) {
	h, engine := newAgentTestHandler(t)
	agent := model.AgentConfig{
		Name: "停用的", Alias: "chat", DataSource: "none",
		PromptTemplate: "{{.input}}", MaxTokens: 128, Enabled: false,
	}
	h.DB.Create(&agent)
	// enabled 带 gorm default，得写回去才真的是 false
	h.DB.Model(&model.AgentConfig{}).Where("id = ?", agent.ID).
		Updates(map[string]any{"enabled": false})

	_, body := postModelJSON(t, engine, "/ai/agents/"+itoa(agent.ID)+"/run", `{"input":"hi"}`)
	if body["code"] == float64(0) {
		t.Fatalf("停用的 Agent 不该能跑: %+v", body)
	}
	if msg, _ := body["msg"].(string); !strings.Contains(msg, "已停用") {
		t.Fatalf("报错不明确: %q", msg)
	}
}

func TestListAgentRunsFilters(t *testing.T) {
	h, engine := newAgentTestHandler(t)
	base := time.Now().Add(-time.Hour)
	rows := []model.AgentRun{
		{AgentID: 1, AgentName: "告警归因", Alias: "chat", DataSource: "alert",
			RunStatus: "success", Username: "alice", Output: "结论 A", CreatedAt: base},
		{AgentID: 1, AgentName: "告警归因", Alias: "chat", DataSource: "alert",
			RunStatus: "failed", Username: "alice", ErrorMsg: "上游超时",
			CreatedAt: base.Add(time.Minute)},
		{AgentID: 2, AgentName: "主机体检", Alias: "chat", DataSource: "host_metric",
			RunStatus: "success", Username: "bob", Output: "结论 B",
			CreatedAt: base.Add(2 * time.Minute)},
	}
	for i := range rows {
		if err := h.DB.Create(&rows[i]).Error; err != nil {
			t.Fatalf("写入运行记录失败: %v", err)
		}
	}

	check := func(query string, want int) {
		body := getModelJSON(t, engine, "/ai/agent-runs?page=1&pageSize=10&"+query)
		data, _ := body["data"].(map[string]any)
		if data["total"] != float64(want) {
			t.Fatalf("%s 期望 %d 条，实际 %v", query, want, data["total"])
		}
	}
	check("agentId=1", 2)
	check("status=failed", 1)
	check("dataSource=host_metric", 1)
	check("username=ali", 2)
	check("start=2000-01-01&end=2000-01-02", 0)

	body := getModelJSON(t, engine, "/ai/agent-runs?page=1&pageSize=10&agentId=abc")
	if body["code"] == float64(0) {
		t.Fatalf("非法 agentId 应该被拒: %+v", body)
	}
}

// 确认响应里的字段名与前端约定一致（status / dataSource 这些是改过 gorm 列名的）
func TestAgentRunJSONFields(t *testing.T) {
	raw, err := json.Marshal(model.AgentRun{RunStatus: "success", DataSource: "alert"})
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("反序列化失败: %v", err)
	}
	if decoded["status"] != "success" || decoded["dataSource"] != "alert" {
		t.Fatalf("字段名不对: %s", raw)
	}
	if _, ok := decoded["runStatus"]; ok {
		t.Fatalf("不该出现 runStatus: %s", raw)
	}
}

func TestAgentDataSourceMetaExposed(t *testing.T) {
	h, _ := newAgentTestHandler(t)
	engine := gin.New()
	engine.Use(func(c *gin.Context) {
		c.Set("ctx_user", &model.User{ID: 1, Username: "t"})
		c.Next()
	})
	engine.GET("/ai/agents", h.ListAgentConfigs)

	body := getModelJSON(t, engine, "/ai/agents")
	data, _ := body["data"].(map[string]any)
	sources, _ := data["dataSources"].([]any)
	if len(sources) != 5 {
		t.Fatalf("数据来源数量不对: %d", len(sources))
	}
	// 界面要靠 needsTarget 决定是否显示目标选择框
	found := false
	for _, item := range sources {
		entry, _ := item.(map[string]any)
		if entry["key"] == "host_metric" {
			found = true
			if entry["needsTarget"] != true {
				t.Fatalf("host_metric 应该需要目标: %+v", entry)
			}
		}
		if entry["key"] == "alert" && entry["needsTarget"] != false {
			t.Fatalf("alert 不该需要目标: %+v", entry)
		}
	}
	if !found {
		t.Fatal("没有 host_metric 来源")
	}
}

var _ = httptest.NewRequest
