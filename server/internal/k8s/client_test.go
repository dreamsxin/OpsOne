package k8s

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

// 一份最小可用 kubeconfig，形状照 k3s 生成的那份
func sampleKubeconfig(server string) string {
	return fmt.Sprintf(`apiVersion: v1
clusters:
- cluster:
    certificate-authority-data: %s
    server: %s
  name: default
contexts:
- context:
    cluster: default
    user: default
  name: default
- context:
    cluster: default
    namespace: ops
    user: default
  name: second
current-context: default
kind: Config
users:
- name: default
  user:
    client-certificate-data: %s
    client-key-data: %s
`, b64("CA-PEM"), server, b64("CRT-PEM"), b64("KEY-PEM"))
}

func TestParseKubeconfig(t *testing.T) {
	raw := sampleKubeconfig("https://127.0.0.1:6443")

	cfg, err := ParseKubeconfig(raw, "")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if cfg.ContextName != "default" {
		t.Fatalf("留空应取 current-context，实际 %s", cfg.ContextName)
	}
	if cfg.Server != "https://127.0.0.1:6443" {
		t.Fatalf("server 解析错误: %s", cfg.Server)
	}
	if string(cfg.CAData) != "CA-PEM" || string(cfg.CertData) != "CRT-PEM" || string(cfg.KeyData) != "KEY-PEM" {
		t.Fatal("base64 凭据没有正确解开")
	}

	// 指定上下文时要跟着取它的 namespace
	cfg, err = ParseKubeconfig(raw, "second")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if cfg.Namespace != "ops" {
		t.Fatalf("应取到上下文里的 namespace，实际 %q", cfg.Namespace)
	}

	names, current, err := Contexts(raw)
	if err != nil {
		t.Fatalf("列上下文失败: %v", err)
	}
	if len(names) != 2 || current != "default" {
		t.Fatalf("上下文清单不对: %v / %s", names, current)
	}
}

func TestParseKubeconfigRejects(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"非法 yaml", "clusters: [", "解析失败"},
		{"没有 clusters", "apiVersion: v1\nclusters: []\ncontexts: []\n", "没有 clusters"},
		{
			"路径式凭据",
			`apiVersion: v1
clusters:
- cluster: {server: https://1.2.3.4}
  name: c
contexts:
- context: {cluster: c, user: u}
  name: ctx
users:
- name: u
  user: {client-certificate: /etc/k8s/a.crt}
`,
			"嵌入式凭据",
		},
		{
			"server 缺协议",
			`apiVersion: v1
clusters:
- cluster: {server: 1.2.3.4}
  name: c
contexts:
- context: {cluster: c, user: u}
  name: ctx
users:
- name: u
  user: {token: abc}
`,
			"http",
		},
		{
			"证书不是 base64",
			`apiVersion: v1
clusters:
- cluster: {server: https://1.2.3.4, certificate-authority-data: "!!!not base64!!!"}
  name: c
contexts:
- context: {cluster: c, user: u}
  name: ctx
users:
- name: u
  user: {token: abc}
`,
			"base64",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseKubeconfig(tc.raw, "")
			if err == nil {
				t.Fatal("应该被拒绝，却通过了")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("错误信息应包含 %q，实际 %v", tc.want, err)
			}
		})
	}

	// 指定了不存在的上下文
	if _, err := ParseKubeconfig(sampleKubeconfig("https://x"), "nope"); err == nil {
		t.Fatal("不存在的上下文应被拒绝")
	}
}

// fakeAPIServer 用真实 k8s 响应的形状回固定数据
func fakeAPIServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/version", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"gitVersion":"v1.31.5+k3s1","platform":"linux/amd64"}`)
	})
	mux.HandleFunc("/api/v1/nodes", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"items":[
      {"metadata":{"name":"node-a","labels":{"node-role.kubernetes.io/master":"true","node-role.kubernetes.io/control-plane":"true"}},
       "spec":{"unschedulable":true},
       "status":{"capacity":{"cpu":"12","memory":"16292484Ki","pods":"110"},
         "conditions":[{"type":"MemoryPressure","status":"False"},{"type":"Ready","status":"True"}],
         "addresses":[{"type":"Hostname","address":"node-a"},{"type":"InternalIP","address":"10.0.0.5"}],
         "nodeInfo":{"kubeletVersion":"v1.31.5+k3s1","osImage":"Ubuntu 26.04 LTS"}}},
      {"metadata":{"name":"node-b","labels":{}},"spec":{},
       "status":{"capacity":{},"conditions":[{"type":"Ready","status":"False"}],"addresses":[],"nodeInfo":{}}}
    ]}`)
	})
	mux.HandleFunc("/apis/apps/v1/deployments", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"items":[
      {"metadata":{"name":"web","namespace":"default"},
       "spec":{"replicas":3,"template":{"spec":{"containers":[{"image":"nginx:1.27"},{"image":"busybox"}]}}},
       "status":{"readyReplicas":3,"updatedReplicas":3,"availableReplicas":3}},
      {"metadata":{"name":"broken","namespace":"default"},
       "spec":{"replicas":2,"template":{"spec":{"containers":[{"image":"nope:1"}]}}},
       "status":{"readyReplicas":0}}
    ]}`)
	})
	mux.HandleFunc("/apis/apps/v1/namespaces/default/daemonsets", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"items":[
      {"metadata":{"name":"agent","namespace":"default"},
       "spec":{"template":{"spec":{"containers":[{"image":"agent:2"}]}}},
       "status":{"desiredNumberScheduled":2,"numberReady":1,"updatedNumberScheduled":2}}
    ]}`)
	})
	mux.HandleFunc("/api/v1/pods", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"items":[
      {"metadata":{"name":"web-1","namespace":"default"},
       "spec":{"nodeName":"node-a","containers":[{"image":"nginx:1.27"}]},
       "status":{"phase":"Running","podIP":"10.42.0.9",
         "containerStatuses":[{"name":"c1","ready":true,"restartCount":2,"state":{}},
                              {"name":"c2","ready":false,"restartCount":3,
                               "state":{"waiting":{"reason":"CrashLoopBackOff"}}}]}}
    ]}`)
	})
	mux.HandleFunc("/api/v1/events", func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("fieldSelector"); got != "type=Warning" {
			t.Errorf("onlyWarning 应转成 fieldSelector=type=Warning，实际 %q", got)
		}
		fmt.Fprint(w, `{"items":[
      {"metadata":{"namespace":"default"},"type":"Warning","reason":"FailedCreatePodSandBox",
       "message":"boom","count":4,"lastTimestamp":"2026-09-20T10:00:00Z",
       "involvedObject":{"kind":"Pod","name":"web-1"}},
      {"metadata":{"namespace":"default"},"type":"Warning","reason":"NoLastStamp",
       "message":"only first","count":1,"firstTimestamp":"2026-09-20T09:00:00Z",
       "involvedObject":{"kind":"Pod","name":"web-2"}}
    ]}`)
	})
	mux.HandleFunc("/apis/apps/v1/statefulsets", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"kind":"Status","message":"statefulsets is forbidden","code":403}`)
	})
	return httptest.NewServer(mux)
}

func newTestClient(t *testing.T, server string) *Client {
	t.Helper()
	cfg, err := ParseKubeconfig(fmt.Sprintf(`apiVersion: v1
clusters:
- cluster: {server: %s}
  name: c
contexts:
- context: {cluster: c, user: u}
  name: ctx
current-context: ctx
users:
- name: u
  user: {token: fake-token}
`, server), "")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	client, err := NewClient(cfg, 5*time.Second)
	if err != nil {
		t.Fatalf("建客户端失败: %v", err)
	}
	return client
}

func TestClientReads(t *testing.T) {
	srv := fakeAPIServer(t)
	defer srv.Close()
	client := newTestClient(t, srv.URL)
	ctx := context.Background()

	version, err := client.Version(ctx)
	if err != nil || version.GitVersion != "v1.31.5+k3s1" {
		t.Fatalf("版本读取失败: %v / %+v", err, version)
	}

	nodes, err := client.Nodes(ctx)
	if err != nil || len(nodes) != 2 {
		t.Fatalf("节点读取失败: %v / %d", err, len(nodes))
	}
	if !nodes[0].Ready {
		t.Fatal("Ready=True 应解析成就绪")
	}
	if nodes[1].Ready {
		t.Fatal("Ready=False 应解析成未就绪")
	}
	if !nodes[0].Unschedulable {
		t.Fatal("cordon 状态没解析出来")
	}
	if len(nodes[0].Roles) != 2 {
		t.Fatalf("角色应从标签里解析出 2 个，实际 %v", nodes[0].Roles)
	}
	if nodes[0].InternalIP != "10.0.0.5" || nodes[0].CPU != "12" {
		t.Fatalf("节点字段映射不对: %+v", nodes[0])
	}

	deps, err := client.Deployments(ctx, "")
	if err != nil || len(deps) != 2 {
		t.Fatalf("Deployment 读取失败: %v / %d", err, len(deps))
	}
	if !deps[0].Healthy || deps[0].Desired != 3 || len(deps[0].Images) != 2 {
		t.Fatalf("健康的 Deployment 判定不对: %+v", deps[0])
	}
	if deps[1].Healthy {
		t.Fatal("0/2 就绪不该判成健康")
	}

	// DaemonSet 没有 replicas，期望值取 desiredNumberScheduled
	daemons, err := client.DaemonSets(ctx, "default")
	if err != nil || len(daemons) != 1 {
		t.Fatalf("DaemonSet 读取失败: %v / %d", err, len(daemons))
	}
	if daemons[0].Desired != 2 || daemons[0].Ready != 1 || daemons[0].Healthy {
		t.Fatalf("DaemonSet 字段映射不对: %+v", daemons[0])
	}

	// 权限不足时要把 API Server 自己的说法带出来
	if _, err := client.StatefulSets(ctx, ""); err == nil {
		t.Fatal("403 应该报错")
	} else if !strings.Contains(err.Error(), "forbidden") {
		t.Fatalf("错误信息应带上 API 的原文，实际 %v", err)
	}

	pods, err := client.Pods(ctx, "")
	if err != nil || len(pods) != 1 {
		t.Fatalf("Pod 读取失败: %v / %d", err, len(pods))
	}
	if pods[0].Ready != "1/2" {
		t.Fatalf("就绪数应为 1/2，实际 %s", pods[0].Ready)
	}
	if pods[0].Restarts != 5 {
		t.Fatalf("重启次数应为各容器之和 5，实际 %d", pods[0].Restarts)
	}
	if !strings.Contains(pods[0].ContainerMsg, "CrashLoopBackOff") {
		t.Fatalf("容器异常原因没带出来: %q", pods[0].ContainerMsg)
	}

	events, err := client.Events(ctx, "", true, 10)
	if err != nil || len(events) != 2 {
		t.Fatalf("事件读取失败: %v / %d", err, len(events))
	}
	if events[0].Object != "Pod/web-1" || events[0].Count != 4 {
		t.Fatalf("事件字段映射不对: %+v", events[0])
	}
	// lastTimestamp 缺失时退回 firstTimestamp，不能是零值
	if events[1].LastSeen.IsZero() {
		t.Fatal("lastTimestamp 缺失时应退回 firstTimestamp")
	}
}

func TestClientReportsUnreachable(t *testing.T) {
	srv := fakeAPIServer(t)
	srv.Close() // 立刻关掉，模拟集群不可达
	client := newTestClient(t, srv.URL)
	if _, err := client.Version(context.Background()); err == nil {
		t.Fatal("集群不可达时应该报错")
	}
}
