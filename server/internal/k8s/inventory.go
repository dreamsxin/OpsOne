package k8s

import (
	"context"
	"sort"
	"strings"
	"time"
)

// 节点与命名空间的「清点」视图。
//
// 与「容量与配额」页的分工要说清楚，否则两页会互相抄：
//   - 容量与配额回答「还能不能再塞」——已分配 / limits / 实际用量那本账。
//   - 这里回答「这些节点/命名空间自身健康不健康、有没有被什么东西卡住」——
//     conditions、污点、cordon、版本偏斜、Terminating 卡死、有没有配额约束。
//
// 两页都只读。节点的 cordon / drain、命名空间的创建删除都不做：
// 那些是会影响调度与数据的操作，仍然走 kubectl。

// ---------- 节点 ----------

// NodeTaint 一条污点。Effect 决定它有多硬
type NodeTaint struct {
	Key   string `json:"key"`
	Value string `json:"value"`
	// Effect NoSchedule（新 Pod 不往这放）| PreferNoSchedule（尽量不放）|
	//	NoExecute（连已经在上面的 Pod 都会被赶走）
	Effect string `json:"effect"`
}

// NodeCondition 一条节点状态。只把「不正常」的挑出来是不够的 ——
// Ready 之外那几个 pressure 条件是故障的前兆，要带 reason 一起给出来
type NodeCondition struct {
	Type   string `json:"type"`
	Status string `json:"status"`
	Reason string `json:"reason"`
	// Message API Server 给的原话
	Message string `json:"message"`
	// Problem 这条是不是「有问题」：Ready 应当为 True，其余 pressure 类应当为 False
	Problem bool `json:"problem"`
}

// NodeDetail 一个节点的健康与可调度性视图。
//
// 刻意**不含**已分配 / 实际用量：那是「容量与配额」页的账本，
// 两处各算一遍迟早对不上，而对不上的数字比没有数字更糟。
type NodeDetail struct {
	Name       string   `json:"name"`
	Ready      bool     `json:"ready"`
	Roles      []string `json:"roles"`
	Version    string   `json:"version"`
	OSImage    string   `json:"osImage"`
	Kernel     string   `json:"kernel"`
	Runtime    string   `json:"runtime"`
	InternalIP string   `json:"internalIP"`
	// Capacity / Allocatable 容量与可分配。分开给是因为调度看的是 allocatable，
	// 两者的差值是预留给系统组件的部分
	CapacityCPU    string `json:"capacityCpu"`
	CapacityMemory string `json:"capacityMemory"`
	CapacityPods   string `json:"capacityPods"`
	AllocCPU       string `json:"allocCpu"`
	AllocMemory    string `json:"allocMemory"`
	AllocPods      string `json:"allocPods"`
	// PodCount 这台节点上还活着的 Pod 数（已结束的不算）
	PodCount int `json:"podCount"`
	// PodCapacity allocatable.pods 解出来的数字，0 表示读不到
	PodCapacity int `json:"podCapacity"`
	// Unschedulable 被 cordon 了：调度器不会往上放新 Pod，但已有的还在跑
	Unschedulable bool            `json:"unschedulable"`
	Taints        []NodeTaint     `json:"taints"`
	Conditions    []NodeCondition `json:"conditions"`
	// Problems 一句话说清这台节点当前有什么问题，为空表示没发现问题
	Problems  []string  `json:"problems"`
	CreatedAt time.Time `json:"createdAt"`
}

// NodeInventory 节点清点结果
type NodeInventory struct {
	Nodes []NodeDetail `json:"nodes"`
	// Versions 集群里出现过的 kubelet 版本。多于一个就是版本偏斜 ——
	// 升级做到一半停下来是很常见的现场，值得单独报出来
	Versions []string `json:"versions"`
	Ready    int      `json:"ready"`
	NotReady int      `json:"notReady"`
	Cordoned int      `json:"cordoned"`
	Tainted  int      `json:"tainted"`
	// PodCounted 是否成功统计到了 Pod 分布。读不到 Pod 列表时为 false，
	// 界面显示「未知」而不是 0 —— 0 会被当成「这台机器上没有 Pod」
	PodCounted bool `json:"podCounted"`
}

var nodeKind = ResourceKind{
	Kind: "Node", APIVersion: "v1", Resource: "nodes",
	ReadOnly: true, Group: "集群", apiRoot: "/api/v1",
}

var podKindForCount = ResourceKind{
	Kind: "Pod", APIVersion: "v1", Resource: "pods",
	Namespaced: true, ReadOnly: true, Group: "工作负载", apiRoot: "/api/v1",
}

// NodeInventoryList 读节点清单并算出健康结论。
func (c *Client) NodeInventoryList(ctx context.Context) (NodeInventory, error) {
	var out NodeInventory

	items, err := c.ListRawObjects(ctx, nodeKind, ListQuery{})
	if err != nil {
		return out, err
	}

	// Pod 分布：全集群列一次再按 nodeName 归组，而不是每个节点查一次
	// （N 个节点就是 N 次请求）。读不到就如实标 PodCounted=false
	podsByNode := map[string]int{}
	if pods, err := c.ListRawObjects(ctx, podKindForCount, ListQuery{}); err == nil {
		out.PodCounted = true
		for _, pod := range pods {
			status, _ := pod["status"].(map[string]any)
			phase, _ := status["phase"].(string)
			// 已结束的 Pod 资源早还回去了，不该算进「这台机器上有多少 Pod」
			if phase == "Succeeded" || phase == "Failed" {
				continue
			}
			spec, _ := pod["spec"].(map[string]any)
			if name, _ := spec["nodeName"].(string); name != "" {
				podsByNode[name]++
			}
		}
	}

	versions := map[string]struct{}{}
	for _, obj := range items {
		node := convertNodeDetail(obj)
		node.PodCount = podsByNode[node.Name]
		node.Problems = describeNodeProblems(node)

		if node.Ready {
			out.Ready++
		} else {
			out.NotReady++
		}
		if node.Unschedulable {
			out.Cordoned++
		}
		if len(node.Taints) > 0 {
			out.Tainted++
		}
		if node.Version != "" {
			versions[node.Version] = struct{}{}
		}
		out.Nodes = append(out.Nodes, node)
	}

	for v := range versions {
		out.Versions = append(out.Versions, v)
	}
	sort.Strings(out.Versions)

	sort.Slice(out.Nodes, func(i, j int) bool {
		a, b := out.Nodes[i], out.Nodes[j]
		// 排序按「严重程度」而不是「问题条数」：一台 NotReady 的节点
		// 比一台有三条轻微提示的节点更该先看。只按条数排会把 NotReady 压到后面
		if a.Ready != b.Ready {
			return !a.Ready
		}
		if len(a.Problems) != len(b.Problems) {
			return len(a.Problems) > len(b.Problems)
		}
		return a.Name < b.Name
	})
	return out, nil
}

// nodeConditionProblem 判断一条 condition 是不是「有问题」。
// Ready 应当为 True，其余（DiskPressure / MemoryPressure / PIDPressure /
// NetworkUnavailable）应当为 False —— 方向是反的，写错就会把正常报成异常。
func nodeConditionProblem(condType, status string) bool {
	if condType == "Ready" {
		return status != "True"
	}
	return status == "True"
}

func convertNodeDetail(obj map[string]any) NodeDetail {
	node := NodeDetail{Roles: []string{}}
	meta, _ := obj["metadata"].(map[string]any)
	if meta != nil {
		node.Name, _ = meta["name"].(string)
		if stamp, ok := meta["creationTimestamp"].(string); ok {
			if parsed, err := time.Parse(time.RFC3339, stamp); err == nil {
				node.CreatedAt = parsed
			}
		}
		for key := range stringMap(meta["labels"]) {
			if role, ok := trimPrefix(key, "node-role.kubernetes.io/"); ok && role != "" {
				node.Roles = append(node.Roles, role)
			}
		}
		sort.Strings(node.Roles)
	}

	spec, _ := obj["spec"].(map[string]any)
	if spec != nil {
		node.Unschedulable, _ = spec["unschedulable"].(bool)
		for _, raw := range asSlice(spec["taints"]) {
			taint, _ := raw.(map[string]any)
			if taint == nil {
				continue
			}
			var t NodeTaint
			t.Key, _ = taint["key"].(string)
			t.Value, _ = taint["value"].(string)
			t.Effect, _ = taint["effect"].(string)
			if t.Key != "" {
				node.Taints = append(node.Taints, t)
			}
		}
	}

	status, _ := obj["status"].(map[string]any)
	if status == nil {
		return node
	}
	if info, ok := status["nodeInfo"].(map[string]any); ok {
		node.Version, _ = info["kubeletVersion"].(string)
		node.OSImage, _ = info["osImage"].(string)
		node.Kernel, _ = info["kernelVersion"].(string)
		node.Runtime, _ = info["containerRuntimeVersion"].(string)
	}
	if capacity := stringMap(status["capacity"]); capacity != nil {
		node.CapacityCPU, node.CapacityMemory, node.CapacityPods =
			capacity["cpu"], capacity["memory"], capacity["pods"]
	}
	if alloc := stringMap(status["allocatable"]); alloc != nil {
		node.AllocCPU, node.AllocMemory, node.AllocPods =
			alloc["cpu"], alloc["memory"], alloc["pods"]
		node.PodCapacity = atoiSafe(alloc["pods"])
	}
	for _, raw := range asSlice(status["addresses"]) {
		addr, _ := raw.(map[string]any)
		if addr == nil {
			continue
		}
		if t, _ := addr["type"].(string); t == "InternalIP" {
			node.InternalIP, _ = addr["address"].(string)
		}
	}
	for _, raw := range asSlice(status["conditions"]) {
		cond, _ := raw.(map[string]any)
		if cond == nil {
			continue
		}
		var item NodeCondition
		item.Type, _ = cond["type"].(string)
		item.Status, _ = cond["status"].(string)
		item.Reason, _ = cond["reason"].(string)
		item.Message, _ = cond["message"].(string)
		item.Problem = nodeConditionProblem(item.Type, item.Status)
		if item.Type == "Ready" {
			node.Ready = item.Status == "True"
		}
		node.Conditions = append(node.Conditions, item)
	}
	return node
}

// describeNodeProblems 把节点当前的问题说成人话。
// 顺序是按「要先看哪个」排的：不 Ready > 压力 > 被赶走的污点 > cordon > Pod 快满。
func describeNodeProblems(node NodeDetail) []string {
	var problems []string
	if !node.Ready {
		reason := "状态不是 Ready"
		for _, cond := range node.Conditions {
			if cond.Type == "Ready" && cond.Reason != "" {
				reason = "不是 Ready：" + cond.Reason
			}
		}
		problems = append(problems, reason)
	}
	for _, cond := range node.Conditions {
		if cond.Type == "Ready" || !cond.Problem {
			continue
		}
		label := map[string]string{
			"DiskPressure":       "磁盘压力",
			"MemoryPressure":     "内存压力",
			"PIDPressure":        "进程数压力",
			"NetworkUnavailable": "网络不可用",
		}[cond.Type]
		if label == "" {
			label = cond.Type
		}
		text := label
		if cond.Reason != "" {
			text += "（" + cond.Reason + "）"
		}
		problems = append(problems, text)
	}
	for _, taint := range node.Taints {
		if taint.Effect == "NoExecute" {
			problems = append(problems,
				"有 NoExecute 污点 "+taint.Key+"：不容忍它的 Pod 会被赶走")
		}
	}
	if node.Unschedulable {
		problems = append(problems, "已 cordon：不会再调度新 Pod 上来（已有的还在跑）")
	}
	// Pod 数快到上限：调度失败时最容易被忽略的一个原因
	if node.PodCapacity > 0 && node.PodCount >= node.PodCapacity {
		problems = append(problems, "Pod 数已达上限，新 Pod 调度不上来")
	} else if node.PodCapacity > 0 && node.PodCount*10 >= node.PodCapacity*9 {
		problems = append(problems, "Pod 数已用到九成以上")
	}
	return problems
}

// ---------- 命名空间 ----------

// NamespaceQuota 一条 ResourceQuota 的已用/上限
type NamespaceQuota struct {
	Name string `json:"name"`
	// Items 形如 `requests.cpu: 2 / 4`
	Items []string `json:"items"`
}

// NamespaceDetail 一个命名空间里有什么、受什么约束。
type NamespaceDetail struct {
	Name      string    `json:"name"`
	Phase     string    `json:"phase"`
	CreatedAt time.Time `json:"createdAt"`
	// Terminating 卡在删除中。这是很常见的现场：有 finalizer 没清掉，
	// 命名空间会一直停在 Terminating，里面的对象也删不干净
	Terminating bool `json:"terminating"`
	// Finalizers 命名空间自己的 finalizer（spec.finalizers）
	Finalizers []string `json:"finalizers"`
	// Conditions Terminating 时 API Server 会在这里说清卡在哪
	Conditions []string          `json:"conditions"`
	Labels     map[string]string `json:"labels"`

	// PodTotal / PodRunning / PodPending / PodFailed Pod 分布
	PodTotal   int `json:"podTotal"`
	PodRunning int `json:"podRunning"`
	PodPending int `json:"podPending"`
	PodFailed  int `json:"podFailed"`

	Quotas []NamespaceQuota `json:"quotas"`
	// HasLimitRange 有没有 LimitRange。没有的话，这个命名空间里不写
	// requests/limits 的 Pod 能把节点吃满
	HasLimitRange bool `json:"hasLimitRange"`
	// Problems 一句话结论
	Problems []string `json:"problems"`
}

// NamespaceInventory 命名空间清点结果
type NamespaceInventory struct {
	Namespaces  []NamespaceDetail `json:"namespaces"`
	Total       int               `json:"total"`
	Terminating int               `json:"terminating"`
	// NoQuota 既没有 ResourceQuota 也没有 LimitRange 的命名空间数
	NoQuota int `json:"noQuota"`
	// PodCounted 是否成功统计到 Pod 分布
	PodCounted bool `json:"podCounted"`
	// QuotaRead 是否读到了配额对象。没权限时为 false，界面上区分
	// 「没配配额」与「看不到配额」
	QuotaRead bool `json:"quotaRead"`
}

var (
	namespaceKind = ResourceKind{
		Kind: "Namespace", APIVersion: "v1", Resource: "namespaces",
		ReadOnly: true, Group: "集群", apiRoot: "/api/v1",
	}
	quotaKind = ResourceKind{
		Kind: "ResourceQuota", APIVersion: "v1", Resource: "resourcequotas",
		Namespaced: true, ReadOnly: true, Group: "集群", apiRoot: "/api/v1",
	}
	limitRangeKind = ResourceKind{
		Kind: "LimitRange", APIVersion: "v1", Resource: "limitranges",
		Namespaced: true, ReadOnly: true, Group: "集群", apiRoot: "/api/v1",
	}
)

// NamespaceInventoryList 读命名空间清单 + Pod 分布 + 配额约束。
func (c *Client) NamespaceInventoryList(ctx context.Context) (NamespaceInventory, error) {
	var out NamespaceInventory

	items, err := c.ListRawObjects(ctx, namespaceKind, ListQuery{})
	if err != nil {
		return out, err
	}

	// Pod 分布：全集群列一次按命名空间归组
	type podStat struct{ total, running, pending, failed int }
	podsByNS := map[string]*podStat{}
	if pods, err := c.ListRawObjects(ctx, podKindForCount, ListQuery{}); err == nil {
		out.PodCounted = true
		for _, pod := range pods {
			meta, _ := pod["metadata"].(map[string]any)
			ns, _ := meta["namespace"].(string)
			if ns == "" {
				continue
			}
			stat := podsByNS[ns]
			if stat == nil {
				stat = &podStat{}
				podsByNS[ns] = stat
			}
			stat.total++
			status, _ := pod["status"].(map[string]any)
			switch phase, _ := status["phase"].(string); phase {
			case "Running":
				stat.running++
			case "Pending":
				stat.pending++
			case "Failed":
				stat.failed++
			}
		}
	}

	// 配额与 LimitRange：同样全集群读一次
	quotasByNS := map[string][]NamespaceQuota{}
	limitByNS := map[string]bool{}
	if quotas, err := c.ListRawObjects(ctx, quotaKind, ListQuery{}); err == nil {
		out.QuotaRead = true
		for _, obj := range quotas {
			ns, name := objNamespaceName(obj)
			quotasByNS[ns] = append(quotasByNS[ns], NamespaceQuota{
				Name: name, Items: describeQuota(obj),
			})
		}
	}
	if ranges, err := c.ListRawObjects(ctx, limitRangeKind, ListQuery{}); err == nil {
		for _, obj := range ranges {
			ns, _ := objNamespaceName(obj)
			limitByNS[ns] = true
		}
	}

	for _, obj := range items {
		ns := convertNamespaceDetail(obj)
		if stat := podsByNS[ns.Name]; stat != nil {
			ns.PodTotal, ns.PodRunning = stat.total, stat.running
			ns.PodPending, ns.PodFailed = stat.pending, stat.failed
		}
		ns.Quotas = quotasByNS[ns.Name]
		ns.HasLimitRange = limitByNS[ns.Name]
		ns.Problems = describeNamespaceProblems(ns, out.QuotaRead)

		out.Total++
		if ns.Terminating {
			out.Terminating++
		}
		if out.QuotaRead && len(ns.Quotas) == 0 && !ns.HasLimitRange {
			out.NoQuota++
		}
		out.Namespaces = append(out.Namespaces, ns)
	}

	sort.Slice(out.Namespaces, func(i, j int) bool {
		a, b := out.Namespaces[i], out.Namespaces[j]
		// 同样按严重程度：卡在 Terminating 比「有 Pending Pod + 没配额」更该先看。
		// 单测就是在这里发现只按条数排会把 Terminating 压到后面的
		if a.Terminating != b.Terminating {
			return a.Terminating
		}
		if len(a.Problems) != len(b.Problems) {
			return len(a.Problems) > len(b.Problems)
		}
		return a.Name < b.Name
	})
	return out, nil
}

func convertNamespaceDetail(obj map[string]any) NamespaceDetail {
	ns := NamespaceDetail{}
	meta, _ := obj["metadata"].(map[string]any)
	if meta != nil {
		ns.Name, _ = meta["name"].(string)
		ns.Labels = stringMap(meta["labels"])
		if stamp, ok := meta["creationTimestamp"].(string); ok {
			if parsed, err := time.Parse(time.RFC3339, stamp); err == nil {
				ns.CreatedAt = parsed
			}
		}
	}
	if spec, ok := obj["spec"].(map[string]any); ok {
		ns.Finalizers = stringSlice(spec["finalizers"])
	}
	if status, ok := obj["status"].(map[string]any); ok {
		ns.Phase, _ = status["phase"].(string)
		ns.Terminating = ns.Phase == "Terminating"
		for _, raw := range asSlice(status["conditions"]) {
			cond, _ := raw.(map[string]any)
			if cond == nil {
				continue
			}
			t, _ := cond["type"].(string)
			s, _ := cond["status"].(string)
			if s != "True" {
				continue
			}
			message, _ := cond["message"].(string)
			text := t
			if message != "" {
				text += "：" + message
			}
			ns.Conditions = append(ns.Conditions, text)
		}
	}
	return ns
}

// describeQuota 把 ResourceQuota 的已用/上限拍成「key: used / hard」。
// 只列 hard 里有的项：status.used 里会带一堆没设上限的零值，全列出来是噪音。
func describeQuota(obj map[string]any) []string {
	status, _ := obj["status"].(map[string]any)
	if status == nil {
		return nil
	}
	hard := stringMap(status["hard"])
	used := stringMap(status["used"])
	if len(hard) == 0 {
		// status 还没算出来时退回 spec.hard，至少能看出设了什么上限
		if spec, ok := obj["spec"].(map[string]any); ok {
			hard = stringMap(spec["hard"])
		}
	}
	keys := make([]string, 0, len(hard))
	for key := range hard {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	out := make([]string, 0, len(keys))
	for _, key := range keys {
		u := used[key]
		if u == "" {
			u = "?"
		}
		out = append(out, key+": "+u+" / "+hard[key])
	}
	return out
}

func describeNamespaceProblems(ns NamespaceDetail, quotaRead bool) []string {
	var problems []string
	if ns.Terminating {
		text := "卡在 Terminating"
		if len(ns.Finalizers) > 0 {
			text += "，finalizer 未清：" + strings.Join(ns.Finalizers, "、")
		}
		problems = append(problems, text)
	}
	if ns.PodFailed > 0 {
		problems = append(problems, "有 Failed 状态的 Pod")
	}
	if ns.PodPending > 0 {
		problems = append(problems, "有 Pending 的 Pod（调度不上去）")
	}
	// 只在真的读到了配额对象时才说「没配」——
	// 没权限看配额和没配配额是两件事，不能混
	if quotaRead && len(ns.Quotas) == 0 && !ns.HasLimitRange && ns.PodTotal > 0 {
		problems = append(problems,
			"既没有 ResourceQuota 也没有 LimitRange：这里不写 requests/limits 的 Pod 能把节点吃满")
	}
	return problems
}

// atoiSafe 把字符串转成 int，转不了返回 0
func atoiSafe(raw string) int {
	n := 0
	for _, ch := range raw {
		if ch < '0' || ch > '9' {
			return 0
		}
		n = n*10 + int(ch-'0')
	}
	return n
}
