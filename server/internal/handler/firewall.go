package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"ops-platform/server/internal/fwx"
	"ops-platform/server/internal/middleware"
	"ops-platform/server/internal/model"
	"ops-platform/server/internal/response"
	"ops-platform/server/internal/sshx"
)

// 防火墙策略模块。
//
// 这里刻意不做「平台数据库就是真相」那一套：真机规则每次都现读（internal/fwx），
// 平台只存「我们期望有哪些规则 + 谁负责 + 什么时候该收」，两边对账出差异。
// 理由是防火墙规则谁都能上机器手改，平台镜像一定会过期，
// 只落库不对账的功能等于骗人。
//
// 能力边界（页面上也如实写着）：
//   - 能做：读规则（含 iptables 的真实命中计数）、对账、预检、下发、快照回滚、安全组铺规则、清理建议
//   - 不做：实时流量监控 / 进程连接 / 带宽限速 / Agent 接入审批 —— 这些要装 agent，
//     纯 SSH 只能定时采样，做出来也是假的「实时」
//
// 所有下发都过 RunOnHosts 这一个口子，因此命令规则拦截、生产二次确认、
// 执行留痕、闸门流水全都自动继承，不另起一套。

const (
	// CfgFirewallSudo 非 root 账号下是否自动加 sudo -n。默认开：防火墙命令全都要 root
	CfgFirewallSudo = "firewall.sudo"
	// CfgFirewallIdleDays 连续多少天零命中就进清理建议
	CfgFirewallIdleDays = "firewall.idle_days"

	firewallReadTimeout = 25 * time.Second
)

// firewallStateResp 一次现读的结果，规则里带上了与平台登记的对账状态
type firewallStateResp struct {
	HostID        uint              `json:"hostId"`
	HostName      string            `json:"hostName"`
	Backend       string            `json:"backend"`
	Active        bool              `json:"active"`
	Persistent    bool              `json:"persistent"`
	Note          string            `json:"note"`
	DefaultPolicy map[string]string `json:"defaultPolicy"`
	ReadOK        bool              `json:"readOk"`
	ReadError     string            `json:"readError"`
	CostMs        int64             `json:"costMs"`
	Rules         []firewallMerged  `json:"rules"`
	Summary       map[string]int    `json:"summary"`
}

// firewallMerged 真机规则与平台登记合并后的一行
type firewallMerged struct {
	RuleID    uint   `json:"ruleId"` // 0 表示平台没登记
	Direction string `json:"direction"`
	Action    string `json:"action"`
	Protocol  string `json:"protocol"`
	Source    string `json:"source"`
	Port      string `json:"port"`
	Service   string `json:"service"`
	RuleKey   string `json:"ruleKey"`

	Description string     `json:"description"`
	Owner       string     `json:"owner"`
	Lifecycle   string     `json:"lifecycle"`
	ExpiresAt   *time.Time `json:"expiresAt"`
	Origin      string     `json:"origin"`
	Enabled     bool       `json:"enabled"`

	// State synced 两边都有 | drift 平台有真机没有 | extra 真机有平台没登记 | pending 还没下发过
	State      string `json:"state"`
	OnHost     bool   `json:"onHost"`
	Hits       int64  `json:"hits"`
	HasCounter bool   `json:"hasCounter"`
	Raw        string `json:"raw"`
	Index      int    `json:"index"`
}

func (h *Handler) firewallSudo() bool {
	return h.configString(CfgFirewallSudo, "true") != "false"
}

// readFirewall 现读一台主机的防火墙状态
func (h *Handler) readFirewall(ctx context.Context, host *model.Host) (fwx.State, sshx.Result) {
	useSudo := h.firewallSudo() && host.Username != "root"
	runCtx, cancel := context.WithTimeout(ctx, firewallReadTimeout)
	defer cancel()
	res := sshx.Run(runCtx, h.target(host), fwx.ReadScript(useSudo))
	if res.Status != "success" {
		// 读不到就是读不到，绝不返回空规则冒充「这台机器没有规则」
		return fwx.State{Backend: fwx.BackendNone, DefaultPolicy: map[string]string{}}, res
	}
	return fwx.Parse(res.Stdout), res
}

// loadHostForFirewall 取主机并做数据权限校验
func (h *Handler) loadHostForFirewall(c *gin.Context, hostID uint) (*model.Host, bool) {
	if hostID == 0 {
		response.BadRequest(c, "请先选择主机")
		return nil, false
	}
	var host model.Host
	if err := h.DB.First(&host, hostID).Error; err != nil {
		response.NotFound(c, "主机不存在")
		return nil, false
	}
	user := middleware.CurrentUser(c)
	if len(h.filterVisibleHostIDs(user, []uint{hostID})) == 0 {
		response.Forbidden(c, "该主机不在你的数据权限范围内")
		return nil, false
	}
	return &host, true
}

// GetFirewallState 现读真机规则并与平台登记对账
func (h *Handler) GetFirewallState(c *gin.Context) {
	hostID := uintQuery(c, "hostId")
	host, ok := h.loadHostForFirewall(c, hostID)
	if !ok {
		return
	}

	state, res := h.readFirewall(c.Request.Context(), host)
	resp := firewallStateResp{
		HostID: host.ID, HostName: host.Name,
		Backend: state.Backend, Active: state.Active, Persistent: state.Persistent,
		Note: state.Note, DefaultPolicy: state.DefaultPolicy,
		ReadOK: res.Status == "success", CostMs: res.CostMs,
		Summary: map[string]int{},
	}
	if res.Status != "success" {
		detail := strings.TrimSpace(res.Stderr)
		if detail == "" {
			detail = "读取命令返回 " + res.Status
		}
		resp.ReadError = truncate(detail, 300)
		if strings.Contains(detail, "sudo") || strings.Contains(detail, "not permitted") {
			resp.ReadError += "（防火墙命令需要 root：请把主机账号换成 root，或为该账号配置免密 sudo）"
		}
		response.OK(c, resp)
		return
	}

	var rules []model.FirewallRule
	h.DB.Where("host_id = ?", host.ID).Find(&rules)
	resp.Rules = mergeFirewall(rules, state.Rules)
	h.persistFirewallCheck(host.ID, rules, state.Rules)
	for _, r := range resp.Rules {
		resp.Summary[r.State]++
	}
	response.OK(c, resp)
}

// mergeFirewall 平台登记 ∪ 真机实际，按指纹配对
func mergeFirewall(rules []model.FirewallRule, actual []fwx.Rule) []firewallMerged {
	live := map[string]fwx.Rule{}
	for _, a := range actual {
		live[a.Key()] = a
	}
	out := make([]firewallMerged, 0, len(rules)+len(actual))
	seen := map[string]bool{}

	for _, r := range rules {
		row := firewallMerged{
			RuleID: r.ID, Direction: r.Direction, Action: r.Action, Protocol: r.Protocol,
			Source: r.Source, Port: r.Port, Service: r.Service, RuleKey: r.RuleKey,
			Description: r.Description, Owner: r.Owner, Lifecycle: r.Lifecycle,
			ExpiresAt: r.ExpiresAt, Origin: r.Origin, Enabled: r.Enabled,
		}
		if a, ok := live[r.RuleKey]; ok {
			seen[r.RuleKey] = true
			row.OnHost = true
			row.Hits, row.HasCounter, row.Raw, row.Index = a.Hits, a.HasCounter, a.Raw, a.Index
			row.State = "synced"
			if !r.Enabled {
				// 平台已停用但真机还在，下一次下发要把它删掉
				row.State = "drift"
			}
		} else if r.Enabled {
			row.State = "drift"
		} else {
			row.State = "synced"
		}
		out = append(out, row)
	}

	for _, a := range actual {
		if seen[a.Key()] {
			continue
		}
		out = append(out, firewallMerged{
			Direction: a.Direction, Action: a.Action, Protocol: a.Protocol,
			Source: a.Source, Port: a.Port, Service: a.Service, RuleKey: a.Key(),
			Origin: "discovered", Enabled: true, State: "extra", OnHost: true,
			Hits: a.Hits, HasCounter: a.HasCounter, Raw: a.Raw, Index: a.Index,
		})
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Direction != out[j].Direction {
			return out[i].Direction < out[j].Direction
		}
		return out[i].State < out[j].State
	})
	return out
}

// persistFirewallCheck 把这次读到的命中数与状态回写，供清理建议与列表页用
func (h *Handler) persistFirewallCheck(hostID uint, rules []model.FirewallRule, actual []fwx.Rule) {
	live := map[string]fwx.Rule{}
	for _, a := range actual {
		live[a.Key()] = a
	}
	now := time.Now()
	for _, r := range rules {
		updates := map[string]any{"last_check_at": now}
		a, onHost := live[r.RuleKey]
		switch {
		case onHost && r.Enabled:
			updates["state"] = "synced"
		case onHost && !r.Enabled:
			updates["state"] = "drift"
		case !onHost && r.Enabled:
			updates["state"] = "drift"
		default:
			updates["state"] = "synced"
		}
		if onHost {
			updates["has_counter"] = a.HasCounter
			if a.HasCounter {
				if a.Hits > r.Hits {
					updates["last_hit_at"] = now
				}
				updates["hits"] = a.Hits
			}
		}
		h.DB.Model(&model.FirewallRule{}).Where("id = ?", r.ID).Updates(updates)
	}
	_ = hostID
}

// ---------- 平台侧规则 CRUD ----------

type firewallRuleReq struct {
	HostID      uint   `json:"hostId"`
	GroupID     uint   `json:"groupId"`
	Direction   string `json:"direction"`
	Action      string `json:"action"`
	Protocol    string `json:"protocol"`
	Source      string `json:"source"`
	Port        string `json:"port"`
	Service     string `json:"service"`
	Description string `json:"description"`
	Owner       string `json:"owner"`
	Lifecycle   string `json:"lifecycle"`
	ExpiresAt   string `json:"expiresAt"`
	Enabled     *bool  `json:"enabled"`
}

func (r *firewallRuleReq) normalize() (fwx.Spec, *time.Time, error) {
	r.Direction = strings.ToLower(strings.TrimSpace(r.Direction))
	r.Action = strings.ToLower(strings.TrimSpace(r.Action))
	r.Protocol = strings.ToLower(strings.TrimSpace(r.Protocol))
	r.Source = strings.TrimSpace(r.Source)
	r.Port = strings.TrimSpace(r.Port)
	r.Service = strings.TrimSpace(r.Service)
	r.Lifecycle = strings.ToLower(strings.TrimSpace(r.Lifecycle))
	if r.Direction == "" {
		r.Direction = fwx.DirIn
	}
	if r.Action == "" {
		r.Action = fwx.ActionAccept
	}
	if r.Protocol == "" {
		r.Protocol = "tcp"
	}
	if r.Source == "" {
		r.Source = "any"
	}
	if r.Lifecycle == "" {
		r.Lifecycle = "permanent"
	}
	if r.Lifecycle != "permanent" && r.Lifecycle != "temporary" {
		return fwx.Spec{}, nil, fmt.Errorf("生命周期只能是 permanent 或 temporary")
	}

	var expires *time.Time
	if raw := strings.TrimSpace(r.ExpiresAt); raw != "" {
		t, ok := parseLocalTime(raw)
		if !ok {
			return fwx.Spec{}, nil, fmt.Errorf("到期时间格式应为 YYYY-MM-DD HH:mm:ss")
		}
		expires = &t
	}
	// 临时规则必须有到期时间，否则「临时」只是个标签，最后一样变成没人敢删的长期规则
	if r.Lifecycle == "temporary" && expires == nil {
		return fwx.Spec{}, nil, fmt.Errorf("临时规则必须填到期时间")
	}

	spec := fwx.Spec{
		Direction: r.Direction, Action: r.Action, Protocol: r.Protocol,
		Source: r.Source, Port: r.Port, Service: r.Service,
	}
	// 用生成命令的那套校验做入参校验：能不能落库，取决于能不能安全地变成命令
	if _, err := fwx.AddCommand(fwx.BackendIptables, spec); err != nil {
		return fwx.Spec{}, nil, err
	}
	return spec, expires, nil
}

func specKey(s fwx.Spec) string {
	return fwx.Rule{
		Direction: s.Direction, Action: s.Action, Protocol: s.Protocol,
		Source: s.Source, Port: s.Port, Service: s.Service,
	}.Key()
}

// ListFirewallRules 平台登记的规则，按主机或安全组筛
func (h *Handler) ListFirewallRules(c *gin.Context) {
	page, size := pageParams(c)
	q := h.DB.Model(&model.FirewallRule{})
	hostID := uintQuery(c, "hostId")
	groupID := uintQuery(c, "groupId")
	if hostID > 0 {
		q = q.Where("host_id = ?", hostID)
	}
	if groupID > 0 {
		q = q.Where("group_id = ?", groupID)
		// 只按组查时要的是组自己的模板规则；铺出去的副本也带 group_id，
		// 不排掉的话安全组页面会把副本当自己的规则重复列出来
		if hostID == 0 {
			q = q.Where("host_id = 0")
		}
	}
	if dir := c.Query("direction"); dir != "" {
		q = q.Where("direction = ?", dir)
	}
	if state := c.Query("state"); state != "" {
		q = q.Where("state = ?", state)
	}
	if kw := strings.TrimSpace(c.Query("keyword")); kw != "" {
		like := "%" + kw + "%"
		q = q.Where("description LIKE ? OR owner LIKE ? OR source LIKE ? OR port LIKE ? OR service LIKE ?",
			like, like, like, like, like)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		response.Error(c, "查询防火墙规则失败")
		return
	}
	var list []model.FirewallRule
	if err := q.Order("id desc").Offset((page - 1) * size).Limit(size).Find(&list).Error; err != nil {
		response.Error(c, "查询防火墙规则失败")
		return
	}
	response.OKPage(c, list, total, page, size)
}

// CreateFirewallRule 登记一条规则（只落库，生效要走下发）
func (h *Handler) CreateFirewallRule(c *gin.Context) {
	var req firewallRuleReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "请求参数有误")
		return
	}
	spec, expires, err := req.normalize()
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	if req.GroupID == 0 {
		if _, ok := h.loadHostForFirewall(c, req.HostID); !ok {
			return
		}
	}
	user := middleware.CurrentUser(c)
	owner := req.Owner
	if owner == "" {
		owner = user.Username
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	rule := model.FirewallRule{
		HostID: req.HostID, GroupID: req.GroupID,
		Direction: spec.Direction, Action: spec.Action, Protocol: spec.Protocol,
		Source: spec.Source, Port: spec.Port, Service: spec.Service,
		RuleKey: specKey(spec), Description: req.Description, Owner: owner,
		Lifecycle: req.Lifecycle, ExpiresAt: expires,
		Origin: "platform", State: "pending", Enabled: enabled,
		CreatedBy: user.Username,
	}
	if err := h.DB.Create(&rule).Error; err != nil {
		response.Error(c, "规则登记失败")
		return
	}
	// default:true 的字段在 Create 时会被 GORM 忽略掉 false，回写一次
	if !enabled {
		h.DB.Model(&rule).Update("enabled", false)
		rule.Enabled = false
	}
	response.OK(c, rule)
}

// UpdateFirewallRule 改规则。改动只落库，生效仍要下发
func (h *Handler) UpdateFirewallRule(c *gin.Context) {
	var rule model.FirewallRule
	if err := h.DB.First(&rule, idParam(c)).Error; err != nil {
		response.NotFound(c, "规则不存在")
		return
	}
	var req firewallRuleReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "请求参数有误")
		return
	}
	spec, expires, err := req.normalize()
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	if rule.HostID > 0 {
		if _, ok := h.loadHostForFirewall(c, rule.HostID); !ok {
			return
		}
	}
	enabled := rule.Enabled
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	updates := map[string]any{
		"direction": spec.Direction, "action": spec.Action, "protocol": spec.Protocol,
		"source": spec.Source, "port": spec.Port, "service": spec.Service,
		"rule_key": specKey(spec), "description": req.Description, "owner": req.Owner,
		"lifecycle": req.Lifecycle, "expires_at": expires, "enabled": enabled,
	}
	// 被接管的存量规则补齐信息后就算平台的了
	if rule.Origin == "discovered" && req.Owner != "" {
		updates["origin"] = "platform"
	}
	if err := h.DB.Model(&rule).Updates(updates).Error; err != nil {
		response.Error(c, "规则更新失败")
		return
	}
	h.DB.First(&rule, rule.ID)
	response.OK(c, rule)
}

// DeleteFirewallRule 删除登记。真机上那条不会跟着消失，需要再下发一次
func (h *Handler) DeleteFirewallRule(c *gin.Context) {
	var rule model.FirewallRule
	if err := h.DB.First(&rule, idParam(c)).Error; err != nil {
		response.NotFound(c, "规则不存在")
		return
	}
	if rule.HostID > 0 {
		if _, ok := h.loadHostForFirewall(c, rule.HostID); !ok {
			return
		}
	}
	if err := h.DB.Delete(&rule).Error; err != nil {
		response.Error(c, "规则删除失败")
		return
	}
	response.OK(c, gin.H{
		"deleted": rule.ID,
		"note":    "只删了平台登记。真机上那条规则还在，需要先把它停用并下发一次，或到主机上手动清理",
	})
}

// AdoptFirewallRule 接管真机上读到的存量规则：登记进平台并补责任人
func (h *Handler) AdoptFirewallRule(c *gin.Context) {
	var req firewallRuleReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "请求参数有误")
		return
	}
	spec, expires, err := req.normalize()
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	if _, ok := h.loadHostForFirewall(c, req.HostID); !ok {
		return
	}
	key := specKey(spec)
	var exist model.FirewallRule
	if err := h.DB.Where("host_id = ? AND rule_key = ?", req.HostID, key).First(&exist).Error; err == nil {
		response.BadRequest(c, "这条规则已经登记过了")
		return
	}
	user := middleware.CurrentUser(c)
	owner := req.Owner
	if owner == "" {
		owner = user.Username
	}
	rule := model.FirewallRule{
		HostID:    req.HostID,
		Direction: spec.Direction, Action: spec.Action, Protocol: spec.Protocol,
		Source: spec.Source, Port: spec.Port, Service: spec.Service,
		RuleKey: key, Description: req.Description, Owner: owner,
		Lifecycle: req.Lifecycle, ExpiresAt: expires,
		// 真机上已经有了，所以直接是 synced，不需要下发
		Origin: "discovered", State: "synced", Enabled: true,
		CreatedBy: user.Username,
	}
	if err := h.DB.Create(&rule).Error; err != nil {
		response.Error(c, "接管失败")
		return
	}
	response.OK(c, rule)
}

// ---------- 预检与下发 ----------

type firewallApplyReq struct {
	HostID      uint   `json:"hostId"`
	ConfirmProd bool   `json:"confirmProd"`
	Note        string `json:"note"`
}

// firewallPlan 一次下发要做什么
type firewallPlan struct {
	Backend    string   `json:"backend"`
	Persistent bool     `json:"persistent"`
	Adds       []string `json:"adds"`
	Removes    []string `json:"removes"`
	Commands   []string `json:"commands"`
	Skipped    []string `json:"skipped"`
}

// buildFirewallPlan 对账出「要加哪些、要删哪些」。
//
// 真机上有、平台没登记的规则一律不动 —— 擅自删真机规则太危险，
// 这类只在清理建议里提示，由人决定。
func (h *Handler) buildFirewallPlan(host *model.Host, state fwx.State, rules []model.FirewallRule) firewallPlan {
	plan := firewallPlan{Backend: state.Backend, Persistent: state.Persistent}
	live := map[string]bool{}
	for _, a := range state.Rules {
		live[a.Key()] = true
	}
	useSudo := h.firewallSudo() && host.Username != "root"

	for _, r := range rules {
		spec := fwx.Spec{
			Direction: r.Direction, Action: r.Action, Protocol: r.Protocol,
			Source: r.Source, Port: r.Port, Service: r.Service,
		}
		desc := fmt.Sprintf("%s %s %s %s→%s", r.Direction, r.Action, r.Protocol, r.Source, ruleTarget(r))
		switch {
		case r.Enabled && !live[r.RuleKey]:
			cmd, err := fwx.AddCommand(state.Backend, spec)
			if err != nil {
				plan.Skipped = append(plan.Skipped, desc+"："+err.Error())
				continue
			}
			plan.Adds = append(plan.Adds, desc)
			plan.Commands = append(plan.Commands, fwx.SudoWrap(cmd, host.Username, useSudo))
		case !r.Enabled && live[r.RuleKey]:
			cmd, err := fwx.DeleteCommand(state.Backend, spec)
			if err != nil {
				plan.Skipped = append(plan.Skipped, desc+"："+err.Error())
				continue
			}
			plan.Removes = append(plan.Removes, desc)
			plan.Commands = append(plan.Commands, fwx.SudoWrap(cmd, host.Username, useSudo))
		}
	}
	return plan
}

func ruleTarget(r model.FirewallRule) string {
	if r.Service != "" {
		return r.Service
	}
	if r.Port != "" {
		return r.Port
	}
	return "any"
}

// PrecheckFirewall 只算不发：给出将要执行的命令，并过一遍命令规则与生产确认闸门
func (h *Handler) PrecheckFirewall(c *gin.Context) {
	var req firewallApplyReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "请求参数有误")
		return
	}
	host, ok := h.loadHostForFirewall(c, req.HostID)
	if !ok {
		return
	}
	state, res := h.readFirewall(c.Request.Context(), host)
	if res.Status != "success" {
		response.BadRequest(c, "读不到真机规则，无法预检："+truncate(strings.TrimSpace(res.Stderr), 200))
		return
	}
	var rules []model.FirewallRule
	h.DB.Where("host_id = ?", host.ID).Find(&rules)
	plan := h.buildFirewallPlan(host, state, rules)

	out := gin.H{"plan": plan, "hostName": host.Name}
	if len(plan.Commands) == 0 {
		out["verdict"] = "nothing"
		out["reason"] = "平台登记与真机一致，没有要下发的改动"
		response.OK(c, out)
		return
	}
	// 与真正下发同一套闸门判定，预检结果才有意义
	decision := h.inspectExec(strings.Join(plan.Commands, "\n"), []model.Host{*host}, req.ConfirmProd)
	out["verdict"] = decision.Status
	out["reason"] = decision.Reason
	out["hits"] = decision.Hits
	out["prodHosts"] = decision.ProdHosts
	if !state.Persistent {
		out["warning"] = state.Note
	}
	response.OK(c, out)
}

// ApplyFirewall 下发。下发前后各存一份真机快照，可回滚
func (h *Handler) ApplyFirewall(c *gin.Context) {
	var req firewallApplyReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "请求参数有误")
		return
	}
	host, ok := h.loadHostForFirewall(c, req.HostID)
	if !ok {
		return
	}
	user := middleware.CurrentUser(c)

	before, res := h.readFirewall(c.Request.Context(), host)
	if res.Status != "success" {
		response.BadRequest(c, "读不到真机规则，不下发："+truncate(strings.TrimSpace(res.Stderr), 200))
		return
	}
	var rules []model.FirewallRule
	h.DB.Where("host_id = ?", host.ID).Find(&rules)
	plan := h.buildFirewallPlan(host, before, rules)
	if len(plan.Commands) == 0 {
		response.OK(c, gin.H{"applied": false, "reason": "平台登记与真机一致，没有要下发的改动", "plan": plan})
		return
	}

	snapBefore := h.saveFirewallSnapshot(host, before, res.Stdout, "before-apply", user.Username, 0)

	job, err := h.RunOnHosts(c.Request.Context(), ExecRequest{
		Name:        "防火墙下发: " + host.Name,
		Command:     strings.Join(plan.Commands, "\n"),
		Timeout:     120,
		HostIDs:     []uint{host.ID},
		UserID:      user.ID,
		Operator:    user.Username,
		Source:      "manual",
		ConfirmProd: req.ConfirmProd,
		ClientIP:    c.ClientIP(),
	})
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	if snapBefore != nil {
		h.DB.Model(snapBefore).Update("exec_job_id", job.ID)
	}

	// 回读一次：下发成功不等于规则生效，必须现场核对
	after, afterRes := h.readFirewall(c.Request.Context(), host)
	out := gin.H{"applied": true, "job": job, "plan": plan, "snapshotId": snapshotID(snapBefore)}
	if afterRes.Status == "success" {
		h.saveFirewallSnapshot(host, after, afterRes.Stdout, "after-apply", user.Username, job.ID)
		h.DB.Where("host_id = ?", host.ID).Find(&rules)
		h.persistFirewallCheck(host.ID, rules, after.Rules)
		remain := h.buildFirewallPlan(host, after, rules)
		out["verified"] = len(remain.Commands) == 0
		out["remaining"] = remain
		if len(remain.Commands) > 0 {
			out["note"] = "下发已执行，但回读后仍有差异，请看执行输出里的报错"
		}
	} else {
		out["verified"] = false
		out["note"] = "下发已执行，但回读失败，无法确认是否生效：" + truncate(strings.TrimSpace(afterRes.Stderr), 200)
	}
	response.OK(c, out)
}

func snapshotID(s *model.FirewallSnapshot) uint {
	if s == nil {
		return 0
	}
	return s.ID
}

func (h *Handler) saveFirewallSnapshot(host *model.Host, state fwx.State, raw, reason, operator string, jobID uint) *model.FirewallSnapshot {
	rulesJSON, _ := json.Marshal(state.Rules)
	snap := model.FirewallSnapshot{
		HostID: host.ID, Backend: state.Backend, Reason: reason,
		RuleCount: len(state.Rules), Raw: raw, Rules: string(rulesJSON),
		ExecJobID: jobID, Operator: operator,
	}
	if err := h.DB.Create(&snap).Error; err != nil {
		return nil
	}
	return &snap
}

// ListFirewallSnapshots 时间线：这台机器的规则什么时候被谁改过
func (h *Handler) ListFirewallSnapshots(c *gin.Context) {
	page, size := pageParams(c)
	q := h.DB.Model(&model.FirewallSnapshot{})
	if hostID := uintQuery(c, "hostId"); hostID > 0 {
		q = q.Where("host_id = ?", hostID)
	}
	var total int64
	q.Count(&total)
	var list []model.FirewallSnapshot
	// 列表不带 Raw：一份 dump 好几 KB，时间线只需要摘要
	if err := q.Select("id", "host_id", "backend", "reason", "rule_count", "exec_job_id", "operator", "created_at").
		Order("id desc").Offset((page - 1) * size).Limit(size).Find(&list).Error; err != nil {
		response.Error(c, "查询快照失败")
		return
	}
	response.OKPage(c, list, total, page, size)
}

// GetFirewallSnapshot 单份快照详情，含原始 dump
func (h *Handler) GetFirewallSnapshot(c *gin.Context) {
	var snap model.FirewallSnapshot
	if err := h.DB.First(&snap, idParam(c)).Error; err != nil {
		response.NotFound(c, "快照不存在")
		return
	}
	if _, ok := h.loadHostForFirewall(c, snap.HostID); !ok {
		return
	}
	response.OK(c, snap)
}

// RollbackFirewall 回滚到指定快照：把快照里有、现在没有的补回去，现在有、快照里没有的删掉
func (h *Handler) RollbackFirewall(c *gin.Context) {
	var req struct {
		ConfirmProd bool `json:"confirmProd"`
	}
	_ = c.ShouldBindJSON(&req)

	var snap model.FirewallSnapshot
	if err := h.DB.First(&snap, idParam(c)).Error; err != nil {
		response.NotFound(c, "快照不存在")
		return
	}
	host, ok := h.loadHostForFirewall(c, snap.HostID)
	if !ok {
		return
	}
	var want []fwx.Rule
	if err := json.Unmarshal([]byte(snap.Rules), &want); err != nil {
		response.BadRequest(c, "快照内容无法解析，不能回滚")
		return
	}
	current, res := h.readFirewall(c.Request.Context(), host)
	if res.Status != "success" {
		response.BadRequest(c, "读不到真机规则，不回滚："+truncate(strings.TrimSpace(res.Stderr), 200))
		return
	}
	if current.Backend != snap.Backend {
		response.BadRequest(c, fmt.Sprintf("快照是 %s 的规则，当前后端是 %s，不能直接回滚", snap.Backend, current.Backend))
		return
	}

	useSudo := h.firewallSudo() && host.Username != "root"
	wantKeys := map[string]fwx.Rule{}
	for _, r := range want {
		wantKeys[r.Key()] = r
	}
	nowKeys := map[string]fwx.Rule{}
	for _, r := range current.Rules {
		nowKeys[r.Key()] = r
	}

	plan := firewallPlan{Backend: current.Backend, Persistent: current.Persistent}
	for key, r := range wantKeys {
		if _, ok := nowKeys[key]; ok {
			continue
		}
		cmd, err := fwx.AddCommand(current.Backend, ruleSpec(r))
		if err != nil {
			plan.Skipped = append(plan.Skipped, r.Raw+"："+err.Error())
			continue
		}
		plan.Adds = append(plan.Adds, r.Raw)
		plan.Commands = append(plan.Commands, fwx.SudoWrap(cmd, host.Username, useSudo))
	}
	for key, r := range nowKeys {
		if _, ok := wantKeys[key]; ok {
			continue
		}
		cmd, err := fwx.DeleteCommand(current.Backend, ruleSpec(r))
		if err != nil {
			plan.Skipped = append(plan.Skipped, r.Raw+"："+err.Error())
			continue
		}
		plan.Removes = append(plan.Removes, r.Raw)
		plan.Commands = append(plan.Commands, fwx.SudoWrap(cmd, host.Username, useSudo))
	}
	sort.Strings(plan.Commands)

	if len(plan.Commands) == 0 {
		response.OK(c, gin.H{"applied": false, "reason": "当前规则与该快照一致，无需回滚", "plan": plan})
		return
	}
	user := middleware.CurrentUser(c)
	job, err := h.RunOnHosts(c.Request.Context(), ExecRequest{
		Name:        fmt.Sprintf("防火墙回滚到快照#%d: %s", snap.ID, host.Name),
		Command:     strings.Join(plan.Commands, "\n"),
		Timeout:     120,
		HostIDs:     []uint{host.ID},
		UserID:      user.ID,
		Operator:    user.Username,
		Source:      "manual",
		ConfirmProd: req.ConfirmProd,
		ClientIP:    c.ClientIP(),
	})
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	if after, afterRes := h.readFirewall(c.Request.Context(), host); afterRes.Status == "success" {
		h.saveFirewallSnapshot(host, after, afterRes.Stdout, "after-rollback", user.Username, job.ID)
		// 回滚同样会让平台登记与真机产生差异，状态必须跟着回写；
		// 漏了这一步的话清理建议与列表页会一直显示回滚前的旧状态
		var rules []model.FirewallRule
		h.DB.Where("host_id = ?", host.ID).Find(&rules)
		h.persistFirewallCheck(host.ID, rules, after.Rules)
	}
	response.OK(c, gin.H{"applied": true, "job": job, "plan": plan})
}

func ruleSpec(r fwx.Rule) fwx.Spec {
	return fwx.Spec{
		Direction: r.Direction, Action: r.Action, Protocol: r.Protocol,
		Source: r.Source, Port: r.Port, Service: r.Service,
	}
}

// ---------- 清理建议 ----------

// FirewallCleanup 清理建议：过期的、没责任人的、长期零命中的放行规则
func (h *Handler) FirewallCleanup(c *gin.Context) {
	hostID := uintQuery(c, "hostId")
	if hostID > 0 {
		if _, ok := h.loadHostForFirewall(c, hostID); !ok {
			return
		}
	}
	idleDays := h.configInt(CfgFirewallIdleDays, 30)
	q := h.DB.Model(&model.FirewallRule{})
	if hostID > 0 {
		q = q.Where("host_id = ?", hostID)
	}
	var rules []model.FirewallRule
	q.Find(&rules)

	now := time.Now()
	type suggestion struct {
		RuleID uint   `json:"ruleId"`
		HostID uint   `json:"hostId"`
		Level  string `json:"level"`
		Reason string `json:"reason"`
		Detail string `json:"detail"`
	}
	out := []suggestion{}
	for _, r := range rules {
		detail := fmt.Sprintf("%s %s %s %s→%s", r.Direction, r.Action, r.Protocol, r.Source, ruleTarget(r))
		switch {
		case r.ExpiresAt != nil && r.ExpiresAt.Before(now):
			out = append(out, suggestion{r.ID, r.HostID, "error",
				fmt.Sprintf("已过期 %s", r.ExpiresAt.Format("2006-01-02")), detail})
		case r.Action == fwx.ActionAccept && r.Source == "any" && r.Service == "" && r.Port == "":
			out = append(out, suggestion{r.ID, r.HostID, "error", "对所有来源放行且未限端口", detail})
		case r.Owner == "":
			out = append(out, suggestion{r.ID, r.HostID, "warning", "没有责任人", detail})
		case r.Action == fwx.ActionAccept && r.HasCounter && r.Hits == 0 &&
			r.LastCheckAt != nil && r.CreatedAt.Before(now.AddDate(0, 0, -idleDays)):
			out = append(out, suggestion{r.ID, r.HostID, "warning",
				fmt.Sprintf("创建超过 %d 天且从未命中", idleDays), detail})
		case r.State == "drift":
			out = append(out, suggestion{r.ID, r.HostID, "info", "平台登记与真机不一致，待下发", detail})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return levelRank(out[i].Level) < levelRank(out[j].Level) })
	response.OK(c, gin.H{
		"idleDays":    idleDays,
		"suggestions": out,
		"note":        "真机上有、平台没登记的规则不在这里列出，平台不会擅自删它；请在规则表里先接管再决定",
	})
}

func levelRank(level string) int {
	switch level {
	case "error":
		return 0
	case "warning":
		return 1
	default:
		return 2
	}
}

// ---------- 安全组 ----------

type firewallGroupReq struct {
	Name          string `json:"name"`
	Description   string `json:"description"`
	MemberHostIDs []uint `json:"memberHostIds"`
	Enabled       *bool  `json:"enabled"`
}

// ListFirewallGroups 安全组列表，带成员数与规则数
func (h *Handler) ListFirewallGroups(c *gin.Context) {
	var groups []model.FirewallGroup
	if err := h.DB.Order("id asc").Find(&groups).Error; err != nil {
		response.Error(c, "查询安全组失败")
		return
	}
	type row struct {
		model.FirewallGroup
		MemberCount int `json:"memberCount"`
		RuleCount   int `json:"ruleCount"`
	}
	out := make([]row, 0, len(groups))
	for _, g := range groups {
		var n int64
		// 只数模板规则，不数铺到主机上的副本
		h.DB.Model(&model.FirewallRule{}).Where("group_id = ? AND host_id = 0", g.ID).Count(&n)
		out = append(out, row{g, len(parseHostIDs(g.MemberHostIDs)), int(n)})
	}
	response.OK(c, out)
}

// SaveFirewallGroup 新增或更新安全组
func (h *Handler) SaveFirewallGroup(c *gin.Context) {
	var req firewallGroupReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "请求参数有误")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		response.BadRequest(c, "安全组名称不能为空")
		return
	}
	user := middleware.CurrentUser(c)
	// 成员只保留数据权限内的主机，越权的 ID 直接剔除。
	// 存储格式必须是 JSON 数组：全站的主机 ID 列表（CronJob.HostIDs 等）都用 parseHostIDs 读，
	// 它只认 JSON —— 早先这里存逗号分隔，结果成员数恒为 0、铺规则直接报「没有成员主机」。
	members := h.filterVisibleHostIDs(user, req.MemberHostIDs)
	if members == nil {
		members = []uint{}
	}
	idsJSON, _ := json.Marshal(members)
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}

	id := idParam(c)
	if id == 0 {
		group := model.FirewallGroup{
			Name: req.Name, Description: req.Description,
			MemberHostIDs: string(idsJSON), Enabled: enabled, CreatedBy: user.Username,
		}
		if err := h.DB.Create(&group).Error; err != nil {
			response.BadRequest(c, "安全组创建失败，名称可能重复")
			return
		}
		if !enabled {
			h.DB.Model(&group).Update("enabled", false)
		}
		response.OK(c, group)
		return
	}
	var group model.FirewallGroup
	if err := h.DB.First(&group, id).Error; err != nil {
		response.NotFound(c, "安全组不存在")
		return
	}
	if err := h.DB.Model(&group).Updates(map[string]any{
		"name": req.Name, "description": req.Description,
		"member_host_ids": string(idsJSON), "enabled": enabled,
	}).Error; err != nil {
		response.BadRequest(c, "安全组更新失败，名称可能重复")
		return
	}
	h.DB.First(&group, id)
	response.OK(c, group)
}

// DeleteFirewallGroup 删除安全组：只删组自己的模板规则，已经铺到成员主机上的那些留下来。
//
// 理由是那些副本已经是「这台机器该有的规则」，其中可能已经下发生效；
// 跟着组一起删掉会让平台登记与真机凭空产生差异，而且真机上的规则并不会消失。
// 它们只是失去组归属，变成主机自己的独立规则。
func (h *Handler) DeleteFirewallGroup(c *gin.Context) {
	id := idParam(c)
	var group model.FirewallGroup
	if err := h.DB.First(&group, id).Error; err != nil {
		response.NotFound(c, "安全组不存在")
		return
	}
	h.DB.Where("group_id = ? AND host_id = 0", id).Delete(&model.FirewallRule{})
	detached := h.DB.Model(&model.FirewallRule{}).
		Where("group_id = ? AND host_id > 0", id).Update("group_id", 0).RowsAffected
	if err := h.DB.Delete(&group).Error; err != nil {
		response.Error(c, "安全组删除失败")
		return
	}
	response.OK(c, gin.H{
		"deleted":  id,
		"detached": detached,
		"note": fmt.Sprintf("已删除安全组与它的模板规则；已铺到成员主机上的 %d 条保留为主机自己的规则（真机上那些规则不会自动撤销，"+
			"要移除请在对应主机上停用并再下发一次）", detached),
	})
}

// DispatchFirewallGroup 把安全组的规则铺到成员主机：为每台缺这条规则的主机登记一条同样的规则。
//
// 刻意只登记不下发：一次点按钮就往一批生产机上改防火墙风险太高，
// 登记完仍然要逐台过预检与下发（那里有闸门和生产二次确认）。
func (h *Handler) DispatchFirewallGroup(c *gin.Context) {
	var group model.FirewallGroup
	if err := h.DB.First(&group, idParam(c)).Error; err != nil {
		response.NotFound(c, "安全组不存在")
		return
	}
	var rules []model.FirewallRule
	// 只取组自己的模板规则（host_id = 0）。铺出去的副本也带着 group_id，
	// 不加这个条件的话第二次「铺到成员」会把副本当模板再铺一遍，条数指数增长。
	h.DB.Where("group_id = ? AND host_id = 0", group.ID).Find(&rules)
	if len(rules) == 0 {
		response.BadRequest(c, "这个安全组里还没有规则")
		return
	}
	user := middleware.CurrentUser(c)
	members := h.filterVisibleHostIDs(user, parseHostIDs(group.MemberHostIDs))
	if len(members) == 0 {
		response.BadRequest(c, "安全组没有你有权限的成员主机")
		return
	}

	created, skipped := 0, 0
	for _, hostID := range members {
		for _, r := range rules {
			var exist int64
			h.DB.Model(&model.FirewallRule{}).
				Where("host_id = ? AND rule_key = ?", hostID, r.RuleKey).Count(&exist)
			if exist > 0 {
				skipped++
				continue
			}
			copied := model.FirewallRule{
				HostID: hostID, GroupID: group.ID,
				Direction: r.Direction, Action: r.Action, Protocol: r.Protocol,
				Source: r.Source, Port: r.Port, Service: r.Service, RuleKey: r.RuleKey,
				Description: r.Description, Owner: r.Owner,
				Lifecycle: r.Lifecycle, ExpiresAt: r.ExpiresAt,
				Origin: "platform", State: "pending", Enabled: true,
				CreatedBy: user.Username,
			}
			if err := h.DB.Create(&copied).Error; err == nil {
				created++
			}
		}
	}
	response.OK(c, gin.H{
		"hosts": len(members), "created": created, "skipped": skipped,
		"note": "规则已登记到成员主机，仍需逐台预检并下发才会在真机生效",
	})
}

// uintQuery 读 uint 类型的查询参数
func uintQuery(c *gin.Context, key string) uint {
	n, _ := strconv.ParseUint(strings.TrimSpace(c.Query(key)), 10, 64)
	return uint(n)
}
