package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"ops-platform/server/internal/config"
	"ops-platform/server/internal/model"
)

func newNotifyTplTestHandler(t *testing.T) (*Handler, *gin.Engine) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	dir := t.TempDir()
	g, err := gorm.Open(sqlite.Open(dir+"/notifytpl.db"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	if err := g.AutoMigrate(
		&model.NotifyTemplate{}, &model.NotifyChannel{}, &model.Alert{}, &model.User{},
	); err != nil {
		t.Fatalf("建表失败: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, err := g.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})

	h := New(g, &config.Config{})
	engine := gin.New()
	engine.Use(func(c *gin.Context) {
		c.Set("ctx_user", &model.User{ID: 1, Username: "admin"})
		c.Next()
	})
	engine.GET("/notify/template-vars", h.ListNotifyTemplateVars)
	engine.GET("/notify/templates", h.ListNotifyTemplates)
	engine.POST("/notify/templates", h.CreateNotifyTemplate)
	engine.PUT("/notify/templates/:id", h.UpdateNotifyTemplate)
	engine.DELETE("/notify/templates/:id", h.DeleteNotifyTemplate)
	engine.POST("/notify/templates/:id/preview", h.PreviewNotifyTemplate)
	return h, engine
}

func tplJSON(t *testing.T, engine *gin.Engine, method, path, body string) (int, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	engine.ServeHTTP(rec, req)
	var parsed map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &parsed); err != nil {
		t.Fatalf("响应不是 JSON: %s", rec.Body.String())
	}
	return rec.Code, parsed
}

// TestNotifyVarTableIsSingleSource 这一条是整块改造的核心承诺：
// 变量表只有一份权威定义，真实发信用的变量、样例变量、界面清单必须完全对得上。
// 改造之前这三处（再加内置模板的 Variables 提示串）各写一遍，对得上是人工维护的巧合。
func TestNotifyVarTableIsSingleSource(t *testing.T) {
	alert := model.Alert{Title: "t", Severity: "critical", SourceName: "s", Status: "firing"}
	realKeys := make([]string, 0)
	for key := range alertMailVars(alert) {
		realKeys = append(realKeys, key)
	}
	sampleKeys := make([]string, 0)
	for key := range sampleAlertVars() {
		sampleKeys = append(sampleKeys, key)
	}
	specKeys := make([]string, 0)
	for key := range sceneVarKeys(sceneAlert) {
		specKeys = append(specKeys, key)
	}
	sort.Strings(realKeys)
	sort.Strings(sampleKeys)
	sort.Strings(specKeys)

	if strings.Join(realKeys, ",") != strings.Join(specKeys, ",") {
		t.Errorf("真实发信变量与权威表不一致:\n真实 %v\n权威 %v", realKeys, specKeys)
	}
	if strings.Join(sampleKeys, ",") != strings.Join(specKeys, ",") {
		t.Errorf("样例变量与权威表不一致:\n样例 %v\n权威 %v", sampleKeys, specKeys)
	}
}

// TestOnCallVarsCoverScene 值班呼叫场景的变量必须齐 ——
// 修的就是「站内消息里有、邮件里没有」那个缺口，变量缺一个就等于没修全
func TestOnCallVarsCoverScene(t *testing.T) {
	vars := onCallVars(model.OnCallSchedule{Name: "核心值班"}, model.Alert{
		Title: "磁盘满", Severity: "critical", Summary: "/data 92%", Count: 3,
	}, 2, "张三", "值班呼叫（第 2 级）：磁盘满")

	for key := range sceneVarKeys(sceneOnCall) {
		if _, ok := vars[key]; !ok {
			t.Errorf("值班呼叫变量缺少 %q", key)
		}
	}
	// 这几个是「看出这是在叫自己」的关键信息
	if vars["scheduleName"] != "核心值班" || vars["level"] != "2" || vars["assignee"] != "张三" {
		t.Errorf("关键字段不对: %v", vars)
	}
	if !strings.Contains(vars["title"], "第 2 级") {
		t.Errorf("标题要带层级: %q", vars["title"])
	}
}

func TestValidateNotifyTemplate(t *testing.T) {
	// 合法：IM 文本引用已知变量
	if err := validateNotifyTemplate(tplKindIM, sceneAlert,
		"[{{.severity}}] {{.title}}\n{{.summary}}"); err != nil {
		t.Errorf("合法模板被拒: %v", err)
	}

	// 变量白名单：写错变量名必须在**保存时**就被拒。
	// text/template 对 map 的缺失 key 会静默渲染成空串，不校验就要到真发信时才发现
	err := validateNotifyTemplate(tplKindIM, sceneAlert, "{{.titel}} {{.nonsense}}")
	if err == nil {
		t.Fatal("引用了不存在的变量应当被拒")
	}
	for _, want := range []string{"titel", "nonsense", "可用的是"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("错误信息要点出错在哪、可用的是什么: %v", err)
		}
	}

	// 场景不同变量不同：oncall 的变量不能用在 alert 场景里
	if err := validateNotifyTemplate(tplKindIM, sceneAlert, "{{.scheduleName}}"); err == nil {
		t.Error("跨场景的变量应当被拒")
	}
	if err := validateNotifyTemplate(tplKindIM, sceneOnCall, "{{.scheduleName}}"); err != nil {
		t.Errorf("oncall 场景应当允许 scheduleName: %v", err)
	}

	// 语法错误
	if err := validateNotifyTemplate(tplKindIM, sceneAlert, "{{.title"); err == nil {
		t.Error("语法错误应当被拒")
	}
	// 空正文
	if err := validateNotifyTemplate(tplKindIM, sceneAlert, "   "); err == nil {
		t.Error("空正文应当被拒")
	}
	// 未知场景 / 未知类型
	if err := validateNotifyTemplate(tplKindIM, "nope", "{{.title}}"); err == nil {
		t.Error("未知场景应当被拒")
	}
	if err := validateNotifyTemplate("sms", sceneAlert, "{{.title}}"); err == nil {
		t.Error("未知类型应当被拒")
	}
}

// TestValidateWebhookTemplateJSON webhook 模板渲染后必须是合法 JSON 对象，
// 而且这件事要在**保存时**发现 —— 不然接收端会收到一坨语法错误的东西。
func TestValidateWebhookTemplateJSON(t *testing.T) {
	if err := validateNotifyTemplate(tplKindWebhook, sceneAlert,
		`{"text":"{{.title}}","level":"{{.severity}}"}`); err != nil {
		t.Errorf("合法 JSON 模板被拒: %v", err)
	}
	// 少一个引号
	err := validateNotifyTemplate(tplKindWebhook, sceneAlert, `{"text":{{.title}}}`)
	if err == nil {
		t.Fatal("渲染出来不是合法 JSON 应当被拒")
	}
	if !strings.Contains(err.Error(), "JSON") {
		t.Errorf("错误信息要说清是 JSON 问题: %v", err)
	}
	// 顶层是数组也不行：报文就是一个对象
	if err := validateNotifyTemplate(tplKindWebhook, sceneAlert,
		`["{{.title}}"]`); err == nil {
		t.Error("顶层是数组应当被拒")
	}
	// IM 类不做 JSON 校验
	if err := validateNotifyTemplate(tplKindIM, sceneAlert, "{{.title}} 不是 JSON"); err != nil {
		t.Errorf("IM 模板不该做 JSON 校验: %v", err)
	}
}

// TestRenderIMBodyFallsBack 这是这一块最重要的一条：**模板是可选的覆盖，不是前置条件**。
// 没配、配了但停用、配了但类型对不上、渲染失败 —— 四种情况都必须回落到原来的硬编码格式，
// 而不是发不出去。
func TestRenderIMBodyFallsBack(t *testing.T) {
	h, _ := newNotifyTplTestHandler(t)
	alert := model.Alert{
		Title: "磁盘满", Severity: "critical", Status: "firing",
		SourceName: "巡检", Summary: "/data 92%", Value: "92%", Count: 3,
	}
	builtin := imAlertText(alert)

	// 1. 没配模板
	if got := h.renderIMBody(model.NotifyChannel{Type: "wecom"}, alert); got != builtin {
		t.Error("没配模板时应当用默认格式")
	}
	// 2. 配了但模板不存在
	if got := h.renderIMBody(model.NotifyChannel{Type: "wecom", TemplateCode: "nope"}, alert); got != builtin {
		t.Error("模板不存在时应当回落")
	}
	// 3. 配了但停用
	h.DB.Create(&model.NotifyTemplate{
		Code: "off", Name: "停用的", Kind: tplKindIM, Scene: sceneAlert,
		Body: "自定义 {{.title}}", Enabled: false,
	})
	if got := h.renderIMBody(model.NotifyChannel{Type: "wecom", TemplateCode: "off"}, alert); got != builtin {
		t.Error("模板停用时应当回落")
	}
	// 4. 类型对不上（拿 webhook 模板给 IM 用）
	h.DB.Create(&model.NotifyTemplate{
		Code: "wrongkind", Name: "类型不对", Kind: tplKindWebhook, Scene: sceneAlert,
		Body: `{"a":"{{.title}}"}`, Enabled: true,
	})
	if got := h.renderIMBody(model.NotifyChannel{Type: "wecom", TemplateCode: "wrongkind"}, alert); got != builtin {
		t.Error("模板类型对不上时应当回落")
	}

	// 5. 正常生效
	h.DB.Create(&model.NotifyTemplate{
		Code: "ok", Name: "精简", Kind: tplKindIM, Scene: sceneAlert,
		Body: "简报：{{.title}} / {{.severity}}", Enabled: true,
	})
	got := h.renderIMBody(model.NotifyChannel{Type: "wecom", TemplateCode: "ok"}, alert)
	if got != "简报：磁盘满 / critical" {
		t.Errorf("模板没生效: %q", got)
	}
}

// TestRenderWebhookBodyFallsBackWithWarning 渲染结果不是合法 JSON 时回落到默认报文，
// **并且把原因带回去落进投递流水** —— 否则使用者只会发现「模板像是没起作用」，查不到原因。
func TestRenderWebhookBodyFallsBackWithWarning(t *testing.T) {
	h, _ := newNotifyTplTestHandler(t)
	// summary 里带一个双引号，运行期会破掉 JSON（保存时用样例变量校验是过的）
	alert := model.Alert{Title: "标题", Severity: "warning", Summary: `他说 "满了"`}
	h.DB.Create(&model.NotifyTemplate{
		Code: "risky", Name: "有风险的", Kind: tplKindWebhook, Scene: sceneAlert,
		Body: `{"text":"{{.summary}}"}`, Enabled: true,
	})

	payload, warn := h.renderWebhookBody(
		model.NotifyChannel{Type: "webhook", TemplateCode: "risky"}, alert)
	if warn == "" {
		t.Fatal("模板没生效时必须说明原因，否则查不出来")
	}
	if !strings.Contains(warn, "JSON") {
		t.Errorf("原因要说清: %q", warn)
	}
	// 回落到默认报文：字段形状是对接收端的契约
	if _, ok := payload["alertId"]; !ok {
		t.Errorf("应当回落到默认报文（带 alertId 等字段）: %v", payload)
	}

	// 没配模板时不该有告警信息
	if _, warn := h.renderWebhookBody(model.NotifyChannel{Type: "webhook"}, alert); warn != "" {
		t.Errorf("没配模板不是异常，不该带告警信息: %q", warn)
	}
}

func TestNotifyTemplateCRUD(t *testing.T) {
	h, engine := newNotifyTplTestHandler(t)

	code, resp := tplJSON(t, engine, http.MethodPost, "/notify/templates",
		`{"code":"a.im","name":"精简","kind":"im","scene":"alert","body":"{{.title}}"}`)
	if code != http.StatusOK {
		t.Fatalf("创建失败 %d: %v", code, resp)
	}
	// 编码必填
	if code, _ := tplJSON(t, engine, http.MethodPost, "/notify/templates",
		`{"name":"x","kind":"im","scene":"alert","body":"{{.title}}"}`); code != http.StatusBadRequest {
		t.Error("缺编码应当被拒")
	}
	// 编码重复
	if code, _ := tplJSON(t, engine, http.MethodPost, "/notify/templates",
		`{"code":"a.im","name":"y","kind":"im","scene":"alert","body":"{{.title}}"}`); code != http.StatusBadRequest {
		t.Error("重复编码应当被拒")
	}
	// 坏变量在创建时就被拦
	if code, _ := tplJSON(t, engine, http.MethodPost, "/notify/templates",
		`{"code":"b.im","name":"z","kind":"im","scene":"alert","body":"{{.wrongvar}}"}`); code != http.StatusBadRequest {
		t.Error("坏变量应当被拒")
	}

	// 预览
	code, resp = tplJSON(t, engine, http.MethodPost, "/notify/templates/1/preview", `{}`)
	if code != http.StatusOK {
		t.Fatalf("预览失败 %d: %v", code, resp)
	}
	data := resp["data"].(map[string]any)
	if data["rendered"] != "web-01 磁盘使用率过高" {
		t.Errorf("预览用的应当是权威表里的样例值: %v", data["rendered"])
	}

	// 有渠道引用时不许删：删掉之后那些渠道会静默回落到硬编码格式，使用者查不到原因
	h.DB.Create(&model.NotifyChannel{Name: "群机器人", Type: "wecom", TemplateCode: "a.im", Enabled: true})
	code, resp = tplJSON(t, engine, http.MethodDelete, "/notify/templates/1", "")
	if code != http.StatusBadRequest {
		t.Fatalf("有渠道引用时应当拒删, got %d", code)
	}
	if !strings.Contains(resp["msg"].(string), "渠道") {
		t.Errorf("要说清是被谁引用了: %v", resp["msg"])
	}

	// 内置模板不许删
	h.DB.Create(&model.NotifyTemplate{
		Code: "builtin.im", Name: "内置", Kind: tplKindIM, Scene: sceneAlert,
		Body: "{{.title}}", Builtin: true, Enabled: true,
	})
	if code, _ := tplJSON(t, engine, http.MethodDelete, "/notify/templates/2", ""); code != http.StatusBadRequest {
		t.Error("内置模板应当拒删")
	}
}

// TestNotifyTemplateEnabledSticks 关掉启用开关不能被 GORM 默认值翻回来
// （Domain / HostLogTarget 之后的第三处，这次是在模板上）
func TestNotifyTemplateEnabledSticks(t *testing.T) {
	h, engine := newNotifyTplTestHandler(t)
	tplJSON(t, engine, http.MethodPost, "/notify/templates",
		`{"code":"off.im","name":"先别用","kind":"im","scene":"alert","body":"{{.title}}","enabled":false}`)
	var tpl model.NotifyTemplate
	h.DB.First(&tpl)
	if tpl.Enabled {
		t.Fatal("创建时关掉的启用开关被翻回了开")
	}
}

func TestListNotifyTemplateVars(t *testing.T) {
	_, engine := newNotifyTplTestHandler(t)
	code, resp := tplJSON(t, engine, http.MethodGet, "/notify/template-vars", "")
	if code != http.StatusOK {
		t.Fatalf("返回 %d", code)
	}
	data := resp["data"].(map[string]any)
	scenes := data["scenes"].([]any)
	if len(scenes) != 2 {
		t.Fatalf("应当有 alert 与 oncall 两个场景, got %d", len(scenes))
	}
	first := scenes[0].(map[string]any)
	vars := first["vars"].([]any)
	if len(vars) == 0 {
		t.Fatal("场景里应当带变量清单")
	}
	v := vars[0].(map[string]any)
	for _, key := range []string{"key", "label", "sample"} {
		if _, ok := v[key]; !ok {
			t.Errorf("变量定义缺少 %s: %v", key, v)
		}
	}
	// 这份清单是给前端用的，说明必须在接口里
	notes := strings.Join(toStrings(data["notes"]), " ")
	if !strings.Contains(notes, "唯一权威定义") || !strings.Contains(notes, "可选的覆盖") {
		t.Errorf("notes 要写明口径: %s", notes)
	}
}

func toStrings(raw any) []string {
	list, _ := raw.([]any)
	out := make([]string, 0, len(list))
	for _, item := range list {
		if text, ok := item.(string); ok {
			out = append(out, text)
		}
	}
	return out
}
