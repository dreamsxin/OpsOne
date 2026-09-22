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

// 规则类配置的版本历史、字段差异与回滚。
//
// 这一块是「策略审批」那一页的替代方案，而且是刻意不同的方案。参考站在改规则之前
// 插一个审批节点；这里不做审批，做的是**可追溯 + 可回滚**：
//
//   - 为什么不做审批：与 docs/SECURITY.md 第 8 节一贯的口径一致（内网自用、
//     管理员自己对自己负责），「处置审批」也是同样理由没做。
//     加一层点「同意」不会让改动更正确，只会让人绕过它。
//   - 为什么必须做版本：审计日志只记「谁在什么时候 PUT 了哪个接口」，**不记请求体**。
//     所以在此之前，「谁把这条规则的阈值从 80 改成 200、改之前是什么样」查不出来。
//     而改错一条规则的后果是**静默的** —— 之后几周没人知道出了问题。
//     真正能兜住这件事的是「改动留痕 + 一键回滚」，不是事前审批。
//
// 与配置文件那套（ConfigVersion）的差别：配置文件有「机器上的现状」这个第三方，
// 所以那边要在下发前专门存一版 pre-apply 当回滚点。规则的生效态就是数据库里
// 那一行本身，没有第三方 —— 所以这里每次变更之后存一版就够，「改之前的样子」
// 天然是上一版。
//
// # 三种规则共用一套机制
//
// 第一版只接了告警规则。接检测规则与聚合策略时没有复制三份，而是抽出
// ruleTargetSpec 注册表：每种目标只描述「有哪些配置字段、怎么读一行、
// 怎么把快照写回去、回滚时哪些运行态要归零、恢复时怎么建新记录」，
// 列表 / 正文 / 差异 / 回滚 / 恢复五个接口是同一份代码。
//
// 复制三份的代价不是行数，是**口径会漂**：比如「删除即使内容没变也要存一版」
// 这种规则，改了一处忘了另两处，就会出现「有的规则删了能恢复、有的不能」。

const (
	ruleTargetAlert       = "alert_rule"
	ruleTargetDetection   = "detection_rule"
	ruleTargetAggregation = "aggregation_policy"
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

// ruleSnapshotRow 一条规则的当前状态，只含版本机制需要的部分
type ruleSnapshotRow struct {
	ID         uint
	Name       string
	VersionSeq int
	Snapshot   map[string]any
}

// ruleTargetSpec 一种可版本化的规则。
//
// 所有闭包都只做自己那一种规则的事，通用逻辑（hash、去重、版本号推进、
// 跨目标校验、恢复后默认停用）都在外面，三种目标共享。
type ruleTargetSpec struct {
	Target string `json:"target"`
	Label  string `json:"label"`
	// Fields 配置字段清单，顺序即展示顺序，也决定 hash 的稳定性
	Fields []ruleField `json:"fields"`
	// RuntimeNote 说明哪些运行态字段刻意不进快照
	RuntimeNote string `json:"runtimeNote"`

	// Load 读一行当前配置，不存在时 ok=false
	Load func(h *Handler, id uint) (ruleSnapshotRow, bool) `json:"-"`
	// AliveIDs 现存记录的 ID：用来判断某一版还能不能回滚（不能就只能恢复成新记录）
	AliveIDs func(h *Handler) []uint `json:"-"`
	// BumpSeq 把版本号写回业务表
	BumpSeq func(h *Handler, id uint, version int) `json:"-"`
	// ToUpdates 快照 → 数据库列映射，顺带做基本校验（JSON 数字都是 float64，要转回 int）
	ToUpdates func(snapshot map[string]any) (map[string]any, error) `json:"-"`
	// ResetOnRollback 回滚时要一并归零的运行态列。
	// 条件变了，之前累积的连续命中次数不再可比
	ResetOnRollback map[string]any `json:"-"`
	// Apply 把列映射写回已有记录
	Apply func(h *Handler, id uint, updates map[string]any) error `json:"-"`
	// Create 按列映射建一条新记录（用于误删恢复），返回新 ID
	Create func(h *Handler, updates map[string]any, userID uint) (uint, error) `json:"-"`
}

// comparatorChinese 比较符的中文说法。差异页上「gt → lt」远不如「大于 → 小于」好读；
// alert_rule.go 里那个 comparatorLabels 是给表达式用的符号（>、<），两者用途不同
var comparatorChinese = map[string]string{
	"gt": "大于", "gte": "大于等于", "lt": "小于", "lte": "小于等于",
}

// ruleTargetSpecs 三种规则的定义。新增一种可版本化的规则只要往这里加一项。
var ruleTargetSpecs = []ruleTargetSpec{
	{
		Target: ruleTargetAlert, Label: "告警规则",
		Fields: []ruleField{
			{"name", "名称"}, {"metric", "指标"}, {"comparator", "比较符"},
			{"threshold", "阈值"}, {"windowMinutes", "统计窗口（分钟）"},
			{"consecutiveTimes", "连续命中次数"}, {"severity", "级别"},
			{"enabled", "启用"}, {"remark", "备注"},
		},
		RuntimeNote: "HitStreak / LastValue / LastStatus / LastDetail / LastEvalAt / LastFireAt " +
			"六个运行态不进快照 —— 混进去会让定时评估不停地产生新版本",
		Load: func(h *Handler, id uint) (ruleSnapshotRow, bool) {
			var rule model.AlertRule
			if err := h.DB.First(&rule, id).Error; err != nil {
				return ruleSnapshotRow{}, false
			}
			return ruleSnapshotRow{
				ID: rule.ID, Name: rule.Name, VersionSeq: rule.VersionSeq,
				Snapshot: map[string]any{
					"name": rule.Name, "metric": rule.Metric, "comparator": rule.Comparator,
					"threshold": rule.Threshold, "windowMinutes": rule.WindowMinutes,
					"consecutiveTimes": rule.ConsecutiveTimes, "severity": rule.Severity,
					"enabled": rule.Enabled, "remark": rule.Remark,
				},
			}, true
		},
		AliveIDs: func(h *Handler) []uint {
			var ids []uint
			h.DB.Model(&model.AlertRule{}).Pluck("id", &ids)
			return ids
		},
		BumpSeq: func(h *Handler, id uint, version int) {
			h.DB.Model(&model.AlertRule{}).Where("id = ?", id).Update("version_seq", version)
		},
		ToUpdates:       alertSnapshotToUpdates,
		ResetOnRollback: map[string]any{"hit_streak": 0},
		Apply: func(h *Handler, id uint, updates map[string]any) error {
			return h.DB.Model(&model.AlertRule{}).Where("id = ?", id).Updates(updates).Error
		},
		Create: func(h *Handler, updates map[string]any, userID uint) (uint, error) {
			rule := model.AlertRule{
				Name: asString(updates["name"]), Metric: asString(updates["metric"]),
				Comparator: asString(updates["comparator"]), Severity: asString(updates["severity"]),
				Remark: asString(updates["remark"]), LastStatus: "unknown", CreatedBy: userID,
			}
			rule.Threshold, _ = updates["threshold"].(float64)
			rule.WindowMinutes, _ = updates["window_minutes"].(int)
			rule.ConsecutiveTimes, _ = updates["consecutive_times"].(int)
			if err := h.DB.Create(&rule).Error; err != nil {
				return 0, err
			}
			return rule.ID, nil
		},
	},
	{
		Target: ruleTargetDetection, Label: "检测规则",
		Fields: []ruleField{
			{"name", "名称"}, {"mode", "关联方式"}, {"steps", "步骤定义"},
			{"joinLabel", "对齐标签"}, {"windowMinutes", "关联窗口（分钟）"},
			{"severity", "级别"}, {"enabled", "启用"}, {"remark", "备注"},
		},
		RuntimeNote: "LastStatus / LastDetail / LastEvalAt / LastFireAt 不进快照",
		Load: func(h *Handler, id uint) (ruleSnapshotRow, bool) {
			var rule model.DetectionRule
			if err := h.DB.First(&rule, id).Error; err != nil {
				return ruleSnapshotRow{}, false
			}
			return ruleSnapshotRow{
				ID: rule.ID, Name: rule.Name, VersionSeq: rule.VersionSeq,
				Snapshot: map[string]any{
					"name": rule.Name, "mode": rule.Mode, "steps": rule.Steps,
					"joinLabel": rule.JoinLabel, "windowMinutes": rule.WindowMinutes,
					"severity": rule.Severity, "enabled": rule.Enabled, "remark": rule.Remark,
				},
			}, true
		},
		AliveIDs: func(h *Handler) []uint {
			var ids []uint
			h.DB.Model(&model.DetectionRule{}).Pluck("id", &ids)
			return ids
		},
		BumpSeq: func(h *Handler, id uint, version int) {
			h.DB.Model(&model.DetectionRule{}).Where("id = ?", id).Update("version_seq", version)
		},
		ToUpdates: detectionSnapshotToUpdates,
		// 检测规则没有连续命中计数，回滚不需要归零任何东西
		ResetOnRollback: nil,
		Apply: func(h *Handler, id uint, updates map[string]any) error {
			return h.DB.Model(&model.DetectionRule{}).Where("id = ?", id).Updates(updates).Error
		},
		Create: func(h *Handler, updates map[string]any, userID uint) (uint, error) {
			rule := model.DetectionRule{
				Name: asString(updates["name"]), Mode: asString(updates["mode"]),
				Steps: asString(updates["steps"]), JoinLabel: asString(updates["join_label"]),
				Severity: asString(updates["severity"]), Remark: asString(updates["remark"]),
				LastStatus: "unknown", CreatedBy: userID,
			}
			rule.WindowMinutes, _ = updates["window_minutes"].(int)
			if err := h.DB.Create(&rule).Error; err != nil {
				return 0, err
			}
			return rule.ID, nil
		},
	},
	{
		Target: ruleTargetAggregation, Label: "聚合策略",
		Fields: []ruleField{
			{"name", "名称"}, {"dimensions", "归桶维度"}, {"matchSeverity", "匹配级别"},
			{"windowMinutes", "窗口（分钟）"}, {"minCount", "成桶阈值"},
			{"suppressNotify", "抑制重复通知"}, {"priority", "优先级"},
			{"enabled", "启用"}, {"remark", "备注"},
		},
		RuntimeNote: "聚合策略没有运行态字段，快照就是它的全部配置",
		Load: func(h *Handler, id uint) (ruleSnapshotRow, bool) {
			var policy model.AggregationPolicy
			if err := h.DB.First(&policy, id).Error; err != nil {
				return ruleSnapshotRow{}, false
			}
			return ruleSnapshotRow{
				ID: policy.ID, Name: policy.Name, VersionSeq: policy.VersionSeq,
				Snapshot: map[string]any{
					"name": policy.Name, "dimensions": policy.Dimensions,
					"matchSeverity": policy.MatchSeverity, "windowMinutes": policy.WindowMinutes,
					"minCount": policy.MinCount, "suppressNotify": policy.SuppressNotify,
					"priority": policy.Priority, "enabled": policy.Enabled, "remark": policy.Remark,
				},
			}, true
		},
		AliveIDs: func(h *Handler) []uint {
			var ids []uint
			h.DB.Model(&model.AggregationPolicy{}).Pluck("id", &ids)
			return ids
		},
		BumpSeq: func(h *Handler, id uint, version int) {
			h.DB.Model(&model.AggregationPolicy{}).Where("id = ?", id).Update("version_seq", version)
		},
		ToUpdates:       aggregationSnapshotToUpdates,
		ResetOnRollback: nil,
		Apply: func(h *Handler, id uint, updates map[string]any) error {
			return h.DB.Model(&model.AggregationPolicy{}).Where("id = ?", id).Updates(updates).Error
		},
		Create: func(h *Handler, updates map[string]any, userID uint) (uint, error) {
			policy := model.AggregationPolicy{
				Name: asString(updates["name"]), Dimensions: asString(updates["dimensions"]),
				MatchSeverity: asString(updates["match_severity"]), Remark: asString(updates["remark"]),
				CreatedBy: userID,
			}
			policy.WindowMinutes, _ = updates["window_minutes"].(int)
			policy.MinCount, _ = updates["min_count"].(int)
			policy.Priority, _ = updates["priority"].(int)
			policy.SuppressNotify, _ = updates["suppress_notify"].(bool)
			if err := h.DB.Create(&policy).Error; err != nil {
				return 0, err
			}
			return policy.ID, nil
		},
	},
}

func ruleSpecOf(target string) (ruleTargetSpec, bool) {
	for _, spec := range ruleTargetSpecs {
		if spec.Target == target {
			return spec, true
		}
	}
	return ruleTargetSpec{}, false
}

func ruleTargetNames() string {
	names := make([]string, 0, len(ruleTargetSpecs))
	for _, spec := range ruleTargetSpecs {
		names = append(names, spec.Target)
	}
	return strings.Join(names, " / ")
}

// resolveRuleSpec 从查询参数取目标类型。
//
// 刻意不给默认值：默认成告警规则的话，前端少传一个参数就会安静地查错一张表 ——
// 返回的是别人的版本历史，而页面上看不出任何异常。
func resolveRuleSpec(c *gin.Context) (ruleTargetSpec, bool) {
	target := strings.TrimSpace(c.Query("target"))
	if target == "" {
		response.BadRequest(c, "请指定 target："+ruleTargetNames())
		return ruleTargetSpec{}, false
	}
	spec, ok := ruleSpecOf(target)
	if !ok {
		response.BadRequest(c, fmt.Sprintf("不支持的 target %q，可选：%s", target, ruleTargetNames()))
		return ruleTargetSpec{}, false
	}
	return spec, true
}

// marshalSnapshot 按字段清单的顺序序列化，保证同样的配置永远算出同一个 hash
// （map 的遍历顺序是随机的，不能直接 Marshal）
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

// recordRuleVersion 存一版快照并推进版本号。
//
// 内容与上一版完全相同时**不产生新版本**：否则「点了保存但什么都没改」
// 会在历史里留下一串一模一样的版本，把真正的改动淹掉。返回是否真的存了。
func (h *Handler) recordRuleVersion(spec ruleTargetSpec, id uint, source, note, operator string) bool {
	row, ok := spec.Load(h, id)
	if !ok {
		return false
	}
	return h.recordRuleVersionRow(spec, row, source, note, operator)
}

// recordRuleVersionRow 同上，但用调用方已经读到的那一行。
// 删除场景必须用它：记版本的时候记录已经不在库里了。
func (h *Handler) recordRuleVersionRow(spec ruleTargetSpec, row ruleSnapshotRow,
	source, note, operator string) bool {
	content, hash := marshalSnapshot(row.Snapshot, spec.Fields)

	var last model.RuleVersion
	err := h.DB.Where("target = ? AND target_id = ?", spec.Target, row.ID).
		Order("version desc").First(&last).Error
	// 删除要留痕，即使内容没变也存 —— 那一版的意义是「删掉之前长这样」
	if err == nil && last.Hash == hash && source != "deleted" {
		return false
	}

	version := model.RuleVersion{
		Target: spec.Target, TargetID: row.ID, TargetName: row.Name,
		Version: row.VersionSeq + 1, Source: source,
		Content: content, Hash: hash,
		Note: truncate(note, 250), Operator: operator,
	}
	if err := h.DB.Create(&version).Error; err != nil {
		return false
	}
	// 版本号用「读出来 +1 再写回」而不是数据库序列，与 ConfigFile.VersionSeq 一致。
	// 并发下有竞态，单机管理场景可接受
	spec.BumpSeq(h, row.ID, version.Version)
	return true
}

// backfillRuleVersion 版本功能上线前就存在的记录，首次改动时补一版 v1。
//
// 不补的话第一次编辑之后历史里只有「改成什么样」，没有「原来什么样」——
// 而那恰恰是最想知道的那一版。
func (h *Handler) backfillRuleVersion(spec ruleTargetSpec, id uint) {
	var count int64
	h.DB.Model(&model.RuleVersion{}).
		Where("target = ? AND target_id = ?", spec.Target, id).Count(&count)
	if count == 0 {
		h.recordRuleVersion(spec, id, "created", "版本功能上线前的既有配置", "system")
	}
}

// recordVersion 按 target 存一版。给各模块的 CRUD handler 用的短形式。
//
// target 是本文件里的常量，取不到 spec 说明代码写错了 —— 这时安静地什么都不做
// 比 panic 好：版本留痕不该成为「保存规则」失败的原因。
func (h *Handler) recordVersion(target string, id uint, source, note, operator string) bool {
	spec, ok := ruleSpecOf(target)
	if !ok {
		return false
	}
	return h.recordRuleVersion(spec, id, source, note, operator)
}

// recordVersionBeforeDelete 删除前留一版「删掉之前长这样」。
// 必须在真的删掉之前调用：删完就读不到那一行了，而这一版快照是误删恢复的唯一凭据。
func (h *Handler) recordVersionBeforeDelete(target string, id uint, operator string) bool {
	spec, ok := ruleSpecOf(target)
	if !ok {
		return false
	}
	row, alive := spec.Load(h, id)
	if !alive {
		return false
	}
	// 顺带把上线前的既有记录补一版，否则删除留痕会是它唯一的一版，
	// 恢复出来的东西看不出「中间改过什么」
	h.backfillRuleVersion(spec, id)
	row, _ = spec.Load(h, id)
	return h.recordRuleVersionRow(spec, row, "deleted", "删除前的最后一版", operator)
}

// backfillVersion 短形式，给 Update handler 在改之前调用
func (h *Handler) backfillVersion(target string, id uint) {
	if spec, ok := ruleSpecOf(target); ok {
		h.backfillRuleVersion(spec, id)
	}
}

// ---------- 版本列表与正文 ----------

// ListRuleVersions 某条规则的版本历史。
//
// 列表**不带 content**：正文另外取。与配置文件版本列表同样的取舍 ——
// 列表页不需要每一版的全文，带上只是白传输。
func (h *Handler) ListRuleVersions(c *gin.Context) {
	spec, ok := resolveRuleSpec(c)
	if !ok {
		return
	}

	q := h.DB.Model(&model.RuleVersion{}).Where("target = ?", spec.Target)
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

	// 哪些版本对应的记录已经不在了 —— 那些是可以「恢复」的
	alive := make(map[uint]struct{})
	for _, id := range spec.AliveIDs(h) {
		alive[id] = struct{}{}
	}

	type versionView struct {
		model.RuleVersion
		// TargetAlive 记录是否还在。为假时这一版只能「恢复成一条新记录」，不能回滚
		TargetAlive bool `json:"targetAlive"`
	}
	views := make([]versionView, 0, len(list))
	for _, item := range list {
		_, ok := alive[item.TargetID]
		views = append(views, versionView{RuleVersion: item, TargetAlive: ok})
	}

	response.OK(c, gin.H{
		"target":   spec.Target,
		"label":    spec.Label,
		"fields":   spec.Fields,
		"versions": views,
		"limit":    ruleVersionListLimit,
		"notes": []string{
			"版本只追加、不修改，也不进数据留存清理 —— 它是回滚与误删恢复的唯一依据",
			"内容与上一版完全相同时不会产生新版本：否则「点了保存但什么都没改」会在历史里" +
				"留下一串一样的版本，把真正的改动淹掉",
			"source=deleted 的版本是「删掉之前长这样」。记录已经不在了的版本可以**恢复成一条新记录**" +
				"（新 ID），不是原地复活",
			"只含配置态：" + spec.RuntimeNote,
			"**平台不做策略审批**：审计日志不记请求体，所以真正兜住「谁悄悄改了阈值」的是" +
				"这份留痕 + 一键回滚，而不是事前点一下「同意」",
		},
	})
}

// ListRuleVersionTargets 支持版本化的规则类型清单，给前端用
func (h *Handler) ListRuleVersionTargets(c *gin.Context) {
	response.OK(c, ruleTargetSpecs)
}

// GetRuleVersion 取一版的完整快照（字段级展开，不是丢一坨 JSON 给人看）。
// 目标类型从这一版自己的 target 字段来，不用调用方再传一遍。
func (h *Handler) GetRuleVersion(c *gin.Context) {
	var version model.RuleVersion
	if err := h.DB.First(&version, idParam(c)).Error; err != nil {
		response.NotFound(c, "版本不存在")
		return
	}
	spec, ok := ruleSpecOf(version.Target)
	if !ok {
		response.Error(c, "这一版的目标类型已不受支持: "+version.Target)
		return
	}
	response.OK(c, gin.H{
		"version": version,
		"label":   spec.Label,
		"fields":  explodeSnapshot(version.Content, spec.Fields),
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
	case "enabled", "suppressNotify":
		if flag, ok := value.(bool); ok {
			if flag {
				if key == "enabled" {
					return "启用"
				}
				return "开启"
			}
			if key == "enabled" {
				return "停用"
			}
			return "关闭"
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

// DiffRuleVersions 两版之间的字段级差异。
//
// 刻意不用配置文件那套行级 unifiedDiff：两版快照都是 JSON，按行比对的话
// 加一个字段就会让后面全部错位显示成「改了」，而且说不出「阈值 80 → 200」这种话。
func (h *Handler) DiffRuleVersions(c *gin.Context) {
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
	spec, ok := ruleSpecOf(left.Target)
	if !ok {
		response.Error(c, "这一版的目标类型已不受支持: "+left.Target)
		return
	}

	items := diffSnapshots(left.Content, right.Content, spec.Fields)
	changed := 0
	for _, item := range items {
		if item.Changed {
			changed++
		}
	}

	response.OK(c, gin.H{
		"target": spec.Target, "label": spec.Label,
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
	Target    string `json:"target" binding:"required"`
	RuleID    uint   `json:"ruleId" binding:"required"`
	VersionID uint   `json:"versionId" binding:"required"`
	Note      string `json:"note"`
}

// RollbackRule 把规则回滚到某一版。
//
// 与配置文件的回滚同样的两条保障：必须显式指定版本（不猜「上一版」），
// 并且目标版本必须属于这条规则（防拿别人的版本覆盖）。
// 回滚本身也产生一个新版本（source=rollback）—— 历史只追加，不会因为回滚而丢掉中间那几版。
func (h *Handler) RollbackRule(c *gin.Context) {
	var req rollbackReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "请指定 target、ruleId 与要回滚到的 versionId")
		return
	}
	spec, ok := ruleSpecOf(req.Target)
	if !ok {
		response.BadRequest(c, fmt.Sprintf("不支持的 target %q，可选：%s", req.Target, ruleTargetNames()))
		return
	}

	row, ok := spec.Load(h, req.RuleID)
	if !ok {
		response.NotFound(c, spec.Label+"不存在")
		return
	}

	var version model.RuleVersion
	if err := h.DB.First(&version, req.VersionID).Error; err != nil {
		response.NotFound(c, "版本不存在")
		return
	}
	if version.Target != spec.Target || version.TargetID != row.ID {
		response.BadRequest(c, "这一版不属于当前"+spec.Label+"，不能用它回滚")
		return
	}

	_, currentHash := marshalSnapshot(row.Snapshot, spec.Fields)
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
	updates, err := spec.ToUpdates(snapshot)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	for key, value := range spec.ResetOnRollback {
		updates[key] = value
	}
	if err := spec.Apply(h, row.ID, updates); err != nil {
		response.Error(c, "回滚失败")
		return
	}

	note := req.Note
	if note == "" {
		note = fmt.Sprintf("回滚到第 %d 版", version.Version)
	}
	h.recordRuleVersion(spec, row.ID, "rollback", note, middleware.CurrentUser(c).Username)

	after, _ := spec.Load(h, row.ID)
	response.OK(c, gin.H{
		"changed": true, "target": spec.Target, "id": after.ID, "name": after.Name,
		"note": fmt.Sprintf("已回滚到第 %d 版；本次回滚本身也记成了一个新版本，"+
			"中间那几版仍然在历史里", version.Version),
	})
}

// RestoreRuleVersion 从某一版恢复出一条规则。
//
// 用在「误删」上：删除时会存一版 source=deleted 的快照，这里按它重建。
// **重建出来的是一条新记录（新 ID）**，不是原地复活 —— 旧 ID 上挂过的告警、
// 评估状态都已经结束了，硬塞回同一个 ID 只会造成更难解释的状态。
func (h *Handler) RestoreRuleVersion(c *gin.Context) {
	var version model.RuleVersion
	if err := h.DB.First(&version, idParam(c)).Error; err != nil {
		response.NotFound(c, "版本不存在")
		return
	}
	spec, ok := ruleSpecOf(version.Target)
	if !ok {
		response.Error(c, "这一版的目标类型已不受支持: "+version.Target)
		return
	}

	if _, alive := spec.Load(h, version.TargetID); alive {
		response.BadRequest(c, fmt.Sprintf(
			"%s「%s」还在，不需要恢复 —— 要改回旧配置请用「回滚」", spec.Label, version.TargetName))
		return
	}

	var snapshot map[string]any
	if err := json.Unmarshal([]byte(version.Content), &snapshot); err != nil {
		response.Error(c, "这一版的快照解不开，无法恢复")
		return
	}
	updates, err := spec.ToUpdates(snapshot)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	user := middleware.CurrentUser(c)
	// 恢复出来的记录默认**停用**：误删之后直接让它开始评估并发告警，
	// 等于在没人确认的情况下恢复了一条可能已经不适用的策略。
	// 所以建的时候不带 enabled，靠模型零值（false）—— 布尔字段都不带 gorm default，
	// 这一点是可靠的（见 model 包注释）
	wasEnabled, _ := updates["enabled"].(bool)
	newID, err := spec.Create(h, updates, user.ID)
	if err != nil {
		response.Error(c, "恢复失败")
		return
	}
	h.recordRuleVersion(spec, newID, "restored",
		fmt.Sprintf("从「%s」第 %d 版恢复", version.TargetName, version.Version),
		user.Username)

	after, _ := spec.Load(h, newID)
	note := fmt.Sprintf("已按「%s」第 %d 版恢复成一条**新%s**（ID %d）。",
		version.TargetName, version.Version, spec.Label, newID)
	if wasEnabled {
		note += "恢复出来的记录**默认停用** —— 那一版当时是启用的，" +
			"但直接让它生效等于在没人确认的情况下恢复了一条可能已经不适用的策略，请确认后手动启用"
	}
	response.OK(c, gin.H{
		"target": spec.Target, "id": newID, "name": after.Name,
		// rule 是恢复出来的配置（不是完整的数据库行）：三种目标的行结构不同，
		// 这里给的是版本机制认识的那部分，足够界面回显
		"rule":     after.Snapshot,
		"snapshot": explodeSnapshot(version.Content, spec.Fields), "note": note,
	})
}

// ---------- 各自的快照 → 列映射 ----------

// alertSnapshotToUpdates JSON 里的数字一律是 float64，要转回 int 才能写进 int 列
func alertSnapshotToUpdates(snapshot map[string]any) (map[string]any, error) {
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
	times := intFromJSON(snapshot["consecutiveTimes"])
	if times <= 0 {
		times = 1
	}
	enabled, _ := snapshot["enabled"].(bool)

	return map[string]any{
		"name": name, "metric": metric, "comparator": comparator,
		"threshold": threshold, "window_minutes": intFromJSON(snapshot["windowMinutes"]),
		"consecutive_times": times, "severity": asString(snapshot["severity"]),
		"enabled": enabled, "remark": asString(snapshot["remark"]),
	}, nil
}

func detectionSnapshotToUpdates(snapshot map[string]any) (map[string]any, error) {
	name := asString(snapshot["name"])
	if name == "" {
		return nil, fmt.Errorf("这一版的快照缺少名称，无法使用")
	}
	mode := asString(snapshot["mode"])
	if detectionModeLabels[mode] == "" {
		return nil, fmt.Errorf("这一版的关联方式 %q 已不受支持", mode)
	}
	steps := asString(snapshot["steps"])
	// 步骤是 JSON 数组的原文，解不开就别往回写：一条步骤坏掉的检测规则每轮评估都会报错
	var parsed []any
	if err := json.Unmarshal([]byte(steps), &parsed); err != nil || len(parsed) < 2 {
		return nil, fmt.Errorf("这一版的步骤定义不可用（至少要两步）")
	}
	window := intFromJSON(snapshot["windowMinutes"])
	if window <= 0 {
		window = 30
	}
	enabled, _ := snapshot["enabled"].(bool)

	return map[string]any{
		"name": name, "mode": mode, "steps": steps,
		"join_label": asString(snapshot["joinLabel"]), "window_minutes": window,
		"severity": asString(snapshot["severity"]),
		"enabled":  enabled, "remark": asString(snapshot["remark"]),
	}, nil
}

func aggregationSnapshotToUpdates(snapshot map[string]any) (map[string]any, error) {
	name := asString(snapshot["name"])
	dimensions := asString(snapshot["dimensions"])
	if name == "" || dimensions == "" {
		return nil, fmt.Errorf("这一版的快照缺少名称或归桶维度，无法使用")
	}
	window := intFromJSON(snapshot["windowMinutes"])
	if window <= 0 {
		window = 60
	}
	minCount := intFromJSON(snapshot["minCount"])
	if minCount < 2 {
		minCount = 2
	}
	priority := intFromJSON(snapshot["priority"])
	if priority <= 0 {
		priority = 100
	}
	enabled, _ := snapshot["enabled"].(bool)
	suppress, _ := snapshot["suppressNotify"].(bool)

	return map[string]any{
		"name": name, "dimensions": dimensions,
		"match_severity": asString(snapshot["matchSeverity"]),
		"window_minutes": window, "min_count": minCount,
		"suppress_notify": suppress, "priority": priority,
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
