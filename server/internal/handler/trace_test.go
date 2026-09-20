package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ops-platform/server/internal/model"
)

// timeAfterSeconds 小工具：给「不能死循环」这类断言加超时
func timeAfterSeconds(n int) <-chan time.Time {
	return time.After(time.Duration(n) * time.Second)
}

// 一条两层调用链：gateway -> order -> mysql，其中 mysql 那段标了 error
const sampleTraceJSON = `{
  "traceID":"abc123",
  "processes":{
    "p1":{"serviceName":"gateway"},
    "p2":{"serviceName":"order"},
    "p3":{"serviceName":"mysql"}
  },
  "spans":[
    {"traceID":"abc123","spanID":"s3","operationName":"SELECT orders","processID":"p3",
     "startTime":1789910000200000,"duration":50000,
     "tags":[{"key":"error","type":"bool","value":true},{"key":"db.statement","type":"string","value":"select 1"}],
     "references":[{"refType":"CHILD_OF","spanID":"s2"}]},
    {"traceID":"abc123","spanID":"s1","operationName":"GET /order","processID":"p1",
     "startTime":1789910000000000,"duration":300000,
     "tags":[{"key":"http.status_code","type":"int64","value":500},{"key":"sampler.param","type":"float64","value":0.5}],
     "references":[]},
    {"traceID":"abc123","spanID":"s2","operationName":"order.query","processID":"p2",
     "startTime":1789910000100000,"duration":180000,
     "tags":[{"key":"http.status_code","type":"int64","value":200}],
     "references":[{"refType":"CHILD_OF","spanID":"s1"}]}
  ]}`

func mustTrace(t *testing.T, raw string) jaegerTrace {
	t.Helper()
	var trace jaegerTrace
	if err := json.Unmarshal([]byte(raw), &trace); err != nil {
		t.Fatalf("准备数据失败: %v", err)
	}
	return trace
}

func TestFlattenTags(t *testing.T) {
	trace := mustTrace(t, sampleTraceJSON)
	for _, span := range trace.Spans {
		tags := flattenTags(span.Tags)
		switch span.SpanID {
		case "s1":
			// 整数别显示成 500.000000，小数要保留
			if tags["http.status_code"] != "500" {
				t.Fatalf("整数标签格式不对: %q", tags["http.status_code"])
			}
			if tags["sampler.param"] != "0.5" {
				t.Fatalf("小数标签格式不对: %q", tags["sampler.param"])
			}
		case "s3":
			if tags["error"] != "true" || tags["db.statement"] != "select 1" {
				t.Fatalf("标签解析不对: %+v", tags)
			}
		}
	}
}

func TestSpanError(t *testing.T) {
	cases := []struct {
		name string
		tags map[string]string
		want bool
	}{
		{"OpenTracing error 标记", map[string]string{"error": "true"}, true},
		{"error=false", map[string]string{"error": "false"}, false},
		{"HTTP 500", map[string]string{"http.status_code": "500"}, true},
		{"HTTP 404 不算", map[string]string{"http.status_code": "404"}, false},
		{"OTel 新语义的状态码字段", map[string]string{"http.response.status_code": "503"}, true},
		{"OTel 状态", map[string]string{"otel.status_code": "ERROR"}, true},
		{"OTel 状态小写也认", map[string]string{"otel.status_code": "error"}, true},
		{"OTel OK", map[string]string{"otel.status_code": "OK"}, false},
		{"状态码不是数字", map[string]string{"http.status_code": "unknown"}, false},
		{"什么都没有", map[string]string{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := spanError(tc.tags); got != tc.want {
				t.Fatalf("应为 %v，实际 %v", tc.want, got)
			}
		})
	}
}

func TestSummarizeTrace(t *testing.T) {
	got := summarizeTrace(mustTrace(t, sampleTraceJSON))
	if got.TraceID != "abc123" || got.SpanCount != 3 || got.ServiceCount != 3 {
		t.Fatalf("汇总不对: %+v", got)
	}
	// 入口 span 是没有父的那个，即便它不是数组里的第一条
	if got.RootService != "gateway" || got.RootOperation != "GET /order" {
		t.Fatalf("入口 span 判断不对: %+v", got)
	}
	// 跨度 = 最晚结束 - 最早开始 = 300ms
	if got.DurationMs != 300 {
		t.Fatalf("跨度应为 300ms，实际 %v", got.DurationMs)
	}
	if got.StartAt != 1789910000000 {
		t.Fatalf("开始时间应换成毫秒，实际 %d", got.StartAt)
	}
	// s1（HTTP 500）与 s3（error=true）都算错
	if got.ErrorCount != 2 {
		t.Fatalf("出错 span 数应为 2，实际 %d", got.ErrorCount)
	}
	if strings.Join(got.Services, ",") != "gateway,mysql,order" {
		t.Fatalf("服务列表应排序: %+v", got.Services)
	}

	// 空 trace 不能 panic
	empty := summarizeTrace(jaegerTrace{TraceID: "x"})
	if empty.SpanCount != 0 || empty.Services == nil {
		t.Fatalf("空 trace 处理不对: %+v", empty)
	}
}

func TestBuildSpanTree(t *testing.T) {
	spans, truncated := buildSpanTree(mustTrace(t, sampleTraceJSON))
	if truncated || len(spans) != 3 {
		t.Fatalf("应有 3 个 span 且不截断: %d / %v", len(spans), truncated)
	}
	// 深度优先：入口在最前，子孙依次缩进
	if spans[0].SpanID != "s1" || spans[0].Depth != 0 || spans[0].OffsetMs != 0 {
		t.Fatalf("入口 span 不对: %+v", spans[0])
	}
	if spans[1].SpanID != "s2" || spans[1].Depth != 1 || spans[1].ParentID != "s1" {
		t.Fatalf("第二层不对: %+v", spans[1])
	}
	if spans[1].OffsetMs != 100 || spans[1].DurationMs != 180 {
		t.Fatalf("偏移/耗时换算不对: %+v", spans[1])
	}
	if spans[2].SpanID != "s3" || spans[2].Depth != 2 || !spans[2].Error {
		t.Fatalf("第三层不对: %+v", spans[2])
	}
	if spans[2].Service != "mysql" || spans[2].Tags["db.statement"] != "select 1" {
		t.Fatalf("服务名/标签没带上: %+v", spans[2])
	}

	// 父 span 不在本 trace 里（采样丢了父）时不能把 span 藏起来
	orphan := mustTrace(t, `{"traceID":"t","processes":{"p1":{"serviceName":"a"}},
      "spans":[{"spanID":"s9","operationName":"op","processID":"p1","startTime":1000,"duration":10,
        "references":[{"refType":"CHILD_OF","spanID":"missing"}]}]}`)
	spans, _ = buildSpanTree(orphan)
	if len(spans) != 1 || spans[0].Depth != 0 {
		t.Fatalf("孤儿 span 应作为入口展示: %+v", spans)
	}

	// 同一个 spanID 出现两次（客户端/服务端共享 span，或重复上报）时两条都要画出来，
	// 否则界面上的行数会比汇总里的 span 数少，让人以为丢了数据
	shared := mustTrace(t, `{"traceID":"t","processes":{"p1":{"serviceName":"a"},"p2":{"serviceName":"b"}},
      "spans":[
        {"spanID":"root","processID":"p1","startTime":1000,"duration":500,"references":[]},
        {"spanID":"dup","processID":"p1","startTime":1100,"duration":100,
         "references":[{"refType":"CHILD_OF","spanID":"root"}]},
        {"spanID":"dup","processID":"p2","startTime":1120,"duration":60,
         "references":[{"refType":"CHILD_OF","spanID":"root"}]}]}`)
	spans, _ = buildSpanTree(shared)
	if len(spans) != 3 {
		t.Fatalf("重复 spanID 的两条都该画出来，实际 %d 条: %+v", len(spans), spans)
	}
	services := spans[1].Service + "," + spans[2].Service
	if services != "a,b" && services != "b,a" {
		t.Fatalf("两条重复 span 应分别带自己的服务名: %s", services)
	}

	// 环形引用（脏数据）不能死循环
	cyclic := mustTrace(t, `{"traceID":"t","processes":{"p1":{"serviceName":"a"}},"spans":[
      {"spanID":"x","processID":"p1","startTime":1000,"duration":10,
       "references":[{"refType":"CHILD_OF","spanID":"y"}]},
      {"spanID":"y","processID":"p1","startTime":1000,"duration":10,
       "references":[{"refType":"CHILD_OF","spanID":"x"}]}]}`)
	done := make(chan int, 1)
	go func() {
		out, _ := buildSpanTree(cyclic)
		done <- len(out)
	}()
	select {
	case n := <-done:
		if n == 0 {
			t.Fatal("环形引用时也该返回点东西")
		}
	case <-timeAfterSeconds(3):
		t.Fatal("环形引用把 buildSpanTree 卡死了")
	}

	// span 太多要截断
	items := make([]string, 0, traceSpanMaxCount+10)
	for i := 0; i < traceSpanMaxCount+10; i++ {
		items = append(items, fmt.Sprintf(
			`{"spanID":"s%d","processID":"p1","startTime":%d,"duration":10,"references":[]}`, i, 1000+i))
	}
	big := mustTrace(t, `{"traceID":"t","processes":{"p1":{"serviceName":"a"}},"spans":[`+
		strings.Join(items, ",")+`]}`)
	spans, truncated = buildSpanTree(big)
	if !truncated || len(spans) != traceSpanMaxCount {
		t.Fatalf("应截断到 %d 个，实际 %d / %v", traceSpanMaxCount, len(spans), truncated)
	}

	// 空 trace
	if out, _ := buildSpanTree(jaegerTrace{}); out == nil || len(out) != 0 {
		t.Fatalf("空 trace 应返回空数组: %+v", out)
	}
}

func TestTraceSourceReqNormalize(t *testing.T) {
	req := traceSourceReq{Name: "  jaeger ", BaseURL: " http://jaeger:16686/ "}
	if err := req.normalize(); err != nil {
		t.Fatalf("应通过: %v", err)
	}
	if req.Name != "jaeger" || req.BaseURL != "http://jaeger:16686" || req.TimeoutSec != 20 {
		t.Fatalf("规整结果不对: %+v", req)
	}

	bad := []struct {
		name string
		req  traceSourceReq
		want string
	}{
		{"没名字", traceSourceReq{BaseURL: "http://x:16686"}, "名称不能为空"},
		{"没地址", traceSourceReq{Name: "a"}, "地址不能为空"},
		{"缺协议", traceSourceReq{Name: "a", BaseURL: "jaeger:16686"}, "http:// 或 https://"},
		{"粘了查询路径", traceSourceReq{Name: "a", BaseURL: "http://x:16686/api/traces"}, "不要带 /api/"},
		{"超时越界", traceSourceReq{Name: "a", BaseURL: "http://x:16686", TimeoutSec: 200}, "1 到 120 秒"},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			req := tc.req
			if err := req.normalize(); err == nil {
				t.Fatal("应被拒绝，实际通过")
			} else if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("错误信息应包含 %q，实际 %v", tc.want, err)
			}
		})
	}
}

// TestJaegerRequestErrors Jaeger 的错误体有时带着 HTTP 200，只看状态码会漏
func TestJaegerRequestErrors(t *testing.T) {
	var gotAuth string
	mux := http.NewServeMux()
	mux.HandleFunc("/api/services", func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("X-Token")
		fmt.Fprint(w, `{"data":["gateway","order"],"total":2}`)
	})
	mux.HandleFunc("/api/traces/nope", func(w http.ResponseWriter, r *http.Request) {
		// 真实 Jaeger 查不到 trace 时就是这样：200 + errors
		fmt.Fprint(w, `{"data":null,"errors":[{"code":404,"msg":"trace not found"}]}`)
	})
	mux.HandleFunc("/api/traces/boom", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, `{"data":null,"errors":[{"code":500,"msg":"storage unavailable"}]}`)
	})
	mux.HandleFunc("/api/html", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "<html>not jaeger</html>")
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	source := model.TraceSource{BaseURL: srv.URL, HeaderKey: "X-Token", HeaderValue: "secret", TimeoutSec: 5}

	services := []string{}
	if err := jaegerRequest(source, "/api/services", nil, &services); err != nil {
		t.Fatalf("正常请求失败: %v", err)
	}
	if len(services) != 2 || gotAuth != "secret" {
		t.Fatalf("解析或鉴权头不对: %+v / %q", services, gotAuth)
	}

	err := jaegerRequest(source, "/api/traces/nope", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "trace not found") {
		t.Fatalf("HTTP 200 + errors 也必须算失败，实际 %v", err)
	}
	err = jaegerRequest(source, "/api/traces/boom", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "storage unavailable") {
		t.Fatalf("500 的原因应带出来，实际 %v", err)
	}
	err = jaegerRequest(source, "/api/html", nil, &services)
	if err == nil || !strings.Contains(err.Error(), "不是 Jaeger 格式") {
		t.Fatalf("非 Jaeger 响应提示不对: %v", err)
	}
	if err := jaegerRequest(model.TraceSource{}, "/api/services", nil, nil); err == nil {
		t.Fatal("空地址应报错")
	}
}
