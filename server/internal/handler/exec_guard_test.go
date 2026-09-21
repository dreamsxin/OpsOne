package handler

import (
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"ops-platform/server/internal/config"
	"ops-platform/server/internal/model"
)

// newGuardTestHandler 内存库 + 两条命令规则（一条拦截、一条提醒）+ 两台主机（生产 / 测试）
func newGuardTestHandler(t *testing.T) *Handler {
	t.Helper()
	g, err := gorm.Open(sqlite.Open("file::memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开内存库失败: %v", err)
	}
	if err := g.AutoMigrate(&model.CommandRule{}, &model.Host{}, &model.ExecGuardLog{}); err != nil {
		t.Fatalf("建表失败: %v", err)
	}
	t.Cleanup(func() {
		if db, err := g.DB(); err == nil {
			_ = db.Close()
		}
	})

	rules := []model.CommandRule{
		{Pattern: `rm\s+-rf\s+/`, Description: "删除根目录", Action: "block", Enabled: true},
		{Pattern: `systemctl\s+restart`, Description: "重启服务", Action: "warn", Enabled: true},
		{Pattern: `mkfs`, Description: "格式化磁盘（已停用）", Action: "block", Enabled: true},
	}
	for i := range rules {
		if err := g.Create(&rules[i]).Error; err != nil {
			t.Fatalf("写入规则失败: %v", err)
		}
	}
	// Enabled 带 default:true，Create 时传 false 会被 GORM 当零值忽略，只能建完再改
	if err := g.Model(&model.CommandRule{}).Where("pattern = ?", "mkfs").
		Update("enabled", false).Error; err != nil {
		t.Fatalf("停用规则失败: %v", err)
	}
	return New(g, &config.Config{})
}

func guardHosts() []model.Host {
	return []model.Host{
		{ID: 1, Name: "web-prod-01", Env: "prod"},
		{ID: 2, Name: "web-test-01", Env: "test"},
	}
}

// 拦截级规则命中时必须拒绝，且确认与否都不影响结论
func TestInspectExecBlocksDangerousCommand(t *testing.T) {
	h := newGuardTestHandler(t)

	for _, confirmed := range []bool{false, true} {
		decision := h.inspectExec("rm -rf /data && rm -rf /", guardHosts(), confirmed)
		if decision.Status != "blocked" {
			t.Fatalf("confirmed=%v 时应拦截，实际 %s", confirmed, decision.Status)
		}
		if blockingHit(decision.Hits) == nil {
			t.Fatal("应记下命中的拦截级规则")
		}
		if decision.Reason == "" {
			t.Fatal("拦截必须给出原因")
		}
	}
}

// 已停用的规则不参与判定
func TestInspectExecIgnoresDisabledRule(t *testing.T) {
	h := newGuardTestHandler(t)
	decision := h.inspectExec("mkfs.ext4 /dev/sdb1", []model.Host{{ID: 2, Name: "web-test-01", Env: "test"}}, false)
	if decision.Status != "pass" {
		t.Fatalf("停用规则不该拦截，实际 %s：%s", decision.Status, decision.Reason)
	}
}

// 目标含生产主机且未确认 → 拒绝；确认后放行
func TestInspectExecRequiresProdConfirm(t *testing.T) {
	h := newGuardTestHandler(t)

	blocked := h.inspectExec("uptime", guardHosts(), false)
	if blocked.Status != "blocked" {
		t.Fatalf("生产主机未确认应拒绝，实际 %s", blocked.Status)
	}
	if !blocked.NeedConfirm || len(blocked.ProdHosts) != 1 {
		t.Fatalf("应识别出 1 台生产主机，实际 %v", blocked.ProdHosts)
	}
	// 拒绝原因来自「没确认」而不是「命中规则」，这两种情况前端提示不一样
	if blockingHit(blocked.Hits) != nil {
		t.Fatal("这次拒绝不该归因到命令规则")
	}

	ok := h.inspectExec("uptime", guardHosts(), true)
	if ok.Status != "pass" {
		t.Fatalf("确认后应放行，实际 %s：%s", ok.Status, ok.Reason)
	}
}

// 非生产环境不需要确认
func TestInspectExecNonProdNeedsNoConfirm(t *testing.T) {
	h := newGuardTestHandler(t)
	decision := h.inspectExec("uptime", []model.Host{{ID: 2, Name: "web-test-01", Env: "test"}}, false)
	if decision.Status != "pass" || decision.NeedConfirm {
		t.Fatalf("测试环境应直接放行，实际 %s needConfirm=%v", decision.Status, decision.NeedConfirm)
	}
}

// 提醒级规则放行，但要在判定里留下命中记录
func TestInspectExecWarnPasses(t *testing.T) {
	h := newGuardTestHandler(t)
	decision := h.inspectExec("systemctl restart nginx", []model.Host{{ID: 2, Name: "web-test-01", Env: "test"}}, false)
	if decision.Status != "warn" {
		t.Fatalf("提醒级规则应放行并标记 warn，实际 %s", decision.Status)
	}
	if len(decision.Hits) != 1 || decision.Hits[0].Action != "warn" {
		t.Fatalf("应记下 1 条提醒级命中，实际 %v", decision.Hits)
	}
}

// 流水只记 blocked 与 warn：放行且无命中的下发本身就是一条执行记录
func TestRecordExecGuardOnlyKeepsBlockedAndWarn(t *testing.T) {
	h := newGuardTestHandler(t)
	hosts := guardHosts()
	req := ExecRequest{Command: "x", Source: "manual", UserID: 3, Operator: "ops01", ClientIP: "10.1.1.9"}

	h.recordExecGuard(req, hosts, execGuardDecision{Status: "pass"})
	var count int64
	h.DB.Model(&model.ExecGuardLog{}).Count(&count)
	if count != 0 {
		t.Fatalf("pass 不该写流水，实际 %d 条", count)
	}

	blocked := h.inspectExec("rm -rf /", hosts, true)
	h.recordExecGuard(ExecRequest{
		Command: "rm -rf /", Source: "manual", UserID: 3, Operator: "ops01", ClientIP: "10.1.1.9",
	}, hosts, blocked)

	var rows []model.ExecGuardLog
	h.DB.Find(&rows)
	if len(rows) != 1 {
		t.Fatalf("应写入 1 条流水，实际 %d 条", len(rows))
	}
	row := rows[0]
	if row.Status != "blocked" || row.Action != "block" || row.RuleID == 0 {
		t.Fatalf("流水应记下是哪条拦截规则: %+v", row)
	}
	if row.HostCount != 2 || row.ProdCount != 1 {
		t.Fatalf("流水应记下目标台数与生产台数: %+v", row)
	}
	if row.Username != "ops01" || row.ClientIP != "10.1.1.9" {
		t.Fatalf("流水应记下操作人与来源 IP: %+v", row)
	}
	if row.Command != "rm -rf /" {
		t.Fatalf("流水应记下命令原文: %q", row.Command)
	}
}

// 定时任务保存时就要拦掉拦截级命令，别等到凌晨触发
func TestGuardCronCommand(t *testing.T) {
	h := newGuardTestHandler(t)
	if err := h.guardCronCommand("rm -rf /"); err == nil {
		t.Fatal("拦截级命令应拒绝保存为定时任务")
	}
	if err := h.guardCronCommand("systemctl restart nginx"); err != nil {
		t.Fatalf("提醒级命令应允许保存，实际被拒: %v", err)
	}
}

// prodHostNames 只返回生产主机
func TestProdHostNames(t *testing.T) {
	h := newGuardTestHandler(t)
	for _, host := range guardHosts() {
		if err := h.DB.Create(&host).Error; err != nil {
			t.Fatalf("写入主机失败: %v", err)
		}
	}
	names := h.prodHostNames([]uint{1, 2})
	if len(names) != 1 || names[0] != "web-prod-01" {
		t.Fatalf("应只返回生产主机，实际 %v", names)
	}
	if len(h.prodHostNames(nil)) != 0 {
		t.Fatal("空目标应返回空列表")
	}
}
