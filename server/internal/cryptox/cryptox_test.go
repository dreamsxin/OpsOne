package cryptox

import (
	"errors"
	"strings"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	b := New("some-deploy-time-secret")
	if !b.Enabled() {
		t.Fatal("配了密钥却报未启用")
	}
	for _, plain := range []string{"p@ssw0rd", "-----BEGIN OPENSSH PRIVATE KEY-----\nabc\n-----END-----", "中文口令", strings.Repeat("x", 5000)} {
		sealed := b.Seal(plain)
		if sealed == plain {
			t.Fatalf("加密后与明文相同: %q", plain[:min(len(plain), 20)])
		}
		if !IsSealed(sealed) {
			t.Fatalf("密文缺少前缀: %q", sealed[:min(len(sealed), 20)])
		}
		got, err := b.Open(sealed)
		if err != nil {
			t.Fatalf("解密失败: %v", err)
		}
		if got != plain {
			t.Fatalf("解密结果不一致")
		}
	}
}

// 同一明文两次加密必须不同：nonce 复用会让相同口令在库里长得一样，
// 谁在用同一个口令一眼就能看出来
func TestSealIsRandomized(t *testing.T) {
	b := New("k")
	if b.Seal("same") == b.Seal("same") {
		t.Fatal("两次加密结果相同，nonce 没起作用")
	}
}

func TestEmptyStaysEmpty(t *testing.T) {
	b := New("k")
	if got := b.Seal(""); got != "" {
		t.Fatalf("空串被加密了: %q", got)
	}
	if got, err := b.Open(""); err != nil || got != "" {
		t.Fatalf("空串解密异常: %q %v", got, err)
	}
}

// 没配密钥时是明文直存，而且要如实报告 Enabled=false
func TestPlainModeWhenNoKey(t *testing.T) {
	b := New("   ")
	if b.Enabled() {
		t.Fatal("空密钥不该报启用")
	}
	if got := b.Seal("secret"); got != "secret" {
		t.Fatalf("明文模式下被改写: %q", got)
	}
	if got, err := b.Open("secret"); err != nil || got != "secret" {
		t.Fatalf("明文模式读取异常: %q %v", got, err)
	}
}

// 历史明文（无前缀）必须原样读出，否则一上线所有存量凭据都变乱码
func TestLegacyPlaintextPassesThrough(t *testing.T) {
	b := New("k")
	if got, err := b.Open("legacy-plain-password"); err != nil || got != "legacy-plain-password" {
		t.Fatalf("历史明文被当成密文: %q %v", got, err)
	}
}

// 密文 + 没密钥 = 报错，不能返回空串去连主机（那会伪装成「口令错误」）
func TestSealedWithoutKeyErrors(t *testing.T) {
	sealed := New("k").Seal("p")
	if _, err := New("").Open(sealed); !errors.Is(err, ErrNoKey) {
		t.Fatalf("期望 ErrNoKey，实际 %v", err)
	}
}

func TestWrongKeyFails(t *testing.T) {
	sealed := New("key-a").Seal("p")
	if _, err := New("key-b").Open(sealed); err == nil {
		t.Fatal("换了密钥竟然解开了")
	}
}

// GCM 的完整性：改一个字节就要解不开，而不是解出半截垃圾。
//
// 改的是中间位置的字符而不是末尾：末尾那个字符只承载几个有效 bit，
// 改它可能解出完全相同的字节，测试就会时而通过时而失败
func TestTamperDetected(t *testing.T) {
	b := New("k")
	sealed := b.Seal("payload")
	body := strings.TrimPrefix(sealed, "enc:v1:")
	mid := len(body) / 2
	swap := byte('A')
	if body[mid] == 'A' {
		swap = 'B'
	}
	tampered := "enc:v1:" + body[:mid] + string(swap) + body[mid+1:]
	if _, err := b.Open(tampered); err == nil {
		t.Fatal("篡改后仍能解密")
	}
}


func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
