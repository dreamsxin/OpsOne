package handler

import (
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
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
	// probeAlertSourceName 拨测失败产生的告警挂在这个内部接入源下
	probeAlertSourceName = "拨测探测"
	// probeConcurrency 定时批量拨测的并发上限
	probeConcurrency = 5
	// probeBodyLimit 关键字校验时读取响应体的上限
	probeBodyLimit = 1 << 20
)

// probeResult 单次拨测结果
type probeResult struct {
	Status   string // up | down
	Code     int
	CostMs   int64
	ErrorMsg string
}

// runProbeOnce 执行一次拨测。判定标准：
//   - tcp：能在超时内建立连接即为通
//   - http：能拿到响应 + 状态码符合期望 + 响应体包含关键字（若配置）
func runProbeOnce(probe model.Probe) probeResult {
	timeout := time.Duration(probe.TimeoutSec) * time.Second
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	start := time.Now()

	if probe.Type == "tcp" {
		conn, err := net.DialTimeout("tcp", probe.Target, timeout)
		cost := time.Since(start).Milliseconds()
		if err != nil {
			return probeResult{Status: "down", CostMs: cost, ErrorMsg: truncate(err.Error(), 240)}
		}
		_ = conn.Close()
		return probeResult{Status: "up", CostMs: cost}
	}

	method := probe.Method
	if method == "" {
		method = http.MethodGet
	}
	req, err := http.NewRequest(method, probe.Target, nil)
	if err != nil {
		return probeResult{Status: "down", ErrorMsg: truncate(err.Error(), 240)}
	}
	req.Header.Set("User-Agent", "OpsOne-Probe/1.0")

	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	cost := time.Since(start).Milliseconds()
	if err != nil {
		return probeResult{Status: "down", CostMs: cost, ErrorMsg: truncate(err.Error(), 240)}
	}
	defer resp.Body.Close()

	result := probeResult{Status: "up", Code: resp.StatusCode, CostMs: cost}

	switch {
	case probe.ExpectStatus > 0 && resp.StatusCode != probe.ExpectStatus:
		result.Status = "down"
		result.ErrorMsg = fmt.Sprintf("状态码 %d，期望 %d", resp.StatusCode, probe.ExpectStatus)
	case probe.ExpectStatus == 0 && resp.StatusCode >= 400:
		result.Status = "down"
		result.ErrorMsg = fmt.Sprintf("状态码 %d", resp.StatusCode)
	}

	if probe.ExpectKeyword != "" {
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, probeBodyLimit))
		if readErr != nil {
			result.Status = "down"
			result.ErrorMsg = "读取响应体失败: " + truncate(readErr.Error(), 200)
		} else if !strings.Contains(string(body), probe.ExpectKeyword) {
			result.Status = "down"
			// 只说没找到，不回显响应体内容
			result.ErrorMsg = fmt.Sprintf("响应体未包含关键字「%s」", probe.ExpectKeyword)
		}
	}
	return result
}

// executeProbe 拨测一次并落库：更新拨测状态、写入历史记录、处理告警。
// 返回本次结果与更新后的连续失败次数。
func (h *Handler) executeProbe(probe model.Probe, operator string) (probeResult, int) {
	result := runProbeOnce(probe)
	now := time.Now()

	streak := 0
	if result.Status == "down" {
		streak = probe.FailStreak + 1
	}

	updates := map[string]any{
		"last_status": result.Status, "last_code": result.Code,
		"last_cost_ms": result.CostMs, "last_error": result.ErrorMsg,
		"last_check_at": &now, "fail_streak": streak,
		"total_checks": probe.TotalChecks + 1,
	}
	if result.Status == "down" {
		updates["fail_checks"] = probe.FailChecks + 1
	}
	h.DB.Model(&model.Probe{}).Where("id = ?", probe.ID).Updates(updates)

	h.DB.Create(&model.ProbeRecord{
		ProbeID: probe.ID, Status: result.Status, Code: result.Code,
		CostMs: result.CostMs, ErrorMsg: result.ErrorMsg, Operator: operator,
	})

	probe.FailStreak = streak
	probe.LastCode, probe.LastCostMs, probe.LastError = result.Code, result.CostMs, result.ErrorMsg
	h.syncProbeAlert(probe, result)
	return result, streak
}

// syncProbeAlert 连续失败达到阈值才告警；恢复时关闭告警
func (h *Handler) syncProbeAlert(probe model.Probe, result probeResult) {
	if !probe.AlertEnabled {
		return
	}
	source, err := h.internalAlertSource(probeAlertSourceName)
	if err != nil {
		log.Printf("[probe] 内部告警源不可用，跳过告警: %v", err)
		return
	}

	labels := map[string]string{
		"module":  "probe",
		"probeId": strconv.FormatUint(uint64(probe.ID), 10),
		"type":    probe.Type,
		"target":  probe.Target,
	}
	fingerprint := internalAlertFingerprint(fmt.Sprintf("probe|%d", probe.ID))

	if result.Status == "down" && probe.FailStreak >= probe.ConsecutiveFails {
		h.ingestAlert(source, alertPayload{
			Title: "拨测失败：" + probe.Name,
			Summary: fmt.Sprintf("%s %s 拨测失败（连续 %d 次）：%s",
				strings.ToUpper(probe.Type), probe.Target, probe.FailStreak, result.ErrorMsg),
			Severity: "critical", Value: strconv.FormatInt(result.CostMs, 10),
			Fingerprint: fingerprint, Labels: labels,
		})
		return
	}
	if result.Status == "up" {
		h.ingestAlert(source, alertPayload{
			Title: "拨测恢复：" + probe.Name, Status: "resolved",
			Fingerprint: fingerprint, Labels: labels,
		})
	}
}

// ---------- 接口 ----------

type probeReq struct {
	Name             string `json:"name" binding:"required"`
	Type             string `json:"type"`
	Target           string `json:"target" binding:"required"`
	Method           string `json:"method"`
	ExpectStatus     *int   `json:"expectStatus"`
	ExpectKeyword    string `json:"expectKeyword"`
	TimeoutSec       int    `json:"timeoutSec"`
	AlertEnabled     *bool  `json:"alertEnabled"`
	ConsecutiveFails int    `json:"consecutiveFails"`
	Enabled          *bool  `json:"enabled"`
	Remark           string `json:"remark"`
}

func (req *probeReq) normalize() error {
	req.Target = strings.TrimSpace(req.Target)
	if req.Type != "tcp" {
		req.Type = "http"
	}

	if req.Type == "http" {
		parsed, err := url.Parse(req.Target)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return fmt.Errorf("HTTP 拨测目标需要是完整 URL，例如 http://127.0.0.1:8080/healthz")
		}
		switch strings.ToUpper(req.Method) {
		case "", http.MethodGet:
			req.Method = http.MethodGet
		case http.MethodHead:
			req.Method = http.MethodHead
		case http.MethodPost:
			req.Method = http.MethodPost
		default:
			return fmt.Errorf("拨测只支持 GET / HEAD / POST")
		}
	} else {
		host, port, err := net.SplitHostPort(req.Target)
		if err != nil || host == "" || port == "" {
			return fmt.Errorf("TCP 拨测目标需要是 host:port，例如 127.0.0.1:3306")
		}
		req.Method = ""
	}

	if req.TimeoutSec <= 0 {
		req.TimeoutSec = 10
	}
	if req.TimeoutSec > 60 {
		return fmt.Errorf("超时最多 60 秒")
	}
	if req.ConsecutiveFails <= 0 {
		req.ConsecutiveFails = 1
	}
	if req.ConsecutiveFails > 10 {
		return fmt.Errorf("连续失败次数最多 10 次")
	}
	if req.ExpectStatus != nil && (*req.ExpectStatus < 0 || *req.ExpectStatus > 599) {
		return fmt.Errorf("期望状态码需在 0-599 之间，0 表示只要不是 4xx/5xx 就算通")
	}
	return nil
}

func (h *Handler) ListProbes(c *gin.Context) {
	var list []model.Probe
	q := h.DB.Model(&model.Probe{})
	if status := c.Query("status"); status != "" {
		q = q.Where("last_status = ?", status)
	}
	if err := q.Order("id asc").Find(&list).Error; err != nil {
		response.Error(c, "查询拨测失败")
		return
	}
	response.OK(c, list)
}

func (h *Handler) CreateProbe(c *gin.Context) {
	var req probeReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "名称与目标为必填项")
		return
	}
	if err := req.normalize(); err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	item := model.Probe{
		Name: req.Name, Type: req.Type, Target: req.Target, Method: req.Method,
		ExpectStatus: 200, ExpectKeyword: req.ExpectKeyword, TimeoutSec: req.TimeoutSec,
		AlertEnabled: true, ConsecutiveFails: req.ConsecutiveFails,
		LastStatus: "unknown", Enabled: true, Remark: req.Remark,
		CreatedBy: middleware.CurrentUser(c).ID,
	}
	if req.Type == "tcp" {
		item.ExpectStatus = 0
	}
	if req.ExpectStatus != nil {
		item.ExpectStatus = *req.ExpectStatus
	}
	if req.AlertEnabled != nil {
		item.AlertEnabled = *req.AlertEnabled
	}
	if req.Enabled != nil {
		item.Enabled = *req.Enabled
	}
	// GORM 的坑：带 default 的字段在 Create 后会被回填成库默认值，
	// 所以用户关掉的 false 会在 Create 之后变回 true。先记住意图，插完再显式写回。
	wantAlert, wantEnabled := item.AlertEnabled, item.Enabled
	if err := h.DB.Create(&item).Error; err != nil {
		response.Error(c, "创建失败")
		return
	}
	item.AlertEnabled, item.Enabled = wantAlert, wantEnabled
	h.DB.Model(&model.Probe{}).Where("id = ?", item.ID).Updates(map[string]any{
		"alert_enabled": wantAlert, "enabled": wantEnabled,
	})

	// 建完立刻拨一次，避免列表长期停在「未拨测」
	h.executeProbe(item, middleware.CurrentUser(c).Username)
	var fresh model.Probe
	h.DB.First(&fresh, item.ID)
	response.OK(c, fresh)
}

func (h *Handler) UpdateProbe(c *gin.Context) {
	var item model.Probe
	if err := h.DB.First(&item, idParam(c)).Error; err != nil {
		response.NotFound(c, "拨测不存在")
		return
	}

	var req probeReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "参数校验失败")
		return
	}
	if err := req.normalize(); err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	item.Name, item.Type, item.Target, item.Method = req.Name, req.Type, req.Target, req.Method
	item.ExpectKeyword, item.TimeoutSec = req.ExpectKeyword, req.TimeoutSec
	item.ConsecutiveFails, item.Remark = req.ConsecutiveFails, req.Remark
	if req.ExpectStatus != nil {
		item.ExpectStatus = *req.ExpectStatus
	}
	if req.AlertEnabled != nil {
		item.AlertEnabled = *req.AlertEnabled
	}
	if req.Enabled != nil {
		item.Enabled = *req.Enabled
	}
	// 目标或判定条件变了，之前的连续失败次数不再可比
	item.FailStreak = 0
	if err := h.DB.Save(&item).Error; err != nil {
		response.Error(c, "更新失败")
		return
	}
	response.OK(c, item)
}

func (h *Handler) DeleteProbe(c *gin.Context) {
	var item model.Probe
	if err := h.DB.First(&item, idParam(c)).Error; err != nil {
		response.NotFound(c, "拨测不存在")
		return
	}
	// 先关掉可能还在触发的告警，再删记录与拨测本身
	if item.AlertEnabled && item.LastStatus == "down" {
		h.syncProbeAlert(item, probeResult{Status: "up"})
	}
	h.DB.Where("probe_id = ?", item.ID).Delete(&model.ProbeRecord{})
	if err := h.DB.Delete(&model.Probe{}, item.ID).Error; err != nil {
		response.Error(c, "删除失败")
		return
	}
	response.OK(c, nil)
}

// RunProbe 立即拨测一次，与定时拨测走同一条代码路径
func (h *Handler) RunProbe(c *gin.Context) {
	var item model.Probe
	if err := h.DB.First(&item, idParam(c)).Error; err != nil {
		response.NotFound(c, "拨测不存在")
		return
	}
	result, streak := h.executeProbe(item, middleware.CurrentUser(c).Username)
	response.OK(c, gin.H{
		"status": result.Status, "code": result.Code,
		"costMs": result.CostMs, "errorMsg": result.ErrorMsg,
		"failStreak": streak, "needStreak": item.ConsecutiveFails,
	})
}

// ListProbeRecords 拨测历史，默认按时间倒序
func (h *Handler) ListProbeRecords(c *gin.Context) {
	page, size := pageParams(c)
	q := h.DB.Model(&model.ProbeRecord{})
	if probeID := c.Query("probeId"); probeID != "" {
		q = q.Where("probe_id = ?", probeID)
	}
	if status := c.Query("status"); status != "" {
		q = q.Where("status = ?", status)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		response.Error(c, "查询拨测记录失败")
		return
	}
	var list []model.ProbeRecord
	if err := q.Order("id desc").Offset((page - 1) * size).Limit(size).Find(&list).Error; err != nil {
		response.Error(c, "查询拨测记录失败")
		return
	}
	response.OKPage(c, list, total, page, size)
}

// RunProbesForSchedule 定时拨测入口：并发拨测全部启用中的拨测，并清理过期记录
func (h *Handler) RunProbesForSchedule() {
	var list []model.Probe
	if err := h.DB.Where("enabled = ?", true).Find(&list).Error; err != nil {
		log.Printf("[probe] 读取拨测列表失败: %v", err)
		return
	}

	// 记录的清理统一交给「数据留存」的定时任务（retention.probe_record_days），
	// 这里不再各自为政
	if len(list) == 0 {
		return
	}

	var mu sync.Mutex
	down := 0
	sem := make(chan struct{}, probeConcurrency)
	var wg sync.WaitGroup
	for i := range list {
		wg.Add(1)
		go func(probe model.Probe) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			if result, _ := h.executeProbe(probe, "scheduler"); result.Status == "down" {
				mu.Lock()
				down++
				mu.Unlock()
			}
		}(list[i])
	}
	wg.Wait()

	info := fmt.Sprintf("共 %d 个拨测，失败 %d", len(list), down)
	h.markFixedRun("probe", info)
	log.Printf("[probe] 定时拨测完成: %s", info)
}
