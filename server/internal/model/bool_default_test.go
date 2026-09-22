package model

// 布尔默认值陷阱的守卫测试。
//
// 规则写在 model.go 的包注释里：**布尔字段不加 gorm:"default:true"**。
// 这个测试用 go/ast 解析 model.go 本身，断言一个都没有。
//
// 为什么用解析源码而不是反射：反射拿不到「这个包里一共有哪些模型」（没有注册表），
// 而 AST 能保证一个都不漏 —— 包括还没接进 AutoMigrate 的新模型。
//
// 这个坑的历史：先后被踩了五次（Domain.AlertEnabled、NotifyTemplate.Enabled、
// AlertRule.Enabled 各被一条测试逮到过；另有七处在 handler 里各写了一遍
// 「插完再 Updates 一次」的补丁）。清完之后补丁也一并删掉了，
// 剩下这个测试负责让它不再回来。

import (
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"strings"
	"testing"
)

// boolDefaultTrueFields 解析 model.go，找出所有带 default:true 的布尔字段
func boolDefaultTrueFields(t *testing.T) []string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "model.go", nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("解析 model.go 失败: %v", err)
	}

	found := make([]string, 0)
	ast.Inspect(file, func(n ast.Node) bool {
		spec, ok := n.(*ast.TypeSpec)
		if !ok {
			return true
		}
		structType, ok := spec.Type.(*ast.StructType)
		if !ok {
			return true
		}
		for _, field := range structType.Fields.List {
			ident, ok := field.Type.(*ast.Ident)
			if !ok || ident.Name != "bool" || field.Tag == nil {
				continue
			}
			if !strings.Contains(field.Tag.Value, "default:true") {
				continue
			}
			for _, name := range field.Names {
				found = append(found, spec.Name.Name+"."+name.Name)
			}
		}
		return true
	})
	sort.Strings(found)
	return found
}

// 一个都不能有
func TestNoBoolDefaultTrue(t *testing.T) {
	found := boolDefaultTrueFields(t)
	if len(found) == 0 {
		return
	}
	t.Fatalf(`这些布尔字段带了 gorm:"default:true"：
  %s

GORM 对带 default 的字段，Create 时会把零值从 INSERT 里省掉让数据库填默认值。
布尔零值就是 false，于是「用户在界面上把开关关掉」变成「数据库把它填回 true」——
表现是关不掉，而且不报错。这个坑在这个项目里被踩过五次，全部清掉了，别再加回来。

做法：去掉标签，并确认**所有建这条记录的地方都显式给这个字段赋值**
（平台的 CRUD handler 本来就是这么写的：先 enabled := true，再按请求里的 *bool 覆盖）。
如果确实需要数据库层面的默认值，先想清楚「用户显式传 false」这条路径会发生什么。
细节见 model.go 的包注释。`, strings.Join(found, "\n  "))
}

// 顺带确认 default:false 没被误伤 —— 那个方向没有问题（零值与默认值一致），
// 平台里有几处是刻意写的，别在清理时一起删掉
func TestBoolDefaultFalseIsFine(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "model.go", nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("解析 model.go 失败: %v", err)
	}
	count := 0
	ast.Inspect(file, func(n ast.Node) bool {
		field, ok := n.(*ast.Field)
		if !ok || field.Tag == nil {
			return true
		}
		if ident, ok := field.Type.(*ast.Ident); ok && ident.Name == "bool" &&
			strings.Contains(field.Tag.Value, "default:false") {
			count++
		}
		return true
	})
	if count == 0 {
		t.Log("当前没有 default:false 的布尔字段（不是问题，只是记一笔）")
	} else {
		t.Logf("default:false 的布尔字段 %d 个：零值与默认值一致，不受这个坑影响", count)
	}
}
