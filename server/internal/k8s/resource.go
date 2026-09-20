package k8s

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
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
// 白名单而不是通用「任意 GVR」：平台是给值班同学用的，误改 RBAC、Secret、
// Namespace 的代价远大于省下的灵活性。要动这些仍然走 kubectl。
type ResourceKind struct {
	Kind       string `json:"kind"`
	APIVersion string `json:"apiVersion"`
	Resource   string `json:"resource"`
	// Namespaced 是否属于某个命名空间；本白名单里目前全是 true
	Namespaced bool `json:"namespaced"`
	// Scalable 支持 /scale 子资源，可以直接改副本数
	Scalable bool `json:"scalable"`
	apiRoot  string
}

var resourceKinds = []ResourceKind{
	{Kind: "Deployment", APIVersion: "apps/v1", Resource: "deployments", Namespaced: true, Scalable: true, apiRoot: "/apis/apps/v1"},
	{Kind: "StatefulSet", APIVersion: "apps/v1", Resource: "statefulsets", Namespaced: true, Scalable: true, apiRoot: "/apis/apps/v1"},
	{Kind: "DaemonSet", APIVersion: "apps/v1", Resource: "daemonsets", Namespaced: true, apiRoot: "/apis/apps/v1"},
	{Kind: "CronJob", APIVersion: "batch/v1", Resource: "cronjobs", Namespaced: true, apiRoot: "/apis/batch/v1"},
	{Kind: "Service", APIVersion: "v1", Resource: "services", Namespaced: true, apiRoot: "/api/v1"},
	{Kind: "ConfigMap", APIVersion: "v1", Resource: "configmaps", Namespaced: true, apiRoot: "/api/v1"},
	{Kind: "Ingress", APIVersion: "networking.k8s.io/v1", Resource: "ingresses", Namespaced: true, apiRoot: "/apis/networking.k8s.io/v1"},
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
}

// ListObjects 列出某类资源。namespace 为空表示全集群。
func (c *Client) ListObjects(ctx context.Context, kind ResourceKind, namespace string) ([]ResourceItem, error) {
	var raw struct {
		Items []map[string]any `json:"items"`
	}
	if err := c.get(ctx, namespacedPath(kind.apiRoot, namespace, kind.Resource), nil, &raw); err != nil {
		return nil, err
	}
	list := make([]ResourceItem, 0, len(raw.Items))
	for _, obj := range raw.Items {
		meta, _ := obj["metadata"].(map[string]any)
		item := ResourceItem{Kind: kind.Kind, Summary: resourceSummary(kind.Kind, obj)}
		if meta != nil {
			item.Namespace, _ = meta["namespace"].(string)
			item.Name, _ = meta["name"].(string)
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

// resourceSummary 给列表算一句人话摘要。
// 取不到就返回空串——摘要缺失不该让整行读不出来。
func resourceSummary(kind string, obj map[string]any) string {
	spec, _ := obj["spec"].(map[string]any)
	status, _ := obj["status"].(map[string]any)

	switch kind {
	case "Deployment", "StatefulSet":
		return fmt.Sprintf("副本 %d/%d", numField(status, "readyReplicas"), numField(spec, "replicas"))
	case "DaemonSet":
		return fmt.Sprintf("就绪 %d/%d", numField(status, "numberReady"), numField(status, "desiredNumberScheduled"))
	case "CronJob":
		schedule, _ := spec["schedule"].(string)
		if suspend, ok := spec["suspend"].(bool); ok && suspend {
			return schedule + "（已暂停）"
		}
		return schedule
	case "Service":
		svcType, _ := spec["type"].(string)
		clusterIP, _ := spec["clusterIP"].(string)
		if clusterIP == "" {
			return svcType
		}
		return svcType + " " + clusterIP
	case "ConfigMap":
		data, _ := obj["data"].(map[string]any)
		return fmt.Sprintf("%d 个键", len(data))
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

// GetObject 取单个对象的原始 JSON
func (c *Client) GetObject(ctx context.Context, kind ResourceKind, namespace, name string) (map[string]any, error) {
	var obj map[string]any
	path := namespacedPath(kind.apiRoot, namespace, kind.Resource) + "/" + url.PathEscape(name)
	if err := c.get(ctx, path, nil, &obj); err != nil {
		return nil, err
	}
	return obj, nil
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
