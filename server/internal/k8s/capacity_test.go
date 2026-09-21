package k8s

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseCPUUnits(t *testing.T) {
	cases := map[string]float64{
		"":           0,
		"1":          1,
		"1.5":        1.5,
		"100m":       0.1,
		"2500m":      2.5,
		"123456789n": 0.123456789, // metrics API 用纳核
		"500u":       0.0005,
		"bad":        0,
	}
	for raw, want := range cases {
		if got := ParseCPU(raw); got < want-1e-9 || got > want+1e-9 {
			t.Fatalf("ParseCPU(%q)=%v，期望 %v", raw, got, want)
		}
	}
}

func TestParseMemoryUnits(t *testing.T) {
	cases := map[string]int64{
		"":        0,
		"1024":    1024,
		"1Ki":     1024,
		"2Mi":     2 << 20,
		"1Gi":     1 << 30,
		"1M":      1000000, // 十进制后缀比 Mi 小，不能混
		"1k":      1000,
		"1.5Gi":   int64(1.5 * (1 << 30)),
		"garbage": 0,
	}
	for raw, want := range cases {
		if got := ParseMemory(raw); got != want {
			t.Fatalf("ParseMemory(%q)=%d，期望 %d", raw, got, want)
		}
	}
}

func TestFormatHelpers(t *testing.T) {
	if got := FormatCPU(0.25); got != "250m" {
		t.Fatalf("1 核以下应用毫核: %q", got)
	}
	if got := FormatCPU(2); got != "2" {
		t.Fatalf("整核不该带小数: %q", got)
	}
	if got := FormatMemory(1536 * 1024 * 1024); got != "1.5Gi" {
		t.Fatalf("内存格式不对: %q", got)
	}
	if got := FormatMemory(0); got != "0" {
		t.Fatalf("0 应为 0: %q", got)
	}
}

// 调度规则：普通容器 requests 求和，与「单个 init 容器最大值」取较大者。
// 算错会让统计值偏大，进而让人以为节点比实际更满。
func TestPodResourceTotalsFollowsSchedulerRule(t *testing.T) {
	var spec map[string]any
	raw := `{
      "containers": [
        {"resources": {"requests": {"cpu": "100m", "memory": "128Mi"}, "limits": {"cpu": "200m", "memory": "256Mi"}}},
        {"resources": {"requests": {"cpu": "50m", "memory": "64Mi"}, "limits": {"cpu": "100m", "memory": "128Mi"}}}
      ],
      "initContainers": [
        {"resources": {"requests": {"cpu": "500m", "memory": "512Mi"}}},
        {"resources": {"requests": {"cpu": "300m", "memory": "64Mi"}}}
      ]
    }`
	if err := json.Unmarshal([]byte(raw), &spec); err != nil {
		t.Fatal(err)
	}
	cpuReq, cpuLim, memReq, memLim, noLimit := podResourceTotals(spec)
	// 普通容器 150m 对 init 最大 500m → 取 500m
	if cpuReq < 0.4999 || cpuReq > 0.5001 {
		t.Fatalf("CPU requests 应取 500m，实际 %v", cpuReq)
	}
	if memReq != 512<<20 {
		t.Fatalf("内存 requests 应取 512Mi，实际 %d", memReq)
	}
	// limits：普通容器合计 300m / 384Mi，init 没配 limits
	if cpuLim < 0.2999 || cpuLim > 0.3001 || memLim != 384<<20 {
		t.Fatalf("limits 不对: %v / %d", cpuLim, memLim)
	}
	if noLimit {
		t.Fatal("两个普通容器都配了 limits，不该标成缺 limits")
	}
}

func TestPodResourceTotalsFlagsMissingLimits(t *testing.T) {
	var spec map[string]any
	_ = json.Unmarshal([]byte(`{"containers":[
      {"resources":{"requests":{"cpu":"100m","memory":"128Mi"},"limits":{"cpu":"200m"}}}]}`), &spec)
	_, _, _, _, noLimit := podResourceTotals(spec)
	if !noLimit {
		t.Fatal("只配了 cpu limit、没配 memory limit 也该算缺 limits")
	}

	var empty map[string]any
	_ = json.Unmarshal([]byte(`{"containers":[{}]}`), &empty)
	cpuReq, cpuLim, memReq, memLim, missing := podResourceTotals(empty)
	if cpuReq != 0 || cpuLim != 0 || memReq != 0 || memLim != 0 || !missing {
		t.Fatalf("什么都没配的容器: %v %v %d %d %v", cpuReq, cpuLim, memReq, memLim, missing)
	}
}

func TestPodCountsForScheduling(t *testing.T) {
	for phase, want := range map[string]bool{
		"Running": true, "Pending": true, "Unknown": true,
		"Succeeded": false, "Failed": false,
	} {
		got := podCountsForScheduling(map[string]any{"phase": phase})
		if got != want {
			t.Fatalf("%s 是否占资源: %v，期望 %v", phase, got, want)
		}
	}
}

func TestQuotaPercentByResourceType(t *testing.T) {
	if got := quotaPercent("requests.cpu", "500m", "2"); got != 25 {
		t.Fatalf("CPU 配额占比不对: %v", got)
	}
	if got := quotaPercent("limits.memory", "512Mi", "2Gi"); got != 25 {
		t.Fatalf("内存配额占比不对: %v", got)
	}
	if got := quotaPercent("pods", "3", "10"); got != 30 {
		t.Fatalf("计数型配额占比不对: %v", got)
	}
	// 分母为 0 不能出 NaN，否则前端显示 NaN%
	if got := quotaPercent("pods", "1", "0"); got != 0 {
		t.Fatalf("分母为 0 应返回 0，实际 %v", got)
	}
}

// 整体汇总：节点比例分母用 allocatable、终止的 Pod 不计、metrics 缺失要降级说明
func TestCapacityAggregation(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/nodes", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"items":[{
          "metadata":{"name":"node-1","labels":{"node-role.kubernetes.io/control-plane":""}},
          "spec":{},
          "status":{"capacity":{"cpu":"4","memory":"8Gi","pods":"110"},
                    "allocatable":{"cpu":"4","memory":"7Gi","pods":"110"},
                    "conditions":[{"type":"Ready","status":"True"}]}}]}`)
	})
	mux.HandleFunc("/api/v1/pods", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"items":[
          {"metadata":{"name":"live","namespace":"ops"},
           "spec":{"nodeName":"node-1","containers":[
             {"resources":{"requests":{"cpu":"1","memory":"1Gi"},"limits":{"cpu":"2","memory":"2Gi"}}}]},
           "status":{"phase":"Running"}},
          {"metadata":{"name":"nolimit","namespace":"ops"},
           "spec":{"nodeName":"node-1","containers":[
             {"resources":{"requests":{"cpu":"1","memory":"1Gi"}}}]},
           "status":{"phase":"Running"}},
          {"metadata":{"name":"done","namespace":"ops"},
           "spec":{"nodeName":"node-1","containers":[
             {"resources":{"requests":{"cpu":"2","memory":"4Gi"}}}]},
           "status":{"phase":"Succeeded"}}]}`)
	})
	mux.HandleFunc("/api/v1/resourcequotas", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"items":[{"metadata":{"name":"q","namespace":"quota-only"},
          "status":{"hard":{"pods":"10","requests.cpu":"2"},"used":{"pods":"2","requests.cpu":"500m"}}}]}`)
	})
	// metrics 不存在：这里返回 404，模拟没装 metrics-server 的集群
	mux.HandleFunc("/apis/metrics.k8s.io/v1beta1/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"message":"the server could not find the requested resource"}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	client := newTestClient(t, srv.URL)

	result, err := client.Capacity(context.Background(), "")
	if err != nil {
		t.Fatalf("汇总失败: %v", err)
	}
	if result.PodsCounted != 2 || result.PodsSkipped != 1 {
		t.Fatalf("终止的 Pod 不该计入: counted=%d skipped=%d", result.PodsCounted, result.PodsSkipped)
	}
	if len(result.Nodes) != 1 {
		t.Fatalf("节点数不对: %d", len(result.Nodes))
	}
	node := result.Nodes[0]
	if node.CPURequests != "2" || node.CPULimits != "2" {
		t.Fatalf("节点已分配不对: requests=%q limits=%q", node.CPURequests, node.CPULimits)
	}
	// 2 核 / 4 核可分配 = 50%
	if node.CPURequestPct != 50 {
		t.Fatalf("CPU 已分配占比应为 50（分母用 allocatable），实际 %v", node.CPURequestPct)
	}
	// 2Gi / 7Gi 可分配 ≈ 28.6%（不是 8Gi 容量的 25%）
	if node.MemRequestPct < 28 || node.MemRequestPct > 29 {
		t.Fatalf("内存已分配占比应按 allocatable 算，实际 %v", node.MemRequestPct)
	}
	if node.PodCapacity != 110 || node.PodCount != 2 {
		t.Fatalf("Pod 数不对: %d/%d", node.PodCount, node.PodCapacity)
	}
	if node.HasUsage {
		t.Fatal("没有 metrics 时不该声称有实际用量")
	}
	if result.MetricsAvailable {
		t.Fatal("metrics 404 时应标为不可用")
	}
	if !strings.Contains(result.MetricsNote, "metrics-server") || !strings.Contains(result.MetricsNote, "不用 0 充数") {
		t.Fatalf("降级说明要讲清原因: %q", result.MetricsNote)
	}

	byNS := map[string]NamespaceCapacity{}
	for _, item := range result.Namespaces {
		byNS[item.Namespace] = item
	}
	ops := byNS["ops"]
	if ops.PodCount != 2 || ops.NoLimitPods != 1 {
		t.Fatalf("命名空间统计不对: %+v", ops)
	}
	// 配额落在没有 Pod 的命名空间上也要出现，否则配额看不全
	quotaOnly, ok := byNS["quota-only"]
	if !ok || len(quotaOnly.Quotas) != 2 {
		t.Fatalf("只有配额没有 Pod 的命名空间应出现: %+v", byNS)
	}
	for _, item := range quotaOnly.Quotas {
		if item.Resource == "pods" && item.Percent != 20 {
			t.Fatalf("pods 配额占比不对: %+v", item)
		}
		if item.Resource == "requests.cpu" && item.Percent != 25 {
			t.Fatalf("cpu 配额占比不对: %+v", item)
		}
	}
}

// metrics 在的时候，用量要落到节点与命名空间上，并且标 HasUsage
func TestCapacityWithMetrics(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/nodes", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"items":[{"metadata":{"name":"node-1"},"spec":{},
          "status":{"capacity":{"cpu":"4","memory":"8Gi","pods":"110"},
                    "allocatable":{"cpu":"4","memory":"8Gi","pods":"110"},
                    "conditions":[{"type":"Ready","status":"True"}]}}]}`)
	})
	mux.HandleFunc("/api/v1/pods", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"items":[{"metadata":{"name":"web","namespace":"ops"},
          "spec":{"nodeName":"node-1","containers":[{"resources":{"requests":{"cpu":"1","memory":"1Gi"}}}]},
          "status":{"phase":"Running"}}]}`)
	})
	mux.HandleFunc("/api/v1/resourcequotas", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"items":[]}`)
	})
	mux.HandleFunc("/apis/metrics.k8s.io/v1beta1/nodes", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"items":[{"metadata":{"name":"node-1"},
          "usage":{"cpu":"2000000000n","memory":"2Gi"}}]}`)
	})
	mux.HandleFunc("/apis/metrics.k8s.io/v1beta1/pods", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"items":[{"metadata":{"name":"web","namespace":"ops"},
          "containers":[{"usage":{"cpu":"250m","memory":"300Mi"}},{"usage":{"cpu":"250m","memory":"200Mi"}}]}]}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	client := newTestClient(t, srv.URL)

	result, err := client.Capacity(context.Background(), "")
	if err != nil {
		t.Fatalf("汇总失败: %v", err)
	}
	if !result.MetricsAvailable || !strings.Contains(result.MetricsNote, "采样") {
		t.Fatalf("metrics 可用时要说明它是采样值: %v / %q", result.MetricsAvailable, result.MetricsNote)
	}
	node := result.Nodes[0]
	if !node.HasUsage || node.CPUUsage != "2" || node.MemUsage != "2Gi" {
		t.Fatalf("节点用量不对: %+v", node)
	}
	// 实际用量 2 核 / 4 核 = 50%，与已分配 25% 是两个不同的数
	if node.CPUUsagePct != 50 || node.CPURequestPct != 25 {
		t.Fatalf("用量与已分配要各算各的: usage=%v requests=%v", node.CPUUsagePct, node.CPURequestPct)
	}
	ns := result.Namespaces[0]
	// 同一个 Pod 的多个容器用量要相加
	if !ns.HasUsage || ns.CPUUsage != "500m" || ns.MemUsage != "500Mi" {
		t.Fatalf("命名空间用量不对: %+v", ns)
	}
}
