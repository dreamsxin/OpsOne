package handler

import (
	"context"
	"fmt"
	"strings"
	"time"


	"github.com/gin-gonic/gin"

	"ops-platform/server/internal/cryptox"
	"ops-platform/server/internal/middleware"
	"ops-platform/server/internal/model"
	"ops-platform/server/internal/response"
	"ops-platform/server/internal/sshx"
)

// 凭证库：一份口令 / 私钥存一处，多台主机引用。
//
// 这一页解决的是很具体的一件事：同一个运维账号的口令散落在几十台主机记录里，
// 换一次口令要逐台改，改漏的那台就静默失效，而且没人知道「这台机器用的是哪份凭据」。
//
// 设计上的几个硬规则：
//   - 凭据密钥永不出接口（连脱敏版本都不给），界面上只展示指纹与「加密/明文」状态
//   - 主机切到共享凭据时清空本机那份口令，不留副本
//   - 凭据被禁用 / 删除 / 解不开密时，引用它的主机一律连不上并如实报错，绝不回退
//   - 删除凭据前必须先解除引用，不做「删了再说」
//   - 轮换只改凭据本身，引用它的主机下一次连接自动生效 —— 这是这个模块的全部意义，
//     所以轮换接口会明确回答「这次影响了几台主机」

const credentialCheckTimeout = 20 * time.Second

type credentialReq struct {
	Name        string `json:"name" binding:"required"`
	Type        string `json:"type"`     // password | key
	Username    string `json:"username" binding:"required"`
	Secret      string `json:"secret"`   // 口令或私钥 PEM，更新时留空表示不变
	Passphrase  string `json:"passphrase"`
	Description string `json:"description"`
	Owner       string `json:"owner"`
	DeptID      uint   `json:"deptId"`
	Enabled     *bool  `json:"enabled"`
}

// credentialSecret 解密后的凭据，只在进程内传递，不出接口
type credentialSecret struct {
	Username   string
	Type       string
	Secret     string
	Passphrase string
}

// loadCredentialSecret 取出并解密一份凭据。任何一步不成立都返回错误：
// 调用方（buildTarget）会把它变成连接失败的原因，而不是拿着空口令去试。
func (h *Handler) loadCredentialSecret(id uint) (credentialSecret, error) {
	var cred model.Credential
	if err := h.DB.First(&cred, id).Error; err != nil {
		return credentialSecret{}, fmt.Errorf("凭据 #%d 不存在（可能已被删除）", id)
	}
	if !cred.Enabled {
		return credentialSecret{}, fmt.Errorf("凭据「%s」已被禁用", cred.Name)
	}
	secret, err := h.Crypto.Open(cred.Secret)
	if err != nil {
		return credentialSecret{}, fmt.Errorf("凭据「%s」解密失败: %w", cred.Name, err)
	}
	pass, err := h.Crypto.Open(cred.Passphrase)
	if err != nil {
		return credentialSecret{}, fmt.Errorf("凭据「%s」的私钥口令解密失败: %w", cred.Name, err)
	}
	return credentialSecret{Username: cred.Username, Type: cred.Type, Secret: secret, Passphrase: pass}, nil
}

// assertCredentialUsable 主机要引用这份凭据之前的检查：存在、启用、能解开。
// 放在写入前而不是连接时，是为了别把「配错了」推迟到某次批量执行才暴露。
func (h *Handler) assertCredentialUsable(id uint) error {
	if _, err := h.loadCredentialSecret(id); err != nil {
		return err
	}
	return nil
}

// credentialView 出接口的形状。密钥本身一律不出现，只说清「它是什么、存得安全吗」
func (h *Handler) credentialView(cred model.Credential, refCount int64) gin.H {
	storage := "plain"
	if cryptox.IsSealed(cred.Secret) {
		storage = "encrypted"
	}
	return gin.H{
		"id": cred.ID, "name": cred.Name, "type": cred.Type, "username": cred.Username,
		"fingerprint": cred.Fingerprint, "description": cred.Description, "owner": cred.Owner,
		"deptId": cred.DeptID, "enabled": cred.Enabled,
		"rotatedAt": cred.RotatedAt, "lastUsedAt": cred.LastUsedAt, "lastUsedHostId": cred.LastUsedHostID,
		"createdAt": cred.CreatedAt, "updatedAt": cred.UpdatedAt,
		// storage 是给人看的事实：encrypted = 库里是 AES-GCM 密文，plain = 明文
		"storage":       storage,
		"hasPassphrase": cred.Passphrase != "",
		"hostCount":     refCount,
	}
}

// ListCredentials 凭据清单。顺带给出每份凭据被多少台主机引用 ——
// 「这份凭据还有人用吗」是删除与轮换前最需要先知道的事
func (h *Handler) ListCredentials(c *gin.Context) {
	page, size := pageParams(c)
	q := h.DB.Model(&model.Credential{})
	if kw := strings.TrimSpace(c.Query("keyword")); kw != "" {
		like := "%" + kw + "%"
		q = q.Where("name LIKE ? OR username LIKE ? OR owner LIKE ? OR description LIKE ?", like, like, like, like)
	}
	if t := c.Query("type"); t == "password" || t == "key" {
		q = q.Where("type = ?", t)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		response.Error(c, "查询凭据失败")
		return
	}
	var list []model.Credential
	if err := q.Order("id desc").Offset((page - 1) * size).Limit(size).Find(&list).Error; err != nil {
		response.Error(c, "查询凭据失败")
		return
	}

	refs := h.credentialRefCounts()
	views := make([]gin.H, 0, len(list))
	for _, cred := range list {
		views = append(views, h.credentialView(cred, refs[cred.ID]))
	}
	response.OKPage(c, views, total, page, size)
}

// credentialRefCounts 一次查出所有凭据的引用数，避免每行一次 count
func (h *Handler) credentialRefCounts() map[uint]int64 {
	type row struct {
		CredentialID uint
		Cnt          int64
	}
	var rows []row
	h.DB.Model(&model.Host{}).
		Select("credential_id, count(*) as cnt").
		Where("credential_id > 0").
		Group("credential_id").Scan(&rows)

	out := make(map[uint]int64, len(rows))
	for _, r := range rows {
		out[r.CredentialID] = r.Cnt
	}
	return out
}

// GetCredentialState 凭证库的整体事实：加密是否启用、有多少凭据、多少主机还在用自带凭据。
// 「有凭证库」不等于「凭据加密了」，这条必须摆在页面最上面，不能让人自己猜
func (h *Handler) GetCredentialState(c *gin.Context) {
	var total, sealed, hostsShared, hostsLocal int64
	h.DB.Model(&model.Credential{}).Count(&total)
	h.DB.Model(&model.Credential{}).Where("secret LIKE ?", "enc:v1:%").Count(&sealed)
	h.DB.Model(&model.Host{}).Where("credential_id > 0").Count(&hostsShared)
	h.DB.Model(&model.Host{}).Where("credential_id = 0").Count(&hostsLocal)

	response.OK(c, gin.H{
		"encryptEnabled": h.Crypto.Enabled(),
		"total":          total,
		"sealed":         sealed,
		"plain":          total - sealed,
		"hostsShared":    hostsShared,
		"hostsLocal":     hostsLocal,
		// 明文存储不是 bug 而是没配密钥，提示要说清怎么改
		"note": credentialStorageNote(h.Crypto.Enabled(), total-sealed),
	})
}

func credentialStorageNote(enabled bool, plainCount int64) string {
	if !enabled {
		return "未配置 OPS_SECRET_KEY：凭据以明文存在数据库里。设置该环境变量后新增与轮换的凭据会用 AES-GCM 加密，" +
			"已有的明文凭据要轮换一次才会变成密文"
	}
	if plainCount > 0 {
		return fmt.Sprintf("加密已启用，但仍有 %d 份凭据是早先的明文记录：轮换一次即会改写为密文", plainCount)
	}
	return "加密已启用，凭据以 AES-GCM 密文存储。注意：换掉 OPS_SECRET_KEY 会导致现有凭据全部无法解密"
}

// CreateCredential 新增凭据。私钥类型会当场解析一次并算出指纹：
// 解析不了的私钥根本不该落库，否则要等到某次批量执行才发现
func (h *Handler) CreateCredential(c *gin.Context) {
	var req credentialReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "凭据名称与登录用户为必填项")
		return
	}
	if strings.TrimSpace(req.Secret) == "" {
		response.BadRequest(c, "请提供口令或私钥")
		return
	}

	cred := model.Credential{
		Name: strings.TrimSpace(req.Name), Type: defaultAuth(req.Type),
		Username: strings.TrimSpace(req.Username),
		Description: req.Description, Owner: strings.TrimSpace(req.Owner),
		DeptID: req.DeptID, Enabled: true,
		CreatedBy: middleware.CurrentUser(c).ID,
	}
	if req.Enabled != nil {
		cred.Enabled = *req.Enabled
	}

	fp, err := credentialFingerprint(cred.Type, req.Secret, req.Passphrase)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	cred.Fingerprint = fp
	cred.Secret = h.Crypto.Seal(req.Secret)
	cred.Passphrase = h.Crypto.Seal(req.Passphrase)

	if err := h.DB.Create(&cred).Error; err != nil {
		response.BadRequest(c, "凭据创建失败，名称可能已存在")
		return
	}
	response.OK(c, h.credentialView(cred, 0))
}

// UpdateCredential 改凭据的描述性信息。密钥留空表示不变；
// 要换密钥走 /rotate —— 换密钥是会立刻影响一批主机的动作，不该和改备注混在一个按钮里
func (h *Handler) UpdateCredential(c *gin.Context) {
	var cred model.Credential
	if err := h.DB.First(&cred, idParam(c)).Error; err != nil {
		response.NotFound(c, "凭据不存在")
		return
	}
	var req credentialReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "参数校验失败")
		return
	}

	cred.Name = strings.TrimSpace(req.Name)
	cred.Username = strings.TrimSpace(req.Username)
	cred.Description, cred.Owner, cred.DeptID = req.Description, strings.TrimSpace(req.Owner), req.DeptID
	if req.Enabled != nil {
		cred.Enabled = *req.Enabled
	}
	// 类型允许改，但改成 key 就必须同时给私钥（否则会留着一份口令当私钥用）
	newType := defaultAuth(req.Type)
	if newType != cred.Type && strings.TrimSpace(req.Secret) == "" {
		response.BadRequest(c, "切换认证方式必须同时提供新的口令或私钥")
		return
	}
	cred.Type = newType

	if strings.TrimSpace(req.Secret) != "" {
		fp, err := credentialFingerprint(cred.Type, req.Secret, req.Passphrase)
		if err != nil {
			response.BadRequest(c, err.Error())
			return
		}
		cred.Fingerprint = fp
		cred.Secret = h.Crypto.Seal(req.Secret)
		cred.Passphrase = h.Crypto.Seal(req.Passphrase)
		now := time.Now()
		cred.RotatedAt = &now
	}

	if err := h.DB.Save(&cred).Error; err != nil {
		response.BadRequest(c, "凭据更新失败，名称可能已存在")
		return
	}
	response.OK(c, h.credentialView(cred, h.credentialRefCounts()[cred.ID]))
}

// RotateCredential 换密钥。凭证库存在的意义就在这个接口：
// 改一处，所有引用它的主机下一次连接自动用新密钥 —— 所以要明确回答影响了几台
func (h *Handler) RotateCredential(c *gin.Context) {
	var cred model.Credential
	if err := h.DB.First(&cred, idParam(c)).Error; err != nil {
		response.NotFound(c, "凭据不存在")
		return
	}
	var req struct {
		Secret     string `json:"secret" binding:"required"`
		Passphrase string `json:"passphrase"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.Secret) == "" {
		response.BadRequest(c, "请提供新的口令或私钥")
		return
	}

	fp, err := credentialFingerprint(cred.Type, req.Secret, req.Passphrase)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	oldFp := cred.Fingerprint

	now := time.Now()
	cred.Fingerprint = fp
	cred.Secret = h.Crypto.Seal(req.Secret)
	cred.Passphrase = h.Crypto.Seal(req.Passphrase)
	cred.RotatedAt = &now
	if err := h.DB.Save(&cred).Error; err != nil {
		response.Error(c, "凭据轮换失败")
		return
	}

	affected := h.credentialRefCounts()[cred.ID]
	response.OK(c, gin.H{
		"affectedHosts": affected,
		"oldFingerprint": oldFp, "fingerprint": fp,
		// 这里刻意不自动去测每台主机：几十台机器串行连一遍会把请求拖到几分钟，
		// 而且真要验也应该由人挑一台先试
		"note": fmt.Sprintf("已换成新密钥，%d 台引用主机的下一次连接生效。建议挑一台点「测试连接」确认新密钥真的能登进去", affected),
	})
}

// DeleteCredential 删除凭据。有主机还在引用就拒绝：
// 删掉会让那些主机全部连不上，而且从主机那边完全看不出为什么
func (h *Handler) DeleteCredential(c *gin.Context) {
	id := idParam(c)
	var cred model.Credential
	if err := h.DB.First(&cred, id).Error; err != nil {
		response.NotFound(c, "凭据不存在")
		return
	}
	if n := h.credentialRefCounts()[id]; n > 0 {
		response.BadRequest(c, fmt.Sprintf("还有 %d 台主机在引用这份凭据，请先把它们改成自带凭据或换成别的凭据", n))
		return
	}
	if err := h.DB.Delete(&model.Credential{}, id).Error; err != nil {
		response.Error(c, "凭据删除失败")
		return
	}
	response.OK(c, nil)
}

// ListCredentialHosts 引用方：哪些主机在用这份凭据。
// 回答「改它会影响谁」，也是解除引用的入口
func (h *Handler) ListCredentialHosts(c *gin.Context) {
	id := idParam(c)
	var hosts []model.Host
	h.DB.Where("credential_id = ?", id).Order("id desc").Find(&hosts)

	rows := make([]gin.H, 0, len(hosts))
	for _, host := range hosts {
		rows = append(rows, gin.H{
			"id": host.ID, "name": host.Name, "address": host.Address, "port": host.Port,
			"env": host.Env, "status": host.Status, "checkedAt": host.CheckedAt,
		})
	}
	response.OK(c, rows)
}

// BindCredentialHosts 把一批主机改成引用这份凭据。
//
// 这是真改数据的动作：主机原来的用户名会被凭据里的用户名接管，本机口令被清空。
// 清空是刻意的 —— 留着一份不会被轮换的旧口令，等于给这台机器留了个后门。
func (h *Handler) BindCredentialHosts(c *gin.Context) {
	id := idParam(c)
	if err := h.assertCredentialUsable(id); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	var req struct {
		HostIDs []uint `json:"hostIds"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || len(req.HostIDs) == 0 {
		response.BadRequest(c, "请选择要接入的主机")
		return
	}

	user := middleware.CurrentUser(c)
	var bound, skipped int
	var skippedNames []string
	for _, hostID := range req.HostIDs {
		var host model.Host
		if err := h.DB.First(&host, hostID).Error; err != nil {
			skipped++
			continue
		}
		// 看不见 / 没有管理权限的主机不能被顺手改掉凭据
		if !h.hostVisibleWithGrants(user, &host) || !h.hostActionAllowed(user, &host, model.ActionManage) {
			skipped++
			skippedNames = append(skippedNames, host.Name)
			continue
		}
		if host.CredentialID == id {
			continue
		}
		h.DB.Model(&model.Host{}).Where("id = ?", host.ID).
			Updates(map[string]any{"credential_id": id, "secret": ""})
		bound++
	}

	response.OK(c, gin.H{
		"bound": bound, "skipped": skipped, "skippedHosts": skippedNames,
		"note": "已改为引用共享凭据，各主机原来的本机口令已清空；登录用户名以凭据为准",
	})
}

// CheckCredential 用这份凭据真连一台主机。
//
// 可以在绑定之前先测（hostId 传任意一台可见主机），顺序上就是「先测通再铺」。
// 成功才更新 LastUsedAt —— 失败也记时间会让「最近使用」变得毫无意义。
func (h *Handler) CheckCredential(c *gin.Context) {
	id := idParam(c)
	var cred model.Credential
	if err := h.DB.First(&cred, id).Error; err != nil {
		response.NotFound(c, "凭据不存在")
		return
	}
	var req struct {
		HostID uint `json:"hostId"`
	}
	_ = c.ShouldBindJSON(&req)
	if req.HostID == 0 {
		response.BadRequest(c, "请选择一台用于测试的主机")
		return
	}

	var host model.Host
	if err := h.DB.First(&host, req.HostID).Error; err != nil {
		response.NotFound(c, "主机不存在")
		return
	}
	if !h.hostVisibleWithGrants(middleware.CurrentUser(c), &host) {
		response.NotFound(c, "主机不存在")
		return
	}

	secret, err := h.loadCredentialSecret(id)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	// 用这台主机的地址与跳板链，但认证信息强制换成被测凭据：
	// 这样「还没绑定就先测」和「已绑定后复测」走的是同一条路径
	target := h.target(&host)
	target.PrepareError = ""
	target.Username, target.AuthType = secret.Username, secret.Type
	target.Secret, target.Passphrase = secret.Secret, secret.Passphrase

	ctx, cancel := context.WithTimeout(c.Request.Context(), credentialCheckTimeout)
	defer cancel()
	res := sshx.Run(ctx, target, "id -un 2>/dev/null || whoami")

	ok := res.Status == "success"
	if ok {
		now := time.Now()
		h.DB.Model(&model.Credential{}).Where("id = ?", id).
			Updates(map[string]any{"last_used_at": now, "last_used_host_id": host.ID})
	}
	response.OK(c, gin.H{
		"ok": ok, "hostName": host.Name, "address": host.Address,
		"loginUser": strings.TrimSpace(res.Stdout),
		"detail":    strings.TrimSpace(res.Stderr),
		"costMs":    res.CostMs,
		"viaProxy":  h.proxyLabel(&host),
		// 真连上之后 id -un 回来的才是「实际登录成了谁」，与凭据里写的用户名对不上要能看出来
		"usernameMatch": ok && strings.TrimSpace(res.Stdout) == secret.Username,
		"credUsername":  secret.Username,
	})
}

// credentialFingerprint 私钥类型返回公钥指纹，口令类型返回空。
// 顺带把「这份私钥能不能用」在落库前就验了
func credentialFingerprint(credType, secret, passphrase string) (string, error) {
	if credType != "key" {
		return "", nil
	}
	fp, err := sshx.PublicKeyFingerprint(secret, passphrase)
	if err != nil {
		return "", fmt.Errorf("私钥不可用: %v", err)
	}
	return fp, nil
}

