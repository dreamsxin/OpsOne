package k8s

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/goccy/go-yaml"
)

// fieldManager 服务端 apply 的字段管理者标识。
// 集群里能看出哪些字段是这个平台改的（kubectl get -o yaml 的 managedFields）。
const fieldManager = "opsone"

// ResourceKind 平台允许查看与改动的资源类型。
//
// 白名单而不是通用「任意 GVR」：平台是给值班同学用的，误改 RBAC、
// Namespace 的代价远大于省下的灵活性。要动这些仍然走 kubectl。
//
// 白名单里分三档：可写（apply / scale / restart）、只读（能看不能改）、
// 脱敏只读（Secret：能看到有哪些键，看不到值，也不允许提交）。
type ResourceKind struct {
	Kind       string `json:"kind"`
	APIVersion string `json:"apiVersion"`
	Resource   string `json:"resource"`
	// Namespaced 是否属于某个命名空间。false 的是集群级对象（Node / Namespace / PV / StorageClass）
	Namespaced bool `json:"namespaced"`
	// Scalable 支持 /scale 子资源，可以直接改副本数
	Scalable bool `json:"scalable"`
	// Restartable 支持 rollout restart（给 Pod 模板打时间戳注解触发滚动重建）
	Restartable bool `json:"restartable"`
	// ReadOnly 只开放查看：apply / scale / restart 一律拒绝。
	// 这类对象要么是集群自己算出来的（Pod / ReplicaSet / Endpoints），
	// 要么改错了代价太大（Namespace / Node / PV / StorageClass）
	ReadOnly bool `json:"readOnly"`
	// Redacted 详情要脱敏（目前只有 Secret）。脱敏过的 YAML 不能提交回集群 ——
	// 那会把真实的值覆盖成占位符，是最糟糕的一种「功能看起来能用」
	Redacted bool `json:"redacted"`
	// Group 界面上的分组，纯展示用
	Group   string `json:"group"`
	apiRoot string
}

// APIRoot 该类型的 API 根路径（如 /apis/apps/v1）。导出是为了拼子资源路径。
func (k ResourceKind) APIRoot() string { return k.apiRoot }

var resourceKinds = []ResourceKind{
	// 工作负载
	{Kind: "Deployment", APIVersion: "apps/v1", Resource: "deployments", Namespaced: true, Scalable: true, Restartable: true, Group: "工作负载", apiRoot: "/apis/apps/v1"},
	{Kind: "StatefulSet", APIVersion: "apps/v1", Resource: "statefulsets", Namespaced: true, Scalable: true, Restartable: true, Group: "工作负载", apiRoot: "/apis/apps/v1"},
	{Kind: "DaemonSet", APIVersion: "apps/v1", Resource: "daemonsets", Namespaced: true, Restartable: true, Group: "工作负载", apiRoot: "/apis/apps/v1"},
	{Kind: "CronJob", APIVersion: "batch/v1", Resource: "cronjobs", Namespaced: true, Group: "工作负载", apiRoot: "/apis/batch/v1"},
	{Kind: "Job", APIVersion: "batch/v1", Resource: "jobs", Namespaced: true, Group: "工作负载", apiRoot: "/apis/batch/v1"},
	// ReplicaSet / Pod 是控制器算出来的结果，改它们没有意义（会被控制器改回去）
	{Kind: "ReplicaSet", APIVersion: "apps/v1", Resource: "replicasets", Namespaced: true, ReadOnly: true, Group: "工作负载", apiRoot: "/apis/apps/v1"},
	{Kind: "Pod", APIVersion: "v1", Resource: "pods", Namespaced: true, ReadOnly: true, Group: "工作负载", apiRoot: "/api/v1"},

	// 网络
	{Kind: "Service", APIVersion: "v1", Resource: "services", Namespaced: true, Group: "网络", apiRoot: "/api/v1"},
	{Kind: "Ingress", APIVersion: "networking.k8s.io/v1", Resource: "ingresses", Namespaced: true, Group: "网络", apiRoot: "/apis/networking.k8s.io/v1"},
	{Kind: "Endpoints", APIVersion: "v1", Resource: "endpoints", Namespaced: true, ReadOnly: true, Group: "网络", apiRoot: "/api/v1"},

	// 配置
	{Kind: "ConfigMap", APIVersion: "v1", Resource: "configmaps", Namespaced: true, Group: "配置", apiRoot: "/api/v1"},
	{Kind: "Secret", APIVersion: "v1", Resource: "secrets", Namespaced: true, ReadOnly: true, Redacted: true, Group: "配置", apiRoot: "/api/v1"},

	// 存储
	{Kind: "PersistentVolumeClaim", APIVersion: "v1", Resource: "persistentvolumeclaims", Namespaced: true, Group: "存储", apiRoot: "/api/v1"},
	{Kind: "PersistentVolume", APIVersion: "v1", Resource: "persistentvolumes", ReadOnly: true, Group: "存储", apiRoot: "/api/v1"},
	{Kind: "StorageClass", APIVersion: "storage.k8s.io/v1", Resource: "storageclasses", ReadOnly: true, Group: "存储", apiRoot: "/apis/storage.k8s.io/v1"},

	// 集群
	{Kind: "Namespace", APIVersion: "v1", Resource: "namespaces", ReadOnly: true, Group: "集群", apiRoot: "/api/v1"},
	{Kind: "Node", APIVersion: "v1", Resource: "nodes", ReadOnly: true, Group: "集群", apiRoot: "/api/v1"},
}

// SupportedKinds 返回白名单，供界面下拉与前端校验
func SupportedKinds() []ResourceKind {
	out := make([]ResourceKind, len(resourceKinds))
	copy(out, resourceKinds)
	return out
}

// LookupKind 按 kind 名找白名单条目，大小写不敏感
func LookupKind(kind string) (ResourceKind, bool) {
	for _, item := range resourceKinds {
		if strings.EqualFold(item.Kind, kind) {
			return item, true
		}
	}
	return ResourceKind{}, false
}

// ---------- 列表 ----------

// ResourceItem 资源列表里的一行，Summary 是按类型算出来的一句话
type ResourceItem struct {
	Kind      string    `json:"kind"`
	Namespace string    `json:"namespace"`
	Name      string    `json:"name"`
	Summary   string    `json:"summary"`
	CreatedAt time.Time `json:"createdAt"`
	// Labels 只在需要下钻时用得上（比如从 Deployment 找它的 Pod），列表里不展示
	Labels map[string]string `json:"labels,omitempty"`
}

// ListQuery 列表的可选过滤条件
type ListQuery struct {
	// Namespace 留空表示全集群；集群级对象这个字段会被忽略
	Namespace string
	// LabelSelector 标签选择器，形如 app=nginx,tier=web
	LabelSelector string
	// FieldSelector 字段选择器，形如 spec.nodeName=node1
	FieldSelector string
}

// ListObjects 列出某类资源。
func (c *Client) ListObjects(ctx context.Context, kind ResourceKind, namespace string) ([]ResourceItem, error) {
	return c.ListObjectsQuery(ctx, kind, ListQuery{Namespace: namespace})
}

// ListObjectsQuery 带过滤条件的列表。
//
// 集群级对象（Node / Namespace / PV / StorageClass）会忽略传进来的命名空间：
// 拼出 /api/v1/namespaces/x/nodes 这种路径只会拿到 404，错误信息还会很莫名。
func (c *Client) ListObjectsQuery(ctx context.Context, kind ResourceKind, q ListQuery) ([]ResourceItem, error) {
	namespace := q.Namespace
	if !kind.Namespaced {
		namespace = ""
	}
	query := url.Values{}
	if q.LabelSelector != "" {
		query.Set("labelSelector", q.LabelSelector)
	}
	if q.FieldSelector != "" {
		query.Set("fieldSelector", q.FieldSelector)
	}

	var raw struct {
		Items []map[string]any `json:"items"`
	}
	if err := c.get(ctx, namespacedPath(kind.apiRoot, namespace, kind.Resource), query, &raw); err != nil {
		return nil, err
	}
	list := make([]ResourceItem, 0, len(raw.Items))
	for _, obj := range raw.Items {
		meta, _ := obj["metadata"].(map[string]any)
		item := ResourceItem{Kind: kind.Kind, Summary: resourceSummary(kind.Kind, obj)}
		if meta != nil {
			item.Namespace, _ = meta["namespace"].(string)
			item.Name, _ = meta["name"].(string)
			item.Labels = stringMap(meta["labels"])
			if stamp, ok := meta["creationTimestamp"].(string); ok && stamp != "" {
				if parsed, err := time.Parse(time.RFC3339, stamp); err == nil {
					item.CreatedAt = parsed
				}
			}
		}
		list = append(list, item)
	}
	return list, nil
}

// stringMap 把 map[string]any 收成 map[string]string，非字符串值直接丢掉
func stringMap(raw any) map[string]string {
	src, ok := raw.(map[string]any)
	if !ok || len(src) == 0 {
		return nil
	}
	out := make(map[string]string, len(src))
	for key, value := range src {
		if text, ok := value.(string); ok {
			out[key] = text
		}
	}
	return out
}

// resourceSummary 给列表算一句人话摘要。
// 取不到就返回空串——摘要缺失不该让整行读不出来。
func resourceSummary(kind string, obj map[string]any) string {
	spec, _ := obj["spec"].(map[string]any)
	status, _ := obj["status"].(map[string]any)

	switch kind {
	case "Deployment", "StatefulSet", "ReplicaSet":
		return fmt.Sprintf("副本 %d/%d", numField(status, "readyReplicas"), numField(spec, "replicas"))
	case "DaemonSet":
		return fmt.Sprintf("就绪 %d/%d", numField(status, "numberReady"), numField(status, "desiredNumberScheduled"))
	case "CronJob":
		schedule, _ := spec["schedule"].(string)
		if suspend, ok := spec["suspend"].(bool); ok && suspend {
			return schedule + "（已暂停）"
		}
		return schedule
	case "Job":
		// 失败次数要单独说：只看 succeeded 会把「重试了 5 次才成」显示成一切正常
		text := fmt.Sprintf("成功 %d/%d", numField(status, "succeeded"), numField(spec, "completions"))
		if failed := numField(status, "failed"); failed > 0 {
			text += fmt.Sprintf("，失败 %d", failed)
		}
		if active := numField(status, "active"); active > 0 {
			text += fmt.Sprintf("，运行中 %d", active)
		}
		return text
	case "Pod":
		phase, _ := status["phase"].(string)
		node, _ := spec["nodeName"].(string)
		ready, total := podReadyCount(status)
		text := fmt.Sprintf("%s %d/%d", phase, ready, total)
		if restarts := podRestarts(status); restarts > 0 {
			text += fmt.Sprintf("，重启 %d 次", restarts)
		}
		if node != "" {
			text += " @" + node
		}
		return text
	case "Service":
		svcType, _ := spec["type"].(string)
		clusterIP, _ := spec["clusterIP"].(string)
		if clusterIP == "" {
			return svcType
		}
		return svcType + " " + clusterIP
	case "Endpoints":
		count := 0
		subsets, _ := obj["subsets"].([]any)
		for _, entry := range subsets {
			subset, _ := entry.(map[string]any)
			addrs, _ := subset["addresses"].([]any)
			count += len(addrs)
		}
		return fmt.Sprintf("%d 个后端地址", count)
	case "ConfigMap":
		data, _ := obj["data"].(map[string]any)
		return fmt.Sprintf("%d 个键", len(data))
	case "Secret":
		data, _ := obj["data"].(map[string]any)
		secretType, _ := obj["type"].(string)
		return fmt.Sprintf("%s，%d 个键（值不展示）", secretType, len(data))
	case "Ingress":
		hosts := make([]string, 0, 2)
		rules, _ := spec["rules"].([]any)
		for _, entry := range rules {
			rule, _ := entry.(map[string]any)
			if host, ok := rule["host"].(string); ok && host != "" {
				hosts = append(hosts, host)
			}
		}
		return strings.Join(hosts, ", ")
	case "PersistentVolumeClaim":
		phase, _ := status["phase"].(string)
		size := resourceRequestStorage(spec)
		class, _ := spec["storageClassName"].(string)
		text := phase
		if size != "" {
			text += " " + size
		}
		if class != "" {
			text += "（" + class + "）"
		}
		return text
	case "PersistentVolume":
		phase, _ := status["phase"].(string)
		size := ""
		if capacity, ok := spec["capacity"].(map[string]any); ok {
			size, _ = capacity["storage"].(string)
		}
		policy, _ := spec["persistentVolumeReclaimPolicy"].(string)
		text := phase
		if size != "" {
			text += " " + size
		}
		if policy != "" {
			text += "，回收策略 " + policy
		}
		return text
	case "StorageClass":
		provisioner, _ := obj["provisioner"].(string)
		meta, _ := obj["metadata"].(map[string]any)
		if anno := stringMap(meta["annotations"]); anno["storageclass.kubernetes.io/is-default-class"] == "true" {
			return provisioner + "（默认）"
		}
		return provisioner
	case "Namespace":
		phase, _ := status["phase"].(string)
		return phase
	case "Node":
		return nodeSummary(obj)
	}
	return ""
}

// podReadyCount 数 Pod 里就绪的容器数
func podReadyCount(status map[string]any) (ready, total int) {
	statuses, _ := status["containerStatuses"].([]any)
	for _, entry := range statuses {
		item, _ := entry.(map[string]any)
		total++
		if flag, ok := item["ready"].(bool); ok && flag {
			ready++
		}
	}
	return ready, total
}

func podRestarts(status map[string]any) int {
	statuses, _ := status["containerStatuses"].([]any)
	sum := 0
	for _, entry := range statuses {
		item, _ := entry.(map[string]any)
		sum += numField(item, "restartCount")
	}
	return sum
}

// nodeSummary 节点的一句话：就绪状态 + 角色 + kubelet 版本。
// 「就绪」取 Ready 这个 condition，而不是看有没有 taint —— 两者不是一回事
func nodeSummary(obj map[string]any) string {
	status, _ := obj["status"].(map[string]any)
	meta, _ := obj["metadata"].(map[string]any)
	spec, _ := obj["spec"].(map[string]any)

	state := "未知"
	conditions, _ := status["conditions"].([]any)
	for _, entry := range conditions {
		cond, _ := entry.(map[string]any)
		if name, _ := cond["type"].(string); name != "Ready" {
			continue
		}
		if value, _ := cond["status"].(string); value == "True" {
			state = "Ready"
		} else {
			state = "NotReady"
		}
	}
	roles := make([]string, 0, 2)
	for key := range stringMap(meta["labels"]) {
		if strings.HasPrefix(key, "node-role.kubernetes.io/") {
			roles = append(roles, strings.TrimPrefix(key, "node-role.kubernetes.io/"))
		}
	}
	text := state
	if len(roles) > 0 {
		text += " " + strings.Join(roles, ",")
	}
	if info, ok := status["nodeInfo"].(map[string]any); ok {
		if version, _ := info["kubeletVersion"].(string); version != "" {
			text += " " + version
		}
	}
	if flag, ok := spec["unschedulable"].(bool); ok && flag {
		text += "（已封锁）"
	}
	return text
}

// resourceRequestStorage 取 PVC 申请的容量，兼容新旧字段名
func resourceRequestStorage(spec map[string]any) string {
	res, _ := spec["resources"].(map[string]any)
	if res == nil {
		return ""
	}
	for _, key := range []string{"requests", "limits"} {
		if entry, ok := res[key].(map[string]any); ok {
			if size, ok := entry["storage"].(string); ok && size != "" {
				return size
			}
		}
	}
	return ""
}

// numField 从 map 里取一个数字字段。JSON 解出来是 float64，缺失当 0。
func numField(m map[string]any, key string) int {
	if m == nil {
		return 0
	}
	if value, ok := m[key].(float64); ok {
		return int(value)
	}
	return 0
}

// ---------- 单对象读取 ----------

// GetObject 取单个对象的原始 JSON。集群级对象忽略 namespace。
func (c *Client) GetObject(ctx context.Context, kind ResourceKind, namespace, name string) (map[string]any, error) {
	if !kind.Namespaced {
		namespace = ""
	}
	var obj map[string]any
	path := namespacedPath(kind.apiRoot, namespace, kind.Resource) + "/" + url.PathEscape(name)
	if err := c.get(ctx, path, nil, &obj); err != nil {
		return nil, err
	}
	return obj, nil
}

// RedactPlaceholder 脱敏后填进去的占位符。界面与「不能提交」的判断都认这个串。
const RedactPlaceholder = "<已隐藏：平台不展示 Secret 的值>"

// RedactSecret 把 Secret 的 data / stringData 的值换成占位符，只保留键名。
//
// 为什么不直接连 Secret 都不给看：值班时「这个 Secret 里到底有没有 tls.key 这个键」
// 是真实需求，而值本身谁都不需要在平台上看到。返回的 YAML 因此是**不能提交回去的**，
// 调用方必须拒绝 apply（否则会把真实值覆盖成这行占位符）。
func RedactSecret(obj map[string]any) map[string]any {
	if obj == nil {
		return nil
	}
	clone := make(map[string]any, len(obj))
	for key, value := range obj {
		clone[key] = value
	}
	for _, field := range []string{"data", "stringData"} {
		src, ok := clone[field].(map[string]any)
		if !ok {
			continue
		}
		masked := make(map[string]any, len(src))
		for key := range src {
			masked[key] = RedactPlaceholder
		}
		clone[field] = masked
	}
	return clone
}

// noisyMetadata 这些字段是集群自己维护的，回填到 apply 里只会报错或制造无意义差异
var noisyMetadata = []string{
	"managedFields", "resourceVersion", "uid", "generation",
	"creationTimestamp", "selfLink", "ownerReferences",
}

// ToEditableYAML 把对象清成「可以直接改完再提交」的 YAML。
//
// 去掉 status 与集群自己维护的 metadata 字段，以及 kubectl 的
// last-applied-configuration（那是它自己的账本，跟着一起提交只会互相覆盖）。
func ToEditableYAML(obj map[string]any) ([]byte, error) {
	clean := make(map[string]any, len(obj))
	for key, value := range obj {
		if key == "status" {
			continue
		}
		clean[key] = value
	}
	if meta, ok := clean["metadata"].(map[string]any); ok {
		metaCopy := make(map[string]any, len(meta))
		for key, value := range meta {
			metaCopy[key] = value
		}
		for _, key := range noisyMetadata {
			delete(metaCopy, key)
		}
		if annotations, ok := metaCopy["annotations"].(map[string]any); ok {
			annoCopy := make(map[string]any, len(annotations))
			for key, value := range annotations {
				if key == "kubectl.kubernetes.io/last-applied-configuration" ||
					key == "deployment.kubernetes.io/revision" {
					continue
				}
				annoCopy[key] = value
			}
			if len(annoCopy) == 0 {
				delete(metaCopy, "annotations")
			} else {
				metaCopy["annotations"] = annoCopy
			}
		}
		clean["metadata"] = metaCopy
	}

	encoded, err := json.Marshal(clean)
	if err != nil {
		return nil, err
	}
	return yaml.JSONToYAML(encoded)
}

// ---------- 提交改动 ----------

// Manifest 从提交的 YAML 里解出来的身份信息
type Manifest struct {
	APIVersion string
	Kind       string
	Name       string
	Namespace  string
}

// ParseManifest 解析单个对象的 YAML，只取身份字段并做基本校验。
// 多文档（--- 分隔）直接拒绝：一次只改一个对象，失败原因才说得清。
func ParseManifest(raw string) (*Manifest, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, fmt.Errorf("YAML 内容不能为空")
	}
	if documentCount(raw) > 1 {
		return nil, fmt.Errorf("一次只能提交一个对象，检测到多段 YAML（--- 分隔），请拆开分别提交")
	}
	encoded, err := yaml.YAMLToJSON([]byte(raw))
	if err != nil {
		return nil, fmt.Errorf("YAML 解析失败: %w", err)
	}
	var obj struct {
		APIVersion string `json:"apiVersion"`
		Kind       string `json:"kind"`
		Metadata   struct {
			Name      string `json:"name"`
			Namespace string `json:"namespace"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(encoded, &obj); err != nil {
		return nil, fmt.Errorf("YAML 顶层结构不是一个对象: %w", err)
	}
	if obj.Kind == "" || obj.APIVersion == "" {
		return nil, fmt.Errorf("YAML 里缺少 apiVersion 或 kind")
	}
	if obj.Metadata.Name == "" {
		return nil, fmt.Errorf("YAML 里缺少 metadata.name")
	}
	return &Manifest{
		APIVersion: obj.APIVersion, Kind: obj.Kind,
		Name: obj.Metadata.Name, Namespace: obj.Metadata.Namespace,
	}, nil
}

// documentCount 数 YAML 里有几段文档。只认单独成行的 ---。
func documentCount(raw string) int {
	count := 1
	for _, line := range strings.Split(raw, "\n") {
		if strings.TrimSpace(strings.TrimRight(line, "\r")) != "---" {
			continue
		}
		count++
	}
	// 开头的 --- 不算新起一段
	if strings.HasPrefix(strings.TrimSpace(raw), "---") {
		count--
	}
	return count
}

// ApplyOptions 一次 apply 的开关
type ApplyOptions struct {
	// DryRun 走服务端预检：API Server 完整校验（含准入控制）但不落盘
	DryRun bool
	// Force 字段冲突时强行接管。别人（比如 kubectl）管着同一字段时才需要
	Force bool
}

// Apply 服务端 apply（application/apply-patch+yaml）。
//
// 用 SSA 而不是 get-改-put：不用自己处理 resourceVersion 冲突，对象不存在时
// 直接创建，同一个 fieldManager 反复提交也不会和别人的字段互相抹掉。
func (c *Client) Apply(ctx context.Context, kind ResourceKind, namespace, name string,
	body []byte, opts ApplyOptions) (map[string]any, error) {
	if kind.ReadOnly {
		return nil, fmt.Errorf("%s 在平台上是只读的", kind.Kind)
	}
	query := url.Values{}
	query.Set("fieldManager", fieldManager)
	if opts.DryRun {
		query.Set("dryRun", "All")
	}
	if opts.Force {
		query.Set("force", "true")
	}
	path := namespacedPath(kind.apiRoot, namespace, kind.Resource) + "/" + url.PathEscape(name)

	var out map[string]any
	err := c.do(ctx, http.MethodPatch, path, query, "application/apply-patch+yaml", body, &out)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ScaleResult 改副本数之后的实际情况
type ScaleResult struct {
	Previous int `json:"previous"`
	Current  int `json:"current"`
}

// Scale 通过 /scale 子资源改副本数。
// 先读一次拿到改之前的值，好在记录和界面上说清「2 → 4」。
func (c *Client) Scale(ctx context.Context, kind ResourceKind, namespace, name string,
	replicas int, dryRun bool) (*ScaleResult, error) {
	if !kind.Scalable {
		return nil, fmt.Errorf("%s 不支持直接改副本数", kind.Kind)
	}
	path := namespacedPath(kind.apiRoot, namespace, kind.Resource) + "/" + url.PathEscape(name) + "/scale"

	var before struct {
		Spec struct {
			Replicas int `json:"replicas"`
		} `json:"spec"`
	}
	if err := c.get(ctx, path, nil, &before); err != nil {
		return nil, err
	}

	query := url.Values{}
	query.Set("fieldManager", fieldManager)
	if dryRun {
		query.Set("dryRun", "All")
	}
	patch := []byte(`{"spec":{"replicas":` + strconv.Itoa(replicas) + `}}`)

	var after struct {
		Spec struct {
			Replicas int `json:"replicas"`
		} `json:"spec"`
	}
	if err := c.do(ctx, http.MethodPatch, path, query,
		"application/merge-patch+json", patch, &after); err != nil {
		return nil, err
	}
	return &ScaleResult{Previous: before.Spec.Replicas, Current: after.Spec.Replicas}, nil
}

// ---------- 下钻：事件、关联 Pod、滚动重启 ----------

// restartAnnotation 与 kubectl rollout restart 用同一个注解键。
// 用同一个键的好处是：平台重启过的工作负载，kubectl 看到的也是同一回事，
// 不会出现两套互不相认的时间戳注解。
const restartAnnotation = "kubectl.kubernetes.io/restartedAt"

// ObjectEvents 按对象反查事件。
//
// 之前只能按命名空间捞一把事件自己找，排查「这个 Pod 为什么起不来」很别扭。
// 这里用 fieldSelector 让 API Server 过滤，省得把整命名空间的事件拉回来。
func (c *Client) ObjectEvents(ctx context.Context, namespace, kind, name string, limit int) ([]Event, error) {
	selector := fmt.Sprintf("involvedObject.name=%s", name)
	if kind != "" {
		selector += ",involvedObject.kind=" + kind
	}
	query := url.Values{}
	query.Set("fieldSelector", selector)
	if limit > 0 {
		query.Set("limit", strconv.Itoa(limit))
	}

	var raw eventList
	if err := c.get(ctx, namespacedPath("/api/v1", namespace, "events"), query, &raw); err != nil {
		return nil, err
	}
	return convertEvents(raw), nil
}

// PodSelector 从工作负载 / Service 对象里取出「它的 Pod 怎么选」的标签选择器。
//
// 只认等值匹配（matchLabels / spec.selector）：matchExpressions 表达能力更强，
// 但 fieldSelector 拼不出来，硬拼只会给出一个看似对、实则漏的结果。
// 返回空串表示「问不出来」，调用方要照实说，不能当成「没有 Pod」。
func PodSelector(obj map[string]any) string {
	spec, _ := obj["spec"].(map[string]any)
	if spec == nil {
		return ""
	}
	// 工作负载：spec.selector.matchLabels
	if selector, ok := spec["selector"].(map[string]any); ok {
		if match, ok := selector["matchLabels"].(map[string]any); ok {
			return joinSelector(stringMap(match))
		}
		// Service：spec.selector 直接就是键值对
		if plain := stringMap(selector); len(plain) > 0 {
			return joinSelector(plain)
		}
	}
	return ""
}

func joinSelector(labels map[string]string) string {
	if len(labels) == 0 {
		return ""
	}
	keys := make([]string, 0, len(labels))
	for key := range labels {
		keys = append(keys, key)
	}
	sort.Strings(keys) // 顺序固定，便于比对与写进日志
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+"="+labels[key])
	}
	return strings.Join(parts, ",")
}

// RestartResult 一次滚动重启的结果
type RestartResult struct {
	// RestartedAt 打进 Pod 模板的时间戳，和 kubectl rollout restart 的注解一致
	RestartedAt string `json:"restartedAt"`
	// Replicas 期望副本数，用来提示会有多少个 Pod 被换掉
	Replicas int `json:"replicas"`
}

// RolloutRestart 滚动重启：给 Pod 模板打一个时间戳注解，让控制器重建 Pod。
//
// 这是 kubectl rollout restart 的原理，也是唯一不删对象就能重建 Pod 的正规做法。
// 注意它**不是**「重启容器」：Pod 会被新建替换，滚动策略与就绪探针决定过程是否有损。
func (c *Client) RolloutRestart(ctx context.Context, kind ResourceKind, namespace, name string,
	stamp time.Time, dryRun bool) (*RestartResult, error) {
	if !kind.Restartable {
		return nil, fmt.Errorf("%s 不支持滚动重启（只有 Deployment / StatefulSet / DaemonSet 有 Pod 模板）", kind.Kind)
	}
	restartedAt := stamp.UTC().Format(time.RFC3339)
	patch := map[string]any{
		"spec": map[string]any{
			"template": map[string]any{
				"metadata": map[string]any{
					"annotations": map[string]any{restartAnnotation: restartedAt},
				},
			},
		},
	}
	body, err := json.Marshal(patch)
	if err != nil {
		return nil, err
	}

	query := url.Values{}
	query.Set("fieldManager", fieldManager)
	if dryRun {
		query.Set("dryRun", "All")
	}
	path := namespacedPath(kind.apiRoot, namespace, kind.Resource) + "/" + url.PathEscape(name)

	var out map[string]any
	if err := c.do(ctx, http.MethodPatch, path, query,
		"application/merge-patch+json", body, &out); err != nil {
		return nil, err
	}
	result := &RestartResult{RestartedAt: restartedAt}
	if spec, ok := out["spec"].(map[string]any); ok {
		result.Replicas = numField(spec, "replicas")
	}
	return result, nil
}
