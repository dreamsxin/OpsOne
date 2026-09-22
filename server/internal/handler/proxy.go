package handler

// 出口代理与代理检测。
//
// 平台此前十几处出网点（拨测、云 API、IM、Webhook、指标 / 日志 / 链路数据源…）
// 各自 `&http.Client{Timeout: ...}`，**没有任何一处设过 Transport.Proxy**，
// 也不读 HTTP_PROXY 环境变量。所以这一页做两件事，缺一件它就只是个装饰性的台账：
//
//  1. **检测**：直连与走代理各请求一次，把两个结果摆在一起。
//     这是判断「是网络本来就不通，还是代理坏了」唯一靠得住的方式 ——
//     只测代理的话，两种故障给出的是同一个报错。
//  2. **被用**：HTTP 拨测可以指定走某个代理（probe.go）。
//     这是目前**唯一**接入了代理的模块，页面上照实列出来，
//     不让人以为登记完就全局生效。
//
// socks5 不需要额外依赖：标准库 http.Transport.Proxy 直接支持
// http / https / socks5 三种 scheme。

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"ops-platform/server/internal/cryptox"
	"ops-platform/server/internal/middleware"
	"ops-platform/server/internal/model"
	"ops-platform/server/internal/response"
)

const (
	// CfgProxyTestURL 检测代理时默认请求的地址。默认留空 —— 内网不一定有可用的回显服务，
	// 替用户默认填一个公网地址等于替他决定了「这台机器可以访问公网」
	CfgProxyTestURL = "proxy.test_url"
	// proxyCheckTimeout 单次检测超时
	proxyCheckTimeout = 8 * time.Second
	// proxyBodyLimit 读取响应体上限，只用来找出口 IP
	proxyBodyLimit = 64 << 10
)

var proxySchemes = []struct {
	Code  string `json:"code"`
	Label string `json:"label"`
}{
	{"http", "HTTP 代理"},
	{"https", "HTTPS 代理"},
	{"socks5", "SOCKS5 代理"},
}

func validProxyScheme(scheme string) bool {
	for _, item := range proxySchemes {
		if item.Code == scheme {
			return true
		}
	}
	return false
}

// ipPattern 从回显响应里抠一个 IPv4。刻意不解析 JSON：
// 各家回显服务（httpbin / ip.sb / 自建 nginx echo）的字段名都不一样，
// 认字段等于只支持其中一家。
var ipPattern = regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`)

type proxyReq struct {
	Name     string `json:"name" binding:"required"`
	Scheme   string `json:"scheme"`
	Host     string `json:"host" binding:"required"`
	Port     int    `json:"port"`
	Username string `json:"username"`
	Password string `json:"password"`
	TestURL  string `json:"testUrl"`
	Enabled  *bool  `json:"enabled"`
	Remark   string `json:"remark"`
}

func proxyView(item model.EgressProxy, probeCount int64) gin.H {
	storage := "plain"
	if cryptox.IsSealed(item.Password) {
		storage = "encrypted"
	}
	return gin.H{
		"id": item.ID, "name": item.Name, "scheme": item.Scheme,
		"host": item.Host, "port": item.Port, "username": item.Username,
		"testUrl": item.TestURL, "enabled": item.Enabled, "remark": item.Remark,
		"lastCheckAt": item.LastCheckAt, "lastStatus": item.LastStatus,
		"lastCostMs": item.LastCostMs, "lastError": item.LastError, "exitIp": item.ExitIP,
		"createdAt": item.CreatedAt, "updatedAt": item.UpdatedAt,
		"storage":     storage,
		"hasPassword": item.Password != "",
		"endpoint":    fmt.Sprintf("%s://%s:%d", item.Scheme, item.Host, item.Port),
		"probeCount":  probeCount,
	}
}

// proxyURL 拼出给 http.Transport.Proxy 用的地址（含认证信息）。
func (h *Handler) proxyURL(item model.EgressProxy) (*url.URL, error) {
	u := &url.URL{
		Scheme: item.Scheme,
		Host:   net.JoinHostPort(item.Host, fmt.Sprint(item.Port)),
	}
	if item.Username != "" {
		password, err := h.openSecret(fmt.Sprintf("代理「%s」口令", item.Name), item.Password)
		if err != nil {
			return nil, err
		}
		u.User = url.UserPassword(item.Username, password)
	}
	return u, nil
}

// proxyClient 构造一个走该代理的 http.Client。
//
// 这个函数是「代理真的生效了」的唯一实现点：其它模块要接代理就调它，
// 而不是各自再拼一遍 Transport。
func (h *Handler) proxyClient(item model.EgressProxy, timeout time.Duration) (*http.Client, error) {
	u, err := h.proxyURL(item)
	if err != nil {
		return nil, err
	}
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			Proxy:               http.ProxyURL(u),
			TLSHandshakeTimeout: timeout,
		},
	}, nil
}

// proxyForProbe 取拨测要用的代理。
//
// 代理不存在或被停用时返回错误而**不是**悄悄改成直连：静默直连会把
// 「代理挂了」表现成「目标正常」，那比拨测失败危险得多。
func (h *Handler) proxyForProbe(proxyID uint) (*model.EgressProxy, error) {
	if proxyID == 0 {
		return nil, nil
	}
	var item model.EgressProxy
	if err := h.DB.First(&item, proxyID).Error; err != nil {
		return nil, fmt.Errorf("指定的出口代理（ID %d）已不存在", proxyID)
	}
	if !item.Enabled {
		return nil, fmt.Errorf("指定的出口代理「%s」已停用", item.Name)
	}
	return &item, nil
}

// ---------- 检测 ----------

// probeAttempt 一次请求的结果
type probeAttempt struct {
	OK     bool   `json:"ok"`
	Code   int    `json:"code"`
	CostMs int64  `json:"costMs"`
	Error  string `json:"error"`
	ExitIP string `json:"exitIp"`
}

// tryURL 用给定 client 请求一次，顺带从响应体里找出口 IP。
func tryURL(client *http.Client, target string) probeAttempt {
	start := time.Now()
	req, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		return probeAttempt{Error: truncate(err.Error(), 200)}
	}
	req.Header.Set("User-Agent", "OpsOne-ProxyCheck/1.0")

	resp, err := client.Do(req)
	cost := time.Since(start).Milliseconds()
	if err != nil {
		return probeAttempt{CostMs: cost, Error: truncate(err.Error(), 200)}
	}
	defer resp.Body.Close()

	attempt := probeAttempt{OK: resp.StatusCode < 400, Code: resp.StatusCode, CostMs: cost}
	if !attempt.OK {
		attempt.Error = fmt.Sprintf("状态码 %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, proxyBodyLimit))
	if ip := ipPattern.FindString(string(body)); ip != "" {
		attempt.ExitIP = ip
	}
	return attempt
}

type proxyCheckReq struct {
	TestURL string `json:"testUrl"`
}

// CheckEgressProxy 直连一次 + 走代理一次，把两个结果摆在一起。
//
// 两次都打同一个地址，否则对比没有意义。
func (h *Handler) CheckEgressProxy(c *gin.Context) {
	var item model.EgressProxy
	if err := h.DB.First(&item, idParam(c)).Error; err != nil {
		response.NotFound(c, "代理不存在")
		return
	}
	var req proxyCheckReq
	_ = c.ShouldBindJSON(&req)

	target := strings.TrimSpace(req.TestURL)
	if target == "" {
		target = strings.TrimSpace(item.TestURL)
	}
	if target == "" {
		target = h.configString(CfgProxyTestURL, "")
	}
	if target == "" {
		response.BadRequest(c, "没有测试地址：请在这条代理上填一个，或在系统配置里设 proxy.test_url。"+
			"平台不替你默认填一个公网地址 —— 那等于替你决定了这台机器可以访问公网")
		return
	}
	if !strings.HasPrefix(target, "http://") && !strings.HasPrefix(target, "https://") {
		response.BadRequest(c, "测试地址必须是 http:// 或 https:// 开头")
		return
	}

	direct := tryURL(&http.Client{Timeout: proxyCheckTimeout}, target)

	client, err := h.proxyClient(item, proxyCheckTimeout)
	var viaProxy probeAttempt
	if err != nil {
		viaProxy = probeAttempt{Error: err.Error()}
	} else {
		viaProxy = tryURL(client, target)
	}

	now := time.Now()
	status := "fail"
	if viaProxy.OK {
		status = "ok"
	}
	updates := map[string]any{
		"last_check_at": &now, "last_status": status,
		"last_cost_ms": viaProxy.CostMs, "last_error": truncate(viaProxy.Error, 240),
		"exit_ip": viaProxy.ExitIP,
	}
	h.DB.Model(&item).Updates(updates)

	// 结论是这一页的价值所在：把两个结果的组合翻译成一句人能直接照着做的话
	verdict := ""
	switch {
	case viaProxy.OK && direct.OK:
		verdict = "代理可用；这个地址直连也通，所以走不走代理都能到"
	case viaProxy.OK && !direct.OK:
		verdict = "代理可用，而且是必需的 —— 直连不通，走代理通"
	case !viaProxy.OK && direct.OK:
		verdict = "代理不可用，但直连是通的：问题在代理本身，不在网络"
	default:
		verdict = "代理和直连都不通：先确认测试地址本身是否可达，这时候还判断不了代理好坏"
	}

	payload := gin.H{
		"target": target, "direct": direct, "viaProxy": viaProxy,
		"verdict": verdict, "status": status,
	}
	if viaProxy.ExitIP == "" {
		payload["exitIPNote"] = "测试地址不回显 IP，无法判断出口 IP —— 换一个会把来源 IP 写在响应里的地址才能看到"
	} else if direct.ExitIP != "" && direct.ExitIP != viaProxy.ExitIP {
		payload["exitIPNote"] = fmt.Sprintf("出口 IP 确实变了：直连 %s → 走代理 %s", direct.ExitIP, viaProxy.ExitIP)
	} else if direct.ExitIP != "" && direct.ExitIP == viaProxy.ExitIP {
		payload["exitIPNote"] = "出口 IP 与直连相同：代理可能是透明代理，也可能与本机共用出口"
	}
	response.OK(c, payload)
}

// ---------- CRUD ----------

func (h *Handler) ListEgressProxies(c *gin.Context) {
	var list []model.EgressProxy
	if err := h.DB.Order("name asc").Find(&list).Error; err != nil {
		response.Error(c, "查询代理失败")
		return
	}

	var refs []struct {
		ProxyID uint
		Count   int64
	}
	h.DB.Model(&model.Probe{}).Select("proxy_id, COUNT(1) as count").
		Where("proxy_id <> 0").Group("proxy_id").Scan(&refs)
	refMap := map[uint]int64{}
	for _, r := range refs {
		refMap[r.ProxyID] = r.Count
	}

	views := make([]gin.H, 0, len(list))
	for _, item := range list {
		views = append(views, proxyView(item, refMap[item.ID]))
	}
	response.OK(c, gin.H{
		"list":    views,
		"schemes": proxySchemes,
		"testUrl": h.configString(CfgProxyTestURL, ""),
		"notes": []string{
			"检测会**直连一次、走代理一次**，两个结果摆在一起 —— 只测代理的话，「网络不通」和「代理坏了」给出的是同一个报错",
			"目前只有 **HTTP 拨测**能指定走代理。云 API、IM、Webhook、指标 / 日志 / 链路数据源、邮件都还是直连，登记代理不会让它们改道",
			"拨测指定的代理被停用或删除时，那条拨测**直接失败并点名原因**，不会静默改成直连（静默直连会把「代理挂了」显示成「目标正常」）",
			"出口 IP 只能从会回显来源 IP 的测试地址里读到；读不到就照实说读不到，不留空让人猜",
			"平台不替你预设测试地址：默认填一个公网地址等于替你决定了这台机器可以访问公网",
			"代理口令加密落库、不出接口；编辑时留空表示不修改",
		},
	})
}

func (h *Handler) CreateEgressProxy(c *gin.Context) {
	var req proxyReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "名称与地址为必填项")
		return
	}
	scheme := orDefault(req.Scheme, "http")
	if !validProxyScheme(scheme) {
		response.BadRequest(c, "代理类型只能是 http / https / socks5")
		return
	}
	if req.Port <= 0 || req.Port > 65535 {
		response.BadRequest(c, "端口不合法")
		return
	}

	item := model.EgressProxy{
		Name: strings.TrimSpace(req.Name), Scheme: scheme,
		Host: strings.TrimSpace(req.Host), Port: req.Port,
		Username: strings.TrimSpace(req.Username), Password: h.sealSecret(req.Password),
		TestURL: strings.TrimSpace(req.TestURL), Enabled: true, Remark: req.Remark,
		LastStatus: "unknown",
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
	response.OK(c, proxyView(item, 0))
}

func (h *Handler) UpdateEgressProxy(c *gin.Context) {
	var item model.EgressProxy
	if err := h.DB.First(&item, idParam(c)).Error; err != nil {
		response.NotFound(c, "代理不存在")
		return
	}
	var req proxyReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "参数校验失败")
		return
	}
	scheme := orDefault(req.Scheme, item.Scheme)
	if !validProxyScheme(scheme) {
		response.BadRequest(c, "代理类型只能是 http / https / socks5")
		return
	}
	if req.Port <= 0 || req.Port > 65535 {
		response.BadRequest(c, "端口不合法")
		return
	}

	updates := map[string]any{
		"name": strings.TrimSpace(req.Name), "scheme": scheme,
		"host": strings.TrimSpace(req.Host), "port": req.Port,
		"username": strings.TrimSpace(req.Username), "test_url": strings.TrimSpace(req.TestURL),
		"remark": req.Remark,
	}
	if req.Enabled != nil {
		updates["enabled"] = *req.Enabled
	}
	if strings.TrimSpace(req.Password) != "" {
		updates["password"] = h.sealSecret(req.Password)
	}
	if err := h.DB.Model(&item).Updates(updates).Error; err != nil {
		response.Error(c, "更新失败，名称可能重复")
		return
	}
	h.DB.First(&item, item.ID)
	response.OK(c, proxyView(item, 0))
}

func (h *Handler) DeleteEgressProxy(c *gin.Context) {
	id := idParam(c)
	var count int64
	h.DB.Model(&model.Probe{}).Where("proxy_id = ?", id).Count(&count)
	if count > 0 {
		response.BadRequest(c, fmt.Sprintf("还有 %d 个拨测指定了这个代理，请先把它们改成直连或换一个代理", count))
		return
	}
	if err := h.DB.Delete(&model.EgressProxy{}, id).Error; err != nil {
		response.Error(c, "删除失败")
		return
	}
	response.OK(c, gin.H{"deleted": true})
}
