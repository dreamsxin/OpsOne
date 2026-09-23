package scheduler

// 调度器的不重入测试。
//
// robfig/cron 默认每次触发都新开 goroutine。一条 `* * * * *` 的任务如果实际要跑
// 5 分钟，就会有 5 份叠着跑 —— 表现是同一条命令在目标机器上被重复下发。
// 这里不靠等真实的分钟节拍（那样一条测试要跑一分钟），而是直接把包好的 entry
// 取出来连续触发两次。

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"ops-platform/server/internal/model"
)

func newTestScheduler(t *testing.T, run RunFunc) *Scheduler {
	t.Helper()
	g, err := gorm.Open(sqlite.Open(t.TempDir()+"/sched.db"),
		&gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	if err := g.AutoMigrate(&model.CronJob{}); err != nil {
		t.Fatalf("建表失败: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, err := g.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	return New(g, run)
}

// 上一次还没跑完时再触发要被跳过，而不是并发再跑一遍
func TestSkipIfStillRunning(t *testing.T) {
	var running, total int32
	release := make(chan struct{})
	var once sync.Once

	s := newTestScheduler(t, nil)
	if err := s.AddFixed("* * * * *", func() {
		atomic.AddInt32(&total, 1)
		atomic.AddInt32(&running, 1)
		defer atomic.AddInt32(&running, -1)
		<-release
	}); err != nil {
		t.Fatalf("注册固定任务失败: %v", err)
	}

	entries := s.cron.Entries()
	if len(entries) != 1 {
		t.Fatalf("应该只有 1 个 entry，实际 %d", len(entries))
	}
	job := entries[0].WrappedJob

	go job.Run() // 第一次：会卡在 <-release
	waitFor(t, func() bool { return atomic.LoadInt32(&running) == 1 }, "第一次触发没跑起来")

	go job.Run() // 第二次：上一次还在跑，应该被跳过
	time.Sleep(150 * time.Millisecond)
	if got := atomic.LoadInt32(&total); got != 1 {
		t.Fatalf("上一次还没跑完时不该再跑一遍，实际执行了 %d 次 —— "+
			"这会让同一条命令在目标机器上被重复下发", got)
	}

	once.Do(func() { close(release) })
	waitFor(t, func() bool { return atomic.LoadInt32(&running) == 0 }, "第一次触发没结束")

	// 前一次跑完之后，新的触发要能正常执行 —— 跳过不能变成永久卡死
	s2 := newTestScheduler(t, nil)
	var count2 int32
	if err := s2.AddFixed("* * * * *", func() {
		atomic.AddInt32(&count2, 1)
	}); err != nil {
		t.Fatalf("注册失败: %v", err)
	}
	job2 := s2.cron.Entries()[0].WrappedJob
	job2.Run()
	job2.Run()
	if got := atomic.LoadInt32(&count2); got != 2 {
		t.Fatalf("前一次结束后应该能再跑，实际 %d 次", got)
	}
}

// Stats 要能分清用户任务与内置固定任务，健康页靠它核对「库里启用的是否都在跑」
func TestStatsCountsFixedAndUserJobs(t *testing.T) {
	s := newTestScheduler(t, func(model.CronJob) {})
	s.db.Create(&model.CronJob{Name: "用户任务", Spec: "*/5 * * * *", Enabled: true, Command: "echo 1"})
	if err := s.AddFixed("0 3 * * *", func() {}); err != nil {
		t.Fatalf("注册固定任务失败: %v", err)
	}
	if err := s.Start(); err != nil {
		t.Fatalf("Start 失败: %v", err)
	}
	defer s.Stop()

	user, fixed, entries := s.Stats()
	if user != 1 || fixed != 1 || entries != 2 {
		t.Fatalf("Stats 应为 user=1 fixed=1 entries=2，实际 %d/%d/%d", user, fixed, entries)
	}
}

// 没 Start 过的调度器 Stop 不能卡死 —— standby 实例退出时走的就是这条路
func TestStopWithoutStartDoesNotHang(t *testing.T) {
	s := newTestScheduler(t, nil)
	done := make(chan struct{})
	go func() {
		s.Stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("没 Start 过就 Stop 卡住了 —— standby 实例停机时会挂在这里")
	}
}

func waitFor(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal(msg)
}
