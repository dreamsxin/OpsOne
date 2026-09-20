package handler

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"ops-platform/server/internal/model"
)

func sampleIMAlert() model.Alert {
	return model.Alert{
		ID: 7, Title: "磁盘将满：web-01", Summary: "/ 使用率 91%，阈值 85%",
		Severity: "critical", Status: "firing", SourceName: "告警规则",
		Value: "91", Count: 3,
		Labels:     `{"host":"web-01","env":"prod"}`,
		LastSeenAt: time.Date(2026, 9, 20, 21, 30, 0, 0, time.Local),
	}
}

func TestIMAlertText(t *testing.T) {
	text := imAlertText(sampleIMAlert())
	for _, want := range []string{
		"[严重] 磁盘将满：web-01", "级别：critical", "（进行中）", "来源：告警规则",
		"详情：/ 使用率 91%，阈值 85%", "当前值：91", "标签：env=prod host=web-01",
		"累计次数：3", "最近发生：2026-09-20 21:30:00",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("正文缺少 %q:\n%s", want, text)
		}
	}
	// 标签按键排序，保证同一条告警每次发出来都一样
	if strings.Index(text, "env=prod") > strings.Index(text, "host=web-01") {
		t.Fatalf("标签应按键排序:\n%s", text)
	}

	// 恢复通知要能看出是恢复
	resolved := sampleIMAlert()
	resolved.Status = "resolved"
	if !strings.Contains(imAlertText(resolved), "已恢复") {
		t.Fatal("resolved 应显示已恢复")
	}

	// 字段缺失时不能出现空标签行
	bare := model.Alert{Title: "只有标题", Severity: "warning"}
	text = imAlertText(bare)
	if strings.Contains(text, "详情：") || strings.Contains(text, "标签：") ||
		strings.Contains(text, "当前值：") || strings.Contains(text, "累计次数：") {
		t.Fatalf("字段为空时不该输出对应行:\n%s", text)
	}
	if !strings.HasPrefix(text, "[警告] ") {
		t.Fatalf("级别前缀不对: %q", text)
	}

	// 标签 JSON 坏了也不能把整条消息毁掉
	broken := model.Alert{Title: "x", Labels: "{not json"}
	if strings.Contains(imAlertText(broken), "标签：") {
		t.Fatal("标签解析失败时应直接省略这一行")
	}
}

func TestIMPayloadShapes(t *testing.T) {
	// 企业微信：mentioned_list 收 userid，@所有人追加 @all
	payload := imPayload(model.NotifyChannel{
		Type: channelWecom, MentionList: "zhangsan, lisi", MentionAll: true,
	}, "内容")
	if payload["msgtype"] != "text" {
		t.Fatalf("企微 msgtype 不对: %+v", payload)
	}
	text := payload["text"].(map[string]any)
	if text["content"] != "内容" {
		t.Fatalf("企微正文不对: %+v", text)
	}
	list := text["mentioned_list"].([]string)
	if len(list) != 3 || list[0] != "zhangsan" || list[2] != "@all" {
		t.Fatalf("企微 @ 名单不对: %+v", list)
	}
	// 不 @ 人时不该带空字段
	payload = imPayload(model.NotifyChannel{Type: channelWecom}, "内容")
	if _, ok := payload["text"].(map[string]any)["mentioned_list"]; ok {
		t.Fatal("没有 @ 名单时不该带 mentioned_list")
	}

	// 钉钉：atMobiles + isAtAll，且手机号要出现在正文里才会高亮
	payload = imPayload(model.NotifyChannel{
		Type: channelDingTalk, MentionList: "13800000000", MentionAll: false,
	}, "内容")
	at := payload["at"].(map[string]any)
	if at["isAtAll"] != false {
		t.Fatalf("钉钉 isAtAll 不对: %+v", at)
	}
	if mobiles := at["atMobiles"].([]string); len(mobiles) != 1 || mobiles[0] != "13800000000" {
		t.Fatalf("钉钉 atMobiles 不对: %+v", at)
	}
	content := payload["text"].(map[string]any)["content"].(string)
	if !strings.Contains(content, "@13800000000") {
		t.Fatalf("钉钉正文应带上被 @ 的手机号: %q", content)
	}

	// 飞书：只支持 @所有人，写成标签放正文开头
	payload = imPayload(model.NotifyChannel{Type: channelFeishu, MentionAll: true}, "内容")
	if payload["msg_type"] != "text" {
		t.Fatalf("飞书 msg_type 不对: %+v", payload)
	}
	got := payload["content"].(map[string]any)["text"].(string)
	if !strings.HasPrefix(got, `<at user_id="all">`) || !strings.HasSuffix(got, "内容") {
		t.Fatalf("飞书 @所有人写法不对: %q", got)
	}
	payload = imPayload(model.NotifyChannel{Type: channelFeishu}, "内容")
	if payload["content"].(map[string]any)["text"] != "内容" {
		t.Fatal("飞书不 @ 人时正文应原样")
	}
}

func TestDingtalkSignedURL(t *testing.T) {
	const secret = "SEC0123456789"
	ts := int64(1789907000123)
	raw := "https://oapi.dingtalk.com/robot/send?access_token=abc"

	signed := dingtalkSignedURL(raw, secret, ts)
	parsed, err := url.Parse(signed)
	if err != nil {
		t.Fatalf("签名后的地址不合法: %v", err)
	}
	query := parsed.Query()
	if query.Get("access_token") != "abc" {
		t.Fatal("原有参数被弄丢了")
	}
	if query.Get("timestamp") != strconv.FormatInt(ts, 10) {
		t.Fatalf("timestamp 不对: %s", query.Get("timestamp"))
	}

	// 独立算一遍：密钥是 secret，待签串是 "<timestamp>\n<secret>"
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(strconv.FormatInt(ts, 10) + "\n" + secret))
	want := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	if query.Get("sign") != want {
		t.Fatalf("签名不对:\n实际 %s\n期望 %s", query.Get("sign"), want)
	}

	// 没配密钥就原样返回（机器人可能用关键词或 IP 白名单校验）
	if got := dingtalkSignedURL(raw, "  ", ts); got != raw {
		t.Fatalf("无密钥时不该改地址: %s", got)
	}
	// 地址本来没有 query 时要用 ? 起头
	if got := dingtalkSignedURL("https://example.com/robot", secret, ts); !strings.Contains(got, "/robot?timestamp=") {
		t.Fatalf("参数分隔符不对: %s", got)
	}
}

func TestFeishuSign(t *testing.T) {
	const secret = "feishu-secret"
	ts := int64(1789907000)

	// 飞书是把 "<timestamp>\n<secret>" 当密钥、对空串签名
	mac := hmac.New(sha256.New, []byte(strconv.FormatInt(ts, 10)+"\n"+secret))
	want := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	if got := feishuSign(secret, ts); got != want {
		t.Fatalf("飞书签名不对:\n实际 %s\n期望 %s", got, want)
	}
	// 和钉钉的算法不能混：同样的输入必须算出不同结果
	if feishuSign(secret, ts) == dingtalkSign(secret, ts) {
		t.Fatal("两家签名算法被写成一样了")
	}
}

func TestInterpretIMResponse(t *testing.T) {
	cases := []struct {
		name       string
		channel    string
		httpStatus int
		body       string
		wantErr    string
	}{
		{"企微成功", channelWecom, 200, `{"errcode":0,"errmsg":"ok"}`, ""},
		{"企微被拒", channelWecom, 200, `{"errcode":93000,"errmsg":"invalid webhook url"}`,
			"企业微信 拒收：errcode=93000 invalid webhook url"},
		{"钉钉签名过期", channelDingTalk, 200,
			`{"errcode":310000,"errmsg":"sign not match"}`, "钉钉 拒收：errcode=310000"},
		{"飞书成功", channelFeishu, 200, `{"code":0,"msg":"success"}`, ""},
		{"飞书被拒", channelFeishu, 200, `{"code":19021,"msg":"sign match fail"}`,
			"飞书 拒收：code=19021 sign match fail"},
		{"HTTP 失败带原文", channelWecom, 500, `internal error`, "HTTP 500: internal error"},
		{"HTTP 失败无原文", channelDingTalk, 502, "", "钉钉 返回 HTTP 502"},
		// 有些自建网关会把机器人包一层，返回非 JSON 的 200，只能以状态码为准
		{"非 JSON 的 200", channelFeishu, 200, "OK", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := interpretIMResponse(tc.channel, tc.httpStatus, []byte(tc.body))
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("应判为成功，实际 %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("应判为失败，实际成功")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("错误信息应包含 %q，实际 %v", tc.wantErr, err)
			}
		})
	}
}

// TestPostIMAgainstFakeRobots 用本地假机器人验证整条发送链路：
// 报文形状、钉钉签名参数、飞书签名字段、以及「HTTP 200 + 非零 errcode 必须算失败」。
func TestPostIMAgainstFakeRobots(t *testing.T) {
	type captured struct {
		path  string
		query url.Values
		body  map[string]any
	}
	var got captured

	mux := http.NewServeMux()
	record := func(w http.ResponseWriter, r *http.Request, reply string) {
		body := map[string]any{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		got = captured{path: r.URL.Path, query: r.URL.Query(), body: body}
		fmt.Fprint(w, reply)
	}
	mux.HandleFunc("/wecom", func(w http.ResponseWriter, r *http.Request) {
		record(w, r, `{"errcode":0,"errmsg":"ok"}`)
	})
	mux.HandleFunc("/dingtalk", func(w http.ResponseWriter, r *http.Request) {
		record(w, r, `{"errcode":0,"errmsg":"ok"}`)
	})
	mux.HandleFunc("/feishu", func(w http.ResponseWriter, r *http.Request) {
		record(w, r, `{"code":0,"msg":"success"}`)
	})
	mux.HandleFunc("/kicked", func(w http.ResponseWriter, r *http.Request) {
		// 机器人被移出群：HTTP 200 但 errcode 非零
		record(w, r, `{"errcode":93000,"errmsg":"webhook url invalid"}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	h := &Handler{}
	alert := sampleIMAlert()

	status, err := h.postIM(model.NotifyChannel{
		Type: channelWecom, URL: srv.URL + "/wecom", MentionList: "zhangsan",
	}, imAlertText(alert))
	if err != nil || status != 200 {
		t.Fatalf("企微投递失败: %d / %v", status, err)
	}
	if got.body["msgtype"] != "text" {
		t.Fatalf("假机器人收到的报文不对: %+v", got.body)
	}
	content := got.body["text"].(map[string]any)["content"].(string)
	if !strings.Contains(content, "磁盘将满：web-01") {
		t.Fatalf("正文没送到: %q", content)
	}

	status, err = h.postIM(model.NotifyChannel{
		Type: channelDingTalk, URL: srv.URL + "/dingtalk?access_token=t", Secret: "SEC123",
	}, "内容")
	if err != nil || status != 200 {
		t.Fatalf("钉钉投递失败: %d / %v", status, err)
	}
	if got.query.Get("access_token") != "t" {
		t.Fatal("钉钉原有 access_token 丢了")
	}
	ts, convErr := strconv.ParseInt(got.query.Get("timestamp"), 10, 64)
	if convErr != nil {
		t.Fatalf("钉钉 timestamp 不是数字: %v", convErr)
	}
	if got.query.Get("sign") != dingtalkSign("SEC123", ts) {
		t.Fatal("钉钉签名与 timestamp 不匹配")
	}

	status, err = h.postIM(model.NotifyChannel{
		Type: channelFeishu, URL: srv.URL + "/feishu", Secret: "fs",
	}, "内容")
	if err != nil || status != 200 {
		t.Fatalf("飞书投递失败: %d / %v", status, err)
	}
	tsStr, ok := got.body["timestamp"].(string)
	if !ok {
		t.Fatalf("飞书报文里应带 timestamp: %+v", got.body)
	}
	ts, _ = strconv.ParseInt(tsStr, 10, 64)
	if got.body["sign"] != feishuSign("fs", ts) {
		t.Fatal("飞书签名与 timestamp 不匹配")
	}
	// 没配密钥就不该带签名字段
	if _, err := h.postIM(model.NotifyChannel{Type: channelFeishu, URL: srv.URL + "/feishu"}, "内容"); err != nil {
		t.Fatalf("无密钥投递失败: %v", err)
	}
	if _, exists := got.body["sign"]; exists {
		t.Fatalf("没配密钥时不该带 sign: %+v", got.body)
	}

	// 关键一条：HTTP 200 + errcode 非零必须算失败
	status, err = h.postIM(model.NotifyChannel{Type: channelWecom, URL: srv.URL + "/kicked"}, "内容")
	if status != 200 {
		t.Fatalf("状态码应为 200，实际 %d", status)
	}
	if err == nil || !strings.Contains(err.Error(), "93000") {
		t.Fatalf("errcode 非零应判为失败，实际 %v", err)
	}

	// 地址没填要立刻报错，不发请求
	if _, err := h.postIM(model.NotifyChannel{Type: channelWecom}, "内容"); err == nil {
		t.Fatal("没有地址应该报错")
	}
}

func TestValidateChannel(t *testing.T) {
	ok := []struct {
		name string
		typ  string
		req  channelReq
	}{
		{"webhook 有地址", "webhook", channelReq{URL: "http://x"}},
		{"email 有收件人", "email", channelReq{Recipients: "a@b.c"}},
		{"silent 什么都不用填", "silent", channelReq{}},
		{"企微有地址", channelWecom, channelReq{URL: "http://x"}},
		{"钉钉有地址", channelDingTalk, channelReq{URL: "http://x"}},
		{"飞书只 @所有人", channelFeishu, channelReq{URL: "http://x"}},
	}
	for _, tc := range ok {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateChannel(tc.typ, tc.req); err != nil {
				t.Fatalf("应通过，实际 %v", err)
			}
		})
	}

	bad := []struct {
		name string
		typ  string
		req  channelReq
		want string
	}{
		{"webhook 没地址", "webhook", channelReq{}, "必须填写 URL"},
		{"email 没收件人", "email", channelReq{}, "必须填写收件人"},
		{"企微没地址", channelWecom, channelReq{}, "企业微信 渠道必须填写机器人 Webhook 地址"},
		{"钉钉没地址", channelDingTalk, channelReq{}, "钉钉 渠道必须填写机器人"},
		{"飞书填了 @ 名单", channelFeishu, channelReq{URL: "http://x", MentionList: "a"}, "飞书只支持 @所有人"},
		{"类型写错", "dingding", channelReq{URL: "http://x"}, "不支持的渠道类型"},
		{"类型为空", "", channelReq{URL: "http://x"}, "不支持的渠道类型"},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			err := validateChannel(tc.typ, tc.req)
			if err == nil {
				t.Fatal("应被拒绝，实际通过")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("错误信息应包含 %q，实际 %v", tc.want, err)
			}
		})
	}
}
