package handler

import (
	"fmt"
	"strings"

	"github.com/gin-gonic/gin"

	"ops-platform/server/internal/model"
	"ops-platform/server/internal/response"
)

// ---------- 菜单管理 ----------

type menuReq struct {
	ParentID  uint   `json:"parentId"`
	Name      string `json:"name"`
	Title     string `json:"title" binding:"required"`
	Path      string `json:"path"`
	Component string `json:"component"`
	Icon      string `json:"icon"`
	Type      string `json:"type"`
	AuthCode  string `json:"authCode"`
	Sort      int    `json:"sort"`
	Hidden    bool   `json:"hidden"`
}

// CreateMenu 新建自定义菜单。内置菜单由代码维护，这里只能建自建项。
func (h *Handler) CreateMenu(c *gin.Context) {
	var req menuReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "菜单标题不能为空")
		return
	}
	menuType := normalizeMenuType(req.Type)
	if menuType == "menu" && req.Path == "" {
		response.BadRequest(c, "菜单类型必须填写路由路径")
		return
	}
	if menuType == "button" && req.AuthCode == "" {
		response.BadRequest(c, "按钮类型必须填写权限标识")
		return
	}
	if req.ParentID != 0 {
		if err := h.DB.First(&model.Menu{}, req.ParentID).Error; err != nil {
			response.BadRequest(c, "上级菜单不存在")
			return
		}
	}

	menu := model.Menu{
		ParentID: req.ParentID, Name: req.Name, Title: req.Title, Path: req.Path,
		Component: req.Component, Icon: req.Icon, Type: menuType,
		AuthCode: req.AuthCode, Sort: req.Sort, Hidden: req.Hidden, Builtin: false,
	}
	if err := h.DB.Create(&menu).Error; err != nil {
		response.Error(c, "菜单创建失败")
		return
	}
	response.OK(c, menu)
}

// UpdateMenu 编辑菜单。
// 内置菜单只允许改展示字段（标题、图标、排序、隐藏）；自建菜单可全改。
func (h *Handler) UpdateMenu(c *gin.Context) {
	var menu model.Menu
	if err := h.DB.First(&menu, idParam(c)).Error; err != nil {
		response.NotFound(c, "菜单不存在")
		return
	}
	var req menuReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "菜单标题不能为空")
		return
	}

	updates := map[string]any{
		"title": req.Title, "icon": req.Icon, "sort": req.Sort, "hidden": req.Hidden,
	}
	if !menu.Builtin {
		if req.ParentID == menu.ID {
			response.BadRequest(c, "上级菜单不能是自己")
			return
		}
		updates["parent_id"] = req.ParentID
		updates["name"] = req.Name
		updates["path"] = req.Path
		updates["component"] = req.Component
		updates["type"] = normalizeMenuType(req.Type)
		updates["auth_code"] = req.AuthCode
	}

	if err := h.DB.Model(&menu).Updates(updates).Error; err != nil {
		response.Error(c, "菜单更新失败")
		return
	}
	response.OK(c, gin.H{"builtin": menu.Builtin, "updatedFields": len(updates)})
}

// DeleteMenu 删除自建菜单，内置菜单不允许删除
func (h *Handler) DeleteMenu(c *gin.Context) {
	var menu model.Menu
	if err := h.DB.First(&menu, idParam(c)).Error; err != nil {
		response.NotFound(c, "菜单不存在")
		return
	}
	if menu.Builtin {
		response.BadRequest(c, "内置菜单不可删除，可用「隐藏」把它从导航里去掉")
		return
	}

	var childCount int64
	h.DB.Model(&model.Menu{}).Where("parent_id = ?", menu.ID).Count(&childCount)
	if childCount > 0 {
		response.BadRequest(c, fmt.Sprintf("该菜单下还有 %d 个子项，请先处理", childCount))
		return
	}

	if err := h.DB.Delete(&menu).Error; err != nil {
		response.Error(c, "菜单删除失败")
		return
	}
	h.DB.Exec("DELETE FROM role_menus WHERE menu_id = ?", menu.ID)
	response.OK(c, nil)
}

func normalizeMenuType(t string) string {
	if t == "button" {
		return "button"
	}
	return "menu"
}

// ---------- 站点导航 ----------

type siteLinkReq struct {
	Name        string `json:"name" binding:"required"`
	URL         string `json:"url" binding:"required"`
	Category    string `json:"category"`
	Icon        string `json:"icon"`
	Description string `json:"description"`
	Sort        int    `json:"sort"`
	Enabled     *bool  `json:"enabled"`
}

func (h *Handler) ListSiteLinks(c *gin.Context) {
	q := h.DB.Model(&model.SiteLink{})
	if c.Query("enabledOnly") == "true" {
		q = q.Where("enabled = ?", true)
	}

	var list []model.SiteLink
	if err := q.Order("category asc, sort asc, id asc").Find(&list).Error; err != nil {
		response.Error(c, "查询站点导航失败")
		return
	}
	response.OK(c, list)
}

func (h *Handler) CreateSiteLink(c *gin.Context) {
	var req siteLinkReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "名称与地址为必填项")
		return
	}
	if err := validateLinkURL(req.URL); err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	item := model.SiteLink{
		Name: req.Name, URL: req.URL, Category: orDefault(req.Category, "general"),
		Icon: req.Icon, Description: req.Description, Sort: req.Sort, Enabled: true,
	}
	if req.Enabled != nil {
		item.Enabled = *req.Enabled
	}
	if err := h.DB.Create(&item).Error; err != nil {
		response.Error(c, "创建失败")
		return
	}
	response.OK(c, item)
}

func (h *Handler) UpdateSiteLink(c *gin.Context) {
	var item model.SiteLink
	if err := h.DB.First(&item, idParam(c)).Error; err != nil {
		response.NotFound(c, "导航项不存在")
		return
	}
	var req siteLinkReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "参数校验失败")
		return
	}
	if err := validateLinkURL(req.URL); err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	item.Name, item.URL = req.Name, req.URL
	item.Category, item.Icon = orDefault(req.Category, "general"), req.Icon
	item.Description, item.Sort = req.Description, req.Sort
	if req.Enabled != nil {
		item.Enabled = *req.Enabled
	}
	if err := h.DB.Save(&item).Error; err != nil {
		response.Error(c, "更新失败")
		return
	}
	response.OK(c, item)
}

func (h *Handler) DeleteSiteLink(c *gin.Context) {
	if err := h.DB.Delete(&model.SiteLink{}, idParam(c)).Error; err != nil {
		response.Error(c, "删除失败")
		return
	}
	response.OK(c, nil)
}

// validateLinkURL 只允许 http/https，避免把 javascript: 之类的协议塞进导航
func validateLinkURL(raw string) error {
	url := strings.ToLower(strings.TrimSpace(raw))
	if strings.HasPrefix(url, "http://") || strings.HasPrefix(url, "https://") {
		return nil
	}
	return fmt.Errorf("地址必须以 http:// 或 https:// 开头")
}
