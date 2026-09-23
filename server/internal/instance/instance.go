// Package instance 实例注册与调度选主。
//
// # 这一轮修的是什么
//
// 在它之前平台**完全不知道自己有没有被开两份**。没有 pid 文件、没有 flock、
// 没有任何实例登记 —— 两个进程连同一个库时谁都不会报错，然后：
//
//   - 20 个内置定时任务与全部用户定时任务**各跑两遍**。数据留存删两次、备份写两份、
//     主机指标 SSH 两轮；而用户定时任务是**在生产机器上执行命令**，跑两遍是真事故。
//   - 转发隧道与扫码 ticket 是进程内状态，谁接到请求谁认账，另一半必然报「已失效」；
//     更糟的是 `CloseKubeForward` 在内存里查不到隧道就把库里的记录标成已关闭 ——
//     会误杀另一个实例正在用的隧道。
//   - SQLite 并发写直接 `database is locked`。
//
// # 语义：默认拒绝，显式才允许
//
//   - 发现还有别的实例心跳是新鲜的 → **默认拒绝启动**，日志里列出对方的
//     主机名 / PID / 版本 / 心跳时间，并说清为什么不能这么跑。
//   - 设了 `OPS_ALLOW_MULTI_INSTANCE=true` → 以 **standby** 起来：照常提供 HTTP，
//     但**一个调度任务都不注册**。用途是升级期间的短暂重叠，或者一个只用来看页面的备用实例。
//   - 心跳过期的实例视为已死，standby 会接过 leader。这是真的接管：
//     leader 被 `kill -9` 之后，standby 在一个租约周期内开始跑调度。
//
// # 明确不是高可用
//
// 这一轮**没有**做 HA：前端流量不会自动切、终端与隧道不会迁移、
// SQLite 仍然是一台机器上的文件。做的是把「悄悄跑两遍」变成
// 「要么明确拒绝，要么明确只有一个在跑调度」。差别不在功能多少，
// 而在于前者会在凌晨把一批命令下发两遍且没人知道。
package instance

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"sync"
	"time"

	"gorm.io/gorm"

	"ops-platform/server/internal/model"
)

const (
	// RoleLeader 跑调度的那一个
	RoleLeader = "leader"
	// RoleStandby 只提供 HTTP，不跑任何调度
	RoleStandby = "standby"

	// DefaultLeaseSeconds 心跳超过这个秒数没更新就认为实例死了。
	//
	// 取 45 秒是权衡：太短会让一次 GC 停顿或者磁盘卡顿被误判成宕机，
	// 于是两个实例同时认为自己是 leader（那就回到了「跑两遍」）；
	// 太长则 leader 真的挂掉后 standby 要等很久才接管。
	DefaultLeaseSeconds = 45
)

// heartbeatInterval 心跳间隔：租约的三分之一 —— 丢两次心跳还有一次机会。
//
// **必须跟着实际租约算**，不能写成按默认租约算的常量：把租约调到 15 秒而心跳
// 还是每 15 秒一次的话，实例会在自己的租约过期边缘上反复被判死，
// 另一个实例于是抢走 leader —— 一个把配置项变成地雷的经典错法。
func heartbeatInterval(lease int) time.Duration {
	interval := time.Duration(lease/3) * time.Second
	if interval < time.Second {
		interval = time.Second
	}
	return interval
}

// Manager 管理本进程在实例表里的那一行
type Manager struct {
	db   *gorm.DB
	self model.PlatformInstance

	mu       sync.Mutex
	role     string
	stopCh   chan struct{}
	stopOnce sync.Once
	// onPromote standby 接过 leader 时回调（启动调度）
	onPromote func()
}

// Options 启动参数
type Options struct {
	Addr    string
	Version string
	// AllowMulti 允许第二个实例以 standby 起来（OPS_ALLOW_MULTI_INSTANCE）
	AllowMulti bool
	// LeaseSeconds 0 时用 DefaultLeaseSeconds
	LeaseSeconds int
}

// Claim 登记本实例并决定角色。
//
// 返回的 Manager 必须在退出时 Release，否则那行记录会留着直到租约过期 ——
// 那期间重启会被自己的上一条记录挡住。
func Claim(db *gorm.DB, opts Options) (*Manager, error) {
	lease := opts.LeaseSeconds
	if lease <= 0 {
		lease = DefaultLeaseSeconds
	}
	id, err := randomID()
	if err != nil {
		return nil, fmt.Errorf("生成实例标识失败: %w", err)
	}
	host, _ := os.Hostname()
	now := time.Now()

	m := &Manager{
		db: db,
		self: model.PlatformInstance{
			ID: id, Role: RoleStandby, Hostname: host, PID: os.Getpid(),
			Addr: opts.Addr, Version: opts.Version, LeaseSeconds: lease,
			StartedAt: now, HeartbeatAt: now,
		},
		stopCh: make(chan struct{}),
	}

	alive, err := m.aliveOthers(lease)
	if err != nil {
		return nil, err
	}
	if len(alive) > 0 && !opts.AllowMulti {
		return nil, fmt.Errorf("同一个库上已经有实例在跑：%s\n"+
			"两个实例连同一个 SQLite 文件不是「慢一点」而是会出事："+
			"内置与用户定时任务各跑两遍（数据留存删两次、备份写两份、"+
			"**用户定时任务会在生产机器上把命令执行两遍**）；"+
			"转发隧道与扫码登录是进程内状态，一半请求会报「已失效」；"+
			"SQLite 并发写还会 database is locked。\n"+
			"确实要短暂双开（比如升级时重叠几秒），设 OPS_ALLOW_MULTI_INSTANCE=true —— "+
			"那样第二个实例以 standby 起来，**不跑任何调度**。\n"+
			"如果上一个实例已经不在了，等 %d 秒租约过期再起，或者直接删掉 platform_instances 里那一行",
			describe(alive), lease)
	}

	m.role = RoleLeader
	if len(alive) > 0 {
		m.role = RoleStandby
	}
	m.self.Role = m.role
	if err := db.Create(&m.self).Error; err != nil {
		return nil, fmt.Errorf("登记实例失败: %w", err)
	}

	switch m.role {
	case RoleLeader:
		log.Printf("[instance] 本实例 %s 是 leader，定时任务在这里跑", m.self.ID)
	default:
		log.Printf("[instance] 本实例 %s 是 standby：只提供 HTTP，**不跑任何定时任务**。"+
			"已有 leader：%s。leader 心跳超过 %d 秒没更新时本实例会接管",
			m.self.ID, describe(alive), lease)
	}
	return m, nil
}

// Start 开始心跳。onPromote 在 standby 接过 leader 时被调用（用来启动调度）。
func (m *Manager) Start(onPromote func()) {
	m.mu.Lock()
	m.onPromote = onPromote
	m.mu.Unlock()

	go func() {
		ticker := time.NewTicker(heartbeatInterval(m.self.LeaseSeconds))
		defer ticker.Stop()
		for {
			select {
			case <-m.stopCh:
				return
			case <-ticker.C:
				if err := m.beat(); err != nil {
					// 心跳写不进去就说明库有问题，但这里不该让进程退出：
					// 页面还能用比整个平台挂掉好。租约会过期，另一个实例会接管
					log.Printf("[instance] 心跳写入失败（租约可能过期并被接管）: %v", err)
				}
			}
		}
	}()
}

// Role 当前角色
func (m *Manager) Role() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.role
}

// ID 本实例标识
func (m *Manager) ID() string { return m.self.ID }

// Release 优雅退出时清掉自己那一行。
//
// 删掉而不是只标 stopped：留着的话下次启动会被自己的上一条记录挡住，
// 而「重启失败，说已经有实例在跑」是最让人恼火的一类故障。
func (m *Manager) Release() {
	m.stopOnce.Do(func() { close(m.stopCh) })
	if err := m.db.Delete(&model.PlatformInstance{}, "id = ?", m.self.ID).Error; err != nil {
		log.Printf("[instance] 注销实例失败（%d 秒后租约自然过期）: %v", m.self.LeaseSeconds, err)
	}
}

// beat 写一次心跳，顺带在 leader 已死时接管
func (m *Manager) beat() error {
	now := time.Now()
	err := m.db.Model(&model.PlatformInstance{}).Where("id = ?", m.self.ID).
		Update("heartbeat_at", now).Error
	if err != nil {
		return err
	}

	if m.Role() == RoleLeader {
		return nil
	}
	// standby：看 leader 还在不在
	alive, err := m.aliveOthers(m.self.LeaseSeconds)
	if err != nil {
		return err
	}
	for _, other := range alive {
		if other.Role == RoleLeader {
			return nil // leader 还活着，继续待着
		}
	}
	return m.promote()
}

// promote standby 接过 leader。
//
// 用条件更新做互斥：只有当库里确实没有活着的 leader 时才改自己的角色。
// 两个 standby 同时想接管的话，SQLite 的写串行化保证只有一个会看到「没有 leader」。
func (m *Manager) promote() error {
	deadline := time.Now().Add(-time.Duration(m.self.LeaseSeconds) * time.Second)
	err := m.db.Transaction(func(tx *gorm.DB) error {
		var count int64
		if err := tx.Model(&model.PlatformInstance{}).
			Where("role = ? AND id <> ? AND heartbeat_at > ?", RoleLeader, m.self.ID, deadline).
			Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			return nil // 别人先接了
		}
		return tx.Model(&model.PlatformInstance{}).Where("id = ?", m.self.ID).
			Update("role", RoleLeader).Error
	})
	if err != nil {
		return err
	}

	var fresh model.PlatformInstance
	if err := m.db.First(&fresh, "id = ?", m.self.ID).Error; err != nil {
		return err
	}
	if fresh.Role != RoleLeader {
		return nil
	}

	m.mu.Lock()
	m.role = RoleLeader
	promote := m.onPromote
	m.mu.Unlock()
	log.Printf("[instance] 原 leader 心跳超过 %d 秒没更新，本实例 %s 接管调度",
		m.self.LeaseSeconds, m.self.ID)
	if promote != nil {
		promote()
	}
	return nil
}

// aliveOthers 心跳还新鲜的其它实例。
//
// 除了看心跳，**同一台机器上的记录还要看 PID 是否真的还在**：
// 被 `kill -9`（或 IDE 直接停止）的进程来不及注销，它的心跳会在租约内一直显示新鲜，
// 于是「杀掉再起」在一个租约周期内起不来 —— 这是开发与紧急重启时最常见的动作。
// 跨主机不做这个判断：没法知道对面那台机器上的进程还在不在，那种情况只能等租约。
func (m *Manager) aliveOthers(lease int) ([]model.PlatformInstance, error) {
	deadline := time.Now().Add(-time.Duration(lease) * time.Second)
	var rows []model.PlatformInstance
	err := m.db.Where("id <> ? AND heartbeat_at > ?", m.self.ID, deadline).
		Order("started_at asc").Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("读实例表失败: %w", err)
	}

	out := make([]model.PlatformInstance, 0, len(rows))
	for _, row := range rows {
		if row.Hostname == m.self.Hostname && !processAlive(row.PID) {
			log.Printf("[instance] 清掉一条僵尸记录：%s pid=%d 在本机已经不存在了"+
				"（上次大概是被 kill -9 停的，没来得及注销）", row.ID[:8], row.PID)
			if err := m.db.Delete(&model.PlatformInstance{}, "id = ?", row.ID).Error; err != nil {
				// 删不掉就当它还活着：宁可这次起不来，也不要两个实例同时跑调度
				log.Printf("[instance] 僵尸记录删除失败，按「还活着」处理: %v", err)
				out = append(out, row)
			}
			continue
		}
		out = append(out, row)
	}
	return out, nil
}

// SoleInstance 当前是不是唯一活着的实例。
//
// 给那些「假设自己是唯一实例」的启动收尾用（比如把残留的转发隧道标成已关闭）——
// 有别人活着的时候那些动作会误伤对方。
func (m *Manager) SoleInstance() bool {
	alive, err := m.aliveOthers(m.self.LeaseSeconds)
	if err != nil {
		// 查不出来时按「不是唯一」处理：宁可少做一次清理，也不要误杀别人的隧道
		log.Printf("[instance] 判断是否唯一实例时出错，按非唯一处理: %v", err)
		return false
	}
	return len(alive) == 0
}

// Alive 所有心跳新鲜的实例（含自己），给平台健康页用
func Alive(db *gorm.DB, lease int) ([]model.PlatformInstance, error) {
	if lease <= 0 {
		lease = DefaultLeaseSeconds
	}
	deadline := time.Now().Add(-time.Duration(lease) * time.Second)
	var rows []model.PlatformInstance
	err := db.Where("heartbeat_at > ?", deadline).Order("started_at asc").Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("读实例表失败: %w", err)
	}
	return rows, nil
}

func describe(rows []model.PlatformInstance) string {
	parts := make([]string, 0, len(rows))
	for _, row := range rows {
		parts = append(parts, fmt.Sprintf("%s@%s pid=%d 版本=%s 角色=%s 心跳=%s",
			row.ID[:8], row.Hostname, row.PID, row.Version, row.Role,
			row.HeartbeatAt.Format("15:04:05")))
	}
	if len(parts) == 0 {
		return "（无）"
	}
	out := parts[0]
	for _, p := range parts[1:] {
		out += "；" + p
	}
	return out
}

func randomID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}
