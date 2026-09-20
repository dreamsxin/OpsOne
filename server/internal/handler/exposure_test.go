package handler

import (
	"net"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"ops-platform/server/internal/model"
)

// mustListen 起一个本地监听，用来造出「确实开着」的端口
func mustListen(t *testing.T) net.Listener {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("起监听失败: %v", err)
	}
	return listener
}

func listenerPort(t *testing.T, listener net.Listener) int {
	t.Helper()
	_, portStr, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatalf("取端口失败: %v", err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("端口不是数字: %v", err)
	}
	return port
}

func TestParsePortSpec(t *testing.T) {
	cases := []struct {
		name string
		spec string
		want []int
	}{
		{"单个", "22", []int{22}},
		{"多个乱序", "443,22,80", []int{22, 80, 443}},
		{"区间", "8000-8003", []int{8000, 8001, 8002, 8003}},
		{"混写并去重", "22, 80 ,22,79-81", []int{22, 79, 80, 81}},
		{"空白项忽略", "22,,,80,", []int{22, 80}},
		{"单端口区间", "80-80", []int{80}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parsePortSpec(tc.spec)
			if err != nil {
				t.Fatalf("解析失败: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("应为 %v，实际 %v", tc.want, got)
			}
		})
	}

	bad := []struct {
		name string
		spec string
		want string
	}{
		{"空", "   ", "不能为空"},
		{"非数字", "22,abc", "不是合法端口"},
		{"越界", "70000", "超出"},
		{"零", "0", "超出"},
		{"区间反了", "100-80", "起止写反"},
		{"区间过大", "1-2000", "一次最多"},
		{"总数过大", "1-600,700-1400", "端口总数超过"},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := parsePortSpec(tc.spec); err == nil {
				t.Fatal("应该被拒绝，却通过了")
			} else if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("错误信息应包含 %q，实际 %v", tc.want, err)
			}
		})
	}
}

func TestDiffPorts(t *testing.T) {
	scanned := []int{22, 80, 443, 3306, 6379}

	// 开着但没登记 → unexpected；登记了却没开 → missing
	unexpected, missing := diffPorts([]int{22, 80, 6379}, []int{22, 80, 443}, scanned)
	if !reflect.DeepEqual(unexpected, []int{6379}) {
		t.Fatalf("unexpected 应为 [6379]，实际 %v", unexpected)
	}
	if !reflect.DeepEqual(missing, []int{443}) {
		t.Fatalf("missing 应为 [443]，实际 %v", missing)
	}

	// 基线里写了但这次没扫的端口不做判断：否则换个扫描范围就误报「服务挂了」
	unexpected, missing = diffPorts([]int{22}, []int{22, 8080}, []int{22, 80})
	if len(unexpected) != 0 || len(missing) != 0 {
		t.Fatalf("没扫到的基线端口不该判定，实际 unexpected=%v missing=%v", unexpected, missing)
	}

	// 没有基线时，任何开放端口都算未登记
	unexpected, missing = diffPorts([]int{22, 80}, []int{}, scanned)
	if !reflect.DeepEqual(unexpected, []int{22, 80}) || len(missing) != 0 {
		t.Fatalf("空基线判定不对: unexpected=%v missing=%v", unexpected, missing)
	}

	// 全都登记过 → 干净
	unexpected, missing = diffPorts([]int{22, 443}, []int{22, 443, 3306}, []int{22, 443})
	if len(unexpected) != 0 || len(missing) != 0 {
		t.Fatalf("应判为干净，实际 unexpected=%v missing=%v", unexpected, missing)
	}
}

func TestFormatPorts(t *testing.T) {
	if got := formatPorts([]int{22, 80, 443}); got != "22,80,443" {
		t.Fatalf("格式化结果不对: %q", got)
	}
	if got := formatPorts([]int{}); got != "" {
		t.Fatalf("空列表应为空串，实际 %q", got)
	}
}

func TestExposureReqNormalize(t *testing.T) {
	enabled := false
	req := exposureReq{
		Name: "  网关  ", Address: " gw.example.com ", Ports: "80, 443,8000-8002",
		Baseline: "443,80", Enabled: &enabled,
	}
	if err := req.normalize(); err != nil {
		t.Fatalf("校验失败: %v", err)
	}
	if req.Name != "网关" || req.Address != "gw.example.com" {
		t.Fatalf("首尾空白没去掉: %+v", req)
	}
	// 端口清单保留用户写法（区间比展开好读），基线规整成升序
	if req.Ports != "80, 443,8000-8002" {
		t.Fatalf("端口清单不该被改写: %q", req.Ports)
	}
	if req.Baseline != "80,443" {
		t.Fatalf("基线应规整为升序: %q", req.Baseline)
	}
	if req.TimeoutMs != 800 {
		t.Fatalf("超时应回落到默认 800，实际 %d", req.TimeoutMs)
	}

	bad := []struct {
		name string
		req  exposureReq
		want string
	}{
		{"没名字", exposureReq{Address: "1.2.3.4", Ports: "22"}, "名称不能为空"},
		{"没地址", exposureReq{Name: "a", Ports: "22"}, "地址不能为空"},
		{"地址带端口", exposureReq{Name: "a", Address: "1.2.3.4:22", Ports: "22"}, "端口写在端口清单里"},
		{"地址带路径", exposureReq{Name: "a", Address: "1.2.3.4/24", Ports: "22"}, "端口写在端口清单里"},
		{"端口为空", exposureReq{Name: "a", Address: "1.2.3.4"}, "端口清单不能为空"},
		{"端口非法", exposureReq{Name: "a", Address: "1.2.3.4", Ports: "22,x"}, "不是合法端口"},
		{"基线非法", exposureReq{Name: "a", Address: "1.2.3.4", Ports: "22", Baseline: "99999"}, "基线端口有问题"},
		{"超时太小", exposureReq{Name: "a", Address: "1.2.3.4", Ports: "22", TimeoutMs: 10}, "毫秒之间"},
		{"超时太大", exposureReq{Name: "a", Address: "1.2.3.4", Ports: "22", TimeoutMs: 60000}, "毫秒之间"},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			req := tc.req
			if err := req.normalize(); err == nil {
				t.Fatal("应该被拒绝，却通过了")
			} else if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("错误信息应包含 %q，实际 %v", tc.want, err)
			}
		})
	}
}

// TestScanExposureTargetAgainstListener 用真实监听端口验证扫描判定：
// 开着的端口要被认出来，没人监听的端口不能算开。
func TestScanExposureTargetAgainstListener(t *testing.T) {
	listener := mustListen(t)
	defer listener.Close()
	openPort := listenerPort(t, listener)

	closed := mustListen(t)
	closedPort := listenerPort(t, closed)
	closed.Close() // 立刻关掉，这个端口应当扫不到

	target := model.ExposureTarget{
		Address:   "127.0.0.1",
		Ports:     formatPorts([]int{openPort, closedPort}),
		Baseline:  formatPorts([]int{closedPort}), // 故意错配：开的没登记、登记的没开
		TimeoutMs: 500,
	}
	result := scanExposureTarget(target)

	if result.Status != "unexpected" {
		t.Fatalf("有未登记端口时状态应为 unexpected，实际 %s（错误：%s）", result.Status, result.ErrorMsg)
	}
	if !reflect.DeepEqual(result.Open, []int{openPort}) {
		t.Fatalf("应只认出监听中的端口，实际 %v", result.Open)
	}
	if !reflect.DeepEqual(result.Unexpected, []int{openPort}) {
		t.Fatalf("未登记端口判定不对: %v", result.Unexpected)
	}
	if !reflect.DeepEqual(result.Missing, []int{closedPort}) {
		t.Fatalf("登记了却没开的端口判定不对: %v", result.Missing)
	}
	if result.Scanned != 2 {
		t.Fatalf("应扫 2 个端口，实际 %d", result.Scanned)
	}

	// 把开着的端口登记进基线就该判为干净
	target.Baseline = formatPorts([]int{openPort})
	target.Ports = formatPorts([]int{openPort})
	if result := scanExposureTarget(target); result.Status != "ok" || len(result.Unexpected) != 0 {
		t.Fatalf("登记齐全时应为 ok，实际 %s / %v", result.Status, result.Unexpected)
	}
}

func TestScanExposureTargetRejectsBadInput(t *testing.T) {
	// 端口清单坏了要立刻失败，不去连任何端口
	result := scanExposureTarget(model.ExposureTarget{Address: "127.0.0.1", Ports: "abc"})
	if result.Status != "failed" || !strings.Contains(result.ErrorMsg, "不是合法端口") {
		t.Fatalf("非法端口清单应直接失败，实际 %+v", result)
	}

	// 解析不了的域名同样直接失败，不必逐个端口撞超时
	result = scanExposureTarget(model.ExposureTarget{
		Address: "nonexistent-host-for-ops-test.invalid", Ports: "80", TimeoutMs: 300,
	})
	if result.Status != "failed" || !strings.Contains(result.ErrorMsg, "无法解析") {
		t.Fatalf("域名解析失败应直接报错，实际 %+v", result)
	}
}
