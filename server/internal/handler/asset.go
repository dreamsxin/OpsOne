package handler

import (
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"ops-platform/server/internal/middleware"
	"ops-platform/server/internal/model"
	"ops-platform/server/internal/response"
)

// ---------- 标签字典 ----------

type tagReq struct {
	Name     string `json:"name" binding:"required"`
	Category string `json:"category"`
	Color    string `json:"color"`
	Remark   string `json:"remark"`
}

// tagView 字典条目附带在主机与数据库上的使用次数
type tagView struct {
	model.Tag
	HostCount int `json:"hostCount"`
	DBCount   int `json:"dbCount"`
}

// ListTags 标签字典，统计每个标签被多少资产使用
func (h *Handler) ListTags(c *gin.Context) {
	var tags []model.Tag
	if err := h.DB.Order("category asc, id asc").Find(&tags).Error; err != nil {
		response.Error(c, "查询标签失败")
		return
	}

	// tags 字段是逗号分隔文本，这里一次性取出再在内存里统计，避免 N 次 LIKE 查询
	var hostTags, dbTags []string
	h.DB.Model(&model.Host{}).Pluck("tags", &hostTags)
	h.DB.Model(&model.DBInstance{}).Pluck("tags", &dbTags)

	hostCount := countTagUsage(hostTags)
	dbCount := countTagUsage(dbTags)

	views := make([]tagView, 0, len(tags))
	for _, tag := range tags {
		views = append(views, tagView{
			Tag: tag, HostCount: hostCount[tag.Name], DBCount: dbCount[tag.Name],
		})
	}
	response.OK(c, views)
}

func countTagUsage(rows []string) map[string]int {
	counter := map[string]int{}
	for _, row := range rows {
		for _, item := range strings.Split(row, ",") {
			name := strings.TrimSpace(item)
			if name != "" {
				counter[name]++
			}
		}
	}
	return counter
}

func (h *Handler) CreateTag(c *gin.Context) {
	var req tagReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "标签名称不能为空")
		return
	}
	name := strings.TrimSpace(req.Name)
	if strings.Contains(name, ",") {
		response.BadRequest(c, "标签名称不能包含逗号")
		return
	}

	tag := model.Tag{
		Name: name, Category: orDefault(req.Category, "general"),
		Color: orDefault(req.Color, "info"), Remark: req.Remark,
	}
	if err := h.DB.Create(&tag).Error; err != nil {
		response.BadRequest(c, "创建失败，标签可能已存在")
		return
	}
	response.OK(c, tag)
}

func (h *Handler) UpdateTag(c *gin.Context) {
	var tag model.Tag
	if err := h.DB.First(&tag, idParam(c)).Error; err != nil {
		response.NotFound(c, "标签不存在")
		return
	}
	var req tagReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "参数校验失败")
		return
	}

	tag.Category = orDefault(req.Category, "general")
	tag.Color = orDefault(req.Color, "info")
	tag.Remark = req.Remark
	if err := h.DB.Save(&tag).Error; err != nil {
		response.Error(c, "标签更新失败")
		return
	}
	response.OK(c, tag)
}

// DeleteTag 删除字典条目。已打在资产上的标签文本不会被改动，只是从词表移除。
func (h *Handler) DeleteTag(c *gin.Context) {
	if err := h.DB.Delete(&model.Tag{}, idParam(c)).Error; err != nil {
		response.Error(c, "标签删除失败")
		return
	}
	response.OK(c, nil)
}

// ---------- 数据库资产 ----------

type dbInstanceReq struct {
	Name     string `json:"name" binding:"required"`
	Type     string `json:"type"`
	Address  string `json:"address" binding:"required"`
	Port     int    `json:"port"`
	Username string `json:"username"`
	Secret   string `json:"secret"` // 留空表示不修改
	DBName   string `json:"dbName"`
	Version  string `json:"version"`
	Env      string `json:"env"`
	DeptID   uint   `json:"deptId"`
	Tags     string `json:"tags"`
	Remark   string `json:"remark"`
}

func (h *Handler) ListDBInstances(c *gin.Context) {
	page, size := pageParams(c)
	q := h.applyScope(h.DB.Model(&model.DBInstance{}), middleware.CurrentUser(c))

	if kw := strings.TrimSpace(c.Query("keyword")); kw != "" {
		like := "%" + kw + "%"
		q = q.Where("name LIKE ? OR address LIKE ? OR db_name LIKE ? OR tags LIKE ?", like, like, like, like)
	}
	if dbType := c.Query("type"); dbType != "" {
		q = q.Where("type = ?", dbType)
	}
	if env := c.Query("env"); env != "" {
		q = q.Where("env = ?", env)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		response.Error(c, "查询数据库资产失败")
		return
	}
	var list []model.DBInstance
	if err := q.Order("id desc").Offset((page - 1) * size).Limit(size).Find(&list).Error; err != nil {
		response.Error(c, "查询数据库资产失败")
		return
	}
	response.OKPage(c, list, total, page, size)
}

func (h *Handler) CreateDBInstance(c *gin.Context) {
	var req dbInstanceReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "名称与地址为必填项")
		return
	}

	item := model.DBInstance{
		Name: req.Name, Type: normalizeDBType(req.Type), Address: req.Address,
		Port:     defaultDBPort(req.Port, normalizeDBType(req.Type)),
		Username: req.Username, Secret: req.Secret, DBName: req.DBName,
		Version: req.Version, Env: defaultEnv(req.Env), DeptID: req.DeptID,
		Tags: req.Tags, Remark: req.Remark, Status: "unknown",
		CreatedBy: middleware.CurrentUser(c).ID,
	}
	if err := h.DB.Create(&item).Error; err != nil {
		response.Error(c, "创建失败")
		return
	}
	response.OK(c, item)
}

// loadDBScoped 按数据权限加载数据库实例
func (h *Handler) loadDBScoped(c *gin.Context) (*model.DBInstance, bool) {
	var item model.DBInstance
	if err := h.DB.First(&item, idParam(c)).Error; err != nil {
		response.NotFound(c, "数据库资产不存在")
		return nil, false
	}
	if !h.resourceVisible(middleware.CurrentUser(c), item.DeptID, item.CreatedBy) {
		response.NotFound(c, "数据库资产不存在")
		return nil, false
	}
	return &item, true
}

func (h *Handler) UpdateDBInstance(c *gin.Context) {
	itemPtr, ok := h.loadDBScoped(c)
	if !ok {
		return
	}
	item := *itemPtr

	var req dbInstanceReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "参数校验失败")
		return
	}

	item.Name, item.Type = req.Name, normalizeDBType(req.Type)
	item.Address, item.Port = req.Address, defaultDBPort(req.Port, normalizeDBType(req.Type))
	item.Username, item.DBName, item.Version = req.Username, req.DBName, req.Version
	item.Env, item.DeptID, item.Tags, item.Remark = defaultEnv(req.Env), req.DeptID, req.Tags, req.Remark
	if req.Secret != "" {
		item.Secret = req.Secret
	}
	if err := h.DB.Save(&item).Error; err != nil {
		response.Error(c, "更新失败")
		return
	}
	response.OK(c, item)
}

func (h *Handler) DeleteDBInstance(c *gin.Context) {
	item, ok := h.loadDBScoped(c)
	if !ok {
		return
	}
	if err := h.DB.Delete(&model.DBInstance{}, item.ID).Error; err != nil {
		response.Error(c, "删除失败")
		return
	}
	response.OK(c, nil)
}

// CheckDBInstance 连通性探测。
//
// 注意：这里只做 TCP 端口可达性检测，不做数据库协议握手与账号验证 ——
// 平台不内置各数据库驱动，端口通不代表账号密码可用。
func (h *Handler) CheckDBInstance(c *gin.Context) {
	item, ok := h.loadDBScoped(c)
	if !ok {
		return
	}

	start := time.Now()
	addr := net.JoinHostPort(item.Address, strconv.Itoa(item.Port))
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	cost := time.Since(start).Milliseconds()

	now := time.Now()
	status := "offline"
	detail := ""
	if err == nil {
		_ = conn.Close()
		status = "online"
	} else {
		detail = err.Error()
	}

	h.DB.Model(&model.DBInstance{}).Where("id = ?", item.ID).
		Updates(map[string]any{"status": status, "checked_at": &now})

	response.OK(c, gin.H{
		"status": status, "costMs": cost, "detail": detail,
		"note": "仅检测 TCP 端口可达性，未做账号认证",
	})
}

func normalizeDBType(t string) string {
	switch t {
	case "postgres", "redis", "mongo", "other":
		return t
	default:
		return "mysql"
	}
}

func defaultDBPort(port int, dbType string) int {
	if port > 0 && port <= 65535 {
		return port
	}
	switch dbType {
	case "postgres":
		return 5432
	case "redis":
		return 6379
	case "mongo":
		return 27017
	default:
		return 3306
	}
}

// ---------- 固定资产 ----------

type fixedAssetReq struct {
	Name          string  `json:"name" binding:"required"`
	Category      string  `json:"category"`
	SN            string  `json:"sn"`
	Model         string  `json:"model"`
	Vendor        string  `json:"vendor"`
	Location      string  `json:"location"`
	Owner         string  `json:"owner"`
	HostID        uint    `json:"hostId"`
	DeptID        uint    `json:"deptId"`
	Status        string  `json:"status"`
	PurchaseDate  string  `json:"purchaseDate"` // YYYY-MM-DD
	PurchasePrice float64 `json:"purchasePrice"`
	WarrantyEnd   string  `json:"warrantyEnd"` // YYYY-MM-DD
	Remark        string  `json:"remark"`
}

func (h *Handler) ListFixedAssets(c *gin.Context) {
	page, size := pageParams(c)
	q := h.applyScope(h.DB.Model(&model.FixedAsset{}), middleware.CurrentUser(c))

	if kw := strings.TrimSpace(c.Query("keyword")); kw != "" {
		like := "%" + kw + "%"
		q = q.Where("name LIKE ? OR sn LIKE ? OR model LIKE ? OR location LIKE ?", like, like, like, like)
	}
	if category := c.Query("category"); category != "" {
		q = q.Where("category = ?", category)
	}
	if status := c.Query("status"); status != "" {
		q = q.Where("status = ?", status)
	}
	// 到保筛选：warrantyDays=30 表示 30 天内到保（含已过保）
	if days := c.Query("warrantyDays"); days != "" {
		if n, err := strconv.Atoi(days); err == nil && n > 0 {
			deadline := time.Now().AddDate(0, 0, n)
			q = q.Where("warranty_end IS NOT NULL AND warranty_end <= ?", deadline)
		}
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		response.Error(c, "查询固定资产失败")
		return
	}
	var list []model.FixedAsset
	if err := q.Order("id desc").Offset((page - 1) * size).Limit(size).Find(&list).Error; err != nil {
		response.Error(c, "查询固定资产失败")
		return
	}
	response.OKPage(c, list, total, page, size)
}

// FixedAssetStats 台账概览：状态分布、到保提醒、资产原值合计
func (h *Handler) FixedAssetStats(c *gin.Context) {
	user := middleware.CurrentUser(c)
	count := func(conds ...any) int64 {
		var n int64
		q := h.applyScope(h.DB.Model(&model.FixedAsset{}), user)
		if len(conds) > 0 {
			q = q.Where(conds[0], conds[1:]...)
		}
		q.Count(&n)
		return n
	}

	var totalPrice float64
	h.applyScope(h.DB.Model(&model.FixedAsset{}), user).
		Select("COALESCE(SUM(purchase_price), 0)").Scan(&totalPrice)

	now := time.Now()
	response.OK(c, gin.H{
		"total":      count(),
		"inUse":      count("status = ?", "in_use"),
		"idle":       count("status = ?", "idle"),
		"repair":     count("status = ?", "repair"),
		"scrapped":   count("status = ?", "scrapped"),
		"expiring30": count("warranty_end IS NOT NULL AND warranty_end > ? AND warranty_end <= ?", now, now.AddDate(0, 0, 30)),
		"expired":    count("warranty_end IS NOT NULL AND warranty_end <= ?", now),
		"totalPrice": totalPrice,
	})
}

func (h *Handler) CreateFixedAsset(c *gin.Context) {
	var req fixedAssetReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "资产名称不能为空")
		return
	}
	purchase, err := parseDate(req.PurchaseDate)
	if err != nil {
		response.BadRequest(c, "采购日期格式应为 YYYY-MM-DD")
		return
	}
	warranty, err := parseDate(req.WarrantyEnd)
	if err != nil {
		response.BadRequest(c, "保修到期格式应为 YYYY-MM-DD")
		return
	}

	item := model.FixedAsset{
		Name: req.Name, Category: normalizeAssetCategory(req.Category), SN: req.SN,
		Model: req.Model, Vendor: req.Vendor, Location: req.Location, Owner: req.Owner,
		HostID: req.HostID, DeptID: req.DeptID, Status: normalizeAssetStatus(req.Status),
		PurchaseDate: purchase, PurchasePrice: req.PurchasePrice, WarrantyEnd: warranty,
		Remark: req.Remark, CreatedBy: middleware.CurrentUser(c).ID,
	}
	if err := h.DB.Create(&item).Error; err != nil {
		response.Error(c, "创建失败")
		return
	}
	response.OK(c, item)
}

func (h *Handler) loadAssetScoped(c *gin.Context) (*model.FixedAsset, bool) {
	var item model.FixedAsset
	if err := h.DB.First(&item, idParam(c)).Error; err != nil {
		response.NotFound(c, "固定资产不存在")
		return nil, false
	}
	if !h.resourceVisible(middleware.CurrentUser(c), item.DeptID, item.CreatedBy) {
		response.NotFound(c, "固定资产不存在")
		return nil, false
	}
	return &item, true
}

func (h *Handler) UpdateFixedAsset(c *gin.Context) {
	itemPtr, ok := h.loadAssetScoped(c)
	if !ok {
		return
	}
	item := *itemPtr

	var req fixedAssetReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "参数校验失败")
		return
	}
	purchase, err := parseDate(req.PurchaseDate)
	if err != nil {
		response.BadRequest(c, "采购日期格式应为 YYYY-MM-DD")
		return
	}
	warranty, err := parseDate(req.WarrantyEnd)
	if err != nil {
		response.BadRequest(c, "保修到期格式应为 YYYY-MM-DD")
		return
	}

	item.Name, item.Category = req.Name, normalizeAssetCategory(req.Category)
	item.SN, item.Model, item.Vendor = req.SN, req.Model, req.Vendor
	item.Location, item.Owner, item.HostID = req.Location, req.Owner, req.HostID
	item.DeptID, item.Status = req.DeptID, normalizeAssetStatus(req.Status)
	item.PurchaseDate, item.PurchasePrice, item.WarrantyEnd = purchase, req.PurchasePrice, warranty
	item.Remark = req.Remark

	if err := h.DB.Save(&item).Error; err != nil {
		response.Error(c, "更新失败")
		return
	}
	response.OK(c, item)
}

func (h *Handler) DeleteFixedAsset(c *gin.Context) {
	item, ok := h.loadAssetScoped(c)
	if !ok {
		return
	}
	if err := h.DB.Delete(&model.FixedAsset{}, item.ID).Error; err != nil {
		response.Error(c, "删除失败")
		return
	}
	response.OK(c, nil)
}

func parseDate(raw string) (*time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	// 前端可能回传带时间的 ISO 串，这里只取日期部分
	if len(raw) > 10 {
		raw = raw[:10]
	}
	t, err := time.ParseInLocation("2006-01-02", raw, time.Local)
	if err != nil {
		return nil, fmt.Errorf("日期格式非法")
	}
	return &t, nil
}

func normalizeAssetCategory(v string) string {
	switch v {
	case "network", "storage", "terminal", "other":
		return v
	default:
		return "server"
	}
}

func normalizeAssetStatus(v string) string {
	switch v {
	case "idle", "repair", "scrapped":
		return v
	default:
		return "in_use"
	}
}
