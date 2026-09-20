package handler

import (
	"fmt"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"ops-platform/server/internal/middleware"
	"ops-platform/server/internal/model"
	"ops-platform/server/internal/response"
)

type grantReq struct {
	SubjectType  string `json:"subjectType" binding:"required"`
	SubjectID    uint   `json:"subjectId" binding:"required"`
	ResourceType string `json:"resourceType"`
	ResourceID   uint   `json:"resourceId" binding:"required"`
	Actions      string `json:"actions"`
	ExpiresAt    string `json:"expiresAt"` // YYYY-MM-DD，空表示长期有效
	Remark       string `json:"remark"`
}

// ListResourceGrants 授权列表，可按主体与资源类型过滤
func (h *Handler) ListResourceGrants(c *gin.Context) {
	page, size := pageParams(c)
	q := h.DB.Model(&model.ResourceGrant{})

	if v := c.Query("subjectType"); v != "" {
		q = q.Where("subject_type = ?", v)
	}
	if v := c.Query("subjectId"); v != "" {
		q = q.Where("subject_id = ?", v)
	}
	if v := c.Query("resourceType"); v != "" {
		q = q.Where("resource_type = ?", v)
	}
	if c.Query("activeOnly") == "true" {
		q = q.Where("expires_at IS NULL OR expires_at > ?", time.Now())
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		response.Error(c, "查询资源授权失败")
		return
	}
	var list []model.ResourceGrant
	if err := q.Order("id desc").Offset((page - 1) * size).Limit(size).Find(&list).Error; err != nil {
		response.Error(c, "查询资源授权失败")
		return
	}
	response.OKPage(c, list, total, page, size)
}

// CreateResourceGrant 新增授权。主体与资源都会校验存在性，并回填名称便于列表展示。
func (h *Handler) CreateResourceGrant(c *gin.Context) {
	var req grantReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "主体与资源为必填项")
		return
	}

	subjectType := normalizeSubjectType(req.SubjectType)
	subjectName, err := h.resolveSubjectName(subjectType, req.SubjectID)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	resourceType := normalizeResourceType(req.ResourceType)
	resourceName, err := h.resolveResourceName(resourceType, req.ResourceID)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	actions, err := normalizeActions(req.Actions)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	expires, err := parseDate(req.ExpiresAt)
	if err != nil {
		response.BadRequest(c, "有效期格式应为 YYYY-MM-DD")
		return
	}

	grant := model.ResourceGrant{
		SubjectType: subjectType, SubjectID: req.SubjectID, SubjectName: subjectName,
		ResourceType: resourceType, ResourceID: req.ResourceID, ResourceName: resourceName,
		Actions: actions, ExpiresAt: expires, Remark: req.Remark,
		Operator: middleware.CurrentUser(c).Username,
	}
	if err := h.DB.Create(&grant).Error; err != nil {
		response.Error(c, "授权创建失败")
		return
	}
	response.OK(c, grant)
}

// UpdateResourceGrant 调整动作集合、有效期与备注，主体和资源不允许改（改了就是另一条授权）
func (h *Handler) UpdateResourceGrant(c *gin.Context) {
	var grant model.ResourceGrant
	if err := h.DB.First(&grant, idParam(c)).Error; err != nil {
		response.NotFound(c, "授权不存在")
		return
	}
	var req grantReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "参数校验失败")
		return
	}

	actions, err := normalizeActions(req.Actions)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	expires, err := parseDate(req.ExpiresAt)
	if err != nil {
		response.BadRequest(c, "有效期格式应为 YYYY-MM-DD")
		return
	}

	grant.Actions, grant.ExpiresAt, grant.Remark = actions, expires, req.Remark
	grant.Operator = middleware.CurrentUser(c).Username
	if err := h.DB.Save(&grant).Error; err != nil {
		response.Error(c, "授权更新失败")
		return
	}
	response.OK(c, grant)
}

func (h *Handler) DeleteResourceGrant(c *gin.Context) {
	if err := h.DB.Delete(&model.ResourceGrant{}, idParam(c)).Error; err != nil {
		response.Error(c, "授权删除失败")
		return
	}
	response.OK(c, nil)
}

// DiagnoseResourceGrants 诊断某个用户对主机的实际权限：
// 数据范围内的主机 + 仅靠授权拿到的主机（含动作集合）
func (h *Handler) DiagnoseResourceGrants(c *gin.Context) {
	userID := idParam(c)
	if userID == 0 {
		userID = middleware.CurrentUser(c).ID
	}

	var user model.User
	if err := h.DB.Preload("Roles").First(&user, userID).Error; err != nil {
		response.NotFound(c, "用户不存在")
		return
	}

	var scopedCount int64
	h.applyScope(h.DB.Model(&model.Host{}), &user).Count(&scopedCount)

	grants := h.activeGrants(&user, "host")
	extra := make([]gin.H, 0, len(grants))
	for hostID, actions := range grants {
		var host model.Host
		if err := h.DB.First(&host, hostID).Error; err != nil {
			continue
		}
		// 只列出「靠授权才拿到」的主机，范围内的不重复展示
		if h.resourceVisible(&user, host.DeptID, host.CreatedBy) {
			continue
		}
		names := make([]string, 0, len(actions))
		for action := range actions {
			names = append(names, action)
		}
		extra = append(extra, gin.H{
			"hostId": host.ID, "hostName": host.Name, "address": host.Address,
			"actions": names,
		})
	}

	response.OK(c, gin.H{
		"user":         gin.H{"id": user.ID, "username": user.Username},
		"scopedHosts":  scopedCount,
		"grantedHosts": extra,
	})
}

func (h *Handler) resolveSubjectName(subjectType string, id uint) (string, error) {
	if subjectType == "role" {
		var role model.Role
		if err := h.DB.First(&role, id).Error; err != nil {
			return "", fmt.Errorf("角色不存在")
		}
		return role.Name, nil
	}
	var user model.User
	if err := h.DB.First(&user, id).Error; err != nil {
		return "", fmt.Errorf("用户不存在")
	}
	if user.Nickname != "" {
		return fmt.Sprintf("%s(%s)", user.Username, user.Nickname), nil
	}
	return user.Username, nil
}

func (h *Handler) resolveResourceName(resourceType string, id uint) (string, error) {
	if resourceType == "database" {
		var db model.DBInstance
		if err := h.DB.First(&db, id).Error; err != nil {
			return "", fmt.Errorf("数据库资产不存在")
		}
		return fmt.Sprintf("%s(%s:%d)", db.Name, db.Address, db.Port), nil
	}
	var host model.Host
	if err := h.DB.First(&host, id).Error; err != nil {
		return "", fmt.Errorf("主机不存在")
	}
	return fmt.Sprintf("%s(%s)", host.Name, host.Address), nil
}

func normalizeSubjectType(v string) string {
	if v == "role" {
		return "role"
	}
	return "user"
}

func normalizeResourceType(v string) string {
	if v == "database" {
		return "database"
	}
	return "host"
}

// normalizeActions 校验动作集合，空值按全部动作处理
func normalizeActions(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == model.ActionAll {
		return model.ActionAll, nil
	}

	valid := map[string]bool{
		model.ActionTerminal: true,
		model.ActionFile:     true,
		model.ActionExec:     true,
		model.ActionManage:   true,
	}
	result := make([]string, 0, 4)
	for _, item := range strings.Split(raw, ",") {
		action := strings.TrimSpace(item)
		if action == "" {
			continue
		}
		if !valid[action] {
			return "", fmt.Errorf("不支持的动作: %s（可选 terminal/file/exec/manage 或 *）", action)
		}
		result = append(result, action)
	}
	if len(result) == 0 {
		return "", fmt.Errorf("请至少选择一个动作")
	}
	return strings.Join(result, ","), nil
}
