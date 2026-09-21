package handler

import (
	"context"
	"encoding/csv"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"ops-platform/server/internal/dbquery"
	"ops-platform/server/internal/middleware"
	"ops-platform/server/internal/model"
	"ops-platform/server/internal/response"
)

// 数据库只读查询：连到被纳管的实例上浏览元数据、跑只读 SQL。
//
// 与「数据库资产」的关系：资产那边只是登记（名称/地址/环境/标签），
// 这里第一次真正用上登记的账号密码 —— 之前 DBInstance.Secret 存了却没人读，
// 属于「有风险没收益」。
//
// 边界（刻意的）：
//   - 只支持 MySQL/MariaDB 与 PostgreSQL。Redis/Mongo 不在这套 SQL 抽象里。
//   - 只读。语句白名单 + 连接层只读会话两道闸，见 internal/dbquery/guard.go。
//   - 每次查询都落流水（含被拦下的），因为能查库等于能看业务数据本身。

const (
	dbQueryMaxRows    = 500
	dbQueryTimeout    = 30 * time.Second
	dbMetaTimeout     = 15 * time.Second
	dbExportMaxRows   = 10000
	dbQueryStmtMaxLen = 20000
)

// dbTarget 把资产记录转成连接参数
func dbTarget(item *model.DBInstance, schema string) dbquery.Target {
	name := item.DBName
	if schema != "" && item.Type == dbquery.TypeMySQL {
		// MySQL 的「库」就是 schema，直接连到目标库上，省掉每条语句写全限定名
		name = schema
	}
	return dbquery.Target{
		Type: item.Type, Address: item.Address, Port: item.Port,
		Username: item.Username, Secret: item.Secret, DBName: name,
	}
}

// loadQueryableDB 取实例并确认类型支持查询
func (h *Handler) loadQueryableDB(c *gin.Context) (*model.DBInstance, bool) {
	item, ok := h.loadDBScoped(c)
	if !ok {
		return nil, false
	}
	if !dbquery.Supported(item.Type) {
		response.BadRequest(c, fmt.Sprintf("%s 类型暂不支持在线查询，目前只支持 MySQL 与 PostgreSQL", item.Type))
		return nil, false
	}
	if strings.TrimSpace(item.Username) == "" || strings.TrimSpace(item.Secret) == "" {
		response.BadRequest(c, "这个实例没有登记账号或密码，先到「数据库资产」里补上才能查询")
		return nil, false
	}
	return item, true
}

// recordQueryLog 落一条查询流水。失败只记日志，不影响主流程返回。
func (h *Handler) recordQueryLog(c *gin.Context, item *model.DBInstance, schema, stmt, status, reason string, rows int, costMs int64, exported bool) {
	log := model.DBQueryLog{
		InstanceID: item.ID, InstanceName: item.Name, DBType: item.Type,
		Schema: schema, Statement: truncate(stmt, 4000), Status: status,
		Reason: truncate(reason, 240), Rows: rows, CostMs: costMs, Exported: exported,
		ClientIP: c.ClientIP(),
	}
	if user := middleware.CurrentUser(c); user != nil {
		log.UserID = user.ID
		log.Username = user.Username
	}
	_ = h.DB.Create(&log).Error
}

// ---------- 元数据 ----------

// ListDBSchemas 列出库 / 模式
func (h *Handler) ListDBSchemas(c *gin.Context) {
	item, ok := h.loadQueryableDB(c)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), dbMetaTimeout)
	defer cancel()

	conn, err := dbquery.Open(ctx, dbTarget(item, ""))
	if err != nil {
		response.Error(c, "连接数据库失败: "+err.Error())
		return
	}
	defer conn.Close()

	list, err := dbquery.ListSchemas(ctx, conn, item.Type)
	if err != nil {
		response.Error(c, "读取库列表失败: "+err.Error())
		return
	}
	response.OK(c, gin.H{"instance": item.Name, "dbType": item.Type, "list": list})
}

// ListDBTables 列出表与视图
func (h *Handler) ListDBTables(c *gin.Context) {
	item, ok := h.loadQueryableDB(c)
	if !ok {
		return
	}
	schema := strings.TrimSpace(c.Query("schema"))
	if schema == "" {
		response.BadRequest(c, "缺少 schema 参数")
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), dbMetaTimeout)
	defer cancel()

	conn, err := dbquery.Open(ctx, dbTarget(item, schema))
	if err != nil {
		response.Error(c, "连接数据库失败: "+err.Error())
		return
	}
	defer conn.Close()

	list, err := dbquery.ListTables(ctx, conn, item.Type, schema)
	if err != nil {
		response.Error(c, "读取表列表失败: "+err.Error())
		return
	}
	response.OK(c, gin.H{"schema": schema, "list": list})
}

// DescribeDBTable 表结构：列与索引
func (h *Handler) DescribeDBTable(c *gin.Context) {
	item, ok := h.loadQueryableDB(c)
	if !ok {
		return
	}
	schema := strings.TrimSpace(c.Query("schema"))
	table := strings.TrimSpace(c.Query("table"))
	if schema == "" || table == "" {
		response.BadRequest(c, "缺少 schema 或 table 参数")
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), dbMetaTimeout)
	defer cancel()

	conn, err := dbquery.Open(ctx, dbTarget(item, schema))
	if err != nil {
		response.Error(c, "连接数据库失败: "+err.Error())
		return
	}
	defer conn.Close()

	cols, idx, err := dbquery.DescribeTable(ctx, conn, item.Type, schema, table)
	if err != nil {
		response.Error(c, "读取表结构失败: "+err.Error())
		return
	}
	response.OK(c, gin.H{"schema": schema, "table": table, "columns": cols, "indexes": idx})
}

// ---------- 查询 ----------

type dbQueryReq struct {
	Schema    string `json:"schema"`
	Statement string `json:"statement"`
	Limit     int    `json:"limit"`
}

// RunDBQuery 执行一条只读查询
func (h *Handler) RunDBQuery(c *gin.Context) {
	item, ok := h.loadQueryableDB(c)
	if !ok {
		return
	}
	var req dbQueryReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "参数错误")
		return
	}
	raw := strings.TrimSpace(req.Statement)
	if raw == "" {
		response.BadRequest(c, "语句不能为空")
		return
	}
	if len(raw) > dbQueryStmtMaxLen {
		response.BadRequest(c, "语句太长（上限 20000 字符）")
		return
	}

	// 第一道闸：语句守卫。被拦下的语句压根不会下发到库，但要留痕。
	stmt, err := dbquery.Guard(raw)
	if err != nil {
		h.recordQueryLog(c, item, req.Schema, raw, "blocked", err.Error(), 0, 0, false)
		response.BadRequest(c, err.Error())
		return
	}

	limit := req.Limit
	if limit <= 0 || limit > dbQueryMaxRows {
		limit = dbQueryMaxRows
	}
	// 多取一行：只有看到第 limit+1 行才能确定「还有更多」。
	// 否则一张正好 500 行的表和一张 50 万行的表返回的东西一模一样，
	// 使用者不知道自己看到的是不是全部。
	stmt = dbquery.WithLimit(stmt, limit+1)

	ctx := c.Request.Context()
	conn, err := dbquery.Open(ctx, dbTarget(item, req.Schema))
	if err != nil {
		h.recordQueryLog(c, item, req.Schema, stmt, "failed", "连接失败: "+err.Error(), 0, 0, false)
		response.Error(c, "连接数据库失败: "+err.Error())
		return
	}
	defer conn.Close()

	res, err := dbquery.Query(ctx, conn, stmt, limit, dbQueryTimeout)
	if err != nil {
		h.recordQueryLog(c, item, req.Schema, stmt, "failed", err.Error(), 0, 0, false)
		response.Error(c, "查询失败: "+err.Error())
		return
	}
	h.recordQueryLog(c, item, req.Schema, stmt, "success", "", len(res.Rows), res.CostMs, false)
	response.OK(c, res)
}

// ExportDBQuery 导出查询结果为 CSV。单独算一种操作：这是数据外带。
func (h *Handler) ExportDBQuery(c *gin.Context) {
	item, ok := h.loadQueryableDB(c)
	if !ok {
		return
	}
	var req dbQueryReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "参数错误")
		return
	}
	stmt, err := dbquery.Guard(strings.TrimSpace(req.Statement))
	if err != nil {
		h.recordQueryLog(c, item, req.Schema, req.Statement, "blocked", err.Error(), 0, 0, true)
		response.BadRequest(c, err.Error())
		return
	}
	stmt = dbquery.WithLimit(stmt, dbExportMaxRows+1)

	ctx := c.Request.Context()
	conn, err := dbquery.Open(ctx, dbTarget(item, req.Schema))
	if err != nil {
		h.recordQueryLog(c, item, req.Schema, stmt, "failed", "连接失败: "+err.Error(), 0, 0, true)
		response.Error(c, "连接数据库失败: "+err.Error())
		return
	}
	defer conn.Close()

	res, err := dbquery.Query(ctx, conn, stmt, dbExportMaxRows, dbQueryTimeout)
	if err != nil {
		h.recordQueryLog(c, item, req.Schema, stmt, "failed", err.Error(), 0, 0, true)
		response.Error(c, "查询失败: "+err.Error())
		return
	}
	h.recordQueryLog(c, item, req.Schema, stmt, "success", "导出 CSV", len(res.Rows), res.CostMs, true)

	filename := fmt.Sprintf("query-%s-%s.csv", item.Name, time.Now().Format("20060102-150405"))
	c.Header("Content-Type", "text/csv; charset=utf-8")
	c.Header("Content-Disposition", "attachment; filename="+strconv.Quote(filename))
	if res.Truncated {
		c.Header("X-Export-Truncated", "1")
	}
	// BOM：不然 Excel 打开中文是乱码
	_, _ = c.Writer.Write([]byte{0xEF, 0xBB, 0xBF})
	w := csv.NewWriter(c.Writer)
	_ = w.Write(res.Columns)
	for _, row := range res.Rows {
		_ = w.Write(row)
	}
	w.Flush()
}

// ListDBQueryLogs 查询流水
func (h *Handler) ListDBQueryLogs(c *gin.Context) {
	page, size := pageParams(c)
	q := h.DB.Model(&model.DBQueryLog{})
	if id := strings.TrimSpace(c.Query("instanceId")); id != "" {
		q = q.Where("instance_id = ?", id)
	}
	if status := strings.TrimSpace(c.Query("status")); status != "" {
		q = q.Where("status = ?", status)
	}
	if user := strings.TrimSpace(c.Query("username")); user != "" {
		q = q.Where("username LIKE ?", "%"+user+"%")
	}
	if kw := strings.TrimSpace(c.Query("keyword")); kw != "" {
		q = q.Where("statement LIKE ?", "%"+kw+"%")
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		response.Error(c, "查询流水失败")
		return
	}
	var list []model.DBQueryLog
	if err := q.Order("id desc").Offset((page - 1) * size).Limit(size).Find(&list).Error; err != nil {
		response.Error(c, "查询流水失败")
		return
	}
	response.OKPage(c, list, total, page, size)
}

// CheckDBQueryStatement 只做语句预检，不连库。前端「预检」按钮用它，
// 让人在按下执行之前就知道会不会被拦。
func (h *Handler) CheckDBQueryStatement(c *gin.Context) {
	var req dbQueryReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "参数错误")
		return
	}
	stmt, err := dbquery.Guard(strings.TrimSpace(req.Statement))
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"code": 0, "msg": "ok", "data": gin.H{
			"allowed": false, "reason": err.Error(),
		}})
		return
	}
	limit := req.Limit
	if limit <= 0 || limit > dbQueryMaxRows {
		limit = dbQueryMaxRows
	}
	response.OK(c, gin.H{
		"allowed": true, "statement": dbquery.WithLimit(stmt, limit),
	})
}
