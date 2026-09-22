package handler

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"ops-platform/server/internal/config"
	"ops-platform/server/internal/cryptox"
	"ops-platform/server/internal/model"
)

// ---------- 一个够用的假 SMTP 服务器 ----------
//
// 只实现发一封信需要的那几条命令。有它才能验证「真的发出去了」以及
// 报文里到底写了什么（RFC 2047 编码、发件人显示名），而不是只验证不报错。

type fakeSMTP struct {
	ln                net.Listener
	mu                sync.Mutex
	messages          []string
	advertiseStartTLS bool
}

func startFakeSMTP(t *testing.T, advertiseStartTLS bool) *fakeSMTP {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听失败: %v", err)
	}
	srv := &fakeSMTP{ln: ln, advertiseStartTLS: advertiseStartTLS}
	go srv.serve()
	t.Cleanup(func() { _ = ln.Close() })
	return srv
}

func (f *fakeSMTP) hostPort() (string, int) {
	addr := f.ln.Addr().(*net.TCPAddr)
	return "127.0.0.1", addr.Port
}

func (f *fakeSMTP) received() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.messages...)
}

func (f *fakeSMTP) serve() {
	for {
		conn, err := f.ln.Accept()
		if err != nil {
			return
		}
		go f.handle(conn)
	}
}

func (f *fakeSMTP) handle(conn net.Conn) {
	defer conn.Close()
	reader := bufio.NewReader(conn)
	writer := bufio.NewWriter(conn)
	say := func(line string) {
		_, _ = writer.WriteString(line + "\r\n")
		_ = writer.Flush()
	}

	say("220 fake ESMTP ready")
	for {
		raw, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		cmd := strings.ToUpper(strings.TrimSpace(raw))
		switch {
		case strings.HasPrefix(cmd, "EHLO"):
			_, _ = writer.WriteString("250-fake greets you\r\n")
			if f.advertiseStartTLS {
				_, _ = writer.WriteString("250-STARTTLS\r\n")
			}
			_, _ = writer.WriteString("250 SIZE 10485760\r\n")
			_ = writer.Flush()
		case strings.HasPrefix(cmd, "HELO"):
			say("250 fake")
		case strings.HasPrefix(cmd, "MAIL FROM"), strings.HasPrefix(cmd, "RCPT TO"),
			strings.HasPrefix(cmd, "RSET"), strings.HasPrefix(cmd, "NOOP"):
			say("250 ok")
		case cmd == "DATA":
			say("354 end with <CRLF>.<CRLF>")
			var sb strings.Builder
			for {
				line, err := reader.ReadString('\n')
				if err != nil {
					return
				}
				if line == ".\r\n" || line == ".\n" {
					break
				}
				sb.WriteString(line)
			}
			f.mu.Lock()
			f.messages = append(f.messages, sb.String())
			f.mu.Unlock()
			say("250 queued")
		case cmd == "STARTTLS":
			// 声明支持却握手失败：用来验证「失败就是失败，不降级」
			say("454 TLS temporarily unavailable")
		case cmd == "QUIT":
			say("221 bye")
			return
		default:
			say("500 unrecognized")
		}
	}
}

// ---------- 测试脚手架 ----------

func newMailTestHandler(t *testing.T, secretKey string) (*Handler, *gin.Engine) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	dir := t.TempDir()
	g, err := gorm.Open(sqlite.Open(dir+"/mail.db"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	if err := g.AutoMigrate(
		&model.MailAccount{}, &model.NotifyChannel{}, &model.EmailTemplate{},
		&model.SysConfig{}, &model.User{},
	); err != nil {
		t.Fatalf("建表失败: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, err := g.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})

	h := New(g, &config.Config{SecretKey: secretKey})
	engine := gin.New()
	engine.Use(func(c *gin.Context) {
		c.Set("ctx_user", &model.User{ID: 1, Username: "admin"})
		c.Next()
	})
	engine.GET("/notify/mail-state", h.GetMailState)
	engine.GET("/notify/mail-accounts", h.ListMailAccounts)
	engine.POST("/notify/mail-accounts", h.CreateMailAccount)
	engine.PUT("/notify/mail-accounts/:id", h.UpdateMailAccount)
	engine.DELETE("/notify/mail-accounts/:id", h.DeleteMailAccount)
	engine.POST("/notify/mail-accounts/:id/default", h.SetDefaultMailAccount)
	engine.POST("/notify/mail-accounts/:id/test", h.TestMailAccount)
	engine.POST("/notify/mail-accounts/import-global", h.ImportGlobalSMTP)
	return h, engine
}

func mailJSON(t *testing.T, engine *gin.Engine, method, path, body string) (int, map[string]any, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	engine.ServeHTTP(rec, req)
	var parsed map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &parsed); err != nil {
		t.Fatalf("响应不是 JSON: %s", rec.Body.String())
	}
	return rec.Code, parsed, rec.Body.String()
}

// mailAccountBody 指向假 SMTP 的一条创建请求
func mailAccountBody(name, host string, port int, extra string) string {
	return `{"name":"` + name + `","host":"` + host + `","port":` + strconv.Itoa(port) +
		`,"from":"alert@corp.local","fromName":"OpsOne 告警","tlsMode":"plain"` + extra + `}`
}

// 真发一封信到假 SMTP，验证报文里的编码与发件人显示名
func TestDeliverMailToFakeSMTP(t *testing.T) {
	srv := startFakeSMTP(t, false)
	host, port := srv.hostPort()

	sender := mailSender{
		Source: "account", Name: "测试邮箱", Host: host, Port: port,
		From: "alert@corp.local", FromName: "OpsOne 告警", TLSMode: tlsModePlain,
	}
	if err := deliverMail(sender, []string{"ops@corp.local", "sre@corp.local"},
		"【严重】磁盘使用率 92%", "正文内容"); err != nil {
		t.Fatalf("发信失败: %v", err)
	}

	msgs := srv.received()
	if len(msgs) != 1 {
		t.Fatalf("应该收到 1 封，实际 %d 封", len(msgs))
	}
	msg := msgs[0]

	// 中文主题必须被 RFC 2047 编码：不编码时部分客户端显示乱码
	if strings.Contains(msg, "Subject: 【严重】") {
		t.Fatal("中文主题没有编码，直接塞进了 Subject 头")
	}
	if !strings.Contains(msg, "Subject: =?UTF-8?") {
		t.Fatalf("主题不是 RFC 2047 编码: %s", msg)
	}
	// 发件人显示名同理，且地址要在尖括号里
	if !strings.Contains(msg, "<alert@corp.local>") {
		t.Fatalf("发件人显示名格式不对: %s", msg)
	}
	if !strings.Contains(msg, "To: ops@corp.local, sre@corp.local") {
		t.Fatalf("收件人不对: %s", msg)
	}
	if !strings.Contains(msg, "charset=UTF-8") || !strings.Contains(msg, "正文内容") {
		t.Fatalf("正文或字符集不对: %s", msg)
	}
}

// 选了 STARTTLS 而服务器没声明支持时必须报错，绝不降级成明文。
// 这正是老实现的问题：它交给标准库，对方不 advertise 就静默发明文。
func TestStartTLSNeverDowngrades(t *testing.T) {
	srv := startFakeSMTP(t, false)
	host, port := srv.hostPort()

	sender := mailSender{
		Host: host, Port: port, From: "a@corp.local", TLSMode: tlsModeSTARTTLS,
	}
	err := deliverMail(sender, []string{"b@corp.local"}, "主题", "正文")
	if err == nil {
		t.Fatal("服务器不支持 STARTTLS 却发送成功了 —— 说明降级成了明文")
	}
	if !strings.Contains(err.Error(), "STARTTLS") {
		t.Fatalf("错误信息应说明是 STARTTLS 的问题: %v", err)
	}
	if len(srv.received()) != 0 {
		t.Fatal("拒绝之后仍然把信发了出去")
	}

	// 声明支持但握手失败时，同样是失败而不是回落
	srv2 := startFakeSMTP(t, true)
	host2, port2 := srv2.hostPort()
	err = deliverMail(mailSender{Host: host2, Port: port2, From: "a@corp.local",
		TLSMode: tlsModeSTARTTLS}, []string{"b@corp.local"}, "主题", "正文")
	if err == nil {
		t.Fatal("STARTTLS 握手失败却发送成功了")
	}
	if len(srv2.received()) != 0 {
		t.Fatal("握手失败之后仍然把信发了出去")
	}
}

// 发件账号的解析顺序：渠道指定 → 默认邮箱 → 全局 SMTP
func TestMailSenderResolutionOrder(t *testing.T) {
	h, engine := newMailTestHandler(t, "")

	// 一个邮箱都没有、全局也没配：照实报「没有可用的发件邮箱」
	if _, err := h.resolveMailSender(model.NotifyChannel{}); err == nil {
		t.Fatal("什么都没配却返回了可用的发件配置")
	}

	// 只配全局 SMTP → 回落到它（既有部署必须照常发信）
	h.DB.Create(&model.SysConfig{Group: "smtp", Key: CfgSMTPHost, Value: "global.smtp", Type: "string"})
	h.DB.Create(&model.SysConfig{Group: "smtp", Key: CfgSMTPUser, Value: "global@corp", Type: "string"})
	sender, err := h.resolveMailSender(model.NotifyChannel{})
	if err != nil {
		t.Fatalf("应该回落到全局 SMTP: %v", err)
	}
	if sender.Source != "global" || sender.Host != "global.smtp" || sender.From != "global@corp" {
		t.Fatalf("回落结果不对: %+v", sender)
	}

	// 登记一个默认邮箱 → 优先它
	mailJSON(t, engine, http.MethodPost, "/notify/mail-accounts",
		mailAccountBody("默认邮箱", "default.smtp", 25, `,"isDefault":true`))
	sender, err = h.resolveMailSender(model.NotifyChannel{})
	if err != nil {
		t.Fatalf("取默认邮箱失败: %v", err)
	}
	if sender.Source != "account" || sender.Host != "default.smtp" {
		t.Fatalf("没用默认邮箱: %+v", sender)
	}

	// 渠道显式指定另一个 → 用指定的那个
	mailJSON(t, engine, http.MethodPost, "/notify/mail-accounts",
		mailAccountBody("值班邮箱", "oncall.smtp", 465, ""))
	sender, err = h.resolveMailSender(model.NotifyChannel{MailAccountID: 2})
	if err != nil {
		t.Fatalf("取指定邮箱失败: %v", err)
	}
	if sender.Host != "oncall.smtp" {
		t.Fatalf("没用渠道指定的邮箱: %+v", sender)
	}
}

// 渠道指定的邮箱被停用或删除时直接报错，不静默换一个发件人
func TestMailSenderNeverSilentlyFallsBack(t *testing.T) {
	h, engine := newMailTestHandler(t, "")
	mailJSON(t, engine, http.MethodPost, "/notify/mail-accounts",
		mailAccountBody("默认邮箱", "default.smtp", 25, `,"isDefault":true`))
	mailJSON(t, engine, http.MethodPost, "/notify/mail-accounts",
		mailAccountBody("值班邮箱", "oncall.smtp", 25, ""))

	h.DB.Model(&model.MailAccount{}).Where("id = ?", 2).Update("enabled", false)
	_, err := h.resolveMailSender(model.NotifyChannel{MailAccountID: 2})
	if err == nil {
		t.Fatal("指定的邮箱停用了却静默用了别的发件人")
	}
	if !strings.Contains(err.Error(), "值班邮箱") {
		t.Fatalf("报错要点名是哪个邮箱: %v", err)
	}

	h.DB.Delete(&model.MailAccount{}, 2)
	_, err = h.resolveMailSender(model.NotifyChannel{MailAccountID: 2})
	if err == nil || !strings.Contains(err.Error(), "不存在") {
		t.Fatalf("指定的邮箱被删了应该报错: %v", err)
	}
}

// 口令加密落库、不出接口；编辑留空表示不修改
func TestMailAccountPasswordHandling(t *testing.T) {
	h, engine := newMailTestHandler(t, "unit-test-key")
	mailJSON(t, engine, http.MethodPost, "/notify/mail-accounts",
		mailAccountBody("邮箱", "smtp.corp", 465, `,"username":"u","password":"s3cret"`))

	var item model.MailAccount
	h.DB.First(&item, 1)
	if !cryptox.IsSealed(item.Password) {
		t.Fatalf("口令没加密: %s", item.Password)
	}

	_, parsed, raw := mailJSON(t, engine, http.MethodGet, "/notify/mail-accounts", "")
	if strings.Contains(raw, "s3cret") {
		t.Fatalf("列表接口泄露了口令: %s", raw)
	}
	list, _ := parsed["data"].([]any)
	row := list[0].(map[string]any)
	if row["storage"] != "encrypted" || row["hasPassword"] != true {
		t.Fatalf("存储状态不对: %v", row)
	}

	// 编辑不带口令 → 保持原值
	mailJSON(t, engine, http.MethodPut, "/notify/mail-accounts/1",
		`{"name":"邮箱","host":"smtp.corp","port":587,"username":"u","tlsMode":"starttls"}`)
	var after model.MailAccount
	h.DB.First(&after, 1)
	if after.Password != item.Password {
		t.Fatal("编辑时口令留空却把口令改掉了")
	}
	if after.Port != 587 || after.TLSMode != tlsModeSTARTTLS {
		t.Fatalf("其它字段没更新: %+v", after)
	}

	// 解密要能拿回原文
	sender, err := h.senderFromAccount(after)
	if err != nil {
		t.Fatalf("解密失败: %v", err)
	}
	if sender.Password != "s3cret" {
		t.Fatalf("解出来的口令不对: %s", sender.Password)
	}
}

// 默认邮箱全局唯一；停用的不能设为默认
func TestMailAccountDefaultIsUnique(t *testing.T) {
	h, engine := newMailTestHandler(t, "")
	mailJSON(t, engine, http.MethodPost, "/notify/mail-accounts",
		mailAccountBody("A", "a.smtp", 25, `,"isDefault":true`))
	mailJSON(t, engine, http.MethodPost, "/notify/mail-accounts",
		mailAccountBody("B", "b.smtp", 25, `,"isDefault":true`))

	var defaults []model.MailAccount
	h.DB.Where("is_default = ?", true).Find(&defaults)
	if len(defaults) != 1 || defaults[0].Name != "B" {
		t.Fatalf("默认邮箱应该只有 B 一个: %+v", defaults)
	}

	h.DB.Model(&model.MailAccount{}).Where("id = ?", 1).Update("enabled", false)
	code, _, _ := mailJSON(t, engine, http.MethodPost, "/notify/mail-accounts/1/default", "")
	if code != 400 {
		t.Fatalf("停用的邮箱不该能设为默认，实际 %d", code)
	}
}

// GORM default:true 布尔陷阱回归
func TestMailAccountEnabledSticksFalse(t *testing.T) {
	h, engine := newMailTestHandler(t, "")
	mailJSON(t, engine, http.MethodPost, "/notify/mail-accounts",
		mailAccountBody("停用的", "x.smtp", 25, `,"enabled":false`))
	var item model.MailAccount
	h.DB.First(&item, 1)
	if item.Enabled {
		t.Fatal("创建时 enabled=false 被数据库默认值翻成了 true（gorm default 陷阱）")
	}
}

// 被渠道引用的发件邮箱拒绝删除
func TestMailAccountDeleteBlockedByChannel(t *testing.T) {
	h, engine := newMailTestHandler(t, "")
	mailJSON(t, engine, http.MethodPost, "/notify/mail-accounts",
		mailAccountBody("邮箱", "x.smtp", 25, ""))
	h.DB.Create(&model.NotifyChannel{Name: "告警邮件", Type: "email", MailAccountID: 1})

	code, parsed, _ := mailJSON(t, engine, http.MethodDelete, "/notify/mail-accounts/1", "")
	if code != 400 {
		t.Fatalf("被引用时应该拒绝删除，实际 %d", code)
	}
	if msg, _ := parsed["msg"].(string); !strings.Contains(msg, "通知渠道") {
		t.Fatalf("错误信息要说清原因: %v", msg)
	}

	// 列表要显示引用数
	_, list, _ := mailJSON(t, engine, http.MethodGet, "/notify/mail-accounts", "")
	rows := list["data"].([]any)
	if rows[0].(map[string]any)["channelCount"] != float64(1) {
		t.Fatalf("引用数不对: %v", rows[0])
	}
}

// 从系统配置导入：口令也带过来（界面上看不到它，让人重输最容易出错）
func TestImportGlobalSMTP(t *testing.T) {
	h, engine := newMailTestHandler(t, "unit-test-key")
	h.DB.Create(&model.SysConfig{Group: "smtp", Key: CfgSMTPHost, Value: "old.smtp", Type: "string"})
	h.DB.Create(&model.SysConfig{Group: "smtp", Key: CfgSMTPPort, Value: "587", Type: "int"})
	h.DB.Create(&model.SysConfig{Group: "smtp", Key: CfgSMTPUser, Value: "old@corp", Type: "string"})
	h.DB.Create(&model.SysConfig{Group: "smtp", Key: CfgSMTPPass, Value: h.sealSecret("oldpass"), Type: "string"})
	h.DB.Create(&model.SysConfig{Group: "smtp", Key: CfgSMTPTLS, Value: "false", Type: "bool"})

	code, _, raw := mailJSON(t, engine, http.MethodPost, "/notify/mail-accounts/import-global", "")
	if code != 200 {
		t.Fatalf("导入失败: %s", raw)
	}
	var item model.MailAccount
	h.DB.First(&item, 1)
	if item.Host != "old.smtp" || item.Port != 587 || item.Username != "old@corp" {
		t.Fatalf("导入的连接信息不对: %+v", item)
	}
	// 老配置 smtp.tls=false 按原语义映射成 starttls
	if item.TLSMode != tlsModeSTARTTLS {
		t.Fatalf("加密方式映射不对: %s", item.TLSMode)
	}
	sender, err := h.senderFromAccount(item)
	if err != nil || sender.Password != "oldpass" {
		t.Fatalf("口令没带过来: %v %s", err, sender.Password)
	}

	// 不允许导第二次
	if code, _, _ := mailJSON(t, engine, http.MethodPost, "/notify/mail-accounts/import-global", ""); code != 400 {
		t.Fatalf("重复导入应该被拒，实际 %d", code)
	}
}

// 试发：走假 SMTP 真发一封，失败也返回 200 + ok:false
func TestMailAccountTestSend(t *testing.T) {
	srv := startFakeSMTP(t, false)
	host, port := srv.hostPort()
	h, engine := newMailTestHandler(t, "")
	mailJSON(t, engine, http.MethodPost, "/notify/mail-accounts",
		mailAccountBody("可用邮箱", host, port, ""))

	code, parsed, raw := mailJSON(t, engine, http.MethodPost, "/notify/mail-accounts/1/test",
		`{"to":"ops@corp.local"}`)
	if code != 200 {
		t.Fatalf("试发接口应该返回 200: %s", raw)
	}
	data := parsed["data"].(map[string]any)
	if data["ok"] != true {
		t.Fatalf("试发应该成功: %s", raw)
	}
	if len(srv.received()) != 1 {
		t.Fatal("试发没有真的发出去")
	}
	var item model.MailAccount
	h.DB.First(&item, 1)
	if item.LastTestOK == nil || !*item.LastTestOK || item.LastTestTo != "ops@corp.local" {
		t.Fatalf("试发痕迹没记下: %+v", item)
	}

	// 指向一个没人监听的端口：失败也是 200 + ok:false，并把原因记下来
	mailJSON(t, engine, http.MethodPut, "/notify/mail-accounts/1",
		`{"name":"可用邮箱","host":"127.0.0.1","port":1,"tlsMode":"plain","from":"a@corp"}`)
	code, parsed, raw = mailJSON(t, engine, http.MethodPost, "/notify/mail-accounts/1/test",
		`{"to":"ops@corp.local"}`)
	if code != 200 {
		t.Fatalf("试发失败也该返回 200: %s", raw)
	}
	if parsed["data"].(map[string]any)["ok"] != false {
		t.Fatalf("应该失败: %s", raw)
	}
	h.DB.First(&item, 1)
	if item.LastTestOK == nil || *item.LastTestOK || item.LastTestErr == "" {
		t.Fatalf("失败原因没记下: %+v", item)
	}
}

// 事实条要照实说现在是哪套配置在发信
func TestMailStateTellsActiveSender(t *testing.T) {
	h, engine := newMailTestHandler(t, "")

	_, parsed, raw := mailJSON(t, engine, http.MethodGet, "/notify/mail-state", "")
	data := parsed["data"].(map[string]any)
	if data["activeError"] == nil {
		t.Fatalf("什么都没配时应该给出错误说明: %s", raw)
	}
	notes, _ := data["notes"].([]any)
	if len(notes) < 6 {
		t.Fatalf("口径说明至少 6 条，实际 %d", len(notes))
	}
	if modes, _ := data["tlsModes"].([]any); len(modes) != 3 {
		t.Fatalf("加密方式应该是三选一: %s", raw)
	}

	h.DB.Create(&model.SysConfig{Group: "smtp", Key: CfgSMTPHost, Value: "g.smtp", Type: "string"})
	h.DB.Create(&model.SysConfig{Group: "smtp", Key: CfgSMTPFrom, Value: "g@corp", Type: "string"})
	_, parsed, raw = mailJSON(t, engine, http.MethodGet, "/notify/mail-state", "")
	data = parsed["data"].(map[string]any)
	if data["activeSource"] != "global" {
		t.Fatalf("应该显示当前走全局 SMTP: %s", raw)
	}

	mailJSON(t, engine, http.MethodPost, "/notify/mail-accounts",
		mailAccountBody("默认", "d.smtp", 25, `,"isDefault":true`))
	_, parsed, raw = mailJSON(t, engine, http.MethodGet, "/notify/mail-state", "")
	data = parsed["data"].(map[string]any)
	if data["activeSource"] != "account" || !strings.Contains(data["activeSender"].(string), "默认") {
		t.Fatalf("应该显示走登记的邮箱: %s", raw)
	}
}

// 参数校验：加密方式白名单、端口范围、From 与账号不能都空
func TestMailAccountValidation(t *testing.T) {
	_, engine := newMailTestHandler(t, "")
	cases := []struct{ name, body, wantIn string }{
		{"加密方式非法", `{"name":"a","host":"h","port":25,"from":"a@b","tlsMode":"tls13"}`, "ssl / starttls / plain"},
		{"端口非法", `{"name":"a","host":"h","port":0,"from":"a@b"}`, "端口"},
		{"没有发件人", `{"name":"a","host":"h","port":25}`, "至少填一个"},
	}
	for _, tc := range cases {
		code, parsed, _ := mailJSON(t, engine, http.MethodPost, "/notify/mail-accounts", tc.body)
		if code != 400 {
			t.Fatalf("%s 应该被拒，实际 %d", tc.name, code)
		}
		if msg, _ := parsed["msg"].(string); !strings.Contains(msg, tc.wantIn) {
			t.Fatalf("%s 的错误信息不清楚: %v", tc.name, msg)
		}
	}
}
