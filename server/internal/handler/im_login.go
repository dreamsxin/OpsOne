package handler

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"ops-platform/server/internal/middleware"
	"ops-platform/server/internal/model"
	"ops-platform/server/internal/response"
)

// IM 扫码登录。
//
// 流程刻意做成「浏览器只拿一次性 ticket，JWT 走 POST 取回」：
//
//   1. 前端 GET  /auth/im/authorize?appId=N  → 平台生成 state 并给出厂商授权地址
//   2. 用户扫码，厂商回调 GET /auth/im/callback?code&state
//   3. 平台校验 state（一次性、有过期）、用 code 换 IM 用户标识、按绑定关系找平台账号，
//      生成一次性 ticket，302 回前端 LoginRedirect?ticket=xxx
//   4. 前端 POST /auth/im/exchange {ticket} → 拿到 JWT
//
// 为什么不在第 3 步直接把 JWT 塞进 URL：URL 会进浏览器历史、Referer、反向代理与
// CDN 的访问日志，等于把一个 12 小时有效的令牌写进一堆地方。ticket 只活 2 分钟、
// 只能用一次，泄露的窗口小得多。
//
// state 与 ticket 都放在进程内存里：重启会让「正在进行中的登录」失效（重新扫一次即可），
// 但省掉了一张表和它的清理逻辑。内网单实例部署，这个取舍是划算的；
// 要多实例就得换成共享存储，见 docs/SECURITY.md 24。

const (
	// imStateTTL 授权 state 的有效期：留足扫码时间，又不至于长到能被慢慢试
	imStateTTL = 5 * time.Minute
	// imTicketTTL 一次性 ticket 的有效期，只够前端立刻换令牌
	imTicketTTL = 2 * time.Minute
	// imLoginStoreMax 内存里最多存多少条待处理记录，防止有人靠刷 authorize 撑爆内存
	imLoginStoreMax = 500
)

// imLoginState 一次待完成的授权
type imLoginState struct {
	appID     uint
	expiresAt time.Time
}

// imLoginTicket 一张换令牌的凭据
type imLoginTicket struct {
	userID    uint
	appID     uint
	expiresAt time.Time
}

// imLoginStore state 与 ticket 的内存存储
type imLoginStore struct {
	mu      sync.Mutex
	states  map[string]imLoginState
	tickets map[string]imLoginTicket
}

func newImLoginStore() *imLoginStore {
	return &imLoginStore{
		states:  map[string]imLoginState{},
		tickets: map[string]imLoginTicket{},
	}
}

// randomToken 生成不可预测的随机串
func randomToken() (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// putState 存一个 state。顺手清掉过期的，避免内存只增不减。
func (s *imLoginStore) putState(appID uint) (string, error) {
	state, err := randomToken()
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked()
	if len(s.states) >= imLoginStoreMax {
		return "", fmt.Errorf("待处理的登录请求过多，请稍后再试")
	}
	s.states[state] = imLoginState{appID: appID, expiresAt: time.Now().Add(imStateTTL)}
	return state, nil
}

// takeState 取出并立刻删除 —— state 只能用一次，用完即焚是防重放的关键
func (s *imLoginStore) takeState(state string) (imLoginState, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.states[state]
	if !ok {
		return imLoginState{}, false
	}
	delete(s.states, state)
	if time.Now().After(item.expiresAt) {
		return imLoginState{}, false
	}
	return item, true
}

func (s *imLoginStore) putTicket(userID, appID uint) (string, error) {
	ticket, err := randomToken()
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked()
	s.tickets[ticket] = imLoginTicket{
		userID: userID, appID: appID, expiresAt: time.Now().Add(imTicketTTL),
	}
	return ticket, nil
}

// takeTicket 同样是取出即删除
func (s *imLoginStore) takeTicket(ticket string) (imLoginTicket, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.tickets[ticket]
	if !ok {
		return imLoginTicket{}, false
	}
	delete(s.tickets, ticket)
	if time.Now().After(item.expiresAt) {
		return imLoginTicket{}, false
	}
	return item, true
}

func (s *imLoginStore) sweepLocked() {
	now := time.Now()
	for key, item := range s.states {
		if now.After(item.expiresAt) {
			delete(s.states, key)
		}
	}
	for key, item := range s.tickets {
		if now.After(item.expiresAt) {
			delete(s.tickets, key)
		}
	}
}

// ---------- 授权地址 ----------

// imAuthorizeURL 按厂商拼出扫码页地址。
//
// 三家的参数名都不一样，而且都要求 redirect_uri 与后台登记的完全一致，
// 所以这里不替用户拼域名，直接用配置里的 RedirectURI。
func imAuthorizeURL(app *model.ImApp, state string) (string, error) {
	redirect := strings.TrimSpace(app.RedirectURI)
	if redirect == "" {
		return "", fmt.Errorf("该应用没有配置回调地址（RedirectURI）")
	}
	switch app.Provider {
	case channelWecom:
		if strings.TrimSpace(app.AgentID) == "" {
			return "", fmt.Errorf("企业微信扫码登录需要填 AgentID")
		}
		// 企微的 PC 扫码走独立域名 login.work.weixin.qq.com
		return "https://login.work.weixin.qq.com/wwlogin/sso/login?login_type=CorpApp" +
			"&appid=" + url.QueryEscape(app.CorpID) +
			"&agentid=" + url.QueryEscape(app.AgentID) +
			"&redirect_uri=" + url.QueryEscape(redirect) +
			"&state=" + url.QueryEscape(state), nil
	case channelDingTalk:
		return "https://login.dingtalk.com/oauth2/auth?response_type=code&scope=openid" +
			"&prompt=consent" +
			"&client_id=" + url.QueryEscape(app.CorpID) +
			"&redirect_uri=" + url.QueryEscape(redirect) +
			"&state=" + url.QueryEscape(state), nil
	case channelFeishu:
		base := strings.TrimRight(strings.TrimSpace(app.BaseURL), "/")
		if base == "" {
			base = feishuDefaultBase
		}
		return base + "/open-apis/authen/v1/index?app_id=" + url.QueryEscape(app.CorpID) +
			"&redirect_uri=" + url.QueryEscape(redirect) +
			"&state=" + url.QueryEscape(state), nil
	}
	return "", fmt.Errorf("不支持的 IM 类型 %s", app.Provider)
}

// ---------- code 换 IM 用户标识 ----------

// imResolveLoginUser 用授权码换出 IM 侧的用户标识。
//
// 返回的标识必须与「组织同步」落进 im_accounts 的那个一致，否则登录时找不到人。
// 钉钉这里有个真实的坑：扫码登录拿到的是 unionId，而通讯录同步用的是 userid，
// 必须再调一次 getbyunionid 换过来（见下面的实现）。
func (h *Handler) imResolveLoginUser(ctx context.Context, app *model.ImApp, code string) (string, error) {
	base := strings.TrimRight(strings.TrimSpace(app.BaseURL), "/")
	switch app.Provider {
	case channelWecom:
		if base == "" {
			base = wecomDefaultBase
		}
		dir, err := imDirectoryFor(app.Provider, app.BaseURL)
		if err != nil {
			return "", err
		}
		token, err := dir.token(ctx, app.CorpID, app.AppSecret)
		if err != nil {
			return "", err
		}
		var out struct {
			imErrEnvelope
			UserID string `json:"userid"`
			// OpenID 外部联系人才有，企业成员拿到的是 userid
			OpenID string `json:"openid"`
		}
		target := base + "/cgi-bin/auth/getuserinfo?access_token=" + url.QueryEscape(token) +
			"&code=" + url.QueryEscape(code)
		if err := imGetJSON(ctx, target, "", &out); err != nil {
			return "", err
		}
		if err := out.check("企业微信"); err != nil {
			return "", err
		}
		if out.UserID == "" {
			// 非企业成员（外部联系人/微信用户）扫码：平台里不会有对应账号，直接说清楚
			return "", fmt.Errorf("这个码不是企业成员扫的（没有返回 userid）")
		}
		return out.UserID, nil

	case channelFeishu:
		if base == "" {
			base = feishuDefaultBase
		}
		dir, err := imDirectoryFor(app.Provider, app.BaseURL)
		if err != nil {
			return "", err
		}
		appToken, err := dir.token(ctx, app.CorpID, app.AppSecret)
		if err != nil {
			return "", err
		}
		var out struct {
			Code int    `json:"code"`
			Msg  string `json:"msg"`
			Data struct {
				OpenID string `json:"open_id"`
				Name   string `json:"name"`
			} `json:"data"`
		}
		body := map[string]any{"grant_type": "authorization_code", "code": code}
		if err := imPostJSON(ctx, base+"/open-apis/authen/v1/access_token", appToken,
			body, &out); err != nil {
			return "", err
		}
		if out.Code != 0 {
			return "", fmt.Errorf("飞书返回 code=%d: %s", out.Code, out.Msg)
		}
		if out.Data.OpenID == "" {
			return "", fmt.Errorf("飞书没有返回 open_id")
		}
		return out.Data.OpenID, nil

	case channelDingTalk:
		// 钉钉扫码登录走新版 api.dingtalk.com，与通讯录的 oapi 不是一个域名；
		// BaseURL 只覆盖 oapi，这里单独留一个默认值
		loginBase := "https://api.dingtalk.com"
		if base != "" {
			loginBase = base
		}
		var tokenOut struct {
			AccessToken string `json:"accessToken"`
			// 新版接口出错时给的是 HTTP 4xx + {code,message}，imDoJSON 已经带出来了
			Code    string `json:"code"`
			Message string `json:"message"`
		}
		body := map[string]any{
			"clientId": app.CorpID, "clientSecret": app.AppSecret,
			"code": code, "grantType": "authorization_code",
		}
		if err := imPostJSON(ctx, loginBase+"/v1.0/oauth2/userAccessToken", "",
			body, &tokenOut); err != nil {
			return "", err
		}
		if tokenOut.AccessToken == "" {
			return "", fmt.Errorf("钉钉没有返回 userAccessToken: %s %s",
				tokenOut.Code, tokenOut.Message)
		}
		var me struct {
			UnionID string `json:"unionId"`
			Nick    string `json:"nick"`
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet,
			loginBase+"/v1.0/contact/users/me", nil)
		if err != nil {
			return "", err
		}
		req.Header.Set("x-acs-dingtalk-access-token", tokenOut.AccessToken)
		if err := imDoJSON(req, &me); err != nil {
			return "", err
		}
		if me.UnionID == "" {
			return "", fmt.Errorf("钉钉没有返回 unionId")
		}
		// 关键一步：unionId 换 userid，否则与通讯录同步落的 userid 对不上，永远找不到人
		return h.dingtalkUserIDByUnionID(ctx, app, me.UnionID)
	}
	return "", fmt.Errorf("不支持的 IM 类型 %s", app.Provider)
}

// dingtalkUserIDByUnionID 把扫码拿到的 unionId 换成通讯录里的 userid
func (h *Handler) dingtalkUserIDByUnionID(ctx context.Context, app *model.ImApp,
	unionID string) (string, error) {
	dir, err := imDirectoryFor(app.Provider, app.BaseURL)
	if err != nil {
		return "", err
	}
	token, err := dir.token(ctx, app.CorpID, app.AppSecret)
	if err != nil {
		return "", err
	}
	base := strings.TrimRight(strings.TrimSpace(app.BaseURL), "/")
	if base == "" {
		base = dingtalkDefaultBase
	}
	var out struct {
		imErrEnvelope
		Result struct {
			UserID string `json:"userid"`
		} `json:"result"`
	}
	target := base + "/topapi/user/getbyunionid?access_token=" + url.QueryEscape(token)
	if err := imPostJSON(ctx, target, "", map[string]any{"unionid": unionID}, &out); err != nil {
		return "", err
	}
	if err := out.check("钉钉"); err != nil {
		return "", err
	}
	if out.Result.UserID == "" {
		return "", fmt.Errorf("钉钉没能把 unionId 换成 userid")
	}
	return out.Result.UserID, nil
}

// ---------- 接口 ----------

// ListImLoginProviders 登录页要用，免登录。只给出最少信息：id、名字、类型。
func (h *Handler) ListImLoginProviders(c *gin.Context) {
	var apps []model.ImApp
	if err := h.DB.Where("enabled = ? AND login_enabled = ?", true, true).
		Order("id asc").Find(&apps).Error; err != nil {
		response.Error(c, "查询失败")
		return
	}
	items := make([]gin.H, 0, len(apps))
	for _, app := range apps {
		items = append(items, gin.H{
			"id": app.ID, "name": app.Name, "provider": app.Provider,
		})
	}
	response.OK(c, items)
}

// ImAuthorize 生成授权地址，免登录
func (h *Handler) ImAuthorize(c *gin.Context) {
	raw := strings.TrimSpace(c.Query("appId"))
	id, err := strconv.Atoi(raw)
	if err != nil || id <= 0 {
		response.BadRequest(c, "appId 不合法")
		return
	}
	var app model.ImApp
	if err := h.DB.First(&app, id).Error; err != nil {
		response.NotFound(c, "IM 应用不存在")
		return
	}
	if !app.Enabled || !app.LoginEnabled {
		response.BadRequest(c, "该应用没有开启扫码登录")
		return
	}

	state, err := h.imLogins.putState(app.ID)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	target, err := imAuthorizeURL(&app, state)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.OK(c, gin.H{
		"authorizeUrl": target, "state": state,
		"expiresIn": int(imStateTTL.Seconds()),
	})
}

// imLoginFailRedirect 失败时也把人送回登录页，并带上原因。
// 直接返回 JSON 会让用户停在一个白页上，不知道发生了什么。
func imLoginFailRedirect(c *gin.Context, redirect, reason string) {
	if redirect == "" {
		response.BadRequest(c, reason)
		return
	}
	sep := "?"
	if strings.Contains(redirect, "?") {
		sep = "&"
	}
	c.Redirect(http.StatusFound, redirect+sep+"imError="+url.QueryEscape(reason))
}

// ImLoginCallback 厂商回调，免登录
func (h *Handler) ImLoginCallback(c *gin.Context) {
	state := strings.TrimSpace(c.Query("state"))
	code := strings.TrimSpace(c.Query("code"))
	// 企微用 auth_code，钉钉/飞书用 code
	if code == "" {
		code = strings.TrimSpace(c.Query("auth_code"))
	}

	saved, ok := h.imLogins.takeState(state)
	if !ok {
		// state 不认识或已过期：可能是重放，也可能是用户扫得太慢
		response.BadRequest(c, "登录请求已失效，请回到登录页重新扫码")
		return
	}
	var app model.ImApp
	if err := h.DB.First(&app, saved.appID).Error; err != nil {
		response.BadRequest(c, "IM 应用不存在")
		return
	}
	redirect := strings.TrimSpace(app.LoginRedirect)
	if code == "" {
		imLoginFailRedirect(c, redirect, "IM 没有返回授权码")
		return
	}
	if !app.Enabled || !app.LoginEnabled {
		imLoginFailRedirect(c, redirect, "该应用没有开启扫码登录")
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), imDirTimeout)
	defer cancel()
	imUserID, err := h.imResolveLoginUser(ctx, &app, code)
	if err != nil {
		imLoginFailRedirect(c, redirect, "换取 IM 身份失败: "+err.Error())
		return
	}

	var binding model.ImAccount
	if err := h.DB.Where("provider = ? AND im_user_id = ?", app.Provider, imUserID).
		First(&binding).Error; err != nil {
		// 没绑定就不给进：扫码登录只认「已经同步/绑定过」的人，
		// 不在这里顺手建账号 —— 那等于让任何企业成员自助开通平台权限
		imLoginFailRedirect(c, redirect,
			"这个 IM 账号还没有绑定平台账号，请先让管理员执行组织同步")
		return
	}
	var user model.User
	if err := h.DB.First(&user, binding.UserID).Error; err != nil {
		imLoginFailRedirect(c, redirect, "绑定的平台账号已不存在，请联系管理员")
		return
	}
	if user.Status != 1 {
		imLoginFailRedirect(c, redirect, "平台账号已停用")
		return
	}

	ticket, err := h.imLogins.putTicket(user.ID, app.ID)
	if err != nil {
		imLoginFailRedirect(c, redirect, err.Error())
		return
	}
	if redirect == "" {
		// 没配前端地址时退而给 JSON，至少让人能看到 ticket 自己调一次
		response.OK(c, gin.H{"ticket": ticket, "expiresIn": int(imTicketTTL.Seconds())})
		return
	}
	sep := "?"
	if strings.Contains(redirect, "?") {
		sep = "&"
	}
	c.Redirect(http.StatusFound, redirect+sep+"imTicket="+url.QueryEscape(ticket))
}

type imExchangeReq struct {
	Ticket string `json:"ticket"`
}

// ImLoginExchange 用一次性 ticket 换 JWT，免登录。
// 响应体与普通登录一致，前端可以共用同一套处理。
func (h *Handler) ImLoginExchange(c *gin.Context) {
	var req imExchangeReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "参数错误")
		return
	}
	item, ok := h.imLogins.takeTicket(strings.TrimSpace(req.Ticket))
	if !ok {
		response.Unauthorized(c, "登录凭据已失效，请重新扫码")
		return
	}
	var user model.User
	if err := h.DB.First(&user, item.userID).Error; err != nil {
		response.Unauthorized(c, "账号不存在")
		return
	}
	// 从扫码到换令牌之间账号可能被停用，这里再查一次
	if user.Status != 1 {
		response.Forbidden(c, "账号已停用")
		return
	}

	ttl := time.Duration(h.Cfg.TokenTTLHour) * time.Hour
	token, expiresAt, err := middleware.IssueToken(h.Cfg.JWTSecret, &user, ttl)
	if err != nil {
		response.Error(c, "签发令牌失败")
		return
	}
	now := time.Now()
	h.DB.Model(&model.User{}).Where("id = ?", user.ID).
		Updates(map[string]any{"last_login_at": &now})

	response.OK(c, gin.H{
		"token":     token,
		"expiresAt": expiresAt.Unix(),
		"user": gin.H{
			"id": user.ID, "username": user.Username, "nickname": user.Nickname,
		},
		// 说清楚这次是怎么进来的，便于前端提示「已通过 IM 登录」
		"loginBy": "im",
	})
}
