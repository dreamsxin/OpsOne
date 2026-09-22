package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"ops-platform/server/internal/config"
	"ops-platform/server/internal/model"
)

// newCloudTestHandler 建一个带真实加密（test-key）的 Handler。
// 用非空 SecretKey 是刻意的：这样 AK/SK 会真的被加密落库、同步时再解回来，
// 「加密 → 落库 → 解密 → 调 API」整条链都在测试覆盖里。
func newCloudTestHandler(t *testing.T) (*Handler, *gin.Engine) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	dir := t.TempDir()
	g, err := gorm.Open(sqlite.Open(dir+"/cloud.db"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	if err := g.AutoMigrate(
		&model.CloudAccount{}, &model.CloudResource{}, &model.CloudSyncRun{},
		&model.Host{}, &model.User{}, &model.Credential{},
	); err != nil {
		t.Fatalf("建表失败: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, err := g.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})

	h := New(g, &config.Config{SecretKey: "test-key"})
	if !h.Crypto.Enabled() {
		t.Fatal("测试期望加密是开启的")
	}

	engine := gin.New()
	engine.Use(func(c *gin.Context) {
		c.Set("ctx_user", &model.User{ID: 1, Username: "admin"})
		c.Next()
	})
	engine.POST("/cloud-accounts/:id/sync", h.RunCloudSync)
	engine.GET("/cloud-resources", h.ListCloudResources)
	engine.GET("/cloud-resources/drift", h.CloudDrift)
	engine.POST("/cloud-resources/:id/adopt", h.AdoptCloudResource)
	engine.GET("/cloud-sync-runs", h.ListCloudSyncRuns)
	return h, engine
}

func cloudJSON(t *testing.T, engine *gin.Engine, method, path, body string) (int, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	engine.ServeHTTP(rec, req)

	var parsed map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &parsed); err != nil {
		t.Fatalf("响应不是 JSON: %s", rec.Body.String())
	}
	return rec.Code, parsed
}

// cloudStub 起一个假云端，按 Action 返回预置 body。
// 通过 cloudEndpointOverride 把 handler 的出站请求接到这里 —— 不联网。
func cloudStub(t *testing.T, bodies map[string]string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		action := r.URL.Query().Get("Action")
		body, ok := bodies[action]
		if !ok {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"Code":"InvalidAction","Message":"未预置的 Action: ` + action + `"}`))
			return
		}
		// 签名参数必须齐 —— 少一个就说明客户端漏了公共参数
		for _, key := range []string{"Signature", "SignatureNonce", "Timestamp", "AccessKeyId", "Version"} {
			if r.URL.Query().Get(key) == "" {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"Code":"MissingParameter","Message":"缺少 ` + key + `"}`))
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	prev := cloudEndpointOverride
	cloudEndpointOverride = srv.URL
	t.Cleanup(func() { cloudEndpointOverride = prev })
}

// cloudErrStub 假云端固定返回一个错误
func cloudErrStub(t *testing.T, status int, body string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	prev := cloudEndpointOverride
	cloudEndpointOverride = srv.URL
	t.Cleanup(func() { cloudEndpointOverride = prev })
}

func seedCloudAccount(t *testing.T, h *Handler, region string) model.CloudAccount {
	t.Helper()
	acct := model.CloudAccount{
		Name: "阿里云-测试", Provider: "aliyun",
		AccessKeyID: "AKID-TEST", AccessKeySecret: h.sealSecret("SK-TEST"),
		Region: region, Enabled: true, CreatedBy: 1,
	}
	if err := h.DB.Create(&acct).Error; err != nil {
		t.Fatalf("建云账号失败: %v", err)
	}
	// 确认真的加密落库了，不是明文
	var raw string
	h.DB.Raw("SELECT access_key_secret FROM cloud_accounts WHERE id = ?", acct.ID).Scan(&raw)
	if raw == "SK-TEST" {
		t.Fatal("AK/SK 应当加密落库")
	}
	return acct
}

const twoInstances = `{
  "TotalCount": 2, "PageNumber": 1, "PageSize": 100,
  "Instances": { "Instance": [
    { "InstanceId": "i-aaa", "InstanceName": "web-01", "Status": "Running",
      "RegionId": "cn-hangzhou", "InstanceType": "ecs.g6.large",
      "InstanceChargeType": "PrePaid", "ExpiredTime": "2026-12-01T16:00Z",
      "VpcAttributes": { "PrivateIpAddress": { "IpAddress": ["172.16.0.10"] } },
      "PublicIpAddress": { "IpAddress": ["47.1.2.3"] } },
    { "InstanceId": "i-bbb", "InstanceName": "db-01", "Status": "Stopped",
      "RegionId": "cn-hangzhou", "InstanceType": "ecs.c6.xlarge",
      "InstanceChargeType": "PostPaid",
      "VpcAttributes": { "PrivateIpAddress": { "IpAddress": ["172.16.0.11"] } } }
  ] }
}`

const oneInstance = `{
  "TotalCount": 1, "PageNumber": 1, "PageSize": 100,
  "Instances": { "Instance": [
    { "InstanceId": "i-aaa", "InstanceName": "web-01-renamed", "Status": "Running",
      "RegionId": "cn-hangzhou", "InstanceType": "ecs.g6.large",
      "InstanceChargeType": "PrePaid", "ExpiredTime": "2026-12-01T16:00Z",
      "VpcAttributes": { "PrivateIpAddress": { "IpAddress": ["172.16.0.10"] } } }
  ] }
}`

// TestCloudSyncCreatesMatchesAndMarksGone 覆盖同步的主链路：
// 首次入库 → 按 IP 自动匹配主机 → 再次同步时云上少了一台，那台标 Gone 而不是删掉。
func TestCloudSyncCreatesMatchesAndMarksGone(t *testing.T) {
	h, engine := newCloudTestHandler(t)
	seedCloudAccount(t, h, "cn-hangzhou")

	// 平台里已纳管一台，地址正好是 i-aaa 的私网 IP
	host := model.Host{Name: "web-01", Address: "172.16.0.10", Port: 22,
		Username: "root", AuthType: "password", Status: "online", CreatedBy: 1}
	if err := h.DB.Create(&host).Error; err != nil {
		t.Fatalf("建主机失败: %v", err)
	}

	cloudStub(t, map[string]string{"DescribeInstances": twoInstances})

	code, resp := cloudJSON(t, engine, http.MethodPost,
		"/cloud-accounts/1/sync", `{"resourceType":"ecs"}`)
	if code != http.StatusOK {
		t.Fatalf("同步返回 %d: %v", code, resp)
	}

	var run model.CloudSyncRun
	h.DB.Order("id desc").First(&run)
	if run.Status != "success" {
		t.Fatalf("同步应当成功, got %s / %s", run.Status, run.Message)
	}
	if run.TotalCount != 2 || run.CreatedCount != 2 || run.UpdatedCount != 0 {
		t.Errorf("首轮计数不对: total=%d created=%d updated=%d",
			run.TotalCount, run.CreatedCount, run.UpdatedCount)
	}
	if run.MatchedCount != 1 {
		t.Errorf("应当匹配到 1 台主机, got %d", run.MatchedCount)
	}
	if run.Trigger != "manual" || run.Operator != "admin" {
		t.Errorf("触发来源记录不对: %s / %s", run.Trigger, run.Operator)
	}

	var matched model.CloudResource
	h.DB.Where("resource_id = ?", "i-aaa").First(&matched)
	if matched.MatchedHostID != host.ID || matched.MatchBy != "private_ip" {
		t.Errorf("i-aaa 应当按私网 IP 匹配到主机 %d, got %d / %s",
			host.ID, matched.MatchedHostID, matched.MatchBy)
	}
	if matched.ExpiredAt == nil {
		t.Error("包年包月实例应当有到期时间")
	}
	if matched.PublicIPs != "47.1.2.3" {
		t.Errorf("公网 IP 落库 = %q", matched.PublicIPs)
	}

	var unmatched model.CloudResource
	h.DB.Where("resource_id = ?", "i-bbb").First(&unmatched)
	if unmatched.MatchedHostID != 0 {
		t.Error("i-bbb 没有对应主机，不该匹配上")
	}
	if unmatched.ExpiredAt != nil {
		t.Error("按量付费实例不该有到期时间")
	}

	// ---- 第二轮：云上只剩 i-aaa，且改了名 ----
	cloudStub(t, map[string]string{"DescribeInstances": oneInstance})
	code, resp = cloudJSON(t, engine, http.MethodPost,
		"/cloud-accounts/1/sync", `{"resourceType":"ecs"}`)
	if code != http.StatusOK {
		t.Fatalf("二次同步返回 %d: %v", code, resp)
	}

	var run2 model.CloudSyncRun
	h.DB.Order("id desc").First(&run2)
	if run2.UpdatedCount != 1 || run2.CreatedCount != 0 || run2.GoneCount != 1 {
		t.Errorf("二轮计数不对: created=%d updated=%d gone=%d",
			run2.CreatedCount, run2.UpdatedCount, run2.GoneCount)
	}

	// i-bbb 标 Gone 而不是被删：什么时候消失的是对账要用的线索
	var goneRes model.CloudResource
	if err := h.DB.Where("resource_id = ?", "i-bbb").First(&goneRes).Error; err != nil {
		t.Fatalf("i-bbb 不该被删除: %v", err)
	}
	if !goneRes.Gone {
		t.Error("i-bbb 应当被标记为云上已消失")
	}

	var renamed model.CloudResource
	h.DB.Where("resource_id = ?", "i-aaa").First(&renamed)
	if renamed.Name != "web-01-renamed" {
		t.Errorf("云上改名应当同步过来, got %q", renamed.Name)
	}
	if renamed.Gone {
		t.Error("i-aaa 还在，不该被标 Gone")
	}

	// 总行数仍是 2（一条 Gone 一条在用），不是 1
	var total int64
	h.DB.Model(&model.CloudResource{}).Count(&total)
	if total != 2 {
		t.Errorf("云资源行数 = %d, want 2", total)
	}
}

// TestCloudSyncAuthFailureRecorded 密钥不对时：落一条 failed 记录，
// 并给出「去改云账号」而不是「重试」的提示。
func TestCloudSyncAuthFailureRecorded(t *testing.T) {
	h, engine := newCloudTestHandler(t)
	seedCloudAccount(t, h, "cn-hangzhou")
	cloudErrStub(t, http.StatusNotFound,
		`{"Code":"InvalidAccessKeyId.NotFound","Message":"not found","RequestId":"R1"}`)

	code, _ := cloudJSON(t, engine, http.MethodPost,
		"/cloud-accounts/1/sync", `{"resourceType":"ecs"}`)
	if code != http.StatusOK {
		t.Fatalf("失败的同步也应当返回 200 并带回记录, got %d", code)
	}

	var run model.CloudSyncRun
	h.DB.Order("id desc").First(&run)
	if run.Status != "failed" {
		t.Fatalf("应当记为 failed, got %s", run.Status)
	}
	if !contains(run.Message, "密钥认证失败") || !contains(run.Message, "InvalidAccessKeyId.NotFound") {
		t.Errorf("失败原因要点名密钥问题并带上云端 Code, got %q", run.Message)
	}

	var count int64
	h.DB.Model(&model.CloudResource{}).Count(&count)
	if count != 0 {
		t.Errorf("失败的同步不该写入资源, got %d 条", count)
	}
}

// TestCloudSyncNeedsRegion 账号没配地域时直接失败，不能静默只同步默认地域 ——
// 那会让「云上有、平台没有」的对照凭空少一批机器。
func TestCloudSyncNeedsRegion(t *testing.T) {
	h, engine := newCloudTestHandler(t)
	seedCloudAccount(t, h, "")

	cloudJSON(t, engine, http.MethodPost, "/cloud-accounts/1/sync", `{"resourceType":"ecs"}`)

	var run model.CloudSyncRun
	h.DB.Order("id desc").First(&run)
	if run.Status != "failed" || !contains(run.Message, "地域") {
		t.Fatalf("应当因缺地域而失败, got %s / %q", run.Status, run.Message)
	}
}

// TestCloudSyncRejectsNonAliyun 其它云只登记不同步，要照实说，不装作成功。
func TestCloudSyncRejectsNonAliyun(t *testing.T) {
	h, engine := newCloudTestHandler(t)
	acct := model.CloudAccount{Name: "腾讯云", Provider: "tencent",
		AccessKeyID: "x", AccessKeySecret: h.sealSecret("y"),
		Region: "ap-guangzhou", Enabled: true, CreatedBy: 1}
	if err := h.DB.Create(&acct).Error; err != nil {
		t.Fatalf("建云账号失败: %v", err)
	}

	cloudJSON(t, engine, http.MethodPost, "/cloud-accounts/1/sync", `{"resourceType":"ecs"}`)

	var run model.CloudSyncRun
	h.DB.Order("id desc").First(&run)
	if run.Status != "failed" || !contains(run.Message, "tencent") {
		t.Fatalf("腾讯云应当明确报「暂不支持」, got %s / %q", run.Status, run.Message)
	}
}

// TestAdoptCloudResource 纳管：用私网 IP 建主机，并回写匹配关系；重复纳管被拒。
func TestAdoptCloudResource(t *testing.T) {
	h, engine := newCloudTestHandler(t)
	seedCloudAccount(t, h, "cn-hangzhou")
	cloudStub(t, map[string]string{"DescribeInstances": twoInstances})
	cloudJSON(t, engine, http.MethodPost, "/cloud-accounts/1/sync", `{"resourceType":"ecs"}`)

	var res model.CloudResource
	h.DB.Where("resource_id = ?", "i-bbb").First(&res)

	path := "/cloud-resources/" + cloudID(res.ID) + "/adopt"
	code, resp := cloudJSON(t, engine, http.MethodPost, path,
		`{"name":"db-01","username":"root","secret":"p@ss","env":"prod"}`)
	if code != http.StatusOK {
		t.Fatalf("纳管失败 %d: %v", code, resp)
	}

	var host model.Host
	if err := h.DB.Where("address = ?", "172.16.0.11").First(&host).Error; err != nil {
		t.Fatalf("应当按私网 IP 建出主机: %v", err)
	}
	if host.Name != "db-01" || host.Username != "root" || host.Port != 22 {
		t.Errorf("主机字段不对: %+v", host)
	}
	// 口令必须加密落库
	var rawSecret string
	h.DB.Raw("SELECT secret FROM hosts WHERE id = ?", host.ID).Scan(&rawSecret)
	if rawSecret == "p@ss" {
		t.Error("纳管时填的口令应当加密落库")
	}

	h.DB.First(&res, res.ID)
	if res.MatchedHostID != host.ID || res.MatchBy != "adopt" {
		t.Errorf("纳管后应当回写匹配关系, got %d / %s", res.MatchedHostID, res.MatchBy)
	}

	// 再纳管一次要被拒
	code, _ = cloudJSON(t, engine, http.MethodPost, path,
		`{"name":"db-01-again","username":"root","secret":"p@ss"}`)
	if code != http.StatusBadRequest {
		t.Errorf("重复纳管应当被拒, got %d", code)
	}
}

// TestAdoptNeedsCredential 不给口令也不引用凭证库 → 拒绝。
// 建一台连不上的主机等于在资产表里放一条假数据。
func TestAdoptNeedsCredential(t *testing.T) {
	h, engine := newCloudTestHandler(t)
	seedCloudAccount(t, h, "cn-hangzhou")
	cloudStub(t, map[string]string{"DescribeInstances": twoInstances})
	cloudJSON(t, engine, http.MethodPost, "/cloud-accounts/1/sync", `{"resourceType":"ecs"}`)

	var res model.CloudResource
	h.DB.Where("resource_id = ?", "i-bbb").First(&res)

	code, resp := cloudJSON(t, engine, http.MethodPost,
		"/cloud-resources/"+cloudID(res.ID)+"/adopt", `{"name":"db-01","username":"root"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("没凭据应当被拒, got %d: %v", code, resp)
	}
}

// TestDriftStaysQuietBeforeFirstSync 从没成功同步过时不能把全部主机列成「云上没有」——
// 那时候云上清单本来就是空的，列出来全是噪音。
func TestDriftStaysQuietBeforeFirstSync(t *testing.T) {
	h, engine := newCloudTestHandler(t)
	h.DB.Create(&model.Host{Name: "自建-01", Address: "10.1.1.1", Port: 22,
		Username: "root", AuthType: "password", CreatedBy: 1})

	code, resp := cloudJSON(t, engine, http.MethodGet, "/cloud-resources/drift", "")
	if code != http.StatusOK {
		t.Fatalf("返回 %d", code)
	}
	data := resp["data"].(map[string]any)
	if data["syncedOnce"] != false {
		t.Error("没同步过时 syncedOnce 应当是 false")
	}
	if data["hostOnly"].(float64) != 0 {
		t.Errorf("没同步过时不该统计 hostOnly, got %v", data["hostOnly"])
	}
}

// TestDriftAfterSync 同步过之后：未匹配的云机器算 cloud_only，
// 没对应云资源的主机算 host_only。
func TestDriftAfterSync(t *testing.T) {
	h, engine := newCloudTestHandler(t)
	seedCloudAccount(t, h, "cn-hangzhou")
	// 一台匹配得上，一台是自建机房
	h.DB.Create(&model.Host{Name: "web-01", Address: "172.16.0.10", Port: 22,
		Username: "root", AuthType: "password", CreatedBy: 1})
	h.DB.Create(&model.Host{Name: "自建-01", Address: "10.1.1.1", Port: 22,
		Username: "root", AuthType: "password", CreatedBy: 1})

	cloudStub(t, map[string]string{"DescribeInstances": twoInstances})
	cloudJSON(t, engine, http.MethodPost, "/cloud-accounts/1/sync", `{"resourceType":"ecs"}`)

	_, resp := cloudJSON(t, engine, http.MethodGet, "/cloud-resources/drift", "")
	data := resp["data"].(map[string]any)
	if data["syncedOnce"] != true {
		t.Fatal("同步过之后 syncedOnce 应当是 true")
	}
	if got := data["cloudOnly"].(float64); got != 1 {
		t.Errorf("cloudOnly = %v, want 1 (i-bbb)", got)
	}
	if got := data["hostOnly"].(float64); got != 1 {
		t.Errorf("hostOnly = %v, want 1 (自建-01)", got)
	}
}

const oneDomain = `{
  "TotalCount": 1, "PageNumber": 1, "PageSize": 100,
  "Domains": { "Domain": [
    { "DomainId": "d-1", "DomainName": "example.com", "VersionName": "免费版",
      "RecordCount": 12, "AliDomain": true,
      "DnsServers": { "DnsServer": ["ns1.alidns.com"] } }
  ] }
}`

// TestCloudSyncDomains 域名同步落成 domain 类型的资源，状态是 hosted、没有到期时间。
// 后者是刻意的：云解析接口给不出**注册**到期时间，编一个出来就是假数据。
func TestCloudSyncDomains(t *testing.T) {
	h, engine := newCloudTestHandler(t)
	seedCloudAccount(t, h, "cn-hangzhou")
	cloudStub(t, map[string]string{"DescribeDomains": oneDomain})

	cloudJSON(t, engine, http.MethodPost, "/cloud-accounts/1/sync", `{"resourceType":"domain"}`)

	var run model.CloudSyncRun
	h.DB.Order("id desc").First(&run)
	if run.Status != "success" || run.CreatedCount != 1 {
		t.Fatalf("域名同步应当成功且新增 1 条, got %s / %d / %s",
			run.Status, run.CreatedCount, run.Message)
	}

	var res model.CloudResource
	h.DB.Where("resource_type = ?", "domain").First(&res)
	if res.ResourceID != "example.com" || res.Status != "hosted" {
		t.Errorf("域名资源落库不对: %+v", res)
	}
	if res.ExpiredAt != nil {
		t.Error("云解析接口给不出注册到期时间，这里必须是空")
	}
	if !contains(res.Extra, "recordCount") {
		t.Errorf("解析记录数应当留在 Extra 里: %s", res.Extra)
	}
}

// TestListCloudResourcesFilters 清单页的过滤条件真的生效（不是摆设）。
func TestListCloudResourcesFilters(t *testing.T) {
	h, engine := newCloudTestHandler(t)
	seedCloudAccount(t, h, "cn-hangzhou")
	cloudStub(t, map[string]string{"DescribeInstances": twoInstances})
	cloudJSON(t, engine, http.MethodPost, "/cloud-accounts/1/sync", `{"resourceType":"ecs"}`)

	_, resp := cloudJSON(t, engine, http.MethodGet,
		"/cloud-resources?status=Stopped", "")
	page := resp["data"].(map[string]any)
	if page["total"].(float64) != 1 {
		t.Errorf("按状态过滤 total = %v, want 1", page["total"])
	}

	_, resp = cloudJSON(t, engine, http.MethodGet,
		"/cloud-resources?keyword="+url.QueryEscape("172.16.0.11"), "")
	page = resp["data"].(map[string]any)
	if page["total"].(float64) != 1 {
		t.Errorf("按 IP 关键字过滤 total = %v, want 1", page["total"])
	}
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }

func cloudID(v uint) string { return strconv.FormatUint(uint64(v), 10) }
