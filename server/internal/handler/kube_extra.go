package handler

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"

	"ops-platform/server/internal/k8s"
	"ops-platform/server/internal/response"
)

// 容器平台的三个按类型专用页：Helm 应用、自定义资源（含 Gateway API）、RBAC 账户。
//
// 通用「资源管理」页是按白名单 18 种类型的 YAML 浏览器，这三类它答不了：
//   - Helm release 藏在 Secret 里（而 Secret 在平台上是脱敏只读的，看不到载荷）
//   - CRD 每个集群都不一样，编译期的白名单列不出来
//   - 「这个 ServiceAccount 实际能干什么」要反查 binding → role → rules
//
// 共同底线：**全部只读**。写操作仍然走 kubectl / helm，理由在 k8s/discovery.go 开头。

// kubeCRDInstanceLimit 单个 CRD 一次最多返回多少实例。
// 有些 CRD（比如 Argo 的 Workflow）实例数能上万，全拉回来会把浏览器卡死
const kubeCRDInstanceLimit = 500

// ---------- Helm 应用 ----------

// KubeHelmReleases 列 Helm release。
//
// 数据来源是集群里 `type=helm.sh/release.v1` 的 Secret，解出来的是真实的 release 清单，
// 不需要 helm 二进制、也不需要平台所在机器能访问 chart 仓库。
func (h *Handler) KubeHelmReleases(c *gin.Context) {
	_, client, ok := h.requireKubeCluster(c)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), kubeCallTimeout)
	defer cancel()

	namespace := strings.TrimSpace(c.Query("namespace"))
	releases, err := client.ListHelmReleases(ctx, namespace)
	if err != nil {
		response.Error(c, "读取 Helm release 失败: "+err.Error())
		return
	}

	if keyword := strings.ToLower(strings.TrimSpace(c.Query("keyword"))); keyword != "" {
		filtered := releases[:0:0]
		for _, item := range releases {
			if strings.Contains(strings.ToLower(item.Name), keyword) ||
				strings.Contains(strings.ToLower(item.Chart), keyword) {
				filtered = append(filtered, item)
			}
		}
		releases = filtered
	}

	byStatus := map[string]int{}
	for _, item := range releases {
		byStatus[item.Status]++
	}

	response.OK(c, gin.H{
		"releases": releases,
		"byStatus": byStatus,
		"total":    len(releases),
		"notes": []string{
			"数据直接来自集群里 type=helm.sh/release.v1 的 Secret，不依赖 helm 二进制，也不访问 chart 仓库",
			"只认 Helm 3 默认的 secret driver；用 configmap driver 存 release 的集群在这里看不到",
			"同一个 release 的多个历史版本只显示最新那个，「历史」列是它一共留了几个版本",
			"**只读**：install / upgrade / rollback / uninstall 都不做 —— 那需要真正的 Helm 引擎" +
				"（模板渲染、钩子、依赖、CRD 处理），半套实现比没有更危险",
			"不返回 values 与渲染后的 manifest：values 里常有数据库口令，manifest 动辄几百 KB",
		},
	})
}

// ---------- 自定义资源 ----------

// KubeCRDs 列集群里的全部 CRD。
func (h *Handler) KubeCRDs(c *gin.Context) {
	_, client, ok := h.requireKubeCluster(c)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), kubeCallTimeout)
	defer cancel()

	crds, err := client.ListCRDs(ctx)
	if err != nil {
		response.Error(c, "读取 CRD 失败: "+err.Error()+
			"（需要对 apiextensions.k8s.io 的 customresourcedefinitions 有 list 权限）")
		return
	}

	if keyword := strings.ToLower(strings.TrimSpace(c.Query("keyword"))); keyword != "" {
		filtered := crds[:0:0]
		for _, item := range crds {
			if strings.Contains(strings.ToLower(item.Name), keyword) ||
				strings.Contains(strings.ToLower(item.Kind), keyword) ||
				strings.Contains(strings.ToLower(item.KnownAs), keyword) {
				filtered = append(filtered, item)
			}
		}
		crds = filtered
	}

	// 按组汇总，界面上先按组折叠再看具体 kind
	groups := map[string]int{}
	var notEstablished int
	for _, item := range crds {
		groups[item.Group]++
		if !item.Established {
			notEstablished++
		}
	}
	groupNames := make([]string, 0, len(groups))
	for name := range groups {
		groupNames = append(groupNames, name)
	}
	sort.Strings(groupNames)

	response.OK(c, gin.H{
		"crds":           crds,
		"groups":         groupNames,
		"groupCounts":    groups,
		"total":          len(crds),
		"notEstablished": notEstablished,
		"gatewayAPI":     gatewayAPIVersion(crds),
		"notes": []string{
			"CRD 是运行时从集群里读出来的，不是平台内置清单 —— 装了什么就能看到什么",
			"自定义资源一律**只读**：平台不懂它们的语义，改错一条 Gateway 或 Application 的代价太大",
			"「未就绪」指 CRD 自己的 Established 条件不为 True，查它的实例会失败 —— " +
				"那是 CRD 没装好，不是查不到东西",
			"多版本 CRD 默认查 storage 版本；可以切到其它 served 版本，但查未在服务的版本只会 404",
			fmt.Sprintf("单个类型一次最多返回 %d 个实例，超出会提示缩小范围", kubeCRDInstanceLimit),
		},
	})
}

// gatewayAPIVersion 从 CRD 清单里找出 Gateway API 的 HTTPRoute 版本。
// 没装就返回空串 —— 界面据此显示「这个集群没装 Gateway API」而不是一张空表。
func gatewayAPIVersion(crds []k8s.CRDInfo) string {
	for _, item := range crds {
		if item.Group != "gateway.networking.k8s.io" || item.Kind != "HTTPRoute" {
			continue
		}
		if item.StorageVersion != "" {
			return item.StorageVersion
		}
		if len(item.ServedVersions) > 0 {
			return item.ServedVersions[0]
		}
	}
	return ""
}

// requireCRD 按 CRD 全名取定义，并构造出可查实例的资源类型
func (h *Handler) requireCRD(c *gin.Context, client *k8s.Client) (k8s.CRDInfo, k8s.ResourceKind, bool) {
	name := strings.TrimSpace(c.Query("crd"))
	if name == "" {
		response.BadRequest(c, "请指定 CRD（形如 applications.argoproj.io）")
		return k8s.CRDInfo{}, k8s.ResourceKind{}, false
	}

	ctx, cancel := context.WithTimeout(context.Background(), kubeCallTimeout)
	defer cancel()
	crds, err := client.ListCRDs(ctx)
	if err != nil {
		response.Error(c, "读取 CRD 失败: "+err.Error())
		return k8s.CRDInfo{}, k8s.ResourceKind{}, false
	}

	for _, item := range crds {
		if !strings.EqualFold(item.Name, name) {
			continue
		}
		if !item.Established {
			response.BadRequest(c, fmt.Sprintf(
				"CRD %s 还没就绪（Established 不为 True），查它的实例会失败 —— 先去看 CRD 本身的状态", item.Name))
			return k8s.CRDInfo{}, k8s.ResourceKind{}, false
		}
		kind, err := k8s.KindForCRD(item, strings.TrimSpace(c.Query("version")))
		if err != nil {
			response.BadRequest(c, err.Error())
			return k8s.CRDInfo{}, k8s.ResourceKind{}, false
		}
		return item, kind, true
	}
	response.NotFound(c, "集群里没有这个 CRD: "+name)
	return k8s.CRDInfo{}, k8s.ResourceKind{}, false
}

// KubeCRDResources 列某个自定义资源类型的实例
func (h *Handler) KubeCRDResources(c *gin.Context) {
	_, client, ok := h.requireKubeCluster(c)
	if !ok {
		return
	}
	crd, kind, ok := h.requireCRD(c, client)
	if !ok {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), kubeCallTimeout)
	defer cancel()
	items, err := client.ListObjectsQuery(ctx, kind, k8s.ListQuery{
		Namespace:     strings.TrimSpace(c.Query("namespace")),
		LabelSelector: strings.TrimSpace(c.Query("labelSelector")),
	})
	if err != nil {
		response.Error(c, fmt.Sprintf("读取 %s 失败: %s", kind.Kind, err.Error()))
		return
	}

	total := len(items)
	if keyword := strings.ToLower(strings.TrimSpace(c.Query("keyword"))); keyword != "" {
		filtered := items[:0:0]
		for _, item := range items {
			if strings.Contains(strings.ToLower(item.Name), keyword) ||
				strings.Contains(strings.ToLower(item.Namespace), keyword) {
				filtered = append(filtered, item)
			}
		}
		items = filtered
	}
	truncated := false
	if len(items) > kubeCRDInstanceLimit {
		items = items[:kubeCRDInstanceLimit]
		truncated = true
	}

	response.OK(c, gin.H{
		"items": items, "total": total, "shown": len(items), "truncated": truncated,
		"crd":        crd,
		"kind":       kind.Kind,
		"apiVersion": kind.APIVersion,
		"namespaced": kind.Namespaced,
		// 自定义资源没有平台认得的摘要算法，列表里的 summary 会是空的 ——
		// 说清这一点，免得被当成「数据没读出来」
		"note": "自定义资源的摘要平台算不出来（不懂它们的 spec 语义），列表只给名字、命名空间与创建时间；" +
			"点进去看完整 YAML",
	})
}

// KubeCRDResourceDetail 取一个自定义资源实例的完整 YAML
func (h *Handler) KubeCRDResourceDetail(c *gin.Context) {
	_, client, ok := h.requireKubeCluster(c)
	if !ok {
		return
	}
	crd, kind, ok := h.requireCRD(c, client)
	if !ok {
		return
	}
	name := strings.TrimSpace(c.Query("name"))
	if name == "" {
		response.BadRequest(c, "请指定对象名")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), kubeCallTimeout)
	defer cancel()
	obj, err := client.GetObject(ctx, kind, strings.TrimSpace(c.Query("namespace")), name)
	if err != nil {
		response.Error(c, fmt.Sprintf("读取 %s/%s 失败: %s", kind.Kind, name, err.Error()))
		return
	}
	// 自定义资源的 status 常常是最有价值的部分（Argo 的同步状态、
	// cert-manager 的签发结果），所以这里**保留 status**，不用 ToEditableYAML 清掉
	yamlText, err := k8s.ToFullYAML(obj)
	if err != nil {
		response.Error(c, "序列化失败: "+err.Error())
		return
	}
	response.OK(c, gin.H{
		"crd": crd.Name, "kind": kind.Kind, "apiVersion": kind.APIVersion,
		"name": name, "namespace": strings.TrimSpace(c.Query("namespace")),
		"yaml": string(yamlText),
		"note": "完整原文，**包含 status** —— 自定义资源的 status 往往才是要看的那部分。" +
			"平台不提供自定义资源的编辑与提交",
	})
}

// ---------- Gateway API ----------

// KubeGatewayRoutes 列 HTTPRoute 并拍平成「什么流量 → 送到哪」。
//
// 单独做这个而不是让人在自定义资源页看 YAML：Gateway API 的 spec 嵌三层，
// 「这条路由把流量送到哪个 Service」在 YAML 里要来回翻。
func (h *Handler) KubeGatewayRoutes(c *gin.Context) {
	_, client, ok := h.requireKubeCluster(c)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), kubeCallTimeout)
	defer cancel()

	crds, err := client.ListCRDs(ctx)
	if err != nil {
		response.Error(c, "读取 CRD 失败: "+err.Error())
		return
	}
	version := gatewayAPIVersion(crds)
	if version == "" {
		// 没装就说没装，不给一张空表
		response.OK(c, gin.H{
			"installed": false,
			"routes":    []any{},
			"note": "这个集群没有安装 Gateway API（找不到 gateway.networking.k8s.io 的 HTTPRoute CRD）。" +
				"入口流量大概率还走 Ingress —— 去「资源管理」页选 Ingress 看",
		})
		return
	}

	routes, err := client.ListGatewayRoutes(ctx, version, strings.TrimSpace(c.Query("namespace")))
	if err != nil {
		response.Error(c, "读取 HTTPRoute 失败: "+err.Error())
		return
	}
	response.OK(c, gin.H{
		"installed": true, "version": version, "routes": routes, "total": len(routes),
		"notes": []string{
			fmt.Sprintf("集群里 HTTPRoute 的版本是 %s（按 CRD 的 storage 版本选）", version),
			"只解析 HTTPRoute。Gateway / GatewayClass / TCPRoute / GRPCRoute 可以在「自定义资源」页浏览原文",
			"跨命名空间的后端会标出来：那种引用需要 ReferenceGrant 才真正生效，这里只提示、不代为核对",
			"没有 matches 的规则按 Gateway API 的默认语义是「匹配所有请求」，界面上会显式写出来",
			"**只读**：不提供 HTTPRoute 的编辑 —— 改一条路由的影响面等同于切流量",
		},
	})
}

// ---------- RBAC 账户 ----------

// KubeRBAC ServiceAccount 及它们实际拿到的权限。
//
// 这一页回答的是「这个 SA 到底能干什么」——
// 在 kubectl 里要 get rolebinding / clusterrolebinding 再逐个 describe role 才拼得出来。
func (h *Handler) KubeRBAC(c *gin.Context) {
	_, client, ok := h.requireKubeCluster(c)
	if !ok {
		return
	}
	// RBAC 要读五类对象（SA / Role / ClusterRole / RoleBinding / ClusterRoleBinding），
	// 比单类列表慢，给它宽一点的超时
	ctx, cancel := context.WithTimeout(context.Background(), kubeApplyTimeout)
	defer cancel()

	snap, err := client.RBAC(ctx, strings.TrimSpace(c.Query("namespace")))
	if err != nil {
		response.Error(c, "读取 RBAC 失败: "+err.Error()+
			"（需要对 rbac.authorization.k8s.io 的 roles/clusterroles/bindings 有 list 权限）")
		return
	}

	accounts := snap.ServiceAccounts
	if keyword := strings.ToLower(strings.TrimSpace(c.Query("keyword"))); keyword != "" {
		filtered := accounts[:0:0]
		for _, item := range accounts {
			if strings.Contains(strings.ToLower(item.Name), keyword) ||
				strings.Contains(strings.ToLower(item.Namespace), keyword) {
				filtered = append(filtered, item)
			}
		}
		accounts = filtered
	}
	switch c.Query("risk") {
	case "admin":
		accounts = filterAccounts(accounts, func(a k8s.ServiceAccountView) bool { return a.ClusterAdmin })
	case "wildcard":
		accounts = filterAccounts(accounts, func(a k8s.ServiceAccountView) bool { return a.Wildcard })
	case "unused":
		// 没有任何 binding 的 SA：多半是遗留下来的，清理时先看这一批
		accounts = filterAccounts(accounts, func(a k8s.ServiceAccountView) bool { return len(a.Bindings) == 0 })
	}

	var adminCount, wildcardCount, unboundCount int
	for _, item := range snap.ServiceAccounts {
		if item.ClusterAdmin {
			adminCount++
		}
		if item.Wildcard {
			wildcardCount++
		}
		if len(item.Bindings) == 0 {
			unboundCount++
		}
	}

	response.OK(c, gin.H{
		"accounts": accounts,
		"total":    len(snap.ServiceAccounts),
		"shown":    len(accounts),
		"stats": gin.H{
			"clusterAdmin": adminCount, "wildcard": wildcardCount, "unbound": unboundCount,
			"roles": snap.RoleCount, "clusterRoles": snap.ClusterRoleCount,
			"bindings": snap.BindingCount, "clusterBindings": snap.ClusterBindingCount,
			"orphanBindings": snap.OrphanBindings,
		},
		"notes": []string{
			"「实际权限」是从 RoleBinding / ClusterRoleBinding 反查到 Role / ClusterRole 的 rules 算出来的，" +
				"不是读某个字段 —— k8s 里没有「这个 SA 有哪些权限」这样一个对象",
			"即使只看某个命名空间，Role 与 Binding 也一律全集群读：" +
				"命名空间里的 SA 可能被 ClusterRoleBinding 授权，只读本命名空间会漏掉最危险的那一类",
			"只反查 ServiceAccount。User / Group 主体在这个平台里没有对应对象（它们由集群外的认证系统管）",
			"「绑了但 Role 不存在」会被单独数出来（孤儿绑定）：那种配置的表现是「绑了却什么权限都没有」，" +
				"很容易被当成权限不够去加更大的角色",
			"**只读**：不提供 RBAC 的编辑 —— 改错就是提权，这类操作仍然走 kubectl",
		},
	})
}

// ---------- 节点与命名空间清点 ----------

// KubeNodeInventory 节点清点：健康、可调度性、版本偏斜。
//
// 与「容量与配额」页刻意不重叠：那一页回答「还能不能再塞」（已分配 / limits /
// 实际用量那本账），这里回答「这些节点自身健康不健康、有没有被什么东西卡住」。
// 两处各算一遍资源账迟早会对不上，而对不上的数字比没有数字更糟。
func (h *Handler) KubeNodeInventory(c *gin.Context) {
	_, client, ok := h.requireKubeCluster(c)
	if !ok {
		return
	}
	// 要读节点 + 全集群 Pod，比单类列表慢，给宽一点的超时
	ctx, cancel := context.WithTimeout(context.Background(), kubeApplyTimeout)
	defer cancel()

	inv, err := client.NodeInventoryList(ctx)
	if err != nil {
		response.Error(c, "读取节点失败: "+err.Error())
		return
	}

	nodes := inv.Nodes
	if keyword := strings.ToLower(strings.TrimSpace(c.Query("keyword"))); keyword != "" {
		filtered := nodes[:0:0]
		for _, item := range nodes {
			if strings.Contains(strings.ToLower(item.Name), keyword) ||
				strings.Contains(strings.ToLower(item.InternalIP), keyword) ||
				strings.Contains(strings.ToLower(strings.Join(item.Roles, ",")), keyword) {
				filtered = append(filtered, item)
			}
		}
		nodes = filtered
	}
	if c.Query("problem") == "1" {
		filtered := nodes[:0:0]
		for _, item := range nodes {
			if len(item.Problems) > 0 {
				filtered = append(filtered, item)
			}
		}
		nodes = filtered
	}

	response.OK(c, gin.H{
		"nodes": nodes, "total": len(inv.Nodes), "shown": len(nodes),
		"ready": inv.Ready, "notReady": inv.NotReady,
		"cordoned": inv.Cordoned, "tainted": inv.Tainted,
		"versions": inv.Versions, "versionSkew": len(inv.Versions) > 1,
		"podCounted": inv.PodCounted,
		"notes": []string{
			"这一页只看节点自身的健康与可调度性。**资源账本（已分配 / limits / 实际用量）在「容量与配额」页** —— " +
				"两处各算一遍迟早对不上，而对不上的数字比没有数字更糟",
			"conditions 的判断方向是反的：Ready 应当为 True，DiskPressure / MemoryPressure / " +
				"PIDPressure / NetworkUnavailable 应当为 False —— 后面这几个是故障的前兆",
			"Pod 数是全集群列一次 Pod 再按节点归组算的（不是每个节点查一次）；" +
				"已结束（Succeeded / Failed）的不计入。读不到 Pod 列表时显示「未知」而不是 0",
			"容量与可分配分开给：调度看的是 allocatable，两者的差值是预留给系统组件的部分",
			"出现多个 kubelet 版本会单独提示 —— 升级做到一半停下来是很常见的现场",
			"**只读**：不提供 cordon / uncordon / drain / 打污点 —— 那些会直接影响调度，仍然走 kubectl",
		},
	})
}

// KubeNamespaceInventory 命名空间清点：里面有什么、受什么约束、有没有卡住。
func (h *Handler) KubeNamespaceInventory(c *gin.Context) {
	_, client, ok := h.requireKubeCluster(c)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), kubeApplyTimeout)
	defer cancel()

	inv, err := client.NamespaceInventoryList(ctx)
	if err != nil {
		response.Error(c, "读取命名空间失败: "+err.Error())
		return
	}

	list := inv.Namespaces
	if keyword := strings.ToLower(strings.TrimSpace(c.Query("keyword"))); keyword != "" {
		filtered := list[:0:0]
		for _, item := range list {
			if strings.Contains(strings.ToLower(item.Name), keyword) {
				filtered = append(filtered, item)
			}
		}
		list = filtered
	}
	switch c.Query("filter") {
	case "problem":
		list = filterNamespaces(list, func(n k8s.NamespaceDetail) bool { return len(n.Problems) > 0 })
	case "terminating":
		list = filterNamespaces(list, func(n k8s.NamespaceDetail) bool { return n.Terminating })
	case "noquota":
		// 只有真读到了配额对象才敢筛这一类
		if inv.QuotaRead {
			list = filterNamespaces(list, func(n k8s.NamespaceDetail) bool {
				return len(n.Quotas) == 0 && !n.HasLimitRange
			})
		}
	case "empty":
		list = filterNamespaces(list, func(n k8s.NamespaceDetail) bool { return n.PodTotal == 0 })
	}

	response.OK(c, gin.H{
		"namespaces": list, "total": inv.Total, "shown": len(list),
		"terminating": inv.Terminating, "noQuota": inv.NoQuota,
		"podCounted": inv.PodCounted, "quotaRead": inv.QuotaRead,
		"notes": []string{
			"「卡在 Terminating」会连 finalizer 一起列出来 —— 命名空间删不掉几乎总是因为有 finalizer 没清",
			"「既没有 ResourceQuota 也没有 LimitRange」只在**真的读到了配额对象**之后才判定：" +
				"没权限看配额和没配配额是两件事，不能混成一个结论",
			"Pod 分布是全集群列一次 Pod 按命名空间归组算的；读不到时显示「未知」而不是 0",
			"ResourceQuota 只列 hard 里设了上限的项：status.used 里会带一堆没设上限的零值，全列出来是噪音",
			"**只读**：不提供命名空间的创建与删除 —— 删一个命名空间等于删掉里面的全部对象",
		},
	})
}

func filterNamespaces(list []k8s.NamespaceDetail,
	keep func(k8s.NamespaceDetail) bool) []k8s.NamespaceDetail {
	out := list[:0:0]
	for _, item := range list {
		if keep(item) {
			out = append(out, item)
		}
	}
	return out
}

func filterAccounts(list []k8s.ServiceAccountView,
	keep func(k8s.ServiceAccountView) bool) []k8s.ServiceAccountView {
	out := list[:0:0]
	for _, item := range list {
		if keep(item) {
			out = append(out, item)
		}
	}
	return out
}
