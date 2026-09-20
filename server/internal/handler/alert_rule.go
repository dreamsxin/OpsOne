package handler

import (
	"fmt"
	"log"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"ops-platform/server/internal/middleware"
	"ops-platform/server/internal/model"
	"ops-platform/server/internal/response"
)

// ruleAlertSourceName 规则命中产生的告警挂在这个内部接入源下
const ruleAlertSourceName = "告警规则"

// metricDef 一个可被规则引用的内置指标。
//
// 指标全部读平台自己的表，所以「规则能不能跑」不依赖任何外部监控系统。
type metricDef struct {
	Key      string
	Label    string
	Unit     string
	Windowed bool // 是否使用规则上的「最近 N 分钟」窗口
	Hint     string
	// Eval 返回当前值与一句人能看懂的明细
	Eval func(h *Handler, window time.Duration) (float64, string)
}

// countWithNames 统计数量并附带前几个对象的名字，让告警内容可直接定位
func countWithNames(h *Handler, table any, names *[]string, where string, args ...any) (float64, string) {
	var total int64
	h.DB.Model(table).Where(where, args...).Count(&total)
	if total == 0 {
		return 0, "无"
	}
	h.DB.Model(table).Where(where, args...).Limit(3).Pluck("name", names)

	detail := strings.Join(*names, "、")
	if total > int64(len(*names)) {
		detail += fmt.Sprintf(" 等 %d 个", total)
	}
	return float64(total), detail
}

func plainCount(h *Handler, table any, where string, args ...any) (float64, string) {
	var total int64
	q := h.DB.Model(table)
	if where != "" {
		q = q.Where(where, args...)
	}
	q.Count(&total)
	return float64(total), fmt.Sprintf("当前 %d", total)
}

// builtinMetrics 内置指标注册表。新增指标只要在这里加一条，规则页会自动出现。
var builtinMetrics = []metricDef{
	{
		Key: "host.offline", Label: "探测离线的主机数", Unit: "台",
		Hint: "依赖主机连通性探测的结果，从未探测过的主机不计入",
		Eval: func(h *Handler, _ time.Duration) (float64, string) {
			var names []string
			return countWithNames(h, &model.Host{}, &names, "status = ?", "offline")
		},
	},
	{
		Key: "host.stale", Label: "超过窗口未探测的主机数", Unit: "台", Windowed: true,
		Hint: "包含从未探测过的主机，用来发现「探测本身停了」",
		Eval: func(h *Handler, window time.Duration) (float64, string) {
			var names []string
			return countWithNames(h, &model.Host{}, &names,
				"checked_at IS NULL OR checked_at < ?", time.Now().Add(-window))
		},
	},
	{
		Key: "cert.expiring", Label: "即将到期的证书数", Unit: "个",
		Eval: func(h *Handler, _ time.Duration) (float64, string) {
			var names []string
			return countWithNames(h, &model.Certificate{}, &names, "status = ?", "expiring")
		},
	},
	{
		Key: "cert.expired", Label: "已过期的证书数", Unit: "个",
		Eval: func(h *Handler, _ time.Duration) (float64, string) {
			var names []string
			return countWithNames(h, &model.Certificate{}, &names, "status = ?", "expired")
		},
	},
	{
		Key: "cert.error", Label: "巡检失败的证书数", Unit: "个",
		Eval: func(h *Handler, _ time.Duration) (float64, string) {
			var names []string
			return countWithNames(h, &model.Certificate{}, &names, "status = ?", "error")
		},
	},
	{
		Key: "exec.failed_jobs", Label: "窗口内有失败主机的批量作业数", Unit: "个", Windowed: true,
		Eval: func(h *Handler, window time.Duration) (float64, string) {
			return plainCount(h, &model.ExecJob{},
				"started_at >= ? AND failed_num > 0", time.Now().Add(-window))
		},
	},
	{
		Key: "cron.failed_runs", Label: "窗口内失败的定时任务运行数", Unit: "次", Windowed: true,
		Hint: "统计由调度器触发且结果不是「全部成功」的运行",
		Eval: func(h *Handler, window time.Duration) (float64, string) {
			return plainCount(h, &model.ExecJob{},
				"source = ? AND started_at >= ? AND status <> ?", "cron", time.Now().Add(-window), "success")
		},
	},
	{
		Key: "build.failed", Label: "窗口内失败的构建数", Unit: "次", Windowed: true,
		Hint: "只统计已同步到最终状态的构建记录",
		Eval: func(h *Handler, window time.Duration) (float64, string) {
			return plainCount(h, &model.BuildRecord{},
				"status = ? AND started_at >= ?", "failure", time.Now().Add(-window))
		},
	},
	{
		Key: "alert.firing", Label: "未处理的告警数", Unit: "条",
		Hint: "统计 status=firing 的告警，用来发现「告警堆积没人看」",
		Eval: func(h *Handler, _ time.Duration) (float64, string) {
			return plainCount(h, &model.Alert{}, "status = ?", "firing")
		},
	},
	{
		Key: "alert.critical_unresolved", Label: "未恢复的严重告警数", Unit: "条",
		Eval: func(h *Handler, _ time.Duration) (float64, string) {
			return plainCount(h, &model.Alert{}, "status <> ? AND severity = ?", "resolved", "critical")
		},
	},
	{
		Key: "notify.failed", Label: "窗口内投递失败的通知数", Unit: "条", Windowed: true,
		Hint: "通知发不出去时告警本身也送不到人，这条用来兜底",
		Eval: func(h *Handler, window time.Duration) (float64, string) {
			return plainCount(h, &model.NotifyRecord{},
				"status <> ? AND created_at >= ?", "success", time.Now().Add(-window))
		},
	},
}

func findMetric(key string) *metricDef {
	for i := range builtinMetrics {
		if builtinMetrics[i].Key == key {
			return &builtinMetrics[i]
		}
	}
	return nil
}

var comparatorLabels = map[string]string{
	"gt": ">", "gte": ">=", "lt": "<", "lte": "<=",
}

func compareValue(value, threshold float64, comparator string) bool {
	switch comparator {
	case "gte":
		return value >= threshold
	case "lt":
		return value < threshold
	case "lte":
		return value <= threshold
	default: // gt
		return value > threshold
	}
}

// ---------- 接口 ----------

// ListAlertRuleMetrics 内置指标清单，带当前取值，便于建规则时直接看到基线
func (h *Handler) ListAlertRuleMetrics(c *gin.Context) {
	window := time.Hour
	if raw := c.Query("windowMinutes"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			window = time.Duration(n) * time.Minute
		}
	}

	items := make([]gin.H, 0, len(builtinMetrics))
	for i := range builtinMetrics {
		metric := builtinMetrics[i]
		value, detail := metric.Eval(h, window)
		items = append(items, gin.H{
			"key": metric.Key, "label": metric.Label, "unit": metric.Unit,
			"windowed": metric.Windowed, "hint": metric.Hint,
			"currentValue": value, "detail": detail,
		})
	}
	response.OK(c, items)
}

type alertRuleReq struct {
	Name             string   `json:"name" binding:"required"`
	Metric           string   `json:"metric" binding:"required"`
	Comparator       string   `json:"comparator"`
	Threshold        *float64 `json:"threshold"`
	WindowMinutes    int      `json:"windowMinutes"`
	ConsecutiveTimes int      `json:"consecutiveTimes"`
	Severity         string   `json:"severity"`
	Enabled          *bool    `json:"enabled"`
	Remark           string   `json:"remark"`
}

func (req *alertRuleReq) normalize() error {
	if findMetric(req.Metric) == nil {
		return fmt.Errorf("不支持的指标: %s", req.Metric)
	}
	if _, ok := comparatorLabels[req.Comparator]; !ok {
		req.Comparator = "gt"
	}
	if req.Threshold == nil {
		return fmt.Errorf("请填写阈值")
	}
	if math.IsNaN(*req.Threshold) || math.IsInf(*req.Threshold, 0) {
		return fmt.Errorf("阈值不是有效数字")
	}
	if req.WindowMinutes <= 0 {
		req.WindowMinutes = 60
	}
	if req.WindowMinutes > 7*24*60 {
		return fmt.Errorf("窗口最长 7 天")
	}
	if req.ConsecutiveTimes <= 0 {
		req.ConsecutiveTimes = 1
	}
	if req.ConsecutiveTimes > 10 {
		return fmt.Errorf("连续命中次数最多 10 次")
	}
	req.Severity = normalizeSeverity(req.Severity)
	return nil
}

func (h *Handler) ListAlertRules(c *gin.Context) {
	var list []model.AlertRule
	q := h.DB.Model(&model.AlertRule{})
	if status := c.Query("status"); status != "" {
		q = q.Where("last_status = ?", status)
	}
	if err := q.Order("id asc").Find(&list).Error; err != nil {
		response.Error(c, "查询告警规则失败")
		return
	}
	response.OK(c, list)
}

func (h *Handler) CreateAlertRule(c *gin.Context) {
	var req alertRuleReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "名称与指标为必填项")
		return
	}
	if err := req.normalize(); err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	item := model.AlertRule{
		Name: req.Name, Metric: req.Metric, Comparator: req.Comparator,
		Threshold: *req.Threshold, WindowMinutes: req.WindowMinutes,
		ConsecutiveTimes: req.ConsecutiveTimes, Severity: req.Severity,
		LastStatus: "unknown", Enabled: true, Remark: req.Remark,
		CreatedBy: middleware.CurrentUser(c).ID,
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

func (h *Handler) UpdateAlertRule(c *gin.Context) {
	var item model.AlertRule
	if err := h.DB.First(&item, idParam(c)).Error; err != nil {
		response.NotFound(c, "规则不存在")
		return
	}

	var req alertRuleReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "参数校验失败")
		return
	}
	if err := req.normalize(); err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	item.Name, item.Metric, item.Comparator = req.Name, req.Metric, req.Comparator
	item.Threshold, item.WindowMinutes = *req.Threshold, req.WindowMinutes
	item.ConsecutiveTimes, item.Severity = req.ConsecutiveTimes, req.Severity
	item.Remark = req.Remark
	// 条件变了，之前累积的连续命中次数不再可比，重新开始计数
	item.HitStreak = 0
	if req.Enabled != nil {
		item.Enabled = *req.Enabled
	}
	if err := h.DB.Save(&item).Error; err != nil {
		response.Error(c, "更新失败")
		return
	}
	response.OK(c, item)
}

func (h *Handler) DeleteAlertRule(c *gin.Context) {
	var item model.AlertRule
	if err := h.DB.First(&item, idParam(c)).Error; err != nil {
		response.NotFound(c, "规则不存在")
		return
	}
	// 规则删除后它产生的告警不跟着删，先把还在触发中的关掉，避免留下无主告警
	h.resolveRuleAlert(item)
	if err := h.DB.Delete(&model.AlertRule{}, item.ID).Error; err != nil {
		response.Error(c, "删除失败")
		return
	}
	response.OK(c, nil)
}

// EvaluateAlertRule 立即评估一条规则（试跑）。
// 会真实写入状态与告警，和定时评估走同一条路径，避免「试跑通了、定时不灵」。
func (h *Handler) EvaluateAlertRule(c *gin.Context) {
	var item model.AlertRule
	if err := h.DB.First(&item, idParam(c)).Error; err != nil {
		response.NotFound(c, "规则不存在")
		return
	}
	result := h.evaluateRule(&item)
	response.OK(c, result)
}

// ---------- 评估 ----------

// evaluateRule 取指标、判阈值、按连续命中次数决定是否告警，并回写规则状态
func (h *Handler) evaluateRule(rule *model.AlertRule) gin.H {
	metric := findMetric(rule.Metric)
	now := time.Now()
	if metric == nil {
		// 指标被下线（例如升级后改名），明确标成 error 而不是假装 ok
		detail := "指标不存在: " + rule.Metric
		h.DB.Model(&model.AlertRule{}).Where("id = ?", rule.ID).Updates(map[string]any{
			"last_status": "error", "last_detail": detail, "last_eval_at": &now, "hit_streak": 0,
		})
		return gin.H{"status": "error", "detail": detail}
	}

	window := time.Duration(rule.WindowMinutes) * time.Minute
	value, detail := metric.Eval(h, window)
	hit := compareValue(value, rule.Threshold, rule.Comparator)

	streak := 0
	if hit {
		streak = rule.HitStreak + 1
	}
	firing := hit && streak >= rule.ConsecutiveTimes

	updates := map[string]any{
		"hit_streak": streak, "last_value": value,
		"last_detail": truncate(detail, 240), "last_eval_at": &now,
	}
	if firing {
		updates["last_status"] = "firing"
		updates["last_fire_at"] = &now
	} else {
		updates["last_status"] = "ok"
	}
	h.DB.Model(&model.AlertRule{}).Where("id = ?", rule.ID).Updates(updates)

	switch {
	case firing:
		h.fireRuleAlert(*rule, *metric, value, detail)
	case rule.LastStatus == "firing":
		// 上一轮在触发，这一轮不满足条件，关掉对应告警
		h.resolveRuleAlert(*rule)
	}

	return gin.H{
		"status":    updates["last_status"],
		"value":     value,
		"threshold": rule.Threshold,
		"expression": fmt.Sprintf("%s %s %g %s", metric.Label,
			comparatorLabels[rule.Comparator], rule.Threshold, metric.Unit),
		"hit":        hit,
		"hitStreak":  streak,
		"needStreak": rule.ConsecutiveTimes,
		"detail":     detail,
	}
}

func (h *Handler) fireRuleAlert(rule model.AlertRule, metric metricDef, value float64, detail string) {
	source, err := h.internalAlertSource(ruleAlertSourceName)
	if err != nil {
		log.Printf("[rule] 内部告警源不可用，跳过告警: %v", err)
		return
	}

	summary := fmt.Sprintf("%s 当前 %g %s，阈值 %s %g；明细：%s",
		metric.Label, value, metric.Unit, comparatorLabels[rule.Comparator], rule.Threshold, detail)
	if metric.Windowed {
		summary = fmt.Sprintf("最近 %d 分钟内，", rule.WindowMinutes) + summary
	}

	h.ingestAlert(source, alertPayload{
		Title:       "规则触发：" + rule.Name,
		Summary:     summary,
		Severity:    rule.Severity,
		Value:       strconv.FormatFloat(value, 'f', -1, 64),
		Fingerprint: ruleAlertFingerprint(rule.ID),
		Labels: map[string]string{
			"module": "alert_rule",
			"ruleId": strconv.FormatUint(uint64(rule.ID), 10),
			"metric": rule.Metric,
		},
	})
}

func (h *Handler) resolveRuleAlert(rule model.AlertRule) {
	source, err := h.internalAlertSource(ruleAlertSourceName)
	if err != nil {
		return
	}
	h.ingestAlert(source, alertPayload{
		Title: "规则恢复：" + rule.Name, Status: "resolved",
		Fingerprint: ruleAlertFingerprint(rule.ID),
		Labels: map[string]string{
			"module": "alert_rule",
			"ruleId": strconv.FormatUint(uint64(rule.ID), 10),
		},
	})
}

func ruleAlertFingerprint(ruleID uint) string {
	return internalAlertFingerprint(fmt.Sprintf("alert_rule|%d", ruleID))
}

// EvaluateAlertRulesForSchedule 定时评估入口：处理全部启用中的规则
func (h *Handler) EvaluateAlertRulesForSchedule() {
	var rules []model.AlertRule
	if err := h.DB.Where("enabled = ?", true).Find(&rules).Error; err != nil {
		log.Printf("[rule] 读取告警规则失败: %v", err)
		return
	}
	if len(rules) == 0 {
		return
	}

	firing := 0
	for i := range rules {
		result := h.evaluateRule(&rules[i])
		if result["status"] == "firing" {
			firing++
		}
	}
	h.markFixedRun("rule", fmt.Sprintf("共 %d 条规则，触发 %d 条", len(rules), firing))
	log.Printf("[rule] 定时评估完成: 共 %d 条规则，触发 %d 条", len(rules), firing)
}
