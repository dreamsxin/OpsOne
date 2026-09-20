package handler

import (
	"fmt"
	"log"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"ops-platform/server/internal/middleware"
	"ops-platform/server/internal/model"
	"ops-platform/server/internal/response"
)

const (
	// exposureAlertSourceName 暴露面告警挂在这个内部接入源下
	exposureAlertSourceName = "暴露面监测"
	// exposurePortLimit 单个目标一次最多扫多少个端口。
	// 不做全端口扫描：平台是登记制的暴露面核对，不是端口扫描器。
	exposurePortLimit = 1024
	// exposureDialConcurrency 单个目标内部的并发连接数
	exposureDialConcurrency = 20
	// exposureTargetConcurrency 定时扫描时同时处理几个目标
	exposureTargetConcurrency = 3
)

// ---------- 端口清单解析（纯函数，便于单测） ----------

// parsePortSpec 解析 "22,80,443,8000-8010" 这样的端口清单。
// 结果去重且升序，方便和基线做差集与稳定展示。
func parsePortSpec(spec string) ([]int, error) {
	seen := make(map[int]struct{})
	for _, piece := range strings.Split(spec, ",") {
		piece = strings.TrimSpace(piece)
		if piece == "" {
			continue
		}
		low, high := piece, piece
		if idx := strings.Index(piece, "-"); idx >= 0 {
			low, high = strings.TrimSpace(piece[:idx]), strings.TrimSpace(piece[idx+1:])
		}
		from, err := parsePort(low)
		if err != nil {
			return nil, err
		}
		to, err := parsePort(high)
		if err != nil {
			return nil, err
		}
		if from > to {
			return nil, fmt.Errorf("端口区间 %s 的起止写反了", piece)
		}
		if to-from+1 > exposurePortLimit {
			return nil, fmt.Errorf("端口区间 %s 太大，一次最多 %d 个端口", piece, exposurePortLimit)
		}
		for port := from; port <= to; port++ {
			seen[port] = struct{}{}
		}
		if len(seen) > exposurePortLimit {
			return nil, fmt.Errorf("端口总数超过 %d 个，请拆成多个目标", exposurePortLimit)
		}
	}
	if len(seen) == 0 {
		return nil, fmt.Errorf("端口清单不能为空")
	}
	ports := make([]int, 0, len(seen))
	for port := range seen {
		ports = append(ports, port)
	}
	sort.Ints(ports)
	return ports, nil
}

func parsePort(raw string) (int, error) {
	port, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%q 不是合法端口", raw)
	}
	if port < 1 || port > 65535 {
		return 0, fmt.Errorf("端口 %d 超出 1-65535", port)
	}
	return port, nil
}

// formatPorts 把端口列表拍回逗号分隔的字符串
func formatPorts(ports []int) string {
	parts := make([]string, 0, len(ports))
	for _, port := range ports {
		parts = append(parts, strconv.Itoa(port))
	}
	return strings.Join(parts, ",")
}

// diffPorts 对比实际开放端口与基线。
//
// unexpected 是「开着但没登记」——这是这个模块真正要抓的东西；
// missing 是「登记了却没开」，通常意味着服务挂了或被人关掉了。
// 只在扫过的端口里比较：基线里写了但这次没扫的端口不做判断。
func diffPorts(open, baseline, scanned []int) (unexpected, missing []int) {
	baseSet := make(map[int]struct{}, len(baseline))
	for _, port := range baseline {
		baseSet[port] = struct{}{}
	}
	openSet := make(map[int]struct{}, len(open))
	for _, port := range open {
		openSet[port] = struct{}{}
	}
	scannedSet := make(map[int]struct{}, len(scanned))
	for _, port := range scanned {
		scannedSet[port] = struct{}{}
	}

	unexpected, missing = []int{}, []int{}
	for _, port := range open {
		if _, ok := baseSet[port]; !ok {
			unexpected = append(unexpected, port)
		}
	}
	for _, port := range baseline {
		if _, ok := scannedSet[port]; !ok {
			continue
		}
		if _, ok := openSet[port]; !ok {
			missing = append(missing, port)
		}
	}
	return unexpected, missing
}

// ---------- 扫描 ----------

type exposureResult struct {
	Status     string
	Scanned    int
	Open       []int
	Unexpected []int
	Missing    []int
	CostMs     int64
	ErrorMsg   string
}

// scanExposureTarget 逐个端口做 TCP 连接判断开放与否。
//
// 只做 connect 探测：不发探针载荷、不识别服务指纹，避免把平台变成扫描器，
// 也避免被对端的安全设备当成攻击。
func scanExposureTarget(target model.ExposureTarget) exposureResult {
	started := time.Now()
	result := exposureResult{Status: "ok", Open: []int{}, Unexpected: []int{}, Missing: []int{}}

	ports, err := parsePortSpec(target.Ports)
	if err != nil {
		result.Status, result.ErrorMsg = "failed", err.Error()
		result.CostMs = time.Since(started).Milliseconds()
		return result
	}
	baseline := []int{}
	if strings.TrimSpace(target.Baseline) != "" {
		baseline, err = parsePortSpec(target.Baseline)
		if err != nil {
			result.Status, result.ErrorMsg = "failed", "基线端口无法解析: "+err.Error()
			result.CostMs = time.Since(started).Milliseconds()
			return result
		}
	}
	result.Scanned = len(ports)

	// 先解析一次域名：解析不了就没必要逐个端口去撞超时
	host := strings.TrimSpace(target.Address)
	if net.ParseIP(host) == nil {
		if _, err := net.LookupHost(host); err != nil {
			result.Status = "failed"
			result.ErrorMsg = truncate("地址无法解析: "+err.Error(), 240)
			result.CostMs = time.Since(started).Milliseconds()
			return result
		}
	}

	timeout := time.Duration(target.TimeoutMs) * time.Millisecond
	if timeout <= 0 {
		timeout = 800 * time.Millisecond
	}

	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, exposureDialConcurrency)
	for _, port := range ports {
		wg.Add(1)
		go func(port int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(port)), timeout)
			if err != nil {
				return
			}
			_ = conn.Close()
			mu.Lock()
			result.Open = append(result.Open, port)
			mu.Unlock()
		}(port)
	}
	wg.Wait()
	sort.Ints(result.Open)

	result.Unexpected, result.Missing = diffPorts(result.Open, baseline, ports)
	if len(result.Unexpected) > 0 {
		result.Status = "unexpected"
	}
	result.CostMs = time.Since(started).Milliseconds()
	return result
}

// executeExposureScan 扫一次、落库、联动告警
func (h *Handler) executeExposureScan(target model.ExposureTarget, operator string) exposureResult {
	result := scanExposureTarget(target)
	now := time.Now()

	h.DB.Model(&model.ExposureTarget{}).Where("id = ?", target.ID).Updates(map[string]any{
		"last_status":     result.Status,
		"last_open":       truncate(formatPorts(result.Open), 480),
		"last_unexpected": truncate(formatPorts(result.Unexpected), 480),
		"last_missing":    truncate(formatPorts(result.Missing), 480),
		"last_cost_ms":    result.CostMs,
		"last_error":      truncate(result.ErrorMsg, 240),
		"last_scan_at":    &now,
		"total_scans":     target.TotalScans + 1,
	})
	h.DB.Create(&model.ExposureScan{
		TargetID: target.ID, Status: result.Status, Scanned: result.Scanned,
		OpenPorts:  truncate(formatPorts(result.Open), 480),
		Unexpected: truncate(formatPorts(result.Unexpected), 480),
		Missing:    truncate(formatPorts(result.Missing), 480),
		CostMs:     result.CostMs, ErrorMsg: truncate(result.ErrorMsg, 240),
		Operator: operator,
	})

	if target.AlertEnabled {
		h.syncExposureAlert(target, result)
	}
	return result
}

// syncExposureAlert 有未登记端口或扫描失败就告警，恢复正常即关闭
func (h *Handler) syncExposureAlert(target model.ExposureTarget, result exposureResult) {
	source, err := h.internalAlertSource(exposureAlertSourceName)
	if err != nil {
		log.Printf("[exposure] 内部告警源不可用，跳过告警: %v", err)
		return
	}
	labels := map[string]string{
		"module":   "exposure",
		"targetId": strconv.FormatUint(uint64(target.ID), 10),
		"target":   target.Name,
		"address":  target.Address,
	}
	fingerprint := internalAlertFingerprint(fmt.Sprintf("exposure|%d", target.ID))

	switch result.Status {
	case "unexpected":
		summary := fmt.Sprintf("%s 开放了未登记的端口：%s（已登记：%s）",
			target.Address, formatPorts(result.Unexpected), emptyAs(target.Baseline, "无"))
		if len(result.Missing) > 0 {
			summary += fmt.Sprintf("；登记了却没开：%s", formatPorts(result.Missing))
		}
		h.ingestAlert(source, alertPayload{
			Title: "暴露面异常：" + target.Name, Summary: summary, Severity: "warning",
			Value: strconv.Itoa(len(result.Unexpected)), Fingerprint: fingerprint, Labels: labels,
		})
	case "failed":
		h.ingestAlert(source, alertPayload{
			Title: "暴露面扫描失败：" + target.Name, Summary: result.ErrorMsg,
			Severity: "warning", Fingerprint: fingerprint, Labels: labels,
		})
	default:
		h.ingestAlert(source, alertPayload{
			Title: "暴露面恢复：" + target.Name, Status: "resolved",
			Fingerprint: fingerprint, Labels: labels,
		})
	}
}

func emptyAs(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

// ---------- CRUD ----------

type exposureReq struct {
	Name         string `json:"name"`
	Address      string `json:"address"`
	Ports        string `json:"ports"`
	Baseline     string `json:"baseline"`
	TimeoutMs    int    `json:"timeoutMs"`
	AlertEnabled *bool  `json:"alertEnabled"`
	Enabled      *bool  `json:"enabled"`
	Remark       string `json:"remark"`
}

// normalize 校验并规整参数。端口清单在这里就解析一遍，别等扫描时才报错。
func (r *exposureReq) normalize() error {
	r.Name = strings.TrimSpace(r.Name)
	r.Address = strings.TrimSpace(r.Address)
	if r.Name == "" {
		return fmt.Errorf("名称不能为空")
	}
	if r.Address == "" {
		return fmt.Errorf("地址不能为空")
	}
	if strings.Contains(r.Address, "/") || strings.Contains(r.Address, ":") {
		return fmt.Errorf("地址只填 IP 或域名，端口写在端口清单里")
	}
	ports, err := parsePortSpec(r.Ports)
	if err != nil {
		return err
	}
	r.Ports = formatPortSpecInput(r.Ports, ports)
	if strings.TrimSpace(r.Baseline) != "" {
		baseline, err := parsePortSpec(r.Baseline)
		if err != nil {
			return fmt.Errorf("基线端口有问题: %w", err)
		}
		r.Baseline = formatPorts(baseline)
	} else {
		r.Baseline = ""
	}
	if r.TimeoutMs == 0 {
		r.TimeoutMs = 800
	}
	if r.TimeoutMs < 100 || r.TimeoutMs > 10000 {
		return fmt.Errorf("单端口超时需在 100 到 10000 毫秒之间")
	}
	return nil
}

// formatPortSpecInput 端口清单原样保留用户写的区间写法（8000-8010 比展开成 11 个数字好读），
// 只有在写法过长时才用展开后的规整形式
func formatPortSpecInput(raw string, ports []int) string {
	trimmed := strings.TrimSpace(raw)
	if len(trimmed) <= 480 {
		return trimmed
	}
	return truncate(formatPorts(ports), 480)
}

func (h *Handler) ListExposureTargets(c *gin.Context) {
	query := h.DB.Model(&model.ExposureTarget{})
	if status := strings.TrimSpace(c.Query("status")); status != "" {
		query = query.Where("last_status = ?", status)
	}
	var list []model.ExposureTarget
	if err := query.Order("id asc").Find(&list).Error; err != nil {
		response.Error(c, "查询目标失败")
		return
	}
	response.OK(c, list)
}

func (h *Handler) CreateExposureTarget(c *gin.Context) {
	var req exposureReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "参数错误")
		return
	}
	if err := req.normalize(); err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	target := model.ExposureTarget{
		Name: req.Name, Address: req.Address, Ports: req.Ports, Baseline: req.Baseline,
		TimeoutMs: req.TimeoutMs, LastStatus: "unknown", Remark: req.Remark,
		CreatedBy: middleware.CurrentUser(c).ID,
	}
	wantAlert, wantEnabled := true, true
	if req.AlertEnabled != nil {
		wantAlert = *req.AlertEnabled
	}
	if req.Enabled != nil {
		wantEnabled = *req.Enabled
	}
	target.AlertEnabled, target.Enabled = wantAlert, wantEnabled
	if err := h.DB.Create(&target).Error; err != nil {
		response.Error(c, "创建失败")
		return
	}
	// 带 gorm default 的布尔字段 Create 后会被回填成 true，显式关掉的要写回去
	if !wantAlert || !wantEnabled {
		h.DB.Model(&model.ExposureTarget{}).Where("id = ?", target.ID).
			Updates(map[string]any{"alert_enabled": wantAlert, "enabled": wantEnabled})
		target.AlertEnabled, target.Enabled = wantAlert, wantEnabled
	}

	// 建完立刻扫一次，让人马上看到当前暴露面
	result := h.executeExposureScan(target, operatorName(c))
	h.DB.First(&target, target.ID)
	response.OK(c, gin.H{"target": target, "result": exposureResultView(result)})
}

func (h *Handler) UpdateExposureTarget(c *gin.Context) {
	var target model.ExposureTarget
	if err := h.DB.First(&target, idParam(c)).Error; err != nil {
		response.NotFound(c, "目标不存在")
		return
	}
	var req exposureReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "参数错误")
		return
	}
	if err := req.normalize(); err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	updates := map[string]any{
		"name": req.Name, "address": req.Address, "ports": req.Ports,
		"baseline": req.Baseline, "timeout_ms": req.TimeoutMs, "remark": req.Remark,
	}
	if req.AlertEnabled != nil {
		updates["alert_enabled"] = *req.AlertEnabled
	}
	if req.Enabled != nil {
		updates["enabled"] = *req.Enabled
	}
	if err := h.DB.Model(&model.ExposureTarget{}).Where("id = ?", target.ID).
		Updates(updates).Error; err != nil {
		response.Error(c, "更新失败")
		return
	}
	h.DB.First(&target, target.ID)
	response.OK(c, target)
}

func (h *Handler) DeleteExposureTarget(c *gin.Context) {
	var target model.ExposureTarget
	if err := h.DB.First(&target, idParam(c)).Error; err != nil {
		response.NotFound(c, "目标不存在")
		return
	}
	// 先关掉它可能还挂着的告警，别留下无主告警
	if target.AlertEnabled && target.LastStatus != "ok" {
		h.syncExposureAlert(target, exposureResult{Status: "ok"})
	}
	if err := h.DB.Delete(&model.ExposureTarget{}, target.ID).Error; err != nil {
		response.Error(c, "删除失败")
		return
	}
	h.DB.Where("target_id = ?", target.ID).Delete(&model.ExposureScan{})
	response.OK(c, gin.H{"detail": "目标与历史扫描记录已删除"})
}

// ScanExposureTarget 手动扫一次
func (h *Handler) ScanExposureTarget(c *gin.Context) {
	var target model.ExposureTarget
	if err := h.DB.First(&target, idParam(c)).Error; err != nil {
		response.NotFound(c, "目标不存在")
		return
	}
	result := h.executeExposureScan(target, operatorName(c))
	response.OK(c, exposureResultView(result))
}

func exposureResultView(result exposureResult) gin.H {
	return gin.H{
		"status": result.Status, "scanned": result.Scanned,
		"open": formatPorts(result.Open), "unexpected": formatPorts(result.Unexpected),
		"missing": formatPorts(result.Missing),
		"costMs":  result.CostMs, "error": result.ErrorMsg,
	}
}

// operatorName 取当前操作人用户名，取不到就算系统
func operatorName(c *gin.Context) string {
	if user := middleware.CurrentUser(c); user != nil {
		return user.Username
	}
	return "system"
}

func (h *Handler) ListExposureScans(c *gin.Context) {
	page, size := pageParams(c)
	query := h.DB.Model(&model.ExposureScan{})
	if raw := strings.TrimSpace(c.Query("targetId")); raw != "" {
		if id, err := strconv.Atoi(raw); err == nil && id > 0 {
			query = query.Where("target_id = ?", id)
		}
	}
	if status := strings.TrimSpace(c.Query("status")); status != "" {
		query = query.Where("status = ?", status)
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		response.Error(c, "查询扫描记录失败")
		return
	}
	var list []model.ExposureScan
	if err := query.Order("id desc").Offset((page - 1) * size).Limit(size).
		Find(&list).Error; err != nil {
		response.Error(c, "查询扫描记录失败")
		return
	}
	response.OKPage(c, list, total, page, size)
}

// ScanExposuresForSchedule 定时扫描全部启用中的目标
func (h *Handler) ScanExposuresForSchedule() {
	var list []model.ExposureTarget
	if err := h.DB.Where("enabled = ?", true).Find(&list).Error; err != nil {
		log.Printf("[exposure] 读取目标失败: %v", err)
		return
	}
	if len(list) == 0 {
		h.markFixedRun("exposure", "没有启用中的目标")
		return
	}

	var mu sync.Mutex
	abnormal, failed := 0, 0
	var wg sync.WaitGroup
	sem := make(chan struct{}, exposureTargetConcurrency)
	for i := range list {
		wg.Add(1)
		go func(target model.ExposureTarget) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			result := h.executeExposureScan(target, "scheduler")
			mu.Lock()
			switch result.Status {
			case "unexpected":
				abnormal++
			case "failed":
				failed++
			}
			mu.Unlock()
		}(list[i])
	}
	wg.Wait()

	info := fmt.Sprintf("共 %d 个目标，发现未登记端口 %d 个目标，扫描失败 %d 个", len(list), abnormal, failed)
	h.markFixedRun("exposure", info)
	log.Printf("[exposure] 定时扫描完成: %s", info)
}
