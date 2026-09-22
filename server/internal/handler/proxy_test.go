package handler

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"ops-platform/server/internal/config"
	"ops-platform/server/internal/cryptox"
	"ops-platform/server/internal/model"
)

// ---------- 一个真的正向代理 ----------
//
// http.Transport.Proxy 对明文 HTTP 目标会把绝对 URI 发给代理（GET http://host/path），
// 所以一个普通的 httptest server 就能当代理用。这样测的是**真的走了代理**，
// 而不是「我们调用了设置 Proxy 的那行代码」。

func startFakeProxy(t *testing.T) (*httptest.Server, *int32) {
	t.Helper()
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !r.URL.IsAbs() {
			http.Error(w, "这不是一个代理请求", http.StatusBadRequest)
			return
		}
		atomic.AddInt32(&hits, 1)
		out, err := http.NewRequest(r.Method, r.URL.String(), r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		out.Header.Set("X-Via-Proxy", "1")
		resp, err := http.DefaultTransport.RoundTrip(out)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

// startEchoTarget 目标服务：按是否经过代理回显不同的「来源 IP」，
// 用来验证出口 IP 对比那段逻辑
func startEchoTarget(t *testing.T) (*httptest.Server, *int32) {
	t.Helper()
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		ip := "198.51.100.5"
		if r.Header.Get("X-Via-Proxy") != "" {
			ip = "203.0.113.9"
		}
		_, _ = w.Write([]byte(`{"origin":"` + ip + `","ok":true}`))
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func hostPortOf(t *testing.T, rawURL string) (string, int) {
	t.Helper()
	trimmed := strings.TrimPrefix(rawURL, "http://")
	parts := strings.Split(trimmed, ":")
	if len(parts) != 2 {
		t.Fatalf("地址解析失败: %s", rawURL)
	}
	port, err := strconv.Atoi(parts[1])
	if err != nil {
		t.Fatalf("端口解析失败: %s", rawURL)
	}
	return parts[0], port
}

func newProxyTestHandler(t *testing.T, secretKey string) (*Handler, *gin.Engine) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	dir := t.TempDir()
	g, err := gorm.Open(sqlite.Open(dir+"/proxy.db"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	if err := g.AutoMigrate(
		&model.EgressProxy{}, &model.Probe{}, &model.ProbeRecord{},
		&model.Alert{}, &model.AlertSource{}, &model.SysConfig{}, &model.User{},
	); err != nil {
		t.Fatalf("建表失败: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, err := g.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})

	h := New(g, &config.Config{SecretKey: secretKey})
	engine := gin.New()
	engine.Use(func(c *gin.Context) {
		c.Set("ctx_user", &model.User{ID: 1, Username: "admin"})
		c.Next()
	})
	engine.GET("/network/proxies", h.ListEgressProxies)
	engine.POST("/network/proxies", h.CreateEgressProxy)
	engine.PUT("/network/proxies/:id", h.UpdateEgressProxy)
	engine.DELETE("/network/proxies/:id", h.DeleteEgressProxy)
	engine.POST("/network/proxies/:id/check", h.CheckEgressProxy)
	return h, engine
}

func proxyJSON(t *testing.T, engine *gin.Engine, method, path, body string) (int, map[string]any, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	engine.ServeHTTP(rec, req)
	var parsed map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &parsed); err != nil {
		t.Fatalf("响应不是 JSON: %s", rec.Body.String())
	}
	return rec.Code, parsed, rec.Body.String()
}

// 检测要直连一次、走代理一次，并给出一句能照着做的结论
func TestProxyCheckComparesDirectAndProxy(t *testing.T) {
	target, targetHits := startEchoTarget(t)
	proxySrv, proxyHits := startFakeProxy(t)
	host, port := hostPortOf(t, proxySrv.URL)

	h, engine := newProxyTestHandler(t, "")
	h.DB.Create(&model.EgressProxy{Name: "出口代理", Scheme: "http", Host: host, Port: port,
		Enabled: true, LastStatus: "unknown"})

	code, parsed, raw := proxyJSON(t, engine, http.MethodPost, "/network/proxies/1/check",
		`{"testUrl":"`+target.URL+`"}`)
	if code != 200 {
		t.Fatalf("检测失败: %s", raw)
	}
	data := parsed["data"].(map[string]any)
	direct := data["direct"].(map[string]any)
	via := data["viaProxy"].(map[string]any)

	if direct["ok"] != true || via["ok"] != true {
		t.Fatalf("两边都该通: %s", raw)
	}
	if atomic.LoadInt32(proxyHits) != 1 {
		t.Fatalf("代理没有被真的用到，命中 %d 次", atomic.LoadInt32(proxyHits))
	}
	if atomic.LoadInt32(targetHits) != 2 {
		t.Fatalf("目标应该被打两次（直连 + 走代理），实际 %d", atomic.LoadInt32(targetHits))
	}
	if !strings.Contains(data["verdict"].(string), "直连也通") {
		t.Fatalf("结论不对: %v", data["verdict"])
	}
	// 出口 IP 对比：直连与走代理读到的来源 IP 不同，要照实说变了
	if direct["exitIp"] != "198.51.100.5" || via["exitIp"] != "203.0.113.9" {
		t.Fatalf("出口 IP 没读出来: %s", raw)
	}
	note, _ := data["exitIPNote"].(string)
	if !strings.Contains(note, "出口 IP 确实变了") {
		t.Fatalf("出口变化的说明不对: %v", note)
	}

	var item model.EgressProxy
	h.DB.First(&item, 1)
	if item.LastStatus != "ok" || item.ExitIP != "203.0.113.9" || item.LastCheckAt == nil {
		t.Fatalf("检测痕迹没落库: %+v", item)
	}
}

// 代理不通但直连通：结论必须指向代理本身，不能让人去查网络
func TestProxyCheckSeparatesProxyFailureFromNetwork(t *testing.T) {
	target, _ := startEchoTarget(t)
	h, engine := newProxyTestHandler(t, "")
	// 指向一个没人监听的端口
	h.DB.Create(&model.EgressProxy{Name: "坏代理", Scheme: "http", Host: "127.0.0.1", Port: 1,
		Enabled: true, LastStatus: "unknown"})

	_, parsed, raw := proxyJSON(t, engine, http.MethodPost, "/network/proxies/1/check",
		`{"testUrl":"`+target.URL+`"}`)
	data := parsed["data"].(map[string]any)
	if data["viaProxy"].(map[string]any)["ok"] != false {
		t.Fatalf("走代理应该失败: %s", raw)
	}
	if data["direct"].(map[string]any)["ok"] != true {
		t.Fatalf("直连应该成功: %s", raw)
	}
	if !strings.Contains(data["verdict"].(string), "问题在代理本身") {
		t.Fatalf("结论应该指向代理: %v", data["verdict"])
	}

	var item model.EgressProxy
	h.DB.First(&item, 1)
	if item.LastStatus != "fail" || item.LastError == "" {
		t.Fatalf("失败原因没记下: %+v", item)
	}
}

// 没有测试地址时照实拒绝，并说明为什么不预设一个
func TestProxyCheckRequiresTestURL(t *testing.T) {
	h, engine := newProxyTestHandler(t, "")
	h.DB.Create(&model.EgressProxy{Name: "代理", Scheme: "http", Host: "127.0.0.1", Port: 3128,
		Enabled: true})

	code, parsed, _ := proxyJSON(t, engine, http.MethodPost, "/network/proxies/1/check", `{}`)
	if code != 400 {
		t.Fatalf("没有测试地址应该被拒，实际 %d", code)
	}
	msg, _ := parsed["msg"].(string)
	if !strings.Contains(msg, "proxy.test_url") || !strings.Contains(msg, "公网") {
		t.Fatalf("错误信息要说清为什么不预设: %v", msg)
	}

	// 非 http(s) 地址拒绝
	if code, _, _ := proxyJSON(t, engine, http.MethodPost, "/network/proxies/1/check",
		`{"testUrl":"ftp://x/y"}`); code != 400 {
		t.Fatalf("非 http 地址应该被拒，实际 %d", code)
	}

	// 配置项里有地址时就不用每次传
	h.DB.Create(&model.SysConfig{Group: "proxy", Key: CfgProxyTestURL,
		Value: "http://127.0.0.1:1/", Type: "string"})
	if code, _, raw := proxyJSON(t, engine, http.MethodPost, "/network/proxies/1/check", `{}`); code != 200 {
		t.Fatalf("配置里有地址时应该能检测: %d %s", code, raw)
	}
}

// 拨测真的走代理：目标看到的是代理转发过来的请求
func TestProbeGoesThroughProxy(t *testing.T) {
	target, targetHits := startEchoTarget(t)
	proxySrv, proxyHits := startFakeProxy(t)
	host, port := hostPortOf(t, proxySrv.URL)

	h, _ := newProxyTestHandler(t, "")
	h.DB.Create(&model.EgressProxy{Name: "出口代理", Scheme: "http", Host: host, Port: port,
		Enabled: true, LastStatus: "unknown"})

	probe := model.Probe{
		Name: "走代理的拨测", Type: "http", Target: target.URL, Method: http.MethodGet,
		ExpectStatus: 200, ExpectKeyword: "203.0.113.9", TimeoutSec: 5,
		ProxyID: 1, Enabled: true,
	}
	result := h.runProbeWithProxy(probe)
	if result.Status != "up" {
		t.Fatalf("拨测应该通: %+v", result)
	}
	if atomic.LoadInt32(proxyHits) != 1 {
		t.Fatalf("代理没被用到: %d", atomic.LoadInt32(proxyHits))
	}
	if atomic.LoadInt32(targetHits) != 1 {
		t.Fatalf("目标应该只被打一次: %d", atomic.LoadInt32(targetHits))
	}

	// 关键字断言的是「代理转发过来」那个分支的响应，等于验证了确实经过代理
	probe.ProxyID = 0
	direct := h.runProbeWithProxy(probe)
	if direct.Status != "down" {
		t.Fatal("不走代理时目标回显的 IP 不同，关键字应该匹配不上 —— 说明上面那次并没有真的走代理")
	}
}

// 代理被停用 / 删除时拨测直接失败并点名原因，绝不静默改成直连
func TestProbeFailsInsteadOfSilentDirect(t *testing.T) {
	target, targetHits := startEchoTarget(t)
	proxySrv, _ := startFakeProxy(t)
	host, port := hostPortOf(t, proxySrv.URL)

	h, _ := newProxyTestHandler(t, "")
	h.DB.Create(&model.EgressProxy{Name: "出口代理", Scheme: "http", Host: host, Port: port,
		Enabled: false, LastStatus: "unknown"})

	probe := model.Probe{
		Name: "走代理的拨测", Type: "http", Target: target.URL, Method: http.MethodGet,
		TimeoutSec: 5, ProxyID: 1, Enabled: true,
	}
	result := h.runProbeWithProxy(probe)
	if result.Status != "down" {
		t.Fatalf("代理停用时拨测应该失败: %+v", result)
	}
	if !strings.Contains(result.ErrorMsg, "出口代理") || !strings.Contains(result.ErrorMsg, "停用") {
		t.Fatalf("失败原因要点名是代理的问题: %s", result.ErrorMsg)
	}
	if atomic.LoadInt32(targetHits) != 0 {
		t.Fatal("代理不可用却直连打了目标 —— 这会把「代理挂了」显示成「目标正常」")
	}

	h.DB.Delete(&model.EgressProxy{}, 1)
	result = h.runProbeWithProxy(probe)
	if result.Status != "down" || !strings.Contains(result.ErrorMsg, "不存在") {
		t.Fatalf("代理被删时应该报不存在: %+v", result)
	}
	if atomic.LoadInt32(targetHits) != 0 {
		t.Fatal("代理被删却直连打了目标")
	}
}

// TCP 拨测不接代理：HTTP 代理测不了任意端口的可用性
func TestTCPProbeDropsProxy(t *testing.T) {
	req := probeReq{Name: "库", Type: "tcp", Target: "127.0.0.1:3306", ProxyID: 3}
	if err := req.normalize(); err != nil {
		t.Fatalf("归一化失败: %v", err)
	}
	if req.ProxyID != 0 {
		t.Fatalf("TCP 拨测的代理应该被清掉，实际 %d", req.ProxyID)
	}
}

// 被拨测引用的代理拒绝删除
func TestProxyDeleteBlockedByProbe(t *testing.T) {
	h, engine := newProxyTestHandler(t, "")
	h.DB.Create(&model.EgressProxy{Name: "代理", Scheme: "http", Host: "127.0.0.1", Port: 3128,
		Enabled: true})
	h.DB.Create(&model.Probe{Name: "拨测", Type: "http", Target: "http://x/", ProxyID: 1})

	code, parsed, _ := proxyJSON(t, engine, http.MethodDelete, "/network/proxies/1", "")
	if code != 400 {
		t.Fatalf("被引用时应该拒绝删除，实际 %d", code)
	}
	if msg, _ := parsed["msg"].(string); !strings.Contains(msg, "拨测") {
		t.Fatalf("错误信息要说清原因: %v", msg)
	}

	_, parsed, raw := proxyJSON(t, engine, http.MethodGet, "/network/proxies", "")
	data := parsed["data"].(map[string]any)
	rows := data["list"].([]any)
	if rows[0].(map[string]any)["probeCount"] != float64(1) {
		t.Fatalf("引用数不对: %s", raw)
	}
	notes, _ := data["notes"].([]any)
	if len(notes) < 5 {
		t.Fatalf("口径说明至少 5 条，实际 %d", len(notes))
	}
}

// 代理口令加密落库、不出接口，拼进代理地址时能解回来
func TestProxyPasswordSealed(t *testing.T) {
	h, engine := newProxyTestHandler(t, "unit-test-key")
	code, _, raw := proxyJSON(t, engine, http.MethodPost, "/network/proxies",
		`{"name":"认证代理","scheme":"socks5","host":"10.0.0.9","port":1080,"username":"pu","password":"pp"}`)
	if code != 200 {
		t.Fatalf("创建失败: %s", raw)
	}

	var item model.EgressProxy
	h.DB.First(&item, 1)
	if !cryptox.IsSealed(item.Password) {
		t.Fatalf("口令没加密: %s", item.Password)
	}
	_, _, listRaw := proxyJSON(t, engine, http.MethodGet, "/network/proxies", "")
	if strings.Contains(listRaw, `"pp"`) {
		t.Fatalf("列表泄露了口令: %s", listRaw)
	}

	u, err := h.proxyURL(item)
	if err != nil {
		t.Fatalf("拼代理地址失败: %v", err)
	}
	if u.Scheme != "socks5" || u.Host != "10.0.0.9:1080" {
		t.Fatalf("代理地址不对: %s", u.String())
	}
	pass, ok := u.User.Password()
	if !ok || pass != "pp" || u.User.Username() != "pu" {
		t.Fatalf("认证信息不对: %s", u.Redacted())
	}

	// 编辑留空 = 不改口令
	proxyJSON(t, engine, http.MethodPut, "/network/proxies/1",
		`{"name":"认证代理","scheme":"socks5","host":"10.0.0.9","port":1081,"username":"pu"}`)
	var after model.EgressProxy
	h.DB.First(&after, 1)
	if after.Password != item.Password || after.Port != 1081 {
		t.Fatalf("编辑结果不对: %+v", after)
	}
}

// 参数校验 + GORM 布尔陷阱
func TestProxyValidationAndEnabledFlag(t *testing.T) {
	h, engine := newProxyTestHandler(t, "")
	if code, parsed, _ := proxyJSON(t, engine, http.MethodPost, "/network/proxies",
		`{"name":"a","scheme":"ftp","host":"h","port":8080}`); code != 400 {
		t.Fatalf("非法 scheme 应该被拒，实际 %d %v", code, parsed["msg"])
	}
	if code, _, _ := proxyJSON(t, engine, http.MethodPost, "/network/proxies",
		`{"name":"a","scheme":"http","host":"h","port":70000}`); code != 400 {
		t.Fatal("非法端口应该被拒")
	}

	proxyJSON(t, engine, http.MethodPost, "/network/proxies",
		`{"name":"停用的","scheme":"http","host":"h","port":3128,"enabled":false}`)
	var item model.EgressProxy
	h.DB.First(&item, 1)
	if item.Enabled {
		t.Fatal("创建时 enabled=false 被数据库默认值翻成了 true（gorm default 陷阱）")
	}
}

// 这一轮新增的两列密钥必须登记进加密体检清单
func TestRound8SecretFieldsRegistered(t *testing.T) {
	want := map[string]string{
		"mail_accounts":  "password",
		"egress_proxies": "password",
	}
	for table, column := range want {
		found := false
		for _, field := range secretFields {
			if field.Table == table && field.Column == column {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("%s.%s 没登记到 secretFields，加密体检会漏报", table, column)
		}
	}
}
