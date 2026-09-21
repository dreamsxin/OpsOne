package middleware

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"net"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"ops-platform/server/internal/model"
	"ops-platform/server/internal/response"
)

// API 令牌认证：给 CI、脚本、外部系统调平台接口用，不用拿人的账号口令去登录。
//
// 判定顺序与几条硬规则（同样写在 docs/SECURITY.md 第 30 节）：
//  1. 库里只有 SHA-256 哈希，明文只在创建/轮换时返回一次。平台自己也查不回来。
//  2. 令牌权限 = 显式授予的权限码 ∩ 归属人当下的权限。归属人被降权或停用，
//     令牌立刻跟着失效——否则「离职后令牌还能干活」。
//  3. 默认只读（只放 GET/HEAD）。要写必须显式关掉只读**并且**授予对应权限码。
//  4. **不接受 access_token 查询参数**，因此天然用不了 Web 终端那条 WebSocket 链路；
//     另外显式拒绝 Upgrade: websocket。终端必须是人在操作，会话审计才有意义。
const (
	// ApiTokenPrefix 令牌明文前缀，便于在日志/代码里一眼认出这是平台令牌
	ApiTokenPrefix = "opst_"
	// apiTokenPrefixLen 存库展示的前缀长度（含 opst_）
	apiTokenPrefixLen = 12

	ctxTokenKey = "ctx_api_token"
)

// HashAPIToken 令牌明文 → 库里存的哈希
func HashAPIToken(plain string) string {
	sum := sha256.Sum256([]byte(plain))
	return hex.EncodeToString(sum[:])
}

// APITokenPrefixOf 取展示用前缀
func APITokenPrefixOf(plain string) string {
	if len(plain) <= apiTokenPrefixLen {
		return plain
	}
	return plain[:apiTokenPrefixLen]
}

// ScopeList 解析令牌的权限码列表
func ScopeList(raw string) []string {
	list := []string{}
	if strings.TrimSpace(raw) == "" {
		return list
	}
	if err := json.Unmarshal([]byte(raw), &list); err != nil {
		return []string{}
	}
	return list
}

// IPAllowed 判断来源 IP 是否在白名单里。空白名单表示不限制。
//
// 条目支持单个 IP 与 CIDR；写错的条目按「不匹配」处理，不会意外放开。
func IPAllowed(allow string, ip string) bool {
	if strings.TrimSpace(allow) == "" {
		return true
	}
	addr := net.ParseIP(strings.TrimSpace(ip))
	if addr == nil {
		return false
	}
	for _, item := range strings.Split(allow, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if strings.Contains(item, "/") {
			if _, cidr, err := net.ParseCIDR(item); err == nil && cidr.Contains(addr) {
				return true
			}
			continue
		}
		if one := net.ParseIP(item); one != nil && one.Equal(addr) {
			return true
		}
	}
	return false
}

// IntersectScopes 令牌授予的权限码与归属人实际权限的交集
func IntersectScopes(scopes []string, owner map[string]struct{}) map[string]struct{} {
	perms := map[string]struct{}{}
	for _, code := range scopes {
		code = strings.TrimSpace(code)
		if code == "" {
			continue
		}
		if _, ok := owner[code]; ok {
			perms[code] = struct{}{}
		}
	}
	return perms
}

// CurrentAPIToken 取本次请求使用的令牌，人工登录时返回 nil
func CurrentAPIToken(c *gin.Context) *model.ApiToken {
	if v, ok := c.Get(ctxTokenKey); ok {
		if t, ok := v.(*model.ApiToken); ok {
			return t
		}
	}
	return nil
}

// authByAPIToken 用 API 令牌完成认证。返回 false 表示已经给出了错误响应。
func authByAPIToken(c *gin.Context, g *gorm.DB, raw string) bool {
	// WebSocket 一律不给令牌用：终端/端口转发必须是人在操作
	if strings.EqualFold(c.GetHeader("Upgrade"), "websocket") {
		response.Forbidden(c, "API 令牌不能用于 WebSocket 链路（终端、端口转发请用人工登录）")
		return false
	}

	hash := HashAPIToken(raw)
	var token model.ApiToken
	// 先按前缀缩小范围，再做常量时间比较，避免把哈希直接丢进 SQL 比较
	if err := g.Where("prefix = ?", APITokenPrefixOf(raw)).First(&token).Error; err != nil {
		response.Unauthorized(c, "令牌无效或已过期")
		return false
	}
	if subtle.ConstantTimeCompare([]byte(token.TokenHash), []byte(hash)) != 1 {
		response.Unauthorized(c, "令牌无效或已过期")
		return false
	}
	if !token.Enabled || token.RevokedAt != nil {
		response.Unauthorized(c, "令牌已停用或已撤销")
		return false
	}
	if token.ExpiresAt != nil && time.Now().After(*token.ExpiresAt) {
		response.Unauthorized(c, "令牌已过期")
		return false
	}
	if !IPAllowed(token.AllowIPs, c.ClientIP()) {
		response.Forbidden(c, "来源 IP 不在该令牌的白名单内")
		return false
	}
	if token.ReadOnly && c.Request.Method != "GET" && c.Request.Method != "HEAD" {
		response.Forbidden(c, "该令牌是只读令牌，不能执行写操作")
		return false
	}

	var owner model.User
	if err := g.Preload("Roles").First(&owner, token.OwnerUserID).Error; err != nil {
		response.Unauthorized(c, "令牌归属账号不存在")
		return false
	}
	if owner.Status != 1 {
		response.Forbidden(c, "令牌归属账号已被禁用")
		return false
	}

	ownerPerms, err := loadPerms(g, &owner)
	if err != nil {
		response.Error(c, "加载权限失败")
		return false
	}

	now := time.Now()
	g.Model(&model.ApiToken{ID: token.ID}).Updates(map[string]any{
		"last_used_at": &now, "last_used_ip": c.ClientIP(),
		"use_count": gorm.Expr("use_count + 1"),
	})

	c.Set(ctxUserKey, &owner)
	// 权限取交集：令牌不可能比归属人权限更大
	c.Set(ctxPermsKey, IntersectScopes(ScopeList(token.Scopes), ownerPerms))
	c.Set(ctxTokenKey, &token)
	return true
}
