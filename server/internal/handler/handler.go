package handler

import (
	"strconv"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"ops-platform/server/internal/config"
	"ops-platform/server/internal/scheduler"
)

// Handler 持有所有处理器共享的依赖
type Handler struct {
	DB  *gorm.DB
	Cfg *config.Config
	// Sched 定时任务调度器，由 main 在构造后注入（调度回调需要 Handler 自身）
	Sched *scheduler.Scheduler
}

func New(g *gorm.DB, cfg *config.Config) *Handler {
	return &Handler{DB: g, Cfg: cfg}
}

func pageParams(c *gin.Context) (page, size int) {
	page, _ = strconv.Atoi(c.DefaultQuery("page", "1"))
	size, _ = strconv.Atoi(c.DefaultQuery("pageSize", "20"))
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 200 {
		size = 20
	}
	return
}

func idParam(c *gin.Context) uint {
	n, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	return uint(n)
}
