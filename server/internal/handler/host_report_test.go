package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"ops-platform/server/internal/config"
	"ops-platform/server/internal/model"
)

func newHostReportTestHandler(t *testing.T) (*Handler, *gin.Engine) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	dir := t.TempDir()
	g, err := gorm.Open(sqlite.Open(dir+"/report.db"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	if err := g.AutoMigrate(
		&model.Host{}, &model.HostMetric{}, &model.HostService{}, &model.ConfigFile{},
		&model.HostLogTarget{}, &model.HostLogUsage{}, &model.FirewallRule{},
		&model.Session{}, &model.ExecResult{}, &model.FileAudit{}, &model.FixedAsset{},
		&model.User{}, &model.Role{}, &model.ResourceGrant{}, &model.SysConfig{},
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
		c.Set("ctx_perms", map[string]struct{}{})
		c.Next()
	})
	engine.GET("/hosts/:id/report", h.GetHostReport)

	// CreatedBy = 1：没有角色的用户数据范围是「仅本人」，这样报告才可见
	h.DB.Create(&model.Host{
		Name: "web-01", Address: "10.0.0.11", Port: 22, Username: "root",
		Env: "prod", Status: "online", CreatedBy: 1,
	})
	return h, engine
}

func reportOf(t *testing.T, engine *gin.Engine) (map[string]any, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/hosts/1/report", nil))
	var parsed map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &parsed); err != nil {
		t.Fatalf("响应不是 JSON: %s", rec.Body.String())
	}
	if rec.Code != 200 {
		t.Fatalf("取报告失败: %d %s", rec.Code, rec.Body.String())
	}
	data, ok := parsed["data"].(map[string]any)
	if !ok {
		t.Fatalf("响应里没有 data: %s", rec.Body.String())
	}
	return data, rec.Body.String()
}

func sectionByTitle(t *testing.T, data map[string]any, title string) map[string]any {
	t.Helper()
	for _, item := range data["sections"].([]any) {
		section := item.(map[string]any)
		if section["title"] == title {
			return section
		}
	}
	t.Fatalf("报告里没有「%s」这一段", title)
	return nil
}

// 「从没采过」「采过但失败」「采到了」三种状态必须分得开
func TestHostReportSeparatesNoDataFromFailure(t *testing.T) {
	h, engine := newHostReportTestHandler(t)

	// 一条记录都没有
	data, raw := reportOf(t, engine)
	section := sectionByTitle(t, data, "资源使用")
	gap, _ := section["gap"].(string)
	if !strings.Contains(gap, "从来没有采集成功过") {
		t.Fatalf("没有任何采样时要说清是「从没采过」: %s", raw)
	}
	if items, _ := section["items"].([]any); len(items) != 0 {
		t.Fatal("没有数据时不该编出条目")
	}

	// 只有一条失败记录：要说「采集失败」而不是「从没采过」
	h.DB.Create(&model.HostMetric{HostID: 1, Status: "failed", Error: "连接超时"})
	data, raw = reportOf(t, engine)
	section = sectionByTitle(t, data, "资源使用")
	gap, _ = section["gap"].(string)
	if !strings.Contains(gap, "采集失败") || !strings.Contains(gap, "连接超时") {
		t.Fatalf("失败时要给出原因: %s", raw)
	}
	if section["problems"] != float64(1) {
		t.Fatalf("采集失败要算一个问题: %v", section["problems"])
	}

	// 有成功记录：出数字，并且磁盘那条要带口径说明
	h.DB.Create(&model.HostMetric{
		HostID: 1, Status: "ok", CPUPercent: 12.5, MemPercent: 40, MemTotalMB: 8192,
		MemUsedMB: 3276, DiskMaxPercent: 91.2, DiskMaxMount: "/data",
		Load1: 0.5, Load5: 0.6, Load15: 0.7, CPUCores: 4, ProcCount: 210,
		TCPConn: 88, UptimeSec: 200000,
	})
	data, raw = reportOf(t, engine)
	section = sectionByTitle(t, data, "资源使用")
	if section["gap"] != "" {
		t.Fatalf("有成功采样时不该有 gap: %s", raw)
	}
	items := section["items"].([]any)
	if len(items) < 8 {
		t.Fatalf("条目太少: %s", raw)
	}
	var disk map[string]any
	for _, item := range items {
		row := item.(map[string]any)
		if row["name"] == "磁盘最满挂载点" {
			disk = row
		}
	}
	if disk == nil || disk["detail"] != "/data" || disk["warn"] != true {
		t.Fatalf("磁盘那条不对: %v", disk)
	}
	if note, _ := disk["note"].(string); !strings.Contains(note, "只记最满") {
		t.Fatalf("要说清只记一个挂载点: %v", note)
	}
	// 进程数与连接数必须写明「只有数量」
	for _, item := range items {
		row := item.(map[string]any)
		if row["name"] == "进程数" {
			if note, _ := row["note"].(string); !strings.Contains(note, "没有进程清单") {
				t.Fatalf("进程数要说明只有数量: %v", note)
			}
		}
	}
	// 24 小时统计要把失败那次算进去
	if summary, _ := section["summary"].(string); !strings.Contains(summary, "失败 1 次") {
		t.Fatalf("采样成功率没算对: %v", summary)
	}
}

// 没采过的东西要逐条列出来，并且不能用「未发现」这种读起来像结论的措辞
func TestHostReportNotCollectedIsExplicit(t *testing.T) {
	_, engine := newHostReportTestHandler(t)
	data, raw := reportOf(t, engine)

	notCollected, _ := data["notCollected"].([]any)
	if len(notCollected) < 6 {
		t.Fatalf("没采集的项目至少 6 条，实际 %d", len(notCollected))
	}
	joined := ""
	for _, item := range notCollected {
		row := item.(map[string]any)
		name, _ := row["item"].(string)
		reason, _ := row["reason"].(string)
		if reason == "" {
			t.Fatalf("「%s」没有写明为什么没有", name)
		}
		joined += name + "|" + reason + "\n"
	}
	for _, want := range []string{"漏洞", "补丁", "软件包", "进程清单", "端口", "失败登录"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("没采集清单里缺少「%s」: %s", want, joined)
		}
	}

	// 整份报告里不允许出现「未发现漏洞」这类措辞
	if strings.Contains(raw, "未发现漏洞") || strings.Contains(raw, "无漏洞") {
		t.Fatal("报告里出现了「未发现漏洞」式措辞 —— 平台根本没扫过")
	}
	if notes, _ := data["notes"].([]any); len(notes) < 6 {
		t.Fatalf("口径说明至少 6 条，实际 %d", len(notes))
	}
}

// 超过阈值没更新的段落要标成已过期
func TestHostReportMarksStale(t *testing.T) {
	h, engine := newHostReportTestHandler(t)
	old := time.Now().Add(-48 * time.Hour)
	h.DB.Create(&model.HostMetric{HostID: 1, Status: "ok", CPUCores: 2})
	h.DB.Model(&model.HostMetric{}).Where("host_id = ?", 1).Update("created_at", old)

	data, raw := reportOf(t, engine)
	section := sectionByTitle(t, data, "资源使用")
	if section["stale"] != true {
		t.Fatalf("48 小时前的数据应该被标成已过期: %s", raw)
	}
	if data["staleAfterHrs"] != float64(hostReportStaleHours) {
		t.Fatalf("过期阈值要出接口: %v", data["staleAfterHrs"])
	}
}

// 服务巡检失败时，实际态沿用上一次成功的值 —— 这一点必须写在条目上
func TestHostReportServiceErrorKeepsLastValue(t *testing.T) {
	h, engine := newHostReportTestHandler(t)
	now := time.Now()
	h.DB.Create(&model.HostService{
		HostID: 1, Name: "nginx", Critical: true, Drift: "error",
		ActiveState: "active", EnableState: "enabled",
		LastError: "ssh: connect timeout", LastCheckAt: &now,
	})
	h.DB.Create(&model.HostService{
		HostID: 1, Name: "redis", Drift: "ok", ActiveState: "active", LastCheckAt: &now,
	})

	data, raw := reportOf(t, engine)
	section := sectionByTitle(t, data, "纳管服务")
	if section["problems"] != float64(1) {
		t.Fatalf("只有 nginx 有问题: %s", raw)
	}
	for _, item := range section["items"].([]any) {
		row := item.(map[string]any)
		if row["name"] == "nginx" {
			note, _ := row["note"].(string)
			if !strings.Contains(note, "上一次成功") || !strings.Contains(note, "timeout") {
				t.Fatalf("要说清这是上次的值以及失败原因: %v", note)
			}
		}
	}
	if source, _ := section["source"].(string); !strings.Contains(source, "只巡检你登记过的") {
		t.Fatalf("要说清没登记的服务不在内: %v", source)
	}
}

// 没登记 = 灰色的「没有数据」，不是「检查通过」
func TestHostReportEmptySectionsAreGapsNotPasses(t *testing.T) {
	_, engine := newHostReportTestHandler(t)
	data, raw := reportOf(t, engine)

	for _, title := range []string{"纳管服务", "配置文件", "日志与目录占用", "防火墙规则"} {
		section := sectionByTitle(t, data, title)
		if gap, _ := section["gap"].(string); gap == "" {
			t.Fatalf("「%s」没有登记任何东西，应该给出 gap 而不是空列表: %s", title, raw)
		}
		if section["problems"] != float64(0) {
			t.Fatalf("「%s」没有数据时不该算问题数: %v", title, section["problems"])
		}
	}
	// 一台什么都没登记的主机：5 段里除了「操作痕迹」全是 gap
	// （指标也算一段 —— 从没采过和采到 0 是两件事）
	if data["gapCount"] != float64(5) {
		t.Fatalf("gap 段落数应为 5: %v", data["gapCount"])
	}
	if gap, _ := sectionByTitle(t, data, "资源使用")["gap"].(string); gap == "" {
		t.Fatal("没有任何采样时资源使用也该是 gap")
	}
}

// 防火墙那段要说清它没有定时任务；登记了规则但从未核对过要算一个问题
func TestHostReportFirewallHasNoCron(t *testing.T) {
	h, engine := newHostReportTestHandler(t)
	h.DB.Create(&model.FirewallRule{HostID: 1, State: "pending"})
	h.DB.Create(&model.FirewallRule{HostID: 1, State: "synced"})

	data, raw := reportOf(t, engine)
	section := sectionByTitle(t, data, "防火墙规则")
	if source, _ := section["source"].(string); !strings.Contains(source, "没有定时巡检") {
		t.Fatalf("要说清防火墙没有定时任务: %v", source)
	}
	if summary, _ := section["summary"].(string); !strings.Contains(summary, "从未与真机核对过") {
		t.Fatalf("没有 LastCheckAt 时要照实说没核对过: %s", raw)
	}
	if section["checkedAt"] != nil {
		t.Fatal("从未核对过时不该给出 checkedAt")
	}
}

// 基本信息要标出每一项是人填的还是采的
func TestHostReportBasicMarksSource(t *testing.T) {
	_, engine := newHostReportTestHandler(t)
	data, raw := reportOf(t, engine)

	basic, _ := data["basic"].([]any)
	if len(basic) < 8 {
		t.Fatalf("基本信息条目太少: %s", raw)
	}
	for _, item := range basic {
		row := item.(map[string]any)
		if source, _ := row["source"].(string); source == "" {
			t.Fatalf("「%v」没有标注来源", row["name"])
		}
	}
	for _, item := range basic {
		row := item.(map[string]any)
		if row["name"] == "系统信息" {
			source, _ := row["source"].(string)
			if !strings.Contains(source, "没有定时任务") {
				t.Fatalf("系统信息要说清只在人点检查时更新: %v", source)
			}
			if row["value"] != "未采集" {
				t.Fatalf("没采到时要照实写「未采集」: %v", row["value"])
			}
		}
	}
}

// 看不见这台主机的人也拿不到它的报告
func TestHostReportRespectsHostScope(t *testing.T) {
	h, engine := newHostReportTestHandler(t)
	// 把主机划给别人：无角色用户的数据范围是「仅本人」
	h.DB.Model(&model.Host{}).Where("id = ?", 1).Update("created_by", 99)

	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/hosts/1/report", nil))
	if rec.Code == 200 {
		t.Fatalf("看不到这台主机的人不该拿到报告: %s", rec.Body.String())
	}
}
