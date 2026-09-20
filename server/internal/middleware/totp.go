package middleware

import (
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"ops-platform/server/internal/model"
	"ops-platform/server/internal/response"
)

// totpModeKey 与 handler.TOTPModeKey 同一个配置项
const totpModeKey = "security.totp.mode"

// totpAllowedPrefix 强制模式下未绑定账号仍可访问的路径前缀。
// 放开整个 /me 是有意的：用户只能看自己的资料/菜单并完成绑定，碰不到任何业务数据。
const totpAllowedPrefix = "/api/v1/me"

// RequireTOTP 强制双因子。
//
// 这是真正的拦截：配置为 required 且当前账号未绑定时，除 /me 系接口外一律 403。
// 前端的引导跳转只是体验，绕过前端直接调接口同样过不去。
func RequireTOTP(g *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		if strings.HasPrefix(c.Request.URL.Path, totpAllowedPrefix) {
			c.Next()
			return
		}

		var cfg model.SysConfig
		if err := g.Where("`key` = ?", totpModeKey).First(&cfg).Error; err != nil {
			c.Next()
			return
		}
		if strings.TrimSpace(cfg.Value) != "required" {
			c.Next()
			return
		}

		user := CurrentUser(c)
		if user == nil || user.TOTPEnabled {
			c.Next()
			return
		}
		response.Forbidden(c, "平台已强制启用双因子口令，请先在「安全合规 → 双因子口令」完成绑定")
	}
}
