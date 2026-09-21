package handler

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"ops-platform/server/internal/middleware"
	"ops-platform/server/internal/model"
	"ops-platform/server/internal/response"
	"ops-platform/server/internal/sshx"
)

type hostReq struct {
	Name        string `json:"name" binding:"required"`
	Address     string `json:"address" binding:"required"`
	Port        int    `json:"port"`
	Username    string `json:"username" binding:"required"`
	AuthType    string `json:"authType"`
	Secret      string `json:"secret"` // 密码或私钥，更新时留空表示不变
	Env         string `json:"env"`
	Tags        string `json:"tags"`
	Remark      string `json:"remark"`
	ProxyHostID uint   `json:"proxyHostId"` // 跳板机，0 表示直连
	DeptID      uint   `json:"deptId"`      // 归属部门，参与数据权限过滤
	// CredentialID 引用凭证库里的共享凭据，0 表示用本机自填的凭据。
	// 非 0 时 username / authType / secret 都以凭据为准，本机那份会被清空
	CredentialID uint `json:"credentialId"`
}

// loadHostScoped 按数据权限（含资源授权）加载主机，不校验具体动作
func (h *Handler) loadHostScoped(c *gin.Context) (*model.Host, bool) {
	return h.loadHostForAction(c, "")
}

// loadHostForAction 加载主机并校验动作权限。
//
// 不可见一律按「不存在」处理，不泄露存在性；可见但动作未被授权时返回 403，
// 让操作人知道是权限不足而不是资源不存在。
func (h *Handler) loadHostForAction(c *gin.Context, action string) (*model.Host, bool) {
	var host model.Host
	if err := h.DB.First(&host, idParam(c)).Error; err != nil {
		response.NotFound(c, "主机不存在")
		return nil, false
	}

	user := middleware.CurrentUser(c)
	if !h.hostVisibleWithGrants(user, &host) {
		response.NotFound(c, "主机不存在")
		return nil, false
	}
	if action != "" && !h.hostActionAllowed(user, &host, action) {
		response.Forbidden(c, "该主机未授权此操作: "+action)
		return nil, false
	}
	return &host, true
}

// ListHosts 主机清单，支持关键字与环境过滤，结果受数据权限与资源授权约束
func (h *Handler) ListHosts(c *gin.Context) {
	page, size := pageParams(c)
	q := h.applyScopeWithGrants(h.DB.Model(&model.Host{}), middleware.CurrentUser(c), "host")

	if kw := strings.TrimSpace(c.Query("keyword")); kw != "" {
		like := "%" + kw + "%"
		q = q.Where("name LIKE ? OR address LIKE ? OR tags LIKE ?", like, like, like)
	}
	if env := c.Query("env"); env != "" {
		q = q.Where("env = ?", env)
	}
	if status := c.Query("status"); status != "" {
		q = q.Where("status = ?", status)
	}
	if deptID := c.Query("deptId"); deptID != "" {
		q = q.Where("dept_id = ?", deptID)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		response.Error(c, "查询主机失败")
		return
	}
	var list []model.Host
	if err := q.Order("id desc").Offset((page - 1) * size).Limit(size).Find(&list).Error; err != nil {
		response.Error(c, "查询主机失败")
		return
	}
	response.OKPage(c, list, total, page, size)
}

// CreateHost 新增主机
func (h *Handler) CreateHost(c *gin.Context) {
	var req hostReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "主机名称、地址、登录用户为必填项")
		return
	}
	// 引用共享凭据时不需要本机口令；反之必须给
	if req.CredentialID != 0 {
		if err := h.assertCredentialUsable(req.CredentialID); err != nil {
			response.BadRequest(c, err.Error())
			return
		}
	} else if req.Secret == "" {
		response.BadRequest(c, "请提供登录密码或私钥，或改为引用凭证库")
		return
	}
	if req.ProxyHostID != 0 {
		if err := h.DB.First(&model.Host{}, req.ProxyHostID).Error; err != nil {
			response.BadRequest(c, "指定的跳板机不存在")
			return
		}
	}

	host := model.Host{
		Name: req.Name, Address: req.Address, Port: defaultPort(req.Port),
		Username: req.Username, AuthType: defaultAuth(req.AuthType), Secret: h.sealSecret(req.Secret),
		Env: defaultEnv(req.Env), Tags: req.Tags, Remark: req.Remark, Status: "unknown",
		ProxyHostID: req.ProxyHostID, DeptID: req.DeptID,
		CredentialID: req.CredentialID,
		CreatedBy:    middleware.CurrentUser(c).ID,
	}
	// 引用凭证库的主机不留本机口令副本：留着就是一份没人维护、也不会被轮换的后门
	if host.CredentialID != 0 {
		host.Secret = ""
	}
	if err := h.DB.Create(&host).Error; err != nil {
		response.Error(c, "主机创建失败")
		return
	}
	response.OK(c, host)
}

// UpdateHost 编辑主机
func (h *Handler) UpdateHost(c *gin.Context) {
	hostPtr, ok := h.loadHostForAction(c, model.ActionManage)
	if !ok {
		return
	}
	host := *hostPtr

	var req hostReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "参数校验失败")
		return
	}
	if req.ProxyHostID == host.ID {
		response.BadRequest(c, "跳板机不能是自己")
		return
	}
	if req.ProxyHostID != 0 {
		if err := h.DB.First(&model.Host{}, req.ProxyHostID).Error; err != nil {
			response.BadRequest(c, "指定的跳板机不存在")
			return
		}
	}

	// 地址或端口变了，原有主机指纹不再可信，清空以便重新记录
	if host.Address != req.Address || host.Port != defaultPort(req.Port) {
		host.HostKey = ""
	}

	host.Name, host.Address, host.Port = req.Name, req.Address, defaultPort(req.Port)
	host.Username, host.AuthType = req.Username, defaultAuth(req.AuthType)
	host.Env, host.Tags, host.Remark = defaultEnv(req.Env), req.Tags, req.Remark
	host.ProxyHostID = req.ProxyHostID
	host.DeptID = req.DeptID

	if req.Secret != "" {
		host.Secret = h.sealSecret(req.Secret)
	}

	// 凭据来源切换。注意「secret 留空表示不变」这条老规则在这里会咬人：
	// 从共享凭据切回本机自填时，本机那份早就被清空了，此时必须让人重新给一份，
	// 否则会存出一台没有任何凭据、连不上却看不出原因的主机。
	switch {
	case req.CredentialID != 0:
		if err := h.assertCredentialUsable(req.CredentialID); err != nil {
			response.BadRequest(c, err.Error())
			return
		}
		host.CredentialID = req.CredentialID
		host.Secret = ""
	case host.CredentialID != 0 && req.CredentialID == 0:
		if req.Secret == "" {
			response.BadRequest(c, "要从共享凭据切回本机自填，必须同时提供登录密码或私钥")
			return
		}
		host.CredentialID = 0
	}

	if err := h.DB.Save(&host).Error; err != nil {
		response.Error(c, "主机更新失败")
		return
	}
	response.OK(c, host)
}

// DeleteHost 删除主机
func (h *Handler) DeleteHost(c *gin.Context) {
	host, ok := h.loadHostForAction(c, model.ActionManage)
	if !ok {
		return
	}
	id := host.ID

	var refCount int64
	h.DB.Model(&model.Host{}).Where("proxy_host_id = ?", id).Count(&refCount)
	if refCount > 0 {
		response.BadRequest(c, fmt.Sprintf("该主机被 %d 台主机作为跳板机使用，请先解除引用", refCount))
		return
	}
	if err := h.DB.Delete(&model.Host{}, id).Error; err != nil {
		response.Error(c, "主机删除失败")
		return
	}
	response.OK(c, nil)
}

// CheckHost 连通性探测，顺带采集系统信息
func (h *Handler) CheckHost(c *gin.Context) {
	hostPtr, ok := h.loadHostScoped(c)
	if !ok {
		return
	}
	host := *hostPtr

	ctx, cancel := context.WithTimeout(c.Request.Context(), 20*time.Second)
	defer cancel()
	res := sshx.Run(ctx, h.target(&host), "uname -srm 2>/dev/null || ver")

	now := time.Now()
	host.CheckedAt = &now
	if res.Status == "success" {
		host.Status = "online"
		host.OSInfo = strings.TrimSpace(res.Stdout)
	} else {
		host.Status = "offline"
	}
	h.DB.Model(&host).Select("status", "os_info", "checked_at").Updates(host)

	response.OK(c, gin.H{
		"status":   host.Status,
		"osInfo":   host.OSInfo,
		"detail":   strings.TrimSpace(res.Stderr),
		"costMs":   res.CostMs,
		"viaProxy": h.proxyLabel(&host),
	})
}

// target 构造 SSH 连接目标，按 ProxyHostID 递归拼出跳板链。
// 开启指纹校验时，首次连接学到的主机公钥会写回数据库。
func (h *Handler) target(host *model.Host) sshx.Target {
	return h.buildTarget(host, map[uint]bool{})
}

func (h *Handler) buildTarget(host *model.Host, seen map[uint]bool) sshx.Target {
	hostID := host.ID
	t := sshx.Target{
		Address:       host.Address,
		Port:          host.Port,
		Username:      host.Username,
		AuthType:      host.AuthType,
		StrictHostKey: h.Cfg.SSHStrictHostKey,
		KnownHostKey:  host.HostKey,
		OnLearnHostKey: func(authorizedKey string) {
			h.DB.Model(&model.Host{}).Where("id = ?", hostID).Update("host_key", authorizedKey)
		},
	}
	// 库里存的是密文（配了 OPS_SECRET_KEY 时），解不开就直接抛原因，
	// 不拿乱码去连主机 —— 那会表现成「认证失败」，把人引向错误的排查方向
	if secret, err := h.openSecret("主机凭据", host.Secret); err != nil {
		t.PrepareError = fmt.Sprintf("主机 %s 的凭据不可用: %v", host.Name, err)
	} else {
		t.Secret = secret
	}

	// 引用了凭证库：用户名与密钥一律以凭据为准。
	// 解析失败不回退到主机自带凭据 —— 那会让「凭据被禁用了」表现成「连上了」，
	// 是比连不上更糟的结果。
	if host.CredentialID != 0 {
		cred, err := h.loadCredentialSecret(host.CredentialID)
		if err != nil {
			t.PrepareError = fmt.Sprintf("主机 %s 引用的共享凭据不可用: %v", host.Name, err)
		} else {
			t.Username = cred.Username
			t.AuthType = cred.Type
			t.Secret = cred.Secret
			t.Passphrase = cred.Passphrase
		}
	}

	seen[host.ID] = true
	if host.ProxyHostID != 0 && !seen[host.ProxyHostID] {
		var proxy model.Host
		if err := h.DB.First(&proxy, host.ProxyHostID).Error; err == nil {
			proxyTarget := h.buildTarget(&proxy, seen)
			t.Proxy = &proxyTarget
		}
	}
	return t
}

// proxyLabel 返回跳板链的可读描述，直连时为空字符串
func (h *Handler) proxyLabel(host *model.Host) string {
	labels := make([]string, 0, sshx.MaxProxyDepth)
	seen := map[uint]bool{host.ID: true}
	next := host.ProxyHostID

	for next != 0 && !seen[next] && len(labels) < sshx.MaxProxyDepth {
		seen[next] = true
		var proxy model.Host
		if err := h.DB.First(&proxy, next).Error; err != nil {
			break
		}
		labels = append(labels, fmt.Sprintf("%s(%s)", proxy.Name, proxy.Address))
		next = proxy.ProxyHostID
	}
	return strings.Join(labels, " → ")
}

func defaultPort(p int) int {
	if p <= 0 || p > 65535 {
		return 22
	}
	return p
}

func defaultAuth(a string) string {
	if a == "key" {
		return "key"
	}
	return "password"
}

func defaultEnv(e string) string {
	switch e {
	case "test", "prod":
		return e
	default:
		return "dev"
	}
}
