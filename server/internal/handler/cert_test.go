package handler

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestProbeCertificateSelfSigned 用 httptest 起一个自签名 TLS 服务，
// 验证「证书能读出来，但链校验不通过」这条路径：探测不应该因为不可信就失败。
func TestProbeCertificateSelfSigned(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer server.Close()

	host, portStr, err := net.SplitHostPort(strings.TrimPrefix(server.URL, "https://"))
	if err != nil {
		t.Fatalf("解析测试服务地址失败: %v", err)
	}
	port, _ := strconv.Atoi(portStr)

	probe, err := probeCertificate(host, port, host)
	if err != nil {
		t.Fatalf("自签名证书也应该能读到，却报错: %v", err)
	}
	if probe.Leaf == nil {
		t.Fatal("没有读到证书")
	}
	if probe.Trusted {
		t.Error("自签名证书不应判为可信")
	}
	if probe.VerifyError == "" {
		t.Error("链校验失败时必须给出原因")
	}
	if got := certFingerprint(probe.Leaf); len(got) != 95 {
		t.Errorf("SHA256 指纹格式不对: %s", got)
	}
}

// TestProbeCertificateUnreachable 端口没人监听时必须报错，而不是静默成功
func TestProbeCertificateUnreachable(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("占端口失败: %v", err)
	}
	addr := listener.Addr().(*net.TCPAddr)
	listener.Close() // 立刻释放，制造一个大概率无人监听的端口

	if _, err := probeCertificate("127.0.0.1", addr.Port, "127.0.0.1"); err == nil {
		t.Error("端口不可达时应该返回错误")
	}
}

func TestCertStatus(t *testing.T) {
	now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name      string
		notAfter  time.Time
		alertDays int
		want      string
	}{
		{"已过期", now.Add(-time.Hour), 30, "expired"},
		{"刚好等于阈值", now.Add(30 * 24 * time.Hour), 30, "expiring"},
		{"阈值内", now.Add(5 * 24 * time.Hour), 30, "expiring"},
		{"阈值外", now.Add(90 * 24 * time.Hour), 30, "valid"},
	}
	for _, tc := range cases {
		if got := certStatus(now, tc.notAfter, tc.alertDays); got != tc.want {
			t.Errorf("%s: 期望 %s，实际 %s", tc.name, tc.want, got)
		}
	}
}
