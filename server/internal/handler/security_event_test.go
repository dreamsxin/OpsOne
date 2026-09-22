package handler

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"ops-platform/server/internal/config"
	"ops-platform/server/internal/model"
)

func newSecEventTestHandler(t *testing.T) (*Handler, *gin.Engine) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	dir := t.TempDir()
	g, err := gorm.Open(sqlite.Open(dir+"/secevent.db"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	if err := g.AutoMigrate(
		&model.SecurityEvent{}, &model.SecurityEventLog{}, &model.SecurityEventMute{},
		&model.ExecGuardLog{}, &model.SessionCommand{}, &model.Session{},
		&model.ExposureScan{}, &model.ExposureTarget{}, &model.AuditLog{},
		&model.Signature{}, &model.SysConfig{}, &model.User{},
		&model.FirewallRule{}, &model.Host{},
	); err != nil {
		t.Fatalf("建表失败: %v", err)
	}
	// Windows 上不关连接，t.TempDir 的清理会失败并把测试判成 FAIL
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
	engine.POST("/security/events/triage", h.TriageSecurityEvents)
	engine.DELETE("/security/event-mutes/:id", h.DeleteSecurityMute)
	return h, engine
}

func secPostJSON(t *testing.T, engine *gin.Engine, method, path, body string) (int, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	engine.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func TestSecFingerprintIgnoresTimeAndRefID(t *testing.T) {
	base := secCandidate{
		Source: secSourceExecGuard, Actor: "ops01", ActorIP: "10.0.0.5",
		Target: "web-01", FingerKey: `rm\s+-rf\s+/`,
	}
	// 时间和原始流水 ID 不参与指纹：否则每一行流水都是新事件，去重就没了
	a := base
	a.SeenAt = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	a.RefID = 1
	b := base
	b.SeenAt = time.Date(2026, 9, 9, 9, 9, 9, 0, time.UTC)
	b.RefID = 999
	if secFingerprint(a) != secFingerprint(b) {
		t.Fatal("同一个「谁对谁做了什么」必须是同一个指纹")
	}

	// 换任何一个「谁 / 从哪 / 对谁 / 干了什么」都必须换指纹
	for _, mut := range []func(c *secCandidate){
		func(c *secCandidate) { c.Actor = "ops02" },
		func(c *secCandidate) { c.ActorIP = "10.0.0.6" },
		func(c *secCandidate) { c.Target = "web-02" },
		func(c *secCandidate) { c.Port = "22" },
		func(c *secCandidate) { c.FingerKey = "mkfs" },
		func(c *secCandidate) { c.Source = secSourceTerminal },
	} {
		other := base
		mut(&other)
		if secFingerprint(other) == secFingerprint(base) {
			t.Fatalf("改了关键字段指纹却没变: %+v", other)
		}
	}
}

func TestCollectExecGuardSkipsWorkflowBlocks(t *testing.T) {
	h, _ := newSecEventTestHandler(t)
	rows := []model.ExecGuardLog{
		// 真的命中拦截规则：要进研判台
		{Source: "manual", Status: "blocked", Action: "block", Pattern: `rm\s+-rf\s+/`,
			Reason: "命令命中拦截级命令规则，已阻止下发", Command: "rm -rf /",
			Username: "ops01", ClientIP: "10.0.0.5", HostNames: "web-01", HostCount: 1},
		// 生产主机没二次确认：这是工作流保护，不是安全事件
		{Source: "manual", Status: "blocked", Action: "", Pattern: "",
			Reason:  "目标里有 2 台生产主机（web-01、web-02），需要显式确认后才能下发",
			Command: "systemctl restart nginx", Username: "ops01", ClientIP: "10.0.0.5"},
		// 只是提醒级：放过去了，不算事件
		{Source: "manual", Status: "warn", Action: "warn", Pattern: `systemctl\s+restart`,
			Reason: "命中提醒级规则", Command: "systemctl restart nginx", Username: "ops01"},
	}
	for i := range rows {
		if err := h.DB.Create(&rows[i]).Error; err != nil {
			t.Fatalf("写入闸门流水失败: %v", err)
		}
	}

	cands, cursor := h.collectExecGuard()
	if len(cands) != 1 {
		got := make([]string, 0, len(cands))
		for _, c := range cands {
			got = append(got, c.Title)
		}
		t.Fatalf("只有命中拦截规则的那条该进研判台，实际 %d 条：%v", len(cands), got)
	}
	if !strings.Contains(cands[0].Evidence, "rm -rf /") {
		t.Fatalf("证据里必须有命令原文，实际：%s", cands[0].Evidence)
	}
	if cands[0].ActorIP != "10.0.0.5" || cands[0].Actor != "ops01" {
		t.Fatalf("发起方与源地址没带上：%+v", cands[0])
	}
	// 游标推进到这一批里实际读到的最大 ID。没被 WHERE 命中的行不可能推进游标，
	// 否则下一次跑时那些行会被跳过 —— 但它们根本不是安全事件，跳过是对的。
	if cursor != 1 {
		t.Fatalf("游标应推进到 1（唯一被读到的行），实际 %d", cursor)
	}
}

func TestUpsertDedupsAndCountsHits(t *testing.T) {
	h, _ := newSecEventTestHandler(t)
	cand := secCandidate{
		Source: secSourceTerminal, Title: "终端高危命令被拦：关机", Severity: "critical",
		Actor: "ops01", ActorIP: "10.0.0.5", Target: "web-01", Protocol: "ssh",
		Evidence: "shutdown -h now", RefTable: "session_commands", RefID: 1,
		SeenAt: time.Now().Add(-time.Hour), FingerKey: "关机或重启主机",
	}
	if got := h.upsertSecCandidate(cand); got != "created" {
		t.Fatalf("第一次应该新建，实际 %s", got)
	}
	again := cand
	again.RefID = 2
	again.SeenAt = time.Now()
	if got := h.upsertSecCandidate(again); got != "updated" {
		t.Fatalf("第二次应该累加，实际 %s", got)
	}

	var event model.SecurityEvent
	if err := h.DB.First(&event).Error; err != nil {
		t.Fatalf("取事件失败: %v", err)
	}
	var count int64
	h.DB.Model(&model.SecurityEvent{}).Count(&count)
	if count != 1 {
		t.Fatalf("同一指纹只该有一条事件，实际 %d 条", count)
	}
	if event.HitCount != 2 {
		t.Fatalf("命中次数应为 2，实际 %d", event.HitCount)
	}
	if event.RefID != 2 {
		t.Fatalf("溯源应指向最新一条流水，实际 %d", event.RefID)
	}
	if !event.LastSeenAt.After(event.FirstSeenAt) {
		t.Fatal("最近一次命中应该晚于首次")
	}
}

func TestClosedEventCountsRehitWithoutReopening(t *testing.T) {
	h, _ := newSecEventTestHandler(t)
	cand := secCandidate{
		Source: secSourceAuthz, Title: "越权访问被拒", Severity: "warning",
		Actor: "ops01", ActorIP: "10.0.0.9", Target: "/api/v1/hosts/:id",
		RefTable: "audit_logs", RefID: 1, SeenAt: time.Now(), FingerKey: "DELETE /api/v1/hosts/:id",
	}
	h.upsertSecCandidate(cand)

	var event model.SecurityEvent
	h.DB.First(&event)
	// 人判成「忽略」结案
	h.DB.Model(&event).Updates(map[string]any{"status": secStatusIgnored, "verdict": "内部演练"})

	next := cand
	next.RefID = 2
	if got := h.upsertSecCandidate(next); got != "rehit" {
		t.Fatalf("结案后再命中应算 rehit，实际 %s", got)
	}

	h.DB.First(&event, event.ID)
	if event.Status != secStatusIgnored {
		t.Fatalf("结案状态是人的判断，采集器不能改回去，实际 %s", event.Status)
	}
	if event.HitsAfterClose != 1 {
		t.Fatalf("结案后命中次数应为 1，实际 %d", event.HitsAfterClose)
	}
	var logs []model.SecurityEventLog
	h.DB.Where("event_id = ? AND action = ?", event.ID, "rehit").Find(&logs)
	if len(logs) != 1 {
		t.Fatal("结案后又命中必须在处置链路上留痕，否则没人会知道")
	}
}

func TestMuteSkipsCollectionButKeepsCounting(t *testing.T) {
	h, _ := newSecEventTestHandler(t)
	cand := secCandidate{
		Source: secSourceExecGuard, Title: "高危命令下发被拦", Severity: "critical",
		Actor: "ci-bot", ActorIP: "10.0.0.7", Target: "build-01",
		RefTable: "exec_guard_logs", RefID: 1, SeenAt: time.Now(), FingerKey: "dd if=",
	}
	if err := h.DB.Create(&model.SecurityEventMute{
		Fingerprint: secFingerprint(cand), Source: cand.Source,
		Title: cand.Title, Reason: "CI 的正常清盘动作", Operator: "admin",
	}).Error; err != nil {
		t.Fatalf("写白名单失败: %v", err)
	}

	if got := h.upsertSecCandidate(cand); got != "muted" {
		t.Fatalf("命中白名单应被跳过，实际 %s", got)
	}
	var count int64
	h.DB.Model(&model.SecurityEvent{}).Count(&count)
	if count != 0 {
		t.Fatal("命中白名单不该建事件")
	}
	// 跳过不等于装作没发生：挡掉的次数必须记下来，挡得异常多说明当初判错了
	var mute model.SecurityEventMute
	h.DB.First(&mute)
	if mute.HitCount != 1 || mute.LastHitAt == nil {
		t.Fatalf("白名单要记挡掉了多少次，实际 %d / %v", mute.HitCount, mute.LastHitAt)
	}
}

func TestCollectCursorDoesNotReprocess(t *testing.T) {
	h, _ := newSecEventTestHandler(t)
	if err := h.DB.Create(&model.ExecGuardLog{
		Source: "manual", Status: "blocked", Action: "block", Pattern: "mkfs",
		Reason: "命中拦截规则", Command: "mkfs.ext4 /dev/sdb", Username: "ops01", ClientIP: "10.0.0.5",
	}).Error; err != nil {
		t.Fatalf("写入流水失败: %v", err)
	}

	first := h.CollectSecurityEvents()
	if first.Created != 1 {
		t.Fatalf("第一次应新建 1 条，实际 %+v", first)
	}
	second := h.CollectSecurityEvents()
	if second.Created != 0 || second.Updated != 0 {
		t.Fatalf("同一行流水不该被消费两次，实际 %+v", second)
	}
	if h.secCursor(secSourceExecGuard) != 1 {
		t.Fatalf("游标应停在 1，实际 %d", h.secCursor(secSourceExecGuard))
	}
}

func TestExposureUsesPortSignatureSeverity(t *testing.T) {
	h, _ := newSecEventTestHandler(t)
	// port 类特征在此之前没有任何持久化消费方，这里让它决定事件级别
	if err := h.DB.Create(&model.Signature{
		Name: "管理端口对外开放", Kind: sigKindPort, Pattern: "22,3389",
		Stage: "observe", Severity: "high", Enabled: true,
	}).Error; err != nil {
		t.Fatalf("写特征失败: %v", err)
	}
	target := model.ExposureTarget{Name: "边界机", Address: "203.0.113.7", Ports: "22,8080", Baseline: "8080"}
	if err := h.DB.Create(&target).Error; err != nil {
		t.Fatalf("写目标失败: %v", err)
	}
	if err := h.DB.Create(&model.ExposureScan{
		TargetID: target.ID, Status: "unexpected", Scanned: 2,
		OpenPorts: "22,8080", Unexpected: "22,9999", Operator: "scheduler",
	}).Error; err != nil {
		t.Fatalf("写扫描记录失败: %v", err)
	}

	cands, _ := h.collectExposure()
	if len(cands) != 2 {
		t.Fatalf("两个未登记端口应各出一条事件，实际 %d", len(cands))
	}
	bySeverity := map[string]string{}
	for _, c := range cands {
		bySeverity[c.Port] = c.Severity
	}
	if bySeverity["22"] != "critical" {
		t.Fatalf("命中 high 级特征的端口应升成 critical，实际 %s", bySeverity["22"])
	}
	if bySeverity["9999"] != "warning" {
		t.Fatalf("没命中特征的端口保持 warning，实际 %s", bySeverity["9999"])
	}
	for _, c := range cands {
		if c.Port == "22" && !strings.Contains(c.Evidence, "命中特征库") {
			t.Fatalf("命中特征要写进证据，实际：%s", c.Evidence)
		}
	}
}

func TestTriageRequiresVerdictBeforeClosing(t *testing.T) {
	h, engine := newSecEventTestHandler(t)
	event := model.SecurityEvent{
		Fingerprint: "fp-1", Source: secSourceAuthz, Title: "越权访问被拒",
		Severity: "warning", Status: secStatusNew,
		FirstSeenAt: time.Now(), LastSeenAt: time.Now(),
	}
	if err := h.DB.Create(&event).Error; err != nil {
		t.Fatalf("写事件失败: %v", err)
	}

	// 不写结论就想结案：必须被拒。让线索消失是要负责的动作。
	for _, status := range []string{secStatusFalsePositive, secStatusIgnored, secStatusHandled} {
		code, out := secPostJSON(t, engine, "POST", "/security/events/triage",
			`{"ids":[`+secIDText(event.ID)+`],"status":"`+status+`"}`)
		if code == 200 {
			t.Fatalf("状态 %s 没写结论却通过了", status)
		}
		if msg, _ := out["msg"].(string); !strings.Contains(msg, "必须写清结论") {
			t.Fatalf("报错要说清为什么，实际：%s", msg)
		}
	}
	// 研判中不算结案，可以不写结论
	code, _ := secPostJSON(t, engine, "POST", "/security/events/triage",
		`{"ids":[`+secIDText(event.ID)+`],"status":"investigating"}`)
	if code != 200 {
		t.Fatalf("转研判中不该要求结论，实际 %d", code)
	}
}

func TestTriageFalsePositiveAddsMuteAndRevokeReopens(t *testing.T) {
	h, engine := newSecEventTestHandler(t)
	event := model.SecurityEvent{
		Fingerprint: "fp-2", Source: secSourceExecGuard, Title: "高危命令下发被拦",
		Severity: "critical", Status: secStatusNew, HitCount: 3, HitsAfterClose: 0,
		FirstSeenAt: time.Now(), LastSeenAt: time.Now(),
	}
	if err := h.DB.Create(&event).Error; err != nil {
		t.Fatalf("写事件失败: %v", err)
	}

	code, out := secPostJSON(t, engine, "POST", "/security/events/triage",
		`{"ids":[`+secIDText(event.ID)+`],"status":"false-positive","verdict":"CI 的正常动作"}`)
	if code != 200 {
		t.Fatalf("判误报失败: %d %v", code, out)
	}
	var mute model.SecurityEventMute
	if err := h.DB.Where("fingerprint = ?", event.Fingerprint).First(&mute).Error; err != nil {
		t.Fatal("判成误报应该同时进白名单，否则同一条噪音明天又会冒出来")
	}
	// 白名单挡了几次，撤销时要说得出来
	h.DB.Model(&mute).Update("hit_count", 12)

	code, out = secPostJSON(t, engine, "DELETE", "/security/event-mutes/"+secIDText(mute.ID), "")
	if code != 200 {
		t.Fatalf("撤销白名单失败: %d %v", code, out)
	}
	var after model.SecurityEvent
	h.DB.First(&after, event.ID)
	if after.Status != secStatusNew {
		t.Fatalf("撤销白名单说明当初判错了，事件要回到待研判，实际 %s", after.Status)
	}
	if after.ClosedAt != nil {
		t.Fatal("回到待研判时结案信息要清掉")
	}
	var logs []model.SecurityEventLog
	h.DB.Where("event_id = ?", event.ID).Find(&logs)
	found := false
	for _, l := range logs {
		if strings.Contains(l.Content, "挡掉 12 次") {
			found = true
		}
	}
	if !found {
		t.Fatalf("撤销留痕要带上挡掉的次数，实际：%+v", logs)
	}
}

func TestTriageRejectsUnknownStatusAndEmptyIDs(t *testing.T) {
	_, engine := newSecEventTestHandler(t)
	if code, _ := secPostJSON(t, engine, "POST", "/security/events/triage",
		`{"ids":[],"status":"ignored","verdict":"x"}`); code == 200 {
		t.Fatal("没选中任何事件应被拒")
	}
	if code, _ := secPostJSON(t, engine, "POST", "/security/events/triage",
		`{"ids":[1],"status":"closed","verdict":"x"}`); code == 200 {
		t.Fatal("未知状态应被拒")
	}
}

func secIDText(v uint) string {
	return strconv.FormatUint(uint64(v), 10)
}
