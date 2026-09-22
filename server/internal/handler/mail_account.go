package handler

// 发件邮箱（自定义邮箱）。
//
// 在这之前全平台只有一套 SMTP —— 系统配置里 smtp.host / smtp.port / … 六个键，
// `sendMail` 每次都直接读那六个键，签名里根本没有「用哪个邮箱发」的入参。
// 这一轮把发件账号变成一张表，并顺手修了三处一直存在的短板：
//
//  1. **STARTTLS 之前是被动发生的**。原来只有一个 `smtp.tls` 布尔：为 true 走 465 直连 TLS，
//     为 false 就交给 `smtp.SendMail` —— 标准库在服务端 advertise STARTTLS 时会自动升级。
//     问题是中继**不** advertise 时它会静默发明文，而配置上完全看不出来。
//     现在是显式三选一（ssl / starttls / plain），选了 starttls 而对方不支持就报错。
//  2. **自签证书的内网 SMTP 之前连不上**（没有跳过校验的开关）。现在有了，
//     但它是一次真实的降级，所以出接口、上界面、写进说明。
//  3. **中文主题没做 RFC 2047 编码**，部分客户端（尤其国产邮箱网页版）会显示成乱码。
//     现在用 mime.QEncoding 编码，发件人显示名同理。
//
// 全局那套配置**没有废弃**：一个发件邮箱都没登记时仍然走它。这样既有部署升级后
// 邮件照发，而不是突然全部失败。

import (
	"crypto/tls"
	"fmt"
	"mime"
	"net/smtp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"ops-platform/server/internal/cryptox"
	"ops-platform/server/internal/middleware"
	"ops-platform/server/internal/model"
	"ops-platform/server/internal/response"
)

const (
	tlsModeSSL      = "ssl"
	tlsModeSTARTTLS = "starttls"
	tlsModePlain    = "plain"
)

var mailTLSModes = []struct {
	Code  string `json:"code"`
	Label string `json:"label"`
	Note  string `json:"note"`
}{
	{tlsModeSSL, "SSL/TLS 直连", "465 端口常用；建连即加密"},
	{tlsModeSTARTTLS, "STARTTLS 升级", "587/25 常用；先明文握手再升级，对方不支持时报错而不是发明文"},
	{tlsModePlain, "不加密", "口令与正文在网络上是明文，仅限完全可信的内网中继"},
}

func validTLSMode(mode string) bool {
	return mode == tlsModeSSL || mode == tlsModeSTARTTLS || mode == tlsModePlain
}

// mailSender 一次发信需要的全部参数。
//
// 它可能来自 MailAccount，也可能来自系统配置里那套全局 SMTP，
// 所以发信函数只认这个结构，不关心配置从哪来。
type mailSender struct {
	// Source account | global，只用于错误信息与流水，让人知道这封信是哪套配置发的
	Source     string
	AccountID  uint
	Name       string
	Host       string
	Port       int
	Username   string
	Password   string
	From       string
	FromName   string
	TLSMode    string
	SkipVerify bool
}

func (s mailSender) addr() string {
	return fmt.Sprintf("%s:%d", s.Host, s.Port)
}

// label 出错时指名是哪套配置，「发信失败」不带这个信息基本没法查
func (s mailSender) label() string {
	if s.Source == "global" {
		return "系统配置里的全局 SMTP"
	}
	return fmt.Sprintf("发件邮箱「%s」", s.Name)
}

type mailAccountReq struct {
	Name       string `json:"name" binding:"required"`
	Host       string `json:"host" binding:"required"`
	Port       int    `json:"port"`
	Username   string `json:"username"`
	Password   string `json:"password"`
	From       string `json:"from"`
	FromName   string `json:"fromName"`
	TLSMode    string `json:"tlsMode"`
	SkipVerify bool   `json:"skipVerify"`
	IsDefault  bool   `json:"isDefault"`
	Enabled    *bool  `json:"enabled"`
	Remark     string `json:"remark"`
}

func mailAccountView(item model.MailAccount, refCount int64) gin.H {
	storage := "plain"
	if cryptox.IsSealed(item.Password) {
		storage = "encrypted"
	}
	return gin.H{
		"id": item.ID, "name": item.Name, "host": item.Host, "port": item.Port,
		"username": item.Username, "from": item.From, "fromName": item.FromName,
		"tlsMode": item.TLSMode, "skipVerify": item.SkipVerify,
		"isDefault": item.IsDefault, "enabled": item.Enabled, "remark": item.Remark,
		"lastTestAt": item.LastTestAt, "lastTestOk": item.LastTestOK,
		"lastTestErr": item.LastTestErr, "lastTestTo": item.LastTestTo,
		"createdAt": item.CreatedAt, "updatedAt": item.UpdatedAt,
		"storage":     storage,
		"hasPassword": item.Password != "",
		// 实际生效的发件地址：From 留空时回落到认证账号
		"effectiveFrom": orDefault(item.From, item.Username),
		"channelCount":  refCount,
	}
}

// ---------- 发件账号解析 ----------

// resolveMailSender 决定这一封信用哪套配置发。
//
// 顺序：渠道显式指定 → 默认发件邮箱 → 系统配置里的全局 SMTP。
// 渠道指定的邮箱不存在或被停用时**直接报错**，不静默回落到默认邮箱 ——
// 那会让「这个渠道的信换了个发件人」变成一件没人知道的事。
func (h *Handler) resolveMailSender(channel model.NotifyChannel) (mailSender, error) {
	if channel.MailAccountID != 0 {
		var account model.MailAccount
		if err := h.DB.First(&account, channel.MailAccountID).Error; err != nil {
			return mailSender{}, fmt.Errorf("渠道指定的发件邮箱（ID %d）已不存在，请重新选择",
				channel.MailAccountID)
		}
		if !account.Enabled {
			return mailSender{}, fmt.Errorf("渠道指定的发件邮箱「%s」已停用", account.Name)
		}
		return h.senderFromAccount(account)
	}

	var account model.MailAccount
	err := h.DB.Where("is_default = ? AND enabled = ?", true, true).First(&account).Error
	if err == nil {
		return h.senderFromAccount(account)
	}

	// 一个都没登记（或默认那个被停用了）：回落到全局 SMTP，既有部署照常发信
	return h.globalMailSender()
}

func (h *Handler) senderFromAccount(account model.MailAccount) (mailSender, error) {
	password, err := h.openSecret(fmt.Sprintf("发件邮箱「%s」口令", account.Name), account.Password)
	if err != nil {
		return mailSender{}, err
	}
	from := orDefault(account.From, account.Username)
	if from == "" {
		return mailSender{}, fmt.Errorf("发件邮箱「%s」既没填发件地址也没填认证账号", account.Name)
	}
	mode := account.TLSMode
	if !validTLSMode(mode) {
		mode = tlsModeSSL
	}
	return mailSender{
		Source: "account", AccountID: account.ID, Name: account.Name,
		Host: account.Host, Port: account.Port, Username: account.Username,
		Password: password, From: from, FromName: account.FromName,
		TLSMode: mode, SkipVerify: account.SkipVerify,
	}, nil
}

// globalMailSender 系统配置里那套全局 SMTP。保留它是为了兼容既有部署。
func (h *Handler) globalMailSender() (mailSender, error) {
	host := h.configString(CfgSMTPHost, "")
	if host == "" {
		return mailSender{}, fmt.Errorf("没有可用的发件邮箱：既没登记发件邮箱，系统配置里的 SMTP 服务器也是空的")
	}
	password, err := h.configSecret(CfgSMTPPass)
	if err != nil {
		return mailSender{}, err
	}
	username := h.configString(CfgSMTPUser, "")
	from := h.configString(CfgSMTPFrom, username)
	if from == "" {
		return mailSender{}, fmt.Errorf("系统配置里的发件人地址未填写")
	}
	// 老配置只有一个布尔开关，按原语义映射：true = 465 直连 TLS，false = 交给标准库
	mode := tlsModeSTARTTLS
	if h.configString(CfgSMTPTLS, "true") == "true" {
		mode = tlsModeSSL
	}
	return mailSender{
		Source: "global", Name: "全局 SMTP", Host: host,
		Port: h.configInt(CfgSMTPPort, 465), Username: username, Password: password,
		From: from, TLSMode: mode,
	}, nil
}

// ---------- 真正发信 ----------

// deliverMail 按 sender 的配置把一封信投出去。
func deliverMail(sender mailSender, recipients []string, subject, body string) error {
	msg := buildMessage(sender, recipients, subject, body)

	var auth smtp.Auth
	if sender.Username != "" {
		auth = smtp.PlainAuth("", sender.Username, sender.Password, sender.Host)
	}

	switch sender.TLSMode {
	case tlsModeSSL:
		return sendMailSSL(sender, auth, recipients, msg)
	case tlsModePlain:
		return sendMailPlain(sender, auth, recipients, msg, false)
	default:
		return sendMailPlain(sender, auth, recipients, msg, true)
	}
}

func (s mailSender) tlsConfig() *tls.Config {
	return &tls.Config{
		ServerName:         s.Host,
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: s.SkipVerify, //nolint:gosec // 开关由使用者显式勾选，界面上标为降级
	}
}

// sendMailSSL 465 这类建连即 TLS 的场景
func sendMailSSL(sender mailSender, auth smtp.Auth, to []string, msg []byte) error {
	conn, err := tls.Dial("tcp", sender.addr(), sender.tlsConfig())
	if err != nil {
		return fmt.Errorf("TLS 连接失败: %w", err)
	}
	defer conn.Close()

	client, err := smtp.NewClient(conn, sender.Host)
	if err != nil {
		return err
	}
	defer client.Close()
	return finishSMTP(client, auth, sender.From, to, msg)
}

// sendMailPlain 先明文建连。starttls=true 时**要求**升级成功，
// 这正是与老实现的区别：老实现依赖标准库「对方 advertise 就升级」，
// 不 advertise 就静默发明文。
func sendMailPlain(sender mailSender, auth smtp.Auth, to []string, msg []byte, starttls bool) error {
	client, err := smtp.Dial(sender.addr())
	if err != nil {
		return fmt.Errorf("连接 SMTP 失败: %w", err)
	}
	defer client.Close()

	if starttls {
		if ok, _ := client.Extension("STARTTLS"); !ok {
			return fmt.Errorf("选择了 STARTTLS，但服务器 %s 没有声明支持 —— 不降级成明文发送，请改用 SSL/TLS 直连或确认端口",
				sender.addr())
		}
		if err := client.StartTLS(sender.tlsConfig()); err != nil {
			return fmt.Errorf("STARTTLS 升级失败: %w", err)
		}
	}
	return finishSMTP(client, auth, sender.From, to, msg)
}

func finishSMTP(client *smtp.Client, auth smtp.Auth, from string, to []string, msg []byte) error {
	if auth != nil {
		if err := client.Auth(auth); err != nil {
			return fmt.Errorf("SMTP 认证失败: %w", err)
		}
	}
	if err := client.Mail(from); err != nil {
		return err
	}
	for _, rcpt := range to {
		if err := client.Rcpt(rcpt); err != nil {
			return fmt.Errorf("收件人 %s 被拒绝: %w", rcpt, err)
		}
	}
	writer, err := client.Data()
	if err != nil {
		return err
	}
	if _, err := writer.Write(msg); err != nil {
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	return client.Quit()
}

// buildMessage 组装邮件。主题与发件人显示名按 RFC 2047 编码 ——
// 不编码时中文主题在部分客户端是乱码，这个问题一直存在。
func buildMessage(sender mailSender, to []string, subject, body string) []byte {
	var sb strings.Builder
	if sender.FromName != "" {
		sb.WriteString("From: " + mime.QEncoding.Encode("UTF-8", sender.FromName) +
			" <" + sender.From + ">\r\n")
	} else {
		sb.WriteString("From: " + sender.From + "\r\n")
	}
	sb.WriteString("To: " + strings.Join(to, ", ") + "\r\n")
	sb.WriteString("Subject: " + mime.QEncoding.Encode("UTF-8", subject) + "\r\n")
	sb.WriteString("MIME-Version: 1.0\r\n")
	sb.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
	sb.WriteString("\r\n")
	sb.WriteString(body)
	return []byte(sb.String())
}

// ---------- 接口 ----------

func (h *Handler) ListMailAccounts(c *gin.Context) {
	var list []model.MailAccount
	if err := h.DB.Order("is_default desc, name asc").Find(&list).Error; err != nil {
		response.Error(c, "查询发件邮箱失败")
		return
	}

	var refs []struct {
		MailAccountID uint
		Count         int64
	}
	h.DB.Model(&model.NotifyChannel{}).
		Select("mail_account_id, COUNT(1) as count").
		Where("mail_account_id <> 0").Group("mail_account_id").Scan(&refs)
	refMap := map[uint]int64{}
	for _, r := range refs {
		refMap[r.MailAccountID] = r.Count
	}

	views := make([]gin.H, 0, len(list))
	for _, item := range list {
		views = append(views, mailAccountView(item, refMap[item.ID]))
	}
	response.OK(c, views)
}

// GetMailState 这一页的事实条：现在到底是哪套配置在发信。
func (h *Handler) GetMailState(c *gin.Context) {
	var total, enabled int64
	h.DB.Model(&model.MailAccount{}).Count(&total)
	h.DB.Model(&model.MailAccount{}).Where("enabled = ?", true).Count(&enabled)

	state := gin.H{
		"total":            total,
		"enabled":          enabled,
		"tlsModes":         mailTLSModes,
		"globalHost":       h.configString(CfgSMTPHost, ""),
		"globalConfigured": h.configString(CfgSMTPHost, "") != "",
	}

	// 照实说当前会用哪套：这是这一页最该回答的问题
	sender, err := h.resolveMailSender(model.NotifyChannel{})
	if err != nil {
		state["activeSender"] = ""
		state["activeError"] = err.Error()
	} else {
		state["activeSender"] = sender.label()
		state["activeFrom"] = sender.From
		state["activeSource"] = sender.Source
		state["activeTLSMode"] = sender.TLSMode
	}

	state["notes"] = []string{
		"渠道没指定发件邮箱时用「默认」那个；一个发件邮箱都没登记时回落到系统配置里的全局 SMTP（既有部署不受影响）",
		"渠道指定的邮箱被删除或停用时，这个渠道的邮件**直接失败并点名原因**，不会静默换一个发件人",
		"STARTTLS 是显式选项：选了它而服务器没声明支持就报错，不降级成明文 —— 老实现在这种情况下会静默发明文",
		"跳过证书校验是一次真实的降级，只适合内网自签证书的中继；开着时列表里会标出来",
		"主题与发件人显示名按 RFC 2047 编码，中文主题不再依赖客户端猜字符集",
		"口令加密落库、不出接口；编辑时留空表示不修改",
	}
	response.OK(c, state)
}

func (h *Handler) CreateMailAccount(c *gin.Context) {
	var req mailAccountReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "名称与 SMTP 服务器为必填项")
		return
	}
	if strings.TrimSpace(req.From) == "" && strings.TrimSpace(req.Username) == "" {
		response.BadRequest(c, "发件地址与认证账号至少填一个：两个都空的话没有 From 可写")
		return
	}
	mode := orDefault(req.TLSMode, tlsModeSSL)
	if !validTLSMode(mode) {
		response.BadRequest(c, "加密方式只能是 ssl / starttls / plain")
		return
	}
	port := req.Port
	if port <= 0 || port > 65535 {
		response.BadRequest(c, "端口不合法")
		return
	}

	item := model.MailAccount{
		Name: strings.TrimSpace(req.Name), Host: strings.TrimSpace(req.Host), Port: port,
		Username: strings.TrimSpace(req.Username), Password: h.sealSecret(req.Password),
		From: strings.TrimSpace(req.From), FromName: req.FromName,
		TLSMode: mode, SkipVerify: req.SkipVerify, Enabled: true, Remark: req.Remark,
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
	if req.IsDefault {
		h.setDefaultMailAccount(item.ID)
		h.DB.First(&item, item.ID)
	}
	response.OK(c, mailAccountView(item, 0))
}

func (h *Handler) UpdateMailAccount(c *gin.Context) {
	var item model.MailAccount
	if err := h.DB.First(&item, idParam(c)).Error; err != nil {
		response.NotFound(c, "发件邮箱不存在")
		return
	}
	var req mailAccountReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "参数校验失败")
		return
	}
	mode := orDefault(req.TLSMode, item.TLSMode)
	if !validTLSMode(mode) {
		response.BadRequest(c, "加密方式只能是 ssl / starttls / plain")
		return
	}
	port := req.Port
	if port <= 0 || port > 65535 {
		response.BadRequest(c, "端口不合法")
		return
	}

	updates := map[string]any{
		"name": strings.TrimSpace(req.Name), "host": strings.TrimSpace(req.Host), "port": port,
		"username": strings.TrimSpace(req.Username), "from": strings.TrimSpace(req.From),
		"from_name": req.FromName, "tls_mode": mode, "skip_verify": req.SkipVerify,
		"remark": req.Remark,
	}
	if req.Enabled != nil {
		updates["enabled"] = *req.Enabled
	}
	// 口令留空 = 不修改。与系统配置里的密钥项同一约定
	if strings.TrimSpace(req.Password) != "" {
		updates["password"] = h.sealSecret(req.Password)
	}
	if err := h.DB.Model(&item).Updates(updates).Error; err != nil {
		response.Error(c, "更新失败，名称可能重复")
		return
	}
	if req.IsDefault && !item.IsDefault {
		h.setDefaultMailAccount(item.ID)
	}
	h.DB.First(&item, item.ID)
	response.OK(c, mailAccountView(item, 0))
}

// setDefaultMailAccount 默认发件邮箱全局只能有一个，设新的就把旧的取消。
func (h *Handler) setDefaultMailAccount(id uint) {
	h.DB.Model(&model.MailAccount{}).Where("id <> ?", id).
		Update("is_default", false)
	h.DB.Model(&model.MailAccount{}).Where("id = ?", id).
		Update("is_default", true)
}

func (h *Handler) SetDefaultMailAccount(c *gin.Context) {
	var item model.MailAccount
	if err := h.DB.First(&item, idParam(c)).Error; err != nil {
		response.NotFound(c, "发件邮箱不存在")
		return
	}
	if !item.Enabled {
		response.BadRequest(c, "停用的邮箱不能设为默认：那会让所有没指定邮箱的渠道立刻发不出去")
		return
	}
	h.setDefaultMailAccount(item.ID)
	response.OK(c, gin.H{"id": item.ID, "isDefault": true})
}

func (h *Handler) DeleteMailAccount(c *gin.Context) {
	id := idParam(c)
	var count int64
	h.DB.Model(&model.NotifyChannel{}).Where("mail_account_id = ?", id).Count(&count)
	if count > 0 {
		response.BadRequest(c, fmt.Sprintf("还有 %d 个通知渠道指定了这个发件邮箱，请先改掉它们", count))
		return
	}
	if err := h.DB.Delete(&model.MailAccount{}, id).Error; err != nil {
		response.Error(c, "删除失败")
		return
	}
	response.OK(c, gin.H{"deleted": true})
}

type mailTestReq struct {
	To string `json:"to" binding:"required"`
}

// TestMailAccount 用这套配置真发一封信。
//
// 与通知渠道试发同一个约定：失败也返回 200 + ok:false，因为「配置不通」
// 是一个有效的检测结果，不是服务端错误。
func (h *Handler) TestMailAccount(c *gin.Context) {
	var item model.MailAccount
	if err := h.DB.First(&item, idParam(c)).Error; err != nil {
		response.NotFound(c, "发件邮箱不存在")
		return
	}
	var req mailTestReq
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.To) == "" {
		response.BadRequest(c, "请填写收件地址")
		return
	}
	recipients := splitRecipients(req.To)
	if len(recipients) == 0 {
		response.BadRequest(c, "收件地址不合法")
		return
	}

	start := time.Now()
	sender, err := h.senderFromAccount(item)
	if err == nil {
		err = deliverMail(sender, recipients,
			fmt.Sprintf("OpsOne 发件邮箱测试（%s）", item.Name),
			fmt.Sprintf("这封信用发件邮箱「%s」发出。\n\n服务器: %s\n加密方式: %s\n发件地址: %s\n跳过证书校验: %v\n\n"+
				"能收到说明这套配置可用；主题里的中文正常显示说明 RFC 2047 编码生效。",
				item.Name, sender.addr(), item.TLSMode, sender.From, item.SkipVerify))
	}
	cost := time.Since(start).Milliseconds()

	now := time.Now()
	ok := err == nil
	updates := map[string]any{
		"last_test_at": &now, "last_test_ok": &ok, "last_test_to": truncate(recipients[0], 120),
		"last_test_err": "",
	}
	if err != nil {
		updates["last_test_err"] = truncate(err.Error(), 240)
	}
	h.DB.Model(&item).Updates(updates)

	detail := "已投递到 SMTP 服务器"
	if err != nil {
		detail = err.Error()
	}
	response.OK(c, gin.H{"ok": ok, "detail": detail, "costMs": cost})
}

// ImportGlobalSMTP 把系统配置里那套全局 SMTP 复制成一个发件邮箱条目。
//
// 存在的理由很实际：这一页上线前所有部署的配置都在那六个键里，
// 让人对着旧配置重新手输一遍容易输错 —— 尤其口令，它在界面上是看不到的。
func (h *Handler) ImportGlobalSMTP(c *gin.Context) {
	sender, err := h.globalMailSender()
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	name := "系统配置导入"
	var exist int64
	h.DB.Model(&model.MailAccount{}).Where("name = ?", name).Count(&exist)
	if exist > 0 {
		response.BadRequest(c, "已经导入过一次了（条目「系统配置导入」），请直接编辑那一条")
		return
	}

	item := model.MailAccount{
		Name: name, Host: sender.Host, Port: sender.Port, Username: sender.Username,
		Password: h.sealSecret(sender.Password), From: sender.From,
		TLSMode: sender.TLSMode, Enabled: true,
		Remark: "从系统配置的 smtp.* 键导入；原配置保留不动，仍作为没有任何发件邮箱时的兜底",
	}
	if user := middleware.CurrentUser(c); user != nil {
		item.CreatedBy = user.ID
	}
	if err := h.DB.Create(&item).Error; err != nil {
		response.Error(c, "导入失败")
		return
	}
	response.OK(c, mailAccountView(item, 0))
}
