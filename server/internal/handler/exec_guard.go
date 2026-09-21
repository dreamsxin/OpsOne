package handler

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/gin-gonic/gin"

	"ops-platform/server/internal/middleware"
	"ops-platform/server/internal/model"
	"ops-platform/server/internal/response"
)

// 下发闸门：所有「把命令送到主机上」的路径都要过这里。
//
// 为什么放在 RunOnHosts 里而不是各个 handler 里：批量执行、脚本下发、定时任务
// （立即执行与调度触发）四条路径最终都收敛到 RunOnHosts。闸门写在这里，就不会
// 出现「新加一个下发入口忘了挂校验」的情况——这正是之前的真实状态：命令规则只
// 作用于交互式终端与脚本库，批量执行与定时任务是两条完全不过滤命令内容的直达
// SSH 通道；而生产环境二次确认更是只在三个 .vue 里弹窗，后端连 confirm 字段都
// 没有，直接调接口就绕过了。
//
// 闸门只做两件事，都是「拦不住就别声称能拦」的那种：
//  1. 命令内容按启用中的命令规则逐行匹配，命中 block 级规则一律拒绝下发；
//  2. 目标里有生产主机时，必须由调用方显式确认（confirmProd），否则拒绝。
//
// 能力边界与终端拦截一致（见 bastion/auditor.go）：静态正则挡得住写在明面上的
// 高危命令，挡不住 base64 解码后执行、变量拼接、下载脚本再跑这类绕过。
const (
	// execGuardHostNameMax 流水里存主机名的长度上限，几百台时别把单行撑爆
	execGuardHostNameMax = 500
	// execGuardCommandMax 流水里存命令原文的长度上限
	execGuardCommandMax = 4000
)

// execGuardDecision 一次下发前的判定结果
type execGuardDecision struct {
	// Status: pass 放行 | warn 放行但命中提醒规则 | blocked 拒绝下发
	Status    string        `json:"status"`
	Reason    string        `json:"reason"`
	Hits      []precheckHit `json:"hits"`
	ProdHosts []string      `json:"prodHosts"`
	// NeedConfirm 目标里有生产主机，调用方必须显式确认
	NeedConfirm bool `json:"needConfirm"`
}

// blockingHit 返回命中的第一条拦截级规则，用于流水里记「被哪条规则拦的」
func blockingHit(hits []precheckHit) *precheckHit {
	for i := range hits {
		if hits[i].Action == "block" {
			return &hits[i]
		}
	}
	return nil
}

// inspectExec 判定一次下发是否放行。confirmed 表示调用方已显式确认生产变更。
func (h *Handler) inspectExec(command string, hosts []model.Host, confirmed bool) execGuardDecision {
	status, hits := h.precheckScript(command)
	if status == "unknown" {
		// 规则读不出来时不假装校验过，按放行处理但不声称 pass
		log.Printf("[exec-guard] 命令规则加载失败，本次下发未做内容校验")
	}

	prod := make([]string, 0)
	for i := range hosts {
		if hosts[i].Env == "prod" {
			prod = append(prod, hosts[i].Name)
		}
	}

	decision := execGuardDecision{
		Status: status, Hits: hits, ProdHosts: prod, NeedConfirm: len(prod) > 0,
	}

	if status == "blocked" {
		hit := blockingHit(hits)
		detail := ""
		if hit != nil {
			detail = fmt.Sprintf("：第 %d 行命中「%s」", hit.Line, firstNonEmpty(hit.Description, hit.Pattern))
		}
		decision.Reason = fmt.Sprintf("命令命中拦截级命令规则，已阻止下发%s", detail)
		return decision
	}

	if len(prod) > 0 && !confirmed {
		decision.Status = "blocked"
		decision.Reason = fmt.Sprintf("目标里有 %d 台生产主机（%s），需要显式确认后才能下发",
			len(prod), truncate(strings.Join(prod, "、"), 120))
		return decision
	}

	return decision
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// recordExecGuard 把判定结果写进闸门流水。
//
// 只记 blocked 与 warn：pass 的下发本身就是一条执行记录（exec_jobs），
// 再记一遍是重复；而「被拦下来的下发」根本不会产生执行记录，不记就查不到。
func (h *Handler) recordExecGuard(req ExecRequest, hosts []model.Host, decision execGuardDecision) {
	if decision.Status != "blocked" && decision.Status != "warn" {
		return
	}

	names := make([]string, 0, len(hosts))
	for i := range hosts {
		names = append(names, hosts[i].Name)
	}

	entry := model.ExecGuardLog{
		Source: firstNonEmpty(req.Source, "manual"), Status: decision.Status,
		Reason: truncate(decision.Reason, 500), Command: truncate(req.Command, execGuardCommandMax),
		HostCount: len(hosts), ProdCount: len(decision.ProdHosts),
		HostNames: truncate(strings.Join(names, "、"), execGuardHostNameMax),
		CronJobID: req.CronJobID, UserID: req.UserID,
		Username: firstNonEmpty(req.Operator, "unknown"), ClientIP: req.ClientIP,
	}
	if hit := blockingHit(decision.Hits); hit != nil {
		entry.RuleID, entry.Pattern, entry.Action = hit.RuleID, hit.Pattern, hit.Action
	} else if len(decision.Hits) > 0 {
		entry.RuleID, entry.Pattern, entry.Action = decision.Hits[0].RuleID, decision.Hits[0].Pattern, decision.Hits[0].Action
	}

	if err := h.DB.Create(&entry).Error; err != nil {
		log.Printf("[exec-guard] 流水写入失败: %v", err)
	}
}

// ---------- 接口 ----------

type execPrecheckReq struct {
	Command string `json:"command" binding:"required"`
	HostIDs []uint `json:"hostIds"`
}

// PrecheckExec 下发前预检：这条命令会不会被拦、目标里有几台生产主机。
//
// 与真正下发走的是同一个判定函数，所以预检说放行就是真的放行（除非中间有人改了规则）。
func (h *Handler) PrecheckExec(c *gin.Context) {
	var req execPrecheckReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "命令不能为空")
		return
	}

	user := middleware.CurrentUser(c)
	var hosts []model.Host
	if len(req.HostIDs) > 0 {
		allowed := h.filterVisibleHostIDs(user, req.HostIDs)
		if len(allowed) > 0 {
			h.DB.Where("id IN ?", allowed).Find(&hosts)
		}
	}

	// 预检不代表已确认，confirmed 固定传 false，这样 needConfirm 能如实反映
	decision := h.inspectExec(req.Command, hosts, false)
	blocked := decision.Status == "blocked"
	// 只因为「没确认生产」而 blocked 的，语义上是「要确认」而不是「被规则拦」
	ruleBlocked := blockingHit(decision.Hits) != nil

	response.OK(c, gin.H{
		"status": decision.Status, "blocked": blocked, "ruleBlocked": ruleBlocked,
		"needConfirm": decision.NeedConfirm, "reason": decision.Reason,
		"hits": decision.Hits, "prodHosts": decision.ProdHosts,
		"hostCount": len(hosts),
	})
}

// ListExecGuardLogs 闸门流水：被拦下的下发尝试与命中提醒规则的下发
func (h *Handler) ListExecGuardLogs(c *gin.Context) {
	page, size := pageParams(c)
	q := h.DB.Model(&model.ExecGuardLog{})
	if status := strings.TrimSpace(c.Query("status")); status != "" {
		q = q.Where("status = ?", status)
	}
	if source := strings.TrimSpace(c.Query("source")); source != "" {
		q = q.Where("source = ?", source)
	}
	if kw := strings.TrimSpace(c.Query("keyword")); kw != "" {
		like := "%" + kw + "%"
		q = q.Where("command LIKE ? OR username LIKE ? OR host_names LIKE ?", like, like, like)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		response.Error(c, "查询拦截流水失败")
		return
	}
	var list []model.ExecGuardLog
	if err := q.Order("id desc").Offset((page - 1) * size).Limit(size).Find(&list).Error; err != nil {
		response.Error(c, "查询拦截流水失败")
		return
	}

	var blocked, warned int64
	h.DB.Model(&model.ExecGuardLog{}).Where("status = ?", "blocked").Count(&blocked)
	h.DB.Model(&model.ExecGuardLog{}).Where("status = ?", "warn").Count(&warned)

	response.OK(c, gin.H{
		"list": list, "total": total, "page": page, "pageSize": size,
		"summary": gin.H{"blocked": blocked, "warn": warned},
	})
}

// hitsJSON 把命中结果序列化后挂到执行记录上，方便事后回答「这次下发命中过什么」
func hitsJSON(hits []precheckHit) string {
	if len(hits) == 0 {
		return ""
	}
	raw, err := json.Marshal(hits)
	if err != nil {
		return ""
	}
	return string(raw)
}

// guardCronCommand 定时任务保存时的命令校验。
//
// 在保存时就拦，而不是等到凌晨三点触发才失败——那时没人看着。
func (h *Handler) guardCronCommand(command string) error {
	status, hits := h.precheckScript(command)
	if status != "blocked" {
		return nil
	}
	hit := blockingHit(hits)
	if hit == nil {
		return fmt.Errorf("命令命中拦截级命令规则，不允许作为定时任务保存")
	}
	return fmt.Errorf("命令第 %d 行命中拦截级命令规则「%s」，不允许作为定时任务保存",
		hit.Line, firstNonEmpty(hit.Description, hit.Pattern))
}

// prodHostNames 目标主机里属于生产环境的名字，用于保存定时任务时判断要不要确认
func (h *Handler) prodHostNames(hostIDs []uint) []string {
	names := make([]string, 0)
	if len(hostIDs) == 0 {
		return names
	}
	var hosts []model.Host
	h.DB.Where("id IN ? AND env = ?", hostIDs, "prod").Find(&hosts)
	for i := range hosts {
		names = append(names, hosts[i].Name)
	}
	return names
}
