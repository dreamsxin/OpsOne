package handler

import (
	"strings"
	"time"

	"gorm.io/gorm"

	"ops-platform/server/internal/model"
)

// grantMap 资源 ID -> 该用户在此资源上被授予的动作集合
type grantMap map[uint]map[string]bool

// activeGrants 汇总用户自身与其角色的有效授权（未过期）
func (h *Handler) activeGrants(user *model.User, resourceType string) grantMap {
	result := grantMap{}

	roleIDs := make([]uint, 0, len(user.Roles))
	for _, role := range user.Roles {
		roleIDs = append(roleIDs, role.ID)
	}

	now := time.Now()
	q := h.DB.Model(&model.ResourceGrant{}).
		Where("resource_type = ?", resourceType).
		Where("expires_at IS NULL OR expires_at > ?", now)

	if len(roleIDs) > 0 {
		q = q.Where("(subject_type = ? AND subject_id = ?) OR (subject_type = ? AND subject_id IN ?)",
			"user", user.ID, "role", roleIDs)
	} else {
		q = q.Where("subject_type = ? AND subject_id = ?", "user", user.ID)
	}

	var grants []model.ResourceGrant
	if err := q.Find(&grants).Error; err != nil {
		return result
	}

	for _, grant := range grants {
		actions := result[grant.ResourceID]
		if actions == nil {
			actions = map[string]bool{}
			result[grant.ResourceID] = actions
		}
		for _, action := range strings.Split(grant.Actions, ",") {
			action = strings.TrimSpace(action)
			if action != "" {
				actions[action] = true
			}
		}
	}
	return result
}

// grantedResourceIDs 取被授权的资源 ID 列表
func (h *Handler) grantedResourceIDs(user *model.User, resourceType string) []uint {
	grants := h.activeGrants(user, resourceType)
	ids := make([]uint, 0, len(grants))
	for id := range grants {
		ids = append(ids, id)
	}
	return ids
}

// applyScopeWithGrants 数据范围之外叠加资源授权：范围内 OR 被显式授权
func (h *Handler) applyScopeWithGrants(q *gorm.DB, user *model.User, resourceType string) *gorm.DB {
	granted := h.grantedResourceIDs(user, resourceType)
	if len(granted) == 0 {
		return h.applyScope(q, user)
	}

	scope := h.resolveScope(user)
	if scope.All {
		return q
	}

	// 用子查询表达「范围内或被授权」，避免把两个条件写成 AND
	switch {
	case len(scope.DeptIDs) > 0 && scope.IncludeSelf:
		return q.Where("dept_id IN ? OR created_by = ? OR id IN ?", scope.DeptIDs, user.ID, granted)
	case len(scope.DeptIDs) > 0:
		return q.Where("dept_id IN ? OR id IN ?", scope.DeptIDs, granted)
	case scope.IncludeSelf:
		return q.Where("created_by = ? OR id IN ?", user.ID, granted)
	default:
		return q.Where("id IN ?", granted)
	}
}

// grantAllows 判断授权是否覆盖指定动作
func grantAllows(actions map[string]bool, action string) bool {
	if actions == nil {
		return false
	}
	return actions[model.ActionAll] || actions[action]
}

// hostActionAllowed 判断用户能否对主机执行指定动作。
//
// 数据范围内的主机保持原有行为（动作不受限）；仅靠授权可见的主机则按授权动作集合判断。
func (h *Handler) hostActionAllowed(user *model.User, host *model.Host, action string) bool {
	if h.resourceVisible(user, host.DeptID, host.CreatedBy) {
		return true
	}
	return grantAllows(h.activeGrants(user, "host")[host.ID], action)
}

// hostVisibleWithGrants 可见性判断：数据范围内或存在任意有效授权
func (h *Handler) hostVisibleWithGrants(user *model.User, host *model.Host) bool {
	if h.resourceVisible(user, host.DeptID, host.CreatedBy) {
		return true
	}
	return len(h.activeGrants(user, "host")[host.ID]) > 0
}
