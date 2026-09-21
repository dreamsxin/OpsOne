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

func baseSilence() model.AlertSilence {
	start := time.Date(2026, 9, 21, 22, 0, 0, 0, time.Local)
	return model.AlertSilence{
		ID: 1, Name: "发布窗口", Kind: silenceKindMaintenance, Enabled: true,
		MatchSeverity: "warning,critical",
		StartAt:       start, EndAt: start.Add(2 * time.Hour),
	}
}

func TestSilenceMatchesWindow(t *testing.T) {
	s := baseSilence()
	alert := model.Alert{Severity: "critical", Title: "web-api 5xx 升高", SourceName: "Prometheus"}

	cases := []struct {
		name string
		at   time.Time
		want bool
	}{
		{"窗口开始前不拦", s.StartAt.Add(-time.Minute), false},
		{"窗口起点即生效", s.StartAt, true},
		{"窗口中间生效", s.StartAt.Add(time.Hour), true},
		{"窗口结束那一刻即失效", s.EndAt, false},
		{"窗口结束后不拦", s.EndAt.Add(time.Minute), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := silenceMatches(s, alert, tc.at); got != tc.want {
				t.Fatalf("期望 %v，实际 %v", tc.want, got)
			}
		})
	}
}

func TestSilenceMatchesConditions(t *testing.T) {
	at := baseSilence().StartAt.Add(time.Minute)
	alert := model.Alert{
		Severity: "warning", Title: "磁盘 /var/log 使用率 88%", SourceName: "Prometheus 生产集群",
		Labels: `{"service":"web-api","env":"prod"}`,
	}

	cases := []struct {
		name  string
		tweak func(*model.AlertSilence)
		want  bool
	}{
		{"级别命中", func(s *model.AlertSilence) { s.MatchSeverity = "warning" }, true},
		{"级别不命中", func(s *model.AlertSilence) { s.MatchSeverity = "critical" }, false},
		{"级别为空即不限", func(s *model.AlertSilence) { s.MatchSeverity = "" }, true},
		{"来源命中", func(s *model.AlertSilence) { s.MatchSource = "Prometheus 生产集群" }, true},
		{"来源不命中", func(s *model.AlertSilence) { s.MatchSource = "Zabbix" }, false},
		{"标题关键字命中", func(s *model.AlertSilence) { s.MatchTitle = "/var/log" }, true},
		{"标题关键字不命中", func(s *model.AlertSilence) { s.MatchTitle = "内存" }, false},
		{"标签全部命中", func(s *model.AlertSilence) { s.MatchLabels = `{"env":"prod"}` }, true},
		{"标签有一项不命中就不拦", func(s *model.AlertSilence) {
			s.MatchLabels = `{"env":"prod","service":"order-db"}`
		}, false},
		{"标签条件是坏 JSON 时宁可把通知发出去", func(s *model.AlertSilence) { s.MatchLabels = "{oops" }, false},
		{"多条件同时满足", func(s *model.AlertSilence) {
			s.MatchSeverity = "warning"
			s.MatchTitle = "磁盘"
			s.MatchLabels = `{"service":"web-api"}`
		}, true},
		{"多条件中有一个不满足", func(s *model.AlertSilence) {
			s.MatchSeverity = "warning"
			s.MatchTitle = "磁盘"
			s.MatchLabels = `{"service":"order-db"}`
		}, false},
		{"匹配全部时忽略其它条件", func(s *model.AlertSilence) {
			s.MatchAll = true
			s.MatchSeverity = "info"
		}, true},
		{"停用的静默不生效", func(s *model.AlertSilence) { s.Enabled = false }, false},
		{"提前结束后不生效", func(s *model.AlertSilence) {
			ended := at.Add(-time.Second)
			s.EndedAt = &ended
		}, false},
		{"提前结束时间还没到时仍生效", func(s *model.AlertSilence) {
			ended := at.Add(time.Minute)
			s.EndedAt = &ended
		}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := baseSilence()
			s.MatchSeverity = ""
			tc.tweak(&s)
			if got := silenceMatches(s, alert, at); got != tc.want {
				t.Fatalf("期望 %v，实际 %v", tc.want, got)
			}
		})
	}
}

func TestSilenceStatus(t *testing.T) {
	s := baseSilence()
	ended := s.StartAt.Add(30 * time.Minute)

	cases := []struct {
		name  string
		tweak func(*model.AlertSilence)
		at    time.Time
		want  string
	}{
		{"未开始", func(*model.AlertSilence) {}, s.StartAt.Add(-time.Hour), "pending"},
		{"生效中", func(*model.AlertSilence) {}, s.StartAt.Add(time.Minute), "active"},
		{"已过期", func(*model.AlertSilence) {}, s.EndAt.Add(time.Minute), "expired"},
		{"已停用", func(x *model.AlertSilence) { x.Enabled = false }, s.StartAt.Add(time.Minute), "disabled"},
		{"已提前结束", func(x *model.AlertSilence) { x.EndedAt = &ended }, ended.Add(time.Minute), "ended"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			item := baseSilence()
			tc.tweak(&item)
			if got := silenceStatus(item, tc.at); got != tc.want {
				t.Fatalf("期望 %s，实际 %s", tc.want, got)
			}
		})
	}
}

func TestSilenceReqNormalize(t *testing.T) {
	ok := silenceReq{
		Name: "发布窗口", MatchSeverity: "warning",
		StartAt: "2026-09-21 22:00:00", EndAt: "2026-09-22 00:00:00",
	}

	cases := []struct {
		name    string
		tweak   func(*silenceReq)
		wantErr string
	}{
		{"合法", func(*silenceReq) {}, ""},
		{"名称必填", func(r *silenceReq) { r.Name = "  " }, "名称必填"},
		{"没有任何条件要被拒", func(r *silenceReq) { r.MatchSeverity = "" }, "至少要填一个匹配条件"},
		{"显式勾选匹配全部就放行", func(r *silenceReq) {
			r.MatchSeverity = ""
			r.MatchAll = true
		}, ""},
		{"结束早于开始", func(r *silenceReq) { r.EndAt = "2026-09-21 21:00:00" }, "结束时间必须晚于开始时间"},
		{"窗口超过 30 天", func(r *silenceReq) { r.EndAt = "2026-11-21 22:00:00" }, "最长 30 天"},
		{"时间格式非法", func(r *silenceReq) { r.StartAt = "昨天" }, "开始时间无效"},
		{"标签条件不是 JSON 对象", func(r *silenceReq) { r.MatchLabels = "[1,2]" }, "JSON 对象"},
		{"非法级别被过滤后等于没条件", func(r *silenceReq) { r.MatchSeverity = "urgent" }, "至少要填一个匹配条件"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := ok
			tc.tweak(&req)
			parsed, err := req.normalize()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("不该报错: %v", err)
				}
				if parsed.Kind != silenceKindSilence && parsed.Kind != silenceKindMaintenance {
					t.Fatalf("类型归一化失败: %s", parsed.Kind)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("期望错误包含 %q，实际 %v", tc.wantErr, err)
			}
		})
	}
}

func TestNormalizeSeverityList(t *testing.T) {
	cases := map[string]string{
		"":                          "",
		"warning":                   "warning",
		" critical , warning ":      "critical,warning",
		"urgent":                    "",
		"info,urgent,critical":      "info,critical",
		"warning,warning":           "warning,warning", // 不去重，保持用户填的顺序
		"CRITICAL":                  "",                // 大小写敏感，避免把不认识的值当成命中
		"critical,warning,info,xxx": "critical,warning,info",
	}
	for raw, want := range cases {
		if got := normalizeSeverityList(raw); got != want {
			t.Errorf("%q: 期望 %q，实际 %q", raw, want, got)
		}
	}
}

// 命中静默时：告警要被标记，静默的命中计数要自增
func TestSilencedForAlertMarksBoth(t *testing.T) {
	g, err := gorm.Open(sqlite.Open("file::memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开内存库失败: %v", err)
	}
	if err := g.AutoMigrate(&model.Alert{}, &model.AlertSilence{}); err != nil {
		t.Fatalf("建表失败: %v", err)
	}
	t.Cleanup(func() {
		if db, err := g.DB(); err == nil {
			_ = db.Close()
		}
	})
	h := New(g, &config.Config{})

	now := time.Now()
	silence := model.AlertSilence{
		Name: "夜间发布", Kind: silenceKindMaintenance, Enabled: true,
		MatchSeverity: "critical", StartAt: now.Add(-time.Minute), EndAt: now.Add(time.Hour),
	}
	if err := g.Create(&silence).Error; err != nil {
		t.Fatalf("写入静默失败: %v", err)
	}

	hot := model.Alert{Title: "web-api 5xx 升高", Severity: "critical", Status: "firing"}
	if err := g.Create(&hot).Error; err != nil {
		t.Fatalf("写入告警失败: %v", err)
	}
	if !h.silencedForAlert(&hot) {
		t.Fatal("critical 告警应当被这条静默拦下")
	}
	if hot.SilencedBy != "夜间发布" || hot.SilenceID != silence.ID {
		t.Fatalf("告警上的静默痕迹不对: by=%q id=%d", hot.SilencedBy, hot.SilenceID)
	}

	var stored model.Alert
	g.First(&stored, hot.ID)
	if stored.SilencedBy != "夜间发布" {
		t.Fatalf("库里的告警没标记: %q", stored.SilencedBy)
	}
	if stored.SuppressedBy != "" {
		t.Fatal("静默不该写 suppressed_by，那是聚合抑制在用的字段")
	}

	var after model.AlertSilence
	g.First(&after, silence.ID)
	if after.HitCount != 1 || after.LastHitAt == nil {
		t.Fatalf("命中计数没记上: count=%d lastHit=%v", after.HitCount, after.LastHitAt)
	}

	// 级别不匹配的告警不该被拦
	warn := model.Alert{Title: "磁盘告警", Severity: "warning", Status: "firing"}
	g.Create(&warn)
	if h.silencedForAlert(&warn) {
		t.Fatal("warning 告警不该被只匹配 critical 的静默拦下")
	}
	g.First(&after, silence.ID)
	if after.HitCount != 1 {
		t.Fatalf("没命中却动了计数: %d", after.HitCount)
	}
}
