package handler

import (
	"encoding/csv"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"ops-platform/server/internal/model"
)

// newSessionTestHandler 起一个内存库，造两次会话与若干命令
func newSessionTestHandler(t *testing.T) *Handler {
	t.Helper()
	g, err := gorm.Open(sqlite.Open("file::memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开内存库失败: %v", err)
	}
	if err := g.AutoMigrate(&model.Session{}, &model.SessionCommand{}); err != nil {
		t.Fatalf("建表失败: %v", err)
	}

	base := time.Date(2026, 9, 18, 10, 0, 0, 0, time.Local)
	sessions := []model.Session{
		{
			HostID: 1, HostName: "web-01", Address: "10.0.0.11", LoginUser: "root",
			UserID: 1, Username: "admin", ClientIP: "192.168.1.10",
			Status: "closed", StartedAt: base, BlockedCount: 1, CommandCount: 3,
		},
		{
			// 这一次会话异常中断：命令明细里有被拦的命令，但 blocked_count 停在 0
			HostID: 2, HostName: "db-01", Address: "10.0.0.22", LoginUser: "mysql",
			UserID: 2, Username: "ops01", ClientIP: "192.168.1.11",
			Status: "error", StartedAt: base.Add(48 * time.Hour), CommandCount: 2,
		},
		{
			// 只有高风险命令，没有被拦的
			HostID: 3, HostName: "cache-01", Address: "10.0.0.33", LoginUser: "redis",
			UserID: 2, Username: "ops01", ClientIP: "192.168.1.12",
			Status: "closed", StartedAt: base.Add(72 * time.Hour), CommandCount: 1,
		},
	}
	for i := range sessions {
		if err := g.Create(&sessions[i]).Error; err != nil {
			t.Fatalf("写入会话失败: %v", err)
		}
	}

	commands := []model.SessionCommand{
		// 会话 1：admin 在 web-01 上，含一条被拦的 rm -rf
		{SessionID: sessions[0].ID, Command: "ls -l /data", Risk: "normal", OffsetMs: 1000, CreatedAt: base},
		{SessionID: sessions[0].ID, Command: "rm -rf /data/tmp", Risk: "blocked", RuleID: 3,
			RuleDesc: "禁止 rm -rf", OffsetMs: 5000, CreatedAt: base.Add(5 * time.Second)},
		{SessionID: sessions[0].ID, Command: "systemctl restart nginx", Risk: "warn", RuleID: 4,
			RuleDesc: "重启服务需复核", OffsetMs: 9000, CreatedAt: base.Add(9 * time.Second)},
		// 会话 2：ops01 在 db-01 上，两天后
		{SessionID: sessions[1].ID, Command: "mysql -e 'show databases'", Risk: "normal",
			OffsetMs: 2000, CreatedAt: base.Add(48 * time.Hour)},
		{SessionID: sessions[1].ID, Command: "rm -rf /var/log/mysql", Risk: "blocked", RuleID: 3,
			RuleDesc: "禁止 rm -rf", OffsetMs: 4000, CreatedAt: base.Add(48*time.Hour + 4*time.Second)},
		// 会话 3：只有一条高风险
		{SessionID: sessions[2].ID, Command: "redis-cli flushall", Risk: "warn", RuleID: 5,
			RuleDesc: "清空缓存需复核", OffsetMs: 3000, CreatedAt: base.Add(72 * time.Hour)},
	}
	for i := range commands {
		if err := g.Create(&commands[i]).Error; err != nil {
			t.Fatalf("写入命令失败: %v", err)
		}
	}
	return New(g, nil)
}

type commandSearchBody struct {
	Data struct {
		List    []commandRow `json:"list"`
		Total   int64        `json:"total"`
		Blocked int64        `json:"blocked"`
	} `json:"data"`
}

func searchCommands(t *testing.T, h *Handler, rawQuery string) (int, commandSearchBody) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest("GET", "/sessions/commands?"+rawQuery, nil)
	h.SearchSessionCommands(c)

	var body commandSearchBody
	if rec.Code == 200 {
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("解析响应失败: %v, body=%s", err, rec.Body.String())
		}
	}
	return rec.Code, body
}

func TestSearchSessionCommands(t *testing.T) {
	h := newSessionTestHandler(t)

	// 不带条件：全部 6 条，其中 2 条被拦
	code, body := searchCommands(t, h, "")
	if code != 200 || body.Data.Total != 6 || body.Data.Blocked != 2 {
		t.Fatalf("全量检索不对: code=%d total=%d blocked=%d", code, body.Data.Total, body.Data.Blocked)
	}
	// join 出来的会话上下文必须跟着每一行
	first := body.Data.List[0]
	if first.HostName == "" || first.Username == "" || first.LoginUser == "" {
		t.Fatalf("结果行缺少会话上下文: %+v", first)
	}

	// 按命令内容检索：这正是「谁敲过 rm -rf」
	code, body = searchCommands(t, h, "keyword=rm+-rf")
	if code != 200 || body.Data.Total != 2 {
		t.Fatalf("按命令检索应命中 2 条，实际 %d", body.Data.Total)
	}
	hosts := map[string]bool{}
	for _, row := range body.Data.List {
		hosts[row.HostName] = true
		if row.RuleDesc != "禁止 rm -rf" {
			t.Fatalf("规则说明应跟着记录留下来: %+v", row)
		}
	}
	if !hosts["web-01"] || !hosts["db-01"] {
		t.Fatalf("两台机器上的命令都该被找到: %+v", hosts)
	}

	// 风险筛选
	code, body = searchCommands(t, h, "risk=blocked")
	if code != 200 || body.Data.Total != 2 {
		t.Fatalf("blocked 应 2 条，实际 %d", body.Data.Total)
	}
	_, body = searchCommands(t, h, "risk=risky")
	if body.Data.Total != 4 {
		t.Fatalf("risky（warn+blocked）应 4 条，实际 %d", body.Data.Total)
	}
	_, body = searchCommands(t, h, "risk=normal")
	if body.Data.Total != 2 {
		t.Fatalf("normal 应 2 条，实际 %d", body.Data.Total)
	}
	if code, _ := searchCommands(t, h, "risk=bogus"); code != 400 {
		t.Fatalf("非法风险级别应 400，实际 %d", code)
	}

	// 操作人 / 登录账号 / 主机
	_, body = searchCommands(t, h, "username=ops01")
	if body.Data.Total != 3 {
		t.Fatalf("按操作人应 3 条，实际 %d", body.Data.Total)
	}
	_, body = searchCommands(t, h, "loginUser=mysql")
	if body.Data.Total != 2 {
		t.Fatalf("按登录账号应 2 条，实际 %d", body.Data.Total)
	}
	_, body = searchCommands(t, h, "host=10.0.0.11")
	if body.Data.Total != 3 {
		t.Fatalf("按主机地址应 3 条，实际 %d", body.Data.Total)
	}
	_, body = searchCommands(t, h, "host=web")
	if body.Data.Total != 3 {
		t.Fatalf("按主机名应 3 条，实际 %d", body.Data.Total)
	}

	// 时间范围：结束日期要含当天
	_, body = searchCommands(t, h, "start=2026-09-20&end=2026-09-20")
	if body.Data.Total != 2 {
		t.Fatalf("按日期应命中第二次会话的 2 条，实际 %d", body.Data.Total)
	}
	_, body = searchCommands(t, h, "end=2026-09-18")
	if body.Data.Total != 3 {
		t.Fatalf("截止 9-18 应 3 条，实际 %d", body.Data.Total)
	}

	// 指定会话
	_, body = searchCommands(t, h, "sessionId=1")
	if body.Data.Total != 3 {
		t.Fatalf("按会话应 3 条，实际 %d", body.Data.Total)
	}
	if code, _ := searchCommands(t, h, "sessionId=abc"); code != 400 {
		t.Fatalf("会话 ID 非法应 400，实际 %d", code)
	}
	if code, _ := searchCommands(t, h, "start=昨天"); code != 400 {
		t.Fatalf("时间格式非法应 400，实际 %d", code)
	}
}

// TestSearchSessionCommandsBlockedCountDoesNotLeak 统计「被拦条数」时加的条件
// 不能漏回主查询，否则列表会只剩被拦的命令
func TestSearchSessionCommandsBlockedCountDoesNotLeak(t *testing.T) {
	h := newSessionTestHandler(t)
	code, body := searchCommands(t, h, "")
	if code != 200 {
		t.Fatalf("检索失败: %d", code)
	}
	if body.Data.Total != 6 || len(body.Data.List) != 6 {
		t.Fatalf("总数与列表都应是 6：total=%d list=%d", body.Data.Total, len(body.Data.List))
	}
	if body.Data.Blocked != 2 {
		t.Fatalf("被拦条数应为 2，实际 %d", body.Data.Blocked)
	}
}

func TestExportSessionCommands(t *testing.T) {
	h := newSessionTestHandler(t)
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest("GET", "/sessions/commands/export?keyword=rm+-rf", nil)
	h.ExportSessionCommands(c)

	if rec.Code != 200 {
		t.Fatalf("导出应成功，实际 %d", rec.Code)
	}
	if rec.Header().Get("X-Export-Truncated") != "false" {
		t.Fatalf("没超上限不该标记截断: %q", rec.Header().Get("X-Export-Truncated"))
	}
	body := rec.Body.String()
	if !strings.HasPrefix(body, "\ufeff") {
		t.Fatal("CSV 应带 UTF-8 BOM，否则 Excel 打开是乱码")
	}

	reader := csv.NewReader(strings.NewReader(strings.TrimPrefix(body, "\ufeff")))
	records, err := reader.ReadAll()
	if err != nil {
		t.Fatalf("CSV 解析失败: %v", err)
	}
	if len(records) != 3 { // 表头 + 2 条
		t.Fatalf("应有表头加 2 行，实际 %d 行", len(records))
	}
	if records[0][11] != "command" || records[0][2] != "sessionId" {
		t.Fatalf("表头字段不对: %+v", records[0])
	}
	// 命令原文与规则说明都要落到 CSV 里
	joined := strings.Join(records[1], "|") + strings.Join(records[2], "|")
	if !strings.Contains(joined, "rm -rf /data/tmp") || !strings.Contains(joined, "禁止 rm -rf") {
		t.Fatalf("导出内容不完整: %+v", records)
	}
}

func TestListSessionsFilters(t *testing.T) {
	h := newSessionTestHandler(t)
	listSessions := func(rawQuery string) (int, int64, []model.Session) {
		gin.SetMode(gin.TestMode)
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest("GET", "/sessions?"+rawQuery, nil)
		h.ListSessions(c)
		var body struct {
			Data struct {
				List  []model.Session `json:"list"`
				Total int64           `json:"total"`
			} `json:"data"`
		}
		if rec.Code == 200 {
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("解析失败: %v", err)
			}
		}
		return rec.Code, body.Data.Total, body.Data.List
	}

	if _, total, _ := listSessions(""); total != 3 {
		t.Fatalf("全量应 3 条，实际 %d", total)
	}
	if _, total, _ := listSessions("loginUser=mysql"); total != 1 {
		t.Fatalf("按登录账号应 1 条，实际 %d", total)
	}
	if _, total, _ := listSessions("hostId=1"); total != 1 {
		t.Fatalf("按主机应 1 条，实际 %d", total)
	}
	if code, _, _ := listSessions("hostId=abc"); code != 400 {
		t.Fatalf("主机 ID 非法应 400，实际 %d", code)
	}
	// 时间范围按会话开始时间
	if _, total, _ := listSessions("start=2026-09-20"); total != 2 {
		t.Fatalf("9-20 起应 2 条（9-20 与 9-21），实际 %d", total)
	}
	if _, total, _ := listSessions("end=2026-09-18"); total != 1 {
		t.Fatalf("截止 9-18 应 1 条，实际 %d", total)
	}
	// risk 筛选不只看 blocked_count：会话 2 的计数停在 0，但命令明细里有被拦的
	if _, total, _ := listSessions("risk=blocked"); total != 2 {
		t.Fatalf("有拦截的会话应 2 条（含计数没写回的那次），实际 %d", total)
	}
	if _, total, _ := listSessions("risk=risky"); total != 3 {
		t.Fatalf("有风险的会话应 3 条（含只有高风险命令的那次），实际 %d", total)
	}
	if code, _, _ := listSessions("risk=bogus"); code != 400 {
		t.Fatalf("非法风险筛选应 400，实际 %d", code)
	}
	// 旧参数语义等同 risk=blocked
	if _, total, _ := listSessions("riskOnly=true"); total != 2 {
		t.Fatalf("旧参数 riskOnly 应 2 条，实际 %d", total)
	}
}

func TestListSessionCommands(t *testing.T) {
	h := newSessionTestHandler(t)
	call := func(id, rawQuery string) (int, int64, []model.SessionCommand) {
		gin.SetMode(gin.TestMode)
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Params = gin.Params{{Key: "id", Value: id}}
		c.Request = httptest.NewRequest("GET", "/sessions/"+id+"/commands?"+rawQuery, nil)
		h.ListSessionCommands(c)
		var body struct {
			Data struct {
				List  []model.SessionCommand `json:"list"`
				Total int64                  `json:"total"`
			} `json:"data"`
		}
		if rec.Code == 200 {
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("解析失败: %v", err)
			}
		}
		return rec.Code, body.Data.Total, body.Data.List
	}

	code, total, list := call("1", "")
	if code != 200 || total != 3 || len(list) != 3 {
		t.Fatalf("会话 1 应 3 条命令: code=%d total=%d", code, total)
	}
	// 命令按时间正序，回放定位时才符合直觉
	if list[0].OffsetMs > list[1].OffsetMs || list[1].OffsetMs > list[2].OffsetMs {
		t.Fatalf("命令应按时间正序: %+v", list)
	}
	if _, total, _ := call("1", "risk=blocked"); total != 1 {
		t.Fatalf("会话 1 被拦命令应 1 条，实际 %d", total)
	}
	if _, total, _ := call("1", "risk=risky"); total != 2 {
		t.Fatalf("会话 1 有风险命令应 2 条，实际 %d", total)
	}
	if _, total, _ := call("1", "keyword=systemctl"); total != 1 {
		t.Fatalf("按关键字应 1 条，实际 %d", total)
	}
	if code, _, _ := call("999", ""); code != 404 {
		t.Fatalf("会话不存在应 404，实际 %d", code)
	}
}
