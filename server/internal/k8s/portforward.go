package k8s

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// port-forward 走 API Server 的 WebSocket 通道，而不是 SPDY。
//
// kubectl 用的是 SPDY（新版本改成「用 WebSocket 隧道承载 SPDY 帧」），
// 自己实现 SPDY 要做帧解析、流管理和带字典的 zlib 头压缩，代价太大。
// kubelet 同时还支持一套更朴素的 WebSocket 协议（各语言客户端库用的就是它）：
//
//   - 每个二进制消息的第一个字节是通道号
//   - 请求里第 i 个端口对应两个通道：2i 是数据、2i+1 是错误
//   - 服务端在每个通道上先写 2 字节端口号（小端），之后才是真正的载荷
//   - 客户端写数据只需要带通道号，不需要再写端口号
//
// 局限：一条 WebSocket 只承载一条 TCP 连接。因此隧道对每个进来的本地连接
// 单独拨一条 WebSocket，而不是复用一条长连接（见 handler/forward.go）。
const (
	portForwardDataChannel  byte = 0
	portForwardErrorChannel byte = 1
)

// PortForwardStream 一条已经打通到 Pod 端口的字节流。
//
// 只实现 Read / Write / Close：上层要做的就是把它和本地 TCP 连接对着 io.Copy。
type PortForwardStream struct {
	ws *websocket.Conn

	writeMu sync.Mutex

	// pending 上一条消息没被读完的剩余载荷
	pending []byte
	// dataPortSeen / errPortSeen 该通道的 2 字节端口号前缀是否已经吃掉
	dataPortSeen bool
	errPortSeen  bool

	errMu   sync.Mutex
	errText strings.Builder

	closeOnce sync.Once
}

// Read 读 Pod 侧发回来的数据。
//
// 错误通道上的内容不是数据，而是 kubelet 的说法（端口没开、容器已退出等），
// 攒起来在流结束时一并返回 —— 否则用户只看到「连接被重置」，查不出原因。
func (s *PortForwardStream) Read(p []byte) (int, error) {
	for {
		if len(s.pending) > 0 {
			n := copy(p, s.pending)
			s.pending = s.pending[n:]
			return n, nil
		}

		msgType, payload, err := s.ws.ReadMessage()
		if err != nil {
			if remote := s.remoteError(); remote != "" {
				return 0, fmt.Errorf("集群侧转发失败: %s", remote)
			}
			if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				return 0, io.EOF
			}
			return 0, err
		}
		if msgType != websocket.BinaryMessage || len(payload) == 0 {
			continue
		}

		channel, body := payload[0], payload[1:]
		switch channel {
		case portForwardDataChannel:
			if !s.dataPortSeen {
				if len(body) < 2 {
					continue
				}
				s.dataPortSeen = true
				body = body[2:]
			}
			if len(body) == 0 {
				continue
			}
			s.pending = body
		case portForwardErrorChannel:
			if !s.errPortSeen {
				if len(body) < 2 {
					continue
				}
				s.errPortSeen = true
				body = body[2:]
			}
			if len(body) > 0 {
				s.errMu.Lock()
				s.errText.Write(body)
				s.errMu.Unlock()
			}
		default:
			// 只请求了一个端口，不该出现别的通道；忽略而不是报错，
			// 免得未来 API Server 加通道时把转发弄挂
		}
	}
}

// Write 把本地连接的数据送进 Pod
func (s *PortForwardStream) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	frame := make([]byte, 0, len(p)+1)
	frame = append(frame, portForwardDataChannel)
	frame = append(frame, p...)

	// gorilla 不允许并发写，读侧不写、但 Close 会写关闭帧
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if err := s.ws.WriteMessage(websocket.BinaryMessage, frame); err != nil {
		return 0, err
	}
	return len(p), nil
}

// RemoteError 集群侧在错误通道上说过的话，没有则为空
func (s *PortForwardStream) RemoteError() string { return s.remoteError() }

func (s *PortForwardStream) remoteError() string {
	s.errMu.Lock()
	defer s.errMu.Unlock()
	return strings.TrimSpace(s.errText.String())
}

func (s *PortForwardStream) Close() error {
	var err error
	s.closeOnce.Do(func() {
		s.writeMu.Lock()
		_ = s.ws.WriteControl(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
			time.Now().Add(time.Second))
		s.writeMu.Unlock()
		err = s.ws.Close()
	})
	return err
}

// portForwardURL 把 API 地址换成 ws/wss 并拼出 portforward 子资源路径。
// 单独拆出来是为了可单测（scheme 换错是这类代码最容易犯的错）。
func portForwardURL(server, namespace, pod string, port int) (string, error) {
	if port < 1 || port > 65535 {
		return "", fmt.Errorf("端口 %d 不在 1-65535 之间", port)
	}
	parsed, err := url.Parse(server)
	if err != nil {
		return "", fmt.Errorf("集群地址无法解析: %w", err)
	}
	switch parsed.Scheme {
	case "https":
		parsed.Scheme = "wss"
	case "http":
		parsed.Scheme = "ws"
	default:
		return "", fmt.Errorf("集群地址的协议 %s 不支持", parsed.Scheme)
	}
	// Path 给未转义的原文，RawPath 给转义后的：url.String() 只会按 RawPath 输出，
	// 直接把转义串塞进 Path 会被二次转义（%2F 变成 %252F）
	parsed.Path = fmt.Sprintf("/api/v1/namespaces/%s/pods/%s/portforward", namespace, pod)
	parsed.RawPath = fmt.Sprintf("/api/v1/namespaces/%s/pods/%s/portforward",
		url.PathEscape(namespace), url.PathEscape(pod))
	parsed.RawQuery = url.Values{"ports": []string{strconv.Itoa(port)}}.Encode()
	return parsed.String(), nil
}

// DialPortForward 打通一条到 Pod 端口的流。每条本地连接调一次。
func (c *Client) DialPortForward(ctx context.Context, namespace, pod string, port int) (*PortForwardStream, error) {
	if strings.TrimSpace(namespace) == "" || strings.TrimSpace(pod) == "" {
		return nil, errors.New("命名空间与 Pod 名不能为空")
	}
	target, err := portForwardURL(c.cfg.Server, namespace, pod, port)
	if err != nil {
		return nil, err
	}

	dialer := &websocket.Dialer{
		TLSClientConfig:  c.tls,
		HandshakeTimeout: 15 * time.Second,
		// 只要二进制通道：base64 变体会把载荷编码，转发二进制协议会出问题
		Subprotocols: []string{"v4.channel.k8s.io", "channel.k8s.io"},
	}
	header := http.Header{}
	if c.cfg.Token != "" {
		header.Set("Authorization", "Bearer "+c.cfg.Token)
	} else if c.cfg.Username != "" {
		header.Set("Authorization", "Basic "+basicAuth(c.cfg.Username, c.cfg.Password))
	}

	ws, resp, err := dialer.DialContext(ctx, target, header)
	if err != nil {
		// 握手失败时 API Server 会把原因写在响应体里（没权限、Pod 不存在等）
		if resp != nil {
			defer resp.Body.Close()
			raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
			if msg := apiMessage(raw); msg != "" {
				return nil, fmt.Errorf("建立转发失败（HTTP %d）: %s", resp.StatusCode, msg)
			}
			return nil, fmt.Errorf("建立转发失败（HTTP %d）: %w", resp.StatusCode, err)
		}
		return nil, fmt.Errorf("建立转发失败: %w", err)
	}
	if sub := ws.Subprotocol(); sub != "" && !strings.HasSuffix(sub, "channel.k8s.io") {
		_ = ws.Close()
		return nil, fmt.Errorf("集群选了不支持的子协议 %s", sub)
	}
	return &PortForwardStream{ws: ws}, nil
}

// apiMessage 从 API Server 的错误体里取 message
func apiMessage(raw []byte) string {
	var status apiStatus
	if len(raw) == 0 {
		return ""
	}
	if err := json.Unmarshal(raw, &status); err == nil && status.Message != "" {
		return status.Message
	}
	text := strings.TrimSpace(string(raw))
	if len(text) > 300 {
		text = text[:300] + "…"
	}
	return text
}

// basicAuth WebSocket 握手不能用 http.Request.SetBasicAuth，只能自己拼头
func basicAuth(user, pass string) string {
	return base64.StdEncoding.EncodeToString([]byte(user + ":" + pass))
}

// ---------- Service 目标解析 ----------

// ServiceTarget Service 端口在某个后端 Pod 上的落点
type ServiceTarget struct {
	Pod  string `json:"pod"`
	Port int    `json:"port"`
}

// ResolveServicePort 把 Service 的端口换算成某个就绪 Pod 上的端口。
//
// 每来一条新连接都重新解析一次：这样 Pod 被重建、扩缩容之后隧道还能用，
// 不至于把用户的隧道钉死在一个已经消失的 Pod 上。
func (c *Client) ResolveServicePort(ctx context.Context, namespace, service string, port int) (*ServiceTarget, error) {
	var svc struct {
		Spec struct {
			Ports []struct {
				Name string `json:"name"`
				Port int    `json:"port"`
			} `json:"ports"`
		} `json:"spec"`
	}
	base := namespacedPath("/api/v1", namespace, "services") + "/" + url.PathEscape(service)
	if err := c.get(ctx, base, nil, &svc); err != nil {
		return nil, err
	}

	portName := ""
	matched := false
	for _, item := range svc.Spec.Ports {
		if item.Port == port {
			portName, matched = item.Name, true
			break
		}
	}
	if !matched {
		have := make([]string, 0, len(svc.Spec.Ports))
		for _, item := range svc.Spec.Ports {
			have = append(have, strconv.Itoa(item.Port))
		}
		return nil, fmt.Errorf("Service %s 没有端口 %d（它开的是 %s）",
			service, port, strings.Join(have, ", "))
	}

	// Endpoints 里的端口已经是 Pod 上的真实端口，省掉自己解析 targetPort 名字
	var eps struct {
		Subsets []struct {
			Addresses []struct {
				IP        string `json:"ip"`
				TargetRef *struct {
					Kind string `json:"kind"`
					Name string `json:"name"`
				} `json:"targetRef"`
			} `json:"addresses"`
			Ports []struct {
				Name string `json:"name"`
				Port int    `json:"port"`
			} `json:"ports"`
		} `json:"subsets"`
	}
	epPath := namespacedPath("/api/v1", namespace, "endpoints") + "/" + url.PathEscape(service)
	if err := c.get(ctx, epPath, nil, &eps); err != nil {
		return nil, err
	}

	for _, subset := range eps.Subsets {
		podPort := 0
		for _, item := range subset.Ports {
			if item.Name == portName || len(subset.Ports) == 1 {
				podPort = item.Port
				break
			}
		}
		if podPort == 0 {
			continue
		}
		for _, addr := range subset.Addresses {
			if addr.TargetRef != nil && addr.TargetRef.Kind == "Pod" && addr.TargetRef.Name != "" {
				return &ServiceTarget{Pod: addr.TargetRef.Name, Port: podPort}, nil
			}
		}
	}
	return nil, fmt.Errorf("Service %s 当前没有就绪的后端 Pod", service)
}
