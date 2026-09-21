// Package cryptox 做凭据字段的对称加密。
//
// 只解决一件事：凭证库里的口令 / 私钥不要以明文躺在 SQLite 文件里。
// 刻意不做的事：不做密钥轮换、不做 KMS/Vault 接入、不加密历史 hosts.secret —— 那些是另外的工程，
// 这里如果假装做了，反而会让人以为凭据已经被妥善保管。
//
// 密钥来自 OPS_SECRET_KEY，**没配就是明文存**，并且要在界面上明说，
// 绝不能让人以为「有凭证库 = 加密了」。
package cryptox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"
)

// prefix 密文前缀。留着它是为了能区分「这条是密文」与「这条是历史明文」：
// 没有前缀的一律按明文原样返回，否则换个版本上线会把存量数据全读成乱码。
const prefix = "enc:v1:"

// ErrNoKey 库里是密文但进程没有密钥 —— 这种情况必须报错，不能返回空串去连主机，
// 那会表现成「口令错误」，让人去查错误的方向。
var ErrNoKey = errors.New("库中凭据是密文，但未配置 OPS_SECRET_KEY，无法解密")

// Box 一把钥匙。Key 为空时退化成「明文直存」，Enabled 返回 false。
type Box struct {
	aead cipher.AEAD
}

// New 从任意长度的口令派生 256 位密钥（SHA-256）。secret 为空时返回明文模式的 Box。
//
// 用 SHA-256 而不是 argon2/scrypt：这把钥匙来自部署时写的环境变量，不是用户口令，
// 威胁模型里没有「在线爆破」，加 KDF 只增加依赖不增加安全性。
func New(secret string) *Box {
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return &Box{}
	}
	sum := sha256.Sum256([]byte(secret))
	block, err := aes.NewCipher(sum[:])
	if err != nil {
		// 32 字节的 key 不可能让 NewCipher 失败，真失败了说明标准库变了，不该静默降级成明文
		panic(fmt.Sprintf("cryptox: 初始化 AES 失败: %v", err))
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		panic(fmt.Sprintf("cryptox: 初始化 GCM 失败: %v", err))
	}
	return &Box{aead: aead}
}

// Enabled 是否真的在加密。false 表示明文存储，界面与文档都要如实说明。
func (b *Box) Enabled() bool { return b != nil && b.aead != nil }

// Seal 加密。明文模式下原样返回（不加前缀），空串一律原样返回 ——
// 空的凭据字段在业务上表示「没有这一项」，加密一个空串没有意义还会让判空逻辑失效。
func (b *Box) Seal(plain string) string {
	if plain == "" || !b.Enabled() {
		return plain
	}
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		// 熵不够时宁可 panic 也不能退回明文：那会让一次偶发故障变成永久的明文落库
		panic(fmt.Sprintf("cryptox: 生成 nonce 失败: %v", err))
	}
	sealed := b.aead.Seal(nil, nonce, []byte(plain), nil)
	return prefix + base64.StdEncoding.EncodeToString(append(nonce, sealed...))
}

// Open 解密。没有前缀的当历史明文原样返回；有前缀但解不开一律报错，
// 不返回半个结果 —— 密钥换了 / 数据被改了，都必须看得见。
func (b *Box) Open(stored string) (string, error) {
	if !strings.HasPrefix(stored, prefix) {
		return stored, nil
	}
	if !b.Enabled() {
		return "", ErrNoKey
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(stored, prefix))
	if err != nil {
		return "", fmt.Errorf("密文格式损坏: %w", err)
	}
	ns := b.aead.NonceSize()
	if len(raw) <= ns {
		return "", errors.New("密文长度不足")
	}
	plain, err := b.aead.Open(nil, raw[:ns], raw[ns:], nil)
	if err != nil {
		return "", errors.New("解密失败：OPS_SECRET_KEY 与加密时不一致，或数据已被篡改")
	}
	return string(plain), nil
}

// IsSealed 这条存储值是不是密文。用于界面上区分「加密存的」与「历史明文」。
func IsSealed(stored string) bool { return strings.HasPrefix(stored, prefix) }
