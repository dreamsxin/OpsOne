package handler

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"ops-platform/server/internal/config"
	"ops-platform/server/internal/model"
)

func labelJSON(kv map[string]string) string {
	raw, _ := json.Marshal(kv)
	return string(raw)
}

func stepsJSON(steps []runbookStep) string {
	raw, _ := json.Marshal(steps)
	return string(raw)
}

func TestMatchRunbookRequiresAllLabels(t *testing.T) {
	book := model.Runbook{MatchLabels: labelJSON(map[string]string{"metric": "disk", "env": "prod"})}

	// 只对上一个标签就不该推荐：写了两个条件就是「两个都得对上」
	if _, _, ok := matchRunbook(book, map[string]string{"metric": "disk"}, "磁盘满", "critical"); ok {
		t.Fatal("标签条件没有全部命中时不应推荐")
	}
	score, reasons, ok := matchRunbook(book,
		map[string]string{"metric": "disk", "env": "prod", "host": "web-1"}, "磁盘满", "critical")
	if !ok || score != 6 {
		t.Fatalf("两个标签都命中应得 6 分，实际 ok=%v score=%d", ok, score)
	}
	if len(reasons) != 2 {
		t.Fatalf("应给出 2 条理由，实际 %v", reasons)
	}
}

func TestMatchRunbookWildcardLabelValue(t *testing.T) {
	book := model.Runbook{MatchLabels: labelJSON(map[string]string{"probeId": "*"})}

	if _, _, ok := matchRunbook(book, map[string]string{"host": "web-1"}, "x", "info"); ok {
		t.Fatal("键不存在时不该命中")
	}
	_, reasons, ok := matchRunbook(book, map[string]string{"probeId": "7"}, "x", "info")
	if !ok {
		t.Fatal("键存在就该命中")
	}
	if reasons[0] != "标签 probeId 存在" {
		t.Fatalf("通配匹配的理由要说清是「存在」而不是值相等，实际 %q", reasons[0])
	}
}

func TestMatchRunbookKeywordAndSeverity(t *testing.T) {
	book := model.Runbook{MatchKeywords: "磁盘, disk ", MatchSeverity: "critical"}

	// 关键词大小写不敏感
	score, reasons, ok := matchRunbook(book, nil, "Node DISK usage high", "critical")
	if !ok || score != 3 {
		t.Fatalf("关键词 2 分 + 级别 1 分 = 3，实际 ok=%v score=%d", ok, score)
	}
	if len(reasons) != 2 {
		t.Fatalf("应给出关键词与级别两条理由，实际 %v", reasons)
	}
	if _, _, ok := matchRunbook(book, nil, "Node DISK usage high", "warning"); ok {
		t.Fatal("级别不符时不该推荐")
	}
	if _, _, ok := matchRunbook(book, nil, "内存使用率高", "critical"); ok {
		t.Fatal("关键词一个都没命中时不该推荐")
	}
}

func TestMatchRunbookGenericIsExplainedAndRanksLast(t *testing.T) {
	generic := model.Runbook{ID: 1, Name: "通用", Enabled: true}
	specific := model.Runbook{ID: 2, Name: "专用", Enabled: true,
		MatchLabels: labelJSON(map[string]string{"metric": "disk"})}

	score, reasons, ok := matchRunbook(generic, map[string]string{"metric": "disk"}, "磁盘", "critical")
	if !ok || score != 0 {
		t.Fatalf("通用剧本应命中但得 0 分，实际 ok=%v score=%d", ok, score)
	}
	if len(reasons) != 1 || reasons[0] == "" {
		t.Fatalf("通用剧本必须说明自己是兜底，实际 %v", reasons)
	}

	h := newRunbookTestHandler(t)
	h.DB.Create(&generic)
	h.DB.Create(&specific)
	h.DB.Model(&model.Runbook{}).Where("1 = 1").Update("enabled", true)

	matches := h.matchRunbooksFor(map[string]string{"metric": "disk"}, "磁盘使用率高", "critical")
	if len(matches) != 2 {
		t.Fatalf("两本都该出现，实际 %d 本", len(matches))
	}
	if matches[0].Runbook["name"] != "专用" {
		t.Fatalf("分数高的排前面，实际第一本是 %v", matches[0].Runbook["name"])
	}
}

func TestMatchRunbooksSkipsDisabled(t *testing.T) {
	h := newRunbookTestHandler(t)
	h.DB.Create(&model.Runbook{ID: 1, Name: "停用的", MatchKeywords: "磁盘"})
	h.DB.Model(&model.Runbook{}).Where("id = ?", 1).Update("enabled", false)

	if got := h.matchRunbooksFor(nil, "磁盘使用率高", "critical"); len(got) != 0 {
		t.Fatalf("停用的剧本不该被推荐，实际推了 %d 本", len(got))
	}
}

func TestRunbookCommandsKeepsStepLineNumbers(t *testing.T) {
	steps := []runbookStep{
		{Title: "看挂载点", Command: "df -hT"},
		{Title: "人工确认业务影响"}, // 没有命令，但必须占一行，否则行号对不上步骤
		{Title: "清理日志", Command: "rm -rf /var/log/old"},
	}
	got := runbookCommands(steps)
	want := "df -hT\n\nrm -rf /var/log/old"
	if got != want {
		t.Fatalf("命令拼接结果应保留空行占位，期望 %q 实际 %q", want, got)
	}
}

func TestApplyRunbookPrecheckMarksBlocked(t *testing.T) {
	h := newRunbookTestHandler(t)
	h.DB.Create(&model.CommandRule{Pattern: `rm\s+-rf\s+/\s*$`, Description: "删根目录",
		Action: "block", Enabled: true})

	book := model.Runbook{Steps: stepsJSON([]runbookStep{
		{Title: "看盘", Command: "df -hT"},
		{Title: "清空", Command: "rm -rf /"},
	})}
	hits := h.applyRunbookPrecheck(&book)

	if book.PrecheckStatus != "blocked" {
		t.Fatalf("命中拦截规则应为 blocked，实际 %q", book.PrecheckStatus)
	}
	if len(hits) != 1 || hits[0].Line != 2 {
		t.Fatalf("命中应定位到第 2 步，实际 %+v", hits)
	}
	if book.PrecheckedAt == nil {
		t.Fatal("预检时间要记下来")
	}
}

func TestApplyRunbookPrecheckPassesReadOnlyCommands(t *testing.T) {
	h := newRunbookTestHandler(t)
	h.DB.Create(&model.CommandRule{Pattern: `^\s*(shutdown|reboot)\b`, Action: "block", Enabled: true})

	book := model.Runbook{Steps: stepsJSON([]runbookStep{
		{Title: "看盘", Command: "df -hT"},
		{Title: "看进程", Command: "ps -eo pid,pcpu,cmd --sort=-pcpu | head -n 16"},
	})}
	h.applyRunbookPrecheck(&book)
	if book.PrecheckStatus != "pass" {
		t.Fatalf("只读命令应预检通过，实际 %q", book.PrecheckStatus)
	}
}

func TestRunbookReqRejectsMultilineCommand(t *testing.T) {
	req := runbookReq{Name: "x", Steps: []runbookStep{{Title: "一步", Command: "a\nb"}}}
	if err := req.normalize(); err == nil {
		t.Fatal("一步里塞多行命令应被拒，否则预检行号对不上步骤")
	}
	req2 := runbookReq{Name: "x"}
	if err := req2.normalize(); err == nil {
		t.Fatal("没有步骤的剧本应被拒")
	}
	req3 := runbookReq{Name: "x", Steps: []runbookStep{{Command: "df -h"}}}
	if err := req3.normalize(); err == nil {
		t.Fatal("步骤没有标题应被拒")
	}
}

func newRunbookTestHandler(t *testing.T) *Handler {
	t.Helper()
	dir := t.TempDir()
	g, err := gorm.Open(sqlite.Open(dir+"/runbook.db"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	if err := g.AutoMigrate(&model.Runbook{}, &model.RunbookUse{}, &model.CommandRule{},
		&model.Event{}, &model.EventLog{}, &model.Alert{}); err != nil {
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

func TestEventMatchInputMergesAlertLabels(t *testing.T) {
	h := newRunbookTestHandler(t)
	event := model.Event{Title: "磁盘告警汇总", Severity: "critical"}
	alerts := []model.Alert{
		{ID: 1, Title: "web-1 磁盘满", Labels: labelJSON(map[string]string{"metric": "disk", "host": "web-1"})},
		{ID: 2, Title: "web-2 磁盘满", Labels: labelJSON(map[string]string{"metric": "disk", "host": "web-2"})},
	}
	labels, title, severity := h.eventMatchInput(event, alerts)

	if labels["metric"] != "disk" {
		t.Fatalf("标签应取并集，实际 %v", labels)
	}
	// 同名标签取第一条告警的值：并集只是为了匹配，不假装它代表所有实例
	if labels["host"] != "web-1" {
		t.Fatalf("同名标签应保留首个值，实际 %q", labels["host"])
	}
	if severity != "critical" {
		t.Fatalf("级别取事件级别，实际 %q", severity)
	}
	for _, want := range []string{"磁盘告警汇总", "web-1 磁盘满", "web-2 磁盘满"} {
		if !strings.Contains(title, want) {
			t.Fatalf("标题应包含 %q，实际 %q", want, title)
		}
	}
}
