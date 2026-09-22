package handler

// 2FA 验证码库：共享账号的 TOTP 种子托管。
//
// 要解决的是一个很具体的麻烦：某个后台账号开了两步验证，验证器绑在一个人的
// 手机上，这个人休假或者离职，其他人就登不进去。种子存平台，谁需要谁现算一个码。
//
// 三个必须照实说的点：
//  1. 算码用的是**服务器时间**。服务器时钟偏 30 秒以上，算出来的码全是错的，
//     而表现是「验证码总是不对」，非常容易被误判成种子存错了。所以出码接口
//     一并返回服务器时间，界面上摆出来。
//  2. 种子存进来时就用 totp.DecodeSecret 验一遍。不验的话，乱码要等到
//     真的要登录时才暴露 —— 那通常正是最急的时候。
//  3. 种子等于第二因子本身。托管在平台上，2FA 对这些账号就退化成「知道
//     密码库口令的人都能登」，这句话写在页面上，不能让它悄悄发生。

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"ops-platform/server/internal/cryptox"
	"ops-platform/server/internal/middleware"
	"ops-platform/server/internal/model"
	"ops-platform/server/internal/response"
	"ops-platform/server/internal/totp"
)

type vaultTOTPReq struct {
	Name string `json:"name" binding:"required"`
	// Secret 可以是裸 base32 种子，也可以直接粘 otpauth:// 地址
	Secret      string `json:"secret"`
	Issuer      string `json:"issuer"`
	Account     string `json:"account"`
	AccountID   uint   `json:"accountId"`
	Description string `json:"description"`
	Owner       string `json:"owner"`
	Enabled     *bool  `json:"enabled"`
}

// parsedSeed 一份种子解析结果
type parsedSeed struct {
	Secret  string
	Issuer  string
	Account string
}

// parseTOTPSeed 解析用户粘进来的东西。
//
// 两种形态都接：从二维码里导出来的 otpauth:// 地址（顺带能拿到 issuer/account），
// 和手抄的 base32 种子。otpauth 地址里如果带了非默认的 digits/period/algorithm，
// 直接拒绝而不是忽略 —— 忽略的话算出来的码永远对不上，且没人能想到是这个原因。
func parseTOTPSeed(input string) (parsedSeed, error) {
	raw := strings.TrimSpace(input)
	if raw == "" {
		return parsedSeed{}, fmt.Errorf("种子不能为空")
	}

	if strings.HasPrefix(strings.ToLower(raw), "otpauth://") {
		u, err := url.Parse(raw)
		if err != nil {
			return parsedSeed{}, fmt.Errorf("otpauth 地址解析失败: %w", err)
		}
		if !strings.EqualFold(u.Host, "totp") {
			return parsedSeed{}, fmt.Errorf("只支持 otpauth://totp/，收到的是 otpauth://%s（基于计数器的 HOTP 不适合托管）", u.Host)
		}
		q := u.Query()
		if alg := q.Get("algorithm"); alg != "" && !strings.EqualFold(alg, "SHA1") {
			return parsedSeed{}, fmt.Errorf("该种子用的是 %s 算法，平台只实现了 SHA1（通用验证器的默认值）", alg)
		}
		if d := q.Get("digits"); d != "" && d != fmt.Sprint(totp.Digits) {
			return parsedSeed{}, fmt.Errorf("该种子是 %s 位验证码，平台只实现了 %d 位", d, totp.Digits)
		}
		if p := q.Get("period"); p != "" && p != fmt.Sprint(totp.Period) {
			return parsedSeed{}, fmt.Errorf("该种子步长是 %s 秒，平台只实现了 %d 秒", p, totp.Period)
		}

		seed := parsedSeed{Secret: q.Get("secret"), Issuer: q.Get("issuer")}
		// 标签形如 /Issuer:account 或 /account
		label := strings.TrimPrefix(u.Path, "/")
		if decoded, err := url.PathUnescape(label); err == nil {
			label = decoded
		}
		if idx := strings.Index(label, ":"); idx >= 0 {
			if seed.Issuer == "" {
				seed.Issuer = label[:idx]
			}
			seed.Account = strings.TrimSpace(label[idx+1:])
		} else {
			seed.Account = label
		}
		if _, err := totp.DecodeSecret(seed.Secret); err != nil {
			return parsedSeed{}, fmt.Errorf("otpauth 地址里的 secret 不合法: %w", err)
		}
		return seed, nil
	}

	if _, err := totp.DecodeSecret(raw); err != nil {
		return parsedSeed{}, err
	}
	// 统一成大写无空格，抄下来的种子常带空格分组
	normalized := strings.ToUpper(strings.NewReplacer(" ", "", "-", "", "=", "").Replace(raw))
	return parsedSeed{Secret: normalized}, nil
}

func vaultTOTPView(item model.VaultTOTP, accountName string) gin.H {
	storage := "plain"
	if cryptox.IsSealed(item.Secret) {
		storage = "encrypted"
	}
	return gin.H{
		"id": item.ID, "name": item.Name, "issuer": item.Issuer, "account": item.Account,
		"accountId": item.AccountID, "accountName": accountName,
		"description": item.Description, "owner": item.Owner, "enabled": item.Enabled,
		"lastViewedAt": item.LastViewedAt, "lastViewedBy": item.LastViewedBy,
		"viewCount": item.ViewCount, "createdAt": item.CreatedAt, "updatedAt": item.UpdatedAt,
		"storage": storage,
	}
}

// accountNames 批量取密码库条目名，给列表页显示关联关系用
func (h *Handler) accountNames(ids []uint) map[uint]string {
	names := map[uint]string{}
	if len(ids) == 0 {
		return names
	}
	var rows []model.VaultAccount
	h.DB.Select("id", "name").Where("id IN ?", ids).Find(&rows)
	for _, row := range rows {
		names[row.ID] = row.Name
	}
	return names
}

func (h *Handler) ListVaultTOTPs(c *gin.Context) {
	page, size := pageParams(c)
	query := h.DB.Model(&model.VaultTOTP{})
	if kw := strings.TrimSpace(c.Query("keyword")); kw != "" {
		like := "%" + kw + "%"
		query = query.Where("name LIKE ? OR issuer LIKE ? OR account LIKE ? OR owner LIKE ?",
			like, like, like, like)
	}

	var total int64
	query.Count(&total)

	var list []model.VaultTOTP
	if err := query.Order("name asc").Offset((page - 1) * size).Limit(size).
		Find(&list).Error; err != nil {
		response.Error(c, "查询 2FA 验证码库失败")
		return
	}

	ids := make([]uint, 0, len(list))
	for _, item := range list {
		if item.AccountID != 0 {
			ids = append(ids, item.AccountID)
		}
	}
	names := h.accountNames(ids)

	views := make([]gin.H, 0, len(list))
	for _, item := range list {
		views = append(views, vaultTOTPView(item, names[item.AccountID]))
	}
	response.OKPage(c, views, total, page, size)
}

func (h *Handler) CreateVaultTOTP(c *gin.Context) {
	var req vaultTOTPReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "名称为必填项")
		return
	}
	seed, err := parseTOTPSeed(req.Secret)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	if req.AccountID != 0 {
		var count int64
		h.DB.Model(&model.VaultAccount{}).Where("id = ?", req.AccountID).Count(&count)
		if count == 0 {
			response.BadRequest(c, "关联的密码库条目不存在")
			return
		}
	}

	item := model.VaultTOTP{
		Name: strings.TrimSpace(req.Name),
		// 粘 otpauth 地址时用地址里带的 issuer/account 兜底，手填的优先
		Issuer: orDefault(req.Issuer, seed.Issuer), Account: orDefault(req.Account, seed.Account),
		AccountID: req.AccountID, Secret: h.sealSecret(seed.Secret),
		Description: req.Description, Owner: req.Owner, Enabled: true,
	}
	if req.Enabled != nil {
		item.Enabled = *req.Enabled
	}
	if user := middleware.CurrentUser(c); user != nil {
		item.CreatedBy = user.ID
	}
	if err := h.DB.Create(&item).Error; err != nil {
		response.Error(c, "创建失败，名称可能重复")
		return
	}
	response.OK(c, vaultTOTPView(item, ""))
}

func (h *Handler) UpdateVaultTOTP(c *gin.Context) {
	var item model.VaultTOTP
	if err := h.DB.First(&item, idParam(c)).Error; err != nil {
		response.NotFound(c, "条目不存在")
		return
	}
	var req vaultTOTPReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "参数校验失败")
		return
	}
	if req.AccountID != 0 {
		var count int64
		h.DB.Model(&model.VaultAccount{}).Where("id = ?", req.AccountID).Count(&count)
		if count == 0 {
			response.BadRequest(c, "关联的密码库条目不存在")
			return
		}
	}

	updates := map[string]any{
		"name": strings.TrimSpace(req.Name), "issuer": req.Issuer, "account": req.Account,
		"account_id": req.AccountID, "description": req.Description, "owner": req.Owner,
	}
	if req.Enabled != nil {
		updates["enabled"] = *req.Enabled
	}
	// 种子留空 = 不修改；填了就当作换绑，重新校验一遍
	if strings.TrimSpace(req.Secret) != "" {
		seed, err := parseTOTPSeed(req.Secret)
		if err != nil {
			response.BadRequest(c, err.Error())
			return
		}
		updates["secret"] = h.sealSecret(seed.Secret)
	}
	if err := h.DB.Model(&item).Updates(updates).Error; err != nil {
		response.Error(c, "更新失败，名称可能重复")
		return
	}
	h.DB.First(&item, item.ID)
	response.OK(c, vaultTOTPView(item, ""))
}

func (h *Handler) DeleteVaultTOTP(c *gin.Context) {
	if err := h.DB.Delete(&model.VaultTOTP{}, idParam(c)).Error; err != nil {
		response.Error(c, "删除失败")
		return
	}
	response.OK(c, gin.H{"deleted": true, "accessKept": true})
}

// CodeVaultTOTP 现算一个验证码。
//
// 和取口令一样：先算出来 → 再留痕（失败就中止）→ 才返回。
// remainSeconds 是这个码还剩多少秒有效，不足 5 秒时一并给出下一个码 ——
// 否则人复制粘贴的过程里码就过期了，会以为种子是错的。
func (h *Handler) CodeVaultTOTP(c *gin.Context) {
	var item model.VaultTOTP
	if err := h.DB.First(&item, idParam(c)).Error; err != nil {
		response.NotFound(c, "条目不存在")
		return
	}
	if !item.Enabled {
		response.BadRequest(c, "该条目已停用")
		return
	}
	var req vaultRevealReq
	_ = c.ShouldBindJSON(&req)

	secret, err := h.openSecret("2FA 种子", item.Secret)
	if err != nil {
		response.Error(c, err.Error())
		return
	}
	now := time.Now()
	code, err := totp.Code(secret, now)
	if err != nil {
		response.Error(c, fmt.Sprintf("算码失败: %v", err))
		return
	}
	if err := h.recordVaultAccess(c, vaultTargetTOTP, item.ID, item.Name, "code", req.Reason); err != nil {
		response.Error(c, "取用留痕写入失败，已中止：这一页的前提是每次取用都留痕")
		return
	}

	remain := totp.Period - int(now.Unix()%int64(totp.Period))
	payload := gin.H{
		"code": code, "remainSeconds": remain, "period": totp.Period,
		"serverTime": now.Format("2006-01-02 15:04:05"),
		"note":       "验证码按服务器时间计算，服务器时钟偏差超过 30 秒会导致验证码全部无效",
	}
	if remain <= 5 {
		next, err := totp.Code(secret, now.Add(time.Duration(remain)*time.Second))
		if err == nil {
			payload["nextCode"] = next
		}
	}

	h.DB.Model(&item).Updates(map[string]any{
		"last_viewed_at": &now,
		"last_viewed_by": operatorName(c),
		"view_count":     item.ViewCount + 1,
	})
	response.OK(c, payload)
}

// URIVaultTOTP 导出 otpauth 地址，用来把种子重新绑到某个人的验证器上。
//
// 这等于把第二因子原件交出去，所以和取明文同级留痕（action=uri）。
func (h *Handler) URIVaultTOTP(c *gin.Context) {
	var item model.VaultTOTP
	if err := h.DB.First(&item, idParam(c)).Error; err != nil {
		response.NotFound(c, "条目不存在")
		return
	}
	var req vaultRevealReq
	_ = c.ShouldBindJSON(&req)

	secret, err := h.openSecret("2FA 种子", item.Secret)
	if err != nil {
		response.Error(c, err.Error())
		return
	}
	account := orDefault(item.Account, item.Name)
	if err := h.recordVaultAccess(c, vaultTargetTOTP, item.ID, item.Name, "uri", req.Reason); err != nil {
		response.Error(c, "取用留痕写入失败，已中止")
		return
	}

	now := time.Now()
	h.DB.Model(&item).Updates(map[string]any{
		"last_viewed_at": &now,
		"last_viewed_by": operatorName(c),
		"view_count":     item.ViewCount + 1,
	})
	response.OK(c, gin.H{
		"uri":  totp.ProvisioningURI(secret, account, item.Issuer),
		"note": "这串地址里含种子原文，等同于第二因子本身，本次导出已记入留痕",
	})
}
