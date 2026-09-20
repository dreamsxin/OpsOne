package handler

import (
	"testing"
	"time"

	"ops-platform/server/internal/model"
)

func TestRotationIndex(t *testing.T) {
	start := time.Date(2026, 9, 20, 10, 0, 0, 0, time.Local)

	cases := []struct {
		name     string
		rotation string
		at       time.Time
		members  int
		want     int
	}{
		{"基准时刻就是第一个", rotationDaily, start, 3, 0},
		{"当天之内不换人", rotationDaily, start.Add(23 * time.Hour), 3, 0},
		{"满一天换第二个", rotationDaily, start.Add(24 * time.Hour), 3, 1},
		{"满三天转回第一个", rotationDaily, start.Add(72 * time.Hour), 3, 0},
		{"每小时轮换", rotationHourly, start.Add(90 * time.Minute), 3, 1},
		{"每周轮换：第 6 天还没换", rotationWeekly, start.Add(6 * 24 * time.Hour), 2, 0},
		{"每周轮换：第 8 天换人", rotationWeekly, start.Add(8 * 24 * time.Hour), 2, 1},
		{"基准时间之前按第一个算", rotationDaily, start.Add(-48 * time.Hour), 3, 0},
		{"没有成员不炸", rotationDaily, start, 0, 0},
		{"只有一个人永远是他", rotationDaily, start.Add(100 * 24 * time.Hour), 1, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := rotationIndex(tc.rotation, start, tc.at, tc.members); got != tc.want {
				t.Fatalf("期望 %d，实际 %d", tc.want, got)
			}
		})
	}
}

func TestEscalationLevel(t *testing.T) {
	cases := []struct {
		name     string
		minutes  int
		ackWait  int
		maxLevel int
		want     int
	}{
		{"刚出现就叫当班", 0, 10, 3, 0},
		{"没到等待时间不升级", 9, 10, 3, 0},
		{"满一个等待周期升一级", 10, 10, 3, 1},
		{"满两个周期升两级", 25, 10, 3, 2},
		{"到顶就不再往上", 500, 10, 3, 2},
		{"maxLevel=1 表示只叫当班", 500, 10, 1, 0},
		{"等待时间配成 0 时只叫当班", 100, 0, 3, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := escalationLevel(tc.minutes, tc.ackWait, tc.maxLevel); got != tc.want {
				t.Fatalf("期望 %d，实际 %d", tc.want, got)
			}
		})
	}
}

func TestSeverityMatches(t *testing.T) {
	cases := []struct {
		match    string
		severity string
		want     bool
	}{
		{"", "info", true},
		{"", "critical", true},
		{"critical", "critical", true},
		{"critical", "warning", false},
		{"warning,critical", "warning", true},
		{" warning , critical ", "critical", true},
		{"warning,critical", "info", false},
	}
	for _, tc := range cases {
		if got := severityMatches(tc.match, tc.severity); got != tc.want {
			t.Fatalf("severityMatches(%q, %q) = %v，期望 %v", tc.match, tc.severity, got, tc.want)
		}
	}
}

func TestNextRotateAt(t *testing.T) {
	start := time.Date(2026, 9, 20, 10, 0, 0, 0, time.Local)
	schedule := model.OnCallSchedule{Rotation: rotationDaily, StartAt: start}

	// 还没开始时，下一次交接就是基准时间本身
	if got := nextRotateAt(schedule, start.Add(-time.Hour)); !got.Equal(start) {
		t.Fatalf("未开始时应返回基准时间，实际 %s", got)
	}
	// 当天 15:00，下一次交接是次日 10:00
	want := start.Add(24 * time.Hour)
	if got := nextRotateAt(schedule, start.Add(5*time.Hour)); !got.Equal(want) {
		t.Fatalf("期望 %s，实际 %s", want, got)
	}
	// 正好到交接点时，下一次是再一个周期之后
	if got := nextRotateAt(schedule, want); !got.Equal(want.Add(24 * time.Hour)) {
		t.Fatalf("交接点应指向下一个周期，实际 %s", got)
	}
}

func TestScheduleReqNormalize(t *testing.T) {
	cases := []struct {
		name string
		req  scheduleReq
		ok   bool
	}{
		{"没名字被拒", scheduleReq{Members: []uint{1}}, false},
		{"没成员被拒", scheduleReq{Name: "值班"}, false},
		{"成员重复被拒", scheduleReq{Name: "值班", Members: []uint{1, 1}}, false},
		{"升级级数超过人数被拒", scheduleReq{Name: "值班", Members: []uint{1, 2}, MaxLevel: 3}, false},
		{"非法级别被拒", scheduleReq{Name: "值班", Members: []uint{1}, MatchSeverity: "fatal"}, false},
		{"等待时间超 24 小时被拒", scheduleReq{Name: "值班", Members: []uint{1}, AckWaitMinutes: 2000}, false},
		{"时间格式非法被拒", scheduleReq{Name: "值班", Members: []uint{1}, StartAt: "2026/09/20"}, false},
		{"正常", scheduleReq{Name: "值班", Members: []uint{1, 2}, MaxLevel: 2, StartAt: "2026-09-20 10:00"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := tc.req
			_, err := req.normalize()
			if tc.ok && err != nil {
				t.Fatalf("应通过，却报错: %v", err)
			}
			if !tc.ok && err == nil {
				t.Fatal("应被拒绝，却通过了")
			}
		})
	}

	// 默认值：轮换周期与等待时间都要有兜底
	req := scheduleReq{Name: "值班", Members: []uint{1}, Rotation: "whatever"}
	if _, err := req.normalize(); err != nil {
		t.Fatalf("不该报错: %v", err)
	}
	if req.Rotation != rotationDaily {
		t.Fatalf("非法轮换周期应回落到每天，实际 %s", req.Rotation)
	}
	if req.AckWaitMinutes != 10 || req.MaxLevel != 1 {
		t.Fatalf("默认值不对: wait=%d maxLevel=%d", req.AckWaitMinutes, req.MaxLevel)
	}
}
