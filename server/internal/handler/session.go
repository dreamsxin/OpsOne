package handler

import (
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"ops-platform/server/internal/model"
	"ops-platform/server/internal/response"
)

// ListSessions 会话审计列表。
//
// 审计时问的是「上周三谁登过这台机器」「谁的会话里有被拦的命令」，
// 所以除了主机与操作人，时间范围、登录账号与风险级别都要能筛。
func (h *Handler) ListSessions(c *gin.Context) {
	page, size := pageParams(c)
	q := h.DB.Model(&model.Session{})

	if username := strings.TrimSpace(c.Query("username")); username != "" {
		q = q.Where("username LIKE ?", "%"+username+"%")
	}
	if keyword := strings.TrimSpace(c.Query("keyword")); keyword != "" {
		like := "%" + keyword + "%"
		q = q.Where("host_name LIKE ? OR address LIKE ?", like, like)
	}
	if loginUser := strings.TrimSpace(c.Query("loginUser")); loginUser != "" {
		q = q.Where("login_user LIKE ?", "%"+loginUser+"%")
	}
	if status := strings.TrimSpace(c.Query("status")); status != "" {
		q = q.Where("status = ?", status)
	}
	if raw := strings.TrimSpace(c.Query("hostId")); raw != "" {
		id, err := strconv.Atoi(raw)
		if err != nil || id <= 0 {
			response.BadRequest(c, "主机 ID 必须是正整数")
			return
		}
		q = q.Where("host_id = ?", id)
	}
	// risk 筛选不能只看 blocked_count：那个计数是会话结束时写回的，
	// 会话异常中断（进程被杀、网络断开）时可能停在 0，而命令明细已经落库了。
	// 所以「计数」与「命令证据」哪个命中都算。
	switch strings.TrimSpace(c.Query("risk")) {
	case "blocked":
		q = q.Where("blocked_count > 0 OR id IN (SELECT session_id FROM session_commands WHERE risk = ?)", "blocked")
	case "risky":
		q = q.Where("blocked_count > 0 OR id IN (SELECT session_id FROM session_commands WHERE risk IN ?)",
			[]string{"warn", "blocked"})
	case "":
	default:
		response.BadRequest(c, "风险筛选只能是 blocked 或 risky")
		return
	}
	// 兼容旧参数，语义等同 risk=blocked
	if c.Query("riskOnly") == "true" {
		q = q.Where("blocked_count > 0 OR id IN (SELECT session_id FROM session_commands WHERE risk = ?)", "blocked")
	}
	if raw := strings.TrimSpace(c.Query("start")); raw != "" {
		at, err := parseAuditTime(raw, false)
		if err != nil {
			response.BadRequest(c, err.Error())
			return
		}
		q = q.Where("started_at >= ?", at)
	}
	if raw := strings.TrimSpace(c.Query("end")); raw != "" {
		at, err := parseAuditTime(raw, true)
		if err != nil {
			response.BadRequest(c, err.Error())
			return
		}
		q = q.Where("started_at <= ?", at)
	}
	q = q.Session(&gorm.Session{})

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

// GetSession 会话详情与录像是否可用。
// 命令明细走 /sessions/:id/commands 分页取，长会话不一次性全推。
func (h *Handler) GetSession(c *gin.Context) {
	var session model.Session
	if err := h.DB.First(&session, idParam(c)).Error; err != nil {
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
