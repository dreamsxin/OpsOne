package handler

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"ops-platform/server/internal/k8s"
	"ops-platform/server/internal/middleware"
	"ops-platform/server/internal/model"
	"ops-platform/server/internal/response"
)

// kubeApplyTimeout 写操作的超时。比只读列表给得宽一点：准入控制、webhook
// 都在这条链路上，10 秒偶尔真不够。
const kubeApplyTimeout = 30 * time.Second

// kubePayloadMaxBytes 提交的 YAML 上限。再大基本不是「改一处配置」的场景了
const kubePayloadMaxBytes = 256 << 10

// kubeChangeLogPageMax 留痕列表单页上限
const kubeChangeLogPageMax = 100

// KubeResourceKinds 平台允许操作的资源类型白名单
func (h *Handler) KubeResourceKinds(c *gin.Context) {
	response.OK(c, k8s.SupportedKinds())
}

// requireKind 取并校验 kind 参数
func requireKind(c *gin.Context, raw string) (k8s.ResourceKind, bool) {
	kind, ok := k8s.LookupKind(strings.TrimSpace(raw))
	if !ok {
		response.BadRequest(c, "不支持的资源类型，平台只开放了 "+supportedKindNames())
		return k8s.ResourceKind{}, false
	}
	return kind, true
}

func supportedKindNames() string {
	kinds := k8s.SupportedKinds()
	names := make([]string, 0, len(kinds))
	for _, item := range kinds {
		names = append(names, item.Kind)
	}
	return strings.Join(names, " / ")
}

// KubeResources 列某类资源。namespace 留空表示全集群。
//
// 过滤放在平台侧而不是让 API Server 做：name 的子串匹配 fieldSelector 表达不了
// （它只支持等值），把整份列表拉回来再筛，语义才和界面上看到的一致。
func (h *Handler) KubeResources(c *gin.Context) {
	_, client, ok := h.requireKubeCluster(c)
	if !ok {
		return
	}
	kind, ok := requireKind(c, c.Query("kind"))
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), kubeCallTimeout)
	defer cancel()

	namespace := strings.TrimSpace(c.Query("namespace"))
	items, err := client.ListObjectsQuery(ctx, kind, k8s.ListQuery{
		Namespace:     namespace,
		LabelSelector: strings.TrimSpace(c.Query("labelSelector")),
	})
	if err != nil {
		response.Error(c, "读取"+kind.Kind+"失败: "+err.Error())
		return
	}
	total := len(items)
	if keyword := strings.ToLower(strings.TrimSpace(c.Query("keyword"))); keyword != "" {
		filtered := make([]k8s.ResourceItem, 0, len(items))
		for _, item := range items {
			if strings.Contains(strings.ToLower(item.Name), keyword) {
				filtered = append(filtered, item)
			}
		}
		items = filtered
	}
	response.OK(c, gin.H{
		"items": items, "total": len(items), "matched": total,
		"kind": kind.Kind, "scalable": kind.Scalable, "restartable": kind.Restartable,
		"readOnly": kind.ReadOnly, "namespaced": kind.Namespaced,
		// 集群级对象忽略命名空间筛选，这一点要让界面能说清楚
		"namespaceIgnored": !kind.Namespaced && namespace != "",
	})
}

// KubeResourceDetail 取单个对象，返回清理过的可编辑 YAML
func (h *Handler) KubeResourceDetail(c *gin.Context) {
	_, client, ok := h.requireKubeCluster(c)
	if !ok {
		return
	}
	kind, ok := requireKind(c, c.Query("kind"))
	if !ok {
		return
	}
	name := strings.TrimSpace(c.Query("name"))
	namespace := strings.TrimSpace(c.Query("namespace"))
	if name == "" {
		response.BadRequest(c, "名称不能为空")
		return
	}
	// 集群级对象（Node / Namespace / PV / StorageClass）本来就没有命名空间
	if kind.Namespaced && namespace == "" {
		response.BadRequest(c, kind.Kind+" 属于某个命名空间，请一并指定命名空间")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), kubeCallTimeout)
	defer cancel()

	obj, err := client.GetObject(ctx, kind, namespace, name)
	if err != nil {
		respondKubeError(c, "读取对象失败: "+err.Error(), err)
		return
	}

	hint := "已去掉 status 与集群自己维护的字段（resourceVersion / uid / managedFields 等），可以直接改完提交"
	editable := obj
	if kind.Redacted {
		editable = k8s.RedactSecret(obj)
		hint = "Secret 的值不在平台上展示，只列出有哪些键；因此这份 YAML 不能提交回集群（提交会把真实值覆盖成占位符），平台也会拒绝"
	} else if kind.ReadOnly {
		hint = kind.Kind + " 在平台上是只读的：这份 YAML 只供查看，提交会被拒绝（它由控制器维护，改了也会被改回去）"
	}
	yamlText, err := k8s.ToEditableYAML(editable)
	if err != nil {
		response.Error(c, "转换 YAML 失败: "+err.Error())
		return
	}
	response.OK(c, gin.H{
		"kind": kind.Kind, "namespace": namespace, "name": name,
		"scalable": kind.Scalable, "restartable": kind.Restartable,
		"readOnly": kind.ReadOnly, "redacted": kind.Redacted,
		"yaml": string(yamlText), "hint": hint,
		// podSelector 非空表示这个对象能下钻到 Pod
		"podSelector": k8s.PodSelector(obj),
	})
}

// KubeObjectEvents 按对象反查事件：「这个 Pod 为什么起不来」的第一站
func (h *Handler) KubeObjectEvents(c *gin.Context) {
	_, client, ok := h.requireKubeCluster(c)
	if !ok {
		return
	}
	kind, ok := requireKind(c, c.Query("kind"))
	if !ok {
		return
	}
	name := strings.TrimSpace(c.Query("name"))
	if name == "" {
		response.BadRequest(c, "名称不能为空")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), kubeCallTimeout)
	defer cancel()

	events, err := client.ObjectEvents(ctx, strings.TrimSpace(c.Query("namespace")), kind.Kind, name, 100)
	if err != nil {
		response.Error(c, "读取事件失败: "+err.Error())
		return
	}
	response.OK(c, gin.H{
		"items": events, "total": len(events),
		// 事件有保留期（默认 1 小时），空列表不等于「一切正常」
		"note": "事件在集群里只保留一段时间（默认 1 小时），空列表说明的是「最近没有事件」，不是「从来没出过问题」",
	})
}

// KubeRelatedPods 从工作负载 / Service 下钻到它的 Pod。
//
// 走 spec.selector 的等值标签，和集群自己的选法一致；选择器解析不出来时
// 如实返回空 + 原因，不拿「按名字前缀猜」糊弄（猜出来的关联关系比没有更危险）。
func (h *Handler) KubeRelatedPods(c *gin.Context) {
	_, client, ok := h.requireKubeCluster(c)
	if !ok {
		return
	}
	kind, ok := requireKind(c, c.Query("kind"))
	if !ok {
		return
	}
	name := strings.TrimSpace(c.Query("name"))
	namespace := strings.TrimSpace(c.Query("namespace"))
	if name == "" || namespace == "" {
		response.BadRequest(c, "命名空间与名称都不能为空")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), kubeCallTimeout)
	defer cancel()

	obj, err := client.GetObject(ctx, kind, namespace, name)
	if err != nil {
		respondKubeError(c, "读取对象失败: "+err.Error(), err)
		return
	}
	selector := k8s.PodSelector(obj)
	if selector == "" {
		response.OK(c, gin.H{
			"items": []any{}, "total": 0, "selector": "",
			"note": kind.Kind + " 没有可用的等值标签选择器（可能用的是 matchExpressions），平台无法可靠地列出它的 Pod",
		})
		return
	}

	podKind, _ := k8s.LookupKind("Pod")
	pods, err := client.ListObjectsQuery(ctx, podKind, k8s.ListQuery{Namespace: namespace, LabelSelector: selector})
	if err != nil {
		response.Error(c, "按标签读取 Pod 失败: "+err.Error())
		return
	}
	response.OK(c, gin.H{
		"items": pods, "total": len(pods), "selector": selector,
		"note": "按标签选择器 " + selector + " 列出，与集群自己的选法一致",
	})
}

// RestartKubeWorkload 滚动重启。真改集群，过留痕，与 apply / scale 同一条链路。
func (h *Handler) RestartKubeWorkload(c *gin.Context) {
	cluster, client, ok := h.requireKubeCluster(c)
	if !ok {
		return
	}
	var req struct {
		Kind      string `json:"kind"`
		Namespace string `json:"namespace"`
		Name      string `json:"name"`
		DryRun    bool   `json:"dryRun"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "参数错误")
		return
	}
	kind, ok := requireKind(c, req.Kind)
	if !ok {
		return
	}
	if !kind.Restartable {
		response.BadRequest(c, kind.Kind+" 不支持滚动重启，只有 Deployment / StatefulSet / DaemonSet 有 Pod 模板")
		return
	}
	req.Namespace, req.Name = strings.TrimSpace(req.Namespace), strings.TrimSpace(req.Name)
	if req.Namespace == "" || req.Name == "" {
		response.BadRequest(c, "命名空间与名称都不能为空")
		return
	}

	started := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), kubeApplyTimeout)
	defer cancel()

	result, restartErr := client.RolloutRestart(ctx, kind, req.Namespace, req.Name, started, req.DryRun)
	entry := model.KubeChangeLog{
		ClusterID: cluster.ID, ClusterName: cluster.Name,
		Kind: kind.Kind, Namespace: req.Namespace, Name: req.Name,
		Action: "restart", DryRun: req.DryRun,
		Payload: "rollout restart", CostMs: time.Since(started).Milliseconds(),
	}
	if restartErr != nil {
		h.recordKubeChange(c, entry, "", restartErr)
		respondKubeError(c, "滚动重启失败: "+restartErr.Error(), restartErr)
		return
	}

	summary := fmt.Sprintf("已触发滚动重启（restartedAt=%s），Pod 会被逐步替换，不是原地重启容器", result.RestartedAt)
	if req.DryRun {
		summary = "预检通过，集群未改动"
	}
	h.recordKubeChange(c, entry, summary, nil)
	response.OK(c, gin.H{
		"kind": kind.Kind, "namespace": req.Namespace, "name": req.Name,
		"restartedAt": result.RestartedAt, "replicas": result.Replicas,
		"dryRun": req.DryRun, "detail": summary,
	})
}

// ApplyKubeResource 提交一段 YAML。dryRun=true 时只让 API Server 校验不落盘。
func (h *Handler) ApplyKubeResource(c *gin.Context) {
	cluster, client, ok := h.requireKubeCluster(c)
	if !ok {
		return
	}
	var req struct {
		YAML   string `json:"yaml"`
		DryRun bool   `json:"dryRun"`
		Force  bool   `json:"force"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "参数错误")
		return
	}
	if len(req.YAML) > kubePayloadMaxBytes {
		response.BadRequest(c, fmt.Sprintf("YAML 超过 %d KB 上限", kubePayloadMaxBytes>>10))
		return
	}

	manifest, err := k8s.ParseManifest(req.YAML)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	kind, ok := k8s.LookupKind(manifest.Kind)
	if !ok {
		response.BadRequest(c, "平台不允许改动 "+manifest.Kind+"，只开放了 "+supportedKindNames())
		return
	}
	if kind.Redacted {
		// Secret 的 YAML 是脱敏过的，提交回去等于把真实值覆盖成占位符
		response.BadRequest(c, kind.Kind+" 在平台上只读且值已脱敏，提交会把真实内容覆盖成占位符；要改请用 kubectl")
		return
	}
	if kind.ReadOnly {
		response.BadRequest(c, kind.Kind+" 在平台上是只读的（由控制器维护，改了也会被改回去）")
		return
	}
	if manifest.APIVersion != kind.APIVersion {
		response.BadRequest(c, fmt.Sprintf("%s 的 apiVersion 应为 %s，收到 %s",
			kind.Kind, kind.APIVersion, manifest.APIVersion))
		return
	}
	if manifest.Namespace == "" {
		// 不替它猜默认命名空间：apply 到 default 去往往不是本意
		response.BadRequest(c, "YAML 里必须写明 metadata.namespace")
		return
	}

	started := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), kubeApplyTimeout)
	defer cancel()

	obj, applyErr := client.Apply(ctx, kind, manifest.Namespace, manifest.Name,
		[]byte(req.YAML), k8s.ApplyOptions{DryRun: req.DryRun, Force: req.Force})

	entry := model.KubeChangeLog{
		ClusterID: cluster.ID, ClusterName: cluster.Name,
		Kind: kind.Kind, Namespace: manifest.Namespace, Name: manifest.Name,
		Action: "apply", DryRun: req.DryRun, Forced: req.Force,
		Payload: req.YAML, CostMs: time.Since(started).Milliseconds(),
	}
	if applyErr != nil {
		h.recordKubeChange(c, entry, "", applyErr)
		detail := applyErr.Error()
		if strings.Contains(detail, "conflict") || strings.Contains(detail, "Conflict") {
			detail += "（这些字段现在由别人管着，确认要接管就勾上「强制接管字段」重试）"
		}
		respondKubeError(c, "提交失败: "+detail, applyErr)
		return
	}

	summary := "已应用"
	if req.DryRun {
		summary = "预检通过，集群未改动"
	}
	// 从返回对象里读回副本数，让人确认真正生效的值
	replicas := -1
	if spec, hit := obj["spec"].(map[string]any); hit {
		if value, hit := spec["replicas"].(float64); hit {
			replicas = int(value)
			summary += fmt.Sprintf("，当前期望副本 %d", replicas)
		}
	}
	h.recordKubeChange(c, entry, summary, nil)
	response.OK(c, gin.H{
		"kind": kind.Kind, "namespace": manifest.Namespace, "name": manifest.Name,
		"dryRun": req.DryRun, "forced": req.Force, "replicas": replicas, "detail": summary,
	})
}

// ScaleKubeResource 改副本数。只对支持 /scale 的类型开放。
func (h *Handler) ScaleKubeResource(c *gin.Context) {
	cluster, client, ok := h.requireKubeCluster(c)
	if !ok {
		return
	}
	var req struct {
		Kind      string `json:"kind"`
		Namespace string `json:"namespace"`
		Name      string `json:"name"`
		Replicas  *int   `json:"replicas"`
		DryRun    bool   `json:"dryRun"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "参数错误")
		return
	}
	kind, ok := requireKind(c, req.Kind)
	if !ok {
		return
	}
	if !kind.Scalable {
		response.BadRequest(c, kind.Kind+" 不支持直接改副本数")
		return
	}
	req.Namespace, req.Name = strings.TrimSpace(req.Namespace), strings.TrimSpace(req.Name)
	if req.Namespace == "" || req.Name == "" {
		response.BadRequest(c, "命名空间与名称都不能为空")
		return
	}
	if req.Replicas == nil {
		response.BadRequest(c, "请填写目标副本数")
		return
	}
	if *req.Replicas < 0 || *req.Replicas > 200 {
		response.BadRequest(c, "副本数需在 0 到 200 之间")
		return
	}

	started := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), kubeApplyTimeout)
	defer cancel()

	result, scaleErr := client.Scale(ctx, kind, req.Namespace, req.Name, *req.Replicas, req.DryRun)
	entry := model.KubeChangeLog{
		ClusterID: cluster.ID, ClusterName: cluster.Name,
		Kind: kind.Kind, Namespace: req.Namespace, Name: req.Name,
		Action: "scale", DryRun: req.DryRun,
		Payload: "replicas=" + strconv.Itoa(*req.Replicas),
		CostMs:  time.Since(started).Milliseconds(),
	}
	if scaleErr != nil {
		h.recordKubeChange(c, entry, "", scaleErr)
		respondKubeError(c, "改副本数失败: "+scaleErr.Error(), scaleErr)
		return
	}

	summary := fmt.Sprintf("副本数 %d → %d", result.Previous, result.Current)
	if req.DryRun {
		summary = fmt.Sprintf("预检通过，副本数仍是 %d（目标 %d）", result.Previous, *req.Replicas)
	}
	h.recordKubeChange(c, entry, summary, nil)
	response.OK(c, gin.H{
		"kind": kind.Kind, "namespace": req.Namespace, "name": req.Name,
		"previous": result.Previous, "current": result.Current,
		"dryRun": req.DryRun, "detail": summary,
	})
}

// respondKubeError 按 API Server 的说法挑一个合适的状态码。
//
// 对象不存在、字段不合法都是调用方填错了，报 404 / 400 比一律 500 好判断；
// 其余（连不上、没权限、准入拒绝）才算服务端问题。
func respondKubeError(c *gin.Context, message string, opErr error) {
	detail := opErr.Error()
	switch {
	case strings.Contains(detail, "not found"):
		response.NotFound(c, message)
	case strings.Contains(detail, "API 返回 400"), strings.Contains(detail, "API 返回 422"):
		response.BadRequest(c, message)
	default:
		response.Error(c, message)
	}
}

// recordKubeChange 落一条集群写操作留痕。失败也记，且记下 API Server 的原话。
func (h *Handler) recordKubeChange(c *gin.Context, entry model.KubeChangeLog, detail string, opErr error) {
	entry.Status, entry.Detail = "success", truncate(detail, 480)
	if opErr != nil {
		entry.Status, entry.Detail = "failed", truncate(opErr.Error(), 480)
	}
	entry.ClientIP = c.ClientIP()
	if user := middleware.CurrentUser(c); user != nil {
		entry.UserID, entry.Username = user.ID, user.Username
	}
	if err := h.DB.Create(&entry).Error; err != nil {
		// 留痕写失败不该把已经成功的改动回滚成一个错误响应
		log.Printf("[kube] 写入集群改动留痕失败: %v", err)
	}
}

// ListKubeChangeLogs 集群改动留痕检索
func (h *Handler) ListKubeChangeLogs(c *gin.Context) {
	page, size := pageParams(c)
	if size > kubeChangeLogPageMax {
		size = kubeChangeLogPageMax
	}
	query := h.DB.Model(&model.KubeChangeLog{})
	if raw := strings.TrimSpace(c.Query("clusterId")); raw != "" {
		if id, err := strconv.Atoi(raw); err == nil && id > 0 {
			query = query.Where("cluster_id = ?", id)
		}
	}
	if action := strings.TrimSpace(c.Query("action")); action != "" {
		query = query.Where("action = ?", action)
	}
	if status := strings.TrimSpace(c.Query("status")); status != "" {
		query = query.Where("status = ?", status)
	}
	if keyword := strings.TrimSpace(c.Query("keyword")); keyword != "" {
		like := "%" + keyword + "%"
		query = query.Where("name LIKE ? OR namespace LIKE ?", like, like)
	}
	if c.Query("realOnly") == "true" {
		// 只看真改过集群的，预检记录不算
		query = query.Where("dry_run = ?", false)
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		response.Error(c, "查询留痕失败")
		return
	}
	var list []model.KubeChangeLog
	if err := query.Order("id desc").Offset((page - 1) * size).Limit(size).
		Find(&list).Error; err != nil {
		response.Error(c, "查询留痕失败")
		return
	}
	response.OKPage(c, list, total, page, size)
}
