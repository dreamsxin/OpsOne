package handler

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"ops-platform/server/internal/middleware"
	"ops-platform/server/internal/model"
	"ops-platform/server/internal/response"
)

// 已接入运行时的内置配置键
const (
	CfgPlatformName  = "platform.name"
	CfgLoginNotice   = "platform.login_notice"
	CfgMaxUploadMB   = "file.max_upload_mb"
	CfgExecConcurr   = "exec.concurrency"
	CfgRecordKeepDay = "session.record_keep_days"
)

// ListConfigs 配置项列表，可按分组过滤
func (h *Handler) ListConfigs(c *gin.Context) {
	q := h.DB.Model(&model.SysConfig{})
	if group := c.Query("group"); group != "" {
		q = q.Where("`group` = ?", group)
	}

	var list []model.SysConfig
	if err := q.Order("`group` asc, id asc").Find(&list).Error; err != nil {
		response.Error(c, "查询配置失败")
		return
	}
	response.OK(c, list)
}

type configItemReq struct {
	Group  string `json:"group"`
	Key    string `json:"key" binding:"required"`
	Value  string `json:"value"`
	Type   string `json:"type"`
	Label  string `json:"label"`
	Remark string `json:"remark"`
}

// CreateConfig 新增自定义配置项
func (h *Handler) CreateConfig(c *gin.Context) {
	var req configItemReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "配置键不能为空")
		return
	}
	if err := validateConfigValue(normalizeConfigType(req.Type), req.Value); err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	item := model.SysConfig{
		Group: orDefault(req.Group, "general"), Key: strings.TrimSpace(req.Key),
		Value: req.Value, Type: normalizeConfigType(req.Type),
		Label: req.Label, Remark: req.Remark, Builtin: false,
		UpdatedBy: middleware.CurrentUser(c).Username,
	}
	if err := h.DB.Create(&item).Error; err != nil {
		response.BadRequest(c, "创建失败，配置键可能已存在")
		return
	}
	response.OK(c, item)
}

type configUpdateReq struct {
	Items []struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	} `json:"items" binding:"required,min=1"`
}

// UpdateConfigs 批量更新配置值。内置键只能改值，不能改类型与说明。
func (h *Handler) UpdateConfigs(c *gin.Context) {
	var req configUpdateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "请提供要更新的配置项")
		return
	}

	operator := middleware.CurrentUser(c).Username
	updated := 0
	for _, item := range req.Items {
		var cfg model.SysConfig
		if err := h.DB.Where("`key` = ?", item.Key).First(&cfg).Error; err != nil {
			response.BadRequest(c, "配置项不存在: "+item.Key)
			return
		}
		if err := validateConfigValue(cfg.Type, item.Value); err != nil {
			response.BadRequest(c, fmt.Sprintf("%s: %s", cfg.Key, err.Error()))
			return
		}
		err := h.DB.Model(&cfg).Updates(map[string]any{
			"value": item.Value, "updated_by": operator,
		}).Error
		if err != nil {
			response.Error(c, "配置更新失败: "+cfg.Key)
			return
		}
		updated++
	}
	response.OK(c, gin.H{"updated": updated})
}

// DeleteConfig 删除自定义配置项，内置键不允许删除
func (h *Handler) DeleteConfig(c *gin.Context) {
	var cfg model.SysConfig
	if err := h.DB.First(&cfg, idParam(c)).Error; err != nil {
		response.NotFound(c, "配置项不存在")
		return
	}
	if cfg.Builtin {
		response.BadRequest(c, "内置配置项不可删除，只能修改取值")
		return
	}
	if err := h.DB.Delete(&cfg).Error; err != nil {
		response.Error(c, "配置删除失败")
		return
	}
	response.OK(c, nil)
}

// Branding 登录页与顶栏用到的品牌信息，无需登录即可读取
func (h *Handler) Branding(c *gin.Context) {
	response.OK(c, gin.H{
		"platformName": h.configString(CfgPlatformName, "OpsOne 一体化运维平台"),
		"loginNotice":  h.configString(CfgLoginNotice, ""),
	})
}

// ---------- 配置读取 ----------

func (h *Handler) configString(key, def string) string {
	var cfg model.SysConfig
	if err := h.DB.Where("`key` = ?", key).First(&cfg).Error; err != nil {
		return def
	}
	if strings.TrimSpace(cfg.Value) == "" {
		return def
	}
	return cfg.Value
}

func (h *Handler) configInt(key string, def int) int {
	raw := h.configString(key, "")
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || n <= 0 {
		return def
	}
	return n
}

func normalizeConfigType(t string) string {
	switch t {
	case "int", "bool", "text":
		return t
	default:
		return "string"
	}
}

func validateConfigValue(valueType, value string) error {
	switch valueType {
	case "int":
		if _, err := strconv.Atoi(strings.TrimSpace(value)); err != nil {
			return fmt.Errorf("取值必须是整数")
		}
	case "bool":
		if value != "true" && value != "false" {
			return fmt.Errorf("取值必须是 true 或 false")
		}
	}
	return nil
}

func orDefault(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}
