package k8s

import (
	"fmt"
	"sort"
	"strings"
)

// Pod 结构化详情。
//
// 为什么不让界面直接读 YAML：排查时真正要回答的是「哪个容器挂了、为什么挂、
// 上次退出码是多少、挂了几次、挂载的是哪个 ConfigMap」。这些散在 spec 与 status
// 的不同角落，让人在 YAML 里翻等于把解析工作推给值班的人。
//
// 原则是**只算不编**：字段缺失就留空并在界面上显示「—」，不拿默认值冒充事实
// （比如 QoS 取不到就说未知，而不是猜一个 Burstable）。

// PodContainer 一个容器的状态。Init 为 true 时是 initContainer。
type PodContainer struct {
	Name  string `json:"name"`
	Image string `json:"image"`
	Init  bool   `json:"init"`
	Ready bool   `json:"ready"`
	// Restarts 该容器的重启次数
	Restarts int `json:"restarts"`
	// State running | waiting | terminated | unknown（status 里还没有这个容器时）
	State string `json:"state"`
	// Reason / Message 来自当前状态，waiting 时是 CrashLoopBackOff / ImagePullBackOff 这类
	Reason  string `json:"reason"`
	Message string `json:"message"`
	// ExitCode 仅 terminated 有；-1 表示没有这个信息（0 是正常退出，不能拿它当缺失）
	ExitCode int `json:"exitCode"`
	// LastTerminated 上一次退出的原因与退出码，CrashLoop 时最关键的一行
	LastTerminated string `json:"lastTerminated"`
	StartedAt      string `json:"startedAt"`
	Ports          string `json:"ports"`
	Requests       string `json:"requests"`
	Limits         string `json:"limits"`
	// Probes 配了哪些探针。没配就绪探针的容器「Ready」意义很弱，这点要能看出来
	Probes string `json:"probes"`
	// Mounts 挂载点：卷名 → 容器内路径
	Mounts []string `json:"mounts"`
}

// PodCondition Pod 级别的条件
type PodCondition struct {
	Type           string `json:"type"`
	Status         string `json:"status"`
	Reason         string `json:"reason"`
	Message        string `json:"message"`
	LastTransition string `json:"lastTransition"`
}

// PodVolume 卷及其来源。Secret 只给名字，不碰内容。
type PodVolume struct {
	Name   string `json:"name"`
	Type   string `json:"type"`
	Source string `json:"source"`
}

// PodDetail 结构化的 Pod 详情
type PodDetail struct {
	Name           string         `json:"name"`
	Namespace      string         `json:"namespace"`
	Phase          string         `json:"phase"`
	Reason         string         `json:"reason"`
	Message        string         `json:"message"`
	NodeName       string         `json:"nodeName"`
	PodIP          string         `json:"podIp"`
	HostIP         string         `json:"hostIp"`
	QoSClass       string         `json:"qosClass"`
	ServiceAccount string         `json:"serviceAccount"`
	StartedAt      string         `json:"startedAt"`
	Owner          string         `json:"owner"`
	Restarts       int            `json:"restarts"`
	ReadyCount     int            `json:"readyCount"`
	TotalCount     int            `json:"totalCount"`
	Containers     []PodContainer `json:"containers"`
	Conditions     []PodCondition `json:"conditions"`
	Volumes        []PodVolume    `json:"volumes"`
	NodeSelector   string         `json:"nodeSelector"`
	// LogContainers 可以取日志的容器名（含 init 容器，它们的日志常常是启动失败的关键）
	LogContainers []string `json:"logContainers"`
}

// DescribePod 从对象原文算出结构化详情。纯函数，不发请求。
func DescribePod(obj map[string]any) PodDetail {
	meta, _ := obj["metadata"].(map[string]any)
	spec, _ := obj["spec"].(map[string]any)
	status, _ := obj["status"].(map[string]any)

	detail := PodDetail{}
	if meta != nil {
		detail.Name, _ = meta["name"].(string)
		detail.Namespace, _ = meta["namespace"].(string)
		detail.Owner = ownerLabel(meta)
	}
	if spec != nil {
		detail.NodeName, _ = spec["nodeName"].(string)
		detail.ServiceAccount, _ = spec["serviceAccountName"].(string)
		detail.NodeSelector = joinSelector(stringMap(spec["nodeSelector"]))
	}
	if status != nil {
		detail.Phase, _ = status["phase"].(string)
		detail.Reason, _ = status["reason"].(string)
		detail.Message, _ = status["message"].(string)
		detail.PodIP, _ = status["podIP"].(string)
		detail.HostIP, _ = status["hostIP"].(string)
		detail.QoSClass, _ = status["qosClass"].(string)
		detail.StartedAt, _ = status["startTime"].(string)
	}
	if detail.QoSClass == "" {
		// QoS 是 API Server 算出来写在 status 里的，平台不自己推算：
		// 推算规则（requests/limits 是否齐全、是否相等）很容易实现得和集群不一致
		detail.QoSClass = "未知"
	}

	statuses := containerStatusIndex(status, "containerStatuses")
	initStatuses := containerStatusIndex(status, "initContainerStatuses")

	detail.Containers = append(detail.Containers,
		describeContainers(spec, "initContainers", initStatuses, true)...)
	main := describeContainers(spec, "containers", statuses, false)
	detail.Containers = append(detail.Containers, main...)

	for _, item := range main {
		detail.TotalCount++
		if item.Ready {
			detail.ReadyCount++
		}
	}
	for _, item := range detail.Containers {
		detail.Restarts += item.Restarts
		detail.LogContainers = append(detail.LogContainers, item.Name)
	}

	detail.Conditions = describeConditions(status)
	detail.Volumes = describeVolumes(spec)
	return detail
}

// ownerLabel 归属控制器，形如 ReplicaSet/coredns-abc。
// 只取标了 controller: true 的那个；Pod 上可能挂多个 ownerReference。
func ownerLabel(meta map[string]any) string {
	refs, _ := meta["ownerReferences"].([]any)
	fallback := ""
	for _, entry := range refs {
		ref, _ := entry.(map[string]any)
		kind, _ := ref["kind"].(string)
		name, _ := ref["name"].(string)
		if kind == "" || name == "" {
			continue
		}
		label := kind + "/" + name
		if flag, ok := ref["controller"].(bool); ok && flag {
			return label
		}
		if fallback == "" {
			fallback = label
		}
	}
	return fallback
}

func containerStatusIndex(status map[string]any, key string) map[string]map[string]any {
	out := map[string]map[string]any{}
	list, _ := status[key].([]any)
	for _, entry := range list {
		item, _ := entry.(map[string]any)
		if name, _ := item["name"].(string); name != "" {
			out[name] = item
		}
	}
	return out
}

// describeContainers 把 spec 里的容器定义与 status 里的运行状态并起来。
//
// 以 spec 为准遍历：status 里没有的容器（还没被调度 / 还没拉起来）也必须出现在列表里，
// 否则「为什么少了一个容器」会变成新的疑问。
func describeContainers(spec map[string]any, key string,
	statuses map[string]map[string]any, isInit bool) []PodContainer {
	list, _ := spec[key].([]any)
	out := make([]PodContainer, 0, len(list))
	for _, entry := range list {
		item, _ := entry.(map[string]any)
		container := PodContainer{Init: isInit, State: "unknown", ExitCode: -1}
		container.Name, _ = item["name"].(string)
		container.Image, _ = item["image"].(string)
		container.Ports = joinPorts(item["ports"])
		container.Requests, container.Limits = describeResources(item["resources"])
		container.Probes = describeProbes(item)
		container.Mounts = describeMounts(item["volumeMounts"])

		if st, ok := statuses[container.Name]; ok {
			if flag, ok := st["ready"].(bool); ok {
				container.Ready = flag
			}
			container.Restarts = numField(st, "restartCount")
			fillContainerState(&container, st["state"])
			container.LastTerminated = lastTerminatedLabel(st["lastState"])
		}
		out = append(out, container)
	}
	return out
}

func fillContainerState(container *PodContainer, raw any) {
	state, _ := raw.(map[string]any)
	if state == nil {
		return
	}
	for _, name := range []string{"running", "waiting", "terminated"} {
		body, ok := state[name].(map[string]any)
		if !ok {
			continue
		}
		container.State = name
		container.Reason, _ = body["reason"].(string)
		container.Message, _ = body["message"].(string)
		if name == "running" {
			container.StartedAt, _ = body["startedAt"].(string)
		}
		if name == "terminated" {
			container.ExitCode = numField(body, "exitCode")
			container.StartedAt, _ = body["startedAt"].(string)
		}
		return
	}
}

// lastTerminatedLabel 上一次退出的摘要。CrashLoopBackOff 时当前状态只会说
// 「在等下一次重启」，真正的原因（OOMKilled、退出码 1）在 lastState 里。
func lastTerminatedLabel(raw any) string {
	last, _ := raw.(map[string]any)
	if last == nil {
		return ""
	}
	body, ok := last["terminated"].(map[string]any)
	if !ok {
		return ""
	}
	reason, _ := body["reason"].(string)
	code := numField(body, "exitCode")
	finished, _ := body["finishedAt"].(string)
	text := fmt.Sprintf("%s，退出码 %d", reason, code)
	if reason == "" {
		text = fmt.Sprintf("退出码 %d", code)
	}
	if finished != "" {
		text += "（" + finished + "）"
	}
	return text
}

func joinPorts(raw any) string {
	list, _ := raw.([]any)
	parts := make([]string, 0, len(list))
	for _, entry := range list {
		port, _ := entry.(map[string]any)
		num := numField(port, "containerPort")
		proto, _ := port["protocol"].(string)
		if proto == "" {
			proto = "TCP"
		}
		parts = append(parts, fmt.Sprintf("%d/%s", num, proto))
	}
	return strings.Join(parts, " ")
}

// describeResources 返回 requests 与 limits 的可读串。
// 没配就是空串 —— 「没配 limits」本身是要看出来的事实（它决定 QoS 与被驱逐的顺序）。
func describeResources(raw any) (requests, limits string) {
	res, _ := raw.(map[string]any)
	if res == nil {
		return "", ""
	}
	return resourceLabel(res["requests"]), resourceLabel(res["limits"])
}

func resourceLabel(raw any) string {
	entry := stringMap(raw)
	if len(entry) == 0 {
		return ""
	}
	keys := make([]string, 0, len(entry))
	for key := range entry {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+"="+entry[key])
	}
	return strings.Join(parts, " ")
}

func describeProbes(item map[string]any) string {
	names := map[string]string{
		"livenessProbe":  "存活",
		"readinessProbe": "就绪",
		"startupProbe":   "启动",
	}
	parts := make([]string, 0, 3)
	for _, key := range []string{"livenessProbe", "readinessProbe", "startupProbe"} {
		if _, ok := item[key].(map[string]any); ok {
			parts = append(parts, names[key])
		}
	}
	return strings.Join(parts, " ")
}

func describeMounts(raw any) []string {
	list, _ := raw.([]any)
	out := make([]string, 0, len(list))
	for _, entry := range list {
		mount, _ := entry.(map[string]any)
		name, _ := mount["name"].(string)
		path, _ := mount["mountPath"].(string)
		text := name + " → " + path
		if flag, ok := mount["readOnly"].(bool); ok && flag {
			text += "（只读）"
		}
		out = append(out, text)
	}
	return out
}

func describeConditions(status map[string]any) []PodCondition {
	list, _ := status["conditions"].([]any)
	out := make([]PodCondition, 0, len(list))
	for _, entry := range list {
		cond, _ := entry.(map[string]any)
		item := PodCondition{}
		item.Type, _ = cond["type"].(string)
		item.Status, _ = cond["status"].(string)
		item.Reason, _ = cond["reason"].(string)
		item.Message, _ = cond["message"].(string)
		item.LastTransition, _ = cond["lastTransitionTime"].(string)
		out = append(out, item)
	}
	return out
}

// volumeSources 卷类型 → 里面存名字的字段。只覆盖常见的几种，
// 其余按「其他」处理并给出原始类型名，不假装认识所有卷插件。
var volumeSources = []struct {
	key   string
	label string
	field string
}{
	{"configMap", "ConfigMap", "name"},
	{"secret", "Secret", "secretName"},
	{"persistentVolumeClaim", "PVC", "claimName"},
	{"hostPath", "HostPath", "path"},
	{"emptyDir", "EmptyDir", ""},
	{"projected", "Projected", ""},
	{"downwardAPI", "DownwardAPI", ""},
	{"nfs", "NFS", "server"},
	{"csi", "CSI", "driver"},
}

func describeVolumes(spec map[string]any) []PodVolume {
	list, _ := spec["volumes"].([]any)
	out := make([]PodVolume, 0, len(list))
	for _, entry := range list {
		vol, _ := entry.(map[string]any)
		name, _ := vol["name"].(string)
		item := PodVolume{Name: name, Type: "其他"}
		for _, src := range volumeSources {
			body, ok := vol[src.key].(map[string]any)
			if !ok {
				continue
			}
			item.Type = src.label
			if src.field != "" {
				item.Source, _ = body[src.field].(string)
			}
			break
		}
		if item.Type == "其他" {
			// 把实际的键名说出来，比只写「其他」有用
			for key := range vol {
				if key != "name" {
					item.Source = key
					break
				}
			}
		}
		out = append(out, item)
	}
	return out
}
