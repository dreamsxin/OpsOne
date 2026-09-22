package handler

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"ops-platform/server/internal/middleware"
	"ops-platform/server/internal/model"
	"ops-platform/server/internal/response"
)

// 告警规则的版本历史、字段差异与回滚。
//
// 这一块是「策略审批」那一页的替代方案，而且是刻意不同的方案。参考站在改规则之前
// 插一个审批节点；这里不做审批，做的是**可追溯 + 可回滚**：
//
//   - 为什么不做审批：与 docs/SECURITY.md 第 8 节一贯的口径一致（内网自用、
//     管理员自己对自己负责），上一轮的「处置审批」也是同样理由没做。
//     加一层点「同意」不会让改动更正确，只会让人绕过它。
//   - 为什么必须做版本：审计日志只记「谁在什么时候 PUT 了哪个接口」，**不记请求体**。
//     所以在此之前，「谁把这条规则的阈值从 80 改成 200、改之前是什么样」查不出来。
//     而改错一条告警规则的后果是**静默的** —— 之后几周没人知道出了问题。
//     真正能兜住这件事的是「改动留痕 + 一键回滚」，不是事前审批。
//
// 与配置文件那套（ConfigVersion）的差别：配置文件有「机器上的现状」这个第三方，
// 所以那边要在下发前专门存一版 pre-apply 当回滚点。告警规则的生效态就是数据库里
// 那一行本身，没有第三方 —— 所以这里每次变更之后存一版就够，「改之前的样子」
// 天然是上一版。

const (
	ruleTargetAlert = "alert_rule"
	// ruleVersionListLimit 版本列表一次最多返回多少条。
	// 与配置文件版本列表同样的取舍：版本是只追加的，翻历史用不着一次全拿
	ruleVersionListLimit = 100
)

// ruleField 一个配置字段的元信息。
//
// 存在的理由：差异要做**字段级**而不是把两版 JSON 丢进行级 diff。
// 后者能跑（都是文本），但 JSON 里加一个字段就会让后面所有行错位显示成「改了」，
// 而且「阈值 80 → 200」这种话说不出来。
type ruleField struct {
	Key   string `json:"key"`
	Label string `json:"label"`
}

// alertRuleFields 告警规则的配置字段清单。
// **只含配置态**：HitStreak / LastValue / LastStatus / LastDetail / LastEvalAt /
// LastFireAt 那六个是每轮评估都在变的运行态，混进快照会让定时评估不停地产生新版本。
var alertRuleFields = []ruleField{
	{"name", "名称"},
	{"metric", "指标"},
	{"comparator", "比较符"},
	{"threshold", "阈值"},
	{"windowMinutes", "统计窗口（分钟）"},
	{"consecutiveTimes", "连续命中次数"},
	{"severity", "级别"},
	{"enabled", "启用"},
	{"remark", "备注"},
}

// comparatorChinese 比较符的中文说法。差异页上「gt → lt」远不如「大于 → 小于」好读；
// alert_rule.go 里那个 comparatorLabels 是给表达式用的符号（>、<），两者用途不同
var comparatorChinese = map[string]string{
	"gt": "大于", "gte": "大于等于", "lt": "小于", "lte": "小于等于",
}

// alertRuleSnapshot 把一条规则拍成配置快照。字段顺序固定，
// 这样同样的配置永远算出同一个 hash（map 的遍历顺序是随机的，不能直接 Marshal）。
func alertRuleSnapshot(rule model.AlertRule) map[string]any {
	return map[string]any{
		"name":             rule.Name,
		"metric":           rule.Metric,
		"comparator":       rule.Comparator,
		"threshold":        rule.Threshold,
		"windowMinutes":    rule.WindowMinutes,
		"consecutiveTimes": rule.ConsecutiveTimes,
		"severity":         rule.Severity,
		"enabled":          rule.Enabled,
		"remark":           rule.Remark,
	}
}

// marshalSnapshot 按 alertRuleFields 的顺序序列化，保证 hash 稳定
func marshalSnapshot(snapshot map[string]any, fields []ruleField) (string, string) {
	pairs := make([]string, 0, len(fields))
	for _, field := range fields {
		value, _ := json.Marshal(snapshot[field.Key])
		pairs = append(pairs, strconv.Quote(field.Key)+":"+string(value))
	}
	content := "{" + strings.Join(pairs, ",") + "}"
	sum := sha256.Sum256([]byte(content))
	return content, hex.EncodeToString(sum[:])
}

// recordAlertRuleVersion 存一版快照并推进 VersionSeq。
//
// 内容与上一版完全相同时**不产生新版本**：否则「点了保存但什么都没改」
// 会在历史里留下一串一模一样的版本，把真正的改动淹掉。返回是否真的存了。
func (h *Handler) recordAlertRuleVersion(rule model.AlertRule, source, note, operator string) bool {
	content, hash := marshalSnapshot(alertRuleSnapshot(rule), alertRuleFields)

	var last model.RuleVersion
	err := h.DB.Where("target = ? AND target_id = ?", ruleTargetAlert, rule.ID).
		Order("version desc").First(&last).Error
	// 删除要留痕，即使内容没变也存 —— 那一版的意义是「删掉之前长这样」
	if err == nil && last.Hash == hash && source != "deleted" {
		return false
	}

	version := model.RuleVersion{
		Target: ruleTargetAlert, TargetID: rule.ID, TargetName: rule.Name,
		Version: rule.VersionSeq + 1, Source: source,
		Content: content, Hash: hash,
		Note: truncate(note, 250), Operator: operator,
	}
	if err := h.DB.Create(&version).Error; err != nil {
		return false
	}
	// 版本号用「读出来 +1 再写回」而不是数据库序列，与 ConfigFile.VersionSeq 一致。
	// 并发下有竞态，单机管理场景可接受
	h.DB.Model(&model.AlertRule{}).Where("id = ?", rule.ID).
		Update("version_seq", version.Version)
	return true
}

// ---------- 版本列表与正文 ----------

// ListAlertRuleVersions 某条规则的版本历史。
//
// 列表**不带 content**：正文另外取。与配置文件版本列表同样的取舍 ——
// 列表页不需要每一版的全文，带上只是白传输。
func (h *Handler) ListAlertRuleVersions(c *gin.Context) {
	q := h.DB.Model(&model.RuleVersion{}).Where("target = ?", ruleTargetAlert)
	if v := strings.TrimSpace(c.Query("targetId")); v != "" {
		q = q.Where("target_id = ?", v)
	}
	if v := strings.TrimSpace(c.Query("source")); v != "" {
		q = q.Where("source = ?", v)
	}

	var list []model.RuleVersion
	if err := q.Select("id, target, target_id, target_name, version, source, hash, note, operator, created_at").
		Order("id desc").Limit(ruleVersionListLimit).Find(&list).Error; err != nil {
		response.Error(c, "查询版本历史失败")
		return
	}

	// 哪些版本对应的规则已经不在了 —— 那些是可以「恢复」的
	var aliveIDs []uint
	h.DB.Model(&model.AlertRule{}).Pluck("id", &aliveIDs)
	alive := make(map[uint]struct{}, len(aliveIDs))
	for _, id := range aliveIDs {
		alive[id] = struct{}{}
	}

	type versionView struct {
		model.RuleVersion
		// TargetAlive 规则是否还在。为假时这一版只能「恢复成一条新规则」，不能回滚
		TargetAlive bool `json:"targetAlive"`
	}
	views := make([]versionView, 0, len(list))
	for _, item := range list {
		_, ok := alive[item.TargetID]
		views = append(views, versionView{RuleVersion: item, TargetAlive: ok})
	}

	response.OK(c, gin.H{
		"versions": views,
		"limit":    ruleVersionListLimit,
		"notes": []string{
			"版本只追加、不修改，也不进数据留存清理 —— 它是回滚与误删恢复的唯一依据",
			"内容与上一版完全相同时不会产生新版本：否则「点了保存但什么都没改」会在历史里" +
				"留下一串一样的版本，把真正的改动淹掉",
			"source=deleted 的版本是「删掉之前长这样」。规则已经不在了的版本可以**恢复成一条新规则**" +
				"（新 ID），不是原地复活",
			"**平台不做策略审批**：审计日志不记请求体，所以真正兜住「谁悄悄改了阈值」的是" +
				"这份留痕 + 一键回滚，而不是事前点一下「同意」",
		},
	})
}

// GetAlertRuleVersion 取一版的完整快照（字段级展开，不是丢一坨 JSON 给人看）
func (h *Handler) GetAlertRuleVersion(c *gin.Context) {
	var version model.RuleVersion
	if err := h.DB.First(&version, idParam(c)).Error; err != nil {
		response.NotFound(c, "版本不存在")
		return
	}
	response.OK(c, gin.H{
		"version": version,
		"fields":  explodeSnapshot(version.Content, alertRuleFields),
	})
}

// snapshotField 展开后的一个字段
type snapshotField struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Value string `json:"value"`
}

// explodeSnapshot 把快照 JSON 展成「字段名 → 可读值」的有序列表
func explodeSnapshot(content string, fields []ruleField) []snapshotField {
	var raw map[string]any
	_ = json.Unmarshal([]byte(content), &raw)
	out := make([]snapshotField, 0, len(fields))
	for _, field := range fields {
		out = append(out, snapshotField{
			Key: field.Key, Label: field.Label,
			Value: formatRuleValue(field.Key, raw[field.Key]),
		})
	}
	return out
}

// formatRuleValue 把快照里的值说成人话。
// 比较符翻成中文、布尔翻成启用/停用 —— 差异页上「gt → lt」远不如「大于 → 小于」好读。
func formatRuleValue(key string, value any) string {
	if value == nil {
		return ""
	}
	switch key {
	case "comparator":
		if text, ok := value.(string); ok {
			if label := comparatorChinese[text]; label != "" {
				return label + "（" + text + "）"
			}
			return text
		}
	case "enabled":
		if flag, ok := value.(bool); ok {
			if flag {
				return "启用"
			}
			return "停用"
		}
	}
	switch v := value.(type) {
	case string:
		return v
	case float64:
		// 整数不要显示成 80.000000
		if v == float64(int64(v)) {
			return strconv.FormatInt(int64(v), 10)
		}
		return strconv.FormatFloat(v, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(v)
	}
	return fmt.Sprint(value)
}

// ---------- 字段级差异 ----------

type ruleDiffItem struct {
	Key     string `json:"key"`
	Label   string `json:"label"`
	Before  string `json:"before"`
	After   string `json:"after"`
	Changed bool   `json:"changed"`
}

// DiffAlertRuleVersions 两版之间的字段级差异。
//
// 刻意不用配置文件那套行级 unifiedDiff：两版快照都是 JSON，按行比对的话
// 加一个字段就会让后面全部错位显示成「改了」，而且说不出「阈值 80 → 200」这种话。
func (h *Handler) DiffAlertRuleVersions(c *gin.Context) {
	fromID := strings.TrimSpace(c.Query("from"))
	toID := strings.TrimSpace(c.Query("to"))
	if fromID == "" || toID == "" {
		response.BadRequest(c, "请指定 from 与 to 两个版本")
		return
	}

	var left, right model.RuleVersion
	if err := h.DB.First(&left, fromID).Error; err != nil {
		response.NotFound(c, "起始版本不存在")
		return
	}
	if err := h.DB.First(&right, toID).Error; err != nil {
		response.NotFound(c, "目标版本不存在")
		return
	}
	// 不允许跨目标比：两条不同规则的版本放一起比出来的差异没有意义
	if left.Target != right.Target || left.TargetID != right.TargetID {
		response.BadRequest(c, "两个版本不属于同一条规则，没法比较")
		return
	}

	items := diffSnapshots(left.Content, right.Content, alertRuleFields)
	changed := 0
	for _, item := range items {
		if item.Changed {
			changed++
		}
	}

	response.OK(c, gin.H{
		"left": gin.H{
			"id": left.ID, "version": left.Version, "source": left.Source,
			"operator": left.Operator, "createdAt": left.CreatedAt, "hash": left.Hash,
		},
		"right": gin.H{
			"id": right.ID, "version": right.Version, "source": right.Source,
			"operator": right.Operator, "createdAt": right.CreatedAt, "hash": right.Hash,
		},
		"same":    left.Hash == right.Hash,
		"changed": changed,
		"items":   items,
	})
}

func diffSnapshots(leftContent, rightContent string, fields []ruleField) []ruleDiffItem {
	var left, right map[string]any
	_ = json.Unmarshal([]byte(leftContent), &left)
	_ = json.Unmarshal([]byte(rightContent), &right)

	out := make([]ruleDiffItem, 0, len(fields))
	for _, field := range fields {
		before := formatRuleValue(field.Key, left[field.Key])
		after := formatRuleValue(field.Key, right[field.Key])
		out = append(out, ruleDiffItem{
			Key: field.Key, Label: field.Label,
			Before: before, After: after, Changed: before != after,
		})
	}
	return out
}

// ---------- 回滚与误删恢复 ----------

type rollbackReq struct {
	VersionID uint   `json:"versionId" binding:"required"`
	Note      string `json:"note"`
}

// RollbackAlertRule 把规则回滚到某一版。
//
// 与配置文件的回滚同样的两条保障：必须显式指定版本（不猜「上一版」），
// 并且目标版本必须属于这条规则（防拿别人的版本覆盖）。
// 回滚本身也产生一个新版本（source=rollback）—— 历史只追加，不会因为回滚而丢掉中间那几版。
func (h *Handler) RollbackAlertRule(c *gin.Context) {
	var rule model.AlertRule
	if err := h.DB.First(&rule, idParam(c)).Error; err != nil {
		response.NotFound(c, "规则不存在")
		return
	}

	var req rollbackReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "请指定要回滚到的版本")
		return
	}

	var version model.RuleVersion
	if err := h.DB.First(&version, req.VersionID).Error; err != nil {
		response.NotFound(c, "版本不存在")
		return
	}
	if version.Target != ruleTargetAlert || version.TargetID != rule.ID {
		response.BadRequest(c, "这一版不属于当前规则，不能用它回滚")
		return
	}

	current, currentHash := marshalSnapshot(alertRuleSnapshot(rule), alertRuleFields)
	_ = current
	if currentHash == version.Hash {
		response.OK(c, gin.H{
			"changed": false,
			"note":    fmt.Sprintf("当前配置与第 %d 版完全相同，没有改动", version.Version),
		})
		return
	}

	var snapshot map[string]any
	if err := json.Unmarshal([]byte(version.Content), &snapshot); err != nil {
		response.Error(c, "这一版的快照解不开，无法回滚")
		return
	}

	updates, err := snapshotToUpdates(snapshot)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	// 与 UpdateAlertRule 同一个口径：条件变了，之前累积的连续命中次数不再可比
	updates["hit_streak"] = 0
	if err := h.DB.Model(&model.AlertRule{}).Where("id = ?", rule.ID).
		Updates(updates).Error; err != nil {
		response.Error(c, "回滚失败")
		return
	}

	var after model.AlertRule
	h.DB.First(&after, rule.ID)
	note := req.Note
	if note == "" {
		note = fmt.Sprintf("回滚到第 %d 版", version.Version)
	}
	h.recordAlertRuleVersion(after, "rollback", note, middleware.CurrentUser(c).Username)

	h.DB.First(&after, rule.ID)
	response.OK(c, gin.H{
		"changed": true, "rule": after,
		"note": fmt.Sprintf("已回滚到第 %d 版；本次回滚本身也记成了一个新版本，"+
			"中间那几版仍然在历史里", version.Version),
	})
}

// RestoreAlertRuleVersion 从某一版恢复出一条规则。
//
// 用在「误删」上：删除时会存一版 source=deleted 的快照，这里按它重建。
// **重建出来的是一条新规则（新 ID）**，不是原地复活 —— 旧 ID 上挂过的告警、
// 评估状态都已经结束了，硬塞回同一个 ID 只会造成更难解释的状态。
func (h *Handler) RestoreAlertRuleVersion(c *gin.Context) {
	var version model.RuleVersion
	if err := h.DB.First(&version, idParam(c)).Error; err != nil {
		response.NotFound(c, "版本不存在")
		return
	}
	if version.Target != ruleTargetAlert {
		response.BadRequest(c, "这一版不是告警规则的快照")
		return
	}

	var exists int64
	h.DB.Model(&model.AlertRule{}).Where("id = ?", version.TargetID).Count(&exists)
	if exists > 0 {
		response.BadRequest(c, fmt.Sprintf(
			"规则「%s」还在，不需要恢复 —— 要改回旧配置请用「回滚」", version.TargetName))
		return
	}

	var snapshot map[string]any
	if err := json.Unmarshal([]byte(version.Content), &snapshot); err != nil {
		response.Error(c, "这一版的快照解不开，无法恢复")
		return
	}
	updates, err := snapshotToUpdates(snapshot)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	user := middleware.CurrentUser(c)
	rule := model.AlertRule{
		Name:       asString(updates["name"]),
		Metric:     asString(updates["metric"]),
		Comparator: asString(updates["comparator"]),
		Severity:   asString(updates["severity"]),
		Remark:     asString(updates["remark"]),
		LastStatus: "unknown", CreatedBy: user.ID,
	}
	rule.Threshold, _ = updates["threshold"].(float64)
	rule.WindowMinutes, _ = updates["window_minutes"].(int)
	rule.ConsecutiveTimes, _ = updates["consecutive_times"].(int)
	rule.Enabled, _ = updates["enabled"].(bool)

	// 恢复出来的规则默认**停用**：误删之后直接让它开始评估并发告警，
	// 等于在没人确认的情况下恢复了一条可能已经不适用的策略
	wasEnabled := rule.Enabled
	rule.Enabled = false

	if err := h.DB.Create(&rule).Error; err != nil {
		response.Error(c, "恢复失败")
		return
	}
	h.recordAlertRuleVersion(rule, "restored",
		fmt.Sprintf("从「%s」第 %d 版恢复", version.TargetName, version.Version),
		user.Username)

	var after model.AlertRule
	h.DB.First(&after, rule.ID)
	note := fmt.Sprintf("已按「%s」第 %d 版恢复成一条**新规则**（ID %d）。",
		version.TargetName, version.Version, rule.ID)
	if wasEnabled {
		note += "恢复出来的规则**默认停用** —— 那一版当时是启用的，" +
			"但直接让它开始评估等于在没人确认的情况下恢复了一条可能已经不适用的策略，请确认后手动启用"
	}
	response.OK(c, gin.H{"rule": after, "note": note})
}

// snapshotToUpdates 把快照转成可以写回数据库的列映射，并做一次基本校验。
// JSON 里的数字一律是 float64，要转回 int 才能写进 int 列。
func snapshotToUpdates(snapshot map[string]any) (map[string]any, error) {
	name := asString(snapshot["name"])
	metric := asString(snapshot["metric"])
	if name == "" || metric == "" {
		return nil, fmt.Errorf("这一版的快照缺少名称或指标，无法使用")
	}
	comparator := asString(snapshot["comparator"])
	if comparatorChinese[comparator] == "" {
		return nil, fmt.Errorf("这一版的比较符 %q 已不受支持", comparator)
	}

	threshold, _ := snapshot["threshold"].(float64)
	window := intFromJSON(snapshot["windowMinutes"])
	times := intFromJSON(snapshot["consecutiveTimes"])
	if times <= 0 {
		times = 1
	}
	enabled, _ := snapshot["enabled"].(bool)

	return map[string]any{
		"name": name, "metric": metric, "comparator": comparator,
		"threshold": threshold, "window_minutes": window,
		"consecutive_times": times, "severity": asString(snapshot["severity"]),
		"enabled": enabled, "remark": asString(snapshot["remark"]),
	}, nil
}

func asString(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	return ""
}

func intFromJSON(value any) int {
	switch v := value.(type) {
	case float64:
		return int(v)
	case int:
		return v
	}
	return 0
}
