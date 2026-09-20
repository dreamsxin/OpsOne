package handler

import (
	"regexp"

	"github.com/gin-gonic/gin"

	"ops-platform/server/internal/model"
	"ops-platform/server/internal/response"
)

type commandRuleReq struct {
	Pattern     string `json:"pattern" binding:"required"`
	Description string `json:"description"`
	Action      string `json:"action"`
	Enabled     *bool  `json:"enabled"`
}

// ListCommandRules 命令规则列表
func (h *Handler) ListCommandRules(c *gin.Context) {
	var list []model.CommandRule
	if err := h.DB.Order("id asc").Find(&list).Error; err != nil {
		response.Error(c, "查询命令规则失败")
		return
	}
	response.OK(c, list)
}

// CreateCommandRule 新增规则
func (h *Handler) CreateCommandRule(c *gin.Context) {
	var req commandRuleReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "正则表达式不能为空")
		return
	}
	if _, err := regexp.Compile(req.Pattern); err != nil {
		response.BadRequest(c, "正则表达式非法: "+err.Error())
		return
	}

	rule := model.CommandRule{
		Pattern: req.Pattern, Description: req.Description,
		Action: normalizeAction(req.Action), Enabled: true,
	}
	if req.Enabled != nil {
		rule.Enabled = *req.Enabled
	}
	if err := h.DB.Create(&rule).Error; err != nil {
		response.Error(c, "规则创建失败")
		return
	}
	response.OK(c, rule)
}

// UpdateCommandRule 编辑规则
func (h *Handler) UpdateCommandRule(c *gin.Context) {
	var rule model.CommandRule
	if err := h.DB.First(&rule, idParam(c)).Error; err != nil {
		response.NotFound(c, "规则不存在")
		return
	}
	var req commandRuleReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "参数校验失败")
		return
	}
	if _, err := regexp.Compile(req.Pattern); err != nil {
		response.BadRequest(c, "正则表达式非法: "+err.Error())
		return
	}

	rule.Pattern, rule.Description = req.Pattern, req.Description
	rule.Action = normalizeAction(req.Action)
	if req.Enabled != nil {
		rule.Enabled = *req.Enabled
	}
	if err := h.DB.Save(&rule).Error; err != nil {
		response.Error(c, "规则更新失败")
		return
	}
	response.OK(c, rule)
}

// DeleteCommandRule 删除规则
func (h *Handler) DeleteCommandRule(c *gin.Context) {
	if err := h.DB.Delete(&model.CommandRule{}, idParam(c)).Error; err != nil {
		response.Error(c, "规则删除失败")
		return
	}
	response.OK(c, nil)
}

// TestCommandRule 用一条命令试跑全部启用规则，便于确认规则是否如预期生效
func (h *Handler) TestCommandRule(c *gin.Context) {
	var req struct {
		Command string `json:"command" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "命令不能为空")
		return
	}

	for _, rule := range h.loadRules() {
		if rule.Pattern.MatchString(req.Command) {
			response.OK(c, gin.H{
				"matched": true, "ruleId": rule.ID,
				"action": rule.Action, "description": rule.Description,
			})
			return
		}
	}
	response.OK(c, gin.H{"matched": false})
}

func normalizeAction(a string) string {
	if a == "warn" {
		return "warn"
	}
	return "block"
}
