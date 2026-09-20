package k8s

import (
	"context"
	"encoding/binary"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestPortForwardURL(t *testing.T) {
	cases := []struct {
		server, ns, pod string
		port            int
		want            string
		wantErr         bool
	}{
		{server: "https://1.2.3.4:6443", ns: "default", pod: "web-0", port: 8080,
			want: "wss://1.2.3.4:6443/api/v1/namespaces/default/pods/web-0/portforward?ports=8080"},
		{server: "http://127.0.0.1:8001", ns: "kube-system", pod: "coredns-1", port: 53,
			want: "ws://127.0.0.1:8001/api/v1/namespaces/kube-system/pods/coredns-1/portforward?ports=53"},
		// 命名空间与 Pod 名要转义，不能让它们改变路径结构
		{server: "https://h:6443", ns: "a/b", pod: "c d", port: 80,
			want: "wss://h:6443/api/v1/namespaces/a%2Fb/pods/c%20d/portforward?ports=80"},
		{server: "https://h:6443", ns: "default", pod: "p", port: 0, wantErr: true},
		{server: "https://h:6443", ns: "default", pod: "p", port: 70000, wantErr: true},
		{server: "ftp://h", ns: "default", pod: "p", port: 80, wantErr: true},
	}
	for _, tc := range cases {
		got, err := portForwardURL(tc.server, tc.ns, tc.pod, tc.port)
		if tc.wantErr {
			if err == nil {
				t.Fatalf("%s 应该报错，却得到 %s", tc.server, got)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s 意外报错: %v", tc.server, err)
		}
		if got != tc.want {
			t.Fatalf("URL 不对\n得到 %s\n期望 %s", got, tc.want)
		}
	}
}

// newFakePortForward 起一个假 API Server：按 kubelet 的 WebSocket 约定回话，
// 把收到的数据大写后回送，并在错误通道上写一句话。
func newFakePortForward(t *testing.T, errText string) (*httptest.Server, *[]string) {
	t.Helper()
	upgrader := websocket.Upgrader{
		Subprotocols: []string{"v4.channel.k8s.io"},
		CheckOrigin:  func(*http.Request) bool { return true },
	}
	seen := &[]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*seen = append(*seen, r.URL.Path+"?"+r.URL.RawQuery)
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("升级失败: %v", err)
			return
		}
		defer conn.Close()

		portBytes := make([]byte, 2)
		binary.LittleEndian.PutUint16(portBytes, 8080)
		// 每个通道先写 2 字节端口号，客户端必须把它吃掉
		_ = conn.WriteMessage(websocket.BinaryMessage, append([]byte{0}, portBytes...))
		_ = conn.WriteMessage(websocket.BinaryMessage, append([]byte{1}, portBytes...))
		if errText != "" {
			_ = conn.WriteMessage(websocket.BinaryMessage, append([]byte{1}, []byte(errText)...))
		}

		for {
			_, payload, err := conn.ReadMessage()
			if err != nil {
				return
			}
			if len(payload) == 0 || payload[0] != 0 {
				continue
			}
			echo := append([]byte{0}, []byte(strings.ToUpper(string(payload[1:])))...)
			if err := conn.WriteMessage(websocket.BinaryMessage, echo); err != nil {
				return
			}
		}
	}))
	t.Cleanup(srv.Close)
	return srv, seen
}

func TestPortForwardStreamRoundTrip(t *testing.T) {
	srv, seen := newFakePortForward(t, "")
	client, err := NewClient(&Config{Server: srv.URL, Token: "t"}, 5*time.Second)
	if err != nil {
		t.Fatalf("建客户端失败: %v", err)
	}

	stream, err := client.DialPortForward(context.Background(), "default", "web-0", 8080)
	if err != nil {
		t.Fatalf("拨号失败: %v", err)
	}
	defer stream.Close()

	if len(*seen) != 1 || !strings.Contains((*seen)[0], "/pods/web-0/portforward?ports=8080") {
		t.Fatalf("请求路径不对: %v", *seen)
	}
	if _, err := stream.Write([]byte("ping")); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	buf := make([]byte, 16)
	n, err := stream.Read(buf)
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	// 端口号前缀必须被吃掉，只剩真正的载荷
	if got := string(buf[:n]); got != "PING" {
		t.Fatalf("读到 %q，期望 PING", got)
	}
}

func TestPortForwardStreamSurfacesRemoteError(t *testing.T) {
	srv, _ := newFakePortForward(t, "unable to do port forwarding: port 8080 closed")
	client, _ := NewClient(&Config{Server: srv.URL, Token: "t"}, 5*time.Second)

	stream, err := client.DialPortForward(context.Background(), "default", "web-0", 8080)
	if err != nil {
		t.Fatalf("拨号失败: %v", err)
	}
	defer stream.Close()

	// 错误通道上的内容不是数据，Read 不该把它当载荷返回
	if _, err := stream.Write([]byte("x")); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	buf := make([]byte, 16)
	n, err := stream.Read(buf)
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if got := string(buf[:n]); got != "X" {
		t.Fatalf("读到 %q，期望 X", got)
	}
	if msg := stream.RemoteError(); !strings.Contains(msg, "port 8080 closed") {
		t.Fatalf("集群侧报错没被带出来: %q", msg)
	}
}

func TestDialPortForwardBadRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"kind":"Status","message":"pods \"ghost\" not found","code":404}`))
	}))
	defer srv.Close()

	client, _ := NewClient(&Config{Server: srv.URL, Token: "t"}, 5*time.Second)
	_, err := client.DialPortForward(context.Background(), "default", "ghost", 80)
	if err == nil {
		t.Fatal("Pod 不存在时应该报错")
	}
	// API Server 自己的说法要透出来，否则用户只看到「握手失败」
	if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("报错没带上 API Server 的原话: %v", err)
	}
}

func TestResolveServicePort(t *testing.T) {
	const svcJSON = `{"spec":{"ports":[{"name":"http","port":80},{"name":"metrics","port":9090}]}}`
	const epJSON = `{"subsets":[{
		"addresses":[{"ip":"10.42.0.5","targetRef":{"kind":"Pod","name":"web-abc"}}],
		"ports":[{"name":"http","port":8080},{"name":"metrics","port":9100}]}]}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/services/"):
			_, _ = w.Write([]byte(svcJSON))
		case strings.Contains(r.URL.Path, "/endpoints/"):
			_, _ = w.Write([]byte(epJSON))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	client, _ := NewClient(&Config{Server: srv.URL, Token: "t"}, 5*time.Second)
	ctx := context.Background()

	// Service 端口要换算成 Pod 上的真实端口（80 -> 8080，按端口名对齐）
	target, err := client.ResolveServicePort(ctx, "default", "web", 80)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if target.Pod != "web-abc" || target.Port != 8080 {
		t.Fatalf("解析结果不对: %+v", target)
	}

	// 同名多端口时别串台
	target, err = client.ResolveServicePort(ctx, "default", "web", 9090)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if target.Port != 9100 {
		t.Fatalf("metrics 端口解析成了 %d", target.Port)
	}

	if _, err := client.ResolveServicePort(ctx, "default", "web", 8443); err == nil {
		t.Fatal("Service 上没有的端口应该报错")
	} else if !strings.Contains(err.Error(), "它开的是") {
		t.Fatalf("报错应该告诉用户 Service 开了哪些端口: %v", err)
	}
}

func TestResolveServicePortNoReadyBackend(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/services/") {
			_, _ = w.Write([]byte(`{"spec":{"ports":[{"name":"http","port":80}]}}`))
			return
		}
		// Pod 全挂了的时候 Endpoints 是空的
		_, _ = w.Write([]byte(`{"subsets":[]}`))
	}))
	defer srv.Close()

	client, _ := NewClient(&Config{Server: srv.URL, Token: "t"}, 5*time.Second)
	_, err := client.ResolveServicePort(context.Background(), "default", "web", 80)
	if err == nil || !strings.Contains(err.Error(), "没有就绪的后端 Pod") {
		t.Fatalf("应该明确说没有就绪后端，实际: %v", err)
	}
}
