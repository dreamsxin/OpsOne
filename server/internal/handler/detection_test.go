package handler

import (
	"testing"
	"time"

	"ops-platform/server/internal/model"
)

func alertAt(id uint, title, severity, source, labels string, minute int) model.Alert {
	base := time.Date(2026, 9, 20, 10, 0, 0, 0, time.Local)
	return model.Alert{
		ID: id, Title: title, Severity: severity, SourceName: source,
		Labels: labels, Status: "firing",
		FirstSeenAt: base.Add(time.Duration(minute) * time.Minute),
		LastSeenAt:  base.Add(time.Duration(minute) * time.Minute),
	}
}

func TestStepMatches(t *testing.T) {
	alert := alertAt(1, "主机离线：web-01", "critical", "主机探测", `{"host":"web-01","env":"prod"}`, 0)
	alert.Summary = "连续 3 次探测失败"

	cases := []struct {
		name string
		step detectionStep
		want bool
	}{
		{"标题关键字忽略大小写", detectionStep{TitleKeyword: "WEB-01"}, true},
		{"关键字也匹配摘要", detectionStep{TitleKeyword: "探测失败"}, true},
		{"关键字不存在", detectionStep{TitleKeyword: "磁盘"}, false},
		{"接入源精确匹配", detectionStep{Source: "主机探测"}, true},
		{"接入源不符", detectionStep{Source: "告警规则"}, false},
		{"级别匹配", detectionStep{Severity: "critical"}, true},
		{"级别不符", detectionStep{Severity: "warning"}, false},
		{"只要求标签存在", detectionStep{LabelKey: "host"}, true},
		{"标签值匹配", detectionStep{LabelKey: "env", LabelValue: "prod"}, true},
		{"标签值不符", detectionStep{LabelKey: "env", LabelValue: "dev"}, false},
		{"标签不存在", detectionStep{LabelKey: "cluster"}, false},
		{"多个条件是与关系", detectionStep{TitleKeyword: "离线", Severity: "warning"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := stepMatches(tc.step, alert); got != tc.want {
				t.Fatalf("期望 %v，实际 %v", tc.want, got)
			}
		})
	}
}

func TestDetectConcurrent(t *testing.T) {
	steps := []detectionStep{{TitleKeyword: "构建失败"}, {TitleKeyword: "主机离线"}}

	both := []model.Alert{
		alertAt(1, "构建失败：order-api", "warning", "构建发布", "", 0),
		alertAt(2, "主机离线：web-01", "critical", "主机探测", "", 3),
	}
	if out := detect(detectConcurrent, "", steps, both); !out.Hit {
		t.Fatalf("两步都出现应该命中，实际未命中：%s", out.Detail)
	}

	onlyOne := both[:1]
	out := detect(detectConcurrent, "", steps, onlyOne)
	if out.Hit {
		t.Fatal("只出现一步不该命中")
	}
	if out.Steps[1].Hit {
		t.Fatal("第二步不该被标记命中")
	}

	// 并发窗不看先后：把时间倒过来仍然命中
	reversed := []model.Alert{
		alertAt(1, "主机离线：web-01", "critical", "主机探测", "", 0),
		alertAt(2, "构建失败：order-api", "warning", "构建发布", "", 5),
	}
	if out := detect(detectConcurrent, "", steps, reversed); !out.Hit {
		t.Fatal("并发窗不该受顺序影响")
	}
}

func TestDetectSequence(t *testing.T) {
	steps := []detectionStep{{TitleKeyword: "构建失败"}, {TitleKeyword: "主机离线"}}

	inOrder := []model.Alert{
		alertAt(1, "构建失败：order-api", "warning", "构建发布", "", 0),
		alertAt(2, "主机离线：web-01", "critical", "主机探测", "", 5),
	}
	if out := detect(detectSequence, "", steps, inOrder); !out.Hit {
		t.Fatalf("先构建失败后主机离线应命中：%s", out.Detail)
	}

	// 顺序颠倒：只有「离线在前、构建失败在后」，链子接不上
	outOfOrder := []model.Alert{
		alertAt(1, "主机离线：web-01", "critical", "主机探测", "", 0),
		alertAt(2, "构建失败：order-api", "warning", "构建发布", "", 5),
	}
	out := detect(detectSequence, "", steps, outOfOrder)
	if out.Hit {
		t.Fatalf("顺序不对不该命中，detail=%s", out.Detail)
	}

	// 同一条告警不能把两步都顶掉
	sameAlert := []model.Alert{
		alertAt(1, "构建失败后主机离线", "warning", "混合", "", 0),
	}
	if out := detect(detectSequence, "", steps, sameAlert); out.Hit {
		t.Fatal("一条告警同时满足两步时不该算完整顺序链")
	}
}

func TestDetectJoin(t *testing.T) {
	steps := []detectionStep{{TitleKeyword: "磁盘"}, {TitleKeyword: "离线"}}

	// 两步都发生在 web-01 上
	sameHost := []model.Alert{
		alertAt(1, "磁盘使用率过高", "warning", "规则", `{"host":"web-01"}`, 0),
		alertAt(2, "主机离线", "critical", "探测", `{"host":"web-01"}`, 2),
	}
	out := detect(detectJoin, "host", steps, sameHost)
	if !out.Hit {
		t.Fatalf("同一台机器上两步都出现应命中：%s", out.Detail)
	}
	if len(out.JoinValues) != 1 || out.JoinValues[0] != "web-01" {
		t.Fatalf("命中对象应为 web-01，实际 %v", out.JoinValues)
	}

	// 分别发生在不同机器上：并发窗会命中，但 Join 不该命中
	differentHosts := []model.Alert{
		alertAt(1, "磁盘使用率过高", "warning", "规则", `{"host":"web-01"}`, 0),
		alertAt(2, "主机离线", "critical", "探测", `{"host":"db-01"}`, 2),
	}
	if out := detect(detectConcurrent, "", steps, differentHosts); !out.Hit {
		t.Fatal("并发窗在不同机器上也应命中（对照用）")
	}
	if out := detect(detectJoin, "host", steps, differentHosts); out.Hit {
		t.Fatal("不同机器不该通过窗口 Join")
	}

	// 没指定对齐标签
	if out := detect(detectJoin, "", steps, sameHost); out.Hit {
		t.Fatal("未指定对齐标签时不该命中")
	}
}

func TestDetectionRuleReqNormalize(t *testing.T) {
	two := []detectionStep{{TitleKeyword: "a"}, {TitleKeyword: "b"}}

	cases := []struct {
		name string
		req  detectionRuleReq
		ok   bool
	}{
		{"少于两步被拒", detectionRuleReq{Steps: []detectionStep{{TitleKeyword: "a"}}}, false},
		{"步骤无条件被拒", detectionRuleReq{Steps: []detectionStep{{TitleKeyword: "a"}, {}}}, false},
		{"join 缺对齐标签被拒", detectionRuleReq{Mode: detectJoin, Steps: two}, false},
		{"窗口超过 24 小时被拒", detectionRuleReq{Steps: two, WindowMinutes: 24*60 + 1}, false},
		{"正常并发窗", detectionRuleReq{Steps: two}, true},
		{"正常 join", detectionRuleReq{Mode: detectJoin, JoinLabel: "host", Steps: two}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := tc.req
			err := req.normalize()
			if tc.ok && err != nil {
				t.Fatalf("应通过校验，却报错: %v", err)
			}
			if !tc.ok && err == nil {
				t.Fatal("应被拒绝，却通过了")
			}
		})
	}

	// 默认值：模式与窗口都要有兜底
	req := detectionRuleReq{Steps: two, Mode: "whatever"}
	if err := req.normalize(); err != nil {
		t.Fatalf("不该报错: %v", err)
	}
	if req.Mode != detectConcurrent {
		t.Fatalf("非法模式应回落到并发窗，实际 %s", req.Mode)
	}
	if req.WindowMinutes != 30 {
		t.Fatalf("窗口默认应为 30 分钟，实际 %d", req.WindowMinutes)
	}
}
