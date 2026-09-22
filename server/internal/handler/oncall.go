package handler

import (
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"ops-platform/server/internal/middleware"
	"ops-platform/server/internal/model"
	"ops-platform/server/internal/response"
)

// 轮换周期
const (
	rotationHourly = "hourly"
	rotationDaily  = "daily"
	rotationWeekly = "weekly"
)

var rotationLabels = map[string]string{
	rotationHourly: "每小时",
	rotationDaily:  "每天",
	rotationWeekly: "每周",
}

const (
	// onCallLookbackHours 只处理这个时间窗内产生的告警。
	// 否则新建一张值班表会立刻把库里积压的老告警全部重叫一遍。
	onCallLookbackHours = 24
	// onCallMaxAlertsPerRun 单次处理的告警上限，防止告警风暴时把消息表刷爆
	onCallMaxAlertsPerRun = 200
)

// rotationPeriod 一个轮换周期的长度
func rotationPeriod(rotation string) time.Duration {
	switch rotation {
	case rotationHourly:
		return time.Hour
	case rotationWeekly:
		return 7 * 24 * time.Hour
	default:
		return 24 * time.Hour
	}
}

// rotationIndex 算出 at 时刻轮到成员列表里的第几个（纯函数，便于单测）。
//
// at 早于基准时间时固定返回 0：排班表还没开始生效，按第一个人算最符合直觉。
func rotationIndex(rotation string, startAt, at time.Time, memberCount int) int {
	if memberCount <= 0 {
		return 0
	}
	period := rotationPeriod(rotation)
	elapsed := at.Sub(startAt)
	if elapsed < 0 {
		return 0
	}
	return int(elapsed/period) % memberCount
}

// escalationLevel 按"已经过了多久还没人确认"算出当前应该叫到第几级。
//
// level 0 = 当班的人，告警一出现就叫；之后每过 ackWait 分钟升一级，最多到 maxLevel-1。
func escalationLevel(minutesSince, ackWaitMinutes, maxLevel int) int {
	if maxLevel <= 1 {
		return 0
	}
	if ackWaitMinutes <= 0 {
		// 等待时间配成 0 等于"立刻叫到顶"，这不是想要的语义，按只叫当班处理
		return 0
	}
	level := minutesSince / ackWaitMinutes
	if level > maxLevel-1 {
		level = maxLevel - 1
	}
	if level < 0 {
		level = 0
	}
	return level
}

// severityMatches 告警级别是否在值班表的处理范围内（空表示全部）
func severityMatches(matchSeverity, severity string) bool {
	matchSeverity = strings.TrimSpace(matchSeverity)
	if matchSeverity == "" {
		return true
	}
	for _, item := range strings.Split(matchSeverity, ",") {
		if strings.TrimSpace(item) == severity {
			return true
		}
	}
	return false
}

// onCallPerson 某一级应该叫的人
type onCallPerson struct {
	Level    int    `json:"level"`
	UserID   uint   `json:"userId"`
	UserName string `json:"userName"`
	Nickname string `json:"nickname"`
	Email    string `json:"email"`
	// Source rotation | override
	Source string `json:"source"`
}

// userBrief 成员候选与展示用的精简用户
type userBrief struct {
	ID       uint   `json:"id"`
	Username string `json:"username"`
	Nickname string `json:"nickname"`
	Email    string `json:"email"`
}

// loadUsers 按 ID 批量取用户，返回 map 便于按需取用
func (h *Handler) loadUsers(ids []uint) map[uint]userBrief {
	result := map[uint]userBrief{}
	if len(ids) == 0 {
		return result
	}
	var users []model.User
	h.DB.Select("id", "username", "nickname", "email").Where("id IN ?", ids).Find(&users)
	for _, item := range users {
		result[item.ID] = userBrief{
			ID: item.ID, Username: item.Username,
			Nickname: item.Nickname, Email: item.Email,
		}
	}
	return result
}

// activeOverride 取 at 时刻生效的代班（多条重叠时取最晚创建的那条）
func (h *Handler) activeOverride(scheduleID uint, at time.Time) *model.OnCallOverride {
	var override model.OnCallOverride
	err := h.DB.Where("schedule_id = ? AND start_at <= ? AND end_at > ?", scheduleID, at, at).
		Order("id desc").First(&override).Error
	if err != nil {
		return nil
	}
	return &override
}

// resolveOnCall 算出某时刻某一级该叫谁。
//
// level 0 会被代班覆盖；升级的级别一律按成员顺序往下走 ——
// 代班是"今天我替你值"，不是"我替你承担所有升级"。
func (h *Handler) resolveOnCall(schedule model.OnCallSchedule, at time.Time, level int) *onCallPerson {
	members := parseIDList(schedule.Members)
	if len(members) == 0 {
		return nil
	}

	base := rotationIndex(schedule.Rotation, schedule.StartAt, at, len(members))
	userID := members[(base+level)%len(members)]
	source := "rotation"
	if level == 0 {
		if override := h.activeOverride(schedule.ID, at); override != nil {
			userID, source = override.UserID, "override"
		}
	}

	users := h.loadUsers([]uint{userID})
	person := &onCallPerson{Level: level, UserID: userID, Source: source}
	if user, ok := users[userID]; ok {
		person.UserName, person.Nickname, person.Email = user.Username, user.Nickname, user.Email
	} else {
		// 成员被删号了也要如实暴露，而不是静默跳过
		person.UserName = fmt.Sprintf("#%d(已删除)", userID)
	}
	return person
}

// ---------- 值班表接口 ----------

type scheduleView struct {
	model.OnCallSchedule
	RotationLabel string        `json:"rotationLabel"`
	MemberList    []userBrief   `json:"memberList"`
	Current       *onCallPerson `json:"current"`
	NextRotateAt  *time.Time    `json:"nextRotateAt"`
}

// nextRotateAt 下一次交接的时刻
func nextRotateAt(schedule model.OnCallSchedule, at time.Time) *time.Time {
	period := rotationPeriod(schedule.Rotation)
	if at.Before(schedule.StartAt) {
		start := schedule.StartAt
		return &start
	}
	elapsed := at.Sub(schedule.StartAt)
	next := schedule.StartAt.Add((elapsed/period + 1) * period)
	return &next
}

func (h *Handler) ListOnCallSchedules(c *gin.Context) {
	var list []model.OnCallSchedule
	if err := h.DB.Order("id asc").Find(&list).Error; err != nil {
		response.Error(c, "查询值班表失败")
		return
	}

	now := time.Now()
	views := make([]scheduleView, 0, len(list))
	for _, item := range list {
		members := parseIDList(item.Members)
		users := h.loadUsers(members)
		briefs := make([]userBrief, 0, len(members))
		for _, id := range members {
			if user, ok := users[id]; ok {
				briefs = append(briefs, user)
			} else {
				briefs = append(briefs, userBrief{ID: id, Username: fmt.Sprintf("#%d(已删除)", id)})
			}
		}
		views = append(views, scheduleView{
			OnCallSchedule: item,
			RotationLabel:  rotationLabels[item.Rotation],
			MemberList:     briefs,
			Current:        h.resolveOnCall(item, now, 0),
			NextRotateAt:   nextRotateAt(item, now),
		})
	}
	response.OK(c, views)
}

// OnCallCandidates 可选成员：启用中的用户
func (h *Handler) OnCallCandidates(c *gin.Context) {
	var users []model.User
	h.DB.Select("id", "username", "nickname", "email").
		Where("status = ?", 1).Order("id asc").Find(&users)
	briefs := make([]userBrief, 0, len(users))
	for _, item := range users {
		briefs = append(briefs, userBrief{
			ID: item.ID, Username: item.Username, Nickname: item.Nickname, Email: item.Email,
		})
	}
	response.OK(c, gin.H{
		"users": briefs,
		"rotations": []gin.H{
			{"key": rotationHourly, "label": rotationLabels[rotationHourly]},
			{"key": rotationDaily, "label": rotationLabels[rotationDaily]},
			{"key": rotationWeekly, "label": rotationLabels[rotationWeekly]},
		},
	})
}

type scheduleReq struct {
	Name           string `json:"name"`
	Members        []uint `json:"members"`
	Rotation       string `json:"rotation"`
	StartAt        string `json:"startAt"`
	MatchSeverity  string `json:"matchSeverity"`
	AckWaitMinutes int    `json:"ackWaitMinutes"`
	MaxLevel       int    `json:"maxLevel"`
	NotifyEmail    *bool  `json:"notifyEmail"`
	Enabled        *bool  `json:"enabled"`
	Remark         string `json:"remark"`
}

// normalize 校验并补默认值，同时返回解析好的基准时间
func (req *scheduleReq) normalize() (time.Time, error) {
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		return time.Time{}, fmt.Errorf("值班表名称不能为空")
	}
	if len(req.Members) == 0 {
		return time.Time{}, fmt.Errorf("至少选一个值班成员")
	}
	if len(req.Members) > 20 {
		return time.Time{}, fmt.Errorf("成员最多 20 人")
	}
	seen := map[uint]bool{}
	for _, id := range req.Members {
		if seen[id] {
			return time.Time{}, fmt.Errorf("成员不能重复")
		}
		seen[id] = true
	}
	if _, ok := rotationLabels[req.Rotation]; !ok {
		req.Rotation = rotationDaily
	}

	startAt := time.Now().Truncate(time.Hour)
	if raw := strings.TrimSpace(req.StartAt); raw != "" {
		parsed, err := time.ParseInLocation("2006-01-02 15:04:05", raw, time.Local)
		if err != nil {
			parsed, err = time.ParseInLocation("2006-01-02 15:04", raw, time.Local)
		}
		if err != nil {
			return time.Time{}, fmt.Errorf("轮换基准时间格式应为 2006-01-02 15:04")
		}
		startAt = parsed
	}

	if req.AckWaitMinutes <= 0 {
		req.AckWaitMinutes = 10
	}
	if req.AckWaitMinutes > 24*60 {
		return time.Time{}, fmt.Errorf("升级等待时间最长 24 小时")
	}
	if req.MaxLevel <= 0 {
		req.MaxLevel = 1
	}
	if req.MaxLevel > len(req.Members) {
		// 级数超过人数就会转回同一个人，等于反复骚扰同一个人
		return time.Time{}, fmt.Errorf("升级级数不能超过成员人数（%d）", len(req.Members))
	}
	for _, item := range strings.Split(req.MatchSeverity, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if item != "info" && item != "warning" && item != "critical" {
			return time.Time{}, fmt.Errorf("告警级别只能是 info / warning / critical")
		}
	}
	return startAt, nil
}

func (h *Handler) CreateOnCallSchedule(c *gin.Context) {
	var req scheduleReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "参数错误")
		return
	}
	startAt, err := req.normalize()
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	members, _ := json.Marshal(req.Members)

	item := model.OnCallSchedule{
		Name: req.Name, Members: string(members), Rotation: req.Rotation,
		StartAt: startAt, MatchSeverity: strings.TrimSpace(req.MatchSeverity),
		AckWaitMinutes: req.AckWaitMinutes, MaxLevel: req.MaxLevel,
		Remark: req.Remark, CreatedBy: middleware.CurrentUser(c).ID,
	}
	wantEnabled, wantEmail := true, false
	if req.Enabled != nil {
		wantEnabled = *req.Enabled
	}
	if req.NotifyEmail != nil {
		wantEmail = *req.NotifyEmail
	}
	item.Enabled, item.NotifyEmail = wantEnabled, wantEmail
	if err := h.DB.Create(&item).Error; err != nil {
		response.Error(c, "创建失败")
		return
	}
	// enabled 带 gorm default，Create 后会被回填成 true，显式关掉的要写回去
	if !wantEnabled {
		h.DB.Model(&model.OnCallSchedule{}).Where("id = ?", item.ID).
			Updates(map[string]any{"enabled": false})
		item.Enabled = false
	}
	response.OK(c, item)
}

func (h *Handler) UpdateOnCallSchedule(c *gin.Context) {
	var item model.OnCallSchedule
	if err := h.DB.First(&item, idParam(c)).Error; err != nil {
		response.NotFound(c, "值班表不存在")
		return
	}
	var req scheduleReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "参数错误")
		return
	}
	startAt, err := req.normalize()
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	members, _ := json.Marshal(req.Members)

	updates := map[string]any{
		"name": req.Name, "members": string(members), "rotation": req.Rotation,
		"start_at": startAt, "match_severity": strings.TrimSpace(req.MatchSeverity),
		"ack_wait_minutes": req.AckWaitMinutes, "max_level": req.MaxLevel,
		"remark": req.Remark,
	}
	if req.Enabled != nil {
		updates["enabled"] = *req.Enabled
	}
	if req.NotifyEmail != nil {
		updates["notify_email"] = *req.NotifyEmail
	}
	if err := h.DB.Model(&model.OnCallSchedule{}).Where("id = ?", item.ID).Updates(updates).Error; err != nil {
		response.Error(c, "更新失败")
		return
	}
	h.DB.First(&item, item.ID)
	response.OK(c, item)
}

func (h *Handler) DeleteOnCallSchedule(c *gin.Context) {
	var item model.OnCallSchedule
	if err := h.DB.First(&item, idParam(c)).Error; err != nil {
		response.NotFound(c, "值班表不存在")
		return
	}
	// 代班跟着值班表走；升级记录留着，它是"当时到底叫了谁"的凭据
	h.DB.Where("schedule_id = ?", item.ID).Delete(&model.OnCallOverride{})
	if err := h.DB.Delete(&model.OnCallSchedule{}, item.ID).Error; err != nil {
		response.Error(c, "删除失败")
		return
	}
	response.OK(c, gin.H{"detail": "值班表与其代班已删除，历史升级记录保留"})
}

// PreviewOnCall 预览未来若干天的排班，建表时用来确认"是不是我想的那个顺序"
func (h *Handler) PreviewOnCall(c *gin.Context) {
	var item model.OnCallSchedule
	if err := h.DB.First(&item, idParam(c)).Error; err != nil {
		response.NotFound(c, "值班表不存在")
		return
	}
	days := 7
	if raw := c.Query("days"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 && n <= 30 {
			days = n
		}
	}

	period := rotationPeriod(item.Rotation)
	now := time.Now()
	deadline := now.Add(time.Duration(days) * 24 * time.Hour)
	// 从当前所在周期的起点开始列，避免第一段显示成"从现在开始"
	cursor := item.StartAt
	if now.After(item.StartAt) {
		cursor = item.StartAt.Add(now.Sub(item.StartAt) / period * period)
	}

	type shift struct {
		StartAt time.Time     `json:"startAt"`
		EndAt   time.Time     `json:"endAt"`
		Person  *onCallPerson `json:"person"`
	}
	shifts := make([]shift, 0, 32)
	for cursor.Before(deadline) && len(shifts) < 60 {
		shifts = append(shifts, shift{
			StartAt: cursor, EndAt: cursor.Add(period),
			Person: h.resolveOnCall(item, cursor, 0),
		})
		cursor = cursor.Add(period)
	}
	response.OK(c, gin.H{"shifts": shifts, "rotationLabel": rotationLabels[item.Rotation]})
}

// ---------- 代班 ----------

func (h *Handler) ListOnCallOverrides(c *gin.Context) {
	scheduleID := idParam(c)
	var list []model.OnCallOverride
	if err := h.DB.Where("schedule_id = ?", scheduleID).Order("start_at desc").
		Limit(100).Find(&list).Error; err != nil {
		response.Error(c, "查询代班失败")
		return
	}

	ids := make([]uint, 0, len(list))
	for _, item := range list {
		ids = append(ids, item.UserID)
	}
	users := h.loadUsers(ids)

	type view struct {
		model.OnCallOverride
		UserName string `json:"userName"`
		Active   bool   `json:"active"`
	}
	now := time.Now()
	views := make([]view, 0, len(list))
	for _, item := range list {
		name := fmt.Sprintf("#%d(已删除)", item.UserID)
		if user, ok := users[item.UserID]; ok {
			name = user.Username
		}
		views = append(views, view{
			OnCallOverride: item, UserName: name,
			Active: !item.StartAt.After(now) && item.EndAt.After(now),
		})
	}
	response.OK(c, views)
}

func (h *Handler) CreateOnCallOverride(c *gin.Context) {
	scheduleID := idParam(c)
	var schedule model.OnCallSchedule
	if err := h.DB.First(&schedule, scheduleID).Error; err != nil {
		response.NotFound(c, "值班表不存在")
		return
	}
	var req struct {
		UserID  uint   `json:"userId"`
		StartAt string `json:"startAt"`
		EndAt   string `json:"endAt"`
		Reason  string `json:"reason"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "参数错误")
		return
	}
	if req.UserID == 0 {
		response.BadRequest(c, "请选择代班人")
		return
	}
	parse := func(raw string) (time.Time, error) {
		raw = strings.TrimSpace(raw)
		if t, err := time.ParseInLocation("2006-01-02 15:04:05", raw, time.Local); err == nil {
			return t, nil
		}
		return time.ParseInLocation("2006-01-02 15:04", raw, time.Local)
	}
	startAt, err := parse(req.StartAt)
	if err != nil {
		response.BadRequest(c, "开始时间格式应为 2006-01-02 15:04")
		return
	}
	endAt, err := parse(req.EndAt)
	if err != nil {
		response.BadRequest(c, "结束时间格式应为 2006-01-02 15:04")
		return
	}
	if !endAt.After(startAt) {
		response.BadRequest(c, "结束时间必须晚于开始时间")
		return
	}
	if len(h.loadUsers([]uint{req.UserID})) == 0 {
		response.BadRequest(c, "代班人不存在")
		return
	}

	item := model.OnCallOverride{
		ScheduleID: schedule.ID, UserID: req.UserID,
		StartAt: startAt, EndAt: endAt, Reason: req.Reason,
		CreatedBy: middleware.CurrentUser(c).ID,
	}
	if err := h.DB.Create(&item).Error; err != nil {
		response.Error(c, "创建失败")
		return
	}
	response.OK(c, item)
}

func (h *Handler) DeleteOnCallOverride(c *gin.Context) {
	overrideID, _ := strconv.ParseUint(c.Param("overrideId"), 10, 64)
	var item model.OnCallOverride
	if err := h.DB.First(&item, uint(overrideID)).Error; err != nil {
		response.NotFound(c, "代班记录不存在")
		return
	}
	if err := h.DB.Delete(&model.OnCallOverride{}, item.ID).Error; err != nil {
		response.Error(c, "删除失败")
		return
	}
	response.OK(c, gin.H{"detail": "代班已删除"})
}

// ---------- 升级 ----------

// ListAlertEscalations 升级记录。带 alertId 时只看某条告警的叫人历史。
func (h *Handler) ListAlertEscalations(c *gin.Context) {
	page, size := pageParams(c)
	q := h.DB.Model(&model.AlertEscalation{})
	if raw := c.Query("alertId"); raw != "" {
		if id, err := strconv.Atoi(raw); err == nil {
			q = q.Where("alert_id = ?", id)
		}
	}
	if raw := c.Query("scheduleId"); raw != "" {
		if id, err := strconv.Atoi(raw); err == nil {
			q = q.Where("schedule_id = ?", id)
		}
	}
	var total int64
	q.Count(&total)

	var list []model.AlertEscalation
	if err := q.Order("id desc").Offset((page - 1) * size).Limit(size).Find(&list).Error; err != nil {
		response.Error(c, "查询升级记录失败")
		return
	}

	// 顺带把告警标题带出来，不然只有 alertId 看不出是哪条
	alertIDs := make([]uint, 0, len(list))
	for _, item := range list {
		alertIDs = append(alertIDs, item.AlertID)
	}
	titles := map[uint]string{}
	if len(alertIDs) > 0 {
		var alerts []model.Alert
		h.DB.Select("id", "title", "status").Where("id IN ?", alertIDs).Find(&alerts)
		for _, alert := range alerts {
			titles[alert.ID] = alert.Title + "（" + alert.Status + "）"
		}
	}
	type view struct {
		model.AlertEscalation
		AlertTitle string `json:"alertTitle"`
	}
	views := make([]view, 0, len(list))
	for _, item := range list {
		views = append(views, view{AlertEscalation: item, AlertTitle: titles[item.AlertID]})
	}
	response.OK(c, gin.H{"list": views, "total": total, "page": page, "pageSize": size})
}

// notifyOnCall 给某个人发一次"叫人"通知，并落升级记录。
//
// 站内消息是平台自带通道，一定可用；邮件是可选的，SMTP 没配或没填邮箱就只记一条
// failed，不影响站内消息 —— 叫人这件事不能因为邮件挂了就整条链断掉。
func (h *Handler) notifyOnCall(schedule model.OnCallSchedule, alert model.Alert, person onCallPerson) {
	base := model.AlertEscalation{
		AlertID: alert.ID, ScheduleID: schedule.ID, Level: person.Level,
		UserID: person.UserID, UserName: person.UserName, Source: person.Source,
	}

	title := fmt.Sprintf("值班呼叫（第 %d 级）：%s", person.Level+1, alert.Title)
	if person.Level == 0 {
		title = "值班呼叫：" + alert.Title
	}
	content := fmt.Sprintf("值班表【%s】把这条 %s 级告警派给你。\n\n%s\n\n首次出现 %s，已累计 %d 次。%s",
		schedule.Name, alert.Severity, alert.Summary,
		alert.FirstSeenAt.Format("2006-01-02 15:04:05"), alert.Count,
		"确认后不再向上升级。")

	record := base
	record.Channel = "message"
	if sent := h.fanoutMessages([]uint{person.UserID}, model.Message{
		Type: "alert", Title: title, Content: content,
		Level: alert.Severity, RefID: alert.ID,
	}); sent > 0 {
		record.Status, record.Detail = "sent", "站内消息已投递"
	} else {
		record.Status, record.Detail = "failed", "站内消息投递失败"
	}
	h.DB.Create(&record)

	if !schedule.NotifyEmail {
		return
	}
	mail := base
	mail.Channel = "email"
	if person.Email == "" {
		mail.Status, mail.Detail = "failed", "该用户没有填邮箱"
		h.DB.Create(&mail)
		return
	}
	// 借用邮件渠道的发信能力，收件人临时指定成值班人的邮箱。
	// 这里刻意指定 oncall.call 模板并传值班场景的变量 —— 在这之前它用的是
	// alertMailVars + 回落到 alert.default，于是上面精心拼好的「值班呼叫（第 N 级）/
	// 派给你 / 确认后不再升级」只进了站内消息，邮件里收到的是一封和普通告警
	// 一模一样的信，看不出这是在叫自己
	err := h.sendMail(model.NotifyChannel{
		Type: "email", Recipients: person.Email, Name: "值班呼叫",
		TemplateCode: tplOnCallCode,
	}, onCallVars(schedule, alert, person.Level+1, person.UserName, title))
	if err != nil {
		mail.Status, mail.Detail = "failed", truncate(err.Error(), 240)
	} else {
		mail.Status, mail.Detail = "sent", "邮件已发送至 "+person.Email
	}
	h.DB.Create(&mail)
}

// escalateSchedule 按一张值班表处理当前未确认的告警，返回叫人次数与一句说明
func (h *Handler) escalateSchedule(schedule model.OnCallSchedule) (int, string) {
	members := parseIDList(schedule.Members)
	if len(members) == 0 {
		return 0, "值班表没有成员"
	}

	now := time.Now()
	// 只叫"还在烧、且是这张表建好之后出现"的告警：
	// 前者避免骚扰已确认/已恢复的，后者避免新建表时把积压老告警全叫一遍
	since := now.Add(-onCallLookbackHours * time.Hour)
	if schedule.CreatedAt.After(since) {
		since = schedule.CreatedAt
	}
	var alerts []model.Alert
	err := h.DB.Where("status = ? AND first_seen_at >= ?", "firing", since).
		Order("id asc").Limit(onCallMaxAlertsPerRun).Find(&alerts).Error
	if err != nil {
		return 0, "读取告警失败: " + err.Error()
	}

	called := 0
	skipped := 0
	silenced := 0
	for _, alert := range alerts {
		if !severityMatches(schedule.MatchSeverity, alert.Severity) {
			skipped++
			continue
		}
		// 静默 / 维护窗口按「当下」重新判定：窗口里不叫人，窗口一结束又会照常升级。
		// 不能只看 alert.silenced_by —— 那是入库那一刻的结论，窗口早就可能结束了。
		if hit, ok := h.matchSilence(alert, now); ok {
			silenced++
			log.Printf("[oncall] 告警 %d 在「%s」窗口内，本轮不叫人", alert.ID, hit.Name)
			continue
		}
		minutes := int(now.Sub(alert.FirstSeenAt).Minutes())
		target := escalationLevel(minutes, schedule.AckWaitMinutes, schedule.MaxLevel)

		// 已经叫过的级别不再重复叫
		var done []int
		h.DB.Model(&model.AlertEscalation{}).
			Where("alert_id = ? AND schedule_id = ? AND channel = ?", alert.ID, schedule.ID, "message").
			Distinct().Pluck("level", &done)
		doneSet := map[int]bool{}
		for _, level := range done {
			doneSet[level] = true
		}

		for level := 0; level <= target; level++ {
			if doneSet[level] {
				continue
			}
			person := h.resolveOnCall(schedule, now, level)
			if person == nil {
				break
			}
			h.notifyOnCall(schedule, alert, *person)
			called++
		}
	}

	detail := fmt.Sprintf("扫描 %d 条未确认告警，叫人 %d 次", len(alerts)-skipped, called)
	if silenced > 0 {
		detail += fmt.Sprintf("，另有 %d 条在静默/维护窗口内未叫", silenced)
	}
	return called, detail
}

// RunOnCallEscalation 手动跑一次某张值班表的升级（试跑，和定时同一条路径）
func (h *Handler) RunOnCallEscalation(c *gin.Context) {
	var schedule model.OnCallSchedule
	if err := h.DB.First(&schedule, idParam(c)).Error; err != nil {
		response.NotFound(c, "值班表不存在")
		return
	}
	called, detail := h.escalateSchedule(schedule)
	response.OK(c, gin.H{"called": called, "detail": detail})
}

// EscalateOnCallForSchedule 定时入口：处理全部启用中的值班表
func (h *Handler) EscalateOnCallForSchedule() {
	var schedules []model.OnCallSchedule
	if err := h.DB.Where("enabled = ?", true).Find(&schedules).Error; err != nil {
		log.Printf("[oncall] 读取值班表失败: %v", err)
		return
	}
	if len(schedules) == 0 {
		return
	}
	total := 0
	for _, schedule := range schedules {
		called, _ := h.escalateSchedule(schedule)
		total += called
	}
	h.markFixedRun("oncall", fmt.Sprintf("共 %d 张值班表，叫人 %d 次", len(schedules), total))
	if total > 0 {
		log.Printf("[oncall] 升级完成: 共 %d 张值班表，叫人 %d 次", len(schedules), total)
	}
}
