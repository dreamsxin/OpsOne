package handler

import (
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"ops-platform/server/internal/middleware"
	"ops-platform/server/internal/model"
	"ops-platform/server/internal/response"
)

// ---------- 云账号 ----------

type cloudAccountReq struct {
	Name            string `json:"name" binding:"required"`
	Provider        string `json:"provider"`
	AccessKeyID     string `json:"accessKeyId"`
	AccessKeySecret string `json:"accessKeySecret"` // 留空表示不修改
	Region          string `json:"region"`
	AccountID       string `json:"accountId"`
	DeptID          uint   `json:"deptId"`
	Enabled         *bool  `json:"enabled"`
	Remark          string `json:"remark"`
}

func (h *Handler) ListCloudAccounts(c *gin.Context) {
	q := h.applyScope(h.DB.Model(&model.CloudAccount{}), middleware.CurrentUser(c))
	if provider := c.Query("provider"); provider != "" {
		q = q.Where("provider = ?", provider)
	}

	var list []model.CloudAccount
	if err := q.Order("id desc").Find(&list).Error; err != nil {
		response.Error(c, "查询云账号失败")
		return
	}
	response.OK(c, list)
}

func (h *Handler) CreateCloudAccount(c *gin.Context) {
	var req cloudAccountReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "账号名称不能为空")
		return
	}

	item := model.CloudAccount{
		Name: req.Name, Provider: normalizeProvider(req.Provider),
		AccessKeyID: req.AccessKeyID, AccessKeySecret: h.sealSecret(req.AccessKeySecret),
		Region: req.Region, AccountID: req.AccountID, DeptID: req.DeptID,
		Enabled: true, Remark: req.Remark,
		CreatedBy: middleware.CurrentUser(c).ID,
	}
	if req.Enabled != nil {
		item.Enabled = *req.Enabled
	}
	if err := h.DB.Create(&item).Error; err != nil {
		response.Error(c, "创建失败")
		return
	}
	response.OK(c, item)
}

func (h *Handler) loadCloudScoped(c *gin.Context) (*model.CloudAccount, bool) {
	var item model.CloudAccount
	if err := h.DB.First(&item, idParam(c)).Error; err != nil {
		response.NotFound(c, "云账号不存在")
		return nil, false
	}
	if !h.resourceVisible(middleware.CurrentUser(c), item.DeptID, item.CreatedBy) {
		response.NotFound(c, "云账号不存在")
		return nil, false
	}
	return &item, true
}

func (h *Handler) UpdateCloudAccount(c *gin.Context) {
	itemPtr, ok := h.loadCloudScoped(c)
	if !ok {
		return
	}
	item := *itemPtr

	var req cloudAccountReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "参数校验失败")
		return
	}

	item.Name, item.Provider = req.Name, normalizeProvider(req.Provider)
	item.AccessKeyID, item.Region = req.AccessKeyID, req.Region
	item.AccountID, item.DeptID, item.Remark = req.AccountID, req.DeptID, req.Remark
	if req.AccessKeySecret != "" {
		item.AccessKeySecret = h.sealSecret(req.AccessKeySecret)
	}
	if req.Enabled != nil {
		item.Enabled = *req.Enabled
	}
	if err := h.DB.Save(&item).Error; err != nil {
		response.Error(c, "更新失败")
		return
	}
	response.OK(c, item)
}

func (h *Handler) DeleteCloudAccount(c *gin.Context) {
	item, ok := h.loadCloudScoped(c)
	if !ok {
		return
	}
	if err := h.DB.Delete(&model.CloudAccount{}, item.ID).Error; err != nil {
		response.Error(c, "删除失败")
		return
	}
	response.OK(c, nil)
}

func normalizeProvider(v string) string {
	switch v {
	case "tencent", "huawei", "aws", "other":
		return v
	default:
		return "aliyun"
	}
}

// ---------- 资产盘点 ----------

type inventoryBatchReq struct {
	Name        string `json:"name" binding:"required"`
	ScopeDeptID uint   `json:"scopeDeptId"`
	Remark      string `json:"remark"`
}

func (h *Handler) ListInventoryBatches(c *gin.Context) {
	page, size := pageParams(c)
	q := h.applyScope(h.DB.Model(&model.InventoryBatch{}), middleware.CurrentUser(c))
	if status := c.Query("status"); status != "" {
		q = q.Where("status = ?", status)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		response.Error(c, "查询盘点批次失败")
		return
	}
	var list []model.InventoryBatch
	if err := q.Order("id desc").Offset((page - 1) * size).Limit(size).Find(&list).Error; err != nil {
		response.Error(c, "查询盘点批次失败")
		return
	}
	response.OKPage(c, list, total, page, size)
}

// CreateInventoryBatch 创建批次并按范围对固定资产做快照。
// 快照后新增或删除的资产不会影响已建批次，保证盘点结果可复查。
func (h *Handler) CreateInventoryBatch(c *gin.Context) {
	var req inventoryBatchReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "批次名称不能为空")
		return
	}

	user := middleware.CurrentUser(c)
	q := h.applyScope(h.DB.Model(&model.FixedAsset{}), user).
		Where("status <> ?", "scrapped")
	if req.ScopeDeptID != 0 {
		q = q.Where("dept_id = ?", req.ScopeDeptID)
	}

	var assets []model.FixedAsset
	if err := q.Find(&assets).Error; err != nil {
		response.Error(c, "读取资产失败")
		return
	}
	if len(assets) == 0 {
		response.BadRequest(c, "该范围内没有可盘点的资产（已报废资产不参与盘点）")
		return
	}

	batch := model.InventoryBatch{
		Name: req.Name, ScopeDeptID: req.ScopeDeptID, Status: "ongoing",
		Operator: user.Username, DeptID: req.ScopeDeptID, CreatedBy: user.ID,
		TotalCount: len(assets), Remark: req.Remark, StartedAt: time.Now(),
	}
	if err := h.DB.Create(&batch).Error; err != nil {
		response.Error(c, "批次创建失败")
		return
	}

	items := make([]model.InventoryItem, 0, len(assets))
	for _, asset := range assets {
		items = append(items, model.InventoryItem{
			BatchID: batch.ID, AssetID: asset.ID, AssetName: asset.Name,
			SN: asset.SN, ExpectLocation: asset.Location, Result: "pending",
		})
	}
	if err := h.DB.Create(&items).Error; err != nil {
		response.Error(c, "盘点明细生成失败")
		return
	}

	batch.Items = items
	response.OK(c, batch)
}

func (h *Handler) GetInventoryBatch(c *gin.Context) {
	var batch model.InventoryBatch
	if err := h.DB.Preload("Items").First(&batch, idParam(c)).Error; err != nil {
		response.NotFound(c, "批次不存在")
		return
	}
	if !h.resourceVisible(middleware.CurrentUser(c), batch.DeptID, batch.CreatedBy) {
		response.NotFound(c, "批次不存在")
		return
	}
	response.OK(c, batch)
}

type inventoryItemReq struct {
	Result         string `json:"result" binding:"required"` // matched | missing | moved
	ActualLocation string `json:"actualLocation"`
	Note           string `json:"note"`
}

// UpdateInventoryItem 登记单条盘点结果，并回写批次统计
func (h *Handler) UpdateInventoryItem(c *gin.Context) {
	var item model.InventoryItem
	if err := h.DB.First(&item, idParam(c)).Error; err != nil {
		response.NotFound(c, "盘点明细不存在")
		return
	}

	var batch model.InventoryBatch
	if err := h.DB.First(&batch, item.BatchID).Error; err != nil {
		response.NotFound(c, "批次不存在")
		return
	}
	if batch.Status == "finished" {
		response.BadRequest(c, "批次已完成，不能再修改盘点结果")
		return
	}

	var req inventoryItemReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "请提供盘点结果")
		return
	}
	result := normalizeInventoryResult(req.Result)
	if result == "" {
		response.BadRequest(c, "盘点结果只能是 matched / missing / moved")
		return
	}
	if result == "moved" && strings.TrimSpace(req.ActualLocation) == "" {
		response.BadRequest(c, "标记位置变更时必须填写实际位置")
		return
	}

	now := time.Now()
	user := middleware.CurrentUser(c)
	err := h.DB.Model(&item).Updates(map[string]any{
		"result": result, "actual_location": req.ActualLocation, "note": req.Note,
		"checked_by": user.Username, "checked_at": &now,
	}).Error
	if err != nil {
		response.Error(c, "登记失败")
		return
	}

	h.refreshInventoryStats(batch.ID)
	response.OK(c, nil)
}

// FinishInventoryBatch 完成批次。未盘到的明细统一按缺失处理。
func (h *Handler) FinishInventoryBatch(c *gin.Context) {
	var batch model.InventoryBatch
	if err := h.DB.First(&batch, idParam(c)).Error; err != nil {
		response.NotFound(c, "批次不存在")
		return
	}
	if !h.resourceVisible(middleware.CurrentUser(c), batch.DeptID, batch.CreatedBy) {
		response.NotFound(c, "批次不存在")
		return
	}
	if batch.Status == "finished" {
		response.BadRequest(c, "批次已完成")
		return
	}

	now := time.Now()
	user := middleware.CurrentUser(c)
	h.DB.Model(&model.InventoryItem{}).
		Where("batch_id = ? AND result = ?", batch.ID, "pending").
		Updates(map[string]any{
			"result": "missing", "note": "批次结束时仍未盘到",
			"checked_by": user.Username, "checked_at": &now,
		})

	h.refreshInventoryStats(batch.ID)
	h.DB.Model(&batch).Updates(map[string]any{"status": "finished", "finished_at": &now})

	var updated model.InventoryBatch
	h.DB.First(&updated, batch.ID)
	response.OK(c, updated)
}

func (h *Handler) DeleteInventoryBatch(c *gin.Context) {
	var batch model.InventoryBatch
	if err := h.DB.First(&batch, idParam(c)).Error; err != nil {
		response.NotFound(c, "批次不存在")
		return
	}
	if !h.resourceVisible(middleware.CurrentUser(c), batch.DeptID, batch.CreatedBy) {
		response.NotFound(c, "批次不存在")
		return
	}
	if err := h.DB.Delete(&batch).Error; err != nil {
		response.Error(c, "删除失败")
		return
	}
	h.DB.Where("batch_id = ?", batch.ID).Delete(&model.InventoryItem{})
	response.OK(c, nil)
}

// refreshInventoryStats 重算批次计数，避免累加时漏掉「改结果」的场景
func (h *Handler) refreshInventoryStats(batchID uint) {
	count := func(result string) int {
		var n int64
		q := h.DB.Model(&model.InventoryItem{}).Where("batch_id = ?", batchID)
		if result != "" {
			q = q.Where("result = ?", result)
		} else {
			q = q.Where("result <> ?", "pending")
		}
		q.Count(&n)
		return int(n)
	}

	h.DB.Model(&model.InventoryBatch{}).Where("id = ?", batchID).Updates(map[string]any{
		"checked_count": count(""),
		"matched_count": count("matched"),
		"missing_count": count("missing"),
		"moved_count":   count("moved"),
	})
}

func normalizeInventoryResult(v string) string {
	switch v {
	case "matched", "missing", "moved":
		return v
	default:
		return ""
	}
}
