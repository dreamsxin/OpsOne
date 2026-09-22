package db

import (
	"encoding/json"
	"errors"
	"log"
	"time"

	"github.com/glebarez/sqlite"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"ops-platform/server/internal/model"
)

func Open(dsn string, debug bool) (*gorm.DB, error) {
	level := logger.Warn
	if debug {
		level = logger.Info
	}
	return gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(level)})
}

func Migrate(g *gorm.DB) error {
	if err := g.AutoMigrate(
		&model.Company{}, &model.Department{},
		&model.User{}, &model.Role{}, &model.Menu{},
		&model.Host{}, &model.ExecJob{}, &model.ExecResult{}, &model.AuditLog{},
		&model.Credential{},
		&model.CommandRule{}, &model.Session{}, &model.SessionCommand{},
		&model.CronJob{}, &model.FileAudit{},
		&model.AlertSource{}, &model.Alert{}, &model.NotifyChannel{},
		&model.NotifyRoute{}, &model.NotifyRecord{},
		&model.Announcement{}, &model.Message{}, &model.SysConfig{},
		&model.Tag{}, &model.DBInstance{}, &model.FixedAsset{},
		&model.SiteLink{}, &model.EmailTemplate{}, &model.ResourceGrant{},
		&model.CloudAccount{}, &model.CloudResource{}, &model.CloudSyncRun{},
		&model.Domain{},
		&model.HostLogTarget{}, &model.HostLogScan{}, &model.HostLogUsage{},
		&model.SecuritySuggestionDismissal{},
		&model.RuleVersion{}, &model.NotifyTemplate{},
		&model.VaultAccount{}, &model.VaultTOTP{}, &model.VaultAccess{},
		&model.MailAccount{}, &model.EgressProxy{}, &model.KubeGrant{},
		&model.InventoryBatch{}, &model.InventoryItem{},
		&model.PurchaseOrder{}, &model.PurchaseItem{},
		&model.BuildServer{}, &model.BuildJob{}, &model.BuildRecord{},
		&model.Certificate{}, &model.AlertRule{},
		&model.Probe{}, &model.ProbeRecord{},
		&model.AggregationPolicy{},
		&model.AlertSilence{},
		&model.DBQueryLog{},
		&model.ExecGuardLog{},
		&model.Event{}, &model.EventLog{},
		&model.EventReview{}, &model.EventActionItem{},
		&model.Runbook{}, &model.RunbookUse{},
		&model.HostService{}, &model.HostServiceAction{},
		&model.ConfigFile{}, &model.ConfigVersion{}, &model.ConfigApply{},
		&model.Script{},
		&model.Topology{}, &model.TopologyNode{}, &model.TopologyEdge{},
		&model.DetectionRule{},
		&model.KubeCluster{}, &model.KubeChangeLog{}, &model.KubeForward{},
		&model.HostMetric{},
		&model.OnCallSchedule{}, &model.OnCallOverride{}, &model.AlertEscalation{},
		&model.ExposureTarget{}, &model.ExposureScan{},
		&model.MetricSource{}, &model.SavedMetricQuery{}, &model.LogSource{}, &model.TraceSource{},
		&model.ModelUpstream{}, &model.ModelCall{}, &model.AgentConfig{}, &model.AgentRun{},
		&model.ImApp{}, &model.ImAccount{}, &model.ImSyncRun{},
		&model.LdapServer{}, &model.LdapAccount{},
		&model.ApiToken{},
		&model.FirewallRule{}, &model.FirewallGroup{}, &model.FirewallSnapshot{},
		&model.AwarenessCourse{}, &model.AwarenessQuestion{}, &model.AwarenessRecord{},
		&model.Signature{},
		&model.SecurityEvent{}, &model.SecurityEventLog{}, &model.SecurityEventMute{},
	); err != nil {
		return err
	}
	if err := backfillCronProdConfirmed(g); err != nil {
		return err
	}
	return backfillEventResponded(g)
}

// cfgEventRespondedBackfill 记录「存量事件的响应时间已回填过」
const cfgEventRespondedBackfill = "migration.event_responded_backfilled"

// backfillEventResponded 升级兼容：SLA 上线前建的事件没有 responded_at。
//
// 如果留空不管，这些单子在新界面上会一律显示「响应已超时 N 天」——
// 明明当时有人处理过，只是平台那时候没记这个时间点，等于凭空造出一批违约。
// 所以从只追加的时间线里把真实时间捞回来：最早一条 status / note 记录就是
// 「第一次有人动手」；连时间线都没有的已解决事件，退一步用解决时间
// （解决本身也是一次响应，只是把响应时间算晚了，宁可算晚也不凭空算超时）。
func backfillEventResponded(g *gorm.DB) error {
	var exist model.SysConfig
	err := g.Where("`key` = ?", cfgEventRespondedBackfill).First(&exist).Error
	if err == nil {
		return nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}

	var events []model.Event
	if err := g.Where("responded_at IS NULL").Find(&events).Error; err != nil {
		return err
	}
	fromLog, fromResolved := 0, 0
	for _, event := range events {
		var first model.EventLog
		err := g.Where("event_id = ? AND action IN ?", event.ID, []string{"status", "note"}).
			Order("id asc").First(&first).Error
		switch {
		case err == nil:
			at := first.CreatedAt
			if err := g.Model(&model.Event{}).Where("id = ?", event.ID).
				Update("responded_at", &at).Error; err != nil {
				return err
			}
			fromLog++
		case errors.Is(err, gorm.ErrRecordNotFound):
			if event.ResolvedAt == nil {
				continue // 确实没人动过，留空是实话
			}
			if err := g.Model(&model.Event{}).Where("id = ?", event.ID).
				Update("responded_at", event.ResolvedAt).Error; err != nil {
				return err
			}
			fromResolved++
		default:
			return err
		}
	}
	if fromLog > 0 || fromResolved > 0 {
		log.Printf("[migrate] 事件 SLA 上线：回填响应时间 %d 条（取自时间线）+ %d 条（退回解决时间）",
			fromLog, fromResolved)
	}
	return g.Create(&model.SysConfig{
		Group: "migration", Key: cfgEventRespondedBackfill, Value: "true", Type: "bool",
		Label:   "事件响应时间已回填",
		Remark:  "SLA 上线时从事件时间线回填存量事件的响应时间，只执行一次，请勿手工改动",
		Builtin: true,
	}).Error
}

// cfgCronBackfill 记录「存量定时任务的生产确认已回填过」，避免每次启动都回填
const cfgCronBackfill = "migration.cron_prod_confirmed_backfilled"

// backfillCronProdConfirmed 升级兼容：下发闸门上线前建的定时任务没有「生产确认」这个概念。
//
// 如果直接按未确认处理，存量任务会在下一次触发时被闸门拦掉 —— 而定时任务通常在
// 凌晨触发，没人看着，等于悄悄停掉了一批运维作业。所以这里把存量任务一次性视为
// 已确认，并落一条内置配置记住做过了；之后新建或编辑任务都要重新显式确认。
func backfillCronProdConfirmed(g *gorm.DB) error {
	var exist model.SysConfig
	err := g.Where("`key` = ?", cfgCronBackfill).First(&exist).Error
	if err == nil {
		return nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}

	res := g.Model(&model.CronJob{}).Where("prod_confirmed = ?", false).Update("prod_confirmed", true)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected > 0 {
		log.Printf("[migrate] 下发闸门上线：%d 个存量定时任务按「已确认生产变更」处理，之后编辑需重新确认",
			res.RowsAffected)
	}
	return g.Create(&model.SysConfig{
		Group: "migration", Key: cfgCronBackfill, Value: "true", Type: "bool",
		Label:   "定时任务生产确认已回填",
		Remark:  "下发闸门上线时把存量定时任务视为已确认，只执行一次，请勿手工改动",
		Builtin: true,
	}).Error
}

// Seed 初始化菜单树、内置角色与管理员账号，可重复执行。
//
// 菜单 ID 按模块分段：10 工作台 / 100 资产 / 200 运维执行 / 300 容器 / 400 监控告警 /
// 500 安全合规 / 600 智能与成本 / 700 配置中心 / 800 系统管理 / 900 消息中心。
// Component 指向 /placeholder/index 的条目表示模块规划已确定、功能尚未实现。
func Seed(g *gorm.DB, adminPwd string) error {
	const todo = "/placeholder/index"

	menus := []model.Menu{
		// ---------- 工作台 ----------
		{ID: 10, Name: "Workbench", Title: "工作台", Path: "/dashboard", Icon: "Odometer", Sort: 10},
		{ID: 11, ParentID: 10, Name: "DashboardOverview", Title: "平台总览", Path: "/dashboard/overview", Component: "/dashboard/index", Icon: "PieChart", Sort: 1},
		{ID: 12, ParentID: 10, Name: "DashboardPersonal", Title: "个人工作台", Path: "/dashboard/personal", Component: "/dashboard/personal/index", Icon: "User", Sort: 2},
		{ID: 13, ParentID: 10, Name: "MyResources", Title: "我的资源", Path: "/dashboard/my-resources", Component: "/dashboard/my-resources/index", Icon: "Files", Sort: 3},
		{ID: 14, ParentID: 10, Name: "MyActivity", Title: "我的活动", Path: "/dashboard/my-activity", Component: "/dashboard/my-activity/index", Icon: "Notebook", Sort: 4},

		// ---------- 资产管理 ----------
		{ID: 100, Name: "Asset", Title: "资产管理", Path: "/asset", Icon: "Coin", Sort: 20},
		{ID: 101, ParentID: 100, Name: "HostList", Title: "主机资产", Path: "/asset/host", Component: "/asset/host/index", Icon: "Monitor", Sort: 1},
		{ID: 102, ParentID: 101, Title: "新增主机", Type: "button", AuthCode: "host:create", Sort: 1},
		{ID: 103, ParentID: 101, Title: "编辑主机", Type: "button", AuthCode: "host:update", Sort: 2},
		{ID: 104, ParentID: 101, Title: "删除主机", Type: "button", AuthCode: "host:delete", Sort: 3},
		{ID: 105, ParentID: 101, Title: "连通性探测", Type: "button", AuthCode: "host:check", Sort: 4},
		{ID: 110, ParentID: 100, Name: "DatabaseAsset", Title: "数据库资产", Path: "/asset/database", Component: "/asset/database/index", Icon: "Coin", Sort: 2},
		{ID: 116, ParentID: 110, Title: "维护数据库资产", Type: "button", AuthCode: "db:manage", Sort: 1},
		{ID: 111, ParentID: 100, Name: "CloudAccount", Title: "云账号", Path: "/asset/cloud", Component: "/asset/cloud/index", Icon: "Cloudy", Sort: 3},
		{ID: 119, ParentID: 111, Title: "维护云账号", Type: "button", AuthCode: "cloud:manage", Sort: 1},
		{ID: 112, ParentID: 100, Name: "AssetTag", Title: "标签管理", Path: "/asset/tag", Component: "/asset/tag/index", Icon: "PriceTag", Sort: 4},
		{ID: 117, ParentID: 112, Title: "维护标签", Type: "button", AuthCode: "tag:manage", Sort: 1},
		{ID: 113, ParentID: 100, Name: "FixedAsset", Title: "固定资产", Path: "/asset/fixed", Component: "/asset/fixed/index", Icon: "Box", Sort: 5},
		{ID: 118, ParentID: 113, Title: "维护固定资产", Type: "button", AuthCode: "asset:manage", Sort: 1},
		{ID: 114, ParentID: 100, Name: "AssetInventory", Title: "资产盘点", Path: "/asset/inventory", Component: "/asset/inventory/index", Icon: "Tickets", Sort: 6},
		{ID: 120, ParentID: 114, Title: "维护盘点", Type: "button", AuthCode: "inventory:manage", Sort: 1},
		{ID: 115, ParentID: 100, Name: "AssetPurchase", Title: "采购记录", Path: "/asset/purchase", Component: "/asset/purchase/index", Icon: "ShoppingCart", Sort: 7},
		{ID: 121, ParentID: 115, Title: "维护采购单", Type: "button", AuthCode: "purchase:manage", Sort: 1},
		{ID: 122, ParentID: 100, Name: "HostService", Title: "主机服务", Path: "/asset/service", Component: "/asset/service/index", Icon: "SetUp", Sort: 8},
		{ID: 123, ParentID: 122, Title: "纳管与维护服务", Type: "button", AuthCode: "service:manage", Sort: 1},
		{ID: 124, ParentID: 122, Title: "启停服务", Type: "button", AuthCode: "service:control", Sort: 2},
		{ID: 125, ParentID: 100, Name: "ConfigFile", Title: "配置文件", Path: "/asset/config-file", Component: "/asset/config-file/index", Icon: "Document", Sort: 9},
		{ID: 126, ParentID: 125, Title: "登记与编辑配置", Type: "button", AuthCode: "configfile:manage", Sort: 1},
		{ID: 127, ParentID: 125, Title: "下发与回滚配置", Type: "button", AuthCode: "configfile:apply", Sort: 2},
		// 云资源同步排在最后（Sort 10）而不是插到「云账号」后面：
		// 重排会改掉其它条目的 Sort，而菜单的展示字段归管理员，代码不该覆盖
		{ID: 128, ParentID: 100, Name: "CloudSync", Title: "云资源同步", Path: "/asset/cloud-sync", Component: "/asset/cloud-sync/index", Icon: "Refresh", Sort: 10},
		{ID: 129, ParentID: 128, Title: "触发同步", Type: "button", AuthCode: "cloud:sync", Sort: 1},
		{ID: 130, ParentID: 128, Title: "纳管为主机", Type: "button", AuthCode: "cloud:adopt", Sort: 2},
		{ID: 131, ParentID: 100, Name: "DomainList", Title: "域名管理", Path: "/asset/domain", Component: "/asset/domain/index", Icon: "Link", Sort: 11},
		{ID: 132, ParentID: 131, Title: "维护域名", Type: "button", AuthCode: "domain:manage", Sort: 1},
		{ID: 133, ParentID: 131, Title: "执行巡检", Type: "button", AuthCode: "domain:check", Sort: 2},
		// 主机体检报告：汇总已有巡检数据，不触发新的采集。平台没有 agent，
		// 所以它与参照站的「Agent 报告」不是一回事，页面上照实写明
		{ID: 134, ParentID: 100, Name: "HostReport", Title: "主机体检报告", Path: "/asset/host-report", Component: "/asset/host-report/index", Icon: "Document", Sort: 12},
		// 磁盘占用分析：按需 SSH 跑 df/du 找大目录大文件，并对照 df 与 du 的差值
		// 解释「已删除但仍被进程持有」的空间。只读、结果不落库
		{ID: 135, ParentID: 100, Name: "DiskUsage", Title: "磁盘占用分析", Path: "/asset/disk-usage", Component: "/asset/disk-usage/index", Icon: "PieChart", Sort: 13},

		// ---------- 运维执行 ----------
		// 「堡垒机」是个二级分组：Web 终端 / 会话审计 / 文件管理 这三件事合起来
		// 才是大家认知里的堡垒机，分散在一级列表里会让人以为平台没有这个能力。
		{ID: 200, Name: "Execute", Title: "运维执行", Path: "/execute", Icon: "Promotion", Sort: 30},
		{ID: 230, ParentID: 200, Name: "Bastion", Title: "堡垒机", Path: "/execute/bastion", Icon: "Platform", Sort: 1},
		{ID: 201, ParentID: 230, Name: "WebTerminal", Title: "Web 终端", Path: "/execute/terminal", Component: "/execute/terminal/index", Icon: "Platform", Sort: 1},
		{ID: 202, ParentID: 201, Title: "登录终端", Type: "button", AuthCode: "terminal:connect", Sort: 1},
		{ID: 206, ParentID: 230, Name: "SessionAudit", Title: "会话审计", Path: "/execute/session", Component: "/execute/session/index", Icon: "VideoCamera", Sort: 2},
		{ID: 207, ParentID: 206, Title: "回放会话", Type: "button", AuthCode: "session:replay", Sort: 1},
		{ID: 210, ParentID: 230, Name: "FileManager", Title: "文件管理", Path: "/execute/file", Component: "/execute/file/index", Icon: "Folder", Sort: 3},
		{ID: 213, ParentID: 210, Title: "上传与新建", Type: "button", AuthCode: "file:write", Sort: 1},
		{ID: 214, ParentID: 210, Title: "浏览与下载", Type: "button", AuthCode: "file:read", Sort: 2},
		{ID: 215, ParentID: 210, Title: "删除文件", Type: "button", AuthCode: "file:delete", Sort: 3},
		{ID: 203, ParentID: 200, Name: "BatchExec", Title: "批量执行", Path: "/execute/batch", Component: "/execute/batch/index", Icon: "Cpu", Sort: 2},
		{ID: 204, ParentID: 203, Title: "下发命令", Type: "button", AuthCode: "exec:run", Sort: 1},
		{ID: 211, ParentID: 200, Name: "Scheduler", Title: "定时任务", Path: "/execute/scheduler", Component: "/execute/scheduler/index", Icon: "Timer", Sort: 3},
		{ID: 216, ParentID: 211, Title: "维护任务", Type: "button", AuthCode: "cron:manage", Sort: 1},
		{ID: 217, ParentID: 211, Title: "立即执行", Type: "button", AuthCode: "cron:run", Sort: 2},
		{ID: 205, ParentID: 200, Name: "ScriptLibrary", Title: "脚本库", Path: "/execute/script", Component: "/execute/script/index", Icon: "Notebook", Sort: 4},
		{ID: 220, ParentID: 205, Title: "维护脚本", Type: "button", AuthCode: "script:manage", Sort: 1},
		{ID: 212, ParentID: 200, Name: "BuildDeploy", Title: "构建发布", Path: "/execute/build", Component: "/execute/build/index", Icon: "SetUp", Sort: 5},
		{ID: 218, ParentID: 212, Title: "维护服务器与任务", Type: "button", AuthCode: "build:manage", Sort: 1},
		{ID: 219, ParentID: 212, Title: "触发构建与同步", Type: "button", AuthCode: "build:run", Sort: 2},
		{ID: 221, ParentID: 200, Name: "DBQuery", Title: "数据库查询", Path: "/execute/db-query", Component: "/execute/db-query/index", Icon: "Coin", Sort: 6},
		{ID: 222, ParentID: 221, Title: "浏览与查询数据库", Type: "button", AuthCode: "db:query", Sort: 1},

		// ---------- 容器平台 ----------
		{ID: 300, Name: "Kubernetes", Title: "容器平台", Path: "/kubernetes", Icon: "Ship", Sort: 40},
		{ID: 301, ParentID: 300, Name: "K8sSource", Title: "集群接入", Path: "/kubernetes/source", Component: "/kubernetes/source/index", Icon: "Connection", Sort: 1},
		{ID: 305, ParentID: 301, Title: "维护集群", Type: "button", AuthCode: "kube:manage", Sort: 1},
		{ID: 302, ParentID: 300, Name: "K8sWorkload", Title: "工作负载", Path: "/kubernetes/workload", Component: "/kubernetes/workload/index", Icon: "Grid", Sort: 2},
		{ID: 303, ParentID: 300, Name: "K8sResource", Title: "资源管理", Path: "/kubernetes/resource", Component: "/kubernetes/resource/index", Icon: "Files", Sort: 3},
		{ID: 306, ParentID: 303, Title: "改动集群资源", Type: "button", AuthCode: "kube:write", Sort: 1},
		{ID: 304, ParentID: 300, Name: "K8sForward", Title: "服务转发", Path: "/kubernetes/forward", Component: "/kubernetes/forward/index", Icon: "Share", Sort: 4},
		{ID: 307, ParentID: 304, Title: "开关转发隧道", Type: "button", AuthCode: "kube:forward", Sort: 1},
		{ID: 308, ParentID: 300, Name: "K8sCapacity", Title: "容量与配额", Path: "/kubernetes/capacity", Component: "/kubernetes/capacity/index", Icon: "Odometer", Sort: 5},
		// 按类型的专用页，全部只读 —— 通用「资源管理」页是白名单 YAML 浏览器，答不了这三类
		{ID: 309, ParentID: 300, Name: "K8sHelm", Title: "Helm 应用", Path: "/kubernetes/helm", Component: "/kubernetes/helm/index", Icon: "Box", Sort: 6},
		{ID: 310, ParentID: 300, Name: "K8sCRD", Title: "自定义资源", Path: "/kubernetes/crd", Component: "/kubernetes/crd/index", Icon: "Coin", Sort: 7},
		{ID: 311, ParentID: 300, Name: "K8sRBAC", Title: "RBAC 账户", Path: "/kubernetes/rbac", Component: "/kubernetes/rbac/index", Icon: "User", Sort: 8},
		// 节点与命名空间：只看它们自身的健康与约束，资源账本仍然在「容量与配额」页
		{ID: 312, ParentID: 300, Name: "K8sNode", Title: "节点与命名空间", Path: "/kubernetes/node", Component: "/kubernetes/node/index", Icon: "Cpu", Sort: 9},
		// 授权管理：一条授权同时表达「哪个集群 + 哪些命名空间 + 哪些资源类型」，
		// 刻意不拆成「授权」与「数据授权」两页，理由见 model.KubeGrant 的注释
		{ID: 313, ParentID: 300, Name: "K8sGrant", Title: "授权管理", Path: "/kubernetes/grant", Component: "/kubernetes/grant/index", Icon: "Unlock", Sort: 10},
		{ID: 314, ParentID: 313, Title: "维护授权", Type: "button", AuthCode: "kubegrant:manage", Sort: 1},

		// ---------- 监控告警 ----------
		{ID: 400, Name: "Monitor", Title: "监控告警", Path: "/monitor", Icon: "TrendCharts", Sort: 50},
		{ID: 401, ParentID: 400, Name: "AlertSituation", Title: "告警态势", Path: "/monitor/situation", Component: "/monitor/situation/index", Icon: "DataAnalysis", Sort: 1},
		{ID: 402, ParentID: 400, Name: "AlertList", Title: "告警列表", Path: "/monitor/alerts", Component: "/monitor/alerts/index", Icon: "Bell", Sort: 2},
		{ID: 420, ParentID: 402, Title: "确认与恢复", Type: "button", AuthCode: "alert:handle", Sort: 1},
		{ID: 403, ParentID: 400, Name: "AlertRule", Title: "告警规则", Path: "/monitor/alert-rules", Component: "/monitor/alert-rules/index", Icon: "Tickets", Sort: 3},
		{ID: 422, ParentID: 403, Title: "维护与试跑规则", Type: "button", AuthCode: "alertrule:manage", Sort: 1},
		{ID: 404, ParentID: 400, Name: "DetectionRule", Title: "检测规则", Path: "/monitor/detection-rules", Component: "/monitor/detection-rules/index", Icon: "Aim", Sort: 4},
		{ID: 427, ParentID: 404, Title: "维护与试跑检测规则", Type: "button", AuthCode: "detection:manage", Sort: 1},
		{ID: 405, ParentID: 400, Name: "NotifyRouting", Title: "通知路由", Path: "/monitor/routing", Component: "/monitor/routing/index", Icon: "Guide", Sort: 5},
		{ID: 421, ParentID: 405, Title: "维护路由", Type: "button", AuthCode: "route:manage", Sort: 1},
		{ID: 406, ParentID: 400, Name: "AggregationPolicy", Title: "聚合策略", Path: "/monitor/aggregation", Component: "/monitor/aggregation/index", Icon: "Operation", Sort: 6},
		{ID: 424, ParentID: 406, Title: "维护聚合策略", Type: "button", AuthCode: "aggregation:manage", Sort: 1},
		{ID: 407, ParentID: 400, Name: "EventCenter", Title: "事件中心", Path: "/monitor/events", Component: "/monitor/events/index", Icon: "Warning", Sort: 7},
		{ID: 425, ParentID: 407, Title: "建单与处置", Type: "button", AuthCode: "event:manage", Sort: 1},
		{ID: 408, ParentID: 400, Name: "MetricQuery", Title: "指标查询", Path: "/monitor/metrics", Component: "/monitor/metrics/index", Icon: "Histogram", Sort: 8},
		{ID: 431, ParentID: 408, Title: "维护数据源与常用查询", Type: "button", AuthCode: "metric:manage", Sort: 1},
		{ID: 409, ParentID: 400, Name: "LogQuery", Title: "日志查询", Path: "/monitor/logs", Component: "/monitor/logs/index", Icon: "Document", Sort: 9},
		{ID: 432, ParentID: 409, Title: "维护日志数据源", Type: "button", AuthCode: "log:manage", Sort: 1},
		// 主机日志排在「日志查询」（Loki 代理）后面：两者是两条独立的路，
		// 没部署 Loki 的环境里只有这一条能看到日志。Sort 取末尾，不动已有条目
		{ID: 442, ParentID: 400, Name: "HostLog", Title: "主机日志", Path: "/monitor/host-logs", Component: "/monitor/host-logs/index", Icon: "Tickets", Sort: 20},
		{ID: 443, ParentID: 442, Title: "查看日志内容", Type: "button", AuthCode: "hostlog:view", Sort: 1},
		{ID: 444, ParentID: 442, Title: "维护监控点与巡检", Type: "button", AuthCode: "hostlog:manage", Sort: 2},
		// 值班大屏与通知模板。大屏回答「现在要不要动手」，与「告警态势」的运营统计分工不同
		{ID: 445, ParentID: 400, Name: "Wallboard", Title: "值班大屏", Path: "/monitor/wallboard", Component: "/monitor/wallboard/index", Icon: "DataBoard", Sort: 21},
		{ID: 446, ParentID: 400, Name: "NotifyTemplate", Title: "通知模板", Path: "/monitor/notify-template", Component: "/monitor/notify-template/index", Icon: "ChatDotSquare", Sort: 22},
		{ID: 447, ParentID: 446, Title: "维护通知模板", Type: "button", AuthCode: "channel:manage", Sort: 1},
		{ID: 410, ParentID: 400, Name: "TraceQuery", Title: "链路追踪", Path: "/monitor/traces", Component: "/monitor/traces/index", Icon: "Share", Sort: 10},
		{ID: 433, ParentID: 410, Title: "维护链路数据源", Type: "button", AuthCode: "trace:manage", Sort: 1},
		{ID: 411, ParentID: 400, Name: "Probe", Title: "拨测探测", Path: "/monitor/probe", Component: "/monitor/probe/index", Icon: "Position", Sort: 11},
		{ID: 423, ParentID: 411, Title: "维护与执行拨测", Type: "button", AuthCode: "probe:manage", Sort: 1},
		{ID: 412, ParentID: 400, Name: "Topology", Title: "业务拓扑", Path: "/monitor/topology", Component: "/monitor/topology/index", Icon: "Connection", Sort: 12},
		{ID: 426, ParentID: 412, Title: "维护拓扑", Type: "button", AuthCode: "topology:manage", Sort: 1},
		{ID: 413, ParentID: 400, Name: "PlatformHealth", Title: "平台健康", Path: "/monitor/health", Component: "/monitor/health/index", Icon: "FirstAidKit", Sort: 13},
		{ID: 414, ParentID: 400, Name: "PublicIPMonitor", Title: "公网监测", Path: "/monitor/public-ip", Component: "/monitor/public-ip/index", Icon: "Compass", Sort: 14},
		{ID: 430, ParentID: 414, Title: "维护与扫描暴露面", Type: "button", AuthCode: "exposure:manage", Sort: 1},
		{ID: 415, ParentID: 400, Name: "HostMetric", Title: "主机指标", Path: "/monitor/host-metrics", Component: "/monitor/host-metrics/index", Icon: "Odometer", Sort: 15},
		{ID: 428, ParentID: 415, Title: "手动采集", Type: "button", AuthCode: "host:check", Sort: 1},
		{ID: 416, ParentID: 400, Name: "OnCall", Title: "值班升级", Path: "/monitor/oncall", Component: "/monitor/oncall/index", Icon: "AlarmClock", Sort: 16},
		{ID: 429, ParentID: 416, Title: "维护值班表", Type: "button", AuthCode: "oncall:manage", Sort: 1},
		{ID: 417, ParentID: 400, Name: "AlertSilence", Title: "静默与维护窗口", Path: "/monitor/silences", Component: "/monitor/silences/index", Icon: "MuteNotification", Sort: 17},
		{ID: 434, ParentID: 417, Title: "维护静默窗口", Type: "button", AuthCode: "silence:manage", Sort: 1},
		{ID: 418, ParentID: 400, Name: "EventReview", Title: "事件复盘", Path: "/monitor/reviews", Component: "/monitor/reviews/index", Icon: "Notebook", Sort: 18},
		{ID: 435, ParentID: 418, Title: "维护复盘与改进项", Type: "button", AuthCode: "review:manage", Sort: 1},
		{ID: 419, ParentID: 400, Name: "Runbook", Title: "处置剧本", Path: "/monitor/runbooks", Component: "/monitor/runbooks/index", Icon: "Reading", Sort: 19},
		{ID: 436, ParentID: 419, Title: "维护剧本与记录使用", Type: "button", AuthCode: "runbook:manage", Sort: 1},
		// 400-419 这段页面 ID 用满了，按钮占到 436，页面从 440 起续接
		{ID: 440, ParentID: 400, Name: "MonitorSLA", Title: "监控设置", Path: "/monitor/sla-settings", Component: "/monitor/sla-settings/index", Icon: "Clock", Sort: 20},
		{ID: 441, ParentID: 440, Title: "维护 SLA 目标", Type: "button", AuthCode: "sla:manage", Sort: 1},

		// ---------- 安全合规 ----------
		{ID: 500, Name: "Security", Title: "安全合规", Path: "/security", Icon: "Key", Sort: 60},
		{ID: 501, ParentID: 500, Name: "SSLCertificate", Title: "证书管理", Path: "/security/ssl", Component: "/security/ssl/index", Icon: "Stamp", Sort: 1},
		{ID: 506, ParentID: 501, Title: "维护证书", Type: "button", AuthCode: "cert:manage", Sort: 1},
		{ID: 507, ParentID: 501, Title: "执行巡检", Type: "button", AuthCode: "cert:check", Sort: 2},
		{ID: 502, ParentID: 500, Name: "Firewall", Title: "防火墙策略", Path: "/security/firewall", Component: "/security/firewall/index", Icon: "Lock", Sort: 2},
		{ID: 509, ParentID: 502, Title: "维护规则", Type: "button", AuthCode: "firewall:manage", Sort: 1},
		{ID: 510, ParentID: 502, Title: "下发与回滚", Type: "button", AuthCode: "firewall:apply", Sort: 2},
		{ID: 503, ParentID: 500, Name: "TwoFA", Title: "双因子口令", Path: "/security/twofa", Component: "/security/twofa/index", Icon: "Key", Sort: 3},
		{ID: 508, ParentID: 503, Title: "重置他人绑定", Type: "button", AuthCode: "totp:reset", Sort: 1},
		{ID: 504, ParentID: 500, Name: "SecurityAwareness", Title: "安全意识", Path: "/security/awareness", Component: "/security/awareness/index", Icon: "Reading", Sort: 4},
		{ID: 514, ParentID: 504, Title: "维护与催办", Type: "button", AuthCode: "awareness:manage", Sort: 1},
		{ID: 505, ParentID: 500, Name: "FeatureLibrary", Title: "特征库", Path: "/security/features", Component: "/security/features/index", Icon: "Collection", Sort: 5},
		{ID: 515, ParentID: 505, Title: "维护特征", Type: "button", AuthCode: "signature:manage", Sort: 1},
		{ID: 516, ParentID: 505, Title: "应用与撤下", Type: "button", AuthCode: "signature:apply", Sort: 2},
		{ID: 511, ParentID: 500, Name: "Credential", Title: "凭证库", Path: "/security/credential", Component: "/security/credential/index", Icon: "Key", Sort: 6},
		{ID: 512, ParentID: 511, Title: "维护凭据", Type: "button", AuthCode: "credential:manage", Sort: 1},
		{ID: 513, ParentID: 511, Title: "测试连接", Type: "button", AuthCode: "credential:check", Sort: 2},
		// 500 段页面与按钮是混着递增的，501-516 已用满，从 517 续
		{ID: 517, ParentID: 500, Name: "SecurityEvent", Title: "安全事件", Path: "/security/events", Component: "/security/events/index", Icon: "WarnTriangleFilled", Sort: 7},
		{ID: 518, ParentID: 517, Title: "研判与处置", Type: "button", AuthCode: "secevent:manage", Sort: 1},
		{ID: 519, ParentID: 517, Title: "生成封禁草稿", Type: "button", AuthCode: "secevent:respond", Sort: 2},
		{ID: 520, ParentID: 500, Name: "SecurityOverview", Title: "安全概览", Path: "/security/overview", Component: "/security/overview/index", Icon: "DataBoard", Sort: 8},
		{ID: 521, ParentID: 500, Name: "SecuritySuggestion", Title: "学习建议", Path: "/security/suggestions", Component: "/security/suggestions/index", Icon: "MagicStick", Sort: 9},
		{ID: 522, ParentID: 521, Title: "通过与拒绝建议", Type: "button", AuthCode: "suggestion:apply", Sort: 1},
		{ID: 523, ParentID: 500, Name: "Vault", Title: "账号密码库", Path: "/security/vault", Component: "/security/vault/index", Icon: "Wallet", Sort: 10},
		{ID: 524, ParentID: 523, Title: "维护条目", Type: "button", AuthCode: "vault:manage", Sort: 1},
		{ID: 525, ParentID: 523, Title: "取用明文", Type: "button", AuthCode: "vault:reveal", Sort: 2},
		{ID: 526, ParentID: 500, Name: "VaultTOTP", Title: "2FA 验证码库", Path: "/security/vault-totp", Component: "/security/vault-totp/index", Icon: "Iphone", Sort: 11},
		{ID: 527, ParentID: 526, Title: "维护种子", Type: "button", AuthCode: "vault:manage", Sort: 1},
		{ID: 528, ParentID: 526, Title: "出验证码", Type: "button", AuthCode: "vault:reveal", Sort: 2},

		// ---------- 智能与成本 ----------
		{ID: 600, Name: "Intelligence", Title: "智能与成本", Path: "/ai", Icon: "MagicStick", Sort: 70},
		{ID: 601, ParentID: 600, Name: "ModelPool", Title: "模型资源池", Path: "/ai/model-pool", Component: "/ai/model-pool/index", Icon: "Cpu", Sort: 1},
		{ID: 605, ParentID: 601, Title: "维护上游", Type: "button", AuthCode: "model:manage", Sort: 1},
		{ID: 606, ParentID: 601, Title: "调用模型", Type: "button", AuthCode: "model:call", Sort: 2},
		{ID: 602, ParentID: 600, Name: "ModelUsage", Title: "用量与成本", Path: "/ai/model-usage", Component: "/ai/model-usage/index", Icon: "Money", Sort: 2},
		{ID: 603, ParentID: 600, Name: "AgentRuns", Title: "Agent 运行", Path: "/ai/agent-runs", Component: "/ai/agent-runs/index", Icon: "Cpu", Sort: 3},
		{ID: 604, ParentID: 600, Name: "AgentConfig", Title: "Agent 配置", Path: "/ai/agent-config", Component: "/ai/agent-config/index", Icon: "SetUp", Sort: 4},
		{ID: 607, ParentID: 604, Title: "维护 Agent", Type: "button", AuthCode: "agent:manage", Sort: 1},
		{ID: 608, ParentID: 604, Title: "运行 Agent", Type: "button", AuthCode: "agent:run", Sort: 2},

		// ---------- 配置中心 ----------
		{ID: 700, Name: "ConfigCenter", Title: "配置中心", Path: "/config", Icon: "Tools", Sort: 80},
		{ID: 701, ParentID: 700, Name: "ConfigItem", Title: "配置项", Path: "/config/items", Component: "/config/items/index", Icon: "Files", Sort: 1},
		{ID: 706, ParentID: 701, Title: "维护配置", Type: "button", AuthCode: "config:manage", Sort: 1},
		{ID: 702, ParentID: 700, Name: "WebhookInbound", Title: "Webhook 接入", Path: "/config/webhooks", Component: "/config/webhooks/index", Icon: "Link", Sort: 2},
		{ID: 705, ParentID: 702, Title: "维护接入源", Type: "button", AuthCode: "source:manage", Sort: 1},
		{ID: 703, ParentID: 700, Name: "SiteNavigation", Title: "站点导航", Path: "/config/site-navigation", Component: "/config/site-navigation/index", Icon: "Compass", Sort: 3},
		{ID: 704, ParentID: 700, Name: "EmailTemplate", Title: "邮件模板", Path: "/config/email-templates", Component: "/config/email-templates/index", Icon: "Message", Sort: 4},
		{ID: 707, ParentID: 700, Name: "MailAccount", Title: "发件邮箱", Path: "/config/mail-accounts", Component: "/config/mail-accounts/index", Icon: "Promotion", Sort: 5},
		{ID: 708, ParentID: 707, Title: "维护发件邮箱", Type: "button", AuthCode: "mail:manage", Sort: 1},
		{ID: 709, ParentID: 700, Name: "EgressProxy", Title: "代理检测", Path: "/config/proxies", Component: "/config/proxies/index", Icon: "Switch", Sort: 6},
		{ID: 710, ParentID: 709, Title: "维护与检测代理", Type: "button", AuthCode: "proxy:manage", Sort: 1},

		// ---------- 系统管理 ----------
		{ID: 800, Name: "System", Title: "系统管理", Path: "/system", Icon: "Setting", Sort: 90},
		{ID: 801, ParentID: 800, Name: "SysUser", Title: "用户管理", Path: "/system/user", Component: "/system/user/index", Icon: "User", Sort: 1},
		{ID: 802, ParentID: 801, Title: "新增用户", Type: "button", AuthCode: "user:create", Sort: 1},
		{ID: 803, ParentID: 801, Title: "编辑用户", Type: "button", AuthCode: "user:update", Sort: 2},
		{ID: 804, ParentID: 801, Title: "删除用户", Type: "button", AuthCode: "user:delete", Sort: 3},
		{ID: 805, ParentID: 800, Name: "SysRole", Title: "角色权限", Path: "/system/role", Component: "/system/role/index", Icon: "Lock", Sort: 2},
		{ID: 806, ParentID: 805, Title: "维护角色", Type: "button", AuthCode: "role:manage", Sort: 1},
		{ID: 807, ParentID: 800, Name: "SysDepartment", Title: "部门管理", Path: "/system/department", Component: "/system/department/index", Icon: "OfficeBuilding", Sort: 3},
		{ID: 825, ParentID: 807, Title: "维护组织", Type: "button", AuthCode: "org:manage", Sort: 1},
		{ID: 808, ParentID: 800, Name: "SysCompany", Title: "公司管理", Path: "/system/company", Component: "/system/company/index", Icon: "OfficeBuilding", Sort: 4},
		{ID: 809, ParentID: 800, Name: "SysMenu", Title: "菜单管理", Path: "/system/menu", Component: "/system/menu/index", Icon: "Menu", Sort: 5},
		{ID: 826, ParentID: 809, Title: "维护菜单", Type: "button", AuthCode: "menu:manage", Sort: 1},
		{ID: 810, ParentID: 800, Name: "DataPermission", Title: "数据权限", Path: "/system/data-permission", Component: "/system/data-permission/index", Icon: "Filter", Sort: 6},
		{ID: 811, ParentID: 800, Name: "ResourceGrant", Title: "资源授权", Path: "/system/resource-grant", Component: "/system/resource-grant/index", Icon: "Unlock", Sort: 7},
		{ID: 827, ParentID: 811, Title: "维护授权", Type: "button", AuthCode: "grant:manage", Sort: 1},
		{ID: 812, ParentID: 800, Name: "NotifyChannel", Title: "通知渠道", Path: "/system/notify-channel", Component: "/system/notify-channel/index", Icon: "Message", Sort: 8},
		{ID: 823, ParentID: 812, Title: "维护渠道", Type: "button", AuthCode: "channel:manage", Sort: 1},
		{ID: 813, ParentID: 800, Name: "NotifyRecord", Title: "通知记录", Path: "/system/notify-record", Component: "/system/notify-record/index", Icon: "MessageBox", Sort: 9},
		{ID: 814, ParentID: 800, Name: "Announcement", Title: "公告管理", Path: "/system/announcement", Component: "/system/announcement/index", Icon: "Bell", Sort: 10},
		{ID: 824, ParentID: 814, Title: "维护与发布", Type: "button", AuthCode: "announcement:manage", Sort: 1},
		{ID: 815, ParentID: 800, Name: "ImIntegration", Title: "IM 集成", Path: "/system/im", Component: "/system/im/index", Icon: "ChatDotRound", Sort: 11},
		{ID: 829, ParentID: 815, Title: "维护 IM 应用", Type: "button", AuthCode: "im:manage", Sort: 1},
		{ID: 830, ParentID: 815, Title: "执行组织同步", Type: "button", AuthCode: "im:sync", Sort: 2},
		{ID: 831, ParentID: 800, Name: "LdapAuth", Title: "LDAP 账号接入", Path: "/system/ldap", Component: "/system/ldap/index", Icon: "Connection", Sort: 14},
		{ID: 832, ParentID: 831, Title: "维护目录与绑定", Type: "button", AuthCode: "ldap:manage", Sort: 1},
		{ID: 833, ParentID: 800, Name: "ApiToken", Title: "API 令牌", Path: "/system/api-token", Component: "/system/api-token/index", Icon: "Key", Sort: 15},
		{ID: 834, ParentID: 833, Title: "维护令牌", Type: "button", AuthCode: "token:manage", Sort: 1},
		{ID: 816, ParentID: 800, Name: "SysConfig", Title: "系统配置", Path: "/system/config", Component: "/system/config/index", Icon: "Tools", Sort: 12},
		{ID: 820, ParentID: 800, Name: "CommandRule", Title: "命令规则", Path: "/system/command-rule", Component: "/system/command-rule/index", Icon: "WarningFilled", Sort: 13},
		{ID: 821, ParentID: 820, Title: "维护规则", Type: "button", AuthCode: "rule:manage", Sort: 1},
		{ID: 822, ParentID: 800, Name: "AuditLog", Title: "操作审计", Path: "/system/audit", Component: "/system/audit/index", Icon: "Document", Sort: 14},
		{ID: 817, ParentID: 800, Name: "DataRetention", Title: "数据留存", Path: "/system/retention", Component: "/system/retention/index", Icon: "DeleteFilled", Sort: 15},
		{ID: 828, ParentID: 817, Title: "执行清理", Type: "button", AuthCode: "retention:run", Sort: 1},

		// ---------- 消息中心 ----------
		{ID: 900, Name: "MessageCenter", Title: "消息中心", Path: "/message", Icon: "Message", Sort: 100},
		{ID: 901, ParentID: 900, Name: "MessageInbox", Title: "我的消息", Path: "/message/inbox", Component: "/message/inbox/index", Icon: "MessageBox", Sort: 1},
		{ID: 902, ParentID: 900, Name: "AnnouncementFeed", Title: "公告中心", Path: "/message/announcement", Component: "/message/announcement/index", Icon: "Bell", Sort: 2},
	}
	if err := upsertBuiltinMenus(g, menus); err != nil {
		return err
	}

	var allMenus []model.Menu
	if err := g.Find(&allMenus).Error; err != nil {
		return err
	}

	admin := model.Role{ID: 1, Code: "admin", Name: "超级管理员", Description: "拥有全部菜单与操作权限"}
	if err := g.Where("id = ?", admin.ID).Attrs(admin).FirstOrCreate(&model.Role{}).Error; err != nil {
		return err
	}
	var adminRole model.Role
	if err := g.First(&adminRole, 1).Error; err != nil {
		return err
	}
	if err := g.Model(&adminRole).Association("Menus").Replace(allMenus); err != nil {
		return err
	}

	// 只读角色：可见菜单，不含按钮权限
	viewer := model.Role{ID: 2, Code: "viewer", Name: "只读运维", Description: "仅可查看，不可变更"}
	if err := g.Where("id = ?", viewer.ID).Attrs(viewer).FirstOrCreate(&model.Role{}).Error; err != nil {
		return err
	}
	var viewerRole model.Role
	if err := g.First(&viewerRole, 2).Error; err != nil {
		return err
	}
	var viewMenus []model.Menu
	for _, m := range allMenus {
		if m.Type != "button" {
			viewMenus = append(viewMenus, m)
		}
	}
	if err := g.Model(&viewerRole).Association("Menus").Replace(viewMenus); err != nil {
		return err
	}

	var user model.User
	err := g.Where("username = ?", "admin").First(&user).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		hash, hErr := bcrypt.GenerateFromPassword([]byte(adminPwd), bcrypt.DefaultCost)
		if hErr != nil {
			return hErr
		}
		user = model.User{Username: "admin", PasswordHash: string(hash), Nickname: "管理员", Status: 1}
		if err := g.Create(&user).Error; err != nil {
			return err
		}
		if err := g.Model(&user).Association("Roles").Replace([]model.Role{adminRole}); err != nil {
			return err
		}
		log.Printf("[init] 已创建管理员账号 admin，初始密码来自 OPS_ADMIN_PASSWORD，请首次登录后立即修改")
	} else if err != nil {
		return err
	}

	if err := seedCommandRules(g); err != nil {
		return err
	}
	if err := seedSignatures(g); err != nil {
		return err
	}
	if err := seedAwarenessDemo(g); err != nil {
		return err
	}
	if err := seedRunbooks(g); err != nil {
		return err
	}
	if err := seedEmailTemplates(g); err != nil {
		return err
	}
	if err := seedNotifyTemplates(g); err != nil {
		return err
	}
	return seedSysConfigs(g)
}

// seedRunbooks 内置处置剧本。
//
// 只写那些平台自己一定会产的告警（内置指标、拨测、证书巡检），并且步骤里的命令
// 一律是只读排查命令 —— 内置剧本不替人做决定，止血动作留给人按当时情况填。
// 按 ID 存在即跳过，管理员改过的内容不会被启动覆盖。
func seedRunbooks(g *gorm.DB) error {
	type stepDef struct {
		Title   string `json:"title"`
		Detail  string `json:"detail"`
		Command string `json:"command"`
	}
	pack := func(items []stepDef) string {
		raw, _ := json.Marshal(items)
		return string(raw)
	}
	labels := func(kv map[string]string) string {
		raw, _ := json.Marshal(kv)
		return string(raw)
	}

	books := []model.Runbook{
		{
			ID: 1, Name: "磁盘使用率高", Category: "主机", RiskLevel: "medium", Enabled: true, Version: 1,
			Summary:       "根分区或数据盘快满。先找出谁占的空间，再决定清理还是扩容 —— 不要直接 rm 大文件，先确认没有进程正在写它。",
			MatchLabels:   labels(map[string]string{"metric": "disk"}),
			MatchKeywords: "磁盘,disk,使用率",
			Precheck:      "确认这台机器的角色（数据库 / 日志节点 / 无状态应用），数据库节点的清理要和 DBA 一起做。",
			Rollback:      "清理是不可逆的。删之前先把要删的清单落到处置记录里；日志类文件优先用 truncate 而不是 rm，避免进程句柄还占着空间。",
			Steps: pack([]stepDef{
				{Title: "看各挂载点的使用率，确认是哪个分区", Command: "df -hT"},
				{Title: "找出占空间最多的前 20 个目录", Detail: "从告警里的挂载点开始往下找", Command: "du -xh --max-depth=2 / 2>/dev/null | sort -rh | head -n 20"},
				{Title: "确认有没有已删除但句柄未释放的文件", Detail: "这种情况 du 看不到、df 却满着", Command: "lsof +L1 2>/dev/null | head -n 20"},
				{Title: "检查日志目录与轮转配置", Command: "ls -lhS /var/log | head -n 20"},
				{Title: "决定处置方式并记录", Detail: "清理 / truncate / 扩容 / 调整轮转。动手前把清单写进事件处置记录。"},
			}),
		},
		{
			ID: 2, Name: "CPU 或负载持续偏高", Category: "主机", RiskLevel: "low", Enabled: true, Version: 1,
			Summary:       "先分清是「真忙」还是「等 IO」，再定位到进程。负载数字本身不说明问题，要看是谁在占。",
			MatchLabels:   labels(map[string]string{"metric": "cpu"}),
			MatchKeywords: "CPU,负载,load",
			Precheck:      "先看这是不是预期内的高峰（发布、批处理、压测）。是的话记录下来直接关掉告警，不要为了让曲线好看去杀进程。",
			Rollback:      "只要不杀进程，这本剧本的步骤都是只读的。",
			Steps: pack([]stepDef{
				{Title: "看整体负载与运行/阻塞队列", Command: "uptime"},
				{Title: "区分用户态、系统态与 iowait", Detail: "iowait 高说明瓶颈在磁盘或网络存储，不是 CPU", Command: "vmstat 1 5"},
				{Title: "按 CPU 占用排出前 15 个进程", Command: "ps -eo pid,ppid,pcpu,pmem,etime,cmd --sort=-pcpu | head -n 16"},
				{Title: "确认是不是有大量处于 D 状态的进程", Command: "ps -eo stat,pid,cmd | awk '$1 ~ /D/' | head -n 20"},
				{Title: "对照监控里的主机指标曲线，判断是突发还是持续"},
				{Title: "决定处置方式并记录", Detail: "限流 / 扩容 / 回滚发布 / 联系业务。动手前先在事件里写清判断依据。"},
			}),
		},
		{
			ID: 3, Name: "拨测失败：服务不可达", Category: "服务", RiskLevel: "low", Enabled: true, Version: 1,
			Summary:       "拨测红了先分清是「服务挂了」还是「路上不通」。平台自己就是一个拨测点，从别的位置再测一次能省很多时间。",
			MatchLabels:   labels(map[string]string{"module": "probe"}),
			MatchKeywords: "拨测,probe,不可达,超时",
			Precheck:      "先看这个目标的其他拨测点、以及同机器上其他服务是否也在报警 —— 一起红通常是网络或机器问题，单个红通常是服务问题。",
			Rollback:      "全是只读检查，没有需要回滚的动作。",
			Steps: pack([]stepDef{
				{Title: "在平台里手动重跑一次这条拨测", Detail: "监控告警 → 拨测探测 → 执行，确认是持续失败还是抖动"},
				{Title: "从目标机器本地访问一次", Detail: "本地通、远端不通 = 网络或防火墙；本地也不通 = 服务本身", Command: "curl -sS -o /dev/null -w '%{http_code} %{time_total}s\\n' http://127.0.0.1"},
				{Title: "确认端口在听", Command: "ss -lntp | head -n 30"},
				{Title: "看服务进程与最近的错误日志", Command: "systemctl status --no-pager"},
				{Title: "检查平台的暴露面记录，确认端口是否被改过", Detail: "监控告警 → 公网监测，对比基线端口"},
			}),
		},
		{
			ID: 4, Name: "证书即将到期", Category: "安全", RiskLevel: "medium", Enabled: true, Version: 1,
			Summary:       "证书到期是少数能提前几十天知道、却经常真的过期的故障。这本剧本的重点是把「谁负责换、换完谁验证」定下来。",
			MatchKeywords: "证书,cert,到期,过期",
			Precheck:      "先确认这张证书是谁签发、谁在续（自动续签的只要确认续签任务是否正常，不要重复申请）。平台不签发也不托管私钥。",
			Rollback:      "换证前备份旧证书与私钥；新证书生效后如果握手失败，把旧文件换回去并 reload（不是 restart）。",
			Steps: pack([]stepDef{
				{Title: "确认对端当前实际在用的证书与剩余天数", Detail: "以真实握手为准，不要只看文件", Command: "echo | openssl s_client -servername example.com -connect example.com:443 2>/dev/null | openssl x509 -noout -subject -dates -issuer"},
				{Title: "在平台里跑一次证书巡检，确认告警不是陈旧数据", Detail: "安全合规 → 证书管理 → 执行巡检"},
				{Title: "确认所有使用这张证书的位置", Detail: "网关、Nginx、K8s Ingress Secret、CDN 回源都可能各存一份"},
				{Title: "申请/续签并替换，reload 服务", Detail: "reload 而不是 restart，避免断连"},
				{Title: "替换后再跑一次巡检确认状态恢复", Detail: "状态没回到 valid 就说明还有一处没换到"},
			}),
		},
		{
			ID: 5, Name: "告警在响但没人收到通知", Category: "平台", RiskLevel: "low", Enabled: true, Version: 1,
			Summary:       "「告警明明触发了却没人知道」是最危险的一类问题。平台把通知链路上每个可能吞掉消息的环节都留了痕，按顺序查即可。",
			MatchKeywords: "未收到,通知,没人,静默",
			Precheck:      "先确认告警确实进了平台（告警列表里能看到），否则问题在上游而不是通知链路。",
			Rollback:      "只读排查。若需要临时结束静默，操作会留痕。",
			Steps: pack([]stepDef{
				{Title: "看这条告警的通知记录", Detail: "告警列表 → 详情，没有记录说明根本没发出；有记录但 failed 看错误信息"},
				{Title: "检查是否被静默或维护窗口挡住", Detail: "告警上的「静默来源」字段会写明是哪一条"},
				{Title: "检查是否被聚合策略抑制", Detail: "告警上的「被抑制」字段会写明策略名"},
				{Title: "用选路预演确认它应该走哪条路由", Detail: "监控告警 → 通知路由 → 选路预演"},
				{Title: "试发一次渠道，确认渠道本身可用", Detail: "系统管理 → 通知渠道 → 试发"},
				{Title: "检查值班表与升级链", Detail: "监控告警 → 值班升级，确认当班人算得出来、叫人记录有没有产生"},
			}),
		},
	}

	for _, book := range books {
		var exist model.Runbook
		if err := g.Where("id = ?", book.ID).First(&exist).Error; err == nil {
			continue
		}
		if err := g.Create(&book).Error; err != nil {
			return err
		}
		// 带 default 的布尔列 Create 后会被回填成库默认值，显式写回
		if err := g.Model(&model.Runbook{}).Where("id = ?", book.ID).
			Update("enabled", book.Enabled).Error; err != nil {
			return err
		}
	}
	return nil
}

// seedEmailTemplates 内置邮件模板，只在编码不存在时写入
func seedEmailTemplates(g *gorm.DB) error {
	templates := []model.EmailTemplate{{
		Code:    "alert.default",
		Name:    "告警通知（默认）",
		Subject: "[{{.severity}}] {{.title}}",
		Body: `告警标题：{{.title}}
级别：{{.severity}}
来源：{{.source}}
当前值：{{.value}}
出现次数：{{.count}}
首次出现：{{.firstSeenAt}}
最近出现：{{.lastSeenAt}}

摘要：
{{.summary}}

标签：
{{.labels}}

-- 本邮件由 OpsOne 自动发送`,
		Variables: "title,severity,source,value,count,summary,labels,firstSeenAt,lastSeenAt,status",
		Enabled:   true,
		Builtin:   true,
		Remark:    "email 类型通知渠道未指定模板时使用",
	}, {
		// 值班呼叫专用模板。在它之前值班呼叫邮件回落到 alert.default，
		// 于是「派给你、第几级、确认后不再升级」这些只进了站内消息，邮件里看不到 ——
		// 收到的是一封和普通告警一模一样的信，看不出这是在叫自己
		Code:    "oncall.call",
		Name:    "值班呼叫",
		Subject: "{{.title}}",
		Body: `值班表【{{.scheduleName}}】把这条 {{.severity}} 级告警派给你（第 {{.level}} 级）。

告警：{{.alertTitle}}
详情：{{.summary}}
首次出现：{{.firstSeenAt}}，已累计 {{.count}} 次。

确认后不再向上升级。

-- 本邮件由 OpsOne 自动发送`,
		Variables: "title,scheduleName,level,assignee,alertTitle,severity,summary,count,firstSeenAt",
		Enabled:   true,
		Builtin:   true,
		Remark:    "值班升级叫人时使用，变量与告警通知不同（见通知模板页的变量表）",
	}}

	for i := range templates {
		var exist model.EmailTemplate
		err := g.Where("code = ?", templates[i].Code).First(&exist).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			if err := g.Create(&templates[i]).Error; err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// seedNotifyTemplates 内置的 IM / webhook 模板。
//
// 都**默认停用**：启用它们会改变现有渠道发出去的内容形状，
// 尤其 webhook 的默认报文是对接收端的契约。这里只是给人一个可以照着改的起点。
func seedNotifyTemplates(g *gorm.DB) error {
	templates := []model.NotifyTemplate{{
		Code: "alert.im.compact", Name: "告警 IM（精简）",
		Kind: "im", Scene: "alert",
		Body: `[{{.severity}}] {{.title}}
{{.summary}}
当前值 {{.value}}，累计 {{.count}} 次
{{.labels}}`,
		Builtin: true, Enabled: false,
		Remark: "比默认格式更短，适合群里刷得快的场景。启用前先在预览里看一眼",
	}, {
		Code: "alert.webhook.simple", Name: "告警 Webhook（精简 JSON）",
		Kind: "webhook", Scene: "alert",
		Body:    `{"text":"[{{.severity}}] {{.title}}","detail":"{{.summary}}","value":"{{.value}}","count":"{{.count}}"}`,
		Builtin: true, Enabled: false,
		Remark: "默认报文有 11 个字段且形状是对接收端的契约；这个模板给只认少量字段的接收端用",
	}}

	for i := range templates {
		var exist model.NotifyTemplate
		err := g.Where("code = ?", templates[i].Code).First(&exist).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			if err := g.Create(&templates[i]).Error; err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// upsertBuiltinMenus 同步内置菜单。
//
// 结构字段（父级、路径、组件、类型、权限码、路由名）以代码为准每次覆盖；
// 展示字段（标题、图标、排序、隐藏）只在首次写入时给默认值，之后由菜单管理页面维护。
// 不在清单内的内置菜单会被删除（版本升级清理旧入口），用户自建菜单不受影响。
func upsertBuiltinMenus(g *gorm.DB, menus []model.Menu) error {
	keepIDs := make([]uint, 0, len(menus))

	for i := range menus {
		menu := menus[i]
		menu.Builtin = true
		// 清单里普通菜单不写 Type，靠列默认值只在 INSERT 时生效；
		// UPDATE 必须显式补上，否则会把已有行的 type 覆盖成空串，菜单树会整片消失
		if menu.Type == "" {
			menu.Type = "menu"
		}
		keepIDs = append(keepIDs, menu.ID)

		var exist model.Menu
		err := g.Where("id = ?", menu.ID).First(&exist).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			if err := g.Create(&menu).Error; err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}

		updates := map[string]any{
			"parent_id": menu.ParentID, "name": menu.Name, "path": menu.Path,
			"component": menu.Component, "type": menu.Type, "auth_code": menu.AuthCode,
			"builtin": true,
		}
		if err := g.Model(&exist).Updates(updates).Error; err != nil {
			return err
		}
	}

	// 只清理内置菜单里已下线的条目，自建菜单保留
	if err := g.Where("builtin = ? AND id NOT IN ?", true, keepIDs).Delete(&model.Menu{}).Error; err != nil {
		return err
	}
	// 菜单删除后清理悬空授权
	return g.Exec("DELETE FROM role_menus WHERE menu_id NOT IN (SELECT id FROM menus)").Error
}

// seedSysConfigs 内置配置项，只在键不存在时写入，用户改过的值不会被覆盖
func seedSysConfigs(g *gorm.DB) error {
	configs := []model.SysConfig{
		{Group: "platform", Key: "platform.name", Value: "OpsOne 一体化运维平台", Type: "string", Label: "平台名称", Remark: "显示在登录页与顶栏", Builtin: true},
		{Group: "platform", Key: "platform.login_notice", Value: "", Type: "text", Label: "登录页公告", Remark: "留空则不展示", Builtin: true},
		{Group: "execute", Key: "exec.concurrency", Value: "10", Type: "int", Label: "批量执行并发数", Remark: "单个作业同时连接的主机数上限", Builtin: true},
		{Group: "execute", Key: "file.max_upload_mb", Value: "512", Type: "int", Label: "上传文件大小上限(MB)", Remark: "文件管理单文件上限", Builtin: true},
		{Group: "bastion", Key: "session.record_keep_days", Value: "0", Type: "int", Label: "会话录像保留天数", Remark: "0 表示永久保留；启动时清理过期录像", Builtin: true},
		{Group: "smtp", Key: "smtp.host", Value: "", Type: "string", Label: "SMTP 服务器", Remark: "留空表示不启用邮件通知", Builtin: true},
		{Group: "smtp", Key: "smtp.port", Value: "465", Type: "int", Label: "SMTP 端口", Remark: "465 走 TLS，587/25 走明文或 STARTTLS", Builtin: true},
		{Group: "smtp", Key: "smtp.username", Value: "", Type: "string", Label: "SMTP 账号", Builtin: true},
		{Group: "smtp", Key: "smtp.password", Value: "", Type: "string", Label: "SMTP 密码", Remark: "配了 OPS_SECRET_KEY 时加密落库；配置接口不回传取值，留空表示不修改", Builtin: true},
		{Group: "smtp", Key: "smtp.from", Value: "", Type: "string", Label: "发件人地址", Remark: "留空则用 SMTP 账号", Builtin: true},
		{Group: "smtp", Key: "smtp.tls", Value: "true", Type: "bool", Label: "使用 TLS 直连", Remark: "465 端口通常需要开启", Builtin: true},
		{Group: "proxy", Key: "proxy.test_url", Value: "", Type: "string", Label: "代理检测地址",
			Remark: "检测出口代理时请求的地址。默认留空——内网不一定有可用的回显服务，平台不替你决定这台机器可以访问公网。填一个会回显来源 IP 的地址才能看出出口 IP", Builtin: true},
		{Group: "kube", Key: "kube.grant_enforce", Value: "false", Type: "bool", Label: "启用容器平台授权",
			Remark: "默认关闭，此时任何登录用户都能看到所有集群的只读数据（含 Pod 日志、RBAC）。开启前请先在「容器平台 → 授权管理」把授权配好：没有任何授权的集群，开启后对非管理员立刻完全不可见。拥有 kube:manage 的人不受限制", Builtin: true},
		{Group: "security", Key: "security.totp.mode", Value: "optional", Type: "string", Label: "双因子口令策略", Remark: "optional 自愿绑定；required 未绑定的账号除个人页与绑定接口外一律拒绝", Builtin: true},
		{Group: "retention", Key: "retention.exec_job_days", Value: "90", Type: "int", Label: "执行记录保留天数", Remark: "批量执行/脚本/定时任务的作业与逐台结果；0 表示永久保留", Builtin: true},
		{Group: "retention", Key: "retention.probe_record_days", Value: "7", Type: "int", Label: "拨测记录保留天数", Remark: "拨测频率高、增长快，建议保持较短；0 表示永久保留", Builtin: true},
		{Group: "retention", Key: "retention.exposure_scan_days", Value: "180", Type: "int", Label: "暴露面扫描记录保留天数", Remark: "留着才能回答「这个端口是什么时候开的」；0 表示永久保留", Builtin: true},
		{Group: "retention", Key: "retention.host_metric_days", Value: "14", Type: "int", Label: "主机指标保留天数", Remark: "每台主机每次采集一条，是增长最快的表；0 表示永久保留", Builtin: true},
		{Group: "retention", Key: "retention.notify_record_days", Value: "90", Type: "int", Label: "通知投递记录保留天数", Remark: "0 表示永久保留", Builtin: true},
		{Group: "retention", Key: "retention.audit_log_days", Value: "180", Type: "int", Label: "操作审计保留天数", Remark: "写操作留痕，删除后无法追溯；0 表示永久保留", Builtin: true},
		{Group: "retention", Key: "retention.kube_change_days", Value: "180", Type: "int", Label: "集群改动留痕保留天数", Remark: "对集群 apply / 改副本数的记录，含提交的 YAML 原文；0 表示永久保留", Builtin: true},
		{Group: "retention", Key: "retention.session_days", Value: "180", Type: "int", Label: "会话记录保留天数", Remark: "删除会话流水会连带命令明细与录像文件；0 表示永久保留", Builtin: true},
		{Group: "retention", Key: "retention.alert_days", Value: "180", Type: "int", Label: "已恢复告警保留天数", Remark: "只清理已恢复的告警，未恢复的不动；0 表示永久保留", Builtin: true},
		{Group: "retention", Key: "retention.model_call_days", Value: "365", Type: "int", Label: "模型调用流水保留天数", Remark: "AI 网关的用量与成本就从这张表算；删了就算不出那段时间的账。0 表示永久保留", Builtin: true},
		{Group: "retention", Key: "retention.agent_run_days", Value: "365", Type: "int", Label: "Agent 运行记录保留天数", Remark: "含模型给出的结论正文；删了结论就找不回来了。0 表示永久保留", Builtin: true},
		{Group: "retention", Key: "retention.db_query_days", Value: "180", Type: "int", Label: "数据库查询流水保留天数", Remark: "谁在哪个库跑过什么只读语句、被拦了哪些；能查库等于能看业务数据，建议与审计日志同档。0 表示永久保留", Builtin: true},
		{Group: "retention", Key: "retention.exec_guard_days", Value: "365", Type: "int", Label: "下发拦截流水保留天数", Remark: "被闸门拦下的下发尝试不会产生执行记录，这张表是唯一线索；建议比执行记录留得更久。0 表示永久保留", Builtin: true},
		{Group: "retention", Key: "retention.firewall_snapshot_days", Value: "180", Type: "int", Label: "防火墙快照保留天数", Remark: "每次下发前后各存一份真机规则原文，删了就没法回滚到那个时点；0 表示永久保留", Builtin: true},
		{Group: "security", Key: "firewall.sudo", Value: "true", Type: "bool", Label: "防火墙命令自动 sudo", Remark: "防火墙命令全都要 root。主机账号不是 root 时自动加 sudo -n，需要为该账号配置免密 sudo；关掉则原样执行", Builtin: true},
		{Group: "security", Key: "service.sudo", Value: "true", Type: "bool", Label: "服务启停自动 sudo", Remark: "systemctl 的读操作普通用户可以做，start/stop/enable 必须 root。主机账号不是 root 时自动加 sudo -n；关掉则原样执行", Builtin: true},
		{Group: "security", Key: "firewall.idle_days", Value: "30", Type: "int", Label: "防火墙规则闲置天数", Remark: "创建超过这么多天且从未命中的放行规则会进清理建议", Builtin: true},
		{Group: "monitor", Key: "monitor.sla_respond_critical", Value: "15", Type: "int", Label: "紧急事件响应目标(分钟)", Remark: "从建单算到「有人接手或写下第一条处置记录」。0 表示这一级不设 SLA", Builtin: true},
		{Group: "monitor", Key: "monitor.sla_respond_warning", Value: "60", Type: "int", Label: "警告事件响应目标(分钟)", Remark: "同上；指派不算响应，把单子转给别人不等于开始处理", Builtin: true},
		{Group: "monitor", Key: "monitor.sla_respond_info", Value: "480", Type: "int", Label: "提示事件响应目标(分钟)", Remark: "同上", Builtin: true},
		{Group: "monitor", Key: "monitor.sla_recover_critical", Value: "60", Type: "int", Label: "紧急事件恢复目标(分钟)", Remark: "从建单算到状态变成「已解决」；标记「已关闭」的事件不计 SLA。0 表示这一级不设", Builtin: true},
		{Group: "monitor", Key: "monitor.sla_recover_warning", Value: "240", Type: "int", Label: "警告事件恢复目标(分钟)", Remark: "同上", Builtin: true},
		{Group: "monitor", Key: "monitor.sla_recover_info", Value: "1440", Type: "int", Label: "提示事件恢复目标(分钟)", Remark: "同上", Builtin: true},
		{Group: "monitor", Key: "monitor.sla_remind_before", Value: "5", Type: "int", Label: "SLA 临期提前量(分钟)", Remark: "距超时不足这么多分钟标成「临期」并提醒一次。0 表示只在真超时后才提醒", Builtin: true},
		{Group: "monitor", Key: "monitor.sla_repeat_hours", Value: "4", Type: "int", Label: "SLA 重复提醒间隔(小时)", Remark: "同一个事件的同一条 SLA 在这段时间内不重复发消息，避免一个不处理的单子刷满消息中心", Builtin: true},
	}

	for i := range configs {
		cfg := configs[i]
		var exist model.SysConfig
		err := g.Where("`key` = ?", cfg.Key).First(&exist).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			if err := g.Create(&cfg).Error; err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		// 已存在时只补齐说明性字段，取值保持用户配置
		if err := g.Model(&exist).Updates(map[string]any{
			"group": cfg.Group, "type": cfg.Type, "label": cfg.Label,
			"remark": cfg.Remark, "builtin": true,
		}).Error; err != nil {
			return err
		}
	}
	return nil
}

// seedCommandRules 内置高危命令规则，仅在对应 ID 不存在时写入，用户改动不会被覆盖
func seedCommandRules(g *gorm.DB) error {
	rules := []model.CommandRule{
		{ID: 1, Pattern: `rm\s+(-[a-zA-Z]*[rf][a-zA-Z]*\s+)+/\s*$`, Description: "递归删除根目录", Action: "block", Enabled: true},
		{ID: 2, Pattern: `^\s*mkfs(\.\w+)?\s`, Description: "格式化文件系统", Action: "block", Enabled: true},
		{ID: 3, Pattern: `\bdd\s+.*of=/dev/(sd|nvme|vd|hd)`, Description: "裸写块设备", Action: "block", Enabled: true},
		{ID: 4, Pattern: `^\s*(shutdown|reboot|halt|poweroff)\b`, Description: "关机或重启主机", Action: "block", Enabled: true},
		{ID: 5, Pattern: `\bchmod\s+(-R\s+)?777\s+/\s*$`, Description: "根目录权限放开为 777", Action: "block", Enabled: true},
		{ID: 6, Pattern: `\b(iptables\s+-F|systemctl\s+(stop|disable)\s+(firewalld|sshd))\b`, Description: "关闭防火墙或 SSH 服务", Action: "warn", Enabled: true},
		{ID: 7, Pattern: `\b(drop\s+database|truncate\s+table)\b`, Description: "数据库删库或清表", Action: "warn", Enabled: true},
	}
	for i := range rules {
		r := rules[i]
		if err := g.Where("id = ?", r.ID).Attrs(r).FirstOrCreate(&model.CommandRule{}).Error; err != nil {
			return err
		}
	}
	return nil
}

// seedSignatures 内置特征库。
//
// 前 7 条刻意**直接挂在内置命令规则（id 1-7）上**：那 7 条规则本来就是「危险命令特征」，
// 让特征库一打开就是「已生效」的真实状态，而不是一张空表或一堆等着被应用的摆设。
// 后面几条是内置规则里没有的，默认停在 observe 且未应用 —— 由人决定要不要放进闸门。
//
// 只在 ID 不存在时写入：管理员改过的灰度与开关不会被启动覆盖。
func seedSignatures(g *gorm.DB) error {
	sigs := []model.Signature{
		// kind=command，已挂到内置命令规则
		{ID: 1, Name: "递归删除根目录", Kind: "command", Pattern: `rm\s+(-[a-zA-Z]*[rf][a-zA-Z]*\s+)+/\s*$`,
			Description: "rm -rf / 这类一条命令干掉整台机器的写法", Stage: "enforce", Severity: "high",
			Enabled: true, Builtin: true, RuleID: 1},
		{ID: 2, Name: "格式化文件系统", Kind: "command", Pattern: `^\s*mkfs(\.\w+)?\s`,
			Description: "mkfs 会把分区上的数据全部抹掉", Stage: "enforce", Severity: "high",
			Enabled: true, Builtin: true, RuleID: 2},
		{ID: 3, Name: "裸写块设备", Kind: "command", Pattern: `\bdd\s+.*of=/dev/(sd|nvme|vd|hd)`,
			Description: "dd of=/dev/sdX 绕过文件系统直接写盘", Stage: "enforce", Severity: "high",
			Enabled: true, Builtin: true, RuleID: 3},
		{ID: 4, Name: "关机或重启主机", Kind: "command", Pattern: `^\s*(shutdown|reboot|halt|poweroff)\b`,
			Description: "批量执行里最常见的误操作之一", Stage: "enforce", Severity: "high",
			Enabled: true, Builtin: true, RuleID: 4},
		{ID: 5, Name: "根目录权限放开为 777", Kind: "command", Pattern: `\bchmod\s+(-R\s+)?777\s+/\s*$`,
			Description: "chmod -R 777 / 之后 sshd 会拒绝登录，机器基本失联", Stage: "enforce", Severity: "high",
			Enabled: true, Builtin: true, RuleID: 5},
		{ID: 6, Name: "关闭防火墙或 SSH 服务", Kind: "command", Pattern: `\b(iptables\s+-F|systemctl\s+(stop|disable)\s+(firewalld|sshd))\b`,
			Description: "关掉之后往往连不回来；默认只提醒，确认过场景再升到 enforce", Stage: "observe", Severity: "medium",
			Enabled: true, Builtin: true, RuleID: 6},
		{ID: 7, Name: "数据库删库或清表", Kind: "command", Pattern: `\b(drop\s+database|truncate\s+table)\b`,
			Description: "命令行里直接删库；正常流程应当走数据库查询模块", Stage: "observe", Severity: "medium",
			Enabled: true, Builtin: true, RuleID: 7},

		// kind=command，内置规则里没有，默认未应用（RuleID=0）
		{ID: 8, Name: "下载脚本直接执行", Kind: "command", Pattern: `\b(curl|wget)\b[^|]*\|\s*(sudo\s+)?(ba)?sh\b`,
			Description: "curl ... | sh：执行的内容完全取决于对端那一刻返回什么，事后无法复现",
			Stage:       "observe", Severity: "high", Enabled: true, Builtin: true},
		{ID: 9, Name: "清空命令历史", Kind: "command", Pattern: `\b(history\s+-c|>\s*~?/?\.bash_history)\b`,
			Description: "清历史本身不破坏系统，但它是「掩盖痕迹」的典型动作，值得留痕",
			Stage:       "observe", Severity: "medium", Enabled: true, Builtin: true},
		{ID: 10, Name: "监听端口等待连接", Kind: "command", Pattern: `\bnc\s+(-[a-zA-Z]*l[a-zA-Z]*)\s`,
			Description: "nc -l 在主机上开一个监听口，常见于临时后门与数据外带",
			Stage:       "observe", Severity: "medium", Enabled: true, Builtin: true},

		// kind=port，配合暴露面扫描结果核对
		{ID: 11, Name: "管理端口对外开放", Kind: "port", Pattern: "22,3389,5900",
			Description: "SSH / RDP / VNC 直接暴露在公网是入口级风险",
			Stage:       "observe", Severity: "high", Enabled: true, Builtin: true},
		{ID: 12, Name: "数据库端口对外开放", Kind: "port", Pattern: "3306,5432,6379,9200,27017",
			Description: "MySQL / PG / Redis / ES / Mongo 暴露在公网，等于把数据摆在门口",
			Stage:       "observe", Severity: "high", Enabled: true, Builtin: true},
	}
	now := time.Now()
	for i := range sigs {
		s := sigs[i]
		if s.RuleID != 0 {
			s.AppliedAt = &now
			s.AppliedBy = "内置"
		}
		if err := g.Where("id = ?", s.ID).Attrs(s).FirstOrCreate(&model.Signature{}).Error; err != nil {
			return err
		}
	}
	return nil
}

// seedAwarenessDemo 一条示例培训项（草稿，不自动发布）。
//
// 刻意不发布：一发布就会给所有人推站内消息，那不该由「启动初始化」替管理员决定。
// 内容与题目都可以直接改，也可以整条删掉（还没有人留下记录时允许删除）。
func seedAwarenessDemo(g *gorm.DB) error {
	course := model.AwarenessCourse{
		ID:      1,
		Title:   "高危命令与生产变更须知（示例）",
		Summary: "示例培训项：读完确认 + 两道题。可直接改成贵司的版本，也可以整条删掉",
		Content: `一、下发前先想清楚「影响面」
- 批量执行选中的主机里有没有生产？平台会拦下来要你二次确认，那一步不是形式。
- 命令里带 rm -rf、mkfs、dd of=/dev/sdX、chmod 777 / 的，闸门会直接拒绝下发。

二、不要把口令写在命令行里
- 会话录像与命令审计会留下完整原文，口令会跟着一起留在记录里。
- 需要凭据时用「凭证库」，轮换一次所有引用主机下次连接生效。

三、拿不准就先预检
- 批量执行与脚本下发都有「预检」：把将要执行的命令原样摊开、把命中的规则列出来，再决定发不发。

四、出事之后
- 先在「消息中心 / 告警」里确认影响面，再动手回滚；防火墙与集群改动都有快照可回滚。`,
		Scope: "all", PassScore: 100,
	}
	if err := g.Where("id = ?", course.ID).Attrs(course).
		FirstOrCreate(&model.AwarenessCourse{}).Error; err != nil {
		return err
	}

	questions := []model.AwarenessQuestion{
		{ID: 1, CourseID: 1, Multi: false, Sort: 1,
			Content: "批量执行时目标里包含生产主机，平台的行为是？",
			Options: `["直接下发，事后在审计里留痕","要求显式确认后才下发","一律拒绝，生产只能上机操作","按主机标签随机决定"]`,
			Answer:  `[1]`,
			Explain: "下发闸门会把生产主机列出来并要求显式确认；确认这一步是后端校验的，绕过前端直接调接口同样过不去。"},
		{ID: 2, CourseID: 1, Multi: true, Sort: 2,
			Content: "以下哪些做法是对的？（多选）",
			Options: `["需要口令时从凭证库引用","把 root 口令写在批量执行的命令里","下发前先用预检看命中了哪些规则","用 curl 下载脚本直接管道给 sh 执行"]`,
			Answer:  `[0,2]`,
			Explain: "命令原文会进审计与录像，口令绝不能写在命令行里；curl | sh 执行的内容取决于对端那一刻返回什么，事后无法复现。"},
	}
	for i := range questions {
		q := questions[i]
		if err := g.Where("id = ?", q.ID).Attrs(q).
			FirstOrCreate(&model.AwarenessQuestion{}).Error; err != nil {
			return err
		}
	}
	return nil
}
