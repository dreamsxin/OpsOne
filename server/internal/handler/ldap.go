package handler

import (
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"ops-platform/server/internal/ldapx"
	"ops-platform/server/internal/middleware"
	"ops-platform/server/internal/model"
	"ops-platform/server/internal/response"
)

// LDAP / AD 账号接入：让员工用域账号口令登录平台。
//
// 三条刻意的设计取舍：
//
//  1. **绑定即授权，且不自动建号**。能不能进平台，由「平台里有没有这个账号、
//     有没有绑定到目录条目」决定，不由目录决定。目录里新增一个人不会凭空得到
//     平台账号——这与 IM 扫码登录同一条原则（见 docs/SECURITY.md 第 24 节）。
//  2. **一旦绑定，本地口令立即失效**。否则会留下一个真实的后门：域账号被停用/
//     离职后，此人仍能用平台里那份旧口令哈希登录。所以判定顺序是「先看有没有
//     绑定」，有绑定就只认目录，本地哈希完全不参与。
//  3. **平台只读目录**。只做 bind 与 search，不写回、不建组、不同步组织结构。
//     组→角色映射也不做：那会把「谁是管理员」交给目录管理员决定。
//
// 能力边界：目录里把账号停用/锁定的表现形式各家不同（AD 用 userAccountControl，
// OpenLDAP 常用 ppolicy）。平台不解析这些属性，而是**依赖 bind 本身失败**——
// 账号被停用后 bind 会被目录拒绝，这正是我们要的效果，但「为什么失败」只能显示
// 目录返回的原话。

const (
	ldapDefaultNickname = "displayName"
	ldapDefaultEmail    = "mail"
	ldapSearchMax       = 50
)

// ---------- 配置与视图 ----------

type ldapServerView struct {
	model.LdapServer
	// HasBindPassword 只告诉界面「配过没有」，不回传口令本身
	HasBindPassword bool   `json:"hasBindPassword"`
	URL             string `json:"url"`
	BoundCount      int64  `json:"boundCount"`
}

func (h *Handler) toLdapView(item model.LdapServer) ldapServerView {
	view := ldapServerView{
		LdapServer:      item,
		HasBindPassword: item.BindPassword != "",
		URL:             h.ldapTarget(item).URL(),
	}
	h.DB.Model(&model.LdapAccount{}).Where("server_id = ?", item.ID).Count(&view.BoundCount)
	return view
}

// ldapTarget 把库里的配置翻译成 ldapx 的入参
func (h *Handler) ldapTarget(item model.LdapServer) ldapx.Server {
	return ldapx.Server{
		Host: item.Host, Port: item.Port, Encryption: item.Encryption, SkipVerify: item.SkipVerify,
		BindDN: item.BindDN, BindPassword: item.BindPassword, BaseDN: item.BaseDN,
		UserFilter: item.UserFilter, AttrNickname: item.AttrNickname, AttrEmail: item.AttrEmail,
		TimeoutSec: item.TimeoutSec,
	}
}

type ldapServerReq struct {
	Name         string `json:"name" binding:"required"`
	Host         string `json:"host" binding:"required"`
	Port         int    `json:"port"`
	Encryption   string `json:"encryption"`
	SkipVerify   bool   `json:"skipVerify"`
	BindDN       string `json:"bindDn"`
	BindPassword string `json:"bindPassword"` // 留空表示不修改
	BaseDN       string `json:"baseDn" binding:"required"`
	UserFilter   string `json:"userFilter"`
	AttrNickname string `json:"attrNickname"`
	AttrEmail    string `json:"attrEmail"`
	TimeoutSec   int    `json:"timeoutSec"`
	Enabled      *bool  `json:"enabled"`
	LoginEnabled *bool  `json:"loginEnabled"`
	AutoBind     *bool  `json:"autoBind"`
}

func (r *ldapServerReq) normalize() error {
	r.Name = strings.TrimSpace(r.Name)
	r.Host = strings.TrimSpace(r.Host)
	r.BindDN = strings.TrimSpace(r.BindDN)
	r.BaseDN = strings.TrimSpace(r.BaseDN)
	r.UserFilter = strings.TrimSpace(r.UserFilter)

	switch r.Encryption {
	case ldapx.EncNone, ldapx.EncLDAPS, ldapx.EncStartTLS:
	case "":
		r.Encryption = ldapx.EncNone
	default:
		return fmt.Errorf("加密方式只能是 none / ldaps / starttls")
	}
	if r.Port <= 0 {
		if r.Encryption == ldapx.EncLDAPS {
			r.Port = 636
		} else {
			r.Port = 389
		}
	}
	if r.Port > 65535 {
		return fmt.Errorf("端口不合法")
	}
	if r.UserFilter == "" {
		r.UserFilter = ldapx.DefaultUserFilter
	}
	if !strings.Contains(r.UserFilter, "%s") {
		return fmt.Errorf("用户过滤器必须含一个 %%s 占位，登录名会替换到那里")
	}
	if strings.Count(r.UserFilter, "%s") > 1 {
		return fmt.Errorf("用户过滤器只允许一个 %%s 占位")
	}
	if r.AttrNickname == "" {
		r.AttrNickname = ldapDefaultNickname
	}
	if r.AttrEmail == "" {
		r.AttrEmail = ldapDefaultEmail
	}
	if r.TimeoutSec <= 0 {
		r.TimeoutSec = 8
	}
	if r.TimeoutSec > 60 {
		return fmt.Errorf("超时最多 60 秒")
	}
	// 配了服务账号 DN 却没口令，搜索会退化成匿名，多半不是本意
	if r.BindDN != "" && r.BindPassword == "" {
		return nil // 更新场景留空表示不改，真正的校验在 Update 里
	}
	return nil
}

// ---------- 服务器 CRUD ----------

func (h *Handler) ListLdapServers(c *gin.Context) {
	var list []model.LdapServer
	if err := h.DB.Order("id asc").Find(&list).Error; err != nil {
		response.Error(c, "查询 LDAP 服务器失败")
		return
	}
	views := make([]ldapServerView, 0, len(list))
	for _, item := range list {
		views = append(views, h.toLdapView(item))
	}
	response.OK(c, views)
}

func (h *Handler) CreateLdapServer(c *gin.Context) {
	var req ldapServerReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "名称、地址与 Base DN 为必填项")
		return
	}
	if err := req.normalize(); err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	item := model.LdapServer{
		Name: req.Name, Host: req.Host, Port: req.Port, Encryption: req.Encryption,
		SkipVerify: req.SkipVerify, BindDN: req.BindDN, BindPassword: req.BindPassword,
		BaseDN: req.BaseDN, UserFilter: req.UserFilter,
		AttrNickname: req.AttrNickname, AttrEmail: req.AttrEmail, TimeoutSec: req.TimeoutSec,
		CreatedBy: middleware.CurrentUser(c).ID,
	}
	// 带 default 的布尔在 Create 时会被 GORM 当零值忽略，先记下意图再回写
	enabled, loginEnabled := true, true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	if req.LoginEnabled != nil {
		loginEnabled = *req.LoginEnabled
	}
	autoBind := req.AutoBind != nil && *req.AutoBind
	item.Enabled, item.LoginEnabled, item.AutoBind = enabled, loginEnabled, autoBind

	if err := h.DB.Create(&item).Error; err != nil {
		response.Error(c, "保存失败")
		return
	}
	if item.Enabled != enabled || item.LoginEnabled != loginEnabled || item.AutoBind != autoBind {
		h.DB.Model(&item).Updates(map[string]any{
			"enabled": enabled, "login_enabled": loginEnabled, "auto_bind": autoBind,
		})
		item.Enabled, item.LoginEnabled, item.AutoBind = enabled, loginEnabled, autoBind
	}
	response.OK(c, h.toLdapView(item))
}

func (h *Handler) UpdateLdapServer(c *gin.Context) {
	var item model.LdapServer
	if err := h.DB.First(&item, idParam(c)).Error; err != nil {
		response.NotFound(c, "服务器不存在")
		return
	}
	var req ldapServerReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "参数校验失败")
		return
	}
	if err := req.normalize(); err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	updates := map[string]any{
		"name": req.Name, "host": req.Host, "port": req.Port, "encryption": req.Encryption,
		"skip_verify": req.SkipVerify, "bind_dn": req.BindDN, "base_dn": req.BaseDN,
		"user_filter": req.UserFilter, "attr_nickname": req.AttrNickname,
		"attr_email": req.AttrEmail, "timeout_sec": req.TimeoutSec,
	}
	// 口令留空表示不改，避免编辑其它字段时把口令清掉
	if req.BindPassword != "" {
		updates["bind_password"] = req.BindPassword
	}
	if req.Enabled != nil {
		updates["enabled"] = *req.Enabled
	}
	if req.LoginEnabled != nil {
		updates["login_enabled"] = *req.LoginEnabled
	}
	if req.AutoBind != nil {
		updates["auto_bind"] = *req.AutoBind
	}
	if err := h.DB.Model(&item).Updates(updates).Error; err != nil {
		response.Error(c, "更新失败")
		return
	}
	h.DB.First(&item, item.ID)
	response.OK(c, h.toLdapView(item))
}

// DeleteLdapServer 删除服务器。还有账号绑在上面时拒绝：
// 直接删会让那些平台账号既登不上目录、也回不到本地口令。
func (h *Handler) DeleteLdapServer(c *gin.Context) {
	id := idParam(c)
	var bound int64
	h.DB.Model(&model.LdapAccount{}).Where("server_id = ?", id).Count(&bound)
	if bound > 0 {
		response.BadRequest(c, fmt.Sprintf("还有 %d 个账号绑定在这台服务器上，请先解绑（解绑后这些账号会回落到本地口令）", bound))
		return
	}
	if err := h.DB.Delete(&model.LdapServer{}, id).Error; err != nil {
		response.Error(c, "删除失败")
		return
	}
	response.OK(c, gin.H{"id": id})
}

// ---------- 连通性与搜索 ----------

// CheckLdapServer 真的建连、真的用服务账号 bind、真的在 BaseDN 下搜一次
func (h *Handler) CheckLdapServer(c *gin.Context) {
	var item model.LdapServer
	if err := h.DB.First(&item, idParam(c)).Error; err != nil {
		response.NotFound(c, "服务器不存在")
		return
	}

	now := time.Now()
	count, err := ldapx.Ping(h.ldapTarget(item))
	status, message := "success", fmt.Sprintf("连接正常，%s 下匹配到 %d 个用户条目", item.BaseDN, count)
	if err != nil {
		status, message = "failed", err.Error()
	}
	h.DB.Model(&item).Updates(map[string]any{
		"last_check_at": &now, "last_status": status, "last_message": truncate(message, 500),
	})

	if err != nil {
		response.BadRequest(c, message)
		return
	}
	response.OK(c, gin.H{"status": status, "userCount": count, "detail": message, "url": h.ldapTarget(item).URL()})
}

// SearchLdapUsers 在目录里找人，供管理员挑选后绑定到平台账号
func (h *Handler) SearchLdapUsers(c *gin.Context) {
	var item model.LdapServer
	if err := h.DB.First(&item, idParam(c)).Error; err != nil {
		response.NotFound(c, "服务器不存在")
		return
	}
	entries, err := ldapx.Search(h.ldapTarget(item), c.Query("keyword"), ldapSearchMax)
	if err != nil {
		response.BadRequest(c, "搜索失败: "+err.Error())
		return
	}

	// 标出哪些条目已经绑过，避免重复绑定
	var bound []model.LdapAccount
	h.DB.Where("server_id = ?", item.ID).Find(&bound)
	boundBy := map[string]string{}
	for _, b := range bound {
		boundBy[b.LdapDN] = b.Username
	}

	list := make([]gin.H, 0, len(entries))
	for _, e := range entries {
		list = append(list, gin.H{
			"dn": e.DN, "uid": e.UID, "nickname": e.Nickname, "email": e.Email,
			"boundTo": boundBy[e.DN],
		})
	}
	response.OK(c, gin.H{"list": list, "total": len(list), "limit": ldapSearchMax})
}

// ---------- 绑定关系 ----------

func (h *Handler) ListLdapAccounts(c *gin.Context) {
	page, size := pageParams(c)
	q := h.DB.Model(&model.LdapAccount{})
	if sid := strings.TrimSpace(c.Query("serverId")); sid != "" {
		q = q.Where("server_id = ?", sid)
	}
	if kw := strings.TrimSpace(c.Query("keyword")); kw != "" {
		like := "%" + kw + "%"
		q = q.Where("username LIKE ? OR ldap_uid LIKE ? OR ldap_dn LIKE ?", like, like, like)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		response.Error(c, "查询绑定关系失败")
		return
	}
	var list []model.LdapAccount
	if err := q.Order("id desc").Offset((page - 1) * size).Limit(size).Find(&list).Error; err != nil {
		response.Error(c, "查询绑定关系失败")
		return
	}
	response.OKPage(c, list, total, page, size)
}

type ldapBindReq struct {
	ServerID uint   `json:"serverId" binding:"required"`
	UserID   uint   `json:"userId" binding:"required"`
	LdapDN   string `json:"ldapDn" binding:"required"`
	LdapUID  string `json:"ldapUid"`
}

// BindLdapAccount 把平台账号绑到目录条目上。
//
// 绑定成功意味着这个账号今后只能用域口令登录。这里不校验口令（管理员不该知道
// 员工口令），但会用服务账号确认条目真实存在——否则会绑出一个谁都登不上的账号。
func (h *Handler) BindLdapAccount(c *gin.Context) {
	var req ldapBindReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "服务器、平台账号与目录条目都必须指定")
		return
	}

	var server model.LdapServer
	if err := h.DB.First(&server, req.ServerID).Error; err != nil {
		response.NotFound(c, "服务器不存在")
		return
	}
	var user model.User
	if err := h.DB.First(&user, req.UserID).Error; err != nil {
		response.NotFound(c, "平台账号不存在")
		return
	}

	var exist model.LdapAccount
	if err := h.DB.Where("user_id = ?", user.ID).First(&exist).Error; err == nil {
		response.BadRequest(c, fmt.Sprintf("平台账号 %s 已经绑定了目录条目 %s，请先解绑", user.Username, exist.LdapDN))
		return
	}
	if err := h.DB.Where("server_id = ? AND ldap_dn = ?", server.ID, req.LdapDN).First(&exist).Error; err == nil {
		response.BadRequest(c, fmt.Sprintf("该目录条目已绑定到平台账号 %s", exist.Username))
		return
	}

	uid := strings.TrimSpace(req.LdapUID)
	if uid == "" {
		uid = user.Username
	}
	// 确认条目真的存在，并顺手取回显示名与邮箱
	entry, err := ldapx.Lookup(h.ldapTarget(server), uid)
	if err != nil {
		response.BadRequest(c, "查询目录失败: "+err.Error())
		return
	}
	if entry == nil {
		response.BadRequest(c, fmt.Sprintf("目录里按用户过滤器找不到 %s，请确认登录名或过滤器", uid))
		return
	}
	if entry.DN != req.LdapDN {
		response.BadRequest(c, fmt.Sprintf("登录名 %s 在目录里对应的是 %s，与选中的条目不一致", uid, entry.DN))
		return
	}

	item := model.LdapAccount{
		ServerID: server.ID, LdapUID: uid, LdapDN: entry.DN,
		LdapName: entry.Nickname, LdapEmail: entry.Email,
		UserID: user.ID, Username: user.Username,
		BoundBy: middleware.CurrentUser(c).Username,
	}
	if err := h.DB.Create(&item).Error; err != nil {
		response.Error(c, "绑定失败")
		return
	}
	response.OK(c, item)
}

// UnbindLdapAccount 解绑。解绑后该账号回落到本地口令——如果本地口令是随机初始值，
// 等于这个人暂时登不进来，所以提示里点明这件事。
func (h *Handler) UnbindLdapAccount(c *gin.Context) {
	var item model.LdapAccount
	if err := h.DB.First(&item, idParam(c)).Error; err != nil {
		response.NotFound(c, "绑定关系不存在")
		return
	}
	if err := h.DB.Delete(&model.LdapAccount{}, item.ID).Error; err != nil {
		response.Error(c, "解绑失败")
		return
	}
	response.OK(c, gin.H{
		"id":     item.ID,
		"detail": fmt.Sprintf("已解绑 %s，该账号回落到本地口令登录（本地口令未设置过的话需要管理员重置）", item.Username),
	})
}

// ---------- 试登录诊断 ----------

type ldapTryReq struct {
	ServerID uint   `json:"serverId" binding:"required"`
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
}

// TryLdapLogin 用真实口令走一遍目录校验，把「卡在哪一步」说清楚。
//
// 它只做诊断，不签发令牌、不建立绑定：接入调试期最常见的问题是过滤器写错、
// Base DN 写错、服务账号没权限，光看登录页那句「用户名或密码错误」查不出来。
func (h *Handler) TryLdapLogin(c *gin.Context) {
	var req ldapTryReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "服务器、登录名与口令都要填")
		return
	}
	var server model.LdapServer
	if err := h.DB.First(&server, req.ServerID).Error; err != nil {
		response.NotFound(c, "服务器不存在")
		return
	}

	target := h.ldapTarget(server)
	steps := []gin.H{}
	add := func(name string, ok bool, detail string) {
		steps = append(steps, gin.H{"step": name, "ok": ok, "detail": detail})
	}

	entry, err := ldapx.Lookup(target, req.Username)
	if err != nil {
		add("按用户过滤器查条目", false, err.Error())
		response.OK(c, gin.H{"ok": false, "steps": steps, "detail": "目录查询失败：" + err.Error()})
		return
	}
	if entry == nil {
		add("按用户过滤器查条目", false,
			fmt.Sprintf("在 %s 下用 %s 没找到这个人", server.BaseDN, ldapx.Filter(server.UserFilter, req.Username)))
		response.OK(c, gin.H{"ok": false, "steps": steps, "detail": "目录里找不到这个登录名"})
		return
	}
	add("按用户过滤器查条目", true, entry.DN)

	if err := ldapx.Authenticate(target, entry.DN, req.Password); err != nil {
		add("用该条目校验口令", false, err.Error())
		response.OK(c, gin.H{"ok": false, "steps": steps, "detail": "目录拒绝了这个口令：" + err.Error()})
		return
	}
	add("用该条目校验口令", true, "目录接受了这个口令")

	// 到这一步目录已经认了，剩下的是平台侧的账号与绑定
	var binding model.LdapAccount
	if err := h.DB.Where("server_id = ? AND ldap_dn = ?", server.ID, entry.DN).First(&binding).Error; err != nil {
		var user model.User
		if err := h.DB.Where("username = ?", req.Username).First(&user).Error; err != nil {
			add("平台侧账号与绑定", false, "平台里没有同名账号，且平台不会按目录自动建号")
			response.OK(c, gin.H{"ok": false, "steps": steps,
				"detail": "目录认证通过，但平台里没有这个账号——请先在「用户管理」建号再绑定"})
			return
		}
		if server.AutoBind {
			add("平台侧账号与绑定", true,
				fmt.Sprintf("尚未绑定，但该服务器开了首次登录自动绑定，真实登录时会绑到 %s", user.Username))
		} else {
			add("平台侧账号与绑定", false,
				fmt.Sprintf("平台账号 %s 存在但没有绑定，且未开启自动绑定，真实登录会走本地口令", user.Username))
			response.OK(c, gin.H{"ok": false, "steps": steps,
				"detail": "目录认证通过，但这个平台账号没绑定到目录，域口令登录不会生效"})
			return
		}
	} else {
		add("平台侧账号与绑定", true, fmt.Sprintf("已绑定到平台账号 %s", binding.Username))
	}

	if !server.Enabled || !server.LoginEnabled {
		add("服务器开关", false, "服务器已停用或关闭了域账号登录，真实登录会被拒绝")
		response.OK(c, gin.H{"ok": false, "steps": steps, "detail": "配置本身没问题，但开关是关的"})
		return
	}
	add("服务器开关", true, "已启用且允许域账号登录")

	response.OK(c, gin.H{"ok": true, "steps": steps, "detail": "这个账号可以用域口令登录平台",
		"entry": gin.H{"dn": entry.DN, "nickname": entry.Nickname, "email": entry.Email}})
}

// ---------- 登录链路 ----------

// ldapLoginResult 登录判定结果
type ldapLoginResult struct {
	// Handled 为 true 表示这个账号的口令由目录说了算，本地口令不再参与
	Handled bool
	OK      bool
	Reason  string
	Server  model.LdapServer
	Binding *model.LdapAccount
}

// ldapAuthenticate 在本地口令校验之前判定这个账号该怎么登。
//
// 返回 Handled=false 表示与 LDAP 无关，交回本地口令流程。
func (h *Handler) ldapAuthenticate(user *model.User, password string) ldapLoginResult {
	var binding model.LdapAccount
	err := h.DB.Where("user_id = ?", user.ID).First(&binding).Error
	if err == nil {
		var server model.LdapServer
		if err := h.DB.First(&server, binding.ServerID).Error; err != nil {
			// 绑定指向的服务器没了：不能回落到本地口令（那正是要堵的后门）
			return ldapLoginResult{Handled: true, Reason: "账号绑定的目录服务器已不存在，请联系管理员"}
		}
		if !server.Enabled || !server.LoginEnabled {
			return ldapLoginResult{Handled: true, Server: server,
				Reason: "账号由目录服务器托管，但该服务器当前不允许登录，请联系管理员"}
		}
		if err := ldapx.Authenticate(h.ldapTarget(server), binding.LdapDN, password); err != nil {
			return ldapLoginResult{Handled: true, Server: server, Binding: &binding,
				Reason: "用户名或密码错误"}
		}
		return ldapLoginResult{Handled: true, OK: true, Server: server, Binding: &binding}
	}

	// 没有绑定：只有开了自动绑定的服务器才可能接管
	var servers []model.LdapServer
	h.DB.Where("enabled = ? AND login_enabled = ? AND auto_bind = ?", true, true, true).
		Order("id asc").Find(&servers)
	for _, server := range servers {
		entry, err := ldapx.Lookup(h.ldapTarget(server), user.Username)
		if err != nil {
			log.Printf("[ldap] 自动绑定查询失败(%s): %v", server.Name, err)
			continue
		}
		if entry == nil {
			continue
		}
		if err := ldapx.Authenticate(h.ldapTarget(server), entry.DN, password); err != nil {
			// 目录里有这个人但口令不对：不要偷偷回落到本地口令，否则「域口令改了
			// 之后旧的本地口令还能用」，等于两套口令并存
			return ldapLoginResult{Handled: true, Server: server, Reason: "用户名或密码错误"}
		}
		binding := model.LdapAccount{
			ServerID: server.ID, LdapUID: user.Username, LdapDN: entry.DN,
			LdapName: entry.Nickname, LdapEmail: entry.Email,
			UserID: user.ID, Username: user.Username, BoundBy: "auto",
		}
		if err := h.DB.Create(&binding).Error; err != nil {
			log.Printf("[ldap] 自动绑定写库失败: %v", err)
		}
		return ldapLoginResult{Handled: true, OK: true, Server: server, Binding: &binding}
	}
	return ldapLoginResult{}
}

// markLdapLogin 记一次目录登录成功
func (h *Handler) markLdapLogin(binding *model.LdapAccount) {
	if binding == nil || binding.ID == 0 {
		return
	}
	now := time.Now()
	h.DB.Model(&model.LdapAccount{ID: binding.ID}).Update("last_login", &now)
}

// ListLdapLoginHint 登录页用：是否存在启用中的目录登录，用来提示「请使用域账号口令」
func (h *Handler) ListLdapLoginHint(c *gin.Context) {
	var count int64
	h.DB.Model(&model.LdapServer{}).
		Where("enabled = ? AND login_enabled = ?", true, true).Count(&count)
	response.OK(c, gin.H{"enabled": count > 0})
}
