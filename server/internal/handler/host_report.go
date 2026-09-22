package handler

// 主机体检报告。
//
// 参照站对应的那页叫「Agent 报告」：装一个常驻 agent，由它上报漏洞、补丁、
// 已装软件包、进程与端口清单。本平台**没有任何常驻采集**，所以这一页
// 不是那一页的实现，而是另一件事：把已有的几条 SSH 定时巡检链路
// 按主机汇总成一张可以打印给人看的单子。
//
// 这个区别必须写在页面上，否则「体检报告」这个名字会让人以为漏洞和补丁也在里面。
// 报告里因此有一个固定的 gaps 段落，逐条列出**平台一次都没采过**的东西：
//
//	漏洞 / CVE、补丁、已装软件包、进程清单、完整监听端口、失败登录记录
//
// 这些不是"暂未实现"的委婉说法 —— 它们需要在主机上常驻或定期执行包管理器查询，
// 那是 agent 的活。宁可在报告上留一块空白，也不要用「未发现漏洞」这种
// 读起来像结论、实际上什么都没查的措辞。
//
// 另一个容易做错的地方：**「没采集」和「采集到 0」必须分开**。
// 三条链路在失败时留下的东西还不一样：
//   - 指标：新增一条 status=failed 的记录（所以能区分"采过但失败"与"从没采过"）
//   - 服务 / 配置：更新同一行，drift 置为 error，实际态沿用上一次成功的值
//   - 防火墙：**根本没有定时任务**，只有人打开那一页时才现读一次
//
// 报告里每一段都带 source 与 checkedAt，读的人自己判断这个数字有多新。

import (
	"fmt"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"ops-platform/server/internal/model"
	"ops-platform/server/internal/response"
)

// hostReportStaleHours 超过这个时长的巡检结果在报告里标成「已过期」。
// 取 24 小时是因为最慢的那条链路（配置巡检 30 分钟一轮）也远快于它，
// 超过一天没更新意味着这台机器或那条链路本身出了问题。
const hostReportStaleHours = 24

// reportSection 报告里的一段。
//
// 三个字段缺一不可：Source 说清数据是怎么来的、CheckedAt 说清有多新、
// Gap 说清这段是不是压根没有数据 —— 只给 Items 的话，空数组既可能是
// 「一切正常」也可能是「从没采过」。
type reportSection struct {
	Title     string     `json:"title"`
	Source    string     `json:"source"`
	CheckedAt *time.Time `json:"checkedAt"`
	// Stale 数据是否已经过期（超过 hostReportStaleHours）
	Stale bool `json:"stale"`
	// Gap 非空表示这一段没有可用数据，值就是原因
	Gap string `json:"gap"`
	// Summary 一句话结论
	Summary string `json:"summary"`
	// Items 明细，字段随段落不同
	Items []gin.H `json:"items"`
	// Problems 这一段里有几条是有问题的，用来算总分
	Problems int `json:"problems"`
}

func markStale(section *reportSection) {
	if section.CheckedAt != nil && time.Since(*section.CheckedAt) > hostReportStaleHours*time.Hour {
		section.Stale = true
	}
}

// hostReportNotCollected 平台一次都没采集过的东西。
//
// 这份清单是这一页诚实性的核心，逐条给出「为什么没有」而不只是「没有」。
var hostReportNotCollected = []gin.H{
	{"item": "漏洞 / CVE", "reason": "平台没有漏洞库，也没有任何一处在主机上执行过扫描。所以这一页给不出漏洞结论 —— 空白就是空白，不用读起来像结论的措辞去填它"},
	{"item": "补丁与待升级包", "reason": "需要在主机上跑包管理器查询（yum/apt/dnf），平台的 SSH 巡检刻意只做只读的 /proc 与 systemctl 采样"},
	{"item": "已装软件包清单", "reason": "同上；一台机器几千个包，按巡检节奏拉全量也不合适"},
	{"item": "进程清单", "reason": "只采了进程**数量**（/proc 下的数字目录计数）与持有监听端口的进程数，没有进程名、PID、命令行"},
	{"item": "完整监听端口", "reason": "服务巡检时采到过全量监听端口，但只有能归到纳管 unit 的那部分落库 —— 全存下来是噪音。另有「暴露面扫描」从平台侧连过去探测，那是外部视角，不是主机自报"},
	{"item": "失败登录 / 爆破记录", "reason": "平台自己的审计中间件挂在鉴权之后，登录接口在公开路由上，从来没有记录过一次失败登录；主机上的 /var/log/secure 也只在你显式登记为日志巡检目标时才会被读"},
	{"item": "硬件信息（CPU 型号 / 内存条 / 磁盘序列号）", "reason": "没有读 lscpu / dmidecode。CPU 核数与内存总量有采集（在指标里），厂商与序列号只能在「固定资产」里人工登记"},
}

// ---------- 各段落 ----------

// metricSection 指标段。这一段是唯一的时序事实，也是唯一能区分
// 「采过但失败」与「从没采过」的一段。
func (h *Handler) metricSection(hostID uint) reportSection {
	section := reportSection{
		Title:  "资源使用",
		Source: "SSH 定时采样（/proc 与 df，默认每 5 分钟一次）",
		Items:  []gin.H{},
	}

	var latest model.HostMetric
	err := h.DB.Where("host_id = ?", hostID).Order("id desc").First(&latest).Error
	if err != nil {
		section.Gap = "从来没有采集成功过，也没有失败记录 —— 这台主机可能从未参与过指标采集（检查 OPS_HOST_METRIC_SPEC 与主机是否启用）"
		return section
	}
	section.CheckedAt = &latest.CreatedAt
	markStale(&section)

	if latest.Status != "ok" {
		section.Gap = "最近一次采集失败：" + orDefault(latest.Error, "原因未记录")
		section.Summary = "采集失败，下面的数字不可用"
		section.Problems = 1
		return section
	}

	section.Items = append(section.Items,
		gin.H{"name": "CPU 使用率", "value": fmt.Sprintf("%.1f%%", latest.CPUPercent), "warn": latest.CPUPercent >= 85},
		gin.H{"name": "内存使用率", "value": fmt.Sprintf("%.1f%%", latest.MemPercent),
			"detail": fmt.Sprintf("%d / %d MB", latest.MemUsedMB, latest.MemTotalMB), "warn": latest.MemPercent >= 90},
		gin.H{"name": "交换分区", "value": fmt.Sprintf("%.1f%%", latest.SwapPercent), "warn": latest.SwapPercent >= 50},
		gin.H{"name": "磁盘最满挂载点", "value": fmt.Sprintf("%.1f%%", latest.DiskMaxPercent),
			"detail": latest.DiskMaxMount, "warn": latest.DiskMaxPercent >= 85,
			"note": "只记最满的那一个挂载点，网络挂载不在统计内"},
		inodeReportItem(latest),
		gin.H{"name": "负载", "value": fmt.Sprintf("%.2f / %.2f / %.2f", latest.Load1, latest.Load5, latest.Load15),
			"detail": fmt.Sprintf("%d 核", latest.CPUCores),
			"warn":   latest.CPUCores > 0 && latest.Load5 > float64(latest.CPUCores)*2},
		gin.H{"name": "进程数", "value": fmt.Sprint(latest.ProcCount), "note": "只有数量，没有进程清单"},
		gin.H{"name": "TCP 连接数", "value": fmt.Sprint(latest.TCPConn), "note": "只有数量，没有连接明细"},
		gin.H{"name": "运行时长", "value": formatUptime(latest.UptimeSec)},
	)
	for _, item := range section.Items {
		if warn, _ := item["warn"].(bool); warn {
			section.Problems++
		}
	}

	// 24 小时采样成功率：比"最近一次成功"更能说明这条链路稳不稳
	var total, failed int64
	since := time.Now().Add(-24 * time.Hour)
	h.DB.Model(&model.HostMetric{}).Where("host_id = ? AND created_at >= ?", hostID, since).Count(&total)
	h.DB.Model(&model.HostMetric{}).
		Where("host_id = ? AND created_at >= ? AND status <> ?", hostID, since, "ok").Count(&failed)
	if total > 0 {
		section.Summary = fmt.Sprintf("近 24 小时采样 %d 次，失败 %d 次", total, failed)
		if failed > 0 {
			section.Problems++
		}
	} else {
		section.Summary = "近 24 小时没有采样记录"
	}
	return section
}

func formatUptime(sec int64) string {
	if sec <= 0 {
		return "未知"
	}
	days := sec / 86400
	hours := (sec % 86400) / 3600
	if days > 0 {
		return fmt.Sprintf("%d 天 %d 小时", days, hours)
	}
	return fmt.Sprintf("%d 小时", hours)
}

// inodeReportItem inode 那一条。老采样没有这个数据，照实说没有而不是显示 0%。
func inodeReportItem(latest model.HostMetric) gin.H {
	if !latest.InodeRead {
		return gin.H{"name": "inode 最满挂载点", "value": "未采集",
			"note": "这条采样是 inode 采集上线前采的（或者这台机器的 df -i 读不到）——" +
				"不是「inode 很空」，是没有数据"}
	}
	return gin.H{"name": "inode 最满挂载点", "value": fmt.Sprintf("%.1f%%", latest.InodeMaxPercent),
		"detail": latest.InodeMaxMount, "warn": latest.InodeMaxPercent >= 85,
		"note": "inode 用光的表现是写文件报 No space left on device，而磁盘空间看着还很空"}
}

// serviceSection 纳管服务段。注意它的失败语义与指标不同：
// 巡检失败时 drift=error，而实际态字段沿用上一次成功的值。
func (h *Handler) serviceSection(hostID uint) reportSection {
	section := reportSection{
		Title:  "纳管服务",
		Source: "SSH 定时巡检（systemctl，默认每 10 分钟一次）。只巡检你登记过的 unit，机器上没登记的服务不在内",
		Items:  []gin.H{},
	}

	var services []model.HostService
	h.DB.Where("host_id = ?", hostID).Order("critical desc, name asc").Find(&services)
	if len(services) == 0 {
		section.Gap = "这台主机没有登记任何纳管服务 —— 不代表机器上没跑服务，只代表平台没被告知要看哪些"
		return section
	}

	var newest *time.Time
	for _, svc := range services {
		if svc.LastCheckAt != nil && (newest == nil || svc.LastCheckAt.After(*newest)) {
			newest = svc.LastCheckAt
		}
		item := gin.H{
			"name": svc.Name, "critical": svc.Critical, "drift": svc.Drift,
			"activeState": svc.ActiveState, "enableState": svc.EnableState,
			"ports": svc.Ports, "detail": svc.DriftDetail, "checkedAt": svc.LastCheckAt,
		}
		if svc.Drift != "ok" && svc.Drift != "" {
			section.Problems++
			item["warn"] = true
		}
		if svc.Drift == "error" {
			item["note"] = "巡检本身失败了，上面的状态是上一次成功时的值：" + orDefault(svc.LastError, "原因未记录")
		}
		if svc.LastCheckAt == nil {
			item["note"] = "从未巡检过"
		}
		section.Items = append(section.Items, item)
	}
	section.CheckedAt = newest
	markStale(&section)
	section.Summary = fmt.Sprintf("登记 %d 个，%d 个与期望不符", len(services), section.Problems)
	return section
}

// configSection 配置文件段，失败语义同服务段
func (h *Handler) configSection(hostID uint) reportSection {
	section := reportSection{
		Title:  "配置文件",
		Source: "SSH 定时巡检（读文件算 hash，默认每 30 分钟一次）",
		Items:  []gin.H{},
	}

	var files []model.ConfigFile
	h.DB.Where("host_id = ?", hostID).Order("critical desc, path asc").Find(&files)
	if len(files) == 0 {
		section.Gap = "这台主机没有登记任何配置文件"
		return section
	}

	var newest *time.Time
	for _, file := range files {
		if file.LastCheckAt != nil && (newest == nil || file.LastCheckAt.After(*newest)) {
			newest = file.LastCheckAt
		}
		item := gin.H{
			"path": file.Path, "category": file.Category, "critical": file.Critical,
			"drift": file.Drift, "diffLines": file.DiffLines, "checkedAt": file.LastCheckAt,
		}
		switch file.Drift {
		case "ok":
		case "no-desired":
			item["note"] = "还没有基线版本，平台只是记录了它的 hash，改了也不会被判为漂移"
			item["warn"] = true
			section.Problems++
		case "error":
			item["note"] = "巡检失败：" + orDefault(file.LastError, "原因未记录")
			item["warn"] = true
			section.Problems++
		default:
			item["warn"] = true
			section.Problems++
		}
		section.Items = append(section.Items, item)
	}
	section.CheckedAt = newest
	markStale(&section)
	section.Summary = fmt.Sprintf("登记 %d 个，%d 个需要关注", len(files), section.Problems)
	return section
}

// logSection 日志巡检段
func (h *Handler) logSection(hostID uint) reportSection {
	section := reportSection{
		Title:  "日志与目录占用",
		Source: "SSH 定时巡检（按水位增量读，默认每 15 分钟一次）",
		Items:  []gin.H{},
	}

	var targets []model.HostLogTarget
	h.DB.Where("host_id = ?", hostID).Order("path asc").Find(&targets)

	var usage model.HostLogUsage
	hasUsage := h.DB.Where("host_id = ?", hostID).Order("id desc").First(&usage).Error == nil

	if len(targets) == 0 && !hasUsage {
		section.Gap = "这台主机没有登记日志巡检目标，也没有目录占用快照"
		return section
	}

	var newest *time.Time
	for _, target := range targets {
		if target.LastScanAt != nil && (newest == nil || target.LastScanAt.After(*newest)) {
			newest = target.LastScanAt
		}
		item := gin.H{
			"path": target.Path, "status": target.LastStatus,
			"hitCount": target.LastHitCount, "scannedAt": target.LastScanAt,
			"rotated": target.LastRotated,
		}
		if target.LastStatus == "hit" || target.LastStatus == "missing" ||
			target.LastStatus == "denied" || target.LastStatus == "failed" {
			item["warn"] = true
			section.Problems++
		}
		if target.LastStatus == "ok" {
			item["note"] = "这一轮没有命中关键字（不代表日志里没有别的问题，只代表没命中你登记的那些词）"
		}
		section.Items = append(section.Items, item)
	}

	if hasUsage {
		item := gin.H{
			"path": "日志目录占用", "status": usage.Status,
			"value": fmt.Sprintf("%.1f GB", float64(usage.TotalKB)/1024/1024),
		}
		if usage.TopIsDir {
			item["note"] = "Top 清单里混有目录（du -a 退化），别照着它去删"
		}
		section.Items = append(section.Items, item)
		if newest == nil || usage.CreatedAt.After(*newest) {
			newest = &usage.CreatedAt
		}
	}

	section.CheckedAt = newest
	markStale(&section)
	section.Summary = fmt.Sprintf("巡检 %d 个目标，%d 个需要关注", len(targets), section.Problems)
	return section
}

// activitySection 谁在这台机器上做过什么。这一段不是"体检"，
// 但排查问题时它常常是最有用的一段。
func (h *Handler) activitySection(hostID uint, hostName string) reportSection {
	section := reportSection{
		Title:  "近 7 天操作痕迹",
		Source: "平台自身的会话 / 批量执行 / 文件操作流水（不是主机上的记录）",
		Items:  []gin.H{},
	}
	since := time.Now().Add(-7 * 24 * time.Hour)

	var sessionCount, blockedCount, execCount, fileCount int64
	h.DB.Model(&model.Session{}).Where("host_id = ? AND started_at >= ?", hostID, since).Count(&sessionCount)
	h.DB.Model(&model.Session{}).
		Where("host_id = ? AND started_at >= ? AND blocked_count > 0", hostID, since).Count(&blockedCount)
	h.DB.Model(&model.ExecResult{}).Where("host_id = ?", hostID).Count(&execCount)
	h.DB.Model(&model.FileAudit{}).Where("host_id = ? AND created_at >= ?", hostID, since).Count(&fileCount)

	section.Items = append(section.Items,
		gin.H{"name": "终端会话", "value": fmt.Sprint(sessionCount)},
		gin.H{"name": "含被拦命令的会话", "value": fmt.Sprint(blockedCount), "warn": blockedCount > 0},
		gin.H{"name": "批量执行结果（全部历史）", "value": fmt.Sprint(execCount),
			"note": "批量执行结果表没有时间字段可按 7 天筛，这里是累计数"},
		gin.H{"name": "文件上传 / 下载 / 删除", "value": fmt.Sprint(fileCount)},
	)
	if blockedCount > 0 {
		section.Problems++
	}
	section.Summary = fmt.Sprintf("7 天内 %d 次终端会话", sessionCount)

	// 会话录像与命令明细都在各自的页面上，这里只给数量与入口
	section.Items = append(section.Items, gin.H{
		"name": "命令明细", "value": "见「会话审计」",
		"note": "按主机名 / 地址检索：" + hostName,
	})
	return section
}

// firewallSection 防火墙段。它的特殊之处是**没有定时任务** ——
// 数据新旧完全取决于有没有人打开过那一页。
func (h *Handler) firewallSection(hostID uint) reportSection {
	section := reportSection{
		Title:  "防火墙规则",
		Source: "SSH 现读（**没有定时巡检**：数据新旧取决于最近一次有人打开防火墙页或执行下发）",
		Items:  []gin.H{},
	}

	var rules []model.FirewallRule
	h.DB.Where("host_id = ?", hostID).Find(&rules)
	if len(rules) == 0 {
		section.Gap = "这台主机没有登记防火墙规则。机器上可能有存量规则，但平台只在你打开防火墙页时才去现读"
		return section
	}

	states := map[string]int{}
	var newest *time.Time
	for _, rule := range rules {
		states[rule.State]++
		if rule.LastCheckAt != nil && (newest == nil || rule.LastCheckAt.After(*newest)) {
			newest = rule.LastCheckAt
		}
	}
	for _, state := range []string{"synced", "pending", "drift", "extra"} {
		if count := states[state]; count > 0 {
			item := gin.H{"name": firewallStateLabel(state), "value": fmt.Sprint(count)}
			if state != "synced" {
				item["warn"] = true
				section.Problems += count
			}
			section.Items = append(section.Items, item)
		}
	}
	section.CheckedAt = newest
	markStale(&section)
	if newest == nil {
		section.Summary = fmt.Sprintf("登记 %d 条规则，但从未与真机核对过", len(rules))
		section.Problems++
	} else {
		section.Summary = fmt.Sprintf("登记 %d 条规则，%d 条与真机不一致或待下发", len(rules), section.Problems)
	}
	return section
}

func firewallStateLabel(state string) string {
	switch state {
	case "synced":
		return "已生效"
	case "pending":
		return "待下发"
	case "drift":
		return "与真机不一致"
	case "extra":
		return "真机多出来"
	default:
		return state
	}
}

// formatTimePtr 空指针照实写「从未」，不要输出零值时间让人误以为是 1970 年出的问题
func formatTimePtr(t *time.Time) string {
	if t == nil {
		return "从未"
	}
	return t.Format("2006-01-02 15:04:05")
}

// ---------- 接口 ----------

// GetHostReport 生成一台主机的体检报告。
//
// 现算不落库：报告是"当下已有数据的一个视图"，存下来只会多一份会过期的副本。
func (h *Handler) GetHostReport(c *gin.Context) {
	// 复用主机的数据范围 + 资源授权：看不到这台主机的人也拿不到它的报告
	host, ok := h.loadHostForAction(c, model.ActionExec)
	if !ok {
		return
	}

	sections := []reportSection{
		h.metricSection(host.ID),
		h.serviceSection(host.ID),
		h.configSection(host.ID),
		h.logSection(host.ID),
		h.firewallSection(host.ID),
		h.activitySection(host.ID, host.Name),
	}

	problems, gaps := 0, 0
	for _, section := range sections {
		problems += section.Problems
		if section.Gap != "" {
			gaps++
		}
	}

	// 基本信息里要说清哪些是人填的、哪些是采的
	basic := []gin.H{
		{"name": "名称", "value": host.Name, "source": "人工登记"},
		{"name": "地址", "value": fmt.Sprintf("%s:%d", host.Address, host.Port), "source": "人工登记"},
		{"name": "环境", "value": host.Env, "source": "人工登记"},
		{"name": "标签", "value": orDefault(host.Tags, "无"), "source": "人工登记"},
		{"name": "登录账号", "value": host.Username, "source": "人工登记"},
		{"name": "系统信息", "value": orDefault(host.OSInfo, "未采集"),
			"source": "SSH 采集（uname -srm，只在有人点「检查」时更新，没有定时任务）"},
		{"name": "连通状态", "value": host.Status, "source": "同上，时间见下"},
		{"name": "最近检查", "value": formatTimePtr(host.CheckedAt), "source": "人工触发"},
	}

	var asset model.FixedAsset
	if err := h.DB.Where("host_id = ?", host.ID).First(&asset).Error; err == nil {
		basic = append(basic,
			gin.H{"name": "序列号", "value": orDefault(asset.SN, "未登记"), "source": "固定资产台账（人工登记）"},
			gin.H{"name": "型号 / 厂商", "value": strings.TrimSpace(asset.Model + " " + asset.Vendor), "source": "同上"},
		)
	}

	response.OK(c, gin.H{
		"host": gin.H{
			"id": host.ID, "name": host.Name, "address": host.Address,
			"env": host.Env, "status": host.Status,
		},
		"generatedAt":   time.Now(),
		"basic":         basic,
		"sections":      sections,
		"problemCount":  problems,
		"gapCount":      gaps,
		"notCollected":  hostReportNotCollected,
		"staleAfterHrs": hostReportStaleHours,
		"notes": []string{
			"这份报告是**已有巡检数据的汇总**，不是一次新的体检 —— 点开它不会触发任何 SSH 连接",
			"平台没有常驻 agent：所有主机侧事实都来自 SSH 定时采样，间隔从 5 分钟到 30 分钟不等，每一段都标了自己的 checkedAt",
			"「没有数据」和「数据正常」分开显示：某一段显示灰色说明这台主机压根没登记那类巡检，不是检查通过",
			"漏洞、补丁、软件包、进程与端口清单、失败登录**一次都没采过**，页面底部逐条列出原因 —— 不用「未发现」这种读起来像结论的措辞",
			"防火墙那一段没有定时任务，数据新旧取决于最近一次有人打开防火墙页",
			fmt.Sprintf("超过 %d 小时没更新的段落会被标成「已过期」", hostReportStaleHours),
		},
	})
}
