package migrate

// 迁移引擎的测试。
//
// 这些断言守的是升级路径，而升级路径的特点是：**出错时才被执行到，
// 而那时候通常没人在看**。所以每一条都对着一个具体的事故场景。

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"ops-platform/server/internal/model"
)

func openTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	g, err := gorm.Open(sqlite.Open(t.TempDir()+"/migrate.db"),
		&gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, err := g.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	return g
}

// 测试用的最小模型集：够覆盖两步回填 + 空库判定
func testModels() []any {
	return []any{
		&model.User{}, &model.SysConfig{}, &model.CronJob{},
		&model.Event{}, &model.EventLog{}, &model.SchemaMigration{},
	}
}

func runFullMigrate(t *testing.T, g *gorm.DB, binVersion string) []model.SchemaMigration {
	t.Helper()
	state, err := Prepare(g)
	if err != nil {
		t.Fatalf("Prepare 失败: %v", err)
	}
	if err := g.AutoMigrate(testModels()...); err != nil {
		t.Fatalf("AutoMigrate 失败: %v", err)
	}
	done, err := Finish(g, state, binVersion)
	if err != nil {
		t.Fatalf("Finish 失败: %v", err)
	}
	return done
}

// 全新库：步骤记成 baseline 而不是执行 —— 空表上跑回填毫无意义，
// 而且「跑过」和「新库跳过」在排查时是两件事
func TestFreshInstallBaselinesSteps(t *testing.T) {
	g := openTestDB(t)
	done := runFullMigrate(t, g, "v-test")

	if len(done) != len(steps) {
		t.Fatalf("新装库应该记录全部 %d 个步骤，实际 %d", len(steps), len(done))
	}
	for _, row := range done {
		if row.Source != "baseline" {
			t.Fatalf("v%d 在新装库上应该记为 baseline，实际 %s", row.Version, row.Source)
		}
	}
	st, err := Report(g, testModels())
	if err != nil {
		t.Fatalf("Report 失败: %v", err)
	}
	if st.Version != KnownVersion() {
		t.Fatalf("新装库的版本应该等于已知最高版本 %d，实际 %d", KnownVersion(), st.Version)
	}
	if len(st.Pending) != 0 {
		t.Fatalf("新装库不该有待应用步骤，实际 %d 个", len(st.Pending))
	}
}

// 重复启动不该重复执行
func TestSecondRunIsNoOp(t *testing.T) {
	g := openTestDB(t)
	runFullMigrate(t, g, "v-test")
	done := runFullMigrate(t, g, "v-test")
	if len(done) != 0 {
		t.Fatalf("第二次启动不该再执行任何步骤，实际 %d 个", len(done))
	}
}

// 存量库（AutoMigrate 时代建的）：步骤要真的执行，而且要真的改到数据
func TestExistingDBRunsStepsForReal(t *testing.T) {
	g := openTestDB(t)
	// 先造一个「老库」：有 users 表，有一条未确认的定时任务、一个没有响应时间的事件
	if err := g.AutoMigrate(&model.User{}, &model.SysConfig{}, &model.CronJob{},
		&model.Event{}, &model.EventLog{}); err != nil {
		t.Fatalf("造老库失败: %v", err)
	}
	g.Create(&model.User{Username: "admin"})
	g.Create(&model.CronJob{Name: "夜间清理", ProdConfirmed: false})
	g.Create(&model.Event{Title: "老事件", Status: "resolved"})
	firstTouch := time.Now().Add(-2 * time.Hour)
	g.Create(&model.EventLog{EventID: 1, Action: "status", CreatedAt: firstTouch})

	done := runFullMigrate(t, g, "v-new")
	if len(done) != len(steps) {
		t.Fatalf("存量库应该执行全部 %d 步，实际 %d", len(steps), len(done))
	}
	for _, row := range done {
		if row.Source != "applied" {
			t.Fatalf("v%d 在存量库上应该记为 applied，实际 %s", row.Version, row.Source)
		}
		if row.AppliedBy != "v-new" {
			t.Fatalf("应该记下执行它的二进制版本，实际 %q", row.AppliedBy)
		}
	}

	var job model.CronJob
	g.First(&job, 1)
	if !job.ProdConfirmed {
		t.Fatal("存量定时任务应该被回填成已确认 —— 否则它会在凌晨被闸门静默拦掉")
	}
	var event model.Event
	g.First(&event, 1)
	if event.RespondedAt == nil {
		t.Fatal("存量事件应该从时间线回填响应时间 —— 否则会凭空出现一批「响应已超时」")
	}
	if event.RespondedAt.Unix() != firstTouch.Unix() {
		t.Fatalf("响应时间应该取时间线最早一条（%v），实际 %v", firstTouch, event.RespondedAt)
	}
}

// 被老二进制回填过的库：步骤要认出来并变成空操作，不能把数据再改一遍
func TestLegacyMarkerMakesStepNoOp(t *testing.T) {
	g := openTestDB(t)
	if err := g.AutoMigrate(&model.User{}, &model.SysConfig{}, &model.CronJob{},
		&model.Event{}, &model.EventLog{}); err != nil {
		t.Fatalf("造老库失败: %v", err)
	}
	g.Create(&model.User{Username: "admin"})
	// 老二进制留下的标记 + 一条「人后来又改回未确认」的任务
	g.Create(&model.SysConfig{Group: "migration", Key: cfgCronBackfill, Value: "true"})
	g.Create(&model.CronJob{Name: "人工改成未确认", ProdConfirmed: false})

	runFullMigrate(t, g, "v-new")

	var job model.CronJob
	g.First(&job, 1)
	if job.ProdConfirmed {
		t.Fatal("老二进制已经回填过，这一步必须是空操作 —— " +
			"否则会把人后来主动改成「未确认」的任务又翻回去")
	}
	// 但记录还是要落，否则每次启动都要重新判断一遍
	var count int64
	g.Model(&model.SchemaMigration{}).Where("version = ?", 1).Count(&count)
	if count != 1 {
		t.Fatalf("即使是空操作也要记录，实际记录数 %d", count)
	}
}

// 用旧二进制起新库：必须在 AutoMigrate 之前就拒绝，而不是等访问新字段时崩
func TestRefusesDowngrade(t *testing.T) {
	g := openTestDB(t)
	runFullMigrate(t, g, "v-new")
	// 伪造一个「未来版本」的记录，模拟这个库被更新的程序升级过
	future := KnownVersion() + 5
	g.Create(&model.SchemaMigration{
		Version: future, Name: "来自未来的步骤", Source: "applied",
		AppliedBy: "v-future", AppliedAt: time.Now(),
	})

	_, err := Prepare(g)
	if err == nil {
		t.Fatal("库版本比二进制新时必须拒绝启动")
	}
	for _, want := range []string{"旧版本", "ops restore"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("错误信息要说清怎么办（缺 %q）: %v", want, err)
		}
	}
}

// 步骤执行失败：不能记成已应用，否则下次启动会跳过它
func TestFailedStepIsNotRecorded(t *testing.T) {
	g := openTestDB(t)
	if err := g.AutoMigrate(&model.User{}, &model.SchemaMigration{}); err != nil {
		t.Fatalf("造老库失败: %v", err)
	}
	g.Create(&model.User{Username: "admin"})

	original := steps
	t.Cleanup(func() { steps = original })
	steps = []Step{{
		Version: 9001, Name: "会失败的步骤", Note: "测试用",
		Run: func(*gorm.DB) error { return errors.New("故意失败") },
	}}

	state, err := Prepare(g)
	if err != nil {
		t.Fatalf("Prepare 失败: %v", err)
	}
	if _, err := Finish(g, state, "v-test"); err == nil {
		t.Fatal("步骤失败时 Finish 必须返回错误（启动流程靠它 fatal）")
	}
	var count int64
	g.Model(&model.SchemaMigration{}).Where("version = ?", 9001).Count(&count)
	if count != 0 {
		t.Fatal("失败的步骤不能留下记录 —— 否则下次启动会当它跑过而跳过")
	}
}

// 陈旧列：报出来、给出可执行的语句，但绝不自动删
func TestDriftReportsStaleColumnOnly(t *testing.T) {
	g := openTestDB(t)
	runFullMigrate(t, g, "v-test")

	// 模拟一次「模型里删了字段但 AutoMigrate 不删列」
	if err := g.Exec("ALTER TABLE `users` ADD COLUMN `legacy_nickname` text").Error; err != nil {
		t.Fatalf("造陈旧列失败: %v", err)
	}
	drift, err := Drift(g, testModels())
	if err != nil {
		t.Fatalf("Drift 失败: %v", err)
	}
	if len(drift) != 1 {
		t.Fatalf("应该只报出 1 个陈旧列，实际 %d: %+v", len(drift), drift)
	}
	if drift[0].Table != "users" || drift[0].Column != "legacy_nickname" {
		t.Fatalf("报错的表/列不对: %+v", drift[0])
	}
	if drift[0].SQL != "ALTER TABLE `users` DROP COLUMN `legacy_nickname`;" {
		t.Fatalf("要给出人可以照着执行的语句，实际 %q", drift[0].SQL)
	}
	// 关键：报完之后列还在
	if !g.Migrator().HasColumn(&model.User{}, "legacy_nickname") {
		t.Fatal("漂移检查不能删列 —— 删列不可逆，平台不替人做这个决定")
	}
}

// 版本号必须唯一且递增；Name 也不能重复（status 输出全靠它认人）
func TestStepVersionsAreUniqueAndOrdered(t *testing.T) {
	seenVersion := map[int]bool{}
	seenName := map[string]bool{}
	last := 0
	for _, step := range sortedSteps() {
		if step.Version <= 0 {
			t.Fatalf("版本号必须是正数，%s 是 %d", step.Name, step.Version)
		}
		if seenVersion[step.Version] {
			t.Fatalf("版本号 %d 重复 —— 重复意味着有一个步骤永远不会执行", step.Version)
		}
		if seenName[step.Name] {
			t.Fatalf("步骤名 %q 重复", step.Name)
		}
		if step.Name == "" || step.Note == "" || step.Run == nil {
			t.Fatalf("v%d 的 Name / Note / Run 都不能空（Note 会落库，供 status 解释）", step.Version)
		}
		seenVersion[step.Version] = true
		seenName[step.Name] = true
		if step.Version <= last {
			t.Fatalf("排序后版本号应该严格递增，%d 出现在 %d 之后", step.Version, last)
		}
		last = step.Version
	}
}
