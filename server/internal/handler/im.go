package handler

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"ops-platform/server/internal/model"
)

// 群机器人渠道类型。
//
// 为什么不复用 webhook：这三家都要求特定的 JSON 结构（msgtype/text.content 之类），
// 而且**HTTP 200 不代表发成功**——机器人被停用、超频、签名不对都会返回 200
// 加一个非零 errcode。webhook 渠道只看 HTTP 状态码，接这些机器人会把失败记成成功。
const (
	channelWecom    = "wecom"    // 企业微信群机器人
	channelDingTalk = "dingtalk" // 钉钉群机器人
	channelFeishu   = "feishu"   // 飞书自定义机器人
)

// imResponseLimit 读响应体的上限。机器人的返回都是几十字节的小 JSON
const imResponseLimit = 8 << 10

func isIMChannel(channelType string) bool {
	switch channelType {
	case channelWecom, channelDingTalk, channelFeishu:
		return true
	}
	return false
}

// imChannelLabel 出错时带上是哪家，便于按厂商文档查 errcode
func imChannelLabel(channelType string) string {
	switch channelType {
	case channelWecom:
		return "企业微信"
	case channelDingTalk:
		return "钉钉"
	case channelFeishu:
		return "飞书"
	}
	return channelType
}

// ---------- 消息正文 ----------

// imAlertText 把告警拍成群里能直接看懂的纯文本。
//
// 只发 text 不发 markdown/卡片：三家的富文本语法互不兼容，而群消息的价值
// 就是「一眼看清是什么、在哪、多严重」，纯文本够了也最不容易被格式坑。
func imAlertText(alert model.Alert) string {
	var b strings.Builder
	b.WriteString(imSeverityPrefix(alert.Severity))
	b.WriteString(alert.Title)
	b.WriteString("\n级别：" + alert.Severity)
	if alert.Status != "" {
		b.WriteString("（" + imStatusText(alert.Status) + "）")
	}
	if alert.SourceName != "" {
		b.WriteString("\n来源：" + alert.SourceName)
	}
	if alert.Summary != "" {
		b.WriteString("\n详情：" + alert.Summary)
	}
	if alert.Value != "" {
		b.WriteString("\n当前值：" + alert.Value)
	}
	if labels := imLabelText(alert.Labels); labels != "" {
		b.WriteString("\n标签：" + labels)
	}
	if alert.Count > 1 {
		b.WriteString("\n累计次数：" + strconv.Itoa(alert.Count))
	}
	if !alert.LastSeenAt.IsZero() {
		b.WriteString("\n最近发生：" + alert.LastSeenAt.Format("2006-01-02 15:04:05"))
	}
	return b.String()
}

// imSeverityPrefix 级别前缀。群消息刷得快，先给一个能一眼扫到的标记
func imSeverityPrefix(severity string) string {
	switch severity {
	case "critical":
		return "[严重] "
	case "warning":
		return "[警告] "
	case "info":
		return "[提示] "
	}
	return ""
}

func imStatusText(status string) string {
	if status == "resolved" {
		return "已恢复"
	}
	return "进行中"
}

// imLabelText 把标签 JSON 拍成 k=v，按键排序保证同一条告警每次发出来都一样
func imLabelText(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	labels := map[string]string{}
	if err := json.Unmarshal([]byte(raw), &labels); err != nil {
		return ""
	}
	keys := make([]string, 0, len(labels))
	for key := range labels {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+"="+labels[key])
	}
	return strings.Join(parts, " ")
}

// splitMentions 拆 @ 名单，顺带去掉空项
func splitMentions(raw string) []string {
	out := make([]string, 0)
	for _, item := range strings.Split(raw, ",") {
		item = strings.TrimSpace(item)
		if item != "" {
			out = append(out, item)
		}
	}
	return out
}

// ---------- 各家报文 ----------

// imPayload 按渠道类型拼出对应厂商要求的报文。
// 纯函数，便于单测断言 JSON 形状。
func imPayload(channel model.NotifyChannel, text string) map[string]any {
	mentions := splitMentions(channel.MentionList)

	switch channel.Type {
	case channelWecom:
		content := map[string]any{"content": text}
		// 企微的 mentioned_list 收 userid，也接受 "@all"
		list := append([]string{}, mentions...)
		if channel.MentionAll {
			list = append(list, "@all")
		}
		if len(list) > 0 {
			content["mentioned_list"] = list
		}
		return map[string]any{"msgtype": "text", "text": content}

	case channelDingTalk:
		at := map[string]any{"isAtAll": channel.MentionAll}
		if len(mentions) > 0 {
			// 钉钉按手机号 @ 人，且被 @ 的人手机号还要出现在正文里才会高亮
			at["atMobiles"] = mentions
			text += "\n@" + strings.Join(mentions, " @")
		}
		return map[string]any{
			"msgtype": "text",
			"text":    map[string]any{"content": text},
			"at":      at,
		}

	case channelFeishu:
		// 飞书 text 消息里 @ 人要写成标签，@ 单人需要 open_id，
		// 平台不做通讯录同步，所以只支持 @所有人
		if channel.MentionAll {
			text = "<at user_id=\"all\">所有人</at>\n" + text
		}
		return map[string]any{"msg_type": "text", "content": map[string]any{"text": text}}
	}
	return map[string]any{"text": text}
}

// dingtalkSign 钉钉加签：HMAC-SHA256("<timestamp>\n<secret>")，base64 后再 urlencode
func dingtalkSign(secret string, timestampMs int64) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(strconv.FormatInt(timestampMs, 10) + "\n" + secret))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// dingtalkSignedURL 把 timestamp 与 sign 追加到 webhook 地址上。
// secret 为空表示机器人用的是关键词或 IP 白名单校验，原样返回。
func dingtalkSignedURL(rawURL, secret string, timestampMs int64) string {
	if strings.TrimSpace(secret) == "" {
		return rawURL
	}
	sign := dingtalkSign(secret, timestampMs)
	sep := "?"
	if strings.Contains(rawURL, "?") {
		sep = "&"
	}
	return fmt.Sprintf("%s%stimestamp=%d&sign=%s",
		rawURL, sep, timestampMs, url.QueryEscape(sign))
}

// feishuSign 飞书签名校验：把 "<timestamp>\n<secret>" 当密钥、对空串做 HMAC-SHA256
func feishuSign(secret string, timestampSec int64) string {
	key := strconv.FormatInt(timestampSec, 10) + "\n" + secret
	mac := hmac.New(sha256.New, []byte(key))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// ---------- 发送与结果判定 ----------

// imAPIResponse 三家的返回结构：企微/钉钉用 errcode+errmsg，飞书用 code+msg
type imAPIResponse struct {
	ErrCode *int   `json:"errcode"`
	ErrMsg  string `json:"errmsg"`
	Code    *int   `json:"code"`
	Msg     string `json:"msg"`
}

// interpretIMResponse 判定一次投递到底成没成。
//
// 这是接群机器人最容易踩的地方：机器人被移出群、超频、签名过期都会返回
// HTTP 200，只有 body 里的 errcode 才说明真相。
func interpretIMResponse(channelType string, httpStatus int, body []byte) error {
	label := imChannelLabel(channelType)
	trimmed := strings.TrimSpace(string(body))

	if httpStatus >= 300 {
		if trimmed != "" {
			return fmt.Errorf("%s 返回 HTTP %d: %s", label, httpStatus, truncate(trimmed, 160))
		}
		return fmt.Errorf("%s 返回 HTTP %d", label, httpStatus)
	}

	var parsed imAPIResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		// 解析不了就只能以 HTTP 状态码为准，但要留下原文便于排查
		return nil
	}
	if parsed.ErrCode != nil && *parsed.ErrCode != 0 {
		return fmt.Errorf("%s 拒收：errcode=%d %s", label, *parsed.ErrCode, parsed.ErrMsg)
	}
	if parsed.Code != nil && *parsed.Code != 0 {
		return fmt.Errorf("%s 拒收：code=%d %s", label, *parsed.Code, parsed.Msg)
	}
	return nil
}

// postIM 向群机器人发一条消息，返回 HTTP 状态码与判定结果
func (h *Handler) postIM(channel model.NotifyChannel, text string) (int, error) {
	if strings.TrimSpace(channel.URL) == "" {
		return 0, fmt.Errorf("渠道未配置机器人地址")
	}
	payload := imPayload(channel, text)
	target := channel.URL

	switch channel.Type {
	case channelDingTalk:
		target = dingtalkSignedURL(channel.URL, channel.Secret, time.Now().UnixMilli())
	case channelFeishu:
		if strings.TrimSpace(channel.Secret) != "" {
			ts := time.Now().Unix()
			payload["timestamp"] = strconv.FormatInt(ts, 10)
			payload["sign"] = feishuSign(channel.Secret, ts)
		}
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequest(http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	// 群机器人本身不需要额外鉴权头，但有人会把机器人挂在自建网关后面
	if channel.HeaderKey != "" {
		req.Header.Set(channel.HeaderKey, channel.HeaderValue)
	}

	client := &http.Client{Timeout: notifyTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, imResponseLimit))
	return resp.StatusCode, interpretIMResponse(channel.Type, resp.StatusCode, respBody)
}
