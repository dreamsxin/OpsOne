package handler

import (
	"bytes"
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

func newSecResponseTestHandler(t *testing.T) (*Handler, *gin.Engine) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	dir := t.TempDir()
	g, err := gorm.Open(sqlite.Open(dir+"/secresp.db"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	if err := g.AutoMigrate(
		&model.SecurityEvent{}, &model.SecurityEventLog{}, &model.SecurityEventMute{},
		&model.SecuritySuggestionDismissal{},
		&model.ExecGuardLog{}, &model.SessionCommand{}, &model.Session{},
		&model.ExposureScan{}, &model.ExposureTarget{}, &model.AuditLog{},
		&model.Signature{}, &model.CommandRule{},
		&model.Event{}, &model.EventLog{}, &model.Alert{}, &model.AlertSource{},
		&model.Message{}, &model.User{}, &model.Role{}, &model.Menu{},
		&model.SysConfig{}, &model.Certificate{}, &model.Domain{},
		&model.FirewallRule{}, &model.HostLogTarget{},
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
	engine.GET("/security/events/:id/raw", h.GetSecurityEventRaw)
	engine.POST("/security/events/:id/escalate", h.EscalateSecurityEvent)
	engine.GET("/security/overview", h.SecurityOverview)
	engine.GET("/security/suggestions", h.ListSecuritySuggestions)
	engine.POST("/security/suggestions/apply", h.ApplySecuritySuggestions)
	engine.GET("/security/suggestion-dismissals", h.ListSuggestionDismissals)
	engine.DELETE("/security/suggestion-dismissals/:id", h.DeleteSuggestionDismissal)
	return h, engine
}

func secRespJSON(t *testing.T, engine *gin.Engine, method, path, body string) (int, map[string]any) {
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

// ---------- 原始数据视图 ----------

// TestSecRawFromExecGuard 拿回下发闸门的原始行，重点是**没被摘要截断的命令原文**。
func TestSecRawFromExecGuard(t *testing.T) {
	h, engine := newSecResponseTestHandler(t)

	longCmd := strings.Repeat("rm -rf /data/very-long-path-", 200) + "END"
	raw := model.ExecGuardLog{
		Source: "manual", Status: "blocked", Action: "block",
		Reason: "命中高危命令", Command: longCmd,
		HostCount: 3, ProdCount: 2, HostNames: "web-01、web-02、db-01",
		RuleID: 7, Pattern: `rm\s+-rf`, Username: "ops", ClientIP: "10.0.0.9",
	}
	h.DB.Create(&raw)
	h.DB.Create(&model.SecurityEvent{
		Fingerprint: "fp-1", Source: "exec-guard", Title: "下发被拦",
		Severity: "warning", Evidence: truncate(longCmd, 2000),
		RefTable: "exec_guard_logs", RefID: raw.ID,
		Status: secStatusNew, HitCount: 1,
		FirstSeenAt: time.Now(), LastSeenAt: time.Now(),
	})

	code, resp := secRespJSON(t, engine, http.MethodGet, "/security/events/1/raw", "")
	if code != http.StatusOK {
		t.Fatalf("返回 %d: %v", code, resp)
	}
	data := resp["data"].(map[string]any)
	if data["available"] != true {
		t.Fatalf("应当取到原始行: %v", data["reason"])
	}
	if data["table"] != "exec_guard_logs" {
		t.Errorf("table = %v", data["table"])
	}

	fields := data["fields"].([]any)
	got := map[string]string{}
	for _, f := range fields {
		m := f.(map[string]any)
		got[m["label"].(string)] = m["value"].(string)
	}
	// 原始行必须是全文，不是摘要
	if got["命令原文"] != longCmd {
		t.Errorf("命令原文应当是未截断的全文（长度 %d，实际 %d）", len(longCmd), len(got["命令原文"]))
	}
	if !strings.HasSuffix(got["命令原文"], "END") {
		t.Error("原文末尾丢了 —— 说明被截断过")
	}
	if got["目标主机"] != "web-01、web-02、db-01" {
		t.Errorf("目标主机 = %q", got["目标主机"])
	}
	if got["主机数 / 其中生产"] != "3 / 2" {
		t.Errorf("主机数 = %q", got["主机数 / 其中生产"])
	}
}

// TestSecRawSessionWithoutParent 会话被清理掉时要如实说「查不到是谁敲的」，
// 而不是留一片空白让人以为没人操作。
func TestSecRawSessionWithoutParent(t *testing.T) {
	h, engine := newSecResponseTestHandler(t)
	cmd := model.SessionCommand{
		SessionID: 999, Command: "cat /etc/shadow", Risk: "blocked",
		RuleID: 3, RuleDesc: "读取影子文件", OffsetMs: 4200,
	}
	h.DB.Create(&cmd)
	h.DB.Create(&model.SecurityEvent{
		Fingerprint: "fp-2", Source: "terminal", Title: "终端拦截",
		Severity: "warning", RefTable: "session_commands", RefID: cmd.ID,
		Status: secStatusNew, FirstSeenAt: time.Now(), LastSeenAt: time.Now(),
	})

	_, resp := secRespJSON(t, engine, http.MethodGet, "/security/events/1/raw", "")
	data := resp["data"].(map[string]any)
	if data["available"] != true {
		t.Fatalf("命令行本身还在，应当能取到: %v", data["reason"])
	}
	body, _ := json.Marshal(data["fields"])
	if !strings.Contains(string(body), "会话记录已被清理") {
		t.Errorf("会话没了要明说，实际: %s", body)
	}
	extra := data["extra"].(map[string]any)
	if extra["sessionAlive"] != false {
		t.Error("sessionAlive 应当是 false")
	}
}

// TestSecRawDanglingReference 原始行被留存清理删掉时不是错误，但必须说清。
// 这是上一轮在 SECURITY.md 里写明的「悬空引用」，现在界面上能看见了。
func TestSecRawDanglingReference(t *testing.T) {
	h, engine := newSecResponseTestHandler(t)
	h.DB.Create(&model.SecurityEvent{
		Fingerprint: "fp-3", Source: "authz", Title: "越权被拒",
		Severity: "warning", Evidence: "POST /api/v1/hosts 403",
		RefTable: "audit_logs", RefID: 12345, // 这一行不存在
		Status: secStatusNew, FirstSeenAt: time.Now(), LastSeenAt: time.Now(),
	})

	code, resp := secRespJSON(t, engine, http.MethodGet, "/security/events/1/raw", "")
	if code != http.StatusOK {
		t.Fatalf("悬空引用不是接口错误, got %d", code)
	}
	data := resp["data"].(map[string]any)
	if data["available"] != false {
		t.Fatal("原始行不存在时 available 应当是 false")
	}
	reason := data["reason"].(string)
	if !strings.Contains(reason, "数据留存") {
		t.Errorf("要说清大概率是被留存清理删掉了: %q", reason)
	}
	// 摘要仍然要给出来 —— 那是当时抄下来的，还有用
	if data["evidence"] != "POST /api/v1/hosts 403" {
		t.Errorf("证据摘要应当照旧返回: %v", data["evidence"])
	}
}

// ---------- 升格为事件工单 ----------

func TestEscalateSecurityEvent(t *testing.T) {
	h, engine := newSecResponseTestHandler(t)
	h.DB.Create(&model.SecurityEvent{
		Fingerprint: "fp-4", Source: "exposure", Title: "未登记端口 3306 开放",
		Severity: "critical", Actor: "", ActorIP: "10.0.0.5",
		Target: "10.0.0.5", Port: "3306", Protocol: "tcp",
		Evidence: "扫描发现 3306 开放但不在基线里",
		RefTable: "exposure_scans", RefID: 1,
		Status: secStatusNew, HitCount: 6, HitsAfterClose: 0,
		FirstSeenAt: time.Now().Add(-72 * time.Hour), LastSeenAt: time.Now(),
	})

	code, resp := secRespJSON(t, engine, http.MethodPost,
		"/security/events/1/escalate", `{"assignee":"zhangsan"}`)
	if code != http.StatusOK {
		t.Fatalf("升格失败 %d: %v", code, resp)
	}
	data := resp["data"].(map[string]any)
	ticketID := uint(data["eventId"].(float64))
	if ticketID == 0 {
		t.Fatal("没拿到工单号")
	}

	var ticket model.Event
	h.DB.First(&ticket, ticketID)
	if ticket.Origin != "secevent" {
		t.Errorf("工单来源应当是 secevent, got %q", ticket.Origin)
	}
	if ticket.Severity != "critical" {
		t.Errorf("级别应当继承安全事件, got %q", ticket.Severity)
	}
	if !strings.HasPrefix(ticket.Title, "[安全]") {
		t.Errorf("标题应当带安全前缀: %q", ticket.Title)
	}
	if ticket.Assignee != "zhangsan" || ticket.AssignedAt == nil {
		t.Errorf("指派没落下来: %+v", ticket.Assignee)
	}
	// 摘要要把研判上下文带过去，免得接手的人还得回研判台翻
	for _, want := range []string{"10.0.0.5", "3306", "exposure_scans#1", "命中 6 次"} {
		if !strings.Contains(ticket.Summary, want) {
			t.Errorf("摘要缺了 %q:\n%s", want, ticket.Summary)
		}
	}
	// 没有关联告警是刻意的：线索来自流水表
	if ticket.AlertIDs != "[]" && ticket.AlertIDs != "" && ticket.AlertIDs != "null" {
		t.Errorf("安全事件升格的工单不该挂告警, got %q", ticket.AlertIDs)
	}

	// 安全事件侧：记下工单号，并从「待研判」推进到「研判中」
	var after model.SecurityEvent
	h.DB.First(&after, 1)
	if after.EventID != ticketID {
		t.Errorf("安全事件上应当记下工单号, got %d", after.EventID)
	}
	if after.Status != secStatusInvestigating {
		t.Errorf("建了单就不该还挂在待研判, got %q", after.Status)
	}
	// 处置链路要留痕
	var logs []model.SecurityEventLog
	h.DB.Where("event_id = ? AND action = ?", 1, "respond").Find(&logs)
	if len(logs) != 1 || !strings.Contains(logs[0].Content, "升格为事件工单") {
		t.Errorf("处置链路没留痕: %+v", logs)
	}

	// 重复升格要被拒：同一条线索两张单会让处置记录分叉
	code, resp = secRespJSON(t, engine, http.MethodPost, "/security/events/1/escalate", `{}`)
	if code != http.StatusBadRequest {
		t.Errorf("重复升格应当被拒, got %d: %v", code, resp)
	}
}

// TestEscalateKeepsNonNewStatus 已经在研判中/已确认的，升格不该把状态改回去。
func TestEscalateKeepsNonNewStatus(t *testing.T) {
	h, engine := newSecResponseTestHandler(t)
	h.DB.Create(&model.SecurityEvent{
		Fingerprint: "fp-5", Source: "terminal", Title: "终端拦截",
		Severity: "warning", Status: secStatusConfirmed,
		FirstSeenAt: time.Now(), LastSeenAt: time.Now(),
	})
	secRespJSON(t, engine, http.MethodPost, "/security/events/1/escalate", `{}`)

	var after model.SecurityEvent
	h.DB.First(&after, 1)
	if after.Status != secStatusConfirmed {
		t.Errorf("已确认的状态不该被升格动作改掉, got %q", after.Status)
	}
}

// ---------- 学习建议 ----------

// TestSuggestPortBaselineAndApply 反复被报的未登记端口 → 建议 → 通过后**真的**写进基线。
func TestSuggestPortBaselineAndApply(t *testing.T) {
	h, engine := newSecResponseTestHandler(t)
	target := model.ExposureTarget{
		Name: "外网入口", Address: "203.0.113.10", Ports: "22,80,443,3306",
		Baseline: "22,443", Enabled: true,
	}
	h.DB.Create(&target)
	// 复现 6 次的端口 → 该建议
	h.DB.Create(&model.SecurityEvent{
		Fingerprint: "fp-p1", Source: "exposure", Title: "未登记端口 80",
		Severity: "warning", Target: "203.0.113.10", Port: "80",
		Status: secStatusNew, HitCount: 6,
		FirstSeenAt: time.Now(), LastSeenAt: time.Now(),
	})
	// 只出现 1 次的 → 不该建议（1 次不算规律）
	h.DB.Create(&model.SecurityEvent{
		Fingerprint: "fp-p2", Source: "exposure", Title: "未登记端口 3306",
		Severity: "warning", Target: "203.0.113.10", Port: "3306",
		Status: secStatusNew, HitCount: 1,
		FirstSeenAt: time.Now(), LastSeenAt: time.Now(),
	})
	// 已经在基线里的 → 不该建议
	h.DB.Create(&model.SecurityEvent{
		Fingerprint: "fp-p3", Source: "exposure", Title: "未登记端口 443",
		Severity: "warning", Target: "203.0.113.10", Port: "443",
		Status: secStatusNew, HitCount: 9,
		FirstSeenAt: time.Now(), LastSeenAt: time.Now(),
	})

	code, resp := secRespJSON(t, engine, http.MethodGet, "/security/suggestions", "")
	if code != http.StatusOK {
		t.Fatalf("返回 %d", code)
	}
	data := resp["data"].(map[string]any)
	items := data["items"].([]any)
	if len(items) != 1 {
		body, _ := json.Marshal(items)
		t.Fatalf("应当只有 1 条建议（80 端口），got %d: %s", len(items), body)
	}
	item := items[0].(map[string]any)
	key := item["key"].(string)
	if item["kind"] != secSuggestPortBaseline {
		t.Errorf("kind = %v", item["kind"])
	}
	if item["hitCount"].(float64) != 6 {
		t.Errorf("hitCount = %v", item["hitCount"])
	}
	// 建议必须说清「通过之后平台会做什么」
	if !strings.Contains(item["action"].(string), "基线") {
		t.Errorf("action 要说清会改什么: %v", item["action"])
	}

	// 通过 → 真的写进基线
	code, resp = secRespJSON(t, engine, http.MethodPost, "/security/suggestions/apply",
		`{"decision":"approve","keys":["`+key+`"]}`)
	if code != http.StatusOK {
		t.Fatalf("应用失败 %d: %v", code, resp)
	}
	applied := resp["data"].(map[string]any)
	if len(applied["failed"].([]any)) != 0 {
		t.Fatalf("不该有失败项: %v", applied["failed"])
	}

	var after model.ExposureTarget
	h.DB.First(&after, target.ID)
	if after.Baseline != "22,80,443" {
		t.Errorf("基线应当变成 22,80,443（升序合并），got %q", after.Baseline)
	}
	// 只动基线一列：别的字段不能被顺手覆盖
	if after.Name != "外网入口" || after.Ports != "22,80,443,3306" {
		t.Errorf("只该动 baseline，其它字段被改了: %+v", after)
	}

	// 改完之后这条建议就不该再出现了（现算的好处）
	_, resp = secRespJSON(t, engine, http.MethodGet, "/security/suggestions", "")
	if n := len(resp["data"].(map[string]any)["items"].([]any)); n != 0 {
		t.Errorf("端口已进基线，建议应当消失, 还剩 %d 条", n)
	}
}

// TestSuggestDismissAndRestore 「拒绝」必须是真的：下次不再出现；撤销后又回来。
func TestSuggestDismissAndRestore(t *testing.T) {
	h, engine := newSecResponseTestHandler(t)
	h.DB.Create(&model.ExposureTarget{
		Name: "t", Address: "10.0.0.1", Ports: "8080", Baseline: "", Enabled: true,
	})
	h.DB.Create(&model.SecurityEvent{
		Fingerprint: "fp-d", Source: "exposure", Title: "未登记端口 8080",
		Severity: "warning", Target: "10.0.0.1", Port: "8080",
		Status: secStatusNew, HitCount: 5,
		FirstSeenAt: time.Now(), LastSeenAt: time.Now(),
	})

	_, resp := secRespJSON(t, engine, http.MethodGet, "/security/suggestions", "")
	items := resp["data"].(map[string]any)["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("前置条件不成立: %d 条建议", len(items))
	}
	key := items[0].(map[string]any)["key"].(string)

	secRespJSON(t, engine, http.MethodPost, "/security/suggestions/apply",
		`{"decision":"dismiss","keys":["`+key+`"],"reason":"这个端口马上要关"}`)

	_, resp = secRespJSON(t, engine, http.MethodGet, "/security/suggestions", "")
	data := resp["data"].(map[string]any)
	if n := len(data["items"].([]any)); n != 0 {
		t.Errorf("拒绝之后不该再出现, 还剩 %d 条", n)
	}
	if data["dismissed"].(float64) != 1 {
		t.Errorf("应当报出被拒绝了 1 条, got %v", data["dismissed"])
	}
	// 基线没被动过 —— 拒绝不是「通过」
	var target model.ExposureTarget
	h.DB.First(&target, 1)
	if target.Baseline != "" {
		t.Errorf("拒绝不该改配置, baseline = %q", target.Baseline)
	}

	// 撤销拒绝 → 建议回来
	_, resp = secRespJSON(t, engine, http.MethodGet, "/security/suggestion-dismissals", "")
	rows := resp["data"].(map[string]any)["rows"].([]any)
	if len(rows) != 1 {
		t.Fatalf("拒绝记录 = %d 条", len(rows))
	}
	secRespJSON(t, engine, http.MethodDelete, "/security/suggestion-dismissals/1", "")

	_, resp = secRespJSON(t, engine, http.MethodGet, "/security/suggestions", "")
	if n := len(resp["data"].(map[string]any)["items"].([]any)); n != 1 {
		t.Errorf("撤销拒绝后建议应当回来, got %d 条", n)
	}
}

// TestSuggestMuteFingerprint 判了忽略却还在累加命中的指纹 → 建议加白名单 → 通过即生效。
func TestSuggestMuteFingerprint(t *testing.T) {
	h, engine := newSecResponseTestHandler(t)
	h.DB.Create(&model.SecurityEvent{
		Fingerprint: "fp-ignored", Source: "authz", Title: "越权被拒（已知的探测脚本）",
		Severity: "warning", Status: secStatusIgnored, Verdict: "已知扫描器，不用管",
		HitCount: 40, HitsAfterClose: 12,
		FirstSeenAt: time.Now(), LastSeenAt: time.Now(),
	})
	// 判了忽略但没再命中的 → 不该建议
	h.DB.Create(&model.SecurityEvent{
		Fingerprint: "fp-quiet", Source: "authz", Title: "另一条",
		Severity: "warning", Status: secStatusIgnored, Verdict: "x",
		HitCount: 3, HitsAfterClose: 0,
		FirstSeenAt: time.Now(), LastSeenAt: time.Now(),
	})

	_, resp := secRespJSON(t, engine, http.MethodGet, "/security/suggestions", "")
	items := resp["data"].(map[string]any)["items"].([]any)
	if len(items) != 1 {
		body, _ := json.Marshal(items)
		t.Fatalf("应当只建议那条还在响的, got %d: %s", len(items), body)
	}
	item := items[0].(map[string]any)
	if item["kind"] != secSuggestMute {
		t.Fatalf("kind = %v", item["kind"])
	}
	key := item["key"].(string)

	secRespJSON(t, engine, http.MethodPost, "/security/suggestions/apply",
		`{"decision":"approve","keys":["`+key+`"]}`)

	var mute model.SecurityEventMute
	if err := h.DB.Where("fingerprint = ?", "fp-ignored").First(&mute).Error; err != nil {
		t.Fatalf("应当真的加进白名单: %v", err)
	}
	if !strings.Contains(mute.Reason, "学习建议") {
		t.Errorf("白名单理由要能追溯来源: %q", mute.Reason)
	}
	// 处置链路留痕
	var logs int64
	h.DB.Model(&model.SecurityEventLog{}).Where("action = ?", "respond").Count(&logs)
	if logs == 0 {
		t.Error("加白名单要在处置链路上留痕")
	}
}

// TestSuggestSignatureEnforce observe 态但已经 warn 很多次的特征 → 建议提到 enforce；
// 通过之后**特征阶段与命令规则都要真的改**，否则就是「页面说拦、实际放过」。
func TestSuggestSignatureEnforce(t *testing.T) {
	h, engine := newSecResponseTestHandler(t)
	rule := model.CommandRule{Pattern: `curl\s+.*\|\s*sh`, Action: "warn",
		Description: "[特征库] 管道执行远端脚本", Enabled: true}
	h.DB.Create(&rule)
	sig := model.Signature{
		Name: "管道执行远端脚本", Kind: "command", Pattern: `curl\s+.*\|\s*sh`,
		Stage: "observe", Severity: "high", Enabled: true, RuleID: rule.ID,
	}
	h.DB.Create(&sig)
	for i := 0; i < 5; i++ {
		h.DB.Create(&model.ExecGuardLog{
			Source: "manual", Status: "warn", Action: "warn",
			RuleID: rule.ID, Pattern: sig.Pattern, Command: "curl x | sh",
		})
	}

	_, resp := secRespJSON(t, engine, http.MethodGet, "/security/suggestions", "")
	items := resp["data"].(map[string]any)["items"].([]any)
	if len(items) != 1 {
		body, _ := json.Marshal(items)
		t.Fatalf("应当有 1 条提到拦截的建议, got %d: %s", len(items), body)
	}
	item := items[0].(map[string]any)
	if item["kind"] != secSuggestEnforce || item["hitCount"].(float64) != 5 {
		t.Fatalf("建议内容不对: %v", item)
	}
	key := item["key"].(string)

	_, resp = secRespJSON(t, engine, http.MethodPost, "/security/suggestions/apply",
		`{"decision":"approve","keys":["`+key+`"]}`)
	applied := resp["data"].(map[string]any)
	if n := len(applied["failed"].([]any)); n != 0 {
		t.Fatalf("不该有失败项: %v", applied["failed"])
	}

	var afterSig model.Signature
	h.DB.First(&afterSig, sig.ID)
	if afterSig.Stage != "enforce" {
		t.Errorf("特征阶段应当变成 enforce, got %q", afterSig.Stage)
	}
	// 关键：命令规则要跟着变成 block，否则页面说拦、实际还在放过
	var afterRule model.CommandRule
	h.DB.First(&afterRule, rule.ID)
	if afterRule.Action != "block" {
		t.Errorf("命令规则应当被重写为 block, got %q —— 否则就是假的「已生效」", afterRule.Action)
	}
}

// TestApplyStaleSuggestionRejected 页面上那份建议可能已经过期，
// 应用时必须重算、不信前端传来的内容。
func TestApplyStaleSuggestionRejected(t *testing.T) {
	_, engine := newSecResponseTestHandler(t)
	code, resp := secRespJSON(t, engine, http.MethodPost, "/security/suggestions/apply",
		`{"decision":"approve","keys":["port_baseline|999|8080"]}`)
	if code != http.StatusOK {
		t.Fatalf("返回 %d", code)
	}
	data := resp["data"].(map[string]any)
	failed := data["failed"].([]any)
	if len(failed) != 1 {
		t.Fatalf("不存在的建议应当进 failed, got %v", data)
	}
	msg := failed[0].(map[string]any)["error"].(string)
	if !strings.Contains(msg, "已经不成立") {
		t.Errorf("要说清是建议过期了: %q", msg)
	}
}

func TestApplySuggestionValidatesDecision(t *testing.T) {
	_, engine := newSecResponseTestHandler(t)
	for _, body := range []string{
		`{"decision":"maybe","keys":["x"]}`,
		`{"decision":"approve","keys":[]}`,
	} {
		if code, _ := secRespJSON(t, engine, http.MethodPost,
			"/security/suggestions/apply", body); code != http.StatusBadRequest {
			t.Errorf("%s 应当被拒, got %d", body, code)
		}
	}
}

// ---------- 安全概览 ----------

// TestSecurityOverviewSeparatesGaps 概览最重要的一条：
// 「没有采集」和「采集到 0」必须分开表达，不能用 0 冒充「没有问题」。
func TestSecurityOverviewSeparatesGaps(t *testing.T) {
	h, engine := newSecResponseTestHandler(t)
	now := time.Now()
	h.DB.Create(&model.SecurityEvent{
		Fingerprint: "o1", Source: "exec-guard", Title: "a", Severity: "critical",
		Status: secStatusNew, HitCount: 1, HitsAfterClose: 2,
		FirstSeenAt: now, LastSeenAt: now,
	})
	h.DB.Create(&model.SecurityEvent{
		Fingerprint: "o2", Source: "authz", Title: "b", Severity: "warning",
		Status: secStatusHandled, HitCount: 1, EventID: 7,
		FirstSeenAt: now, LastSeenAt: now,
	})
	h.DB.Create(&model.ExecGuardLog{Status: "blocked", Action: "block", Command: "x"})
	h.DB.Create(&model.ExposureTarget{Name: "t", Address: "1.1.1.1", Ports: "22",
		Baseline: "", Enabled: true, LastStatus: "unexpected"})
	h.DB.Create(&model.Certificate{Name: "c", Domain: "a.com", Enabled: true, Status: "expiring"})
	h.DB.Create(&model.User{Username: "u1", PasswordHash: "x", Status: 1, TOTPEnabled: true})

	code, resp := secRespJSON(t, engine, http.MethodGet, "/security/overview", "")
	if code != http.StatusOK {
		t.Fatalf("返回 %d: %v", code, resp)
	}
	data := resp["data"].(map[string]any)

	events := data["events"].(map[string]any)
	if events["open"].(float64) != 1 {
		t.Errorf("待办事件 = %v, want 1", events["open"])
	}
	if events["critical"].(float64) != 1 {
		t.Errorf("严重待办 = %v, want 1", events["critical"])
	}
	if events["rehit"].(float64) != 1 {
		t.Errorf("结案后又命中 = %v, want 1", events["rehit"])
	}
	if events["escalated"].(float64) != 1 {
		t.Errorf("已升格 = %v, want 1", events["escalated"])
	}

	exposure := data["exposure"].(map[string]any)
	if exposure["noBaseline"].(float64) != 1 {
		t.Errorf("基线留空的目标要单独数出来, got %v", exposure["noBaseline"])
	}
	if data["certs"].(map[string]any)["expiring"].(float64) != 1 {
		t.Errorf("证书将到期 = %v", data["certs"].(map[string]any)["expiring"])
	}
	tf := data["twoFactor"].(map[string]any)
	if tf["users"].(float64) != 1 || tf["enabled"].(float64) != 1 {
		t.Errorf("双因子统计 = %v", tf)
	}

	// 这是这一页的核心承诺
	gaps := data["gaps"].([]any)
	if len(gaps) < 4 {
		t.Fatalf("gaps 至少要列出那几项做不出真数据的, got %d", len(gaps))
	}
	body, _ := json.Marshal(gaps)
	for _, want := range []string{"阻断包数", "agent", "失败登录", "威胁情报"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("gaps 里应当提到 %q: %s", want, body)
		}
	}
}

func TestSecEventTrendGroupsByDay(t *testing.T) {
	h, _ := newSecResponseTestHandler(t)
	now := time.Now()
	for i := 0; i < 3; i++ {
		h.DB.Create(&model.SecurityEvent{
			Fingerprint: "t" + string(rune('a'+i)), Source: "authz", Title: "x",
			Severity: "info", Status: secStatusNew,
			FirstSeenAt: now, LastSeenAt: now,
		})
	}
	trend := h.secEventTrend(now.Add(-7 * 24 * time.Hour))
	if len(trend) != 1 {
		t.Fatalf("同一天的应当归成一组, got %d 组: %v", len(trend), trend)
	}
	if trend[0]["total"].(int64) != 3 {
		t.Errorf("当天总数 = %v, want 3", trend[0]["total"])
	}
}
