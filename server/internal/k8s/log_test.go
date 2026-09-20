package k8s

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLogQuery(t *testing.T) {
	// 零值不该产生任何参数：交给 API Server 的默认行为
	if got := logQuery(LogOptions{}).Encode(); got != "" {
		t.Fatalf("零值不该带参数，实际 %q", got)
	}

	got := logQuery(LogOptions{
		Container: "web", TailLines: 200, SinceSeconds: 600,
		Previous: true, Timestamps: true,
	})
	want := map[string]string{
		"container": "web", "tailLines": "200", "sinceSeconds": "600",
		"previous": "true", "timestamps": "true",
	}
	for key, value := range want {
		if got.Get(key) != value {
			t.Fatalf("%s 应为 %s，实际 %q（完整参数 %s）", key, value, got.Get(key), got.Encode())
		}
	}

	// previous=false 不能写成 previous=false 传上去：那和不传是两回事更容易出歧义
	if logQuery(LogOptions{Previous: false}).Has("previous") {
		t.Fatal("previous 为假时不该带这个参数")
	}
}

func TestPodLogs(t *testing.T) {
	var captured string
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/namespaces/ops/pods/web-1/log", func(w http.ResponseWriter, r *http.Request) {
		captured = r.URL.RawQuery
		fmt.Fprint(w, "line-1\nline-2\nline-3\n")
	})
	mux.HandleFunc("/api/v1/namespaces/ops/pods/fresh/log", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"kind":"Status","message":"previous terminated container \"web\" in pod \"fresh\" not found","code":400}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	client := newTestClient(t, srv.URL)

	text, truncated, err := client.PodLogs(context.Background(), "ops", "web-1",
		LogOptions{Container: "web", TailLines: 100})
	if err != nil {
		t.Fatalf("读日志失败: %v", err)
	}
	if truncated {
		t.Fatal("没超上限不该标记截断")
	}
	if text != "line-1\nline-2\nline-3\n" {
		t.Fatalf("日志内容不对: %q", text)
	}
	if !strings.Contains(captured, "container=web") || !strings.Contains(captured, "tailLines=100") {
		t.Fatalf("参数没带上: %s", captured)
	}

	// 超出上限要截断并如实告知
	text, truncated, err = client.PodLogs(context.Background(), "ops", "web-1",
		LogOptions{LimitBytes: 8})
	if err != nil {
		t.Fatalf("读日志失败: %v", err)
	}
	if !truncated || len(text) != 8 {
		t.Fatalf("应截断到 8 字节，实际 %d / truncated=%v", len(text), truncated)
	}

	// 刚起的容器没有上一个容器日志，要把 API Server 的原话带出来
	if _, _, err := client.PodLogs(context.Background(), "ops", "fresh",
		LogOptions{Previous: true}); err == nil {
		t.Fatal("没有上一个容器时应该报错")
	} else if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("错误信息应带上 API 原文，实际 %v", err)
	}
}

func TestPodsExposeContainers(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/namespaces/ops/pods", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"items":[
      {"metadata":{"name":"multi","namespace":"ops"},
       "spec":{"nodeName":"n1",
         "containers":[{"name":"web","image":"nginx"},{"name":"sidecar","image":"envoy"}],
         "initContainers":[{"name":"wait-db"}]},
       "status":{"phase":"Running",
         "containerStatuses":[{"name":"web","ready":true,"restartCount":0,"state":{}},
                              {"name":"sidecar","ready":false,"restartCount":3,
                               "state":{"waiting":{"reason":"CrashLoopBackOff"}}}]}},
      {"metadata":{"name":"single","namespace":"ops"},
       "spec":{"containers":[{"name":"only","image":"busybox"}]},
       "status":{"phase":"Running","containerStatuses":[{"name":"only","ready":true,"restartCount":0,"state":{}}]}}
    ]}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	client := newTestClient(t, srv.URL)

	pods, err := client.Pods(context.Background(), "ops")
	if err != nil || len(pods) != 2 {
		t.Fatalf("读 Pod 失败: %v / %d", err, len(pods))
	}
	if len(pods[0].Containers) != 2 || pods[0].Containers[1] != "sidecar" {
		t.Fatalf("容器名没解析出来: %+v", pods[0].Containers)
	}
	if len(pods[0].InitContainers) != 1 || pods[0].InitContainers[0] != "wait-db" {
		t.Fatalf("init 容器没解析出来: %+v", pods[0].InitContainers)
	}
	// 有容器重启过，界面才有必要给「上一个容器」的入口
	if !pods[0].Restarted {
		t.Fatal("有容器 restartCount>0 时应标记重启过")
	}
	if pods[1].Restarted {
		t.Fatal("没重启过的 Pod 不该标记")
	}
	if len(pods[1].InitContainers) != 0 {
		t.Fatal("没有 init 容器时应是空数组而不是 nil")
	}
}
