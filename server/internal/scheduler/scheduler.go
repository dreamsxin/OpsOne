// Package scheduler 负责定时任务的注册与触发，具体执行逻辑由调用方注入。
package scheduler

import (
	"log"
	"sync"

	"github.com/robfig/cron/v3"
	"gorm.io/gorm"

	"ops-platform/server/internal/model"
)

// RunFunc 任务触发时的执行逻辑
type RunFunc func(job model.CronJob)

type Scheduler struct {
	db      *gorm.DB
	cron    *cron.Cron
	run     RunFunc
	mu      sync.Mutex
	entries map[uint]cron.EntryID
	fixed   int // 代码内置的固定任务数量
}

func New(db *gorm.DB, run RunFunc) *Scheduler {
	return &Scheduler{
		db: db,
		// SkipIfStillRunning：上一次还没跑完就跳过这一次，而不是再起一个 goroutine。
		//
		// robfig/cron 的默认行为是每次触发都新开 goroutine。一条 `* * * * *` 的任务
		// 如果实际要跑 5 分钟，就会有 5 份叠着跑 —— 表现是同一条命令在目标机器上
		// 被重复下发，而 `run_count` 还会互相覆盖。
		// 跳过是这两种错法里明显更安全的那个：漏跑一次下一分钟还有机会，
		// 重复下发一条命令可能已经把事情做坏了。跳过会打一行日志，不是静默的。
		cron:    cron.New(cron.WithChain(cron.SkipIfStillRunning(cronLogger{}))),
		run:     run,
		entries: make(map[uint]cron.EntryID),
	}
}

// cronLogger 把 cron 库内部的日志接到标准 log 上。
//
// 只实现它要的两个方法；主要是为了让 SkipIfStillRunning 的「跳过」被记下来 ——
// 一次静默的跳过和一次没触发看起来是一样的。
type cronLogger struct{}

func (cronLogger) Info(msg string, keysAndValues ...any) {
	log.Printf("[scheduler] %s %v", msg, keysAndValues)
}

func (cronLogger) Error(err error, msg string, keysAndValues ...any) {
	log.Printf("[scheduler] %s: %v %v", msg, err, keysAndValues)
}

// Start 载入所有启用中的任务并开始调度
func (s *Scheduler) Start() error {
	var jobs []model.CronJob
	if err := s.db.Where("enabled = ?", true).Find(&jobs).Error; err != nil {
		return err
	}
	for i := range jobs {
		if err := s.register(jobs[i]); err != nil {
			log.Printf("[scheduler] 任务 %d(%s) 注册失败: %v", jobs[i].ID, jobs[i].Name, err)
		}
	}
	s.cron.Start()
	log.Printf("[scheduler] 已加载 %d 个定时任务", len(s.entries))
	return nil
}

func (s *Scheduler) Stop() {
	ctx := s.cron.Stop()
	<-ctx.Done()
}

// Sync 按数据库当前状态重新注册单个任务：停用或不存在则移除
func (s *Scheduler) Sync(jobID uint) error {
	s.Remove(jobID)

	var job model.CronJob
	if err := s.db.First(&job, jobID).Error; err != nil {
		return nil // 已删除，移除即可
	}
	if !job.Enabled {
		return nil
	}
	return s.register(job)
}

// Remove 取消调度
func (s *Scheduler) Remove(jobID uint) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id, ok := s.entries[jobID]; ok {
		s.cron.Remove(id)
		delete(s.entries, jobID)
	}
}

// ValidateSpec 校验 cron 表达式，供接口层在保存前提示
func ValidateSpec(spec string) error {
	_, err := cron.ParseStandard(spec)
	return err
}

// AddFixed 注册代码内置的固定任务（如证书巡检）。
// 这类任务不落库、不出现在定时任务列表里，也不能在界面上停用，只能通过配置关闭。
func (s *Scheduler) AddFixed(spec string, fn func()) error {
	_, err := s.cron.AddFunc(spec, fn)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.fixed++
	s.mu.Unlock()
	return nil
}

// Stats 当前调度器里的条目数，供平台健康页核对「库里启用的任务是否都真的在跑」
func (s *Scheduler) Stats() (userJobs, fixedJobs, cronEntries int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.entries), s.fixed, len(s.cron.Entries())
}

func (s *Scheduler) register(job model.CronJob) error {
	id, err := s.cron.AddFunc(job.Spec, func() {
		// 每次触发都重新读库，避免用到注册时的旧配置
		var fresh model.CronJob
		if err := s.db.First(&fresh, job.ID).Error; err != nil || !fresh.Enabled {
			return
		}
		s.run(fresh)
	})
	if err != nil {
		return err
	}

	s.mu.Lock()
	s.entries[job.ID] = id
	s.mu.Unlock()
	return nil
}
