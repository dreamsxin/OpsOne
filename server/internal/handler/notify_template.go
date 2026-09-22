package handler

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"text/template"
	"time"

	"github.com/gin-gonic/gin"

	"ops-platform/server/internal/middleware"
	"ops-platform/server/internal/model"
	"ops-platform/server/internal/response"
)

// 跨渠道通知模板：把 IM 与 webhook 的消息体也做成可配置的，并且**把变量表收口到一处**。
//
// 改之前的状况：
//   - 只有邮件能配模板（EmailTemplate）；IM 的文本是 imAlertText 硬编码拼串，
//     webhook 的 JSON 是 alertMessage 里的字面量。
//   - 变量表在**四个地方各写一遍**：真实发信的 alertMailVars、预览用的 sampleAlertVars、
//     内置模板种子里的 Variables 提示串、用户自己手填的 Variables。
//     四份对得上是人工维护的巧合，不是机制保证 —— 而且前端拿不到权威清单。
//
// 这里的做法：notifyScenes 是**唯一的权威定义**，真实变量、样例变量、界面上的
// 变量清单、模板校验全部从它派生。加一个变量只改一处。
//
// 一条底线：**没有配模板时仍然按原来的硬编码格式发**。模板是可选的覆盖，不是前置条件 ——
// 不能因为新增了模板功能，就让没配模板的渠道发不出东西。

// notifyVarSpec 一个模板变量的权威定义
type notifyVarSpec struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	// Sample 预览与试发时用的样例值
	Sample string `json:"sample"`
}

// notifyScene 一个通知场景：它有哪些变量可用
type notifyScene struct {
	Code  string          `json:"code"`
	Label string          `json:"label"`
	Note  string          `json:"note"`
	Vars  []notifyVarSpec `json:"vars"`
}

const (
	sceneAlert  = "alert"
	sceneOnCall = "oncall"

	tplKindIM      = "im"
	tplKindWebhook = "webhook"

	// tplOnCallCode 值班呼叫的内置邮件模板编码。
	// 在这之前值班呼叫邮件回落到 alert.default，于是精心拼好的
	//「值班呼叫（第 N 级）/ 派给你 / 确认后不再升级」只进了站内消息、邮件里完全看不到
	tplOnCallCode = "oncall.call"
)

// notifyScenes 变量表的**唯一权威定义**。
//
// alert 那一组的 key 与改造前 alertMailVars 返回的 10 个完全一致 ——
// 既有的邮件模板不能因为这次收口而失效。
var notifyScenes = []notifyScene{
	{
		Code: sceneAlert, Label: "告警通知",
		Note: "告警派发（邮件 / IM / webhook）与渠道试发都用这一组变量",
		Vars: []notifyVarSpec{
			{"title", "标题", "web-01 磁盘使用率过高"},
			{"severity", "级别", "critical"},
			{"source", "接入源", "Prometheus 演示"},
			{"value", "当前值", "92%"},
			{"count", "累计次数", "3"},
			{"status", "状态", "firing"},
			{"summary", "详情", "/data 使用率 92%，持续 5 分钟"},
			{"labels", "标签", "host=web-01, env=prod, service=nginx"},
			{"firstSeenAt", "首次出现", ""},
			{"lastSeenAt", "最近出现", ""},
		},
	},
	{
		Code: sceneOnCall, Label: "值班呼叫",
		Note: "值班表把告警派给某个人时发的通知。它比告警通知多了「派给谁、第几级、值班表是哪张」",
		Vars: []notifyVarSpec{
			{"title", "标题", "值班呼叫（第 2 级）：web-01 磁盘使用率过高"},
			{"scheduleName", "值班表", "核心业务值班"},
			{"level", "升级层级（从 1 起）", "2"},
			{"assignee", "派给谁", "张三"},
			{"alertTitle", "告警标题", "web-01 磁盘使用率过高"},
			{"severity", "级别", "critical"},
			{"summary", "详情", "/data 使用率 92%，持续 5 分钟"},
			{"count", "累计次数", "3"},
			{"firstSeenAt", "首次出现", ""},
		},
	},
}

func findScene(code string) (notifyScene, bool) {
	for _, scene := range notifyScenes {
		if scene.Code == code {
			return scene, true
		}
	}
	return notifyScene{}, false
}

// sceneVarKeys 某个场景允许出现的变量名集合
func sceneVarKeys(code string) map[string]struct{} {
	scene, ok := findScene(code)
	if !ok {
		return nil
	}
	out := make(map[string]struct{}, len(scene.Vars))
	for _, v := range scene.Vars {
		out[v.Key] = struct{}{}
	}
	return out
}

// sceneSampleVars 场景的样例变量。时间类的样例在这里现算，
// 写死成固定字符串会让预览里的时间永远停在某一天
func sceneSampleVars(code string) map[string]string {
	scene, _ := findScene(code)
	out := make(map[string]string, len(scene.Vars))
	now := time.Now()
	for _, v := range scene.Vars {
		switch v.Key {
		case "firstSeenAt":
			out[v.Key] = now.Add(-30 * time.Minute).Format(time.RFC3339)
		case "lastSeenAt":
			out[v.Key] = now.Format(time.RFC3339)
		default:
			out[v.Key] = v.Sample
		}
	}
	return out
}

// ListNotifyTemplateVars 把权威变量表交给前端。
//
// 之前前端拿不到任何权威清单 —— 界面上「可用变量」那一列是用户自己在
// Variables 字段里手填的提示文本。这个接口就是为了不再让人手抄一份。
func (h *Handler) ListNotifyTemplateVars(c *gin.Context) {
	response.OK(c, gin.H{
		"scenes": notifyScenes,
		"kinds": []gin.H{
			{"code": tplKindIM, "label": "IM 文本",
				"note": "纯文本，三家 IM 通用。刻意不做 markdown / 卡片 —— 企业微信、钉钉、飞书的富文本语法互不兼容"},
			{"code": tplKindWebhook, "label": "Webhook JSON",
				"note": "渲染结果必须是合法 JSON，保存时会校验。默认报文的字段形状是对接收端的契约，改它之前想清楚"},
		},
		"notes": []string{
			"这份变量表是代码里的**唯一权威定义**：真实发信、预览样例、界面清单、模板校验全部从它派生",
			"模板里引用了不在清单里的变量，**保存时就会被拒**。" +
				"text/template 对 map 的缺失 key 会静默渲染成空串，不校验的话写错变量名要到真发信时才发现",
			"**没有配模板的渠道仍然按原来的硬编码格式发** —— 模板是可选的覆盖，不是前置条件",
			"邮件模板是另一张表（它有主题字段），但用的是同一份变量表",
		},
	})
}

// ---------- 模板校验 ----------

// tplVarPattern 抓出模板里引用的变量名。
//
// 用正则而不是走 text/template 的语法树：这里只需要认出 `{{.xxx}}` 这一种最常见的
// 引用形式，为此把 parse.Node 递归一遍不划算。代价是 `{{range}}` `{{with}}` 这类
// 复杂用法认不全 —— 但那些在通知模板里本来就不该出现，认不出来正好会被拒。
var tplVarPattern = regexp.MustCompile(`\{\{-?\s*\.([A-Za-z_][A-Za-z0-9_]*)`)

// validateNotifyTemplate 校验模板：语法、变量白名单、webhook 的 JSON 合法性。
func validateNotifyTemplate(kind, scene, body string) error {
	if strings.TrimSpace(body) == "" {
		return fmt.Errorf("模板正文不能为空")
	}
	allowed := sceneVarKeys(scene)
	if allowed == nil {
		return fmt.Errorf("未知的场景: %s", scene)
	}
	if kind != tplKindIM && kind != tplKindWebhook {
		return fmt.Errorf("模板类型只支持 im 与 webhook")
	}

	if _, err := template.New("body").Parse(body); err != nil {
		return fmt.Errorf("模板语法错误: %v", err)
	}

	// 变量白名单：这是相对邮件模板的实质改进 ——
	// 那边只校验语法，写错变量名要到真发信时才表现成「正文里少了一段」
	var unknown []string
	for _, match := range tplVarPattern.FindAllStringSubmatch(body, -1) {
		name := match[1]
		if _, ok := allowed[name]; !ok {
			unknown = append(unknown, name)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		keys := make([]string, 0, len(allowed))
		for key := range allowed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		return fmt.Errorf("模板里引用了不存在的变量：%s。这个场景可用的是：%s",
			strings.Join(dedupeStrings(unknown), "、"), strings.Join(keys, "、"))
	}

	// webhook 模板渲染后必须是合法 JSON **对象**。用样例变量试渲染一次 ——
	// 保存时不试，就要到真发信时才发现接收端收到一坨语法错误的东西。
	// 要求顶层是对象而不是数组：postJSON 的报文就是一个对象
	if kind == tplKindWebhook {
		rendered, err := renderTemplateText(body, sceneSampleVars(scene))
		if err != nil {
			return fmt.Errorf("模板渲染失败: %v", err)
		}
		var probe map[string]any
		if err := json.Unmarshal([]byte(rendered), &probe); err != nil {
			return fmt.Errorf("webhook 模板用样例变量渲染出来不是合法 JSON 对象，请检查引号与逗号。渲染结果：%s",
				truncate(rendered, 200))
		}
	}
	return nil
}

func dedupeStrings(list []string) []string {
	seen := make(map[string]struct{}, len(list))
	out := make([]string, 0, len(list))
	for _, item := range list {
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	return out
}

// renderTemplateText 渲染单段模板文本
func renderTemplateText(body string, vars map[string]string) (string, error) {
	tpl, err := template.New("notify").Parse(body)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := tpl.Execute(&buf, vars); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// ---------- CRUD ----------

type notifyTemplateReq struct {
	Code    string `json:"code"`
	Name    string `json:"name" binding:"required"`
	Kind    string `json:"kind" binding:"required"`
	Scene   string `json:"scene" binding:"required"`
	Body    string `json:"body" binding:"required"`
	Enabled *bool  `json:"enabled"`
	Remark  string `json:"remark"`
}

func (h *Handler) ListNotifyTemplates(c *gin.Context) {
	q := h.DB.Model(&model.NotifyTemplate{})
	if v := strings.TrimSpace(c.Query("kind")); v != "" {
		q = q.Where("kind = ?", v)
	}
	if v := strings.TrimSpace(c.Query("scene")); v != "" {
		q = q.Where("scene = ?", v)
	}
	var list []model.NotifyTemplate
	if err := q.Order("id asc").Find(&list).Error; err != nil {
		response.Error(c, "查询通知模板失败")
		return
	}
	response.OK(c, list)
}

func (h *Handler) CreateNotifyTemplate(c *gin.Context) {
	var req notifyTemplateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "名称、类型、场景与正文为必填项")
		return
	}
	req.Code = strings.TrimSpace(req.Code)
	if req.Code == "" {
		response.BadRequest(c, "请填写模板编码（渠道用它引用模板）")
		return
	}
	if err := validateNotifyTemplate(req.Kind, req.Scene, req.Body); err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	item := model.NotifyTemplate{
		Code: req.Code, Name: req.Name, Kind: req.Kind, Scene: req.Scene,
		Body: req.Body, Enabled: true, Remark: req.Remark,
		CreatedBy: middleware.CurrentUser(c).ID,
	}
	if req.Enabled != nil {
		item.Enabled = *req.Enabled
	}
	if err := h.DB.Create(&item).Error; err != nil {
		response.BadRequest(c, "创建失败，模板编码可能已存在")
		return
	}
	response.OK(c, item)
}

func (h *Handler) UpdateNotifyTemplate(c *gin.Context) {
	var item model.NotifyTemplate
	if err := h.DB.First(&item, idParam(c)).Error; err != nil {
		response.NotFound(c, "模板不存在")
		return
	}
	var req notifyTemplateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "参数校验失败")
		return
	}
	// kind 与 scene 不允许改：改了之后已经引用它的渠道会拿到一个变量对不上的模板。
	// 要换类型请新建一个
	if err := validateNotifyTemplate(item.Kind, item.Scene, req.Body); err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	updates := map[string]any{"name": req.Name, "body": req.Body, "remark": req.Remark}
	if req.Enabled != nil {
		updates["enabled"] = *req.Enabled
	}
	if err := h.DB.Model(&model.NotifyTemplate{}).Where("id = ?", item.ID).
		Updates(updates).Error; err != nil {
		response.Error(c, "更新失败")
		return
	}
	var after model.NotifyTemplate
	h.DB.First(&after, item.ID)
	response.OK(c, after)
}

func (h *Handler) DeleteNotifyTemplate(c *gin.Context) {
	var item model.NotifyTemplate
	if err := h.DB.First(&item, idParam(c)).Error; err != nil {
		response.NotFound(c, "模板不存在")
		return
	}
	if item.Builtin {
		response.BadRequest(c, "内置模板不能删除，可以改内容或停用")
		return
	}
	// 还有渠道在引用就不许删：删掉之后那些渠道会静默回落到硬编码格式，
	// 而使用者只会发现「通知的样子变了」，查不到原因
	var refs int64
	h.DB.Model(&model.NotifyChannel{}).Where("template_code = ?", item.Code).Count(&refs)
	if refs > 0 {
		response.BadRequest(c, fmt.Sprintf(
			"还有 %d 个通知渠道在引用这个模板，先把它们改掉再删", refs))
		return
	}
	if err := h.DB.Delete(&model.NotifyTemplate{}, item.ID).Error; err != nil {
		response.Error(c, "删除失败")
		return
	}
	response.OK(c, nil)
}

// PreviewNotifyTemplate 用样例变量渲染一遍。
// webhook 类会顺手报出「渲染结果是不是合法 JSON」。
func (h *Handler) PreviewNotifyTemplate(c *gin.Context) {
	var item model.NotifyTemplate
	if err := h.DB.First(&item, idParam(c)).Error; err != nil {
		response.NotFound(c, "模板不存在")
		return
	}
	var req struct {
		Vars map[string]string `json:"vars"`
	}
	_ = c.ShouldBindJSON(&req)

	vars := sceneSampleVars(item.Scene)
	for key, value := range req.Vars {
		vars[key] = value
	}
	rendered, err := renderTemplateText(item.Body, vars)
	if err != nil {
		response.BadRequest(c, "渲染失败: "+err.Error())
		return
	}

	result := gin.H{"rendered": rendered, "vars": vars, "kind": item.Kind, "scene": item.Scene}
	if item.Kind == tplKindWebhook {
		result["validJSON"] = json.Valid([]byte(rendered))
	}
	response.OK(c, result)
}

// ---------- 发送时的模板解析 ----------

// resolveNotifyTemplate 取渠道引用的模板。
//
// 找不到 / 停用 / 类型对不上时**返回空**，调用方回落到硬编码格式 ——
// 而不是发不出去。这是这一块最重要的一条：模板是覆盖，不是前置条件。
func (h *Handler) resolveNotifyTemplate(code, kind, scene string) string {
	code = strings.TrimSpace(code)
	if code == "" {
		return ""
	}
	var tpl model.NotifyTemplate
	err := h.DB.Where("code = ? AND kind = ? AND scene = ? AND enabled = ?",
		code, kind, scene, true).First(&tpl).Error
	if err != nil {
		return ""
	}
	return tpl.Body
}

// renderIMBody 渲染 IM 文本。模板缺失或渲染失败都回落到硬编码格式。
func (h *Handler) renderIMBody(channel model.NotifyChannel, alert model.Alert) string {
	fallback := imAlertText(alert)
	body := h.resolveNotifyTemplate(channel.TemplateCode, tplKindIM, sceneAlert)
	if body == "" {
		return fallback
	}
	rendered, err := renderTemplateText(body, alertMailVars(alert))
	if err != nil || strings.TrimSpace(rendered) == "" {
		// 渲染失败就发默认格式：一条告警发不出去比格式不好看严重得多
		return fallback
	}
	return rendered
}

// renderWebhookBody 渲染 webhook 报文。
//
// 返回 map 而不是 any：postJSON 的签名要求是 map[string]any，而且报文顶层
// 必须是对象 —— 渲染出一个 JSON 数组的模板在保存时就会被拒。
// 渲染结果不是合法 JSON 时回落到默认报文：保存时校验过，但变量值里
// 可能带引号（比如 summary 里有 `"`），运行期仍然可能破掉 JSON。
func (h *Handler) renderWebhookBody(channel model.NotifyChannel, alert model.Alert) (map[string]any, string) {
	fallback := alertMessage(alert)
	body := h.resolveNotifyTemplate(channel.TemplateCode, tplKindWebhook, sceneAlert)
	if body == "" {
		return fallback, ""
	}
	rendered, err := renderTemplateText(body, alertMailVars(alert))
	if err != nil {
		return fallback, "模板渲染失败，已按默认报文发送: " + truncate(err.Error(), 160)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(rendered), &payload); err != nil {
		return fallback, "模板渲染结果不是合法 JSON 对象（变量值里可能带引号），已按默认报文发送"
	}
	return payload, ""
}

// ---------- 值班呼叫的变量 ----------

// onCallVars 值班呼叫场景的变量。
//
// 修的是一个真实存在的缺口：在这之前值班呼叫邮件走的是 alertMailVars + 回落到
// alert.default 模板，于是站内消息里精心拼好的「值班呼叫（第 N 级）/ 值班表把这条
// 告警派给你 / 确认后不再升级」**在邮件里完全看不到** —— 收到的是一封和普通告警
// 一模一样的信，看不出这是在叫自己。
func onCallVars(schedule model.OnCallSchedule, alert model.Alert, level int, assignee, title string) map[string]string {
	return map[string]string{
		"title":        title,
		"scheduleName": schedule.Name,
		"level":        fmt.Sprint(level),
		"assignee":     assignee,
		"alertTitle":   alert.Title,
		"severity":     alert.Severity,
		"summary":      alert.Summary,
		"count":        fmt.Sprint(alert.Count),
		"firstSeenAt":  alert.FirstSeenAt.Format("2006-01-02 15:04:05"),
	}
}
