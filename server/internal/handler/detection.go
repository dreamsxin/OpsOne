package handler

import (
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"ops-platform/server/internal/middleware"
	"ops-platform/server/internal/model"
	"ops-platform/server/internal/response"
)

// detectionAlertSourceName 检测命中产生的告警挂在这个内部接入源下
const detectionAlertSourceName = "检测规则"

// 检测模式
const (
	detectSequence   = "sequence"   // 顺序链：按步骤先后发生
	detectConcurrent = "concurrent" // 并发窗：同一窗口内都出现，不论先后
	detectJoin       = "join"       // 窗口 Join：都出现且落在同一个对象上
)

var detectionModeLabels = map[string]string{
	detectSequence:   "顺序链",
	detectConcurrent: "并发窗",
	detectJoin:       "窗口 Join",
}

// detectionStep 一步的告警匹配条件。留空的字段表示不限制。
type detectionStep struct {
	Name         string `json:"name"`
	TitleKeyword string `json:"titleKeyword"` // 匹配标题或摘要，忽略大小写
	Source       string `json:"source"`       // 接入源名称，精确匹配
	Severity     string `json:"severity"`     // info | warning | critical
	LabelKey     string `json:"labelKey"`     // 要求带这个标签
	LabelValue   string `json:"labelValue"`   // 且标签值等于它（留空只要求标签存在）
}

// label 给页面与告警摘要用的一句话描述
func (s detectionStep) label(index int) string {
	if s.Name != "" {
		return s.Name
	}
	parts := make([]string, 0, 4)
	if s.TitleKeyword != "" {
		parts = append(parts, "含「"+s.TitleKeyword+"」")
	}
	if s.Source != "" {
		parts = append(parts, "源="+s.Source)
	}
	if s.Severity != "" {
		parts = append(parts, "级别="+s.Severity)
	}
	if s.LabelKey != "" {
		if s.LabelValue != "" {
			parts = append(parts, s.LabelKey+"="+s.LabelValue)
		} else {
			parts = append(parts, "带标签 "+s.LabelKey)
		}
	}
	if len(parts) == 0 {
		return fmt.Sprintf("第 %d 步（不限条件）", index+1)
	}
	return strings.Join(parts, " 且 ")
}

// alertLabels 解析告警上的标签，坏 JSON 当作没有标签而不是报错
func alertLabels(alert model.Alert) map[string]string {
	labels := map[string]string{}
	if alert.Labels != "" {
		_ = json.Unmarshal([]byte(alert.Labels), &labels)
	}
	return labels
}

// stepMatches 判断一条告警是否满足这一步的条件（各条件是 AND 关系）
func stepMatches(step detectionStep, alert model.Alert) bool {
	if step.TitleKeyword != "" {
		keyword := strings.ToLower(step.TitleKeyword)
		if !strings.Contains(strings.ToLower(alert.Title), keyword) &&
			!strings.Contains(strings.ToLower(alert.Summary), keyword) {
			return false
		}
	}
	if step.Source != "" && step.Source != alert.SourceName {
		return false
	}
	if step.Severity != "" && step.Severity != alert.Severity {
		return false
	}
	if step.LabelKey != "" {
		labels := alertLabels(alert)
		value, ok := labels[step.LabelKey]
		if !ok {
			return false
		}
		if step.LabelValue != "" && step.LabelValue != value {
			return false
		}
	}
	return true
}

// matchedAlert 命中详情里的一条告警
type matchedAlert struct {
	ID          uint      `json:"id"`
	Title       string    `json:"title"`
	Severity    string    `json:"severity"`
	SourceName  string    `json:"sourceName"`
	Status      string    `json:"status"`
	FirstSeenAt time.Time `json:"firstSeenAt"`
}

func toMatched(alert model.Alert) matchedAlert {
	return matchedAlert{
		ID: alert.ID, Title: alert.Title, Severity: alert.Severity,
		SourceName: alert.SourceName, Status: alert.Status, FirstSeenAt: alert.FirstSeenAt,
	}
}

// stepResult 一步的匹配结果
type stepResult struct {
	Index   int            `json:"index"`
	Label   string         `json:"label"`
	Matched []matchedAlert `json:"matched"`
	Hit     bool           `json:"hit"`
}

// detectionOutcome 一次检测的完整结果
type detectionOutcome struct {
	Hit        bool         `json:"hit"`
	Steps      []stepResult `json:"steps"`
	JoinValues []string     `json:"joinValues"` // join 模式下同时命中所有步骤的对象
	Detail     string       `json:"detail"`
}

// detect 在给定告警集合上跑一次检测。
//
// 纯函数（不碰数据库），方便单测把各种时间组合摆出来验证。
//
// 说明一个已知取舍：concurrent / join 模式按步独立判定，同一条告警可以同时满足多步。
// 真要区分请把步骤条件写得互斥；预演页会把每步命中的告警列出来，写重了一眼能看见。
func detect(mode, joinLabel string, steps []detectionStep, alerts []model.Alert) detectionOutcome {
	if len(steps) == 0 {
		return detectionOutcome{Hit: false, Steps: []stepResult{}, Detail: "没有配置步骤"}
	}

	// 按开始时间排序：顺序链要看先后，其余模式排序也让命中列表更好读
	sorted := make([]model.Alert, len(alerts))
	copy(sorted, alerts)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].FirstSeenAt.Equal(sorted[j].FirstSeenAt) {
			return sorted[i].ID < sorted[j].ID
		}
		return sorted[i].FirstSeenAt.Before(sorted[j].FirstSeenAt)
	})

	results := make([]stepResult, len(steps))
	for i, step := range steps {
		results[i] = stepResult{Index: i, Label: step.label(i), Matched: []matchedAlert{}}
		for _, alert := range sorted {
			if stepMatches(step, alert) {
				results[i].Matched = append(results[i].Matched, toMatched(alert))
			}
		}
		results[i].Hit = len(results[i].Matched) > 0
	}

	switch mode {
	case detectSequence:
		// 贪心取每步最早、且排在上一步之后的那条：存在这样一条链就算命中。
		// 用下标推进而不是只比时间，避免同一条告警把两步都顶掉。
		var chain []matchedAlert
		cursor := -1
		for i, step := range steps {
			picked := -1
			for j := cursor + 1; j < len(sorted); j++ {
				if stepMatches(step, sorted[j]) {
					picked = j
					break
				}
			}
			if picked < 0 {
				return detectionOutcome{
					Hit: false, Steps: results,
					Detail: fmt.Sprintf("顺序链断在第 %d 步「%s」：窗口内没有在上一步之后发生的匹配告警", i+1, step.label(i)),
				}
			}
			chain = append(chain, toMatched(sorted[picked]))
			cursor = picked
		}
		names := make([]string, 0, len(chain))
		for _, item := range chain {
			names = append(names, fmt.Sprintf("#%d %s（%s）", item.ID, item.Title, item.FirstSeenAt.Format("15:04:05")))
		}
		return detectionOutcome{
			Hit: true, Steps: results,
			Detail: "顺序链完整：" + strings.Join(names, " → "),
		}

	case detectJoin:
		if joinLabel == "" {
			return detectionOutcome{Hit: false, Steps: results, Detail: "窗口 Join 必须指定对齐标签"}
		}
		// 先把每步命中的标签值收集起来，再取交集
		valuesPerStep := make([]map[string]bool, len(steps))
		for i, step := range steps {
			valuesPerStep[i] = map[string]bool{}
			for _, alert := range sorted {
				if !stepMatches(step, alert) {
					continue
				}
				if value := alertLabels(alert)[joinLabel]; value != "" {
					valuesPerStep[i][value] = true
				}
			}
		}
		shared := []string{}
		for value := range valuesPerStep[0] {
			all := true
			for i := 1; i < len(valuesPerStep); i++ {
				if !valuesPerStep[i][value] {
					all = false
					break
				}
			}
			if all {
				shared = append(shared, value)
			}
		}
		sort.Strings(shared)
		if len(shared) == 0 {
			return detectionOutcome{
				Hit: false, Steps: results, JoinValues: shared,
				Detail: fmt.Sprintf("没有一个 %s 的取值同时命中全部 %d 步", joinLabel, len(steps)),
			}
		}
		return detectionOutcome{
			Hit: true, Steps: results, JoinValues: shared,
			Detail: fmt.Sprintf("%s=%s 上全部 %d 步都出现了", joinLabel, strings.Join(shared, "、"), len(steps)),
		}

	default: // detectConcurrent
		missing := make([]string, 0, len(steps))
		for i := range steps {
			if !results[i].Hit {
				missing = append(missing, fmt.Sprintf("第 %d 步「%s」", i+1, results[i].Label))
			}
		}
		if len(missing) > 0 {
			return detectionOutcome{
				Hit: false, Steps: results,
				Detail: "窗口内缺少：" + strings.Join(missing, "、"),
			}
		}
		total := 0
		for i := range results {
			total += len(results[i].Matched)
		}
		return detectionOutcome{
			Hit: true, Steps: results,
			Detail: fmt.Sprintf("全部 %d 步都在窗口内出现，共命中 %d 条告警", len(steps), total),
		}
	}
}

// windowAlerts 取窗口内「发生过」的告警。
//
// 两个刻意的选择：
//  1. 不过滤 resolved —— 检测关心的是「这段时间里发生了什么」，
//     一条已经恢复的告警照样说明当时出过问题。
//  2. 排除检测规则自己产生的告警 —— 命中告警的摘要里会引用原始告警标题，
//     如果把它也算作输入，规则就会被自己的输出顶住，永远不恢复（自激）。
//     其他内部源（告警规则、拨测、证书巡检）的告警是正常输入，不排除。
func (h *Handler) windowAlerts(window time.Duration) ([]model.Alert, error) {
	var alerts []model.Alert
	err := h.DB.Where("last_seen_at >= ? AND source_name <> ?",
		time.Now().Add(-window), detectionAlertSourceName).
		Order("first_seen_at asc").Find(&alerts).Error
	return alerts, err
}

func parseDetectionSteps(raw string) []detectionStep {
	steps := []detectionStep{}
	if raw != "" {
		_ = json.Unmarshal([]byte(raw), &steps)
	}
	return steps
}

// ---------- 接口 ----------

type detectionRuleReq struct {
	Name          string          `json:"name" binding:"required"`
	Mode          string          `json:"mode"`
	Steps         []detectionStep `json:"steps"`
	JoinLabel     string          `json:"joinLabel"`
	WindowMinutes int             `json:"windowMinutes"`
	Severity      string          `json:"severity"`
	Enabled       *bool           `json:"enabled"`
	Remark        string          `json:"remark"`
}

func (req *detectionRuleReq) normalize() error {
	if _, ok := detectionModeLabels[req.Mode]; !ok {
		req.Mode = detectConcurrent
	}
	// 一步的「检测」和告警规则没区别，至少两步才是关联
	if len(req.Steps) < 2 {
		return fmt.Errorf("至少需要两个步骤，单条条件请用「告警规则」")
	}
	if len(req.Steps) > 5 {
		return fmt.Errorf("最多 5 个步骤")
	}
	for i := range req.Steps {
		step := &req.Steps[i]
		step.Name = strings.TrimSpace(step.Name)
		step.TitleKeyword = strings.TrimSpace(step.TitleKeyword)
		step.Source = strings.TrimSpace(step.Source)
		step.LabelKey = strings.TrimSpace(step.LabelKey)
		step.LabelValue = strings.TrimSpace(step.LabelValue)
		if step.Severity != "" {
			step.Severity = normalizeSeverity(step.Severity)
		}
		// 条件全空的步骤会匹配到任意告警，等于没设条件
		if step.TitleKeyword == "" && step.Source == "" && step.Severity == "" && step.LabelKey == "" {
			return fmt.Errorf("第 %d 步没有任何匹配条件", i+1)
		}
	}
	req.JoinLabel = strings.TrimSpace(req.JoinLabel)
	if req.Mode == detectJoin && req.JoinLabel == "" {
		return fmt.Errorf("窗口 Join 必须指定用来对齐的标签键，例如 host")
	}
	if req.WindowMinutes <= 0 {
		req.WindowMinutes = 30
	}
	if req.WindowMinutes > 24*60 {
		return fmt.Errorf("关联窗口最长 24 小时")
	}
	req.Severity = normalizeSeverity(req.Severity)
	return nil
}

// ListDetectionMeta 建规则时要用的候选值：现有接入源与告警上出现过的标签键
func (h *Handler) ListDetectionMeta(c *gin.Context) {
	var sources []string
	h.DB.Model(&model.AlertSource{}).Order("name asc").Pluck("name", &sources)

	// 标签键从最近的告警里抽，避免让人凭记忆填
	var recent []model.Alert
	h.DB.Order("id desc").Limit(500).Find(&recent)
	keySet := map[string]bool{}
	for _, alert := range recent {
		for key := range alertLabels(alert) {
			keySet[key] = true
		}
	}
	keys := make([]string, 0, len(keySet))
	for key := range keySet {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	modes := make([]gin.H, 0, len(detectionModeLabels))
	for _, mode := range []string{detectConcurrent, detectSequence, detectJoin} {
		hint := map[string]string{
			detectConcurrent: "窗口内每一步都出现过就算命中，不看先后",
			detectSequence:   "必须按步骤顺序先后发生，用来表达因果",
			detectJoin:       "在并发窗之上再要求落在同一个对象上（同一台主机、同一个服务）",
		}[mode]
		modes = append(modes, gin.H{"key": mode, "label": detectionModeLabels[mode], "hint": hint})
	}
	response.OK(c, gin.H{"modes": modes, "sources": sources, "labelKeys": keys})
}

func (h *Handler) ListDetectionRules(c *gin.Context) {
	var list []model.DetectionRule
	if err := h.DB.Order("id asc").Find(&list).Error; err != nil {
		response.Error(c, "查询检测规则失败")
		return
	}
	type view struct {
		model.DetectionRule
		ModeLabel string          `json:"modeLabel"`
		StepList  []detectionStep `json:"stepList"`
	}
	views := make([]view, 0, len(list))
	for _, item := range list {
		views = append(views, view{
			DetectionRule: item,
			ModeLabel:     detectionModeLabels[item.Mode],
			StepList:      parseDetectionSteps(item.Steps),
		})
	}
	response.OK(c, views)
}

func (h *Handler) CreateDetectionRule(c *gin.Context) {
	var req detectionRuleReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "名称为必填项")
		return
	}
	if err := req.normalize(); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	steps, _ := json.Marshal(req.Steps)

	item := model.DetectionRule{
		Name: req.Name, Mode: req.Mode, Steps: string(steps),
		JoinLabel: req.JoinLabel, WindowMinutes: req.WindowMinutes,
		Severity: req.Severity, LastStatus: "unknown", Enabled: true,
		Remark: req.Remark, CreatedBy: middleware.CurrentUser(c).ID,
	}
	wantEnabled := true
	if req.Enabled != nil {
		wantEnabled = *req.Enabled
	}
	item.Enabled = wantEnabled
	if err := h.DB.Create(&item).Error; err != nil {
		response.Error(c, "创建失败")
		return
	}
	h.recordVersion(ruleTargetDetection, item.ID, "created", "新建", middleware.CurrentUser(c).Username)
	h.DB.First(&item, item.ID)
	response.OK(c, item)
}

func (h *Handler) UpdateDetectionRule(c *gin.Context) {
	var item model.DetectionRule
	if err := h.DB.First(&item, idParam(c)).Error; err != nil {
		response.NotFound(c, "规则不存在")
		return
	}
	var req detectionRuleReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "参数校验失败")
		return
	}
	if err := req.normalize(); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	steps, _ := json.Marshal(req.Steps)

	// 版本功能上线前就存在的规则，先把「改之前的样子」补成第一版
	h.backfillVersion(ruleTargetDetection, item.ID)

	updates := map[string]any{
		"name": req.Name, "mode": req.Mode, "steps": string(steps),
		"join_label": req.JoinLabel, "window_minutes": req.WindowMinutes,
		"severity": req.Severity, "remark": req.Remark,
	}
	if req.Enabled != nil {
		updates["enabled"] = *req.Enabled
	}
	if err := h.DB.Model(&model.DetectionRule{}).Where("id = ?", item.ID).Updates(updates).Error; err != nil {
		response.Error(c, "更新失败")
		return
	}
	h.recordVersion(ruleTargetDetection, item.ID, "edited", "", middleware.CurrentUser(c).Username)
	h.DB.First(&item, item.ID)
	response.OK(c, item)
}

func (h *Handler) DeleteDetectionRule(c *gin.Context) {
	var item model.DetectionRule
	if err := h.DB.First(&item, idParam(c)).Error; err != nil {
		response.NotFound(c, "规则不存在")
		return
	}
	// 删之前留一版「删掉之前长这样」——留着它，误删才能恢复出来
	h.recordVersionBeforeDelete(ruleTargetDetection, item.ID, middleware.CurrentUser(c).Username)
	// 和告警规则一致：先把还在触发的关掉，别留下无主告警
	h.resolveDetectionAlert(item)
	if err := h.DB.Delete(&model.DetectionRule{}, item.ID).Error; err != nil {
		response.Error(c, "删除失败")
		return
	}
	response.OK(c, nil)
}

// PreviewDetection 用请求里的草稿条件看每一步各命中哪些告警，不落库、不写告警。
// 建规则时先拿它确认关键字真能匹配上东西。
func (h *Handler) PreviewDetection(c *gin.Context) {
	var req detectionRuleReq
	req.Name = "preview"
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "参数错误")
		return
	}
	if err := req.normalize(); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	alerts, err := h.windowAlerts(time.Duration(req.WindowMinutes) * time.Minute)
	if err != nil {
		response.Error(c, "读取告警失败")
		return
	}
	outcome := detect(req.Mode, req.JoinLabel, req.Steps, alerts)
	response.OK(c, gin.H{
		"scanned": len(alerts), "mode": req.Mode,
		"modeLabel": detectionModeLabels[req.Mode], "outcome": outcome,
	})
}

// EvaluateDetectionRule 立即评估一条规则（试跑）。
// 和定时评估走同一条路径并真实写告警，避免「试跑通了、定时不灵」。
func (h *Handler) EvaluateDetectionRule(c *gin.Context) {
	var item model.DetectionRule
	if err := h.DB.First(&item, idParam(c)).Error; err != nil {
		response.NotFound(c, "规则不存在")
		return
	}
	response.OK(c, h.evaluateDetectionRule(&item))
}

// ---------- 评估 ----------

func (h *Handler) evaluateDetectionRule(rule *model.DetectionRule) gin.H {
	now := time.Now()
	steps := parseDetectionSteps(rule.Steps)
	if len(steps) < 2 {
		detail := "规则步骤缺失或已损坏，请重新编辑保存"
		h.DB.Model(&model.DetectionRule{}).Where("id = ?", rule.ID).Updates(map[string]any{
			"last_status": "error", "last_detail": detail, "last_eval_at": &now,
		})
		return gin.H{"status": "error", "detail": detail}
	}

	alerts, err := h.windowAlerts(time.Duration(rule.WindowMinutes) * time.Minute)
	if err != nil {
		detail := "读取告警失败: " + err.Error()
		h.DB.Model(&model.DetectionRule{}).Where("id = ?", rule.ID).Updates(map[string]any{
			"last_status": "error", "last_detail": truncate(detail, 480), "last_eval_at": &now,
		})
		return gin.H{"status": "error", "detail": detail}
	}

	outcome := detect(rule.Mode, rule.JoinLabel, steps, alerts)
	updates := map[string]any{
		"last_detail": truncate(outcome.Detail, 480), "last_eval_at": &now,
	}
	if outcome.Hit {
		updates["last_status"] = "firing"
		updates["last_fire_at"] = &now
	} else {
		updates["last_status"] = "ok"
	}
	h.DB.Model(&model.DetectionRule{}).Where("id = ?", rule.ID).Updates(updates)

	switch {
	case outcome.Hit:
		h.fireDetectionAlert(*rule, outcome)
	case rule.LastStatus == "firing":
		h.resolveDetectionAlert(*rule)
	}

	return gin.H{
		"status": updates["last_status"], "scanned": len(alerts),
		"modeLabel": detectionModeLabels[rule.Mode], "outcome": outcome,
	}
}

func (h *Handler) fireDetectionAlert(rule model.DetectionRule, outcome detectionOutcome) {
	source, err := h.internalAlertSource(detectionAlertSourceName)
	if err != nil {
		log.Printf("[detection] 内部告警源不可用，跳过告警: %v", err)
		return
	}
	labels := map[string]string{
		"module":     "detection_rule",
		"detectMode": rule.Mode,
		"ruleId":     strconv.FormatUint(uint64(rule.ID), 10),
	}
	if rule.Mode == detectJoin && len(outcome.JoinValues) > 0 {
		labels[rule.JoinLabel] = strings.Join(outcome.JoinValues, ",")
	}
	h.ingestAlert(source, alertPayload{
		Title: "检测命中：" + rule.Name,
		Summary: fmt.Sprintf("%s，最近 %d 分钟窗口内：%s",
			detectionModeLabels[rule.Mode], rule.WindowMinutes, outcome.Detail),
		Severity:    rule.Severity,
		Fingerprint: detectionAlertFingerprint(rule.ID),
		Labels:      labels,
	})
}

func (h *Handler) resolveDetectionAlert(rule model.DetectionRule) {
	source, err := h.internalAlertSource(detectionAlertSourceName)
	if err != nil {
		return
	}
	h.ingestAlert(source, alertPayload{
		Title: "检测恢复：" + rule.Name, Status: "resolved",
		Fingerprint: detectionAlertFingerprint(rule.ID),
		Labels: map[string]string{
			"module": "detection_rule",
			"ruleId": strconv.FormatUint(uint64(rule.ID), 10),
		},
	})
}

func detectionAlertFingerprint(ruleID uint) string {
	return internalAlertFingerprint(fmt.Sprintf("detection_rule|%d", ruleID))
}

// EvaluateDetectionRulesForSchedule 定时评估入口
func (h *Handler) EvaluateDetectionRulesForSchedule() {
	var rules []model.DetectionRule
	if err := h.DB.Where("enabled = ?", true).Find(&rules).Error; err != nil {
		log.Printf("[detection] 读取检测规则失败: %v", err)
		return
	}
	if len(rules) == 0 {
		return
	}
	firing := 0
	for i := range rules {
		if h.evaluateDetectionRule(&rules[i])["status"] == "firing" {
			firing++
		}
	}
	h.markFixedRun("detection", fmt.Sprintf("共 %d 条规则，命中 %d 条", len(rules), firing))
	log.Printf("[detection] 定时评估完成: 共 %d 条规则，命中 %d 条", len(rules), firing)
}
