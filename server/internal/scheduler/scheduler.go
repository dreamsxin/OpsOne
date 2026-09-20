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
}

func New(db *gorm.DB, run RunFunc) *Scheduler {
	return &Scheduler{
		db:      db,
		cron:    cron.New(),
		run:     run,
		entries: make(map[uint]cron.EntryID),
	}
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
