package middleware

// 写操作审计。
//
// # 这一轮补了什么
//
// 审计原来只记元数据（谁、什么时候、打了哪个路由、结果如何），**不记请求体**。
// 于是「谁把角色 3 的数据范围从 dept 改成了 all」这种问题答不出来 ——
// 只剩一行 `PUT /system/roles/3`。对一个要追责的运维平台来说，
// 「有人改过」和「改成了什么」差别很大。
//
// 现在记请求体，但有三条硬约束：
//
//  1. **脱敏在落库之前做**。请求体里有口令、私钥、AK/SK、令牌 ——
//     把它们抄进审计表等于又造了一处明文存储，而审计表的查询权限比密码库宽。
//  2. **按字段名脱敏，不按值猜**。命中的键值一律替换成固定占位串，
//     而且**保留键名**：知道「这次改了 password 字段」本身就是有用的信息。
//  3. **有大小上限**。配置文件正文、脚本内容可以是几百 KB，
//     审计表不是内容仓库（内容本来就有 ConfigVersion 在存）。超限截断并标注。

import (
	"bytes"
	"encoding/json"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"ops-platform/server/internal/model"
)

const (
	// auditBodyLimit 落库的请求体上限。超出的部分截断，并在末尾标注。
	auditBodyLimit = 4 * 1024
	// auditReadLimit 读请求体的上限。超过这个尺寸就不解析了 ——
	// 配置文件下发的 body 可以是几百 KB，为了审计把它整份读进内存不值得
	auditReadLimit = 256 * 1024
	// auditRedacted 脱敏后的占位串。固定文案，便于在审计里 grep 出「这次动了密钥」
	auditRedacted = "***已脱敏***"
	// ctxAuditDetailKey handler 可以往这里写一句人话，解释这次操作动了什么。
	// 给「请求体看不出来」的场景用，典型是 DELETE —— body 是空的，
	// 而 path 里那个 ID 在删完之后就查不到对应的东西了
	ctxAuditDetailKey = "ctx_audit_detail"
)

// sensitiveBodyKeys 需要脱敏的字段名（小写比较，子串匹配）。
//
// 用子串而不是精确匹配：实际字段名有 password / newPassword / accessKeySecret /
// privateKey / clientSecret / webhookToken 这么多花样，精确清单必漏。
// 代价是可能误脱敏一个叫 tokenName 的无害字段 —— 那个方向的错误是安全的。
var sensitiveBodyKeys = []string{
	"password", "passwd", "secret", "token", "privatekey", "private_key",
	"passphrase", "credential", "apikey", "api_key", "accesskey", "access_key",
	"authorization", "totp", "otp", "pin", "keyfile", "cert",
}

// SetAuditDetail 让 handler 给这次操作补一句说明。
//
// 只在「请求体答不出问题」时用，主要是删除：删授权、删角色、删主机这些操作，
// 光看 `DELETE /resource-grants/7` 事后什么都查不到。
func SetAuditDetail(c *gin.Context, detail string) {
	c.Set(ctxAuditDetailKey, detail)
}

// Audit 记录写操作审计日志，读请求不落库以免噪声过多。
func Audit(g *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()

		// 请求体只能读一次，读完要塞回去给 handler 用
		var body []byte
		if c.Request.Method != "GET" && c.Request.Body != nil {
			body, _ = io.ReadAll(io.LimitReader(c.Request.Body, auditReadLimit))
			c.Request.Body = io.NopCloser(bytes.NewReader(body))
		}

		c.Next()

		if c.Request.Method == "GET" {
			return
		}
		user := CurrentUser(c)
		entry := model.AuditLog{
			Method: c.Request.Method,
			Path:   c.Request.URL.Path,
			Action: c.FullPath(),
			Status: c.Writer.Status(),
			IP:     c.ClientIP(),
			CostMs: time.Since(start).Milliseconds(),
			Body:   redactBody(c.ContentType(), body),
		}
		if v, ok := c.Get(ctxAuditDetailKey); ok {
			if text, ok := v.(string); ok {
				entry.Detail = truncateRunes(text, 500)
			}
		}
		if user != nil {
			entry.UserID = user.ID
			entry.Username = user.Username
		}
		// 令牌调用要能区分出来：归属人仍记在 UserID 上，另外标出是哪个令牌
		if token := CurrentAPIToken(c); token != nil {
			entry.TokenID = token.ID
			entry.TokenName = token.Name
		}
		// 审计写入失败不应影响主流程
		_ = g.Create(&entry).Error
	}
}

// redactBody 脱敏后的请求体。
//
// 只处理 JSON。其它类型（表单上传、二进制）只记一句类型说明 ——
// 把上传的文件内容抄进审计表没有意义，而且那是 FileAudit 的活。
func redactBody(contentType string, body []byte) string {
	if len(body) == 0 {
		return ""
	}
	if !strings.Contains(strings.ToLower(contentType), "json") {
		return "（非 JSON 请求体，未记录内容；文件上传另见文件操作审计）"
	}

	var parsed any
	if err := json.Unmarshal(body, &parsed); err != nil {
		// 解析不了就不记：原始文本里可能带着口令，而我们无法按字段脱敏
		return "（请求体不是合法 JSON，未记录内容）"
	}
	redactValue(parsed)
	out, err := json.Marshal(parsed)
	if err != nil {
		return "（脱敏后序列化失败，未记录内容）"
	}
	return truncateRunes(string(out), auditBodyLimit)
}

// redactValue 递归脱敏。就地改，所以传进来的必须是 json.Unmarshal 出来的结构
func redactValue(node any) {
	switch v := node.(type) {
	case map[string]any:
		keys := make([]string, 0, len(v))
		for key := range v {
			keys = append(keys, key)
		}
		sort.Strings(keys) // 顺序稳定，便于 diff 两条审计
		for _, key := range keys {
			if isSensitiveKey(key) {
				if v[key] != nil && v[key] != "" {
					v[key] = auditRedacted
				}
				continue
			}
			redactValue(v[key])
		}
	case []any:
		for _, item := range v {
			redactValue(item)
		}
	}
}

func isSensitiveKey(key string) bool {
	lower := strings.ToLower(key)
	for _, needle := range sensitiveBodyKeys {
		if strings.Contains(lower, needle) {
			return true
		}
	}
	return false
}

// truncateRunes 按字符截断，不按字节 —— 按字节切会把中文切成半个字，
// 落库后整条审计的 JSON 就解析不了了
func truncateRunes(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + "…（已截断）"
}
