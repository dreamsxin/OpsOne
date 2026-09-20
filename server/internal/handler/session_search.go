package handler

import (
	"encoding/csv"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"ops-platform/server/internal/model"
	"ops-platform/server/internal/response"
)

// sessionCommandExportMaxRows 命令检索导出的行数上限
const sessionCommandExportMaxRows = 10000

// commandRow 跨会话命令检索的一行：命令本身 + 它属于哪次会话、谁在哪台机器上敲的
type commandRow struct {
	ID        uint      `json:"id"`
	SessionID uint      `json:"sessionId"`
	Command   string    `json:"command"`
	Risk      string    `json:"risk"`
	RuleID    uint      `json:"ruleId"`
	RuleDesc  string    `json:"ruleDesc"`
	OffsetMs  int64     `json:"offsetMs"`
	CreatedAt time.Time `json:"createdAt"`
	// 以下来自会话表，让检索结果不用再点进去才知道上下文
	Username  string `json:"username"`
	LoginUser string `json:"loginUser"`
	HostName  string `json:"hostName"`
	Address   string `json:"address"`
	ClientIP  string `json:"clientIp"`
}

// commandSearchQuery 组装跨会话命令检索的条件。
//
// 命令明细原先只能在单个会话详情里翻，回答不了「谁在哪台机器上敲过 rm -rf」
// 这种问题——那恰恰是审计时最常问的。这里把 session_commands 与 sessions join
// 起来，按命令内容、风险、操作人、主机、时间范围检索。
func (h *Handler) commandSearchQuery(c *gin.Context) (*gorm.DB, error) {
	q := h.DB.Table("session_commands as sc").
		Joins("JOIN sessions as s ON s.id = sc.session_id")

	if keyword := strings.TrimSpace(c.Query("keyword")); keyword != "" {
		q = q.Where("sc.command LIKE ?", "%"+keyword+"%")
	}
	if risk := strings.TrimSpace(c.Query("risk")); risk != "" {
		switch risk {
		case "normal", "warn", "blocked":
			q = q.Where("sc.risk = ?", risk)
		case "risky":
			// 「有风险」= 命中过规则的，warn 与 blocked 一起看
			q = q.Where("sc.risk IN ?", []string{"warn", "blocked"})
		default:
			return nil, fmt.Errorf("风险级别只能是 normal / warn / blocked / risky")
		}
	}
	if username := strings.TrimSpace(c.Query("username")); username != "" {
		q = q.Where("s.username LIKE ?", "%"+username+"%")
	}
	if loginUser := strings.TrimSpace(c.Query("loginUser")); loginUser != "" {
		q = q.Where("s.login_user LIKE ?", "%"+loginUser+"%")
	}
	if host := strings.TrimSpace(c.Query("host")); host != "" {
		like := "%" + host + "%"
		q = q.Where("s.host_name LIKE ? OR s.address LIKE ?", like, like)
	}
	if raw := strings.TrimSpace(c.Query("sessionId")); raw != "" {
		id, err := strconv.Atoi(raw)
		if err != nil || id <= 0 {
			return nil, fmt.Errorf("会话 ID 必须是正整数")
		}
		q = q.Where("sc.session_id = ?", id)
	}
	if raw := strings.TrimSpace(c.Query("start")); raw != "" {
		at, err := parseAuditTime(raw, false)
		if err != nil {
			return nil, err
		}
		q = q.Where("sc.created_at >= ?", at)
	}
	if raw := strings.TrimSpace(c.Query("end")); raw != "" {
		at, err := parseAuditTime(raw, true)
		if err != nil {
			return nil, err
		}
		q = q.Where("sc.created_at <= ?", at)
	}

	// 条件已经加完，Session 化一次再交出去：后面 Count 与 Find 复用同一个
	// *gorm.DB 时不会把彼此的条件串起来
	return q.Session(&gorm.Session{}), nil
}

// commandSelectFields join 查询要显式列出字段，否则两表同名列（id / created_at）会互相覆盖
const commandSelectFields = `sc.id as id, sc.session_id as session_id, sc.command as command,
	sc.risk as risk, sc.rule_id as rule_id, sc.rule_desc as rule_desc,
	sc.offset_ms as offset_ms, sc.created_at as created_at,
	s.username as username, s.login_user as login_user,
	s.host_name as host_name, s.address as address, s.client_ip as client_ip`

// SearchSessionCommands 跨会话检索命令
func (h *Handler) SearchSessionCommands(c *gin.Context) {
	q, err := h.commandSearchQuery(c)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	page, size := pageParams(c)

	var total int64
	if err := q.Count(&total).Error; err != nil {
		response.Error(c, "检索命令失败")
		return
	}
	// 命中拦截的条数单独给一个，界面上可以一眼看出这批结果里有多少是被拦下来的
	var blocked int64
	if err := q.Where("sc.risk = ?", "blocked").Count(&blocked).Error; err != nil {
		response.Error(c, "检索命令失败")
		return
	}

	var list []commandRow
	if err := q.Select(commandSelectFields).
		Order("sc.id desc").Offset((page - 1) * size).Limit(size).
		Find(&list).Error; err != nil {
		response.Error(c, "检索命令失败")
		return
	}
	response.OK(c, gin.H{
		"list": list, "total": total, "blocked": blocked,
		"page": page, "pageSize": size,
	})
}

// ExportSessionCommands 按当前检索条件导出 CSV
func (h *Handler) ExportSessionCommands(c *gin.Context) {
	q, err := h.commandSearchQuery(c)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	var list []commandRow
	if err := q.Select(commandSelectFields).
		Order("sc.id desc").Limit(sessionCommandExportMaxRows).
		Find(&list).Error; err != nil {
		response.Error(c, "导出失败")
		return
	}

	c.Header("Content-Type", "text/csv; charset=utf-8")
	c.Header("Content-Disposition", `attachment; filename="session-commands.csv"`)
	c.Header("X-Export-Truncated", strconv.FormatBool(len(list) >= sessionCommandExportMaxRows))
	_, _ = c.Writer.Write(utf8BOM)

	writer := csv.NewWriter(c.Writer)
	_ = writer.Write([]string{
		"id", "time", "sessionId", "username", "loginUser", "host", "address",
		"clientIp", "risk", "rule", "offsetSec", "command",
	})
	for _, item := range list {
		_ = writer.Write([]string{
			strconv.FormatUint(uint64(item.ID), 10),
			item.CreatedAt.Format("2006-01-02 15:04:05"),
			strconv.FormatUint(uint64(item.SessionID), 10),
			item.Username, item.LoginUser, item.HostName, item.Address, item.ClientIP,
			item.Risk, item.RuleDesc,
			strconv.FormatInt(item.OffsetMs/1000, 10),
			item.Command,
		})
	}
	writer.Flush()
}

// ListSessionCommands 单个会话的命令明细，分页返回。
//
// 原先是在会话详情里一次性 Preload 全部命令，长会话能敲出上千条，
// 一次全推给前端既慢又没必要。
func (h *Handler) ListSessionCommands(c *gin.Context) {
	sessionID := idParam(c)
	var exists int64
	if err := h.DB.Model(&model.Session{}).Where("id = ?", sessionID).Count(&exists).Error; err != nil || exists == 0 {
		response.NotFound(c, "会话不存在")
		return
	}

	page, size := pageParams(c)
	q := h.DB.Model(&model.SessionCommand{}).Where("session_id = ?", sessionID)
	if risk := strings.TrimSpace(c.Query("risk")); risk != "" {
		if risk == "risky" {
			q = q.Where("risk IN ?", []string{"warn", "blocked"})
		} else {
			q = q.Where("risk = ?", risk)
		}
	}
	if keyword := strings.TrimSpace(c.Query("keyword")); keyword != "" {
		q = q.Where("command LIKE ?", "%"+keyword+"%")
	}
	q = q.Session(&gorm.Session{})

	var total int64
	if err := q.Count(&total).Error; err != nil {
		response.Error(c, "查询命令失败")
		return
	}
	var list []model.SessionCommand
	if err := q.Order("id asc").Offset((page - 1) * size).Limit(size).
		Find(&list).Error; err != nil {
		response.Error(c, "查询命令失败")
		return
	}
	response.OKPage(c, list, total, page, size)
}
