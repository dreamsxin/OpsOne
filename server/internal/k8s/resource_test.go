package k8s

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLookupKindWhitelist(t *testing.T) {
	if _, ok := LookupKind("deployment"); !ok {
		t.Fatal("kind 匹配应大小写不敏感")
	}
	kind, ok := LookupKind("Ingress")
	if !ok || kind.APIVersion != "networking.k8s.io/v1" {
		t.Fatalf("Ingress 白名单条目不对: %+v", kind)
	}
	// 白名单之外的一律拒绝，尤其是这几个
	for _, name := range []string{"Secret", "Namespace", "ClusterRole", "Pod", ""} {
		if _, ok := LookupKind(name); ok {
			t.Fatalf("%s 不该在白名单里", name)
		}
	}
	for _, item := range SupportedKinds() {
		if item.Kind == "DaemonSet" && item.Scalable {
			t.Fatal("DaemonSet 没有 replicas，不该标成可改副本数")
		}
	}
}

func TestParseManifest(t *testing.T) {
	manifest, err := ParseManifest(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: web
  namespace: ops
spec:
  replicas: 2
`)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if manifest.Kind != "Deployment" || manifest.Name != "web" || manifest.Namespace != "ops" {
		t.Fatalf("身份字段解析不对: %+v", manifest)
	}

	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"空内容", "   \n", "不能为空"},
		{"缺 kind", "apiVersion: v1\nmetadata:\n  name: a\n", "apiVersion 或 kind"},
		{"缺名字", "apiVersion: v1\nkind: ConfigMap\nmetadata: {}\n", "metadata.name"},
		{"非法 yaml", "apiVersion: v1\nkind: [", "解析失败"},
		{
			"多段 yaml",
			"apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: a\n---\napiVersion: v1\nkind: Service\nmetadata:\n  name: b\n",
			"一次只能提交一个对象",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseManifest(tc.raw); err == nil {
				t.Fatal("应该被拒绝，却通过了")
			} else if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("错误信息应包含 %q，实际 %v", tc.want, err)
			}
		})
	}

	// 开头写了 --- 是合法的单段写法，不该被当成两段
	if _, err := ParseManifest("---\napiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: a\n"); err != nil {
		t.Fatalf("开头的 --- 不该被拒绝: %v", err)
	}
}

func TestToEditableYAML(t *testing.T) {
	var obj map[string]any
	raw := `{
      "apiVersion":"apps/v1","kind":"Deployment",
      "metadata":{"name":"web","namespace":"ops","uid":"abc","resourceVersion":"1234",
        "generation":7,"creationTimestamp":"2026-09-20T10:00:00Z","managedFields":[{"manager":"kubectl"}],
        "annotations":{"kubectl.kubernetes.io/last-applied-configuration":"{}",
                       "deployment.kubernetes.io/revision":"3","team":"sre"}},
      "spec":{"replicas":3},
      "status":{"readyReplicas":3}
    }`
	if err := json.Unmarshal([]byte(raw), &obj); err != nil {
		t.Fatalf("准备数据失败: %v", err)
	}

	out, err := ToEditableYAML(obj)
	if err != nil {
		t.Fatalf("转换失败: %v", err)
	}
	text := string(out)
	for _, banned := range []string{
		"status", "uid", "resourceVersion", "generation", "managedFields",
		"creationTimestamp", "last-applied-configuration", "deployment.kubernetes.io/revision",
	} {
		if strings.Contains(text, banned) {
			t.Fatalf("清理后不该还有 %s:\n%s", banned, text)
		}
	}
	// 自己写的注解要留着，副本数要保持整数形态
	if !strings.Contains(text, "team: sre") {
		t.Fatalf("业务注解被误删:\n%s", text)
	}
	if !strings.Contains(text, "replicas: 3") {
		t.Fatalf("数字不该被写成浮点:\n%s", text)
	}

	// 原对象不能被改动：界面上可能还要用它渲染状态
	meta := obj["metadata"].(map[string]any)
	if _, ok := meta["uid"]; !ok {
		t.Fatal("清理不该在原对象上做")
	}
	if _, ok := obj["status"]; !ok {
		t.Fatal("清理不该在原对象上做")
	}
}

func TestResourceSummary(t *testing.T) {
	cases := []struct {
		kind string
		raw  string
		want string
	}{
		{"Deployment", `{"spec":{"replicas":3},"status":{"readyReplicas":1}}`, "副本 1/3"},
		{"DaemonSet", `{"status":{"desiredNumberScheduled":2,"numberReady":2}}`, "就绪 2/2"},
		{"CronJob", `{"spec":{"schedule":"*/5 * * * *","suspend":true}}`, "*/5 * * * *（已暂停）"},
		{"Service", `{"spec":{"type":"ClusterIP","clusterIP":"10.43.0.1"}}`, "ClusterIP 10.43.0.1"},
		{"ConfigMap", `{"data":{"a":"1","b":"2"}}`, "2 个键"},
		{"Ingress", `{"spec":{"rules":[{"host":"a.example"},{"host":"b.example"},{}]}}`, "a.example, b.example"},
		// 字段缺失也要给出结果，不能 panic
		{"Deployment", `{}`, "副本 0/0"},
		{"ConfigMap", `{}`, "0 个键"},
	}
	for _, tc := range cases {
		var obj map[string]any
		if err := json.Unmarshal([]byte(tc.raw), &obj); err != nil {
			t.Fatalf("准备数据失败: %v", err)
		}
		if got := resourceSummary(tc.kind, obj); got != tc.want {
			t.Fatalf("%s 摘要应为 %q，实际 %q", tc.kind, tc.want, got)
		}
	}
}

// fakeWriteServer 记录收到的写请求，用来断言 URL 参数与 content-type
type capturedRequest struct {
	method      string
	path        string
	query       string
	contentType string
	body        string
}

func fakeWriteServer(t *testing.T, captured *[]capturedRequest) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	mux.HandleFunc("/apis/apps/v1/namespaces/ops/deployments/web", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		*captured = append(*captured, capturedRequest{
			method: r.Method, path: r.URL.Path, query: r.URL.RawQuery,
			contentType: r.Header.Get("Content-Type"), body: string(body),
		})
		fmt.Fprint(w, `{"metadata":{"name":"web","namespace":"ops"},"spec":{"replicas":4}}`)
	})
	mux.HandleFunc("/apis/apps/v1/namespaces/ops/deployments/web/scale", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		*captured = append(*captured, capturedRequest{
			method: r.Method, path: r.URL.Path, query: r.URL.RawQuery,
			contentType: r.Header.Get("Content-Type"), body: string(body),
		})
		if r.Method == http.MethodGet {
			fmt.Fprint(w, `{"spec":{"replicas":2}}`)
			return
		}
		fmt.Fprint(w, `{"spec":{"replicas":5}}`)
	})
	mux.HandleFunc("/apis/apps/v1/namespaces/ops/deployments/locked", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		fmt.Fprint(w, `{"kind":"Status","message":"Apply failed with 1 conflict: conflict with \"kubectl\"","code":409}`)
	})
	mux.HandleFunc("/apis/apps/v1/namespaces/ops/configmaps/cm", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"metadata":{"name":"cm","namespace":"ops"},"data":{"a":"1"}}`)
	})
	return httptest.NewServer(mux)
}

func TestApplySendsServerSideApply(t *testing.T) {
	var captured []capturedRequest
	srv := fakeWriteServer(t, &captured)
	defer srv.Close()
	client := newTestClient(t, srv.URL)
	kind, _ := LookupKind("Deployment")
	manifest := "apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: web\n  namespace: ops\n"

	if _, err := client.Apply(context.Background(), kind, "ops", "web",
		[]byte(manifest), ApplyOptions{DryRun: true}); err != nil {
		t.Fatalf("预检失败: %v", err)
	}
	got := captured[0]
	if got.method != http.MethodPatch {
		t.Fatalf("apply 应走 PATCH，实际 %s", got.method)
	}
	if got.contentType != "application/apply-patch+yaml" {
		t.Fatalf("content-type 不对: %s", got.contentType)
	}
	if !strings.Contains(got.query, "dryRun=All") {
		t.Fatalf("dryRun 应转成 dryRun=All，实际 %s", got.query)
	}
	if !strings.Contains(got.query, "fieldManager=opsone") {
		t.Fatalf("应带上 fieldManager，实际 %s", got.query)
	}
	if strings.Contains(got.query, "force") {
		t.Fatalf("没勾强制接管时不该带 force，实际 %s", got.query)
	}
	if got.body != manifest {
		t.Fatalf("提交的应是原始 YAML，实际 %q", got.body)
	}

	// 真正落盘时不带 dryRun，勾了强制才带 force
	obj, err := client.Apply(context.Background(), kind, "ops", "web",
		[]byte(manifest), ApplyOptions{Force: true})
	if err != nil {
		t.Fatalf("apply 失败: %v", err)
	}
	got = captured[1]
	if strings.Contains(got.query, "dryRun") {
		t.Fatalf("非预检不该带 dryRun，实际 %s", got.query)
	}
	if !strings.Contains(got.query, "force=true") {
		t.Fatalf("强制接管应带 force=true，实际 %s", got.query)
	}
	spec, _ := obj["spec"].(map[string]any)
	if spec["replicas"].(float64) != 4 {
		t.Fatalf("应回传 API Server 返回的对象: %+v", obj)
	}
}

func TestApplySurfacesConflict(t *testing.T) {
	var captured []capturedRequest
	srv := fakeWriteServer(t, &captured)
	defer srv.Close()
	client := newTestClient(t, srv.URL)
	kind, _ := LookupKind("Deployment")

	_, err := client.Apply(context.Background(), kind, "ops", "locked",
		[]byte("apiVersion: apps/v1\nkind: Deployment\n"), ApplyOptions{})
	if err == nil {
		t.Fatal("409 应该报错")
	}
	if !strings.Contains(err.Error(), "conflict") {
		t.Fatalf("字段冲突的原文应带出来，实际 %v", err)
	}
}

func TestScaleReadsBeforeAndAfter(t *testing.T) {
	var captured []capturedRequest
	srv := fakeWriteServer(t, &captured)
	defer srv.Close()
	client := newTestClient(t, srv.URL)
	kind, _ := LookupKind("Deployment")

	result, err := client.Scale(context.Background(), kind, "ops", "web", 5, false)
	if err != nil {
		t.Fatalf("改副本数失败: %v", err)
	}
	if result.Previous != 2 || result.Current != 5 {
		t.Fatalf("应记录改前改后，实际 %+v", result)
	}
	if captured[0].method != http.MethodGet {
		t.Fatalf("应先读一次原值，实际 %s", captured[0].method)
	}
	patch := captured[1]
	if patch.method != http.MethodPatch || patch.contentType != "application/merge-patch+json" {
		t.Fatalf("scale 请求形状不对: %+v", patch)
	}
	if patch.body != `{"spec":{"replicas":5}}` {
		t.Fatalf("patch 内容不对: %s", patch.body)
	}
	if !strings.HasSuffix(patch.path, "/scale") {
		t.Fatalf("应打在 /scale 子资源上，实际 %s", patch.path)
	}

	// 不可缩放的类型在客户端就挡住，不发请求
	daemon, _ := LookupKind("DaemonSet")
	before := len(captured)
	if _, err := client.Scale(context.Background(), daemon, "ops", "agent", 1, false); err == nil {
		t.Fatal("DaemonSet 改副本数应被拒绝")
	}
	if len(captured) != before {
		t.Fatal("被拒绝的请求不该发出去")
	}
}

func TestListObjectsMapsItems(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/namespaces/ops/configmaps", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"items":[
      {"metadata":{"name":"cm-a","namespace":"ops","creationTimestamp":"2026-09-20T10:00:00Z"},
       "data":{"a":"1","b":"2"}},
      {"metadata":{"name":"cm-b","namespace":"ops"}}
    ]}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	client := newTestClient(t, srv.URL)
	kind, _ := LookupKind("ConfigMap")

	items, err := client.ListObjects(context.Background(), kind, "ops")
	if err != nil || len(items) != 2 {
		t.Fatalf("列 ConfigMap 失败: %v / %d", err, len(items))
	}
	if items[0].Name != "cm-a" || items[0].Summary != "2 个键" || items[0].CreatedAt.IsZero() {
		t.Fatalf("字段映射不对: %+v", items[0])
	}
	// 没有创建时间戳也要正常出行
	if items[1].Name != "cm-b" || !items[1].CreatedAt.IsZero() {
		t.Fatalf("缺时间戳的行应照常返回: %+v", items[1])
	}
}
