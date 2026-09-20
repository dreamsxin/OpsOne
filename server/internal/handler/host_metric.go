package handler

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"ops-platform/server/internal/middleware"
	"ops-platform/server/internal/model"
	"ops-platform/server/internal/response"
	"ops-platform/server/internal/sshx"
)

const (
	// hostMetricConcurrency 采集并发上限。和批量执行的并发是两条独立的池子，
	// 避免定时采集把人工执行的名额占满
	hostMetricConcurrency = 10
	// hostMetricTimeout 单台采集超时。命令里有 1 秒 sleep（算 CPU 差值），留足余量
	hostMetricTimeout = 15 * time.Second
	// hostMetricStaleMinutes 超过这个时间没采到的样本不参与告警判定，
	// 否则机器早就下线了、平台还在拿半天前的数据报警
	hostMetricStaleMinutes = 30
)

// hostMetricCommand 采集命令。
//
// 设计原则：远端只负责"把原始数字读出来"，所有换算（CPU 百分比、内存占比、
// 单核负载）都在 Go 里做 —— 这样解析逻辑可以单测，也不用假设远端有 bc/python。
// 只用 /proc 与 df/awk，POSIX sh 即可，不需要 bash。
//
// 磁盘只统计真实块设备（/dev/* 且非 loop，排除 /snap 挂载点）。原因是实测踩过：
// snap 的 squashfs 镜像按设计永远报 100%，WSL 注入的 9p 挂载会把 Windows 盘的
// 占用算进来 —— 这两类都会让"磁盘满了"变成天天误报。代价是 NFS 等网络挂载
// 不在统计内，这个指标的语义就是"本地盘还剩多少"。
const hostMetricCommand = `LC_ALL=C
echo "cores=$(grep -c '^processor' /proc/cpuinfo 2>/dev/null || echo 1)"
awk '/^cpu /{t=0; for(i=2;i<=NF;i++) t+=$i; print "cpu1=" t " " $5}' /proc/stat
sleep 1
awk '/^cpu /{t=0; for(i=2;i<=NF;i++) t+=$i; print "cpu2=" t " " $5}' /proc/stat
awk '/^MemTotal:/{tot=$2} /^MemAvailable:/{av=$2} /^SwapTotal:/{st=$2} /^SwapFree:/{sf=$2} END{print "mem=" tot+0 " " av+0 " " st+0 " " sf+0}' /proc/meminfo
echo "load=$(cut -d' ' -f1-3 /proc/loadavg)"
echo "uptime=$(cut -d' ' -f1 /proc/uptime)"
echo "procs=$(ls -1 /proc 2>/dev/null | grep -c '^[0-9]')"
echo "tcp=$(cat /proc/net/tcp /proc/net/tcp6 2>/dev/null | grep -c ':')"
df -kP 2>/dev/null | awk 'NR>1 && $1 ~ /^\/dev\// && $1 !~ /loop/ && $6 !~ /^\/snap\// {gsub("%","",$5); print "disk=" $5 " " $6}'`

// metricSample 解析出来的一次采样，字段与 model.HostMetric 对应
type metricSample struct {
	CPUPercent     float64
	MemPercent     float64
	SwapPercent    float64
	DiskMaxPercent float64
	DiskMaxMount   string
	Load1          float64
	Load5          float64
	Load15         float64
	CPUCores       int
	MemTotalMB     int64
	MemUsedMB      int64
	ProcCount      int
	TCPConn        int
	UptimeSec      int64
}

func parseFloat(s string) float64 {
	v, _ := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return v
}

// parseHostMetrics 把采集命令的输出解析成一次采样。
//
// 解析刻意宽容：单项缺失（老内核没有 MemAvailable、容器里读不到 /proc/net/tcp）
// 只让那一项为 0，不让整次采集失败；只有"连 CPU 都没读到"才算彻底失败。
func parseHostMetrics(raw string) (metricSample, error) {
	var sample metricSample
	var cpuTotal1, cpuIdle1, cpuTotal2, cpuIdle2 float64
	gotCPU1, gotCPU2 := false, false

	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		fields := strings.Fields(value)
		switch key {
		case "cores":
			if n, err := strconv.Atoi(strings.TrimSpace(value)); err == nil && n > 0 {
				sample.CPUCores = n
			}
		case "cpu1":
			if len(fields) >= 2 {
				cpuTotal1, cpuIdle1 = parseFloat(fields[0]), parseFloat(fields[1])
				gotCPU1 = true
			}
		case "cpu2":
			if len(fields) >= 2 {
				cpuTotal2, cpuIdle2 = parseFloat(fields[0]), parseFloat(fields[1])
				gotCPU2 = true
			}
		case "mem":
			// tot avail swapTotal swapFree，单位 KB
			if len(fields) >= 4 {
				total, avail := parseFloat(fields[0]), parseFloat(fields[1])
				swapTotal, swapFree := parseFloat(fields[2]), parseFloat(fields[3])
				if total > 0 {
					sample.MemTotalMB = int64(total / 1024)
					sample.MemUsedMB = int64((total - avail) / 1024)
					sample.MemPercent = (total - avail) / total * 100
				}
				if swapTotal > 0 {
					sample.SwapPercent = (swapTotal - swapFree) / swapTotal * 100
				}
			}
		case "load":
			if len(fields) >= 3 {
				sample.Load1, sample.Load5, sample.Load15 = parseFloat(fields[0]), parseFloat(fields[1]), parseFloat(fields[2])
			}
		case "uptime":
			sample.UptimeSec = int64(parseFloat(value))
		case "procs":
			sample.ProcCount = int(parseFloat(value))
		case "tcp":
			sample.TCPConn = int(parseFloat(value))
		case "disk":
			// 只保留最满的那个挂载点
			if len(fields) >= 2 {
				if used := parseFloat(fields[0]); used > sample.DiskMaxPercent {
					sample.DiskMaxPercent, sample.DiskMaxMount = used, fields[1]
				}
			}
		}
	}

	if !gotCPU1 || !gotCPU2 {
		return sample, fmt.Errorf("没有读到 /proc/stat，确认目标是 Linux 且当前用户可读 /proc")
	}
	if delta := cpuTotal2 - cpuTotal1; delta > 0 {
		busy := delta - (cpuIdle2 - cpuIdle1)
		sample.CPUPercent = busy / delta * 100
		if sample.CPUPercent < 0 {
			sample.CPUPercent = 0
		}
		if sample.CPUPercent > 100 {
			sample.CPUPercent = 100
		}
	}
	if sample.CPUCores <= 0 {
		sample.CPUCores = 1
	}
	return sample, nil
}

// round2 落库前把百分比收到两位小数，前端就不用再处理长尾浮点
func round2(v float64) float64 {
	return float64(int64(v*100+0.5)) / 100
}

// collectHostMetric 采一台主机。采集失败也会落一条 status=failed 的记录 ——
// "采不到"本身就是需要被看见和告警的状态，静默跳过等于假装一切正常。
func (h *Handler) collectHostMetric(ctx context.Context, host model.Host) model.HostMetric {
	record := model.HostMetric{HostID: host.ID, Status: "ok"}

	runCtx, cancel := context.WithTimeout(ctx, hostMetricTimeout)
	defer cancel()
	result := sshx.Run(runCtx, h.target(&host), hostMetricCommand)
	record.CostMs = result.CostMs

	if result.Status != "success" {
		record.Status = "failed"
		detail := strings.TrimSpace(result.Stderr)
		if detail == "" {
			detail = "采集命令返回 " + result.Status
		}
		record.Error = truncate(detail, 240)
		return record
	}

	sample, err := parseHostMetrics(result.Stdout)
	if err != nil {
		record.Status = "failed"
		record.Error = truncate(err.Error(), 240)
		return record
	}

	record.CPUPercent = round2(sample.CPUPercent)
	record.MemPercent = round2(sample.MemPercent)
	record.SwapPercent = round2(sample.SwapPercent)
	record.DiskMaxPercent = round2(sample.DiskMaxPercent)
	record.DiskMaxMount = sample.DiskMaxMount
	record.Load1, record.Load5, record.Load15 = sample.Load1, sample.Load5, sample.Load15
	record.CPUCores, record.MemTotalMB, record.MemUsedMB = sample.CPUCores, sample.MemTotalMB, sample.MemUsedMB
	record.ProcCount, record.TCPConn, record.UptimeSec = sample.ProcCount, sample.TCPConn, sample.UptimeSec
	return record
}

// collectHostMetrics 并发采集一批主机并落库，返回采集结果
func (h *Handler) collectHostMetrics(ctx context.Context, hosts []model.Host) []model.HostMetric {
	records := make([]model.HostMetric, len(hosts))
	sem := make(chan struct{}, hostMetricConcurrency)
	var wg sync.WaitGroup

	for i := range hosts {
		wg.Add(1)
		go func(idx int, host model.Host) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			records[idx] = h.collectHostMetric(ctx, host)
		}(i, hosts[i])
	}
	wg.Wait()

	if len(records) > 0 {
		if err := h.DB.Create(&records).Error; err != nil {
			log.Printf("[metric] 采样写入失败: %v", err)
		}
	}
	return records
}

// ---------- 接口 ----------

// hostMetricView 主机 + 它的最新采样，列表页一行一条
type hostMetricView struct {
	HostID   uint   `json:"hostId"`
	HostName string `json:"hostName"`
	Address  string `json:"address"`
	Env      string `json:"env"`
	// Metric 为空表示这台还没采过
	Metric *model.HostMetric `json:"metric"`
	// LoadPerCore 单核负载，跨机型比较负载时看这个而不是 load1
	LoadPerCore float64 `json:"loadPerCore"`
	// Stale 最新采样已经太旧（超过 30 分钟），页面上要标出来
	Stale bool `json:"stale"`
}

// latestHostMetrics 取每台主机的最新采样。
// 用 id IN (SELECT MAX(id) GROUP BY host_id) 而不是窗口函数，SQLite/MySQL 都吃得下。
func (h *Handler) latestHostMetrics(hostIDs []uint) map[uint]model.HostMetric {
	byHost := map[uint]model.HostMetric{}
	if len(hostIDs) == 0 {
		return byHost
	}
	var metrics []model.HostMetric
	h.DB.Where("id IN (?)",
		h.DB.Model(&model.HostMetric{}).Select("MAX(id)").
			Where("host_id IN ?", hostIDs).Group("host_id"),
	).Find(&metrics)
	for _, item := range metrics {
		byHost[item.HostID] = item
	}
	return byHost
}

// ListHostMetrics 主机指标总览：每台主机一行，带最新采样
func (h *Handler) ListHostMetrics(c *gin.Context) {
	var hosts []model.Host
	q := h.applyScopeWithGrants(h.DB.Model(&model.Host{}), middleware.CurrentUser(c), "host")
	if env := c.Query("env"); env != "" {
		q = q.Where("env = ?", env)
	}
	if err := q.Order("id asc").Find(&hosts).Error; err != nil {
		response.Error(c, "查询主机失败")
		return
	}

	ids := make([]uint, 0, len(hosts))
	for _, host := range hosts {
		ids = append(ids, host.ID)
	}
	latest := h.latestHostMetrics(ids)

	deadline := time.Now().Add(-hostMetricStaleMinutes * time.Minute)
	views := make([]hostMetricView, 0, len(hosts))
	collected, failed := 0, 0
	for _, host := range hosts {
		view := hostMetricView{
			HostID: host.ID, HostName: host.Name,
			Address: host.Address, Env: host.Env,
		}
		if metric, ok := latest[host.ID]; ok {
			copied := metric
			view.Metric = &copied
			view.Stale = metric.CreatedAt.Before(deadline)
			if metric.CPUCores > 0 {
				view.LoadPerCore = round2(metric.Load1 / float64(metric.CPUCores))
			}
			collected++
			if metric.Status != "ok" {
				failed++
			}
		}
		views = append(views, view)
	}

	response.OK(c, gin.H{
		"items": views, "total": len(views),
		"collected": collected, "failed": failed,
		"staleMinutes": hostMetricStaleMinutes,
		"spec":         h.Cfg.HostMetricSpec,
	})
}

// HostMetricHistory 单台主机的采样序列，给趋势图用
func (h *Handler) HostMetricHistory(c *gin.Context) {
	hostID := idParam(c)
	user := middleware.CurrentUser(c)
	if len(h.filterHostIDsForAction(user, []uint{hostID}, model.ActionExec)) == 0 {
		response.Forbidden(c, "没有这台主机的权限")
		return
	}

	hours := 6
	if raw := c.Query("hours"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 && n <= 24*7 {
			hours = n
		}
	}
	var list []model.HostMetric
	err := h.DB.Where("host_id = ? AND created_at >= ?", hostID, time.Now().Add(-time.Duration(hours)*time.Hour)).
		Order("id asc").Limit(2000).Find(&list).Error
	if err != nil {
		response.Error(c, "查询采样失败")
		return
	}
	response.OK(c, gin.H{"items": list, "total": len(list), "hours": hours})
}

// CollectHostMetric 立刻采一台主机
func (h *Handler) CollectHostMetric(c *gin.Context) {
	hostID := idParam(c)
	user := middleware.CurrentUser(c)
	if len(h.filterHostIDsForAction(user, []uint{hostID}, model.ActionExec)) == 0 {
		response.Forbidden(c, "没有这台主机的权限")
		return
	}
	var host model.Host
	if err := h.DB.First(&host, hostID).Error; err != nil {
		response.NotFound(c, "主机不存在")
		return
	}

	records := h.collectHostMetrics(c.Request.Context(), []model.Host{host})
	record := records[0]
	if record.Status != "ok" {
		response.OK(c, gin.H{"metric": record, "detail": "采集失败：" + record.Error})
		return
	}
	response.OK(c, gin.H{
		"metric": record,
		"detail": fmt.Sprintf("CPU %.1f%% · 内存 %.1f%% · 磁盘 %.1f%%(%s) · 负载 %.2f",
			record.CPUPercent, record.MemPercent, record.DiskMaxPercent, record.DiskMaxMount, record.Load1),
	})
}

// CollectAllHostMetrics 立刻采集当前可见范围内的全部主机
func (h *Handler) CollectAllHostMetrics(c *gin.Context) {
	var hosts []model.Host
	q := h.applyScopeWithGrants(h.DB.Model(&model.Host{}), middleware.CurrentUser(c), "host")
	if err := q.Find(&hosts).Error; err != nil {
		response.Error(c, "查询主机失败")
		return
	}
	if len(hosts) == 0 {
		response.BadRequest(c, "没有可采集的主机")
		return
	}

	records := h.collectHostMetrics(c.Request.Context(), hosts)
	ok := 0
	for _, item := range records {
		if item.Status == "ok" {
			ok++
		}
	}
	response.OK(c, gin.H{
		"total": len(records), "ok": ok, "failed": len(records) - ok,
		"detail": fmt.Sprintf("采集 %d 台，成功 %d 台，失败 %d 台", len(records), ok, len(records)-ok),
	})
}

// CollectHostMetricsForSchedule 定时采集入口：全部主机，不做数据权限过滤
// （和定时任务一致 —— 它不是替某个人在看，而是平台自己在采）
func (h *Handler) CollectHostMetricsForSchedule() {
	var hosts []model.Host
	if err := h.DB.Find(&hosts).Error; err != nil {
		log.Printf("[metric] 读取主机失败: %v", err)
		return
	}
	if len(hosts) == 0 {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	records := h.collectHostMetrics(ctx, hosts)

	ok := 0
	for _, item := range records {
		if item.Status == "ok" {
			ok++
		}
	}
	h.markFixedRun("metric", fmt.Sprintf("共 %d 台，成功 %d 台，失败 %d 台", len(records), ok, len(records)-ok))
	log.Printf("[metric] 定时采集完成: 共 %d 台，成功 %d 台，失败 %d 台", len(records), ok, len(records)-ok)
}

// ---------- 供告警规则使用的聚合 ----------

// freshHostMetrics 取窗口内每台主机的最新采样（跨过窗口的样本一律不用）
func (h *Handler) freshHostMetrics(window time.Duration) []model.HostMetric {
	if window <= 0 {
		window = hostMetricStaleMinutes * time.Minute
	}
	var metrics []model.HostMetric
	h.DB.Where("id IN (?)",
		h.DB.Model(&model.HostMetric{}).Select("MAX(id)").
			Where("created_at >= ?", time.Now().Add(-window)).Group("host_id"),
	).Find(&metrics)
	return metrics
}

// hostNameOf 取主机名，用于告警明细；查不到就退回 #ID
func (h *Handler) hostNameOf(hostID uint) string {
	var host model.Host
	if h.DB.Select("name").First(&host, hostID).Error == nil && host.Name != "" {
		return host.Name
	}
	return fmt.Sprintf("#%d", hostID)
}

// maxHostMetric 在窗口内最新采样里找某个维度的最大值，返回值与一句明细。
// pick 返回该采样的取值；只看 status=ok 的样本。
func (h *Handler) maxHostMetric(window time.Duration, unit string, pick func(model.HostMetric) float64) (float64, string) {
	metrics := h.freshHostMetrics(window)
	best, bestID := 0.0, uint(0)
	counted := 0
	for _, item := range metrics {
		if item.Status != "ok" {
			continue
		}
		counted++
		if value := pick(item); value > best || bestID == 0 {
			best, bestID = value, item.HostID
		}
	}
	if counted == 0 {
		return 0, "窗口内没有有效采样"
	}
	name := h.hostNameOf(bestID)
	if unit == "%" {
		return best, fmt.Sprintf("最高的是 %s（%.1f%%），共 %d 台有采样", name, best, counted)
	}
	return best, fmt.Sprintf("最高的是 %s（%.2f%s），共 %d 台有采样", name, best, unit, counted)
}
