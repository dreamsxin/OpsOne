package migrate

// 版本化迁移。
//
// # 这一轮修的是什么
//
// 在这之前表结构完全靠 `AutoMigrate`：**只加不减、没有版本号、没有顺序、没有回滚**。
// 具体的后果有三条，而且都只在升级时才显形：
//
//  1. **说不出库是哪个版本**。出问题时无法回答「这个库跑过哪些结构变更」，
//     只能拿模型代码去猜。
//  2. **用旧二进制起新库不会报错**。AutoMigrate 只加不减，所以旧程序看着一切正常，
//     然后在访问新版本才有的字段时才崩 —— 现象离原因很远。
//  3. **数据回填是散装的**。已有两段（定时任务生产确认、事件响应时间）靠在
//     `sys_configs` 里写一个 key 当「做过了」的标记。第三段要加的时候只能继续这么糊，
//     没有顺序保证，也不知道执行了多久、什么时候执行的。
//
// # 这一轮**不做**什么（说清楚，免得被读成「迁移做完了」）
//
//   - **不把 109 个 model 改写成手写 DDL**。AutoMigrate 干「加表加列」这件事是对的，
//     把它换成一万行手写 SQL 只会引入新 bug。这里做的是在它外面**套一层版本与顺序**。
//   - **没有 down / 回滚**。SQLite 下很多变更（改列类型、加约束）本身就要重建表，
//     而自动生成的 down 通常是错的 —— 一个能跑但把数据弄坏的回滚比没有回滚更危险。
//     回滚路径仍然是**从备份恢复**（`ops restore`），升级流程因此强制先备份。
//     这一点在 `ops migrate status` 的输出里直接写着。
//   - **不自动删陈旧列**。AutoMigrate 留下的旧列这里只**报告**，
//     并打印出人可以照着执行的 `ALTER TABLE ... DROP COLUMN`。
//     删列是不可逆的，平台不替人做这个决定。

import (
	"fmt"
	"log"
	"sort"
	"time"

	"gorm.io/gorm"

	"ops-platform/server/internal/model"
)

// Step 一次结构或数据变更。
//
// 只有 Run，没有 Down —— 见文件头「不做什么」。
type Step struct {
	// Version 严格递增且不允许重复，由 steps_test.go 守着
	Version int
	Name    string
	// Note 落库，让 `ops migrate status` 能解释这一步当时在做什么
	Note string
	Run  func(g *gorm.DB) error
}

// State Prepare 的结果，必须传给 Finish。
type State struct {
	// Fresh 这是一个空库（AutoMigrate 之前连 users 表都没有）。
	// 空库的结构天生就是最新的，所以所有步骤记为 baseline 而不执行
	Fresh bool
	// DBVersion 进来时库里的最高版本
	DBVersion int
}

// KnownVersion 这个二进制知道的最高版本
func KnownVersion() int {
	top := 0
	for _, step := range steps {
		if step.Version > top {
			top = step.Version
		}
	}
	return top
}

// Prepare 在 AutoMigrate **之前**调用。
//
// 顺序是有讲究的：降级检查必须在 AutoMigrate 之前。否则一个旧二进制会先用旧模型
// AutoMigrate 一遍新库，把新版本删掉的列又加回去，而新版本那边已经把对应步骤
// 记成「已应用」——库就此进入一个谁都没预期的状态。
func Prepare(g *gorm.DB) (State, error) {
	var st State
	if err := g.AutoMigrate(&model.SchemaMigration{}); err != nil {
		return st, fmt.Errorf("建迁移记录表失败: %w", err)
	}

	current, err := currentVersion(g)
	if err != nil {
		return st, err
	}
	st.DBVersion = current

	known := KnownVersion()
	if current > known {
		return st, fmt.Errorf("这个库的结构版本是 %d，而当前二进制只认到 %d ——"+
			"说明你在用旧版本的程序启动一个被新版本升级过的库。"+
			"AutoMigrate 只加不减，所以它不会报错，而是等到访问新字段时才崩，"+
			"那时候现象离原因已经很远。请换回新版本程序，或者用 `ops restore` 从升级前的备份恢复",
			current, known)
	}

	// 空库判定要在 AutoMigrate 之前做：之后就分不出「新装」和「升级」了。
	// 用 users 表当标志——它是第一批建的表，且任何能用的库里都有 admin
	st.Fresh = !g.Migrator().HasTable(&model.User{})
	return st, nil
}

// Finish 在 AutoMigrate **之后**调用，返回这次真正执行了哪些步骤。
func Finish(g *gorm.DB, st State, binaryVersion string) ([]model.SchemaMigration, error) {
	applied, err := appliedVersions(g)
	if err != nil {
		return nil, err
	}

	ordered := sortedSteps()
	done := make([]model.SchemaMigration, 0, len(ordered))
	for _, step := range ordered {
		if applied[step.Version] {
			continue
		}
		record := model.SchemaMigration{
			Version: step.Version, Name: step.Name, Note: step.Note,
			Source: "applied", AppliedBy: binaryVersion, AppliedAt: time.Now(),
		}
		if st.Fresh {
			// 新装：结构由 AutoMigrate 一次建到位，回填步骤面对的也是空表。
			// 记成 baseline 而不是 applied —— 以后排查时「这一步跑过」和
			// 「这一步因为是新库而跳过」是两件不同的事
			record.Source = "baseline"
		} else {
			started := time.Now()
			if err := step.Run(g); err != nil {
				return done, fmt.Errorf("迁移步骤 %d（%s）失败: %w", step.Version, step.Name, err)
			}
			record.TookMs = time.Since(started).Milliseconds()
		}
		if err := g.Create(&record).Error; err != nil {
			return done, fmt.Errorf("迁移步骤 %d 执行成功但记录失败: %w", step.Version, err)
		}
		done = append(done, record)
	}

	if len(done) > 0 && !st.Fresh {
		for _, item := range done {
			log.Printf("[migrate] 已应用 v%d %s（%dms）", item.Version, item.Name, item.TookMs)
		}
	}
	return done, nil
}

// currentVersion 库里最高的已记录版本；表还不存在或没有记录时返回 0
func currentVersion(g *gorm.DB) (int, error) {
	var top *int
	err := g.Model(&model.SchemaMigration{}).Select("MAX(version)").Scan(&top).Error
	if err != nil {
		return 0, fmt.Errorf("读迁移记录失败: %w", err)
	}
	if top == nil {
		return 0, nil
	}
	return *top, nil
}

func appliedVersions(g *gorm.DB) (map[int]bool, error) {
	var rows []model.SchemaMigration
	if err := g.Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("读迁移记录失败: %w", err)
	}
	out := make(map[int]bool, len(rows))
	for _, row := range rows {
		out[row.Version] = true
	}
	return out, nil
}

func sortedSteps() []Step {
	ordered := make([]Step, len(steps))
	copy(ordered, steps)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Version < ordered[j].Version })
	return ordered
}

// ---------- 状态与漂移 ----------

// PendingStep 还没应用的步骤
type PendingStep struct {
	Version int    `json:"version"`
	Name    string `json:"name"`
	Note    string `json:"note"`
}

// ColumnDrift 库里有、模型里已经没有的列。
//
// AutoMigrate 不删列，所以改过字段名或删过字段的库里会留下这些。
// 它们不会让程序出错，但会让人在读表结构时误以为那些字段还在用 ——
// 所以照实报出来，删不删由人决定。
type ColumnDrift struct {
	Table  string `json:"table"`
	Column string `json:"column"`
	// SQL 人可以照着执行的语句（备份之后再执行）
	SQL string `json:"sql"`
}

type Status struct {
	Version      int                     `json:"version"`
	KnownVersion int                     `json:"knownVersion"`
	Applied      []model.SchemaMigration `json:"applied"`
	Pending      []PendingStep           `json:"pending"`
	Drift        []ColumnDrift           `json:"drift"`
	// DriftError 漂移检查本身失败时的原因。查不了要说查不了，不能显示成「没有漂移」
	DriftError string `json:"driftError"`
}

// Report 汇总当前迁移状态。models 传 db.Models()，用于漂移比对。
func Report(g *gorm.DB, models []any) (*Status, error) {
	st := &Status{KnownVersion: KnownVersion()}
	if !g.Migrator().HasTable(&model.SchemaMigration{}) {
		// 还没迁移过的库：版本 0，全部待应用
		for _, step := range sortedSteps() {
			st.Pending = append(st.Pending, PendingStep{step.Version, step.Name, step.Note})
		}
		return st, nil
	}

	var rows []model.SchemaMigration
	if err := g.Order("version asc").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("读迁移记录失败: %w", err)
	}
	st.Applied = rows
	done := make(map[int]bool, len(rows))
	for _, row := range rows {
		done[row.Version] = true
		if row.Version > st.Version {
			st.Version = row.Version
		}
	}
	for _, step := range sortedSteps() {
		if !done[step.Version] {
			st.Pending = append(st.Pending, PendingStep{step.Version, step.Name, step.Note})
		}
	}

	drift, err := Drift(g, models)
	if err != nil {
		st.DriftError = err.Error()
	} else {
		st.Drift = drift
	}
	return st, nil
}

// Drift 比对模型声明的列与库里实际的列，返回库里多出来的那些。
//
// 反方向（模型有、库里没有）不需要报：那是 AutoMigrate 的活，它会补上。
func Drift(g *gorm.DB, models []any) ([]ColumnDrift, error) {
	out := make([]ColumnDrift, 0)
	for _, m := range models {
		stmt := &gorm.Statement{DB: g}
		if err := stmt.Parse(m); err != nil {
			return nil, fmt.Errorf("解析模型失败: %w", err)
		}
		table := stmt.Schema.Table
		if !g.Migrator().HasTable(m) {
			continue // 还没建表，不是漂移
		}
		declared := make(map[string]bool, len(stmt.Schema.DBNames))
		for _, name := range stmt.Schema.DBNames {
			declared[name] = true
		}
		cols, err := g.Migrator().ColumnTypes(m)
		if err != nil {
			return nil, fmt.Errorf("读 %s 的列失败: %w", table, err)
		}
		for _, col := range cols {
			name := col.Name()
			if declared[name] {
				continue
			}
			out = append(out, ColumnDrift{
				Table: table, Column: name,
				SQL: fmt.Sprintf("ALTER TABLE `%s` DROP COLUMN `%s`;", table, name),
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Table != out[j].Table {
			return out[i].Table < out[j].Table
		}
		return out[i].Column < out[j].Column
	})
	return out, nil
}
