package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"path"
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

// 主机日志：不依赖任何日志系统，直接 SSH 读主机上的文件。
//
// 与 log_query.go（Loki 查询代理）是两条独立的路：没部署 Loki 的环境里，
// 这里是唯一能看到日志的地方。三件事：
//  1. 实时查看 —— 看末尾 N 行 / grep 关键字，只读、不落库
//  2. 关键字巡检 —— 按 inode + 字节偏移增量读新内容，命中关键字就走告警链
//  3. 日志目录占用 —— 总占用 + 最大的几个文件
//
// 全模块**只跑只读命令**（wc / ls -i / tail / grep / du / find），没有一处会改主机上的东西。

const (
	// hostLogTimeout 单次 SSH 读日志的超时。比连通性探测宽一些：
	// grep 一个几百兆的日志确实要时间，但超过这个数基本就是文件比预期大得多
	hostLogTimeout = 30 * time.Second
	// hostLogScanConcurrency 巡检并发。独立池子，不和批量执行抢名额
	hostLogScanConcurrency = 6
	// hostLogViewMaxBytes 实时查看单次返回的字节上限（1MB）。
	// sshx.Run 对输出**没有**任何大小限制，必须在远端命令上就截断
	hostLogViewMaxBytes = 1 << 20
	// hostLogViewMaxLines 实时查看单次最多取多少行
	hostLogViewMaxLines = 2000
	// hostLogScanMaxBytes 巡检单轮读取新增内容的上限（默认值，可按目标配）
	hostLogScanMaxBytes = 256 * 1024
	// hostLogTopFiles 占用榜列多少条
	hostLogTopFiles = 15
	// hostLogAlertSourceName 内部告警接入源
	hostLogAlertSourceName = "主机日志巡检"
)

// ---------- 路径白名单 ----------

// logPathPrefixes 允许读取的目录前缀。
//
// 这是整个模块唯一真正的安全边界：路径由人在界面上填，没有白名单就等于
// 「有这个页面权限的人可以读主机上任意文件」——包括 /etc/shadow 与主机私钥。
// 默认只放开 /var/log，要加别的目录走 OPS_LOG_PATH_PREFIXES。
func (h *Handler) logPathPrefixes() []string {
	raw := ""
	if h.Cfg != nil {
		raw = h.Cfg.LogPathPrefixes
	}
	if strings.TrimSpace(raw) == "" {
		return []string{"/var/log"}
	}
	var out []string
	for _, p := range strings.Split(raw, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, path.Clean(p))
		}
	}
	if len(out) == 0 {
		return []string{"/var/log"}
	}
	return out
}

// normalizeLogPath 校验并归一化日志路径。
//
// 三道关：必须是绝对路径、Clean 之后不能含 ..（防 /var/log/../../etc/shadow）、
// 必须落在白名单前缀下。Clean 放在前缀判断之前是关键 —— 先判前缀再 Clean 等于没判。
func (h *Handler) normalizeLogPath(raw string) (string, error) {
	p := strings.TrimSpace(raw)
	if p == "" {
		return "", errors.New("请填写日志文件路径")
	}
	if strings.ContainsAny(p, "\x00\n\r") {
		return "", errors.New("路径里不能有换行或空字符")
	}
	if !strings.HasPrefix(p, "/") {
		return "", errors.New("请填写绝对路径")
	}
	clean := path.Clean(p)
	if strings.Contains(clean, "..") {
		return "", errors.New("路径里不能含 ..")
	}

	prefixes := h.logPathPrefixes()
	for _, prefix := range prefixes {
		if clean == prefix || strings.HasPrefix(clean, strings.TrimSuffix(prefix, "/")+"/") {
			return clean, nil
		}
	}
	return "", fmt.Errorf("路径必须在允许的目录下（%s）；要放开别的目录改 OPS_LOG_PATH_PREFIXES",
		strings.Join(prefixes, "、"))
}

// ---------- 实时查看 ----------

type logViewReq struct {
	HostID uint   `json:"hostId" binding:"required"`
	Path   string `json:"path" binding:"required"`
	Lines  int    `json:"lines"`
	// Keyword 非空时走 grep（固定字符串匹配，不当正则），空则单纯看末尾 N 行
	Keyword string `json:"keyword"`
	// IgnoreCase grep 是否忽略大小写，默认是
	IgnoreCase *bool `json:"ignoreCase"`
}

// buildLogViewCommand 拼实时查看的命令。
//
// 所有来自用户的东西（路径、关键字）都过 shellQuote；grep 固定加 -F
// 把关键字当**字面字符串**而不是正则 —— 否则一个 `.*` 就能让 grep 扫全文。
// 输出在远端就用 head -c 截断：sshx.Run 那边没有上限，只能在这里堵。
func buildLogViewCommand(logPath, keyword string, lines int, ignoreCase bool) string {
	q := shellQuote(logPath)
	var body string
	if keyword == "" {
		body = fmt.Sprintf("tail -n %d -- %s", lines, q)
	} else {
		flags := "-F"
		if ignoreCase {
			flags = "-Fi"
		}
		// grep 先过滤再 tail：先 tail 再 grep 会漏掉靠前的命中
		body = fmt.Sprintf("grep %s -e %s -- %s | tail -n %d",
			flags, shellQuote(keyword), q, lines)
	}
	// grep 无匹配时退出码是 1，`|| true` 让它不被当成失败；末尾 exit 0 同理
	return fmt.Sprintf(
		"f=%s\n"+
			"if [ ! -e \"$f\" ]; then echo '__OPS_STATE__=missing'; exit 0; fi\n"+
			"if [ -d \"$f\" ]; then echo '__OPS_STATE__=isdir'; exit 0; fi\n"+
			"if [ ! -r \"$f\" ]; then echo '__OPS_STATE__=denied'; exit 0; fi\n"+
			"echo '__OPS_STATE__=ok'\n"+
			"sz=$(wc -c < \"$f\" 2>/dev/null | tr -d ' ')\n"+
			"echo \"__OPS_SIZE__=$sz\"\n"+
			"echo '__OPS_BODY__'\n"+
			"{ %s ; } 2>/dev/null | head -c %d\n"+
			"exit 0\n",
		q, body, hostLogViewMaxBytes)
}

// logProbe 远端脚本回来的结构化前缀
type logProbe struct {
	State string // ok | missing | isdir | denied
	Size  int64
	Inode string
	Body  string
}

// parseLogProbe 解析远端输出。前缀行用不会出现在日志里的 __OPS_xxx__ 记号，
// 避免日志内容本身把解析带偏。
func parseLogProbe(raw string) logProbe {
	var p logProbe
	rest := raw
	for {
		idx := strings.IndexByte(rest, '\n')
		if idx < 0 {
			break
		}
		line := strings.TrimRight(rest[:idx], "\r")
		switch {
		case strings.HasPrefix(line, "__OPS_STATE__="):
			p.State = strings.TrimPrefix(line, "__OPS_STATE__=")
		case strings.HasPrefix(line, "__OPS_SIZE__="):
			p.Size, _ = strconv.ParseInt(strings.TrimSpace(strings.TrimPrefix(line, "__OPS_SIZE__=")), 10, 64)
		case strings.HasPrefix(line, "__OPS_INODE__="):
			p.Inode = strings.TrimSpace(strings.TrimPrefix(line, "__OPS_INODE__="))
		case line == "__OPS_BODY__":
			p.Body = rest[idx+1:]
			return p
		}
		rest = rest[idx+1:]
	}
	return p
}

func logStateMessage(state string) string {
	switch state {
	case "missing":
		return "文件不存在"
	case "isdir":
		return "这是一个目录，不是文件"
	case "denied":
		return "读不了这个文件（通常是权限不足，登录用户需要能读它）"
	default:
		return ""
	}
}

// ViewHostLog 实时查看主机日志。只读，不落库。
//
// 权限上按「文件管理」那一档判（model.ActionFile）：能读主机上的日志文件，
// 和能用文件管理浏览下载是同一级别的能力，不该比它松。
func (h *Handler) ViewHostLog(c *gin.Context) {
	var req logViewReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "请选择主机并填写日志路径")
		return
	}

	logPath, err := h.normalizeLogPath(req.Path)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	lines := req.Lines
	if lines <= 0 {
		lines = 200
	}
	if lines > hostLogViewMaxLines {
		lines = hostLogViewMaxLines
	}
	ignoreCase := true
	if req.IgnoreCase != nil {
		ignoreCase = *req.IgnoreCase
	}

	var host model.Host
	if err := h.DB.First(&host, req.HostID).Error; err != nil {
		response.NotFound(c, "主机不存在")
		return
	}
	user := middleware.CurrentUser(c)
	if !h.hostAllowedForAction(user, &host, model.ActionFile) {
		response.Forbidden(c, "没有这台主机的文件读取权限")
		return
	}

	command := buildLogViewCommand(logPath, strings.TrimSpace(req.Keyword), lines, ignoreCase)

	// 走一次命令规则预检。这个模块的命令是平台拼的、参数已经 shellQuote，
	// 但路径与关键字来自人输入 —— 预检是成本极低的第二道关。
	// 只拦 blocked，warn 照实带回给前端展示
	precheckStatus, hits := h.precheckScript(command)
	if precheckStatus == "blocked" {
		reason := "命令规则拦截"
		if len(hits) > 0 {
			reason = fmt.Sprintf("命令规则拦截：%s（%s）", hits[0].Description, hits[0].Pattern)
		}
		response.BadRequest(c, reason)
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), hostLogTimeout)
	defer cancel()
	res := sshx.Run(ctx, h.target(&host), command)

	if res.Status != "success" && res.Stdout == "" {
		detail := strings.TrimSpace(res.Stderr)
		if detail == "" {
			detail = res.Status
		}
		response.Error(c, "读取失败: "+truncate(detail, 240))
		return
	}

	probe := parseLogProbe(res.Stdout)
	if msg := logStateMessage(probe.State); msg != "" {
		response.BadRequest(c, msg)
		return
	}

	body := probe.Body
	truncated := len(body) >= hostLogViewMaxBytes
	rows := []string{}
	for _, line := range strings.Split(strings.TrimRight(body, "\n"), "\n") {
		if line != "" || len(rows) > 0 {
			rows = append(rows, truncate(strings.TrimRight(line, "\r"), 4000))
		}
	}

	response.OK(c, gin.H{
		"hostId": host.ID, "hostName": host.Name, "path": logPath,
		"mode":  map[bool]string{true: "grep", false: "tail"}[strings.TrimSpace(req.Keyword) != ""],
		"lines": len(rows), "rows": rows,
		"fileSize": probe.Size, "truncated": truncated,
		"costMs": res.CostMs, "precheck": precheckStatus, "precheckHits": hits,
		"note": "只读：平台只跑 tail / grep，不会改动主机上的任何东西；" +
			"单次最多返回 1MB，超出会从头截断",
	})
}

// hostAllowedForAction 复用主机可见性 + 资源授权的判定（非 HTTP 参数版）
func (h *Handler) hostAllowedForAction(user *model.User, host *model.Host, action string) bool {
	if h.hostVisibleWithGrants(user, host) {
		return true
	}
	return len(h.filterHostIDsForAction(user, []uint{host.ID}, action)) > 0
}

// ---------- 关键字巡检 ----------

// buildLogScanCommand 拼增量读取的命令。
//
// 增量的做法：把上次记下的 inode 与 offset 一起传过去，由远端自己判断要不要归零。
//   - inode 变了 → 文件被轮转或重建，从头读
//   - 当前大小 < 上次 offset → 被 truncate 过，从头读
//   - 否则从 offset+1 读到末尾（tail -c +N 是 POSIX 写法）
//
// 判断放在远端是为了一次往返就拿到结果；放在本地要先问一次 inode 再读，多一轮 SSH。
func buildLogScanCommand(logPath string, lastOffset int64, lastInode string, maxBytes int) string {
	q := shellQuote(logPath)
	return fmt.Sprintf(
		"f=%s\n"+
			"if [ ! -e \"$f\" ]; then echo '__OPS_STATE__=missing'; exit 0; fi\n"+
			"if [ -d \"$f\" ]; then echo '__OPS_STATE__=isdir'; exit 0; fi\n"+
			"if [ ! -r \"$f\" ]; then echo '__OPS_STATE__=denied'; exit 0; fi\n"+
			"sz=$(wc -c < \"$f\" 2>/dev/null | tr -d ' ')\n"+
			"ino=$(ls -i -- \"$f\" 2>/dev/null | awk '{print $1}')\n"+
			"echo '__OPS_STATE__=ok'\n"+
			"echo \"__OPS_SIZE__=$sz\"\n"+
			"echo \"__OPS_INODE__=$ino\"\n"+
			"start=1\n"+
			"if [ \"$ino\" = %s ] && [ \"$sz\" -ge %d ]; then start=%d; fi\n"+
			"echo '__OPS_BODY__'\n"+
			"tail -c +$start -- \"$f\" 2>/dev/null | head -c %d\n"+
			"exit 0\n",
		q, shellQuote(lastInode), lastOffset, lastOffset+1, maxBytes)
}

// logHitResult 关键字匹配的结果
type logHitResult struct {
	HitCount int
	Sample   string
}

// matchLogKeywords 在一段新增内容里数命中。
//
// 规则：固定字符串、不区分大小写、先判 ignore 再判 keyword。
// keywords 为空表示「不做关键字判断」，这时只统计新增字节数 ——
// 有人只想知道「这个文件还在不在写」，那也是一个正当用法。
func matchLogKeywords(body, keywords, ignores string) logHitResult {
	kws := splitLogKeywords(keywords)
	if len(kws) == 0 {
		return logHitResult{}
	}
	igs := splitLogKeywords(ignores)

	var out logHitResult
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		lower := strings.ToLower(line)
		if containsAny(lower, igs) {
			continue
		}
		if !containsAny(lower, kws) {
			continue
		}
		out.HitCount++
		if out.Sample == "" {
			out.Sample = truncate(line, 500)
		}
	}
	return out
}

// splitLogKeywords 切分并小写化关键字列表
func splitLogKeywords(raw string) []string {
	fields := strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == '\n' || r == '\r' })
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if f = strings.ToLower(strings.TrimSpace(f)); f != "" {
			out = append(out, f)
		}
	}
	return out
}

func containsAny(lowerLine string, needles []string) bool {
	for _, n := range needles {
		if strings.Contains(lowerLine, n) {
			return true
		}
	}
	return false
}

// scanHostLogTarget 巡检一个目标并回写。返回更新后的副本。
//
// 首次巡检（LastInode 为空）**只记水位、不回溯历史**：一个跑了半年的
// /var/log/messages 里成千上万条 ERROR 不是「刚出的问题」，一上来就报
// 只会让人把这个功能关掉。这是日志采集器的通用做法，界面上会说明。
func (h *Handler) scanHostLogTarget(target model.HostLogTarget, operator string) model.HostLogTarget {
	now := time.Now()
	var host model.Host
	if err := h.DB.First(&host, target.HostID).Error; err != nil {
		return h.finishLogScan(target, now, logScanOutcome{
			Status: "failed", Error: "关联的主机已不存在",
		}, operator)
	}

	maxBytes := target.MaxBytes
	if maxBytes <= 0 {
		maxBytes = hostLogScanMaxBytes
	}
	firstRun := strings.TrimSpace(target.LastInode) == ""

	command := buildLogScanCommand(target.Path, target.LastOffset, target.LastInode, maxBytes)
	ctx, cancel := context.WithTimeout(context.Background(), hostLogTimeout)
	defer cancel()
	res := sshx.Run(ctx, h.target(&host), command)

	outcome := logScanOutcome{HostName: host.Name, CostMs: res.CostMs}
	if res.Status != "success" && res.Stdout == "" {
		detail := strings.TrimSpace(res.Stderr)
		if detail == "" {
			detail = res.Status
		}
		outcome.Status, outcome.Error = "failed", truncate(detail, 240)
		return h.finishLogScan(target, now, outcome, operator)
	}

	probe := parseLogProbe(res.Stdout)
	switch probe.State {
	case "missing":
		outcome.Status, outcome.Error = "missing", "文件不存在"
		return h.finishLogScan(target, now, outcome, operator)
	case "isdir":
		outcome.Status, outcome.Error = "failed", "登记的路径是一个目录"
		return h.finishLogScan(target, now, outcome, operator)
	case "denied":
		outcome.Status, outcome.Error = "denied", "读不了（登录用户对该文件没有读权限）"
		return h.finishLogScan(target, now, outcome, operator)
	case "ok":
	default:
		outcome.Status, outcome.Error = "failed", "远端返回无法识别: "+truncate(res.Stdout, 160)
		return h.finishLogScan(target, now, outcome, operator)
	}

	outcome.FileSize = probe.Size
	outcome.Inode = probe.Inode
	// 轮转判定：inode 换了，或者文件比上次记的水位还小
	outcome.Rotated = !firstRun &&
		(probe.Inode != target.LastInode || probe.Size < target.LastOffset)
	outcome.NewOffset = probe.Size

	if firstRun {
		// 只对齐水位。这里刻意不看 probe.Body ——
		// 首轮读回来的是整个文件（或它的前 maxBytes），拿它算命中就是在报历史
		outcome.Status = "ok"
		outcome.Note = "首次巡检只记录水位，不回溯历史日志；下一轮开始只看新增内容"
		return h.finishLogScan(target, now, outcome, operator)
	}

	outcome.NewBytes = int64(len(probe.Body))
	hit := matchLogKeywords(probe.Body, target.Keywords, target.IgnoreKeywords)
	outcome.HitCount, outcome.Sample = hit.HitCount, hit.Sample
	if hit.HitCount > 0 {
		outcome.Status = "hit"
	} else {
		outcome.Status = "ok"
	}
	return h.finishLogScan(target, now, outcome, operator)
}

type logScanOutcome struct {
	Status    string
	HostName  string
	HitCount  int
	Sample    string
	NewBytes  int64
	FileSize  int64
	Inode     string
	NewOffset int64
	Rotated   bool
	CostMs    int64
	Error     string
	Note      string
}

// finishLogScan 回写目标状态 + 落一条历史 + 联动告警。
//
// 失败时**不推水位、不清空上次的命中样本**：一次 SSH 失败不该让「上次命中了什么」
// 这条线索消失，也不该让下一轮把失败期间的日志当成新增内容漏掉。
func (h *Handler) finishLogScan(target model.HostLogTarget, now time.Time,
	out logScanOutcome, operator string) model.HostLogTarget {

	updates := map[string]any{
		"last_status":    out.Status,
		"last_cost_ms":   out.CostMs,
		"last_error":     out.Error,
		"last_scan_at":   &now,
		"last_rotated":   out.Rotated,
		"total_scans":    target.TotalScans + 1,
		"last_hit_count": out.HitCount,
	}
	if out.HostName != "" {
		updates["host_name"] = out.HostName
	}
	// 只有真的读到了文件才推水位
	if out.Status == "ok" || out.Status == "hit" {
		updates["last_offset"] = out.NewOffset
		updates["last_inode"] = out.Inode
		updates["last_size"] = out.FileSize
		updates["last_sample"] = out.Sample
	}
	h.DB.Model(&model.HostLogTarget{}).Where("id = ?", target.ID).Updates(updates)

	h.DB.Create(&model.HostLogScan{
		TargetID: target.ID, HostID: target.HostID, Status: out.Status,
		HitCount: out.HitCount, NewBytes: out.NewBytes, FileSize: out.FileSize,
		Rotated: out.Rotated, Sample: out.Sample, CostMs: out.CostMs,
		ErrorMsg: firstNonBlank(out.Error, out.Note), Operator: operator,
	})

	var updated model.HostLogTarget
	h.DB.First(&updated, target.ID)
	h.syncHostLogAlerts(updated)
	return updated
}

// ---------- 告警联动 ----------

var hostLogAlertKinds = []string{"hit", "unreadable"}

// syncHostLogAlerts 命中关键字 / 读不到文件各报一类，恢复时 resolved。
//
// 「读不到」也要报：一个本该一直在写的日志突然消失或没权限读了，
// 等于这个监控点已经瞎了 —— 静默失败比没有监控更糟。
func (h *Handler) syncHostLogAlerts(target model.HostLogTarget) {
	if !target.AlertEnabled {
		return
	}
	source, err := h.internalAlertSource(hostLogAlertSourceName)
	if err != nil {
		log.Printf("[hostlog] 内部告警源不可用，跳过告警: %v", err)
		return
	}

	labels := map[string]string{
		"module":   "hostlog",
		"targetId": strconv.FormatUint(uint64(target.ID), 10),
		"hostId":   strconv.FormatUint(uint64(target.HostID), 10),
		"path":     target.Path,
	}
	fire := func(kind, title, summary, severity, value string) {
		h.ingestAlert(source, alertPayload{
			Title: title, Summary: summary, Severity: severity, Value: value,
			Fingerprint: hostLogAlertFingerprint(target.ID, kind), Labels: labels,
		})
	}
	resolve := func(kind string) {
		h.ingestAlert(source, alertPayload{
			Title: "主机日志巡检恢复", Status: "resolved",
			Fingerprint: hostLogAlertFingerprint(target.ID, kind), Labels: labels,
		})
	}

	switch target.LastStatus {
	case "hit":
		fire("hit",
			fmt.Sprintf("日志命中关键字：%s", target.Name),
			fmt.Sprintf("%s 上 %s 的新增内容里有 %d 行命中关键字（%s）。首条：%s",
				target.HostName, target.Path, target.LastHitCount,
				target.Keywords, target.LastSample),
			"warning", strconv.Itoa(target.LastHitCount))
		resolve("unreadable")
	case "missing", "denied", "failed":
		fire("unreadable",
			fmt.Sprintf("日志读不到：%s", target.Name),
			fmt.Sprintf("%s 上 %s 读取失败：%s —— 这个监控点当前是瞎的",
				target.HostName, target.Path, target.LastError),
			"warning", "")
		// 读不到就没法再确认命中还在不在，把 hit 那条收掉 ——
		// 否则它会永远挂在那里，而真正要看的是「监控瞎了」这条
		resolve("hit")
	default:
		for _, kind := range hostLogAlertKinds {
			resolve(kind)
		}
	}
}

func hostLogAlertFingerprint(targetID uint, kind string) string {
	return internalAlertFingerprint(fmt.Sprintf("hostlog|%d|%s", targetID, kind))
}

func (h *Handler) resolveHostLogAlerts(target model.HostLogTarget) {
	source, err := h.internalAlertSource(hostLogAlertSourceName)
	if err != nil {
		return
	}
	labels := map[string]string{
		"module":   "hostlog",
		"targetId": strconv.FormatUint(uint64(target.ID), 10),
		"path":     target.Path,
	}
	for _, kind := range hostLogAlertKinds {
		h.ingestAlert(source, alertPayload{
			Title: "日志监控点已删除", Status: "resolved",
			Fingerprint: hostLogAlertFingerprint(target.ID, kind), Labels: labels,
		})
	}
}

// ---------- 批量与定时 ----------

func (h *Handler) scanHostLogTargets(list []model.HostLogTarget, operator string) gin.H {
	var (
		wg     sync.WaitGroup
		mu     sync.Mutex
		counts = map[string]int{}
		hits   int
	)
	sem := make(chan struct{}, hostLogScanConcurrency)

	for i := range list {
		wg.Add(1)
		go func(t model.HostLogTarget) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			after := h.scanHostLogTarget(t, operator)
			mu.Lock()
			counts[after.LastStatus]++
			hits += after.LastHitCount
			mu.Unlock()
		}(list[i])
	}
	wg.Wait()

	return gin.H{
		"checked":  len(list),
		"ok":       counts["ok"],
		"hit":      counts["hit"],
		"hitLines": hits,
		"missing":  counts["missing"],
		"denied":   counts["denied"],
		"failed":   counts["failed"],
	}
}

// ScanHostLogsForSchedule 定时入口：巡检所有启用的日志目标，再采一轮日志目录占用。
func (h *Handler) ScanHostLogsForSchedule() {
	var list []model.HostLogTarget
	if err := h.DB.Where("enabled = ?", true).Find(&list).Error; err != nil {
		log.Printf("[hostlog] 定时巡检读取目标失败: %v", err)
		return
	}

	var info string
	if len(list) > 0 {
		s := h.scanHostLogTargets(list, "scheduler")
		info = fmt.Sprintf("目标 %v，正常 %v，命中 %v（共 %v 行），文件缺失 %v，读不了 %v，失败 %v",
			s["checked"], s["ok"], s["hit"], s["hitLines"], s["missing"], s["denied"], s["failed"])
	} else {
		info = "没有启用的日志监控点"
	}

	usage := h.collectLogUsageAll("scheduler")
	info += fmt.Sprintf("；占用采集 %d 台成功 %d 台失败", usage.ok, usage.failed)

	h.markFixedRun("hostlog", info)
	log.Printf("[hostlog] 定时巡检完成: %s", info)
}

// ---------- 日志目录占用 ----------

// buildLogUsageCommand du + 最大文件榜。
//
// find -printf 是 GNU 扩展，很多精简镜像与 BSD 用户空间没有 ——
// 先探一下支持不支持，不支持就退回 du -a 并在输出里标 __OPS_FALLBACK__，
// 界面会如实写明「含目录」。假装那是纯文件清单会让人照着它去删目录。
func buildLogUsageCommand(dir string) string {
	q := shellQuote(dir)
	return fmt.Sprintf(
		"d=%s\n"+
			"if [ ! -d \"$d\" ]; then echo '__OPS_STATE__=missing'; exit 0; fi\n"+
			"echo '__OPS_STATE__=ok'\n"+
			"tot=$(du -sk -- \"$d\" 2>/dev/null | awk '{print $1}')\n"+
			"echo \"__OPS_TOTAL_KB__=$tot\"\n"+
			"if find \"$d\" -maxdepth 0 -printf '' 2>/dev/null; then\n"+
			"  echo '__OPS_BODY__'\n"+
			"  find \"$d\" -type f -printf '%%s %%p\\n' 2>/dev/null | sort -rn | head -n %d\n"+
			"else\n"+
			"  echo '__OPS_FALLBACK__=1'\n"+
			"  echo '__OPS_BODY__'\n"+
			"  du -ak -- \"$d\" 2>/dev/null | sort -rn | head -n %d\n"+
			"fi\n"+
			"exit 0\n",
		q, hostLogTopFiles, hostLogTopFiles)
}

type logTopEntry struct {
	Path   string `json:"path"`
	SizeKB int64  `json:"sizeKb"`
}

// parseLogUsage 解析占用输出。fallback 模式下第一列单位已经是 KB（du -k），
// 非 fallback 下 find -printf %s 给的是字节，要换算 —— 这个差别很容易写错。
func parseLogUsage(raw string) (totalKB int64, fallback bool, entries []logTopEntry, state string) {
	rest := raw
	body := ""
	for {
		idx := strings.IndexByte(rest, '\n')
		if idx < 0 {
			break
		}
		line := strings.TrimRight(rest[:idx], "\r")
		switch {
		case strings.HasPrefix(line, "__OPS_STATE__="):
			state = strings.TrimPrefix(line, "__OPS_STATE__=")
		case strings.HasPrefix(line, "__OPS_TOTAL_KB__="):
			totalKB, _ = strconv.ParseInt(strings.TrimSpace(strings.TrimPrefix(line, "__OPS_TOTAL_KB__=")), 10, 64)
		case strings.HasPrefix(line, "__OPS_FALLBACK__="):
			fallback = true
		case line == "__OPS_BODY__":
			body = rest[idx+1:]
			rest = ""
		}
		if rest == "" {
			break
		}
		rest = rest[idx+1:]
	}

	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(strings.TrimRight(line, "\r"))
		if line == "" {
			continue
		}
		space := strings.IndexAny(line, " \t")
		if space <= 0 {
			continue
		}
		size, err := strconv.ParseInt(strings.TrimSpace(line[:space]), 10, 64)
		if err != nil {
			continue
		}
		p := strings.TrimSpace(line[space+1:])
		if p == "" {
			continue
		}
		kb := size
		if !fallback {
			// find -printf %s 是字节；不足 1KB 的按 1KB 记，免得显示成 0
			kb = size / 1024
			if kb == 0 && size > 0 {
				kb = 1
			}
		}
		entries = append(entries, logTopEntry{Path: p, SizeKB: kb})
	}
	return totalKB, fallback, entries, state
}

// collectHostLogUsage 采一台主机的日志目录占用
func (h *Handler) collectHostLogUsage(host model.Host, dir string) model.HostLogUsage {
	row := model.HostLogUsage{HostID: host.ID, HostName: host.Name, Dir: dir, Status: "failed"}

	ctx, cancel := context.WithTimeout(context.Background(), hostLogTimeout)
	defer cancel()
	res := sshx.Run(ctx, h.target(&host), buildLogUsageCommand(dir))
	row.CostMs = res.CostMs

	if res.Status != "success" && res.Stdout == "" {
		detail := strings.TrimSpace(res.Stderr)
		if detail == "" {
			detail = res.Status
		}
		row.ErrorMsg = truncate(detail, 240)
		return row
	}

	totalKB, fallback, entries, state := parseLogUsage(res.Stdout)
	if state == "missing" {
		row.ErrorMsg = dir + " 不存在"
		return row
	}
	if state != "ok" {
		row.ErrorMsg = "远端返回无法识别: " + truncate(res.Stdout, 160)
		return row
	}

	row.Status = "ok"
	row.TotalKB = totalKB
	row.TopIsDir = fallback
	if len(entries) > 0 {
		b, _ := json.Marshal(entries)
		row.TopFiles = string(b)
	}
	return row
}

type logUsageSummary struct {
	ok     int
	failed int
}

// collectLogUsageAll 对所有启用的主机各采一次。
//
// 采集范围是「全部主机」而不是「登记了日志目标的主机」：磁盘被日志写满
// 跟有没有人给它配监控点无关，恰恰是没人管的机器更容易出事。
func (h *Handler) collectLogUsageAll(operator string) logUsageSummary {
	dir := h.logUsageDir()
	var hosts []model.Host
	if err := h.DB.Find(&hosts).Error; err != nil || len(hosts) == 0 {
		return logUsageSummary{}
	}

	rows := make([]model.HostLogUsage, len(hosts))
	var wg sync.WaitGroup
	sem := make(chan struct{}, hostLogScanConcurrency)
	for i := range hosts {
		wg.Add(1)
		go func(idx int, host model.Host) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			rows[idx] = h.collectHostLogUsage(host, dir)
		}(i, hosts[i])
	}
	wg.Wait()

	var sum logUsageSummary
	for i := range rows {
		if rows[i].Status == "ok" {
			sum.ok++
		} else {
			sum.failed++
		}
	}
	// 一次批插：逐行 Create 在几十台的规模上会把一轮采集拖成几秒
	h.DB.Create(&rows)
	return sum
}

// logUsageDir 采占用的目录，取白名单的第一条（默认 /var/log）
func (h *Handler) logUsageDir() string {
	return h.logPathPrefixes()[0]
}

// CollectLogUsage 手动触发一次占用采集
func (h *Handler) CollectLogUsage(c *gin.Context) {
	sum := h.collectLogUsageAll(middleware.CurrentUser(c).Username)
	if sum.ok == 0 && sum.failed == 0 {
		response.BadRequest(c, "还没有纳管主机")
		return
	}
	response.OK(c, gin.H{
		"dir": h.logUsageDir(), "ok": sum.ok, "failed": sum.failed,
	})
}

// ListLogUsage 最近一次占用采集的结果（每台主机取最新一条）
func (h *Handler) ListLogUsage(c *gin.Context) {
	var rows []model.HostLogUsage
	// 取每台主机最新的一行：子查询比在 Go 里去重更省内存
	sub := h.DB.Model(&model.HostLogUsage{}).
		Select("max(id) as id").Group("host_id")
	if err := h.DB.Where("id IN (?)", sub).Order("total_kb desc").Find(&rows).Error; err != nil {
		response.Error(c, "查询日志占用失败")
		return
	}

	type usageView struct {
		model.HostLogUsage
		Top []logTopEntry `json:"top"`
	}
	views := make([]usageView, 0, len(rows))
	for _, row := range rows {
		v := usageView{HostLogUsage: row}
		if row.TopFiles != "" {
			_ = json.Unmarshal([]byte(row.TopFiles), &v.Top)
		}
		views = append(views, v)
	}
	response.OK(c, gin.H{
		"dir":  h.logUsageDir(),
		"rows": views,
		"note": "TopIsDir 为真表示该主机的 find 不支持 -printf，已退回 du -a，" +
			"清单里**混有目录**，别照着它去删",
	})
}

// ---------- 目标 CRUD ----------

type hostLogTargetReq struct {
	Name           string `json:"name" binding:"required"`
	HostID         uint   `json:"hostId" binding:"required"`
	Path           string `json:"path" binding:"required"`
	Keywords       string `json:"keywords"`
	IgnoreKeywords string `json:"ignoreKeywords"`
	MaxBytes       int    `json:"maxBytes"`
	AlertEnabled   *bool  `json:"alertEnabled"`
	DeptID         uint   `json:"deptId"`
	Enabled        *bool  `json:"enabled"`
	Remark         string `json:"remark"`
}

func (h *Handler) ListHostLogTargets(c *gin.Context) {
	page, size := pageParams(c)
	q := h.applyScope(h.DB.Model(&model.HostLogTarget{}), middleware.CurrentUser(c))
	if v := c.Query("status"); v != "" {
		q = q.Where("last_status = ?", v)
	}
	if v := c.Query("hostId"); v != "" {
		q = q.Where("host_id = ?", v)
	}
	if kw := strings.TrimSpace(c.Query("keyword")); kw != "" {
		like := "%" + kw + "%"
		q = q.Where("name LIKE ? OR path LIKE ? OR host_name LIKE ?", like, like, like)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		response.Error(c, "查询日志监控点失败")
		return
	}
	var list []model.HostLogTarget
	if err := q.Order("id desc").Offset((page - 1) * size).Limit(size).Find(&list).Error; err != nil {
		response.Error(c, "查询日志监控点失败")
		return
	}
	response.OKPage(c, list, total, page, size)
}

func (h *Handler) CreateHostLogTarget(c *gin.Context) {
	var req hostLogTargetReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "名称、主机与路径为必填项")
		return
	}
	logPath, err := h.normalizeLogPath(req.Path)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	var host model.Host
	if err := h.DB.First(&host, req.HostID).Error; err != nil {
		response.BadRequest(c, "指定的主机不存在")
		return
	}
	if !h.hostAllowedForAction(middleware.CurrentUser(c), &host, model.ActionFile) {
		response.Forbidden(c, "没有这台主机的文件读取权限")
		return
	}

	item := model.HostLogTarget{
		Name: req.Name, HostID: host.ID, HostName: host.Name, Path: logPath,
		Keywords: req.Keywords, IgnoreKeywords: req.IgnoreKeywords,
		MaxBytes: req.MaxBytes, AlertEnabled: true,
		DeptID: req.DeptID, CreatedBy: middleware.CurrentUser(c).ID,
		Enabled: true, Remark: req.Remark, LastStatus: "unknown",
	}
	if item.MaxBytes <= 0 {
		item.MaxBytes = hostLogScanMaxBytes
	}
	if req.AlertEnabled != nil {
		item.AlertEnabled = *req.AlertEnabled
	}
	if req.Enabled != nil {
		item.Enabled = *req.Enabled
	}
	if err := h.DB.Create(&item).Error; err != nil {
		response.Error(c, "创建失败")
		return
	}
	response.OK(c, item)
}

func (h *Handler) loadHostLogTargetScoped(c *gin.Context) (*model.HostLogTarget, bool) {
	var item model.HostLogTarget
	if err := h.DB.First(&item, idParam(c)).Error; err != nil {
		response.NotFound(c, "日志监控点不存在")
		return nil, false
	}
	if !h.resourceVisible(middleware.CurrentUser(c), item.DeptID, item.CreatedBy) {
		response.NotFound(c, "日志监控点不存在")
		return nil, false
	}
	return &item, true
}

func (h *Handler) UpdateHostLogTarget(c *gin.Context) {
	itemPtr, ok := h.loadHostLogTargetScoped(c)
	if !ok {
		return
	}
	var req hostLogTargetReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "参数校验失败")
		return
	}
	logPath, err := h.normalizeLogPath(req.Path)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	updates := map[string]any{
		"name": req.Name, "path": logPath,
		"keywords": req.Keywords, "ignore_keywords": req.IgnoreKeywords,
		"dept_id": req.DeptID, "remark": req.Remark,
	}
	if req.MaxBytes > 0 {
		updates["max_bytes"] = req.MaxBytes
	}
	if req.AlertEnabled != nil {
		updates["alert_enabled"] = *req.AlertEnabled
	}
	if req.Enabled != nil {
		updates["enabled"] = *req.Enabled
	}
	// 换了主机或换了文件，旧的水位就没有意义了，归零重新对齐 ——
	// 留着旧 offset 会让第一轮读到一段错位的内容
	if req.HostID != itemPtr.HostID || logPath != itemPtr.Path {
		var host model.Host
		if err := h.DB.First(&host, req.HostID).Error; err != nil {
			response.BadRequest(c, "指定的主机不存在")
			return
		}
		if !h.hostAllowedForAction(middleware.CurrentUser(c), &host, model.ActionFile) {
			response.Forbidden(c, "没有这台主机的文件读取权限")
			return
		}
		updates["host_id"], updates["host_name"] = host.ID, host.Name
		updates["last_offset"], updates["last_inode"], updates["last_size"] = 0, "", 0
		updates["last_status"], updates["last_hit_count"], updates["last_sample"] = "unknown", 0, ""
	}

	if err := h.DB.Model(&model.HostLogTarget{}).Where("id = ?", itemPtr.ID).
		Updates(updates).Error; err != nil {
		response.Error(c, "更新失败")
		return
	}
	var updated model.HostLogTarget
	h.DB.First(&updated, itemPtr.ID)
	response.OK(c, updated)
}

func (h *Handler) DeleteHostLogTarget(c *gin.Context) {
	item, ok := h.loadHostLogTargetScoped(c)
	if !ok {
		return
	}
	if err := h.DB.Delete(&model.HostLogTarget{}, item.ID).Error; err != nil {
		response.Error(c, "删除失败")
		return
	}
	h.DB.Where("target_id = ?", item.ID).Delete(&model.HostLogScan{})
	h.resolveHostLogAlerts(*item)
	response.OK(c, nil)
}

// ScanHostLogTarget 单条巡检
func (h *Handler) ScanHostLogTarget(c *gin.Context) {
	item, ok := h.loadHostLogTargetScoped(c)
	if !ok {
		return
	}
	response.OK(c, h.scanHostLogTarget(*item, middleware.CurrentUser(c).Username))
}

// ScanAllHostLogTargets 批量巡检（当前用户数据范围内、启用中的）
func (h *Handler) ScanAllHostLogTargets(c *gin.Context) {
	var list []model.HostLogTarget
	q := h.applyScope(h.DB.Model(&model.HostLogTarget{}), middleware.CurrentUser(c)).
		Where("enabled = ?", true)
	if err := q.Find(&list).Error; err != nil {
		response.Error(c, "读取日志监控点失败")
		return
	}
	if len(list) == 0 {
		response.BadRequest(c, "没有启用中的日志监控点")
		return
	}
	response.OK(c, h.scanHostLogTargets(list, middleware.CurrentUser(c).Username))
}

// ListHostLogScans 某个监控点的巡检历史
func (h *Handler) ListHostLogScans(c *gin.Context) {
	page, size := pageParams(c)
	q := h.DB.Model(&model.HostLogScan{})
	if v := c.Query("targetId"); v != "" {
		q = q.Where("target_id = ?", v)
	}
	if v := c.Query("status"); v != "" {
		q = q.Where("status = ?", v)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		response.Error(c, "查询巡检历史失败")
		return
	}
	var list []model.HostLogScan
	if err := q.Order("id desc").Offset((page - 1) * size).Limit(size).Find(&list).Error; err != nil {
		response.Error(c, "查询巡检历史失败")
		return
	}
	response.OKPage(c, list, total, page, size)
}

// HostLogMeta 给前端的元信息：允许的目录前缀、各种上限。
// 把这些交出去而不是让前端硬编码，免得改了配置界面上的提示还是旧的。
func (h *Handler) HostLogMeta(c *gin.Context) {
	response.OK(c, gin.H{
		"pathPrefixes": h.logPathPrefixes(),
		"usageDir":     h.logUsageDir(),
		"viewMaxBytes": hostLogViewMaxBytes,
		"viewMaxLines": hostLogViewMaxLines,
		"scanMaxBytes": hostLogScanMaxBytes,
		"note": "只读：平台只在主机上跑 tail / grep / du / find，没有任何写操作。" +
			"路径必须落在允许的目录前缀下 —— 没有这道限制，能进这个页面就等于能读主机上任意文件",
	})
}
