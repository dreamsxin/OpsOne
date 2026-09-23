package handler

// 检测规则与聚合策略的版本化。
//
// 版本机制第一版只接了告警规则，这一组测试守的是「接上另外两种之后口径没有漂」：
// 同一套去重、同一套删除留痕、同一套「恢复出来默认停用」，以及**多态表最容易出的错**
// —— target 不同但 target_id 相同的两条记录，版本历史必须互不串。

import (
	"net/http"
	"strconv"
	"strings"
	"testing"

	"ops-platform/server/internal/model"
)

const detectionBody30 = `{"name":"网关连环故障","mode":"concurrent","windowMinutes":30,` +
	`"severity":"warning","remark":"初版","steps":[` +
	`{"name":"网关 5xx","titleKeyword":"5xx"},{"name":"数据库慢","titleKeyword":"slow"}]}`
const detectionBody60 = `{"name":"网关连环故障","mode":"concurrent","windowMinutes":60,` +
	`"severity":"critical","remark":"初版","steps":[` +
	`{"name":"网关 5xx","titleKeyword":"5xx"},{"name":"数据库慢","titleKeyword":"slow"}]}`

const aggregationBody2 = `{"name":"按来源归桶","dimensions":"source,severity",` +
	`"windowMinutes":60,"minCount":2,"priority":100,"remark":"初版"}`
const aggregationBody5 = `{"name":"按来源归桶","dimensions":"source,severity",` +
	`"windowMinutes":60,"minCount":5,"priority":100,"suppressNotify":true,"remark":"初版"}`

// 列表接口必须显式指定 target。
//
// 默认成告警规则的话，前端少传一个参数就会安静地查错一张表 ——
// 返回的是别人的版本历史，而页面上看不出任何异常。
func TestRuleVersionRequiresTarget(t *testing.T) {
	_, engine := newRuleVersionTestHandler(t)

	code, resp := ruleJSON(t, engine, http.MethodGet, "/monitor/rule-versions", "")
	if code != http.StatusBadRequest {
		t.Fatalf("不传 target 应该被拒，实际 %d", code)
	}
	msg, _ := resp["msg"].(string)
	for _, want := range []string{"alert_rule", "detection_rule", "aggregation_policy"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("错误信息要列出可选值，缺 %s: %v", want, msg)
		}
	}

	if code, resp := ruleJSON(t, engine, http.MethodGet,
		"/monitor/rule-versions?target=notify_route", ""); code != http.StatusBadRequest {
		t.Fatalf("不支持的 target 应该被拒，实际 %d %v", code, resp["msg"])
	}
}

// 多态表最容易出的错：target 不同但 target_id 相同，历史不能串
func TestRuleVersionsAreIsolatedByTarget(t *testing.T) {
	_, engine := newRuleVersionTestHandler(t)
	ruleJSON(t, engine, http.MethodPost, "/monitor/alert-rules", ruleBody80)
	ruleJSON(t, engine, http.MethodPost, "/monitor/detections", detectionBody30)
	ruleJSON(t, engine, http.MethodPost, "/monitor/aggregations", aggregationBody2)

	cases := []struct{ target, wantName string }{
		{"alert_rule", "磁盘告警"},
		{"detection_rule", "网关连环故障"},
		{"aggregation_policy", "按来源归桶"},
	}
	for _, tc := range cases {
		_, resp := ruleJSON(t, engine, http.MethodGet,
			"/monitor/rule-versions?target="+tc.target+"&targetId=1", "")
		data := resp["data"].(map[string]any)
		versions := data["versions"].([]any)
		if len(versions) != 1 {
			t.Fatalf("%s 应该只有自己的 1 版，实际 %d 版", tc.target, len(versions))
		}
		row := versions[0].(map[string]any)
		if row["targetName"] != tc.wantName {
			t.Fatalf("%s 的版本串到了别人身上: %v", tc.target, row["targetName"])
		}
		if row["target"] != tc.target {
			t.Fatalf("target 字段不对: %v", row["target"])
		}
		// 字段清单也要跟着 target 变，否则差异页会用错一套中文名
		fields := data["fields"].([]any)
		if len(fields) == 0 {
			t.Fatalf("%s 没有返回字段清单", tc.target)
		}
	}
}

// 检测规则：编辑 → 字段级差异 → 回滚 → 删除留痕 → 恢复成新规则且默认停用
func TestDetectionRuleVersioning(t *testing.T) {
	h, engine := newRuleVersionTestHandler(t)
	ruleJSON(t, engine, http.MethodPost, "/monitor/detections", detectionBody30)
	ruleJSON(t, engine, http.MethodPut, "/monitor/detections/1", detectionBody60)

	_, resp := ruleJSON(t, engine, http.MethodGet,
		"/monitor/rule-versions?target=detection_rule&targetId=1", "")
	versions := resp["data"].(map[string]any)["versions"].([]any)
	if len(versions) != 2 {
		t.Fatalf("应该有 2 版（新建 + 编辑），实际 %d", len(versions))
	}

	// 差异必须说人话：窗口与级别各改了一处
	_, resp = ruleJSON(t, engine, http.MethodGet, "/monitor/rule-versions/diff?from=1&to=2", "")
	data := resp["data"].(map[string]any)
	if data["label"] != "检测规则" {
		t.Fatalf("差异结果要带上目标类型: %v", data["label"])
	}
	changed := map[string]string{}
	for _, raw := range data["items"].([]any) {
		item := raw.(map[string]any)
		if item["changed"] == true {
			changed[item["label"].(string)] = item["before"].(string) + " → " + item["after"].(string)
		}
	}
	if changed["关联窗口（分钟）"] != "30 → 60" {
		t.Fatalf("窗口差异不对: %v", changed)
	}
	if changed["级别"] != "warning → critical" {
		t.Fatalf("级别差异不对: %v", changed)
	}

	// 回滚回第 1 版
	code, resp := ruleJSON(t, engine, http.MethodPost, "/monitor/rule-versions/rollback",
		`{"target":"detection_rule","ruleId":1,"versionId":1}`)
	if code != http.StatusOK || resp["data"].(map[string]any)["changed"] != true {
		t.Fatalf("回滚失败: %v", resp)
	}
	var rule model.DetectionRule
	h.DB.First(&rule, 1)
	if rule.WindowMinutes != 30 || rule.Severity != "warning" {
		t.Fatalf("回滚后的配置不对: %+v", rule)
	}

	// 删除留痕 → 恢复成新规则，且默认停用
	ruleJSON(t, engine, http.MethodDelete, "/monitor/detections/1", "")
	var deleted model.RuleVersion
	h.DB.Where("target = ? AND source = ?", "detection_rule", "deleted").First(&deleted)
	if deleted.ID == 0 {
		t.Fatal("删除应该留一版 source=deleted")
	}

	code, resp = ruleJSON(t, engine, http.MethodPost,
		"/monitor/rule-versions/"+uintToStr(deleted.ID)+"/restore", "")
	if code != http.StatusOK {
		t.Fatalf("恢复失败: %v", resp)
	}
	restored := resp["data"].(map[string]any)
	newID := uint(restored["id"].(float64))
	if newID == 1 {
		t.Fatal("恢复应该建一条新规则（新 ID），不是原地复活")
	}
	var after model.DetectionRule
	h.DB.First(&after, newID)
	if after.Enabled {
		t.Fatal("恢复出来的检测规则必须默认停用 —— 直接让它开始评估等于没人确认就恢复了一条策略")
	}
	if after.Steps == "" || after.Mode != "concurrent" {
		t.Fatalf("恢复出来的配置不完整: %+v", after)
	}
}

// 聚合策略：布尔开关与数值都要能回滚
func TestAggregationPolicyVersioning(t *testing.T) {
	h, engine := newRuleVersionTestHandler(t)
	ruleJSON(t, engine, http.MethodPost, "/monitor/aggregations", aggregationBody2)
	ruleJSON(t, engine, http.MethodPut, "/monitor/aggregations/1", aggregationBody5)

	_, resp := ruleJSON(t, engine, http.MethodGet, "/monitor/rule-versions/diff?from=1&to=2", "")
	changed := map[string]string{}
	for _, raw := range resp["data"].(map[string]any)["items"].([]any) {
		item := raw.(map[string]any)
		if item["changed"] == true {
			changed[item["label"].(string)] = item["before"].(string) + " → " + item["after"].(string)
		}
	}
	if changed["成桶阈值"] != "2 → 5" {
		t.Fatalf("阈值差异不对: %v", changed)
	}
	// 布尔要说人话，不是 true/false
	if changed["抑制重复通知"] != "关闭 → 开启" {
		t.Fatalf("开关差异应该是中文: %v", changed)
	}

	code, _ := ruleJSON(t, engine, http.MethodPost, "/monitor/rule-versions/rollback",
		`{"target":"aggregation_policy","ruleId":1,"versionId":1}`)
	if code != http.StatusOK {
		t.Fatal("回滚失败")
	}
	var policy model.AggregationPolicy
	h.DB.First(&policy, 1)
	if policy.MinCount != 2 || policy.SuppressNotify {
		t.Fatalf("回滚后的配置不对: %+v", policy)
	}
}

// 恢复时快照不可用要拒绝，而不是建出一条坏记录。
//
// 检测规则的步骤是 JSON 原文，少于两步的规则每轮评估都会报错 ——
// 这种东西不该因为「恢复」而被放进库里。
func TestRestoreRejectsBrokenSnapshot(t *testing.T) {
	h, engine := newRuleVersionTestHandler(t)
	ruleJSON(t, engine, http.MethodPost, "/monitor/detections", detectionBody30)
	ruleJSON(t, engine, http.MethodDelete, "/monitor/detections/1", "")

	var deleted model.RuleVersion
	h.DB.Where("target = ? AND source = ?", "detection_rule", "deleted").First(&deleted)
	// 把快照里的步骤改成只有一步
	broken := strings.Replace(deleted.Content,
		`[{"name":"网关 5xx","titleKeyword":"5xx","source":"","severity":"","labelKey":"","labelValue":""},`,
		`[`, 1)
	if broken == deleted.Content {
		// 步骤序列化的具体形状可能变，退一步用一个必然不可用的值
		broken = strings.Replace(deleted.Content, deleted.Content, `{"name":"x","mode":"concurrent","steps":"[]"}`, 1)
	}
	h.DB.Model(&model.RuleVersion{}).Where("id = ?", deleted.ID).Update("content", broken)

	code, resp := ruleJSON(t, engine, http.MethodPost,
		"/monitor/rule-versions/"+uintToStr(deleted.ID)+"/restore", "")
	if code == http.StatusOK {
		t.Fatalf("步骤不可用的快照不该恢复成功: %v", resp)
	}
	var count int64
	h.DB.Model(&model.DetectionRule{}).Count(&count)
	if count != 0 {
		t.Fatal("恢复被拒之后不该留下半成品记录")
	}
}

// 目标类型清单给前端用，每一种都要带上字段清单与口径说明
func TestRuleVersionTargetsEndpoint(t *testing.T) {
	_, engine := newRuleVersionTestHandler(t)
	_, resp := ruleJSON(t, engine, http.MethodGet, "/monitor/rule-version-targets", "")
	list, ok := resp["data"].([]any)
	// 规则 3 种 + 权限 3 种（角色 / 资源授权 / 容器授权）
	if !ok || len(list) < 6 {
		t.Fatalf("至少应该返回 6 种目标: %v", resp["data"])
	}
	targets := map[string]bool{}
	for _, raw := range list {
		targets[raw.(map[string]any)["target"].(string)] = true
	}
	for _, want := range []string{"alert_rule", "role", "resource_grant", "kube_grant"} {
		if !targets[want] {
			t.Errorf("清单里缺 %s", want)
		}
	}
	for _, raw := range list {
		item := raw.(map[string]any)
		if item["label"] == "" || item["runtimeNote"] == "" {
			t.Fatalf("目标定义不完整: %v", item)
		}
		if fields, _ := item["fields"].([]any); len(fields) == 0 {
			t.Fatalf("%v 没有字段清单", item["target"])
		}
	}
}

func uintToStr(v uint) string {
	return strconv.FormatUint(uint64(v), 10)
}
