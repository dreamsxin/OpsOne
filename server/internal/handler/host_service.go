package handler

import (
	"context"
	"fmt"
	"log"
	"sort"
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

// 主机服务管理。
//
// 立场：**平台库不是真相，真机才是**。所以：
//   - 机器上几百个 unit 不全量落库（那是噪音），只存人纳管的那几个 + 它们的期望态；
//   - 没纳管的 unit 通过「发现服务」实时列出，不落库；
//   - 每次巡检都现读真机状态来对照期望态，不一致就是漂移并产告警；
//   - 启停一律走 RunOnHosts，因此自动继承命令规则拦截、生产主机二次确认与拦截留痕；
//   - 动作前后各读一次真机状态存进留痕 —— 光记「执行了 restart」回答不了「到底起来了没有」。
//
// 不做：非 systemd 的 init 系统（sysvinit / OpenRC）、容器内进程（那是容器平台的事）、
// 服务安装与卸载（那是发布的事）。

const (
	// CfgServiceSudo 非 root 账号下是否自动加 sudo -n。
	// systemctl 的读操作（list-units / show）普通用户可以做，start/stop 这类必须 root
	CfgServiceSudo = "service.sudo"

	serviceAlertSourceName = "服务巡检"
	// serviceCollectTimeout 单台采集超时。要跑 systemctl + ss + 逐个读 /proc/<pid>/cgroup
	serviceCollectTimeout = 20 * time.Second
	// serviceActionTimeout 启停超时。restart 慢的服务（数据库）要留足时间
	serviceActionTimeout = 60
	// serviceCollectConcurrency 巡检并发，与批量执行是独立的池子
	serviceCollectConcurrency = 8
)

var serviceDriftLabels = map[string]string{
	"ok": "一致", "inactive": "该跑没跑", "unexpected": "该停在跑",
	"disabled": "该自启没自启", "missing": "unit 不存在", "unknown": "未巡检", "error": "巡检失败",
}

// serviceActions 允许的动作 → 是否属于「会让服务不可用」的高危动作
var serviceActions = map[string]bool{
	"start": false, "restart": true, "reload": false,
	"stop": true, "enable": false, "disable": true,
}

// enableStateOK 这些 UnitFileState 视为「已满足开机自启」。
// static / indirect / generated / transient 本来就不能 enable，不算漂移。
var enableStateOK = map[string]bool{
	"enabled": true, "enabled-runtime": true, "static": true,
	"indirect": true, "generated": true, "transient": true, "alias": true,
}

// hostServiceCommand 采集命令模板。
//
// 只用 systemctl / ss / /proc，POSIX sh 即可。输出按前缀分类，Go 侧解析：
//
//	U|unit|load|active|sub|description   来自 list-units --all（含没在跑的）
//	F|unit|unitFileState                 来自 list-unit-files（开机自启状态）
//	P|port                               监听端口（不带进程归属也要记，用来判断「是权限问题还是真没端口」）
//	L|port|pid                           监听端口 → 持有它的进程
//	C|pid|unit                           进程 → 所属 unit（读 /proc/<pid>/cgroup）
//
// 端口归属走 cgroup 而不是 MainPID：nginx 这类 master/worker 模型下监听套接字
// 由 worker 持有，只看 MainPID 会把端口漏掉。
//
// %s 是 ss 的 sudo 前缀：`ss -p` 要看别人家的进程必须 root，普通账号跑出来
// 只有端口没有 pid。systemctl 的读操作不需要 root，所以不加前缀。
const hostServiceCommandTmpl = `LC_ALL=C
command -v systemctl >/dev/null 2>&1 || { echo "NOSYSTEMD"; exit 0; }
systemctl list-units --type=service --all --no-pager --no-legend --plain 2>/dev/null | while read -r unit load active sub rest; do
  [ -n "$unit" ] && echo "U|$unit|$load|$active|$sub|$rest"
done
systemctl list-unit-files --type=service --no-pager --no-legend 2>/dev/null | while read -r unit state rest; do
  [ -n "$unit" ] && echo "F|$unit|$state"
done
%[1]sss -lntpH 2>/dev/null | while read -r st rq sq local rest; do
  port=$(printf '%%s' "$local" | sed 's/.*://')
  echo "P|$port"
  printf '%%s' "$rest" | tr ',' '\n' | sed -n 's/.*pid=\([0-9]*\).*/\1/p' | while read -r p; do
    echo "L|$port|$p"
  done
done
%[1]sss -lntpH 2>/dev/null | sed -n 's/.*pid=\([0-9]*\).*/\1/p' | sort -u | while read -r p; do
  u=$(grep -o '[a-zA-Z0-9@:._\\-]*\.service' /proc/$p/cgroup 2>/dev/null | head -n1)
  [ -n "$u" ] && echo "C|$p|$u"
done
# 采集脚本的退出码必须是 0：最后那个 while 循环的最后一次迭代可能以「条件不成立」
# 结束，会把整个脚本的退出码带成 1，SSH 层就会把这次成功的采集判成失败
exit 0`

// unitState 一个 unit 的真机实际态
type unitState struct {
	Unit        string `json:"unit"`
	LoadState   string `json:"loadState"`
	ActiveState string `json:"activeState"`
	SubState    string `json:"subState"`
	Description string `json:"description"`
	EnableState string `json:"enableState"`
	Ports       []int  `json:"ports"`
	PIDs        []int  `json:"pids"`
}

// serviceSnapshot 一次采集的结果
type serviceSnapshot struct {
	// Systemd 目标机器是否有 systemd。false 时 Units 为空，调用方要如实告诉用户
	Systemd bool
	Units   map[string]*unitState
	// ListenPorts 采到的全部监听端口（不论能否归到某个 unit）
	ListenPorts []int
	// PortsUnavailable 看到了监听端口却一个进程归属都拿不到 —— 几乎一定是权限问题：
	// `ss -p` 要看别人家的进程必须 root。这种情况必须说出来，不能让端口列显示成「-」
	// 让人以为服务真的没在听端口。
	PortsUnavailable bool
}

func (s *serviceSnapshot) unit(name string) *unitState {
	if s.Units == nil {
		s.Units = map[string]*unitState{}
	}
	if u, ok := s.Units[name]; ok {
		return u
	}
	u := &unitState{Unit: name, Ports: []int{}, PIDs: []int{}}
	s.Units[name] = u
	return u
}

// parseHostServices 解析采集输出。
//
// 解析刻意宽容：某一段命令在目标机器上不可用（没有 ss、读不到 /proc/<pid>/cgroup）
// 只让对应信息缺失，不让整次采集失败。只有「连 systemctl 都没有」才是硬失败。
func parseHostServices(raw string) serviceSnapshot {
	snap := serviceSnapshot{Units: map[string]*unitState{}}
	pidPorts := map[int][]int{}
	pidUnit := map[int]string{}
	listen := []int{}

	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "NOSYSTEMD" {
			return serviceSnapshot{Systemd: false, Units: map[string]*unitState{}}
		}
		parts := strings.Split(line, "|")
		if len(parts) < 2 {
			continue
		}
		switch parts[0] {
		case "U":
			if len(parts) < 5 {
				continue
			}
			snap.Systemd = true
			u := snap.unit(parts[1])
			u.LoadState, u.ActiveState, u.SubState = parts[2], parts[3], parts[4]
			if len(parts) >= 6 {
				u.Description = truncate(strings.TrimSpace(parts[5]), 250)
			}
		case "F":
			if len(parts) < 3 {
				continue
			}
			snap.Systemd = true
			snap.unit(parts[1]).EnableState = parts[2]
		case "P":
			if port, err := strconv.Atoi(parts[1]); err == nil && port > 0 {
				listen = append(listen, port)
			}
		case "L":
			if len(parts) < 3 {
				continue
			}
			port, err1 := strconv.Atoi(parts[1])
			pid, err2 := strconv.Atoi(parts[2])
			if err1 != nil || err2 != nil || port <= 0 {
				continue
			}
			pidPorts[pid] = append(pidPorts[pid], port)
		case "C":
			if len(parts) < 3 {
				continue
			}
			if pid, err := strconv.Atoi(parts[1]); err == nil {
				pidUnit[pid] = parts[2]
			}
		}
	}

	// 把 端口→PID→unit 三段拼起来
	for pid, ports := range pidPorts {
		name, ok := pidUnit[pid]
		if !ok {
			continue
		}
		u := snap.unit(name)
		u.PIDs = append(u.PIDs, pid)
		u.Ports = append(u.Ports, ports...)
	}
	for _, u := range snap.Units {
		u.Ports = uniqueSortedInts(u.Ports)
		u.PIDs = uniqueSortedInts(u.PIDs)
	}
	snap.ListenPorts = uniqueSortedInts(listen)
	// 有端口、却一个 pid 都没解析出来 = 权限不足（ss -p 对非自己的进程要 root）
	snap.PortsUnavailable = len(snap.ListenPorts) > 0 && len(pidPorts) == 0
	return snap
}

func uniqueSortedInts(items []int) []int {
	seen := map[int]bool{}
	out := make([]int, 0, len(items))
	for _, v := range items {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	sort.Ints(out)
	return out
}

func joinInts(items []int) string {
	parts := make([]string, 0, len(items))
	for _, v := range items {
		parts = append(parts, strconv.Itoa(v))
	}
	return strings.Join(parts, ",")
}

func (h *Handler) serviceSudo() bool {
	return h.configString(CfgServiceSudo, "true") != "false"
}

// collectHostServices 现读一台主机的服务状态
func (h *Handler) collectHostServices(ctx context.Context, host *model.Host) (serviceSnapshot, sshx.Result) {
	prefix := ""
	if h.serviceSudo() && host.Username != "root" {
		prefix = "sudo -n "
	}
	command := fmt.Sprintf(hostServiceCommandTmpl, prefix)

	runCtx, cancel := context.WithTimeout(ctx, serviceCollectTimeout)
	defer cancel()
	res := sshx.Run(runCtx, h.target(host), command)
	if res.Status != "success" {
		// 读不到就是读不到，绝不返回空清单冒充「这台机器没有服务」
		return serviceSnapshot{Units: map[string]*unitState{}}, res
	}
	snap := parseHostServices(res.Stdout)
	// sudo 不可用时 ss 那两段会整段失败，端口就全缺；退回不带 sudo 再试一次，
	// 至少把「有哪些端口在听」拿到，同时把「归不到服务」如实标出来
	if prefix != "" && len(snap.ListenPorts) == 0 {
		retryCtx, cancel2 := context.WithTimeout(ctx, serviceCollectTimeout)
		defer cancel2()
		retry := sshx.Run(retryCtx, h.target(host), fmt.Sprintf(hostServiceCommandTmpl, ""))
		if retry.Status == "success" {
			if fallback := parseHostServices(retry.Stdout); len(fallback.ListenPorts) > 0 {
				return fallback, retry
			}
		}
	}
	return snap, res
}

// portsNote 端口采集的口径说明。拿不到归属时必须说清原因，
// 否则端口列空着会被当成「服务没在听端口」。
func portsNote(snap serviceSnapshot) string {
	if snap.PortsUnavailable {
		return fmt.Sprintf("读到 %d 个监听端口，但拿不到进程归属：ss -p 要看别人家的进程必须 root。"+
			"给主机账号配免密 sudo 或改用 root 账号后端口才会归到服务上", len(snap.ListenPorts))
	}
	return "端口按 /proc/<pid>/cgroup 反查（master/worker 模型也能归对）；读不到 cgroup 的进程端口会缺"
}

// ---------- 漂移判定 ----------

// evalDrift 拿实际态对照期望态。state 为 nil 表示机器上没有这个 unit。
func evalDrift(svc model.HostService, state *unitState) (string, string) {
	if state == nil || state.LoadState == "not-found" {
		return "missing", "机器上找不到这个 unit（可能被卸载或改名了）"
	}
	if state.LoadState == "masked" {
		return "missing", "unit 被 masked，systemd 不会加载它"
	}
	if svc.ExpectActive && state.ActiveState != "active" {
		detail := fmt.Sprintf("期望在运行，实际 %s/%s", state.ActiveState, state.SubState)
		return "inactive", detail
	}
	if !svc.ExpectActive && state.ActiveState == "active" {
		return "unexpected", "期望停着，实际在运行"
	}
	if svc.ExpectEnabled && state.EnableState != "" && !enableStateOK[state.EnableState] {
		return "disabled", fmt.Sprintf("期望开机自启，实际 %s", state.EnableState)
	}
	return "ok", ""
}

// applyServiceState 把实际态写回一条纳管服务，返回漂移是否发生了变化
func (h *Handler) applyServiceState(svc *model.HostService, state *unitState, now time.Time) bool {
	before := svc.Drift
	drift, detail := evalDrift(*svc, state)

	updates := map[string]any{
		"drift": drift, "drift_detail": detail, "last_check_at": &now, "last_error": "",
	}
	if state != nil {
		updates["load_state"] = state.LoadState
		updates["active_state"] = state.ActiveState
		updates["sub_state"] = state.SubState
		updates["enable_state"] = state.EnableState
		updates["ports"] = truncate(joinInts(state.Ports), 250)
		updates["proc_count"] = len(state.PIDs)
		svc.LoadState, svc.ActiveState = state.LoadState, state.ActiveState
		svc.SubState, svc.EnableState = state.SubState, state.EnableState
		svc.Ports, svc.ProcCount = joinInts(state.Ports), len(state.PIDs)
	} else {
		updates["active_state"], updates["sub_state"] = "", ""
		updates["ports"], updates["proc_count"] = "", 0
		svc.ActiveState, svc.SubState, svc.Ports, svc.ProcCount = "", "", "", 0
	}
	svc.Drift, svc.DriftDetail, svc.LastCheckAt = drift, detail, &now
	h.DB.Model(&model.HostService{}).Where("id = ?", svc.ID).Updates(updates)
	return before != drift
}

// markServiceCheckFailed 整台机器采集失败：不改实际态字段（保留上次的真值），
// 只标 error 并记下原因。把状态清空会让人以为「服务停了」，那是撒谎。
func (h *Handler) markServiceCheckFailed(svc *model.HostService, reason string, now time.Time) {
	svc.Drift, svc.DriftDetail, svc.LastError, svc.LastCheckAt = "error", "采集失败，实际态沿用上次结果", truncate(reason, 250), &now
	h.DB.Model(&model.HostService{}).Where("id = ?", svc.ID).Updates(map[string]any{
		"drift": "error", "drift_detail": svc.DriftDetail,
		"last_error": svc.LastError, "last_check_at": &now,
	})
}

// ---------- 告警 ----------

var serviceAlertKinds = []string{"inactive", "unexpected", "disabled", "missing", "error"}

func serviceAlertFingerprint(serviceID uint, kind string) string {
	return fmt.Sprintf("service:%d:%s", serviceID, kind)
}

// syncServiceAlerts 漂移产告警、恢复发 resolved，走既有通知路由
func (h *Handler) syncServiceAlerts(svc model.HostService, host model.Host) {
	if !svc.AlertEnabled {
		return
	}
	source, err := h.internalAlertSource(serviceAlertSourceName)
	if err != nil {
		log.Printf("[service] 内部告警源不可用，跳过告警: %v", err)
		return
	}

	labels := map[string]string{
		"module":    "service",
		"serviceId": strconv.FormatUint(uint64(svc.ID), 10),
		"host":      host.Name,
		"unit":      svc.Unit,
	}
	severity := "warning"
	if svc.Critical {
		severity = "critical"
	}

	fire := func(kind, title, summary string) {
		h.ingestAlert(source, alertPayload{
			Title: title, Summary: summary, Severity: severity,
			Fingerprint: serviceAlertFingerprint(svc.ID, kind), Labels: labels,
		})
	}
	resolve := func(kind string) {
		h.ingestAlert(source, alertPayload{
			Title: "服务恢复：" + svc.Unit, Status: "resolved",
			Fingerprint: serviceAlertFingerprint(svc.ID, kind), Labels: labels,
		})
	}

	switch svc.Drift {
	case "inactive":
		fire("inactive", fmt.Sprintf("服务未运行：%s@%s", svc.Unit, host.Name),
			fmt.Sprintf("%s 上的 %s %s。负责人 %s", host.Name, svc.Unit, svc.DriftDetail, orDash(svc.Owner)))
	case "unexpected":
		fire("unexpected", fmt.Sprintf("服务不该在跑：%s@%s", svc.Unit, host.Name),
			fmt.Sprintf("%s 上的 %s 登记为「应停止」，但实际在运行", host.Name, svc.Unit))
	case "disabled":
		fire("disabled", fmt.Sprintf("服务未设开机自启：%s@%s", svc.Unit, host.Name),
			fmt.Sprintf("%s 上的 %s %s，机器重启后不会自动拉起", host.Name, svc.Unit, svc.DriftDetail))
	case "missing":
		fire("missing", fmt.Sprintf("服务不存在：%s@%s", svc.Unit, host.Name),
			fmt.Sprintf("%s 上找不到 %s：%s", host.Name, svc.Unit, svc.DriftDetail))
	case "error":
		fire("error", fmt.Sprintf("服务巡检失败：%s@%s", svc.Unit, host.Name),
			fmt.Sprintf("%s 采集失败：%s", host.Name, svc.LastError))
	default:
		for _, kind := range serviceAlertKinds {
			resolve(kind)
		}
		return
	}
	// 只清掉其它类别，当前这条留着
	for _, kind := range serviceAlertKinds {
		if kind != svc.Drift {
			resolve(kind)
		}
	}
}

// ---------- 巡检 ----------

// checkHostServices 巡检一台主机上所有纳管服务
func (h *Handler) checkHostServices(ctx context.Context, host model.Host) (int, int) {
	var services []model.HostService
	h.DB.Where("host_id = ?", host.ID).Find(&services)
	if len(services) == 0 {
		return 0, 0
	}

	snap, res := h.collectHostServices(ctx, &host)
	now := time.Now()
	drifted := 0

	if res.Status != "success" || !snap.Systemd {
		reason := strings.TrimSpace(res.Stderr)
		if reason == "" {
			if !snap.Systemd && res.Status == "success" {
				reason = "目标机器没有 systemd，服务管理不适用"
			} else {
				reason = "采集命令返回 " + res.Status
			}
		}
		for i := range services {
			h.markServiceCheckFailed(&services[i], reason, now)
			h.syncServiceAlerts(services[i], host)
			drifted++
		}
		return len(services), drifted
	}

	for i := range services {
		h.applyServiceState(&services[i], snap.Units[services[i].Unit], now)
		h.syncServiceAlerts(services[i], host)
		if services[i].Drift != "ok" {
			drifted++
		}
	}
	return len(services), drifted
}

// CheckServicesForSchedule 定时巡检：扫所有有纳管服务的主机
func (h *Handler) CheckServicesForSchedule() {
	var hostIDs []uint
	if err := h.DB.Model(&model.HostService{}).Distinct().Pluck("host_id", &hostIDs).Error; err != nil {
		log.Printf("[service] 巡检对象查询失败: %v", err)
		return
	}
	if len(hostIDs) == 0 {
		h.markFixedRun("service", "没有纳管的服务")
		return
	}
	var hosts []model.Host
	h.DB.Where("id IN ?", hostIDs).Find(&hosts)

	total, drifted := 0, 0
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, serviceCollectConcurrency)
	ctx := context.Background()

	for i := range hosts {
		wg.Add(1)
		go func(host model.Host) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			n, d := h.checkHostServices(ctx, host)
			mu.Lock()
			total += n
			drifted += d
			mu.Unlock()
		}(hosts[i])
	}
	wg.Wait()

	h.markFixedRun("service", fmt.Sprintf("%d 台主机、%d 个服务，漂移 %d", len(hosts), total, drifted))
	if drifted > 0 {
		log.Printf("[service] 巡检完成: %d 个服务，其中 %d 个漂移", total, drifted)
	}
}

// ---------- 接口 ----------

func serviceView(svc model.HostService, hostName string) gin.H {
	ports := []string{}
	if svc.Ports != "" {
		ports = strings.Split(svc.Ports, ",")
	}
	return gin.H{
		"id": svc.ID, "hostId": svc.HostID, "hostName": hostName,
		"unit": svc.Unit, "name": svc.Name, "description": svc.Description,
		"expectActive": svc.ExpectActive, "expectEnabled": svc.ExpectEnabled,
		"critical": svc.Critical, "owner": svc.Owner,
		"alertEnabled": svc.AlertEnabled, "remark": svc.Remark,
		"loadState": svc.LoadState, "activeState": svc.ActiveState,
		"subState": svc.SubState, "enableState": svc.EnableState,
		"ports": ports, "procCount": svc.ProcCount,
		"drift": svc.Drift, "driftLabel": serviceDriftLabels[svc.Drift],
		"driftDetail": svc.DriftDetail,
		"lastCheckAt": svc.LastCheckAt, "lastError": svc.LastError,
		"createdAt": svc.CreatedAt, "updatedAt": svc.UpdatedAt,
	}
}

func (h *Handler) hostNames(ids []uint) map[uint]string {
	names := map[uint]string{}
	if len(ids) == 0 {
		return names
	}
	var hosts []model.Host
	h.DB.Where("id IN ?", ids).Find(&hosts)
	for _, host := range hosts {
		names[host.ID] = host.Name
	}
	return names
}

func (h *Handler) ListHostServices(c *gin.Context) {
	page, size := pageParams(c)
	q := h.DB.Model(&model.HostService{})
	if hostID := c.Query("hostId"); hostID != "" {
		q = q.Where("host_id = ?", parseUint(hostID))
	}
	if drift := c.Query("drift"); drift != "" {
		if drift == "problem" {
			q = q.Where("drift NOT IN ?", []string{"ok", "unknown"})
		} else {
			q = q.Where("drift = ?", drift)
		}
	}
	if c.Query("critical") == "1" {
		q = q.Where("critical = ?", true)
	}
	if owner := c.Query("owner"); owner != "" {
		q = q.Where("owner = ?", owner)
	}
	if kw := strings.TrimSpace(c.Query("keyword")); kw != "" {
		like := "%" + kw + "%"
		q = q.Where("unit LIKE ? OR name LIKE ? OR description LIKE ?", like, like, like)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		response.Error(c, "查询服务失败")
		return
	}
	var list []model.HostService
	if err := q.Order("critical desc, id asc").Offset((page - 1) * size).Limit(size).
		Find(&list).Error; err != nil {
		response.Error(c, "查询服务失败")
		return
	}

	ids := make([]uint, 0, len(list))
	for _, svc := range list {
		ids = append(ids, svc.HostID)
	}
	names := h.hostNames(ids)
	views := make([]gin.H, 0, len(list))
	for _, svc := range list {
		views = append(views, serviceView(svc, names[svc.HostID]))
	}
	response.OKPage(c, views, total, page, size)
}

func (h *Handler) HostServiceStats(c *gin.Context) {
	count := func(where string, args ...any) int64 {
		var n int64
		q := h.DB.Model(&model.HostService{})
		if where != "" {
			q = q.Where(where, args...)
		}
		q.Count(&n)
		return n
	}
	var hostCount int64
	h.DB.Model(&model.HostService{}).Distinct("host_id").Count(&hostCount)

	response.OK(c, gin.H{
		"total": count(""), "hosts": hostCount,
		"ok":         count("drift = ?", "ok"),
		"inactive":   count("drift = ?", "inactive"),
		"unexpected": count("drift = ?", "unexpected"),
		"disabled":   count("drift = ?", "disabled"),
		"missing":    count("drift = ?", "missing"),
		"error":      count("drift = ?", "error"),
		"unknown":    count("drift = ?", "unknown"),
		"critical":   count("critical = ?", true),
		// criticalDrift 关键服务里正在漂移的，这是最该先看的数字
		"criticalDrift": count("critical = ? AND drift NOT IN ?", true, []string{"ok", "unknown"}),
	})
}

// DiscoverHostServices 实时列出目标机器上的所有 service unit，供人勾选纳管。
// 不落库：机器上有几百个 unit，全存下来只是噪音。
func (h *Handler) DiscoverHostServices(c *gin.Context) {
	var host model.Host
	if err := h.DB.First(&host, idParam(c)).Error; err != nil {
		response.NotFound(c, "主机不存在")
		return
	}

	snap, res := h.collectHostServices(c.Request.Context(), &host)
	if res.Status != "success" {
		detail := strings.TrimSpace(res.Stderr)
		if detail == "" {
			detail = "采集命令返回 " + res.Status
		}
		response.Error(c, "读取服务清单失败："+truncate(detail, 200))
		return
	}
	if !snap.Systemd {
		response.BadRequest(c, "目标机器上没有 systemctl，服务管理只支持 systemd 主机")
		return
	}

	var managed []model.HostService
	h.DB.Where("host_id = ?", host.ID).Find(&managed)
	managedSet := map[string]bool{}
	for _, svc := range managed {
		managedSet[svc.Unit] = true
	}

	units := make([]gin.H, 0, len(snap.Units))
	for _, u := range snap.Units {
		units = append(units, gin.H{
			"unit": u.Unit, "loadState": u.LoadState, "activeState": u.ActiveState,
			"subState": u.SubState, "enableState": u.EnableState,
			"description": u.Description, "ports": u.Ports, "procCount": len(u.PIDs),
			"managed": managedSet[u.Unit],
		})
	}
	// 在跑的、带端口的排前面：这些最可能是需要纳管的业务服务
	sort.SliceStable(units, func(i, j int) bool {
		scoreOf := func(v gin.H) int {
			score := 0
			if ports, ok := v["ports"].([]int); ok && len(ports) > 0 {
				score += 4
			}
			if v["activeState"] == "active" {
				score += 2
			}
			if v["activeState"] == "failed" {
				score += 3
			}
			return score
		}
		si, sj := scoreOf(units[i]), scoreOf(units[j])
		if si != sj {
			return si > sj
		}
		return fmt.Sprint(units[i]["unit"]) < fmt.Sprint(units[j]["unit"])
	})

	// 归不到任何 unit 的监听端口：这恰恰是运维要盯的东西 ——
	// 手工起的进程、脱管的容器、别人偷偷跑的服务都在这里
	attributed := map[int]bool{}
	for _, u := range snap.Units {
		for _, p := range u.Ports {
			attributed[p] = true
		}
	}
	orphan := make([]int, 0, 4)
	for _, p := range snap.ListenPorts {
		if !attributed[p] {
			orphan = append(orphan, p)
		}
	}

	response.OK(c, gin.H{
		"hostId": host.ID, "hostName": host.Name,
		"units": units, "total": len(units),
		"listenPorts":      snap.ListenPorts,
		"orphanPorts":      orphan,
		"portsUnavailable": snap.PortsUnavailable,
		"note":             "实时读取，不落库。" + portsNote(snap),
		"orphanNote": "「无归属端口」是在听但不属于任何 systemd service 的端口：" +
			"手工起的进程、脱管的容器、或者被别人偷偷跑起来的东西",
	})
}

type hostServiceReq struct {
	HostID uint     `json:"hostId"`
	Units  []string `json:"units"`
	// 以下为批量纳管时的共同设置
	ExpectActive  *bool  `json:"expectActive"`
	ExpectEnabled *bool  `json:"expectEnabled"`
	Critical      bool   `json:"critical"`
	Owner         string `json:"owner"`
	Remark        string `json:"remark"`
}

// AdoptHostServices 纳管一批 unit。已纳管的跳过，不覆盖已有的期望态设置。
func (h *Handler) AdoptHostServices(c *gin.Context) {
	var req hostServiceReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "参数格式有误")
		return
	}
	var host model.Host
	if err := h.DB.First(&host, req.HostID).Error; err != nil {
		response.BadRequest(c, "主机不存在")
		return
	}
	if len(req.Units) == 0 {
		response.BadRequest(c, "请至少选择一个服务")
		return
	}
	if len(req.Units) > 50 {
		response.BadRequest(c, "一次最多纳管 50 个服务")
		return
	}
	if req.Owner != "" {
		var user model.User
		if err := h.DB.Where("username = ?", req.Owner).First(&user).Error; err != nil {
			response.BadRequest(c, "负责人不存在: "+req.Owner)
			return
		}
	}

	// 纳管时就地采一次，直接把实际态与漂移填上 —— 否则列表上一片「未巡检」，
	// 用户会以为功能没生效
	snap, res := h.collectHostServices(c.Request.Context(), &host)
	collectOK := res.Status == "success" && snap.Systemd

	user := middleware.CurrentUser(c)
	now := time.Now()
	created, skipped := 0, 0
	for _, unit := range req.Units {
		unit = strings.TrimSpace(unit)
		if unit == "" {
			continue
		}
		var exist model.HostService
		if err := h.DB.Where("host_id = ? AND unit = ?", host.ID, unit).First(&exist).Error; err == nil {
			skipped++
			continue
		}
		svc := model.HostService{
			HostID: host.ID, Unit: truncate(unit, 128),
			Name:          truncate(strings.TrimSuffix(unit, ".service"), 64),
			ExpectActive:  true,
			ExpectEnabled: true,
			Critical:      req.Critical,
			Owner:         req.Owner,
			AlertEnabled:  true,
			Remark:        truncate(req.Remark, 250),
			Drift:         "unknown",
			CreatedBy:     user.ID,
		}
		if req.ExpectActive != nil {
			svc.ExpectActive = *req.ExpectActive
		}
		if req.ExpectEnabled != nil {
			svc.ExpectEnabled = *req.ExpectEnabled
		}
		if state := snap.Units[unit]; state != nil {
			svc.Description = state.Description
		}
		if err := h.DB.Create(&svc).Error; err != nil {
			continue
		}
		created++
		if collectOK {
			h.applyServiceState(&svc, snap.Units[unit], now)
			h.syncServiceAlerts(svc, host)
		}
	}

	body := gin.H{"created": created, "skipped": skipped}
	if !collectOK {
		body["note"] = "已纳管，但这次没采到真机状态（" + truncate(strings.TrimSpace(res.Stderr), 120) + "），列表里先显示未巡检"
	}
	response.OK(c, body)
}

type hostServiceUpdateReq struct {
	Name          string `json:"name"`
	ExpectActive  *bool  `json:"expectActive"`
	ExpectEnabled *bool  `json:"expectEnabled"`
	Critical      *bool  `json:"critical"`
	AlertEnabled  *bool  `json:"alertEnabled"`
	Owner         string `json:"owner"`
	Remark        string `json:"remark"`
}

// UpdateHostService 改期望态。改完立刻重新判一次漂移：
// 把「期望在跑」改成「期望停着」之后，漂移状态必须当场跟着变，否则等于没生效。
func (h *Handler) UpdateHostService(c *gin.Context) {
	var svc model.HostService
	if err := h.DB.First(&svc, idParam(c)).Error; err != nil {
		response.NotFound(c, "服务不存在")
		return
	}
	var req hostServiceUpdateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "参数格式有误")
		return
	}
	if req.Owner != "" {
		var user model.User
		if err := h.DB.Where("username = ?", req.Owner).First(&user).Error; err != nil {
			response.BadRequest(c, "负责人不存在: "+req.Owner)
			return
		}
	}

	updates := map[string]any{"owner": req.Owner, "remark": truncate(req.Remark, 250)}
	if req.Name != "" {
		updates["name"] = truncate(req.Name, 64)
		svc.Name = req.Name
	}
	if req.ExpectActive != nil {
		updates["expect_active"] = *req.ExpectActive
		svc.ExpectActive = *req.ExpectActive
	}
	if req.ExpectEnabled != nil {
		updates["expect_enabled"] = *req.ExpectEnabled
		svc.ExpectEnabled = *req.ExpectEnabled
	}
	if req.Critical != nil {
		updates["critical"] = *req.Critical
		svc.Critical = *req.Critical
	}
	if req.AlertEnabled != nil {
		updates["alert_enabled"] = *req.AlertEnabled
		svc.AlertEnabled = *req.AlertEnabled
	}
	if err := h.DB.Model(&model.HostService{}).Where("id = ?", svc.ID).Updates(updates).Error; err != nil {
		response.Error(c, "更新失败")
		return
	}

	// 期望态变了，用上次采到的实际态重新判一次漂移（不重新 SSH，避免改个备注就连机器）
	if svc.LastCheckAt != nil && svc.Drift != "error" {
		state := &unitState{
			Unit: svc.Unit, LoadState: svc.LoadState, ActiveState: svc.ActiveState,
			SubState: svc.SubState, EnableState: svc.EnableState,
		}
		if svc.LoadState == "" {
			state = nil
		}
		drift, detail := evalDrift(svc, state)
		h.DB.Model(&model.HostService{}).Where("id = ?", svc.ID).
			Updates(map[string]any{"drift": drift, "drift_detail": detail})
		svc.Drift, svc.DriftDetail = drift, detail
		var host model.Host
		if h.DB.First(&host, svc.HostID).Error == nil {
			h.syncServiceAlerts(svc, host)
		}
	}
	response.OK(c, gin.H{"drift": svc.Drift, "driftDetail": svc.DriftDetail})
}

// DeleteHostService 取消纳管。留痕不删：要回答「当时谁动过这个服务」。
func (h *Handler) DeleteHostService(c *gin.Context) {
	var svc model.HostService
	if err := h.DB.First(&svc, idParam(c)).Error; err != nil {
		response.NotFound(c, "服务不存在")
		return
	}
	if err := h.DB.Delete(&model.HostService{}, svc.ID).Error; err != nil {
		response.Error(c, "删除失败")
		return
	}
	// 取消纳管后不该继续报警
	var host model.Host
	if h.DB.First(&host, svc.HostID).Error == nil {
		svc.Drift = "ok"
		h.syncServiceAlerts(svc, host)
	}
	response.OK(c, gin.H{"note": "已取消纳管，启停留痕保留"})
}

// CheckHostServices 手动巡检：按主机或按单个服务
func (h *Handler) CheckHostServices(c *gin.Context) {
	hostID := uint(parseUint(c.Query("hostId")))
	if hostID == 0 {
		response.BadRequest(c, "请指定 hostId")
		return
	}
	var host model.Host
	if err := h.DB.First(&host, hostID).Error; err != nil {
		response.NotFound(c, "主机不存在")
		return
	}
	total, drifted := h.checkHostServices(c.Request.Context(), host)
	response.OK(c, gin.H{"total": total, "drifted": drifted})
}

// ---------- 启停 ----------

type serviceActionReq struct {
	Action string `json:"action" binding:"required"`
	// Confirm 关键服务做 stop / disable / restart 时必须填 unit 名，防手滑
	Confirm string `json:"confirm"`
	// ConfirmProd 目标是生产主机时必须为 true，由下发闸门判定
	ConfirmProd bool `json:"confirmProd"`
}

// OperateHostService 启停服务。
//
// 命令走 RunOnHosts，所以自动带上：命令规则拦截、生产主机二次确认、被拦留痕。
// 动作前后各读一次真机状态存进 HostServiceAction —— 光记「执行了 restart」
// 回答不了「到底起来了没有」。
func (h *Handler) OperateHostService(c *gin.Context) {
	var svc model.HostService
	if err := h.DB.First(&svc, idParam(c)).Error; err != nil {
		response.NotFound(c, "服务不存在")
		return
	}
	var host model.Host
	if err := h.DB.First(&host, svc.HostID).Error; err != nil {
		response.BadRequest(c, "主机不存在")
		return
	}
	var req serviceActionReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "请指定动作")
		return
	}
	risky, ok := serviceActions[req.Action]
	if !ok {
		response.BadRequest(c, "动作只能是 start / stop / restart / reload / enable / disable")
		return
	}
	// 关键服务上的高危动作要求把 unit 名抄一遍：这类操作出事概率最高。
	// 被拒的尝试也要留痕 —— 「谁想停这个关键服务」本身就是需要留下的信息。
	if svc.Critical && risky && strings.TrimSpace(req.Confirm) != svc.Unit {
		reason := fmt.Sprintf("「%s」是关键服务，执行 %s 请把 unit 名抄进确认框：%s", svc.Name, req.Action, svc.Unit)
		h.DB.Create(&model.HostServiceAction{
			HostID: host.ID, HostName: host.Name, ServiceID: svc.ID, Unit: svc.Unit,
			Action: req.Action, Status: "blocked", Detail: "二次确认未通过：" + reason,
			Operator: middleware.CurrentUser(c).Username, ClientIP: c.ClientIP(),
		})
		response.BadRequest(c, reason)
		return
	}

	user := middleware.CurrentUser(c)
	action := model.HostServiceAction{
		HostID: host.ID, HostName: host.Name, ServiceID: svc.ID, Unit: svc.Unit,
		Action: req.Action, Operator: user.Username, ClientIP: c.ClientIP(),
	}

	// 动作前读一次
	ctx := c.Request.Context()
	beforeSnap, _ := h.collectHostServices(ctx, &host)
	action.BeforeState = stateText(beforeSnap.Units[svc.Unit])

	prefix := ""
	if h.serviceSudo() && host.Username != "root" {
		prefix = "sudo -n "
	}
	command := fmt.Sprintf("%ssystemctl %s %s", prefix, req.Action, svc.Unit)

	job, err := h.RunOnHosts(ctx, ExecRequest{
		Name:        fmt.Sprintf("服务%s: %s@%s", req.Action, svc.Unit, host.Name),
		Command:     command,
		Timeout:     serviceActionTimeout,
		HostIDs:     []uint{host.ID},
		UserID:      user.ID,
		Operator:    user.Username,
		Source:      "manual",
		ConfirmProd: req.ConfirmProd,
		ClientIP:    c.ClientIP(),
	})
	if err != nil {
		action.Status, action.Detail = "blocked", truncate(err.Error(), 480)
		h.DB.Create(&action)
		response.BadRequest(c, err.Error())
		return
	}
	action.ExecJobID = job.ID

	var results []model.ExecResult
	h.DB.Where("job_id = ?", job.ID).Find(&results)
	detail := ""
	if len(results) > 0 {
		detail = strings.TrimSpace(results[0].Stdout + "\n" + results[0].Stderr)
	}
	if job.FailedNum > 0 {
		action.Status = "failed"
	} else {
		action.Status = "success"
	}
	action.Detail = truncate(detail, 480)

	// 动作后再读一次，并顺手刷新这台机器上所有纳管服务的状态
	afterSnap, afterRes := h.collectHostServices(ctx, &host)
	now := time.Now()
	if afterRes.Status == "success" && afterSnap.Systemd {
		action.AfterState = stateText(afterSnap.Units[svc.Unit])
		var all []model.HostService
		h.DB.Where("host_id = ?", host.ID).Find(&all)
		for i := range all {
			h.applyServiceState(&all[i], afterSnap.Units[all[i].Unit], now)
			h.syncServiceAlerts(all[i], host)
			if all[i].ID == svc.ID {
				svc = all[i]
			}
		}
	}
	h.DB.Create(&action)

	response.OK(c, gin.H{
		"status": action.Status, "execJobId": job.ID,
		"beforeState": action.BeforeState, "afterState": action.AfterState,
		"drift": svc.Drift, "driftDetail": svc.DriftDetail,
		"detail": action.Detail,
	})
}

func stateText(state *unitState) string {
	if state == nil {
		return "not-found"
	}
	text := state.ActiveState
	if state.SubState != "" {
		text += "/" + state.SubState
	}
	if state.EnableState != "" {
		text += "," + state.EnableState
	}
	return truncate(text, 60)
}

func (h *Handler) ListHostServiceActions(c *gin.Context) {
	page, size := pageParams(c)
	q := h.DB.Model(&model.HostServiceAction{})
	if hostID := c.Query("hostId"); hostID != "" {
		q = q.Where("host_id = ?", parseUint(hostID))
	}
	if serviceID := c.Query("serviceId"); serviceID != "" {
		q = q.Where("service_id = ?", parseUint(serviceID))
	}
	if status := c.Query("status"); status != "" {
		q = q.Where("status = ?", status)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		response.Error(c, "查询留痕失败")
		return
	}
	var list []model.HostServiceAction
	if err := q.Order("id desc").Offset((page - 1) * size).Limit(size).Find(&list).Error; err != nil {
		response.Error(c, "查询留痕失败")
		return
	}
	response.OKPage(c, list, total, page, size)
}
