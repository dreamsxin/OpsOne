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

func TestSeriesName(t *testing.T) {
	cases := []struct {
		name   string
		labels map[string]string
		want   string
	}{
		{"只有指标名", map[string]string{"__name__": "up"}, "up"},
		{"带标签且按键排序", map[string]string{
			"__name__": "up", "job": "prometheus", "instance": "localhost:9090",
		}, `up{instance="localhost:9090",job="prometheus"}`},
		{"没有指标名", map[string]string{"code": "200"}, `{code="200"}`},
		{"空标签", map[string]string{}, "{}"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := seriesName(tc.labels); got != tc.want {
				t.Fatalf("应为 %s，实际 %s", tc.want, got)
			}
		})
	}
}

func TestShapeResult(t *testing.T) {
	// 即时查询：vector
	var vector promResultData
	raw := `{"resultType":"vector","result":[
      {"metric":{"__name__":"up","job":"prom"},"value":[1789900000.5,"1"]},
      {"metric":{"__name__":"up","job":"node"},"value":[1789900000.5,"0"]}
    ]}`
	if err := json.Unmarshal([]byte(raw), &vector); err != nil {
		t.Fatalf("准备数据失败: %v", err)
	}
	series, truncated := shapeResult(vector)
	if truncated || len(series) != 2 {
		t.Fatalf("即时查询整形不对: %d / %v", len(series), truncated)
	}
	if series[0].Value != "1" || len(series[0].Points) != 1 {
		t.Fatalf("当前值/点数不对: %+v", series[0])
	}
	// 秒级时间戳要换成毫秒，前端直接喂 echarts
	if series[0].Points[0].At != 1789900000500 {
		t.Fatalf("时间戳应换成毫秒，实际 %d", series[0].Points[0].At)
	}

	// 范围查询：matrix，当前值取最后一个点
	var matrix promResultData
	raw = `{"resultType":"matrix","result":[
      {"metric":{"__name__":"rate"},"values":[[1789900000,"1"],[1789900060,"2"],[1789900120,"3"]]}
    ]}`
	if err := json.Unmarshal([]byte(raw), &matrix); err != nil {
		t.Fatalf("准备数据失败: %v", err)
	}
	series, _ = shapeResult(matrix)
	if len(series) != 1 || len(series[0].Points) != 3 {
		t.Fatalf("范围查询整形不对: %+v", series)
	}
	if series[0].Value != "3" {
		t.Fatalf("范围查询的当前值应取最后一个点，实际 %s", series[0].Value)
	}

	// 曲线太多要截断并如实告知
	var many promResultData
	items := make([]string, 0, metricMaxSeries+5)
	for i := 0; i < metricMaxSeries+5; i++ {
		items = append(items, fmt.Sprintf(`{"metric":{"__name__":"m","i":"%d"},"value":[1,"1"]}`, i))
	}
	if err := json.Unmarshal([]byte(`{"resultType":"vector","result":[`+
		strings.Join(items, ",")+`]}`), &many); err != nil {
		t.Fatalf("准备数据失败: %v", err)
	}
	series, truncated = shapeResult(many)
	if !truncated || len(series) != metricMaxSeries {
		t.Fatalf("超过 %d 条曲线应截断，实际 %d / %v", metricMaxSeries, len(series), truncated)
	}

	// 坏点（少一个元素 / 类型不对）不能让整条曲线丢掉
	var dirty promResultData
	raw = `{"resultType":"matrix","result":[
      {"metric":{"__name__":"m"},"values":[[1789900000],[1789900060,"2"],["bad","3"],[1789900180,4]]}
    ]}`
	if err := json.Unmarshal([]byte(raw), &dirty); err != nil {
		t.Fatalf("准备数据失败: %v", err)
	}
	series, _ = shapeResult(dirty)
	if len(series) != 1 || len(series[0].Points) != 1 || series[0].Points[0].Value != "2" {
		t.Fatalf("坏点应被跳过、好点保留: %+v", series)
	}

	// 空结果不能返回 nil，前端要能直接 .length
	empty, _ := shapeResult(promResultData{ResultType: "vector"})
	if empty == nil || len(empty) != 0 {
		t.Fatalf("空结果应为空数组，实际 %+v", empty)
	}
}

func TestResolveStep(t *testing.T) {
	// 不指定步长时按范围自动算，目标约 240 个点
	step, err := resolveStep(3600, 0)
	if err != nil || step != 15 {
		t.Fatalf("1 小时应自动取 15 秒，实际 %d / %v", step, err)
	}
	// 范围很小也不能算出 0
	if step, _ := resolveStep(60, 0); step != 1 {
		t.Fatalf("1 分钟步长应为 1 秒，实际 %d", step)
	}
	// 指定步长就照用
	if step, _ := resolveStep(3600, 60); step != 60 {
		t.Fatalf("应照用指定步长，实际 %d", step)
	}
	// 步长太小导致点数超限时，按上限反推而不是直接报错
	step, err = resolveStep(30*24*3600, 1)
	if err != nil {
		t.Fatalf("不该报错: %v", err)
	}
	if points := 30 * 24 * 3600 / step; points > metricMaxPoints {
		t.Fatalf("点数仍超限: %d（步长 %d）", points, step)
	}
	// 结束时间不晚于开始时间要报错
	if _, err := resolveStep(0, 0); err == nil {
		t.Fatal("零区间应报错")
	}
	if _, err := resolveStep(-60, 0); err == nil {
		t.Fatal("负区间应报错")
	}
}

func TestParseMetricTime(t *testing.T) {
	at, err := parseMetricTime("1789900000")
	if err != nil || at.Unix() != 1789900000 {
		t.Fatalf("Unix 秒解析失败: %v / %v", at, err)
	}
	at, err = parseMetricTime("2026-09-20T21:00:00Z")
	if err != nil || at.UTC().Hour() != 21 {
		t.Fatalf("RFC3339 解析失败: %v / %v", at, err)
	}
	for _, bad := range []string{"", "  ", "2026/09/20", "昨天"} {
		if _, err := parseMetricTime(bad); err == nil {
			t.Fatalf("%q 应被拒绝", bad)
		}
	}
}

func TestMetricSourceReqNormalize(t *testing.T) {
	req := metricSourceReq{Name: "  prom  ", BaseURL: " http://prom:9090/ "}
	if err := req.normalize(); err != nil {
		t.Fatalf("应通过: %v", err)
	}
	if req.Name != "prom" || req.BaseURL != "http://prom:9090" {
		t.Fatalf("规整结果不对: %+v", req)
	}
	if req.TimeoutSec != 15 {
		t.Fatalf("超时应回落到 15，实际 %d", req.TimeoutSec)
	}

	bad := []struct {
		name string
		req  metricSourceReq
		want string
	}{
		{"没名字", metricSourceReq{BaseURL: "http://x:9090"}, "名称不能为空"},
		{"没地址", metricSourceReq{Name: "a"}, "地址不能为空"},
		{"缺协议", metricSourceReq{Name: "a", BaseURL: "prom:9090"}, "http:// 或 https://"},
		{"把 api 路径也粘进来", metricSourceReq{Name: "a", BaseURL: "http://x:9090/api/v1/query"}, "不要带 /api/v1"},
		{"超时太小", metricSourceReq{Name: "a", BaseURL: "http://x:9090", TimeoutSec: -1}, "1 到 120 秒"},
		{"超时太大", metricSourceReq{Name: "a", BaseURL: "http://x:9090", TimeoutSec: 300}, "1 到 120 秒"},
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

// TestPromRequestErrorPassthrough 用假 Prometheus 验证信封处理：
// PromQL 写错时要把 Prometheus 自己的话带出来，警告要能透出去。
func TestPromRequestErrorPassthrough(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/ok", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Token") != "secret" {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"status":"error","errorType":"unauthorized","error":"bad token"}`)
			return
		}
		fmt.Fprint(w, `{"status":"success","data":{"resultType":"vector","result":[]},
                    "warnings":["1 series dropped"]}`)
	})
	mux.HandleFunc("/api/v1/bad", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"status":"error","errorType":"bad_data",`+
			`"error":"invalid parameter \"query\": parse error at char 4: unexpected end of input"}`)
	})
	mux.HandleFunc("/api/v1/html", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "<html>not prometheus</html>")
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	source := model.MetricSource{BaseURL: srv.URL, HeaderKey: "X-Token", HeaderValue: "secret", TimeoutSec: 5}

	var data promResultData
	warnings, err := promRequest(source, "/api/v1/ok", nil, &data)
	if err != nil {
		t.Fatalf("正常查询失败: %v", err)
	}
	if len(warnings) != 1 || warnings[0] != "1 series dropped" {
		t.Fatalf("警告没透出来: %+v", warnings)
	}

	// PromQL 错误要带上 errorType 与原文
	_, err = promRequest(source, "/api/v1/bad", nil, nil)
	if err == nil {
		t.Fatal("PromQL 错误应报错")
	}
	if !strings.Contains(err.Error(), "bad_data") || !strings.Contains(err.Error(), "parse error at char 4") {
		t.Fatalf("应带上 Prometheus 原话，实际 %v", err)
	}

	// 地址填成别的服务时要说清楚，而不是「解析失败」
	_, err = promRequest(source, "/api/v1/html", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "不是 Prometheus 格式") {
		t.Fatalf("非 Prometheus 响应的提示不对: %v", err)
	}

	// 鉴权头不对时透传 Prometheus 的说法
	_, err = promRequest(model.MetricSource{BaseURL: srv.URL, TimeoutSec: 5}, "/api/v1/ok", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "bad token") {
		t.Fatalf("鉴权失败的提示不对: %v", err)
	}

	// 地址为空要立刻报错，不发请求
	if _, err := promRequest(model.MetricSource{}, "/api/v1/ok", nil, nil); err == nil {
		t.Fatal("空地址应报错")
	}
}
