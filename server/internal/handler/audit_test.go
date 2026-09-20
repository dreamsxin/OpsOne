package handler

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"ops-platform/server/internal/model"
)

// newAuditTestHandler 起一个内存库，塞几条审计记录
func newAuditTestHandler(t *testing.T) *Handler {
	t.Helper()
	g, err := gorm.Open(sqlite.Open("file::memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开内存库失败: %v", err)
	}
	if err := g.AutoMigrate(&model.AuditLog{}); err != nil {
		t.Fatalf("建表失败: %v", err)
	}
	base := time.Date(2026, 9, 18, 10, 0, 0, 0, time.Local)
	rows := []model.AuditLog{
		{Username: "admin", Method: "POST", Path: "/api/v1/hosts", Action: "/api/v1/hosts", Status: 200, CreatedAt: base},
		{Username: "admin", Method: "DELETE", Path: "/api/v1/hosts/3", Action: "/api/v1/hosts/:id", Status: 403, CreatedAt: base.Add(time.Hour)},
		{Username: "ops", Method: "PUT", Path: "/api/v1/hosts/3", Action: "/api/v1/hosts/:id", Status: 200, CreatedAt: base.Add(48 * time.Hour)},
	}
	for i := range rows {
		if err := g.Create(&rows[i]).Error; err != nil {
			t.Fatalf("写入审计记录失败: %v", err)
		}
	}
	return New(g, nil)
}

type auditListBody struct {
	Data struct {
		List   []model.AuditLog `json:"list"`
		Total  int64            `json:"total"`
		Failed int64            `json:"failed"`
	} `json:"data"`
}

func listAudit(t *testing.T, h *Handler, rawQuery string) (int, auditListBody) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest("GET", "/system/audit-logs?"+rawQuery, nil)
	h.ListAuditLogs(c)

	var body auditListBody
	if rec.Code == 200 {
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("解析响应失败: %v, body=%s", err, rec.Body.String())
		}
	}
	return rec.Code, body
}

// TestListAuditLogsFailedCountDoesNotLeak 失败数是在同一份条件上另起一条语句统计的，
// 这里确认它不会把 status >= 400 带进列表查询（复用 *gorm.DB 时最容易踩的坑）。
func TestListAuditLogsFailedCountDoesNotLeak(t *testing.T) {
	h := newAuditTestHandler(t)

	code, body := listAudit(t, h, "")
	if code != 200 {
		t.Fatalf("期望 200，实际 %d", code)
	}
	if body.Data.Total != 3 {
		t.Fatalf("总数应为 3，实际 %d", body.Data.Total)
	}
	if body.Data.Failed != 1 {
		t.Fatalf("失败数应为 1，实际 %d", body.Data.Failed)
	}
	if len(body.Data.List) != 3 {
		t.Fatalf("列表应返回 3 条（不能被失败条件污染），实际 %d", len(body.Data.List))
	}
}

func TestListAuditLogsFilters(t *testing.T) {
	h := newAuditTestHandler(t)

	cases := []struct {
		name  string
		query string
		want  int64
	}{
		{"按操作人模糊匹配", "username=ops", 1},
		{"按方法精确匹配", "method=delete", 1},
		{"关键字命中实际路径（某台主机身上发生的事）", "keyword=/hosts/3", 2},
		{"关键字命中路由模板", "keyword=/hosts/:id", 2},
		{"只看失败", "result=fail", 1},
		{"时间范围按天闭区间", "start=2026-09-18&end=2026-09-18", 2},
		{"时间范围可精确到秒", "start=2026-09-18+10:30:00&end=2026-09-18+23:59:59", 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, body := listAudit(t, h, tc.query)
			if code != 200 {
				t.Fatalf("期望 200，实际 %d", code)
			}
			if body.Data.Total != tc.want {
				t.Fatalf("期望 %d 条，实际 %d", tc.want, body.Data.Total)
			}
		})
	}
}

func TestListAuditLogsRejectsBadParams(t *testing.T) {
	h := newAuditTestHandler(t)
	for _, query := range []string{
		"method=GET",                      // 读请求不入库，按 GET 查属于误用
		"result=unknown",                  // 结果只有成功/失败
		"start=2026/09/18",                // 格式非法
		"start=2026-09-19&end=2026-09-18", // 区间反了
	} {
		if code, _ := listAudit(t, h, query); code != 400 {
			t.Fatalf("query=%q 应该被拒绝，实际 %d", query, code)
		}
	}
}

func TestParseAuditTimeEndOfDay(t *testing.T) {
	end, err := parseAuditTime("2026-09-18", true)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	// 只给日期时结束时间要补到当天末尾，否则「查今天」一条都查不到
	if end.Hour() != 23 || end.Minute() != 59 || end.Second() != 59 {
		t.Fatalf("结束时间应补到 23:59:59，实际 %s", end.Format(time.RFC3339))
	}

	start, err := parseAuditTime("2026-09-18", false)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if start.Hour() != 0 || start.Minute() != 0 {
		t.Fatalf("开始时间应为零点，实际 %s", start.Format(time.RFC3339))
	}

	if _, err := parseAuditTime("", false); err != nil {
		t.Fatalf("空值应视为不限，却报错: %v", err)
	}
}
