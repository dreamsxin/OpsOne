package handler

import (
	"bytes"
	"crypto/tls"
	"fmt"
	"net/smtp"
	"strings"
	"text/template"
	"time"

	"github.com/gin-gonic/gin"

	"ops-platform/server/internal/model"
	"ops-platform/server/internal/response"
)

// SMTP 相关配置键
const (
	CfgSMTPHost = "smtp.host"
	CfgSMTPPort = "smtp.port"
	CfgSMTPUser = "smtp.username"
	CfgSMTPPass = "smtp.password"
	CfgSMTPFrom = "smtp.from"
	CfgSMTPTLS  = "smtp.tls"
)

// ---------- 邮件模板 ----------

type emailTemplateReq struct {
	Code      string `json:"code" binding:"required"`
	Name      string `json:"name" binding:"required"`
	Subject   string `json:"subject" binding:"required"`
	Body      string `json:"body"`
	Variables string `json:"variables"`
	Remark    string `json:"remark"`
	Enabled   *bool  `json:"enabled"`
}

func (h *Handler) ListEmailTemplates(c *gin.Context) {
	var list []model.EmailTemplate
	if err := h.DB.Order("id asc").Find(&list).Error; err != nil {
		response.Error(c, "查询邮件模板失败")
		return
	}
	response.OK(c, list)
}

func (h *Handler) CreateEmailTemplate(c *gin.Context) {
	var req emailTemplateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "编码、名称、主题为必填项")
		return
	}
	if err := validateTemplate(req.Subject, req.Body); err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	item := model.EmailTemplate{
		Code: strings.TrimSpace(req.Code), Name: req.Name, Subject: req.Subject,
		Body: req.Body, Variables: req.Variables, Remark: req.Remark, Enabled: true,
	}
	if req.Enabled != nil {
		item.Enabled = *req.Enabled
	}
	if err := h.DB.Create(&item).Error; err != nil {
		response.BadRequest(c, "创建失败，模板编码可能已存在")
		return
	}
	response.OK(c, item)
}

func (h *Handler) UpdateEmailTemplate(c *gin.Context) {
	var item model.EmailTemplate
	if err := h.DB.First(&item, idParam(c)).Error; err != nil {
		response.NotFound(c, "模板不存在")
		return
	}
	var req emailTemplateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "参数校验失败")
		return
	}
	if err := validateTemplate(req.Subject, req.Body); err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	item.Name, item.Subject, item.Body = req.Name, req.Subject, req.Body
	item.Variables, item.Remark = req.Variables, req.Remark
	if req.Enabled != nil {
		item.Enabled = *req.Enabled
	}
	if err := h.DB.Save(&item).Error; err != nil {
		response.Error(c, "更新失败")
		return
	}
	response.OK(c, item)
}

func (h *Handler) DeleteEmailTemplate(c *gin.Context) {
	var item model.EmailTemplate
	if err := h.DB.First(&item, idParam(c)).Error; err != nil {
		response.NotFound(c, "模板不存在")
		return
	}
	if item.Builtin {
		response.BadRequest(c, "内置模板不可删除，可直接修改内容")
		return
	}

	var refCount int64
	h.DB.Model(&model.NotifyChannel{}).Where("template_code = ?", item.Code).Count(&refCount)
	if refCount > 0 {
		response.BadRequest(c, fmt.Sprintf("该模板被 %d 个通知渠道引用，请先解除引用", refCount))
		return
	}

	if err := h.DB.Delete(&item).Error; err != nil {
		response.Error(c, "删除失败")
		return
	}
	response.OK(c, nil)
}

// PreviewEmailTemplate 用样例变量渲染模板，便于确认占位符写法
func (h *Handler) PreviewEmailTemplate(c *gin.Context) {
	var item model.EmailTemplate
	if err := h.DB.First(&item, idParam(c)).Error; err != nil {
		response.NotFound(c, "模板不存在")
		return
	}

	var req struct {
		Vars map[string]string `json:"vars"`
	}
	_ = c.ShouldBindJSON(&req)

	vars := sampleAlertVars()
	for k, v := range req.Vars {
		vars[k] = v
	}

	subject, body, err := renderTemplate(item.Subject, item.Body, vars)
	if err != nil {
		response.BadRequest(c, "渲染失败: "+err.Error())
		return
	}
	response.OK(c, gin.H{"subject": subject, "body": body, "vars": vars})
}

func sampleAlertVars() map[string]string {
	return map[string]string{
		"title": "web-01 磁盘使用率过高", "severity": "critical", "source": "Prometheus 演示",
		"value": "92%", "count": "3", "status": "firing",
		"summary":     "/data 使用率 92%，持续 5 分钟",
		"labels":      "host=web-01, env=prod, service=nginx",
		"firstSeenAt": time.Now().Add(-30 * time.Minute).Format(time.RFC3339),
		"lastSeenAt":  time.Now().Format(time.RFC3339),
	}
}

// validateTemplate 提前发现模板语法错误，避免真正发信时才失败
func validateTemplate(subject, body string) error {
	if _, err := template.New("subject").Parse(subject); err != nil {
		return fmt.Errorf("邮件主题模板语法错误: %v", err)
	}
	if _, err := template.New("body").Parse(body); err != nil {
		return fmt.Errorf("邮件正文模板语法错误: %v", err)
	}
	return nil
}

func renderTemplate(subject, body string, vars map[string]string) (string, string, error) {
	render := func(name, text string) (string, error) {
		tpl, err := template.New(name).Parse(text)
		if err != nil {
			return "", err
		}
		var buf bytes.Buffer
		if err := tpl.Execute(&buf, vars); err != nil {
			return "", err
		}
		return buf.String(), nil
	}

	renderedSubject, err := render("subject", subject)
	if err != nil {
		return "", "", err
	}
	renderedBody, err := render("body", body)
	if err != nil {
		return "", "", err
	}
	return renderedSubject, renderedBody, nil
}

// ---------- 邮件发送 ----------

// sendMail 按渠道配置发信。返回错误说明供通知流水记录。
func (h *Handler) sendMail(channel model.NotifyChannel, vars map[string]string) error {
	host := h.configString(CfgSMTPHost, "")
	if host == "" {
		return fmt.Errorf("SMTP 服务器未配置，请在系统配置里填写")
	}
	port := h.configInt(CfgSMTPPort, 465)
	username := h.configString(CfgSMTPUser, "")
	password := h.configString(CfgSMTPPass, "")
	from := h.configString(CfgSMTPFrom, username)
	if from == "" {
		return fmt.Errorf("发件人地址未配置")
	}

	recipients := splitRecipients(channel.Recipients)
	if len(recipients) == 0 {
		return fmt.Errorf("该渠道未配置收件人")
	}

	tpl, err := h.resolveTemplate(channel.TemplateCode)
	if err != nil {
		return err
	}
	subject, body, err := renderTemplate(tpl.Subject, tpl.Body, vars)
	if err != nil {
		return fmt.Errorf("模板渲染失败: %w", err)
	}

	msg := buildMessage(from, recipients, subject, body)
	addr := fmt.Sprintf("%s:%d", host, port)

	var auth smtp.Auth
	if username != "" {
		auth = smtp.PlainAuth("", username, password, host)
	}

	if h.configString(CfgSMTPTLS, "true") == "true" {
		return sendMailTLS(addr, host, auth, from, recipients, msg)
	}
	return smtp.SendMail(addr, auth, from, recipients, msg)
}

// resolveTemplate 取指定编码的模板，未指定时回落到内置告警模板
func (h *Handler) resolveTemplate(code string) (*model.EmailTemplate, error) {
	var tpl model.EmailTemplate
	if code != "" {
		if err := h.DB.Where("code = ? AND enabled = ?", code, true).First(&tpl).Error; err != nil {
			return nil, fmt.Errorf("邮件模板 %s 不存在或已停用", code)
		}
		return &tpl, nil
	}
	if err := h.DB.Where("code = ?", "alert.default").First(&tpl).Error; err != nil {
		return nil, fmt.Errorf("内置邮件模板缺失")
	}
	return &tpl, nil
}

// sendMailTLS 465 端口这类直接 TLS 的场景，需要先建 TLS 连接再交给 smtp
func sendMailTLS(addr, host string, auth smtp.Auth, from string, to []string, msg []byte) error {
	conn, err := tls.Dial("tcp", addr, &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12})
	if err != nil {
		return fmt.Errorf("TLS 连接失败: %w", err)
	}
	defer conn.Close()

	client, err := smtp.NewClient(conn, host)
	if err != nil {
		return err
	}
	defer client.Close()

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

func buildMessage(from string, to []string, subject, body string) []byte {
	var sb strings.Builder
	sb.WriteString("From: " + from + "\r\n")
	sb.WriteString("To: " + strings.Join(to, ", ") + "\r\n")
	// 主题可能含中文，按 RFC 2047 用 UTF-8 base64 编码更稳妥，这里交给邮件库处理不便，
	// 直接声明字符集并保持原文，主流客户端可正常解析
	sb.WriteString("Subject: " + subject + "\r\n")
	sb.WriteString("MIME-Version: 1.0\r\n")
	sb.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
	sb.WriteString("\r\n")
	sb.WriteString(body)
	return []byte(sb.String())
}

func splitRecipients(raw string) []string {
	result := make([]string, 0)
	for _, item := range strings.Split(raw, ",") {
		addr := strings.TrimSpace(item)
		if addr != "" {
			result = append(result, addr)
		}
	}
	return result
}

// alertMailVars 把告警转成模板变量
func alertMailVars(alert model.Alert) map[string]string {
	labels := ""
	if alert.Labels != "" {
		labels = alert.Labels
	}
	return map[string]string{
		"title": alert.Title, "severity": alert.Severity, "source": alert.SourceName,
		"value": alert.Value, "count": fmt.Sprint(alert.Count), "status": alert.Status,
		"summary": alert.Summary, "labels": labels,
		"firstSeenAt": alert.FirstSeenAt.Format(time.RFC3339),
		"lastSeenAt":  alert.LastSeenAt.Format(time.RFC3339),
	}
}
