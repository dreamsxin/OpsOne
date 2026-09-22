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

func newReviewTestHandler(t *testing.T) *Handler {
	t.Helper()
	dir := t.TempDir()
	g, err := gorm.Open(sqlite.Open(dir+"/review.db"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	if err := g.AutoMigrate(
		&model.Event{}, &model.EventLog{}, &model.EventReview{}, &model.EventActionItem{},
		&model.Alert{}, &model.User{}, &model.Message{},
	); err != nil {
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

func TestSuggestMilestonesUsesEarliestAlertAndRealAck(t *testing.T) {
	h := newReviewTestHandler(t)
	base := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	ack := base.Add(12 * time.Minute)

	early := model.Alert{Title: "早的", FirstSeenAt: base}
	late := model.Alert{Title: "晚的", FirstSeenAt: base.Add(5 * time.Minute), AckBy: "zhang", AckAt: &ack}
	h.DB.Create(&early)
	h.DB.Create(&late)

	resolved := base.Add(40 * time.Minute)
	event := model.Event{Title: "磁盘满", Status: "resolved", CreatedAt: base.Add(20 * time.Minute),
		ResolvedAt: &resolved, ResolvedBy: "li"}

	hints := h.suggestMilestones(event, []model.Alert{late, early})

	if !hints["detectedAt"].At.Equal(base) {
		t.Fatalf("发现时间应取最早告警 %v，实际 %v", base, hints["detectedAt"].At)
	}
	// 故障开始与被发现同值，但必须说明这是「推不出来所以按告警填」
	if !hints["happenedAt"].At.Equal(base) {
		t.Fatalf("故障开始应默认跟最早告警一致，实际 %v", hints["happenedAt"].At)
	}
	if hints["happenedAt"].Source == hints["detectedAt"].Source {
		t.Fatal("故障开始的来源说明必须区别于被发现，否则用户不知道要往前改")
	}
	if !hints["respondedAt"].At.Equal(ack) || hints["respondedAt"].Source == "" {
		t.Fatalf("响应时间应取真实确认时间 %v，实际 %v(%s)", ack, hints["respondedAt"].At, hints["respondedAt"].Source)
	}
	if !hints["recoveredAt"].At.Equal(resolved) {
		t.Fatalf("恢复时间应取事件解决时间 %v，实际 %v", resolved, hints["recoveredAt"].At)
	}
	if hints["mitigatedAt"].At != nil {
		t.Fatal("止血时间平台推不出来，不能瞎给一个值")
	}
}

func TestSuggestMilestonesFallsBackWhenNoAck(t *testing.T) {
	h := newReviewTestHandler(t)
	base := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	assigned := base.Add(8 * time.Minute)
	handled := base.Add(20 * time.Minute)
	event := model.Event{Title: "无确认记录", Status: "processing", CreatedAt: base,
		Assignee: "zhang", AssignedAt: &assigned, RespondedAt: &handled}

	hints := h.suggestMilestones(event, nil)

	// 与 SLA 同口径：指派不算响应。这里必须退到事件真正被处理的时间（20 分钟），
	// 不能退到指派时间（8 分钟），否则复盘算出来的 MTTA 会比 SLA 宽一截。
	if !hints["respondedAt"].At.Equal(handled) {
		t.Fatalf("没有告警确认时应退到事件被处理的时间 %v，实际 %v（%s）",
			handled, hints["respondedAt"].At, hints["respondedAt"].Source)
	}
	if hints["recoveredAt"].At != nil {
		t.Fatal("事件没解决时不能编一个恢复时间")
	}
	if hints["happenedAt"].Source == "" || hints["detectedAt"].Source == "" {
		t.Fatal("没有告警时也要说明建议值是哪来的")
	}
}

func TestSuggestMilestonesNeverTreatsAssignAsRespond(t *testing.T) {
	h := newReviewTestHandler(t)
	base := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	assigned := base.Add(8 * time.Minute)
	// 只指派了、没人动手：响应时间只能退到建单时间，并如实说明没人动过手
	event := model.Event{Title: "只指派没人动", Status: "open", CreatedAt: base,
		Assignee: "zhang", AssignedAt: &assigned}

	hints := h.suggestMilestones(event, nil)
	if hints["respondedAt"].At.Equal(assigned) {
		t.Fatal("指派时间不能当成响应时间，否则来回转单就能刷出好看的 MTTA")
	}
	if !hints["respondedAt"].At.Equal(base) {
		t.Fatalf("应退到建单时间 %v，实际 %v", base, hints["respondedAt"].At)
	}
	if !strings.Contains(hints["respondedAt"].Source, "没人在这张单上动过手") {
		t.Fatalf("来源说明要讲清这是兜底值，实际：%s", hints["respondedAt"].Source)
	}
}

func TestReviewDurationsLeaveGapsEmpty(t *testing.T) {
	base := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	detected := base.Add(3 * time.Minute)
	responded := base.Add(9 * time.Minute)
	recovered := base.Add(45 * time.Minute)

	d := reviewDurations(model.EventReview{
		HappenedAt: &base, DetectedAt: &detected, RespondedAt: &responded, RecoveredAt: &recovered,
	})

	if d["detectMinutes"] != int64(3) {
		t.Fatalf("发现耗时应为 3，实际 %v", d["detectMinutes"])
	}
	if d["ackMinutes"] != int64(6) {
		t.Fatalf("响应耗时应为 6，实际 %v", d["ackMinutes"])
	}
	if d["recoverMinutes"] != int64(45) {
		t.Fatalf("总恢复时长应为 45，实际 %v", d["recoverMinutes"])
	}
	// 没填止血时间就该留空，不能用 0 冒充
	if d["mitigateMinutes"] != nil {
		t.Fatalf("止血耗时缺少输入时应为 nil，实际 %v", d["mitigateMinutes"])
	}
}

func TestReviewDurationsIgnoreReversedTimes(t *testing.T) {
	later := time.Date(2026, 3, 1, 11, 0, 0, 0, time.UTC)
	earlier := later.Add(-30 * time.Minute)
	// 人把恢复时间填得比故障开始还早时，不能算出负数指标
	d := reviewDurations(model.EventReview{HappenedAt: &later, RecoveredAt: &earlier})
	if d["recoverMinutes"] != nil {
		t.Fatalf("时间顺序颠倒时应留空，实际 %v", d["recoverMinutes"])
	}
}

func TestEnsureReviewDraftIsIdempotent(t *testing.T) {
	h := newReviewTestHandler(t)
	now := time.Now()
	event := model.Event{Title: "事件", Status: "resolved", Assignee: "zhang",
		CreatedAt: now.Add(-time.Hour), ResolvedAt: &now, ResolvedBy: "li"}
	h.DB.Create(&event)
	h.DB.Create(&model.User{Username: "zhang", Status: 1})

	if !h.ensureReviewDraft(event, "li") {
		t.Fatal("第一次应该建出草稿")
	}
	if h.ensureReviewDraft(event, "li") {
		t.Fatal("已有复盘时不该重复建")
	}

	var count int64
	h.DB.Model(&model.EventReview{}).Where("event_id = ?", event.ID).Count(&count)
	if count != 1 {
		t.Fatalf("复盘应该只有 1 份，实际 %d", count)
	}

	var review model.EventReview
	h.DB.Where("event_id = ?", event.ID).First(&review)
	if review.Owner != "zhang" {
		t.Fatalf("复盘负责人应默认取事件负责人，实际 %q", review.Owner)
	}
	if review.RecoveredAt == nil {
		t.Fatal("草稿应该预填恢复时间")
	}

	// 通知与时间线都要留痕，否则「自动建草稿」对用户是隐形的
	var msgCount, logCount int64
	h.DB.Model(&model.Message{}).Where("type = ?", "review").Count(&msgCount)
	if msgCount != 1 {
		t.Fatalf("应给复盘人发 1 条站内消息，实际 %d", msgCount)
	}
	h.DB.Model(&model.EventLog{}).Where("event_id = ? AND action = ?", event.ID, "review").Count(&logCount)
	if logCount != 1 {
		t.Fatalf("事件时间线应记 1 条，实际 %d", logCount)
	}
}

func TestEnsureReviewDraftFallsBackToOperatorAsOwner(t *testing.T) {
	h := newReviewTestHandler(t)
	now := time.Now()
	event := model.Event{Title: "没人认领的事件", Status: "resolved", CreatedAt: now.Add(-time.Hour), ResolvedAt: &now}
	h.DB.Create(&event)

	h.ensureReviewDraft(event, "li")

	var review model.EventReview
	h.DB.Where("event_id = ?", event.ID).First(&review)
	if review.Owner != "li" {
		t.Fatalf("事件没有负责人时应落到操作人，实际 %q", review.Owner)
	}
}

func TestRemindOverdueActionItemsMergesPerOwnerAndSkipsWithin24h(t *testing.T) {
	h := newReviewTestHandler(t)
	h.DB.Create(&model.User{Username: "zhang", Status: 1})
	past := time.Now().Add(-48 * time.Hour)

	h.DB.Create(&model.EventActionItem{ReviewID: 1, EventID: 1, Title: "项一", Owner: "zhang",
		DueDate: &past, Status: "open"})
	h.DB.Create(&model.EventActionItem{ReviewID: 1, EventID: 1, Title: "项二", Owner: "zhang",
		DueDate: &past, Status: "doing"})
	// 已完成与未设截止的都不该被催
	h.DB.Create(&model.EventActionItem{ReviewID: 1, EventID: 1, Title: "已完成", Owner: "zhang",
		DueDate: &past, Status: "done"})
	h.DB.Create(&model.EventActionItem{ReviewID: 1, EventID: 1, Title: "无截止", Owner: "zhang", Status: "open"})

	h.RemindOverdueActionItems()

	var msgs []model.Message
	h.DB.Find(&msgs)
	if len(msgs) != 1 {
		t.Fatalf("同一个人的多条逾期项应合成 1 条消息，实际 %d 条", len(msgs))
	}
	if msgs[0].Title != "有 2 条复盘改进项已逾期" {
		t.Fatalf("消息标题应报出逾期条数，实际 %q", msgs[0].Title)
	}

	// 再跑一次：24 小时内不应重复催办
	h.RemindOverdueActionItems()
	var again int64
	h.DB.Model(&model.Message{}).Count(&again)
	if again != 1 {
		t.Fatalf("24 小时内不该重复催办，消息数变成 %d", again)
	}

	var items []model.EventActionItem
	h.DB.Where("status IN ?", []string{"open", "doing"}).Where("due_date IS NOT NULL").Find(&items)
	for _, item := range items {
		if item.LastRemindAt == nil {
			t.Fatalf("被催办过的改进项应记下 lastRemindAt: %s", item.Title)
		}
	}
}

func TestRemindOverdueActionItemsSurvivesMissingOwner(t *testing.T) {
	h := newReviewTestHandler(t)
	past := time.Now().Add(-24 * time.Hour)
	// 负责人账号已被删：不能 panic，也不能悄悄把这条当成催过了
	h.DB.Create(&model.EventActionItem{ReviewID: 1, EventID: 1, Title: "孤儿项", Owner: "ghost",
		DueDate: &past, Status: "open"})

	h.RemindOverdueActionItems()

	var item model.EventActionItem
	h.DB.First(&item)
	if item.LastRemindAt != nil {
		t.Fatal("没催到人的改进项不该被打上 lastRemindAt，否则下次也不会再催")
	}
}
