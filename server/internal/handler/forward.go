package handler

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"ops-platform/server/internal/k8s"
	"ops-platform/server/internal/middleware"
	"ops-platform/server/internal/model"
	"ops-platform/server/internal/response"
)

// 集群服务转发：在平台上开一个 TCP 端口，把流量送到集群里的 Pod / Service。
//
// 相当于把 kubectl port-forward 挪到平台上跑，好处是不用给每个人发 kubeconfig；
// 代价是隧道端口本身没有认证（见 docs/SECURITY.md），所以这里把能加的约束都加上：
// 端口区间、条数上限、并发连接上限、到点自动关闭、谁开的记档案。
//
// 隧道是进程内的活物，重启即消失 —— 启动时把残留的 running 行标成 stopped，
// 不在界面上假装还活着。

// forwardDialTimeout 单条连接建立到 API Server 的超时
const forwardDialTimeout = 20 * time.Second

// forwardTunnel 一条运行中的隧道
type forwardTunnel struct {
	id       uint
	listener net.Listener
	cancel   context.CancelFunc
	client   *k8s.Client

	namespace  string
	targetKind string
	targetName string
	targetPort int

	// 并发连接数闸门：每条连接都要向 API Server 单开一条 WebSocket
	sem chan struct{}

	connTotal  atomic.Int64
	connFailed atomic.Int64
	connActive atomic.Int64
	bytesIn    atomic.Int64
	bytesOut   atomic.Int64
	lastActive atomic.Int64 // unix 秒

	expiresAt time.Time
	closeOnce sync.Once
	// lastErr 最近一次拨号失败的原因，列表里直接给出来
	lastErrMu sync.Mutex
	lastErr   string
}

func (t *forwardTunnel) touch() { t.lastActive.Store(time.Now().Unix()) }

func (t *forwardTunnel) setLastErr(msg string) {
	t.lastErrMu.Lock()
	t.lastErr = msg
	t.lastErrMu.Unlock()
}

func (t *forwardTunnel) lastError() string {
	t.lastErrMu.Lock()
	defer t.lastErrMu.Unlock()
	return t.lastErr
}

// forwardRegistry 进程内的隧道表
type forwardRegistry struct {
	mu    sync.Mutex
	items map[uint]*forwardTunnel
}

func newForwardRegistry() *forwardRegistry {
	return &forwardRegistry{items: map[uint]*forwardTunnel{}}
}

func (r *forwardRegistry) add(t *forwardTunnel) {
	r.mu.Lock()
	r.items[t.id] = t
	r.mu.Unlock()
}

func (r *forwardRegistry) get(id uint) *forwardTunnel {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.items[id]
}

func (r *forwardRegistry) remove(id uint) {
	r.mu.Lock()
	delete(r.items, id)
	r.mu.Unlock()
}

func (r *forwardRegistry) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.items)
}

// snapshot 拷一份当前隧道列表：停机时要边遍历边关，不能持着锁调 closeForward
func (r *forwardRegistry) snapshot() []*forwardTunnel {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*forwardTunnel, 0, len(r.items))
	for _, t := range r.items {
		out = append(out, t)
	}
	return out
}

func (r *forwardRegistry) portUsed(port int) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, item := range r.items {
		if _, p, err := net.SplitHostPort(item.listener.Addr().String()); err == nil {
			if n, _ := strconv.Atoi(p); n == port {
				return true
			}
		}
	}
	return false
}

// ResetForwards 进程启动时调用：上一轮进程留下的 running 行已经没有监听了
func (h *Handler) ResetForwards() {
	now := time.Now()
	err := h.DB.Model(&model.KubeForward{}).
		Where("forward_status = ?", "running").
		Updates(map[string]any{
			"forward_status": "stopped",
			"error_msg":      "平台重启，隧道已随进程结束",
			"closed_at":      &now,
		}).Error
	if err != nil {
		log.Printf("[forward] 清理残留隧道失败: %v", err)
	}
}

// ---------- 列表 ----------

// forwardLimits 把环境变量定下的约束告诉界面，免得用户靠报错猜
func (h *Handler) forwardLimits() gin.H {
	return gin.H{
		"bind":       h.Cfg.ForwardBind,
		"portMin":    h.Cfg.ForwardPortMin,
		"portMax":    h.Cfg.ForwardPortMax,
		"maxTunnels": h.Cfg.ForwardMax,
		"ttlMinutes": h.Cfg.ForwardTTLMinutes,
		"connMax":    h.Cfg.ForwardConnMax,
		"running":    h.forwards.count(),
	}
}

func (h *Handler) ListKubeForwards(c *gin.Context) {
	page, size := pageParams(c)
	q := h.DB.Model(&model.KubeForward{})
	if status := strings.TrimSpace(c.Query("status")); status != "" {
		q = q.Where("forward_status = ?", status)
	}
	if clusterID := strings.TrimSpace(c.Query("clusterId")); clusterID != "" {
		id, err := strconv.Atoi(clusterID)
		if err != nil || id <= 0 {
			response.BadRequest(c, "clusterId 不合法")
			return
		}
		q = q.Where("cluster_id = ?", id)
	}
	q = q.Session(&gorm.Session{})

	var total int64
	if err := q.Count(&total).Error; err != nil {
		response.Error(c, "查询隧道失败")
		return
	}
	var rows []model.KubeForward
	if err := q.Order("id desc").Offset((page - 1) * size).Limit(size).Find(&rows).Error; err != nil {
		response.Error(c, "查询隧道失败")
		return
	}

	// 运行中的隧道，计数只在内存里，取实时值覆盖库里的快照
	list := make([]gin.H, 0, len(rows))
	for _, row := range rows {
		item := gin.H{
			"id": row.ID, "clusterId": row.ClusterID, "clusterName": row.ClusterName,
			"namespace": row.Namespace, "targetKind": row.TargetKind, "targetName": row.TargetName,
			"targetPort": row.TargetPort, "listenAddr": row.ListenAddr, "listenPort": row.ListenPort,
			"status": row.ForwardStatus, "errorMsg": row.ErrorMsg,
			"connTotal": row.ConnTotal, "connFailed": row.ConnFailed, "connActive": 0,
			"bytesIn": row.BytesIn, "bytesOut": row.BytesOut,
			"username": row.Username, "clientIp": row.ClientIP,
			"expiresAt": row.ExpiresAt, "lastActiveAt": row.LastActiveAt,
			"closedAt": row.ClosedAt, "createdAt": row.CreatedAt,
		}
		if live := h.forwards.get(row.ID); live != nil {
			item["connTotal"] = live.connTotal.Load()
			item["connFailed"] = live.connFailed.Load()
			item["connActive"] = live.connActive.Load()
			item["bytesIn"] = live.bytesIn.Load()
			item["bytesOut"] = live.bytesOut.Load()
			if unix := live.lastActive.Load(); unix > 0 {
				at := time.Unix(unix, 0)
				item["lastActiveAt"] = &at
			}
			if msg := live.lastError(); msg != "" {
				item["errorMsg"] = msg
			}
		}
		list = append(list, item)
	}
	response.OK(c, gin.H{
		"list": list, "total": total, "page": page, "pageSize": size,
		"limits": h.forwardLimits(),
	})
}

// ---------- 创建 ----------

type kubeForwardReq struct {
	ClusterID  uint   `json:"clusterId"`
	Namespace  string `json:"namespace"`
	TargetKind string `json:"targetKind"` // pod | service
	TargetName string `json:"targetName"`
	TargetPort int    `json:"targetPort"`
	// ListenPort 留空/0 表示在允许区间里自动挑一个空闲端口
	ListenPort int `json:"listenPort"`
	// TTLMinutes 留空取配置上限，不允许超过上限
	TTLMinutes int `json:"ttlMinutes"`
}

// validateForwardReq 参数校验单独拆出来，便于单测
func validateForwardReq(req *kubeForwardReq, portMin, portMax, ttlMax int) error {
	req.Namespace = strings.TrimSpace(req.Namespace)
	req.TargetName = strings.TrimSpace(req.TargetName)
	req.TargetKind = strings.ToLower(strings.TrimSpace(req.TargetKind))
	if req.TargetKind == "" {
		req.TargetKind = "pod"
	}

	if req.ClusterID == 0 {
		return errors.New("请选择集群")
	}
	if req.Namespace == "" {
		return errors.New("命名空间不能为空")
	}
	if req.TargetName == "" {
		return errors.New("转发目标不能为空")
	}
	if req.TargetKind != "pod" && req.TargetKind != "service" {
		return errors.New("转发目标只支持 pod 或 service")
	}
	if req.TargetPort < 1 || req.TargetPort > 65535 {
		return errors.New("目标端口不在 1-65535 之间")
	}
	if req.ListenPort != 0 && (req.ListenPort < portMin || req.ListenPort > portMax) {
		return fmt.Errorf("监听端口只能用 %d-%d，这个区间由部署时的 OPS_FORWARD_PORT_MIN/MAX 决定", portMin, portMax)
	}
	if req.TTLMinutes <= 0 {
		req.TTLMinutes = ttlMax
	}
	if req.TTLMinutes > ttlMax {
		return fmt.Errorf("存活时长最多 %d 分钟", ttlMax)
	}
	return nil
}

func (h *Handler) CreateKubeForward(c *gin.Context) {
	var req kubeForwardReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "参数错误")
		return
	}
	if err := validateForwardReq(&req, h.Cfg.ForwardPortMin, h.Cfg.ForwardPortMax, h.Cfg.ForwardTTLMinutes); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	if h.forwards.count() >= h.Cfg.ForwardMax {
		response.BadRequest(c, fmt.Sprintf("同时最多开 %d 条隧道，先关掉用不上的", h.Cfg.ForwardMax))
		return
	}

	var cluster model.KubeCluster
	if err := h.DB.First(&cluster, req.ClusterID).Error; err != nil {
		response.NotFound(c, "集群不存在")
		return
	}
	if !cluster.Enabled {
		response.BadRequest(c, "集群已停用")
		return
	}
	// 端口转发不走 requireKubeCluster（集群 ID 在请求体里而不是 :id），
	// 所以这里显式补一次授权校验：命名空间与「能不能开隧道」都要过
	if ok, reason := h.kubeForwardGrantCheck(c, &cluster, req.Namespace); !ok {
		response.Forbidden(c, reason)
		return
	}
	client, err := h.kubeClient(cluster)
	if err != nil {
		response.BadRequest(c, "集群凭据不可用: "+err.Error())
		return
	}

	// 先试着把目标解析通再占端口：目标写错时用户马上知道，而不是连上以后才失败
	ctx, cancelProbe := context.WithTimeout(c.Request.Context(), kubeCallTimeout)
	defer cancelProbe()
	if req.TargetKind == "service" {
		if _, err := client.ResolveServicePort(ctx, req.Namespace, req.TargetName, req.TargetPort); err != nil {
			response.BadRequest(c, err.Error())
			return
		}
	} else {
		probe, err := client.DialPortForward(ctx, req.Namespace, req.TargetName, req.TargetPort)
		if err != nil {
			response.BadRequest(c, err.Error())
			return
		}
		_ = probe.Close()
	}

	listener, port, err := h.listenForward(req.ListenPort)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	operator := middleware.CurrentUser(c)
	row := model.KubeForward{
		ClusterID: cluster.ID, ClusterName: cluster.Name,
		Namespace: req.Namespace, TargetKind: req.TargetKind,
		TargetName: req.TargetName, TargetPort: req.TargetPort,
		ListenAddr: h.Cfg.ForwardBind, ListenPort: port,
		ForwardStatus: "running",
		UserID:        operator.ID, Username: operator.Username, ClientIP: c.ClientIP(),
		ExpiresAt: time.Now().Add(time.Duration(req.TTLMinutes) * time.Minute),
	}
	if err := h.DB.Create(&row).Error; err != nil {
		_ = listener.Close()
		response.Error(c, "隧道登记失败")
		return
	}

	tunnelCtx, cancel := context.WithCancel(context.Background())
	tunnel := &forwardTunnel{
		id: row.ID, listener: listener, cancel: cancel, client: client,
		namespace: req.Namespace, targetKind: req.TargetKind,
		targetName: req.TargetName, targetPort: req.TargetPort,
		sem:       make(chan struct{}, h.Cfg.ForwardConnMax),
		expiresAt: row.ExpiresAt,
	}
	h.forwards.add(tunnel)
	go h.serveForward(tunnelCtx, tunnel)
	go h.expireForward(tunnelCtx, tunnel)

	response.OK(c, gin.H{
		"id": row.ID, "listenAddr": row.ListenAddr, "listenPort": row.ListenPort,
		"expiresAt": row.ExpiresAt,
	})
}

// listenForward 占一个监听端口。指定端口就用指定的，否则在区间里挨个试。
func (h *Handler) listenForward(want int) (net.Listener, int, error) {
	bind := h.Cfg.ForwardBind
	if want > 0 {
		if h.forwards.portUsed(want) {
			return nil, 0, fmt.Errorf("端口 %d 已被别的隧道占着", want)
		}
		ln, err := net.Listen("tcp", net.JoinHostPort(bind, strconv.Itoa(want)))
		if err != nil {
			return nil, 0, fmt.Errorf("监听 %d 失败: %v", want, err)
		}
		return ln, want, nil
	}
	for port := h.Cfg.ForwardPortMin; port <= h.Cfg.ForwardPortMax; port++ {
		if h.forwards.portUsed(port) {
			continue
		}
		ln, err := net.Listen("tcp", net.JoinHostPort(bind, strconv.Itoa(port)))
		if err != nil {
			continue
		}
		return ln, port, nil
	}
	return nil, 0, fmt.Errorf("%d-%d 区间里没有空闲端口了", h.Cfg.ForwardPortMin, h.Cfg.ForwardPortMax)
}

// serveForward 收本地连接。每条连接单独拨一条到 API Server 的 WebSocket。
func (h *Handler) serveForward(ctx context.Context, t *forwardTunnel) {
	for {
		conn, err := t.listener.Accept()
		if err != nil {
			if ctx.Err() == nil {
				log.Printf("[forward] 隧道 %d 监听中断: %v", t.id, err)
			}
			return
		}
		select {
		case t.sem <- struct{}{}:
		default:
			// 并发到顶：直接断开而不是排队，让调用方立刻看到失败
			t.connFailed.Add(1)
			t.setLastErr("并发连接已达上限，新连接被拒绝")
			_ = conn.Close()
			continue
		}
		t.connTotal.Add(1)
		t.connActive.Add(1)
		t.touch()
		go func() {
			defer func() {
				<-t.sem
				t.connActive.Add(-1)
			}()
			h.pipeForward(ctx, t, conn)
		}()
	}
}

// pipeForward 把一条本地连接对接到 Pod 端口
func (h *Handler) pipeForward(ctx context.Context, t *forwardTunnel, conn net.Conn) {
	defer conn.Close()

	pod, port := t.targetName, t.targetPort
	if t.targetKind == "service" {
		// 每条连接重新解析：Pod 被重建之后隧道不至于整条报废
		resolveCtx, cancel := context.WithTimeout(ctx, kubeCallTimeout)
		target, err := t.client.ResolveServicePort(resolveCtx, t.namespace, t.targetName, t.targetPort)
		cancel()
		if err != nil {
			t.connFailed.Add(1)
			t.setLastErr(err.Error())
			return
		}
		pod, port = target.Pod, target.Port
	}

	dialCtx, cancel := context.WithTimeout(ctx, forwardDialTimeout)
	stream, err := t.client.DialPortForward(dialCtx, t.namespace, pod, port)
	cancel()
	if err != nil {
		t.connFailed.Add(1)
		t.setLastErr(err.Error())
		return
	}
	defer stream.Close()

	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(&forwardCounter{dst: stream, total: &t.bytesIn, tunnel: t}, conn)
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(&forwardCounter{dst: conn, total: &t.bytesOut, tunnel: t}, stream)
		done <- struct{}{}
	}()

	select {
	case <-done:
	case <-ctx.Done():
	}
	// 任一方向结束就收摊：Close 会让另一个 Copy 立刻返回
	_ = conn.Close()
	_ = stream.Close()
	if msg := stream.RemoteError(); msg != "" {
		t.setLastErr(msg)
	}
	t.touch()
}

// forwardCounter 边转发边计数。
//
// 必须是「写一段加一段」，不能等 io.Copy 返回再加总：连接可能挂着好几个小时，
// 页面上那会儿看到的流量会一直是 0。
type forwardCounter struct {
	dst    io.Writer
	total  *atomic.Int64
	tunnel *forwardTunnel
}

func (w *forwardCounter) Write(p []byte) (int, error) {
	n, err := w.dst.Write(p)
	if n > 0 {
		w.total.Add(int64(n))
		w.tunnel.touch()
	}
	return n, err
}

// expireForward 到点自动关闭
func (h *Handler) expireForward(ctx context.Context, t *forwardTunnel) {
	wait := time.Until(t.expiresAt)
	if wait <= 0 {
		wait = time.Second
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
		h.closeForward(t, "到达存活时长上限，已自动关闭")
	}
}

// closeForward 关掉隧道并把计数写回档案
func (h *Handler) closeForward(t *forwardTunnel, reason string) {
	t.closeOnce.Do(func() {
		t.cancel()
		_ = t.listener.Close()
		h.forwards.remove(t.id)

		now := time.Now()
		fields := map[string]any{
			"forward_status": "stopped",
			"conn_total":     t.connTotal.Load(),
			"conn_failed":    t.connFailed.Load(),
			"bytes_in":       t.bytesIn.Load(),
			"bytes_out":      t.bytesOut.Load(),
			"closed_at":      &now,
			"error_msg":      reason,
		}
		if unix := t.lastActive.Load(); unix > 0 {
			at := time.Unix(unix, 0)
			fields["last_active_at"] = &at
		}
		if err := h.DB.Model(&model.KubeForward{}).Where("id = ?", t.id).Updates(fields).Error; err != nil {
			log.Printf("[forward] 隧道 %d 收尾写库失败: %v", t.id, err)
		}
	})
}

// CloseKubeForward 手动关闭
func (h *Handler) CloseKubeForward(c *gin.Context) {
	id := idParam(c)
	var row model.KubeForward
	if err := h.DB.First(&row, id).Error; err != nil {
		response.NotFound(c, "隧道不存在")
		return
	}
	tunnel := h.forwards.get(row.ID)
	if tunnel == nil {
		if row.ForwardStatus == "running" {
			// 库里说在跑、内存里没有：进程重启过，顺手把状态纠正
			now := time.Now()
			h.DB.Model(&row).Updates(map[string]any{
				"forward_status": "stopped", "closed_at": &now,
				"error_msg": "平台重启，隧道已随进程结束",
			})
		}
		response.OK(c, gin.H{"closed": false, "reason": "隧道已不在运行"})
		return
	}
	operator := middleware.CurrentUser(c)
	h.closeForward(tunnel, "由 "+operator.Username+" 手动关闭")
	response.OK(c, gin.H{"closed": true})
}
