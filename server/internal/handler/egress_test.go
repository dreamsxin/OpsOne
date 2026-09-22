package handler

// 统一出口代理的测试。
//
// 这组测试的重点不是「调用了设置 Proxy 的那行代码」，而是**报文真的经过了代理**：
// 用一个真的正向代理（startFakeProxy，校验 r.URL.IsAbs()）数命中次数，
// 再数目标服务被打到的次数。两个计数放在一起才能区分
// 「走了代理」「直连了」「谁都没连上」三种情况。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"ops-platform/server/internal/config"
	"ops-platform/server/internal/model"
)

func newEgressTestHandler(t *testing.T) (*Handler, *gin.Engine) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	dir := t.TempDir()
	g, err := gorm.Open(sqlite.Open(dir+"/egress.db"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	if err := g.AutoMigrate(
		&model.EgressProxy{}, &model.SysConfig{}, &model.NotifyChannel{}, &model.User{},
	); err != nil {
		t.Fatalf("建表失败: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, err := g.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	// 两项配置是内置键，测试里手动建出来（生产由 seed 负责）
	g.Create(&model.SysConfig{Group: "proxy", Key: CfgProxyEgressID, Value: "0", Type: "int", Builtin: true})
	g.Create(&model.SysConfig{Group: "proxy", Key: CfgProxyBypass, Value: model.DefaultProxyBypass, Type: "string", Builtin: true})

	h := New(g, &config.Config{})
	engine := gin.New()
	engine.Use(func(c *gin.Context) {
		c.Set("ctx_user", &model.User{ID: 1, Username: "admin"})
		c.Next()
	})
	engine.GET("/network/proxy-egress", h.GetEgressPolicy)
	engine.PUT("/network/proxy-egress", h.UpdateEgressPolicy)
	return h, engine
}

// setEgress 直接写配置，绕过校验接口（校验本身另有测试）
func setEgress(t *testing.T, h *Handler, proxyID uint, bypass string) {
	t.Helper()
	if err := h.DB.Model(&model.SysConfig{}).Where("`key` = ?", CfgProxyEgressID).
		Update("value", proxyID).Error; err != nil {
		t.Fatalf("写 %s 失败: %v", CfgProxyEgressID, err)
	}
	if err := h.DB.Model(&model.SysConfig{}).Where("`key` = ?", CfgProxyBypass).
		Update("value", bypass).Error; err != nil {
		t.Fatalf("写 %s 失败: %v", CfgProxyBypass, err)
	}
}

func TestEgressBypassParsing(t *testing.T) {
	rules, bad := parseEgressBypass(
		"localhost, 10.0.0.0/8 ,.internal,*.corp.example.com,prom01, ,10.0.0.0/99,a b,.")
	if len(bad) != 3 {
		t.Fatalf("应该有 3 条解析不了（坏网段 / 带空格 / 单个点），实际 %v", bad)
	}
	if len(rules) != 5 {
		t.Fatalf("应该解析出 5 条规则，实际 %d", len(rules))
	}
	// *.corp.example.com 与 .corp.example.com 是同一个意思
	var gotSuffix bool
	for _, rule := range rules {
		if rule.Suffix == ".corp.example.com" {
			gotSuffix = true
		}
	}
	if !gotSuffix {
		t.Fatal("*.corp.example.com 应该归一成后缀 .corp.example.com")
	}
}

func TestEgressBypassMatch(t *testing.T) {
	rules, bad := parseEgressBypass(model.DefaultProxyBypass)
	if len(bad) > 0 {
		t.Fatalf("默认清单自己必须能全部解析，坏条目：%v", bad)
	}
	cases := []struct {
		host string
		want bool
		why  string
	}{
		{"127.0.0.1", true, "本机在默认清单里"},
		{"10.20.30.40", true, "私有网段"},
		{"192.168.1.10", true, "私有网段"},
		{"172.20.0.5", true, "172.16/12 内"},
		{"172.32.0.5", false, "172.32 不在 172.16/12 内"},
		{"prom.internal", true, ".internal 后缀"},
		{"loki.monitoring.svc", true, ".svc 后缀"},
		{"localhost", true, "精确匹配"},
		{"ecs.cn-hangzhou.aliyuncs.com", false, "公网域名不该被跳过"},
		{"8.8.8.8", false, "公网 IP 不该被跳过"},
		{"", false, "空主机名不匹配任何规则"},
	}
	for _, tc := range cases {
		got, rule := matchEgressBypass(rules, tc.host)
		if got != tc.want {
			t.Fatalf("%s（%s）期望 %v，实际 %v（命中 %q）", tc.host, tc.why, tc.want, got, rule)
		}
	}
}

// 走统一出口的请求要真的经过代理
func TestEgressRoutesWebhookThroughProxy(t *testing.T) {
	h, _ := newEgressTestHandler(t)
	proxySrv, proxyHits := startFakeProxy(t)
	target, targetHits := startEchoTarget(t)

	host, port := hostPortOf(t, proxySrv.URL)
	if err := h.DB.Create(&model.EgressProxy{
		ID: 1, Name: "出口", Scheme: "http", Host: host, Port: port,
		Enabled: true, LastStatus: "ok",
	}).Error; err != nil {
		t.Fatalf("建代理失败: %v", err)
	}
	// 目标是 127.0.0.1，默认清单会跳过它 —— 这里换成一个只放公网后缀的清单
	setEgress(t, h, 1, ".example.com")

	status, err := h.postJSON(model.NotifyChannel{Type: "webhook", URL: target.URL}, map[string]any{"text": "hi"})
	if err != nil || status != 200 {
		t.Fatalf("webhook 应该发成功: status=%d err=%v", status, err)
	}
	if *proxyHits != 1 {
		t.Fatalf("请求应该经过代理一次，实际 %d —— 统一出口没生效", *proxyHits)
	}
	if *targetHits != 1 {
		t.Fatalf("目标应该被代理转发到一次，实际 %d", *targetHits)
	}
}

// 命中 bypass 的目标必须直连，否则内网数据源会被送去代理
func TestEgressBypassGoesDirect(t *testing.T) {
	h, _ := newEgressTestHandler(t)
	proxySrv, proxyHits := startFakeProxy(t)
	target, targetHits := startEchoTarget(t)

	host, port := hostPortOf(t, proxySrv.URL)
	h.DB.Create(&model.EgressProxy{
		ID: 1, Name: "出口", Scheme: "http", Host: host, Port: port,
		Enabled: true, LastStatus: "ok",
	})
	// 默认清单含 127.0.0.0/8，httptest 的目标正好在里面
	setEgress(t, h, 1, model.DefaultProxyBypass)

	if _, err := h.postJSON(model.NotifyChannel{Type: "webhook", URL: target.URL}, map[string]any{"text": "hi"}); err != nil {
		t.Fatalf("命中 bypass 时应该直连成功: %v", err)
	}
	if *proxyHits != 0 {
		t.Fatalf("命中 bypass 的目标不该走代理，代理却被打了 %d 次", *proxyHits)
	}
	if *targetHits != 1 {
		t.Fatalf("目标应该被直连打到一次，实际 %d", *targetHits)
	}
}

// 配了统一出口但代理停用/删除：显式失败，绝不退回直连
func TestEgressFailsInsteadOfSilentDirect(t *testing.T) {
	h, _ := newEgressTestHandler(t)
	proxySrv, proxyHits := startFakeProxy(t)
	target, targetHits := startEchoTarget(t)

	host, port := hostPortOf(t, proxySrv.URL)
	h.DB.Create(&model.EgressProxy{
		ID: 1, Name: "出口", Scheme: "http", Host: host, Port: port,
		Enabled: false, LastStatus: "ok",
	})
	setEgress(t, h, 1, ".example.com")

	_, err := h.postJSON(model.NotifyChannel{Type: "webhook", URL: target.URL}, map[string]any{"text": "hi"})
	if err == nil {
		t.Fatal("统一出口被停用时必须报错")
	}
	if !strings.Contains(err.Error(), "停用") {
		t.Fatalf("错误要点名是代理停用了，实际: %v", err)
	}
	if *targetHits != 0 {
		t.Fatal("代理停用却直连打了目标 —— 这会把「代理挂了」显示成「目标一切正常」")
	}
	if *proxyHits != 0 {
		t.Fatalf("停用的代理不该被使用，实际 %d", *proxyHits)
	}

	h.DB.Delete(&model.EgressProxy{}, 1)
	_, err = h.postJSON(model.NotifyChannel{Type: "webhook", URL: target.URL}, map[string]any{"text": "hi"})
	if err == nil || !strings.Contains(err.Error(), "不存在") {
		t.Fatalf("统一出口被删时应该报不存在，实际: %v", err)
	}
	if *targetHits != 0 {
		t.Fatal("代理被删却直连打了目标")
	}
}

// 没配统一出口：行为与以前一致（不强制直连，仍然遵守环境变量）
func TestEgressUnconfiguredKeepsOldBehaviour(t *testing.T) {
	h, _ := newEgressTestHandler(t)
	target, targetHits := startEchoTarget(t)

	item, err := h.egressProxy()
	if err != nil || item != nil {
		t.Fatalf("没配统一出口时应该返回 (nil, nil)，实际 item=%v err=%v", item, err)
	}
	if _, err := h.postJSON(model.NotifyChannel{Type: "webhook", URL: target.URL}, map[string]any{"text": "hi"}); err != nil {
		t.Fatalf("没配统一出口时不该影响出网: %v", err)
	}
	if *targetHits != 1 {
		t.Fatalf("目标应该被打到一次，实际 %d", *targetHits)
	}
}

func TestEgressPolicySurfacesCoverageAndEnv(t *testing.T) {
	_, engine := newEgressTestHandler(t)
	t.Setenv("HTTPS_PROXY", "http://gateway.example.com:3128")

	code, body, raw := proxyJSON(t, engine, "GET", "/network/proxy-egress", "")
	if code != 200 {
		t.Fatalf("取出口口径失败: %s", raw)
	}
	data, ok := body["data"].(map[string]any)
	if !ok {
		t.Fatalf("响应结构不对: %s", raw)
	}
	if data["configured"] != false {
		t.Fatal("测试库里默认没配统一出口")
	}
	if len(data["covered"].([]any)) == 0 || len(data["notCovered"].([]any)) == 0 {
		t.Fatal("走代理与不走代理两张清单都必须给出来")
	}
	env := data["env"].(map[string]any)
	if env["HTTPS_PROXY"] != "http://gateway.example.com:3128" {
		t.Fatalf("要摊出进程实际看到的环境变量，实际 %v", env)
	}
	// 没配统一出口时必须明说「仍然遵守环境变量」，否则页面读起来像「完全不走代理」
	if !strings.Contains(raw, "HTTP_PROXY") || !strings.Contains(raw, "没有配统一出口") {
		t.Fatalf("未配时的说明不到位: %s", raw)
	}
	// 不走代理的清单里必须点名拨测，说明「按条指定」没被全局出口覆盖
	if !strings.Contains(raw, "拨测") {
		t.Fatal("notCovered 里要写明拨测不受统一出口影响")
	}
}

// 设统一出口前先卡住「停用的」和「没检测过的」
func TestEgressPolicyRejectsUnverifiedProxy(t *testing.T) {
	h, engine := newEgressTestHandler(t)
	h.DB.Create(&model.EgressProxy{ID: 1, Name: "没测过", Scheme: "http", Host: "10.0.0.9", Port: 3128, Enabled: true, LastStatus: "unknown"})
	h.DB.Create(&model.EgressProxy{ID: 2, Name: "停用的", Scheme: "http", Host: "10.0.0.8", Port: 3128, Enabled: false, LastStatus: "ok"})
	h.DB.Create(&model.EgressProxy{ID: 3, Name: "可用的", Scheme: "http", Host: "10.0.0.7", Port: 3128, Enabled: true, LastStatus: "ok"})

	code, _, raw := proxyJSON(t, engine, "PUT", "/network/proxy-egress", `{"proxyId":1}`)
	if code != 400 || !strings.Contains(raw, "检测") {
		t.Fatalf("没有成功检测记录的代理不该被设成出口: %d %s", code, raw)
	}
	code, _, raw = proxyJSON(t, engine, "PUT", "/network/proxy-egress", `{"proxyId":2}`)
	if code != 400 || !strings.Contains(raw, "停用") {
		t.Fatalf("停用的代理不该被设成出口: %d %s", code, raw)
	}
	code, _, raw = proxyJSON(t, engine, "PUT", "/network/proxy-egress", `{"proxyId":9}`)
	if code != 400 {
		t.Fatalf("不存在的代理不该被接受: %d %s", code, raw)
	}
	code, _, raw = proxyJSON(t, engine, "PUT", "/network/proxy-egress", `{"proxyId":3,"bypass":"10.0.0.0/99"}`)
	if code != 400 || !strings.Contains(raw, "解析不了") {
		t.Fatalf("坏 bypass 条目要被顶回来: %d %s", code, raw)
	}

	code, _, raw = proxyJSON(t, engine, "PUT", "/network/proxy-egress", `{"proxyId":3,"bypass":".corp.example.com"}`)
	if code != 200 {
		t.Fatalf("可用的代理应该能设成出口: %d %s", code, raw)
	}
	item, err := h.egressProxy()
	if err != nil || item == nil || item.ID != 3 {
		t.Fatalf("保存后应该能读回 3 号，实际 item=%v err=%v", item, err)
	}
	// 清空也要能回到「不统一出口」
	if code, _, raw = proxyJSON(t, engine, "PUT", "/network/proxy-egress", `{"proxyId":0}`); code != 200 {
		t.Fatalf("取消统一出口失败: %d %s", code, raw)
	}
	if item, err = h.egressProxy(); err != nil || item != nil {
		t.Fatalf("取消后应该回到 (nil, nil)，实际 item=%v err=%v", item, err)
	}
}

// egressExemptClients 允许自己构造 http.Client 的文件，以及为什么。
//
// 这条测试是给「以后新加一个出网功能」的人写的：直接 `&http.Client{...}` 会静默地
// 绕过统一出口，表现是「内网环境下这个功能连不出去，而代理页面上说它应该走代理」。
// 那种不一致只能靠人记得，记不住就会漂 —— 所以让它编译能过但测试失败。
var egressExemptClients = map[string]string{
	"egress.go":       "统一出口 client 的工厂本身",
	"proxy.go":        "代理检测：显式指定某条代理 + 直连对照那一次",
	"probe.go":        "HTTP 拨测按条指定代理，proxyId=0 就是要直连",
	"metric.go":       "Prometheus 是内网数据源，不走出口代理",
	"log_query.go":    "Loki 是内网数据源，不走出口代理",
	"trace.go":        "Jaeger 是内网数据源，不走出口代理",
	"im_directory.go": "client 由调用方注入，nil 只在测试里兜底",
}

func TestNoUnaccountedHTTPClients(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("列文件失败: %v", err)
	}
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("读 %s 失败: %v", file, err)
		}
		for i, line := range strings.Split(string(raw), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "//") || !strings.Contains(line, "http.Client{") {
				continue
			}
			if _, ok := egressExemptClients[filepath.Base(file)]; ok {
				continue
			}
			t.Fatalf("%s:%d 自己构造了 http.Client：\n\t%s\n"+
				"出网请用 h.egressClient(timeout)，这样它才会走统一出口代理。\n"+
				"确实不该走代理（内网数据源、非 HTTP、按条指定代理）的话，"+
				"把文件加进 egressExemptClients 并写明原因，同时更新 egressNotCovered 清单",
				file, i+1, trimmed)
		}
	}
	// 豁免清单本身别过期：列了却已经不构造 client 的文件要删掉
	for file := range egressExemptClients {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("豁免清单里的 %s 已经不存在了，请删掉这一条", file)
		}
		if !strings.Contains(string(raw), "http.Client{") {
			t.Fatalf("%s 已经不自己构造 http.Client 了，请从 egressExemptClients 里删掉", file)
		}
	}
}
