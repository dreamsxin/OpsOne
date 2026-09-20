package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ops-platform/server/internal/model"
)

func TestStreamLabel(t *testing.T) {
	got := streamLabel(map[string]string{"job": "nginx", "app": "web"})
	if got != `{app="web",job="nginx"}` {
		t.Fatalf("标签应按键排序: %s", got)
	}
	if got := streamLabel(map[string]string{}); got != "{}" {
		t.Fatalf("空标签应为 {}，实际 %s", got)
	}
}

func TestShapeLogRows(t *testing.T) {
	// 两个流各自有序，合起来不有序——整形后必须是一条时间线
	raw := `{"resultType":"streams","result":[
      {"stream":{"app":"web"},"values":[
        ["1789913000000000300","web 第三条"],
        ["1789913000000000100","web 第一条"]]},
      {"stream":{"app":"job"},"values":[
        ["1789913000000000200","job 第二条"]]}
    ]}`
	var data lokiStreamData
	if err := json.Unmarshal([]byte(raw), &data); err != nil {
		t.Fatalf("准备数据失败: %v", err)
	}

	rows, truncated := shapeLogRows(data, true, 10)
	if truncated || len(rows) != 3 {
		t.Fatalf("应有 3 行且不截断，实际 %d / %v", len(rows), truncated)
	}
	if rows[0].Line != "web 第三条" || rows[2].Line != "web 第一条" {
		t.Fatalf("backward 应最新在前: %+v", []string{rows[0].Line, rows[1].Line, rows[2].Line})
	}
	if rows[1].Line != "job 第二条" || rows[1].Stream != `{app="job"}` {
		t.Fatalf("跨流归并不对: %+v", rows[1])
	}
	// 毫秒时间戳供前端格式化，纳秒原值保留用于排序
	if rows[0].At != 1789913000000 || rows[0].Nano != "1789913000000000300" {
		t.Fatalf("时间戳不对: %+v", rows[0])
	}

	// forward 就是反过来
	rows, _ = shapeLogRows(data, false, 10)
	if rows[0].Line != "web 第一条" || rows[2].Line != "web 第三条" {
		t.Fatalf("forward 应最旧在前: %+v", rows)
	}

	// 条数达到 limit 就要标记截断：Loki 是服务端按 limit 截的，
	// 拿到满额结果说明很可能还有更多
	rows, truncated = shapeLogRows(data, true, 3)
	if !truncated || len(rows) != 3 {
		t.Fatalf("正好满额也应标记截断，实际 %d / %v", len(rows), truncated)
	}
	rows, truncated = shapeLogRows(data, true, 2)
	if !truncated || len(rows) != 2 {
		t.Fatalf("应截断到 2 行，实际 %d / %v", len(rows), truncated)
	}

	// 聚合表达式返回的 matrix 里时间戳是数字：不能在解码阶段就报错，
	// 否则拿不到 resultType，没法告诉用户「这是指标不是日志」
	var matrix lokiStreamData
	if err := json.Unmarshal([]byte(`{"resultType":"matrix","result":[
      {"metric":{"app":"web"},"values":[[1789913000,"3"],[1789913060,"5"]]}]}`), &matrix); err != nil {
		t.Fatalf("matrix 结果也必须能解码: %v", err)
	}
	if matrix.ResultType != "matrix" {
		t.Fatalf("resultType 应能读出来: %q", matrix.ResultType)
	}

	// 超长行截断并打标记
	long := strings.Repeat("x", logLineMaxLen+100)
	if err := json.Unmarshal([]byte(`{"resultType":"streams","result":[
      {"stream":{"app":"web"},"values":[["1789913000000000100","`+long+`"]]}]}`), &data); err != nil {
		t.Fatalf("准备数据失败: %v", err)
	}
	rows, _ = shapeLogRows(data, true, 10)
	if len(rows) != 1 || !rows[0].Truncated || len(rows[0].Line) != logLineMaxLen {
		t.Fatalf("超长行应截断并标记: %d / %v", len(rows[0].Line), rows[0].Truncated)
	}

	// 坏数据（缺字段、时间戳不是数字）跳过，不影响好数据
	if err := json.Unmarshal([]byte(`{"resultType":"streams","result":[
      {"stream":{"app":"web"},"values":[["1789913000000000100"],["bad","x"],
        ["1789913000000000200","好的一条"]]}]}`), &data); err != nil {
		t.Fatalf("准备数据失败: %v", err)
	}
	rows, _ = shapeLogRows(data, true, 10)
	if len(rows) != 1 || rows[0].Line != "好的一条" {
		t.Fatalf("坏数据应跳过: %+v", rows)
	}

	// 空结果返回空数组而不是 nil
	empty, _ := shapeLogRows(lokiStreamData{ResultType: "streams"}, true, 10)
	if empty == nil || len(empty) != 0 {
		t.Fatalf("空结果应为空数组: %+v", empty)
	}
}

func TestLogSourceReqNormalize(t *testing.T) {
	req := logSourceReq{Name: "  loki  ", BaseURL: " http://loki:3100/ ", Tenant: " team-a "}
	if err := req.normalize(); err != nil {
		t.Fatalf("应通过: %v", err)
	}
	if req.Name != "loki" || req.BaseURL != "http://loki:3100" || req.Tenant != "team-a" {
		t.Fatalf("规整结果不对: %+v", req)
	}
	if req.TimeoutSec != 30 {
		t.Fatalf("超时应回落到 30，实际 %d", req.TimeoutSec)
	}

	bad := []struct {
		name string
		req  logSourceReq
		want string
	}{
		{"没名字", logSourceReq{BaseURL: "http://x:3100"}, "名称不能为空"},
		{"没地址", logSourceReq{Name: "a"}, "地址不能为空"},
		{"缺协议", logSourceReq{Name: "a", BaseURL: "loki:3100"}, "http:// 或 https://"},
		{"粘了查询路径", logSourceReq{Name: "a", BaseURL: "http://x:3100/loki/api/v1/query_range"}, "不要带 /loki/api"},
		{"超时越界", logSourceReq{Name: "a", BaseURL: "http://x:3100", TimeoutSec: 999}, "1 到 120 秒"},
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

// TestLokiRequestErrorHandling Loki 的报错常常是纯文本而不是 JSON 信封，
// 这一点和 Prometheus 不同，必须单独验。
func TestLokiRequestErrorHandling(t *testing.T) {
	var gotTenant, gotAuth string
	mux := http.NewServeMux()
	mux.HandleFunc("/loki/api/v1/labels", func(w http.ResponseWriter, r *http.Request) {
		gotTenant = r.Header.Get("X-Scope-OrgID")
		gotAuth = r.Header.Get("X-Token")
		fmt.Fprint(w, `{"status":"success","data":["app","job"]}`)
	})
	mux.HandleFunc("/loki/api/v1/badql", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		// Loki 真实的 LogQL 报错就是这样一行纯文本
		fmt.Fprint(w, "parse error at line 1, col 1: syntax error: unexpected IDENTIFIER")
	})
	mux.HandleFunc("/loki/api/v1/toobig", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, "maximum of series (500) reached for a single query")
	})
	mux.HandleFunc("/loki/api/v1/html", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "<html>not loki</html>")
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	source := model.LogSource{
		BaseURL: srv.URL, Tenant: "team-a",
		HeaderKey: "X-Token", HeaderValue: "secret", TimeoutSec: 5,
	}

	var labels []string
	if err := lokiRequest(source, "/loki/api/v1/labels", nil, &labels); err != nil {
		t.Fatalf("正常请求失败: %v", err)
	}
	if len(labels) != 2 {
		t.Fatalf("标签解析不对: %+v", labels)
	}
	if gotTenant != "team-a" {
		t.Fatalf("租户头没带上: %q", gotTenant)
	}
	if gotAuth != "secret" {
		t.Fatalf("鉴权头没带上: %q", gotAuth)
	}

	// LogQL 语法错：纯文本原样带出来
	err := lokiRequest(source, "/loki/api/v1/badql", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "unexpected IDENTIFIER") {
		t.Fatalf("应带上 Loki 的纯文本报错，实际 %v", err)
	}
	// 命中 Loki 的各种上限（429）也要说清楚
	err = lokiRequest(source, "/loki/api/v1/toobig", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "maximum of series") {
		t.Fatalf("429 的原文应带出来，实际 %v", err)
	}
	// 地址指到别的服务
	err = lokiRequest(source, "/loki/api/v1/html", nil, &labels)
	if err == nil || !strings.Contains(err.Error(), "不是 Loki 格式") {
		t.Fatalf("非 Loki 响应的提示不对: %v", err)
	}
	// 空地址不发请求
	if err := lokiRequest(model.LogSource{}, "/loki/api/v1/labels", nil, nil); err == nil {
		t.Fatal("空地址应报错")
	}
	// 没配租户时不应该带空的租户头（多租户 Loki 会把空值当非法）
	plain := model.LogSource{BaseURL: srv.URL, TimeoutSec: 5}
	gotTenant = "sentinel"
	if err := lokiRequest(plain, "/loki/api/v1/labels", nil, &labels); err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	if gotTenant != "" {
		t.Fatalf("没配租户时不该带 X-Scope-OrgID，实际 %q", gotTenant)
	}
}
