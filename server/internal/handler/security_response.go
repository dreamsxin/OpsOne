package handler

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"ops-platform/server/internal/middleware"
	"ops-platform/server/internal/model"
	"ops-platform/server/internal/response"
)

// 安全响应：把研判台缺的三块补上。
//
//  1. 原始数据视图 —— 事件的 RefTable/RefID 指向原始流水行，此前只能看到
//     截断过的证据摘要，点不回去。原始行被留存清理删掉时如实说「已被清理」。
//  2. 升格为事件工单 —— 平台里「派发」唯一诚实的落法：交给事件中心那套
//     已经在跑的机制（负责人、处置时间线、SLA），而不是新造一条对外派发链路。
//  3. 学习建议 —— 从已有数据里算出「该改哪个配置」，并且**通过就真的改**。
//
// 刻意**没有**做的：处置审批。平台的定位是内网自用、管理员自己对自己负责
// （见 docs/SECURITY.md 第 8 节的一贯口径），研判台上一轮也已经写明「不做多级审批」。
// 加一层点「同意」的流程不会让处置更安全，只会让人绕过它。

// ---------- 原始数据视图 ----------

// secRawView 原始流水行的统一视图。
//
// 四张来源表字段完全不同，这里拍平成「字段名 → 值」的有序列表而不是各自定义
// 一个结构体：界面只需要如实把原始行摆出来，为此写四个 DTO 是在给自己找活干。
type secRawField struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

// GetSecurityEventRaw 取事件对应的原始流水行。
//
// 这是研判台此前的一个真空洞：证据摘要截断到 2000 字（命令原文常常更长），
// 而 RefTable/RefID 在界面上只是一行纯文本。
func (h *Handler) GetSecurityEventRaw(c *gin.Context) {
	var event model.SecurityEvent
	if err := h.DB.First(&event, idParam(c)).Error; err != nil {
		response.NotFound(c, "安全事件不存在")
		return
	}

	// 只认采集器写过的那四张表。RefTable 本来就是平台自己写进去的，
	// 但拿它去拼查询之前仍要过一次白名单 —— 这是防「以后有人往这个字段写别的」
	if _, ok := secSourceTables[event.Source]; !ok {
		response.BadRequest(c, "未知的事件来源: "+event.Source)
		return
	}
	if event.RefID == 0 || event.RefTable == "" {
		response.OK(c, gin.H{
			"available": false,
			"reason":    "这条事件没有记录原始流水的位置",
			"event":     secEventView(event),
		})
		return
	}

	fields, extra, err := h.loadSecRawRow(event)
	if err != nil {
		// 原始行没了：绝大多数情况是被数据留存清理删掉了。
		// 这不是故障，但必须说清 —— 否则界面上一个空白比错误更让人困惑
		if errors.Is(err, gorm.ErrRecordNotFound) {
			response.OK(c, gin.H{
				"available": false,
				"reason": fmt.Sprintf(
					"原始流水 %s#%d 已经不在了，通常是被「数据留存」清理掉了。"+
						"事件上的证据摘要仍然保留（最多 2000 字），那是当时抄下来的",
					event.RefTable, event.RefID),
				"event":    secEventView(event),
				"evidence": event.Evidence,
			})
			return
		}
		response.Error(c, "读取原始流水失败: "+err.Error())
		return
	}

	response.OK(c, gin.H{
		"available": true,
		"table":     event.RefTable,
		"refId":     event.RefID,
		"fields":    fields,
		"extra":     extra,
		"event":     secEventView(event),
		"note": "这里是原始流水行的原文，没有经过摘要截断。" +
			"它与事件上的证据摘要可能不同：摘要是采集那一刻抄下来的，原始行在那之后可能被更新过",
	})
}

// loadSecRawRow 按来源取原始行。extra 放「需要再跳一次才能看全」的关联信息。
func (h *Handler) loadSecRawRow(event model.SecurityEvent) ([]secRawField, gin.H, error) {
	switch event.RefTable {
	case "exec_guard_logs":
		var row model.ExecGuardLog
		if err := h.DB.First(&row, event.RefID).Error; err != nil {
			return nil, nil, err
		}
		return []secRawField{
			{"发生时间", row.CreatedAt.Format("2006-01-02 15:04:05")},
			{"来源", row.Source},
			{"闸门判定", row.Status},
			{"规则动作", row.Action},
			{"拦截原因", row.Reason},
			{"命令原文", row.Command},
			{"目标主机", row.HostNames},
			{"主机数 / 其中生产", fmt.Sprintf("%d / %d", row.HostCount, row.ProdCount)},
			{"命中规则", fmt.Sprintf("#%d %s", row.RuleID, row.Pattern)},
			{"操作人", row.Username},
			{"客户端 IP", row.ClientIP},
		}, gin.H{"cronJobId": row.CronJobID, "userId": row.UserID}, nil

	case "session_commands":
		var row model.SessionCommand
		if err := h.DB.First(&row, event.RefID).Error; err != nil {
			return nil, nil, err
		}
		// 命令行自己不带「谁在哪台机器上敲的」，那在父表上
		var session model.Session
		hasSession := h.DB.First(&session, row.SessionID).Error == nil
		fields := []secRawField{
			{"发生时间", row.CreatedAt.Format("2006-01-02 15:04:05")},
			{"命令原文", row.Command},
			{"终端判定", row.Risk},
			{"命中规则", fmt.Sprintf("#%d %s", row.RuleID, row.RuleDesc)},
			{"会话内偏移", fmt.Sprintf("%d ms", row.OffsetMs)},
		}
		if hasSession {
			fields = append(fields,
				secRawField{"会话", fmt.Sprintf("#%d", session.ID)},
				secRawField{"操作人", session.Username},
				secRawField{"登录用户", session.LoginUser},
				secRawField{"目标主机", fmt.Sprintf("%s（%s）", session.HostName, session.Address)},
				secRawField{"客户端 IP", session.ClientIP},
			)
		} else {
			fields = append(fields, secRawField{"会话", "会话记录已被清理，查不到是谁在哪台机器上敲的"})
		}
		return fields, gin.H{"sessionId": row.SessionID, "sessionAlive": hasSession}, nil

	case "exposure_scans":
		var row model.ExposureScan
		if err := h.DB.First(&row, event.RefID).Error; err != nil {
			return nil, nil, err
		}
		var target model.ExposureTarget
		hasTarget := h.DB.First(&target, row.TargetID).Error == nil
		fields := []secRawField{
			{"扫描时间", row.CreatedAt.Format("2006-01-02 15:04:05")},
			{"扫描结果", row.Status},
			{"扫过的端口数", strconv.Itoa(row.Scanned)},
			{"开放端口", row.OpenPorts},
			{"未登记开放", row.Unexpected},
			{"登记了却没开", row.Missing},
			{"耗时", fmt.Sprintf("%d ms", row.CostMs)},
			{"触发方式", row.Operator},
			{"错误", row.ErrorMsg},
		}
		if hasTarget {
			fields = append(fields,
				secRawField{"扫描目标", fmt.Sprintf("%s（%s）", target.Name, target.Address)},
				secRawField{"当前基线", orDash(target.Baseline)},
			)
		} else {
			fields = append(fields, secRawField{"扫描目标", "目标登记已被删除"})
		}
		return fields, gin.H{"targetId": row.TargetID, "targetAlive": hasTarget}, nil

	case "audit_logs":
		var row model.AuditLog
		if err := h.DB.First(&row, event.RefID).Error; err != nil {
			return nil, nil, err
		}
		return []secRawField{
			{"发生时间", row.CreatedAt.Format("2006-01-02 15:04:05")},
			{"请求", row.Method + " " + row.Path},
			{"动作", row.Action},
			{"HTTP 状态", strconv.Itoa(row.Status)},
			{"操作人", row.Username},
			{"API 令牌", row.TokenName},
			{"来源 IP", row.IP},
			{"耗时", fmt.Sprintf("%d ms", row.CostMs)},
		}, gin.H{"userId": row.UserID, "tokenId": row.TokenID}, nil
	}
	return nil, nil, fmt.Errorf("不支持的来源表: %s", event.RefTable)
}

// ---------- 升格为事件工单 ----------

type secEscalateReq struct {
	Assignee string `json:"assignee"`
	Title    string `json:"title"`
	Summary  string `json:"summary"`
}

// EscalateSecurityEvent 把安全事件升格成事件工单。
//
// 为什么是这个而不是「派发记录」：平台没有对接任何外部工单/SOAR 系统，
// 做一个派发页只会是一张永远空着的表。而事件中心是**已经在跑**的那套机制 ——
// 有负责人、有处置时间线、有 SLA 计时与超时提醒。升格出来的工单自动带 SLA，
// 因为 SLA 是按级别与时间戳实时算的，不需要额外挂接。
//
// 状态联动：升格同时把安全事件推进到「研判中」（如果还是待研判）——
// 建了单却还挂在待研判队列里，会让两边的计数都不可信。
func (h *Handler) EscalateSecurityEvent(c *gin.Context) {
	var event model.SecurityEvent
	if err := h.DB.First(&event, idParam(c)).Error; err != nil {
		response.NotFound(c, "安全事件不存在")
		return
	}
	if event.EventID != 0 {
		// 已经有单了就不再建第二个：同一条线索两张单，处置记录会分叉
		var existing model.Event
		if h.DB.First(&existing, event.EventID).Error == nil {
			response.BadRequest(c, fmt.Sprintf("这条安全事件已经升格为事件工单 #%d（%s），不重复建单",
				existing.ID, existing.Status))
			return
		}
		// 工单被删了，允许重新升格，但要说清
	}

	var req secEscalateReq
	_ = c.ShouldBindJSON(&req)

	title := strings.TrimSpace(req.Title)
	if title == "" {
		title = fmt.Sprintf("[安全] %s", event.Title)
	}
	summary := strings.TrimSpace(req.Summary)
	if summary == "" {
		summary = h.secEscalateSummary(event)
	}

	user := middleware.CurrentUser(c)
	created, err := h.newEvent(user, eventCreateReq{
		Title: title, Severity: event.Severity, Summary: summary,
		Assignee: req.Assignee,
	}, "secevent",
		fmt.Sprintf("安全事件 #%d %s", event.ID, event.Title),
		fmt.Sprintf("由安全事件 #%d 升格（来源 %s，命中 %d 次）",
			event.ID, secSourceLabels[event.Source], event.HitCount))
	if err != nil {
		response.Error(c, err.Error())
		return
	}

	updates := map[string]any{"event_id": created.ID}
	// 建了单就不该还挂在「待研判」里
	if event.Status == secStatusNew {
		updates["status"] = secStatusInvestigating
	}
	h.DB.Model(&model.SecurityEvent{}).Where("id = ?", event.ID).Updates(updates)

	h.appendSecLog(event.ID, "respond",
		fmt.Sprintf("升格为事件工单 #%d%s", created.ID,
			map[bool]string{true: "，指派给 " + req.Assignee, false: ""}[req.Assignee != ""]),
		user.Username)

	response.OK(c, gin.H{
		"eventId": created.ID,
		"status":  created.Status,
		"note": "工单自动进入事件中心的 SLA 计时。注意 SLA 的「响应」口径：" +
			"指派不算响应，要状态离开待处理或写下第一条处置记录才算",
	})
}

// secEscalateSummary 拼工单摘要。把研判需要的上下文一次性带过去，
// 免得接手的人还得回研判台翻一遍。
func (h *Handler) secEscalateSummary(event model.SecurityEvent) string {
	var b strings.Builder
	fmt.Fprintf(&b, "来源：%s（%s）\n", secSourceLabels[event.Source], event.Source)
	fmt.Fprintf(&b, "首次 %s，最近 %s，共命中 %d 次",
		event.FirstSeenAt.Format("2006-01-02 15:04"),
		event.LastSeenAt.Format("2006-01-02 15:04"), event.HitCount)
	if event.HitsAfterClose > 0 {
		fmt.Fprintf(&b, "（结案后又命中 %d 次）", event.HitsAfterClose)
	}
	b.WriteString("\n")
	if event.Actor != "" || event.ActorIP != "" {
		fmt.Fprintf(&b, "发起方：%s %s\n", orDash(event.Actor), orDash(event.ActorIP))
	}
	if event.Target != "" {
		fmt.Fprintf(&b, "目标：%s %s %s\n", event.Target, orDash(event.Port), orDash(event.Protocol))
	}
	if event.RefTable != "" {
		fmt.Fprintf(&b, "原始流水：%s#%d\n", event.RefTable, event.RefID)
	}
	if event.Evidence != "" {
		fmt.Fprintf(&b, "\n证据摘要：\n%s", truncate(event.Evidence, 1500))
	}
	return b.String()
}

// ---------- 学习建议 ----------

const (
	secSuggestPortBaseline = "port_baseline"
	secSuggestMute         = "mute_fingerprint"
	secSuggestEnforce      = "signature_enforce"
	// secSuggestMinHits 一条线索复现多少次才值得建议改配置。
	// 1 次不算规律，拿单次命中去劝人改基线是在制造噪音
	secSuggestMinHits = 3
	secSuggestLimit   = 100
)

var secSuggestKindLabels = map[string]string{
	secSuggestPortBaseline: "端口基线",
	secSuggestMute:         "误报白名单",
	secSuggestEnforce:      "特征提到拦截",
}

// secSuggestion 一条建议。Key 是稳定标识，拒绝时记的是它。
type secSuggestion struct {
	Key   string `json:"key"`
	Kind  string `json:"kind"`
	Label string `json:"kindLabel"`
	Title string `json:"title"`
	// Reason 为什么给这条建议，带上支撑它的数字
	Reason string `json:"reason"`
	// Action 通过之后平台会做什么，一句话说清 —— 不能让人点了才知道改了什么
	Action string `json:"action"`
	// Evidence 支撑数据，界面直接展示
	HitCount int    `json:"hitCount"`
	RefID    uint   `json:"refId"`
	RefName  string `json:"refName"`
	Extra    string `json:"extra"`
}

// ListSecuritySuggestions 现算一遍建议。
//
// 不落库是刻意的：建议是「当前数据下该改什么」的函数，存下来就会过期 ——
// 端口关掉了、特征撤下了，那条建议还挂着。只有「被拒绝过」这件事需要持久化。
func (h *Handler) ListSecuritySuggestions(c *gin.Context) {
	dismissed := h.secDismissedKeys()

	var all []secSuggestion
	all = append(all, h.suggestPortBaseline()...)
	all = append(all, h.suggestMuteFingerprints()...)
	all = append(all, h.suggestSignatureEnforce()...)

	kept := make([]secSuggestion, 0, len(all))
	var skipped int
	for _, s := range all {
		if _, ok := dismissed[s.Key]; ok {
			skipped++
			continue
		}
		kept = append(kept, s)
	}
	// 复现次数多的排前面：那是最有把握的建议
	sort.SliceStable(kept, func(i, j int) bool { return kept[i].HitCount > kept[j].HitCount })
	if len(kept) > secSuggestLimit {
		kept = kept[:secSuggestLimit]
	}

	byKind := map[string]int{}
	for _, s := range kept {
		byKind[s.Kind]++
	}

	response.OK(c, gin.H{
		"items":     kept,
		"byKind":    byKind,
		"dismissed": skipped,
		"minHits":   secSuggestMinHits,
		"note": fmt.Sprintf("建议是**每次现算**的，不落库 —— 端口关掉了、特征撤下了，"+
			"对应的建议下一次就不再出现。只有「拒绝」会被记下来（避免反复提示），"+
			"随时可以在下面撤销。复现少于 %d 次的不进建议：1 次不算规律。", secSuggestMinHits),
	})
}

func (h *Handler) secDismissedKeys() map[string]struct{} {
	var rows []model.SecuritySuggestionDismissal
	h.DB.Select("key").Find(&rows)
	out := make(map[string]struct{}, len(rows))
	for _, r := range rows {
		out[r.Key] = struct{}{}
	}
	return out
}

// suggestPortBaseline 反复被报「未登记开放」的端口 → 建议加进该目标的基线。
//
// 数据源是安全事件表里 source=exposure 的行：采集器对每个端口单独建一条事件，
// HitCount 就是这个端口被扫到的次数。用它而不是 ExposureTarget.LastUnexpected：
// 后者只是最后一次扫描的快照，推不出「反复」。
func (h *Handler) suggestPortBaseline() []secSuggestion {
	var events []model.SecurityEvent
	h.DB.Where("source = ? AND port <> '' AND hit_count >= ?", "exposure", secSuggestMinHits).
		Order("hit_count desc").Limit(200).Find(&events)
	if len(events) == 0 {
		return nil
	}

	var targets []model.ExposureTarget
	h.DB.Find(&targets)
	byAddr := make(map[string]model.ExposureTarget, len(targets))
	for _, t := range targets {
		byAddr[t.Address] = t
	}

	out := make([]secSuggestion, 0, len(events))
	for _, e := range events {
		target, ok := byAddr[e.Target]
		if !ok {
			continue // 目标登记已删除，建议无处可落
		}
		port, err := parsePort(e.Port)
		if err != nil {
			continue
		}
		// 已经在基线里了就不用再建议
		base, _ := parsePortSpec(target.Baseline)
		if containsInt(base, port) {
			continue
		}
		out = append(out, secSuggestion{
			Key:   fmt.Sprintf("%s|%d|%d", secSuggestPortBaseline, target.ID, port),
			Kind:  secSuggestPortBaseline,
			Label: secSuggestKindLabels[secSuggestPortBaseline],
			Title: fmt.Sprintf("%s（%s）的 %d 端口反复被报未登记", target.Name, target.Address, port),
			Reason: fmt.Sprintf("这个端口已经被扫到 %d 次，每次都算「开着但没登记」。"+
				"如果它本来就该开，登记进基线之后就不会再报；如果不该开，那要去关它而不是加基线",
				e.HitCount),
			Action:   fmt.Sprintf("把 %d 加入「%s」的基线（只动基线一列，不碰要扫的端口清单）", port, target.Name),
			HitCount: e.HitCount,
			RefID:    target.ID,
			RefName:  target.Name,
			Extra:    fmt.Sprintf("当前基线：%s", orDash(target.Baseline)),
		})
	}
	return out
}

// suggestMuteFingerprints 判了「忽略」却还在累加命中的指纹 → 建议加白名单。
//
// 「忽略」只改这条事件的状态，采集器该记还是记、命中照样累加 ——
// 这是刻意的（不能让一次判断把后续的同类线索永久藏起来），
// 但如果它已经又响了很多次，说明判断是稳定的，那就该正式加白名单让采集器跳过。
func (h *Handler) suggestMuteFingerprints() []secSuggestion {
	var events []model.SecurityEvent
	h.DB.Where("status = ? AND hits_after_close >= ?", secStatusIgnored, secSuggestMinHits).
		Order("hits_after_close desc").Limit(100).Find(&events)
	if len(events) == 0 {
		return nil
	}

	var mutes []model.SecurityEventMute
	h.DB.Select("fingerprint").Find(&mutes)
	muted := make(map[string]struct{}, len(mutes))
	for _, m := range mutes {
		muted[m.Fingerprint] = struct{}{}
	}

	out := make([]secSuggestion, 0, len(events))
	for _, e := range events {
		if _, ok := muted[e.Fingerprint]; ok {
			continue
		}
		out = append(out, secSuggestion{
			Key:   fmt.Sprintf("%s|%s", secSuggestMute, e.Fingerprint),
			Kind:  secSuggestMute,
			Label: secSuggestKindLabels[secSuggestMute],
			Title: fmt.Sprintf("「%s」判了忽略之后又命中 %d 次", e.Title, e.HitsAfterClose),
			Reason: fmt.Sprintf("这条已经被判为「忽略」，但采集器还在记、命中又累加了 %d 次。"+
				"判断既然是稳定的，加白名单能让它彻底不再进研判台 —— 挡掉的次数仍然会照记",
				e.HitsAfterClose),
			Action:   "把这条指纹加入误报白名单（采集器直接跳过，但仍统计挡掉次数）",
			HitCount: e.HitsAfterClose,
			RefID:    e.ID,
			RefName:  secSourceLabels[e.Source],
			Extra:    fmt.Sprintf("结论：%s", orDash(e.Verdict)),
		})
	}
	return out
}

// suggestSignatureEnforce observe 阶段但已经真的警告过很多次的 command 特征
// → 建议提到 enforce（真拦）。
//
// 数据源是 exec_guard_logs 里按规则 ID 统计的 warn 次数：特征应用后会生成一条
// 命令规则，observe 阶段对应 action=warn。一条规则反复 warn 说明它确实命中真实场景，
// 留在观察态等于「看着它过去」。
func (h *Handler) suggestSignatureEnforce() []secSuggestion {
	var signatures []model.Signature
	h.DB.Where("kind = ? AND stage = ? AND enabled = ? AND rule_id > 0", "command", "observe", true).
		Find(&signatures)
	if len(signatures) == 0 {
		return nil
	}

	out := make([]secSuggestion, 0, len(signatures))
	for _, sig := range signatures {
		var warned int64
		h.DB.Model(&model.ExecGuardLog{}).
			Where("rule_id = ? AND status = ?", sig.RuleID, "warn").Count(&warned)
		if warned < int64(secSuggestMinHits) {
			continue
		}
		out = append(out, secSuggestion{
			Key:   fmt.Sprintf("%s|%d", secSuggestEnforce, sig.ID),
			Kind:  secSuggestEnforce,
			Label: secSuggestKindLabels[secSuggestEnforce],
			Title: fmt.Sprintf("特征「%s」在观察态已经警告 %d 次", sig.Name, warned),
			Reason: fmt.Sprintf("这条特征生成的命令规则（#%d）已经 warn 过 %d 次，"+
				"说明它确实命中真实场景。留在观察态等于看着这些命令下发过去",
				sig.RuleID, warned),
			Action:   "把特征阶段改为 enforce 并重写命令规则为 block（下次下发会真的被拦）",
			HitCount: int(warned),
			RefID:    sig.ID,
			RefName:  sig.Name,
			Extra:    fmt.Sprintf("模式：%s", truncate(sig.Pattern, 120)),
		})
	}
	return out
}

// ---------- 应用 / 拒绝建议 ----------

type secSuggestApplyReq struct {
	Keys []string `json:"keys"`
	// Decision approve 真的去改 | dismiss 记下「不用提示了」
	Decision string `json:"decision"`
	Reason   string `json:"reason"`
}

// ApplySecuritySuggestions 批量通过或拒绝建议。
//
// 「通过」是真的去改配置，不是标个已读 —— 一个点了没反应的按钮比没有这个按钮更糟。
// 「拒绝」写一条 dismissal，下次不再提示（可撤销）。
func (h *Handler) ApplySecuritySuggestions(c *gin.Context) {
	var req secSuggestApplyReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "参数格式有误")
		return
	}
	if len(req.Keys) == 0 {
		response.BadRequest(c, "请至少选择一条建议")
		return
	}
	if len(req.Keys) > 200 {
		response.BadRequest(c, "一次最多处理 200 条")
		return
	}
	if req.Decision != "approve" && req.Decision != "dismiss" {
		response.BadRequest(c, "decision 只能是 approve 或 dismiss")
		return
	}

	// 重算一遍拿到当前建议，按 Key 索引。**不信前端传来的建议内容** ——
	// 页面上那份可能已经过期（端口关了、特征撤了），照它去改就是改错东西
	current := map[string]secSuggestion{}
	for _, s := range append(append(h.suggestPortBaseline(), h.suggestMuteFingerprints()...),
		h.suggestSignatureEnforce()...) {
		current[s.Key] = s
	}

	user := middleware.CurrentUser(c)
	done, failed := []gin.H{}, []gin.H{}
	for _, key := range req.Keys {
		s, ok := current[key]
		if !ok {
			failed = append(failed, gin.H{"key": key,
				"error": "这条建议已经不成立了（数据变了），刷新后重新看"})
			continue
		}
		if req.Decision == "dismiss" {
			if err := h.dismissSuggestion(s, req.Reason, user.Username); err != nil {
				failed = append(failed, gin.H{"key": key, "error": err.Error()})
				continue
			}
			done = append(done, gin.H{"key": key, "title": s.Title, "result": "已记为不再提示"})
			continue
		}
		result, err := h.approveSuggestion(s, user.Username)
		if err != nil {
			failed = append(failed, gin.H{"key": key, "error": err.Error()})
			continue
		}
		done = append(done, gin.H{"key": key, "title": s.Title, "result": result})
	}

	response.OK(c, gin.H{"done": done, "failed": failed})
}

func (h *Handler) dismissSuggestion(s secSuggestion, reason, operator string) error {
	row := model.SecuritySuggestionDismissal{
		Key: s.Key, Kind: s.Kind, Title: truncate(s.Title, 250),
		Reason: truncate(reason, 250), Operator: operator,
	}
	if err := h.DB.Create(&row).Error; err != nil {
		return fmt.Errorf("记录失败，可能已经拒绝过")
	}
	return nil
}

// approveSuggestion 真的去改配置。每一种建议对应一个明确的写操作。
func (h *Handler) approveSuggestion(s secSuggestion, operator string) (string, error) {
	switch s.Kind {
	case secSuggestPortBaseline:
		return h.applyPortBaseline(s, operator)
	case secSuggestMute:
		return h.applyMuteSuggestion(s, operator)
	case secSuggestEnforce:
		return h.applyEnforceSuggestion(s, operator)
	}
	return "", fmt.Errorf("不支持的建议类型: %s", s.Kind)
}

// applyPortBaseline 把端口并进目标的基线。
//
// 只 Update 基线一列，不走 UpdateExposureTarget —— 那个接口是全量覆盖，
// 会把 name/address/ports/timeout 一起写回去，批量应用时很容易把别的字段改没。
func (h *Handler) applyPortBaseline(s secSuggestion, operator string) (string, error) {
	parts := strings.Split(s.Key, "|")
	if len(parts) != 3 {
		return "", fmt.Errorf("建议标识异常")
	}
	port, err := parsePort(parts[2])
	if err != nil {
		return "", fmt.Errorf("端口异常: %s", parts[2])
	}

	var target model.ExposureTarget
	if err := h.DB.First(&target, s.RefID).Error; err != nil {
		return "", fmt.Errorf("扫描目标已不存在")
	}
	base, _ := parsePortSpec(target.Baseline)
	if containsInt(base, port) {
		return fmt.Sprintf("%d 已经在基线里了", port), nil
	}
	base = append(base, port)
	sort.Ints(base)
	merged := formatPorts(base)
	if len(merged) > 500 {
		return "", fmt.Errorf("基线长度超过 500 字符，先精简一下再加")
	}
	if err := h.DB.Model(&model.ExposureTarget{}).Where("id = ?", target.ID).
		Update("baseline", merged).Error; err != nil {
		return "", fmt.Errorf("写入基线失败")
	}
	return fmt.Sprintf("已把 %d 加入「%s」的基线（现在是 %s）", port, target.Name, merged), nil
}

// applyMuteSuggestion 加误报白名单。复用研判台那条路径的语义：
// 指纹级白名单、采集器跳过、挡掉次数照记。
func (h *Handler) applyMuteSuggestion(s secSuggestion, operator string) (string, error) {
	var event model.SecurityEvent
	if err := h.DB.First(&event, s.RefID).Error; err != nil {
		return "", fmt.Errorf("安全事件已不存在")
	}
	reason := fmt.Sprintf("按学习建议加入：判了忽略之后又命中 %d 次", event.HitsAfterClose)
	if !h.muteFingerprint(event, reason, operator) {
		return "这条指纹已经在白名单里了", nil
	}
	h.appendSecLog(event.ID, "respond", "按学习建议加入误报白名单", operator)
	return "已加入误报白名单，采集器之后会跳过这条指纹（挡掉次数仍然照记）", nil
}

// applyEnforceSuggestion 把特征从 observe 提到 enforce，并重写它生成的命令规则。
//
// 这一步会让下一次下发**真的被拦**，是这三类建议里唯一有真实拦截后果的一个，
// 所以返回文案要说清。规则重写复用特征库自己的 writeSignatureRule，
// 不在这里另写一份 —— 两处逻辑分叉的话，界面上的「已生效」就不可信了。
func (h *Handler) applyEnforceSuggestion(s secSuggestion, operator string) (string, error) {
	var sig model.Signature
	if err := h.DB.First(&sig, s.RefID).Error; err != nil {
		return "", fmt.Errorf("特征已不存在")
	}
	if sig.Stage == "enforce" {
		return "这条特征已经是拦截态了", nil
	}
	if err := h.DB.Model(&model.Signature{}).Where("id = ?", sig.ID).
		Update("stage", "enforce").Error; err != nil {
		return "", fmt.Errorf("更新特征阶段失败")
	}
	sig.Stage = "enforce"
	if err := h.writeSignatureRule(&sig, operator); err != nil {
		// 阶段改了但规则没重写 = 页面说拦、实际还在放过。必须报出来而不是当成功
		return "", fmt.Errorf("阶段已改为 enforce，但命令规则重写失败（当前仍按观察态放过）: %w", err)
	}
	return fmt.Sprintf("特征「%s」已提到拦截态，命令规则 #%d 已重写为 block —— 下一次下发会真的被拦",
		sig.Name, sig.RuleID), nil
}

// ListSuggestionDismissals 已拒绝的建议，可撤销
func (h *Handler) ListSuggestionDismissals(c *gin.Context) {
	var rows []model.SecuritySuggestionDismissal
	h.DB.Order("id desc").Limit(200).Find(&rows)
	response.OK(c, gin.H{
		"rows": rows,
		"note": "撤销之后，如果这条建议当前仍然成立，它会重新出现在建议列表里",
	})
}

func (h *Handler) DeleteSuggestionDismissal(c *gin.Context) {
	if err := h.DB.Delete(&model.SecuritySuggestionDismissal{}, idParam(c)).Error; err != nil {
		response.Error(c, "撤销失败")
		return
	}
	response.OK(c, nil)
}

// ---------- 安全概览 ----------

// SecurityOverview 安全概览：把散在各页的安全数据汇成一屏。
//
// 这一页只做聚合，**不产生任何新数据**。每一块都指向一个已经在跑的模块；
// 平台没采集的东西（流量、进程连接、阻断包数）在 gaps 里明说，
// 不用「0」冒充「没有问题」—— 那是最容易骗人的一种看板。
func (h *Handler) SecurityOverview(c *gin.Context) {
	countOf := func(dest any, where string, args ...any) int64 {
		var n int64
		q := h.DB.Model(dest)
		if where != "" {
			q = q.Where(where, args...)
		}
		q.Count(&n)
		return n
	}
	now := time.Now()
	last24h := now.Add(-24 * time.Hour)
	last7d := now.Add(-7 * 24 * time.Hour)

	openStatuses := []string{secStatusNew, secStatusInvestigating, secStatusConfirmed}

	// 安全事件
	var muteHits int64
	h.DB.Model(&model.SecurityEventMute{}).Select("COALESCE(SUM(hit_count),0)").Scan(&muteHits)
	events := gin.H{
		"open":      countOf(&model.SecurityEvent{}, "status IN ?", openStatuses),
		"critical":  countOf(&model.SecurityEvent{}, "severity = ? AND status IN ?", "critical", openStatuses),
		"rehit":     countOf(&model.SecurityEvent{}, "hits_after_close > 0"),
		"new24h":    countOf(&model.SecurityEvent{}, "created_at >= ?", last24h),
		"escalated": countOf(&model.SecurityEvent{}, "event_id > 0"),
		"mutedHits": muteHits,
	}

	// 拦截流水（原始事实，不去重）
	intercept := gin.H{
		"execBlocked24h": countOf(&model.ExecGuardLog{}, "status = ? AND created_at >= ?", "blocked", last24h),
		"execWarn24h":    countOf(&model.ExecGuardLog{}, "status = ? AND created_at >= ?", "warn", last24h),
		"terminal24h":    countOf(&model.SessionCommand{}, "risk = ? AND created_at >= ?", "blocked", last24h),
		"authz24h":       countOf(&model.AuditLog{}, "status = ? AND created_at >= ?", 403, last24h),
	}

	// 暴露面
	var noBaseline int64
	h.DB.Model(&model.ExposureTarget{}).Where("enabled = ? AND baseline = ''", true).Count(&noBaseline)
	exposure := gin.H{
		"targets":      countOf(&model.ExposureTarget{}, "enabled = ?", true),
		"unexpected":   countOf(&model.ExposureTarget{}, "last_status = ?", "unexpected"),
		"failed":       countOf(&model.ExposureTarget{}, "last_status = ?", "failed"),
		"neverScanned": countOf(&model.ExposureTarget{}, "last_scan_at IS NULL"),
		// 基线留空 = 任何开放端口都要提示，这类目标会持续刷噪音
		"noBaseline": noBaseline,
	}

	// 证书与域名
	certs := gin.H{
		"total":     countOf(&model.Certificate{}, "enabled = ?", true),
		"expiring":  countOf(&model.Certificate{}, "status = ?", "expiring"),
		"expired":   countOf(&model.Certificate{}, "status = ?", "expired"),
		"error":     countOf(&model.Certificate{}, "status = ?", "error"),
		"untrusted": countOf(&model.Certificate{}, "trusted = ? AND status <> ?", false, "unknown"),
	}
	domains := gin.H{
		"total":    countOf(&model.Domain{}, "enabled = ?", true),
		"expiring": countOf(&model.Domain{}, "expire_status = ?", "expiring"),
		"expired":  countOf(&model.Domain{}, "expire_status = ?", "expired"),
		"noExpiry": countOf(&model.Domain{}, "expire_status = ?", "unknown"),
		"dnsDrift": countOf(&model.Domain{}, "dns_status = ?", "drift"),
	}

	// 特征库：观察态 vs 拦截态，以及有多少条真的落到了命令规则上
	signatures := gin.H{
		"total":    countOf(&model.Signature{}, "enabled = ?", true),
		"observe":  countOf(&model.Signature{}, "enabled = ? AND stage = ?", true, "observe"),
		"enforce":  countOf(&model.Signature{}, "enabled = ? AND stage = ?", true, "enforce"),
		"applied":  countOf(&model.Signature{}, "enabled = ? AND rule_id > 0", true),
		"portKind": countOf(&model.Signature{}, "enabled = ? AND kind = ?", true, "port"),
	}

	// 防火墙：待下发的规则是「登记了但还没生效」，这个数字长期不降就说明有人在攒
	firewall := gin.H{
		"rules":   countOf(&model.FirewallRule{}, ""),
		"pending": countOf(&model.FirewallRule{}, "state = ?", "pending"),
		"applied": countOf(&model.FirewallRule{}, "state = ?", "applied"),
	}

	// 双因子：启用率
	var users, totpUsers int64
	h.DB.Model(&model.User{}).Where("status = ?", 1).Count(&users)
	h.DB.Model(&model.User{}).Where("status = ? AND totp_enabled = ?", 1, true).Count(&totpUsers)

	// 主机日志监控点
	hostLogs := gin.H{
		"targets":    countOf(&model.HostLogTarget{}, "enabled = ?", true),
		"hit":        countOf(&model.HostLogTarget{}, "last_status = ?", "hit"),
		"unreadable": countOf(&model.HostLogTarget{}, "last_status IN ?", []string{"missing", "denied", "failed"}),
	}

	// 学习建议待办
	dismissed := h.secDismissedKeys()
	var pending int
	for _, s := range append(append(h.suggestPortBaseline(), h.suggestMuteFingerprints()...),
		h.suggestSignatureEnforce()...) {
		if _, ok := dismissed[s.Key]; !ok {
			pending++
		}
	}

	response.OK(c, gin.H{
		"events":      events,
		"intercept":   intercept,
		"exposure":    exposure,
		"certs":       certs,
		"domains":     domains,
		"signatures":  signatures,
		"firewall":    firewall,
		"twoFactor":   gin.H{"users": users, "enabled": totpUsers},
		"hostLogs":    hostLogs,
		"suggestions": pending,
		"trend":       h.secEventTrend(last7d),
		// 这一段是这一页最该被认真读的部分
		"gaps": []gin.H{
			{"item": "阻断包数 / 流量统计", "why": "平台不采流量，也没有常驻 agent；" +
				"防火墙是 SSH 下发 iptables，读不到计数器。这一栏不做，不用 0 冒充「没有攻击」"},
			{"item": "进程与连接明细", "why": "同上，需要常驻 agent 才能持续上报"},
			{"item": "登录失败 / 爆破来源", "why": "审计中间件只挂在鉴权后的路由组上，登录接口在公开路由上 —— " +
				"平台从来没有记录过一次失败登录，这是一处真实的检测盲区"},
			{"item": "读接口的越权尝试", "why": "审计跳过 GET，authz 来源只能看到被拒的写操作"},
			{"item": "外部威胁情报", "why": "内网没有可信的情报通道，特征库只有人工维护的本地规则"},
		},
		"note": "这一页只汇总已有数据，不产生任何新数据；每一块都对应一个在跑的模块。" +
			"「没有采集」和「采集到 0」在这里是分开表达的 —— 前者在 gaps 里列出来",
	})
}

// secEventTrend 最近 7 天每天新增多少条安全事件。
// 按 created_at 的日期分组，用 SQL 做而不是全读出来在 Go 里数。
func (h *Handler) secEventTrend(since time.Time) []gin.H {
	type row struct {
		Day   string
		Total int64
	}
	var rows []row
	// date() 在 sqlite 与 MySQL 下都可用；PG 走 to_char，这里为了跨库用最朴素的写法
	h.DB.Model(&model.SecurityEvent{}).
		Select("substr(created_at, 1, 10) as day, count(*) as total").
		Where("created_at >= ?", since).
		Group("day").Order("day asc").Find(&rows)

	out := make([]gin.H, 0, len(rows))
	for _, r := range rows {
		out = append(out, gin.H{"day": r.Day, "total": r.Total})
	}
	return out
}

func containsInt(list []int, v int) bool {
	for _, item := range list {
		if item == v {
			return true
		}
	}
	return false
}
