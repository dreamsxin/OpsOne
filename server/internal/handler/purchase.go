package handler

import (
	"fmt"
	"time"

	"github.com/gin-gonic/gin"

	"ops-platform/server/internal/middleware"
	"ops-platform/server/internal/model"
	"ops-platform/server/internal/response"
)

type purchaseItemReq struct {
	Name      string  `json:"name" binding:"required"`
	Category  string  `json:"category"`
	Model     string  `json:"model"`
	Vendor    string  `json:"vendor"`
	Quantity  int     `json:"quantity"`
	UnitPrice float64 `json:"unitPrice"`
	Remark    string  `json:"remark"`
}

type purchaseOrderReq struct {
	OrderNo      string            `json:"orderNo" binding:"required"`
	Title        string            `json:"title" binding:"required"`
	Vendor       string            `json:"vendor"`
	Applicant    string            `json:"applicant"`
	Status       string            `json:"status"`
	DeptID       uint              `json:"deptId"`
	OrderDate    string            `json:"orderDate"`
	ExpectedDate string            `json:"expectedDate"`
	Remark       string            `json:"remark"`
	Items        []purchaseItemReq `json:"items"`
}

func (h *Handler) ListPurchaseOrders(c *gin.Context) {
	page, size := pageParams(c)
	q := h.applyScope(h.DB.Model(&model.PurchaseOrder{}), middleware.CurrentUser(c))

	if kw := c.Query("keyword"); kw != "" {
		like := "%" + kw + "%"
		q = q.Where("order_no LIKE ? OR title LIKE ? OR vendor LIKE ?", like, like, like)
	}
	if status := c.Query("status"); status != "" {
		q = q.Where("status = ?", status)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		response.Error(c, "查询采购单失败")
		return
	}
	var list []model.PurchaseOrder
	if err := q.Order("id desc").Offset((page - 1) * size).Limit(size).Find(&list).Error; err != nil {
		response.Error(c, "查询采购单失败")
		return
	}
	response.OKPage(c, list, total, page, size)
}

func (h *Handler) GetPurchaseOrder(c *gin.Context) {
	order, ok := h.loadOrderScoped(c)
	if !ok {
		return
	}
	var items []model.PurchaseItem
	h.DB.Where("order_id = ?", order.ID).Order("id asc").Find(&items)
	order.Items = items
	response.OK(c, order)
}

func (h *Handler) CreatePurchaseOrder(c *gin.Context) {
	var req purchaseOrderReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "单号与标题为必填项")
		return
	}
	orderDate, err := parseDate(req.OrderDate)
	if err != nil {
		response.BadRequest(c, "采购日期格式应为 YYYY-MM-DD")
		return
	}
	expected, err := parseDate(req.ExpectedDate)
	if err != nil {
		response.BadRequest(c, "预计到货格式应为 YYYY-MM-DD")
		return
	}

	order := model.PurchaseOrder{
		OrderNo: req.OrderNo, Title: req.Title, Vendor: req.Vendor,
		Applicant: req.Applicant, Status: normalizeOrderStatus(req.Status),
		DeptID: req.DeptID, OrderDate: orderDate, ExpectedDate: expected,
		Remark: req.Remark, CreatedBy: middleware.CurrentUser(c).ID,
	}
	if err := h.DB.Create(&order).Error; err != nil {
		response.BadRequest(c, "创建失败，单号可能已存在")
		return
	}

	if err := h.replaceOrderItems(order.ID, req.Items); err != nil {
		response.Error(c, err.Error())
		return
	}
	h.refreshOrderAmount(order.ID)

	var created model.PurchaseOrder
	h.DB.Preload("Items").First(&created, order.ID)
	response.OK(c, created)
}

func (h *Handler) UpdatePurchaseOrder(c *gin.Context) {
	orderPtr, ok := h.loadOrderScoped(c)
	if !ok {
		return
	}
	order := *orderPtr
	if order.Status == "received" {
		response.BadRequest(c, "已收货的采购单不允许修改")
		return
	}

	var req purchaseOrderReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "参数校验失败")
		return
	}
	orderDate, err := parseDate(req.OrderDate)
	if err != nil {
		response.BadRequest(c, "采购日期格式应为 YYYY-MM-DD")
		return
	}
	expected, err := parseDate(req.ExpectedDate)
	if err != nil {
		response.BadRequest(c, "预计到货格式应为 YYYY-MM-DD")
		return
	}

	order.Title, order.Vendor, order.Applicant = req.Title, req.Vendor, req.Applicant
	order.Status = normalizeOrderStatus(req.Status)
	order.DeptID, order.Remark = req.DeptID, req.Remark
	order.OrderDate, order.ExpectedDate = orderDate, expected
	if err := h.DB.Save(&order).Error; err != nil {
		response.Error(c, "更新失败")
		return
	}

	if req.Items != nil {
		if err := h.replaceOrderItems(order.ID, req.Items); err != nil {
			response.Error(c, err.Error())
			return
		}
	}
	h.refreshOrderAmount(order.ID)

	var updated model.PurchaseOrder
	h.DB.Preload("Items").First(&updated, order.ID)
	response.OK(c, updated)
}

func (h *Handler) DeletePurchaseOrder(c *gin.Context) {
	order, ok := h.loadOrderScoped(c)
	if !ok {
		return
	}
	if order.AssetCreated {
		response.BadRequest(c, "该采购单已生成固定资产，不允许删除")
		return
	}
	if err := h.DB.Delete(&model.PurchaseOrder{}, order.ID).Error; err != nil {
		response.Error(c, "删除失败")
		return
	}
	h.DB.Where("order_id = ?", order.ID).Delete(&model.PurchaseItem{})
	response.OK(c, nil)
}

type receiveReq struct {
	ReceivedDate string `json:"receivedDate"`
	CreateAssets bool   `json:"createAssets"`
	Location     string `json:"location"`
}

// ReceivePurchaseOrder 收货入库。可选按明细生成固定资产：
// 数量 N 的明细生成 N 条资产，名称追加序号，采购价取单价，保修到期留空由后续补录。
func (h *Handler) ReceivePurchaseOrder(c *gin.Context) {
	orderPtr, ok := h.loadOrderScoped(c)
	if !ok {
		return
	}
	order := *orderPtr
	if order.Status == "received" {
		response.BadRequest(c, "该采购单已收货")
		return
	}
	if order.Status == "cancelled" {
		response.BadRequest(c, "已取消的采购单不能收货")
		return
	}

	var req receiveReq
	_ = c.ShouldBindJSON(&req)
	received, err := parseDate(req.ReceivedDate)
	if err != nil {
		response.BadRequest(c, "收货日期格式应为 YYYY-MM-DD")
		return
	}
	if received == nil {
		now := time.Now()
		received = &now
	}

	createdAssets := 0
	if req.CreateAssets {
		var items []model.PurchaseItem
		h.DB.Where("order_id = ?", order.ID).Find(&items)

		assets := make([]model.FixedAsset, 0)
		user := middleware.CurrentUser(c)
		for _, item := range items {
			quantity := item.Quantity
			if quantity <= 0 {
				quantity = 1
			}
			for i := 1; i <= quantity; i++ {
				name := item.Name
				if quantity > 1 {
					name = fmt.Sprintf("%s #%d", item.Name, i)
				}
				assets = append(assets, model.FixedAsset{
					Name: name, Category: normalizeAssetCategory(item.Category),
					Model: item.Model, Vendor: orDefault(item.Vendor, order.Vendor),
					Location: req.Location, DeptID: order.DeptID, Status: "in_use",
					PurchaseDate: received, PurchasePrice: item.UnitPrice,
					Remark:    fmt.Sprintf("采购单 %s 入库", order.OrderNo),
					CreatedBy: user.ID,
				})
			}
		}
		if len(assets) > 0 {
			if err := h.DB.Create(&assets).Error; err != nil {
				response.Error(c, "生成固定资产失败")
				return
			}
			createdAssets = len(assets)
		}
	}

	updates := map[string]any{"status": "received", "received_date": received}
	if createdAssets > 0 {
		updates["asset_created"] = true
	}
	if err := h.DB.Model(&order).Updates(updates).Error; err != nil {
		response.Error(c, "收货登记失败")
		return
	}

	response.OK(c, gin.H{"received": true, "createdAssets": createdAssets})
}

func (h *Handler) loadOrderScoped(c *gin.Context) (*model.PurchaseOrder, bool) {
	var order model.PurchaseOrder
	if err := h.DB.First(&order, idParam(c)).Error; err != nil {
		response.NotFound(c, "采购单不存在")
		return nil, false
	}
	if !h.resourceVisible(middleware.CurrentUser(c), order.DeptID, order.CreatedBy) {
		response.NotFound(c, "采购单不存在")
		return nil, false
	}
	return &order, true
}

// replaceOrderItems 整体替换明细，比逐条 diff 简单且不会留下孤儿行
func (h *Handler) replaceOrderItems(orderID uint, reqItems []purchaseItemReq) error {
	h.DB.Where("order_id = ?", orderID).Delete(&model.PurchaseItem{})
	if len(reqItems) == 0 {
		return nil
	}

	items := make([]model.PurchaseItem, 0, len(reqItems))
	for _, item := range reqItems {
		quantity := item.Quantity
		if quantity <= 0 {
			quantity = 1
		}
		items = append(items, model.PurchaseItem{
			OrderID: orderID, Name: item.Name,
			Category: normalizeAssetCategory(item.Category), Model: item.Model,
			Vendor: item.Vendor, Quantity: quantity, UnitPrice: item.UnitPrice,
			Remark: item.Remark,
		})
	}
	if err := h.DB.Create(&items).Error; err != nil {
		return fmt.Errorf("采购明细保存失败")
	}
	return nil
}

// refreshOrderAmount 按明细汇总金额，避免前后端各算一遍对不上
func (h *Handler) refreshOrderAmount(orderID uint) {
	var amount float64
	h.DB.Model(&model.PurchaseItem{}).
		Where("order_id = ?", orderID).
		Select("COALESCE(SUM(quantity * unit_price), 0)").Scan(&amount)
	h.DB.Model(&model.PurchaseOrder{}).Where("id = ?", orderID).Update("amount", amount)
}

func normalizeOrderStatus(v string) string {
	switch v {
	case "ordered", "received", "cancelled":
		return v
	default:
		return "draft"
	}
}
