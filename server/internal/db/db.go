package db

import (
	"errors"
	"log"

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
	return g.AutoMigrate(
		&model.Company{}, &model.Department{},
		&model.User{}, &model.Role{}, &model.Menu{},
		&model.Host{}, &model.ExecJob{}, &model.ExecResult{}, &model.AuditLog{},
		&model.CommandRule{}, &model.Session{}, &model.SessionCommand{},
		&model.CronJob{}, &model.FileAudit{},
		&model.AlertSource{}, &model.Alert{}, &model.NotifyChannel{},
		&model.NotifyRoute{}, &model.NotifyRecord{},
		&model.Announcement{}, &model.Message{}, &model.SysConfig{},
		&model.Tag{}, &model.DBInstance{}, &model.FixedAsset{},
		&model.SiteLink{}, &model.EmailTemplate{}, &model.ResourceGrant{},
		&model.CloudAccount{}, &model.InventoryBatch{}, &model.InventoryItem{},
		&model.PurchaseOrder{}, &model.PurchaseItem{},
		&model.BuildServer{}, &model.BuildJob{}, &model.BuildRecord{},
		&model.Certificate{}, &model.AlertRule{},
		&model.Probe{}, &model.ProbeRecord{},
		&model.AggregationPolicy{},
		&model.Event{}, &model.EventLog{},
		&model.Script{},
		&model.Topology{}, &model.TopologyNode{}, &model.TopologyEdge{},
		&model.DetectionRule{},
		&model.KubeCluster{}, &model.KubeChangeLog{}, &model.KubeForward{},
		&model.HostMetric{},
		&model.OnCallSchedule{}, &model.OnCallOverride{}, &model.AlertEscalation{},
		&model.ExposureTarget{}, &model.ExposureScan{},
		&model.MetricSource{}, &model.SavedMetricQuery{}, &model.LogSource{}, &model.TraceSource{},
		&model.ModelUpstream{}, &model.ModelCall{}, &model.AgentConfig{}, &model.AgentRun{},
	)
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

		// ---------- 运维执行 ----------
		{ID: 200, Name: "Execute", Title: "运维执行", Path: "/execute", Icon: "Promotion", Sort: 30},
		{ID: 201, ParentID: 200, Name: "WebTerminal", Title: "Web 终端", Path: "/execute/terminal", Component: "/execute/terminal/index", Icon: "Platform", Sort: 1},
		{ID: 202, ParentID: 201, Title: "登录终端", Type: "button", AuthCode: "terminal:connect", Sort: 1},
		{ID: 203, ParentID: 200, Name: "BatchExec", Title: "批量执行", Path: "/execute/batch", Component: "/execute/batch/index", Icon: "Cpu", Sort: 2},
		{ID: 204, ParentID: 203, Title: "下发命令", Type: "button", AuthCode: "exec:run", Sort: 1},
		{ID: 206, ParentID: 200, Name: "SessionAudit", Title: "会话审计", Path: "/execute/session", Component: "/execute/session/index", Icon: "VideoCamera", Sort: 3},
		{ID: 207, ParentID: 206, Title: "回放会话", Type: "button", AuthCode: "session:replay", Sort: 1},
		{ID: 210, ParentID: 200, Name: "FileManager", Title: "文件管理", Path: "/execute/file", Component: "/execute/file/index", Icon: "Folder", Sort: 4},
		{ID: 213, ParentID: 210, Title: "上传与新建", Type: "button", AuthCode: "file:write", Sort: 1},
		{ID: 214, ParentID: 210, Title: "浏览与下载", Type: "button", AuthCode: "file:read", Sort: 2},
		{ID: 215, ParentID: 210, Title: "删除文件", Type: "button", AuthCode: "file:delete", Sort: 3},
		{ID: 211, ParentID: 200, Name: "Scheduler", Title: "定时任务", Path: "/execute/scheduler", Component: "/execute/scheduler/index", Icon: "Timer", Sort: 5},
		{ID: 216, ParentID: 211, Title: "维护任务", Type: "button", AuthCode: "cron:manage", Sort: 1},
		{ID: 217, ParentID: 211, Title: "立即执行", Type: "button", AuthCode: "cron:run", Sort: 2},
		{ID: 212, ParentID: 200, Name: "BuildDeploy", Title: "构建发布", Path: "/execute/build", Component: "/execute/build/index", Icon: "SetUp", Sort: 6},
		{ID: 218, ParentID: 212, Title: "维护服务器与任务", Type: "button", AuthCode: "build:manage", Sort: 1},
		{ID: 219, ParentID: 212, Title: "触发构建与同步", Type: "button", AuthCode: "build:run", Sort: 2},
		{ID: 205, ParentID: 200, Name: "ScriptLibrary", Title: "脚本库", Path: "/execute/script", Component: "/execute/script/index", Icon: "Notebook", Sort: 7},
		{ID: 220, ParentID: 205, Title: "维护脚本", Type: "button", AuthCode: "script:manage", Sort: 1},

		// ---------- 容器平台 ----------
		{ID: 300, Name: "Kubernetes", Title: "容器平台", Path: "/kubernetes", Icon: "Ship", Sort: 40},
		{ID: 301, ParentID: 300, Name: "K8sSource", Title: "集群接入", Path: "/kubernetes/source", Component: "/kubernetes/source/index", Icon: "Connection", Sort: 1},
		{ID: 305, ParentID: 301, Title: "维护集群", Type: "button", AuthCode: "kube:manage", Sort: 1},
		{ID: 302, ParentID: 300, Name: "K8sWorkload", Title: "工作负载", Path: "/kubernetes/workload", Component: "/kubernetes/workload/index", Icon: "Grid", Sort: 2},
		{ID: 303, ParentID: 300, Name: "K8sResource", Title: "资源管理", Path: "/kubernetes/resource", Component: "/kubernetes/resource/index", Icon: "Files", Sort: 3},
		{ID: 306, ParentID: 303, Title: "改动集群资源", Type: "button", AuthCode: "kube:write", Sort: 1},
		{ID: 304, ParentID: 300, Name: "K8sForward", Title: "服务转发", Path: "/kubernetes/forward", Component: "/kubernetes/forward/index", Icon: "Share", Sort: 4},
		{ID: 307, ParentID: 304, Title: "开关转发隧道", Type: "button", AuthCode: "kube:forward", Sort: 1},

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

		// ---------- 安全合规 ----------
		{ID: 500, Name: "Security", Title: "安全合规", Path: "/security", Icon: "Key", Sort: 60},
		{ID: 501, ParentID: 500, Name: "SSLCertificate", Title: "证书管理", Path: "/security/ssl", Component: "/security/ssl/index", Icon: "Stamp", Sort: 1},
		{ID: 506, ParentID: 501, Title: "维护证书", Type: "button", AuthCode: "cert:manage", Sort: 1},
		{ID: 507, ParentID: 501, Title: "执行巡检", Type: "button", AuthCode: "cert:check", Sort: 2},
		{ID: 502, ParentID: 500, Name: "Firewall", Title: "防火墙策略", Path: "/security/firewall", Component: todo, Icon: "Lock", Sort: 2},
		{ID: 503, ParentID: 500, Name: "TwoFA", Title: "双因子口令", Path: "/security/twofa", Component: "/security/twofa/index", Icon: "Key", Sort: 3},
		{ID: 508, ParentID: 503, Title: "重置他人绑定", Type: "button", AuthCode: "totp:reset", Sort: 1},
		{ID: 504, ParentID: 500, Name: "SecurityAwareness", Title: "安全意识", Path: "/security/awareness", Component: todo, Icon: "Reading", Sort: 4},
		{ID: 505, ParentID: 500, Name: "FeatureLibrary", Title: "特征库", Path: "/security/features", Component: todo, Icon: "Collection", Sort: 5},

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
		{ID: 815, ParentID: 800, Name: "ImIntegration", Title: "IM 集成", Path: "/system/im", Component: todo, Icon: "ChatDotRound", Sort: 11},
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
	if err := seedEmailTemplates(g); err != nil {
		return err
	}
	return seedSysConfigs(g)
}

// seedEmailTemplates 内置告警邮件模板，只在编码不存在时写入
func seedEmailTemplates(g *gorm.DB) error {
	tpl := model.EmailTemplate{
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
	}

	var exist model.EmailTemplate
	err := g.Where("code = ?", tpl.Code).First(&exist).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return g.Create(&tpl).Error
	}
	return err
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
		{Group: "smtp", Key: "smtp.password", Value: "", Type: "string", Label: "SMTP 密码", Remark: "明文存储，与主机凭据同等对待", Builtin: true},
		{Group: "smtp", Key: "smtp.from", Value: "", Type: "string", Label: "发件人地址", Remark: "留空则用 SMTP 账号", Builtin: true},
		{Group: "smtp", Key: "smtp.tls", Value: "true", Type: "bool", Label: "使用 TLS 直连", Remark: "465 端口通常需要开启", Builtin: true},
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
