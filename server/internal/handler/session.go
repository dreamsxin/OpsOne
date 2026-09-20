package handler

import (
	"net/http"
	"os"

	"github.com/gin-gonic/gin"

	"ops-platform/server/internal/model"
	"ops-platform/server/internal/response"
)

// ListSessions 会话审计列表
func (h *Handler) ListSessions(c *gin.Context) {
	page, size := pageParams(c)
	q := h.DB.Model(&model.Session{})

	if username := c.Query("username"); username != "" {
		q = q.Where("username LIKE ?", "%"+username+"%")
	}
	if keyword := c.Query("keyword"); keyword != "" {
		like := "%" + keyword + "%"
		q = q.Where("host_name LIKE ? OR address LIKE ?", like, like)
	}
	if status := c.Query("status"); status != "" {
		q = q.Where("status = ?", status)
	}
	if c.Query("riskOnly") == "true" {
		q = q.Where("blocked_count > 0")
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		response.Error(c, "查询会话失败")
		return
	}
	var list []model.Session
	if err := q.Order("id desc").Offset((page - 1) * size).Limit(size).Find(&list).Error; err != nil {
		response.Error(c, "查询会话失败")
		return
	}
	response.OKPage(c, list, total, page, size)
}

// GetSession 会话详情，含命令明细与录像是否可用
func (h *Handler) GetSession(c *gin.Context) {
	var session model.Session
	if err := h.DB.Preload("Commands").First(&session, idParam(c)).Error; err != nil {
		response.NotFound(c, "会话不存在")
		return
	}

	replayable := false
	if session.RecordPath != "" {
		if _, err := os.Stat(session.RecordPath); err == nil {
			replayable = true
		}
	}

	response.OK(c, gin.H{"session": session, "replayable": replayable})
}

// ReplaySession 返回 asciinema v2 录像原文，由前端播放器解析
func (h *Handler) ReplaySession(c *gin.Context) {
	var session model.Session
	if err := h.DB.First(&session, idParam(c)).Error; err != nil {
		response.NotFound(c, "会话不存在")
		return
	}
	if session.RecordPath == "" {
		response.NotFound(c, "该会话没有录像")
		return
	}
	// 路径只来自数据库写入，不接受外部输入
	content, err := os.ReadFile(session.RecordPath)
	if err != nil {
		response.NotFound(c, "录像文件已丢失")
		return
	}
	c.Data(http.StatusOK, "application/x-asciicast; charset=utf-8", content)
}
