package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"ops-platform/server/internal/config"
	"ops-platform/server/internal/model"
)

func newHostLogTestHandler(t *testing.T, prefixes string) (*Handler, *gin.Engine) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	dir := t.TempDir()
	g, err := gorm.Open(sqlite.Open(dir+"/hostlog.db"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	if err := g.AutoMigrate(
		&model.HostLogTarget{}, &model.HostLogScan{}, &model.HostLogUsage{},
		&model.Host{}, &model.User{}, &model.Role{}, &model.Menu{},
		&model.Alert{}, &model.AlertSource{}, &model.AlertSilence{},
		&model.AggregationPolicy{}, &model.NotifyChannel{}, &model.NotifyRoute{},
		&model.NotifyRecord{}, &model.Message{}, &model.CommandRule{},
		&model.ResourceGrant{}, &model.Department{},
	); err != nil {
		t.Fatalf("建表失败: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, err := g.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})

	h := New(g, &config.Config{LogPathPrefixes: prefixes})
	engine := gin.New()
	engine.Use(func(c *gin.Context) {
		c.Set("ctx_user", &model.User{ID: 1, Username: "admin"})
		c.Next()
	})
	engine.GET("/monitor/host-logs/meta", h.HostLogMeta)
	engine.GET("/monitor/host-log-targets", h.ListHostLogTargets)
	engine.POST("/monitor/host-log-targets", h.CreateHostLogTarget)
	engine.PUT("/monitor/host-log-targets/:id", h.UpdateHostLogTarget)
	engine.DELETE("/monitor/host-log-targets/:id", h.DeleteHostLogTarget)
	engine.POST("/monitor/host-log-targets/:id/scan", h.ScanHostLogTarget)
	engine.GET("/monitor/log-usage", h.ListLogUsage)
	return h, engine
}

func hostLogJSON(t *testing.T, engine *gin.Engine, method, path, body string) (int, map[string]any) {
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

// ---------- 路径白名单：这个模块唯一真正的安全边界 ----------

func TestNormalizeLogPath(t *testing.T) {
	h, _ := newHostLogTestHandler(t, "")

	// 白名单内
	for _, in := range []string{"/var/log/messages", "/var/log/nginx/access.log", "/var/log//syslog", "/var/log"} {
		if _, err := h.normalizeLogPath(in); err != nil {
			t.Errorf("%q 应当被允许: %v", in, err)
		}
	}

	// 必须被拒的：相对路径、.. 穿越、白名单外、前缀相似但不是子目录
	bad := map[string]string{
		"var/log/messages":          "相对路径",
		"/etc/shadow":               "白名单外",
		"/var/log/../../etc/shadow": ".. 穿越",
		"/var/log/../etc/passwd":    ".. 穿越（一层）",
		"/var/logother/x.log":       "前缀相似但不是 /var/log 的子目录",
		"":                          "空路径",
		"/var/log/a\nb":             "含换行",
	}
	for in, why := range bad {
		if _, err := h.normalizeLogPath(in); err == nil {
			t.Errorf("%q 应当被拒（%s）", in, why)
		}
	}

	// Clean 必须发生在前缀判断之前：先判前缀再 Clean 等于没判
	if got, err := h.normalizeLogPath("/var/log/nginx/../access.log"); err != nil || got != "/var/log/access.log" {
		t.Errorf("同目录内的 .. 应当归一后放行, got %q / %v", got, err)
	}
}

func TestNormalizeLogPathCustomPrefixes(t *testing.T) {
	h, _ := newHostLogTestHandler(t, "/var/log, /opt/app/logs")
	if _, err := h.normalizeLogPath("/opt/app/logs/app.log"); err != nil {
		t.Errorf("自定义前缀下应当允许: %v", err)
	}
	if _, err := h.normalizeLogPath("/opt/app/other.log"); err == nil {
		t.Error("自定义前缀之外仍应被拒")
	}
	if got := h.logUsageDir(); got != "/var/log" {
		t.Errorf("占用采集目录取第一个前缀, got %q", got)
	}
}

// ---------- 命令拼装：注入与输出上限 ----------

func TestBuildLogViewCommandQuotesInput(t *testing.T) {
	// 路径与关键字里塞 shell 元字符，必须全部被单引号包住
	cmd := buildLogViewCommand("/var/log/a'; rm -rf /; echo '.log", "err'; id; #", 100, true)

	if strings.Contains(cmd, "rm -rf /;") && !strings.Contains(cmd, `'\''`) {
		t.Fatal("路径里的引号没有被转义，存在命令注入")
	}
	// shellQuote 把 ' 变成 '\'' —— 出现这个序列说明转义生效了
	if !strings.Contains(cmd, `'\''`) {
		t.Error("期望看到 shellQuote 的转义序列")
	}
	// grep 必须带 -F：否则关键字被当正则，一个 .* 就能让它扫全文
	if !strings.Contains(cmd, "grep -Fi -e ") {
		t.Errorf("grep 必须用 -F 把关键字当字面字符串:\n%s", cmd)
	}
	// 输出必须在远端截断：sshx.Run 那边没有大小限制
	if !strings.Contains(cmd, "head -c "+strconv.Itoa(hostLogViewMaxBytes)) {
		t.Error("缺少远端输出上限")
	}

	// 不带关键字时走 tail
	plain := buildLogViewCommand("/var/log/messages", "", 50, true)
	if !strings.Contains(plain, "tail -n 50 -- '/var/log/messages'") {
		t.Errorf("无关键字应当走 tail:\n%s", plain)
	}
	if strings.Contains(plain, "grep") {
		t.Error("没填关键字不该出现 grep")
	}
	// 大小写敏感开关要真的生效
	cs := buildLogViewCommand("/var/log/m", "ERR", 10, false)
	if !strings.Contains(cs, "grep -F -e ") {
		t.Errorf("区分大小写时不该带 i:\n%s", cs)
	}
}

func TestBuildLogScanCommandCarriesWatermark(t *testing.T) {
	cmd := buildLogScanCommand("/var/log/app.log", 4096, "12345", 1024)
	if !strings.Contains(cmd, "start=4097") {
		t.Errorf("应当从上次水位 +1 开始读:\n%s", cmd)
	}
	if !strings.Contains(cmd, `[ "$ino" = '12345' ]`) {
		t.Errorf("应当把上次的 inode 带过去比对:\n%s", cmd)
	}
	if !strings.Contains(cmd, "head -c 1024") {
		t.Error("缺少单轮读取上限")
	}
	if !strings.Contains(cmd, "exit 0") {
		t.Error("末尾要 exit 0：否则最后一条命令的退出码会把整次采集判成失败")
	}
}

// ---------- 输出解析 ----------

func TestParseLogProbe(t *testing.T) {
	raw := "__OPS_STATE__=ok\n__OPS_SIZE__=1234\n__OPS_INODE__=99\n__OPS_BODY__\nline1\nline2\n"
	p := parseLogProbe(raw)
	if p.State != "ok" || p.Size != 1234 || p.Inode != "99" {
		t.Fatalf("前缀解析错: %+v", p)
	}
	if p.Body != "line1\nline2\n" {
		t.Errorf("正文解析错: %q", p.Body)
	}

	// 日志正文里出现类似记号也不该把解析带偏 —— 正文是分隔符之后的整段，不再逐行解析
	raw2 := "__OPS_STATE__=ok\n__OPS_SIZE__=1\n__OPS_INODE__=2\n__OPS_BODY__\n__OPS_STATE__=denied\nreal\n"
	p2 := parseLogProbe(raw2)
	if p2.State != "ok" {
		t.Errorf("正文里的伪记号不该改变状态, got %s", p2.State)
	}
	if !strings.Contains(p2.Body, "__OPS_STATE__=denied") {
		t.Error("正文应当原样保留")
	}

	if got := parseLogProbe("__OPS_STATE__=missing\n"); got.State != "missing" || got.Body != "" {
		t.Errorf("missing 状态解析错: %+v", got)
	}
}

func TestMatchLogKeywords(t *testing.T) {
	body := "2026-09-22 INFO started\n" +
		"2026-09-22 ERROR db timeout\n" +
		"2026-09-22 error retry ok\n" +
		"2026-09-22 WARN disk 85%\n" +
		"2026-09-22 ERROR known-noise please ignore\n"

	// 不区分大小写，命中 2 条 ERROR/error
	got := matchLogKeywords(body, "error", "")
	if got.HitCount != 3 {
		t.Errorf("命中数 = %d, want 3（ERROR / error / ERROR known-noise）", got.HitCount)
	}
	if !strings.Contains(got.Sample, "db timeout") {
		t.Errorf("样本应当是第一条命中: %q", got.Sample)
	}

	// ignore 优先于 keyword
	got = matchLogKeywords(body, "error", "known-noise")
	if got.HitCount != 2 {
		t.Errorf("加了忽略词后命中数 = %d, want 2", got.HitCount)
	}

	// 多关键字
	if got := matchLogKeywords(body, "error,warn", ""); got.HitCount != 4 {
		t.Errorf("多关键字命中数 = %d, want 4", got.HitCount)
	}

	// 关键字为空 = 不做判断，不是「全部命中」
	if got := matchLogKeywords(body, "", ""); got.HitCount != 0 {
		t.Errorf("没填关键字时不该有命中, got %d", got.HitCount)
	}
	// 空正文
	if got := matchLogKeywords("", "error", ""); got.HitCount != 0 {
		t.Error("空正文不该有命中")
	}
}

func TestParseLogUsage(t *testing.T) {
	// find -printf 模式：第一列是字节，要换算成 KB
	raw := "__OPS_STATE__=ok\n__OPS_TOTAL_KB__=204800\n__OPS_BODY__\n" +
		"10485760 /var/log/big.log\n2048 /var/log/small.log\n500 /var/log/tiny.log\n"
	total, fallback, entries, state := parseLogUsage(raw)
	if state != "ok" || total != 204800 || fallback {
		t.Fatalf("头部解析错: state=%s total=%d fallback=%v", state, total, fallback)
	}
	if len(entries) != 3 {
		t.Fatalf("条目数 = %d", len(entries))
	}
	if entries[0].SizeKB != 10240 || entries[0].Path != "/var/log/big.log" {
		t.Errorf("字节应当换算成 KB: %+v", entries[0])
	}
	// 不足 1KB 的按 1KB 记，免得显示成 0
	if entries[2].SizeKB != 1 {
		t.Errorf("500 字节应当记成 1KB, got %d", entries[2].SizeKB)
	}

	// fallback 模式：du -k 给的已经是 KB，不能再除
	rawFB := "__OPS_STATE__=ok\n__OPS_TOTAL_KB__=100\n__OPS_FALLBACK__=1\n__OPS_BODY__\n" +
		"4096 /var/log\n2048 /var/log/nginx\n"
	total, fallback, entries, _ = parseLogUsage(rawFB)
	if !fallback {
		t.Fatal("应当识别出 fallback")
	}
	if entries[0].SizeKB != 4096 {
		t.Errorf("fallback 下 KB 应当原样, got %d", entries[0].SizeKB)
	}

	if _, _, _, state := parseLogUsage("__OPS_STATE__=missing\n"); state != "missing" {
		t.Error("missing 状态没解析出来")
	}
}

// ---------- 接口 ----------

func TestCreateHostLogTargetRejectsBadPath(t *testing.T) {
	h, engine := newHostLogTestHandler(t, "")
	h.DB.Create(&model.Host{Name: "web-01", Address: "10.0.0.1", Port: 22,
		Username: "root", AuthType: "password", CreatedBy: 1})

	// 白名单外的路径必须在登记阶段就被拒，不能等到巡检才发现
	code, resp := hostLogJSON(t, engine, http.MethodPost, "/monitor/host-log-targets",
		`{"name":"shadow","hostId":1,"path":"/etc/shadow","keywords":"root"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("/etc/shadow 应当被拒, got %d: %v", code, resp)
	}
	if !strings.Contains(resp["msg"].(string), "OPS_LOG_PATH_PREFIXES") {
		t.Errorf("错误信息应当告诉人怎么放开: %v", resp["msg"])
	}

	code, resp = hostLogJSON(t, engine, http.MethodPost, "/monitor/host-log-targets",
		`{"name":"messages","hostId":1,"path":"/var/log/messages","keywords":"error,fatal"}`)
	if code != http.StatusOK {
		t.Fatalf("合法路径应当创建成功 %d: %v", code, resp)
	}
	data := resp["data"].(map[string]any)
	if data["lastStatus"] != "unknown" {
		t.Errorf("新建时状态应当是 unknown, got %v", data["lastStatus"])
	}
	if data["maxBytes"].(float64) != float64(hostLogScanMaxBytes) {
		t.Errorf("未填时应当给默认上限, got %v", data["maxBytes"])
	}
}

// TestHostLogAlertSwitchSticks 关掉告警开关不能被 GORM 默认值翻回来
// （与 Domain.AlertEnabled 同一个坑，这里守住第二处）。
func TestHostLogAlertSwitchSticks(t *testing.T) {
	h, engine := newHostLogTestHandler(t, "")
	h.DB.Create(&model.Host{Name: "web-01", Address: "10.0.0.1", Port: 22,
		Username: "root", AuthType: "password", CreatedBy: 1})

	hostLogJSON(t, engine, http.MethodPost, "/monitor/host-log-targets",
		`{"name":"m","hostId":1,"path":"/var/log/messages","alertEnabled":false}`)
	var target model.HostLogTarget
	h.DB.First(&target)
	if target.AlertEnabled {
		t.Fatal("关掉的告警开关被翻回了开")
	}
}

// TestUpdateHostLogTargetResetsWatermark 换了文件或换了主机，旧水位必须归零 ——
// 留着旧 offset 会让下一轮从一个错位的字节开始读。
func TestUpdateHostLogTargetResetsWatermark(t *testing.T) {
	h, engine := newHostLogTestHandler(t, "")
	h.DB.Create(&model.Host{Name: "web-01", Address: "10.0.0.1", Port: 22,
		Username: "root", AuthType: "password", CreatedBy: 1})
	hostLogJSON(t, engine, http.MethodPost, "/monitor/host-log-targets",
		`{"name":"m","hostId":1,"path":"/var/log/messages","keywords":"error"}`)

	// 假装已经巡检过，有了水位
	h.DB.Model(&model.HostLogTarget{}).Where("id = 1").Updates(map[string]any{
		"last_offset": 8192, "last_inode": "777", "last_size": 8192,
		"last_status": "ok", "last_hit_count": 3, "last_sample": "old sample",
	})

	// 只改名字：水位保留
	hostLogJSON(t, engine, http.MethodPut, "/monitor/host-log-targets/1",
		`{"name":"messages","hostId":1,"path":"/var/log/messages","keywords":"error"}`)
	var kept model.HostLogTarget
	h.DB.First(&kept, 1)
	if kept.LastOffset != 8192 || kept.LastInode != "777" {
		t.Errorf("只改名不该动水位: offset=%d inode=%q", kept.LastOffset, kept.LastInode)
	}

	// 换文件：水位归零
	hostLogJSON(t, engine, http.MethodPut, "/monitor/host-log-targets/1",
		`{"name":"messages","hostId":1,"path":"/var/log/syslog","keywords":"error"}`)
	var reset model.HostLogTarget
	h.DB.First(&reset, 1)
	if reset.LastOffset != 0 || reset.LastInode != "" || reset.LastStatus != "unknown" {
		t.Errorf("换文件后水位应当归零: %+v", reset)
	}
	if reset.LastSample != "" || reset.LastHitCount != 0 {
		t.Error("换文件后上次的命中样本也该清掉，那已经不是同一个文件了")
	}
}

// TestScanHostLogTargetMissingHost 关联主机被删掉时如实记失败，不 panic。
func TestScanHostLogTargetMissingHost(t *testing.T) {
	h, _ := newHostLogTestHandler(t, "")
	target := model.HostLogTarget{
		Name: "orphan", HostID: 999, Path: "/var/log/messages",
		Keywords: "error", AlertEnabled: true, Enabled: true,
		MaxBytes: 1024, LastStatus: "unknown", CreatedBy: 1,
	}
	h.DB.Create(&target)

	after := h.scanHostLogTarget(target, "admin")
	if after.LastStatus != "failed" {
		t.Fatalf("主机不存在应当记 failed, got %s", after.LastStatus)
	}
	if !strings.Contains(after.LastError, "主机") {
		t.Errorf("失败原因要点明: %q", after.LastError)
	}
	// 失败也要落一条历史
	var scans int64
	h.DB.Model(&model.HostLogScan{}).Count(&scans)
	if scans != 1 {
		t.Errorf("失败的巡检也要留痕, got %d 条", scans)
	}
	// 「监控瞎了」要告警
	var firing int64
	h.DB.Model(&model.Alert{}).Where("status = ?", "firing").Count(&firing)
	if firing == 0 {
		t.Error("读不到日志等于这个监控点瞎了，必须告警")
	}
}

func TestDeleteHostLogTargetCleansUp(t *testing.T) {
	h, engine := newHostLogTestHandler(t, "")
	target := model.HostLogTarget{
		Name: "m", HostID: 999, Path: "/var/log/messages",
		AlertEnabled: true, Enabled: true, LastStatus: "unknown", CreatedBy: 1,
	}
	h.DB.Create(&target)
	h.scanHostLogTarget(target, "admin")

	code, _ := hostLogJSON(t, engine, http.MethodDelete, "/monitor/host-log-targets/1", "")
	if code != http.StatusOK {
		t.Fatalf("删除失败 %d", code)
	}
	var scans, firing int64
	h.DB.Model(&model.HostLogScan{}).Count(&scans)
	if scans != 0 {
		t.Errorf("删监控点应当连带清掉它的巡检历史, 还剩 %d 条", scans)
	}
	h.DB.Model(&model.Alert{}).Where("status = ?", "firing").Count(&firing)
	if firing != 0 {
		t.Errorf("删监控点应当把它名下的告警恢复, 还剩 %d 条 firing", firing)
	}
}

func TestHostLogMetaExposesLimits(t *testing.T) {
	_, engine := newHostLogTestHandler(t, "/var/log,/opt/logs")
	code, resp := hostLogJSON(t, engine, http.MethodGet, "/monitor/host-logs/meta", "")
	if code != http.StatusOK {
		t.Fatalf("返回 %d", code)
	}
	data := resp["data"].(map[string]any)
	prefixes := data["pathPrefixes"].([]any)
	if len(prefixes) != 2 || prefixes[0] != "/var/log" {
		t.Errorf("前缀应当交给前端而不是让它硬编码: %v", prefixes)
	}
	if data["viewMaxBytes"].(float64) != float64(hostLogViewMaxBytes) {
		t.Errorf("上限要一起交出去: %v", data["viewMaxBytes"])
	}
}

// ---------- 用真 shell 验证生成的脚本 ----------

// bashAvailable 本机有没有可用的 POSIX shell（Windows 上是 WSL 的 bash）。
// 没有就跳过 —— 这几条是「锦上添花」的真实验证，不该让没装 WSL 的环境红。
func bashAvailable(t *testing.T) bool {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		return false
	}
	out, err := runShellRaw("echo __probe_ok__\n")
	if err != nil || !strings.Contains(out, "__probe_ok__") {
		t.Logf("bash 不可用，跳过 shell 验证: %v / %q", err, out)
		return false
	}
	return true
}

// runShellRaw 把脚本**从 stdin 喂给 bash**，而不是塞进 -c。
//
// 这一点是踩出来的：Windows 上 WSL 的 bash.exe 会把 -c 参数里的换行吃掉，
// 多行脚本被拼成一行之后 `f='/tmp' if [ ... ]` 变成了带前缀赋值的命令、
// $f 是空的 —— 表现成「/tmp 不存在」这种看不懂的失败。走 stdin 没这个问题。
func runShellRaw(script string) (string, error) {
	cmd := exec.Command("bash", "-s")
	cmd.Stdin = strings.NewReader(script)
	out, err := cmd.Output()
	return string(out), err
}

func runShell(t *testing.T, script string) string {
	t.Helper()
	out, err := runShellRaw(script)
	if err != nil {
		t.Fatalf("执行脚本失败: %v\n脚本:\n%s", err, script)
	}
	return out
}

// TestScanScriptIncrementsOnRealShell 把生成的增量脚本丢给真 shell 跑一遍。
//
// 这是这个模块最该被真实验证的一段：tail -c +N 的偏移语义、ls -i 取 inode、
// 轮转后水位归零 —— 三处任何一个写错，表现都是「日志监控看着在跑但内容是错的」，
// 单测里拿字符串比对是查不出来的。
func TestScanScriptIncrementsOnRealShell(t *testing.T) {
	if !bashAvailable(t) {
		t.Skip("没有可用的 bash")
	}
	logFile := "/tmp/ops-hostlog-inc.log"
	t.Cleanup(func() { _ = exec.Command("bash", "-c", "rm -f "+logFile).Run() })

	// 第一轮：文件里两行，水位为空 → 读到全部
	runShell(t, "printf 'line1\\nline2\\n' > "+logFile)
	first := parseLogProbe(runShell(t, buildLogScanCommand(logFile, 0, "", 65536)))
	if first.State != "ok" {
		t.Fatalf("state = %q", first.State)
	}
	if first.Size != 12 {
		t.Errorf("size = %d, want 12", first.Size)
	}
	if first.Inode == "" {
		t.Error("没取到 inode —— 轮转判定会失效")
	}
	if first.Body != "line1\nline2\n" {
		t.Errorf("首轮应当读到全文, got %q", first.Body)
	}

	// 第二轮：追加两行，带上水位 → 只读到新增的
	runShell(t, "printf 'line3\\nline4\\n' >> "+logFile)
	second := parseLogProbe(runShell(t, buildLogScanCommand(logFile, first.Size, first.Inode, 65536)))
	if second.Body != "line3\nline4\n" {
		t.Fatalf("增量读错了，应当只有新增两行, got %q", second.Body)
	}
	if second.Inode != first.Inode {
		t.Errorf("追加写不该换 inode: %q → %q", first.Inode, second.Inode)
	}

	// 第三轮：没有新内容 → 正文为空，但状态仍是 ok
	third := parseLogProbe(runShell(t, buildLogScanCommand(logFile, second.Size, second.Inode, 65536)))
	if third.State != "ok" {
		t.Errorf("没有新日志不是错误, state = %q", third.State)
	}
	if third.Body != "" {
		t.Errorf("没有新内容时正文应当为空, got %q", third.Body)
	}

	// 第四轮：truncate 后重写（`> file` 保留 inode 但大小变小）→ 必须从头读
	runShell(t, "printf 'fresh\\n' > "+logFile)
	fourth := parseLogProbe(runShell(t, buildLogScanCommand(logFile, third.Size, third.Inode, 65536)))
	if fourth.Body != "fresh\n" {
		t.Fatalf("truncate 后应当从头读, got %q", fourth.Body)
	}
	// Go 侧据此判轮转：当前大小 < 上次水位
	if !(fourth.Size < third.Size) {
		t.Errorf("truncate 后大小应当变小: %d → %d", third.Size, fourth.Size)
	}

	// 第五轮：删掉重建（inode 变）→ 也必须从头读
	runShell(t, "rm -f "+logFile+" && printf 'rotated\\n' > "+logFile)
	fifth := parseLogProbe(runShell(t, buildLogScanCommand(logFile, 999999, fourth.Inode, 65536)))
	if fifth.Inode == fourth.Inode {
		t.Skip("重建后 inode 被复用了，这一步换不出可判定的条件")
	}
	if fifth.Body != "rotated\n" {
		t.Errorf("inode 变了应当从头读, got %q", fifth.Body)
	}
}

// TestScanScriptHandlesMissingAndDir 缺失、目录、不可读三种情况都要被识别出来，
// 而不是表现成一次「成功但内容为空」的巡检。
func TestScanScriptHandlesMissingAndDir(t *testing.T) {
	if !bashAvailable(t) {
		t.Skip("没有可用的 bash")
	}
	if got := parseLogProbe(runShell(t, buildLogScanCommand("/tmp/ops-nope-"+t.Name()+".log", 0, "", 1024))); got.State != "missing" {
		t.Errorf("不存在的文件 state = %q, want missing", got.State)
	}
	if got := parseLogProbe(runShell(t, buildLogScanCommand("/tmp", 0, "", 1024))); got.State != "isdir" {
		t.Errorf("目录 state = %q, want isdir", got.State)
	}
}

// TestViewScriptOnRealShell tail 与 grep 两种模式都要真的能跑通，
// 并且 grep 无匹配时不能被当成失败（grep 无匹配退出码是 1）。
func TestViewScriptOnRealShell(t *testing.T) {
	if !bashAvailable(t) {
		t.Skip("没有可用的 bash")
	}
	logFile := "/tmp/ops-hostlog-view.log"
	t.Cleanup(func() { _ = exec.Command("bash", "-c", "rm -f "+logFile).Run() })
	runShell(t, "printf 'a INFO\\nb ERROR x\\nc WARN\\nd error y\\n' > "+logFile)

	// tail 末尾 2 行
	tailOut := parseLogProbe(runShell(t, buildLogViewCommand(logFile, "", 2, true)))
	if tailOut.State != "ok" {
		t.Fatalf("state = %q", tailOut.State)
	}
	if tailOut.Body != "c WARN\nd error y\n" {
		t.Errorf("tail -n 2 结果不对: %q", tailOut.Body)
	}

	// grep 忽略大小写，应当同时匹配 ERROR 与 error
	grepOut := parseLogProbe(runShell(t, buildLogViewCommand(logFile, "error", 10, true)))
	if !strings.Contains(grepOut.Body, "b ERROR x") || !strings.Contains(grepOut.Body, "d error y") {
		t.Errorf("忽略大小写的 grep 结果不对: %q", grepOut.Body)
	}

	// 区分大小写时只匹配小写那条
	csOut := parseLogProbe(runShell(t, buildLogViewCommand(logFile, "error", 10, false)))
	if strings.Contains(csOut.Body, "b ERROR x") {
		t.Errorf("区分大小写时不该匹配 ERROR: %q", csOut.Body)
	}

	// 无匹配：grep 退出码 1，但整个脚本必须 exit 0、状态仍是 ok
	noneOut := parseLogProbe(runShell(t, buildLogViewCommand(logFile, "no-such-token", 10, true)))
	if noneOut.State != "ok" {
		t.Errorf("grep 无匹配不是失败, state = %q", noneOut.State)
	}
	if strings.TrimSpace(noneOut.Body) != "" {
		t.Errorf("无匹配时正文应当为空: %q", noneOut.Body)
	}

	// 关键字里带 shell 元字符：不能被执行，也不能让脚本挂掉
	evil := parseLogProbe(runShell(t, buildLogViewCommand(logFile, "x'; touch /tmp/ops-pwned; #", 10, true)))
	if evil.State != "ok" {
		t.Errorf("带元字符的关键字应当被当成普通字符串, state = %q", evil.State)
	}
	if err := exec.Command("bash", "-c", "test -e /tmp/ops-pwned").Run(); err == nil {
		_ = exec.Command("bash", "-c", "rm -f /tmp/ops-pwned").Run()
		t.Fatal("命令注入成功了 —— 关键字没有被正确转义")
	}
}

// TestUsageScriptOnRealShell 占用采集：总量与 Top 清单都要能解出来。
func TestUsageScriptOnRealShell(t *testing.T) {
	if !bashAvailable(t) {
		t.Skip("没有可用的 bash")
	}
	dir := "/tmp/ops-hostlog-usage"
	t.Cleanup(func() { _ = exec.Command("bash", "-c", "rm -rf "+dir).Run() })
	// 造两个大小明显不同的文件
	runShell(t, "mkdir -p "+dir+"/sub && dd if=/dev/zero of="+dir+"/big.log bs=1024 count=200 2>/dev/null && printf 'tiny\\n' > "+dir+"/sub/small.log")

	total, fallback, entries, state := parseLogUsage(runShell(t, buildLogUsageCommand(dir)))
	if state != "ok" {
		t.Fatalf("state = %q", state)
	}
	if total < 200 {
		t.Errorf("总占用 = %d KB，至少应当有 200KB", total)
	}
	if len(entries) == 0 {
		t.Fatal("没解出 Top 清单")
	}
	if !strings.Contains(entries[0].Path, "big.log") {
		t.Errorf("最大的应当是 big.log, got %+v", entries[0])
	}
	if entries[0].SizeKB < 190 {
		t.Errorf("big.log 应当约 200KB, got %d（字节/KB 换算可能搞反了）", entries[0].SizeKB)
	}
	if fallback {
		t.Log("本机 find 不支持 -printf，走了 du -a 退路（清单含目录）")
	}

	// 目录不存在
	if _, _, _, state := parseLogUsage(runShell(t, buildLogUsageCommand("/tmp/ops-no-such-dir"))); state != "missing" {
		t.Errorf("不存在的目录 state = %q, want missing", state)
	}
}
