package metrics

// 平台的指标定义与 HTTP 中间件。
//
// 指标名字统一 `opsone_` 前缀；单位按 Prometheus 约定放在名字里（`_seconds`、`_bytes`、`_total`）。

import (
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
)

const (
	HTTPRequestsTotal   = "opsone_http_requests_total"
	HTTPRequestDuration = "opsone_http_request_duration_seconds"
	// FixedTaskRunsTotal 内置固定任务（证书巡检、指标采集这类）跑了几次。
	// 这是「定时任务到底有没有在跑」唯一可靠的外部观测点 —— 它们不落库。
	//
	// **刻意不带 result 标签**：这些任务没有结构化的成败，只有一句给人看的描述
	// （"共 3 个集群，健康 3 个" / "失败: ..."）。按文本前缀猜出一个 ok/failed
	// 会造出一个看起来精确、实则不准的指标。要判断「有没有在跑」用下面那个时间戳。
	FixedTaskRunsTotal = "opsone_fixed_task_runs_total"
	// FixedTaskLastRun 每个内置任务最近一次运行的 Unix 时间戳。
	// 真正该拿来告警的是它：`time() - opsone_fixed_task_last_run_timestamp_seconds > 21600`
	// 比「失败次数 > 0」有用得多 —— 任务悄悄不跑了是更常见也更危险的故障。
	FixedTaskLastRun = "opsone_fixed_task_last_run_timestamp_seconds"
	// CronTriggersTotal 用户定时任务的触发计数，按结果分（这个是结构化的）
	CronTriggersTotal = "opsone_cron_triggers_total"
)

// durationBuckets 接口耗时的桶。
//
// 上界给到 30 秒是因为平台里有几个**同步的长请求**：批量执行、真机防火墙对账、
// 磁盘占用分析。按默认的 10 秒收尾会让这些请求全挤在 +Inf 里，
// 那个直方图就回答不了「是慢了还是挂了」。
var durationBuckets = []float64{0.005, 0.025, 0.1, 0.5, 1, 2.5, 5, 10, 30}

// Install 登记全部指标族。可重复调用（测试里每个用例都会构造一次路由）。
func Install(r *Registry) {
	r.RegisterCounter(HTTPRequestsTotal, "HTTP 请求数，按路由模板/方法/状态码族分")
	r.RegisterHistogram(HTTPRequestDuration, "HTTP 请求耗时（秒），按路由模板分", durationBuckets)
	r.RegisterCounter(FixedTaskRunsTotal, "内置固定任务的运行次数，按任务分")
	r.RegisterCounter(CronTriggersTotal, "用户定时任务的触发次数，按结果分")
}

// Middleware 记录每个请求。
//
// **按路由模板而不是实际路径打标签**：`/api/v1/hosts/:id` 而不是 `/api/v1/hosts/17`。
// 用实际路径的话，每台主机、每条告警都会生成一组新时序，
// 几天下来就能把抓取端的内存吃掉 —— 这是 Prometheus 最常见的一种自伤。
// 没匹配到路由的请求（404、扫描器）统一记成 `unmatched`，同理。
func Middleware(r *Registry) gin.HandlerFunc {
	return func(c *gin.Context) {
		started := time.Now()
		c.Next()

		route := c.FullPath()
		if route == "" {
			route = "unmatched"
		}
		elapsed := time.Since(started).Seconds()
		status := c.Writer.Status()
		r.AddCounter(HTTPRequestsTotal, map[string]string{
			"route":  route,
			"method": c.Request.Method,
			// 只记状态码族：2xx/4xx/5xx 足够回答「在报错吗」，
			// 而完整状态码会让时序数乘以一个常数却几乎不增加信息
			"status": statusClass(status),
		}, 1)
		r.Observe(HTTPRequestDuration, map[string]string{"route": route}, elapsed)
	}
}

func statusClass(status int) string {
	switch {
	case status >= 500:
		return "5xx"
	case status >= 400:
		return "4xx"
	case status >= 300:
		return "3xx"
	default:
		return "2xx"
	}
}

// MarkFixedTask 记一次内置固定任务的运行
func MarkFixedTask(r *Registry, task string) {
	r.AddCounter(FixedTaskRunsTotal, map[string]string{"task": task}, 1)
}

// MarkCronTrigger 记一次用户定时任务触发。result 取 success / partial / failed，
// 与库里 `cron_jobs.last_status` 同一套取值 —— 指标层不发明新词
func MarkCronTrigger(r *Registry, result string) {
	r.AddCounter(CronTriggersTotal, map[string]string{"result": result}, 1)
}

// One 单值样本的简写
func One(value float64) []Sample {
	return []Sample{{Value: value}}
}

// Labeled 带一组标签的单值样本
func Labeled(labels map[string]string, value float64) []Sample {
	return []Sample{{Labels: labels, Value: value}}
}

// BoolValue Prometheus 里没有布尔，约定 1/0
func BoolValue(ok bool) float64 {
	if ok {
		return 1
	}
	return 0
}

// FormatInt 给文档与测试用的小工具
func FormatInt(v int64) string { return strconv.FormatInt(v, 10) }
