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
		// 审计写入失败不应影响主流程
		_ = g.Create(&entry).Error
	}
}
