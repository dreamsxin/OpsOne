package handler

import (
	"fmt"
	"log"
	"time"

	"github.com/gin-gonic/gin"

	"ops-platform/server/internal/middleware"
	"ops-platform/server/internal/model"
	"ops-platform/server/internal/response"
)

// ---------- 公告管理 ----------

type announcementReq struct {
	Title   string `json:"title" binding:"required"`
	Content string `json:"content"`
	Level   string `json:"level"`
}

func (h *Handler) ListAnnouncements(c *gin.Context) {
	page, size := pageParams(c)
	q := h.DB.Model(&model.Announcement{})
	if published := c.Query("published"); published != "" {
		q = q.Where("published = ?", published == "true")
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		response.Error(c, "查询公告失败")
		return
	}
	var list []model.Announcement
	if err := q.Order("id desc").Offset((page - 1) * size).Limit(size).Find(&list).Error; err != nil {
		response.Error(c, "查询公告失败")
		return
	}
	response.OKPage(c, list, total, page, size)
}

func (h *Handler) CreateAnnouncement(c *gin.Context) {
	var req announcementReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "公告标题不能为空")
		return
	}

	item := model.Announcement{
		Title: req.Title, Content: req.Content, Level: normalizeLevel(req.Level),
	}
	if err := h.DB.Create(&item).Error; err != nil {
		response.Error(c, "公告创建失败")
		return
	}
	response.OK(c, item)
}

func (h *Handler) UpdateAnnouncement(c *gin.Context) {
	var item model.Announcement
	if err := h.DB.First(&item, idParam(c)).Error; err != nil {
		response.NotFound(c, "公告不存在")
		return
	}
	var req announcementReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "参数校验失败")
		return
	}

	item.Title, item.Content, item.Level = req.Title, req.Content, normalizeLevel(req.Level)
	if err := h.DB.Save(&item).Error; err != nil {
		response.Error(c, "公告更新失败")
		return
	}
	response.OK(c, item)
}

func (h *Handler) DeleteAnnouncement(c *gin.Context) {
	id := idParam(c)
	if err := h.DB.Delete(&model.Announcement{}, id).Error; err != nil {
		response.Error(c, "公告删除失败")
		return
	}
	// 关联的站内消息一并清理，避免用户点开后拿不到内容
	h.DB.Where("type = ? AND ref_id = ?", "announcement", id).Delete(&model.Message{})
	response.OK(c, nil)
}

// PublishAnnouncement 发布公告，并给每个启用用户投递一条站内消息
func (h *Handler) PublishAnnouncement(c *gin.Context) {
	var item model.Announcement
	if err := h.DB.First(&item, idParam(c)).Error; err != nil {
		response.NotFound(c, "公告不存在")
		return
	}
	if item.Published {
		response.BadRequest(c, "公告已处于发布状态")
		return
	}

	now := time.Now()
	user := middleware.CurrentUser(c)
	err := h.DB.Model(&item).Updates(map[string]any{
		"published": true, "published_at": &now, "publisher": user.Username,
	}).Error
	if err != nil {
		response.Error(c, "发布失败")
		return
	}

	var userIDs []uint
	h.DB.Model(&model.User{}).Where("status = ?", 1).Pluck("id", &userIDs)
	sent := h.fanoutMessages(userIDs, model.Message{
		Type: "announcement", Title: item.Title, Content: item.Content,
		Level: item.Level, RefID: item.ID,
	})

	response.OK(c, gin.H{"published": true, "messageSent": sent})
}

// UnpublishAnnouncement 下线公告，同时撤回未读的站内消息
func (h *Handler) UnpublishAnnouncement(c *gin.Context) {
	var item model.Announcement
	if err := h.DB.First(&item, idParam(c)).Error; err != nil {
		response.NotFound(c, "公告不存在")
		return
	}

	if err := h.DB.Model(&item).Updates(map[string]any{"published": false}).Error; err != nil {
		response.Error(c, "下线失败")
		return
	}
	// 已读的消息保留痕迹，未读的直接撤回
	h.DB.Where("type = ? AND ref_id = ? AND read = ?", "announcement", item.ID, false).
		Delete(&model.Message{})

	response.OK(c, nil)
}

// ListPublishedAnnouncements 全员可见的已发布公告
func (h *Handler) ListPublishedAnnouncements(c *gin.Context) {
	page, size := pageParams(c)
	q := h.DB.Model(&model.Announcement{}).Where("published = ?", true)

	var total int64
	if err := q.Count(&total).Error; err != nil {
		response.Error(c, "查询公告失败")
		return
	}
	var list []model.Announcement
	if err := q.Order("published_at desc").Offset((page - 1) * size).Limit(size).Find(&list).Error; err != nil {
		response.Error(c, "查询公告失败")
		return
	}
	response.OKPage(c, list, total, page, size)
}

func normalizeLevel(l string) string {
	if l == "warning" {
		return "warning"
	}
	return "info"
}

// ---------- 站内消息 ----------

// MessageSummary 未读汇总，供顶栏铃铛使用
func (h *Handler) MessageSummary(c *gin.Context) {
	user := middleware.CurrentUser(c)

	var unread, unreadAlert int64
	h.DB.Model(&model.Message{}).Where("user_id = ? AND read = ?", user.ID, false).Count(&unread)
	h.DB.Model(&model.Message{}).
		Where("user_id = ? AND read = ? AND type = ?", user.ID, false, "alert").Count(&unreadAlert)

	var latest []model.Message
	h.DB.Where("user_id = ? AND read = ?", user.ID, false).
		Order("id desc").Limit(5).Find(&latest)

	response.OK(c, gin.H{"unread": unread, "unreadAlert": unreadAlert, "latest": latest})
}

func (h *Handler) ListMessages(c *gin.Context) {
	user := middleware.CurrentUser(c)
	page, size := pageParams(c)

	q := h.DB.Model(&model.Message{}).Where("user_id = ?", user.ID)
	if c.Query("unreadOnly") == "true" {
		q = q.Where("read = ?", false)
	}
	if msgType := c.Query("type"); msgType != "" {
		q = q.Where("type = ?", msgType)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		response.Error(c, "查询消息失败")
		return
	}
	var list []model.Message
	if err := q.Order("id desc").Offset((page - 1) * size).Limit(size).Find(&list).Error; err != nil {
		response.Error(c, "查询消息失败")
		return
	}
	response.OKPage(c, list, total, page, size)
}

// ReadMessage 标记单条已读，只能操作自己的消息
func (h *Handler) ReadMessage(c *gin.Context) {
	user := middleware.CurrentUser(c)
	now := time.Now()

	res := h.DB.Model(&model.Message{}).
		Where("id = ? AND user_id = ?", idParam(c), user.ID).
		Updates(map[string]any{"read": true, "read_at": &now})
	if res.Error != nil {
		response.Error(c, "标记失败")
		return
	}
	if res.RowsAffected == 0 {
		response.NotFound(c, "消息不存在")
		return
	}
	response.OK(c, nil)
}

// ReadAllMessages 全部标记已读
func (h *Handler) ReadAllMessages(c *gin.Context) {
	user := middleware.CurrentUser(c)
	now := time.Now()

	res := h.DB.Model(&model.Message{}).
		Where("user_id = ? AND read = ?", user.ID, false).
		Updates(map[string]any{"read": true, "read_at": &now})
	if res.Error != nil {
		response.Error(c, "标记失败")
		return
	}
	response.OK(c, gin.H{"updated": res.RowsAffected})
}

// fanoutMessages 按用户批量投递站内消息，返回成功条数
func (h *Handler) fanoutMessages(userIDs []uint, tpl model.Message) int {
	if len(userIDs) == 0 {
		return 0
	}
	messages := make([]model.Message, 0, len(userIDs))
	for _, uid := range userIDs {
		item := tpl
		item.UserID = uid
		messages = append(messages, item)
	}
	if err := h.DB.Create(&messages).Error; err != nil {
		log.Printf("[message] 站内消息投递失败: %v", err)
		return 0
	}
	return len(messages)
}

// notifyCriticalAlert 严重告警给持有处理权限的用户投站内消息
func (h *Handler) notifyCriticalAlert(alert model.Alert) {
	var userIDs []uint
	err := h.DB.Model(&model.User{}).
		Joins("JOIN user_roles ON user_roles.user_id = users.id").
		Joins("JOIN role_menus ON role_menus.role_id = user_roles.role_id").
		Joins("JOIN menus ON menus.id = role_menus.menu_id").
		Where("users.status = ? AND menus.auth_code = ?", 1, "alert:handle").
		Distinct().Pluck("users.id", &userIDs).Error
	if err != nil {
		log.Printf("[message] 查询告警处理人失败: %v", err)
		return
	}

	h.fanoutMessages(userIDs, model.Message{
		Type:  "alert",
		Title: fmt.Sprintf("[严重] %s", alert.Title),
		Content: fmt.Sprintf("来源 %s，当前值 %s。%s",
			orUnknown(alert.SourceName), orDash(alert.Value), alert.Summary),
		Level: "critical", RefID: alert.ID,
	})
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
