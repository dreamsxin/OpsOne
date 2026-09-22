package handler

import (
	"fmt"
	"log"
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

// 监控设置里的 SLA 目标，存在 sys_configs 的 monitor 分组里。
//
// 为什么用配置项而不是单独一张表：这是一组全局参数，没有多版本、没有作用域，
// 用 SysConfig 就能同时白捡「系统配置页也能改」和「留存/审计已覆盖」两件事。
const (
	CfgSLARespondCritical = "monitor.sla_respond_critical"
	CfgSLARespondWarning  = "monitor.sla_respond_warning"
	CfgSLARespondInfo     = "monitor.sla_respond_info"
	CfgSLARecoverCritical = "monitor.sla_recover_critical"
	CfgSLARecoverWarning  = "monitor.sla_recover_warning"
	CfgSLARecoverInfo     = "monitor.sla_recover_info"
	CfgSLARemindBefore    = "monitor.sla_remind_before"
	CfgSLARepeatHours     = "monitor.sla_repeat_hours"
)

// slaSeverities 固定三级，顺序就是展示顺序
var slaSeverities = []string{"critical", "warning", "info"}

var slaSeverityLabels = map[string]string{
	"critical": "紧急", "warning": "警告", "info": "提示",
}

var slaRespondKeys = map[string]string{
	"critical": CfgSLARespondCritical,
	"warning":  CfgSLARespondWarning,
	"info":     CfgSLARespondInfo,
}

var slaRecoverKeys = map[string]string{
	"critical": CfgSLARecoverCritical,
	"warning":  CfgSLARecoverWarning,
	"info":     CfgSLARecoverInfo,
}

// slaDefaults 内置默认值，单位分钟。与 seedSysConfigs 里的一致。
var slaDefaults = map[string]int{
	CfgSLARespondCritical: 15, CfgSLARespondWarning: 60, CfgSLARespondInfo: 480,
	CfgSLARecoverCritical: 60, CfgSLARecoverWarning: 240, CfgSLARecoverInfo: 1440,
	CfgSLARemindBefore: 5, CfgSLARepeatHours: 4,
}

// slaMaxMinutes 一周。再长的目标没有管理意义，还容易是把 15 分钟误填成 15000。
const slaMaxMinutes = 7 * 24 * 60

// slaTargets 一次读齐的目标值，避免在循环里反复查库
type slaTargets struct {
	Respond map[string]int `json:"respond"`
	Recover map[string]int `json:"recover"`
	// RemindBefore 距超时多少分钟算「临期」。0 表示不提前提醒。
	RemindBefore int `json:"remindBefore"`
	// RepeatHours 同一个事件的同一条 SLA 最短提醒间隔（小时）
	RepeatHours int `json:"repeatHours"`
}

// slaConfigMinutes 读一个分钟数配置。
//
// 不能用 h.configInt：那个把 0 当成非法值回落到默认，而这里 0 是有意义的
// ——「这一级不设 SLA」。想关掉某一级就填 0，不该被默认值悄悄顶回来。
func (h *Handler) slaConfigMinutes(key string) int {
	raw := strings.TrimSpace(h.configString(key, ""))
	if raw == "" {
		return slaDefaults[key]
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 || n > slaMaxMinutes {
		return slaDefaults[key]
	}
	return n
}

func (h *Handler) slaTargets() slaTargets {
	t := slaTargets{Respond: map[string]int{}, Recover: map[string]int{}}
	for _, sev := range slaSeverities {
		t.Respond[sev] = h.slaConfigMinutes(slaRespondKeys[sev])
		t.Recover[sev] = h.slaConfigMinutes(slaRecoverKeys[sev])
	}
	t.RemindBefore = h.slaConfigMinutes(CfgSLARemindBefore)
	t.RepeatHours = h.slaConfigMinutes(CfgSLARepeatHours)
	if t.RepeatHours <= 0 {
		t.RepeatHours = slaDefaults[CfgSLARepeatHours]
	}
	return t
}

// slaClock 一条 SLA 的实时状态。
//
// State 的取值都是「人一眼能判断要不要动」的：
//
//	na       不计这条 SLA（没设目标，或事件已关闭且没人响应过）
//	pending  还在时间内
//	risk     快到点了（距超时不足 RemindBefore）
//	met      已经做到，用时在目标内
//	breached 超时了（还没做到，或做到时已经超了）
type slaClock struct {
	Kind          string     `json:"kind"` // respond | recover
	TargetMinutes int        `json:"targetMinutes"`
	State         string     `json:"state"`
	Reason        string     `json:"reason"`
	DueAt         *time.Time `json:"dueAt"`
	DoneAt        *time.Time `json:"doneAt"`
	// UsedSeconds 已用时长：做到了就是「建单到做到」，没做到就是「建单到现在」
	UsedSeconds int64 `json:"usedSeconds"`
	// RemainSeconds 距超时还有多少秒，负数表示已经超了多少秒
	RemainSeconds int64 `json:"remainSeconds"`
}

var slaStateLabels = map[string]string{
	"na": "不计", "pending": "进行中", "risk": "临期", "met": "达标", "breached": "已超时",
}

// evalSLAClock 算一条 SLA。纯函数，时间从外面传进来，方便测。
//
// start 一律是建单时间：平台无法知道「故障真正开始」是几点（那是复盘里人填的），
// 所以 SLA 只承诺「从平台上有这张单开始算」，界面上也这么写。
func evalSLAClock(kind string, targetMinutes int, start time.Time, done *time.Time,
	closed bool, remindBefore int, now time.Time) slaClock {
	clock := slaClock{Kind: kind, TargetMinutes: targetMinutes, DoneAt: done}
	if targetMinutes <= 0 {
		clock.State, clock.Reason = "na", "没有为该级别设置目标"
		return clock
	}
	due := start.Add(time.Duration(targetMinutes) * time.Minute)
	clock.DueAt = &due

	if done != nil {
		clock.UsedSeconds = int64(done.Sub(start).Seconds())
		clock.RemainSeconds = int64(due.Sub(*done).Seconds())
		if clock.RemainSeconds < 0 {
			clock.State = "breached"
			clock.Reason = fmt.Sprintf("用了 %s，超目标 %s",
				humanDuration(clock.UsedSeconds), humanDuration(-clock.RemainSeconds))
		} else {
			clock.State = "met"
			clock.Reason = fmt.Sprintf("用了 %s，目标 %d 分钟", humanDuration(clock.UsedSeconds), targetMinutes)
		}
		return clock
	}

	if closed {
		clock.State = "na"
		clock.Reason = "事件已关闭（判定为无需处理），不计 SLA"
		clock.DueAt = nil
		return clock
	}

	clock.UsedSeconds = int64(now.Sub(start).Seconds())
	clock.RemainSeconds = int64(due.Sub(now).Seconds())
	switch {
	case clock.RemainSeconds < 0:
		clock.State = "breached"
		clock.Reason = fmt.Sprintf("已超时 %s", humanDuration(-clock.RemainSeconds))
	case remindBefore > 0 && clock.RemainSeconds <= int64(remindBefore)*60:
		clock.State = "risk"
		clock.Reason = fmt.Sprintf("还剩 %s", humanDuration(clock.RemainSeconds))
	default:
		clock.State = "pending"
		clock.Reason = fmt.Sprintf("还剩 %s", humanDuration(clock.RemainSeconds))
	}
	return clock
}

// eventSLA 算一个事件的两条 SLA
func eventSLA(event model.Event, targets slaTargets, now time.Time) (slaClock, slaClock) {
	closed := event.Status == "closed"
	respond := evalSLAClock("respond", targets.Respond[event.Severity], event.CreatedAt,
		event.RespondedAt, closed, targets.RemindBefore, now)
	recoverClock := evalSLAClock("recover", targets.Recover[event.Severity], event.CreatedAt,
		event.ResolvedAt, closed, targets.RemindBefore, now)
	return respond, recoverClock
}

// humanDuration 把秒数说成人话。负数交给调用方处理符号。
func humanDuration(seconds int64) string {
	if seconds < 0 {
		seconds = -seconds
	}
	if seconds < 60 {
		return fmt.Sprintf("%d 秒", seconds)
	}
	minutes := seconds / 60
	if minutes < 60 {
		return fmt.Sprintf("%d 分钟", minutes)
	}
	hours := minutes / 60
	if hours < 48 {
		if minutes%60 == 0 {
			return fmt.Sprintf("%d 小时", hours)
		}
		return fmt.Sprintf("%d 小时 %d 分钟", hours, minutes%60)
	}
	return fmt.Sprintf("%d 天 %d 小时", hours/24, hours%24)
}

// eventSLAView 挂在 eventView 上的 sla 字段
func eventSLAView(respond, recoverClock slaClock) gin.H {
	worst := "na"
	rank := map[string]int{"na": 0, "met": 1, "pending": 2, "risk": 3, "breached": 4}
	for _, clock := range []slaClock{respond, recoverClock} {
		if rank[clock.State] > rank[worst] {
			worst = clock.State
		}
	}
	return gin.H{
		"respond": respond, "recover": recoverClock,
		"worst": worst, "worstLabel": slaStateLabels[worst],
	}
}

// ---------- 列表筛选 ----------

// slaFilterStates 列表上能筛的状态。
//
// 只筛「还没做到而且已经超时 / 临期」的单子，不筛历史上超时但已经做完的
// —— 那是复盘口径，翻历史应该去事件复盘里看耗时，别在值班列表里混。
var slaFilterStates = map[string]bool{"breached": true, "risk": true}

// applySLAFilter 按 SLA 状态过滤事件查询。
//
// 直接翻成 SQL 而不是查出来再过滤：过滤发生在分页之前，
// 不然「第 1 页 20 条里筛出 3 条」这种结果会让人以为只超时了 3 条。
func applySLAFilter(q *gorm.DB, state string, targets slaTargets, now time.Time) *gorm.DB {
	if !slaFilterStates[state] {
		return q
	}
	// 已关闭的不计 SLA；已解决的恢复时钟已经停了，响应时钟也一定已经停了
	q = q.Where("status IN ?", []string{"open", "processing"})

	conds := make([]string, 0, 6)
	args := make([]any, 0, 12)
	add := func(doneCol, sev string, minutes int) {
		if minutes <= 0 {
			return
		}
		breachBound := now.Add(-time.Duration(minutes) * time.Minute)
		if state == "breached" {
			conds = append(conds, fmt.Sprintf("(severity = ? AND %s IS NULL AND created_at < ?)", doneCol))
			args = append(args, sev, breachBound)
			return
		}
		// risk：已经进入提醒区间但还没超时
		if targets.RemindBefore <= 0 {
			return
		}
		riskBound := now.Add(-time.Duration(minutes-targets.RemindBefore) * time.Minute)
		conds = append(conds, fmt.Sprintf(
			"(severity = ? AND %s IS NULL AND created_at < ? AND created_at >= ?)", doneCol))
		args = append(args, sev, riskBound, breachBound)
	}
	for _, sev := range slaSeverities {
		add("responded_at", sev, targets.Respond[sev])
		add("resolved_at", sev, targets.Recover[sev])
	}
	if len(conds) == 0 {
		// 一条目标都没设，却要按 SLA 筛：如实返回空，而不是把全部事件当成超时
		return q.Where("1 = 0")
	}
	return q.Where(strings.Join(conds, " OR "), args...)
}

// countSLAState 数一下当前有多少单子处在某个 SLA 状态
func (h *Handler) countSLAState(state string, targets slaTargets, now time.Time) int64 {
	var n int64
	applySLAFilter(h.DB.Model(&model.Event{}), state, targets, now).Count(&n)
	return n
}

// ---------- 响应时间落点 ----------

// markEventResponded 记下第一次有人动手的时间。
//
// 只写一次：第一次之后再怎么改状态、再写多少条记录都不会把响应时间往后推。
func (h *Handler) markEventResponded(event model.Event, at time.Time) {
	if event.RespondedAt != nil {
		return
	}
	h.DB.Model(&model.Event{}).Where("id = ? AND responded_at IS NULL", event.ID).
		Update("responded_at", &at)
}

// ---------- 设置接口 ----------

func (h *Handler) GetSLASettings(c *gin.Context) {
	targets := h.slaTargets()
	now := time.Now()

	levels := make([]gin.H, 0, len(slaSeverities))
	for _, sev := range slaSeverities {
		levels = append(levels, gin.H{
			"severity": sev, "label": slaSeverityLabels[sev],
			"respondKey": slaRespondKeys[sev], "respondMinutes": targets.Respond[sev],
			"recoverKey": slaRecoverKeys[sev], "recoverMinutes": targets.Recover[sev],
		})
	}

	response.OK(c, gin.H{
		"levels":       levels,
		"remindBefore": targets.RemindBefore,
		"repeatHours":  targets.RepeatHours,
		"stats": gin.H{
			"respondBreached": h.countSLAState("breached", slaTargets{
				Respond: targets.Respond, Recover: map[string]int{}, RemindBefore: targets.RemindBefore,
			}, now),
			"recoverBreached": h.countSLAState("breached", slaTargets{
				Respond: map[string]int{}, Recover: targets.Recover, RemindBefore: targets.RemindBefore,
			}, now),
			"risk": h.countSLAState("risk", targets, now),
		},
		// 口径写在接口里，页面直接显示，避免两边各写一份说法
		"notes": []string{
			"响应 SLA 从「事件建单时间」开始算，平台无法知道故障真正开始于几点（那是复盘里人填的）",
			"响应时间的落点只认两件事：状态离开「待处理」，或写下第一条处置记录。指派不算响应",
			"恢复 SLA 从建单算到「已解决」；标记为「已关闭」（判定无需处理）的事件不计 SLA",
			"目标填 0 表示这一级不设 SLA；列表与看板只统计「还没做到且已超时/临期」的单子",
		},
	})
}

type slaSettingsReq struct {
	// Respond / Recover 按 severity 传分钟数，缺的键保持原值
	Respond      map[string]int `json:"respond"`
	Recover      map[string]int `json:"recover"`
	RemindBefore *int           `json:"remindBefore"`
	RepeatHours  *int           `json:"repeatHours"`
}

func (h *Handler) UpdateSLASettings(c *gin.Context) {
	var req slaSettingsReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "参数格式有误")
		return
	}

	updates := map[string]int{}
	collect := func(values map[string]int, keys map[string]string, what string) error {
		for sev, minutes := range values {
			key, ok := keys[sev]
			if !ok {
				return fmt.Errorf("级别只能是 critical / warning / info，收到 %q", sev)
			}
			if minutes < 0 || minutes > slaMaxMinutes {
				return fmt.Errorf("%s%s目标要在 0 ~ %d 分钟之间（0 表示不设）",
					slaSeverityLabels[sev], what, slaMaxMinutes)
			}
			updates[key] = minutes
		}
		return nil
	}
	if err := collect(req.Respond, slaRespondKeys, "响应"); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	if err := collect(req.Recover, slaRecoverKeys, "恢复"); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	if req.RemindBefore != nil {
		if *req.RemindBefore < 0 || *req.RemindBefore > slaMaxMinutes {
			response.BadRequest(c, "临期提醒提前量要在 0 ~ 10080 分钟之间（0 表示不提前提醒）")
			return
		}
		updates[CfgSLARemindBefore] = *req.RemindBefore
	}
	if req.RepeatHours != nil {
		if *req.RepeatHours < 1 || *req.RepeatHours > 24*7 {
			response.BadRequest(c, "重复提醒间隔要在 1 ~ 168 小时之间")
			return
		}
		updates[CfgSLARepeatHours] = *req.RepeatHours
	}
	if len(updates) == 0 {
		response.BadRequest(c, "没有要修改的项")
		return
	}

	// 恢复目标小于响应目标是明显填反了：先把最终值算出来再校验，
	// 不然只改一边的时候会拿新值跟旧值比出误报。
	final := h.slaTargets()
	for _, sev := range slaSeverities {
		if v, ok := updates[slaRespondKeys[sev]]; ok {
			final.Respond[sev] = v
		}
		if v, ok := updates[slaRecoverKeys[sev]]; ok {
			final.Recover[sev] = v
		}
	}
	for _, sev := range slaSeverities {
		if final.Respond[sev] > 0 && final.Recover[sev] > 0 && final.Recover[sev] < final.Respond[sev] {
			response.BadRequest(c, fmt.Sprintf("%s的恢复目标(%d 分钟)不能小于响应目标(%d 分钟)",
				slaSeverityLabels[sev], final.Recover[sev], final.Respond[sev]))
			return
		}
	}

	operator := middleware.CurrentUser(c).Username
	keys := make([]string, 0, len(updates))
	for key := range updates {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		value := strconv.Itoa(updates[key])
		if err := h.DB.Model(&model.SysConfig{}).Where("`key` = ?", key).
			Updates(map[string]any{"value": value, "updated_by": operator}).Error; err != nil {
			response.Error(c, "保存失败: "+key)
			return
		}
	}
	response.OK(c, gin.H{"updated": len(keys)})
}

// ---------- 超时提醒 ----------

// slaAlertTarget 该提醒谁：负责人优先，没人认领就退回建单人。
// 两个都找不到时如实返回空，调用方只写时间线不发消息。
func (h *Handler) slaAlertTarget(event model.Event) (model.User, string, bool) {
	candidates := []struct {
		name string
		why  string
	}{
		{event.Assignee, "负责人"},
		{event.CreatedByName, "建单人（事件还没有负责人）"},
	}
	for _, cand := range candidates {
		if strings.TrimSpace(cand.name) == "" {
			continue
		}
		var user model.User
		if err := h.DB.Where("username = ?", cand.name).First(&user).Error; err == nil {
			return user, cand.why, true
		}
	}
	return model.User{}, "", false
}

// CheckEventSLAForSchedule 扫未完结的事件，对临期和超时的发提醒。
//
// 两条 SLA 各自独立提醒、各自记自己的上次提醒时间，因为「没人接手」和
// 「接手了但迟迟没恢复」要找的人和要做的事不一样。
func (h *Handler) CheckEventSLAForSchedule() {
	now := time.Now()
	targets := h.slaTargets()

	var events []model.Event
	if err := h.DB.Where("status IN ?", []string{"open", "processing"}).Find(&events).Error; err != nil {
		log.Printf("[sla] 查询未完结事件失败: %v", err)
		return
	}

	gap := time.Duration(targets.RepeatHours) * time.Hour
	notified, silent := 0, 0
	for _, event := range events {
		respond, recoverClock := eventSLA(event, targets, now)
		for _, item := range []struct {
			clock  slaClock
			last   *time.Time
			column string
			what   string
			todo   string
		}{
			{respond, event.SLARespondAlertedAt, "sla_respond_alerted_at", "响应",
				"请到「监控告警 → 事件中心」接手，或改派给能处理的人"},
			{recoverClock, event.SLARecoverAlertedAt, "sla_recover_alerted_at", "恢复",
				"请到「监控告警 → 事件中心」推进处置，处理不动就升级"},
		} {
			if item.clock.State != "risk" && item.clock.State != "breached" {
				continue
			}
			if item.last != nil && now.Sub(*item.last) < gap {
				continue
			}

			title := fmt.Sprintf("事件 %s SLA 已超时：%s", item.what, truncate(event.Title, 180))
			if item.clock.State == "risk" {
				title = fmt.Sprintf("事件 %s SLA 即将超时：%s", item.what, truncate(event.Title, 180))
			}
			line := fmt.Sprintf("%s目标 %d 分钟，%s", item.what, item.clock.TargetMinutes, item.clock.Reason)

			user, why, ok := h.slaAlertTarget(event)
			if ok {
				h.DB.Create(&model.Message{
					UserID: user.ID, Type: "event", Level: event.Severity, RefID: event.ID,
					Title: title,
					Content: fmt.Sprintf("事件 #%d（%s）\n%s\n提醒你是因为你是这个事件的%s。\n%s",
						event.ID, slaSeverityLabels[event.Severity], line, why, item.todo),
				})
				notified++
			} else {
				// 没有负责人也没有可用的建单人：消息发不出去，但时间线上必须留痕，
				// 否则这条超时就彻底没人知道了
				silent++
			}

			logText := line
			if !ok {
				logText += "；没有找到可提醒的人（负责人为空且建单人账号不存在），只在时间线留痕"
			} else {
				logText += fmt.Sprintf("；已提醒 %s（%s）", user.Username, why)
			}
			h.appendEventLog(event.ID, "sla", logText, "系统")
			h.DB.Model(&model.Event{}).Where("id = ?", event.ID).Update(item.column, &now)
		}
	}
	if notified > 0 || silent > 0 {
		log.Printf("[sla] 事件 SLA 扫描完成：发出提醒 %d 条，无人可提醒 %d 条（共 %d 个未完结事件）",
			notified, silent, len(events))
	}
}

// RunEventSLACheck 手动触发一次 SLA 扫描
func (h *Handler) RunEventSLACheck(c *gin.Context) {
	h.CheckEventSLAForSchedule()
	response.OK(c, gin.H{"message": "已扫描一次"})
}
