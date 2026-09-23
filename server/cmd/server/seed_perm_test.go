package main

// 权限码与菜单种子的一致性检查。
//
// # 这条测试是踩出来的
//
// 加「在生产主机上执行」和「查看与导出审计」两个按钮时，我给它们的菜单 ID
// 撞上了已有条目（205 与「脚本库」、829 与「维护 IM 应用」）。
// `upsertBuiltinMenus` 按 ID upsert，所以清单里后写的把先写的覆盖掉了 ——
// 结果是**那两个权限码根本没被种进去**，任何角色都拿不到它们，
// 于是「往生产主机下发」和「IM 应用管理」变成谁都调不了的接口。
//
// 这种 bug 的形态很讨厌：编译能过、Seed 不报错、页面上只是少了一个勾选项，
// 而接口在运行时返回 403。所以用一条测试把两件事钉死：
//
//  1. 种子里的菜单 ID 不重复（源码扫描 —— 数据库看不出这个问题，
//     它只会安静地把后者当成对前者的更新）；
//  2. main.go 里每一个 RequirePerm / RequireAnyPerm 用到的码，
//     **在真的跑完 Seed 之后**都能在库里找到。

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"ops-platform/server/internal/db"
	"ops-platform/server/internal/model"
)

var (
	seedMenuIDLine = regexp.MustCompile(`\{ID:\s*(\d+),\s*ParentID:`)
	permCodeUse    = regexp.MustCompile(`Require(?:Any)?Perm\(([^)]*)\)`)
	quotedCode     = regexp.MustCompile(`"([a-z][a-z0-9]*:[a-z0-9_]+)"`)
)

// 种子里的菜单 ID 必须唯一。重复的 ID 不会报错，只会让后者覆盖前者
func TestSeedMenuIDsAreUnique(t *testing.T) {
	raw, err := os.ReadFile("../../internal/db/db.go")
	if err != nil {
		t.Fatalf("读 db.go 失败: %v", err)
	}
	seen := map[string][]int{}
	for lineNo, line := range strings.Split(string(raw), "\n") {
		for _, m := range seedMenuIDLine.FindAllStringSubmatch(line, -1) {
			seen[m[1]] = append(seen[m[1]], lineNo+1)
		}
	}
	ids := make([]string, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if len(seen[id]) > 1 {
			t.Errorf("菜单 ID %s 在种子里出现了 %d 次（db.go 第 %v 行）——"+
				"upsertBuiltinMenus 按 ID upsert，后写的会把前面那条整条覆盖掉，"+
				"对应的权限码就等于没种进去", id, len(seen[id]), seen[id])
		}
	}
	if len(ids) < 100 {
		t.Fatalf("只扫到 %d 个菜单 ID，正则可能失效了", len(ids))
	}
}

// main.go 里用到的每个权限码，跑完 Seed 之后都必须真的存在
func TestEveryUsedPermCodeIsSeeded(t *testing.T) {
	raw, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("读 main.go 失败: %v", err)
	}
	used := map[string]bool{}
	for _, call := range permCodeUse.FindAllStringSubmatch(string(raw), -1) {
		for _, code := range quotedCode.FindAllStringSubmatch(call[1], -1) {
			used[code[1]] = true
		}
	}
	if len(used) < 50 {
		t.Fatalf("只扫到 %d 个权限码，正则可能失效了", len(used))
	}

	dir := t.TempDir()
	g, err := gorm.Open(sqlite.Open(dir+"/seed.db"),
		&gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, err := g.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	if err := db.Migrate(g, "test"); err != nil {
		t.Fatalf("建表失败: %v", err)
	}
	if err := db.Seed(g, "Test@123456"); err != nil {
		t.Fatalf("种子失败: %v", err)
	}

	var codes []string
	g.Model(&model.Menu{}).Where("auth_code <> ''").Pluck("auth_code", &codes)
	seeded := make(map[string]bool, len(codes))
	for _, code := range codes {
		seeded[code] = true
	}

	missing := make([]string, 0)
	for code := range used {
		if !seeded[code] {
			missing = append(missing, code)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("这些权限码在路由上用了、但 Seed 之后库里没有：%s\n"+
			"\t也就是说任何角色都拿不到它们，对应的接口谁都调不了（403）。\n"+
			"\t最常见的原因是菜单 ID 撞了，被清单里后面那条覆盖掉",
			strings.Join(missing, ", "))
	}

	// 反方向只提示不报错：种了码但还没在路由上用，通常是「功能做了一半」，
	// 也可能是前端 v-perm 在用（那不在 main.go 里），所以不当失败处理
	var unused []string
	for code := range seeded {
		if !used[code] {
			unused = append(unused, code)
		}
	}
	sort.Strings(unused)
	if len(unused) > 0 {
		t.Logf("提示：这些码种了但路由上没用到（可能只被前端 v-perm 使用）：%s",
			strings.Join(unused, ", "))
	}
}
