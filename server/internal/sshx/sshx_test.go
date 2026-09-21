package sshx

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func genKey(t *testing.T, passphrase string) string {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("生成密钥失败: %v", err)
	}
	var block *pem.Block
	if passphrase == "" {
		block, err = ssh.MarshalPrivateKey(priv, "")
	} else {
		block, err = ssh.MarshalPrivateKeyWithPassphrase(priv, "", []byte(passphrase))
	}
	if err != nil {
		t.Fatalf("序列化私钥失败: %v", err)
	}
	return string(pem.EncodeToMemory(block))
}

func TestParseKeyPlain(t *testing.T) {
	pemStr := genKey(t, "")
	fp, err := PublicKeyFingerprint(pemStr, "")
	if err != nil || !strings.HasPrefix(fp, "SHA256:") {
		t.Fatalf("无口令私钥应能解析: fp=%q err=%v", fp, err)
	}
}

// 带口令的私钥以前完全用不了（ParsePrivateKey 直接报错），这是凭证库补上的能力
func TestParseKeyWithPassphrase(t *testing.T) {
	pemStr := genKey(t, "s3cret")
	if _, err := PublicKeyFingerprint(pemStr, "s3cret"); err != nil {
		t.Fatalf("带口令私钥应能解析: %v", err)
	}
}

// 缺口令时的报错必须说「缺的是口令」，否则只会看到一句 parse failed
func TestParseKeyMissingPassphraseSaysSo(t *testing.T) {
	pemStr := genKey(t, "s3cret")
	_, err := PublicKeyFingerprint(pemStr, "")
	if err == nil || !strings.Contains(err.Error(), "口令保护") {
		t.Fatalf("期望提示需要口令，实际: %v", err)
	}
}

func TestParseKeyWrongPassphrase(t *testing.T) {
	pemStr := genKey(t, "s3cret")
	if _, err := PublicKeyFingerprint(pemStr, "nope"); err == nil {
		t.Fatal("口令不对却解析成功了")
	}
}

// 私钥没加密但表单里留着旧口令：这没有安全影响，不该拿「口令不对」误导人
func TestParseKeyIgnoresPassphraseOnPlainKey(t *testing.T) {
	pemStr := genKey(t, "")
	if _, err := PublicKeyFingerprint(pemStr, "stale-passphrase"); err != nil {
		t.Fatalf("无口令私钥多给了口令也该能用: %v", err)
	}
}

// 组装 Target 时就失败的情况（引用的共享凭据被禁用 / 解不开）必须直接抛原因，
// 不能拿空口令去连主机 —— 那会表现成「认证失败」，把人引向错误方向
func TestPrepareErrorShortCircuitsDial(t *testing.T) {
	_, err := Dial(Target{Address: "127.0.0.1", Port: 1, PrepareError: "凭据「x」已被禁用"}, 0)
	if err == nil || !strings.Contains(err.Error(), "已被禁用") {
		t.Fatalf("期望直接返回准备阶段的原因，实际: %v", err)
	}
}
