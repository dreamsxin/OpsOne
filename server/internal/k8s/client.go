package k8s

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// Client 一个绑定到具体集群的 REST 客户端
type Client struct {
	cfg  *Config
	http *http.Client
	// tls 留着给 WebSocket 拨号用（port-forward 不走 http.Client）
	tls *tls.Config
}

// NewClient 按连接配置组装 HTTP 客户端（含 mTLS）
func NewClient(cfg *Config, timeout time.Duration) (*Client, error) {
	tlsCfg := &tls.Config{InsecureSkipVerify: cfg.SkipVerify} //nolint:gosec // 由 kubeconfig 显式声明

	if len(cfg.CAData) > 0 {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(cfg.CAData) {
			return nil, fmt.Errorf("certificate-authority-data 不是合法的 PEM 证书")
		}
		tlsCfg.RootCAs = pool
	}
	if len(cfg.CertData) > 0 {
		pair, err := tls.X509KeyPair(cfg.CertData, cfg.KeyData)
		if err != nil {
			return nil, fmt.Errorf("客户端证书与私钥不匹配: %w", err)
		}
		tlsCfg.Certificates = []tls.Certificate{pair}
	}
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &Client{
		cfg: cfg,
		tls: tlsCfg,
		http: &http.Client{
			Timeout:   timeout,
			Transport: &http.Transport{TLSClientConfig: tlsCfg},
		},
	}, nil
}

// apiStatus API Server 返回错误时的标准结构
type apiStatus struct {
	Kind    string `json:"kind"`
	Message string `json:"message"`
	Reason  string `json:"reason"`
	Code    int    `json:"code"`
}

// get 发一次 GET 并把响应体解到 out
func (c *Client) get(ctx context.Context, path string, query url.Values, out any) error {
	return c.do(ctx, http.MethodGet, path, query, "", nil, out)
}

// do 发一次请求并把响应体解到 out。body 为空时不带请求体。
//
// 错误信息一律带上 API Server 自己的 message：k8s 的报错（字段不合法、
// 字段冲突、没权限）说得比任何本地包装都清楚。
func (c *Client) do(ctx context.Context, method, path string, query url.Values,
	contentType string, body []byte, out any) error {
	target := c.cfg.Server + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	var reader io.Reader
	if len(body) > 0 {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if c.cfg.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.cfg.Token)
	} else if c.cfg.Username != "" {
		req.SetBasicAuth(c.cfg.Username, c.cfg.Password)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 400 {
		var status apiStatus
		if json.Unmarshal(raw, &status) == nil && status.Message != "" {
			// 把 API Server 自己的说法透出去，比「请求失败」有用得多
			return fmt.Errorf("API 返回 %d: %s", resp.StatusCode, status.Message)
		}
		return fmt.Errorf("API 返回 %d", resp.StatusCode)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(raw, out)
}

// getRaw 发一次 GET 并原样返回响应体。
//
// 日志这类纯文本接口不能走 JSON 解码；limit 用来防止一次把内存拉爆，
// 返回值第二项说明是否已被截断（上层要告诉用户「只看到一部分」）。
func (c *Client) getRaw(ctx context.Context, path string, query url.Values, limit int64) ([]byte, bool, error) {
	target := c.cfg.Server + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, false, err
	}
	if c.cfg.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.cfg.Token)
	} else if c.cfg.Username != "" {
		req.SetBasicAuth(c.cfg.Username, c.cfg.Password)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, false, err
	}
	defer resp.Body.Close()

	// 多读 1 字节，用来判断是否触到了上限
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, false, err
	}
	if resp.StatusCode >= 400 {
		var status apiStatus
		if json.Unmarshal(body, &status) == nil && status.Message != "" {
			return nil, false, fmt.Errorf("API 返回 %d: %s", resp.StatusCode, status.Message)
		}
		return nil, false, fmt.Errorf("API 返回 %d", resp.StatusCode)
	}
	if int64(len(body)) > limit {
		return body[:limit], true, nil
	}
	return body, false, nil
}

// ---------- 版本与节点 ----------

// VersionInfo /version 的返回
type VersionInfo struct {
	GitVersion string `json:"gitVersion"`
	Platform   string `json:"platform"`
}

func (c *Client) Version(ctx context.Context) (*VersionInfo, error) {
	var info VersionInfo
	if err := c.get(ctx, "/version", nil, &info); err != nil {
		return nil, err
	}
	return &info, nil
}

// Node 节点的精简视图
type Node struct {
	Name       string   `json:"name"`
	Ready      bool     `json:"ready"`
	Roles      []string `json:"roles"`
	Version    string   `json:"version"`
	OSImage    string   `json:"osImage"`
	InternalIP string   `json:"internalIP"`
	CPU        string   `json:"cpu"`
	Memory     string   `json:"memory"`
	Pods       string   `json:"pods"`
	// Unschedulable 节点被 cordon 了，调度器不会往上放新 Pod
	Unschedulable bool      `json:"unschedulable"`
	CreatedAt     time.Time `json:"createdAt"`
}

type nodeList struct {
	Items []struct {
		Metadata struct {
			Name              string            `json:"name"`
			Labels            map[string]string `json:"labels"`
			CreationTimestamp time.Time         `json:"creationTimestamp"`
		} `json:"metadata"`
		Spec struct {
			Unschedulable bool `json:"unschedulable"`
		} `json:"spec"`
		Status struct {
			Capacity   map[string]string `json:"capacity"`
			Conditions []struct {
				Type   string `json:"type"`
				Status string `json:"status"`
			} `json:"conditions"`
			Addresses []struct {
				Type    string `json:"type"`
				Address string `json:"address"`
			} `json:"addresses"`
			NodeInfo struct {
				KubeletVersion string `json:"kubeletVersion"`
				OSImage        string `json:"osImage"`
			} `json:"nodeInfo"`
		} `json:"status"`
	} `json:"items"`
}

func (c *Client) Nodes(ctx context.Context) ([]Node, error) {
	var raw nodeList
	if err := c.get(ctx, "/api/v1/nodes", nil, &raw); err != nil {
		return nil, err
	}
	nodes := make([]Node, 0, len(raw.Items))
	for _, item := range raw.Items {
		node := Node{
			Name:          item.Metadata.Name,
			Version:       item.Status.NodeInfo.KubeletVersion,
			OSImage:       item.Status.NodeInfo.OSImage,
			Unschedulable: item.Spec.Unschedulable,
			CreatedAt:     item.Metadata.CreationTimestamp,
			CPU:           item.Status.Capacity["cpu"],
			Memory:        item.Status.Capacity["memory"],
			Pods:          item.Status.Capacity["pods"],
			Roles:         []string{},
		}
		for _, cond := range item.Status.Conditions {
			if cond.Type == "Ready" {
				node.Ready = cond.Status == "True"
			}
		}
		for _, addr := range item.Status.Addresses {
			if addr.Type == "InternalIP" {
				node.InternalIP = addr.Address
			}
		}
		// 角色写在标签里：node-role.kubernetes.io/<role>
		for key := range item.Metadata.Labels {
			if role, ok := trimPrefix(key, "node-role.kubernetes.io/"); ok && role != "" {
				node.Roles = append(node.Roles, role)
			}
		}
		nodes = append(nodes, node)
	}
	return nodes, nil
}

func trimPrefix(s, prefix string) (string, bool) {
	if len(s) > len(prefix) && s[:len(prefix)] == prefix {
		return s[len(prefix):], true
	}
	return "", false
}

// ---------- 命名空间 ----------

type Namespace struct {
	Name      string    `json:"name"`
	Phase     string    `json:"phase"`
	CreatedAt time.Time `json:"createdAt"`
}

func (c *Client) Namespaces(ctx context.Context) ([]Namespace, error) {
	var raw struct {
		Items []struct {
			Metadata struct {
				Name              string    `json:"name"`
				CreationTimestamp time.Time `json:"creationTimestamp"`
			} `json:"metadata"`
			Status struct {
				Phase string `json:"phase"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := c.get(ctx, "/api/v1/namespaces", nil, &raw); err != nil {
		return nil, err
	}
	list := make([]Namespace, 0, len(raw.Items))
	for _, item := range raw.Items {
		list = append(list, Namespace{
			Name: item.Metadata.Name, Phase: item.Status.Phase,
			CreatedAt: item.Metadata.CreationTimestamp,
		})
	}
	return list, nil
}

// ---------- 工作负载 ----------

// Workload Deployment / StatefulSet / DaemonSet 的统一精简视图
type Workload struct {
	Kind      string    `json:"kind"`
	Namespace string    `json:"namespace"`
	Name      string    `json:"name"`
	Desired   int       `json:"desired"`
	Ready     int       `json:"ready"`
	Updated   int       `json:"updated"`
	Available int       `json:"available"`
	Images    []string  `json:"images"`
	CreatedAt time.Time `json:"createdAt"`
	// Healthy 就绪副本是否已达期望值；期望为 0 时视为健康（有意缩容到 0）
	Healthy bool `json:"healthy"`
}

type workloadList struct {
	Items []struct {
		Metadata struct {
			Name              string    `json:"name"`
			Namespace         string    `json:"namespace"`
			CreationTimestamp time.Time `json:"creationTimestamp"`
		} `json:"metadata"`
		Spec struct {
			Replicas *int `json:"replicas"`
			Template struct {
				Spec struct {
					Containers []struct {
						Image string `json:"image"`
					} `json:"containers"`
				} `json:"spec"`
			} `json:"template"`
		} `json:"spec"`
		Status struct {
			ReadyReplicas   int `json:"readyReplicas"`
			UpdatedReplicas int `json:"updatedReplicas"`
			// StatefulSet 用 currentReplicas，Deployment 用 availableReplicas
			CurrentReplicas    int `json:"currentReplicas"`
			AvailableReplicas  int `json:"availableReplicas"`
			DesiredNumberSched int `json:"desiredNumberScheduled"`
			NumberReady        int `json:"numberReady"`
			UpdatedNumberSched int `json:"updatedNumberScheduled"`
		} `json:"status"`
	} `json:"items"`
}

// namespacedPath 拼出带/不带命名空间的资源路径
func namespacedPath(apiRoot, namespace, resource string) string {
	if namespace == "" {
		return fmt.Sprintf("%s/%s", apiRoot, resource)
	}
	return fmt.Sprintf("%s/namespaces/%s/%s", apiRoot, url.PathEscape(namespace), resource)
}

func (c *Client) workloads(ctx context.Context, namespace, resource, kind string) ([]Workload, error) {
	var raw workloadList
	path := namespacedPath("/apis/apps/v1", namespace, resource)
	if err := c.get(ctx, path, nil, &raw); err != nil {
		return nil, err
	}
	list := make([]Workload, 0, len(raw.Items))
	for _, item := range raw.Items {
		work := Workload{
			Kind: kind, Namespace: item.Metadata.Namespace, Name: item.Metadata.Name,
			CreatedAt: item.Metadata.CreationTimestamp, Images: []string{},
		}
		for _, container := range item.Spec.Template.Spec.Containers {
			work.Images = append(work.Images, container.Image)
		}
		if kind == "DaemonSet" {
			// DaemonSet 没有 replicas，期望值由调度结果给出
			work.Desired = item.Status.DesiredNumberSched
			work.Ready = item.Status.NumberReady
			work.Updated = item.Status.UpdatedNumberSched
			work.Available = item.Status.NumberReady
		} else {
			work.Desired = 1
			if item.Spec.Replicas != nil {
				work.Desired = *item.Spec.Replicas
			}
			work.Ready = item.Status.ReadyReplicas
			work.Updated = item.Status.UpdatedReplicas
			work.Available = item.Status.AvailableReplicas
			if kind == "StatefulSet" {
				work.Available = item.Status.CurrentReplicas
			}
		}
		work.Healthy = work.Ready >= work.Desired
		list = append(list, work)
	}
	return list, nil
}

func (c *Client) Deployments(ctx context.Context, namespace string) ([]Workload, error) {
	return c.workloads(ctx, namespace, "deployments", "Deployment")
}

func (c *Client) StatefulSets(ctx context.Context, namespace string) ([]Workload, error) {
	return c.workloads(ctx, namespace, "statefulsets", "StatefulSet")
}

func (c *Client) DaemonSets(ctx context.Context, namespace string) ([]Workload, error) {
	return c.workloads(ctx, namespace, "daemonsets", "DaemonSet")
}

// ---------- Pod ----------

type Pod struct {
	Namespace string    `json:"namespace"`
	Name      string    `json:"name"`
	Phase     string    `json:"phase"`
	NodeName  string    `json:"nodeName"`
	PodIP     string    `json:"podIP"`
	Ready     string    `json:"ready"` // 形如 1/2
	Restarts  int       `json:"restarts"`
	Images    []string  `json:"images"`
	CreatedAt time.Time `json:"createdAt"`
	// Containers 容器名，看日志时要指定；init 容器用 initContainers 单列
	Containers     []string `json:"containers"`
	InitContainers []string `json:"initContainers"`
	// Restarted 有容器重启过，说明「上一个容器的日志」有内容可看
	Restarted    bool   `json:"restarted"`
	ContainerMsg string `json:"containerMsg"` // 不正常容器的原因，例如 CrashLoopBackOff
}

func (c *Client) Pods(ctx context.Context, namespace string) ([]Pod, error) {
	var raw struct {
		Items []struct {
			Metadata struct {
				Name              string    `json:"name"`
				Namespace         string    `json:"namespace"`
				CreationTimestamp time.Time `json:"creationTimestamp"`
			} `json:"metadata"`
			Spec struct {
				NodeName   string `json:"nodeName"`
				Containers []struct {
					Name  string `json:"name"`
					Image string `json:"image"`
				} `json:"containers"`
				InitContainers []struct {
					Name string `json:"name"`
				} `json:"initContainers"`
			} `json:"spec"`
			Status struct {
				Phase             string `json:"phase"`
				PodIP             string `json:"podIP"`
				ContainerStatuses []struct {
					Name         string `json:"name"`
					Ready        bool   `json:"ready"`
					RestartCount int    `json:"restartCount"`
					State        struct {
						Waiting *struct {
							Reason  string `json:"reason"`
							Message string `json:"message"`
						} `json:"waiting"`
						Terminated *struct {
							Reason   string `json:"reason"`
							ExitCode int    `json:"exitCode"`
						} `json:"terminated"`
					} `json:"state"`
				} `json:"containerStatuses"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := c.get(ctx, namespacedPath("/api/v1", namespace, "pods"), nil, &raw); err != nil {
		return nil, err
	}

	list := make([]Pod, 0, len(raw.Items))
	for _, item := range raw.Items {
		pod := Pod{
			Namespace: item.Metadata.Namespace, Name: item.Metadata.Name,
			Phase: item.Status.Phase, NodeName: item.Spec.NodeName,
			PodIP: item.Status.PodIP, CreatedAt: item.Metadata.CreationTimestamp,
			Images: []string{}, Containers: []string{}, InitContainers: []string{},
		}
		for _, container := range item.Spec.Containers {
			pod.Images = append(pod.Images, container.Image)
			pod.Containers = append(pod.Containers, container.Name)
		}
		for _, container := range item.Spec.InitContainers {
			pod.InitContainers = append(pod.InitContainers, container.Name)
		}
		ready := 0
		for _, status := range item.Status.ContainerStatuses {
			if status.Ready {
				ready++
			}
			pod.Restarts += status.RestartCount
			if status.RestartCount > 0 {
				pod.Restarted = true
			}
			if pod.ContainerMsg == "" {
				if status.State.Waiting != nil && status.State.Waiting.Reason != "" {
					pod.ContainerMsg = status.Name + ": " + status.State.Waiting.Reason
				} else if status.State.Terminated != nil && status.State.Terminated.Reason != "" &&
					status.State.Terminated.ExitCode != 0 {
					pod.ContainerMsg = fmt.Sprintf("%s: %s(exit %d)",
						status.Name, status.State.Terminated.Reason, status.State.Terminated.ExitCode)
				}
			}
		}
		pod.Ready = strconv.Itoa(ready) + "/" + strconv.Itoa(len(item.Status.ContainerStatuses))
		list = append(list, pod)
	}
	return list, nil
}

// ---------- 容器日志 ----------

// LogOptions 取日志的条件。零值表示「不带这个参数，交给 API Server 的默认」。
type LogOptions struct {
	// Container 容器名。Pod 只有一个容器时可以留空
	Container string
	// TailLines 只要最后多少行。0 表示全部（配合 LimitBytes 才不至于拉爆）
	TailLines int
	// SinceSeconds 只要最近多少秒内的
	SinceSeconds int
	// Previous 看上一个已退出容器的日志——排查 CrashLoopBackOff 就靠这个
	Previous bool
	// Timestamps 每行前面带上时间戳
	Timestamps bool
	// LimitBytes 本地读取上限，超出即截断并告知上层
	LimitBytes int64
}

// logQuery 把条件拍成 URL 参数，单独拆出来是为了可单测
func logQuery(opts LogOptions) url.Values {
	query := url.Values{}
	if opts.Container != "" {
		query.Set("container", opts.Container)
	}
	if opts.TailLines > 0 {
		query.Set("tailLines", strconv.Itoa(opts.TailLines))
	}
	if opts.SinceSeconds > 0 {
		query.Set("sinceSeconds", strconv.Itoa(opts.SinceSeconds))
	}
	if opts.Previous {
		query.Set("previous", "true")
	}
	if opts.Timestamps {
		query.Set("timestamps", "true")
	}
	return query
}

// PodLogs 取某个容器的日志。第二个返回值表示是否因为超过 LimitBytes 被截断。
//
// 不做 follow（流式）：一次取一段、由界面按需刷新，链路上少一个长连接，
// 也不用在服务端维护订阅状态。
func (c *Client) PodLogs(ctx context.Context, namespace, pod string, opts LogOptions) (string, bool, error) {
	if opts.LimitBytes <= 0 {
		opts.LimitBytes = 1 << 20
	}
	path := namespacedPath("/api/v1", namespace, "pods") + "/" + url.PathEscape(pod) + "/log"
	body, truncated, err := c.getRaw(ctx, path, logQuery(opts), opts.LimitBytes)
	if err != nil {
		return "", false, err
	}
	return string(body), truncated, nil
}

// ---------- 事件 ----------

type Event struct {
	Namespace string    `json:"namespace"`
	Type      string    `json:"type"` // Normal | Warning
	Reason    string    `json:"reason"`
	Object    string    `json:"object"`
	Message   string    `json:"message"`
	Count     int       `json:"count"`
	LastSeen  time.Time `json:"lastSeen"`
}

// Events 取事件。onlyWarning=true 时只要 Warning，排查时先看这些。
func (c *Client) Events(ctx context.Context, namespace string, onlyWarning bool, limit int) ([]Event, error) {
	query := url.Values{}
	if limit > 0 {
		query.Set("limit", strconv.Itoa(limit))
	}
	if onlyWarning {
		query.Set("fieldSelector", "type=Warning")
	}

	var raw struct {
		Items []struct {
			Metadata struct {
				Namespace string `json:"namespace"`
			} `json:"metadata"`
			Type           string `json:"type"`
			Reason         string `json:"reason"`
			Message        string `json:"message"`
			Count          int    `json:"count"`
			LastTimestamp  string `json:"lastTimestamp"`
			FirstTimestamp string `json:"firstTimestamp"`
			InvolvedObject struct {
				Kind string `json:"kind"`
				Name string `json:"name"`
			} `json:"involvedObject"`
		} `json:"items"`
	}
	if err := c.get(ctx, namespacedPath("/api/v1", namespace, "events"), query, &raw); err != nil {
		return nil, err
	}

	list := make([]Event, 0, len(raw.Items))
	for _, item := range raw.Items {
		event := Event{
			Namespace: item.Metadata.Namespace, Type: item.Type, Reason: item.Reason,
			Message: item.Message, Count: item.Count,
			Object: item.InvolvedObject.Kind + "/" + item.InvolvedObject.Name,
		}
		// lastTimestamp 在部分事件上为空，退回 firstTimestamp
		stamp := item.LastTimestamp
		if stamp == "" {
			stamp = item.FirstTimestamp
		}
		if stamp != "" {
			if parsed, err := time.Parse(time.RFC3339, stamp); err == nil {
				event.LastSeen = parsed
			}
		}
		list = append(list, event)
	}
	return list, nil
}
