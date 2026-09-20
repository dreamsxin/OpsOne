package handler

import (
	"sort"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"

	"ops-platform/server/internal/middleware"
	"ops-platform/server/internal/model"
	"ops-platform/server/internal/response"
)

type loginReq struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
}

// Login 账号密码登录
func (h *Handler) Login(c *gin.Context) {
	var req loginReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "用户名和密码不能为空")
		return
	}

	var user model.User
	if err := h.DB.Where("username = ?", req.Username).First(&user).Error; err != nil {
		// 不区分「用户不存在」与「密码错误」，避免账号枚举
		response.Unauthorized(c, "用户名或密码错误")
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.Password)) != nil {
		response.Unauthorized(c, "用户名或密码错误")
		return
	}
	if user.Status != 1 {
		response.Forbidden(c, "账号已被禁用")
		return
	}

	ttl := time.Duration(h.Cfg.TokenTTLHour) * time.Hour
	token, expire, err := middleware.IssueToken(h.Cfg.JWTSecret, &user, ttl)
	if err != nil {
		response.Error(c, "签发令牌失败")
		return
	}

	now := time.Now()
	h.DB.Model(&user).Update("last_login_at", &now)

	response.OK(c, gin.H{
		"token":     token,
		"expiresAt": expire.Unix(),
		"user":      gin.H{"id": user.ID, "username": user.Username, "nickname": user.Nickname},
	})
}

// Profile 当前登录用户信息与权限码
func (h *Handler) Profile(c *gin.Context) {
	user := middleware.CurrentUser(c)
	perms := middleware.Perms(c)
	codes := make([]string, 0, len(perms))
	for code := range perms {
		codes = append(codes, code)
	}
	sort.Strings(codes)

	roles := make([]gin.H, 0, len(user.Roles))
	for _, r := range user.Roles {
		roles = append(roles, gin.H{"id": r.ID, "code": r.Code, "name": r.Name})
	}

	response.OK(c, gin.H{
		"id":          user.ID,
		"username":    user.Username,
		"nickname":    user.Nickname,
		"email":       user.Email,
		"lastLoginAt": user.LastLoginAt,
		"roles":       roles,
		"permissions": codes,
	})
}

// MenuNode 前端路由树节点
type MenuNode struct {
	ID        uint       `json:"id"`
	Name      string     `json:"name"`
	Title     string     `json:"title"`
	Path      string     `json:"path"`
	Component string     `json:"component"`
	Icon      string     `json:"icon"`
	Sort      int        `json:"sort"`
	Hidden    bool       `json:"hidden"`
	Children  []MenuNode `json:"children,omitempty"`
}

// MyMenus 返回当前用户可见的菜单树，前端据此动态注册路由
func (h *Handler) MyMenus(c *gin.Context) {
	user := middleware.CurrentUser(c)
	roleIDs := make([]uint, 0, len(user.Roles))
	for _, r := range user.Roles {
		roleIDs = append(roleIDs, r.ID)
	}
	if len(roleIDs) == 0 {
		response.OK(c, []MenuNode{})
		return
	}

	var menus []model.Menu
	err := h.DB.Model(&model.Menu{}).
		Joins("JOIN role_menus ON role_menus.menu_id = menus.id").
		Where("role_menus.role_id IN ?", roleIDs).
		Where("menus.type = ?", "menu").
		Distinct().Order("menus.sort asc").Find(&menus).Error
	if err != nil {
		response.Error(c, "加载菜单失败")
		return
	}
	response.OK(c, buildMenuTree(menus, 0))
}

func buildMenuTree(menus []model.Menu, parent uint) []MenuNode {
	nodes := make([]MenuNode, 0)
	for _, m := range menus {
		if m.ParentID != parent {
			continue
		}
		node := MenuNode{
			ID: m.ID, Name: m.Name, Title: m.Title, Path: m.Path,
			Component: m.Component, Icon: m.Icon, Sort: m.Sort, Hidden: m.Hidden,
		}
		node.Children = buildMenuTree(menus, m.ID)
		nodes = append(nodes, node)
	}
	sort.SliceStable(nodes, func(i, j int) bool { return nodes[i].Sort < nodes[j].Sort })
	return nodes
}

type changePwdReq struct {
	OldPassword string `json:"oldPassword" binding:"required"`
	NewPassword string `json:"newPassword" binding:"required,min=8"`
}

// ChangePassword 修改自己的密码
func (h *Handler) ChangePassword(c *gin.Context) {
	var req changePwdReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "新密码至少 8 位")
		return
	}
	user := middleware.CurrentUser(c)
	if bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.OldPassword)) != nil {
		response.BadRequest(c, "原密码不正确")
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(req.NewPassword), bcrypt.DefaultCost)
	if err != nil {
		response.Error(c, "密码加密失败")
		return
	}
	if err := h.DB.Model(user).Update("password_hash", string(hash)).Error; err != nil {
		response.Error(c, "密码更新失败")
		return
	}
	response.OK(c, nil)
}
