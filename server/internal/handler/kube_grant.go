package handler

// 容器平台授权。
//
// 这一页之前的现状很值得先写下来：**所有 K8s 只读接口对任意登录用户开放** ——
// 节点、命名空间、工作负载、Pod、**Pod 日志**、RBAC 账户（含绑定关系）、
// Secret 的键名，全都只要登录就能看，路由上一个 RequirePerm 都没有。
// 而主机与数据库早就有「数据范围 + 资源授权」两层机制（grant_scope.go），
// K8s 只是一直没接进去。
//
// 闸门只放在一个地方
//
// 授权校验写在 requireKubeCluster 里，而不是散到 25 个 handler 上。
// 理由和 secret_audit.go 里选择「显式调用 + 体检兜底」正好相反：那里是写路径、
// 每处语义不同；这里是读路径、语义完全一致，而且 gin.Context 里就有判断所需的
// 全部信息（集群 ID 在 :id、命名空间在 ?namespace、资源类型在 ?kind、
// 是不是日志看路径）。放一处的代价是要靠路径做判断，收益是**漏不掉**：
// 以后新增只读接口只要走 requireKubeCluster 就自动被卡住。
//
// 默认不生效
//
// 配置项 kube.grant_enforce 默认 false：不开时行为与从前完全一致。
// 默认打开会让升级瞬间把所有非管理员锁在外面 —— 那种"安全"是靠制造事故换来的。
// 页面顶部照实显示当前是哪种状态，以及开启后谁会看不到什么。

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"ops-platform/server/internal/middleware"
	"ops-platform/server/internal/model"
	"ops-platform/server/internal/response"
)

const (
	// CfgKubeGrantEnforce 是否按授权卡 K8s 读取。默认 false
	CfgKubeGrantEnforce = "kube.grant_enforce"
	// kubeAdminPerm 拥有它的人不受授权限制 —— 否则配错一条授权就能把管理员自己锁死，
	// 而 K8s 授权页本身也在平台里，锁死之后没有后门可走
	kubeAdminPerm = "kube:manage"
)

// kubeGrantEnforced 授权是否生效
func (h *Handler) kubeGrantEnforced() bool {
	return h.configString(CfgKubeGrantEnforce, "false") == "true"
}

// kubeGrantScope 一个用户在某个集群上的有效授权合并结果。
//
// 同一个人可能同时被用户维度和角色维度授权，多条授权是**并集**：
// 命名空间清单取并集（任一条不限就等于不限），三个开关取或。
type kubeGrantScope struct {
	Found bool
	// AllNamespaces 有一条授权没限定命名空间
	AllNamespaces bool
	Namespaces    map[string]bool
	AllKinds      bool
	Kinds         map[string]bool
	AllowLogs     bool
	AllowWrite    bool
	AllowForward  bool
}

func splitGrantList(raw string) []string {
	items := make([]string, 0)
	for _, item := range strings.Split(raw, ",") {
		if v := strings.TrimSpace(item); v != "" {
			items = append(items, v)
		}
	}
	return items
}

// kubeScopeFor 汇总某人在某集群上的授权。clusterID 为 0 时返回空结果。
func (h *Handler) kubeScopeFor(user *model.User, clusterID uint) kubeGrantScope {
	scope := kubeGrantScope{Namespaces: map[string]bool{}, Kinds: map[string]bool{}}
	if user == nil || clusterID == 0 {
		return scope
	}

	roleIDs := make([]uint, 0, len(user.Roles))
	for _, role := range user.Roles {
		roleIDs = append(roleIDs, role.ID)
	}

	q := h.DB.Model(&model.KubeGrant{}).
		Where("cluster_id = ?", clusterID).
		Where("expires_at IS NULL OR expires_at > ?", time.Now())
	if len(roleIDs) > 0 {
		q = q.Where("(subject_type = ? AND subject_id = ?) OR (subject_type = ? AND subject_id IN ?)",
			"user", user.ID, "role", roleIDs)
	} else {
		q = q.Where("subject_type = ? AND subject_id = ?", "user", user.ID)
	}

	var grants []model.KubeGrant
	if err := q.Find(&grants).Error; err != nil || len(grants) == 0 {
		return scope
	}

	scope.Found = true
	for _, grant := range grants {
		namespaces := splitGrantList(grant.Namespaces)
		if len(namespaces) == 0 {
			scope.AllNamespaces = true
		}
		for _, ns := range namespaces {
			scope.Namespaces[ns] = true
		}
		kinds := splitGrantList(grant.Kinds)
		if len(kinds) == 0 {
			scope.AllKinds = true
		}
		for _, kind := range kinds {
			scope.Kinds[kind] = true
		}
		scope.AllowLogs = scope.AllowLogs || grant.AllowLogs
		scope.AllowWrite = scope.AllowWrite || grant.AllowWrite
		scope.AllowForward = scope.AllowForward || grant.AllowForward
	}
	return scope
}

// kubeGrantBypass 这个人是否不受授权限制
func kubeGrantBypass(c *gin.Context) bool {
	perms := middleware.Perms(c)
	if _, ok := perms[kubeAdminPerm]; ok {
		return true
	}
	if _, ok := perms["*"]; ok {
		return true
	}
	return false
}

// sortedKeys 稳定输出，错误信息里列出可见范围时不要每次顺序都不一样
func sortedKeys(set map[string]bool) []string {
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// kubeGrantCheck 判断这次请求是否被允许。返回 (允许, 拒绝原因)。
//
// 拒绝原因会原样返回给调用方 —— 「没有权限」这四个字帮不了任何人，
// 得说清是集群没授权、还是命名空间不在清单里、还是这条授权没开日志。
func (h *Handler) kubeGrantCheck(c *gin.Context, cluster *model.KubeCluster) (bool, string) {
	if !h.kubeGrantEnforced() || kubeGrantBypass(c) {
		return true, ""
	}
	user := middleware.CurrentUser(c)
	scope := h.kubeScopeFor(user, cluster.ID)
	if !scope.Found {
		return false, fmt.Sprintf("没有集群「%s」的授权。容器平台授权已启用，"+
			"请让管理员在「容器平台 → 授权管理」里为你或你的角色加一条", cluster.Name)
	}

	// 命名空间：限定了清单时，跨命名空间的请求一律拒绝而不是静默过滤
	namespace := strings.TrimSpace(c.Query("namespace"))
	if !scope.AllNamespaces {
		if namespace == "" {
			return false, fmt.Sprintf("这条授权只放开了命名空间 %s，"+
				"不能做跨命名空间的查询 —— 请在页面上先选一个命名空间。"+
				"（平台不静默只返回允许的那部分：那会让你以为集群里只有这些东西）",
				strings.Join(sortedKeys(scope.Namespaces), " / "))
		}
		if !scope.Namespaces[namespace] {
			return false, fmt.Sprintf("命名空间「%s」不在你的授权范围内，可见的是 %s",
				namespace, strings.Join(sortedKeys(scope.Namespaces), " / "))
		}
	}

	// 资源类型
	if kind := strings.TrimSpace(c.Query("kind")); kind != "" && !scope.AllKinds {
		if !scope.Kinds[kind] {
			return false, fmt.Sprintf("资源类型「%s」不在你的授权范围内，可见的是 %s",
				kind, strings.Join(sortedKeys(scope.Kinds), " / "))
		}
	}

	path := c.FullPath()
	if strings.HasSuffix(path, "/pod-logs") && !scope.AllowLogs {
		return false, "这条授权没有开放 Pod 日志。日志里常带业务数据与密钥，" +
			"所以它与「能看到对象」是分开授权的"
	}
	if strings.Contains(path, "/resource/apply") || strings.Contains(path, "/resource/scale") ||
		strings.Contains(path, "/resource/restart") {
		if !scope.AllowWrite {
			return false, "这条授权没有开放写操作（apply / scale / restart）"
		}
	}
	return true, ""
}

// kubeVisibleClusterIDs 这个人能看到哪些集群。第二个返回值为 false 表示不受限。
func (h *Handler) kubeVisibleClusterIDs(c *gin.Context) ([]uint, bool) {
	if !h.kubeGrantEnforced() || kubeGrantBypass(c) {
		return nil, false
	}
	user := middleware.CurrentUser(c)
	if user == nil {
		return []uint{}, true
	}

	roleIDs := make([]uint, 0, len(user.Roles))
	for _, role := range user.Roles {
		roleIDs = append(roleIDs, role.ID)
	}
	q := h.DB.Model(&model.KubeGrant{}).
		Where("expires_at IS NULL OR expires_at > ?", time.Now())
	if len(roleIDs) > 0 {
		q = q.Where("(subject_type = ? AND subject_id = ?) OR (subject_type = ? AND subject_id IN ?)",
			"user", user.ID, "role", roleIDs)
	} else {
		q = q.Where("subject_type = ? AND subject_id = ?", "user", user.ID)
	}

	var grants []model.KubeGrant
	if err := q.Find(&grants).Error; err != nil {
		return []uint{}, true
	}
	seen := map[uint]bool{}
	ids := make([]uint, 0, len(grants))
	for _, grant := range grants {
		if !seen[grant.ClusterID] {
			seen[grant.ClusterID] = true
			ids = append(ids, grant.ClusterID)
		}
	}
	return ids, true
}

// kubeForwardGrantCheck 端口转发的授权校验。
//
// 转发的集群 ID 在请求体里，走不了 requireKubeCluster 那条闸门，所以单独一个函数；
// 命名空间也是请求体里的字段而不是 query 参数。
func (h *Handler) kubeForwardGrantCheck(c *gin.Context, cluster *model.KubeCluster, namespace string) (bool, string) {
	if !h.kubeGrantEnforced() || kubeGrantBypass(c) {
		return true, ""
	}
	scope := h.kubeScopeFor(middleware.CurrentUser(c), cluster.ID)
	if !scope.Found {
		return false, fmt.Sprintf("没有集群「%s」的授权", cluster.Name)
	}
	if !scope.AllowForward {
		return false, "这条授权没有开放端口转发 —— 隧道会把集群内的服务暴露到平台所在的机器上"
	}
	if !scope.AllNamespaces {
		ns := strings.TrimSpace(namespace)
		if ns == "" || !scope.Namespaces[ns] {
			return false, fmt.Sprintf("命名空间「%s」不在你的授权范围内，可见的是 %s",
				ns, strings.Join(sortedKeys(scope.Namespaces), " / "))
		}
	}
	return true, ""
}

// ---------- 授权管理接口 ----------

type kubeGrantReq struct {
	SubjectType  string     `json:"subjectType"`
	SubjectID    uint       `json:"subjectId"`
	ClusterID    uint       `json:"clusterId"`
	Namespaces   string     `json:"namespaces"`
	Kinds        string     `json:"kinds"`
	AllowLogs    bool       `json:"allowLogs"`
	AllowWrite   bool       `json:"allowWrite"`
	AllowForward bool       `json:"allowForward"`
	ExpiresAt    *time.Time `json:"expiresAt"`
	Remark       string     `json:"remark"`
}

func kubeGrantView(item model.KubeGrant) gin.H {
	expired := item.ExpiresAt != nil && item.ExpiresAt.Before(time.Now())
	return gin.H{
		"id": item.ID, "subjectType": item.SubjectType, "subjectId": item.SubjectID,
		"subjectName": item.SubjectName, "clusterId": item.ClusterID,
		"clusterName": item.ClusterName, "namespaces": item.Namespaces, "kinds": item.Kinds,
		"allowLogs": item.AllowLogs, "allowWrite": item.AllowWrite,
		"allowForward": item.AllowForward, "expiresAt": item.ExpiresAt,
		"remark": item.Remark, "operator": item.Operator,
		"createdAt": item.CreatedAt, "updatedAt": item.UpdatedAt,
		"expired": expired,
		// 把"全部"这件事显式表达出来，前端不用自己判断空字符串的含义
		"allNamespaces": strings.TrimSpace(item.Namespaces) == "",
		"allKinds":      strings.TrimSpace(item.Kinds) == "",
	}
}

func (h *Handler) ListKubeGrants(c *gin.Context) {
	q := h.DB.Model(&model.KubeGrant{})
	if raw := strings.TrimSpace(c.Query("clusterId")); raw != "" {
		q = q.Where("cluster_id = ?", raw)
	}
	if st := strings.TrimSpace(c.Query("subjectType")); st == "user" || st == "role" {
		q = q.Where("subject_type = ?", st)
	}
	var list []model.KubeGrant
	if err := q.Order("cluster_id asc, subject_type asc, subject_name asc").
		Find(&list).Error; err != nil {
		response.Error(c, "查询授权失败")
		return
	}
	views := make([]gin.H, 0, len(list))
	for _, item := range list {
		views = append(views, kubeGrantView(item))
	}
	response.OK(c, views)
}

// GetKubeGrantState 这一页的事实条。最要紧的一条是「现在到底卡不卡」。
func (h *Handler) GetKubeGrantState(c *gin.Context) {
	enforced := h.kubeGrantEnforced()

	var grantCount, clusterCount int64
	h.DB.Model(&model.KubeGrant{}).Count(&grantCount)
	h.DB.Model(&model.KubeCluster{}).Count(&clusterCount)

	// 有多少集群一条授权都没有 —— 开启后这些集群对非管理员就是完全不可见的
	var covered []uint
	h.DB.Model(&model.KubeGrant{}).Distinct().Pluck("cluster_id", &covered)
	var expired int64
	h.DB.Model(&model.KubeGrant{}).
		Where("expires_at IS NOT NULL AND expires_at <= ?", time.Now()).Count(&expired)

	state := gin.H{
		"enforced":          enforced,
		"grantCount":        grantCount,
		"clusterCount":      clusterCount,
		"coveredClusters":   len(covered),
		"uncoveredClusters": int(clusterCount) - len(covered),
		"expiredGrants":     expired,
		"configKey":         CfgKubeGrantEnforce,
		"adminPerm":         kubeAdminPerm,
		"notes": []string{
			"未开启时行为与从前完全一致：任何登录用户都能看到所有集群的只读数据（含 Pod 日志、RBAC、Secret 键名）",
			"开启前请先把授权配好：没有任何授权的集群，开启后对非管理员立刻完全不可见",
			"拥有 " + kubeAdminPerm + " 的人不受授权限制 —— 否则配错一条就能把管理员自己锁死，而这一页本身也在平台里",
			"命名空间清单非空时，跨命名空间的查询会被**拒绝**而不是静默过滤：静默过滤会让人以为集群里只有这些东西",
			"Pod 日志单独一个开关：日志里常带业务数据与密钥，与「能看到对象」不是一件事",
			"写操作与端口转发是 AND 关系：既要有 kube:write / kube:forward 功能权限，也要这条授权放开",
			"闸门放在 requireKubeCluster 一处，新增的只读接口只要走它就自动被卡住",
		},
	}
	if enforced {
		state["activeNote"] = "授权已生效：非管理员只能看到被授权的集群与命名空间"
	} else {
		state["activeNote"] = "授权未生效（" + CfgKubeGrantEnforce + " = false）：这里配的内容暂时不影响任何人的可见范围"
	}
	response.OK(c, state)
}

func (h *Handler) CreateKubeGrant(c *gin.Context) {
	var req kubeGrantReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "参数校验失败")
		return
	}
	if req.SubjectType != "user" && req.SubjectType != "role" {
		response.BadRequest(c, "授权对象只能是用户或角色")
		return
	}
	if req.ClusterID == 0 {
		response.BadRequest(c, "必须指定集群：不支持「全部集群」——那等于一张空白支票，新接入的集群会被自动包含进来")
		return
	}

	var cluster model.KubeCluster
	if err := h.DB.First(&cluster, req.ClusterID).Error; err != nil {
		response.BadRequest(c, "集群不存在")
		return
	}

	subjectName := ""
	if req.SubjectType == "user" {
		var user model.User
		if err := h.DB.First(&user, req.SubjectID).Error; err != nil {
			response.BadRequest(c, "用户不存在")
			return
		}
		subjectName = user.Username
	} else {
		var role model.Role
		if err := h.DB.First(&role, req.SubjectID).Error; err != nil {
			response.BadRequest(c, "角色不存在")
			return
		}
		subjectName = role.Name
	}

	item := model.KubeGrant{
		SubjectType: req.SubjectType, SubjectID: req.SubjectID, SubjectName: subjectName,
		ClusterID: cluster.ID, ClusterName: cluster.Name,
		Namespaces: normalizeGrantList(req.Namespaces), Kinds: normalizeGrantList(req.Kinds),
		AllowLogs: req.AllowLogs, AllowWrite: req.AllowWrite, AllowForward: req.AllowForward,
		ExpiresAt: req.ExpiresAt, Remark: req.Remark, Operator: operatorName(c),
	}
	if err := h.DB.Create(&item).Error; err != nil {
		response.Error(c, "创建失败")
		return
	}
	h.recordVersion(ruleTargetKubeGrant, item.ID, "created", req.Remark, item.Operator)
	response.OK(c, kubeGrantView(item))
}

// normalizeGrantList 去空格与空项，保持输入顺序
func normalizeGrantList(raw string) string {
	return strings.Join(splitGrantList(raw), ",")
}

func (h *Handler) UpdateKubeGrant(c *gin.Context) {
	var item model.KubeGrant
	if err := h.DB.First(&item, idParam(c)).Error; err != nil {
		response.NotFound(c, "授权不存在")
		return
	}
	var req kubeGrantReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "参数校验失败")
		return
	}
	// 改前补一版，否则 diff 没有「改前」
	h.backfillVersion(ruleTargetKubeGrant, item.ID)
	h.DB.First(&item, item.ID)

	// 主体与集群不允许改：那等于换了一条授权，留着原记录只会让审计看不懂
	updates := map[string]any{
		"namespaces": normalizeGrantList(req.Namespaces), "kinds": normalizeGrantList(req.Kinds),
		"allow_logs": req.AllowLogs, "allow_write": req.AllowWrite,
		"allow_forward": req.AllowForward, "expires_at": req.ExpiresAt,
		"remark": req.Remark, "operator": operatorName(c),
	}
	if err := h.DB.Model(&item).Updates(updates).Error; err != nil {
		response.Error(c, "更新失败")
		return
	}
	h.DB.First(&item, item.ID)
	h.recordVersion(ruleTargetKubeGrant, item.ID, "edited", "", operatorName(c))
	response.OK(c, kubeGrantView(item))
}

func (h *Handler) DeleteKubeGrant(c *gin.Context) {
	// 同 DeleteResourceGrant：删完就查不到删的是什么了，先写进审计
	var grant model.KubeGrant
	if err := h.DB.First(&grant, idParam(c)).Error; err == nil {
		middleware.SetAuditDetail(c, fmt.Sprintf(
			"删除容器授权：%s#%d → 集群 %d 命名空间 %q kinds %q（日志 %v / 写 %v / 转发 %v）",
			grant.SubjectType, grant.SubjectID, grant.ClusterID,
			grant.Namespaces, grant.Kinds, grant.AllowLogs, grant.AllowWrite, grant.AllowForward))
	}
	// 删前留一版，可用「误删恢复」建回来
	h.recordVersionBeforeDelete(ruleTargetKubeGrant, idParam(c), operatorName(c))
	if err := h.DB.Delete(&model.KubeGrant{}, idParam(c)).Error; err != nil {
		response.Error(c, "删除失败")
		return
	}
	response.OK(c, gin.H{"deleted": true})
}

// DiagnoseKubeGrant 「某个人现在到底能看到什么」。
//
// 这个接口存在的理由：授权是并集 + 有效期 + 管理员豁免 + 总开关四件事叠起来的结果，
// 靠人对着列表推演一定会算错。
func (h *Handler) DiagnoseKubeGrant(c *gin.Context) {
	if !h.canDiagnoseUser(c, idParam(c), "kubegrant:manage") {
		response.Forbidden(c, "只能诊断自己的可见范围；要看别人的需要「维护授权」权限")
		return
	}
	var user model.User
	if err := h.DB.Preload("Roles").First(&user, idParam(c)).Error; err != nil {
		response.NotFound(c, "用户不存在")
		return
	}

	var clusters []model.KubeCluster
	h.DB.Order("id asc").Find(&clusters)

	// 管理员豁免要按被诊断的那个人算，而不是按当前登录的人
	isAdmin := false
	var perms []string
	h.DB.Raw(`SELECT DISTINCT menus.auth_code FROM menus
		JOIN role_menus ON role_menus.menu_id = menus.id
		JOIN user_roles ON user_roles.role_id = role_menus.role_id
		WHERE user_roles.user_id = ? AND menus.auth_code <> ''`, user.ID).Scan(&perms)
	for _, code := range perms {
		if code == kubeAdminPerm {
			isAdmin = true
			break
		}
	}

	rows := make([]gin.H, 0, len(clusters))
	for _, cluster := range clusters {
		scope := h.kubeScopeFor(&user, cluster.ID)
		row := gin.H{"clusterId": cluster.ID, "clusterName": cluster.Name}
		switch {
		case !h.kubeGrantEnforced():
			row["visible"] = true
			row["reason"] = "授权未生效，任何登录用户都能看"
		case isAdmin:
			row["visible"] = true
			row["reason"] = "拥有 " + kubeAdminPerm + "，不受授权限制"
		case !scope.Found:
			row["visible"] = false
			row["reason"] = "没有这个集群的有效授权"
		default:
			row["visible"] = true
			if scope.AllNamespaces {
				row["namespaces"] = "全部"
			} else {
				row["namespaces"] = strings.Join(sortedKeys(scope.Namespaces), ",")
			}
			if scope.AllKinds {
				row["kinds"] = "全部"
			} else {
				row["kinds"] = strings.Join(sortedKeys(scope.Kinds), ",")
			}
			row["allowLogs"] = scope.AllowLogs
			row["allowWrite"] = scope.AllowWrite
			row["allowForward"] = scope.AllowForward
			row["reason"] = "按授权可见"
		}
		rows = append(rows, row)
	}

	response.OK(c, gin.H{
		"userId": user.ID, "username": user.Username,
		"enforced": h.kubeGrantEnforced(), "isKubeAdmin": isAdmin,
		"clusters": rows,
	})
}
