package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"ops-platform/server/internal/config"
	"ops-platform/server/internal/dnsx"
	"ops-platform/server/internal/model"
)

func newDomainTestHandler(t *testing.T) (*Handler, *gin.Engine) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	dir := t.TempDir()
	g, err := gorm.Open(sqlite.Open(dir+"/domain.db"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	// 告警链路要用到的表一并建出来：域名巡检会走 ingestAlert
	if err := g.AutoMigrate(
		&model.Domain{}, &model.Certificate{}, &model.CloudResource{},
		&model.Alert{}, &model.AlertSource{}, &model.AlertSilence{},
		&model.AggregationPolicy{}, &model.NotifyChannel{}, &model.NotifyRoute{},
		&model.NotifyRecord{}, &model.Message{}, &model.User{},
		&model.Role{}, &model.Menu{},
	); err != nil {
		t.Fatalf("建表失败: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, err := g.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})

	h := New(g, &config.Config{})
	engine := gin.New()
	engine.Use(func(c *gin.Context) {
		c.Set("ctx_user", &model.User{ID: 1, Username: "admin"})
		c.Next()
	})
	engine.GET("/domains", h.ListDomains)
	engine.GET("/domains/stats", h.DomainStats)
	engine.POST("/domains", h.CreateDomain)
	engine.PUT("/domains/:id", h.UpdateDomain)
	engine.DELETE("/domains/:id", h.DeleteDomain)
	engine.POST("/domains/import-cloud", h.ImportCloudDomains)
	engine.POST("/domains/:id/check", h.CheckDomain)
	return h, engine
}

func domainJSON(t *testing.T, engine *gin.Engine, method, path, body string) (int, map[string]any) {
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

// ---------- 纯函数 ----------

func TestDomainExpireStatus(t *testing.T) {
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	day := func(d int) *time.Time {
		v := now.AddDate(0, 0, d)
		return &v
	}

	// 没登记到期日 → unknown，而不是 valid。
	// 这是最容易忘续费的一批域名，不能被当成「还很久」藏起来
	if status, _ := domainExpireStatus(now, nil, 30); status != "unknown" {
		t.Errorf("没到期日应当是 unknown, got %s", status)
	}
	if status, _ := domainExpireStatus(now, day(-1), 30); status != "expired" {
		t.Errorf("已过期判断错: %s", status)
	}
	if status, _ := domainExpireStatus(now, day(10), 30); status != "expiring" {
		t.Errorf("将到期判断错: %s", status)
	}
	if status, _ := domainExpireStatus(now, day(100), 30); status != "valid" {
		t.Errorf("正常判断错: %s", status)
	}
	// 阈值是每条记录自己的
	if status, _ := domainExpireStatus(now, day(100), 200); status != "expiring" {
		t.Errorf("阈值 200 天时 100 天后到期应当算将到期, got %s", status)
	}
}

func TestSansCoverDomain(t *testing.T) {
	cases := []struct {
		sans []string
		name string
		want bool
	}{
		{[]string{"example.com"}, "example.com", true},
		{[]string{"api.example.com"}, "example.com", true},  // 子域算这个域名在用
		{[]string{"*.example.com"}, "example.com", true},    // 通配符
		{[]string{"EXAMPLE.COM."}, "example.com", true},     // 大小写与末尾点
		{[]string{"example.com"}, "api.example.com", false}, // 反向不成立
		{[]string{"notexample.com"}, "example.com", false},  // 后缀相近但不是子域
		{[]string{"badexample.com"}, "example.com", false},
		{nil, "example.com", false},
	}
	for _, c := range cases {
		if got := sansCoverDomain(c.sans, c.name); got != c.want {
			t.Errorf("sansCoverDomain(%v, %q) = %v, want %v", c.sans, c.name, got, c.want)
		}
	}
}

func TestParseDay(t *testing.T) {
	// 空串必须是 nil 而不是零值时间：零值会被当成 1970 年到期，凭空造一条已过期告警
	got, err := parseDay("")
	if err != nil || got != nil {
		t.Fatalf("空串应当是 (nil, nil), got %v / %v", got, err)
	}
	got, err = parseDay("2027-03-05")
	if err != nil || got == nil || got.Year() != 2027 || got.Month() != 3 || got.Day() != 5 {
		t.Fatalf("解析失败: %v / %v", got, err)
	}
	if _, err := parseDay("2027/03/05"); err == nil {
		t.Error("格式不对应当报错，不该静默当成空")
	}
}

func TestJudgeDNS(t *testing.T) {
	full := dnsx.Result{
		IPs: []string{"1.1.1.1", "2.2.2.2"},
		NS:  []string{"ns1.example.com", "ns2.example.com"},
	}

	// 没填任何期望 → nocheck，不是 ok。
	// 「没检查」和「检查通过」在值班视角里完全不是一回事
	status, detail := judgeDNS(model.Domain{}, full)
	if status != "nocheck" {
		t.Errorf("没期望值时 status = %s, want nocheck", status)
	}
	if !strings.Contains(detail, "没有填写") {
		t.Errorf("detail 应当说明原因: %q", detail)
	}

	// 期望与实际一致
	status, _ = judgeDNS(model.Domain{ExpectIPs: "2.2.2.2,1.1.1.1"}, full)
	if status != "ok" {
		t.Errorf("顺序不同但集合相同应当是 ok, got %s", status)
	}

	// 地址漂移
	status, detail = judgeDNS(model.Domain{ExpectIPs: "1.1.1.1,9.9.9.9"}, full)
	if status != "drift" {
		t.Fatalf("应当判漂移, got %s", status)
	}
	if !strings.Contains(detail, "9.9.9.9") || !strings.Contains(detail, "2.2.2.2") {
		t.Errorf("差异说明要同时点出少了什么、多了什么: %q", detail)
	}

	// NS 漂移（域名被转走的典型症状）
	status, detail = judgeDNS(model.Domain{ExpectNS: "ns1.example.com,ns2.example.com"},
		dnsx.Result{IPs: []string{"1.1.1.1"}, NS: []string{"ns1.evil.com"}})
	if status != "drift" || !strings.Contains(detail, "NS") {
		t.Errorf("NS 漂移没被识别: %s / %q", status, detail)
	}

	// CNAME 期望
	status, _ = judgeDNS(model.Domain{ExpectCNAME: "cdn.example.net"},
		dnsx.Result{IPs: []string{"1.1.1.1"}, CNAME: "cdn.example.net"})
	if status != "ok" {
		t.Errorf("CNAME 一致应当是 ok, got %s", status)
	}
	status, detail = judgeDNS(model.Domain{ExpectCNAME: "cdn.example.net"},
		dnsx.Result{IPs: []string{"1.1.1.1"}})
	if status != "drift" || !strings.Contains(detail, "没有 CNAME") {
		t.Errorf("期望有 CNAME 但实际没有，应当判漂移并说清: %s / %q", status, detail)
	}

	// 只有 NS、没有 A：很多域名的正常形态（没做网站、只发邮件），不该判异常
	status, _ = judgeDNS(model.Domain{}, dnsx.Result{NS: []string{"ns1.example.com"}})
	if status != "nocheck" {
		t.Errorf("只有 NS 时不该判 unresolved, got %s", status)
	}

	// 什么记录都没有 → unresolved
	status, detail = judgeDNS(model.Domain{}, dnsx.Result{IPErr: "没有这条记录"})
	if status != "unresolved" {
		t.Fatalf("什么都查不到应当是 unresolved, got %s", status)
	}
	if !strings.Contains(detail, "没有这条记录") {
		t.Errorf("应当带上解析器给的原因: %q", detail)
	}

	// NS 查询失败不该让已经对上的地址作废，但也不能吞掉
	status, detail = judgeDNS(model.Domain{ExpectIPs: "1.1.1.1"},
		dnsx.Result{IPs: []string{"1.1.1.1"}, NSErr: "解析超时"})
	if status != "ok" {
		t.Errorf("地址对上了就该是 ok, got %s", status)
	}
	if !strings.Contains(detail, "解析超时") {
		t.Errorf("NS 查询的错误要附上: %q", detail)
	}
}

// ---------- 接口 ----------

func TestCreateDomainNormalizesAndValidates(t *testing.T) {
	_, engine := newDomainTestHandler(t)

	// 粘了整个 URL 进来也要能存成干净的域名
	code, resp := domainJSON(t, engine, http.MethodPost, "/domains",
		`{"name":"HTTPS://Example.COM/path","registrar":"阿里云","expiresAt":"2027-01-01"}`)
	if code != http.StatusOK {
		t.Fatalf("创建失败 %d: %v", code, resp)
	}
	data := resp["data"].(map[string]any)
	if data["name"] != "example.com" {
		t.Errorf("域名应当被归一成 example.com, got %v", data["name"])
	}
	if data["alertDays"].(float64) != 30 {
		t.Errorf("提醒阈值默认应当是 30, got %v", data["alertDays"])
	}
	if data["source"] != "manual" {
		t.Errorf("手工登记的来源应当是 manual, got %v", data["source"])
	}

	// 重复域名要被拒（唯一索引）
	code, _ = domainJSON(t, engine, http.MethodPost, "/domains", `{"name":"example.com"}`)
	if code != http.StatusBadRequest {
		t.Errorf("重复域名应当被拒, got %d", code)
	}

	// 带端口、不像域名、日期格式错都要报清楚
	for _, body := range []string{
		`{"name":"example.com:8443"}`,
		`{"name":"localhost"}`,
		`{"name":"a.com","expiresAt":"2027/01/01"}`,
		`{"name":"b.com","alertDays":400}`,
	} {
		if code, _ := domainJSON(t, engine, http.MethodPost, "/domains", body); code != http.StatusBadRequest {
			t.Errorf("%s 应当被拒, got %d", body, code)
		}
	}
}

// TestUpdateDomainRecalculatesExpire 填上到期日后列表要立刻显示到期状态，
// 不能等下一次巡检 —— 否则看着像没保存成功。
func TestUpdateDomainRecalculatesExpire(t *testing.T) {
	h, engine := newDomainTestHandler(t)
	domainJSON(t, engine, http.MethodPost, "/domains", `{"name":"example.com"}`)

	var before model.Domain
	h.DB.First(&before)
	if before.ExpireStatus != "unknown" {
		t.Fatalf("没填到期日时应当是 unknown, got %s", before.ExpireStatus)
	}

	soon := time.Now().AddDate(0, 0, 5).Format("2006-01-02")
	code, resp := domainJSON(t, engine, http.MethodPut, "/domains/1",
		`{"name":"example.com","expiresAt":"`+soon+`","alertDays":30}`)
	if code != http.StatusOK {
		t.Fatalf("更新失败 %d: %v", code, resp)
	}
	var after model.Domain
	h.DB.First(&after)
	if after.ExpireStatus != "expiring" {
		t.Errorf("填上 5 天后到期应当立刻变成 expiring, got %s", after.ExpireStatus)
	}
}

// TestCheckDomainRealResolution 走真实解析：用 localhost 当目标，不需要联网。
// 这条覆盖「查 DNS → 对期望 → 回写 → 联动告警」整条链。
func TestCheckDomainRealResolution(t *testing.T) {
	h, engine := newDomainTestHandler(t)
	// localhost 过不了 CreateDomain 的「得像个域名」校验，直接建记录
	d := model.Domain{
		Name: "localhost", ExpectIPs: "127.0.0.1",
		AlertDays: 30, AlertEnabled: true, Enabled: true, CreatedBy: 1,
		DNSStatus: "unknown", ExpireStatus: "unknown",
	}
	if err := h.DB.Create(&d).Error; err != nil {
		t.Fatalf("建域名失败: %v", err)
	}

	code, resp := domainJSON(t, engine, http.MethodPost, "/domains/"+strconv.Itoa(int(d.ID))+"/check", "")
	if code != http.StatusOK {
		t.Fatalf("巡检失败 %d: %v", code, resp)
	}

	var after model.Domain
	h.DB.First(&after, d.ID)
	if after.LastCheckAt == nil {
		t.Error("巡检时间应当被回写")
	}
	if !strings.Contains(after.ResolvedIPs, "127.0.0.1") {
		t.Errorf("实际解析结果应当落库, got %q", after.ResolvedIPs)
	}
	// localhost 常同时解析出 ::1，期望里只写了 127.0.0.1，所以这里判漂移是对的 ——
	// 重点是状态必须是「对比过」的那几个，不能还是 unknown
	if after.DNSStatus == "unknown" {
		t.Error("巡检后 DNS 状态不该还是 unknown")
	}
	if after.DNSStatus != "ok" && after.DNSStatus != "drift" {
		t.Errorf("状态应当是 ok 或 drift, got %s (%s)", after.DNSStatus, after.DNSDetail)
	}
	if after.DNSStatus == "drift" && after.DNSDetail == "" {
		t.Error("判了漂移就必须给出差异说明")
	}
}

// TestDomainDriftRaisesAlert 解析漂移要真的产生一条告警，而不是只改个状态。
func TestDomainDriftRaisesAlert(t *testing.T) {
	h, engine := newDomainTestHandler(t)
	d := model.Domain{
		Name: "localhost", ExpectIPs: "203.0.113.1", // 刻意填一个不可能的地址
		AlertDays: 30, AlertEnabled: true, Enabled: true, CreatedBy: 1,
		DNSStatus: "unknown", ExpireStatus: "unknown",
	}
	h.DB.Create(&d)

	domainJSON(t, engine, http.MethodPost, "/domains/"+strconv.Itoa(int(d.ID))+"/check", "")

	var alerts []model.Alert
	h.DB.Find(&alerts)
	var firing *model.Alert
	for i := range alerts {
		if alerts[i].Status == "firing" {
			firing = &alerts[i]
			break
		}
	}
	if firing == nil {
		t.Fatalf("漂移应当产生一条 firing 告警，实际有 %d 条告警", len(alerts))
	}
	if !strings.Contains(firing.Title, "localhost") {
		t.Errorf("告警标题应当点名域名: %q", firing.Title)
	}

	// 再巡检一次：同指纹只累加，不刷屏
	domainJSON(t, engine, http.MethodPost, "/domains/"+strconv.Itoa(int(d.ID))+"/check", "")
	var count int64
	h.DB.Model(&model.Alert{}).Where("status = ?", "firing").Count(&count)
	if count != 1 {
		t.Errorf("重复巡检不该新增告警，firing 数 = %d", count)
	}
}

// TestDomainAlertDisabledStaysQuiet 关掉告警开关就一条告警都不该有。
//
// 这条用例同时守着一个很容易踩的 GORM 陷阱：AlertEnabled 带 `default:true`，
// 零值字段会被 GORM 从 INSERT 里省掉、由数据库填默认值 ——
// 也就是「用户关了开关，落库之后又被翻回开」。所以这里刻意走 HTTP 接口，
// 而不是在测试里直接 Create：直接 Create 就绕过了真实写入路径，测不到这个坑。
func TestDomainAlertDisabledStaysQuiet(t *testing.T) {
	h, engine := newDomainTestHandler(t)

	// .invalid 是保留 TLD，永远解析不出来 —— 拿它当「一定会出问题」的目标
	code, resp := domainJSON(t, engine, http.MethodPost, "/domains",
		`{"name":"never-resolves.invalid","alertEnabled":false}`)
	if code != http.StatusOK {
		t.Fatalf("创建失败 %d: %v", code, resp)
	}

	var created model.Domain
	h.DB.First(&created)
	if created.AlertEnabled {
		t.Fatal("表单里关掉的告警开关被数据库默认值翻回了「开」")
	}

	domainJSON(t, engine, http.MethodPost, "/domains/"+strconv.Itoa(int(created.ID))+"/check", "")

	var count int64
	h.DB.Model(&model.Alert{}).Count(&count)
	if count != 0 {
		t.Errorf("关了告警开关还产生了 %d 条告警", count)
	}
	// 但状态照样要回写：关的是告警，不是巡检
	var after model.Domain
	h.DB.First(&after, created.ID)
	if after.DNSStatus != "unresolved" {
		t.Errorf(".invalid 域名应当判成 unresolved, got %s (%s)", after.DNSStatus, after.DNSDetail)
	}
}

// TestDomainMatchesCertificateBySAN 按证书 SAN 反查，命中数与最早到期天数要对。
func TestDomainMatchesCertificateBySAN(t *testing.T) {
	h, engine := newDomainTestHandler(t)
	h.DB.Create(&model.Certificate{
		Name: "通配符证书", Domain: "www.example.com",
		DNSNames: "example.com,*.example.com", DaysLeft: 40, Enabled: true,
	})
	h.DB.Create(&model.Certificate{
		Name: "API 证书", Domain: "api.example.com",
		DNSNames: "api.example.com", DaysLeft: 12, Enabled: true,
	})
	h.DB.Create(&model.Certificate{
		Name: "无关证书", Domain: "other.net",
		DNSNames: "other.net", DaysLeft: 5, Enabled: true,
	})

	d := model.Domain{
		Name: "example.com", AlertDays: 30, AlertEnabled: false,
		Enabled: true, CreatedBy: 1, DNSStatus: "unknown", ExpireStatus: "unknown",
	}
	h.DB.Create(&d)
	domainJSON(t, engine, http.MethodPost, "/domains/"+strconv.Itoa(int(d.ID))+"/check", "")

	var after model.Domain
	h.DB.First(&after, d.ID)
	if after.CertCount != 2 {
		t.Errorf("应当命中 2 张证书（通配符 + 子域），got %d（%s）", after.CertCount, after.CertNames)
	}
	if after.CertMinDaysLeft != 12 {
		t.Errorf("最早到期应当取 12 天, got %d", after.CertMinDaysLeft)
	}
	if strings.Contains(after.CertNames, "无关证书") {
		t.Errorf("不该把无关证书算进来: %s", after.CertNames)
	}
}

// TestImportCloudDomains 从云资源清单导入：只带域名，到期日一律留空给人补。
func TestImportCloudDomains(t *testing.T) {
	h, engine := newDomainTestHandler(t)

	// 没同步过云资源时要给出可执行的提示，而不是一句「成功」
	code, _ := domainJSON(t, engine, http.MethodPost, "/domains/import-cloud", "")
	if code != http.StatusBadRequest {
		t.Errorf("云资源清单为空时应当提示先去同步, got %d", code)
	}

	h.DB.Create(&model.CloudResource{
		Provider: "aliyun", ResourceType: "domain", ResourceID: "example.com",
		Name: "example.com", Status: "hosted",
	})
	h.DB.Create(&model.CloudResource{
		Provider: "aliyun", ResourceType: "domain", ResourceID: "已消失.com",
		Name: "已消失.com", Status: "hosted", Gone: true,
	})
	// 已经在台账里的那个应当被跳过而不是报错
	domainJSON(t, engine, http.MethodPost, "/domains", `{"name":"exists.com"}`)
	h.DB.Create(&model.CloudResource{
		Provider: "aliyun", ResourceType: "domain", ResourceID: "exists.com",
		Name: "exists.com", Status: "hosted",
	})

	code, resp := domainJSON(t, engine, http.MethodPost, "/domains/import-cloud", "")
	if code != http.StatusOK {
		t.Fatalf("导入失败 %d: %v", code, resp)
	}
	data := resp["data"].(map[string]any)
	created, _ := data["created"].([]any)
	skipped, _ := data["skipped"].([]any)
	if len(created) != 1 || created[0] != "example.com" {
		t.Errorf("应当只新建 example.com, got %v", created)
	}
	if len(skipped) != 1 || skipped[0] != "exists.com" {
		t.Errorf("已存在的应当被跳过, got %v", skipped)
	}

	var imported model.Domain
	h.DB.Where("name = ?", "example.com").First(&imported)
	if imported.Source != "cloud" || imported.CloudResourceID == 0 {
		t.Errorf("导入的记录要能追溯来源: %+v", imported)
	}
	// 到期日必须留空：云解析接口给不出注册到期时间，替它猜一个比留空危险
	if imported.ExpiresAt != nil {
		t.Error("导入时不该编一个到期日出来")
	}
	if imported.ExpireStatus != "unknown" {
		t.Errorf("没到期日就该是 unknown, got %s", imported.ExpireStatus)
	}
}

// TestDeleteDomainResolvesAlerts 删台账要把它名下的告警一并恢复，
// 不然值班列表里会留一条点不进去的告警。
func TestDeleteDomainResolvesAlerts(t *testing.T) {
	h, engine := newDomainTestHandler(t)
	d := model.Domain{
		Name: "localhost", ExpectIPs: "203.0.113.1",
		AlertDays: 30, AlertEnabled: true, Enabled: true, CreatedBy: 1,
		DNSStatus: "unknown", ExpireStatus: "unknown",
	}
	h.DB.Create(&d)
	domainJSON(t, engine, http.MethodPost, "/domains/"+strconv.Itoa(int(d.ID))+"/check", "")

	var firing int64
	h.DB.Model(&model.Alert{}).Where("status = ?", "firing").Count(&firing)
	if firing == 0 {
		t.Fatal("前置条件不成立：应当先有一条 firing 告警")
	}

	code, _ := domainJSON(t, engine, http.MethodDelete, "/domains/"+strconv.Itoa(int(d.ID)), "")
	if code != http.StatusOK {
		t.Fatalf("删除失败 %d", code)
	}
	h.DB.Model(&model.Alert{}).Where("status = ?", "firing").Count(&firing)
	if firing != 0 {
		t.Errorf("删除后还剩 %d 条 firing 告警", firing)
	}
}

// TestDomainStatsCallsOutMissingExpiry 概览要把「待补到期日」单独数出来并说明含义。
func TestDomainStatsCallsOutMissingExpiry(t *testing.T) {
	_, engine := newDomainTestHandler(t)
	domainJSON(t, engine, http.MethodPost, "/domains", `{"name":"a.com"}`)
	domainJSON(t, engine, http.MethodPost, "/domains",
		`{"name":"b.com","expiresAt":"`+time.Now().AddDate(1, 0, 0).Format("2006-01-02")+`"}`)

	code, resp := domainJSON(t, engine, http.MethodGet, "/domains/stats", "")
	if code != http.StatusOK {
		t.Fatalf("返回 %d", code)
	}
	data := resp["data"].(map[string]any)
	if data["total"].(float64) != 2 {
		t.Errorf("total = %v, want 2", data["total"])
	}
	if data["noExpiry"].(float64) != 1 {
		t.Errorf("待补到期日应当是 1, got %v", data["noExpiry"])
	}
	note, _ := data["note"].(string)
	if !strings.Contains(note, "只能人工登记") {
		t.Errorf("note 要说清到期日只能人填: %q", note)
	}
}
