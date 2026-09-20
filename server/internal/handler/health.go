package handler

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/gin-gonic/gin"

	"ops-platform/server/internal/model"
	"ops-platform/server/internal/response"
)

// healthItem 一条自检项。
//
// status 只有三档：ok / warn / error。判定标准写在 detail 里，不做模糊的「健康分」。
type healthItem struct {
	Key    string `json:"key"`
	Label  string `json:"label"`
	Status string `json:"status"`
	Value  string `json:"value"`
	Detail string `json:"detail"`
}

type healthGroup struct {
	Name  string       `json:"name"`
	Items []healthItem `json:"items"`
}

// PlatformHealth 平台自检。
//
// 全部是即时计算，不落库、不做历史趋势：这一页回答的是「现在平台自己有没有毛病」。
func (h *Handler) PlatformHealth(c *gin.Context) {
	groups := []healthGroup{
		{Name: "运行时", Items: h.healthRuntime()},
		{Name: "定时调度", Items: h.healthScheduler()},
		{Name: "告警链路", Items: h.healthAlerting()},
		{Name: "资产与会话", Items: h.healthAssets()},
		{Name: "存储", Items: h.healthStorage()},
	}

	overall := "ok"
	warn, errCount := 0, 0
	for _, group := range groups {
		for _, item := range group.Items {
			switch item.Status {
			case "warn":
				warn++
			case "error":
				errCount++
			}
		}
	}
	if warn > 0 {
		overall = "warn"
	}
	if errCount > 0 {
		overall = "error"
	}

	response.OK(c, gin.H{
		"overall": overall, "warnCount": warn, "errorCount": errCount,
		"checkedAt": time.Now(), "groups": groups,
	})
}

func (h *Handler) healthRuntime() []healthItem {
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	uptime := time.Since(h.StartedAt)

	items := []healthItem{
		{
			Key: "uptime", Label: "运行时长", Status: "ok",
			Value:  formatDuration(uptime),
			Detail: "启动于 " + h.StartedAt.Format("2006-01-02 15:04:05"),
		},
		{
			Key: "goroutine", Label: "协程数", Status: "ok",
			Value:  fmt.Sprint(runtime.NumGoroutine()),
			Detail: "每个 Web 终端会话会常驻协程，数量随在线会话增长",
		},
		{
			Key: "memory", Label: "内存占用", Status: "ok",
			Value:  fmt.Sprintf("%.1f MB", float64(mem.Alloc)/1024/1024),
			Detail: fmt.Sprintf("进程向系统申请 %.1f MB，GC 已执行 %d 次", float64(mem.Sys)/1024/1024, mem.NumGC),
		},
		{
			Key: "runtime", Label: "运行环境", Status: "ok",
			Value:  runtime.Version(),
			Detail: fmt.Sprintf("%s/%s，%d 核", runtime.GOOS, runtime.GOARCH, runtime.NumCPU()),
		},
	}

	// 协程数异常增长通常意味着会话没有正常释放
	if n := runtime.NumGoroutine(); n > 500 {
		items[1].Status = "warn"
		items[1].Detail = "协程数偏高，检查是否有会话未正常关闭"
	}
	if h.Cfg.Debug {
		items = append(items, healthItem{
			Key: "debug", Label: "调试模式", Status: "warn", Value: "已开启",
			Detail: "OPS_DEBUG=true 会输出 SQL 日志并启用 gin 调试模式，正式环境建议关闭",
		})
	}
	return items
}

func (h *Handler) healthScheduler() []healthItem {
	var enabled, total int64
	h.DB.Model(&model.CronJob{}).Count(&total)
	h.DB.Model(&model.CronJob{}).Where("enabled = ?", true).Count(&enabled)

	items := make([]healthItem, 0, 4)
	if h.Sched == nil {
		items = append(items, healthItem{
			Key: "scheduler", Label: "调度器", Status: "error", Value: "未启动",
			Detail: "定时任务与内置巡检都不会运行",
		})
		return items
	}

	userJobs, fixedJobs, cronEntries := h.Sched.Stats()
	item := healthItem{
		Key: "scheduler", Label: "调度器", Status: "ok",
		Value: fmt.Sprintf("%d 条在跑", cronEntries),
		Detail: fmt.Sprintf("用户定时任务 %d/%d 已注册，内置固定任务 %d 个",
			userJobs, enabled, fixedJobs),
	}
	// 库里启用的任务数与实际注册数不一致，说明有任务注册失败（多为 cron 表达式问题）
	if int64(userJobs) != enabled {
		item.Status = "warn"
		item.Detail += "；启用数与注册数不一致，检查启动日志里的注册失败记录"
	}
	items = append(items, item)

	items = append(items, h.fixedTaskItem("cert", "证书巡检", h.Cfg.CertCheckSpec,
		"OPS_CERT_CHECK_SPEC 为空，证书只能手动巡检"))
	items = append(items, h.fixedTaskItem("rule", "告警规则评估", h.Cfg.AlertRuleSpec,
		"OPS_ALERT_RULE_SPEC 为空，规则只能手动试跑"))
	return items
}

// fixedTaskItem 内置固定任务的状态。
// 运行时间只记在内存里，进程重启后会显示「本次启动后还没运行过」，不是故障。
func (h *Handler) fixedTaskItem(key, label, spec, disabledHint string) healthItem {
	if spec == "" {
		return healthItem{
			Key: key, Label: label, Status: "warn", Value: "未启用定时",
			Detail: disabledHint,
		}
	}

	at, info := h.lastFixedRun(key)
	if at.IsZero() {
		return healthItem{
			Key: key, Label: label, Status: "ok", Value: spec,
			Detail: "本次启动后还没到执行时间（运行记录只存内存，重启即清空）",
		}
	}
	return healthItem{
		Key: key, Label: label, Status: "ok", Value: spec,
		Detail: fmt.Sprintf("最近运行 %s，%s", at.Format("2006-01-02 15:04:05"), info),
	}
}

func (h *Handler) healthAlerting() []healthItem {
	count := func(table any, where string, args ...any) int64 {
		var n int64
		q := h.DB.Model(table)
		if where != "" {
			q = q.Where(where, args...)
		}
		q.Count(&n)
		return n
	}

	items := make([]healthItem, 0, 5)

	// 通知渠道：一个都没有启用时，所有告警都只会躺在库里
	channels := count(&model.NotifyChannel{}, "")
	enabledChannels := count(&model.NotifyChannel{}, "enabled = ?", true)
	channelItem := healthItem{
		Key: "channels", Label: "通知渠道", Status: "ok",
		Value:  fmt.Sprintf("%d/%d 启用", enabledChannels, channels),
		Detail: "渠道全部停用时告警不会通知到任何人",
	}
	if enabledChannels == 0 {
		channelItem.Status = "error"
	}
	items = append(items, channelItem)

	// 兜底路由：没有兜底时，不匹配任何规则的告警会静默丢弃
	routes := count(&model.NotifyRoute{}, "enabled = ?", true)
	fallback := count(&model.NotifyRoute{}, "enabled = ? AND is_default = ?", true, true)
	routeItem := healthItem{
		Key: "routes", Label: "通知路由", Status: "ok",
		Value:  fmt.Sprintf("%d 条启用", routes),
		Detail: "已配置兜底路由，未命中任何条件的告警仍会投递",
	}
	if fallback == 0 {
		routeItem.Status = "warn"
		routeItem.Detail = "没有兜底路由，未命中任何条件的告警不会通知任何人"
	}
	items = append(items, routeItem)

	// 最近 24 小时的投递成功率
	since := time.Now().Add(-24 * time.Hour)
	sent := count(&model.NotifyRecord{}, "created_at >= ?", since)
	failed := count(&model.NotifyRecord{}, "created_at >= ? AND status <> ?", since, "success")
	deliverItem := healthItem{
		Key: "delivery", Label: "24h 通知投递", Status: "ok",
		Value:  fmt.Sprintf("%d 条，失败 %d", sent, failed),
		Detail: "投递失败意味着告警没送到人，需要检查渠道地址与网络",
	}
	if failed > 0 {
		deliverItem.Status = "warn"
		var last model.NotifyRecord
		if err := h.DB.Where("created_at >= ? AND status <> ?", since, "success").
			Order("id desc").First(&last).Error; err == nil {
			deliverItem.Detail = fmt.Sprintf("最近失败：%s — %s", last.ChannelName, truncate(last.ErrorMsg, 120))
		}
	}
	items = append(items, deliverItem)

	// 告警规则评估情况
	rules := count(&model.AlertRule{}, "enabled = ?", true)
	ruleErrors := count(&model.AlertRule{}, "enabled = ? AND last_status = ?", true, "error")
	neverEval := count(&model.AlertRule{}, "enabled = ? AND last_eval_at IS NULL", true)
	ruleItem := healthItem{
		Key: "rules", Label: "告警规则", Status: "ok",
		Value:  fmt.Sprintf("%d 条启用", rules),
		Detail: "规则命中后写入告警并按路由投递",
	}
	switch {
	case ruleErrors > 0:
		ruleItem.Status = "error"
		ruleItem.Detail = fmt.Sprintf("%d 条规则的指标取值失败，规则实际没在生效", ruleErrors)
	case rules > 0 && neverEval == rules:
		ruleItem.Status = "warn"
		ruleItem.Detail = "所有规则都还没评估过，确认定时评估是否启用"
	}
	items = append(items, ruleItem)

	// 告警积压
	firing := count(&model.Alert{}, "status = ?", "firing")
	backlogItem := healthItem{
		Key: "backlog", Label: "未处理告警", Status: "ok",
		Value:  fmt.Sprint(firing),
		Detail: "长期堆积说明告警没人看或阈值过敏感",
	}
	if firing >= 20 {
		backlogItem.Status = "warn"
	}
	items = append(items, backlogItem)
	return items
}

func (h *Handler) healthAssets() []healthItem {
	count := func(table any, where string, args ...any) int64 {
		var n int64
		q := h.DB.Model(table)
		if where != "" {
			q = q.Where(where, args...)
		}
		q.Count(&n)
		return n
	}

	hosts := count(&model.Host{}, "")
	offline := count(&model.Host{}, "status = ?", "offline")
	unchecked := count(&model.Host{}, "checked_at IS NULL")
	hostItem := healthItem{
		Key: "hosts", Label: "主机探测覆盖", Status: "ok",
		Value:  fmt.Sprintf("%d 台，离线 %d", hosts, offline),
		Detail: "探测结果是主机类告警规则的数据来源",
	}
	if hosts > 0 && unchecked == hosts {
		hostItem.Status = "warn"
		hostItem.Detail = "所有主机都没探测过，主机类规则取不到有效数据"
	} else if unchecked > 0 {
		hostItem.Detail = fmt.Sprintf("其中 %d 台从未探测过，不会计入离线统计", unchecked)
	}

	certs := count(&model.Certificate{}, "")
	certBad := count(&model.Certificate{}, "status IN (?)", []string{"expired", "error"})
	certItem := healthItem{
		Key: "certs", Label: "证书巡检", Status: "ok",
		Value:  fmt.Sprintf("%d 个", certs),
		Detail: "巡检结果异常时会自动写入告警",
	}
	if certBad > 0 {
		certItem.Status = "warn"
		certItem.Detail = fmt.Sprintf("%d 个证书已过期或巡检失败", certBad)
	}

	active := count(&model.Session{}, "status = ?", "active")
	sessionItem := healthItem{
		Key: "sessions", Label: "进行中的终端会话", Status: "ok",
		Value:  fmt.Sprint(active),
		Detail: "会话异常中断时状态会留在 active，数量长期不降需排查",
	}

	return []healthItem{hostItem, certItem, sessionItem}
}

func (h *Handler) healthStorage() []healthItem {
	items := make([]healthItem, 0, 2)

	// 数据库文件：SQLite 下 DSN 就是文件路径
	dbItem := healthItem{Key: "database", Label: "数据库文件", Status: "ok"}
	if info, err := os.Stat(h.Cfg.DSN); err == nil {
		dbItem.Value = formatSize(info.Size())
		dbItem.Detail = h.Cfg.DSN + "，包含主机凭据等敏感数据，备份需同等保护"
	} else {
		dbItem.Status = "warn"
		dbItem.Value = "未知"
		dbItem.Detail = "读不到数据库文件信息: " + err.Error()
	}
	items = append(items, dbItem)

	// 录像目录
	recordItem := healthItem{Key: "recordings", Label: "会话录像", Status: "ok"}
	files, size, err := dirUsage(h.Cfg.RecordDir)
	keepDays := h.configInt(CfgRecordKeepDay, 0)
	switch {
	case err != nil:
		recordItem.Status = "warn"
		recordItem.Value = "不可用"
		recordItem.Detail = "录像目录不可读，会话录像会失败: " + err.Error()
	default:
		recordItem.Value = fmt.Sprintf("%d 个，%s", files, formatSize(size))
		if keepDays > 0 {
			recordItem.Detail = fmt.Sprintf("%s，保留 %d 天（启动时清理过期文件）", h.Cfg.RecordDir, keepDays)
		} else {
			recordItem.Detail = h.Cfg.RecordDir + "，当前配置为永久保留，占用只增不减"
		}
	}
	items = append(items, recordItem)
	return items
}

func dirUsage(dir string) (files int, size int64, err error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, 0, err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		info, statErr := os.Stat(filepath.Join(dir, entry.Name()))
		if statErr != nil {
			continue
		}
		files++
		size += info.Size()
	}
	return files, size, nil
}

func formatSize(size int64) string {
	const unit = 1024
	if size < unit {
		return fmt.Sprintf("%d B", size)
	}
	value := float64(size)
	for _, suffix := range []string{"KB", "MB", "GB"} {
		value /= unit
		if value < unit {
			return fmt.Sprintf("%.1f %s", value, suffix)
		}
	}
	return fmt.Sprintf("%.1f TB", value/unit)
}

func formatDuration(d time.Duration) string {
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	minutes := int(d.Minutes()) % 60
	if days > 0 {
		return fmt.Sprintf("%d 天 %d 小时", days, hours)
	}
	if hours > 0 {
		return fmt.Sprintf("%d 小时 %d 分", hours, minutes)
	}
	return fmt.Sprintf("%d 分钟", minutes)
}
