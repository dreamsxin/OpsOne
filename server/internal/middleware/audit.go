package middleware

import (
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"ops-platform/server/internal/model"
)

// Audit 记录写操作审计日志，读请求不落库以免噪声过多。
func Audit(g *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
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
