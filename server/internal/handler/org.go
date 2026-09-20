package handler

import (
	"fmt"

	"github.com/gin-gonic/gin"

	"ops-platform/server/internal/middleware"
	"ops-platform/server/internal/model"
	"ops-platform/server/internal/response"
)

// ---------- 公司 ----------

type companyReq struct {
	Name   string `json:"name" binding:"required"`
	Code   string `json:"code" binding:"required"`
	Remark string `json:"remark"`
}

func (h *Handler) ListCompanies(c *gin.Context) {
	var list []model.Company
	if err := h.DB.Order("id asc").Find(&list).Error; err != nil {
		response.Error(c, "查询公司失败")
		return
	}
	response.OK(c, list)
}

func (h *Handler) CreateCompany(c *gin.Context) {
	var req companyReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "公司名称与编码为必填项")
		return
	}
	item := model.Company{Name: req.Name, Code: req.Code, Remark: req.Remark}
	if err := h.DB.Create(&item).Error; err != nil {
		response.BadRequest(c, "创建失败，公司编码可能已存在")
		return
	}
	response.OK(c, item)
}

func (h *Handler) UpdateCompany(c *gin.Context) {
	var item model.Company
	if err := h.DB.First(&item, idParam(c)).Error; err != nil {
		response.NotFound(c, "公司不存在")
		return
	}
	var req companyReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "参数校验失败")
		return
	}
	item.Name, item.Remark = req.Name, req.Remark
	if err := h.DB.Save(&item).Error; err != nil {
		response.Error(c, "公司更新失败")
		return
	}
	response.OK(c, item)
}

func (h *Handler) DeleteCompany(c *gin.Context) {
	id := idParam(c)
	var deptCount int64
	h.DB.Model(&model.Department{}).Where("company_id = ?", id).Count(&deptCount)
	if deptCount > 0 {
		response.BadRequest(c, fmt.Sprintf("该公司下还有 %d 个部门，请先处理部门", deptCount))
		return
	}
	if err := h.DB.Delete(&model.Company{}, id).Error; err != nil {
		response.Error(c, "公司删除失败")
		return
	}
	response.OK(c, nil)
}

// ---------- 部门 ----------

type departmentReq struct {
	CompanyID uint   `json:"companyId" binding:"required"`
	ParentID  uint   `json:"parentId"`
	Name      string `json:"name" binding:"required"`
	Code      string `json:"code"`
	Leader    string `json:"leader"`
	Sort      int    `json:"sort"`
}

// DeptNode 部门树节点，附带人员与主机数量便于判断影响面
type DeptNode struct {
	ID        uint       `json:"id"`
	CompanyID uint       `json:"companyId"`
	ParentID  uint       `json:"parentId"`
	Name      string     `json:"name"`
	Code      string     `json:"code"`
	Leader    string     `json:"leader"`
	Sort      int        `json:"sort"`
	UserCount int64      `json:"userCount"`
	HostCount int64      `json:"hostCount"`
	Children  []DeptNode `json:"children,omitempty"`
}

// DepartmentTree 部门树，可按公司过滤
func (h *Handler) DepartmentTree(c *gin.Context) {
	q := h.DB.Model(&model.Department{})
	if companyID := c.Query("companyId"); companyID != "" {
		q = q.Where("company_id = ?", companyID)
	}

	var depts []model.Department
	if err := q.Order("sort asc, id asc").Find(&depts).Error; err != nil {
		response.Error(c, "查询部门失败")
		return
	}

	// 一次性统计人员与主机数，避免逐节点查库
	userCount := map[uint]int64{}
	hostCount := map[uint]int64{}
	type countRow struct {
		DeptID uint
		Num    int64
	}
	var rows []countRow
	h.DB.Model(&model.User{}).Select("dept_id, count(*) as num").Group("dept_id").Scan(&rows)
	for _, row := range rows {
		userCount[row.DeptID] = row.Num
	}
	rows = nil
	h.DB.Model(&model.Host{}).Select("dept_id, count(*) as num").Group("dept_id").Scan(&rows)
	for _, row := range rows {
		hostCount[row.DeptID] = row.Num
	}

	response.OK(c, buildDeptTree(depts, 0, userCount, hostCount))
}

func buildDeptTree(depts []model.Department, parent uint, userCount, hostCount map[uint]int64) []DeptNode {
	nodes := make([]DeptNode, 0)
	for _, dept := range depts {
		if dept.ParentID != parent {
			continue
		}
		node := DeptNode{
			ID: dept.ID, CompanyID: dept.CompanyID, ParentID: dept.ParentID,
			Name: dept.Name, Code: dept.Code, Leader: dept.Leader, Sort: dept.Sort,
			UserCount: userCount[dept.ID], HostCount: hostCount[dept.ID],
		}
		node.Children = buildDeptTree(depts, dept.ID, userCount, hostCount)
		nodes = append(nodes, node)
	}
	return nodes
}

func (h *Handler) CreateDepartment(c *gin.Context) {
	var req departmentReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "所属公司与部门名称为必填项")
		return
	}
	if err := h.DB.First(&model.Company{}, req.CompanyID).Error; err != nil {
		response.BadRequest(c, "所属公司不存在")
		return
	}
	if req.ParentID != 0 {
		if err := h.DB.First(&model.Department{}, req.ParentID).Error; err != nil {
			response.BadRequest(c, "上级部门不存在")
			return
		}
	}

	item := model.Department{
		CompanyID: req.CompanyID, ParentID: req.ParentID, Name: req.Name,
		Code: req.Code, Leader: req.Leader, Sort: req.Sort,
	}
	if err := h.DB.Create(&item).Error; err != nil {
		response.Error(c, "部门创建失败")
		return
	}
	response.OK(c, item)
}

func (h *Handler) UpdateDepartment(c *gin.Context) {
	id := idParam(c)
	var item model.Department
	if err := h.DB.First(&item, id).Error; err != nil {
		response.NotFound(c, "部门不存在")
		return
	}
	var req departmentReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "参数校验失败")
		return
	}
	if req.ParentID == id {
		response.BadRequest(c, "上级部门不能是自己")
		return
	}
	// 不允许把部门挂到自己的下级，否则部门树会成环
	if req.ParentID != 0 {
		for _, child := range collectDescendants(h.deptChildren(), id) {
			if child == req.ParentID {
				response.BadRequest(c, "上级部门不能是自己的下级部门")
				return
			}
		}
	}

	item.CompanyID, item.ParentID = req.CompanyID, req.ParentID
	item.Name, item.Code, item.Leader, item.Sort = req.Name, req.Code, req.Leader, req.Sort
	if err := h.DB.Save(&item).Error; err != nil {
		response.Error(c, "部门更新失败")
		return
	}
	response.OK(c, item)
}

func (h *Handler) DeleteDepartment(c *gin.Context) {
	id := idParam(c)

	var childCount, userCount, hostCount int64
	h.DB.Model(&model.Department{}).Where("parent_id = ?", id).Count(&childCount)
	h.DB.Model(&model.User{}).Where("dept_id = ?", id).Count(&userCount)
	h.DB.Model(&model.Host{}).Where("dept_id = ?", id).Count(&hostCount)

	if childCount > 0 || userCount > 0 || hostCount > 0 {
		response.BadRequest(c, fmt.Sprintf(
			"该部门下还有 %d 个子部门、%d 个用户、%d 台主机，请先迁移", childCount, userCount, hostCount))
		return
	}
	if err := h.DB.Delete(&model.Department{}, id).Error; err != nil {
		response.Error(c, "部门删除失败")
		return
	}
	response.OK(c, nil)
}

// ---------- 数据权限诊断 ----------

// DiagnoseDataScope 给出某个用户当前生效的数据范围与可见主机数，
// 用来回答「为什么他看不到这台机器」这类问题。
func (h *Handler) DiagnoseDataScope(c *gin.Context) {
	userID := idParam(c)
	if userID == 0 {
		userID = middleware.CurrentUser(c).ID
	}

	var user model.User
	if err := h.DB.Preload("Roles").First(&user, userID).Error; err != nil {
		response.NotFound(c, "用户不存在")
		return
	}

	scope := h.resolveScope(&user)

	var visibleHosts, totalHosts int64
	h.DB.Model(&model.Host{}).Count(&totalHosts)
	h.applyHostScope(h.DB.Model(&model.Host{}), &user).Count(&visibleHosts)

	roles := make([]gin.H, 0, len(user.Roles))
	for _, role := range user.Roles {
		roles = append(roles, gin.H{
			"id": role.ID, "name": role.Name, "code": role.Code,
			"dataScope": role.DataScope, "dataDeptIds": parseIDList(role.DataDeptIDs),
		})
	}

	var deptNames []string
	if len(scope.DeptIDs) > 0 {
		h.DB.Model(&model.Department{}).Where("id IN ?", scope.DeptIDs).Pluck("name", &deptNames)
	}

	response.OK(c, gin.H{
		"user": gin.H{
			"id": user.ID, "username": user.Username, "deptId": user.DeptID,
		},
		"roles":        roles,
		"scope":        scope,
		"deptNames":    deptNames,
		"visibleHosts": visibleHosts,
		"totalHosts":   totalHosts,
	})
}
