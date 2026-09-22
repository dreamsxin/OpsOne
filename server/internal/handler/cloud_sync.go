package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"ops-platform/server/internal/cloudapi"
	"ops-platform/server/internal/middleware"
	"ops-platform/server/internal/model"
	"ops-platform/server/internal/response"
)

// ---------- 手动触发 ----------

type syncReq struct {
	ResourceType string `json:"resourceType"` // ecs | domain；空或 all 表示全部
}

// syncLocks 同一个云账号同一时间只允许一轮同步：并发跑同一账号会把
// 「云上已消失」的 Gone 标记弄乱（两轮各自算自己的 seen 集合）。
// 不同账号之间互不影响，所以按 accountID 取锁而不是上全局锁。
var syncLocks sync.Map

// cloudEndpointOverride 仅供单测把云接入点指向本地 httptest 桩。
// 生产路径上它永远是空串 —— 接入点由 cloudapi 按产品与地域推导。
var cloudEndpointOverride string

// RunCloudSync 手动触发一个云账号的同步。
//
// 前端「触发同步」按钮走这里：点一次拉一次，结果实时写回 CloudSyncRun。
// 阿里云以外的 provider 会返回一个「尚未实现」的记录，不报 500。
func (h *Handler) RunCloudSync(c *gin.Context) {
	acct, ok := h.loadCloudScoped(c)
	if !ok {
		return
	}

	var req syncReq
	_ = c.ShouldBindJSON(&req)
	types := syncTypes(req.ResourceType, acct.Provider)

	if _, loaded := syncLocks.LoadOrStore(acct.ID, true); loaded {
		response.BadRequest(c, "该账号正在同步中，请稍后")
		return
	}
	defer syncLocks.Delete(acct.ID)

	user := middleware.CurrentUser(c)
	var runs []model.CloudSyncRun
	for _, rt := range types {
		run := h.doSync(c.Request.Context(), acct, rt, "manual", user.Username)
		runs = append(runs, run)
	}
	response.OK(c, runs)
}

// SyncCloudForSchedule 定时任务入口：扫所有 enabled + 有 AK 的阿里云账号，逐个同步。
// 跟安全事件采集一样的模式：cron 调用、互斥、markFixedRun。
func (h *Handler) SyncCloudForSchedule() {
	var accounts []model.CloudAccount
	h.DB.Where("enabled = ? AND provider = ? AND access_key_id <> ''", true, "aliyun").
		Find(&accounts)
	if len(accounts) == 0 {
		return
	}

	var created, failed int
	for i := range accounts {
		acct := &accounts[i]
		if _, loaded := syncLocks.LoadOrStore(acct.ID, true); loaded {
			continue // 有人手动在跑，跳过
		}
		for _, rt := range []string{"ecs", "domain"} {
			run := h.doSync(context.Background(), acct, rt, "cron", "system")
			if run.Status == "success" {
				created += run.CreatedCount
			} else {
				failed++
			}
		}
		syncLocks.Delete(acct.ID)
	}
	h.markFixedRun("cloud_sync", fmt.Sprintf(
		"%d 个账号，新增 %d 条，失败 %d 轮", len(accounts), created, failed))
	log.Printf("[cloud_sync] 定时同步完成: %d 个账号，新增 %d 条，失败 %d 轮",
		len(accounts), created, failed)
}

func syncTypes(requested, provider string) []string {
	switch strings.ToLower(strings.TrimSpace(requested)) {
	case "ecs":
		return []string{"ecs"}
	case "domain":
		return []string{"domain"}
	default:
		if provider == "aliyun" {
			return []string{"ecs", "domain"}
		}
		return []string{"ecs"}
	}
}

// doSync 执行一轮同步并写 CloudSyncRun 记录。无论成功失败都会落记录。
func (h *Handler) doSync(ctx context.Context, acct *model.CloudAccount, resourceType, trigger, operator string) model.CloudSyncRun {
	start := time.Now()
	run := model.CloudSyncRun{
		CloudAccountID: acct.ID,
		AccountName:    acct.Name,
		Provider:       acct.Provider,
		ResourceType:   resourceType,
		RegionID:       acct.Region,
		Trigger:        trigger,
		Operator:       operator,
		StartedAt:      start,
	}

	finishRun := func(status, msg string) model.CloudSyncRun {
		now := time.Now()
		run.Status = status
		run.Message = msg
		run.FinishedAt = &now
		run.DurationMS = now.Sub(start).Milliseconds()
		h.DB.Create(&run)
		return run
	}

	if acct.Provider != "aliyun" {
		return finishRun("failed", fmt.Sprintf("暂不支持 %s 的资源同步", acct.Provider))
	}

	// 解密 AK/SK
	secret, err := h.openSecret("云账号 AK/SK", acct.AccessKeySecret)
	if err != nil {
		return finishRun("failed", err.Error())
	}
	if acct.AccessKeyID == "" || secret == "" {
		return finishRun("failed", "云账号未配置 AccessKeyID 或 AccessKeySecret")
	}

	client := &cloudapi.AliyunClient{
		AccessKeyID:     acct.AccessKeyID,
		AccessKeySecret: secret,
		Endpoint:        cloudEndpointOverride,
	}

	switch resourceType {
	case "ecs":
		if strings.TrimSpace(acct.Region) == "" {
			return finishRun("failed", "云账号未配置地域（Region），ECS 同步需要指定地域")
		}
		instances, err := client.ListECSInstances(ctx, acct.Region)
		if err != nil {
			msg := "拉取 ECS 列表失败: " + err.Error()
			if apiErr, ok := err.(*cloudapi.APIError); ok && apiErr.IsAuthError() {
				msg = "密钥认证失败（" + apiErr.Code + "），请检查云账号的 AK/SK 和权限"
			}
			return finishRun("failed", msg)
		}
		run = h.mergeECS(acct, instances, run)
		return finishRun("success", fmt.Sprintf(
			"共 %d 台，新增 %d，更新 %d，消失 %d，匹配主机 %d",
			run.TotalCount, run.CreatedCount, run.UpdatedCount, run.GoneCount, run.MatchedCount))

	case "domain":
		domains, err := client.ListDNSDomains(ctx)
		if err != nil {
			msg := "拉取域名列表失败: " + err.Error()
			if apiErr, ok := err.(*cloudapi.APIError); ok && apiErr.IsAuthError() {
				msg = "密钥认证失败（" + apiErr.Code + "），请检查云账号的 AK/SK 和权限"
			}
			return finishRun("failed", msg)
		}
		run = h.mergeDomains(acct, domains, run)
		return finishRun("success", fmt.Sprintf(
			"共 %d 个，新增 %d，更新 %d，消失 %d",
			run.TotalCount, run.CreatedCount, run.UpdatedCount, run.GoneCount))

	default:
		return finishRun("failed", "未知的资源类型: "+resourceType)
	}
}

// ---------- merge 逻辑 ----------

// hostIPIndex 建一份「IP → 主机 ID」的内存索引，供同步时做自动匹配。
// 同时索引公网地址和内网地址（主机的 Address 可能是任一种）。
func (h *Handler) hostIPIndex() map[string]uint {
	var hosts []model.Host
	h.DB.Select("id, address").Find(&hosts)
	idx := make(map[string]uint, len(hosts))
	for _, host := range hosts {
		if host.Address != "" {
			idx[host.Address] = host.ID
		}
	}
	return idx
}

func (h *Handler) mergeECS(acct *model.CloudAccount, instances []cloudapi.ECSInstance, run model.CloudSyncRun) model.CloudSyncRun {
	run.TotalCount = len(instances)
	hostIdx := h.hostIPIndex()

	// 加载此账号 + ecs 的已有记录
	var existing []model.CloudResource
	h.DB.Where("cloud_account_id = ? AND resource_type = ?", acct.ID, "ecs").Find(&existing)
	existMap := make(map[string]*model.CloudResource, len(existing))
	for i := range existing {
		existMap[existing[i].ResourceID] = &existing[i]
	}

	seen := make(map[string]bool, len(instances))
	now := time.Now()

	for _, inst := range instances {
		seen[inst.InstanceID] = true

		privateIPs := strings.Join(inst.PrivateIPs, ",")
		publicIPs := strings.Join(inst.PublicIPs, ",")
		extra := ecsExtra(inst)

		matchedHostID, matchBy := matchHost(hostIdx, inst.PrivateIPs, inst.PublicIPs)

		if prev, ok := existMap[inst.InstanceID]; ok {
			// 已存在 → 更新
			h.DB.Model(prev).Updates(map[string]any{
				"name": inst.Name, "region_id": inst.RegionID, "zone_id": inst.ZoneID,
				"status": inst.Status, "gone": false,
				"private_ips": privateIPs, "public_ips": publicIPs,
				"spec": inst.InstanceType, "os_name": inst.OSName,
				"charge_type": inst.ChargeType, "expired_at": inst.ExpiredAt,
				"matched_host_id": matchedHostID, "match_by": matchBy,
				"extra": extra, "last_sync_at": now,
			})
			run.UpdatedCount++
		} else {
			// 新增
			res := model.CloudResource{
				CloudAccountID: acct.ID, Provider: acct.Provider,
				ResourceType: "ecs", ResourceID: inst.InstanceID,
				Name: inst.Name, RegionID: inst.RegionID, ZoneID: inst.ZoneID,
				Status:     inst.Status,
				PrivateIPs: privateIPs, PublicIPs: publicIPs,
				Spec: inst.InstanceType, OSName: inst.OSName,
				ChargeType: inst.ChargeType, ExpiredAt: inst.ExpiredAt,
				MatchedHostID: matchedHostID, MatchBy: matchBy,
				Extra: extra, FirstSeenAt: now, LastSyncAt: now,
			}
			h.DB.Create(&res)
			run.CreatedCount++
		}
		if matchedHostID != 0 {
			run.MatchedCount++
		}
	}

	// 云上没有了 → 标 Gone
	for rid, prev := range existMap {
		if !seen[rid] && !prev.Gone {
			h.DB.Model(prev).Updates(map[string]any{"gone": true, "last_sync_at": now})
			run.GoneCount++
		}
	}
	return run
}

func (h *Handler) mergeDomains(acct *model.CloudAccount, domains []cloudapi.DNSDomain, run model.CloudSyncRun) model.CloudSyncRun {
	run.TotalCount = len(domains)

	var existing []model.CloudResource
	h.DB.Where("cloud_account_id = ? AND resource_type = ?", acct.ID, "domain").Find(&existing)
	existMap := make(map[string]*model.CloudResource, len(existing))
	for i := range existing {
		existMap[existing[i].ResourceID] = &existing[i]
	}

	seen := make(map[string]bool, len(domains))
	now := time.Now()

	for _, d := range domains {
		seen[d.DomainName] = true
		extra := domainExtra(d)

		if prev, ok := existMap[d.DomainName]; ok {
			h.DB.Model(prev).Updates(map[string]any{
				"name": d.DomainName, "status": "hosted", "gone": false,
				"spec": d.VersionName, "extra": extra, "last_sync_at": now,
			})
			run.UpdatedCount++
		} else {
			res := model.CloudResource{
				CloudAccountID: acct.ID, Provider: acct.Provider,
				ResourceType: "domain", ResourceID: d.DomainName,
				Name: d.DomainName, Status: "hosted",
				Spec: d.VersionName, Extra: extra,
				FirstSeenAt: now, LastSyncAt: now,
			}
			h.DB.Create(&res)
			run.CreatedCount++
		}
	}

	for rid, prev := range existMap {
		if !seen[rid] && !prev.Gone {
			h.DB.Model(prev).Updates(map[string]any{"gone": true, "last_sync_at": now})
			run.GoneCount++
		}
	}
	return run
}

func matchHost(idx map[string]uint, privateIPs, publicIPs []string) (uint, string) {
	for _, ip := range privateIPs {
		if id, ok := idx[ip]; ok {
			return id, "private_ip"
		}
	}
	for _, ip := range publicIPs {
		if id, ok := idx[ip]; ok {
			return id, "public_ip"
		}
	}
	return 0, ""
}

func ecsExtra(inst cloudapi.ECSInstance) string {
	m := map[string]any{
		"cpu": inst.CPU, "memoryMB": inst.MemoryMB,
		"vpcId": inst.VpcID, "zoneId": inst.ZoneID,
	}
	if inst.CreatedAt != nil {
		m["cloudCreatedAt"] = inst.CreatedAt.Format(time.RFC3339)
	}
	if len(inst.Tags) > 0 {
		m["tags"] = inst.Tags
	}
	b, _ := json.Marshal(m)
	return string(b)
}

func domainExtra(d cloudapi.DNSDomain) string {
	m := map[string]any{
		"domainId":    d.DomainID,
		"punyCode":    d.PunyCode,
		"recordCount": d.RecordCount,
		"aliDomain":   d.AliDomain,
	}
	if len(d.DNSServers) > 0 {
		m["dnsServers"] = d.DNSServers
	}
	if d.Remark != "" {
		m["remark"] = d.Remark
	}
	b, _ := json.Marshal(m)
	return string(b)
}

// ---------- 资源清单 ----------

// ListCloudResources 云资源清单，支持按账号、类型、状态过滤，分页。
func (h *Handler) ListCloudResources(c *gin.Context) {
	page, size := pageParams(c)
	q := h.DB.Model(&model.CloudResource{})
	if v := c.Query("cloudAccountId"); v != "" {
		q = q.Where("cloud_account_id = ?", v)
	}
	if v := c.Query("resourceType"); v != "" {
		q = q.Where("resource_type = ?", v)
	}
	if v := c.Query("status"); v != "" {
		q = q.Where("status = ?", v)
	}
	if v := c.Query("gone"); v == "true" {
		q = q.Where("gone = ?", true)
	} else if v == "false" {
		q = q.Where("gone = ?", false)
	}
	if v := c.Query("matched"); v == "true" {
		q = q.Where("matched_host_id > 0")
	} else if v == "false" {
		q = q.Where("matched_host_id = 0")
	}
	if v := c.Query("keyword"); v != "" {
		like := "%" + v + "%"
		q = q.Where("name LIKE ? OR resource_id LIKE ? OR private_ips LIKE ? OR public_ips LIKE ?",
			like, like, like, like)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		response.Error(c, "查询云资源失败")
		return
	}
	var list []model.CloudResource
	if err := q.Order("id desc").Offset((page - 1) * size).Limit(size).Find(&list).Error; err != nil {
		response.Error(c, "查询云资源失败")
		return
	}
	response.OKPage(c, list, total, page, size)
}

// CloudResourceStats 云资源的聚合统计（按类型和状态分组），用于概览卡片。
func (h *Handler) CloudResourceStats(c *gin.Context) {
	type statRow struct {
		ResourceType string `json:"resourceType"`
		Status       string `json:"status"`
		Gone         bool   `json:"gone"`
		Count        int64  `json:"count"`
	}
	var rows []statRow
	h.DB.Model(&model.CloudResource{}).
		Select("resource_type, status, gone, count(*) as count").
		Group("resource_type, status, gone").
		Find(&rows)
	response.OK(c, rows)
}

// ---------- 同步记录 ----------

// ListCloudSyncRuns 同步记录列表。
func (h *Handler) ListCloudSyncRuns(c *gin.Context) {
	page, size := pageParams(c)
	q := h.DB.Model(&model.CloudSyncRun{})
	if v := c.Query("cloudAccountId"); v != "" {
		q = q.Where("cloud_account_id = ?", v)
	}
	if v := c.Query("status"); v != "" {
		q = q.Where("status = ?", v)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		response.Error(c, "查询同步记录失败")
		return
	}
	var list []model.CloudSyncRun
	if err := q.Order("id desc").Offset((page - 1) * size).Limit(size).Find(&list).Error; err != nil {
		response.Error(c, "查询同步记录失败")
		return
	}
	response.OKPage(c, list, total, page, size)
}

// ---------- 漂移对照 ----------

// CloudDrift 漂移对照：云上与平台台账对不上的三类情况。
//
// 诚实前提：只有「至少成功同步过一次 ECS」时才给出 hostOnly 那一栏。
// 从没同步过就把全部主机列成「云上没有」是纯噪音 —— 那时候云上清单本来就是空的。
// 即使同步过，hostOnly 里也可能是自建机房或别家云的机器，页面上会照实标注。
func (h *Handler) CloudDrift(c *gin.Context) {
	var syncedOnce int64
	h.DB.Model(&model.CloudSyncRun{}).
		Where("resource_type = ? AND status = ?", "ecs", "success").
		Count(&syncedOnce)

	// 1. 云上 ECS（仍存在）但没匹配到任何纳管主机
	var cloudOnly []model.CloudResource
	h.DB.Where("resource_type = ? AND gone = ? AND matched_host_id = 0", "ecs", false).
		Order("id desc").Limit(200).Find(&cloudOnly)

	// 2. 平台主机没有对应的云资源
	var hostOnly []model.Host
	if syncedOnce > 0 {
		var matchedIDs []uint
		h.DB.Model(&model.CloudResource{}).
			Where("resource_type = ? AND gone = ? AND matched_host_id > 0", "ecs", false).
			Pluck("matched_host_id", &matchedIDs)

		q := h.DB.Model(&model.Host{})
		if len(matchedIDs) > 0 {
			q = q.Where("id NOT IN ?", matchedIDs)
		}
		q.Order("id desc").Limit(200).Find(&hostOnly)
	}

	// 3. 云上已经查不到、台账里还留着的
	var gone []model.CloudResource
	h.DB.Where("resource_type = ? AND gone = ?", "ecs", true).
		Order("last_sync_at desc").Limit(200).Find(&gone)

	type driftItem struct {
		Side       string `json:"side"` // cloud_only | host_only | gone
		ResourceID string `json:"resourceId"`
		Name       string `json:"name"`
		Address    string `json:"address"`
		Status     string `json:"status"`
		Provider   string `json:"provider"`
		CloudResID uint   `json:"cloudResId"`
		HostID     uint   `json:"hostId"`
		LastSyncAt string `json:"lastSyncAt"`
	}

	items := make([]driftItem, 0, len(cloudOnly)+len(hostOnly)+len(gone))
	for _, res := range cloudOnly {
		addr := firstIP(res.PrivateIPs)
		if addr == "" {
			addr = firstIP(res.PublicIPs)
		}
		items = append(items, driftItem{
			Side: "cloud_only", ResourceID: res.ResourceID, Name: res.Name,
			Address: addr, Status: res.Status, Provider: res.Provider,
			CloudResID: res.ID, LastSyncAt: res.LastSyncAt.Format(time.RFC3339),
		})
	}
	for _, host := range hostOnly {
		items = append(items, driftItem{
			Side: "host_only", Name: host.Name, Address: host.Address,
			Status: host.Status, HostID: host.ID,
		})
	}
	for _, res := range gone {
		items = append(items, driftItem{
			Side: "gone", ResourceID: res.ResourceID, Name: res.Name,
			Status: res.Status, Provider: res.Provider, CloudResID: res.ID,
			LastSyncAt: res.LastSyncAt.Format(time.RFC3339),
		})
	}

	note := "hostOnly 只在成功同步过 ECS 之后才统计；里面也可能是自建机房或其它云的机器，不都是漂移"
	if syncedOnce == 0 {
		note = "还没有一次成功的 ECS 同步，暂不判断「平台有、云上没有」—— 那时候云上清单本来就是空的"
	}
	response.OK(c, gin.H{
		"syncedOnce": syncedOnce > 0,
		"cloudOnly":  len(cloudOnly),
		"hostOnly":   len(hostOnly),
		"gone":       len(gone),
		"items":      items,
		"note":       note,
	})
}

// ---------- 纳管为主机 ----------

type adoptReq struct {
	Name         string `json:"name" binding:"required"`
	Username     string `json:"username" binding:"required"`
	Port         int    `json:"port"`
	AuthType     string `json:"authType"`
	Secret       string `json:"secret"`
	CredentialID uint   `json:"credentialId"`
	Env          string `json:"env"`
	DeptID       uint   `json:"deptId"`
}

// AdoptCloudResource 把一条 CloudResource 纳管为 Host。
//
// 与直接创建主机的区别：地址自动填充、来源可追溯（MatchedHostID 反向关联）。
// 凭据校验逻辑复用 CreateHost 的规则（引用凭证库 or 自带凭据二选一）。
func (h *Handler) AdoptCloudResource(c *gin.Context) {
	var res model.CloudResource
	if err := h.DB.First(&res, idParam(c)).Error; err != nil {
		response.NotFound(c, "云资源不存在")
		return
	}
	if res.ResourceType != "ecs" {
		response.BadRequest(c, "只有 ECS 类型的云资源可以纳管为主机")
		return
	}
	if res.MatchedHostID != 0 {
		response.BadRequest(c, "该云资源已匹配到主机，不需要重复纳管")
		return
	}
	// 云上已经查不到的实例不该被纳管：那会在资产表里建出一台已经被释放的机器
	if res.Gone {
		response.BadRequest(c, "该云资源在云上已经查不到了（可能已释放），不能纳管")
		return
	}

	var req adoptReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "主机名和登录用户为必填项")
		return
	}

	// 凭据校验：与 CreateHost 一致
	if req.CredentialID != 0 {
		if err := h.assertCredentialUsable(req.CredentialID); err != nil {
			response.BadRequest(c, err.Error())
			return
		}
	} else if req.Secret == "" {
		response.BadRequest(c, "请提供登录密码或私钥，或改为引用凭证库")
		return
	}

	// 选地址：优先私网 IP
	address := firstIP(res.PrivateIPs)
	if address == "" {
		address = firstIP(res.PublicIPs)
	}
	if address == "" {
		response.BadRequest(c, "该云资源没有 IP 地址，无法纳管")
		return
	}

	user := middleware.CurrentUser(c)
	host := model.Host{
		Name: req.Name, Address: address, Port: defaultPort(req.Port),
		Username: req.Username, AuthType: defaultAuth(req.AuthType),
		Secret: h.sealSecret(req.Secret),
		Env:    defaultEnv(req.Env), DeptID: req.DeptID, Status: "unknown",
		CredentialID: req.CredentialID,
		CreatedBy:    user.ID,
		Remark:       fmt.Sprintf("从云资源 %s (%s) 纳管", res.ResourceID, res.Name),
	}
	if host.CredentialID != 0 {
		host.Secret = ""
	}
	if err := h.DB.Create(&host).Error; err != nil {
		response.Error(c, "创建主机失败")
		return
	}

	// 回写匹配关系
	h.DB.Model(&res).Updates(map[string]any{
		"matched_host_id": host.ID, "match_by": "adopt",
	})
	response.OK(c, host)
}

func firstIP(commaSeparated string) string {
	for _, ip := range strings.Split(commaSeparated, ",") {
		ip = strings.TrimSpace(ip)
		if ip != "" {
			return ip
		}
	}
	return ""
}
