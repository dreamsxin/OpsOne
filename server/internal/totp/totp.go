// Package totp 实现 RFC 6238 的基于时间的一次性口令（TOTP-SHA1，6 位，30 秒步长），
// 与 Google Authenticator / Microsoft Authenticator / 1Password 等通用验证器兼容。
//
// 只做算法本身，不涉及存储与业务流程。
package totp

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/url"
	"strings"
	"time"
)

const (
	// Period 时间步长（秒）
	Period = 30
	// Digits 验证码位数
	Digits = 6
	// Skew 允许的前后窗口数，1 表示接受前后各 30 秒，用于容忍时钟偏差
	Skew = 1
	// secretBytes 密钥长度，RFC 4226 建议至少 128 位，这里用 160 位
	secretBytes = 20
)

var encoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewSecret 生成一个新的 base32 密钥（无填充，大写）
func NewSecret() (string, error) {
	buf := make([]byte, secretBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return encoding.EncodeToString(buf), nil
}

// DecodeSecret 解析 base32 密钥，容忍小写与空格
func DecodeSecret(secret string) ([]byte, error) {
	normalized := strings.ToUpper(strings.NewReplacer(" ", "", "-", "", "=", "").Replace(secret))
	key, err := encoding.DecodeString(normalized)
	if err != nil {
		return nil, fmt.Errorf("密钥不是合法的 base32: %w", err)
	}
	if len(key) == 0 {
		return nil, fmt.Errorf("密钥为空")
	}
	return key, nil
}

// Code 计算指定时刻的验证码
func Code(secret string, at time.Time) (string, error) {
	key, err := DecodeSecret(secret)
	if err != nil {
		return "", err
	}
	return codeForCounter(key, uint64(at.Unix()/Period)), nil
}

// Verify 校验验证码，前后各放宽 Skew 个窗口。
//
// 比较用常量时间，避免按字符逐位比较带来的时序差异。
func Verify(secret, code string, at time.Time) bool {
	ok, _ := VerifyWithCounter(secret, code, at)
	return ok
}

// VerifyWithCounter 在校验之外返回命中的时间窗计数器，
// 调用方可以记录「已用过的窗口」来拒绝同一验证码在有效期内被重复使用。
func VerifyWithCounter(secret, code string, at time.Time) (bool, int64) {
	code = strings.TrimSpace(code)
	if len(code) != Digits {
		return false, 0
	}
	key, err := DecodeSecret(secret)
	if err != nil {
		return false, 0
	}

	counter := at.Unix() / Period
	for offset := int64(-Skew); offset <= Skew; offset++ {
		candidate := codeForCounter(key, uint64(counter+offset))
		if subtle.ConstantTimeCompare([]byte(candidate), []byte(code)) == 1 {
			return true, counter + offset
		}
	}
	return false, 0
}

// ProvisioningURI 生成验证器扫码用的 otpauth:// 地址
func ProvisioningURI(secret, account, issuer string) string {
	label := account
	if issuer != "" {
		label = issuer + ":" + account
	}
	query := url.Values{}
	query.Set("secret", secret)
	if issuer != "" {
		query.Set("issuer", issuer)
	}
	query.Set("algorithm", "SHA1")
	query.Set("digits", fmt.Sprint(Digits))
	query.Set("period", fmt.Sprint(Period))

	return "otpauth://totp/" + url.PathEscape(label) + "?" + query.Encode()
}

// codeForCounter HOTP（RFC 4226）动态截断
func codeForCounter(key []byte, counter uint64) string {
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], counter)

	mac := hmac.New(sha1.New, key)
	mac.Write(buf[:])
	sum := mac.Sum(nil)

	offset := sum[len(sum)-1] & 0x0f
	value := binary.BigEndian.Uint32(sum[offset:offset+4]) & 0x7fffffff

	mod := uint32(1)
	for i := 0; i < Digits; i++ {
		mod *= 10
	}
	return fmt.Sprintf("%0*d", Digits, value%mod)
}
