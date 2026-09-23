package handler

// /metrics 与 pprof。
//
// # 默认关闭，靠令牌开
//
// 两个端点都**默认不注册**（`OPS_METRICS_TOKEN` 为空时连路由都不存在，
// 访问是 404 而不是 401 —— 不告诉外面「这里有个被保护的端点」）。
// 理由不是洁癖：
//
//   - `/metrics` 会暴露路由清单、请求量、告警积压、实例主机名与版本号。
//     单独看都不敏感，合起来是一份不错的侦察材料。
//   - **pprof 更严重**：heap profile 是进程内存的快照，而这个进程内存里有
//     SSH 私钥、主机口令、解密后的凭据。一个能拉 heap 的人等于能拿走这些。
//     所以 pprof 需要**同时**设 `OPS_PPROF=true` 与令牌，且文档里写明只在排查时开。
//
// 令牌比对用固定时间比较（`subtle.ConstantTimeCompare`）：这里可以被外部反复试，
// 用 `==` 会泄露前缀信息。

import (
	"crypto/subtle"
	"net/http"
	"net/http/pprof"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"ops-platform/server/internal/instance"
	"ops-platform/server/internal/metrics"
	"ops-platform/server/internal/model"
)

// InstallMetrics 登记指标族与「抓取时现算」的 gauge。
//
// 这些 gauge 每次抓取都会执行，所以只放**便宜**的东西：几个走索引的 COUNT、
// 一次 os.Stat、几个内存里的计数。抓取间隔通常 15 秒，放一条全表扫描
// 等于给自己加了个定时压测。
func (h *Handler) InstallMetrics(reg *metrics.Registry, version string) {
	metrics.Install(reg)

	reg.RegisterGauge("opsone_up", "进程是否在服务（恒为 1，用来做 absent 告警）",
		func() []metrics.Sample { return metrics.One(1) })
	reg.RegisterGauge("opsone_build_info", "构建版本（值恒为 1，版本在标签里）",
		func() []metrics.Sample {
			return metrics.Labeled(map[string]string{"version": version}, 1)
		})
	reg.RegisterGauge("opsone_uptime_seconds", "进程运行时长（秒）",
		func() []metrics.Sample { return metrics.One(time.Since(h.StartedAt).Seconds()) })

	reg.RegisterGauge("opsone_goroutines", "协程数（每个 Web 终端会话常驻协程，泄漏时这里会涨）",
		func() []metrics.Sample { return metrics.One(float64(runtime.NumGoroutine())) })
	reg.RegisterGauge("opsone_memory_bytes", "内存用量，kind=alloc 已分配 / sys 向系统申请",
		func() []metrics.Sample {
			var mem runtime.MemStats
			runtime.ReadMemStats(&mem)
			return []metrics.Sample{
				{Labels: map[string]string{"kind": "alloc"}, Value: float64(mem.Alloc)},
				{Labels: map[string]string{"kind": "sys"}, Value: float64(mem.Sys)},
			}
		})

	reg.RegisterGauge("opsone_db_size_bytes", "SQLite 库文件大小（取不到则整族不输出）",
		func() []metrics.Sample {
			if h.Cfg == nil || h.Cfg.DSN == "" {
				return nil
			}
			info, err := os.Stat(h.Cfg.DSN)
			if err != nil {
				// 返回 nil 让这一族整个不输出。写 0 出去会被读成「库是空的」
				return nil
			}
			return metrics.One(float64(info.Size()))
		})

	reg.RegisterGauge(metrics.FixedTaskLastRun,
		"每个内置固定任务最近一次运行的 Unix 时间戳（只含本进程启动后跑过的；它们不落库）",
		func() []metrics.Sample {
			snapshot := h.fixedRunSnapshot()
			if len(snapshot) == 0 {
				return nil
			}
			out := make([]metrics.Sample, 0, len(snapshot))
			for task, at := range snapshot {
				out = append(out, metrics.Sample{
					Labels: map[string]string{"task": task},
					Value:  float64(at.Unix()),
				})
			}
			return out
		})

	reg.RegisterGauge("opsone_scheduler_entries",
		"调度器里的条目数，kind=user 用户定时任务 / fixed 内置固定任务",
		func() []metrics.Sample {
			if h.Sched == nil {
				return nil
			}
			user, fixed, _ := h.Sched.Stats()
			return []metrics.Sample{
				{Labels: map[string]string{"kind": "user"}, Value: float64(user)},
				{Labels: map[string]string{"kind": "fixed"}, Value: float64(fixed)},
			}
		})

	// 调度归属：standby 上这个值是 0。「所有实例的 leader 都是 0」是一种
	// 需要立刻知道的故障（没人在跑定时任务），而它在日志里是看不出来的
	reg.RegisterGauge("opsone_instance_leader",
		"本实例是否在跑调度（1 leader / 0 standby）",
		func() []metrics.Sample {
			if h.Instance == nil {
				return nil
			}
			return metrics.Labeled(
				map[string]string{"instance": h.Instance.ID()[:8]},
				metrics.BoolValue(h.Instance.Role() == instance.RoleLeader))
		})
	reg.RegisterGauge("opsone_instances", "库上心跳新鲜的实例数（>1 说明有人双开了）",
		func() []metrics.Sample {
			if h.Instance == nil || h.DB == nil {
				return nil
			}
			lease := instance.DefaultLeaseSeconds
			if h.Cfg != nil && h.Cfg.InstanceLeaseSec > 0 {
				lease = h.Cfg.InstanceLeaseSec
			}
			rows, err := instance.Alive(h.DB, lease)
			if err != nil {
				return nil
			}
			return metrics.One(float64(len(rows)))
		})

	reg.RegisterGauge("opsone_alerts", "告警条数，status=firing 未恢复 / acked 已确认",
		func() []metrics.Sample {
			if h.DB == nil {
				return nil
			}
			out := make([]metrics.Sample, 0, 2)
			for _, status := range []string{"firing", "acked"} {
				var n int64
				if err := h.DB.Model(&model.Alert{}).Where("status = ?", status).
					Count(&n).Error; err != nil {
					return nil
				}
				out = append(out, metrics.Sample{
					Labels: map[string]string{"status": status}, Value: float64(n),
				})
			}
			return out
		})
	reg.RegisterGauge("opsone_active_sessions", "库里标记为进行中的终端会话数",
		func() []metrics.Sample {
			if h.DB == nil {
				return nil
			}
			var n int64
			if err := h.DB.Model(&model.Session{}).Where("status = ?", "active").
				Count(&n).Error; err != nil {
				return nil
			}
			return metrics.One(float64(n))
		})
	reg.RegisterGauge("opsone_forward_tunnels", "本进程内活着的集群转发隧道数",
		func() []metrics.Sample {
			if h.forwards == nil {
				return nil
			}
			return metrics.One(float64(h.forwards.count()))
		})
}

// MetricsHandler 返回 /metrics 的处理函数。token 为空时调用方不该注册路由。
func MetricsHandler(reg *metrics.Registry, token string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !metricsTokenOK(c, token) {
			c.String(http.StatusUnauthorized, "unauthorized")
			return
		}
		var b strings.Builder
		reg.Write(&b)
		c.Header("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		c.String(http.StatusOK, b.String())
	}
}

// PprofHandler 把 net/http/pprof 的处理器挂到 gin 上。
//
// 只认 /debug/pprof/ 下面那几个固定名字，不做通配转发 —— pprof 的 handler
// 是按路径名分发的，通配会把 `cmdline`（进程启动参数，可能带令牌）也放出去。
func PprofHandler(token string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !metricsTokenOK(c, token) {
			c.String(http.StatusUnauthorized, "unauthorized")
			return
		}
		switch c.Param("profile") {
		case "profile":
			pprof.Profile(c.Writer, c.Request)
		case "heap", "allocs", "goroutine", "block", "mutex", "threadcreate":
			pprof.Handler(c.Param("profile")).ServeHTTP(c.Writer, c.Request)
		case "trace":
			pprof.Trace(c.Writer, c.Request)
		default:
			c.String(http.StatusNotFound, "支持的 profile: profile / heap / allocs / goroutine / block / mutex / threadcreate / trace")
		}
	}
}

// metricsTokenOK 令牌校验。支持 `Authorization: Bearer <token>`（Prometheus 的
// bearer_token 配置项直接可用）与 `?token=`（curl 手动看一眼时方便）。
func metricsTokenOK(c *gin.Context, token string) bool {
	if token == "" {
		return false
	}
	given := strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer ")
	given = strings.TrimSpace(given)
	if given == "" {
		given = c.Query("token")
	}
	return subtle.ConstantTimeCompare([]byte(given), []byte(token)) == 1
}
