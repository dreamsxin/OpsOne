package main

// 路由授权清单测试。
//
// # 这条测试在守什么
//
// 审计时发现：`auth` 组 565 条路由里有 234 条没有任何权限码，其中 217 条是 GET。
// 写接口是被 `RequirePerm` 管住的，**读接口几乎全靠「登录即可」** —— 包括
// 审计日志本身（还能导出 CSV）、别人敲过的每一条命令、凭据与密码库清单、
// 配置文件正文。也就是说「只读账号」在这个平台上等于「能看全部」。
//
// 这一轮给 71 条敏感读接口补了权限码。剩下的 160 条**不是漏掉的**，
// 每一条都在下面的清单里带着理由。这条测试的作用是：
// 以后新加一个 auth 路由，要么带权限码，要么进清单并写明为什么 ——
// 否则测试失败。不这么做的话，下一个新增的读接口会重新变成「登录即可」，
// 而这种漏洞在页面上完全看不出来。
//
// # 为什么不是「所有 GET 都加码」
//
// 因为那样会把工作台、告警看板、事件列表这类**所有人都该看到的**页面一起挡住，
// 结果是运维给每个人都配上全部权限码，等于没有权限。真正的判断标准是
// 「这份数据泄露出去会不会造成实际损害」，所以下面按理由分类，逐条过一遍。

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// 豁免理由。每一类都必须回答「为什么不加码是安全的」。
const (
	// selfService 只返回/修改当前登录者自己的东西，handler 里用 CurrentUser 取主体
	selfService = "只涉及当前登录者自己的数据"
	// dashboard 聚合计数，不含明细与地址、口令
	dashboard = "只有聚合计数，不含明细与凭据"
	// lookup 下拉选项、字典、模板，被多个页面共用；加码会让正常配置流程断掉
	lookup = "下拉选项/字典，被多个页面共用"
	// scoped handler 里走 applyScope / ResourceGrant，看得到什么由数据范围与授权决定
	scoped = "已由数据范围+资源授权过滤"
	// kubeGrant 走 requireKubeCluster + KubeGrant（注意 kube.grant_enforce 默认关，
	// 这一点写在 SECURITY 第 44 节，开启前所有登录用户可读集群）
	kubeGrant = "由 KubeGrant 管控（默认未开启，见 SECURITY 44）"
	// selfDiagnose 查自己的授权允许，查别人要权限——判断在 handler 里
	selfDiagnose = "自查允许、查别人要权限（handler 内判断）"
	// externalData 转发到内网 Prometheus/Loki/Jaeger 的只读查询，
	// 内容是被监控对象的数据，不含平台自身凭据
	externalData = "转发内网数据源的只读查询，不含平台凭据"
	// opsRead 运维协作数据：告警、事件、复盘、剧本、拨测。全员可见是刻意的——
	// 值班的人要能看到别人建的规则与正在烧的告警，否则交接不了
	opsRead = "运维协作数据，全员可见是刻意的"
	// previewOnly 预览/试算/连通性测试，不落库、不产生副作用
	previewOnly = "预览或试算，不落库不产生副作用"
)

type routeExempt struct {
	Method string
	Path   string
	Reason string
}

// routeNoPermAllowlist 允许没有权限码的 auth 路由。
//
// 加一条前先问自己：**这份数据落到一个只有只读账号的人手里，会出事吗？**
// 会就别往这里加，去加权限码。
var routeNoPermAllowlist = []routeExempt{
	// ---- 自助 ----
	{"GET", "/me", selfService},
	{"GET", "/me/menus", selfService},
	{"PUT", "/me/password", selfService},
	{"GET", "/me/whoami", selfService},
	{"GET", "/me/workbench", selfService},
	{"GET", "/me/resources", selfService},
	{"GET", "/me/activity", selfService},
	{"GET", "/me/sessions", selfService},
	{"GET", "/me/exec-jobs", selfService},
	{"GET", "/me/messages", selfService},
	{"GET", "/me/messages/summary", selfService},
	{"POST", "/me/messages/:id/read", selfService},
	{"POST", "/me/messages/read-all", selfService},
	{"GET", "/me/totp", selfService},
	{"POST", "/me/totp/setup", selfService},
	{"POST", "/me/totp/confirm", selfService},
	{"POST", "/me/totp/disable", selfService},
	{"GET", "/announcements/published", selfService},
	{"GET", "/security/awareness/my", selfService},
	{"GET", "/security/awareness/my/:id", selfService},
	{"POST", "/security/awareness/my/:id/read", selfService},
	{"POST", "/security/awareness/my/:id/quiz", selfService},

	// ---- 看板与统计 ----
	{"GET", "/dashboard/stats", dashboard},
	{"GET", "/monitor/health", dashboard},
	{"GET", "/monitor/wallboard", dashboard},
	{"GET", "/security/overview", dashboard},
	{"GET", "/host-services/stats", dashboard},
	{"GET", "/security/events/stats", dashboard},
	{"GET", "/alerts/stats", dashboard},
	{"GET", "/fixed-assets/stats", dashboard},
	{"GET", "/cloud-resources/stats", dashboard},
	{"GET", "/domains/stats", dashboard},
	{"GET", "/certificates/stats", dashboard},
	{"GET", "/monitor/events/stats", dashboard},
	{"GET", "/monitor/reviews/stats", dashboard},
	{"GET", "/monitor/runbooks/stats", dashboard},

	// ---- 字典与下拉 ----
	{"GET", "/tags", lookup},
	{"GET", "/site-links", lookup},
	{"GET", "/alert-sources", lookup},
	{"GET", "/system/companies", lookup},
	{"GET", "/system/departments/tree", lookup},
	{"GET", "/notify/template-vars", lookup},
	{"GET", "/monitor/detection-rules/meta", lookup},
	{"GET", "/monitor/host-logs/meta", lookup},
	{"GET", "/monitor/aggregation/dimensions", lookup},
	{"GET", "/monitor/rule-version-targets", lookup},
	{"GET", "/monitor/oncall/candidates", lookup},
	{"GET", "/monitor/metrics/names", lookup},
	{"GET", "/exec/scripts/categories", lookup},
	{"GET", "/hosts/import-template", lookup},
	{"GET", "/kube/resource-kinds", lookup},

	// ---- 已由数据范围/资源授权过滤 ----
	{"GET", "/hosts", scoped},
	{"GET", "/hosts/:id/report", scoped},
	{"GET", "/hosts/metrics", scoped},
	{"GET", "/hosts/:id/metrics", scoped},
	{"GET", "/databases", scoped},
	{"POST", "/databases/:id/check", scoped},
	{"GET", "/domains", scoped},
	{"GET", "/certificates", scoped},
	{"GET", "/fixed-assets", scoped},
	{"GET", "/cloud-resources", scoped},
	{"GET", "/cloud-resources/drift", scoped},
	{"GET", "/cloud-sync-runs", scoped},
	{"GET", "/inventory/batches", scoped},
	{"GET", "/inventory/batches/:id", scoped},
	{"GET", "/purchase/orders", scoped},
	{"GET", "/purchase/orders/:id", scoped},
	{"GET", "/build/jobs", scoped},
	{"GET", "/build/records", scoped},

	// ---- 容器：由 KubeGrant 管控 ----
	{"GET", "/kube/grant-state", kubeGrant},
	{"GET", "/kube/clusters", kubeGrant},
	{"GET", "/kube/clusters/:id/nodes", kubeGrant},
	{"GET", "/kube/clusters/:id/namespaces", kubeGrant},
	{"GET", "/kube/clusters/:id/workloads", kubeGrant},
	{"GET", "/kube/clusters/:id/pods", kubeGrant},
	{"GET", "/kube/clusters/:id/pod", kubeGrant},
	{"GET", "/kube/clusters/:id/events", kubeGrant},
	{"GET", "/kube/clusters/:id/resources", kubeGrant},
	{"GET", "/kube/clusters/:id/resource/events", kubeGrant},
	{"GET", "/kube/clusters/:id/resource/pods", kubeGrant},
	{"GET", "/kube/clusters/:id/capacity", kubeGrant},
	{"GET", "/kube/clusters/:id/helm-releases", kubeGrant},
	{"GET", "/kube/clusters/:id/crds", kubeGrant},
	{"GET", "/kube/clusters/:id/crd-resources", kubeGrant},
	{"GET", "/kube/clusters/:id/gateway-routes", kubeGrant},
	{"GET", "/kube/clusters/:id/node-inventory", kubeGrant},
	{"GET", "/kube/clusters/:id/namespace-inventory", kubeGrant},

	// ---- 自查诊断 ----
	{"GET", "/system/data-permission/diagnose/:id", selfDiagnose},
	{"GET", "/resource-grants/diagnose/:id", selfDiagnose},
	{"GET", "/kube/grants/diagnose/:id", selfDiagnose},

	// ---- 转发外部数据源 ----
	{"GET", "/monitor/metrics/query", externalData},
	{"GET", "/monitor/metrics/query-range", externalData},
	{"GET", "/monitor/metrics/saved", externalData},
	{"GET", "/monitor/logs/query", externalData},
	{"GET", "/monitor/logs/labels", externalData},
	{"GET", "/monitor/traces/services", externalData},
	{"GET", "/monitor/traces/search", externalData},
	{"GET", "/monitor/traces/detail/:traceId", externalData},

	// ---- 预览/试算 ----
	{"POST", "/system/command-rules/test", previewOnly},
	{"POST", "/system/email-templates/:id/preview", previewOnly},
	{"POST", "/notify/templates/:id/preview", previewOnly},
	{"POST", "/notify/routes/test", previewOnly},
	{"POST", "/monitor/silences/preview", previewOnly},
	{"POST", "/security/signatures/:id/port-check", previewOnly},
	{"POST", "/exec/scripts/:id/precheck", previewOnly},
	{"POST", "/exec/scripts/:id/render", previewOnly},

	// ---- 运维协作数据 ----
	{"GET", "/host-services", opsRead},
	{"GET", "/host-services/actions", opsRead},
	{"GET", "/security/awareness/courses", opsRead},
	{"GET", "/security/signatures", opsRead},
	{"GET", "/security/signatures/reconcile", opsRead},
	{"GET", "/security/events", opsRead},
	{"GET", "/security/events/:id", opsRead},
	{"GET", "/security/event-mutes", opsRead},
	{"GET", "/security/suggestions", opsRead},
	{"GET", "/security/suggestion-dismissals", opsRead},
	{"GET", "/alerts", opsRead},
	{"GET", "/alerts/situation", opsRead},
	{"GET", "/alerts/:id", opsRead},
	{"GET", "/notify/templates", opsRead},
	{"GET", "/notify/routes", opsRead},
	{"GET", "/system/announcements", opsRead},
	{"GET", "/monitor/alert-rules", opsRead},
	{"GET", "/monitor/alert-rules/metrics", opsRead},
	{"GET", "/monitor/rule-versions", opsRead},
	{"GET", "/monitor/rule-versions/diff", opsRead},
	{"GET", "/monitor/rule-versions/:id", opsRead},
	{"GET", "/monitor/detection-rules", opsRead},
	{"GET", "/monitor/oncall/escalations", opsRead},
	{"GET", "/monitor/oncall/schedules", opsRead},
	{"GET", "/monitor/oncall/schedules/:id/preview", opsRead},
	{"GET", "/monitor/oncall/schedules/:id/overrides", opsRead},
	{"GET", "/monitor/probes", opsRead},
	{"GET", "/monitor/probe-records", opsRead},
	{"GET", "/monitor/exposures", opsRead},
	{"GET", "/monitor/exposure-scans", opsRead},
	{"GET", "/monitor/aggregation", opsRead},
	{"GET", "/monitor/aggregation/overlaps", opsRead},
	{"GET", "/monitor/aggregation/:id/preview", opsRead},
	{"GET", "/monitor/silences", opsRead},
	{"GET", "/monitor/silences/:id/hits", opsRead},
	{"GET", "/monitor/topology/resources", opsRead},
	{"GET", "/monitor/topologies", opsRead},
	{"GET", "/monitor/topologies/:id", opsRead},
	{"GET", "/monitor/events", opsRead},
	{"GET", "/monitor/events/:id", opsRead},
	{"GET", "/monitor/events/:id/review", opsRead},
	{"GET", "/monitor/events/:id/review/export", opsRead},
	{"GET", "/monitor/events/:id/evidence", opsRead},
	{"GET", "/monitor/reviews", opsRead},
	{"GET", "/monitor/action-items", opsRead},
	{"GET", "/monitor/runbooks", opsRead},
	{"GET", "/monitor/runbooks/match", opsRead},
	{"GET", "/monitor/runbooks/:id", opsRead},
	{"GET", "/monitor/runbook-uses", opsRead},
	{"GET", "/monitor/sla-settings", opsRead},
	{"GET", "/exec/scripts", opsRead},
	{"GET", "/ai/agents", opsRead},
	{"GET", "/ai/agent-runs", opsRead},
	{"GET", "/ai/agent-runs/:id", opsRead},
}

var routeLine = regexp.MustCompile(`^\s+auth\.(GET|POST|PUT|DELETE|PATCH)\("([^"]+)"`)

// TestEveryAuthRouteIsGatedOrExempted 扫 main.go 源码。
//
// 用扫源码而不是遍历 gin 的路由树，是因为路由树上拿不到「这条路由挂了哪个中间件」——
// gin 把中间件合并进 HandlersChain 之后只剩函数指针，认不出是哪个权限码。
//
// 另外会拒绝「注释后面出现路由注册」的行。这一条是踩出来的：
// 用脚本插入路由时换行没生效，三条路由全落到了一行注释后面 ——
// **编译能过、这条测试当时也过了，但接口在运行时是 404**。
func TestEveryAuthRouteIsGatedOrExempted(t *testing.T) {
	raw, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("读 main.go 失败: %v", err)
	}

	allowed := make(map[string]string, len(routeNoPermAllowlist))
	for _, item := range routeNoPermAllowlist {
		key := item.Method + " " + item.Path
		if _, dup := allowed[key]; dup {
			t.Fatalf("豁免清单里 %s 重复了", key)
		}
		allowed[key] = item.Reason
	}

	seen := make(map[string]bool, len(allowed))
	var gated, exempted int
	for lineNo, line := range strings.Split(string(raw), "\n") {
		if idx := strings.Index(line, "//"); idx >= 0 {
			for _, verb := range []string{"auth.GET(", "auth.POST(", "auth.PUT(", "auth.DELETE("} {
				if strings.Contains(line[idx:], verb) {
					t.Errorf("main.go:%d 注释后面出现了路由注册，这条路由实际不会生效：\n\t%s",
						lineNo+1, strings.TrimSpace(line))
					break
				}
			}
		}
		m := routeLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		key := m[1] + " " + m[2]
		if strings.Contains(line, "RequirePerm") || strings.Contains(line, "RequireAnyPerm") {
			gated++
			if _, ok := allowed[key]; ok {
				t.Errorf("%s 已经有权限码了，请从豁免清单里删掉这一条", key)
			}
			continue
		}
		if _, ok := allowed[key]; !ok {
			t.Errorf("%s 既没有权限码，也不在豁免清单里。\n"+
				"\t要么加 middleware.RequirePerm(\"xxx\")，"+
				"要么把它加进 routeNoPermAllowlist 并写明理由。\n"+
				"\t判断标准：这份数据落到一个只有只读账号的人手里会不会出事", key)
			continue
		}
		seen[key] = true
		exempted++
	}

	if gated == 0 || exempted == 0 {
		t.Fatalf("扫描结果不对（有码 %d 条、豁免 %d 条），正则可能失效了", gated, exempted)
	}
	// 清单别过期：列了却已经不存在的路由要删掉，否则它会掩盖同名新路由
	for key := range allowed {
		if !seen[key] {
			t.Errorf("豁免清单里的 %s 在 main.go 里已经不存在了，请删掉这一条", key)
		}
	}
}

// TestSensitiveReadsAreGated 把审计里点名的几条单独钉住。
//
// 它们是这一轮修复的起点，值得有一条测试直接以路径为单位断言 ——
// 上面那条通用测试只保证「有码或有理由」，不保证**这几条**没被人加进豁免清单。
func TestSensitiveReadsAreGated(t *testing.T) {
	raw, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("读 main.go 失败: %v", err)
	}
	text := string(raw)

	mustGate := map[string]string{
		// 审计对被审计的人必须不透明
		`auth.GET("/system/audit-logs"`:        "audit:view",
		`auth.GET("/system/audit-logs/export"`: "audit:view",
		// 命令明细里出现口令、token 是常事
		`auth.GET("/sessions/commands"`:        "session:view",
		`auth.GET("/sessions/commands/export"`: "session:view",
		`auth.GET("/sessions/:id/commands"`:    "session:view",
		// 凭据与密码库清单
		`auth.GET("/credentials"`:    "credential:manage",
		`auth.GET("/vault/accounts"`: "vault:manage",
		// 配置文件正文
		`auth.GET("/config-files/:id"`:    "configfile:manage",
		`auth.GET("/config-versions/:id"`: "configfile:manage",
		// 2FA 库的取用要独立的码：平台把口令和 2FA 种子分两个库存，
		// 取用权限却曾经合成一个 vault:reveal —— 双因子的分离在权限层面被抹掉了
		`auth.POST("/vault/totps/:id/code"`:      "totp:reveal",
		`auth.POST("/vault/totps/:id/uri"`:       "totp:reveal",
		`auth.POST("/vault/accounts/:id/reveal"`: "vault:reveal",
	}
	for needle, code := range mustGate {
		idx := strings.Index(text, needle)
		if idx < 0 {
			t.Errorf("找不到路由 %s（改名了？那请同步改这条测试）", needle)
			continue
		}
		lineEnd := strings.Index(text[idx:], "\n")
		line := text[idx : idx+lineEnd]
		if !strings.Contains(line, code) {
			t.Errorf("%s 必须带 %s，当前是：%s", needle, code, strings.TrimSpace(line))
		}
	}
}
