package handler

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"ops-platform/server/internal/middleware"
	"ops-platform/server/internal/model"
	"ops-platform/server/internal/response"
)

// 特征库。
//
// 这个模块最容易做成「又一张永远不生效的模式表」：录一堆正则，页面上看着很专业，
// 但没有任何一条真的参与判定。所以定了一条硬规矩：**特征必须能落到一个真在跑的检测点上**。
// 目前只收两类：
//
//   - command：应用之后会在命令规则里生成 / 维护一条规则。命令规则是下发闸门
//     （批量执行 / 脚本 / 定时任务 / 防火墙下发）与 Web 终端真正在用的东西，
//     所以「应用了」等于「真会拦」。灰度（observe/enforce）映射成规则的 warn/block。
//   - port：拿真机扫描出来的开放端口（暴露面模块的 LastOpen）与特征端口清单求交集。
//     这里刻意**不改基线**：基线代表「我登记允许开放的端口」，是人的决定；
//     特征只负责告诉你「这些机器上开着你关心的端口」。
//
// 另一条规矩是对账：特征应用之后，命令规则那边可能被人手工改过或删掉。列表里给出
// unapplied / applied / drift / missing 四种状态，而不是显示一个永远「已应用」的绿标 ——
// 与防火墙模块「平台登记 vs 真机现读」是同一种思路。
const (
	sigKindCommand = "command"
	sigKindPort    = "port"

	sigStageObserve = "observe"
	sigStageEnforce = "enforce"

	// sigRulePrefix 由特征库生成的命令规则，描述统一带这个前缀，便于人一眼看出来源
	sigRulePrefix = "[特征库] "
)

// stageAction 灰度映射到命令规则的动作
func stageAction(stage string) string {
	if stage == sigStageEnforce {
		return "block"
	}
	return "warn"
}

type signatureReq struct {
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	Pattern     string `json:"pattern"`
	Description string `json:"description"`
	Stage       string `json:"stage"`
	Severity    string `json:"severity"`
	Enabled     *bool  `json:"enabled"`
}

func (r *signatureReq) normalize() error {
	r.Name = strings.TrimSpace(r.Name)
	r.Pattern = strings.TrimSpace(r.Pattern)
	r.Description = strings.TrimSpace(r.Description)
	if r.Name == "" {
		return fmt.Errorf("特征名称不能为空")
	}
	if r.Pattern == "" {
		return fmt.Errorf("特征内容不能为空")
	}
	switch r.Kind {
	case sigKindCommand:
		// 正则必须当场能编译：存一条编译不过的特征，等于在闸门里留一个永远不生效的洞
		if _, err := regexp.Compile(r.Pattern); err != nil {
			return fmt.Errorf("正则不合法: %v", err)
		}
	case sigKindPort:
		ports, err := parsePortSpec(r.Pattern)
		if err != nil {
			return fmt.Errorf("端口清单不合法: %v", err)
		}
		r.Pattern = formatPorts(ports)
	case "":
		return fmt.Errorf("请选择特征类型（command / port）")
	default:
		return fmt.Errorf("特征类型只支持 command 与 port")
	}
	switch r.Stage {
	case sigStageObserve, sigStageEnforce:
	case "":
		r.Stage = sigStageObserve
	default:
		return fmt.Errorf("灰度只能是 observe（只提醒）或 enforce（真拦）")
	}
	switch r.Severity {
	case "high", "medium", "low":
	case "":
		r.Severity = "medium"
	default:
		return fmt.Errorf("严重程度只能是 high / medium / low")
	}
	return nil
}

// signatureView 特征 + 与检测点的对账结果
type signatureView struct {
	model.Signature
	// ApplyStatus unapplied 未应用 | applied 已应用且一致 | drift 规则被手工改过 | missing 规则被删了
	ApplyStatus string `json:"applyStatus"`
	ApplyDetail string `json:"applyDetail"`
	// RuleAction / RuleEnabled 命令规则那边的实际状态
	RuleAction  string `json:"ruleAction"`
	RuleEnabled bool   `json:"ruleEnabled"`
}

// reconcile 对账一条特征：看它声称的状态与检测点里的实际状态是否一致
func (h *Handler) reconcile(sig model.Signature) signatureView {
	view := signatureView{Signature: sig, ApplyStatus: "unapplied"}
	if sig.Kind != sigKindCommand {
		view.ApplyStatus = "n/a"
		view.ApplyDetail = "端口特征不生成规则，用「按真机扫描结果核对」看命中情况"
		return view
	}
	if sig.RuleID == 0 {
		view.ApplyDetail = "还没应用到命令规则，当前不参与任何拦截"
		return view
	}
	var rule model.CommandRule
	if err := h.DB.First(&rule, sig.RuleID).Error; err != nil {
		view.ApplyStatus = "missing"
		view.ApplyDetail = fmt.Sprintf("对应的命令规则（id=%d）已不存在，特征当前不生效", sig.RuleID)
		return view
	}
	view.RuleAction, view.RuleEnabled = rule.Action, rule.Enabled
	diffs := make([]string, 0, 3)
	if rule.Pattern != sig.Pattern {
		diffs = append(diffs, "正则被改过")
	}
	if rule.Action != stageAction(sig.Stage) {
		diffs = append(diffs, fmt.Sprintf("动作是 %s，按当前灰度应为 %s", rule.Action, stageAction(sig.Stage)))
	}
	if rule.Enabled != sig.Enabled {
		diffs = append(diffs, fmt.Sprintf("规则 enabled=%v 与特征 enabled=%v 不一致", rule.Enabled, sig.Enabled))
	}
	if len(diffs) > 0 {
		view.ApplyStatus = "drift"
		view.ApplyDetail = "命令规则被改动过：" + strings.Join(diffs, "；") + "。点「应用」按特征重新写一遍"
		return view
	}
	view.ApplyStatus = "applied"
	view.ApplyDetail = fmt.Sprintf("已生效：命令规则 id=%d，动作 %s", rule.ID, rule.Action)
	return view
}

func (h *Handler) ListSignatures(c *gin.Context) {
	q := h.DB.Model(&model.Signature{})
	if kind := strings.TrimSpace(c.Query("kind")); kind != "" {
		q = q.Where("kind = ?", kind)
	}
	if stage := strings.TrimSpace(c.Query("stage")); stage != "" {
		q = q.Where("stage = ?", stage)
	}
	if keyword := strings.TrimSpace(c.Query("keyword")); keyword != "" {
		like := "%" + keyword + "%"
		q = q.Where("name LIKE ? OR pattern LIKE ? OR description LIKE ?", like, like, like)
	}
	var list []model.Signature
	if err := q.Order("kind asc, severity asc, id asc").Find(&list).Error; err != nil {
		response.Error(c, "查询特征库失败")
		return
	}

	views := make([]signatureView, 0, len(list))
	stats := map[string]int{"applied": 0, "drift": 0, "missing": 0, "unapplied": 0}
	for _, sig := range list {
		view := h.reconcile(sig)
		if _, ok := stats[view.ApplyStatus]; ok {
			stats[view.ApplyStatus]++
		}
		views = append(views, view)
	}
	response.OK(c, gin.H{
		"list": views, "stats": stats,
		"note": "command 类特征应用后会在「命令规则」里生成一条规则，下发闸门与 Web 终端都按它拦；" +
			"observe 只提醒、enforce 真拦。port 类特征不改暴露面基线，只拿真机扫描结果核对。",
	})
}

func (h *Handler) CreateSignature(c *gin.Context) {
	var req signatureReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "参数错误")
		return
	}
	if err := req.normalize(); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	sig := model.Signature{
		Name: req.Name, Kind: req.Kind, Pattern: req.Pattern,
		Description: req.Description, Stage: req.Stage, Severity: req.Severity,
		CreatedBy: middleware.CurrentUser(c).ID,
	}
	sig.Enabled = req.Enabled == nil || *req.Enabled
	if err := h.DB.Create(&sig).Error; err != nil {
		response.Error(c, "创建失败")
		return
	}
	if !sig.Enabled {
		h.DB.Model(&model.Signature{}).Where("id = ?", sig.ID).Update("enabled", false)
		sig.Enabled = false
	}
	response.OK(c, h.reconcile(sig))
}

func (h *Handler) UpdateSignature(c *gin.Context) {
	var sig model.Signature
	if err := h.DB.First(&sig, idParam(c)).Error; err != nil {
		response.NotFound(c, "特征不存在")
		return
	}
	var req signatureReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "参数错误")
		return
	}
	if req.Kind == "" {
		req.Kind = sig.Kind
	}
	if err := req.normalize(); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	if sig.Builtin && req.Kind != sig.Kind {
		response.BadRequest(c, "内置特征不允许改类型（可以改灰度、停用它，或另建一条自定义特征）")
		return
	}

	updates := map[string]any{
		"name": req.Name, "kind": req.Kind, "pattern": req.Pattern,
		"description": req.Description, "stage": req.Stage, "severity": req.Severity,
	}
	if req.Enabled != nil {
		updates["enabled"] = *req.Enabled
	}
	if err := h.DB.Model(&sig).Updates(updates).Error; err != nil {
		response.Error(c, "更新失败")
		return
	}
	h.DB.First(&sig, sig.ID)

	// 已经应用过的特征改了内容：顺手把规则重写一遍，否则「页面上改了、实际还按老的拦」
	if sig.Kind == sigKindCommand && sig.RuleID != 0 {
		if err := h.writeSignatureRule(&sig, middleware.CurrentUser(c).Username); err != nil {
			response.OK(c, gin.H{"signature": h.reconcile(sig),
				"warn": "特征已保存，但同步命令规则失败：" + err.Error()})
			return
		}
		h.DB.First(&sig, sig.ID)
	}
	response.OK(c, gin.H{"signature": h.reconcile(sig)})
}

// DeleteSignature 删除特征。内置的不允许删（删了下次启动又会回来，反而更乱）。
// 同时清掉它生成的命令规则 —— 留着一条没人认领的规则比没有更危险。
func (h *Handler) DeleteSignature(c *gin.Context) {
	var sig model.Signature
	if err := h.DB.First(&sig, idParam(c)).Error; err != nil {
		response.NotFound(c, "特征不存在")
		return
	}
	if sig.Builtin {
		response.BadRequest(c, "内置特征不允许删除，可以停用（enabled=false）")
		return
	}
	if sig.RuleID != 0 {
		if err := h.DB.Delete(&model.CommandRule{}, sig.RuleID).Error; err != nil {
			response.Error(c, "删除特征前清理命令规则失败："+err.Error())
			return
		}
	}
	if err := h.DB.Delete(&model.Signature{}, sig.ID).Error; err != nil {
		response.Error(c, "删除失败")
		return
	}
	response.OK(c, gin.H{"id": sig.ID, "ruleRemoved": sig.RuleID != 0})
}

// writeSignatureRule 把特征写成 / 更新成一条命令规则
func (h *Handler) writeSignatureRule(sig *model.Signature, operator string) error {
	now := time.Now()
	desc := sigRulePrefix + sig.Name
	if sig.Description != "" {
		desc += "：" + sig.Description
	}
	desc = truncate(desc, 240)

	if sig.RuleID != 0 {
		var rule model.CommandRule
		if err := h.DB.First(&rule, sig.RuleID).Error; err == nil {
			err = h.DB.Model(&rule).Updates(map[string]any{
				"pattern": sig.Pattern, "description": desc,
				"action": stageAction(sig.Stage), "enabled": sig.Enabled,
			}).Error
			if err != nil {
				return err
			}
			return h.DB.Model(sig).Updates(map[string]any{
				"applied_at": &now, "applied_by": operator,
			}).Error
		}
		// 规则被人删了：下面重新建一条
	}

	rule := model.CommandRule{
		Pattern: sig.Pattern, Description: desc,
		Action: stageAction(sig.Stage), Enabled: sig.Enabled,
	}
	if err := h.DB.Create(&rule).Error; err != nil {
		return err
	}
	// enabled 带 gorm default:true，显式关掉的要写回去
	if !sig.Enabled {
		h.DB.Model(&model.CommandRule{}).Where("id = ?", rule.ID).Update("enabled", false)
	}
	return h.DB.Model(sig).Updates(map[string]any{
		"rule_id": rule.ID, "applied_at": &now, "applied_by": operator,
	}).Error
}

// ApplySignature 把 command 类特征应用到命令规则（真正开始参与拦截）
func (h *Handler) ApplySignature(c *gin.Context) {
	var sig model.Signature
	if err := h.DB.First(&sig, idParam(c)).Error; err != nil {
		response.NotFound(c, "特征不存在")
		return
	}
	if sig.Kind != sigKindCommand {
		response.BadRequest(c, "只有 command 类特征需要应用；端口特征用「按真机扫描结果核对」")
		return
	}
	if err := h.writeSignatureRule(&sig, middleware.CurrentUser(c).Username); err != nil {
		response.Error(c, "应用失败："+err.Error())
		return
	}
	h.DB.First(&sig, sig.ID)
	view := h.reconcile(sig)
	response.OK(c, gin.H{
		"signature": view,
		"detail": fmt.Sprintf("已写入命令规则（%s）。%s", stageAction(sig.Stage),
			map[string]string{
				"warn":  "当前是观察灰度：命中只提醒、仍会下发",
				"block": "当前是生效灰度：命中会直接拦下下发，并写进闸门流水",
			}[stageAction(sig.Stage)]),
	})
}

// RevokeSignature 撤下特征：删掉它生成的命令规则，特征本身保留
func (h *Handler) RevokeSignature(c *gin.Context) {
	var sig model.Signature
	if err := h.DB.First(&sig, idParam(c)).Error; err != nil {
		response.NotFound(c, "特征不存在")
		return
	}
	if sig.RuleID == 0 {
		response.BadRequest(c, "这条特征本来就没有应用")
		return
	}
	if err := h.DB.Delete(&model.CommandRule{}, sig.RuleID).Error; err != nil {
		response.Error(c, "删除命令规则失败："+err.Error())
		return
	}
	if err := h.DB.Model(&sig).Updates(map[string]any{
		"rule_id": 0, "applied_at": nil, "applied_by": "",
	}).Error; err != nil {
		response.Error(c, "更新特征状态失败")
		return
	}
	h.DB.First(&sig, sig.ID)
	response.OK(c, gin.H{"signature": h.reconcile(sig), "detail": "已撤下，这条特征不再参与拦截"})
}

// CheckPortSignature 拿真机扫描结果核对端口特征。
//
// 数据来源是暴露面模块上一次扫描回填的 LastOpen（真机 TCP connect 的结果），
// 所以这里给的是「事实」，不是「登记值」。没扫过的目标如实标出来，不当成通过。
func (h *Handler) CheckPortSignature(c *gin.Context) {
	var sig model.Signature
	if err := h.DB.First(&sig, idParam(c)).Error; err != nil {
		response.NotFound(c, "特征不存在")
		return
	}
	if sig.Kind != sigKindPort {
		response.BadRequest(c, "只有 port 类特征支持按扫描结果核对")
		return
	}
	want, err := parsePortSpec(sig.Pattern)
	if err != nil {
		response.BadRequest(c, "特征里的端口清单不合法："+err.Error())
		return
	}
	wantSet := map[int]bool{}
	for _, p := range want {
		wantSet[p] = true
	}

	var targets []model.ExposureTarget
	if err := h.DB.Order("id asc").Find(&targets).Error; err != nil {
		response.Error(c, "读取暴露面目标失败")
		return
	}

	type hitRow struct {
		TargetID   uint   `json:"targetId"`
		Name       string `json:"name"`
		Address    string `json:"address"`
		HitPorts   string `json:"hitPorts"`
		LastScanAt string `json:"lastScanAt"`
		Note       string `json:"note"`
	}
	hits := make([]hitRow, 0)
	neverScanned := make([]string, 0)
	for _, t := range targets {
		if t.LastScanAt == nil || strings.TrimSpace(t.LastOpen) == "" {
			if t.LastScanAt == nil {
				neverScanned = append(neverScanned, t.Name)
			}
			continue
		}
		open, err := parsePortSpec(t.LastOpen)
		if err != nil {
			continue
		}
		matched := make([]int, 0)
		for _, p := range open {
			if wantSet[p] {
				matched = append(matched, p)
			}
		}
		if len(matched) == 0 {
			continue
		}
		sort.Ints(matched)
		note := "这些端口真的开着"
		if baseline, err := parsePortSpec(t.Baseline); err == nil && len(baseline) > 0 {
			allowed := map[int]bool{}
			for _, p := range baseline {
				allowed[p] = true
			}
			outside := make([]int, 0)
			for _, p := range matched {
				if !allowed[p] {
					outside = append(outside, p)
				}
			}
			if len(outside) > 0 {
				note = fmt.Sprintf("其中 %s 不在该目标的基线里", formatPorts(outside))
			} else {
				note = "都在基线允许范围内（登记过的开放端口）"
			}
		}
		hits = append(hits, hitRow{
			TargetID: t.ID, Name: t.Name, Address: t.Address,
			HitPorts: formatPorts(matched), Note: note,
			LastScanAt: t.LastScanAt.Format(time.RFC3339),
		})
	}

	detail := fmt.Sprintf("在 %d 个暴露面目标里有 %d 个命中这条特征", len(targets), len(hits))
	if len(neverScanned) > 0 {
		detail += fmt.Sprintf("；另有 %d 个目标从未扫过（%s），它们不算通过、只是没有数据",
			len(neverScanned), truncate(strings.Join(neverScanned, "、"), 120))
	}
	response.OK(c, gin.H{
		"ports": formatPorts(want), "hits": hits,
		"targetCount": len(targets), "neverScanned": neverScanned,
		"detail": detail,
	})
}

// ReconcileSignatures 全量对账：一次性看清「声称已应用」与「实际在跑」差在哪
func (h *Handler) ReconcileSignatures(c *gin.Context) {
	var list []model.Signature
	if err := h.DB.Where("kind = ?", sigKindCommand).Order("id asc").Find(&list).Error; err != nil {
		response.Error(c, "查询特征库失败")
		return
	}
	drift := make([]gin.H, 0)
	for _, sig := range list {
		view := h.reconcile(sig)
		if view.ApplyStatus == "drift" || view.ApplyStatus == "missing" {
			drift = append(drift, gin.H{
				"id": sig.ID, "name": sig.Name,
				"status": view.ApplyStatus, "detail": view.ApplyDetail,
			})
		}
	}
	note := "特征库与命令规则一致"
	if len(drift) > 0 {
		note = fmt.Sprintf("有 %d 条特征与命令规则不一致：命令规则被手工改过或删过，"+
			"点各行的「应用」按特征重写一遍即可", len(drift))
	}
	response.OK(c, gin.H{"total": len(list), "drift": drift, "note": note})
}
