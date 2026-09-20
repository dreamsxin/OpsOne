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

// kubeAlertSourceName 集群健康检查产生的告警挂在这个内部接入源下
const kubeAlertSourceName = "容器集群"

// kubeCallTimeout 单次 API 调用超时。集群不可达时要快速失败，别把请求挂死
const kubeCallTimeout = 10 * time.Second

// kubeClient 按集群记录组装一个客户端。
//
// 每次现场解析 kubeconfig 而不缓存 client：kubeconfig 被改过之后立刻生效，
// 也免得为了几个只读列表维护一套连接池与失效逻辑。
func (h *Handler) kubeClient(cluster model.KubeCluster) (*k8s.Client, error) {
	cfg, err := k8s.ParseKubeconfig(cluster.Kubeconfig, cluster.ContextName)
	if err != nil {
		return nil, err
	}
	return k8s.NewClient(cfg, kubeCallTimeout)
}

// requireKubeCluster 取集群记录，顺带建好客户端
func (h *Handler) requireKubeCluster(c *gin.Context) (*model.KubeCluster, *k8s.Client, bool) {
	var cluster model.KubeCluster
	if err := h.DB.First(&cluster, idParam(c)).Error; err != nil {
		response.NotFound(c, "集群不存在")
		return nil, nil, false
	}
	client, err := h.kubeClient(cluster)
	if err != nil {
		response.BadRequest(c, "集群凭据不可用: "+err.Error())
		return nil, nil, false
	}
	return &cluster, client, true
}

// ---------- 集群接入 ----------

// KubeconfigContexts 解析上传的 kubeconfig，列出其中的上下文供选择。
// 不落库，纯粹是建集群时的辅助。
func (h *Handler) KubeconfigContexts(c *gin.Context) {
	var req struct {
		Kubeconfig string `json:"kubeconfig"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "参数错误")
		return
	}
	names, current, err := k8s.Contexts(req.Kubeconfig)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	if len(names) == 0 {
		response.BadRequest(c, "kubeconfig 里没有任何上下文")
		return
	}
	response.OK(c, gin.H{"contexts": names, "currentContext": current})
}

func (h *Handler) ListKubeClusters(c *gin.Context) {
	var list []model.KubeCluster
	if err := h.DB.Order("id asc").Find(&list).Error; err != nil {
		response.Error(c, "查询集群失败")
		return
	}
	response.OK(c, list)
}

type kubeClusterReq struct {
	Name        string `json:"name"`
	Kubeconfig  string `json:"kubeconfig"`
	ContextName string `json:"contextName"`
	Enabled     *bool  `json:"enabled"`
	Remark      string `json:"remark"`
}

func (h *Handler) CreateKubeCluster(c *gin.Context) {
	var req kubeClusterReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "参数错误")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		response.BadRequest(c, "集群名称不能为空")
		return
	}
	if strings.TrimSpace(req.Kubeconfig) == "" {
		response.BadRequest(c, "请粘贴 kubeconfig 内容")
		return
	}
	// 解析通不过就不用落库了，省得存一堆连不上的集群
	cfg, err := k8s.ParseKubeconfig(req.Kubeconfig, req.ContextName)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	cluster := model.KubeCluster{
		Name: req.Name, Kubeconfig: req.Kubeconfig,
		ContextName: cfg.ContextName, Server: cfg.Server,
		Status: "unknown", Remark: req.Remark,
		CreatedBy: middleware.CurrentUser(c).ID,
	}
	wantEnabled := true
	if req.Enabled != nil {
		wantEnabled = *req.Enabled
	}
	cluster.Enabled = wantEnabled
	if err := h.DB.Create(&cluster).Error; err != nil {
		response.Error(c, "创建失败")
		return
	}
	// enabled 带 gorm default，Create 后会被回填成 true，显式关掉的要写回去
	if !wantEnabled {
		h.DB.Model(&model.KubeCluster{}).Where("id = ?", cluster.ID).
			Updates(map[string]any{"enabled": false})
		cluster.Enabled = false
	}

	// 建完立刻探一次，让人马上知道凭据到底能不能用
	result := h.checkKubeCluster(&cluster)
	response.OK(c, gin.H{"cluster": cluster, "check": result})
}

func (h *Handler) UpdateKubeCluster(c *gin.Context) {
	var cluster model.KubeCluster
	if err := h.DB.First(&cluster, idParam(c)).Error; err != nil {
		response.NotFound(c, "集群不存在")
		return
	}
	var req kubeClusterReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "参数错误")
		return
	}

	updates := map[string]any{"remark": req.Remark}
	if name := strings.TrimSpace(req.Name); name != "" {
		updates["name"] = name
	}
	// kubeconfig 留空表示不改，避免编辑备注时把凭据清掉
	kubeconfig := cluster.Kubeconfig
	if strings.TrimSpace(req.Kubeconfig) != "" {
		kubeconfig = req.Kubeconfig
	}
	cfg, err := k8s.ParseKubeconfig(kubeconfig, req.ContextName)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	updates["kubeconfig"] = kubeconfig
	updates["context_name"] = cfg.ContextName
	updates["server"] = cfg.Server
	if req.Enabled != nil {
		updates["enabled"] = *req.Enabled
	}
	if err := h.DB.Model(&model.KubeCluster{}).Where("id = ?", cluster.ID).Updates(updates).Error; err != nil {
		response.Error(c, "更新失败")
		return
	}
	h.DB.First(&cluster, cluster.ID)
	response.OK(c, cluster)
}

func (h *Handler) DeleteKubeCluster(c *gin.Context) {
	var cluster model.KubeCluster
	if err := h.DB.First(&cluster, idParam(c)).Error; err != nil {
		response.NotFound(c, "集群不存在")
		return
	}
	// 先关掉它可能还在触发的告警，别留下无主告警
	h.resolveKubeAlert(cluster)
	if err := h.DB.Delete(&model.KubeCluster{}, cluster.ID).Error; err != nil {
		response.Error(c, "删除失败")
		return
	}
	response.OK(c, gin.H{"detail": "集群已移除，平台不会对集群本身做任何改动"})
}

// CheckKubeCluster 手动触发一次连通性检查
func (h *Handler) CheckKubeCluster(c *gin.Context) {
	var cluster model.KubeCluster
	if err := h.DB.First(&cluster, idParam(c)).Error; err != nil {
		response.NotFound(c, "集群不存在")
		return
	}
	response.OK(c, h.checkKubeCluster(&cluster))
}

// checkKubeCluster 探一次集群：取版本 + 数节点，回写状态并联动告警。
//
// 状态判定：连不上/没权限 → error；连上但有节点 NotReady → degraded；全就绪 → healthy。
func (h *Handler) checkKubeCluster(cluster *model.KubeCluster) gin.H {
	now := time.Now()
	fail := func(detail string) gin.H {
		h.DB.Model(&model.KubeCluster{}).Where("id = ?", cluster.ID).Updates(map[string]any{
			"status": "error", "last_error": truncate(detail, 480), "last_check_at": &now,
		})
		h.fireKubeAlert(*cluster, "error", detail)
		return gin.H{"status": "error", "detail": detail}
	}

	client, err := h.kubeClient(*cluster)
	if err != nil {
		return fail("凭据不可用: " + err.Error())
	}
	ctx, cancel := context.WithTimeout(context.Background(), kubeCallTimeout)
	defer cancel()

	version, err := client.Version(ctx)
	if err != nil {
		return fail("连接失败: " + err.Error())
	}
	nodes, err := client.Nodes(ctx)
	if err != nil {
		// 版本能取到但列不了节点，通常是 RBAC 给得不够
		return fail("已连上 " + version.GitVersion + "，但读取节点失败: " + err.Error())
	}

	ready := 0
	notReady := make([]string, 0, 2)
	for _, node := range nodes {
		if node.Ready {
			ready++
		} else {
			notReady = append(notReady, node.Name)
		}
	}

	status, detail := "healthy", fmt.Sprintf("%s，%d 个节点全部就绪", version.GitVersion, len(nodes))
	if len(nodes) == 0 {
		status = "degraded"
		detail = version.GitVersion + "，集群里没有任何节点"
	} else if len(notReady) > 0 {
		status = "degraded"
		detail = fmt.Sprintf("%s，%d/%d 节点就绪，未就绪：%s",
			version.GitVersion, ready, len(nodes), strings.Join(notReady, "、"))
	}

	h.DB.Model(&model.KubeCluster{}).Where("id = ?", cluster.ID).Updates(map[string]any{
		"status": status, "version": version.GitVersion,
		"node_total": len(nodes), "node_ready": ready,
		"last_error": "", "last_check_at": &now,
	})
	cluster.Status, cluster.Version = status, version.GitVersion
	cluster.NodeTotal, cluster.NodeReady = len(nodes), ready

	if status == "healthy" {
		h.resolveKubeAlert(*cluster)
	} else {
		h.fireKubeAlert(*cluster, status, detail)
	}
	return gin.H{
		"status": status, "detail": detail, "version": version.GitVersion,
		"platform": version.Platform, "nodeTotal": len(nodes), "nodeReady": ready,
	}
}

func (h *Handler) fireKubeAlert(cluster model.KubeCluster, status, detail string) {
	source, err := h.internalAlertSource(kubeAlertSourceName)
	if err != nil {
		log.Printf("[kube] 内部告警源不可用，跳过告警: %v", err)
		return
	}
	severity := "critical"
	if status == "degraded" {
		severity = "warning"
	}
	h.ingestAlert(source, alertPayload{
		Title:       "集群异常：" + cluster.Name,
		Summary:     detail,
		Severity:    severity,
		Fingerprint: kubeAlertFingerprint(cluster.ID),
		Labels: map[string]string{
			"module":    "kube_cluster",
			"clusterId": strconv.FormatUint(uint64(cluster.ID), 10),
			"cluster":   cluster.Name,
		},
	})
}

func (h *Handler) resolveKubeAlert(cluster model.KubeCluster) {
	source, err := h.internalAlertSource(kubeAlertSourceName)
	if err != nil {
		return
	}
	h.ingestAlert(source, alertPayload{
		Title: "集群恢复：" + cluster.Name, Status: "resolved",
		Fingerprint: kubeAlertFingerprint(cluster.ID),
		Labels: map[string]string{
			"module":    "kube_cluster",
			"clusterId": strconv.FormatUint(uint64(cluster.ID), 10),
		},
	})
}

func kubeAlertFingerprint(clusterID uint) string {
	return internalAlertFingerprint(fmt.Sprintf("kube_cluster|%d", clusterID))
}

// CheckKubeClustersForSchedule 定时检查全部启用中的集群
func (h *Handler) CheckKubeClustersForSchedule() {
	var clusters []model.KubeCluster
	if err := h.DB.Where("enabled = ?", true).Find(&clusters).Error; err != nil {
		log.Printf("[kube] 读取集群失败: %v", err)
		return
	}
	if len(clusters) == 0 {
		return
	}
	healthy := 0
	for i := range clusters {
		if h.checkKubeCluster(&clusters[i])["status"] == "healthy" {
			healthy++
		}
	}
	h.markFixedRun("kube", fmt.Sprintf("共 %d 个集群，健康 %d 个", len(clusters), healthy))
	log.Printf("[kube] 定时检查完成: 共 %d 个集群，健康 %d 个", len(clusters), healthy)
}

// ---------- 集群内资源（只读） ----------

func (h *Handler) KubeNodes(c *gin.Context) {
	_, client, ok := h.requireKubeCluster(c)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), kubeCallTimeout)
	defer cancel()

	nodes, err := client.Nodes(ctx)
	if err != nil {
		response.Error(c, "读取节点失败: "+err.Error())
		return
	}
	response.OK(c, nodes)
}

func (h *Handler) KubeNamespaces(c *gin.Context) {
	_, client, ok := h.requireKubeCluster(c)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), kubeCallTimeout)
	defer cancel()

	list, err := client.Namespaces(ctx)
	if err != nil {
		response.Error(c, "读取命名空间失败: "+err.Error())
		return
	}
	response.OK(c, list)
}

// KubeWorkloads 一次取回 Deployment / StatefulSet / DaemonSet。
// 三类都要挨个看才知道「哪个副本没起来」，分三次请求没意义。
func (h *Handler) KubeWorkloads(c *gin.Context) {
	_, client, ok := h.requireKubeCluster(c)
	if !ok {
		return
	}
	namespace := strings.TrimSpace(c.Query("namespace"))
	ctx, cancel := context.WithTimeout(context.Background(), kubeCallTimeout)
	defer cancel()

	items := make([]k8s.Workload, 0, 16)
	// 某一类读失败不该让整页空白，记下原因继续读其他的
	warnings := make([]string, 0, 3)
	for _, entry := range []struct {
		kind string
		call func(context.Context, string) ([]k8s.Workload, error)
	}{
		{"Deployment", client.Deployments},
		{"StatefulSet", client.StatefulSets},
		{"DaemonSet", client.DaemonSets},
	} {
		part, err := entry.call(ctx, namespace)
		if err != nil {
			warnings = append(warnings, entry.kind+": "+err.Error())
			continue
		}
		items = append(items, part...)
	}
	if len(items) == 0 && len(warnings) == 3 {
		response.Error(c, "读取工作负载失败: "+strings.Join(warnings, "; "))
		return
	}

	unhealthy := 0
	for _, item := range items {
		if !item.Healthy {
			unhealthy++
		}
	}
	response.OK(c, gin.H{
		"items": items, "total": len(items), "unhealthy": unhealthy, "warnings": warnings,
	})
}

func (h *Handler) KubePods(c *gin.Context) {
	_, client, ok := h.requireKubeCluster(c)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), kubeCallTimeout)
	defer cancel()

	pods, err := client.Pods(ctx, strings.TrimSpace(c.Query("namespace")))
	if err != nil {
		response.Error(c, "读取 Pod 失败: "+err.Error())
		return
	}
	abnormal := 0
	for _, pod := range pods {
		if pod.Phase != "Running" && pod.Phase != "Succeeded" {
			abnormal++
		}
	}
	response.OK(c, gin.H{"items": pods, "total": len(pods), "abnormal": abnormal})
}

// kubeLogMaxBytes 单次日志读取上限。再多界面也看不动，还会把内存拉高
const kubeLogMaxBytes = 1 << 20

// kubeLogDefaultTail 默认只取最后多少行
const kubeLogDefaultTail = 500

// KubePodLogs 取容器日志。
//
// 不做 follow：一次取一段，界面按需刷新。少一条长连接，服务端也不用维护订阅。
func (h *Handler) KubePodLogs(c *gin.Context) {
	_, client, ok := h.requireKubeCluster(c)
	if !ok {
		return
	}
	namespace := strings.TrimSpace(c.Query("namespace"))
	pod := strings.TrimSpace(c.Query("pod"))
	if namespace == "" || pod == "" {
		response.BadRequest(c, "命名空间与 Pod 名称都不能为空")
		return
	}

	opts := k8s.LogOptions{
		Container:  strings.TrimSpace(c.Query("container")),
		TailLines:  kubeLogDefaultTail,
		Previous:   c.Query("previous") == "true",
		Timestamps: c.Query("timestamps") == "true",
		LimitBytes: kubeLogMaxBytes,
	}
	if raw := c.Query("tailLines"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 5000 {
			response.BadRequest(c, "行数需在 1 到 5000 之间")
			return
		}
		opts.TailLines = n
	}
	if raw := c.Query("sinceSeconds"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 86400 {
			response.BadRequest(c, "时间范围需在 1 秒到 24 小时之间")
			return
		}
		opts.SinceSeconds = n
	}

	ctx, cancel := context.WithTimeout(context.Background(), kubeCallTimeout)
	defer cancel()

	text, truncated, err := client.PodLogs(ctx, namespace, pod, opts)
	if err != nil {
		respondKubeError(c, "读取日志失败: "+err.Error(), err)
		return
	}
	response.OK(c, gin.H{
		"namespace": namespace, "pod": pod, "container": opts.Container,
		"previous": opts.Previous, "logs": text,
		"lines": countLogLines(text), "truncated": truncated,
		"tailLines": opts.TailLines,
	})
}

// countLogLines 数实际拿到多少行。末尾换行不算新的一行。
func countLogLines(text string) int {
	if text == "" {
		return 0
	}
	return strings.Count(strings.TrimSuffix(text, "\n"), "\n") + 1
}

// ---------- 事件 ----------

func (h *Handler) KubeEvents(c *gin.Context) {
	_, client, ok := h.requireKubeCluster(c)
	if !ok {
		return
	}
	limit := 200
	if raw := c.Query("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 && n <= 500 {
			limit = n
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), kubeCallTimeout)
	defer cancel()

	events, err := client.Events(ctx, strings.TrimSpace(c.Query("namespace")),
		c.Query("onlyWarning") == "true", limit)
	if err != nil {
		response.Error(c, "读取事件失败: "+err.Error())
		return
	}
	response.OK(c, gin.H{"items": events, "total": len(events)})
}
