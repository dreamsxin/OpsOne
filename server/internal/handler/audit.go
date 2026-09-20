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

// auditExportMaxRows 单次导出上限。审计表是平台里增长最快的表之一，
// 不设上限的话一次导出几十万行会把内存和浏览器都拖死。
const auditExportMaxRows = 10000

// parseAuditTime 解析检索用的时间点。
//
// 前端用 value-format="YYYY-MM-DD HH:mm:ss" 回传，这里额外兼容只给日期的情况
// （手工调接口时更顺手）。
func parseAuditTime(raw string, endOfDay bool) (*time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	if t, err := time.ParseInLocation("2006-01-02 15:04:05", raw, time.Local); err == nil {
		return &t, nil
	}
	t, err := time.ParseInLocation("2006-01-02", raw, time.Local)
	if err != nil {
		return nil, fmt.Errorf("时间格式应为 2006-01-02 或 2006-01-02 15:04:05")
	}
	if endOfDay {
		// 只给日期时，结束时间按当天 23:59:59 算，否则「查今天」会一条都查不到
		t = t.Add(24*time.Hour - time.Second)
	}
	return &t, nil
}

// auditQuery 按请求参数拼出审计检索条件，列表与导出共用同一套，
// 避免「界面上看到 20 条、导出却是另一批」。
func (h *Handler) auditQuery(c *gin.Context) (*gorm.DB, error) {
	q := h.DB.Model(&model.AuditLog{})

	if v := strings.TrimSpace(c.Query("username")); v != "" {
		q = q.Where("username LIKE ?", "%"+v+"%")
	}
	if v := strings.ToUpper(strings.TrimSpace(c.Query("method"))); v != "" {
		switch v {
		case "POST", "PUT", "DELETE", "PATCH":
			q = q.Where("method = ?", v)
		default:
			return nil, fmt.Errorf("method 只能是 POST / PUT / DELETE / PATCH")
		}
	}
	// 接口关键字同时匹配实际路径与路由模板：
	// 前者能按资源查（/hosts/3 就是 3 号主机身上发生的事），后者能按动作查（/hosts/:id）
	if v := strings.TrimSpace(c.Query("keyword")); v != "" {
		like := "%" + v + "%"
		q = q.Where("path LIKE ? OR action LIKE ?", like, like)
	}
	switch strings.TrimSpace(c.Query("result")) {
	case "":
	case "success":
		q = q.Where("status < 400")
	case "fail":
		q = q.Where("status >= 400")
	default:
		return nil, fmt.Errorf("result 只能是 success 或 fail")
	}
	if v := strings.TrimSpace(c.Query("ip")); v != "" {
		q = q.Where("ip LIKE ?", "%"+v+"%")
	}

	start, err := parseAuditTime(c.Query("start"), false)
	if err != nil {
		return nil, err
	}
	end, err := parseAuditTime(c.Query("end"), true)
	if err != nil {
		return nil, err
	}
	if start != nil && end != nil && end.Before(*start) {
		return nil, fmt.Errorf("结束时间不能早于开始时间")
	}
	if start != nil {
		q = q.Where("created_at >= ?", *start)
	}
	if end != nil {
		q = q.Where("created_at <= ?", *end)
	}
	// Session 让同一份条件可以被多次复用（计数、查失败数、取列表各起一条语句），
	// 直接复用 *gorm.DB 会把上一次链式调用的条件带进下一次
	return q.Session(&gorm.Session{}), nil
}

// ListAuditLogs 审计检索。除分页数据外额外给出命中条件里的失败条数，
// 排查时先看这个数字决定要不要只看失败。
func (h *Handler) ListAuditLogs(c *gin.Context) {
	page, size := pageParams(c)
	q, err := h.auditQuery(c)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	var total, failed int64
	if err := q.Count(&total).Error; err != nil {
		response.Error(c, "查询审计日志失败")
		return
	}
	// 用同一份条件再起一条语句统计失败数
	if err := q.Where("status >= 400").Count(&failed).Error; err != nil {
		response.Error(c, "查询审计日志失败")
		return
	}

	var list []model.AuditLog
	if err := q.Order("id desc").Offset((page - 1) * size).Limit(size).Find(&list).Error; err != nil {
		response.Error(c, "查询审计日志失败")
		return
	}
	response.OK(c, gin.H{
		"list": list, "total": total, "page": page, "pageSize": size,
		"failed": failed,
	})
}

// AuditOperators 列出审计里出现过的操作人，给检索下拉用。
// 不查 users 表：已删除的账号在审计里仍然留痕，这里要的是「谁动过手」。
func (h *Handler) AuditOperators(c *gin.Context) {
	var names []string
	if err := h.DB.Model(&model.AuditLog{}).
		Where("username <> ''").
		Distinct().Order("username asc").
		Pluck("username", &names).Error; err != nil {
		response.Error(c, "查询操作人失败")
		return
	}
	response.OK(c, names)
}

// ExportAuditLogs 按当前检索条件导出 CSV。
func (h *Handler) ExportAuditLogs(c *gin.Context) {
	q, err := h.auditQuery(c)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	var list []model.AuditLog
	if err := q.Order("id desc").Limit(auditExportMaxRows).Find(&list).Error; err != nil {
		response.Error(c, "导出失败")
		return
	}

	c.Header("Content-Type", "text/csv; charset=utf-8")
	c.Header("Content-Disposition", `attachment; filename="audit-logs.csv"`)
	// 超过上限时只导出最近的部分，用响应头说明，避免以为导全了
	c.Header("X-Export-Truncated", strconv.FormatBool(len(list) >= auditExportMaxRows))
	_, _ = c.Writer.Write(utf8BOM)

	writer := csv.NewWriter(c.Writer)
	_ = writer.Write([]string{"id", "time", "username", "method", "path", "action", "status", "ip", "costMs"})
	for _, item := range list {
		_ = writer.Write([]string{
			strconv.FormatUint(uint64(item.ID), 10),
			item.CreatedAt.Format("2006-01-02 15:04:05"),
			item.Username, item.Method, item.Path, item.Action,
			strconv.Itoa(item.Status), item.IP,
			strconv.FormatInt(item.CostMs, 10),
		})
	}
	writer.Flush()
}
