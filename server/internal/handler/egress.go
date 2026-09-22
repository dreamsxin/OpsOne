package handler

// 统一出口代理：让「登记的代理」真的成为平台的出网口。
//
// # 这一轮修的是什么
//
// 代理检测那一轮做完之后，平台的状态是：代理能登记、能检测、能被 HTTP 拨测按条引用，
// 但**其它十几个出网点全都不走它**（云 API、IM 通讯录与群机器人、Webhook 通知、
// 大模型上游、Jenkins…）。对一个没有公网直连、只能通过代理出网的内网环境来说，
// 那等于这些功能全是坏的，而页面上看不出为什么。
//
// 顺带纠正一处原来写错的注释：那十几处 `&http.Client{Timeout: ...}` 的 Transport 是 nil，
// 运行时会落到 `http.DefaultTransport`，而它的 Proxy 是 `http.ProxyFromEnvironment` ——
// 也就是说它们**一直在读** HTTP_PROXY / HTTPS_PROXY / NO_PROXY 环境变量。
// 原注释说「也不读 HTTP_PROXY 环境变量」是不对的。这件事对「平台到底怎么连出去」
// 有实际影响，所以现在把进程看到的这三个环境变量直接摊在页面上。
//
// # 语义
//
//   - 没配统一出口（`proxy.egress_id` 为 0）：行为与以前完全一致 ——
//     仍然遵守进程的 HTTP_PROXY 环境变量。**刻意不改成强制直连**：
//     真有人靠环境变量在用，悄悄禁掉等于把能用的功能弄坏。
//   - 配了统一出口：走那条代理，但命中 `proxy.bypass` 的目标直连。
//   - 配了统一出口、但那条记录**不存在或已停用**：所有走出口的请求**显式失败**，
//     错误里点名是代理的问题。不退回直连 —— 与拨测同一条不变量：
//     静默直连会把「代理挂了」显示成「目标一切正常」。
//
// # 哪些不走，以及为什么
//
//   - **Prometheus / Loki / Jaeger / Kubernetes apiserver**：这些是内网基础设施，
//     地址是管理员填的内网地址。把它们送去出口代理只会变慢或直接失败，
//     而且代理日志里会多出一堆内网流量。
//   - **HTTP 拨测**：拨测**按条**指定代理，`proxyId = 0` 的含义就是「这条要直连」。
//     让全局出口覆盖它，等于把「直连探测」这个语义抹掉了。
//   - **SSH / SMTP / LDAP / DNS / 裸 TCP（证书检查、端口扫描、TCP 拨测）**：
//     不是 HTTP，标准库的 Transport.Proxy 管不到。想让它们出网得另做（SOCKS5 拨号），
//     这一轮不做，但页面上照实列出来。

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"ops-platform/server/internal/middleware"
	"ops-platform/server/internal/model"
	"ops-platform/server/internal/response"
)

const (
	// CfgProxyEgressID 统一出口代理的 ID。0 或留空表示不统一出口
	CfgProxyEgressID = "proxy.egress_id"
	// CfgProxyBypass 不走出口代理的目标清单
	CfgProxyBypass = "proxy.bypass"
)

// defaultEgressBypass 默认不走代理的目标。与 seed 共用一个常量，见 model.DefaultProxyBypass。
const defaultEgressBypass = model.DefaultProxyBypass

// bypassRule 一条「不走代理」规则。三种形态之一：网段、域名后缀、精确主机名
type bypassRule struct {
	Raw    string
	Net    *net.IPNet
	Suffix string
	Host   string
}

// parseEgressBypass 解析清单，同时把解析不了的条目单独返回。
//
// 坏条目不是静默忽略：一条写错的规则表现成「这个目标本该直连却被送去了代理」，
// 而那个现象很难联想到是清单写错了。
func parseEgressBypass(raw string) (rules []bypassRule, bad []string) {
	for _, part := range strings.Split(raw, ",") {
		item := strings.TrimSpace(part)
		if item == "" {
			continue
		}
		if strings.Contains(item, "/") {
			_, cidr, err := net.ParseCIDR(item)
			if err != nil {
				bad = append(bad, item)
				continue
			}
			rules = append(rules, bypassRule{Raw: item, Net: cidr})
			continue
		}
		// *.example.com 与 .example.com 是同一个意思，统一成后缀
		if strings.HasPrefix(item, "*.") {
			item = item[1:]
		}
		if strings.HasPrefix(item, ".") {
			if len(item) == 1 {
				bad = append(bad, item)
				continue
			}
			rules = append(rules, bypassRule{Raw: item, Suffix: strings.ToLower(item)})
			continue
		}
		if strings.ContainsAny(item, " \t*") {
			bad = append(bad, item)
			continue
		}
		rules = append(rules, bypassRule{Raw: item, Host: strings.ToLower(item)})
	}
	return rules, bad
}

// matchEgressBypass 判断一个主机名/IP 是否命中清单，并返回命中的是哪一条。
//
// **只看字面量，不做 DNS 解析**：解析一次要花时间，而且解析结果会变 ——
// 同一个地址这次直连下次走代理，是最难查的一类问题。代价是
// `prom.corp.example.com` 这种内网域名，不写进清单就会被送去代理。
func matchEgressBypass(rules []bypassRule, host string) (bool, string) {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return false, ""
	}
	ip := net.ParseIP(host)
	for _, rule := range rules {
		switch {
		case rule.Net != nil:
			if ip != nil && rule.Net.Contains(ip) {
				return true, rule.Raw
			}
		case rule.Suffix != "":
			if strings.HasSuffix(host, rule.Suffix) {
				return true, rule.Raw
			}
		case rule.Host != "":
			if host == rule.Host {
				return true, rule.Raw
			}
		}
	}
	return false, ""
}

// egressProxy 取当前的统一出口代理。
//
// 三种返回：
//   - (nil, nil) 没配统一出口
//   - (item, nil) 配好了且可用
//   - (nil, err) 配了但记录不存在 / 已停用 —— 调用方必须把请求失败掉
func (h *Handler) egressProxy() (*model.EgressProxy, error) {
	// 这个函数是在 http.Transport 里被调用的，panic 会直接打挂请求所在的 goroutine，
	// 所以没有库可用时按「没配统一出口」处理，而不是相信调用方一定给了 DB
	if h.DB == nil {
		return nil, nil
	}
	id := h.configInt(CfgProxyEgressID, 0)
	if id <= 0 {
		return nil, nil
	}
	var item model.EgressProxy
	if err := h.DB.First(&item, id).Error; err != nil {
		return nil, fmt.Errorf("统一出口代理（ID %d）已不存在，"+
			"请到「网络 → 代理检测」重新指定或清空 %s", id, CfgProxyEgressID)
	}
	if !item.Enabled {
		return nil, fmt.Errorf("统一出口代理「%s」已停用 —— 出网请求不会退回直连", item.Name)
	}
	return &item, nil
}

// egressBypassRules 读当前的 bypass 清单
func (h *Handler) egressBypassRules() []bypassRule {
	if h.DB == nil {
		return nil
	}
	rules, _ := parseEgressBypass(h.configString(CfgProxyBypass, defaultEgressBypass))
	return rules
}

// egressTransport 平台出网用的 Transport。
//
// Proxy 是**每个请求算一次**的函数（标准库就是这么设计的），所以：
// 改了配置立刻生效，不需要重启也不需要清缓存；代价是每次出网多一两次 SQLite 查询，
// 在这个量级下可以忽略。
func (h *Handler) egressTransport() *http.Transport {
	return &http.Transport{
		Proxy: func(req *http.Request) (*url.URL, error) {
			item, err := h.egressProxy()
			if err != nil {
				return nil, err
			}
			if item == nil {
				// 没配统一出口：保持原行为，仍然遵守环境变量
				return http.ProxyFromEnvironment(req)
			}
			if req.URL != nil {
				if ok, _ := matchEgressBypass(h.egressBypassRules(), req.URL.Hostname()); ok {
					return nil, nil
				}
			}
			return h.proxyURL(*item)
		},
	}
}

// egressClient 出网用的 http.Client。所有「可能要出公网」的模块都该用它拿 client。
func (h *Handler) egressClient(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, Transport: h.egressTransport()}
}

// ---------- 生效范围（页面上要能看见，否则又变成一个看不出效果的开关） ----------

type egressCoverage struct {
	Module string `json:"module"`
	Target string `json:"target"`
	Note   string `json:"note"`
}

// egressCovered 走统一出口的模块。
//
// 这张表不是文档而是断言：`egress_test.go` 里有一条测试扫 handler 包的源码，
// 要求每个「出站 http.Client 构造点」要么走 egressClient，要么在豁免清单里写明原因。
// 少了一处就会失败 —— 否则这张表迟早和代码对不上，而对不上的表比没有表更糟。
var egressCovered = []egressCoverage{
	{"云资源同步", "阿里云 OpenAPI（ecs/alidns）", "域名写死在代码里，必然是公网"},
	{"IM 通讯录 / 扫码登录", "企微 qyapi / 钉钉 oapi+api / 飞书 open.feishu.cn", "默认公网域名，可被 BaseURL 改成自建网关"},
	{"IM 群机器人", "渠道里填的 webhook 地址", "地址是人填的，实践上几乎总是公网"},
	{"通用 Webhook 通知", "渠道里填的 URL", "内外网都可能，命中 bypass 则直连"},
	{"大模型上游", "上游 BaseURL（探活与 chat 转发）", "公网厂商与自建 vLLM 都有人用"},
	{"Jenkins", "构建服务器 URL", "多数装在内网，所以内网网段默认在 bypass 里"},
	{"代理检测（走代理那一次）", "检测地址", "本来就是显式指定某条代理，不受统一出口影响"},
}

// egressNotCovered 不走统一出口的，连原因一起摆出来
var egressNotCovered = []egressCoverage{
	{"Prometheus / Loki / Jaeger", "数据源 BaseURL", "内网基础设施，送去代理只会变慢或失败"},
	{"Kubernetes apiserver", "kubeconfig 里的 server", "同上；而且它用的是自定义 Transport，连环境变量都不读"},
	{"HTTP 拨测", "被监控的 URL", "拨测按条指定代理，proxyId=0 的含义就是「这条要直连」，全局出口不许覆盖它"},
	{"SSH / 跳板机", "主机地址", "不是 HTTP，Transport.Proxy 管不到"},
	{"SMTP 发信", "邮件服务器", "同上"},
	{"LDAP / DNS 查询", "目录服务器 / DNS 服务器", "同上"},
	{"证书检查 / 端口扫描 / TCP 拨测", "裸 TCP 连接", "同上（TCP 拨测还额外强制清掉代理）"},
}

type egressPolicyView struct {
	Configured bool              `json:"configured"`
	ProxyID    uint              `json:"proxyId"`
	ProxyName  string            `json:"proxyName"`
	Endpoint   string            `json:"endpoint"`
	Problem    string            `json:"problem"`
	Bypass     []string          `json:"bypass"`
	BypassBad  []string          `json:"bypassBad"`
	Env        map[string]string `json:"env"`
	Covered    []egressCoverage  `json:"covered"`
	NotCovered []egressCoverage  `json:"notCovered"`
	Notes      []string          `json:"notes"`
}

// GetEgressPolicy 当前出网口径。
func (h *Handler) GetEgressPolicy(c *gin.Context) {
	view := egressPolicyView{
		Covered:    egressCovered,
		NotCovered: egressNotCovered,
		Env: map[string]string{
			"HTTP_PROXY":  firstEnv("HTTP_PROXY", "http_proxy"),
			"HTTPS_PROXY": firstEnv("HTTPS_PROXY", "https_proxy"),
			"NO_PROXY":    firstEnv("NO_PROXY", "no_proxy"),
		},
	}

	raw := h.configString(CfgProxyBypass, defaultEgressBypass)
	rules, bad := parseEgressBypass(raw)
	for _, rule := range rules {
		view.Bypass = append(view.Bypass, rule.Raw)
	}
	view.BypassBad = bad

	id := h.configInt(CfgProxyEgressID, 0)
	view.Configured = id > 0
	view.ProxyID = uint(id)
	item, err := h.egressProxy()
	switch {
	case err != nil:
		view.Problem = err.Error()
	case item != nil:
		view.ProxyName = item.Name
		view.Endpoint = fmt.Sprintf("%s://%s:%d", item.Scheme, item.Host, item.Port)
	}

	if view.Configured {
		view.Notes = append(view.Notes,
			"已配统一出口：上面「走代理」那几类的出网请求都会经过它，命中 bypass 的目标除外")
	} else {
		view.Notes = append(view.Notes,
			"**没有配统一出口**。此时行为与以前一致：这些模块仍然遵守进程的 "+
				"HTTP_PROXY / HTTPS_PROXY 环境变量（上面列的是进程实际看到的值）。"+
				"刻意不改成强制直连——真有人靠环境变量在用，悄悄禁掉等于把能用的功能弄坏")
	}
	view.Notes = append(view.Notes,
		"配了统一出口但那条代理**被停用或删除**时，走出口的请求会**直接失败并点名代理**，"+
			"不会退回直连：静默直连会把「代理挂了」显示成「目标一切正常」",
		"bypass **只比字面量、不做 DNS 解析**——解析结果会变，同一个地址这次直连下次走代理是最难查的问题。"+
			"代价是 `prom.corp.example.com` 这类内网域名不写进清单就会被送去代理",
		"默认清单里有全部私有网段与本机，否则开启统一出口的那一刻内网数据源会全挂",
		"改这两项配置**立刻生效**，不需要重启：Proxy 是每个请求算一次的")
	response.OK(c, view)
}

type egressPolicyReq struct {
	// ProxyID 0 表示不统一出口
	ProxyID uint   `json:"proxyId"`
	Bypass  string `json:"bypass"`
}

// UpdateEgressPolicy 设置统一出口与 bypass 清单。
//
// 校验放在这里而不是等到出网时才报错：指定一条停用的代理当出口，
// 表现是「所有出网功能一起坏掉」，那个现象离「我刚改了个下拉框」太远了。
func (h *Handler) UpdateEgressPolicy(c *gin.Context) {
	var req egressPolicyReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "参数错误")
		return
	}

	var item model.EgressProxy
	if req.ProxyID > 0 {
		if err := h.DB.First(&item, req.ProxyID).Error; err != nil {
			response.BadRequest(c, "指定的代理不存在")
			return
		}
		if !item.Enabled {
			response.BadRequest(c, fmt.Sprintf("代理「%s」当前是停用状态，"+
				"设成统一出口会让所有出网功能一起失败。请先启用它", item.Name))
			return
		}
		if item.LastStatus != "ok" {
			response.BadRequest(c, fmt.Sprintf("代理「%s」还没有一次成功的检测记录"+
				"（当前 %s）。先在列表里点「检测」确认它真的能出网，再设成统一出口",
				item.Name, item.LastStatus))
			return
		}
	}

	bypass := strings.TrimSpace(req.Bypass)
	if bypass == "" {
		bypass = defaultEgressBypass
	}
	if _, bad := parseEgressBypass(bypass); len(bad) > 0 {
		response.BadRequest(c, "bypass 清单里这些条目解析不了："+strings.Join(bad, "、")+
			"。支持的写法：精确主机名、.example.com（后缀）、10.0.0.0/8（网段）")
		return
	}

	if err := h.saveEgressConfig(c, CfgProxyEgressID, strconv.FormatUint(uint64(req.ProxyID), 10)); err != nil {
		response.Error(c, "保存失败: "+err.Error())
		return
	}
	if err := h.saveEgressConfig(c, CfgProxyBypass, bypass); err != nil {
		response.Error(c, "保存失败: "+err.Error())
		return
	}

	detail := "取消统一出口"
	if req.ProxyID > 0 {
		detail = fmt.Sprintf("统一出口设为「%s」", item.Name)
	}
	response.OK(c, gin.H{"proxyId": req.ProxyID, "bypass": bypass, "detail": detail})
}

// saveEgressConfig 写一项配置。两项都是内置键，一定存在，所以直接按 key 更新。
func (h *Handler) saveEgressConfig(c *gin.Context, key, value string) error {
	var cfg model.SysConfig
	if err := h.DB.Where("`key` = ?", key).First(&cfg).Error; err != nil {
		return fmt.Errorf("配置项 %s 不存在", key)
	}
	return h.DB.Model(&cfg).Updates(map[string]any{
		"value": value, "updated_by": middleware.CurrentUser(c).Username,
	}).Error
}

func firstEnv(keys ...string) string {
	for _, key := range keys {
		if v := strings.TrimSpace(os.Getenv(key)); v != "" {
			return v
		}
	}
	return ""
}
