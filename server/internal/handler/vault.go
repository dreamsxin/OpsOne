package handler

// 账号密码库。
//
// 和凭证库（credential.go）的分界是这一页所有设计的出发点：
//
//	凭证库  → 平台自己用（SSH 登录主机），明文永不出接口，人也拿不到
//	密码库  → 人自己用（某个后台系统的管理员账号），明文必须能取出来
//
// 后者只要能取明文，「不返回密钥」这条防线就不存在了，安全性只能换一种兜法：
// **取用必留痕，留痕写不进去就不给明文**。所以 revealVaultAccount 里留痕是
// 前置步骤而不是收尾步骤 —— 收尾的写法一旦写库失败，明文已经发出去了。
//
// 另外刻意没做的两件事：
//   - 没做「审批后才能取」。内网自用，审批只会让人把口令抄到本地记事本里，
//     那比留痕可查更糟（SECURITY.md §8 同一条理由）。
//   - 没做浏览器端自动填充 / 复制即焚。前者要装插件，后者是自欺欺人：
//     明文已经到了浏览器，倒计时清空剪贴板不构成任何保护。

import (
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"ops-platform/server/internal/cryptox"
	"ops-platform/server/internal/middleware"
	"ops-platform/server/internal/model"
	"ops-platform/server/internal/response"
)

const (
	vaultTargetAccount = "account"
	vaultTargetTOTP    = "totp"

	vaultAlertSourceName = "密码库巡检"
)

// vaultCategories 分类白名单。只用于归类展示，不影响任何行为，
// 所以给的是固定几项而不是自由文本 —— 自由文本会退化成同一类五种写法。
var vaultCategories = []struct {
	Code  string `json:"code"`
	Label string `json:"label"`
}{
	{"system", "内部系统"},
	{"database", "数据库"},
	{"network", "网络设备"},
	{"thirdparty", "第三方平台"},
	{"other", "其它"},
}

func validVaultCategory(code string) bool {
	for _, item := range vaultCategories {
		if item.Code == code {
			return true
		}
	}
	return false
}

type vaultAccountReq struct {
	Name        string `json:"name" binding:"required"`
	Category    string `json:"category"`
	Platform    string `json:"platform"`
	URL         string `json:"url"`
	Username    string `json:"username" binding:"required"`
	Secret      string `json:"secret"`
	Description string `json:"description"`
	Owner       string `json:"owner"`
	DeptID      uint   `json:"deptId"`
	RotateDays  int    `json:"rotateDays"`
	Enabled     *bool  `json:"enabled"`
}

// vaultRotateOverdue 算这条口令逾期多少天没轮换。
//
// 基准是「最近一次轮换」，从没轮换过的按创建时间算 —— 把「录进来就没动过」
// 当成 0 天会让最该换的那批永远不提醒。返回 0 表示不逾期或没开提醒。
func vaultRotateOverdue(item model.VaultAccount, now time.Time) int {
	if item.RotateDays <= 0 {
		return 0
	}
	base := item.CreatedAt
	if item.RotatedAt != nil {
		base = *item.RotatedAt
	}
	if base.IsZero() {
		return 0
	}
	elapsed := int(now.Sub(base).Hours() / 24)
	if over := elapsed - item.RotateDays; over > 0 {
		return over
	}
	return 0
}

// vaultAccountView 出接口的形状。口令本身与凭证库一样不在里面，
// 连脱敏版都不给：要明文得走 reveal，那条路会留痕。
func vaultAccountView(item model.VaultAccount, now time.Time) gin.H {
	storage := "plain"
	if cryptox.IsSealed(item.Secret) {
		storage = "encrypted"
	}
	return gin.H{
		"id": item.ID, "name": item.Name, "category": item.Category,
		"platform": item.Platform, "url": item.URL, "username": item.Username,
		"description": item.Description, "owner": item.Owner, "deptId": item.DeptID,
		"enabled": item.Enabled, "rotateDays": item.RotateDays, "rotatedAt": item.RotatedAt,
		"lastViewedAt": item.LastViewedAt, "lastViewedBy": item.LastViewedBy,
		"viewCount": item.ViewCount,
		"createdAt": item.CreatedAt, "updatedAt": item.UpdatedAt,
		"storage":      storage,           // encrypted | plain
		"hasSecret":    item.Secret != "", // 只给布尔
		"overdueDays":  vaultRotateOverdue(item, now),
		"neverRotated": item.RotatedAt == nil,
	}
}

// recordVaultAccess 写取用留痕。
//
// 返回 error 是刻意的：调用方必须先确认它成功再返回明文。
func (h *Handler) recordVaultAccess(c *gin.Context, target string, id uint, name, action, reason string) error {
	access := model.VaultAccess{
		Target: target, TargetID: id, TargetName: name, Action: action,
		Operator: operatorName(c), IP: c.ClientIP(),
		Reason: truncate(strings.TrimSpace(reason), 240),
	}
	if user := middleware.CurrentUser(c); user != nil {
		access.OperatorID = user.ID
	}
	return h.DB.Create(&access).Error
}

// ---------- 条目 ----------

func (h *Handler) ListVaultAccounts(c *gin.Context) {
	page, size := pageParams(c)
	query := h.DB.Model(&model.VaultAccount{})
	if kw := strings.TrimSpace(c.Query("keyword")); kw != "" {
		like := "%" + kw + "%"
		query = query.Where("name LIKE ? OR platform LIKE ? OR username LIKE ? OR owner LIKE ?",
			like, like, like, like)
	}
	if cat := strings.TrimSpace(c.Query("category")); cat != "" && validVaultCategory(cat) {
		query = query.Where("category = ?", cat)
	}
	switch c.Query("enabled") {
	case "true":
		query = query.Where("enabled = ?", true)
	case "false":
		query = query.Where("enabled = ?", false)
	}
	overdueOnly := c.Query("overdueOnly") == "true"
	if overdueOnly {
		// 逾期要按「基准时间 + 周期」算，跨方言的 SQL 不好写，索性把开了
		// 轮换周期的行整批取出来在内存里筛完再分页 —— 这张表天然只有几十行，
		// 用 SQL 分页 + 内存过滤会得到一个对不上的 total
		query = query.Where("rotate_days > 0")
		var all []model.VaultAccount
		if err := query.Order("category asc, name asc").Find(&all).Error; err != nil {
			response.Error(c, "查询密码库失败")
			return
		}
		now := time.Now()
		views := make([]gin.H, 0, len(all))
		for _, item := range all {
			if vaultRotateOverdue(item, now) > 0 {
				views = append(views, vaultAccountView(item, now))
			}
		}
		total := int64(len(views))
		start := (page - 1) * size
		if start > len(views) {
			start = len(views)
		}
		end := start + size
		if end > len(views) {
			end = len(views)
		}
		response.OKPage(c, views[start:end], total, page, size)
		return
	}

	var total int64
	query.Count(&total)

	var list []model.VaultAccount
	if err := query.Order("category asc, name asc").
		Offset((page - 1) * size).Limit(size).Find(&list).Error; err != nil {
		response.Error(c, "查询密码库失败")
		return
	}

	now := time.Now()
	views := make([]gin.H, 0, len(list))
	for _, item := range list {
		views = append(views, vaultAccountView(item, now))
	}
	response.OKPage(c, views, total, page, size)
}

func (h *Handler) CreateVaultAccount(c *gin.Context) {
	var req vaultAccountReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "名称与登录用户为必填项")
		return
	}
	if strings.TrimSpace(req.Secret) == "" {
		response.BadRequest(c, "口令不能为空：一条没有口令的记录在这一页里没有用处")
		return
	}
	category := orDefault(req.Category, "other")
	if !validVaultCategory(category) {
		response.BadRequest(c, "分类取值不合法")
		return
	}
	if req.RotateDays < 0 {
		response.BadRequest(c, "轮换周期不能为负数")
		return
	}

	item := model.VaultAccount{
		Name: strings.TrimSpace(req.Name), Category: category,
		Platform: req.Platform, URL: req.URL, Username: strings.TrimSpace(req.Username),
		Secret: h.sealSecret(req.Secret), Description: req.Description,
		Owner: req.Owner, DeptID: req.DeptID, RotateDays: req.RotateDays, Enabled: true,
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
	response.OK(c, vaultAccountView(item, time.Now()))
}

func (h *Handler) UpdateVaultAccount(c *gin.Context) {
	var item model.VaultAccount
	if err := h.DB.First(&item, idParam(c)).Error; err != nil {
		response.NotFound(c, "条目不存在")
		return
	}
	var req vaultAccountReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "参数校验失败")
		return
	}
	category := orDefault(req.Category, item.Category)
	if !validVaultCategory(category) {
		response.BadRequest(c, "分类取值不合法")
		return
	}
	if req.RotateDays < 0 {
		response.BadRequest(c, "轮换周期不能为负数")
		return
	}

	updates := map[string]any{
		"name": strings.TrimSpace(req.Name), "category": category,
		"platform": req.Platform, "url": req.URL, "username": strings.TrimSpace(req.Username),
		"description": req.Description, "owner": req.Owner, "dept_id": req.DeptID,
		"rotate_days": req.RotateDays,
	}
	if req.Enabled != nil {
		updates["enabled"] = *req.Enabled
	}
	// 口令留空 = 不修改。改口令请走轮换接口，那条路会留痕并更新轮换时间
	if strings.TrimSpace(req.Secret) != "" {
		response.BadRequest(c, "编辑接口不接受口令：改口令请走「轮换」，那条路会记录轮换时间与操作人")
		return
	}
	if err := h.DB.Model(&item).Updates(updates).Error; err != nil {
		response.Error(c, "更新失败，名称可能重复")
		return
	}
	h.DB.First(&item, item.ID)
	response.OK(c, vaultAccountView(item, time.Now()))
}

func (h *Handler) DeleteVaultAccount(c *gin.Context) {
	id := idParam(c)
	var item model.VaultAccount
	if err := h.DB.First(&item, id).Error; err != nil {
		response.NotFound(c, "条目不存在")
		return
	}
	// 关联的 2FA 种子不跟着删：种子是另一件东西，静默连带删除会让人以为丢了
	var totpCount int64
	h.DB.Model(&model.VaultTOTP{}).Where("account_id = ?", id).Count(&totpCount)
	if totpCount > 0 {
		response.BadRequest(c, fmt.Sprintf("还有 %d 个 2FA 种子关联到这条账号，请先解除关联", totpCount))
		return
	}
	if err := h.DB.Delete(&model.VaultAccount{}, id).Error; err != nil {
		response.Error(c, "删除失败")
		return
	}
	// 留痕不跟着删，TargetName 里存了名字，删了也读得懂
	response.OK(c, gin.H{"deleted": true, "accessKept": true})
}

// ---------- 取明文 ----------

type vaultRevealReq struct {
	Reason string `json:"reason"`
}

// RevealVaultAccount 取口令明文。
//
// 顺序很要紧：先解密（确认真能给出东西）→ 再留痕（留痕失败就此中止）→ 才返回。
// 反过来写会出现「留痕失败但明文已经发出去」，那样这张留痕表就不可信了。
func (h *Handler) RevealVaultAccount(c *gin.Context) {
	var item model.VaultAccount
	if err := h.DB.First(&item, idParam(c)).Error; err != nil {
		response.NotFound(c, "条目不存在")
		return
	}
	if !item.Enabled {
		response.BadRequest(c, "该条目已停用。停用的口令不给取，避免有人拿着已经废弃的账号去排查")
		return
	}
	var req vaultRevealReq
	_ = c.ShouldBindJSON(&req)

	plain, err := h.openSecret("密码库口令", item.Secret)
	if err != nil {
		response.Error(c, err.Error())
		return
	}
	if plain == "" {
		response.BadRequest(c, "这条记录没有口令")
		return
	}
	if err := h.recordVaultAccess(c, vaultTargetAccount, item.ID, item.Name, "reveal", req.Reason); err != nil {
		response.Error(c, "取用留痕写入失败，已中止：这一页的前提是每次取用都留痕")
		return
	}

	now := time.Now()
	h.DB.Model(&item).Updates(map[string]any{
		"last_viewed_at": &now,
		"last_viewed_by": operatorName(c),
		"view_count":     item.ViewCount + 1,
	})
	response.OK(c, gin.H{
		"username": item.Username, "secret": plain, "url": item.URL,
		"note": "本次取用已记入留痕，谁取的、什么时候取的在页面上可查",
	})
}

type vaultRotateReq struct {
	Secret string `json:"secret" binding:"required"`
	Reason string `json:"reason"`
}

// RotateVaultAccount 换口令。这是唯一能改口令的入口，换完 RotatedAt 就是新的基准。
func (h *Handler) RotateVaultAccount(c *gin.Context) {
	var item model.VaultAccount
	if err := h.DB.First(&item, idParam(c)).Error; err != nil {
		response.NotFound(c, "条目不存在")
		return
	}
	var req vaultRotateReq
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.Secret) == "" {
		response.BadRequest(c, "新口令不能为空")
		return
	}
	if err := h.recordVaultAccess(c, vaultTargetAccount, item.ID, item.Name, "rotate", req.Reason); err != nil {
		response.Error(c, "取用留痕写入失败，已中止")
		return
	}
	now := time.Now()
	if err := h.DB.Model(&item).Updates(map[string]any{
		"secret":     h.sealSecret(req.Secret),
		"rotated_at": &now,
	}).Error; err != nil {
		response.Error(c, "轮换失败")
		return
	}
	h.DB.First(&item, item.ID)
	response.OK(c, vaultAccountView(item, now))
}

// ---------- 留痕 ----------

func (h *Handler) ListVaultAccesses(c *gin.Context) {
	page, size := pageParams(c)
	query := h.DB.Model(&model.VaultAccess{})
	if target := strings.TrimSpace(c.Query("target")); target != "" {
		query = query.Where("target = ?", target)
	}
	if raw := strings.TrimSpace(c.Query("targetId")); raw != "" {
		if id, err := strconv.Atoi(raw); err == nil && id > 0 {
			query = query.Where("target_id = ?", id)
		}
	}
	if action := strings.TrimSpace(c.Query("action")); action != "" {
		query = query.Where("action = ?", action)
	}
	if op := strings.TrimSpace(c.Query("operator")); op != "" {
		query = query.Where("operator LIKE ?", "%"+op+"%")
	}

	var total int64
	query.Count(&total)

	var list []model.VaultAccess
	if err := query.Order("id desc").Offset((page - 1) * size).Limit(size).
		Find(&list).Error; err != nil {
		response.Error(c, "查询取用留痕失败")
		return
	}
	response.OKPage(c, list, total, page, size)
}

// GetVaultState 这一页的事实条：把口令到底加没加密、有多少条逾期没换、
// 最近一周谁取过，直接摆在页面上。
func (h *Handler) GetVaultState(c *gin.Context) {
	var accounts []model.VaultAccount
	if err := h.DB.Find(&accounts).Error; err != nil {
		response.Error(c, "统计失败")
		return
	}

	now := time.Now()
	var sealed, plain, overdue, noRotatePolicy int
	for _, item := range accounts {
		if cryptox.IsSealed(item.Secret) {
			sealed++
		} else if item.Secret != "" {
			plain++
		}
		if item.RotateDays <= 0 {
			noRotatePolicy++
		} else if vaultRotateOverdue(item, now) > 0 {
			overdue++
		}
	}

	var totpTotal, totpSealed int64
	h.DB.Model(&model.VaultTOTP{}).Count(&totpTotal)
	h.DB.Model(&model.VaultTOTP{}).Where("secret LIKE ?", "enc:v1:%").Count(&totpSealed)

	var access7d, reveal7d int64
	since := now.Add(-7 * 24 * time.Hour)
	h.DB.Model(&model.VaultAccess{}).Where("created_at >= ?", since).Count(&access7d)
	h.DB.Model(&model.VaultAccess{}).Where("created_at >= ? AND action IN ?", since,
		[]string{"reveal", "code", "uri"}).Count(&reveal7d)

	runAt, runInfo := h.lastFixedRun("vault")
	state := gin.H{
		"encryptEnabled": h.Crypto.Enabled(),
		"total":          len(accounts),
		"sealed":         sealed,
		"plain":          plain,
		"overdue":        overdue,
		"noRotatePolicy": noRotatePolicy,
		"totpTotal":      totpTotal,
		"totpSealed":     totpSealed,
		"access7d":       access7d,
		"reveal7d":       reveal7d,
		"categories":     vaultCategories,
		"notes": []string{
			"口令明文只能通过「取用」按钮拿到，每次取用都会留痕；留痕写库失败时接口直接报错，不会把明文发出去",
			"编辑不能改口令，改口令只能走「轮换」——否则轮换时间就不是真的",
			"「逾期」按最近一次轮换算，从没轮换过的按录入时间算",
			"未配 OPS_SECRET_KEY 时口令明文落库，页面上的「存储」列会显示明文",
			"2FA 验证码用服务器时间计算，服务器时钟偏了算出来的码就是错的，出码接口会一并返回服务器时间",
		},
	}
	if !runAt.IsZero() {
		state["lastCheckAt"] = runAt
		state["lastCheckInfo"] = runInfo
	}
	response.OK(c, state)
}

// ---------- 轮换逾期提醒 ----------

// RemindVaultRotationForSchedule 扫一遍开了轮换周期的条目，逾期的进告警通道。
//
// 指纹按条目固定，重复扫描只累加次数；换过口令之后会收到 resolved。
// 没开轮换周期的条目不参与 —— 平台不知道你的口令该多久换一次，
// 替你假设一个 90 天然后天天报警是在制造噪音。
func (h *Handler) RemindVaultRotationForSchedule() {
	var list []model.VaultAccount
	if err := h.DB.Where("rotate_days > 0 AND enabled = ?", true).Find(&list).Error; err != nil {
		log.Printf("[vault] 轮换检查失败: %v", err)
		return
	}
	source, err := h.internalAlertSource(vaultAlertSourceName)
	if err != nil {
		log.Printf("[vault] 内部告警源不可用，跳过提醒: %v", err)
		return
	}

	now := time.Now()
	overdue := 0
	for _, item := range list {
		fingerprint := internalAlertFingerprint(fmt.Sprintf("vault|%d|rotate", item.ID))
		labels := map[string]string{
			"module":   "vault",
			"vaultId":  strconv.FormatUint(uint64(item.ID), 10),
			"platform": item.Platform,
			"owner":    item.Owner,
		}
		days := vaultRotateOverdue(item, now)
		if days <= 0 {
			h.ingestAlert(source, alertPayload{
				Title: "密码库口令已按期轮换", Status: "resolved",
				Fingerprint: fingerprint, Labels: labels,
			})
			continue
		}
		overdue++
		severity := "warning"
		if days > item.RotateDays {
			// 逾期时间已经超过一个完整周期，说明这条口令基本处于失管状态
			severity = "critical"
		}
		base := "录入"
		if item.RotatedAt != nil {
			base = "上次轮换"
		}
		h.ingestAlert(source, alertPayload{
			Title: fmt.Sprintf("密码库口令逾期未轮换: %s", item.Name),
			Summary: fmt.Sprintf("要求每 %d 天轮换一次，距%s已 %d 天，逾期 %d 天。责任人: %s",
				item.RotateDays, base, item.RotateDays+days, days, domainOrNotSet(item.Owner)),
			Severity: severity, Value: strconv.Itoa(days),
			Fingerprint: fingerprint, Labels: labels,
		})
	}

	h.markFixedRun("vault", fmt.Sprintf("检查 %d 条，逾期 %d 条", len(list), overdue))
	if overdue > 0 {
		log.Printf("[vault] 轮换检查完成: %d 条逾期", overdue)
	}
}
