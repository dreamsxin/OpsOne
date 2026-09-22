package handler

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"ops-platform/server/internal/middleware"
	"ops-platform/server/internal/model"
	"ops-platform/server/internal/response"
)

// 服务账号令牌（API Token）：让 CI、脚本、外部系统能调平台接口，
// 而不是把某个人的账号口令写进流水线配置里。
//
// 几条刻意的设计取舍：
//
//   - **明文只出现一次**。创建与轮换时返回，库里只存 SHA-256 哈希。丢了只能轮换，
//     平台自己也查不回来——这是这类功能唯一诚实的做法。
//   - **权限取交集**：令牌权限 = 显式授予的权限码 ∩ 归属人当下的权限。归属人被降权
//     或停用，令牌立即跟着失效，不会出现「人走了令牌还在干活」。
//   - **默认只读**。要写必须显式关掉只读并授予权限码，两步都得做。
//   - **不能开终端**。令牌不接受 access_token 查询参数、也拒绝 WebSocket 升级；
//     终端与端口转发必须是人，否则会话审计里会出现一个没有主体的会话。
//   - **不做细到接口的 ACL**。授权粒度就是现有的权限码（与人一致），不另造一套
//     只给令牌用的权限模型——两套权限模型迟早对不上。
const (
	apiTokenBytes     = 32
	apiTokenMaxTTLDay = 730
	apiTokenListMax   = 200
)

// ---------- 视图与请求 ----------

type apiTokenView struct {
	model.ApiToken
	ScopeList []string `json:"scopeList"`
	Status    string   `json:"status"` // active | disabled | revoked | expired
}

func toAPITokenView(item model.ApiToken) apiTokenView {
	view := apiTokenView{ApiToken: item, ScopeList: middleware.ScopeList(item.Scopes)}
	switch {
	case item.RevokedAt != nil:
		view.Status = "revoked"
	case !item.Enabled:
		view.Status = "disabled"
	case item.ExpiresAt != nil && time.Now().After(*item.ExpiresAt):
		view.Status = "expired"
	default:
		view.Status = "active"
	}
	return view
}

type apiTokenReq struct {
	Name        string   `json:"name" binding:"required"`
	OwnerUserID uint     `json:"ownerUserId"`
	Scopes      []string `json:"scopes"`
	ReadOnly    *bool    `json:"readOnly"`
	AllowIPs    string   `json:"allowIps"`
	// ExpiresInDays 0 表示不过期
	ExpiresInDays int    `json:"expiresInDays"`
	Remark        string `json:"remark"`
	Enabled       *bool  `json:"enabled"`
}

func (r *apiTokenReq) normalize() error {
	r.Name = strings.TrimSpace(r.Name)
	r.AllowIPs = strings.TrimSpace(r.AllowIPs)
	r.Remark = strings.TrimSpace(r.Remark)
	if r.Name == "" {
		return fmt.Errorf("令牌名称不能为空")
	}
	if len(r.Name) > 64 {
		return fmt.Errorf("令牌名称最多 64 字符")
	}
	if r.ExpiresInDays < 0 || r.ExpiresInDays > apiTokenMaxTTLDay {
		return fmt.Errorf("有效期最多 %d 天，0 表示不过期", apiTokenMaxTTLDay)
	}

	cleaned := make([]string, 0, len(r.Scopes))
	seen := map[string]struct{}{}
	for _, code := range r.Scopes {
		code = strings.TrimSpace(code)
		if code == "" {
			continue
		}
		if _, dup := seen[code]; dup {
			continue
		}
		seen[code] = struct{}{}
		cleaned = append(cleaned, code)
	}
	r.Scopes = cleaned

	readOnly := r.ReadOnly == nil || *r.ReadOnly
	if !readOnly && len(cleaned) == 0 {
		return fmt.Errorf("可写令牌必须至少授予一个权限码，否则它什么也做不了")
	}

	// 白名单写错比不写更危险（会以为限制住了），所以格式在这里就挡掉
	for _, item := range strings.Split(r.AllowIPs, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if !validIPRule(item) {
			return fmt.Errorf("来源白名单条目不合法: %s（支持 IP 或 CIDR）", item)
		}
	}
	return nil
}

// validIPRule 判断一条白名单条目是不是合法的 IP 或 CIDR
func validIPRule(item string) bool {
	if strings.Contains(item, "/") {
		_, _, err := net.ParseCIDR(item)
		return err == nil
	}
	return net.ParseIP(item) != nil
}

// newAPITokenPlain 生成令牌明文：opst_ + 43 字符随机串
func newAPITokenPlain() (string, error) {
	buf := make([]byte, apiTokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return middleware.ApiTokenPrefix + base64.RawURLEncoding.EncodeToString(buf), nil
}

// ---------- 接口 ----------

// ListApiTokens 令牌列表。明文与哈希都不回传。
func (h *Handler) ListApiTokens(c *gin.Context) {
	page, size := pageParams(c)
	q := h.DB.Model(&model.ApiToken{})
	if kw := strings.TrimSpace(c.Query("keyword")); kw != "" {
		like := "%" + kw + "%"
		q = q.Where("name LIKE ? OR owner_name LIKE ? OR remark LIKE ?", like, like, like)
	}
	if status := strings.TrimSpace(c.Query("status")); status == "active" {
		q = q.Where("enabled = ? AND revoked_at IS NULL", true)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		response.Error(c, "查询令牌失败")
		return
	}
	var list []model.ApiToken
	if err := q.Order("id desc").Offset((page - 1) * size).Limit(size).Find(&list).Error; err != nil {
		response.Error(c, "查询令牌失败")
		return
	}

	views := make([]apiTokenView, 0, len(list))
	for _, item := range list {
		views = append(views, toAPITokenView(item))
	}
	response.OKPage(c, views, total, page, size)
}

// CreateApiToken 新建令牌，明文只在这里返回一次
func (h *Handler) CreateApiToken(c *gin.Context) {
	var req apiTokenReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "令牌名称不能为空")
		return
	}
	if err := req.normalize(); err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	operator := middleware.CurrentUser(c)
	ownerID := req.OwnerUserID
	if ownerID == 0 {
		ownerID = operator.ID
	}
	var owner model.User
	if err := h.DB.Preload("Roles").First(&owner, ownerID).Error; err != nil {
		response.BadRequest(c, "归属账号不存在")
		return
	}
	if owner.Status != 1 {
		response.BadRequest(c, "归属账号已停用，先启用再发令牌")
		return
	}

	plain, err := newAPITokenPlain()
	if err != nil {
		response.Error(c, "生成令牌失败")
		return
	}
	raw, _ := json.Marshal(req.Scopes)

	readOnly := req.ReadOnly == nil || *req.ReadOnly
	enabled := req.Enabled == nil || *req.Enabled
	item := model.ApiToken{
		Name: req.Name, Prefix: middleware.APITokenPrefixOf(plain),
		TokenHash:   middleware.HashAPIToken(plain),
		OwnerUserID: owner.ID, OwnerName: owner.Username,
		Scopes: string(raw), AllowIPs: req.AllowIPs, Remark: req.Remark,
		CreatedBy: operator.Username,
	}
	if req.ExpiresInDays > 0 {
		expire := time.Now().AddDate(0, 0, req.ExpiresInDays)
		item.ExpiresAt = &expire
	}
	item.ReadOnly, item.Enabled = readOnly, enabled

	if err := h.DB.Create(&item).Error; err != nil {
		response.Error(c, "保存令牌失败")
		return
	}
	// ReadOnly / Enabled 都不带 gorm default（见 model 包注释），Create 原样写入，
	// 「用户要一个可写令牌」不会被数据库默认值悄悄改回只读

	// 这里是明文唯一一次露面
	response.OK(c, gin.H{
		"token": toAPITokenView(item), "plain": plain,
		"detail":          "请立即复制保存：平台只存哈希，这串明文不会再出现，丢了只能轮换",
		"usage":           fmt.Sprintf("curl -H 'Authorization: Bearer %s' <平台地址>/api/v1/hosts", plain),
		"effectiveScopes": h.effectiveScopes(&owner, req.Scopes),
	})
}

// effectiveScopes 告诉调用方「真正生效的权限码」，把授了但归属人没有的那些标出来
func (h *Handler) effectiveScopes(owner *model.User, scopes []string) gin.H {
	roleIDs := make([]uint, 0, len(owner.Roles))
	for _, r := range owner.Roles {
		roleIDs = append(roleIDs, r.ID)
	}
	ownerPerms := map[string]struct{}{}
	if len(roleIDs) > 0 {
		var codes []string
		h.DB.Model(&model.Menu{}).
			Joins("JOIN role_menus ON role_menus.menu_id = menus.id").
			Where("role_menus.role_id IN ?", roleIDs).
			Where("menus.auth_code <> ''").
			Distinct().Pluck("menus.auth_code", &codes)
		for _, code := range codes {
			ownerPerms[code] = struct{}{}
		}
	}

	granted, ignored := []string{}, []string{}
	for _, code := range scopes {
		if _, ok := ownerPerms[code]; ok {
			granted = append(granted, code)
		} else {
			ignored = append(ignored, code)
		}
	}
	return gin.H{"granted": granted, "ignored": ignored}
}

// UpdateApiToken 改配置（名称、权限、只读、白名单、有效期、启停），不换明文
func (h *Handler) UpdateApiToken(c *gin.Context) {
	var item model.ApiToken
	if err := h.DB.First(&item, idParam(c)).Error; err != nil {
		response.NotFound(c, "令牌不存在")
		return
	}
	if item.RevokedAt != nil {
		response.BadRequest(c, "令牌已撤销，不能再修改（请新建一个）")
		return
	}
	var req apiTokenReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "参数校验失败")
		return
	}
	if err := req.normalize(); err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	raw, _ := json.Marshal(req.Scopes)
	updates := map[string]any{
		"name": req.Name, "scopes": string(raw), "allow_ips": req.AllowIPs, "remark": req.Remark,
	}
	if req.ReadOnly != nil {
		updates["read_only"] = *req.ReadOnly
	}
	if req.Enabled != nil {
		updates["enabled"] = *req.Enabled
	}
	if req.ExpiresInDays > 0 {
		expire := time.Now().AddDate(0, 0, req.ExpiresInDays)
		updates["expires_at"] = &expire
	}
	if err := h.DB.Model(&item).Updates(updates).Error; err != nil {
		response.Error(c, "更新失败")
		return
	}
	h.DB.First(&item, item.ID)
	response.OK(c, toAPITokenView(item))
}

// RotateApiToken 轮换：旧明文立即失效，返回新明文（同样只这一次）
func (h *Handler) RotateApiToken(c *gin.Context) {
	var item model.ApiToken
	if err := h.DB.First(&item, idParam(c)).Error; err != nil {
		response.NotFound(c, "令牌不存在")
		return
	}
	if item.RevokedAt != nil {
		response.BadRequest(c, "令牌已撤销，不能轮换")
		return
	}

	plain, err := newAPITokenPlain()
	if err != nil {
		response.Error(c, "生成令牌失败")
		return
	}
	err = h.DB.Model(&item).Updates(map[string]any{
		"prefix": middleware.APITokenPrefixOf(plain),
		// 轮换即让旧明文失效：哈希一换，旧的再也比不上
		"token_hash":   middleware.HashAPIToken(plain),
		"last_used_at": nil, "last_used_ip": "", "use_count": 0,
	}).Error
	if err != nil {
		response.Error(c, "轮换失败")
		return
	}
	h.DB.First(&item, item.ID)
	response.OK(c, gin.H{
		"token": toAPITokenView(item), "plain": plain,
		"detail": "旧令牌已立即失效，请把新明文更新到调用方",
	})
}

// RevokeApiToken 撤销：不删记录，保留「这个令牌曾经存在且用过」的痕迹
func (h *Handler) RevokeApiToken(c *gin.Context) {
	var item model.ApiToken
	if err := h.DB.First(&item, idParam(c)).Error; err != nil {
		response.NotFound(c, "令牌不存在")
		return
	}
	if item.RevokedAt != nil {
		response.OK(c, toAPITokenView(item))
		return
	}
	now := time.Now()
	err := h.DB.Model(&item).Updates(map[string]any{
		"revoked_at": &now, "revoked_by": middleware.CurrentUser(c).Username, "enabled": false,
	}).Error
	if err != nil {
		response.Error(c, "撤销失败")
		return
	}
	h.DB.First(&item, item.ID)
	response.OK(c, toAPITokenView(item))
}

// DeleteApiToken 删除记录。用过的令牌只允许撤销不允许删除，否则审计里的
// token_id 会变成查不到出处的孤儿。
func (h *Handler) DeleteApiToken(c *gin.Context) {
	var item model.ApiToken
	if err := h.DB.First(&item, idParam(c)).Error; err != nil {
		response.NotFound(c, "令牌不存在")
		return
	}
	if item.UseCount > 0 {
		response.BadRequest(c, "该令牌已经调用过接口，只能撤销不能删除（审计里要能查到出处）")
		return
	}
	if err := h.DB.Delete(&model.ApiToken{}, item.ID).Error; err != nil {
		response.Error(c, "删除失败")
		return
	}
	response.OK(c, gin.H{"id": item.ID})
}

// ListApiTokenScopes 可授予的权限码清单，供界面挑选
func (h *Handler) ListApiTokenScopes(c *gin.Context) {
	var menus []model.Menu
	h.DB.Where("auth_code <> ''").Order("id asc").Find(&menus)

	list := make([]gin.H, 0, len(menus))
	for _, m := range menus {
		list = append(list, gin.H{"code": m.AuthCode, "title": m.Title})
	}
	response.OK(c, gin.H{"list": list, "total": len(list)})
}

// WhoAmI 给调用方自测用：这条凭据是谁、生效权限有哪些、是不是只读。
//
// 接令牌的人最需要的就是这个——否则只能靠一个个接口去试 403。
func (h *Handler) WhoAmI(c *gin.Context) {
	user := middleware.CurrentUser(c)
	perms := middleware.Perms(c)
	codes := make([]string, 0, len(perms))
	for code := range perms {
		codes = append(codes, code)
	}

	body := gin.H{"username": user.Username, "userId": user.ID, "permissions": codes}
	if token := middleware.CurrentAPIToken(c); token != nil {
		body["authBy"] = "token"
		body["token"] = gin.H{
			"id": token.ID, "name": token.Name, "prefix": token.Prefix,
			"readOnly": token.ReadOnly, "expiresAt": token.ExpiresAt,
			"allowIps": token.AllowIPs,
		}
	} else {
		body["authBy"] = "login"
	}
	response.OK(c, body)
}
