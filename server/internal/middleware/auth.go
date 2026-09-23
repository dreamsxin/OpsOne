package middleware

import (
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"gorm.io/gorm"

	"ops-platform/server/internal/model"
	"ops-platform/server/internal/response"
)

const (
	ctxUserKey  = "ctx_user"
	ctxPermsKey = "ctx_perms"
)

// Claims JWT 载荷
type Claims struct {
	UserID   uint   `json:"uid"`
	Username string `json:"username"`
	jwt.RegisteredClaims
}

// IssueToken 签发访问令牌
func IssueToken(secret []byte, u *model.User, ttl time.Duration) (string, time.Time, error) {
	expire := time.Now().Add(ttl)
	claims := Claims{
		UserID:   u.ID,
		Username: u.Username,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(expire),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			Subject:   u.Username,
		},
	}
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(secret)
	return token, expire, err
}

// Auth 校验令牌并把用户与权限集合写入上下文。
//
// 两种凭据：
//   - 人工登录的 JWT；WebSocket 无法自定义请求头，因此额外允许 access_token 查询参数。
//   - API 令牌（opst_ 前缀，见 apitoken.go）；**只认请求头**，不认查询参数。
func Auth(secret []byte, g *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		header := strings.TrimSpace(strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer "))
		if strings.HasPrefix(header, ApiTokenPrefix) {
			if !authByAPIToken(c, g, header) {
				return
			}
			c.Next()
			return
		}

		raw := header
		if raw == "" {
			raw = c.Query("access_token")
		}
		if raw == "" {
			response.Unauthorized(c, "缺少访问令牌")
			return
		}
		// 别让 API 令牌从查询参数里混进来：那条路是给 WebSocket 的
		if strings.HasPrefix(raw, ApiTokenPrefix) {
			response.Unauthorized(c, "API 令牌只能放在 Authorization 请求头里")
			return
		}

		claims := &Claims{}
		token, err := jwt.ParseWithClaims(raw, claims, func(t *jwt.Token) (any, error) {
			return secret, nil
		}, jwt.WithValidMethods([]string{"HS256"}))
		if err != nil || !token.Valid {
			response.Unauthorized(c, "令牌无效或已过期")
			return
		}

		var user model.User
		if err := g.Preload("Roles").First(&user, claims.UserID).Error; err != nil {
			response.Unauthorized(c, "用户不存在")
			return
		}
		if user.Status != 1 {
			response.Forbidden(c, "账号已被禁用")
			return
		}

		perms, err := loadPerms(g, &user)
		if err != nil {
			response.Error(c, "加载权限失败")
			return
		}

		c.Set(ctxUserKey, &user)
		c.Set(ctxPermsKey, perms)
		c.Next()
	}
}

func loadPerms(g *gorm.DB, u *model.User) (map[string]struct{}, error) {
	roleIDs := make([]uint, 0, len(u.Roles))
	for _, r := range u.Roles {
		roleIDs = append(roleIDs, r.ID)
	}
	perms := map[string]struct{}{}
	if len(roleIDs) == 0 {
		return perms, nil
	}
	var codes []string
	err := g.Model(&model.Menu{}).
		Joins("JOIN role_menus ON role_menus.menu_id = menus.id").
		Where("role_menus.role_id IN ?", roleIDs).
		Where("menus.auth_code <> ''").
		Distinct().Pluck("menus.auth_code", &codes).Error
	if err != nil {
		return nil, err
	}
	for _, code := range codes {
		perms[code] = struct{}{}
	}
	return perms, nil
}

// RequirePerm 校验按钮级权限
func RequirePerm(code string) gin.HandlerFunc {
	return func(c *gin.Context) {
		perms := Perms(c)
		if _, ok := perms[code]; !ok {
			response.Forbidden(c, "无权执行该操作: "+code)
			return
		}
		c.Next()
	}
}

// RequireAnyPerm 命中任意一个码即可放行。
//
// 给「一个接口被多个角色的正当流程共用」的情况用，例如菜单树既是菜单管理页要读的，
// 也是角色配菜单的弹窗要读的 —— 只认一个码会让只有 role:manage 的人配不了角色。
// 不要拿它当放宽手段：列出来的每个码都应该是「持有它的人本来就该看到这些数据」。
func RequireAnyPerm(codes ...string) gin.HandlerFunc {
	return func(c *gin.Context) {
		perms := Perms(c)
		for _, code := range codes {
			if _, ok := perms[code]; ok {
				c.Next()
				return
			}
		}
		response.Forbidden(c, "无权执行该操作，需要其中之一: "+strings.Join(codes, " / "))
	}
}

// CurrentUser 取当前登录用户
func CurrentUser(c *gin.Context) *model.User {
	if v, ok := c.Get(ctxUserKey); ok {
		if u, ok := v.(*model.User); ok {
			return u
		}
	}
	return nil
}

// Perms 取当前用户权限集合
func Perms(c *gin.Context) map[string]struct{} {
	if v, ok := c.Get(ctxPermsKey); ok {
		if p, ok := v.(map[string]struct{}); ok {
			return p
		}
	}
	return map[string]struct{}{}
}
