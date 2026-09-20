package handler

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"ops-platform/server/internal/middleware"
	"ops-platform/server/internal/model"
	"ops-platform/server/internal/response"
)

// 节点可绑定的资源类型
const (
	nodeKindHost     = "host"
	nodeKindDatabase = "database"
	nodeKindProbe    = "probe"
	nodeKindCert     = "certificate"
	nodeKindExternal = "external"
)

// 节点健康度取值。拓扑自己不采集任何数据，这几个状态全部换算自绑定资源的当前状态。
const (
	healthNormal  = "normal"
	healthWarning = "warning"
	healthError   = "error"
	healthUnknown = "unknown"
)

func validNodeKind(kind string) bool {
	switch kind {
	case nodeKindHost, nodeKindDatabase, nodeKindProbe, nodeKindCert, nodeKindExternal:
		return true
	}
	return false
}

// nodeView 节点 + 实时健康度
type nodeView struct {
	model.TopologyNode
	Health  string `json:"health"`
	Detail  string `json:"detail"`  // 判定理由，照实说明
	RefName string `json:"refName"` // 绑定资源的当前名称，资源改名后这里跟着变
}

// resolveNodeHealth 把绑定资源的当前状态换算成节点健康度。
//
// 资源被删掉时算 error 而不是静默跳过：拓扑指着一个不存在的东西，本身就是要修的问题。
func (h *Handler) resolveNodeHealth(kind string, refID uint) (health, detail, refName string) {
	switch kind {
	case nodeKindHost:
		var host model.Host
		if h.DB.First(&host, refID).Error != nil {
			return healthError, "关联的主机已被删除", ""
		}
		switch host.Status {
		case "online":
			return healthNormal, "主机在线", host.Name
		case "offline":
			return healthError, "主机离线", host.Name
		default:
			return healthUnknown, "主机从未探测过，状态未知", host.Name
		}

	case nodeKindDatabase:
		var instance model.DBInstance
		if h.DB.First(&instance, refID).Error != nil {
			return healthError, "关联的数据库实例已被删除", ""
		}
		switch instance.Status {
		case "online":
			return healthNormal, "端口可达", instance.Name
		case "offline":
			return healthError, "端口不可达", instance.Name
		default:
			return healthUnknown, "实例从未探测过，状态未知", instance.Name
		}

	case nodeKindProbe:
		var probe model.Probe
		if h.DB.First(&probe, refID).Error != nil {
			return healthError, "关联的拨测已被删除", ""
		}
		switch probe.LastStatus {
		case "up":
			return healthNormal, "最近一次拨测通过", probe.Name
		case "down":
			// 还没到告警阈值的失败算「需关注」，和已经告警的区分开
			if probe.FailStreak < probe.ConsecutiveFails {
				return healthWarning, fmt.Sprintf("拨测失败 %d 次，未达告警阈值 %d 次", probe.FailStreak, probe.ConsecutiveFails), probe.Name
			}
			return healthError, fmt.Sprintf("拨测连续失败 %d 次", probe.FailStreak), probe.Name
		default:
			return healthUnknown, "拨测还没执行过", probe.Name
		}

	case nodeKindCert:
		var cert model.Certificate
		if h.DB.First(&cert, refID).Error != nil {
			return healthError, "关联的证书已被删除", ""
		}
		switch cert.Status {
		case "valid":
			if !cert.Trusted {
				return healthWarning, fmt.Sprintf("证书有效（剩 %d 天）但链校验未通过", cert.DaysLeft), cert.Name
			}
			return healthNormal, fmt.Sprintf("证书有效，剩 %d 天", cert.DaysLeft), cert.Name
		case "expiring":
			return healthWarning, fmt.Sprintf("证书即将到期，剩 %d 天", cert.DaysLeft), cert.Name
		case "expired":
			return healthError, "证书已过期", cert.Name
		case "error":
			return healthError, "证书巡检失败", cert.Name
		default:
			return healthUnknown, "证书还没巡检过", cert.Name
		}
	}
	return healthUnknown, "外部依赖，平台没有可观测数据", ""
}

// worseHealth 返回两个健康度里更差的那个，用于算整体状态
func worseHealth(a, b string) string {
	rank := map[string]int{healthNormal: 0, healthUnknown: 1, healthWarning: 2, healthError: 3}
	if rank[b] > rank[a] {
		return b
	}
	return a
}

// findCycle 在依赖连线里找一条环路，返回节点名构成的路径（含闭合节点）。
//
// 依赖成环通常是建模画错了，但现实中也确实存在互相依赖，所以只提示不拦。
func findCycle(nodes []model.TopologyNode, edges []model.TopologyEdge) []string {
	nameByID := map[uint]string{}
	for _, node := range nodes {
		nameByID[node.ID] = node.Name
	}
	next := map[uint][]uint{}
	for _, edge := range edges {
		next[edge.FromNodeID] = append(next[edge.FromNodeID], edge.ToNodeID)
	}

	const (
		white = 0 // 没访问过
		gray  = 1 // 在当前递归栈上
		black = 2 // 已经确认无环
	)
	color := map[uint]int{}
	stack := make([]uint, 0, len(nodes))
	var found []string

	var walk func(id uint) bool
	walk = func(id uint) bool {
		color[id] = gray
		stack = append(stack, id)
		for _, to := range next[id] {
			if color[to] == gray {
				// 从栈里截出环的部分
				start := 0
				for i, item := range stack {
					if item == to {
						start = i
						break
					}
				}
				for _, item := range stack[start:] {
					found = append(found, nameByID[item])
				}
				found = append(found, nameByID[to])
				return true
			}
			if color[to] == white && walk(to) {
				return true
			}
		}
		stack = stack[:len(stack)-1]
		color[id] = black
		return false
	}

	for _, node := range nodes {
		if color[node.ID] == white && walk(node.ID) {
			return found
		}
	}
	return nil
}

// ---------- 拓扑 ----------

func (h *Handler) ListTopologies(c *gin.Context) {
	var list []model.Topology
	if err := h.DB.Order("id desc").Find(&list).Error; err != nil {
		response.Error(c, "查询拓扑失败")
		return
	}

	type summary struct {
		model.Topology
		NodeCount int    `json:"nodeCount"`
		EdgeCount int    `json:"edgeCount"`
		Health    string `json:"health"`
		Problems  int    `json:"problems"` // 非正常节点数
	}
	views := make([]summary, 0, len(list))
	for _, topo := range list {
		var nodes []model.TopologyNode
		h.DB.Where("topology_id = ?", topo.ID).Find(&nodes)
		var edgeCount int64
		h.DB.Model(&model.TopologyEdge{}).Where("topology_id = ?", topo.ID).Count(&edgeCount)

		health, problems := healthNormal, 0
		for _, node := range nodes {
			status, _, _ := h.resolveNodeHealth(node.Kind, node.RefID)
			health = worseHealth(health, status)
			if status != healthNormal {
				problems++
			}
		}
		if len(nodes) == 0 {
			health = healthUnknown
		}
		views = append(views, summary{
			Topology: topo, NodeCount: len(nodes), EdgeCount: int(edgeCount),
			Health: health, Problems: problems,
		})
	}
	response.OK(c, views)
}

// GetTopology 返回拓扑全貌：节点（带实时健康度）、连线、健康分布、环路提示
func (h *Handler) GetTopology(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var topo model.Topology
	if h.DB.First(&topo, id).Error != nil {
		response.NotFound(c, "拓扑不存在")
		return
	}

	var nodes []model.TopologyNode
	h.DB.Where("topology_id = ?", topo.ID).Order("id asc").Find(&nodes)
	var edges []model.TopologyEdge
	h.DB.Where("topology_id = ?", topo.ID).Order("id asc").Find(&edges)

	views := make([]nodeView, 0, len(nodes))
	counts := map[string]int{healthNormal: 0, healthWarning: 0, healthError: 0, healthUnknown: 0}
	overall := healthNormal
	for _, node := range nodes {
		status, detail, refName := h.resolveNodeHealth(node.Kind, node.RefID)
		counts[status]++
		overall = worseHealth(overall, status)
		views = append(views, nodeView{TopologyNode: node, Health: status, Detail: detail, RefName: refName})
	}
	if len(nodes) == 0 {
		overall = healthUnknown
	}

	response.OK(c, gin.H{
		"topology": topo,
		"nodes":    views,
		"edges":    edges,
		"counts":   counts,
		"health":   overall,
		"cycle":    findCycle(nodes, edges),
	})
}

func (h *Handler) CreateTopology(c *gin.Context) {
	var req struct {
		Name   string `json:"name"`
		Remark string `json:"remark"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "参数错误")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		response.BadRequest(c, "拓扑名称不能为空")
		return
	}

	user := middleware.CurrentUser(c)
	topo := model.Topology{Name: req.Name, Remark: req.Remark}
	if user != nil {
		topo.CreatedBy, topo.CreatorName = user.ID, user.Username
	}
	if err := h.DB.Create(&topo).Error; err != nil {
		response.Error(c, "创建失败")
		return
	}
	response.OK(c, topo)
}

func (h *Handler) UpdateTopology(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var topo model.Topology
	if h.DB.First(&topo, id).Error != nil {
		response.NotFound(c, "拓扑不存在")
		return
	}
	var req struct {
		Name   string `json:"name"`
		Remark string `json:"remark"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "参数错误")
		return
	}
	if name := strings.TrimSpace(req.Name); name != "" {
		topo.Name = name
	}
	topo.Remark = req.Remark
	if err := h.DB.Save(&topo).Error; err != nil {
		response.Error(c, "保存失败")
		return
	}
	response.OK(c, topo)
}

// DeleteTopology 连带删除节点与连线：留着孤儿数据没有任何意义
func (h *Handler) DeleteTopology(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var topo model.Topology
	if h.DB.First(&topo, id).Error != nil {
		response.NotFound(c, "拓扑不存在")
		return
	}
	h.DB.Where("topology_id = ?", topo.ID).Delete(&model.TopologyEdge{})
	h.DB.Where("topology_id = ?", topo.ID).Delete(&model.TopologyNode{})
	if err := h.DB.Delete(&topo).Error; err != nil {
		response.Error(c, "删除失败")
		return
	}
	response.OK(c, gin.H{"detail": "拓扑及其节点、连线已删除"})
}

// ---------- 节点 ----------

// bindableResource 可绑定资源的极简视图，只够在下拉里认出是哪一个
type bindableResource struct {
	ID     uint   `json:"id"`
	Name   string `json:"name"`
	Detail string `json:"detail"`
}

// TopologyResources 列出可绑定的平台资源，给节点表单的下拉用
func (h *Handler) TopologyResources(c *gin.Context) {
	var hosts []model.Host
	h.DB.Order("id asc").Find(&hosts)
	hostViews := make([]bindableResource, 0, len(hosts))
	for _, item := range hosts {
		hostViews = append(hostViews, bindableResource{
			ID: item.ID, Name: item.Name,
			Detail: fmt.Sprintf("%s:%d · %s", item.Address, item.Port, item.Env),
		})
	}

	var instances []model.DBInstance
	h.DB.Order("id asc").Find(&instances)
	dbViews := make([]bindableResource, 0, len(instances))
	for _, item := range instances {
		dbViews = append(dbViews, bindableResource{
			ID: item.ID, Name: item.Name,
			Detail: fmt.Sprintf("%s · %s:%d", item.Type, item.Address, item.Port),
		})
	}

	var probes []model.Probe
	h.DB.Order("id asc").Find(&probes)
	probeViews := make([]bindableResource, 0, len(probes))
	for _, item := range probes {
		probeViews = append(probeViews, bindableResource{
			ID: item.ID, Name: item.Name,
			Detail: fmt.Sprintf("%s · %s", item.Type, item.Target),
		})
	}

	var certs []model.Certificate
	h.DB.Order("id asc").Find(&certs)
	certViews := make([]bindableResource, 0, len(certs))
	for _, item := range certs {
		certViews = append(certViews, bindableResource{
			ID: item.ID, Name: item.Name,
			Detail: fmt.Sprintf("%s:%d", item.Domain, item.Port),
		})
	}

	response.OK(c, gin.H{
		"host": hostViews, "database": dbViews,
		"probe": probeViews, "certificate": certViews,
	})
}

// resolveNodeBinding 校验 kind + refId 的组合，并在名称留空时用资源名兜底
func (h *Handler) resolveNodeBinding(kind string, refID uint, name string) (string, uint, string, error) {
	if !validNodeKind(kind) {
		return "", 0, "", fmt.Errorf("节点类型只能是 host / database / probe / certificate / external")
	}
	if kind == nodeKindExternal {
		if name == "" {
			return "", 0, "", fmt.Errorf("外部依赖节点必须自己起名字")
		}
		return kind, 0, name, nil
	}
	if refID == 0 {
		return "", 0, "", fmt.Errorf("请选择要绑定的资源")
	}
	_, _, refName := h.resolveNodeHealth(kind, refID)
	if refName == "" {
		return "", 0, "", fmt.Errorf("要绑定的资源不存在")
	}
	if name == "" {
		name = refName
	}
	return kind, refID, name, nil
}

func (h *Handler) CreateTopologyNode(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var topo model.Topology
	if h.DB.First(&topo, id).Error != nil {
		response.NotFound(c, "拓扑不存在")
		return
	}
	var req struct {
		Name   string `json:"name"`
		Kind   string `json:"kind"`
		RefID  uint   `json:"refId"`
		X      int    `json:"x"`
		Y      int    `json:"y"`
		Remark string `json:"remark"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "参数错误")
		return
	}

	kind, refID, name, err := h.resolveNodeBinding(req.Kind, req.RefID, strings.TrimSpace(req.Name))
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	// 同一个资源在一张拓扑里重复出现，看图的人会以为是两个东西
	if kind != nodeKindExternal {
		var dup int64
		h.DB.Model(&model.TopologyNode{}).
			Where("topology_id = ? AND kind = ? AND ref_id = ?", topo.ID, kind, refID).Count(&dup)
		if dup > 0 {
			response.BadRequest(c, "这个资源已经在本拓扑里了")
			return
		}
	}

	node := model.TopologyNode{
		TopologyID: topo.ID, Name: name, Kind: kind, RefID: refID,
		X: req.X, Y: req.Y, Remark: req.Remark,
	}
	if err := h.DB.Create(&node).Error; err != nil {
		response.Error(c, "创建节点失败")
		return
	}
	response.OK(c, node)
}

func (h *Handler) UpdateTopologyNode(c *gin.Context) {
	nodeID, _ := strconv.Atoi(c.Param("nodeId"))
	var node model.TopologyNode
	if h.DB.First(&node, nodeID).Error != nil {
		response.NotFound(c, "节点不存在")
		return
	}
	var req struct {
		Name   string `json:"name"`
		Remark string `json:"remark"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "参数错误")
		return
	}
	// 只允许改展示信息：改绑定等于换了个东西，让人删了重建更不容易出错
	if name := strings.TrimSpace(req.Name); name != "" {
		node.Name = name
	}
	node.Remark = req.Remark
	if err := h.DB.Save(&node).Error; err != nil {
		response.Error(c, "保存失败")
		return
	}
	response.OK(c, node)
}

// DeleteTopologyNode 连带删掉挂在该节点上的连线
func (h *Handler) DeleteTopologyNode(c *gin.Context) {
	nodeID, _ := strconv.Atoi(c.Param("nodeId"))
	var node model.TopologyNode
	if h.DB.First(&node, nodeID).Error != nil {
		response.NotFound(c, "节点不存在")
		return
	}
	removed := h.DB.Where("from_node_id = ? OR to_node_id = ?", node.ID, node.ID).
		Delete(&model.TopologyEdge{}).RowsAffected
	if err := h.DB.Delete(&node).Error; err != nil {
		response.Error(c, "删除失败")
		return
	}
	response.OK(c, gin.H{"detail": fmt.Sprintf("节点已删除，连带清理 %d 条连线", removed)})
}

// SaveTopologyLayout 批量写回拖动后的坐标。
// 拖一下存一次会打出一堆审计流水，所以前端攒完一起提交。
func (h *Handler) SaveTopologyLayout(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var topo model.Topology
	if h.DB.First(&topo, id).Error != nil {
		response.NotFound(c, "拓扑不存在")
		return
	}
	var req struct {
		Nodes []struct {
			ID uint `json:"id"`
			X  int  `json:"x"`
			Y  int  `json:"y"`
		} `json:"nodes"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "参数错误")
		return
	}
	saved := 0
	for _, item := range req.Nodes {
		result := h.DB.Model(&model.TopologyNode{}).
			Where("id = ? AND topology_id = ?", item.ID, topo.ID).
			Updates(map[string]any{"x": item.X, "y": item.Y})
		saved += int(result.RowsAffected)
	}
	response.OK(c, gin.H{"saved": saved, "detail": fmt.Sprintf("已保存 %d 个节点的位置", saved)})
}

// ---------- 连线 ----------

func (h *Handler) CreateTopologyEdge(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var topo model.Topology
	if h.DB.First(&topo, id).Error != nil {
		response.NotFound(c, "拓扑不存在")
		return
	}
	var req struct {
		FromNodeID uint   `json:"fromNodeId"`
		ToNodeID   uint   `json:"toNodeId"`
		Label      string `json:"label"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "参数错误")
		return
	}
	if req.FromNodeID == 0 || req.ToNodeID == 0 {
		response.BadRequest(c, "请选择连线的起点与终点")
		return
	}
	if req.FromNodeID == req.ToNodeID {
		response.BadRequest(c, "节点不能连到自己")
		return
	}
	// 两端都必须是本拓扑的节点，否则会画出跨拓扑的幽灵连线
	var endpoints int64
	h.DB.Model(&model.TopologyNode{}).
		Where("topology_id = ? AND id IN ?", topo.ID, []uint{req.FromNodeID, req.ToNodeID}).
		Count(&endpoints)
	if endpoints != 2 {
		response.BadRequest(c, "连线两端必须都是本拓扑的节点")
		return
	}
	var dup int64
	h.DB.Model(&model.TopologyEdge{}).
		Where("topology_id = ? AND from_node_id = ? AND to_node_id = ?", topo.ID, req.FromNodeID, req.ToNodeID).
		Count(&dup)
	if dup > 0 {
		response.BadRequest(c, "这条连线已经存在")
		return
	}

	edge := model.TopologyEdge{
		TopologyID: topo.ID, FromNodeID: req.FromNodeID,
		ToNodeID: req.ToNodeID, Label: strings.TrimSpace(req.Label),
	}
	if err := h.DB.Create(&edge).Error; err != nil {
		response.Error(c, "创建连线失败")
		return
	}

	// 成环不拦，但要在返回里说清楚
	var nodes []model.TopologyNode
	h.DB.Where("topology_id = ?", topo.ID).Find(&nodes)
	var edges []model.TopologyEdge
	h.DB.Where("topology_id = ?", topo.ID).Find(&edges)
	response.OK(c, gin.H{"edge": edge, "cycle": findCycle(nodes, edges)})
}

func (h *Handler) DeleteTopologyEdge(c *gin.Context) {
	edgeID, _ := strconv.Atoi(c.Param("edgeId"))
	var edge model.TopologyEdge
	if h.DB.First(&edge, edgeID).Error != nil {
		response.NotFound(c, "连线不存在")
		return
	}
	if err := h.DB.Delete(&edge).Error; err != nil {
		response.Error(c, "删除失败")
		return
	}
	response.OK(c, gin.H{"detail": "连线已删除"})
}
