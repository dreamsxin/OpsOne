package handler

import (
	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"

	"ops-platform/server/internal/middleware"
	"ops-platform/server/internal/model"
	"ops-platform/server/internal/response"
)

// ---------- 用户 ----------

type userReq struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password"`
	Nickname string `json:"nickname"`
	Email    string `json:"email"`
	DeptID   uint   `json:"deptId"`
	Status   *int   `json:"status"`
	RoleIDs  []uint `json:"roleIds"`
}

func (h *Handler) ListUsers(c *gin.Context) {
	page, size := pageParams(c)
	q := h.DB.Model(&model.User{})
	if kw := c.Query("keyword"); kw != "" {
		like := "%" + kw + "%"
		q = q.Where("username LIKE ? OR nickname LIKE ?", like, like)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		response.Error(c, "查询用户失败")
		return
	}
	var list []model.User
	if err := q.Preload("Roles").Order("id asc").Offset((page - 1) * size).Limit(size).Find(&list).Error; err != nil {
		response.Error(c, "查询用户失败")
		return
	}
	response.OKPage(c, list, total, page, size)
}

func (h *Handler) CreateUser(c *gin.Context) {
	var req userReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "用户名不能为空")
		return
	}
	if len(req.Password) < 8 {
		response.BadRequest(c, "初始密码至少 8 位")
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		response.Error(c, "密码加密失败")
		return
	}
	user := model.User{
		Username: req.Username, PasswordHash: string(hash),
		Nickname: req.Nickname, Email: req.Email, DeptID: req.DeptID, Status: 1,
	}
	if req.Status != nil {
		user.Status = *req.Status
	}
	if err := h.DB.Create(&user).Error; err != nil {
		response.BadRequest(c, "创建失败，用户名可能已存在")
		return
	}
	if err := h.bindRoles(&user, req.RoleIDs); err != nil {
		response.Error(c, "角色绑定失败")
		return
	}
	response.OK(c, user)
}

func (h *Handler) UpdateUser(c *gin.Context) {
	var user model.User
	if err := h.DB.First(&user, idParam(c)).Error; err != nil {
		response.NotFound(c, "用户不存在")
		return
	}
	var req userReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "参数校验失败")
		return
	}
	user.Nickname, user.Email = req.Nickname, req.Email
	user.DeptID = req.DeptID
	if req.Status != nil {
		user.Status = *req.Status
	}
	if req.Password != "" {
		if len(req.Password) < 8 {
			response.BadRequest(c, "密码至少 8 位")
			return
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
		if err != nil {
			response.Error(c, "密码加密失败")
			return
		}
		user.PasswordHash = string(hash)
	}
	if err := h.DB.Save(&user).Error; err != nil {
		response.Error(c, "用户更新失败")
		return
	}
	if req.RoleIDs != nil {
		if err := h.bindRoles(&user, req.RoleIDs); err != nil {
			response.Error(c, "角色绑定失败")
			return
		}
	}
	response.OK(c, user)
}

func (h *Handler) DeleteUser(c *gin.Context) {
	id := idParam(c)
	if id == 1 {
		response.BadRequest(c, "内置管理员不可删除")
		return
	}
	if cur := middleware.CurrentUser(c); cur != nil && cur.ID == id {
		response.BadRequest(c, "不能删除当前登录账号")
		return
	}
	if err := h.DB.Delete(&model.User{}, id).Error; err != nil {
		response.Error(c, "用户删除失败")
		return
	}
	h.DB.Exec("DELETE FROM user_roles WHERE user_id = ?", id)
	response.OK(c, nil)
}

func (h *Handler) bindRoles(user *model.User, roleIDs []uint) error {
	var roles []model.Role
	if len(roleIDs) > 0 {
		if err := h.DB.Where("id IN ?", roleIDs).Find(&roles).Error; err != nil {
			return err
		}
	}
	return h.DB.Model(user).Association("Roles").Replace(roles)
}

// ---------- 角色 ----------

type roleReq struct {
	Code        string `json:"code" binding:"required"`
	Name        string `json:"name" binding:"required"`
	Description string `json:"description"`
	MenuIDs     []uint `json:"menuIds"`
	DataScope   string `json:"dataScope"`
	DataDeptIDs []uint `json:"dataDeptIds"`
}

// roleDetailView 角色返回结构，把数据范围的部门列表还原成数组
type roleDetailView struct {
	model.Role
	DataDeptIDs []uint `json:"dataDeptIds"`
}

func toRoleView(role model.Role) roleDetailView {
	return roleDetailView{Role: role, DataDeptIDs: parseIDList(role.DataDeptIDs)}
}

func normalizeDataScope(s string) string {
	switch s {
	case model.ScopeDept, model.ScopeDeptBelow, model.ScopeSelf, model.ScopeCustom:
		return s
	default:
		return model.ScopeAll
	}
}

func (h *Handler) ListRoles(c *gin.Context) {
	var list []model.Role
	if err := h.DB.Preload("Menus").Order("id asc").Find(&list).Error; err != nil {
		response.Error(c, "查询角色失败")
		return
	}
	views := make([]roleDetailView, 0, len(list))
	for _, role := range list {
		views = append(views, toRoleView(role))
	}
	response.OK(c, views)
}

func (h *Handler) CreateRole(c *gin.Context) {
	var req roleReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "角色编码与名称为必填项")
		return
	}
	role := model.Role{
		Code: req.Code, Name: req.Name, Description: req.Description,
		DataScope: normalizeDataScope(req.DataScope), DataDeptIDs: marshalIDs(req.DataDeptIDs),
	}
	if err := h.DB.Create(&role).Error; err != nil {
		response.BadRequest(c, "创建失败，角色编码可能已存在")
		return
	}
	if err := h.bindMenus(&role, req.MenuIDs); err != nil {
		response.Error(c, "菜单权限绑定失败")
		return
	}
	response.OK(c, toRoleView(role))
}

func (h *Handler) UpdateRole(c *gin.Context) {
	var role model.Role
	if err := h.DB.First(&role, idParam(c)).Error; err != nil {
		response.NotFound(c, "角色不存在")
		return
	}
	var req roleReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "参数校验失败")
		return
	}
	role.Name, role.Description = req.Name, req.Description
	role.DataScope = normalizeDataScope(req.DataScope)
	role.DataDeptIDs = marshalIDs(req.DataDeptIDs)
	if err := h.DB.Save(&role).Error; err != nil {
		response.Error(c, "角色更新失败")
		return
	}
	if req.MenuIDs != nil {
		if err := h.bindMenus(&role, req.MenuIDs); err != nil {
			response.Error(c, "菜单权限绑定失败")
			return
		}
	}
	response.OK(c, toRoleView(role))
}

func (h *Handler) DeleteRole(c *gin.Context) {
	id := idParam(c)
	if id == 1 {
		response.BadRequest(c, "内置管理员角色不可删除")
		return
	}
	if err := h.DB.Delete(&model.Role{}, id).Error; err != nil {
		response.Error(c, "角色删除失败")
		return
	}
	h.DB.Exec("DELETE FROM role_menus WHERE role_id = ?", id)
	h.DB.Exec("DELETE FROM user_roles WHERE role_id = ?", id)
	response.OK(c, nil)
}

func (h *Handler) bindMenus(role *model.Role, menuIDs []uint) error {
	var menus []model.Menu
	if len(menuIDs) > 0 {
		if err := h.DB.Where("id IN ?", menuIDs).Find(&menus).Error; err != nil {
			return err
		}
	}
	return h.DB.Model(role).Association("Menus").Replace(menus)
}

// ---------- 菜单 ----------

// MenuTree 全量菜单树（含按钮），供角色授权界面使用
func (h *Handler) MenuTree(c *gin.Context) {
	var menus []model.Menu
	if err := h.DB.Order("sort asc").Find(&menus).Error; err != nil {
		response.Error(c, "查询菜单失败")
		return
	}
	response.OK(c, buildFullTree(menus, 0))
}

// FullMenuNode 授权树节点，保留按钮类型与权限码
type FullMenuNode struct {
	ID        uint           `json:"id"`
	ParentID  uint           `json:"parentId"`
	Name      string         `json:"name"`
	Title     string         `json:"title"`
	Path      string         `json:"path"`
	Component string         `json:"component"`
	Icon      string         `json:"icon"`
	Type      string         `json:"type"`
	AuthCode  string         `json:"authCode"`
	Sort      int            `json:"sort"`
	Hidden    bool           `json:"hidden"`
	Builtin   bool           `json:"builtin"`
	Children  []FullMenuNode `json:"children,omitempty"`
}

func buildFullTree(menus []model.Menu, parent uint) []FullMenuNode {
	nodes := make([]FullMenuNode, 0)
	for _, m := range menus {
		if m.ParentID != parent {
			continue
		}
		nodes = append(nodes, FullMenuNode{
			ID: m.ID, ParentID: m.ParentID, Name: m.Name, Title: m.Title, Path: m.Path,
			Component: m.Component, Icon: m.Icon, Type: m.Type, AuthCode: m.AuthCode,
			Sort: m.Sort, Hidden: m.Hidden, Builtin: m.Builtin,
			Children: buildFullTree(menus, m.ID),
		})
	}
	return nodes
}

// ---------- 审计 ----------

func (h *Handler) ListAuditLogs(c *gin.Context) {
	page, size := pageParams(c)
	q := h.DB.Model(&model.AuditLog{})
	if kw := c.Query("username"); kw != "" {
		q = q.Where("username LIKE ?", "%"+kw+"%")
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		response.Error(c, "查询审计日志失败")
		return
	}
	var list []model.AuditLog
	if err := q.Order("id desc").Offset((page - 1) * size).Limit(size).Find(&list).Error; err != nil {
		response.Error(c, "查询审计日志失败")
		return
	}
	response.OKPage(c, list, total, page, size)
}

// ---------- 工作台 ----------

func (h *Handler) DashboardStats(c *gin.Context) {
	var hostTotal, online, offline, userTotal, jobTotal int64
	h.DB.Model(&model.Host{}).Count(&hostTotal)
	h.DB.Model(&model.Host{}).Where("status = ?", "online").Count(&online)
	h.DB.Model(&model.Host{}).Where("status = ?", "offline").Count(&offline)
	h.DB.Model(&model.User{}).Count(&userTotal)
	h.DB.Model(&model.ExecJob{}).Count(&jobTotal)

	type envStat struct {
		Env   string `json:"env"`
		Count int64  `json:"count"`
	}
	var envStats []envStat
	h.DB.Model(&model.Host{}).Select("env, count(*) as count").Group("env").Scan(&envStats)

	var recentJobs []model.ExecJob
	h.DB.Order("id desc").Limit(5).Find(&recentJobs)

	response.OK(c, gin.H{
		"hostTotal":  hostTotal,
		"online":     online,
		"offline":    offline,
		"userTotal":  userTotal,
		"jobTotal":   jobTotal,
		"envStats":   envStats,
		"recentJobs": recentJobs,
	})
}
