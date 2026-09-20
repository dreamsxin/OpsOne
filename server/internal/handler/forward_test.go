package handler

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/gorilla/websocket"
	"gorm.io/gorm"

	"ops-platform/server/internal/config"
	"ops-platform/server/internal/k8s"
	"ops-platform/server/internal/model"
)

func TestValidateForwardReq(t *testing.T) {
	// 不填 targetKind 时按 pod 处理，不填 ttl 时取配置上限
	req := kubeForwardReq{ClusterID: 1, Namespace: "default", TargetName: "web-0", TargetPort: 8080}
	if err := validateForwardReq(&req, 30000, 30099, 120); err != nil {
		t.Fatalf("合法请求被拒: %v", err)
	}
	if req.TargetKind != "pod" || req.TTLMinutes != 120 {
		t.Fatalf("默认值不对: %+v", req)
	}

	cases := []struct {
		name string
		req  kubeForwardReq
		want string
	}{
		{"没选集群", kubeForwardReq{Namespace: "default", TargetName: "a", TargetPort: 80}, "请选择集群"},
		{"没填命名空间", kubeForwardReq{ClusterID: 1, TargetName: "a", TargetPort: 80}, "命名空间"},
		{"没填目标", kubeForwardReq{ClusterID: 1, Namespace: "default", TargetPort: 80}, "转发目标不能为空"},
		{"目标类型不支持", kubeForwardReq{ClusterID: 1, Namespace: "default", TargetName: "a",
			TargetPort: 80, TargetKind: "deployment"}, "只支持 pod 或 service"},
		{"目标端口越界", kubeForwardReq{ClusterID: 1, Namespace: "default", TargetName: "a",
			TargetPort: 0}, "目标端口"},
		{"监听端口不在区间", kubeForwardReq{ClusterID: 1, Namespace: "default", TargetName: "a",
			TargetPort: 80, ListenPort: 8080}, "30000-30099"},
		{"ttl 超上限", kubeForwardReq{ClusterID: 1, Namespace: "default", TargetName: "a",
			TargetPort: 80, TTLMinutes: 999}, "最多 120 分钟"},
	}
	for _, tc := range cases {
		local := tc.req
		err := validateForwardReq(&local, 30000, 30099, 120)
		if err == nil {
			t.Fatalf("%s 应该报错", tc.name)
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s 的报错是 %q，期望包含 %q", tc.name, err, tc.want)
		}
	}
}

// newForwardTestHandler 内存库 + 只放开一个端口的配置
func newForwardTestHandler(t *testing.T, port int) *Handler {
	t.Helper()
	g, err := gorm.Open(sqlite.Open("file::memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开内存库失败: %v", err)
	}
	if err := g.AutoMigrate(&model.KubeForward{}); err != nil {
		t.Fatalf("建表失败: %v", err)
	}
	h := New(g, &config.Config{
		ForwardBind: "127.0.0.1", ForwardPortMin: port, ForwardPortMax: port,
		ForwardMax: 2, ForwardTTLMinutes: 10, ForwardConnMax: 1,
	})
	return h
}

// fakePodServer 假 API Server：把 portforward 的 WebSocket 接到一个真实的本地
// TCP 回声服务上，用来验证「本地连接 -> 隧道 -> Pod 端口」整条链路。
func fakePodServer(t *testing.T) *httptest.Server {
	t.Helper()

	// 真实的「Pod 里的服务」：收到什么就大写回去
	echo, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("起回声服务失败: %v", err)
	}
	t.Cleanup(func() { _ = echo.Close() })
	go func() {
		for {
			conn, err := echo.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				buf := make([]byte, 256)
				for {
					n, err := conn.Read(buf)
					if n > 0 {
						_, _ = conn.Write([]byte(strings.ToUpper(string(buf[:n]))))
					}
					if err != nil {
						return
					}
				}
			}()
		}
	}()

	upgrader := websocket.Upgrader{
		Subprotocols: []string{"v4.channel.k8s.io"},
		CheckOrigin:  func(*http.Request) bool { return true },
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/portforward") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()

		target, err := net.Dial("tcp", echo.Addr().String())
		if err != nil {
			return
		}
		defer target.Close()

		portBytes := make([]byte, 2)
		binary.LittleEndian.PutUint16(portBytes, 8080)
		_ = conn.WriteMessage(websocket.BinaryMessage, append([]byte{0}, portBytes...))
		_ = conn.WriteMessage(websocket.BinaryMessage, append([]byte{1}, portBytes...))

		go func() {
			buf := make([]byte, 1024)
			for {
				n, err := target.Read(buf)
				if n > 0 {
					frame := append([]byte{0}, buf[:n]...)
					if err := conn.WriteMessage(websocket.BinaryMessage, frame); err != nil {
						return
					}
				}
				if err != nil {
					return
				}
			}
		}()
		for {
			_, payload, err := conn.ReadMessage()
			if err != nil {
				return
			}
			if len(payload) > 1 && payload[0] == 0 {
				if _, err := target.Write(payload[1:]); err != nil {
					return
				}
			}
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// startTestTunnel 按测试需要手工拼一条隧道（不走 gin，免得为了鉴权造一堆上下文）
func startTestTunnel(t *testing.T, h *Handler, srvURL string) (*forwardTunnel, int) {
	t.Helper()
	client, err := k8s.NewClient(&k8s.Config{Server: srvURL, Token: "t"}, 5*time.Second)
	if err != nil {
		t.Fatalf("建客户端失败: %v", err)
	}
	listener, port, err := h.listenForward(0)
	if err != nil {
		t.Fatalf("占端口失败: %v", err)
	}
	row := model.KubeForward{
		ClusterID: 1, ClusterName: "test", Namespace: "default",
		TargetKind: "pod", TargetName: "web-0", TargetPort: 8080,
		ListenAddr: "127.0.0.1", ListenPort: port, ForwardStatus: "running",
		ExpiresAt: time.Now().Add(time.Minute),
	}
	if err := h.DB.Create(&row).Error; err != nil {
		t.Fatalf("登记隧道失败: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	tunnel := &forwardTunnel{
		id: row.ID, listener: listener, cancel: cancel, client: client,
		namespace: "default", targetKind: "pod", targetName: "web-0", targetPort: 8080,
		sem: make(chan struct{}, h.Cfg.ForwardConnMax), expiresAt: row.ExpiresAt,
	}
	h.forwards.add(tunnel)
	go h.serveForward(ctx, tunnel)
	return tunnel, port
}

func TestForwardTunnelPipesTraffic(t *testing.T) {
	srv := fakePodServer(t)
	h := newForwardTestHandler(t, freePort(t))
	tunnel, port := startTestTunnel(t, h, srv.URL)

	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 3*time.Second)
	if err != nil {
		t.Fatalf("连隧道失败: %v", err)
	}
	defer conn.Close()

	if _, err := conn.Write([]byte("hello-pod")); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 64)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("读取失败: %v（集群侧: %s）", err, tunnel.lastError())
	}
	if got := string(buf[:n]); got != "HELLO-POD" {
		t.Fatalf("读到 %q，期望 HELLO-POD", got)
	}

	// 计数要跟着走，否则页面上看不出隧道到底有没有人用。
	// 计数是在写完之后加的，客户端可能比计数先跑到，这里给它一点时间。
	if tunnel.connTotal.Load() != 1 {
		t.Fatalf("连接数 %d，期望 1", tunnel.connTotal.Load())
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if tunnel.bytesIn.Load() == int64(len("hello-pod")) && tunnel.bytesOut.Load() == int64(n) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if tunnel.bytesIn.Load() != int64(len("hello-pod")) || tunnel.bytesOut.Load() != int64(n) {
		t.Fatalf("字节数不对: in=%d out=%d", tunnel.bytesIn.Load(), tunnel.bytesOut.Load())
	}
	if tunnel.lastActive.Load() == 0 {
		t.Fatal("最近活跃时间没被刷新")
	}

	// 关闭要落库：状态、计数、关闭时间
	h.closeForward(tunnel, "测试关闭")
	var row model.KubeForward
	if err := h.DB.First(&row, tunnel.id).Error; err != nil {
		t.Fatalf("读档案失败: %v", err)
	}
	if row.ForwardStatus != "stopped" || row.ClosedAt == nil || row.ConnTotal != 1 {
		t.Fatalf("关闭后的档案不对: %+v", row)
	}
	if row.BytesIn == 0 || row.BytesOut == 0 {
		t.Fatalf("字节数没写回: in=%d out=%d", row.BytesIn, row.BytesOut)
	}
	// 关掉之后端口应该可以再次占用
	if _, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 500*time.Millisecond); err == nil {
		t.Fatal("隧道关闭后端口仍然可连")
	}
}

func TestForwardTunnelRejectsOverConnLimit(t *testing.T) {
	srv := fakePodServer(t)
	h := newForwardTestHandler(t, freePort(t)) // ForwardConnMax = 1
	tunnel, port := startTestTunnel(t, h, srv.URL)
	defer h.closeForward(tunnel, "测试结束")

	addr := fmt.Sprintf("127.0.0.1:%d", port)
	first, err := net.DialTimeout("tcp", addr, 3*time.Second)
	if err != nil {
		t.Fatalf("第一条连接失败: %v", err)
	}
	defer first.Close()
	if _, err := first.Write([]byte("a")); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	_ = first.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := first.Read(make([]byte, 8)); err != nil {
		t.Fatalf("第一条连接没通: %v", err)
	}

	// 第二条连接会被闸门挡掉：立刻断开而不是排队
	second, err := net.DialTimeout("tcp", addr, 3*time.Second)
	if err != nil {
		t.Fatalf("第二条连接拨号失败: %v", err)
	}
	defer second.Close()
	_ = second.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := second.Read(make([]byte, 8)); err == nil {
		t.Fatal("超过并发上限的连接应该被断开")
	}
	if tunnel.connFailed.Load() == 0 {
		t.Fatal("被拒的连接没记进失败计数")
	}
	if !strings.Contains(tunnel.lastError(), "并发连接已达上限") {
		t.Fatalf("失败原因不对: %q", tunnel.lastError())
	}
}

func TestListenForwardExhaustsRange(t *testing.T) {
	h := newForwardTestHandler(t, freePort(t))
	first, port, err := h.listenForward(0)
	if err != nil {
		t.Fatalf("第一次占端口失败: %v", err)
	}
	defer first.Close()

	// 区间只有一个端口，第二次必须明确报「没空闲端口」
	if _, _, err := h.listenForward(0); err == nil {
		t.Fatal("端口用尽时应该报错")
	} else if !strings.Contains(err.Error(), "没有空闲端口") {
		t.Fatalf("报错不对: %v", err)
	}
	// 指定区间外的端口也要拒绝（走的是 validateForwardReq，这里确认监听层不兜）
	if _, _, err := h.listenForward(port); err == nil {
		t.Fatal("端口已被占用时应该报错")
	}
}

func TestResetForwardsMarksStaleRows(t *testing.T) {
	h := newForwardTestHandler(t, freePort(t))
	row := model.KubeForward{
		ClusterID: 1, Namespace: "default", TargetKind: "pod", TargetName: "web-0",
		TargetPort: 80, ListenAddr: "127.0.0.1", ListenPort: 30001,
		ForwardStatus: "running", ExpiresAt: time.Now().Add(time.Hour),
	}
	if err := h.DB.Create(&row).Error; err != nil {
		t.Fatalf("写入失败: %v", err)
	}

	h.ResetForwards()

	var got model.KubeForward
	if err := h.DB.First(&got, row.ID).Error; err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if got.ForwardStatus != "stopped" || got.ClosedAt == nil {
		t.Fatalf("重启后残留的隧道没被标记: %+v", got)
	}
	if !strings.Contains(got.ErrorMsg, "平台重启") {
		t.Fatalf("没说明原因: %q", got.ErrorMsg)
	}
}

// freePort 找一个当前空闲的端口，避免测试之间抢端口
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("找空闲端口失败: %v", err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}
