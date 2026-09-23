package handler

// /metrics 与 pprof 的鉴权测试。
//
// 这两个端点的风险不对称：/metrics 泄露的是侦察材料，pprof 的 heap
// 是**进程内存快照**——里面有 SSH 私钥与主机口令。所以鉴权这一层必须有测试守着。

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"ops-platform/server/internal/metrics"
)

func newMetricsEngine(t *testing.T, token string, pprofOn bool) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	reg := metrics.NewRegistry()
	metrics.Install(reg)
	reg.RegisterGauge("opsone_up", "测试用", func() []metrics.Sample { return metrics.One(1) })

	engine := gin.New()
	if token != "" {
		engine.GET("/metrics", MetricsHandler(reg, token))
		if pprofOn {
			engine.GET("/debug/pprof/:profile", PprofHandler(token))
		}
	}
	return engine
}

func get(t *testing.T, engine *gin.Engine, path, header string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if header != "" {
		req.Header.Set("Authorization", header)
	}
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

// 没配令牌时连路由都不该存在：404 而不是 401 —— 不告诉外面这里有个被保护的端点
func TestMetricsNotRegisteredWithoutToken(t *testing.T) {
	engine := newMetricsEngine(t, "", false)
	if code, _ := get(t, engine, "/metrics", ""); code != http.StatusNotFound {
		t.Fatalf("没配令牌时应该是 404，实际 %d", code)
	}
	if code, _ := get(t, engine, "/debug/pprof/heap", ""); code != http.StatusNotFound {
		t.Fatalf("pprof 同理应该是 404，实际 %d", code)
	}
}

func TestMetricsRequiresCorrectToken(t *testing.T) {
	engine := newMetricsEngine(t, "s3cret", false)

	if code, _ := get(t, engine, "/metrics", ""); code != http.StatusUnauthorized {
		t.Fatalf("不带令牌应该 401，实际 %d", code)
	}
	if code, _ := get(t, engine, "/metrics", "Bearer wrong"); code != http.StatusUnauthorized {
		t.Fatalf("错令牌应该 401，实际 %d", code)
	}
	// 前缀正确但不完整也必须被拒（固定时间比较，不做前缀匹配）
	if code, _ := get(t, engine, "/metrics", "Bearer s3c"); code != http.StatusUnauthorized {
		t.Fatalf("令牌前缀不算通过，实际 %d", code)
	}

	code, body := get(t, engine, "/metrics", "Bearer s3cret")
	if code != http.StatusOK {
		t.Fatalf("正确令牌应该 200，实际 %d", code)
	}
	if !strings.Contains(body, "opsone_up 1") {
		t.Fatalf("应该输出指标:\n%s", body)
	}
	// Prometheus 靠 Content-Type 判断格式版本
	if code, _ = get(t, engine, "/metrics?token=s3cret", ""); code != http.StatusOK {
		t.Fatalf("查询参数带令牌也要认（curl 手看一眼时方便），实际 %d", code)
	}
}

// pprof 只认白名单里的 profile 名，不做通配转发 —— cmdline 会带出进程启动参数
func TestPprofOnlyKnownProfiles(t *testing.T) {
	engine := newMetricsEngine(t, "s3cret", true)

	if code, _ := get(t, engine, "/debug/pprof/heap", "Bearer s3cret"); code != http.StatusOK {
		t.Fatalf("heap 应该可用，实际 %d", code)
	}
	code, body := get(t, engine, "/debug/pprof/cmdline", "Bearer s3cret")
	if code != http.StatusNotFound {
		t.Fatalf("cmdline 不在白名单里，应该 404，实际 %d", code)
	}
	if !strings.Contains(body, "支持的 profile") {
		t.Fatalf("要告诉人支持哪些:\n%s", body)
	}
	if code, _ := get(t, engine, "/debug/pprof/heap", ""); code != http.StatusUnauthorized {
		t.Fatalf("pprof 不带令牌必须 401，实际 %d", code)
	}
}
