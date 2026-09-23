package instance

// 实例互斥与选主的测试。
//
// 这些断言守的都是「悄悄跑两遍」这一类事故：出问题时没人在看，
// 等发现的时候命令已经在生产机器上执行过两遍了。

import (
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"ops-platform/server/internal/model"
)

func openTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	g, err := gorm.Open(sqlite.Open(t.TempDir()+"/instance.db"),
		&gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	if err := g.AutoMigrate(&model.PlatformInstance{}); err != nil {
		t.Fatalf("建表失败: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, err := g.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	return g
}

// 第一个实例是 leader
func TestFirstInstanceIsLeader(t *testing.T) {
	g := openTestDB(t)
	m, err := Claim(g, Options{Addr: ":8080", Version: "v1"})
	if err != nil {
		t.Fatalf("第一个实例应该能起来: %v", err)
	}
	if m.Role() != RoleLeader {
		t.Fatalf("第一个实例应该是 leader，实际 %s", m.Role())
	}
	if !m.SoleInstance() {
		t.Fatal("只有一个实例时 SoleInstance 应该为真")
	}
}

// 第二个实例默认拒绝启动，而且错误里要说清为什么和怎么办
func TestSecondInstanceRefusedByDefault(t *testing.T) {
	g := openTestDB(t)
	if _, err := Claim(g, Options{Version: "v1"}); err != nil {
		t.Fatalf("第一个实例失败: %v", err)
	}
	_, err := Claim(g, Options{Version: "v1"})
	if err == nil {
		t.Fatal("默认必须拒绝第二个实例 —— 两个实例会把定时任务各跑一遍")
	}
	for _, want := range []string{"定时任务", "两遍", "OPS_ALLOW_MULTI_INSTANCE", "database is locked"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("错误信息要说清后果与出路（缺 %q）：%v", want, err)
		}
	}
	// 被拒的实例不该在表里留下记录
	var count int64
	g.Model(&model.PlatformInstance{}).Count(&count)
	if count != 1 {
		t.Fatalf("被拒绝的实例不该登记，表里应该只有 1 行，实际 %d", count)
	}
}

// 显式允许时第二个实例以 standby 起来，且 SoleInstance 为假
func TestSecondInstanceStandbyWhenAllowed(t *testing.T) {
	g := openTestDB(t)
	first, err := Claim(g, Options{Version: "v1"})
	if err != nil {
		t.Fatalf("第一个实例失败: %v", err)
	}
	second, err := Claim(g, Options{Version: "v1", AllowMulti: true})
	if err != nil {
		t.Fatalf("显式允许后第二个实例应该能起来: %v", err)
	}
	if second.Role() != RoleStandby {
		t.Fatalf("第二个实例必须是 standby（不跑调度），实际 %s", second.Role())
	}
	if first.SoleInstance() || second.SoleInstance() {
		t.Fatal("有两个实例时 SoleInstance 必须为假 —— " +
			"否则启动收尾会把对方正在用的隧道与会话标成已结束")
	}
	if first.ID() == second.ID() {
		t.Fatal("两个实例的标识不能一样")
	}
}

// leader 心跳过期后，standby 接管并回调（回调用来启动调度）
func TestStandbyTakesOverWhenLeaseExpires(t *testing.T) {
	g := openTestDB(t)
	leader, err := Claim(g, Options{Version: "v1", LeaseSeconds: 20})
	if err != nil {
		t.Fatalf("leader 失败: %v", err)
	}
	standby, err := Claim(g, Options{Version: "v1", AllowMulti: true, LeaseSeconds: 20})
	if err != nil {
		t.Fatalf("standby 失败: %v", err)
	}

	var promoted int32
	standby.mu.Lock()
	standby.onPromote = func() { atomic.AddInt32(&promoted, 1) }
	standby.mu.Unlock()

	// leader 还活着时不该接管
	if err := standby.beat(); err != nil {
		t.Fatalf("心跳失败: %v", err)
	}
	if standby.Role() != RoleStandby {
		t.Fatal("leader 活着的时候 standby 不能接管 —— 那就变成两个都在跑调度")
	}
	if atomic.LoadInt32(&promoted) != 0 {
		t.Fatal("不该触发接管回调")
	}

	// 把 leader 的心跳推到租约之外，模拟 kill -9
	old := time.Now().Add(-time.Hour)
	if err := g.Model(&model.PlatformInstance{}).Where("id = ?", leader.ID()).
		Update("heartbeat_at", old).Error; err != nil {
		t.Fatalf("改心跳失败: %v", err)
	}
	if err := standby.beat(); err != nil {
		t.Fatalf("心跳失败: %v", err)
	}
	if standby.Role() != RoleLeader {
		t.Fatal("leader 心跳过期后 standby 必须接管，否则一个调度任务都不会跑")
	}
	if atomic.LoadInt32(&promoted) != 1 {
		t.Fatalf("接管要回调一次（用来启动调度），实际 %d 次", atomic.LoadInt32(&promoted))
	}

	// 库里也要改过来，否则下一个起来的实例会以为没有 leader
	var row model.PlatformInstance
	g.First(&row, "id = ?", standby.ID())
	if row.Role != RoleLeader {
		t.Fatalf("角色变更要落库，实际 %s", row.Role)
	}
}

// 两个 standby 同时想接管时，只有一个能成
func TestOnlyOneStandbyWins(t *testing.T) {
	g := openTestDB(t)
	leader, _ := Claim(g, Options{Version: "v1", LeaseSeconds: 20})
	a, err := Claim(g, Options{Version: "v1", AllowMulti: true, LeaseSeconds: 20})
	if err != nil {
		t.Fatalf("standby A 失败: %v", err)
	}
	b, err := Claim(g, Options{Version: "v1", AllowMulti: true, LeaseSeconds: 20})
	if err != nil {
		t.Fatalf("standby B 失败: %v", err)
	}
	g.Model(&model.PlatformInstance{}).Where("id = ?", leader.ID()).
		Update("heartbeat_at", time.Now().Add(-time.Hour))

	if err := a.beat(); err != nil {
		t.Fatalf("A 心跳失败: %v", err)
	}
	if err := b.beat(); err != nil {
		t.Fatalf("B 心跳失败: %v", err)
	}
	var leaders int64
	g.Model(&model.PlatformInstance{}).
		Where("role = ? AND heartbeat_at > ?", RoleLeader, time.Now().Add(-30*time.Second)).
		Count(&leaders)
	if leaders != 1 {
		t.Fatalf("活着的 leader 只能有一个，实际 %d —— 两个都当 leader 就是跑两遍", leaders)
	}
	if a.Role() == RoleLeader && b.Role() == RoleLeader {
		t.Fatal("两个 standby 都认为自己接管了")
	}
}

// 优雅退出后记录要删掉，否则重启会被自己的上一条记录挡住
func TestReleaseFreesTheSlot(t *testing.T) {
	g := openTestDB(t)
	first, err := Claim(g, Options{Version: "v1"})
	if err != nil {
		t.Fatalf("第一个实例失败: %v", err)
	}
	first.Release()

	var count int64
	g.Model(&model.PlatformInstance{}).Count(&count)
	if count != 0 {
		t.Fatalf("注销后不该留记录，实际 %d 行 —— 留着会让重启报「已经有实例在跑」", count)
	}
	second, err := Claim(g, Options{Version: "v1"})
	if err != nil {
		t.Fatalf("上一个实例已注销，重启应该能直接起来: %v", err)
	}
	if second.Role() != RoleLeader {
		t.Fatalf("重启后应该还是 leader，实际 %s", second.Role())
	}
}

// 心跳过期的旧记录不该挡住新实例（比如上次是 kill -9 走的）
func TestStaleRecordDoesNotBlock(t *testing.T) {
	g := openTestDB(t)
	dead, err := Claim(g, Options{Version: "v1", LeaseSeconds: 20})
	if err != nil {
		t.Fatalf("第一个实例失败: %v", err)
	}
	g.Model(&model.PlatformInstance{}).Where("id = ?", dead.ID()).
		Update("heartbeat_at", time.Now().Add(-time.Hour))

	fresh, err := Claim(g, Options{Version: "v1", LeaseSeconds: 20})
	if err != nil {
		t.Fatalf("旧实例心跳已过期，新实例应该能起来: %v", err)
	}
	if fresh.Role() != RoleLeader {
		t.Fatalf("应该直接当 leader，实际 %s", fresh.Role())
	}
}

// Alive 只返回心跳新鲜的
func TestAliveFiltersByLease(t *testing.T) {
	g := openTestDB(t)
	live, _ := Claim(g, Options{Version: "v1", LeaseSeconds: 20})
	dead, _ := Claim(g, Options{Version: "v1", AllowMulti: true, LeaseSeconds: 20})
	g.Model(&model.PlatformInstance{}).Where("id = ?", dead.ID()).
		Update("heartbeat_at", time.Now().Add(-time.Hour))

	rows, err := Alive(g, 20)
	if err != nil {
		t.Fatalf("Alive 失败: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != live.ID() {
		t.Fatalf("只该列出心跳新鲜的那一个，实际 %+v", rows)
	}
}
