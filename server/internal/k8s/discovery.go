package k8s

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/goccy/go-yaml"
)

// 这个文件补三块白名单表答不了的能力：
//
//  1. **CRD 发现与自定义资源浏览** —— 白名单是编译期固定的 18 种，而 CRD 每个集群都不一样。
//     要看自定义资源，只能运行时从集群里读 CRD 定义再现场拼 GVR。
//  2. **Helm 应用** —— Helm 3 把 release 存成 `type=helm.sh/release.v1` 的 Secret，
//     值是 base64(gzip(json))。解出来就是真实的 release 清单，不需要 helm 二进制。
//  3. **RBAC 反查** —— 「这个 ServiceAccount 实际上能干什么」要从
//     RoleBinding/ClusterRoleBinding 反查到 Role/ClusterRole 的 rules 才答得出来。
//
// 共同的底线：**全部只读**。自定义资源的语义平台不懂（改一条 Gateway 可能让整个入口断掉），
// Helm 的 install/upgrade/rollback 需要真正的 Helm 引擎（模板渲染、钩子、依赖），
// RBAC 改错是提权。这三件事都仍然走 kubectl / helm。

// ---------- 运行时构造的资源类型 ----------

// NewReadOnlyKind 按 apiVersion + 复数资源名现场构造一个**只读**资源类型。
//
// 存在的理由：`resourceKinds` 那张白名单表是编译期固定的，列不出集群里的 CRD。
// 一律只读是刻意的 —— 平台不懂自定义资源的语义，误改一条 Gateway 或
// ArgoCD Application 的代价远大于省下的那点方便。
func NewReadOnlyKind(apiVersion, resource, kind, group string, namespaced bool) ResourceKind {
	return ResourceKind{
		Kind: kind, APIVersion: apiVersion, Resource: resource,
		Namespaced: namespaced, ReadOnly: true, Group: group,
		apiRoot: apiRootOf(apiVersion),
	}
}

// apiRootOf 由 apiVersion 推出 API 根路径。
// 核心组（apiVersion 里没有斜杠，如 `v1`）走 /api，其它走 /apis。
func apiRootOf(apiVersion string) string {
	if !strings.Contains(apiVersion, "/") {
		return "/api/" + apiVersion
	}
	return "/apis/" + apiVersion
}

// ListRawObjects 列出某类资源的**原始对象**，不做摘要化。
//
// 与 ListObjectsQuery 的分工：后者把对象压成 ResourceItem（一行一句摘要），
// 适合列表页；这里保留原始 JSON，给需要读具体字段的场景用
// （Helm release 要解 Secret 的 data，Gateway API 要解 spec.rules）。
func (c *Client) ListRawObjects(ctx context.Context, kind ResourceKind, q ListQuery) ([]map[string]any, error) {
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
	path := namespacedPath(kind.apiRoot, namespace, kind.Resource)
	if err := c.get(ctx, path, query, &raw); err != nil {
		return nil, err
	}
	return raw.Items, nil
}

// ---------- CRD 发现 ----------

// crdKind 读 CRD 定义本身用的类型（CRD 也是一种资源）
var crdKind = ResourceKind{
	Kind: "CustomResourceDefinition", APIVersion: "apiextensions.k8s.io/v1",
	Resource: "customresourcedefinitions", ReadOnly: true, Group: "扩展",
	apiRoot: "/apis/apiextensions.k8s.io/v1",
}

// CRDInfo 一条自定义资源定义。
//
// ServedVersions 与 StorageVersion 分开给：多版本 CRD 很常见，
// 「能查哪些版本」和「实际存的是哪个版本」是两件事，混在一起会让人查错版本。
type CRDInfo struct {
	Name  string `json:"name"`  // 完整名，如 applications.argoproj.io
	Group string `json:"group"` // argoproj.io
	Kind  string `json:"kind"`  // Application
	// Plural 复数资源名，拼 API 路径用
	Plural     string   `json:"plural"`
	Singular   string   `json:"singular"`
	ShortNames []string `json:"shortNames"`
	Categories []string `json:"categories"`
	// Scope Namespaced | Cluster
	Scope          string    `json:"scope"`
	Namespaced     bool      `json:"namespaced"`
	ServedVersions []string  `json:"servedVersions"`
	StorageVersion string    `json:"storageVersion"`
	CreatedAt      time.Time `json:"createdAt"`
	// Established CRD 是否已被 API Server 接受。false 时查它的实例会失败，
	// 界面上要能看出来是「CRD 自己没就绪」而不是「查不到东西」
	Established bool `json:"established"`
	// KnownAs 平台认得的知名 CRD 组的中文说明，认不出就是空串。
	// 不假装认得所有 CRD —— 这个字段只是给常见的那几个加一句提示
	KnownAs string `json:"knownAs"`
}

// knownCRDGroups 平台认得的几个常见 CRD 组。
// 只是给界面加一句说明，认不出的 CRD 照样能浏览 —— 不认得不等于不支持。
var knownCRDGroups = map[string]string{
	"gateway.networking.k8s.io":   "Gateway API（Ingress 的后继者）",
	"cert-manager.io":             "cert-manager 证书自动签发",
	"monitoring.coreos.com":       "Prometheus Operator",
	"argoproj.io":                 "Argo CD / Argo Workflows",
	"traefik.io":                  "Traefik 入口",
	"traefik.containo.us":         "Traefik 入口（旧组名）",
	"istio.io":                    "Istio 服务网格",
	"networking.istio.io":         "Istio 流量管理",
	"security.istio.io":           "Istio 安全策略",
	"crd.projectcalico.org":       "Calico 网络",
	"postgresql.cnpg.io":          "CloudNativePG",
	"apps.kruise.io":              "OpenKruise",
	"serving.knative.dev":         "Knative Serving",
	"external-secrets.io":         "External Secrets Operator",
	"velero.io":                   "Velero 备份",
	"ceph.rook.io":                "Rook Ceph 存储",
	"kustomize.toolkit.fluxcd.io": "Flux CD",
	"helm.toolkit.fluxcd.io":      "Flux CD（Helm）",
}

// ListCRDs 列出集群里的全部 CRD。
func (c *Client) ListCRDs(ctx context.Context) ([]CRDInfo, error) {
	items, err := c.ListRawObjects(ctx, crdKind, ListQuery{})
	if err != nil {
		return nil, err
	}

	out := make([]CRDInfo, 0, len(items))
	for _, obj := range items {
		info := CRDInfo{}
		meta, _ := obj["metadata"].(map[string]any)
		if meta != nil {
			info.Name, _ = meta["name"].(string)
			if stamp, ok := meta["creationTimestamp"].(string); ok {
				if parsed, err := time.Parse(time.RFC3339, stamp); err == nil {
					info.CreatedAt = parsed
				}
			}
		}
		spec, _ := obj["spec"].(map[string]any)
		if spec == nil {
			continue
		}
		info.Group, _ = spec["group"].(string)
		info.Scope, _ = spec["scope"].(string)
		info.Namespaced = strings.EqualFold(info.Scope, "Namespaced")
		if names, ok := spec["names"].(map[string]any); ok {
			info.Kind, _ = names["kind"].(string)
			info.Plural, _ = names["plural"].(string)
			info.Singular, _ = names["singular"].(string)
			info.ShortNames = stringSlice(names["shortNames"])
			info.Categories = stringSlice(names["categories"])
		}
		// 版本：served 的收进列表，storage 的单独记
		if versions, ok := spec["versions"].([]any); ok {
			for _, raw := range versions {
				v, _ := raw.(map[string]any)
				if v == nil {
					continue
				}
				name, _ := v["name"].(string)
				if name == "" {
					continue
				}
				if served, ok := v["served"].(bool); ok && served {
					info.ServedVersions = append(info.ServedVersions, name)
				}
				if storage, ok := v["storage"].(bool); ok && storage {
					info.StorageVersion = name
				}
			}
		}
		// Established：CRD 自己有没有被 API Server 接受
		if status, ok := obj["status"].(map[string]any); ok {
			if conds, ok := status["conditions"].([]any); ok {
				for _, raw := range conds {
					cond, _ := raw.(map[string]any)
					if cond == nil {
						continue
					}
					if t, _ := cond["type"].(string); t == "Established" {
						s, _ := cond["status"].(string)
						info.Established = s == "True"
					}
				}
			}
		}
		info.KnownAs = knownCRDGroups[info.Group]
		out = append(out, info)
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Group != out[j].Group {
			return out[i].Group < out[j].Group
		}
		return out[i].Kind < out[j].Kind
	})
	return out, nil
}

// KindForCRD 由 CRD 定义构造出可以查实例的资源类型。
//
// version 留空时用 StorageVersion；它也为空就退回第一个 served 版本。
// 版本选错会 404，所以这里宁可挑不到就报错，也不猜一个。
func KindForCRD(info CRDInfo, version string) (ResourceKind, error) {
	if info.Plural == "" || info.Group == "" {
		return ResourceKind{}, fmt.Errorf("CRD %s 缺少 group 或复数名，无法拼出 API 路径", info.Name)
	}
	if version == "" {
		version = info.StorageVersion
	}
	if version == "" && len(info.ServedVersions) > 0 {
		version = info.ServedVersions[0]
	}
	if version == "" {
		return ResourceKind{}, fmt.Errorf("CRD %s 没有任何 served 版本", info.Name)
	}
	// 只允许 served 的版本：查一个没在服务的版本只会拿到 404
	if len(info.ServedVersions) > 0 && !containsString(info.ServedVersions, version) {
		return ResourceKind{}, fmt.Errorf("CRD %s 的 %s 版本未在服务，可选：%s",
			info.Name, version, strings.Join(info.ServedVersions, "、"))
	}

	apiVersion := info.Group + "/" + version
	label := info.Group
	if info.KnownAs != "" {
		label = info.KnownAs
	}
	return NewReadOnlyKind(apiVersion, info.Plural, info.Kind, label, info.Namespaced), nil
}

// ---------- Helm release ----------

// helmSecretKind 读 Helm release Secret 用的类型。
// 注意它与白名单里那条 Secret 是同一个 GVR，但这里**不脱敏** ——
// Helm release 的载荷就在 data.release 里，脱敏了就什么都解不出来。
// 解出来之后返回给界面的只有 chart/版本/状态那几样，不含 values 里的密码。
var helmSecretKind = ResourceKind{
	Kind: "Secret", APIVersion: "v1", Resource: "secrets",
	Namespaced: true, ReadOnly: true, Group: "配置",
	apiRoot: "/api/v1",
}

// helmReleaseSecretType Helm 3 存 release 用的 Secret 类型
const helmReleaseSecretType = "helm.sh/release.v1"

// HelmRelease 一个 Helm release 的当前状态。
//
// 字段全部来自 Secret 里那段 JSON，没有一处是猜的。
// **刻意不返回 values 与 manifest**：values 里经常有数据库口令、
// manifest 是整个渲染结果（可能几百 KB），都不该顺手摊在列表接口上。
type HelmRelease struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	// Revision 第几次发布。Helm 每次 upgrade 都新建一个 Secret，这里只留最大的那个
	Revision int `json:"revision"`
	// Status deployed | failed | pending-install | pending-upgrade | superseded | uninstalled …
	Status      string    `json:"status"`
	Chart       string    `json:"chart"`
	ChartVer    string    `json:"chartVersion"`
	AppVersion  string    `json:"appVersion"`
	Description string    `json:"description"`
	FirstAt     time.Time `json:"firstDeployedAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
	// Revisions 这个 release 一共留了几个历史版本（Secret 个数）
	Revisions int `json:"revisions"`
	// SecretName 当前版本对应的 Secret 名，便于去资源管理页对照
	SecretName string `json:"secretName"`
	// HasNotes chart 的 NOTES.txt 是否有内容（内容不在列表里返回）
	HasNotes bool `json:"hasNotes"`
}

// ListHelmReleases 列出 Helm release。
//
// 做法：按 `type=helm.sh/release.v1` 用 fieldSelector 让 API Server 帮我们筛
// （不是拉全部 Secret 回来过滤 —— 那在大集群里是几十兆的传输），
// 再解每个 Secret 的 data.release。同一个 release 的多个 revision 只留最大的那个，
// 但会报出一共有几个历史版本。
//
// 已知限制（界面上照实写明）：
//   - 只认 Helm 3 默认的 secret driver。用 `--history-max 0` 或 configmap driver 的看不到
//   - 只读：install / upgrade / rollback / uninstall 都不做，那需要真正的 Helm 引擎
func (c *Client) ListHelmReleases(ctx context.Context, namespace string) ([]HelmRelease, error) {
	items, err := c.ListRawObjects(ctx, helmSecretKind, ListQuery{
		Namespace:     namespace,
		FieldSelector: "type=" + helmReleaseSecretType,
	})
	if err != nil {
		return nil, err
	}

	// 同名 release 取 revision 最大的一条，并统计历史个数
	best := map[string]HelmRelease{}
	counts := map[string]int{}
	for _, obj := range items {
		release, ok := decodeHelmSecret(obj)
		if !ok {
			continue
		}
		key := release.Namespace + "/" + release.Name
		counts[key]++
		if prev, exists := best[key]; !exists || release.Revision > prev.Revision {
			best[key] = release
		}
	}

	out := make([]HelmRelease, 0, len(best))
	for key, release := range best {
		release.Revisions = counts[key]
		out = append(out, release)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Namespace != out[j].Namespace {
			return out[i].Namespace < out[j].Namespace
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// decodeHelmSecret 解一个 Helm release Secret。
//
// 载荷路径：data.release →（k8s 的）base64 →（Helm 的）base64 → gzip → JSON。
// 两层 base64 是 Helm 自己的实现细节；老版本没有 gzip，所以这里按魔数判断而不是硬解。
func decodeHelmSecret(obj map[string]any) (HelmRelease, bool) {
	data, _ := obj["data"].(map[string]any)
	if data == nil {
		return HelmRelease{}, false
	}
	encoded, _ := data["release"].(string)
	if encoded == "" {
		return HelmRelease{}, false
	}

	// 第一层：k8s 把 Secret 的值 base64 过一次
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return HelmRelease{}, false
	}
	// 第二层：Helm 自己又 base64 了一次。老版本可能没有，所以解不开就当已经是原文
	if inner, err := base64.StdEncoding.DecodeString(string(raw)); err == nil {
		raw = inner
	}
	// gzip：按魔数判断，不硬解
	if len(raw) > 2 && raw[0] == 0x1f && raw[1] == 0x8b {
		reader, err := gzip.NewReader(bytes.NewReader(raw))
		if err != nil {
			return HelmRelease{}, false
		}
		defer reader.Close()
		// 8MB 上限：release 载荷含渲染后的全部 manifest，坏数据不该把内存吃光
		plain, err := io.ReadAll(io.LimitReader(reader, 8<<20))
		if err != nil {
			return HelmRelease{}, false
		}
		raw = plain
	}

	var payload struct {
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
		Version   int    `json:"version"`
		Info      struct {
			Status        string `json:"status"`
			Description   string `json:"description"`
			Notes         string `json:"notes"`
			FirstDeployed string `json:"first_deployed"`
			LastDeployed  string `json:"last_deployed"`
		} `json:"info"`
		Chart struct {
			Metadata struct {
				Name       string `json:"name"`
				Version    string `json:"version"`
				AppVersion string `json:"appVersion"`
			} `json:"metadata"`
		} `json:"chart"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return HelmRelease{}, false
	}
	if payload.Name == "" {
		return HelmRelease{}, false
	}

	release := HelmRelease{
		Name: payload.Name, Namespace: payload.Namespace,
		Revision: payload.Version, Status: payload.Info.Status,
		Chart: payload.Chart.Metadata.Name, ChartVer: payload.Chart.Metadata.Version,
		AppVersion:  payload.Chart.Metadata.AppVersion,
		Description: payload.Info.Description,
		HasNotes:    strings.TrimSpace(payload.Info.Notes) != "",
	}
	release.FirstAt = parseHelmTime(payload.Info.FirstDeployed)
	release.UpdatedAt = parseHelmTime(payload.Info.LastDeployed)

	// Secret 的命名空间比载荷里的可靠（载荷是安装时写死的，迁移过就不准）
	if meta, ok := obj["metadata"].(map[string]any); ok {
		if ns, _ := meta["namespace"].(string); ns != "" {
			release.Namespace = ns
		}
		release.SecretName, _ = meta["name"].(string)
	}
	return release, true
}

// parseHelmTime Helm 写的时间戳带纳秒与时区，解不开就返回零值（界面显示「—」）
func parseHelmTime(raw string) time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, raw); err == nil {
			return t
		}
	}
	return time.Time{}
}

// ---------- RBAC 反查 ----------

var (
	saKind = ResourceKind{
		Kind: "ServiceAccount", APIVersion: "v1", Resource: "serviceaccounts",
		Namespaced: true, ReadOnly: true, Group: "RBAC", apiRoot: "/api/v1",
	}
	roleKind = ResourceKind{
		Kind: "Role", APIVersion: "rbac.authorization.k8s.io/v1", Resource: "roles",
		Namespaced: true, ReadOnly: true, Group: "RBAC",
		apiRoot: "/apis/rbac.authorization.k8s.io/v1",
	}
	clusterRoleKind = ResourceKind{
		Kind: "ClusterRole", APIVersion: "rbac.authorization.k8s.io/v1", Resource: "clusterroles",
		ReadOnly: true, Group: "RBAC", apiRoot: "/apis/rbac.authorization.k8s.io/v1",
	}
	roleBindingKind = ResourceKind{
		Kind: "RoleBinding", APIVersion: "rbac.authorization.k8s.io/v1", Resource: "rolebindings",
		Namespaced: true, ReadOnly: true, Group: "RBAC",
		apiRoot: "/apis/rbac.authorization.k8s.io/v1",
	}
	clusterRoleBindingKind = ResourceKind{
		Kind: "ClusterRoleBinding", APIVersion: "rbac.authorization.k8s.io/v1",
		Resource: "clusterrolebindings", ReadOnly: true, Group: "RBAC",
		apiRoot: "/apis/rbac.authorization.k8s.io/v1",
	}
)

// PolicyRule 一条权限规则，字段名与 RBAC 原文一致
type PolicyRule struct {
	APIGroups     []string `json:"apiGroups"`
	Resources     []string `json:"resources"`
	Verbs         []string `json:"verbs"`
	ResourceNames []string `json:"resourceNames,omitempty"`
	NonResource   []string `json:"nonResourceURLs,omitempty"`
}

// RoleRef 一次授权：哪个 binding 把哪个 role 给了这个主体
type RoleRef struct {
	// BindingKind RoleBinding | ClusterRoleBinding
	BindingKind string `json:"bindingKind"`
	BindingName string `json:"bindingName"`
	// BindingNamespace ClusterRoleBinding 时为空
	BindingNamespace string `json:"bindingNamespace"`
	// RoleKind Role | ClusterRole
	RoleKind string `json:"roleKind"`
	RoleName string `json:"roleName"`
	// Scope 这次授权实际生效的范围：`命名空间 xxx` 或 `整个集群`
	Scope string `json:"scope"`
	// Rules 这个 role 的规则。role 找不到时为空并在 Missing 里说明
	Rules []PolicyRule `json:"rules"`
	// Missing 引用的 role 不存在 —— 这是真实会出现的配置错误，
	// 表现是「绑了但什么权限都没有」，必须显式报出来
	Missing bool `json:"missing"`
}

// ServiceAccountView 一个 ServiceAccount 及它实际拿到的权限
type ServiceAccountView struct {
	Name      string    `json:"name"`
	Namespace string    `json:"namespace"`
	CreatedAt time.Time `json:"createdAt"`
	// Secrets 挂在 SA 上的 Secret 名（k8s 1.24 之后默认不再自动创建）
	Secrets []string `json:"secrets"`
	// Bindings 授权给它的全部 binding
	Bindings []RoleRef `json:"bindings"`
	// ClusterAdmin 是否被绑到了 cluster-admin。单独标出来：
	// 这是最该被一眼看到的一件事
	ClusterAdmin bool `json:"clusterAdmin"`
	// Wildcard 是否拿到了通配符权限（apiGroups/resources/verbs 里有 `*`）
	Wildcard bool `json:"wildcard"`
	// VerbSummary 汇总后的动词集合，给列表页一眼看出「能不能写」
	VerbSummary []string `json:"verbSummary"`
}

// rbacSubject binding 里的一个主体
type rbacSubject struct {
	Kind      string
	Name      string
	Namespace string
}

// RBACSnapshot 一次性读齐 RBAC 的四类对象 + ServiceAccount。
//
// 为什么一次读齐而不是按需查：反查需要全量 binding 才能回答「谁绑到了这个 SA」——
// 按 SA 逐个查 binding 在 API 层没有对应的过滤条件（binding 的 subjects 不是可选择字段），
// 只能全量拉回来在内存里连。中小集群这几张表都在百量级。
type RBACSnapshot struct {
	ServiceAccounts []ServiceAccountView `json:"serviceAccounts"`
	// RoleCount / ClusterRoleCount 让界面能说清这次扫了多少
	RoleCount           int `json:"roleCount"`
	ClusterRoleCount    int `json:"clusterRoleCount"`
	BindingCount        int `json:"bindingCount"`
	ClusterBindingCount int `json:"clusterBindingCount"`
	// OrphanBindings 引用了不存在的 role 的 binding 数
	OrphanBindings int `json:"orphanBindings"`
}

// RBAC 读齐 RBAC 并算出每个 ServiceAccount 实际拿到的权限。
func (c *Client) RBAC(ctx context.Context, namespace string) (RBACSnapshot, error) {
	var snap RBACSnapshot

	sas, err := c.ListRawObjects(ctx, saKind, ListQuery{Namespace: namespace})
	if err != nil {
		return snap, fmt.Errorf("读取 ServiceAccount 失败: %w", err)
	}
	// Role / Binding 一律全集群读：命名空间 A 里的 SA 可能被
	// ClusterRoleBinding 授权，只读它自己的命名空间会漏掉最危险的那一类
	roles, err := c.ListRawObjects(ctx, roleKind, ListQuery{})
	if err != nil {
		return snap, fmt.Errorf("读取 Role 失败: %w", err)
	}
	clusterRoles, err := c.ListRawObjects(ctx, clusterRoleKind, ListQuery{})
	if err != nil {
		return snap, fmt.Errorf("读取 ClusterRole 失败: %w", err)
	}
	bindings, err := c.ListRawObjects(ctx, roleBindingKind, ListQuery{})
	if err != nil {
		return snap, fmt.Errorf("读取 RoleBinding 失败: %w", err)
	}
	clusterBindings, err := c.ListRawObjects(ctx, clusterRoleBindingKind, ListQuery{})
	if err != nil {
		return snap, fmt.Errorf("读取 ClusterRoleBinding 失败: %w", err)
	}

	snap.RoleCount = len(roles)
	snap.ClusterRoleCount = len(clusterRoles)
	snap.BindingCount = len(bindings)
	snap.ClusterBindingCount = len(clusterBindings)

	// role 索引：namespace/name → rules（ClusterRole 的 namespace 为空）
	roleRules := map[string][]PolicyRule{}
	for _, obj := range roles {
		ns, name := objNamespaceName(obj)
		roleRules["Role|"+ns+"/"+name] = parsePolicyRules(obj["rules"])
	}
	for _, obj := range clusterRoles {
		_, name := objNamespaceName(obj)
		roleRules["ClusterRole|/"+name] = parsePolicyRules(obj["rules"])
	}

	// 把 binding 按「主体」归到 SA 上
	bySubject := map[string][]RoleRef{}
	addBinding := func(obj map[string]any, bindingKind string) {
		bindingNS, bindingName := objNamespaceName(obj)
		roleKindName, roleName := parseRoleRef(obj["roleRef"])
		if roleName == "" {
			return
		}
		scope := "整个集群"
		if bindingKind == "RoleBinding" {
			scope = "命名空间 " + bindingNS
		}
		// RoleBinding 引用 Role 时，Role 在 binding 所在的命名空间里
		lookupNS := ""
		if roleKindName == "Role" {
			lookupNS = bindingNS
		}
		rules, found := roleRules[roleKindName+"|"+lookupNS+"/"+roleName]
		if !found {
			snap.OrphanBindings++
		}

		ref := RoleRef{
			BindingKind: bindingKind, BindingName: bindingName,
			BindingNamespace: bindingNS,
			RoleKind:         roleKindName, RoleName: roleName,
			Scope: scope, Rules: rules, Missing: !found,
		}
		for _, subject := range parseSubjects(obj["subjects"]) {
			if subject.Kind != "ServiceAccount" {
				continue // 只反查 SA；User / Group 在这个平台里没有对应对象
			}
			ns := subject.Namespace
			if ns == "" {
				ns = bindingNS
			}
			key := ns + "/" + subject.Name
			bySubject[key] = append(bySubject[key], ref)
		}
	}
	for _, obj := range bindings {
		addBinding(obj, "RoleBinding")
	}
	for _, obj := range clusterBindings {
		addBinding(obj, "ClusterRoleBinding")
	}

	for _, obj := range sas {
		ns, name := objNamespaceName(obj)
		view := ServiceAccountView{Name: name, Namespace: ns}
		if meta, ok := obj["metadata"].(map[string]any); ok {
			if stamp, ok := meta["creationTimestamp"].(string); ok {
				if parsed, err := time.Parse(time.RFC3339, stamp); err == nil {
					view.CreatedAt = parsed
				}
			}
		}
		if secrets, ok := obj["secrets"].([]any); ok {
			for _, raw := range secrets {
				if item, ok := raw.(map[string]any); ok {
					if n, _ := item["name"].(string); n != "" {
						view.Secrets = append(view.Secrets, n)
					}
				}
			}
		}
		view.Bindings = bySubject[ns+"/"+name]
		annotateServiceAccount(&view)
		snap.ServiceAccounts = append(snap.ServiceAccounts, view)
	}

	sort.Slice(snap.ServiceAccounts, func(i, j int) bool {
		a, b := snap.ServiceAccounts[i], snap.ServiceAccounts[j]
		// 危险的排前面：cluster-admin → 通配符 → 有授权 → 其它
		if a.ClusterAdmin != b.ClusterAdmin {
			return a.ClusterAdmin
		}
		if a.Wildcard != b.Wildcard {
			return a.Wildcard
		}
		if len(a.Bindings) != len(b.Bindings) {
			return len(a.Bindings) > len(b.Bindings)
		}
		if a.Namespace != b.Namespace {
			return a.Namespace < b.Namespace
		}
		return a.Name < b.Name
	})
	return snap, nil
}

// annotateServiceAccount 算出「该不该被一眼看到」的那几个标记
func annotateServiceAccount(view *ServiceAccountView) {
	verbs := map[string]struct{}{}
	for _, ref := range view.Bindings {
		if ref.RoleKind == "ClusterRole" && ref.RoleName == "cluster-admin" {
			view.ClusterAdmin = true
		}
		for _, rule := range ref.Rules {
			if containsString(rule.Verbs, "*") ||
				containsString(rule.Resources, "*") ||
				containsString(rule.APIGroups, "*") {
				view.Wildcard = true
			}
			for _, verb := range rule.Verbs {
				verbs[verb] = struct{}{}
			}
		}
	}
	for verb := range verbs {
		view.VerbSummary = append(view.VerbSummary, verb)
	}
	sort.Strings(view.VerbSummary)
}

func objNamespaceName(obj map[string]any) (string, string) {
	meta, _ := obj["metadata"].(map[string]any)
	if meta == nil {
		return "", ""
	}
	ns, _ := meta["namespace"].(string)
	name, _ := meta["name"].(string)
	return ns, name
}

func parseRoleRef(raw any) (string, string) {
	ref, _ := raw.(map[string]any)
	if ref == nil {
		return "", ""
	}
	kind, _ := ref["kind"].(string)
	name, _ := ref["name"].(string)
	return kind, name
}

func parseSubjects(raw any) []rbacSubject {
	list, _ := raw.([]any)
	out := make([]rbacSubject, 0, len(list))
	for _, item := range list {
		obj, _ := item.(map[string]any)
		if obj == nil {
			continue
		}
		var s rbacSubject
		s.Kind, _ = obj["kind"].(string)
		s.Name, _ = obj["name"].(string)
		s.Namespace, _ = obj["namespace"].(string)
		if s.Name != "" {
			out = append(out, s)
		}
	}
	return out
}

func parsePolicyRules(raw any) []PolicyRule {
	list, _ := raw.([]any)
	out := make([]PolicyRule, 0, len(list))
	for _, item := range list {
		obj, _ := item.(map[string]any)
		if obj == nil {
			continue
		}
		out = append(out, PolicyRule{
			APIGroups:     stringSlice(obj["apiGroups"]),
			Resources:     stringSlice(obj["resources"]),
			Verbs:         stringSlice(obj["verbs"]),
			ResourceNames: stringSlice(obj["resourceNames"]),
			NonResource:   stringSlice(obj["nonResourceURLs"]),
		})
	}
	return out
}

// ---------- Gateway API 路由解析 ----------

// GatewayRoute 一条 HTTPRoute 拍平后的样子。
//
// 为什么单独解析而不是让人看 YAML：Gateway API 的 spec 嵌了三层
// （parentRefs / hostnames / rules[].matches[] + rules[].backendRefs[]），
// 「这条路由把什么流量送到哪个 Service」在 YAML 里得来回翻。
type GatewayRoute struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	// Gateways 挂在哪些 Gateway 上（parentRefs）
	Gateways  []string `json:"gateways"`
	Hostnames []string `json:"hostnames"`
	// Rules 每条规则一行：匹配条件 → 后端
	Rules []GatewayRouteRule `json:"rules"`
}

// GatewayRouteRule 一条规则：什么样的请求，送到哪
type GatewayRouteRule struct {
	Matches  []string `json:"matches"`
	Backends []string `json:"backends"`
}

// gatewayRouteKind HTTPRoute 的 GVR。v1 与 v1beta1 都存在于野外，
// 调用方按集群里实际 served 的版本传进来
func gatewayRouteKind(version string) ResourceKind {
	return NewReadOnlyKind("gateway.networking.k8s.io/"+version, "httproutes",
		"HTTPRoute", "Gateway API", true)
}

// ListGatewayRoutes 列出并拍平 HTTPRoute。
// version 取集群里 CRD 实际 served 的版本（如 v1 / v1beta1）。
func (c *Client) ListGatewayRoutes(ctx context.Context, version, namespace string) ([]GatewayRoute, error) {
	items, err := c.ListRawObjects(ctx, gatewayRouteKind(version), ListQuery{Namespace: namespace})
	if err != nil {
		return nil, err
	}
	out := make([]GatewayRoute, 0, len(items))
	for _, obj := range items {
		ns, name := objNamespaceName(obj)
		route := GatewayRoute{Namespace: ns, Name: name}
		spec, _ := obj["spec"].(map[string]any)
		if spec == nil {
			out = append(out, route)
			continue
		}
		route.Hostnames = stringSlice(spec["hostnames"])
		for _, raw := range asSlice(spec["parentRefs"]) {
			ref, _ := raw.(map[string]any)
			if ref == nil {
				continue
			}
			label, _ := ref["name"].(string)
			if refNS, _ := ref["namespace"].(string); refNS != "" && refNS != ns {
				label = refNS + "/" + label
			}
			if section, _ := ref["sectionName"].(string); section != "" {
				label += "（监听器 " + section + "）"
			}
			if label != "" {
				route.Gateways = append(route.Gateways, label)
			}
		}
		for _, raw := range asSlice(spec["rules"]) {
			rule, _ := raw.(map[string]any)
			if rule == nil {
				continue
			}
			route.Rules = append(route.Rules, GatewayRouteRule{
				Matches:  describeRouteMatches(rule["matches"]),
				Backends: describeRouteBackends(rule["backendRefs"], ns),
			})
		}
		out = append(out, route)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Namespace != out[j].Namespace {
			return out[i].Namespace < out[j].Namespace
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// describeRouteMatches 把 matches 说成人话。没有 matches 时 Gateway API 的
// 默认语义是「匹配所有路径」，这里显式写出来而不是留空
func describeRouteMatches(raw any) []string {
	list := asSlice(raw)
	if len(list) == 0 {
		return []string{"所有请求（未设匹配条件）"}
	}
	out := make([]string, 0, len(list))
	for _, item := range list {
		match, _ := item.(map[string]any)
		if match == nil {
			continue
		}
		var parts []string
		if p, ok := match["path"].(map[string]any); ok {
			kind, _ := p["type"].(string)
			value, _ := p["value"].(string)
			if kind == "" {
				kind = "PathPrefix" // Gateway API 的默认值
			}
			parts = append(parts, fmt.Sprintf("路径 %s %s", kind, value))
		}
		if m, _ := match["method"].(string); m != "" {
			parts = append(parts, "方法 "+m)
		}
		for _, hraw := range asSlice(match["headers"]) {
			header, _ := hraw.(map[string]any)
			if header == nil {
				continue
			}
			name, _ := header["name"].(string)
			value, _ := header["value"].(string)
			parts = append(parts, fmt.Sprintf("头 %s=%s", name, value))
		}
		for _, qraw := range asSlice(match["queryParams"]) {
			q, _ := qraw.(map[string]any)
			if q == nil {
				continue
			}
			name, _ := q["name"].(string)
			value, _ := q["value"].(string)
			parts = append(parts, fmt.Sprintf("参数 %s=%s", name, value))
		}
		if len(parts) == 0 {
			parts = append(parts, "（空条件）")
		}
		out = append(out, strings.Join(parts, " 且 "))
	}
	return out
}

// describeRouteBackends 把 backendRefs 说成「Service:端口 权重」
func describeRouteBackends(raw any, routeNS string) []string {
	list := asSlice(raw)
	if len(list) == 0 {
		return []string{"没有后端（这条规则不会把流量送到任何地方）"}
	}
	out := make([]string, 0, len(list))
	for _, item := range list {
		ref, _ := item.(map[string]any)
		if ref == nil {
			continue
		}
		name, _ := ref["name"].(string)
		if name == "" {
			continue
		}
		label := name
		if ns, _ := ref["namespace"].(string); ns != "" && ns != routeNS {
			// 跨命名空间要有 ReferenceGrant 才生效，这里标出来提醒去核对
			label = ns + "/" + name + "（跨命名空间，需 ReferenceGrant）"
		}
		if port := numOf(ref["port"]); port > 0 {
			label += fmt.Sprintf(":%d", port)
		}
		if weight := numOf(ref["weight"]); weight > 0 {
			label += fmt.Sprintf(" 权重 %d", weight)
		}
		if kind, _ := ref["kind"].(string); kind != "" && kind != "Service" {
			label += "（" + kind + "）"
		}
		out = append(out, label)
	}
	if len(out) == 0 {
		return []string{"没有可识别的后端"}
	}
	return out
}

// ---------- 小工具 ----------

// ToFullYAML 把对象原样序列化成 YAML，**保留 status**。
//
// 与 ToEditableYAML 的分工：后者是给「看了要改再提交回去」用的，所以清掉
// status 与 managedFields 那些服务端字段；自定义资源不提供编辑，而它们的 status
// 往往才是要看的那部分（Argo 的同步状态、cert-manager 的签发结果），所以原样给出。
func ToFullYAML(obj map[string]any) ([]byte, error) {
	return yaml.Marshal(obj)
}

func stringSlice(raw any) []string {
	list, _ := raw.([]any)
	if len(list) == 0 {
		return nil
	}
	out := make([]string, 0, len(list))
	for _, item := range list {
		if text, ok := item.(string); ok && text != "" {
			out = append(out, text)
		}
	}
	return out
}

func asSlice(raw any) []any {
	list, _ := raw.([]any)
	return list
}

func numOf(raw any) int {
	switch v := raw.(type) {
	case float64:
		return int(v)
	case int:
		return v
	case json.Number:
		if n, err := v.Int64(); err == nil {
			return int(n)
		}
	}
	return 0
}

func containsString(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}
