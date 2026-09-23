package handler

import (
	"strconv"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"ops-platform/server/internal/config"
	"ops-platform/server/internal/cryptox"
	"ops-platform/server/internal/instance"
	"ops-platform/server/internal/metrics"
	"ops-platform/server/internal/scheduler"
)

// Handler 持有所有处理器共享的依赖
type Handler struct {
	DB  *gorm.DB
	Cfg *config.Config
	// Sched 定时任务调度器，由 main 在构造后注入（调度回调需要 Handler 自身）
	Sched *scheduler.Scheduler

	// Instance 本进程在实例表里的那一行，由 main 注入。
	// 单元测试里是 nil —— 平台健康页要按 nil 处理，不能因此 panic
	Instance *instance.Manager

	// StartedAt 进程启动时间，平台健康页展示运行时长
	StartedAt time.Time

	// 内置固定任务（证书巡检、规则评估）的最近运行时间。
	// 这类任务不落库，只记在内存里，重启后归零 —— 平台健康页会照实说明这一点。
	fixedRunMu   sync.RWMutex
	fixedRunAt   map[string]time.Time
	fixedRunInfo map[string]string

	// forwards 运行中的集群转发隧道，只存在于进程内，重启即消失
	forwards *forwardRegistry

	// terminals 进程内活跃的 Web 终端连接，停机时要主动通知并关闭
	terminals *terminalRegistry

	// imLogins 扫码登录的 state 与一次性 ticket，同样只在进程内
	imLogins *imLoginStore

	// Crypto 凭证库的字段加解密。OPS_SECRET_KEY 为空时它是明文模式（Enabled() == false），
	// 页面上会如实标注，不做「看起来加密了」的暗示
	Crypto *cryptox.Box
}

func New(g *gorm.DB, cfg *config.Config) *Handler {
	// 单元测试里会传 nil cfg（只测某个查询），这里不能因此崩掉
	secretKey := ""
	if cfg != nil {
		secretKey = cfg.SecretKey
	}
	return &Handler{
		DB: g, Cfg: cfg, StartedAt: time.Now(),
		fixedRunAt:   map[string]time.Time{},
		fixedRunInfo: map[string]string{},
		forwards:     newForwardRegistry(),
		terminals:    newTerminalRegistry(),
		imLogins:     newImLoginStore(),
		Crypto:       cryptox.New(secretKey),
	}
}

// markFixedRun 记录一次内置定时任务的运行结果
func (h *Handler) markFixedRun(key, info string) {
	h.fixedRunMu.Lock()
	h.fixedRunAt[key] = time.Now()
	h.fixedRunInfo[key] = info
	h.fixedRunMu.Unlock()
	// 这里是所有内置任务的唯一汇合点，指标打在这儿就漏不掉
	metrics.MarkFixedTask(metrics.Default(), key)
}

// fixedRunSnapshot 已经跑过的内置任务及其最近运行时间，给指标用。
// 只含**本进程启动后**跑过的 —— 这些任务不落库，重启后就归零，与健康页同一个口径
func (h *Handler) fixedRunSnapshot() map[string]time.Time {
	h.fixedRunMu.RLock()
	defer h.fixedRunMu.RUnlock()
	out := make(map[string]time.Time, len(h.fixedRunAt))
	for key, at := range h.fixedRunAt {
		out[key] = at
	}
	return out
}

func (h *Handler) lastFixedRun(key string) (time.Time, string) {
	h.fixedRunMu.RLock()
	defer h.fixedRunMu.RUnlock()
	return h.fixedRunAt[key], h.fixedRunInfo[key]
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
