package model

// 布尔默认值陷阱的守卫测试。
//
// 规则写在 model.go 的包注释里：布尔字段不要加 gorm:"default:true"。
// 这个测试用 go/ast 解析 model.go 本身，把还带着这个标签的字段与下面的清单比对：
//
//   - 出现清单之外的字段 → 测试失败。这通常意味着新加的模型又踩了同一个坑，
//     而这个坑的表现是「开关关不掉」且不报错，靠人 review 发现过五次都没发现。
//   - 清单里的字段已经不带标签了 → 测试也失败，提醒把它从清单里删掉，
//     否则清单会慢慢变成一份没人信的过期文档。
//
// 用解析源码而不是反射：反射拿不到「这个包里一共有哪些模型」（没有注册表），
// 而 AST 能保证一个都不漏。

import (
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"strings"
	"testing"
)

// auditedPending 还带着 gorm:"default:true" 的布尔字段。
//
// 它们**不是漏掉的**：这一轮只动了已经逐个确认过「所有建记录的地方都显式赋值」
// 的那 14 个字段（ExposureTarget / ApiToken / Certificate / Probe / ConfigFile /
// HostService / LdapServer 七个模型）。剩下这些要一起改的风险在于：
// 去掉标签之后，任何**没有显式赋值**的建记录路径会插进 false ——
// 那就从「关不掉」变成了「新建出来就是停用的」，同样安静、同样难查。
//
// 所以每一条都要单独确认建记录路径后再动。这份清单的作用是让它们可见、可数、
// 并且在有人新增同类字段时拦一次。
var auditedPending = map[string]string{
	"SiteLink.Enabled":           "站点导航条目",
	"EmailTemplate.Enabled":      "邮件模板；种子数据里有 Builtin 记录，改前要确认 seed 路径",
	"OnCallSchedule.Enabled":     "值班表",
	"Credential.Enabled":         "凭证库；停用会让引用它的主机连不上，改动要谨慎",
	"CommandRule.Enabled":        "命令规则",
	"CronJob.Enabled":            "定时任务",
	"FirewallRule.Enabled":       "防火墙规则模板",
	"FirewallGroup.Enabled":      "安全组",
	"AlertSource.Enabled":        "告警接入源；关掉等于拒收外部推送",
	"AlertSilence.Enabled":       "静默 / 维护窗口",
	"AggregationPolicy.Enabled":  "聚合策略",
	"NotifyChannel.Enabled":      "通知渠道",
	"NotifyRoute.Enabled":        "通知路由",
	"CloudAccount.Enabled":       "云账号",
	"BuildServer.Enabled":        "Jenkins 接入",
	"BuildJob.Enabled":           "构建任务",
	"Runbook.Enabled":            "预案",
	"Script.Enabled":             "脚本库",
	"DetectionRule.Enabled":      "检测规则",
	"MetricSource.Enabled":       "指标数据源",
	"LogSource.Enabled":          "日志数据源",
	"TraceSource.Enabled":        "链路数据源",
	"SavedMetricQuery.RangeMode": "保存的指标查询：区间还是瞬时",
	"KubeCluster.Enabled":        "容器集群接入",
	"ModelUpstream.Enabled":      "模型上游",
	"AgentConfig.Enabled":        "AI 助手配置",
	"ImApp.DisableMissing":       "IM 同步：目录里没有的人是否停用",
	"ImApp.Enabled":              "IM 应用",
	"Signature.Enabled":          "特征库条目",
}

// auditedFixed 这一轮确认过建记录路径、已经去掉标签的字段。
// 它们要是又冒出标签，说明有人把补丁改回去了。
var auditedFixed = []string{
	"ExposureTarget.AlertEnabled", "ExposureTarget.Enabled",
	"ApiToken.ReadOnly", "ApiToken.Enabled",
	"Certificate.AlertEnabled", "Certificate.Enabled",
	"Probe.AlertEnabled", "Probe.Enabled",
	"ConfigFile.AlertEnabled",
	"HostService.ExpectActive", "HostService.ExpectEnabled", "HostService.AlertEnabled",
	"LdapServer.Enabled", "LdapServer.LoginEnabled",
}

// boolDefaultTrueFields 解析 model.go，找出所有带 default:true 的布尔字段
func boolDefaultTrueFields(t *testing.T) map[string]bool {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "model.go", nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("解析 model.go 失败: %v", err)
	}

	found := map[string]bool{}
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
				found[spec.Name.Name+"."+name.Name] = true
			}
		}
		return true
	})
	return found
}

// 没有清单之外的布尔字段带 default:true
func TestNoUnauditedBoolDefaultTrue(t *testing.T) {
	found := boolDefaultTrueFields(t)

	unexpected := make([]string, 0)
	for name := range found {
		if _, ok := auditedPending[name]; !ok {
			unexpected = append(unexpected, name)
		}
	}
	sort.Strings(unexpected)
	if len(unexpected) > 0 {
		t.Fatalf(`这些布尔字段带了 gorm:"default:true"，但不在 auditedPending 清单里：
  %s

这个标签会让 GORM 在 Create 时把 false 从 INSERT 里省掉，数据库默认值把它翻回 true ——
表现是「界面上的开关关不掉」，而且不报错。做法二选一：
  1) 去掉标签（推荐），并确认所有建这条记录的地方都显式给这个字段赋值；
  2) 确实要留着的话，把它加到 auditedPending 并写明理由。
细节见 model.go 的包注释。`, strings.Join(unexpected, "\n  "))
	}
}

// 清单不能过期：已经改好的字段要从 pending 里消失，改回去要被发现
func TestBoolDefaultAuditListsStayAccurate(t *testing.T) {
	found := boolDefaultTrueFields(t)

	for _, name := range auditedFixed {
		if found[name] {
			t.Errorf("%s 又带上了 gorm:\"default:true\" —— 这个字段这一轮刚确认过建记录路径并去掉标签", name)
		}
		if _, ok := auditedPending[name]; ok {
			t.Errorf("%s 同时出现在 auditedFixed 与 auditedPending 里，清单自相矛盾", name)
		}
	}

	stale := make([]string, 0)
	for name := range auditedPending {
		if !found[name] {
			stale = append(stale, name)
		}
	}
	sort.Strings(stale)
	if len(stale) > 0 {
		t.Fatalf(`这些字段已经不带 default:true 了，请把它们从 auditedPending 移到 auditedFixed：
  %s
（清单如果不跟着代码走，很快就会变成一份没人信的过期文档）`, strings.Join(stale, "\n  "))
	}
}

// 报一个数，方便在 CI 日志里看这笔债还剩多少
func TestBoolDefaultDebtCount(t *testing.T) {
	found := boolDefaultTrueFields(t)
	t.Logf("仍带 gorm:\"default:true\" 的布尔字段：%d 个；本轮已确认并修好：%d 个",
		len(found), len(auditedFixed))
	if len(found) != len(auditedPending) {
		t.Fatalf("实际 %d 个，清单 %d 条，两边对不上", len(found), len(auditedPending))
	}
}
