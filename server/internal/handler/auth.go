package handler

import (
	"fmt"
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
	Code     string `json:"code"` // 动态验证码，仅在账号绑定了双因子时需要
}

// Login 账号密码登录
func (h *Handler) Login(c *gin.Context) {
	var req loginReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "用户名和密码不能为空")
		return
	}

	// 失败限速与留痕：以前这两件都没有，口令可以无限试，而且失败一次都不留痕
	now := time.Now()
	gateKey := loginGateKey(req.Username, c.ClientIP())
	maxFail, lockFor := h.loginLockSettings()
	if maxFail > 0 {
		if left := loginGuard.check(gateKey, now); left > 0 {
			h.auditLogin(c, req.Username, nil, false,
				fmt.Sprintf("处于锁定期，还需等待 %d 秒", int(left.Seconds())))
			response.Unauthorized(c, lockMessage(left))
			return
		}
	}
	// deny 统一走这里：计数 + 写审计 + 回错误，避免某条分支忘了记
	deny := func(user *model.User, reason string, forbidden bool) {
		detail := reason
		if maxFail > 0 {
			fails, locked := loginGuard.fail(gateKey, maxFail, lockFor, now)
			if locked {
				detail = fmt.Sprintf("%s；连续失败 %d 次，已锁定 %s",
					reason, maxFail, lockFor.Truncate(time.Minute))
			} else {
				detail = fmt.Sprintf("%s（第 %d 次，满 %d 次锁定）", reason, fails, maxFail)
			}
		}
		h.auditLogin(c, req.Username, user, false, detail)
		if forbidden {
			response.Forbidden(c, reason)
			return
		}
		response.Unauthorized(c, "用户名或密码错误")
	}

	var user model.User
	if err := h.DB.Where("username = ?", req.Username).First(&user).Error; err != nil {
		// 不区分「用户不存在」与「密码错误」，避免账号枚举。
		// 但审计里要记实话 —— 审计的读者是管理员，不是攻击者
		deny(nil, "用户不存在", false)
		return
	}

	// 目录托管的账号只认目录口令，本地哈希完全不参与——否则域账号停用后
	// 平台里那份旧口令就是个后门。详见 handler/ldap.go 的说明。
	ldapResult := h.ldapAuthenticate(&user, req.Password)
	if ldapResult.Handled {
		if !ldapResult.OK {
			deny(&user, ldapResult.Reason, ldapResult.Reason != "用户名或密码错误")
			return
		}
	} else if bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.Password)) != nil {
		deny(&user, "口令错误", false)
		return
	}
	if user.Status != 1 {
		deny(&user, "账号已被禁用", true)
		return
	}

	// 口令通过后再要第二因子。口令错误时不暴露「这个账号开了双因子」这类信息。
	// 域账号登录同样要过双因子——目录只解决「口令由谁管」，不代替第二因子。
	if user.TOTPEnabled {
		ok, detail := h.verifyLoginTOTP(&user, req.Code)
		if !ok {
			// 动态码也计入失败次数：只挡口令不挡验证码的话，
			// 拿到口令的人仍然可以对 6 位数字无限尝试
			if maxFail > 0 {
				loginGuard.fail(gateKey, maxFail, lockFor, now)
			}
			h.auditLogin(c, req.Username, &user, false, "双因子校验未通过: "+detail)
			// 用 200 + totpRequired 表达「还缺一步」，避免与 401（口令错误，前端会清 token 跳登录）混在一起
			response.OK(c, gin.H{"totpRequired": true, "detail": detail})
			return
		}
	}

	ttl := time.Duration(h.Cfg.TokenTTLHour) * time.Hour
	token, expire, err := middleware.IssueToken(h.Cfg.JWTSecret, &user, ttl)
	if err != nil {
		response.Error(c, "签发令牌失败")
		return
	}

	h.DB.Model(&user).Update("last_login_at", &now)
	loginGuard.success(gateKey)

	loginBy := "local"
	if ldapResult.Handled {
		loginBy = "ldap"
		h.markLdapLogin(ldapResult.Binding)
	}
	h.auditLogin(c, req.Username, &user, true, "登录成功（"+loginBy+"）")
	response.OK(c, gin.H{
		"token":     token,
		"expiresAt": expire.Unix(),
		"loginBy":   loginBy,
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
		"totpEnabled": user.TOTPEnabled,
		// 强制模式下未绑定的账号，除 /me 系接口外都会被后端拦下，前端据此引导到绑定页
		"totpEnforced": h.totpMode() == "required",
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
