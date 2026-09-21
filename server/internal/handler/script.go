package handler

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"ops-platform/server/internal/middleware"
	"ops-platform/server/internal/model"
	"ops-platform/server/internal/response"
)

// scriptParam 脚本参数定义
type scriptParam struct {
	Name     string `json:"name"`
	Label    string `json:"label"`
	Default  string `json:"default"`
	Required bool   `json:"required"`
}

// precheckHit 预检命中的一条命令规则
type precheckHit struct {
	RuleID      uint   `json:"ruleId"`
	Pattern     string `json:"pattern"`
	Action      string `json:"action"` // block | warn
	Description string `json:"description"`
	Line        int    `json:"line"`
	Snippet     string `json:"snippet"`
}

// paramNamePattern 参数名只允许字母数字下划线，避免拼出奇怪的占位符
var paramNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// placeholderPattern 匹配脚本里的 ${NAME} 占位
var placeholderPattern = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

func parseScriptParams(raw string) ([]scriptParam, error) {
	params := []scriptParam{}
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "[]" {
		return params, nil
	}
	if err := json.Unmarshal([]byte(raw), &params); err != nil {
		return nil, fmt.Errorf("参数定义必须是 JSON 数组")
	}
	seen := map[string]bool{}
	for i := range params {
		params[i].Name = strings.TrimSpace(params[i].Name)
		if !paramNamePattern.MatchString(params[i].Name) {
			return nil, fmt.Errorf("参数名 %q 不合法，只能用字母、数字、下划线且不以数字开头", params[i].Name)
		}
		if seen[params[i].Name] {
			return nil, fmt.Errorf("参数名重复: %s", params[i].Name)
		}
		seen[params[i].Name] = true
	}
	return params, nil
}

// renderScript 用参数替换 ${NAME} 占位。
//
// 规则严格：脚本里出现的占位必须在参数定义里；必填参数必须有值（调用方传的或默认值）。
// 宁可报错也不把 ${FOO} 原样发到机器上执行。
func renderScript(content string, defs []scriptParam, values map[string]string) (string, error) {
	defined := map[string]scriptParam{}
	for _, def := range defs {
		defined[def.Name] = def
	}

	// 先检查脚本里的占位是否都有定义
	missing := map[string]bool{}
	for _, match := range placeholderPattern.FindAllStringSubmatch(content, -1) {
		if _, ok := defined[match[1]]; !ok {
			missing[match[1]] = true
		}
	}
	if len(missing) > 0 {
		names := make([]string, 0, len(missing))
		for name := range missing {
			names = append(names, name)
		}
		sort.Strings(names)
		return "", fmt.Errorf("脚本里的占位符没有对应的参数定义: %s", strings.Join(names, ", "))
	}

	final := map[string]string{}
	for _, def := range defined {
		value, ok := values[def.Name]
		if !ok || strings.TrimSpace(value) == "" {
			value = def.Default
		}
		if def.Required && strings.TrimSpace(value) == "" {
			return "", fmt.Errorf("参数 %s 是必填项", def.Name)
		}
		final[def.Name] = value
	}

	rendered := placeholderPattern.ReplaceAllStringFunc(content, func(token string) string {
		name := placeholderPattern.FindStringSubmatch(token)[1]
		return final[name]
	})
	return rendered, nil
}

// precheckScript 用启用中的命令规则逐行扫一遍脚本内容。
//
// 这是静态文本匹配，和 Web 终端里的实时拦截是同一批规则，但能力边界一样：
// 挡得住写在明面上的高危命令，挡不住 base64 解码后执行之类的绕过。
func (h *Handler) precheckScript(content string) (string, []precheckHit) {
	var rules []model.CommandRule
	if err := h.DB.Where("enabled = ?", true).Order("id asc").Find(&rules).Error; err != nil {
		return "unknown", nil
	}

	hits := []precheckHit{}
	lines := strings.Split(content, "\n")
	for _, rule := range rules {
		re, err := regexp.Compile(rule.Pattern)
		if err != nil {
			// 规则本身写错了不该影响脚本保存，跳过并在描述里体现
			continue
		}
		for idx, line := range lines {
			if strings.TrimSpace(line) == "" {
				continue
			}
			if re.MatchString(line) {
				hits = append(hits, precheckHit{
					RuleID: rule.ID, Pattern: rule.Pattern, Action: rule.Action,
					Description: rule.Description, Line: idx + 1,
					Snippet: truncate(strings.TrimSpace(line), 120),
				})
				break // 同一规则只报首次命中，避免刷屏
			}
		}
	}

	status := "pass"
	for _, hit := range hits {
		if hit.Action == "block" {
			return "blocked", hits
		}
		status = "warn"
	}
	return status, hits
}

func (h *Handler) applyPrecheck(script *model.Script) []precheckHit {
	status, hits := h.precheckScript(script.Content)
	raw, _ := json.Marshal(hits)
	now := time.Now()
	script.PrecheckStatus, script.PrecheckHits, script.PrecheckedAt = status, string(raw), &now
	return hits
}

// ---------- 接口 ----------

type scriptReq struct {
	Name        string `json:"name" binding:"required"`
	Category    string `json:"category"`
	Description string `json:"description"`
	Content     string `json:"content" binding:"required"`
	Params      string `json:"params"`
	Timeout     int    `json:"timeout"`
	RiskLevel   string `json:"riskLevel"`
	Enabled     *bool  `json:"enabled"`
}

func (req *scriptReq) normalize() ([]scriptParam, error) {
	req.Content = strings.ReplaceAll(req.Content, "\r\n", "\n")
	if strings.TrimSpace(req.Content) == "" {
		return nil, fmt.Errorf("脚本内容不能为空")
	}
	if len(req.Content) > 20000 {
		return nil, fmt.Errorf("脚本内容最多 20000 字符")
	}
	params, err := parseScriptParams(req.Params)
	if err != nil {
		return nil, err
	}
	// 保存时就校验占位符与定义是否配套，别等到下发才发现
	if _, err := renderScript(req.Content, params, map[string]string{}); err != nil &&
		strings.Contains(err.Error(), "没有对应的参数定义") {
		return nil, err
	}

	if req.Timeout <= 0 {
		req.Timeout = 60
	}
	if req.Timeout > 600 {
		return nil, fmt.Errorf("超时最多 600 秒")
	}
	switch req.RiskLevel {
	case "low", "medium", "high":
	case "":
		req.RiskLevel = "low"
	default:
		return nil, fmt.Errorf("风险等级只能是 low / medium / high")
	}
	req.Category = strings.TrimSpace(req.Category)
	if req.Category == "" {
		req.Category = "未分类"
	}
	raw, _ := json.Marshal(params)
	req.Params = string(raw)
	return params, nil
}

func (h *Handler) ListScripts(c *gin.Context) {
	page, size := pageParams(c)
	q := h.DB.Model(&model.Script{})
	if category := c.Query("category"); category != "" {
		q = q.Where("category = ?", category)
	}
	if status := c.Query("precheckStatus"); status != "" {
		q = q.Where("precheck_status = ?", status)
	}
	if kw := strings.TrimSpace(c.Query("keyword")); kw != "" {
		like := "%" + kw + "%"
		q = q.Where("name LIKE ? OR description LIKE ? OR content LIKE ?", like, like, like)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		response.Error(c, "查询脚本失败")
		return
	}
	var list []model.Script
	if err := q.Order("id desc").Offset((page - 1) * size).Limit(size).Find(&list).Error; err != nil {
		response.Error(c, "查询脚本失败")
		return
	}
	response.OKPage(c, list, total, page, size)
}

// ListScriptCategories 现有分类，供下拉使用
func (h *Handler) ListScriptCategories(c *gin.Context) {
	var categories []string
	h.DB.Model(&model.Script{}).Where("category <> ''").
		Distinct().Order("category asc").Pluck("category", &categories)
	response.OK(c, categories)
}

func (h *Handler) CreateScript(c *gin.Context) {
	var req scriptReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "名称与脚本内容为必填项")
		return
	}
	if _, err := req.normalize(); err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	user := middleware.CurrentUser(c)
	item := model.Script{
		Name: req.Name, Category: req.Category, Description: req.Description,
		Content: req.Content, Params: req.Params, Timeout: req.Timeout,
		RiskLevel: req.RiskLevel, Enabled: true,
		CreatorName: user.Username, CreatedBy: user.ID,
	}
	if req.Enabled != nil {
		item.Enabled = *req.Enabled
	}
	hits := h.applyPrecheck(&item)

	wantEnabled := item.Enabled
	if err := h.DB.Create(&item).Error; err != nil {
		response.Error(c, "创建失败")
		return
	}
	// 带 default 的布尔列在 Create 后会被回填成库默认值，显式写回
	item.Enabled = wantEnabled
	h.DB.Model(&model.Script{}).Where("id = ?", item.ID).Update("enabled", wantEnabled)

	response.OK(c, gin.H{"script": item, "precheckHits": hits})
}

func (h *Handler) UpdateScript(c *gin.Context) {
	var item model.Script
	if err := h.DB.First(&item, idParam(c)).Error; err != nil {
		response.NotFound(c, "脚本不存在")
		return
	}

	var req scriptReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "参数校验失败")
		return
	}
	if _, err := req.normalize(); err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	item.Name, item.Category, item.Description = req.Name, req.Category, req.Description
	item.Content, item.Params, item.Timeout = req.Content, req.Params, req.Timeout
	item.RiskLevel = req.RiskLevel
	if req.Enabled != nil {
		item.Enabled = *req.Enabled
	}
	hits := h.applyPrecheck(&item)

	if err := h.DB.Save(&item).Error; err != nil {
		response.Error(c, "更新失败")
		return
	}
	h.DB.Model(&model.Script{}).Where("id = ?", item.ID).Update("enabled", item.Enabled)
	response.OK(c, gin.H{"script": item, "precheckHits": hits})
}

func (h *Handler) DeleteScript(c *gin.Context) {
	if err := h.DB.Delete(&model.Script{}, idParam(c)).Error; err != nil {
		response.Error(c, "删除失败")
		return
	}
	response.OK(c, nil)
}

// PrecheckScript 对已保存的脚本重新跑一次预检（命令规则改过之后有用）
func (h *Handler) PrecheckScript(c *gin.Context) {
	var item model.Script
	if err := h.DB.First(&item, idParam(c)).Error; err != nil {
		response.NotFound(c, "脚本不存在")
		return
	}
	hits := h.applyPrecheck(&item)
	h.DB.Model(&model.Script{}).Where("id = ?", item.ID).Updates(map[string]any{
		"precheck_status": item.PrecheckStatus,
		"precheck_hits":   item.PrecheckHits,
		"prechecked_at":   item.PrecheckedAt,
	})
	response.OK(c, gin.H{
		"precheckStatus": item.PrecheckStatus, "precheckHits": hits,
		"detail": precheckDetail(item.PrecheckStatus, hits),
	})
}

func precheckDetail(status string, hits []precheckHit) string {
	switch status {
	case "blocked":
		return fmt.Sprintf("命中 %d 条命令规则，其中存在拦截级规则，该脚本不允许下发", len(hits))
	case "warn":
		return fmt.Sprintf("命中 %d 条提醒级规则，可以下发但请确认", len(hits))
	case "pass":
		return "未命中任何命令规则"
	default:
		return "尚未预检"
	}
}

type scriptRenderReq struct {
	Params map[string]string `json:"params"`
}

// RenderScript 渲染出最终要执行的命令，给人过目，不执行
func (h *Handler) RenderScript(c *gin.Context) {
	var item model.Script
	if err := h.DB.First(&item, idParam(c)).Error; err != nil {
		response.NotFound(c, "脚本不存在")
		return
	}

	var req scriptRenderReq
	_ = c.ShouldBindJSON(&req)

	defs, err := parseScriptParams(item.Params)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	rendered, err := renderScript(item.Content, defs, req.Params)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.OK(c, gin.H{"command": rendered, "timeout": item.Timeout})
}

type scriptRunReq struct {
	HostIDs []uint            `json:"hostIds" binding:"required,min=1"`
	Params  map[string]string `json:"params"`
	Timeout int               `json:"timeout"`
	// ConfirmProd 目标含生产主机时必须为 true，否则闸门拒绝下发
	ConfirmProd bool `json:"confirmProd"`
}

// RunScript 把脚本渲染后通过批量执行下发。
//
// 预检为 blocked 的脚本一律拒绝；数据权限外的主机会被剔除（与批量执行一致）。
func (h *Handler) RunScript(c *gin.Context) {
	var item model.Script
	if err := h.DB.First(&item, idParam(c)).Error; err != nil {
		response.NotFound(c, "脚本不存在")
		return
	}
	if !item.Enabled {
		response.BadRequest(c, "该脚本已停用")
		return
	}

	// 下发前用当前规则重新预检一次，避免规则更新后旧结论放行了高危脚本
	hits := h.applyPrecheck(&item)
	h.DB.Model(&model.Script{}).Where("id = ?", item.ID).Updates(map[string]any{
		"precheck_status": item.PrecheckStatus,
		"precheck_hits":   item.PrecheckHits,
		"prechecked_at":   item.PrecheckedAt,
	})
	if item.PrecheckStatus == "blocked" {
		response.BadRequest(c, precheckDetail(item.PrecheckStatus, hits))
		return
	}

	var req scriptRunReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "请选择目标主机")
		return
	}

	defs, err := parseScriptParams(item.Params)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	command, err := renderScript(item.Content, defs, req.Params)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	user := middleware.CurrentUser(c)
	allowed := h.filterVisibleHostIDs(user, req.HostIDs)
	if len(allowed) == 0 {
		response.Forbidden(c, "目标主机不在你的数据权限范围内")
		return
	}

	timeout := req.Timeout
	if timeout <= 0 {
		timeout = item.Timeout
	}
	job, err := h.RunOnHosts(c.Request.Context(), ExecRequest{
		Name: "脚本：" + item.Name, Command: command, Timeout: timeout,
		HostIDs: allowed, UserID: user.ID, Operator: user.Username, Source: "script",
		ConfirmProd: req.ConfirmProd, ClientIP: c.ClientIP(),
	})
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	now := time.Now()
	h.DB.Model(&model.Script{}).Where("id = ?", item.ID).Updates(map[string]any{
		"use_count": item.UseCount + 1, "last_used_at": &now, "last_used_by": user.Username,
	})

	response.OK(c, gin.H{
		"job": job, "command": command,
		"precheckStatus": item.PrecheckStatus, "precheckHits": hits,
	})
}
