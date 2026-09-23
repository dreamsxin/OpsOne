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
	// CfgLoginMaxFail 连续失败多少次锁定登录；0 表示不限制（要显式配成 0 才生效）
	CfgLoginMaxFail = "security.login_max_fail"
	// CfgLoginLockMinutes 锁定多少分钟
	CfgLoginLockMinutes = "security.login_lock_minutes"
)

// secretConfigKeys 值是密钥的配置项。
//
// sys_configs 是「一张表混着密钥与普通配置」的典型：平台名称和 SMTP 口令存在同一列，
// 所以不能整列加密，也不能整表照原样回传 —— 在这一轮之前，配置列表接口会把
// SMTP 口令原文发给前端，任何能看配置页的人都能从响应里读到它。
var secretConfigKeys = map[string]bool{
	CfgSMTPPass: true,
}

func isSecretConfig(key string) bool { return secretConfigKeys[strings.TrimSpace(key)] }

// configView 配置项的对外形态：密钥项不回传取值，只说「配过没有」
type configView struct {
	model.SysConfig
	Secret   bool `json:"secret"`
	HasValue bool `json:"hasValue"`
}

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
	views := make([]configView, 0, len(list))
	for _, item := range list {
		view := configView{SysConfig: item, Secret: isSecretConfig(item.Key)}
		if view.Secret {
			view.HasValue = strings.TrimSpace(item.Value) != ""
			view.Value = "" // 不回传密钥，前端按「留空表示不修改」处理
		}
		views = append(views, view)
	}
	response.OK(c, views)
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
	if isSecretConfig(item.Key) {
		item.Value = h.sealSecret(item.Value)
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
		value := item.Value
		if isSecretConfig(cfg.Key) {
			// 密钥项：列表接口不回传取值，前端提交空串只意味着「没改」。
			// 不这么处理的话，改一次平台名称就会把 SMTP 口令清掉。
			if strings.TrimSpace(value) == "" {
				continue
			}
			value = h.sealSecret(value)
		} else if err := validateConfigValue(cfg.Type, value); err != nil {
			response.BadRequest(c, fmt.Sprintf("%s: %s", cfg.Key, err.Error()))
			return
		}
		err := h.DB.Model(&cfg).Updates(map[string]any{
			"value": value, "updated_by": operator,
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

// configSecret 读一项密钥配置并解密。
//
// 与 configString 分开是有意的：密钥解不开必须是个错误，不能像普通配置那样
// 静默回落到默认值 —— 那会变成「用空口令去登 SMTP」，日志里只剩一句认证失败。
func (h *Handler) configSecret(key string) (string, error) {
	stored := h.configString(key, "")
	if stored == "" {
		return "", nil
	}
	return h.openSecret("SMTP 口令", stored)
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
