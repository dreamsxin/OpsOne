package handler

// 权限的版本化：角色、资源授权、容器授权。
//
// # 为什么把它们塞进「规则版本」那套机制
//
// 告警规则、检测规则、聚合策略早就有版本化 + 字段级 diff + 回滚，
// **而权限没有** —— 这是这个平台在治理上最说不过去的一处不对称：
// 业务规则改错了能查能回滚，权限改错了只剩审计里一行 `PUT /system/roles/3`。
// 而权限恰恰是误改代价最大的东西（把 dataScope 从 dept 改成 all、
// 顺手勾上 exec:prod、删掉一条本该保留的授权）。
//
// 这套机制本来就是多态的（`RuleVersion.Target` + `ruleTargetSpec` 闭包），
// 接三个新 target 不用碰通用逻辑：hash、去重、版本号推进、删除留痕、
// 回滚与误删恢复全部复用。
//
// # 接进来之前必须先修的一个洞
//
// 原来回滚/恢复的权限码**写死在路由上**（`alertrule:manage`），
// 也就是说有告警规则权限的人能回滚检测规则与聚合策略。那时候影响有限，
// 但如果就这样把「角色」接进来，就变成**拿一个业务权限就能改权限**。
// 所以先给 `ruleTargetSpec` 加了 `WritePerm`，路由只做粗粒度放行，
// 精确的码在 handler 里按 target 判（见 `allowRuleWrite`）。
//
// 需要说清的一点：**这不是在发明新的提权路径**。能回滚角色的人本来就有
// `role:manage`，本来就能直接编辑角色。版本化只是让「改回去」变得不容易出错。
//
// # 角色快照里的 authCodes 是「当时的样子」
//
// 角色的权限内容其实是 `role_menus` 关联（一串菜单 ID）。ID 对人不可读，
// 所以快照里**额外**存一份当时的权限码清单，让 diff 能直接读出
// 「这次改动加了 exec:prod」。但回滚只按 `menuIds` 生效 ——
// `authCodes` 是派生信息，某个菜单的权限码后来被改了的话，旧快照里那份就是历史值。
// 这一点写在页面的说明里，不留想象空间。

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"ops-platform/server/internal/model"
)

const (
	ruleTargetRole         = "role"
	ruleTargetResourceGrnt = "resource_grant"
	ruleTargetKubeGrant    = "kube_grant"
)

func init() {
	// 追加而不是写进 ruleTargetSpecs 的字面量：权限与业务规则是两类东西，
	// 放在各自的文件里，将来谁都不用为了加一个 target 去读另一类的代码
	ruleTargetSpecs = append(ruleTargetSpecs, permTargetSpecs()...)
}

func permTargetSpecs() []ruleTargetSpec {
	return []ruleTargetSpec{
		{
			Target: ruleTargetRole, Label: "角色", WritePerm: "role:manage", ReadPerm: "role:manage",
			Fields: []ruleField{
				{"name", "名称"}, {"description", "说明"},
				{"dataScope", "数据范围"}, {"dataDeptIds", "自定义部门"},
				{"menuIds", "菜单与按钮"}, {"authCodes", "权限码（当时的快照）"},
			},
			RuntimeNote: "角色编码（code）不进快照：它是别处引用这个角色的键，" +
				"回滚时改掉它等于换了一个角色。authCodes 是按 menuIds 派生的，" +
				"回滚只按 menuIds 生效",
			Load:     roleSnapshotLoad,
			AliveIDs: func(h *Handler) []uint { return pluckIDs(h, &model.Role{}) },
			BumpSeq: func(h *Handler, id uint, version int) {
				h.DB.Model(&model.Role{}).Where("id = ?", id).Update("version_seq", version)
			},
			ToUpdates:       roleSnapshotToUpdates,
			ResetOnRollback: nil,
			Apply:           roleApplyUpdates,
			Create:          roleCreateFromUpdates,
		},
		{
			Target: ruleTargetResourceGrnt, Label: "资源授权", WritePerm: "grant:manage", ReadPerm: "grant:manage",
			Fields: []ruleField{
				{"subjectType", "主体类型"}, {"subjectId", "主体 ID"}, {"subjectName", "主体"},
				{"resourceType", "资源类型"}, {"resourceId", "资源 ID"}, {"resourceName", "资源"},
				{"actions", "动作"}, {"expiresAt", "有效期"}, {"remark", "备注"},
			},
			RuntimeNote: "操作人（operator）不进快照：它记录的是「谁做了这次改动」，" +
				"属于版本自身的属性（版本行里已经有 operator），不是授权的配置",
			Load:     grantSnapshotLoad,
			AliveIDs: func(h *Handler) []uint { return pluckIDs(h, &model.ResourceGrant{}) },
			BumpSeq: func(h *Handler, id uint, version int) {
				h.DB.Model(&model.ResourceGrant{}).Where("id = ?", id).Update("version_seq", version)
			},
			ToUpdates:       grantSnapshotToUpdates,
			ResetOnRollback: nil,
			Apply: func(h *Handler, id uint, updates map[string]any) error {
				return h.DB.Model(&model.ResourceGrant{}).Where("id = ?", id).Updates(updates).Error
			},
			Create: grantCreateFromUpdates,
		},
		{
			Target: ruleTargetKubeGrant, Label: "容器授权", WritePerm: "kubegrant:manage", ReadPerm: "kubegrant:manage",
			Fields: []ruleField{
				{"subjectType", "主体类型"}, {"subjectId", "主体 ID"}, {"subjectName", "主体"},
				{"clusterId", "集群 ID"}, {"clusterName", "集群"},
				{"namespaces", "命名空间"}, {"kinds", "资源类型"},
				{"allowLogs", "可看日志"}, {"allowWrite", "可写"}, {"allowForward", "可转发"},
				{"expiresAt", "有效期"}, {"remark", "备注"},
			},
			RuntimeNote: "同资源授权：operator 不进快照",
			Load:        kubeGrantSnapshotLoad,
			AliveIDs:    func(h *Handler) []uint { return pluckIDs(h, &model.KubeGrant{}) },
			BumpSeq: func(h *Handler, id uint, version int) {
				h.DB.Model(&model.KubeGrant{}).Where("id = ?", id).Update("version_seq", version)
			},
			ToUpdates:       kubeGrantSnapshotToUpdates,
			ResetOnRollback: nil,
			Apply: func(h *Handler, id uint, updates map[string]any) error {
				return h.DB.Model(&model.KubeGrant{}).Where("id = ?", id).Updates(updates).Error
			},
			Create: kubeGrantCreateFromUpdates,
		},
	}
}

func pluckIDs(h *Handler, dest any) []uint {
	var ids []uint
	h.DB.Model(dest).Pluck("id", &ids)
	return ids
}

// ---------- 角色 ----------

func roleSnapshotLoad(h *Handler, id uint) (ruleSnapshotRow, bool) {
	var role model.Role
	if err := h.DB.Preload("Menus").First(&role, id).Error; err != nil {
		return ruleSnapshotRow{}, false
	}
	menuIDs := make([]uint, 0, len(role.Menus))
	codes := make([]string, 0, len(role.Menus))
	for _, menu := range role.Menus {
		menuIDs = append(menuIDs, menu.ID)
		if menu.AuthCode != "" {
			codes = append(codes, menu.AuthCode)
		}
	}
	// 排序是必须的：关联查询的顺序不保证稳定，不排的话同一份权限会算出不同的 hash，
	// 于是每次保存都多出一版「什么都没改」的记录
	sort.Slice(menuIDs, func(i, j int) bool { return menuIDs[i] < menuIDs[j] })
	sort.Strings(codes)

	return ruleSnapshotRow{
		ID: role.ID, Name: role.Name, VersionSeq: role.VersionSeq,
		Snapshot: map[string]any{
			"name": role.Name, "description": role.Description,
			"dataScope": role.DataScope, "dataDeptIds": role.DataDeptIDs,
			"menuIds": menuIDs, "authCodes": strings.Join(codes, ","),
		},
	}, true
}

// roleMenuIDsKey 不是数据库列，只是把菜单 ID 从 ToUpdates 传给 Apply 的信道。
// Apply 里必须先取出来再删掉，否则 GORM 会当成列名去 UPDATE
const roleMenuIDsKey = "__menu_ids"

func roleSnapshotToUpdates(snapshot map[string]any) (map[string]any, error) {
	name := strings.TrimSpace(asString(snapshot["name"]))
	if name == "" {
		return nil, fmt.Errorf("这一版的角色名是空的，不能用它回滚")
	}
	scope := normalizeDataScope(asString(snapshot["dataScope"]))
	deptIDs := asString(snapshot["dataDeptIds"])
	if scope == "custom" && strings.TrimSpace(strings.Trim(deptIDs, "[]")) == "" {
		return nil, fmt.Errorf("这一版的数据范围是「自定义部门」但部门清单是空的，" +
			"回滚会让这个角色什么都看不到 —— 请确认后手工调整")
	}
	return map[string]any{
		"name":          name,
		"description":   asString(snapshot["description"]),
		"data_scope":    scope,
		"data_dept_ids": deptIDs,
		roleMenuIDsKey:  snapshotUintList(snapshot["menuIds"]),
	}, nil
}

func roleApplyUpdates(h *Handler, id uint, updates map[string]any) error {
	menuIDs, _ := updates[roleMenuIDsKey].([]uint)
	columns := make(map[string]any, len(updates))
	for key, value := range updates {
		if key == roleMenuIDsKey {
			continue
		}
		columns[key] = value
	}

	var role model.Role
	if err := h.DB.First(&role, id).Error; err != nil {
		return err
	}
	if err := h.DB.Model(&role).Updates(columns).Error; err != nil {
		return err
	}
	// 菜单关联整份替换：权限是个集合，「补齐差集」那种写法会在回滚时留下
	// 本该被去掉的权限 —— 而那正是回滚要解决的问题
	return h.bindMenus(&role, menuIDs)
}

func roleCreateFromUpdates(h *Handler, updates map[string]any, userID uint) (uint, error) {
	_ = userID // 角色表没有 created_by
	role := model.Role{
		Name:        asString(updates["name"]),
		Description: asString(updates["description"]),
		DataScope:   asString(updates["data_scope"]),
		DataDeptIDs: asString(updates["data_dept_ids"]),
		// Code 必须唯一，而快照里刻意没存 code（它是别处引用角色的键）。
		// 恢复出来的角色给一个带时间戳的占位编码，让人必须去改一次 ——
		// 自动编一个看起来正常的编码会让人以为恢复得很完整
		Code: fmt.Sprintf("restored_%d", time.Now().Unix()),
	}
	if err := h.DB.Create(&role).Error; err != nil {
		return 0, err
	}
	if menuIDs, ok := updates[roleMenuIDsKey].([]uint); ok {
		if err := h.bindMenus(&role, menuIDs); err != nil {
			return role.ID, err
		}
	}
	return role.ID, nil
}

// ---------- 资源授权 ----------

func grantSnapshotLoad(h *Handler, id uint) (ruleSnapshotRow, bool) {
	var grant model.ResourceGrant
	if err := h.DB.First(&grant, id).Error; err != nil {
		return ruleSnapshotRow{}, false
	}
	return ruleSnapshotRow{
		ID: grant.ID, VersionSeq: grant.VersionSeq,
		Name: fmt.Sprintf("%s→%s", grant.SubjectName, grant.ResourceName),
		Snapshot: map[string]any{
			"subjectType": grant.SubjectType, "subjectId": grant.SubjectID,
			"subjectName":  grant.SubjectName,
			"resourceType": grant.ResourceType, "resourceId": grant.ResourceID,
			"resourceName": grant.ResourceName,
			"actions":      grant.Actions, "expiresAt": timePtrText(grant.ExpiresAt),
			"remark": grant.Remark,
		},
	}, true
}

func grantSnapshotToUpdates(snapshot map[string]any) (map[string]any, error) {
	subjectID := snapshotUint(snapshot["subjectId"])
	resourceID := snapshotUint(snapshot["resourceId"])
	if subjectID == 0 || resourceID == 0 {
		return nil, fmt.Errorf("这一版缺主体或资源 ID，不能用它回滚")
	}
	expires, err := parseTimeText(asString(snapshot["expiresAt"]))
	if err != nil {
		return nil, err
	}
	actions, err := normalizeGrantActions(asString(snapshot["actions"]))
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"subject_type":  normalizeSubjectType(asString(snapshot["subjectType"])),
		"subject_id":    subjectID,
		"subject_name":  asString(snapshot["subjectName"]),
		"resource_type": normalizeResourceType(asString(snapshot["resourceType"])),
		"resource_id":   resourceID,
		"resource_name": asString(snapshot["resourceName"]),
		"actions":       actions,
		"expires_at":    expires,
		"remark":        asString(snapshot["remark"]),
	}, nil
}

func grantCreateFromUpdates(h *Handler, updates map[string]any, userID uint) (uint, error) {
	_ = userID
	grant := model.ResourceGrant{
		SubjectType:  asString(updates["subject_type"]),
		SubjectName:  asString(updates["subject_name"]),
		ResourceType: asString(updates["resource_type"]),
		ResourceName: asString(updates["resource_name"]),
		Actions:      asString(updates["actions"]),
		Remark:       asString(updates["remark"]),
	}
	grant.SubjectID, _ = updates["subject_id"].(uint)
	grant.ResourceID, _ = updates["resource_id"].(uint)
	if at, ok := updates["expires_at"].(*time.Time); ok {
		grant.ExpiresAt = at
	}
	if err := h.DB.Create(&grant).Error; err != nil {
		return 0, err
	}
	return grant.ID, nil
}

// ---------- 容器授权 ----------

func kubeGrantSnapshotLoad(h *Handler, id uint) (ruleSnapshotRow, bool) {
	var grant model.KubeGrant
	if err := h.DB.First(&grant, id).Error; err != nil {
		return ruleSnapshotRow{}, false
	}
	return ruleSnapshotRow{
		ID: grant.ID, VersionSeq: grant.VersionSeq,
		Name: fmt.Sprintf("%s→%s", grant.SubjectName, grant.ClusterName),
		Snapshot: map[string]any{
			"subjectType": grant.SubjectType, "subjectId": grant.SubjectID,
			"subjectName": grant.SubjectName,
			"clusterId":   grant.ClusterID, "clusterName": grant.ClusterName,
			"namespaces": grant.Namespaces, "kinds": grant.Kinds,
			"allowLogs": grant.AllowLogs, "allowWrite": grant.AllowWrite,
			"allowForward": grant.AllowForward,
			"expiresAt":    timePtrText(grant.ExpiresAt), "remark": grant.Remark,
		},
	}, true
}

func kubeGrantSnapshotToUpdates(snapshot map[string]any) (map[string]any, error) {
	subjectID := snapshotUint(snapshot["subjectId"])
	clusterID := snapshotUint(snapshot["clusterId"])
	if subjectID == 0 || clusterID == 0 {
		return nil, fmt.Errorf("这一版缺主体或集群 ID，不能用它回滚")
	}
	expires, err := parseTimeText(asString(snapshot["expiresAt"]))
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"subject_type":  normalizeSubjectType(asString(snapshot["subjectType"])),
		"subject_id":    subjectID,
		"subject_name":  asString(snapshot["subjectName"]),
		"cluster_id":    clusterID,
		"cluster_name":  asString(snapshot["clusterName"]),
		"namespaces":    asString(snapshot["namespaces"]),
		"kinds":         asString(snapshot["kinds"]),
		"allow_logs":    asBool(snapshot["allowLogs"]),
		"allow_write":   asBool(snapshot["allowWrite"]),
		"allow_forward": asBool(snapshot["allowForward"]),
		"expires_at":    expires,
		"remark":        asString(snapshot["remark"]),
	}, nil
}

func kubeGrantCreateFromUpdates(h *Handler, updates map[string]any, userID uint) (uint, error) {
	_ = userID
	grant := model.KubeGrant{
		SubjectType: asString(updates["subject_type"]),
		SubjectName: asString(updates["subject_name"]),
		ClusterName: asString(updates["cluster_name"]),
		Namespaces:  asString(updates["namespaces"]),
		Kinds:       asString(updates["kinds"]),
		Remark:      asString(updates["remark"]),
	}
	grant.SubjectID, _ = updates["subject_id"].(uint)
	grant.ClusterID, _ = updates["cluster_id"].(uint)
	grant.AllowLogs, _ = updates["allow_logs"].(bool)
	grant.AllowWrite, _ = updates["allow_write"].(bool)
	grant.AllowForward, _ = updates["allow_forward"].(bool)
	if at, ok := updates["expires_at"].(*time.Time); ok {
		grant.ExpiresAt = at
	}
	if err := h.DB.Create(&grant).Error; err != nil {
		return 0, err
	}
	return grant.ID, nil
}

// ---------- 小工具 ----------

// snapshotUintList JSON 里的数字都是 float64，转回 []uint
func snapshotUintList(value any) []uint {
	raw, ok := value.([]any)
	if !ok {
		// 也可能是本进程刚生成、还没过一轮 JSON 的快照
		if ids, ok := value.([]uint); ok {
			return ids
		}
		return nil
	}
	out := make([]uint, 0, len(raw))
	for _, item := range raw {
		if id := snapshotUint(item); id > 0 {
			out = append(out, id)
		}
	}
	return out
}

func snapshotUint(value any) uint {
	switch v := value.(type) {
	case float64:
		if v <= 0 {
			return 0
		}
		return uint(v)
	case int:
		if v <= 0 {
			return 0
		}
		return uint(v)
	case uint:
		return v
	}
	return 0
}

func asBool(value any) bool {
	v, _ := value.(bool)
	return v
}

// timePtrText 时间存成固定格式的字符串而不是 time.Time：
// 快照要经过 JSON 往返，时区与精度在那个过程里会变，而 diff 是按字符串比的 ——
// 一个只差时区表示的值会被显示成「改过了」
func timePtrText(at *time.Time) string {
	if at == nil {
		return ""
	}
	return at.Format("2006-01-02 15:04:05")
}

func parseTimeText(text string) (*time.Time, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, nil
	}
	at, err := time.ParseInLocation("2006-01-02 15:04:05", text, time.Local)
	if err != nil {
		return nil, fmt.Errorf("这一版的有效期 %q 解析不了", text)
	}
	return &at, nil
}

// normalizeGrantActions 复用授权接口的动作白名单，避免回滚写进一个非法动作。
//
// 校验失败**不静默兜成 `*`**：那会把「这一版记了个非法动作」变成
// 「回滚后权限反而放大到全部」，是这里最不能犯的错。所以把错误往上抛，让回滚失败。
func normalizeGrantActions(actions string) (string, error) {
	cleaned, err := normalizeActions(actions)
	if err != nil {
		return "", fmt.Errorf("这一版的动作清单 %q 不合法：%w", actions, err)
	}
	return cleaned, nil
}

var _ = json.Marshal // 保留：快照内容的序列化由 rule_version.go 负责
