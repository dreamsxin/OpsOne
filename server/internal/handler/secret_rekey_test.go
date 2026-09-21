package handler

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"ops-platform/server/internal/config"
	"ops-platform/server/internal/cryptox"
	"ops-platform/server/internal/model"
)

// newRekeyTestHandler 一个带 credentials 表与临时备份目录的 Handler。
// 备份是 rekey 的前置步骤，所以测试里必须给一个真能写文件的目录。
func newRekeyTestHandler(t *testing.T, key string) *Handler {
	t.Helper()
	dir := t.TempDir()
	g, err := gorm.Open(sqlite.Open(dir+"/rekey.db"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	if err := g.AutoMigrate(&model.Credential{}); err != nil {
		t.Fatalf("建表失败: %v", err)
	}
	// Windows 上不关连接，t.TempDir 的清理会失败并把测试判成 FAIL
	t.Cleanup(func() {
		if sqlDB, err := g.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	cfg := &config.Config{SecretKey: key, BackupDir: dir + "/backups", RecordDir: dir + "/records", BackupKeep: 3}
	return New(g, cfg)
}

func rekey(t *testing.T, h *Handler, body string) (int, map[string]any) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest("POST", "/secrets/rekey", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	h.RekeySecrets(c)
	var out struct {
		Code int            `json:"code"`
		Msg  string         `json:"msg"`
		Data map[string]any `json:"data"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out.Data
}

func storedCredentialSecret(t *testing.T, h *Handler, id uint) string {
	t.Helper()
	var cred model.Credential
	if err := h.DB.First(&cred, id).Error; err != nil {
		t.Fatalf("读取凭据失败: %v", err)
	}
	return cred.Secret
}

// 换密钥要把每一行解开再用新密钥写回，值不能变
func TestRekeyRewritesWithNewKey(t *testing.T) {
	h := newRekeyTestHandler(t, "key-old")
	cred := model.Credential{Name: "c1", Type: "password", Secret: h.Crypto.Seal("p@ss")}
	if err := h.DB.Create(&cred).Error; err != nil {
		t.Fatalf("写入失败: %v", err)
	}

	code, data := rekey(t, h, `{"mode":"encrypt","newKey":"key-new","confirm":"REKEY"}`)
	if code != 200 {
		t.Fatalf("期望 200，实际 %d（%v）", code, data)
	}
	stored := storedCredentialSecret(t, h, cred.ID)
	if !cryptox.IsSealed(stored) {
		t.Fatalf("换完之后不是密文: %q", stored)
	}
	// 新密钥能解出原值
	if got, err := cryptox.New("key-new").Open(stored); err != nil || got != "p@ss" {
		t.Fatalf("新密钥解不出原值: %q %v", got, err)
	}
	// 旧密钥不该再能解开
	if _, err := cryptox.New("key-old").Open(stored); err == nil {
		t.Fatal("旧密钥仍能解开新密文")
	}
	// 进程内存里的密钥也必须跟着换，否则从这一刻到重启之间凭据全不可用
	if got, err := h.Crypto.Open(stored); err != nil || got != "p@ss" {
		t.Fatalf("进程内的密钥没跟着换: %q %v", got, err)
	}
}

// 取消加密：解回明文，Enabled 变 false，值不变
func TestRekeyToPlainKeepsValue(t *testing.T) {
	h := newRekeyTestHandler(t, "key-old")
	cred := model.Credential{Name: "c1", Type: "password", Secret: h.Crypto.Seal("p@ss")}
	if err := h.DB.Create(&cred).Error; err != nil {
		t.Fatalf("写入失败: %v", err)
	}

	code, _ := rekey(t, h, `{"mode":"plain","confirm":"REKEY"}`)
	if code != 200 {
		t.Fatalf("期望 200，实际 %d", code)
	}
	if got := storedCredentialSecret(t, h, cred.ID); got != "p@ss" {
		t.Fatalf("解回明文后值不对: %q", got)
	}
	if h.Crypto.Enabled() {
		t.Fatal("取消加密后仍报启用")
	}
}

// 最要紧的一条：用当前密钥解不开的行必须原样留着并被计为失败，
// 绝不能覆盖成空值 —— 那会把「密钥配错」变成「凭据被清空」的不可逆事故
func TestRekeySkipsUndecryptableRows(t *testing.T) {
	h := newRekeyTestHandler(t, "key-old")
	good := model.Credential{Name: "good", Type: "password", Secret: h.Crypto.Seal("p@ss")}
	// 另一把密钥留下的行：当前进程解不开
	orphan := model.Credential{Name: "orphan", Type: "password", Secret: cryptox.New("key-other").Seal("other")}
	for _, c := range []*model.Credential{&good, &orphan} {
		if err := h.DB.Create(c).Error; err != nil {
			t.Fatalf("写入失败: %v", err)
		}
	}
	orphanBefore := storedCredentialSecret(t, h, orphan.ID)

	code, data := rekey(t, h, `{"mode":"encrypt","newKey":"key-new","confirm":"REKEY"}`)
	if code != 200 {
		t.Fatalf("期望 200，实际 %d", code)
	}
	if failed, _ := data["failed"].(float64); failed != 1 {
		t.Fatalf("应报 1 行失败，实际 %v", data["failed"])
	}
	if got := storedCredentialSecret(t, h, orphan.ID); got != orphanBefore {
		t.Fatalf("解不开的行被改写了: %q -> %q", orphanBefore, got)
	}
	if got, err := cryptox.New("key-new").Open(storedCredentialSecret(t, h, good.ID)); err != nil || got != "p@ss" {
		t.Fatalf("能解开的行没换成新密钥: %q %v", got, err)
	}
	note, _ := data["note"].(string)
	if !strings.Contains(note, "解不开") {
		t.Fatalf("提示里没说清有行解不开: %q", note)
	}
}

func TestRekeyRejectsBadInput(t *testing.T) {
	h := newRekeyTestHandler(t, "key-old")
	for _, body := range []string{
		`{"mode":"encrypt","newKey":"key-new"}`,              // 少了确认
		`{"mode":"encrypt","newKey":"","confirm":"REKEY"}`,   // 想换密钥却不给密钥
		`{"mode":"nonsense","newKey":"x","confirm":"REKEY"}`, // 模式不认识
		`{"mode":"encrypt","newKey":"ab","confirm":"REKEY"}`, // 太短
	} {
		if code, _ := rekey(t, h, body); code != 400 {
			t.Fatalf("body=%s 应该被拒，实际 %d", body, code)
		}
	}
}
