package handler

import (
	"encoding/json"

	"gorm.io/gorm"

	"ops-platform/server/internal/model"
)

// dataScope 当前用户的可见范围。deptIDs 与 includeSelf 之间是「或」关系。
type dataScope struct {
	All         bool   `json:"all"`
	DeptIDs     []uint `json:"deptIds"`
	IncludeSelf bool   `json:"includeSelf"`
}

// resolveScope 汇总用户各角色的数据范围，取并集（范围越宽越优先）。
// 没有任何角色时只能看到自己录入的数据。
func (h *Handler) resolveScope(user *model.User) dataScope {
	if len(user.Roles) == 0 {
		return dataScope{IncludeSelf: true}
	}

	result := dataScope{DeptIDs: make([]uint, 0)}
	deptSet := map[uint]struct{}{}
	var descendants map[uint][]uint // 延迟加载部门树

	for _, role := range user.Roles {
		switch role.DataScope {
		case model.ScopeAll, "":
			return dataScope{All: true}
		case model.ScopeSelf:
			result.IncludeSelf = true
		case model.ScopeDept:
			if user.DeptID != 0 {
				deptSet[user.DeptID] = struct{}{}
			}
		case model.ScopeDeptBelow:
			if user.DeptID != 0 {
				deptSet[user.DeptID] = struct{}{}
				if descendants == nil {
					descendants = h.deptChildren()
				}
				for _, id := range collectDescendants(descendants, user.DeptID) {
					deptSet[id] = struct{}{}
				}
			}
		case model.ScopeCustom:
			for _, id := range parseIDList(role.DataDeptIDs) {
				deptSet[id] = struct{}{}
			}
		}
	}

	for id := range deptSet {
		result.DeptIDs = append(result.DeptIDs, id)
	}
	return result
}

// deptChildren 返回 parentID -> 子部门 ID 列表
func (h *Handler) deptChildren() map[uint][]uint {
	var depts []model.Department
	h.DB.Select("id", "parent_id").Find(&depts)

	children := map[uint][]uint{}
	for _, dept := range depts {
		children[dept.ParentID] = append(children[dept.ParentID], dept.ID)
	}
	return children
}

// collectDescendants 广度遍历取所有下级部门，visited 防止配置成环时死循环
func collectDescendants(children map[uint][]uint, root uint) []uint {
	result := make([]uint, 0)
	visited := map[uint]bool{root: true}
	queue := append([]uint{}, children[root]...)

	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		if visited[id] {
			continue
		}
		visited[id] = true
		result = append(result, id)
		queue = append(queue, children[id]...)
	}
	return result
}

// applyScope 给任意带 dept_id / created_by 字段的资源查询加上数据范围条件
func (h *Handler) applyScope(q *gorm.DB, user *model.User) *gorm.DB {
	s := h.resolveScope(user)
	switch {
	case s.All:
		return q
	case len(s.DeptIDs) > 0 && s.IncludeSelf:
		return q.Where("dept_id IN ? OR created_by = ?", s.DeptIDs, user.ID)
	case len(s.DeptIDs) > 0:
		return q.Where("dept_id IN ?", s.DeptIDs)
	case s.IncludeSelf:
		return q.Where("created_by = ?", user.ID)
	default:
		// 范围为空时不返回任何数据，避免「配错就等于放开」
		return q.Where("1 = 0")
	}
}

// applyHostScope 主机查询的数据范围（与其他资产共用同一套字段约定）
func (h *Handler) applyHostScope(q *gorm.DB, user *model.User) *gorm.DB {
	return h.applyScope(q, user)
}

// resourceVisible 判断用户是否有权访问指定归属的资源
func (h *Handler) resourceVisible(user *model.User, deptID, createdBy uint) bool {
	s := h.resolveScope(user)
	if s.All {
		return true
	}
	if s.IncludeSelf && createdBy == user.ID {
		return true
	}
	for _, id := range s.DeptIDs {
		if id == deptID {
			return true
		}
	}
	return false
}

// hostVisible 判断用户是否有权访问指定主机
func (h *Handler) hostVisible(user *model.User, host *model.Host) bool {
	return h.resourceVisible(user, host.DeptID, host.CreatedBy)
}

// filterVisibleHostIDs 从给定主机 ID 中筛出用户可见的部分
func (h *Handler) filterVisibleHostIDs(user *model.User, ids []uint) []uint {
	if len(ids) == 0 {
		return ids
	}
	var hosts []model.Host
	h.DB.Select("id", "dept_id", "created_by").Where("id IN ?", ids).Find(&hosts)

	allowed := make([]uint, 0, len(hosts))
	for i := range hosts {
		if h.hostVisible(user, &hosts[i]) {
			allowed = append(allowed, hosts[i].ID)
		}
	}
	return allowed
}

func marshalIDs(ids []uint) string {
	if len(ids) == 0 {
		return "[]"
	}
	raw, err := json.Marshal(ids)
	if err != nil {
		return "[]"
	}
	return string(raw)
}
