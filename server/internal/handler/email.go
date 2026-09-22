package handler

import (
	"bytes"
	"fmt"
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

// sampleAlertVars 告警场景的样例变量。
// **从 notifyScenes 那份权威定义派生**，不再单独维护一份 ——
// 之前样例变量、真实变量、界面提示各写一遍，对得上是人工维护的巧合。
func sampleAlertVars() map[string]string {
	return sceneSampleVars(sceneAlert)
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
//
// 发件账号的解析、TLS 与报文组装都在 mail_account.go：这里只管
// 「谁收、用哪个模板、渲染成什么」。
func (h *Handler) sendMail(channel model.NotifyChannel, vars map[string]string) error {
	sender, err := h.resolveMailSender(channel)
	if err != nil {
		return err
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

	if err := deliverMail(sender, recipients, subject, body); err != nil {
		// 点名是哪套配置：「发信失败」不带这个信息基本没法查
		return fmt.Errorf("%s 发信失败: %w", sender.label(), err)
	}
	return nil
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
