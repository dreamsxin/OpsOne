package k8s

import (
	"encoding/json"
	"strings"
	"testing"
)

// 一个真实形状的 Pod：两个 init 容器（一个已完成、一个 spec 里有但 status 还没有）、
// 一个正在 CrashLoop 的主容器、一个正常运行的主容器
const podFixture = `{
  "metadata": {
    "name": "web-7d9f", "namespace": "ops",
    "ownerReferences": [
      {"kind": "ReplicaSet", "name": "web-7d9f", "controller": true},
      {"kind": "Something", "name": "other"}
    ]
  },
  "spec": {
    "nodeName": "node-1", "serviceAccountName": "web-sa",
    "nodeSelector": {"disktype": "ssd", "zone": "a"},
    "initContainers": [
      {"name": "init-db", "image": "busybox:1.36",
       "volumeMounts": [{"name": "conf", "mountPath": "/etc/app", "readOnly": true}]},
      {"name": "init-pending", "image": "busybox:1.36"}
    ],
    "containers": [
      {"name": "app", "image": "app:1.2.3",
       "ports": [{"containerPort": 8080}, {"containerPort": 9090, "protocol": "UDP"}],
       "resources": {"requests": {"cpu": "100m", "memory": "128Mi"}, "limits": {"memory": "256Mi"}},
       "livenessProbe": {}, "readinessProbe": {},
       "volumeMounts": [{"name": "conf", "mountPath": "/etc/app"}, {"name": "data", "mountPath": "/data"}]},
      {"name": "sidecar", "image": "envoy:1.29"}
    ],
    "volumes": [
      {"name": "conf", "configMap": {"name": "app-conf"}},
      {"name": "tls", "secret": {"secretName": "app-tls"}},
      {"name": "data", "persistentVolumeClaim": {"claimName": "app-data"}},
      {"name": "tmp", "emptyDir": {}},
      {"name": "weird", "flexVolume": {"driver": "x"}}
    ]
  },
  "status": {
    "phase": "Running", "podIP": "10.42.0.9", "hostIP": "192.168.1.5",
    "qosClass": "Burstable", "startTime": "2026-09-21T08:00:00Z",
    "conditions": [
      {"type": "Ready", "status": "False", "reason": "ContainersNotReady",
       "message": "containers with unready status: [app]", "lastTransitionTime": "2026-09-21T09:10:00Z"}
    ],
    "initContainerStatuses": [
      {"name": "init-db", "ready": true, "restartCount": 0,
       "state": {"terminated": {"exitCode": 0, "reason": "Completed", "startedAt": "2026-09-21T08:00:05Z"}}}
    ],
    "containerStatuses": [
      {"name": "app", "ready": false, "restartCount": 7,
       "state": {"waiting": {"reason": "CrashLoopBackOff", "message": "back-off 5m0s"}},
       "lastState": {"terminated": {"exitCode": 137, "reason": "OOMKilled", "finishedAt": "2026-09-21T09:09:00Z"}}},
      {"name": "sidecar", "ready": true, "restartCount": 1,
       "state": {"running": {"startedAt": "2026-09-21T08:01:00Z"}}}
    ]
  }
}`

func parsePod(t *testing.T, raw string) map[string]any {
	t.Helper()
	var obj map[string]any
	if err := json.Unmarshal([]byte(raw), &obj); err != nil {
		t.Fatalf("测试数据不合法: %v", err)
	}
	return obj
}

func TestDescribePodBasics(t *testing.T) {
	d := DescribePod(parsePod(t, podFixture))

	if d.Name != "web-7d9f" || d.Namespace != "ops" || d.Phase != "Running" {
		t.Fatalf("基本字段不对: %+v", d)
	}
	if d.NodeName != "node-1" || d.PodIP != "10.42.0.9" || d.QoSClass != "Burstable" {
		t.Fatalf("调度与网络字段不对: %+v", d)
	}
	// 归属只认 controller: true 的那个
	if d.Owner != "ReplicaSet/web-7d9f" {
		t.Fatalf("归属控制器不对: %q", d.Owner)
	}
	// 就绪数只数主容器：init 容器完成了不代表 Pod 能服务
	if d.ReadyCount != 1 || d.TotalCount != 2 {
		t.Fatalf("就绪数应为 1/2，实际 %d/%d", d.ReadyCount, d.TotalCount)
	}
	// 重启次数是所有容器之和
	if d.Restarts != 8 {
		t.Fatalf("重启总数应为 8，实际 %d", d.Restarts)
	}
	if d.NodeSelector != "disktype=ssd,zone=a" {
		t.Fatalf("节点选择器不对: %q", d.NodeSelector)
	}
	if d.ServiceAccount != "web-sa" {
		t.Fatalf("服务账号不对: %q", d.ServiceAccount)
	}
}

// spec 里有但 status 里还没有的容器必须照样出现，否则「少了一个容器」会变成新疑问
func TestDescribePodKeepsContainersMissingFromStatus(t *testing.T) {
	d := DescribePod(parsePod(t, podFixture))
	names := make([]string, 0, len(d.Containers))
	for _, c := range d.Containers {
		names = append(names, c.Name)
	}
	want := []string{"init-db", "init-pending", "app", "sidecar"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("容器列表应为 %v（init 在前），实际 %v", want, names)
	}
	pending := d.Containers[1]
	if pending.State != "unknown" || pending.Ready {
		t.Fatalf("status 里没有的容器应标 unknown: %+v", pending)
	}
	if !d.Containers[0].Init || d.Containers[2].Init {
		t.Fatalf("init 标记不对: %+v / %+v", d.Containers[0], d.Containers[2])
	}
	// 日志容器列表要含 init 容器：启动失败时它们的日志才是关键
	if len(d.LogContainers) != 4 || d.LogContainers[0] != "init-db" {
		t.Fatalf("可取日志的容器列表不对: %v", d.LogContainers)
	}
}

// CrashLoop 的关键信息在 lastState 里：当前状态只会说「在等下一次重启」
func TestDescribePodSurfacesLastTerminated(t *testing.T) {
	d := DescribePod(parsePod(t, podFixture))
	app := d.Containers[2]
	if app.State != "waiting" || app.Reason != "CrashLoopBackOff" {
		t.Fatalf("当前状态不对: %+v", app)
	}
	if !strings.Contains(app.LastTerminated, "OOMKilled") || !strings.Contains(app.LastTerminated, "137") {
		t.Fatalf("上次退出信息应含 OOMKilled 与退出码: %q", app.LastTerminated)
	}
	if app.Restarts != 7 {
		t.Fatalf("重启次数不对: %d", app.Restarts)
	}
	// 正常退出的 init 容器退出码是 0，不能被当成「没有这个信息」
	if d.Containers[0].State != "terminated" || d.Containers[0].ExitCode != 0 {
		t.Fatalf("已完成的 init 容器: %+v", d.Containers[0])
	}
	// 没有 status 的容器退出码是 -1（缺失），与 0 区分开
	if d.Containers[1].ExitCode != -1 {
		t.Fatalf("缺失的退出码应为 -1，实际 %d", d.Containers[1].ExitCode)
	}
}

func TestDescribePodContainerDetails(t *testing.T) {
	d := DescribePod(parsePod(t, podFixture))
	app := d.Containers[2]
	if app.Ports != "8080/TCP 9090/UDP" {
		t.Fatalf("端口不对: %q", app.Ports)
	}
	if app.Requests != "cpu=100m memory=128Mi" || app.Limits != "memory=256Mi" {
		t.Fatalf("资源不对: %q / %q", app.Requests, app.Limits)
	}
	if app.Probes != "存活 就绪" {
		t.Fatalf("探针不对: %q", app.Probes)
	}
	if len(app.Mounts) != 2 || !strings.Contains(app.Mounts[0], "/etc/app") {
		t.Fatalf("挂载不对: %v", app.Mounts)
	}
	// 没配 limits / 探针的容器要留空，「没配」本身就是要看出来的事实
	side := d.Containers[3]
	if side.Limits != "" || side.Requests != "" || side.Probes != "" {
		t.Fatalf("没配的项应留空: %+v", side)
	}
	if !strings.Contains(d.Containers[0].Mounts[0], "只读") {
		t.Fatalf("只读挂载应标出来: %v", d.Containers[0].Mounts)
	}
}

func TestDescribePodVolumesAndConditions(t *testing.T) {
	d := DescribePod(parsePod(t, podFixture))
	want := map[string][2]string{
		"conf":  {"ConfigMap", "app-conf"},
		"tls":   {"Secret", "app-tls"},
		"data":  {"PVC", "app-data"},
		"tmp":   {"EmptyDir", ""},
		"weird": {"其他", "flexVolume"},
	}
	if len(d.Volumes) != len(want) {
		t.Fatalf("卷数量不对: %d", len(d.Volumes))
	}
	for _, vol := range d.Volumes {
		expect, ok := want[vol.Name]
		if !ok {
			t.Fatalf("多出来的卷: %+v", vol)
		}
		if vol.Type != expect[0] || vol.Source != expect[1] {
			t.Fatalf("%s 解析不对: %+v，期望 %v", vol.Name, vol, expect)
		}
	}
	if len(d.Conditions) != 1 || d.Conditions[0].Reason != "ContainersNotReady" {
		t.Fatalf("条件不对: %+v", d.Conditions)
	}
}

// 残缺对象不能让它崩：读不到就留空，界面显示「—」
func TestDescribePodTolerantOfEmptyObject(t *testing.T) {
	d := DescribePod(map[string]any{})
	if d.Name != "" || d.TotalCount != 0 || len(d.Containers) != 0 {
		t.Fatalf("空对象应返回空详情: %+v", d)
	}
	if d.QoSClass != "未知" {
		t.Fatalf("QoS 取不到应说未知，而不是猜一个: %q", d.QoSClass)
	}
	d2 := DescribePod(parsePod(t, `{"metadata":{"name":"x"},"spec":{"containers":[{"name":"c"}]}}`))
	if len(d2.Containers) != 1 || d2.Containers[0].State != "unknown" || d2.TotalCount != 1 {
		t.Fatalf("没有 status 的 Pod: %+v", d2)
	}
}
