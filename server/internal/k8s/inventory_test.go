package k8s

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestNodeConditionProblemDirection 这是整个文件最容易写错的一处：
// Ready 应当为 True，而 DiskPressure / MemoryPressure / PIDPressure /
// NetworkUnavailable 应当为 **False** —— 方向是反的。
// 写错的表现是把健康节点报成异常（或者更糟：把有磁盘压力的节点报成正常）。
func TestNodeConditionProblemDirection(t *testing.T) {
	cases := []struct {
		condType string
		status   string
		problem  bool
	}{
		{"Ready", "True", false},
		{"Ready", "False", true},
		{"Ready", "Unknown", true},
		{"DiskPressure", "False", false},
		{"DiskPressure", "True", true},
		{"MemoryPressure", "True", true},
		{"PIDPressure", "False", false},
		{"NetworkUnavailable", "True", true},
	}
	for _, tc := range cases {
		if got := nodeConditionProblem(tc.condType, tc.status); got != tc.problem {
			t.Errorf("%s=%s 判为 problem=%v, want %v", tc.condType, tc.status, got, tc.problem)
		}
	}
}

func TestAtoiSafe(t *testing.T) {
	cases := map[string]int{"110": 110, "0": 0, "": 0, "110k": 0, "abc": 0, "-1": 0}
	for in, want := range cases {
		if got := atoiSafe(in); got != want {
			t.Errorf("atoiSafe(%q) = %d, want %d", in, got, want)
		}
	}
}

const nodeListPayload = `{"items":[
  {"metadata":{"name":"cp-1","creationTimestamp":"2026-01-01T00:00:00Z",
    "labels":{"node-role.kubernetes.io/control-plane":""}},
   "spec":{"taints":[{"key":"node-role.kubernetes.io/control-plane","effect":"NoSchedule"}]},
   "status":{"capacity":{"cpu":"4","memory":"8Gi","pods":"110"},
     "allocatable":{"cpu":"3800m","memory":"7Gi","pods":"110"},
     "conditions":[{"type":"MemoryPressure","status":"False"},
                   {"type":"DiskPressure","status":"False"},
                   {"type":"Ready","status":"True"}],
     "addresses":[{"type":"InternalIP","address":"10.0.0.1"}],
     "nodeInfo":{"kubeletVersion":"v1.31.2","osImage":"Ubuntu 24.04",
       "kernelVersion":"6.8.0","containerRuntimeVersion":"containerd://1.7.2"}}},
  {"metadata":{"name":"worker-sick"},
   "spec":{"unschedulable":true,
     "taints":[{"key":"disk","value":"bad","effect":"NoExecute"}]},
   "status":{"capacity":{"cpu":"8","memory":"16Gi","pods":"110"},
     "allocatable":{"cpu":"7800m","memory":"15Gi","pods":"4"},
     "conditions":[{"type":"DiskPressure","status":"True","reason":"KubeletHasDiskPressure",
                    "message":"kubelet has disk pressure"},
                   {"type":"Ready","status":"False","reason":"KubeletNotReady"}],
     "addresses":[{"type":"InternalIP","address":"10.0.0.2"}],
     "nodeInfo":{"kubeletVersion":"v1.30.1"}}},
  {"metadata":{"name":"worker-ok"},
   "spec":{},
   "status":{"capacity":{"cpu":"8","memory":"16Gi","pods":"110"},
     "allocatable":{"cpu":"7800m","memory":"15Gi","pods":"110"},
     "conditions":[{"type":"Ready","status":"True"}],
     "addresses":[{"type":"InternalIP","address":"10.0.0.3"}],
     "nodeInfo":{"kubeletVersion":"v1.31.2"}}}
]}`

// podsForNodePayload cp-1 上 1 个活的 + 1 个已结束（不该计入），
// worker-sick 上 4 个活的（正好到 allocatable.pods=4 的上限）
const podsForNodePayload = `{"items":[
  {"metadata":{"name":"a","namespace":"kube-system"},"spec":{"nodeName":"cp-1"},"status":{"phase":"Running"}},
  {"metadata":{"name":"done","namespace":"kube-system"},"spec":{"nodeName":"cp-1"},"status":{"phase":"Succeeded"}},
  {"metadata":{"name":"failed","namespace":"ops"},"spec":{"nodeName":"cp-1"},"status":{"phase":"Failed"}},
  {"metadata":{"name":"b","namespace":"ops"},"spec":{"nodeName":"worker-sick"},"status":{"phase":"Running"}},
  {"metadata":{"name":"c","namespace":"ops"},"spec":{"nodeName":"worker-sick"},"status":{"phase":"Running"}},
  {"metadata":{"name":"d","namespace":"ops"},"spec":{"nodeName":"worker-sick"},"status":{"phase":"Pending"}},
  {"metadata":{"name":"e","namespace":"ops"},"spec":{"nodeName":"worker-sick"},"status":{"phase":"Running"}},
  {"metadata":{"name":"unscheduled","namespace":"ops"},"spec":{},"status":{"phase":"Pending"}}
]}`

func nodeStubServer(t *testing.T, podsStatus int) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/nodes", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, nodeListPayload)
	})
	mux.HandleFunc("/api/v1/pods", func(w http.ResponseWriter, r *http.Request) {
		if podsStatus != http.StatusOK {
			w.WriteHeader(podsStatus)
			fmt.Fprint(w, `{"message":"pods is forbidden"}`)
			return
		}
		fmt.Fprint(w, podsForNodePayload)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestNodeInventoryList(t *testing.T) {
	srv := nodeStubServer(t, http.StatusOK)
	client := newTestClient(t, srv.URL)

	inv, err := client.NodeInventoryList(context.Background())
	if err != nil {
		t.Fatalf("读节点失败: %v", err)
	}
	if len(inv.Nodes) != 3 {
		t.Fatalf("节点数 = %d", len(inv.Nodes))
	}
	if !inv.PodCounted {
		t.Fatal("读到了 Pod 列表，PodCounted 应当为 true")
	}
	if inv.Ready != 2 || inv.NotReady != 1 {
		t.Errorf("就绪统计 = %d/%d", inv.Ready, inv.NotReady)
	}
	if inv.Cordoned != 1 || inv.Tainted != 2 {
		t.Errorf("cordon/污点统计 = %d/%d", inv.Cordoned, inv.Tainted)
	}
	// 版本偏斜：升级做到一半停下来是很常见的现场
	if strings.Join(inv.Versions, ",") != "v1.30.1,v1.31.2" {
		t.Errorf("版本清单 = %v", inv.Versions)
	}

	// 有问题的排最前
	if inv.Nodes[0].Name != "worker-sick" {
		t.Errorf("有问题的节点应当排最前, 实际第一个是 %q", inv.Nodes[0].Name)
	}

	byName := map[string]NodeDetail{}
	for _, item := range inv.Nodes {
		byName[item.Name] = item
	}

	cp := byName["cp-1"]
	if !cp.Ready {
		t.Error("cp-1 应当是 Ready")
	}
	if strings.Join(cp.Roles, ",") != "control-plane" {
		t.Errorf("角色 = %v", cp.Roles)
	}
	// 容量与可分配必须分开：调度看 allocatable
	if cp.CapacityCPU != "4" || cp.AllocCPU != "3800m" {
		t.Errorf("容量/可分配没分开: %q / %q", cp.CapacityCPU, cp.AllocCPU)
	}
	if cp.Kernel != "6.8.0" || !strings.Contains(cp.Runtime, "containerd") {
		t.Errorf("内核/运行时没解出来: %+v", cp)
	}
	// Pod 计数：Succeeded 与 Failed 不计入（资源早还回去了）
	if cp.PodCount != 1 {
		t.Errorf("cp-1 上活着的 Pod 数 = %d, want 1（Succeeded/Failed 不算）", cp.PodCount)
	}
	// control-plane 的 NoSchedule 污点很常见，不该被报成「问题」
	if len(cp.Problems) != 0 {
		t.Errorf("健康的 control-plane 不该有问题: %v", cp.Problems)
	}

	sick := byName["worker-sick"]
	joined := strings.Join(sick.Problems, " | ")
	for _, want := range []string{
		"不是 Ready：KubeletNotReady",
		"磁盘压力（KubeletHasDiskPressure）",
		"NoExecute 污点",
		"已 cordon",
		"Pod 数已达上限",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("问题描述缺少 %q: %s", want, joined)
		}
	}
	if sick.PodCount != 4 || sick.PodCapacity != 4 {
		t.Errorf("Pod 数/上限 = %d/%d", sick.PodCount, sick.PodCapacity)
	}
	// conditions 全部留下来（带 reason 与 message），不是只留异常的
	if len(sick.Conditions) != 2 {
		t.Errorf("conditions 数 = %d", len(sick.Conditions))
	}
	for _, cond := range sick.Conditions {
		if !cond.Problem {
			t.Errorf("%s=%s 应当被判为 problem", cond.Type, cond.Status)
		}
	}

	ok := byName["worker-ok"]
	if len(ok.Problems) != 0 {
		t.Errorf("健康节点不该有问题: %v", ok.Problems)
	}
	if ok.PodCount != 0 {
		t.Errorf("没有 Pod 的节点应当是 0, got %d", ok.PodCount)
	}
}

// TestNodeInventoryWithoutPodAccess 没权限列 Pod 时不该让整页失败，
// 而且必须把 PodCounted 置 false —— 界面显示「未知」而不是 0
// （0 会被当成「这台机器上没有 Pod」，那是完全相反的结论）。
func TestNodeInventoryWithoutPodAccess(t *testing.T) {
	srv := nodeStubServer(t, http.StatusForbidden)
	client := newTestClient(t, srv.URL)

	inv, err := client.NodeInventoryList(context.Background())
	if err != nil {
		t.Fatalf("Pod 读不到不该让整页失败: %v", err)
	}
	if inv.PodCounted {
		t.Fatal("读不到 Pod 列表时 PodCounted 必须是 false")
	}
	if len(inv.Nodes) != 3 {
		t.Errorf("节点照样要列出来, got %d", len(inv.Nodes))
	}
	// 拿不到 Pod 数时不能凭空报「Pod 数已达上限」
	for _, node := range inv.Nodes {
		for _, problem := range node.Problems {
			if strings.Contains(problem, "Pod 数") {
				t.Errorf("没统计到 Pod 时不该给出 Pod 数相关结论: %q", problem)
			}
		}
	}
}

// ---------- 命名空间 ----------

const namespaceListPayload = `{"items":[
  {"metadata":{"name":"kube-system","creationTimestamp":"2026-01-01T00:00:00Z"},
   "status":{"phase":"Active"}},
  {"metadata":{"name":"ops","labels":{"team":"sre"}},"status":{"phase":"Active"}},
  {"metadata":{"name":"stuck"},
   "spec":{"finalizers":["kubernetes"]},
   "status":{"phase":"Terminating",
     "conditions":[{"type":"NamespaceFinalizersRemaining","status":"True",
       "message":"Some content in the namespace has finalizers remaining"}]}},
  {"metadata":{"name":"empty"},"status":{"phase":"Active"}}
]}`

const podsForNSPayload = `{"items":[
  {"metadata":{"namespace":"ops"},"status":{"phase":"Running"}},
  {"metadata":{"namespace":"ops"},"status":{"phase":"Pending"}},
  {"metadata":{"namespace":"ops"},"status":{"phase":"Failed"}},
  {"metadata":{"namespace":"kube-system"},"status":{"phase":"Running"}}
]}`

func namespaceStubServer(t *testing.T, quotaStatus int) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/namespaces", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, namespaceListPayload)
	})
	mux.HandleFunc("/api/v1/pods", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, podsForNSPayload)
	})
	mux.HandleFunc("/api/v1/resourcequotas", func(w http.ResponseWriter, r *http.Request) {
		if quotaStatus != http.StatusOK {
			w.WriteHeader(quotaStatus)
			fmt.Fprint(w, `{"message":"resourcequotas is forbidden"}`)
			return
		}
		fmt.Fprint(w, `{"items":[
      {"metadata":{"name":"compute","namespace":"kube-system"},
       "status":{"hard":{"requests.cpu":"4","limits.memory":"8Gi"},
                 "used":{"requests.cpu":"1500m","limits.memory":"2Gi","pods":"9"}}}
    ]}`)
	})
	mux.HandleFunc("/api/v1/limitranges", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"items":[{"metadata":{"name":"defaults","namespace":"kube-system"}}]}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestNamespaceInventoryList(t *testing.T) {
	srv := namespaceStubServer(t, http.StatusOK)
	client := newTestClient(t, srv.URL)

	inv, err := client.NamespaceInventoryList(context.Background())
	if err != nil {
		t.Fatalf("读命名空间失败: %v", err)
	}
	if inv.Total != 4 {
		t.Fatalf("命名空间数 = %d", inv.Total)
	}
	if !inv.PodCounted || !inv.QuotaRead {
		t.Fatalf("都读到了，两个标记都该为 true: %+v", inv)
	}
	if inv.Terminating != 1 {
		t.Errorf("Terminating 数 = %d", inv.Terminating)
	}
	// ops / stuck / empty 三个都没有配额与 LimitRange
	if inv.NoQuota != 3 {
		t.Errorf("无配额约束的命名空间数 = %d, want 3", inv.NoQuota)
	}

	byName := map[string]NamespaceDetail{}
	for _, item := range inv.Namespaces {
		byName[item.Name] = item
	}

	// 卡在 Terminating：finalizer 必须一起列出来 —— 删不掉几乎总是因为它
	stuck := byName["stuck"]
	if !stuck.Terminating {
		t.Error("stuck 应当被判为 Terminating")
	}
	joined := strings.Join(stuck.Problems, " | ")
	if !strings.Contains(joined, "卡在 Terminating") || !strings.Contains(joined, "kubernetes") {
		t.Errorf("Terminating 的结论要带上 finalizer: %s", joined)
	}
	if len(stuck.Conditions) != 1 || !strings.Contains(stuck.Conditions[0], "finalizers remaining") {
		t.Errorf("API Server 的原话要带出来: %v", stuck.Conditions)
	}

	ops := byName["ops"]
	if ops.PodTotal != 3 || ops.PodRunning != 1 || ops.PodPending != 1 || ops.PodFailed != 1 {
		t.Errorf("Pod 分布不对: %+v", ops)
	}
	opsProblems := strings.Join(ops.Problems, " | ")
	for _, want := range []string{"Failed", "Pending", "requests/limits"} {
		if !strings.Contains(opsProblems, want) {
			t.Errorf("ops 的结论缺少 %q: %s", want, opsProblems)
		}
	}
	if ops.Labels["team"] != "sre" {
		t.Errorf("标签没带出来: %v", ops.Labels)
	}

	// 有配额的命名空间：只列 hard 里设了上限的项
	sys := byName["kube-system"]
	if len(sys.Quotas) != 1 {
		t.Fatalf("配额数 = %d", len(sys.Quotas))
	}
	if !sys.HasLimitRange {
		t.Error("有 LimitRange 应当被识别")
	}
	quotaText := strings.Join(sys.Quotas[0].Items, " | ")
	if !strings.Contains(quotaText, "requests.cpu: 1500m / 4") {
		t.Errorf("配额渲染不对: %s", quotaText)
	}
	// used 里有 pods 但 hard 里没设，不该出现 —— 那是噪音
	if strings.Contains(quotaText, "pods") {
		t.Errorf("hard 里没设上限的项不该列出来: %s", quotaText)
	}
	if len(sys.Problems) != 0 {
		t.Errorf("有配额、Pod 都正常的命名空间不该有问题: %v", sys.Problems)
	}

	// 空命名空间：没 Pod 就不该提示「没配 requests/limits」
	empty := byName["empty"]
	if len(empty.Problems) != 0 {
		t.Errorf("空命名空间不该有问题: %v", empty.Problems)
	}
}

// TestNamespaceInventoryWithoutQuotaAccess 读不到配额对象时，
// **绝对不能**给出「没配配额」的结论 —— 没权限看和没配是两件事。
func TestNamespaceInventoryWithoutQuotaAccess(t *testing.T) {
	srv := namespaceStubServer(t, http.StatusForbidden)
	client := newTestClient(t, srv.URL)

	inv, err := client.NamespaceInventoryList(context.Background())
	if err != nil {
		t.Fatalf("配额读不到不该让整页失败: %v", err)
	}
	if inv.QuotaRead {
		t.Fatal("读不到配额时 QuotaRead 必须是 false")
	}
	if inv.NoQuota != 0 {
		t.Errorf("读不到配额时不该统计「无配额」, got %d", inv.NoQuota)
	}
	for _, ns := range inv.Namespaces {
		for _, problem := range ns.Problems {
			if strings.Contains(problem, "ResourceQuota") {
				t.Errorf("%s: 读不到配额时不该断言没配配额: %q", ns.Name, problem)
			}
		}
	}
}

func TestDescribeQuotaFallsBackToSpec(t *testing.T) {
	// status 还没算出来时退回 spec.hard，至少能看出设了什么上限
	obj := map[string]any{
		"spec":   map[string]any{"hard": map[string]any{"pods": "10"}},
		"status": map[string]any{},
	}
	items := describeQuota(obj)
	if len(items) != 1 || !strings.Contains(items[0], "pods: ? / 10") {
		t.Errorf("退回 spec 的渲染不对: %v", items)
	}
	// 完全没有 status 时返回 nil，不 panic
	if got := describeQuota(map[string]any{}); got != nil {
		t.Errorf("没有 status 时应当返回 nil, got %v", got)
	}
}
