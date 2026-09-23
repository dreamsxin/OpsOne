package metrics

// 暴露格式与打点的测试。
//
// 指标这东西坏掉的方式很特别：**它不会让业务出错**，只会让监控读出错的数字，
// 而错的数字比没有数字更糟。所以这里每条都在守一个「会读出错误结论」的场景。

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func render(r *Registry) string {
	var b strings.Builder
	r.Write(&b)
	return b.String()
}

func TestCounterExposition(t *testing.T) {
	r := NewRegistry()
	r.RegisterCounter("opsone_test_total", "测试用")
	r.AddCounter("opsone_test_total", map[string]string{"route": "/a", "method": "GET"}, 1)
	r.AddCounter("opsone_test_total", map[string]string{"route": "/a", "method": "GET"}, 2)
	r.AddCounter("opsone_test_total", map[string]string{"route": "/b", "method": "GET"}, 1)

	out := render(r)
	if !strings.Contains(out, "# TYPE opsone_test_total counter") {
		t.Fatalf("缺 TYPE 行:\n%s", out)
	}
	if !strings.Contains(out, `opsone_test_total{method="GET",route="/a"} 3`) {
		t.Fatalf("同一组标签要累加到 3:\n%s", out)
	}
	if !strings.Contains(out, `opsone_test_total{method="GET",route="/b"} 1`) {
		t.Fatalf("不同标签要分开:\n%s", out)
	}
}

// 没登记过的指标名不能让打点 panic：打点散在业务代码里，
// 为一个拼错的名字把请求打挂不值得
func TestUnknownMetricIsIgnored(t *testing.T) {
	r := NewRegistry()
	r.AddCounter("不存在", map[string]string{"a": "b"}, 1)
	r.Observe("也不存在", nil, 1.5)
	if out := render(r); out != "" {
		t.Fatalf("不该输出任何东西:\n%s", out)
	}
}

func TestHistogramBucketsAreCumulative(t *testing.T) {
	r := NewRegistry()
	r.RegisterHistogram("opsone_dur_seconds", "测试用", []float64{0.1, 1, 10})
	for _, v := range []float64{0.05, 0.1, 0.5, 5, 50} {
		r.Observe("opsone_dur_seconds", map[string]string{"route": "/x"}, v)
	}
	out := render(r)

	// 0.05 与 0.1 都 <= 0.1（边界值归本桶，不是下一个桶）
	for _, want := range []string{
		`opsone_dur_seconds_bucket{route="/x",le="0.1"} 2`,
		`opsone_dur_seconds_bucket{route="/x",le="1"} 3`,
		`opsone_dur_seconds_bucket{route="/x",le="10"} 4`,
		`opsone_dur_seconds_bucket{route="/x",le="+Inf"} 5`,
		`opsone_dur_seconds_count{route="/x"} 5`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("缺 %q:\n%s", want, out)
		}
	}
	// sum = 0.05+0.1+0.5+5+50
	if !strings.Contains(out, `opsone_dur_seconds_sum{route="/x"} 55.65`) {
		t.Fatalf("sum 不对:\n%s", out)
	}
}

// 标签值里的引号/反斜杠/换行必须转义：一处没转义会让**整份**输出变成非法格式，
// 抓取端拿到的是「解析失败」而不是「少一个指标」
func TestLabelEscaping(t *testing.T) {
	r := NewRegistry()
	r.RegisterCounter("opsone_esc_total", "测试用")
	r.AddCounter("opsone_esc_total", map[string]string{"v": `a"b\c` + "\n" + "d"}, 1)
	out := render(r)
	if !strings.Contains(out, `opsone_esc_total{v="a\"b\\c\nd"} 1`) {
		t.Fatalf("转义不对:\n%s", out)
	}
}

// gauge 取不到值时整族不输出。写 0 出去会被读成「真的是 0」——
// 「库大小 0 字节」和「读不到库文件」是两件完全不同的事
func TestNilGaugeIsOmittedNotZero(t *testing.T) {
	r := NewRegistry()
	r.RegisterGauge("opsone_maybe", "测试用", func() []Sample { return nil })
	r.RegisterGauge("opsone_sure", "测试用", func() []Sample { return One(3) })
	out := render(r)
	if strings.Contains(out, "opsone_maybe") {
		t.Fatalf("取不到的 gauge 不该出现:\n%s", out)
	}
	if !strings.Contains(out, "opsone_sure 3") {
		t.Fatalf("正常的 gauge 要输出:\n%s", out)
	}
}

// 输出顺序稳定：人会 diff 两次抓取结果，顺序抖动会让 diff 没法看
func TestOutputIsStable(t *testing.T) {
	r := NewRegistry()
	r.RegisterCounter("opsone_b_total", "测试用")
	r.RegisterCounter("opsone_a_total", "测试用")
	for i := 0; i < 20; i++ {
		r.AddCounter("opsone_a_total", map[string]string{"k": string(rune('a' + i%5))}, 1)
		r.AddCounter("opsone_b_total", map[string]string{"k": string(rune('a' + i%5))}, 1)
	}
	first := render(r)
	for i := 0; i < 5; i++ {
		if render(r) != first {
			t.Fatal("同一状态下两次输出不一致")
		}
	}
	if strings.Index(first, "opsone_a_total") > strings.Index(first, "opsone_b_total") {
		t.Fatal("指标名应该按字典序输出")
	}
}

// 中间件必须按**路由模板**打标签。用实际路径的话每台主机都会生成一组新时序，
// 几天就能把抓取端的内存吃掉 —— Prometheus 最常见的一种自伤
func TestMiddlewareUsesRouteTemplateNotPath(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := NewRegistry()
	Install(r)
	engine := gin.New()
	engine.Use(Middleware(r))
	engine.GET("/api/v1/hosts/:id", func(c *gin.Context) { c.String(http.StatusOK, "ok") })
	engine.GET("/boom", func(c *gin.Context) { c.String(http.StatusInternalServerError, "boom") })

	for _, path := range []string{"/api/v1/hosts/1", "/api/v1/hosts/2", "/api/v1/hosts/3"} {
		rec := httptest.NewRecorder()
		engine.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
	}
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, httptest.NewRequest("GET", "/boom", nil))
	rec = httptest.NewRecorder()
	engine.ServeHTTP(rec, httptest.NewRequest("GET", "/没有这个路由", nil))

	out := render(r)
	if !strings.Contains(out, `opsone_http_requests_total{method="GET",route="/api/v1/hosts/:id",status="2xx"} 3`) {
		t.Fatalf("三次请求应该聚到同一条时序上:\n%s", out)
	}
	if strings.Contains(out, "/api/v1/hosts/1") {
		t.Fatalf("实际路径不该出现在标签里（会导致基数爆炸）:\n%s", out)
	}
	if !strings.Contains(out, `route="/boom",status="5xx"} 1`) {
		t.Fatalf("5xx 要能看出来:\n%s", out)
	}
	if !strings.Contains(out, `route="unmatched"`) {
		t.Fatalf("没匹配到路由的请求要归到 unmatched:\n%s", out)
	}
	if !strings.Contains(out, `opsone_http_request_duration_seconds_count{route="/api/v1/hosts/:id"} 3`) {
		t.Fatalf("耗时直方图也要按模板聚合:\n%s", out)
	}
}

// 重复 Install 不能 panic：测试里每个用例都会构造一次路由
func TestInstallIsIdempotent(t *testing.T) {
	r := NewRegistry()
	Install(r)
	Install(r)
	r.AddCounter(HTTPRequestsTotal, map[string]string{"route": "/a", "method": "GET", "status": "2xx"}, 1)
	if !strings.Contains(render(r), `status="2xx"} 1`) {
		t.Fatal("重复 Install 之后打点应该照常生效")
	}
}
