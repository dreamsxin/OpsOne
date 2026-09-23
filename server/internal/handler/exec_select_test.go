package handler

// 批量执行的 P1 改动测试：整体超时、取消注册表、目标 ID 留存。
//
// 这几条守的都是「事后说不清发生了什么」这类问题：
// 作业被打断却记成 finished、目标主机只能从结果行反推、无人能停掉别人的下发。

import (
	"context"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"ops-platform/server/internal/config"
	"ops-platform/server/internal/model"
)

func newExecTestHandler(t *testing.T) *Handler {
	t.Helper()
	g, err := gorm.Open(sqlite.Open(t.TempDir()+"/exec.db"),
		&gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	if err := g.AutoMigrate(&model.SysConfig{}, &model.Host{}, &model.ExecJob{}, &model.ExecResult{}); err != nil {
		t.Fatalf("建表失败: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, err := g.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	// 必须给 Cfg：SSH 目标构造会读它（生产路径上 Cfg 永远非空）
	h := New(g, &config.Config{})
	return h
}

// 整体超时必须按台数放大，否则 200 台 × 600 秒会被一个按单台算的上限提前砍掉
func TestOverallTimeoutScalesWithHostCount(t *testing.T) {
	h := newExecTestHandler(t)
	// 没有配置项时用缺省并发 10
	if got := h.overallTimeout(10, 60); got != time.Duration(60+60)*time.Second {
		t.Fatalf("10 台 1 轮：应该是 60+60 秒，实际 %v", got)
	}
	if got := h.overallTimeout(25, 60); got != time.Duration(3*60+60)*time.Second {
		t.Fatalf("25 台 3 轮：应该是 180+60 秒，实际 %v", got)
	}
	// 上限卡住：否则一个同步 HTTP 请求能挂几小时，反向代理会先断而作业还在跑
	if got := h.overallTimeout(5000, 600); got != execOverallCap {
		t.Fatalf("超大作业应该被 %v 卡住，实际 %v", execOverallCap, got)
	}
	// 单台也要有余量，不能刚好等于单台超时（建连与落库还要时间）
	if got := h.overallTimeout(1, 10); got <= 10*time.Second {
		t.Fatalf("单台也要留固定开销余量，实际 %v", got)
	}
}

func TestExecCancelRegistry(t *testing.T) {
	reg := newExecCancelRegistry()
	ctx, cancel := context.WithCancel(context.Background())
	reg.add(7, cancel)

	if reg.cancel(9) {
		t.Fatal("不存在的作业不该报告取消成功 —— 假装成功会让人以为命令已经停了")
	}
	if !reg.cancel(7) {
		t.Fatal("已登记的作业应该能取消")
	}
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("取消函数没被调用")
	}
	if reg.cancel(7) {
		t.Fatal("取消过一次之后不该还能再取消（登记应该被清掉）")
	}

	// remove 之后也不能再取消
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	reg.add(8, cancel2)
	reg.remove(8)
	if reg.cancel(8) {
		t.Fatal("已经 remove 的作业不该还能取消")
	}
	if ctx2.Err() != nil {
		t.Fatal("remove 不该顺手把 ctx 取消掉")
	}
}

func TestTargetIDsRoundTrip(t *testing.T) {
	ids := []uint{3, 1, 2}
	raw := uintsJSON(ids)
	back := parseUintsJSON(raw)
	if len(back) != 3 || back[0] != 3 || back[2] != 2 {
		t.Fatalf("目标 ID 应该原样存取（含顺序），实际 %v ← %q", back, raw)
	}
	if got := parseUintsJSON(""); got != nil {
		t.Fatalf("空串应该解析成 nil，实际 %v", got)
	}
	if got := parseUintsJSON("不是 JSON"); len(got) != 0 {
		t.Fatalf("坏数据应该解析成空而不是 panic，实际 %v", got)
	}
}

// 被取消的作业必须记成 canceled 而不是 finished：
// 后面有人按这条记录判断「命令已经全部执行过」
func TestCanceledJobIsNotRecordedAsFinished(t *testing.T) {
	h := newExecTestHandler(t)
	h.DB.Create(&model.Host{ID: 1, Name: "h1", Address: "10.0.0.1", Env: "dev", Status: "online"})

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 一上来就取消，fanOut 里的 SSH 会立刻以失败收尾

	job, err := h.RunOnHosts(ctx, ExecRequest{
		Command: "echo hi", Timeout: 5, HostIDs: []uint{1},
		Operator: "tester", Source: "manual",
	})
	if err != nil {
		t.Fatalf("RunOnHosts 不该直接报错（作业记录要留下来）: %v", err)
	}
	if job.Status != "canceled" {
		t.Fatalf("被取消的作业状态应该是 canceled，实际 %s", job.Status)
	}
	if job.CanceledBy == "" {
		t.Fatal("要写明是被取消/超时的，否则记录上看不出发生过什么")
	}

	var saved model.ExecJob
	h.DB.First(&saved, job.ID)
	if saved.Status != "canceled" {
		t.Fatalf("库里也要是 canceled，实际 %s", saved.Status)
	}
	if saved.TargetIDs != "[1]" {
		t.Fatalf("目标主机 ID 要落库，实际 %q", saved.TargetIDs)
	}
}
