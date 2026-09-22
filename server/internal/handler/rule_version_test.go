package handler

import (
	"bytes"
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

func newRuleVersionTestHandler(t *testing.T) (*Handler, *gin.Engine) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	dir := t.TempDir()
	g, err := gorm.Open(sqlite.Open(dir+"/rulever.db"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	if err := g.AutoMigrate(
		&model.AlertRule{}, &model.RuleVersion{}, &model.Alert{}, &model.AlertSource{},
		&model.User{}, &model.SysConfig{},
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
	engine.POST("/monitor/alert-rules", h.CreateAlertRule)
	engine.PUT("/monitor/alert-rules/:id", h.UpdateAlertRule)
	engine.DELETE("/monitor/alert-rules/:id", h.DeleteAlertRule)
	engine.GET("/monitor/alert-rule-versions", h.ListAlertRuleVersions)
	engine.GET("/monitor/alert-rule-versions/:id", h.GetAlertRuleVersion)
	engine.GET("/monitor/alert-rule-versions/diff", h.DiffAlertRuleVersions)
	engine.POST("/monitor/alert-rules/:id/rollback", h.RollbackAlertRule)
	engine.POST("/monitor/alert-rule-versions/:id/restore", h.RestoreAlertRuleVersion)
	return h, engine
}

func ruleJSON(t *testing.T, engine *gin.Engine, method, path, body string) (int, map[string]any) {
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

// 一条可用的告警规则请求体
const ruleBody80 = `{"name":"磁盘告警","metric":"host.offline","comparator":"gt",` +
	`"threshold":80,"windowMinutes":60,"consecutiveTimes":2,"severity":"warning","remark":"初版"}`
const ruleBody200 = `{"name":"磁盘告警","metric":"host.offline","comparator":"gt",` +
	`"threshold":200,"windowMinutes":60,"consecutiveTimes":2,"severity":"warning","remark":"初版"}`

// TestRuleVersionCreateAndEdit 建规则存 v1，改一次存 v2，
// 并且差异能说出「阈值 80 → 200」这种话（这是做字段级 diff 而不是行级的理由）。
func TestRuleVersionCreateAndEdit(t *testing.T) {
	h, engine := newRuleVersionTestHandler(t)

	if code, resp := ruleJSON(t, engine, http.MethodPost, "/monitor/alert-rules", ruleBody80); code != http.StatusOK {
		t.Fatalf("建规则失败 %d: %v", code, resp)
	}
	var v1 []model.RuleVersion
	h.DB.Order("id asc").Find(&v1)
	if len(v1) != 1 || v1[0].Source != "created" || v1[0].Version != 1 {
		t.Fatalf("建规则应当存第一版: %+v", v1)
	}
	if !strings.Contains(v1[0].Content, `"threshold":80`) {
		t.Errorf("快照里应当有阈值: %s", v1[0].Content)
	}
	// 运行态字段不该进快照 —— 否则每轮定时评估都会「产生新版本」
	for _, bad := range []string{"hitStreak", "lastValue", "lastStatus", "lastEvalAt"} {
		if strings.Contains(v1[0].Content, bad) {
			t.Errorf("快照里不该有运行态字段 %s: %s", bad, v1[0].Content)
		}
	}

	if code, resp := ruleJSON(t, engine, http.MethodPut, "/monitor/alert-rules/1", ruleBody200); code != http.StatusOK {
		t.Fatalf("改规则失败 %d: %v", code, resp)
	}
	var versions []model.RuleVersion
	h.DB.Order("id asc").Find(&versions)
	if len(versions) != 2 {
		t.Fatalf("改一次应当有两版, got %d", len(versions))
	}
	if versions[1].Source != "edited" || versions[1].Version != 2 {
		t.Errorf("第二版不对: %+v", versions[1])
	}
	if versions[1].Operator != "admin" {
		t.Errorf("操作人没记下来: %q", versions[1].Operator)
	}

	// 字段级差异
	code, resp := ruleJSON(t, engine, http.MethodGet,
		"/monitor/alert-rule-versions/diff?from=1&to=2", "")
	if code != http.StatusOK {
		t.Fatalf("diff 失败 %d: %v", code, resp)
	}
	data := resp["data"].(map[string]any)
	if data["same"] == true {
		t.Error("两版内容不同，same 应当是 false")
	}
	if data["changed"].(float64) != 1 {
		t.Errorf("只改了阈值，变化项应当是 1, got %v", data["changed"])
	}
	items := data["items"].([]any)
	var found bool
	for _, raw := range items {
		item := raw.(map[string]any)
		if item["key"] != "threshold" {
			if item["changed"] == true {
				t.Errorf("%v 不该被判成改了: %v", item["key"], item)
			}
			continue
		}
		found = true
		if item["before"] != "80" || item["after"] != "200" {
			t.Errorf("阈值差异不对: %v", item)
		}
		if item["label"] != "阈值" {
			t.Errorf("字段标签应当是中文: %v", item["label"])
		}
	}
	if !found {
		t.Error("差异里找不到阈值这一项")
	}
}

// TestRuleVersionSkipsNoOpSave 点了保存但什么都没改 → 不产生新版本。
// 否则历史里会堆一串一模一样的版本，把真正的改动淹掉。
func TestRuleVersionSkipsNoOpSave(t *testing.T) {
	h, engine := newRuleVersionTestHandler(t)
	ruleJSON(t, engine, http.MethodPost, "/monitor/alert-rules", ruleBody80)
	ruleJSON(t, engine, http.MethodPut, "/monitor/alert-rules/1", ruleBody80)
	ruleJSON(t, engine, http.MethodPut, "/monitor/alert-rules/1", ruleBody80)

	var count int64
	h.DB.Model(&model.RuleVersion{}).Count(&count)
	if count != 1 {
		t.Errorf("内容没变时不该产生新版本, 版本数 = %d", count)
	}
}

// TestRuleVersionIgnoresRuntimeFields 定时评估改的是运行态字段，
// 不该产生新版本 —— 这条守的是「每 5 分钟评估一次就多一版」这种噪音。
func TestRuleVersionIgnoresRuntimeFields(t *testing.T) {
	h, engine := newRuleVersionTestHandler(t)
	ruleJSON(t, engine, http.MethodPost, "/monitor/alert-rules", ruleBody80)

	now := time.Now()
	h.DB.Model(&model.AlertRule{}).Where("id = 1").Updates(map[string]any{
		"hit_streak": 3, "last_value": 91.5, "last_status": "firing",
		"last_detail": "磁盘 91.5%", "last_eval_at": &now, "last_fire_at": &now,
	})
	var rule model.AlertRule
	h.DB.First(&rule, 1)
	if h.recordAlertRuleVersion(rule, "edited", "", "system") {
		t.Fatal("只有运行态变了，不该产生新版本")
	}
	var count int64
	h.DB.Model(&model.RuleVersion{}).Count(&count)
	if count != 1 {
		t.Errorf("版本数 = %d, want 1", count)
	}
}

// TestRuleVersionBackfillsLegacyRule 版本功能上线之前就存在的规则：
// 第一次改动时要先把「改之前的样子」补成第一版，否则 diff 不出改了什么。
func TestRuleVersionBackfillsLegacyRule(t *testing.T) {
	h, engine := newRuleVersionTestHandler(t)
	// 直接插一条规则，模拟历史数据（没有任何版本记录）
	h.DB.Create(&model.AlertRule{
		Name: "老规则", Metric: "host.offline", Comparator: "gt", Threshold: 80,
		WindowMinutes: 60, ConsecutiveTimes: 1, Severity: "warning",
		Enabled: true, LastStatus: "unknown",
	})

	ruleJSON(t, engine, http.MethodPut, "/monitor/alert-rules/1",
		`{"name":"老规则","metric":"host.offline","comparator":"gt","threshold":150,`+
			`"windowMinutes":60,"consecutiveTimes":1,"severity":"critical"}`)

	var versions []model.RuleVersion
	h.DB.Order("id asc").Find(&versions)
	if len(versions) != 2 {
		t.Fatalf("既有规则第一次改动应当补出两版, got %d", len(versions))
	}
	if versions[0].Source != "created" || !strings.Contains(versions[0].Note, "上线前") {
		t.Errorf("补出来的那版要说明来历: %+v", versions[0])
	}
	if !strings.Contains(versions[0].Content, `"threshold":80`) {
		t.Errorf("第一版应当是改之前的 80: %s", versions[0].Content)
	}
	if !strings.Contains(versions[1].Content, `"threshold":150`) {
		t.Errorf("第二版应当是改之后的 150: %s", versions[1].Content)
	}
	// 版本号要连续，不能因为中途 Update 了 version_seq 而跳号
	if versions[0].Version != 1 || versions[1].Version != 2 {
		t.Errorf("版本号应当是 1、2, got %d、%d", versions[0].Version, versions[1].Version)
	}
}

// TestRuleRollback 回滚：配置写回去、连续命中次数归零、回滚本身也记一版、
// 中间那几版仍然在历史里。
func TestRuleRollback(t *testing.T) {
	h, engine := newRuleVersionTestHandler(t)
	ruleJSON(t, engine, http.MethodPost, "/monitor/alert-rules", ruleBody80)
	ruleJSON(t, engine, http.MethodPut, "/monitor/alert-rules/1", ruleBody200)
	// 假装已经累了几次连续命中
	h.DB.Model(&model.AlertRule{}).Where("id = 1").Update("hit_streak", 5)

	code, resp := ruleJSON(t, engine, http.MethodPost,
		"/monitor/alert-rules/1/rollback", `{"versionId":1}`)
	if code != http.StatusOK {
		t.Fatalf("回滚失败 %d: %v", code, resp)
	}
	data := resp["data"].(map[string]any)
	if data["changed"] != true {
		t.Fatalf("应当报有改动: %v", data)
	}

	var rule model.AlertRule
	h.DB.First(&rule, 1)
	if rule.Threshold != 80 {
		t.Errorf("阈值应当回到 80, got %v", rule.Threshold)
	}
	if rule.HitStreak != 0 {
		t.Errorf("条件变了，连续命中次数应当归零, got %d", rule.HitStreak)
	}

	var versions []model.RuleVersion
	h.DB.Order("id asc").Find(&versions)
	if len(versions) != 3 {
		t.Fatalf("回滚也要记一版, 版本数 = %d", len(versions))
	}
	if versions[2].Source != "rollback" || !strings.Contains(versions[2].Note, "第 1 版") {
		t.Errorf("回滚版本不对: %+v", versions[2])
	}
	// 中间那一版（阈值 200）仍然在 —— 历史只追加
	if !strings.Contains(versions[1].Content, `"threshold":200`) {
		t.Error("回滚不该删掉中间版本")
	}

	// 再回滚到同一版 → 无改动
	_, resp = ruleJSON(t, engine, http.MethodPost,
		"/monitor/alert-rules/1/rollback", `{"versionId":1}`)
	if resp["data"].(map[string]any)["changed"] != false {
		t.Error("配置已经一样了，应当报无改动而不是再记一版")
	}
}

// TestRuleRollbackRejectsForeignVersion 拿别的规则的版本来回滚必须被拒。
func TestRuleRollbackRejectsForeignVersion(t *testing.T) {
	_, engine := newRuleVersionTestHandler(t)
	ruleJSON(t, engine, http.MethodPost, "/monitor/alert-rules", ruleBody80)
	ruleJSON(t, engine, http.MethodPost, "/monitor/alert-rules",
		`{"name":"另一条","metric":"host.offline","comparator":"lt","threshold":5,`+
			`"windowMinutes":30,"consecutiveTimes":1,"severity":"info"}`)

	// 规则 1 用规则 2 的版本回滚
	code, resp := ruleJSON(t, engine, http.MethodPost,
		"/monitor/alert-rules/1/rollback", `{"versionId":2}`)
	if code != http.StatusBadRequest {
		t.Fatalf("跨规则回滚应当被拒, got %d: %v", code, resp)
	}
	// 跨规则 diff 也要被拒
	code, _ = ruleJSON(t, engine, http.MethodGet,
		"/monitor/alert-rule-versions/diff?from=1&to=2", "")
	if code != http.StatusBadRequest {
		t.Errorf("跨规则 diff 应当被拒, got %d", code)
	}
}

// TestRuleDeleteThenRestore 删除留一版「删之前长这样」，
// 从它恢复出来的是一条**新规则**而且**默认停用**。
func TestRuleDeleteThenRestore(t *testing.T) {
	h, engine := newRuleVersionTestHandler(t)
	ruleJSON(t, engine, http.MethodPost, "/monitor/alert-rules", ruleBody80)

	code, resp := ruleJSON(t, engine, http.MethodDelete, "/monitor/alert-rules/1", "")
	if code != http.StatusOK {
		t.Fatalf("删除失败 %d: %v", code, resp)
	}
	note := resp["data"].(map[string]any)["note"].(string)
	if !strings.Contains(note, "恢复") {
		t.Errorf("删除的提示要告诉人可以恢复: %q", note)
	}

	var versions []model.RuleVersion
	h.DB.Order("id asc").Find(&versions)
	if len(versions) != 2 || versions[1].Source != "deleted" {
		t.Fatalf("删除应当留一版 deleted: %+v", versions)
	}

	// 版本列表要标出「规则已经不在了」
	_, resp = ruleJSON(t, engine, http.MethodGet, "/monitor/alert-rule-versions", "")
	list := resp["data"].(map[string]any)["versions"].([]any)
	if list[0].(map[string]any)["targetAlive"] != false {
		t.Error("规则已删，targetAlive 应当是 false")
	}

	// 恢复
	code, resp = ruleJSON(t, engine, http.MethodPost,
		"/monitor/alert-rule-versions/2/restore", "")
	if code != http.StatusOK {
		t.Fatalf("恢复失败 %d: %v", code, resp)
	}
	data := resp["data"].(map[string]any)
	restored := data["rule"].(map[string]any)
	if uint(restored["id"].(float64)) == 1 {
		t.Error("恢复出来的应当是一条新规则（新 ID），不是原地复活")
	}
	// 默认停用：误删之后直接开始评估等于在没人确认的情况下恢复了一条可能不适用的策略
	if restored["enabled"] != false {
		t.Error("恢复出来的规则应当默认停用")
	}
	if !strings.Contains(data["note"].(string), "默认停用") {
		t.Errorf("提示要说清默认停用: %v", data["note"])
	}
	if restored["threshold"].(float64) != 80 {
		t.Errorf("恢复出来的阈值不对: %v", restored["threshold"])
	}
}

// TestRuleRestoreRejectsAliveRule 规则还在的时候不该「恢复」——应该用回滚。
func TestRuleRestoreRejectsAliveRule(t *testing.T) {
	_, engine := newRuleVersionTestHandler(t)
	ruleJSON(t, engine, http.MethodPost, "/monitor/alert-rules", ruleBody80)

	code, resp := ruleJSON(t, engine, http.MethodPost,
		"/monitor/alert-rule-versions/1/restore", "")
	if code != http.StatusBadRequest {
		t.Fatalf("规则还在时不该允许恢复, got %d", code)
	}
	if !strings.Contains(resp["msg"].(string), "回滚") {
		t.Errorf("要指路到回滚: %v", resp["msg"])
	}
}

// TestFormatRuleValueReadable 比较符与布尔要翻成人话 ——
// 差异页上「gt → lt」远不如「大于 → 小于」好读。
func TestFormatRuleValueReadable(t *testing.T) {
	if got := formatRuleValue("comparator", "gt"); !strings.Contains(got, "大于") {
		t.Errorf("比较符没翻译: %q", got)
	}
	if got := formatRuleValue("enabled", false); got != "停用" {
		t.Errorf("布尔没翻译: %q", got)
	}
	// 整数不要显示成 80.000000
	if got := formatRuleValue("threshold", float64(80)); got != "80" {
		t.Errorf("整数阈值 = %q, want 80", got)
	}
	if got := formatRuleValue("threshold", 80.5); got != "80.5" {
		t.Errorf("小数阈值 = %q", got)
	}
	if got := formatRuleValue("name", nil); got != "" {
		t.Errorf("空值应当是空串, got %q", got)
	}
}

// TestSnapshotHashIsStable 同样的配置必须算出同样的 hash（否则去重会失效）。
// 这一条守的是「不能直接 Marshal map」——map 的遍历顺序是随机的。
func TestSnapshotHashIsStable(t *testing.T) {
	rule := model.AlertRule{
		Name: "x", Metric: "m", Comparator: "gt", Threshold: 1,
		WindowMinutes: 5, ConsecutiveTimes: 1, Severity: "info", Enabled: true,
	}
	_, first := marshalSnapshot(alertRuleSnapshot(rule), alertRuleFields)
	for i := 0; i < 20; i++ {
		if _, again := marshalSnapshot(alertRuleSnapshot(rule), alertRuleFields); again != first {
			t.Fatalf("同样的配置算出了不同的 hash: %s vs %s", first, again)
		}
	}
}
