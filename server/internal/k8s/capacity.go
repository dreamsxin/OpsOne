package k8s

import (
	"context"
	"fmt"
	"math"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// 容量与配额。
//
// 这一页要回答的是「还能不能再塞」，而它最容易被做成一个误导人的页面，因为
// **「已分配」和「实际用量」是两件事**：
//   - 已分配 = 节点上所有活着的 Pod 的 requests 之和，决定调度器还愿不愿意往这台机器放 Pod
//   - 实际用量 = 容器真正在烧的 CPU / 内存，要 metrics-server 才有
// 只给其中一个、或者把两者混在一栏里，都会让人得出错误结论（比如「用量才 20%，为什么调度不上去」）。
// 所以这里两个都给，并且 metrics 缺失时明确说「这个集群没装 metrics-server」，不用 0 充数。

// ---------- 资源量解析 ----------

// ParseCPU 把 Kubernetes 的 CPU 量解析成核数。
// 支持 "100m"（毫核）、"1"、"1.5"、"123456n"（纳核，metrics API 用这个）、"500u"（微核）。
func ParseCPU(raw string) float64 {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	multiplier := 1.0
	switch {
	case strings.HasSuffix(raw, "n"):
		multiplier, raw = 1e-9, strings.TrimSuffix(raw, "n")
	case strings.HasSuffix(raw, "u"):
		multiplier, raw = 1e-6, strings.TrimSuffix(raw, "u")
	case strings.HasSuffix(raw, "m"):
		multiplier, raw = 1e-3, strings.TrimSuffix(raw, "m")
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0
	}
	return value * multiplier
}

// memoryUnits 二进制与十进制两套后缀都要认：
// 节点容量常写 Ki，limits 常写 Mi/Gi，而有些工具会写成 M/G（十进制，比 Mi/Gi 小）
var memoryUnits = []struct {
	suffix string
	factor float64
}{
	{"Ki", 1 << 10}, {"Mi", 1 << 20}, {"Gi", 1 << 30}, {"Ti", 1 << 40}, {"Pi", 1 << 50},
	{"k", 1e3}, {"K", 1e3}, {"M", 1e6}, {"G", 1e9}, {"T", 1e12}, {"P", 1e15},
}

// ParseMemory 把内存 / 存储量解析成字节数。认不出来返回 0（并且调用方会显示「—」）。
func ParseMemory(raw string) int64 {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	for _, unit := range memoryUnits {
		if !strings.HasSuffix(raw, unit.suffix) {
			continue
		}
		value, err := strconv.ParseFloat(strings.TrimSuffix(raw, unit.suffix), 64)
		if err != nil {
			return 0
		}
		return int64(value * unit.factor)
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0
	}
	return int64(value)
}

// FormatCPU 核数转可读串。1 核以下用毫核，避免出现 0.05 这种不好比对的数
func FormatCPU(cores float64) string {
	if cores <= 0 {
		return "0"
	}
	if cores < 1 {
		return fmt.Sprintf("%dm", int(math.Round(cores*1000)))
	}
	return strconv.FormatFloat(math.Round(cores*100)/100, 'f', -1, 64)
}

// FormatMemory 字节数转可读串（按 1024 进制，与 kubectl 一致）
func FormatMemory(bytes int64) string {
	if bytes <= 0 {
		return "0"
	}
	units := []struct {
		suffix string
		factor float64
	}{{"Ti", 1 << 40}, {"Gi", 1 << 30}, {"Mi", 1 << 20}, {"Ki", 1 << 10}}
	value := float64(bytes)
	for _, unit := range units {
		if value >= unit.factor {
			return strconv.FormatFloat(math.Round(value/unit.factor*10)/10, 'f', -1, 64) + unit.suffix
		}
	}
	return strconv.FormatInt(bytes, 10)
}

// percent 占比，分母为 0 时返回 0 而不是 NaN（NaN 会让前端显示 "NaN%"）
func percent(used, total float64) float64 {
	if total <= 0 {
		return 0
	}
	return math.Round(used/total*1000) / 10
}

// ---------- 结构 ----------

// NodeCapacity 一个节点的容量账本
type NodeCapacity struct {
	Name        string `json:"name"`
	Roles       string `json:"roles"`
	Ready       bool   `json:"ready"`
	Schedulable bool   `json:"schedulable"`
	PodCount    int    `json:"podCount"`
	PodCapacity int    `json:"podCapacity"`

	CPUCapacitys   string `json:"cpuCapacity"`
	CPUAllocatable string `json:"cpuAllocatable"`
	CPURequests    string `json:"cpuRequests"`
	CPULimits      string `json:"cpuLimits"`
	CPUUsage       string `json:"cpuUsage"`
	// CPURequestPct 已分配占可分配的比例（调度看的是这个）
	CPURequestPct float64 `json:"cpuRequestPct"`
	CPULimitPct   float64 `json:"cpuLimitPct"`
	CPUUsagePct   float64 `json:"cpuUsagePct"`

	MemCapacity    string  `json:"memCapacity"`
	MemAllocatable string  `json:"memAllocatable"`
	MemRequests    string  `json:"memRequests"`
	MemLimits      string  `json:"memLimits"`
	MemUsage       string  `json:"memUsage"`
	MemRequestPct  float64 `json:"memRequestPct"`
	MemLimitPct    float64 `json:"memLimitPct"`
	MemUsagePct    float64 `json:"memUsagePct"`

	// HasUsage 这个节点有没有实际用量数据
	HasUsage bool `json:"hasUsage"`
}

// NamespaceCapacity 命名空间维度的已分配与配额
type NamespaceCapacity struct {
	Namespace   string      `json:"namespace"`
	PodCount    int         `json:"podCount"`
	CPURequests string      `json:"cpuRequests"`
	CPULimits   string      `json:"cpuLimits"`
	MemRequests string      `json:"memRequests"`
	MemLimits   string      `json:"memLimits"`
	CPUUsage    string      `json:"cpuUsage"`
	MemUsage    string      `json:"memUsage"`
	HasUsage    bool        `json:"hasUsage"`
	Quotas      []QuotaItem `json:"quotas"`
	// NoLimitPods 没配 limits 的 Pod 数：这些 Pod 能把节点吃满，是容量事故的常见来源
	NoLimitPods int `json:"noLimitPods"`
}

// QuotaItem 一条 ResourceQuota 的某个资源项
type QuotaItem struct {
	Name     string  `json:"name"`
	Resource string  `json:"resource"`
	Hard     string  `json:"hard"`
	Used     string  `json:"used"`
	Percent  float64 `json:"percent"`
}

// ClusterCapacity 整个集群的容量视图
type ClusterCapacity struct {
	Nodes      []NodeCapacity      `json:"nodes"`
	Namespaces []NamespaceCapacity `json:"namespaces"`
	// PodsCounted 参与统计的 Pod 数（只算活着的）
	PodsCounted int `json:"podsCounted"`
	// PodsSkipped 跳过的 Pod 数（Succeeded / Failed，它们不再占资源）
	PodsSkipped      int    `json:"podsSkipped"`
	MetricsAvailable bool   `json:"metricsAvailable"`
	MetricsNote      string `json:"metricsNote"`
}

// podResourceTotals 一个 Pod 实际占用的调度资源。
//
// 规则与调度器一致：普通容器的 requests 求和，与「单个 init 容器的最大值」取较大者
// （init 容器是串行跑完就退出的，不与普通容器叠加）。这条算错会让统计值偏大，
// 进而让人以为节点比实际更满。
func podResourceTotals(spec map[string]any) (cpuReq, cpuLim float64, memReq, memLim int64, noLimit bool) {
	sum := func(key string) (float64, float64, int64, int64, bool) {
		var cReq, cLim float64
		var mReq, mLim int64
		missing := false
		list, _ := spec[key].([]any)
		for _, entry := range list {
			item, _ := entry.(map[string]any)
			res, _ := item["resources"].(map[string]any)
			req := stringMap(mapField(res, "requests"))
			lim := stringMap(mapField(res, "limits"))
			cReq += ParseCPU(req["cpu"])
			mReq += ParseMemory(req["memory"])
			cLim += ParseCPU(lim["cpu"])
			mLim += ParseMemory(lim["memory"])
			if lim["cpu"] == "" || lim["memory"] == "" {
				missing = true
			}
		}
		return cReq, cLim, mReq, mLim, missing
	}
	cpuReq, cpuLim, memReq, memLim, noLimit = sum("containers")

	// init 容器取单个最大值
	var maxCPUReq, maxCPULim float64
	var maxMemReq, maxMemLim int64
	inits, _ := spec["initContainers"].([]any)
	for _, entry := range inits {
		item, _ := entry.(map[string]any)
		res, _ := item["resources"].(map[string]any)
		req := stringMap(mapField(res, "requests"))
		lim := stringMap(mapField(res, "limits"))
		maxCPUReq = math.Max(maxCPUReq, ParseCPU(req["cpu"]))
		maxCPULim = math.Max(maxCPULim, ParseCPU(lim["cpu"]))
		if v := ParseMemory(req["memory"]); v > maxMemReq {
			maxMemReq = v
		}
		if v := ParseMemory(lim["memory"]); v > maxMemLim {
			maxMemLim = v
		}
	}
	cpuReq = math.Max(cpuReq, maxCPUReq)
	cpuLim = math.Max(cpuLim, maxCPULim)
	if maxMemReq > memReq {
		memReq = maxMemReq
	}
	if maxMemLim > memLim {
		memLim = maxMemLim
	}
	return cpuReq, cpuLim, memReq, memLim, noLimit
}

func mapField(m map[string]any, key string) any {
	if m == nil {
		return nil
	}
	return m[key]
}

// podCountsForScheduling 这个 Pod 是否还占着节点资源。
// Succeeded / Failed 的 Pod 还在 API 里，但资源早就还回去了，算进来会虚高。
func podCountsForScheduling(status map[string]any) bool {
	phase, _ := status["phase"].(string)
	return phase != "Succeeded" && phase != "Failed"
}

// ---------- 采集 ----------

type usageEntry struct {
	cpu float64
	mem int64
}

// NodeUsage / PodUsage metrics-server 的数据。取不到时返回 nil + 原因，
// 调用方据此把「实际用量」列显示成「未知」而不是 0。
func (c *Client) nodeUsage(ctx context.Context) (map[string]usageEntry, error) {
	var raw struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Usage map[string]string `json:"usage"`
		} `json:"items"`
	}
	if err := c.get(ctx, "/apis/metrics.k8s.io/v1beta1/nodes", nil, &raw); err != nil {
		return nil, err
	}
	out := make(map[string]usageEntry, len(raw.Items))
	for _, item := range raw.Items {
		out[item.Metadata.Name] = usageEntry{
			cpu: ParseCPU(item.Usage["cpu"]), mem: ParseMemory(item.Usage["memory"]),
		}
	}
	return out, nil
}

// podUsage 返回 namespace/name → 用量，同时按命名空间聚合
func (c *Client) podUsage(ctx context.Context) (map[string]usageEntry, error) {
	var raw struct {
		Items []struct {
			Metadata struct {
				Name      string `json:"name"`
				Namespace string `json:"namespace"`
			} `json:"metadata"`
			Containers []struct {
				Usage map[string]string `json:"usage"`
			} `json:"containers"`
		} `json:"items"`
	}
	if err := c.get(ctx, "/apis/metrics.k8s.io/v1beta1/pods", nil, &raw); err != nil {
		return nil, err
	}
	out := make(map[string]usageEntry, len(raw.Items))
	for _, item := range raw.Items {
		entry := usageEntry{}
		for _, container := range item.Containers {
			entry.cpu += ParseCPU(container.Usage["cpu"])
			entry.mem += ParseMemory(container.Usage["memory"])
		}
		out[item.Metadata.Namespace+"/"+item.Metadata.Name] = entry
	}
	return out, nil
}

// Capacity 汇总集群容量。
//
// namespace 只过滤「命名空间」那张表；节点账本永远按全集群算 ——
// 「这台机器还能不能再塞」与你正在看哪个命名空间无关，按命名空间过滤过的节点已分配量
// 是个会让人做出错误判断的数字。
func (c *Client) Capacity(ctx context.Context, namespace string) (*ClusterCapacity, error) {
	nodeKind, _ := LookupKind("Node")
	podKind, _ := LookupKind("Pod")

	var nodeList struct {
		Items []map[string]any `json:"items"`
	}
	if err := c.get(ctx, namespacedPath(nodeKind.apiRoot, "", nodeKind.Resource), nil, &nodeList); err != nil {
		return nil, fmt.Errorf("读取节点失败: %w", err)
	}
	var podList struct {
		Items []map[string]any `json:"items"`
	}
	// 始终取全量 Pod：节点账本要的是全集群视角
	if err := c.get(ctx, namespacedPath(podKind.apiRoot, "", podKind.Resource), nil, &podList); err != nil {
		return nil, fmt.Errorf("读取 Pod 失败: %w", err)
	}

	result := &ClusterCapacity{}

	// metrics 是可选的：拿不到不算失败，但要说清原因
	nodeUse, nodeErr := c.nodeUsage(ctx)
	podUse, podErr := c.podUsage(ctx)
	result.MetricsAvailable = nodeErr == nil && podErr == nil
	if !result.MetricsAvailable {
		reason := ""
		if nodeErr != nil {
			reason = nodeErr.Error()
		} else if podErr != nil {
			reason = podErr.Error()
		}
		result.MetricsNote = "这个集群读不到 metrics.k8s.io（多为没装 metrics-server 或它没就绪）：" +
			"「实际用量」列显示为未知，不用 0 充数。已分配（requests）不依赖它，照常统计。原始错误：" + reason
	} else {
		result.MetricsNote = "实际用量来自 metrics-server，是采样值（默认 15 秒一采），与 requests 是两件事：" +
			"调度看 requests，OOM 看实际用量"
	}

	// 先按节点与命名空间累加 Pod 的 requests / limits
	type acc struct {
		cpuReq, cpuLim float64
		memReq, memLim int64
		pods           int
		noLimit        int
		usageCPU       float64
		usageMem       int64
		hasUsage       bool
	}
	byNode := map[string]*acc{}
	byNS := map[string]*acc{}

	for _, pod := range podList.Items {
		meta, _ := pod["metadata"].(map[string]any)
		spec, _ := pod["spec"].(map[string]any)
		status, _ := pod["status"].(map[string]any)
		if !podCountsForScheduling(status) {
			result.PodsSkipped++
			continue
		}
		result.PodsCounted++

		name, _ := meta["name"].(string)
		ns, _ := meta["namespace"].(string)
		node, _ := spec["nodeName"].(string)
		cpuReq, cpuLim, memReq, memLim, noLimit := podResourceTotals(spec)

		add := func(bucket map[string]*acc, id string) {
			if id == "" {
				return
			}
			item := bucket[id]
			if item == nil {
				item = &acc{}
				bucket[id] = item
			}
			item.cpuReq += cpuReq
			item.cpuLim += cpuLim
			item.memReq += memReq
			item.memLim += memLim
			item.pods++
			if noLimit {
				item.noLimit++
			}
			if use, ok := podUse[ns+"/"+name]; ok {
				item.usageCPU += use.cpu
				item.usageMem += use.mem
				item.hasUsage = true
			}
		}
		// 节点账本永远按全集群算；命名空间过滤只作用于命名空间那张表
		add(byNode, node)
		if namespace == "" || ns == namespace {
			add(byNS, ns)
		}
	}

	// 节点账本
	for _, node := range nodeList.Items {
		meta, _ := node["metadata"].(map[string]any)
		spec, _ := node["spec"].(map[string]any)
		status, _ := node["status"].(map[string]any)
		name, _ := meta["name"].(string)

		capacity := stringMap(status["capacity"])
		allocatable := stringMap(status["allocatable"])
		item := NodeCapacity{
			Name:  name,
			Ready: nodeReady(status),
			Roles: nodeRoles(meta),
		}
		item.Schedulable = true
		if flag, ok := spec["unschedulable"].(bool); ok && flag {
			item.Schedulable = false
		}
		cpuCap, cpuAlloc := ParseCPU(capacity["cpu"]), ParseCPU(allocatable["cpu"])
		memCap, memAlloc := ParseMemory(capacity["memory"]), ParseMemory(allocatable["memory"])
		item.CPUCapacitys, item.CPUAllocatable = FormatCPU(cpuCap), FormatCPU(cpuAlloc)
		item.MemCapacity, item.MemAllocatable = FormatMemory(memCap), FormatMemory(memAlloc)
		// pods 是个纯数字（比如 110），单独解析，别混用 CPU 那套后缀逻辑
		if n, err := strconv.Atoi(strings.TrimSpace(allocatable["pods"])); err == nil {
			item.PodCapacity = n
		}

		if entry := byNode[name]; entry != nil {
			item.PodCount = entry.pods
			item.CPURequests, item.CPULimits = FormatCPU(entry.cpuReq), FormatCPU(entry.cpuLim)
			item.MemRequests, item.MemLimits = FormatMemory(entry.memReq), FormatMemory(entry.memLim)
			// 比例的分母用 allocatable 而不是 capacity：调度器看的是可分配量
			item.CPURequestPct = percent(entry.cpuReq, cpuAlloc)
			item.CPULimitPct = percent(entry.cpuLim, cpuAlloc)
			item.MemRequestPct = percent(float64(entry.memReq), float64(memAlloc))
			item.MemLimitPct = percent(float64(entry.memLim), float64(memAlloc))
		} else {
			item.CPURequests, item.CPULimits = "0", "0"
			item.MemRequests, item.MemLimits = "0", "0"
		}
		if use, ok := nodeUse[name]; ok {
			item.HasUsage = true
			item.CPUUsage, item.MemUsage = FormatCPU(use.cpu), FormatMemory(use.mem)
			item.CPUUsagePct = percent(use.cpu, cpuAlloc)
			item.MemUsagePct = percent(float64(use.mem), float64(memAlloc))
		}
		result.Nodes = append(result.Nodes, item)
	}
	sort.Slice(result.Nodes, func(i, j int) bool { return result.Nodes[i].Name < result.Nodes[j].Name })

	// 命名空间账本 + 配额
	quotas, quotaErr := c.resourceQuotas(ctx, namespace)
	for ns, entry := range byNS {
		// 没有配额时给空切片而不是 nil：nil 序列化成 null，前端一次 map 就会把整张表打空
		list := quotas[ns]
		if list == nil {
			list = []QuotaItem{}
		}
		item := NamespaceCapacity{
			Namespace: ns, PodCount: entry.pods, NoLimitPods: entry.noLimit,
			CPURequests: FormatCPU(entry.cpuReq), CPULimits: FormatCPU(entry.cpuLim),
			MemRequests: FormatMemory(entry.memReq), MemLimits: FormatMemory(entry.memLim),
			Quotas: list,
		}

		if entry.hasUsage {
			item.HasUsage = true
			item.CPUUsage, item.MemUsage = FormatCPU(entry.usageCPU), FormatMemory(entry.usageMem)
		}
		result.Namespaces = append(result.Namespaces, item)
	}
	// 配额可能落在一个「当前没有 Pod」的命名空间上，这种也要出现，否则配额看不全
	for ns, items := range quotas {
		if byNS[ns] != nil {
			continue
		}
		result.Namespaces = append(result.Namespaces, NamespaceCapacity{
			Namespace: ns, CPURequests: "0", CPULimits: "0",
			MemRequests: "0", MemLimits: "0", Quotas: items,
		})
	}
	sort.Slice(result.Namespaces, func(i, j int) bool {
		return result.Namespaces[i].Namespace < result.Namespaces[j].Namespace
	})
	if quotaErr != nil && result.MetricsNote != "" {
		result.MetricsNote += "；另外 ResourceQuota 读取失败：" + quotaErr.Error()
	}
	return result, nil
}

// resourceQuotas 读 ResourceQuota，按命名空间归类。
// 读不到不算整页失败（多数集群根本没配配额），但错误要带出去。
func (c *Client) resourceQuotas(ctx context.Context, namespace string) (map[string][]QuotaItem, error) {
	var raw struct {
		Items []struct {
			Metadata struct {
				Name      string `json:"name"`
				Namespace string `json:"namespace"`
			} `json:"metadata"`
			Status struct {
				Hard map[string]string `json:"hard"`
				Used map[string]string `json:"used"`
			} `json:"status"`
		} `json:"items"`
	}
	path := namespacedPath("/api/v1", namespace, "resourcequotas")
	if err := c.get(ctx, path, url.Values{}, &raw); err != nil {
		return map[string][]QuotaItem{}, err
	}
	out := map[string][]QuotaItem{}
	for _, item := range raw.Items {
		keys := make([]string, 0, len(item.Status.Hard))
		for key := range item.Status.Hard {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			hard := item.Status.Hard[key]
			used := item.Status.Used[key]
			out[item.Metadata.Namespace] = append(out[item.Metadata.Namespace], QuotaItem{
				Name: item.Metadata.Name, Resource: key, Hard: hard, Used: used,
				Percent: quotaPercent(key, used, hard),
			})
		}
	}
	return out, nil
}

// quotaPercent 配额使用率。资源项五花八门（cpu / memory / pods / count/deployments.apps），
// 按后缀判断该用哪种解析：认不出来就按纯数字比。
func quotaPercent(resource, used, hard string) float64 {
	switch {
	case strings.Contains(resource, "cpu"):
		return percent(ParseCPU(used), ParseCPU(hard))
	case strings.Contains(resource, "memory"), strings.Contains(resource, "storage"):
		return percent(float64(ParseMemory(used)), float64(ParseMemory(hard)))
	default:
		usedNum, _ := strconv.ParseFloat(used, 64)
		hardNum, _ := strconv.ParseFloat(hard, 64)
		return percent(usedNum, hardNum)
	}
}

func nodeReady(status map[string]any) bool {
	conditions, _ := status["conditions"].([]any)
	for _, entry := range conditions {
		cond, _ := entry.(map[string]any)
		if name, _ := cond["type"].(string); name != "Ready" {
			continue
		}
		value, _ := cond["status"].(string)
		return value == "True"
	}
	return false
}

func nodeRoles(meta map[string]any) string {
	roles := make([]string, 0, 2)
	for key := range stringMap(meta["labels"]) {
		if strings.HasPrefix(key, "node-role.kubernetes.io/") {
			roles = append(roles, strings.TrimPrefix(key, "node-role.kubernetes.io/"))
		}
	}
	sort.Strings(roles)
	return strings.Join(roles, ",")
}
