package handler

import (
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"

	"ops-platform/server/internal/middleware"
	"ops-platform/server/internal/model"
	"ops-platform/server/internal/response"
	"ops-platform/server/internal/totp"
)

// TOTPModeKey 双因子强制策略的配置键：optional 自愿绑定，required 未绑定只能访问绑定接口
const TOTPModeKey = "security.totp.mode"

// totpMode 读取当前策略，取值非法时按 optional 处理
func (h *Handler) totpMode() string {
	if h.configString(TOTPModeKey, "optional") == "required" {
		return "required"
	}
	return "optional"
}

// GetMyTOTP 当前账号的双因子状态
func (h *Handler) GetMyTOTP(c *gin.Context) {
	user := middleware.CurrentUser(c)
	response.OK(c, gin.H{
		"enabled": user.TOTPEnabled,
		"boundAt": user.TOTPBoundAt,
		// 有密钥但未启用，说明绑定流程走到一半
		"pending": !user.TOTPEnabled && user.TOTPSecret != "",
		"mode":    h.totpMode(),
		"issuer":  h.configString("platform.name", "OpsOne"),
	})
}

// SetupMyTOTP 生成新密钥并返回扫码地址。
//
// 每次调用都会覆盖上一次未完成的密钥，避免残留的半成品密钥被用来绕过确认。
func (h *Handler) SetupMyTOTP(c *gin.Context) {
	user := middleware.CurrentUser(c)
	if user.TOTPEnabled {
		response.BadRequest(c, "已绑定双因子口令，请先解绑再重新绑定")
		return
	}

	secret, err := totp.NewSecret()
	if err != nil {
		response.Error(c, "生成密钥失败")
		return
	}
	if err := h.DB.Model(&model.User{}).Where("id = ?", user.ID).Updates(map[string]any{
		"totp_secret":       secret,
		"totp_enabled":      false,
		"totp_last_counter": 0,
	}).Error; err != nil {
		response.Error(c, "保存密钥失败")
		return
	}

	issuer := h.configString("platform.name", "OpsOne")
	response.OK(c, gin.H{
		"secret": secret,
		"uri":    totp.ProvisioningURI(secret, user.Username, issuer),
		"digits": totp.Digits,
		"period": totp.Period,
	})
}

type totpCodeReq struct {
	Code string `json:"code"`
}

// ConfirmMyTOTP 用验证器给出的验证码确认绑定
func (h *Handler) ConfirmMyTOTP(c *gin.Context) {
	user := middleware.CurrentUser(c)
	if user.TOTPEnabled {
		response.BadRequest(c, "已完成绑定")
		return
	}
	if user.TOTPSecret == "" {
		response.BadRequest(c, "请先获取密钥")
		return
	}

	var req totpCodeReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "请输入验证码")
		return
	}

	ok, counter := totp.VerifyWithCounter(user.TOTPSecret, req.Code, time.Now())
	if !ok {
		response.BadRequest(c, "验证码不正确，请确认手机时间与密钥无误")
		return
	}

	now := time.Now()
	if err := h.DB.Model(&model.User{}).Where("id = ?", user.ID).Updates(map[string]any{
		"totp_enabled":      true,
		"totp_bound_at":     &now,
		"totp_last_counter": counter,
	}).Error; err != nil {
		response.Error(c, "绑定失败")
		return
	}
	response.OK(c, gin.H{"enabled": true, "boundAt": now})
}

type totpDisableReq struct {
	Password string `json:"password"`
	Code     string `json:"code"`
}

// DisableMyTOTP 解绑。
//
// 必须同时给出当前口令与动态验证码：只有会话被劫持而攻击者不知道口令时，
// 这一步才能挡住「登录后顺手把双因子关掉」。
func (h *Handler) DisableMyTOTP(c *gin.Context) {
	user := middleware.CurrentUser(c)
	if !user.TOTPEnabled {
		response.BadRequest(c, "当前未绑定双因子口令")
		return
	}
	if h.totpMode() == "required" {
		response.Forbidden(c, "平台已强制启用双因子口令，不能自行解绑，请联系管理员重置")
		return
	}

	var req totpDisableReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "请输入登录口令与验证码")
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.Password)) != nil {
		response.BadRequest(c, "登录口令不正确")
		return
	}
	if ok, _ := totp.VerifyWithCounter(user.TOTPSecret, req.Code, time.Now()); !ok {
		response.BadRequest(c, "验证码不正确")
		return
	}

	if err := h.DB.Model(&model.User{}).Where("id = ?", user.ID).Updates(map[string]any{
		"totp_secret":       "",
		"totp_enabled":      false,
		"totp_bound_at":     nil,
		"totp_last_counter": 0,
	}).Error; err != nil {
		response.Error(c, "解绑失败")
		return
	}
	response.OK(c, gin.H{"enabled": false})
}

// ResetUserTOTP 管理员重置他人绑定（手机丢失场景）。
// 平台不提供恢复码，这是唯一的找回途径，操作会进审计日志。
func (h *Handler) ResetUserTOTP(c *gin.Context) {
	var target model.User
	if err := h.DB.First(&target, idParam(c)).Error; err != nil {
		response.NotFound(c, "用户不存在")
		return
	}
	if !target.TOTPEnabled && target.TOTPSecret == "" {
		response.BadRequest(c, "该用户未绑定双因子口令")
		return
	}

	if err := h.DB.Model(&model.User{}).Where("id = ?", target.ID).Updates(map[string]any{
		"totp_secret":       "",
		"totp_enabled":      false,
		"totp_bound_at":     nil,
		"totp_last_counter": 0,
	}).Error; err != nil {
		response.Error(c, "重置失败")
		return
	}
	response.OK(c, gin.H{"username": target.Username, "enabled": false})
}

// verifyLoginTOTP 登录时校验动态验证码，并记录已用窗口拒绝重放。
// 返回的第二个值是给调用方的提示信息。
func (h *Handler) verifyLoginTOTP(user *model.User, code string) (bool, string) {
	code = strings.TrimSpace(code)
	if code == "" {
		return false, "请输入动态验证码"
	}
	if user.TOTPSecret == "" {
		// 理论上不会出现：启用了却没有密钥，说明数据被改坏了，此时不能放行
		return false, "双因子配置异常，请联系管理员重置"
	}

	ok, counter := totp.VerifyWithCounter(user.TOTPSecret, code, time.Now())
	if !ok {
		return false, "动态验证码不正确"
	}
	if counter <= user.TOTPLastCounter {
		return false, "该验证码已使用过，请等待下一个验证码"
	}
	h.DB.Model(&model.User{}).Where("id = ?", user.ID).
		Update("totp_last_counter", counter)
	return true, ""
}
