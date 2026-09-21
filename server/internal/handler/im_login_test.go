package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"ops-platform/server/internal/config"
	"ops-platform/server/internal/model"
)

func TestImLoginStoreSingleUseAndExpiry(t *testing.T) {
	store := newImLoginStore()

	state, err := store.putState(7)
	if err != nil {
		t.Fatalf("生成 state 失败: %v", err)
	}
	got, ok := store.takeState(state)
	if !ok || got.appID != 7 {
		t.Fatalf("取 state 失败: %+v %v", got, ok)
	}
	// 一次性：同一个 state 不能再用（防重放的关键）
	if _, ok := store.takeState(state); ok {
		t.Fatal("state 被重复使用了")
	}
	if _, ok := store.takeState("not-exist"); ok {
		t.Fatal("不存在的 state 居然通过了")
	}

	// 过期的也不能用
	store.states["stale"] = imLoginState{appID: 1, expiresAt: time.Now().Add(-time.Second)}
	if _, ok := store.takeState("stale"); ok {
		t.Fatal("过期 state 被接受")
	}

	ticket, err := store.putTicket(3, 7)
	if err != nil {
		t.Fatalf("生成 ticket 失败: %v", err)
	}
	item, ok := store.takeTicket(ticket)
	if !ok || item.userID != 3 {
		t.Fatalf("取 ticket 失败: %+v", item)
	}
	if _, ok := store.takeTicket(ticket); ok {
		t.Fatal("ticket 被重复使用了")
	}
	store.tickets["stale"] = imLoginTicket{userID: 1, expiresAt: time.Now().Add(-time.Second)}
	if _, ok := store.takeTicket("stale"); ok {
		t.Fatal("过期 ticket 被接受")
	}

	// state 不会无限堆积：过期的会在写入时被清掉
	for i := 0; i < 5; i++ {
		store.states["old"+string(rune('a'+i))] = imLoginState{
			expiresAt: time.Now().Add(-time.Minute),
		}
	}
	if _, err := store.putState(1); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	if len(store.states) != 1 {
		t.Fatalf("过期 state 没被清理: %d", len(store.states))
	}
}

func TestImAuthorizeURLPerProvider(t *testing.T) {
	base := model.ImApp{
		Provider: channelWecom, CorpID: "corp-1", AgentID: "1000002",
		RedirectURI: "https://ops.corp/api/v1/auth/im/callback",
	}
	got, err := imAuthorizeURL(&base, "st-1")
	if err != nil {
		t.Fatalf("企微授权地址失败: %v", err)
	}
	for _, want := range []string{"login.work.weixin.qq.com", "appid=corp-1",
		"agentid=1000002", "state=st-1", "redirect_uri=https%3A%2F%2Fops.corp"} {
		if !strings.Contains(got, want) {
			t.Fatalf("企微授权地址缺少 %s: %s", want, got)
		}
	}
	// 企微少了 AgentID 就拼不出可用的扫码地址，要当场报错而不是给个跑不通的链接
	noAgent := base
	noAgent.AgentID = ""
	if _, err := imAuthorizeURL(&noAgent, "st"); err == nil ||
		!strings.Contains(err.Error(), "AgentID") {
		t.Fatalf("缺 AgentID 应该报错: %v", err)
	}
	noRedirect := base
	noRedirect.RedirectURI = ""
	if _, err := imAuthorizeURL(&noRedirect, "st"); err == nil {
		t.Fatal("缺回调地址应该报错")
	}

	ding := model.ImApp{Provider: channelDingTalk, CorpID: "appkey",
		RedirectURI: "https://ops.corp/api/v1/auth/im/callback"}
	got, err = imAuthorizeURL(&ding, "st-2")
	if err != nil {
		t.Fatalf("钉钉授权地址失败: %v", err)
	}
	for _, want := range []string{"login.dingtalk.com", "client_id=appkey",
		"response_type=code", "state=st-2"} {
		if !strings.Contains(got, want) {
			t.Fatalf("钉钉授权地址缺少 %s: %s", want, got)
		}
	}

	feishu := model.ImApp{Provider: channelFeishu, CorpID: "cli_x",
		RedirectURI: "https://ops.corp/api/v1/auth/im/callback", BaseURL: "https://fake.local"}
	got, err = imAuthorizeURL(&feishu, "st-3")
	if err != nil {
		t.Fatalf("飞书授权地址失败: %v", err)
	}
	// BaseURL 要能覆盖，否则对不着私有网关也没法用假上游验证
	if !strings.HasPrefix(got, "https://fake.local/open-apis/authen/v1/index") {
		t.Fatalf("飞书授权地址没用上 BaseURL: %s", got)
	}
	if !strings.Contains(got, "app_id=cli_x") || !strings.Contains(got, "state=st-3") {
		t.Fatalf("飞书授权参数不对: %s", got)
	}
}

// newFakeImLogin 假上游：企微 code→userid、飞书 code→open_id、
// 钉钉 code→unionId 再换 userid（这一步是真实存在的坑）
func newFakeImLogin(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	// 企微
	mux.HandleFunc("/cgi-bin/gettoken", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"errcode":0,"access_token":"tok"}`))
	})
	mux.HandleFunc("/cgi-bin/auth/getuserinfo", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("code") {
		case "good":
			_, _ = w.Write([]byte(`{"errcode":0,"userid":"zhangsan"}`))
		case "outsider":
			// 外部联系人扫码：只有 openid，没有 userid
			_, _ = w.Write([]byte(`{"errcode":0,"openid":"oXXXX"}`))
		default:
			_, _ = w.Write([]byte(`{"errcode":40029,"errmsg":"invalid code"}`))
		}
	})
	// 飞书
	mux.HandleFunc("/open-apis/auth/v3/tenant_access_token/internal",
		func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"code":0,"tenant_access_token":"t-feishu"}`))
		})
	mux.HandleFunc("/open-apis/authen/v1/access_token", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["code"] != "good" {
			_, _ = w.Write([]byte(`{"code":20021,"msg":"code invalid"}`))
			return
		}
		_, _ = w.Write([]byte(`{"code":0,"data":{"open_id":"ou_1","name":"张三"}}`))
	})
	// 钉钉
	mux.HandleFunc("/gettoken", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"errcode":0,"access_token":"t-ding"}`))
	})
	mux.HandleFunc("/v1.0/oauth2/userAccessToken", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["code"] != "good" {
			_, _ = w.Write([]byte(`{"code":"invalid_code","message":"code 无效"}`))
			return
		}
		_, _ = w.Write([]byte(`{"accessToken":"ua-1","expireIn":7200}`))
	})
	mux.HandleFunc("/v1.0/contact/users/me", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-acs-dingtalk-access-token") != "ua-1" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"code":"unauthorized"}`))
			return
		}
		_, _ = w.Write([]byte(`{"unionId":"union-1","nick":"张三"}`))
	})
	mux.HandleFunc("/topapi/user/getbyunionid", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["unionid"] != "union-1" {
			_, _ = w.Write([]byte(`{"errcode":60121,"errmsg":"user not found"}`))
			return
		}
		_, _ = w.Write([]byte(`{"errcode":0,"result":{"userid":"zhangsan"}}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestImResolveLoginUserPerProvider(t *testing.T) {
	srv := newFakeImLogin(t)
	h, _ := newImTestHandler(t)
	ctx := context.Background()

	wecom := model.ImApp{Provider: channelWecom, CorpID: "c", AppSecret: "s", BaseURL: srv.URL}
	got, err := h.imResolveLoginUser(ctx, &wecom, "good")
	if err != nil || got != "zhangsan" {
		t.Fatalf("企微换 userid 失败: %q %v", got, err)
	}
	// 外部联系人扫码拿不到 userid，要说清楚而不是抛一个含糊的错
	if _, err := h.imResolveLoginUser(ctx, &wecom, "outsider"); err == nil ||
		!strings.Contains(err.Error(), "企业成员") {
		t.Fatalf("外部联系人应该被明确拒绝: %v", err)
	}
	if _, err := h.imResolveLoginUser(ctx, &wecom, "bad"); err == nil ||
		!strings.Contains(err.Error(), "40029") {
		t.Fatalf("无效 code 的上游原话没带出来: %v", err)
	}

	feishu := model.ImApp{Provider: channelFeishu, CorpID: "c", AppSecret: "s", BaseURL: srv.URL}
	got, err = h.imResolveLoginUser(ctx, &feishu, "good")
	if err != nil || got != "ou_1" {
		t.Fatalf("飞书换 open_id 失败: %q %v", got, err)
	}
	if _, err := h.imResolveLoginUser(ctx, &feishu, "bad"); err == nil ||
		!strings.Contains(err.Error(), "20021") {
		t.Fatalf("飞书错误码没带出来: %v", err)
	}

	// 钉钉：扫码给的是 unionId，必须再换成通讯录里的 userid，
	// 否则与同步落的绑定永远对不上
	ding := model.ImApp{Provider: channelDingTalk, CorpID: "c", AppSecret: "s", BaseURL: srv.URL}
	got, err = h.imResolveLoginUser(ctx, &ding, "good")
	if err != nil || got != "zhangsan" {
		t.Fatalf("钉钉 unionId 没换成 userid: %q %v", got, err)
	}
	if _, err := h.imResolveLoginUser(ctx, &ding, "bad"); err == nil {
		t.Fatal("无效 code 应该报错")
	}
}

// newLoginEngine 挂上四个免鉴权接口
func newLoginEngine(t *testing.T, h *Handler) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.GET("/public/im-logins", h.ListImLoginProviders)
	engine.GET("/auth/im/authorize", h.ImAuthorize)
	engine.GET("/auth/im/callback", h.ImLoginCallback)
	engine.POST("/auth/im/exchange", h.ImLoginExchange)
	return engine
}

func seedLoginApp(t *testing.T, h *Handler, base string, loginEnabled bool) model.ImApp {
	t.Helper()
	app := model.ImApp{
		Name: "假企微", Provider: channelWecom, CorpID: "corp", AppSecret: "secret",
		BaseURL: base, AgentID: "1000002", RootDeptID: "1", TargetCompanyID: 1,
		RedirectURI:   "http://127.0.0.1:8080/api/v1/auth/im/callback",
		LoginRedirect: "http://127.0.0.1:5173/", Enabled: true, LoginEnabled: loginEnabled,
	}
	if err := h.DB.Create(&app).Error; err != nil {
		t.Fatalf("建应用失败: %v", err)
	}
	if !loginEnabled {
		h.DB.Model(&model.ImApp{}).Where("id = ?", app.ID).
			Updates(map[string]any{"login_enabled": false})
	}
	return app
}

func TestImLoginFullFlow(t *testing.T) {
	srv := newFakeImLogin(t)
	h, _ := newImTestHandler(t)
	h.Cfg = &config.Config{JWTSecret: []byte("test-secret-please-change"), TokenTTLHour: 12}
	engine := newLoginEngine(t, h)
	app := seedLoginApp(t, h, srv.URL, true)

	user := model.User{Username: "zhangsan", PasswordHash: "x", Nickname: "张三", Status: 1}
	h.DB.Create(&user)
	h.DB.Create(&model.ImAccount{AppID: app.ID, Provider: channelWecom,
		ImUserID: "zhangsan", UserID: user.ID, Username: user.Username})

	// 1. 登录页拿到可用的登录方式
	body := getModelJSON(t, engine, "/public/im-logins")
	list, _ := body["data"].([]any)
	if len(list) != 1 {
		t.Fatalf("登录方式数量不对: %+v", body["data"])
	}
	first, _ := list[0].(map[string]any)
	if first["provider"] != channelWecom {
		t.Fatalf("登录方式内容不对: %+v", first)
	}
	// 免登录接口不该泄露凭据
	if raw, _ := json.Marshal(body); strings.Contains(string(raw), "secret") ||
		strings.Contains(string(raw), "corp") {
		t.Fatalf("登录方式列表泄露了配置: %s", raw)
	}

	// 2. 取授权地址
	body = getModelJSON(t, engine, "/auth/im/authorize?appId="+itoa(app.ID))
	data, _ := body["data"].(map[string]any)
	state, _ := data["state"].(string)
	if state == "" || !strings.Contains(data["authorizeUrl"].(string), "state="+state) {
		t.Fatalf("授权地址不对: %+v", data)
	}

	// 3. 厂商回调 → 302 带 ticket 回前端
	req := httptest.NewRequest(http.MethodGet,
		"/auth/im/callback?code=good&state="+state, nil)
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("回调应该 302，实际 %d: %s", rec.Code, rec.Body.String())
	}
	location := rec.Header().Get("Location")
	if !strings.HasPrefix(location, "http://127.0.0.1:5173/?imTicket=") {
		t.Fatalf("跳转地址不对: %s", location)
	}
	// JWT 绝不能出现在 URL 里
	if strings.Contains(location, "eyJ") || strings.Contains(location, "token=") {
		t.Fatalf("跳转地址里出现了令牌: %s", location)
	}
	ticket := strings.TrimPrefix(location, "http://127.0.0.1:5173/?imTicket=")

	// 4. ticket 换 JWT
	code, body := postModelJSON(t, engine, "/auth/im/exchange", `{"ticket":"`+ticket+`"}`)
	if code != http.StatusOK || body["code"] != float64(0) {
		t.Fatalf("换令牌失败: %d %+v", code, body)
	}
	data, _ = body["data"].(map[string]any)
	if token, _ := data["token"].(string); len(token) < 20 {
		t.Fatalf("没拿到令牌: %+v", data)
	}
	if data["loginBy"] != "im" {
		t.Fatalf("没标明登录方式: %+v", data)
	}
	userData, _ := data["user"].(map[string]any)
	if userData["username"] != "zhangsan" {
		t.Fatalf("登录的人不对: %+v", userData)
	}
	// 登录时间要落库
	var after model.User
	h.DB.First(&after, user.ID)
	if after.LastLoginAt == nil {
		t.Fatal("last_login_at 没更新")
	}

	// 5. ticket 一次性
	_, body = postModelJSON(t, engine, "/auth/im/exchange", `{"ticket":"`+ticket+`"}`)
	if body["code"] == float64(0) {
		t.Fatalf("ticket 被重复使用: %+v", body)
	}
	// 6. state 一次性：同一个 state 再回调一次必须失败（防重放）
	req = httptest.NewRequest(http.MethodGet, "/auth/im/callback?code=good&state="+state, nil)
	rec = httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	if rec.Code == http.StatusFound {
		t.Fatal("同一个 state 被重复使用")
	}
}

func TestImLoginRejectsUnboundAndDisabled(t *testing.T) {
	srv := newFakeImLogin(t)
	h, _ := newImTestHandler(t)
	h.Cfg = &config.Config{JWTSecret: []byte("s"), TokenTTLHour: 12}
	engine := newLoginEngine(t, h)
	app := seedLoginApp(t, h, srv.URL, true)

	newState := func() string {
		body := getModelJSON(t, engine, "/auth/im/authorize?appId="+itoa(app.ID))
		data, _ := body["data"].(map[string]any)
		state, _ := data["state"].(string)
		return state
	}
	callback := func(state string) (int, string) {
		req := httptest.NewRequest(http.MethodGet, "/auth/im/callback?code=good&state="+state, nil)
		rec := httptest.NewRecorder()
		engine.ServeHTTP(rec, req)
		return rec.Code, rec.Header().Get("Location")
	}

	// 没绑定的人：不给进，也不顺手建账号
	code, location := callback(newState())
	if code != http.StatusFound || !strings.Contains(location, "imError=") {
		t.Fatalf("未绑定应该带错误跳回登录页: %d %s", code, location)
	}
	if !strings.Contains(location, "%E7%BB%91%E5%AE%9A") { // “绑定”
		t.Fatalf("错误原因不明确: %s", location)
	}
	var users int64
	h.DB.Model(&model.User{}).Count(&users)
	if users != 0 {
		t.Fatalf("扫码登录不该自动建账号: %d", users)
	}

	// 绑定了但账号被停用：也不给进
	user := model.User{Username: "zhangsan", PasswordHash: "x", Status: 1}
	h.DB.Create(&user)
	h.DB.Model(&model.User{}).Where("id = ?", user.ID).Updates(map[string]any{"status": 0})
	h.DB.Create(&model.ImAccount{AppID: app.ID, Provider: channelWecom,
		ImUserID: "zhangsan", UserID: user.ID, Username: user.Username})
	code, location = callback(newState())
	if code != http.StatusFound || !strings.Contains(location, "imError=") {
		t.Fatalf("停用账号应该被拒: %d %s", code, location)
	}

	// 停用扫码登录后连授权地址都不给
	h.DB.Model(&model.ImApp{}).Where("id = ?", app.ID).
		Updates(map[string]any{"login_enabled": false})
	body := getModelJSON(t, engine, "/auth/im/authorize?appId="+itoa(app.ID))
	if body["code"] == float64(0) {
		t.Fatalf("没开扫码登录不该给授权地址: %+v", body)
	}
	body = getModelJSON(t, engine, "/public/im-logins")
	list, _ := body["data"].([]any)
	if len(list) != 0 {
		t.Fatalf("没开扫码登录不该出现在登录方式里: %+v", list)
	}
}

func TestImLoginExchangeRejectsDisabledBetweenSteps(t *testing.T) {
	h, _ := newImTestHandler(t)
	h.Cfg = &config.Config{JWTSecret: []byte("s"), TokenTTLHour: 12}
	engine := newLoginEngine(t, h)

	user := model.User{Username: "someone", PasswordHash: "x", Status: 1}
	h.DB.Create(&user)
	ticket, err := h.imLogins.putTicket(user.ID, 1)
	if err != nil {
		t.Fatalf("造 ticket 失败: %v", err)
	}
	// 扫码之后、换令牌之前被停用：这里必须再查一次状态
	h.DB.Model(&model.User{}).Where("id = ?", user.ID).Updates(map[string]any{"status": 0})

	_, body := postModelJSON(t, engine, "/auth/im/exchange", `{"ticket":"`+ticket+`"}`)
	if body["code"] == float64(0) {
		t.Fatalf("中途被停用还能换到令牌: %+v", body)
	}
	if msg, _ := body["msg"].(string); !strings.Contains(msg, "停用") {
		t.Fatalf("报错不明确: %q", msg)
	}
}

func TestImAppReqValidatesLoginFields(t *testing.T) {
	enabled := true
	base := imAppReq{Name: "a", Provider: channelWecom, CorpID: "c", TargetCompanyID: 1,
		LoginEnabled: &enabled}

	// 开了扫码登录但没回调地址
	local := base
	if err := local.normalize(); err == nil || !strings.Contains(err.Error(), "回调地址") {
		t.Fatalf("应该要求回调地址: %v", err)
	}
	// 回调地址写错（没指向 callback）
	local = base
	local.RedirectURI = "https://ops.corp/login"
	local.AgentID = "1"
	if err := local.normalize(); err == nil || !strings.Contains(err.Error(), "/auth/im/callback") {
		t.Fatalf("应该校验回调路径: %v", err)
	}
	// 企微少 AgentID
	local = base
	local.RedirectURI = "https://ops.corp/api/v1/auth/im/callback"
	if err := local.normalize(); err == nil || !strings.Contains(err.Error(), "AgentID") {
		t.Fatalf("企微应该要求 AgentID: %v", err)
	}
	// 齐全了就通过
	local = base
	local.RedirectURI = "https://ops.corp/api/v1/auth/im/callback"
	local.AgentID = "1000002"
	local.LoginRedirect = "https://ops.corp/"
	if err := local.normalize(); err != nil {
		t.Fatalf("合法配置被拒: %v", err)
	}
	// 没开扫码登录时不强求这些字段
	off := false
	local = imAppReq{Name: "a", Provider: channelWecom, CorpID: "c", TargetCompanyID: 1,
		LoginEnabled: &off}
	if err := local.normalize(); err != nil {
		t.Fatalf("不开扫码登录时不该强求回调地址: %v", err)
	}
}
