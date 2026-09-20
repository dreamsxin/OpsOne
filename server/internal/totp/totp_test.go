package totp

import (
	"strings"
	"testing"
	"time"
)

// rfc6238Secret 是 RFC 6238 附录 B 的测试密钥 "12345678901234567890" 的 base32 形式
const rfc6238Secret = "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"

// TestCodeRFC6238 用 RFC 6238 官方测试向量校验算法。
// 官方给的是 8 位码，取后 6 位即为本实现（6 位）的期望值。
func TestCodeRFC6238(t *testing.T) {
	cases := []struct {
		unix int64
		want string // 官方 8 位码的后 6 位
	}{
		{59, "287082"},          // 94287082
		{1111111109, "081804"},  // 07081804
		{1111111111, "050471"},  // 14050471
		{1234567890, "005924"},  // 89005924
		{2000000000, "279037"},  // 69279037
		{20000000000, "353130"}, // 65353130
	}
	for _, tc := range cases {
		got, err := Code(rfc6238Secret, time.Unix(tc.unix, 0))
		if err != nil {
			t.Fatalf("T=%d 计算失败: %v", tc.unix, err)
		}
		if got != tc.want {
			t.Errorf("T=%d: 期望 %s，实际 %s", tc.unix, tc.want, got)
		}
	}
}

func TestVerifyWindow(t *testing.T) {
	now := time.Unix(1111111111, 0)
	code, err := Code(rfc6238Secret, now)
	if err != nil {
		t.Fatalf("计算失败: %v", err)
	}

	if !Verify(rfc6238Secret, code, now) {
		t.Error("当前窗口的验证码应通过")
	}
	// 前后各一个窗口要容忍（时钟偏差）
	if !Verify(rfc6238Secret, code, now.Add(Period*time.Second)) {
		t.Error("后一个窗口应仍然接受")
	}
	if !Verify(rfc6238Secret, code, now.Add(-Period*time.Second)) {
		t.Error("前一个窗口应仍然接受")
	}
	// 超出窗口必须拒绝，否则等于没有时效
	if Verify(rfc6238Secret, code, now.Add(3*Period*time.Second)) {
		t.Error("超出容忍窗口的验证码必须拒绝")
	}
}

func TestVerifyRejectsBadInput(t *testing.T) {
	now := time.Unix(1111111111, 0)
	for _, code := range []string{"", "12345", "1234567", "abcdef", "000000"} {
		if Verify(rfc6238Secret, code, now) && code != "050471" {
			t.Errorf("非法验证码 %q 不应通过", code)
		}
	}
	if Verify("not-base32!!", "050471", now) {
		t.Error("密钥非法时必须拒绝")
	}
}

func TestNewSecretAndURI(t *testing.T) {
	secret, err := NewSecret()
	if err != nil {
		t.Fatalf("生成密钥失败: %v", err)
	}
	if len(secret) != 32 { // 20 字节 base32 无填充
		t.Errorf("密钥长度异常: %d", len(secret))
	}
	if _, err := DecodeSecret(strings.ToLower(secret)); err != nil {
		t.Errorf("小写密钥应能解析: %v", err)
	}

	uri := ProvisioningURI(secret, "admin", "OpsOne")
	for _, want := range []string{"otpauth://totp/", "secret=" + secret, "issuer=OpsOne", "digits=6", "period=30"} {
		if !strings.Contains(uri, want) {
			t.Errorf("otpauth 地址缺少 %s: %s", want, uri)
		}
	}
}
