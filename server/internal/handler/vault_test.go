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
	"ops-platform/server/internal/cryptox"
	"ops-platform/server/internal/model"
	"ops-platform/server/internal/totp"
)

func newVaultTestHandler(t *testing.T, secretKey string) (*Handler, *gin.Engine) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	dir := t.TempDir()
	g, err := gorm.Open(sqlite.Open(dir+"/vault.db"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	if err := g.AutoMigrate(
		&model.VaultAccount{}, &model.VaultTOTP{}, &model.VaultAccess{},
		&model.Alert{}, &model.AlertSource{}, &model.User{}, &model.SysConfig{},
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
		c.Set("ctx_user", &model.User{ID: 7, Username: "zhangsan"})
		c.Next()
	})
	engine.GET("/vault/state", h.GetVaultState)
	engine.GET("/vault/accounts", h.ListVaultAccounts)
	engine.POST("/vault/accounts", h.CreateVaultAccount)
	engine.PUT("/vault/accounts/:id", h.UpdateVaultAccount)
	engine.DELETE("/vault/accounts/:id", h.DeleteVaultAccount)
	engine.POST("/vault/accounts/:id/reveal", h.RevealVaultAccount)
	engine.POST("/vault/accounts/:id/rotate", h.RotateVaultAccount)
	engine.GET("/vault/accesses", h.ListVaultAccesses)
	engine.GET("/vault/totps", h.ListVaultTOTPs)
	engine.POST("/vault/totps", h.CreateVaultTOTP)
	engine.PUT("/vault/totps/:id", h.UpdateVaultTOTP)
	engine.DELETE("/vault/totps/:id", h.DeleteVaultTOTP)
	engine.POST("/vault/totps/:id/code", h.CodeVaultTOTP)
	engine.POST("/vault/totps/:id/uri", h.URIVaultTOTP)
	return h, engine
}

func vaultJSON(t *testing.T, engine *gin.Engine, method, path, body string) (int, map[string]any, string) {
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

func vaultData(t *testing.T, parsed map[string]any) map[string]any {
	t.Helper()
	data, ok := parsed["data"].(map[string]any)
	if !ok {
		t.Fatalf("响应里没有 data 对象: %v", parsed)
	}
	return data
}

const vaultCreateBody = `{"name":"Jenkins 管理员","category":"system","platform":"Jenkins",
	"url":"http://jenkins.internal","username":"admin","secret":"P@ssw0rd-jenkins",
	"owner":"张三","rotateDays":90}`

// 取明文必须留痕，且留痕里不能出现明文本身
func TestVaultRevealRecordsAccess(t *testing.T) {
	h, engine := newVaultTestHandler(t, "")
	if code, _, raw := vaultJSON(t, engine, http.MethodPost, "/vault/accounts", vaultCreateBody); code != 200 {
		t.Fatalf("创建失败: %s", raw)
	}

	code, parsed, raw := vaultJSON(t, engine, http.MethodPost, "/vault/accounts/1/reveal",
		`{"reason":"排查构建失败"}`)
	if code != 200 {
		t.Fatalf("取明文失败: %s", raw)
	}
	if got := vaultData(t, parsed)["secret"]; got != "P@ssw0rd-jenkins" {
		t.Fatalf("取回来的口令不对: %v", got)
	}

	var accesses []model.VaultAccess
	h.DB.Find(&accesses)
	if len(accesses) != 1 {
		t.Fatalf("应该留下 1 条取用记录，实际 %d 条", len(accesses))
	}
	rec := accesses[0]
	if rec.Action != "reveal" || rec.Operator != "zhangsan" || rec.OperatorID != 7 {
		t.Fatalf("留痕内容不对: %+v", rec)
	}
	if rec.TargetName != "Jenkins 管理员" || rec.Reason != "排查构建失败" {
		t.Fatalf("留痕缺少可读信息: %+v", rec)
	}
	if strings.Contains(rec.Reason, "P@ssw0rd") {
		t.Fatal("留痕里出现了明文口令")
	}

	// 冗余汇总要同步更新，列表页靠它显示「最近谁看过」
	var item model.VaultAccount
	h.DB.First(&item, 1)
	if item.ViewCount != 1 || item.LastViewedBy != "zhangsan" || item.LastViewedAt == nil {
		t.Fatalf("取用统计没更新: %+v", item)
	}
}

// 留痕写不进去时必须中止，明文不能发出去。
// 这是这一页最核心的不变量：写在收尾而不是前置的实现会在这里失败。
func TestVaultRevealAbortsWhenAuditFails(t *testing.T) {
	h, engine := newVaultTestHandler(t, "")
	vaultJSON(t, engine, http.MethodPost, "/vault/accounts", vaultCreateBody)

	if err := h.DB.Migrator().DropTable(&model.VaultAccess{}); err != nil {
		t.Fatalf("删留痕表失败: %v", err)
	}

	code, _, raw := vaultJSON(t, engine, http.MethodPost, "/vault/accounts/1/reveal", `{}`)
	if code == 200 {
		t.Fatalf("留痕写不进去却返回了成功: %s", raw)
	}
	if strings.Contains(raw, "P@ssw0rd-jenkins") {
		t.Fatalf("留痕失败但明文已经发出去了: %s", raw)
	}

	// 取用统计也不该被推进
	var item model.VaultAccount
	h.DB.First(&item, 1)
	if item.ViewCount != 0 {
		t.Fatalf("取用失败却累加了次数: %d", item.ViewCount)
	}
}

// 编辑接口不接受口令，改口令只能走轮换 —— 否则 rotatedAt 就是假的
func TestVaultUpdateRejectsSecret(t *testing.T) {
	_, engine := newVaultTestHandler(t, "")
	vaultJSON(t, engine, http.MethodPost, "/vault/accounts", vaultCreateBody)

	code, parsed, _ := vaultJSON(t, engine, http.MethodPut, "/vault/accounts/1",
		`{"name":"Jenkins 管理员","username":"admin","secret":"newpass"}`)
	if code != 400 {
		t.Fatalf("编辑带口令应该被拒，实际 %d", code)
	}
	if msg, _ := parsed["msg"].(string); !strings.Contains(msg, "轮换") {
		t.Fatalf("错误信息应该指向轮换接口: %v", msg)
	}

	// 不带口令的正常编辑要能过，且不影响原口令
	if code, _, raw := vaultJSON(t, engine, http.MethodPut, "/vault/accounts/1",
		`{"name":"Jenkins 管理员","username":"admin","owner":"李四"}`); code != 200 {
		t.Fatalf("正常编辑失败: %s", raw)
	}
	_, parsed, _ = vaultJSON(t, engine, http.MethodPost, "/vault/accounts/1/reveal", `{}`)
	if got := vaultData(t, parsed)["secret"]; got != "P@ssw0rd-jenkins" {
		t.Fatalf("编辑把口令弄丢了: %v", got)
	}
}

// 轮换会重置逾期基准，并留一条 rotate 痕
func TestVaultRotateResetsOverdue(t *testing.T) {
	h, engine := newVaultTestHandler(t, "")
	vaultJSON(t, engine, http.MethodPost, "/vault/accounts", vaultCreateBody)

	// 造一条「100 天前录入、要求 90 天一换」的记录
	old := time.Now().Add(-100 * 24 * time.Hour)
	h.DB.Model(&model.VaultAccount{}).Where("id = ?", 1).Update("created_at", old)

	var item model.VaultAccount
	h.DB.First(&item, 1)
	if days := vaultRotateOverdue(item, time.Now()); days != 10 {
		t.Fatalf("逾期天数应该是 10，实际 %d", days)
	}

	code, parsed, raw := vaultJSON(t, engine, http.MethodPost, "/vault/accounts/1/rotate",
		`{"secret":"NewP@ss","reason":"季度轮换"}`)
	if code != 200 {
		t.Fatalf("轮换失败: %s", raw)
	}
	if got := vaultData(t, parsed)["overdueDays"]; got != float64(0) {
		t.Fatalf("轮换后不该再逾期: %v", got)
	}

	_, parsed, _ = vaultJSON(t, engine, http.MethodPost, "/vault/accounts/1/reveal", `{}`)
	if got := vaultData(t, parsed)["secret"]; got != "NewP@ss" {
		t.Fatalf("轮换后取到的还是老口令: %v", got)
	}

	var rotateCount int64
	h.DB.Model(&model.VaultAccess{}).Where("action = ?", "rotate").Count(&rotateCount)
	if rotateCount != 1 {
		t.Fatalf("轮换应该留痕，实际 %d 条", rotateCount)
	}
}

// 逾期基准：从没轮换过的按录入时间算，不能当成 0 天
func TestVaultRotateOverdueBaseline(t *testing.T) {
	now := time.Now()
	created := now.Add(-200 * 24 * time.Hour)
	rotated := now.Add(-10 * 24 * time.Hour)

	cases := []struct {
		name string
		item model.VaultAccount
		want int
	}{
		{"没开轮换周期", model.VaultAccount{CreatedAt: created}, 0},
		{"从没轮换过", model.VaultAccount{CreatedAt: created, RotateDays: 90}, 110},
		{"换过且没到期", model.VaultAccount{CreatedAt: created, RotateDays: 90, RotatedAt: &rotated}, 0},
		{"换过但又到期", model.VaultAccount{CreatedAt: created, RotateDays: 5, RotatedAt: &rotated}, 5},
	}
	for _, tc := range cases {
		if got := vaultRotateOverdue(tc.item, now); got != tc.want {
			t.Fatalf("%s: 期望 %d，实际 %d", tc.name, tc.want, got)
		}
	}
}

// 配了密钥就必须是密文落库，且能原样解回来
func TestVaultSecretSealed(t *testing.T) {
	h, engine := newVaultTestHandler(t, "unit-test-key")
	vaultJSON(t, engine, http.MethodPost, "/vault/accounts", vaultCreateBody)

	var item model.VaultAccount
	h.DB.First(&item, 1)
	if !cryptox.IsSealed(item.Secret) {
		t.Fatalf("配了密钥却明文落库: %s", item.Secret)
	}
	if strings.Contains(item.Secret, "P@ssw0rd") {
		t.Fatal("密文里能看见明文")
	}

	_, parsed, _ := vaultJSON(t, engine, http.MethodPost, "/vault/accounts/1/reveal", `{}`)
	if got := vaultData(t, parsed)["secret"]; got != "P@ssw0rd-jenkins" {
		t.Fatalf("解不回来: %v", got)
	}

	_, parsed, _ = vaultJSON(t, engine, http.MethodGet, "/vault/accounts?page=1&pageSize=10", "")
	list := vaultData(t, parsed)["list"].([]any)
	row := list[0].(map[string]any)
	if row["storage"] != "encrypted" {
		t.Fatalf("存储状态应该是 encrypted: %v", row["storage"])
	}
	if _, leaked := row["secret"]; leaked {
		t.Fatal("列表接口把口令字段带出去了")
	}
}

// GORM default:true 布尔陷阱回归：创建时显式停用必须真的是停用
func TestVaultEnabledSticksFalse(t *testing.T) {
	h, engine := newVaultTestHandler(t, "")
	body := `{"name":"停用的账号","username":"u","secret":"p","enabled":false}`
	if code, _, raw := vaultJSON(t, engine, http.MethodPost, "/vault/accounts", body); code != 200 {
		t.Fatalf("创建失败: %s", raw)
	}
	var item model.VaultAccount
	h.DB.First(&item, 1)
	if item.Enabled {
		t.Fatal("创建时 enabled=false 被数据库默认值翻成了 true（gorm default 陷阱）")
	}

	// 停用的条目不给取明文
	code, _, _ := vaultJSON(t, engine, http.MethodPost, "/vault/accounts/1/reveal", `{}`)
	if code != 400 {
		t.Fatalf("停用条目应该拒绝取明文，实际 %d", code)
	}
}

// 有 2FA 种子关联时不能静默连带删除
func TestVaultDeleteBlockedByTOTP(t *testing.T) {
	h, engine := newVaultTestHandler(t, "")
	vaultJSON(t, engine, http.MethodPost, "/vault/accounts", vaultCreateBody)
	seed, _ := totp.NewSecret()
	if code, _, raw := vaultJSON(t, engine, http.MethodPost, "/vault/totps",
		`{"name":"Jenkins 2FA","accountId":1,"secret":"`+seed+`"}`); code != 200 {
		t.Fatalf("创建种子失败: %s", raw)
	}

	code, parsed, _ := vaultJSON(t, engine, http.MethodDelete, "/vault/accounts/1", "")
	if code != 400 {
		t.Fatalf("有关联种子时应该拒绝删除，实际 %d", code)
	}
	if msg, _ := parsed["msg"].(string); !strings.Contains(msg, "2FA") {
		t.Fatalf("错误信息应该说明原因: %v", msg)
	}

	// 解除关联后可以删，留痕要留着
	vaultJSON(t, engine, http.MethodPost, "/vault/accounts/1/reveal", `{}`)
	h.DB.Model(&model.VaultTOTP{}).Where("id = ?", 1).Update("account_id", 0)
	if code, _, raw := vaultJSON(t, engine, http.MethodDelete, "/vault/accounts/1", ""); code != 200 {
		t.Fatalf("删除失败: %s", raw)
	}
	var accesses []model.VaultAccess
	h.DB.Find(&accesses)
	if len(accesses) == 0 || accesses[0].TargetName != "Jenkins 管理员" {
		t.Fatalf("条目删了留痕也该留着且读得懂: %+v", accesses)
	}
}

// 种子解析：两种形态都要接，参数不兼容的要当场拒绝而不是忽略
func TestParseTOTPSeed(t *testing.T) {
	good := "JBSWY3DPEHPK3PXP"

	if seed, err := parseTOTPSeed("jbswy3dp ehpk3pxp"); err != nil || seed.Secret != good {
		t.Fatalf("小写带空格的种子应该被接受: %+v %v", seed, err)
	}

	uri := "otpauth://totp/OpsOne:admin%40corp?secret=" + good + "&issuer=OpsOne&algorithm=SHA1&digits=6&period=30"
	seed, err := parseTOTPSeed(uri)
	if err != nil {
		t.Fatalf("otpauth 地址解析失败: %v", err)
	}
	if seed.Secret != good || seed.Issuer != "OpsOne" || seed.Account != "admin@corp" {
		t.Fatalf("otpauth 地址里的信息没提出来: %+v", seed)
	}

	bad := []struct{ name, input, wantIn string }{
		{"乱码", "not-base32!!", "base32"},
		{"空", "   ", "不能为空"},
		{"SHA256", "otpauth://totp/a?secret=" + good + "&algorithm=SHA256", "SHA1"},
		{"8 位", "otpauth://totp/a?secret=" + good + "&digits=8", "位验证码"},
		{"60 秒步长", "otpauth://totp/a?secret=" + good + "&period=60", "步长"},
		{"HOTP", "otpauth://hotp/a?secret=" + good, "otpauth://totp/"},
		{"地址里种子非法", "otpauth://totp/a?secret=###", "不合法"},
	}
	for _, tc := range bad {
		_, err := parseTOTPSeed(tc.input)
		if err == nil {
			t.Fatalf("%s 应该被拒绝", tc.name)
		}
		if !strings.Contains(err.Error(), tc.wantIn) {
			t.Fatalf("%s 的错误信息没说清原因: %v", tc.name, err)
		}
	}
}

// 出码要和算法本身一致，并且照实给出服务器时间与剩余秒数
func TestVaultTOTPCode(t *testing.T) {
	h, engine := newVaultTestHandler(t, "unit-test-key")
	seed := "JBSWY3DPEHPK3PXP"
	vaultJSON(t, engine, http.MethodPost, "/vault/totps",
		`{"name":"Jenkins 2FA","issuer":"OpsOne","account":"admin","secret":"`+seed+`"}`)

	var item model.VaultTOTP
	h.DB.First(&item, 1)
	if !cryptox.IsSealed(item.Secret) {
		t.Fatalf("种子应该密文落库: %s", item.Secret)
	}

	code, parsed, raw := vaultJSON(t, engine, http.MethodPost, "/vault/totps/1/code", `{"reason":"帮同事登录"}`)
	if code != 200 {
		t.Fatalf("出码失败: %s", raw)
	}
	data := vaultData(t, parsed)
	want, _ := totp.Code(seed, time.Now())
	if data["code"] != want {
		t.Fatalf("算出来的码和算法不一致: %v != %v", data["code"], want)
	}
	remain, _ := data["remainSeconds"].(float64)
	if remain < 1 || remain > float64(totp.Period) {
		t.Fatalf("剩余秒数不在 1..%d: %v", totp.Period, remain)
	}
	if data["serverTime"] == nil {
		t.Fatal("必须返回服务器时间：时钟偏了会表现成验证码永远不对")
	}

	var count int64
	h.DB.Model(&model.VaultAccess{}).Where("target = ? AND action = ?", "totp", "code").Count(&count)
	if count != 1 {
		t.Fatalf("出码应该留痕，实际 %d 条", count)
	}
}

// 导出的 otpauth 地址必须能被自己解回同一个种子
func TestVaultTOTPURIRoundTrip(t *testing.T) {
	h, engine := newVaultTestHandler(t, "")
	seed := "JBSWY3DPEHPK3PXP"
	vaultJSON(t, engine, http.MethodPost, "/vault/totps",
		`{"name":"Jenkins 2FA","issuer":"OpsOne","account":"admin@corp","secret":"`+seed+`"}`)

	code, parsed, raw := vaultJSON(t, engine, http.MethodPost, "/vault/totps/1/uri", `{}`)
	if code != 200 {
		t.Fatalf("导出失败: %s", raw)
	}
	uri, _ := vaultData(t, parsed)["uri"].(string)
	back, err := parseTOTPSeed(uri)
	if err != nil {
		t.Fatalf("导出的地址自己解不开: %v", err)
	}
	if back.Secret != seed || back.Issuer != "OpsOne" || back.Account != "admin@corp" {
		t.Fatalf("往返丢信息: %+v", back)
	}

	var count int64
	h.DB.Model(&model.VaultAccess{}).Where("action = ?", "uri").Count(&count)
	if count != 1 {
		t.Fatalf("导出种子应该和取明文同级留痕，实际 %d 条", count)
	}
}

// 换绑种子时要重新校验；乱码不能写进库
func TestVaultTOTPUpdateValidatesSeed(t *testing.T) {
	h, engine := newVaultTestHandler(t, "")
	seed := "JBSWY3DPEHPK3PXP"
	vaultJSON(t, engine, http.MethodPost, "/vault/totps", `{"name":"a","secret":"`+seed+`"}`)

	if code, _, _ := vaultJSON(t, engine, http.MethodPut, "/vault/totps/1",
		`{"name":"a","secret":"garbage!!"}`); code != 400 {
		t.Fatalf("乱码种子应该被拒，实际 %d", code)
	}
	var item model.VaultTOTP
	h.DB.First(&item, 1)
	if item.Secret != seed {
		t.Fatalf("被拒的更新不该动种子: %s", item.Secret)
	}

	// 不带种子的编辑不动种子
	if code, _, raw := vaultJSON(t, engine, http.MethodPut, "/vault/totps/1",
		`{"name":"改个名","owner":"张三"}`); code != 200 {
		t.Fatalf("正常编辑失败: %s", raw)
	}
	h.DB.First(&item, 1)
	if item.Secret != seed || item.Name != "改个名" {
		t.Fatalf("编辑结果不对: %+v", item)
	}
}

// 逾期提醒进告警通道，轮换后收到 resolved
func TestVaultRotationAlert(t *testing.T) {
	h, engine := newVaultTestHandler(t, "")
	vaultJSON(t, engine, http.MethodPost, "/vault/accounts", vaultCreateBody)
	h.DB.Model(&model.VaultAccount{}).Where("id = ?", 1).
		Update("created_at", time.Now().Add(-200*24*time.Hour))

	h.RemindVaultRotationForSchedule()

	var alerts []model.Alert
	h.DB.Find(&alerts)
	if len(alerts) != 1 {
		t.Fatalf("逾期应该产生 1 条告警，实际 %d 条", len(alerts))
	}
	if !strings.Contains(alerts[0].Title, "Jenkins 管理员") {
		t.Fatalf("告警标题里要有条目名: %s", alerts[0].Title)
	}
	// 逾期 110 天 > 一个完整周期 90 天，按 critical 报
	if alerts[0].Severity != "critical" {
		t.Fatalf("逾期超过一个周期应该是 critical，实际 %s", alerts[0].Severity)
	}

	// 再跑一次只累加次数，不刷屏
	h.RemindVaultRotationForSchedule()
	h.DB.Find(&alerts)
	if len(alerts) != 1 || alerts[0].Count != 2 {
		t.Fatalf("重复扫描应该只累加次数: %d 条 count=%d", len(alerts), alerts[0].Count)
	}

	// 轮换之后收到恢复
	vaultJSON(t, engine, http.MethodPost, "/vault/accounts/1/rotate", `{"secret":"NewP@ss"}`)
	h.RemindVaultRotationForSchedule()
	h.DB.First(&alerts[0], alerts[0].ID)
	if alerts[0].Status != "resolved" {
		t.Fatalf("轮换后应该恢复，实际 %s", alerts[0].Status)
	}

	// 没开轮换周期的条目不参与，平台不替用户假设一个周期
	vaultJSON(t, engine, http.MethodPost, "/vault/accounts",
		`{"name":"没设周期","username":"u","secret":"p"}`)
	h.DB.Model(&model.VaultAccount{}).Where("id = ?", 2).
		Update("created_at", time.Now().Add(-2000*24*time.Hour))
	h.RemindVaultRotationForSchedule()
	var total int64
	h.DB.Model(&model.Alert{}).Count(&total)
	if total != 1 {
		t.Fatalf("没设轮换周期的条目不该产生告警，告警总数 %d", total)
	}
}

// 事实条要照实区分「加密了」「还是明文」「逾期」「没设周期」
func TestVaultStateFacts(t *testing.T) {
	h, engine := newVaultTestHandler(t, "")
	vaultJSON(t, engine, http.MethodPost, "/vault/accounts", vaultCreateBody)
	vaultJSON(t, engine, http.MethodPost, "/vault/accounts",
		`{"name":"没设周期","username":"u","secret":"p"}`)
	h.DB.Model(&model.VaultAccount{}).Where("id = ?", 1).
		Update("created_at", time.Now().Add(-200*24*time.Hour))

	_, parsed, raw := vaultJSON(t, engine, http.MethodGet, "/vault/state", "")
	data := vaultData(t, parsed)
	if data["encryptEnabled"] != false {
		t.Fatalf("没配密钥就该照实说没加密: %s", raw)
	}
	if data["total"] != float64(2) || data["plain"] != float64(2) || data["sealed"] != float64(0) {
		t.Fatalf("统计不对: %s", raw)
	}
	if data["overdue"] != float64(1) || data["noRotatePolicy"] != float64(1) {
		t.Fatalf("逾期与未设周期的计数不对: %s", raw)
	}
	notes, _ := data["notes"].([]any)
	if len(notes) < 5 {
		t.Fatalf("口径说明至少 5 条，实际 %d", len(notes))
	}
}

// 新增的密钥列必须登记进体检清单 —— 漏登记比不加密更糟，体检页会显得一切正常
func TestVaultSecretFieldsRegistered(t *testing.T) {
	want := map[string]string{
		"vault_accounts": "secret",
		"vault_totps":    "secret",
	}
	for table, column := range want {
		found := false
		for _, field := range secretFields {
			if field.Table == table && field.Column == column {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("%s.%s 没登记到 secretFields，加密体检会漏报", table, column)
		}
	}
}

// 留痕列表按条目过滤，两个库的痕互不串
func TestVaultAccessFilter(t *testing.T) {
	_, engine := newVaultTestHandler(t, "")
	vaultJSON(t, engine, http.MethodPost, "/vault/accounts", vaultCreateBody)
	vaultJSON(t, engine, http.MethodPost, "/vault/totps",
		`{"name":"t","secret":"JBSWY3DPEHPK3PXP"}`)
	vaultJSON(t, engine, http.MethodPost, "/vault/accounts/1/reveal", `{}`)
	vaultJSON(t, engine, http.MethodPost, "/vault/totps/1/code", `{}`)

	_, parsed, raw := vaultJSON(t, engine, http.MethodGet,
		"/vault/accesses?target=account&targetId=1", "")
	data := vaultData(t, parsed)
	if data["total"] != float64(1) {
		t.Fatalf("按条目过滤应该只剩 1 条: %s", raw)
	}
	list := data["list"].([]any)
	if list[0].(map[string]any)["action"] != "reveal" {
		t.Fatalf("过滤结果串了: %s", raw)
	}

	_, parsed, _ = vaultJSON(t, engine, http.MethodGet, "/vault/accesses?action=code", "")
	if vaultData(t, parsed)["total"] != float64(1) {
		t.Fatal("按动作过滤不对")
	}
}
