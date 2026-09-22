package handler

import (
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"ops-platform/server/internal/config"
	"ops-platform/server/internal/model"
)

func newSLATestHandler(t *testing.T) *Handler {
	t.Helper()
	dir := t.TempDir()
	g, err := gorm.Open(sqlite.Open(dir+"/sla.db"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	if err := g.AutoMigrate(&model.Event{}, &model.EventLog{}, &model.SysConfig{},
		&model.User{}, &model.Message{}); err != nil {
		t.Fatalf("建表失败: %v", err)
	}
	// Windows 上不关连接，t.TempDir 的清理会失败并把测试判成 FAIL
	t.Cleanup(func() {
		if sqlDB, err := g.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	return New(g, &config.Config{})
}

// testTargets 固定目标，避免测试依赖 seed 的默认值
func testTargets() slaTargets {
	return slaTargets{
		Respond:      map[string]int{"critical": 15, "warning": 60, "info": 0},
		Recover:      map[string]int{"critical": 60, "warning": 240, "info": 0},
		RemindBefore: 5,
		RepeatHours:  4,
	}
}

func TestEvalSLAClockStates(t *testing.T) {
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.Local)
	cases := []struct {
		name      string
		target    int
		startAgo  time.Duration
		done      *time.Time
		closed    bool
		wantState string
	}{
		{"没设目标就不计", 0, 10 * time.Hour, nil, false, "na"},
		{"还早", 60, 10 * time.Minute, nil, false, "pending"},
		{"进入提前量算临期", 60, 56 * time.Minute, nil, false, "risk"},
		{"刚好到点还没超", 60, 55 * time.Minute, nil, false, "risk"},
		{"超时", 60, 61 * time.Minute, nil, false, "breached"},
		{"已关闭且没人响应过不计", 60, 10 * time.Hour, nil, true, "na"},
	}
	for _, tc := range cases {
		start := now.Add(-tc.startAgo)
		clock := evalSLAClock("respond", tc.target, start, tc.done, tc.closed, 5, now)
		if clock.State != tc.wantState {
			t.Errorf("%s: 期望 %s，实际 %s（%s）", tc.name, tc.wantState, clock.State, clock.Reason)
		}
		if clock.Reason == "" {
			t.Errorf("%s: 每种状态都要有一句人能看懂的说明", tc.name)
		}
	}
}

func TestEvalSLAClockStopsAtDoneTime(t *testing.T) {
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.Local)
	start := now.Add(-5 * time.Hour)

	// 5 小时前建单，10 分钟后就有人接手：现在几点都不该影响结论
	done := start.Add(10 * time.Minute)
	clock := evalSLAClock("respond", 15, start, &done, false, 5, now)
	if clock.State != "met" {
		t.Fatalf("目标 15 分钟、10 分钟做到，应该达标，实际 %s（%s）", clock.State, clock.Reason)
	}
	if clock.UsedSeconds != 600 {
		t.Fatalf("已用时长应是 600 秒，实际 %d", clock.UsedSeconds)
	}

	// 做到了但已经超了：算 breached，而不是因为「做完了」就算达标
	late := start.Add(40 * time.Minute)
	clock = evalSLAClock("respond", 15, start, &late, false, 5, now)
	if clock.State != "breached" {
		t.Fatalf("目标 15 分钟、40 分钟才做到，应该算超时，实际 %s", clock.State)
	}
	if clock.RemainSeconds >= 0 {
		t.Fatalf("超时时剩余秒数应为负，实际 %d", clock.RemainSeconds)
	}

	// 已关闭但确实有人响应过：照旧算达标，不能因为关闭就把已有事实抹掉
	clock = evalSLAClock("respond", 15, start, &done, true, 5, now)
	if clock.State != "met" {
		t.Fatalf("已关闭但响应过应仍算达标，实际 %s", clock.State)
	}
}

func TestEvalSLAClockRemindBeforeZeroSkipsRisk(t *testing.T) {
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.Local)
	// 提前量填 0 表示「只在真超时后才提醒」，不能再冒出 risk
	clock := evalSLAClock("respond", 60, now.Add(-59*time.Minute), nil, false, 0, now)
	if clock.State != "pending" {
		t.Fatalf("提前量为 0 时不应有临期，实际 %s", clock.State)
	}
}

func TestEventSLAUsesSeverityTarget(t *testing.T) {
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.Local)
	targets := testTargets()

	// 同样 30 分钟没人管：critical（目标 15）已超时，warning（目标 60）还在时间内
	crit := model.Event{Severity: "critical", Status: "open", CreatedAt: now.Add(-30 * time.Minute)}
	warn := model.Event{Severity: "warning", Status: "open", CreatedAt: now.Add(-30 * time.Minute)}
	if respond, _ := eventSLA(crit, targets, now); respond.State != "breached" {
		t.Fatalf("critical 30 分钟没人接应超时，实际 %s", respond.State)
	}
	if respond, _ := eventSLA(warn, targets, now); respond.State != "pending" {
		t.Fatalf("warning 30 分钟还在 60 分钟目标内，实际 %s", respond.State)
	}
	// info 两条目标都是 0：整条都不计
	info := model.Event{Severity: "info", Status: "open", CreatedAt: now.Add(-100 * time.Hour)}
	respond, recoverClock := eventSLA(info, targets, now)
	if respond.State != "na" || recoverClock.State != "na" {
		t.Fatalf("info 没设目标应两条都不计，实际 %s / %s", respond.State, recoverClock.State)
	}
}

func TestEventSLAViewTakesWorst(t *testing.T) {
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.Local)
	respond := evalSLAClock("respond", 15, now.Add(-2*time.Minute), nil, false, 5, now) // pending
	recoverClock := evalSLAClock("recover", 60, now.Add(-90*time.Minute), nil, false, 5, now)
	view := eventSLAView(respond, recoverClock)
	if view["worst"] != "breached" {
		t.Fatalf("一条超时就该整体算超时，实际 %v", view["worst"])
	}
	// 达标不应该盖过进行中：两条都收尾了才算真的没事
	done := now.Add(-80 * time.Minute)
	metClock := evalSLAClock("respond", 15, now.Add(-90*time.Minute), &done, false, 5, now)
	pending := evalSLAClock("recover", 600, now.Add(-90*time.Minute), nil, false, 5, now)
	view = eventSLAView(metClock, pending)
	if view["worst"] != "pending" {
		t.Fatalf("还有一条在跑就该显示进行中，实际 %v", view["worst"])
	}
}

func TestHumanDuration(t *testing.T) {
	cases := map[int64]string{
		0: "0 秒", 45: "45 秒", 60: "1 分钟", 599: "9 分钟",
		3600: "1 小时", 3660: "1 小时 1 分钟", 172800: "2 天 0 小时",
		-120: "2 分钟", // 负号由调用方决定怎么说，这里只给绝对值
	}
	for seconds, want := range cases {
		if got := humanDuration(seconds); got != want {
			t.Errorf("humanDuration(%d) = %q，期望 %q", seconds, got, want)
		}
	}
}

// applySLAFilter 直接翻成 SQL，必须用真库验，否则错的 SQL 在纯函数测试里看不出来
func TestApplySLAFilterMatchesSQL(t *testing.T) {
	h := newSLATestHandler(t)
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.Local)
	targets := testTargets()

	events := []model.Event{
		// 1 critical 30 分钟没人接 -> 响应超时
		{Title: "critical 超时", Severity: "critical", Status: "open", CreatedAt: now.Add(-30 * time.Minute)},
		// 2 critical 12 分钟（目标 15，提前量 5）-> 临期
		{Title: "critical 临期", Severity: "critical", Status: "open", CreatedAt: now.Add(-12 * time.Minute)},
		// 3 critical 5 分钟 -> 还早
		{Title: "critical 还早", Severity: "critical", Status: "open", CreatedAt: now.Add(-5 * time.Minute)},
		// 4 critical 30 分钟但已经有人接手 -> 响应不算超时；恢复目标 60 还没到
		{Title: "critical 已响应", Severity: "critical", Status: "processing",
			CreatedAt: now.Add(-30 * time.Minute), RespondedAt: slaTime(now.Add(-25 * time.Minute))},
		// 5 critical 3 小时已响应未解决 -> 恢复超时
		{Title: "critical 恢复超时", Severity: "critical", Status: "processing",
			CreatedAt: now.Add(-3 * time.Hour), RespondedAt: slaTime(now.Add(-2 * time.Hour))},
		// 6 已关闭：不计 SLA，无论过了多久
		{Title: "已关闭", Severity: "critical", Status: "closed", CreatedAt: now.Add(-100 * time.Hour)},
		// 7 已解决：不在未完结集合里
		{Title: "已解决", Severity: "critical", Status: "resolved", CreatedAt: now.Add(-100 * time.Hour),
			RespondedAt: slaTime(now.Add(-99 * time.Hour)), ResolvedAt: slaTime(now.Add(-98 * time.Hour))},
		// 8 info 没设目标，放多久都不该被筛出来
		{Title: "info 无目标", Severity: "info", Status: "open", CreatedAt: now.Add(-100 * time.Hour)},
	}
	for i := range events {
		if err := h.DB.Create(&events[i]).Error; err != nil {
			t.Fatalf("写入事件失败: %v", err)
		}
	}

	titles := func(state string) []string {
		var list []model.Event
		q := applySLAFilter(h.DB.Model(&model.Event{}), state, targets, now)
		if err := q.Order("id asc").Find(&list).Error; err != nil {
			t.Fatalf("按 %s 查询失败: %v", state, err)
		}
		out := make([]string, 0, len(list))
		for _, item := range list {
			out = append(out, item.Title)
		}
		return out
	}

	got := titles("breached")
	want := []string{"critical 超时", "critical 恢复超时"}
	if !sameStrings(got, want) {
		t.Fatalf("超时筛选结果不对：得到 %v，期望 %v", got, want)
	}

	got = titles("risk")
	want = []string{"critical 临期"}
	if !sameStrings(got, want) {
		t.Fatalf("临期筛选结果不对：得到 %v，期望 %v", got, want)
	}

	// 逐条核对 SQL 与纯函数的判断一致：两边算出来不一样说明 SQL 翻错了
	var all []model.Event
	h.DB.Order("id asc").Find(&all)
	breached := map[string]bool{}
	for _, title := range titles("breached") {
		breached[title] = true
	}
	for _, event := range all {
		respond, recoverClock := eventSLA(event, targets, now)
		wantBreach := event.Status != "resolved" &&
			(respond.State == "breached" || recoverClock.State == "breached")
		if breached[event.Title] != wantBreach {
			t.Errorf("%s：SQL 说超时=%v，纯函数说 %s/%s", event.Title,
				breached[event.Title], respond.State, recoverClock.State)
		}
	}
}

func TestApplySLAFilterWithNoTargetsReturnsNothing(t *testing.T) {
	h := newSLATestHandler(t)
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.Local)
	if err := h.DB.Create(&model.Event{Title: "放了很久", Severity: "critical",
		Status: "open", CreatedAt: now.Add(-1000 * time.Hour)}).Error; err != nil {
		t.Fatalf("写入事件失败: %v", err)
	}
	empty := slaTargets{Respond: map[string]int{}, Recover: map[string]int{}, RemindBefore: 5}

	var n int64
	applySLAFilter(h.DB.Model(&model.Event{}), "breached", empty, now).Count(&n)
	if n != 0 {
		// 一条目标都没设却筛出东西，等于凭空造违约
		t.Fatalf("没有任何目标时应筛出 0 条，实际 %d", n)
	}
}

func TestSLAConfigMinutesAllowsExplicitZero(t *testing.T) {
	h := newSLATestHandler(t)
	// 0 是有意义的取值（这一级不设 SLA），不能被默认值顶回来
	if err := h.DB.Create(&model.SysConfig{Group: "monitor",
		Key: CfgSLARespondCritical, Value: "0", Type: "int"}).Error; err != nil {
		t.Fatalf("写入配置失败: %v", err)
	}
	if got := h.slaConfigMinutes(CfgSLARespondCritical); got != 0 {
		t.Fatalf("显式填 0 应读出 0，实际 %d", got)
	}
	// 没有这一行时回落到内置默认
	if got := h.slaConfigMinutes(CfgSLARespondWarning); got != slaDefaults[CfgSLARespondWarning] {
		t.Fatalf("缺失时应回落默认 %d，实际 %d", slaDefaults[CfgSLARespondWarning], got)
	}
	// 明显不合法的值（负数、超上限、非数字）也回落默认，而不是把 SLA 算崩
	for _, bad := range []string{"-5", "99999999", "abc"} {
		h.DB.Model(&model.SysConfig{}).Where("`key` = ?", CfgSLARespondCritical).
			Update("value", bad)
		if got := h.slaConfigMinutes(CfgSLARespondCritical); got != slaDefaults[CfgSLARespondCritical] {
			t.Fatalf("非法值 %q 应回落默认，实际 %d", bad, got)
		}
	}
}

func TestCheckEventSLARemindsOnceAndLeavesTrail(t *testing.T) {
	h := newSLATestHandler(t)
	if err := h.DB.Create(&model.User{Username: "ops01", Status: 1}).Error; err != nil {
		t.Fatalf("建用户失败: %v", err)
	}
	// 用默认目标（critical 响应 15 分钟），事件放了 2 小时没人接
	event := model.Event{Title: "磁盘满了", Severity: "critical", Status: "open",
		Assignee: "ops01", CreatedAt: time.Now().Add(-2 * time.Hour),
		LastActivityAt: time.Now().Add(-2 * time.Hour)}
	if err := h.DB.Create(&event).Error; err != nil {
		t.Fatalf("写入事件失败: %v", err)
	}

	h.CheckEventSLAForSchedule()

	var msgs []model.Message
	h.DB.Where("ref_id = ?", event.ID).Find(&msgs)
	// 响应与恢复各一条：两条 SLA 要找的人和要做的事不一样，不合并
	if len(msgs) != 2 {
		t.Fatalf("应发出 2 条提醒（响应 + 恢复），实际 %d 条", len(msgs))
	}
	var logs []model.EventLog
	h.DB.Where("event_id = ? AND action = ?", event.ID, "sla").Find(&logs)
	if len(logs) != 2 {
		t.Fatalf("时间线上应有 2 条 SLA 留痕，实际 %d 条", len(logs))
	}

	// 再扫一次：在重复间隔内不应该再发，否则一个没人处理的单子会刷满消息中心
	h.CheckEventSLAForSchedule()
	h.DB.Where("ref_id = ?", event.ID).Find(&msgs)
	if len(msgs) != 2 {
		t.Fatalf("重复间隔内不应再发，实际累计 %d 条", len(msgs))
	}
}

func TestCheckEventSLALeavesTrailWhenNobodyToRemind(t *testing.T) {
	h := newSLATestHandler(t)
	// 没有负责人，建单人账号也不存在：消息发不出去，但超时不能悄悄消失
	event := model.Event{Title: "没人认领", Severity: "critical", Status: "open",
		CreatedByName: "已离职的人", CreatedAt: time.Now().Add(-2 * time.Hour),
		LastActivityAt: time.Now().Add(-2 * time.Hour)}
	if err := h.DB.Create(&event).Error; err != nil {
		t.Fatalf("写入事件失败: %v", err)
	}

	h.CheckEventSLAForSchedule()

	var msgCount int64
	h.DB.Model(&model.Message{}).Count(&msgCount)
	if msgCount != 0 {
		t.Fatalf("找不到人时不该凭空发消息，实际 %d 条", msgCount)
	}
	var logs []model.EventLog
	h.DB.Where("event_id = ? AND action = ?", event.ID, "sla").Find(&logs)
	if len(logs) == 0 {
		t.Fatal("没人可提醒时必须在时间线留痕，否则这条超时彻底没人知道")
	}
	if !strings.Contains(logs[0].Content, "没有找到可提醒的人") {
		t.Fatalf("留痕要说清为什么没提醒，实际：%s", logs[0].Content)
	}
}

// ---------- 小工具 ----------

func slaTime(t time.Time) *time.Time { return &t }

func sameStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
