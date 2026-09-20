package db

import (
	"errors"
	"log"

	"github.com/glebarez/sqlite"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
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
		&model.User{}, &model.Role{}, &model.Menu{},
		&model.Host{}, &model.ExecJob{}, &model.ExecResult{}, &model.AuditLog{},
		&model.CommandRule{}, &model.Session{}, &model.SessionCommand{},
		&model.CronJob{}, &model.FileAudit{},
		&model.AlertSource{}, &model.Alert{}, &model.NotifyChannel{},
		&model.NotifyRoute{}, &model.NotifyRecord{},
		&model.Announcement{}, &model.Message{},
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
		{ID: 12, ParentID: 10, Name: "DashboardPersonal", Title: "个人工作台", Path: "/dashboard/personal", Component: todo, Icon: "User", Sort: 2},
		{ID: 13, ParentID: 10, Name: "MyResources", Title: "我的资源", Path: "/dashboard/my-resources", Component: todo, Icon: "Files", Sort: 3},
		{ID: 14, ParentID: 10, Name: "MyActivity", Title: "我的活动", Path: "/dashboard/my-activity", Component: todo, Icon: "Notebook", Sort: 4},

		// ---------- 资产管理 ----------
		{ID: 100, Name: "Asset", Title: "资产管理", Path: "/asset", Icon: "Coin", Sort: 20},
		{ID: 101, ParentID: 100, Name: "HostList", Title: "主机资产", Path: "/asset/host", Component: "/asset/host/index", Icon: "Monitor", Sort: 1},
		{ID: 102, ParentID: 101, Title: "新增主机", Type: "button", AuthCode: "host:create", Sort: 1},
		{ID: 103, ParentID: 101, Title: "编辑主机", Type: "button", AuthCode: "host:update", Sort: 2},
		{ID: 104, ParentID: 101, Title: "删除主机", Type: "button", AuthCode: "host:delete", Sort: 3},
		{ID: 105, ParentID: 101, Title: "连通性探测", Type: "button", AuthCode: "host:check", Sort: 4},
		{ID: 110, ParentID: 100, Name: "DatabaseAsset", Title: "数据库资产", Path: "/asset/database", Component: todo, Icon: "Coin", Sort: 2},
		{ID: 111, ParentID: 100, Name: "CloudAccount", Title: "云账号", Path: "/asset/cloud", Component: todo, Icon: "Cloudy", Sort: 3},
		{ID: 112, ParentID: 100, Name: "AssetTag", Title: "标签管理", Path: "/asset/tag", Component: todo, Icon: "PriceTag", Sort: 4},
		{ID: 113, ParentID: 100, Name: "FixedAsset", Title: "固定资产", Path: "/asset/fixed", Component: todo, Icon: "Box", Sort: 5},
		{ID: 114, ParentID: 100, Name: "AssetInventory", Title: "资产盘点", Path: "/asset/inventory", Component: todo, Icon: "Tickets", Sort: 6},
		{ID: 115, ParentID: 100, Name: "AssetPurchase", Title: "采购记录", Path: "/asset/purchase", Component: todo, Icon: "ShoppingCart", Sort: 7},

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
		{ID: 212, ParentID: 200, Name: "BuildDeploy", Title: "构建发布", Path: "/execute/build", Component: todo, Icon: "SetUp", Sort: 6},

		// ---------- 容器平台 ----------
		{ID: 300, Name: "Kubernetes", Title: "容器平台", Path: "/kubernetes", Icon: "Ship", Sort: 40},
		{ID: 301, ParentID: 300, Name: "K8sSource", Title: "集群接入", Path: "/kubernetes/source", Component: todo, Icon: "Connection", Sort: 1},
		{ID: 302, ParentID: 300, Name: "K8sWorkload", Title: "工作负载", Path: "/kubernetes/workload", Component: todo, Icon: "Grid", Sort: 2},
		{ID: 303, ParentID: 300, Name: "K8sResource", Title: "资源管理", Path: "/kubernetes/resource", Component: todo, Icon: "Files", Sort: 3},
		{ID: 304, ParentID: 300, Name: "K8sForward", Title: "服务转发", Path: "/kubernetes/forward", Component: todo, Icon: "Share", Sort: 4},

		// ---------- 监控告警 ----------
		{ID: 400, Name: "Monitor", Title: "监控告警", Path: "/monitor", Icon: "TrendCharts", Sort: 50},
		{ID: 401, ParentID: 400, Name: "AlertSituation", Title: "告警态势", Path: "/monitor/situation", Component: "/monitor/situation/index", Icon: "DataAnalysis", Sort: 1},
		{ID: 402, ParentID: 400, Name: "AlertList", Title: "告警列表", Path: "/monitor/alerts", Component: "/monitor/alerts/index", Icon: "Bell", Sort: 2},
		{ID: 420, ParentID: 402, Title: "确认与恢复", Type: "button", AuthCode: "alert:handle", Sort: 1},
		{ID: 403, ParentID: 400, Name: "AlertRule", Title: "告警规则", Path: "/monitor/alert-rules", Component: todo, Icon: "Tickets", Sort: 3},
		{ID: 404, ParentID: 400, Name: "DetectionRule", Title: "检测规则", Path: "/monitor/detection-rules", Component: todo, Icon: "Aim", Sort: 4},
		{ID: 405, ParentID: 400, Name: "NotifyRouting", Title: "通知路由", Path: "/monitor/routing", Component: "/monitor/routing/index", Icon: "Guide", Sort: 5},
		{ID: 421, ParentID: 405, Title: "维护路由", Type: "button", AuthCode: "route:manage", Sort: 1},
		{ID: 406, ParentID: 400, Name: "AggregationPolicy", Title: "聚合策略", Path: "/monitor/aggregation", Component: todo, Icon: "Operation", Sort: 6},
		{ID: 407, ParentID: 400, Name: "EventCenter", Title: "事件中心", Path: "/monitor/events", Component: todo, Icon: "Warning", Sort: 7},
		{ID: 408, ParentID: 400, Name: "MetricQuery", Title: "指标查询", Path: "/monitor/metrics", Component: todo, Icon: "Histogram", Sort: 8},
		{ID: 409, ParentID: 400, Name: "LogQuery", Title: "日志查询", Path: "/monitor/logs", Component: todo, Icon: "Document", Sort: 9},
		{ID: 410, ParentID: 400, Name: "TraceQuery", Title: "链路追踪", Path: "/monitor/traces", Component: todo, Icon: "Share", Sort: 10},
		{ID: 411, ParentID: 400, Name: "Probe", Title: "拨测探测", Path: "/monitor/probe", Component: todo, Icon: "Position", Sort: 11},
		{ID: 412, ParentID: 400, Name: "Topology", Title: "业务拓扑", Path: "/monitor/topology", Component: todo, Icon: "Connection", Sort: 12},
		{ID: 413, ParentID: 400, Name: "PlatformHealth", Title: "平台健康", Path: "/monitor/health", Component: todo, Icon: "FirstAidKit", Sort: 13},
		{ID: 414, ParentID: 400, Name: "PublicIPMonitor", Title: "公网监测", Path: "/monitor/public-ip", Component: todo, Icon: "Compass", Sort: 14},

		// ---------- 安全合规 ----------
		{ID: 500, Name: "Security", Title: "安全合规", Path: "/security", Icon: "Key", Sort: 60},
		{ID: 501, ParentID: 500, Name: "SSLCertificate", Title: "证书管理", Path: "/security/ssl", Component: todo, Icon: "Stamp", Sort: 1},
		{ID: 502, ParentID: 500, Name: "Firewall", Title: "防火墙策略", Path: "/security/firewall", Component: todo, Icon: "Lock", Sort: 2},
		{ID: 503, ParentID: 500, Name: "TwoFA", Title: "双因子口令", Path: "/security/twofa", Component: todo, Icon: "Key", Sort: 3},
		{ID: 504, ParentID: 500, Name: "SecurityAwareness", Title: "安全意识", Path: "/security/awareness", Component: todo, Icon: "Reading", Sort: 4},
		{ID: 505, ParentID: 500, Name: "FeatureLibrary", Title: "特征库", Path: "/security/features", Component: todo, Icon: "Collection", Sort: 5},

		// ---------- 智能与成本 ----------
		{ID: 600, Name: "Intelligence", Title: "智能与成本", Path: "/ai", Icon: "MagicStick", Sort: 70},
		{ID: 601, ParentID: 600, Name: "ModelPool", Title: "模型资源池", Path: "/ai/model-pool", Component: todo, Icon: "Cpu", Sort: 1},
		{ID: 602, ParentID: 600, Name: "ModelUsage", Title: "用量与成本", Path: "/ai/model-usage", Component: todo, Icon: "Money", Sort: 2},
		{ID: 603, ParentID: 600, Name: "AgentRuns", Title: "Agent 运行", Path: "/ai/agent-runs", Component: todo, Icon: "Cpu", Sort: 3},
		{ID: 604, ParentID: 600, Name: "AgentConfig", Title: "Agent 配置", Path: "/ai/agent-config", Component: todo, Icon: "SetUp", Sort: 4},

		// ---------- 配置中心 ----------
		{ID: 700, Name: "ConfigCenter", Title: "配置中心", Path: "/config", Icon: "Tools", Sort: 80},
		{ID: 701, ParentID: 700, Name: "ConfigItem", Title: "配置项", Path: "/config/items", Component: todo, Icon: "Files", Sort: 1},
		{ID: 702, ParentID: 700, Name: "WebhookInbound", Title: "Webhook 接入", Path: "/config/webhooks", Component: "/config/webhooks/index", Icon: "Link", Sort: 2},
		{ID: 705, ParentID: 702, Title: "维护接入源", Type: "button", AuthCode: "source:manage", Sort: 1},
		{ID: 703, ParentID: 700, Name: "SiteNavigation", Title: "站点导航", Path: "/config/site-navigation", Component: todo, Icon: "Compass", Sort: 3},
		{ID: 704, ParentID: 700, Name: "EmailTemplate", Title: "邮件模板", Path: "/config/email-templates", Component: todo, Icon: "Message", Sort: 4},

		// ---------- 系统管理 ----------
		{ID: 800, Name: "System", Title: "系统管理", Path: "/system", Icon: "Setting", Sort: 90},
		{ID: 801, ParentID: 800, Name: "SysUser", Title: "用户管理", Path: "/system/user", Component: "/system/user/index", Icon: "User", Sort: 1},
		{ID: 802, ParentID: 801, Title: "新增用户", Type: "button", AuthCode: "user:create", Sort: 1},
		{ID: 803, ParentID: 801, Title: "编辑用户", Type: "button", AuthCode: "user:update", Sort: 2},
		{ID: 804, ParentID: 801, Title: "删除用户", Type: "button", AuthCode: "user:delete", Sort: 3},
		{ID: 805, ParentID: 800, Name: "SysRole", Title: "角色权限", Path: "/system/role", Component: "/system/role/index", Icon: "Lock", Sort: 2},
		{ID: 806, ParentID: 805, Title: "维护角色", Type: "button", AuthCode: "role:manage", Sort: 1},
		{ID: 807, ParentID: 800, Name: "SysDepartment", Title: "部门管理", Path: "/system/department", Component: todo, Icon: "OfficeBuilding", Sort: 3},
		{ID: 808, ParentID: 800, Name: "SysCompany", Title: "公司管理", Path: "/system/company", Component: todo, Icon: "OfficeBuilding", Sort: 4},
		{ID: 809, ParentID: 800, Name: "SysMenu", Title: "菜单管理", Path: "/system/menu", Component: todo, Icon: "Menu", Sort: 5},
		{ID: 810, ParentID: 800, Name: "DataPermission", Title: "数据权限", Path: "/system/data-permission", Component: todo, Icon: "Filter", Sort: 6},
		{ID: 811, ParentID: 800, Name: "ResourceGrant", Title: "资源授权", Path: "/system/resource-grant", Component: todo, Icon: "Unlock", Sort: 7},
		{ID: 812, ParentID: 800, Name: "NotifyChannel", Title: "通知渠道", Path: "/system/notify-channel", Component: "/system/notify-channel/index", Icon: "Message", Sort: 8},
		{ID: 823, ParentID: 812, Title: "维护渠道", Type: "button", AuthCode: "channel:manage", Sort: 1},
		{ID: 813, ParentID: 800, Name: "NotifyRecord", Title: "通知记录", Path: "/system/notify-record", Component: "/system/notify-record/index", Icon: "MessageBox", Sort: 9},
		{ID: 814, ParentID: 800, Name: "Announcement", Title: "公告管理", Path: "/system/announcement", Component: "/system/announcement/index", Icon: "Bell", Sort: 10},
		{ID: 824, ParentID: 814, Title: "维护与发布", Type: "button", AuthCode: "announcement:manage", Sort: 1},
		{ID: 815, ParentID: 800, Name: "ImIntegration", Title: "IM 集成", Path: "/system/im", Component: todo, Icon: "ChatDotRound", Sort: 11},
		{ID: 816, ParentID: 800, Name: "SysConfig", Title: "系统配置", Path: "/system/config", Component: todo, Icon: "Tools", Sort: 12},
		{ID: 820, ParentID: 800, Name: "CommandRule", Title: "命令规则", Path: "/system/command-rule", Component: "/system/command-rule/index", Icon: "WarningFilled", Sort: 13},
		{ID: 821, ParentID: 820, Title: "维护规则", Type: "button", AuthCode: "rule:manage", Sort: 1},
		{ID: 822, ParentID: 800, Name: "AuditLog", Title: "操作审计", Path: "/system/audit", Component: "/system/audit/index", Icon: "Document", Sort: 14},

		// ---------- 消息中心 ----------
		{ID: 900, Name: "MessageCenter", Title: "消息中心", Path: "/message", Icon: "Message", Sort: 100},
		{ID: 901, ParentID: 900, Name: "MessageInbox", Title: "我的消息", Path: "/message/inbox", Component: "/message/inbox/index", Icon: "MessageBox", Sort: 1},
		{ID: 902, ParentID: 900, Name: "AnnouncementFeed", Title: "公告中心", Path: "/message/announcement", Component: "/message/announcement/index", Icon: "Bell", Sort: 2},
	}
	// 菜单目前由种子数据统一维护（菜单管理模块尚未实现），因此以代码为准做全量覆盖：
	// 按 ID upsert，并清掉不在清单里的历史菜单，避免版本升级后残留旧入口
	if err := g.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "id"}},
		UpdateAll: true,
	}).Create(&menus).Error; err != nil {
		return err
	}

	keepIDs := make([]uint, 0, len(menus))
	for i := range menus {
		keepIDs = append(keepIDs, menus[i].ID)
	}
	if err := g.Where("id NOT IN ?", keepIDs).Delete(&model.Menu{}).Error; err != nil {
		return err
	}
	// 菜单删除后清理悬空授权
	if err := g.Exec("DELETE FROM role_menus WHERE menu_id NOT IN (SELECT id FROM menus)").Error; err != nil {
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

	return seedCommandRules(g)
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
