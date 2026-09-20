package main

import (
	"log"
	"net/http"
	"time"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"ops-platform/server/internal/config"
	"ops-platform/server/internal/db"
	"ops-platform/server/internal/handler"
	"ops-platform/server/internal/middleware"
	"ops-platform/server/internal/response"
	"ops-platform/server/internal/scheduler"
)

func main() {
	cfg := config.Load()

	gormDB, err := db.Open(cfg.DSN, cfg.Debug)
	if err != nil {
		log.Fatalf("数据库连接失败: %v", err)
	}
	if err := db.Migrate(gormDB); err != nil {
		log.Fatalf("数据库迁移失败: %v", err)
	}
	if err := db.Seed(gormDB, cfg.AdminInitPwd); err != nil {
		log.Fatalf("初始化数据失败: %v", err)
	}

	h := handler.New(gormDB, cfg)

	sched := scheduler.New(gormDB, h.ExecuteCronJob)
	h.Sched = sched
	// 证书巡检是代码内置的固定任务，不在定时任务列表里，只能通过配置开关
	if cfg.CertCheckSpec != "" {
		if err := sched.AddFixed(cfg.CertCheckSpec, h.CheckAllCertificatesForSchedule); err != nil {
			log.Fatalf("证书巡检 cron 表达式无效(%s): %v", cfg.CertCheckSpec, err)
		}
		log.Printf("[cert] 证书定时巡检已启用: %s", cfg.CertCheckSpec)
	} else {
		log.Println("[cert] 证书定时巡检未启用（OPS_CERT_CHECK_SPEC 为空），只能手动巡检")
	}
	if cfg.AlertRuleSpec != "" {
		if err := sched.AddFixed(cfg.AlertRuleSpec, h.EvaluateAlertRulesForSchedule); err != nil {
			log.Fatalf("告警规则 cron 表达式无效(%s): %v", cfg.AlertRuleSpec, err)
		}
		log.Printf("[rule] 告警规则定时评估已启用: %s", cfg.AlertRuleSpec)
	} else {
		log.Println("[rule] 告警规则定时评估未启用（OPS_ALERT_RULE_SPEC 为空），只能手动试跑")
	}
	if cfg.ProbeSpec != "" {
		if err := sched.AddFixed(cfg.ProbeSpec, h.RunProbesForSchedule); err != nil {
			log.Fatalf("拨测 cron 表达式无效(%s): %v", cfg.ProbeSpec, err)
		}
		log.Printf("[probe] 定时拨测已启用: %s", cfg.ProbeSpec)
	} else {
		log.Println("[probe] 定时拨测未启用（OPS_PROBE_SPEC 为空），只能手动拨测")
	}
	if cfg.RetentionSpec != "" {
		if err := sched.AddFixed(cfg.RetentionSpec, h.RunRetentionForSchedule); err != nil {
			log.Fatalf("数据留存清理 cron 表达式无效(%s): %v", cfg.RetentionSpec, err)
		}
		log.Printf("[retention] 定时清理已启用: %s", cfg.RetentionSpec)
	} else {
		log.Println("[retention] 定时清理未启用（OPS_RETENTION_SPEC 为空），只能手动清理")
	}
	if cfg.DetectionSpec != "" {
		if err := sched.AddFixed(cfg.DetectionSpec, h.EvaluateDetectionRulesForSchedule); err != nil {
			log.Fatalf("检测规则 cron 表达式无效(%s): %v", cfg.DetectionSpec, err)
		}
		log.Printf("[detection] 定时评估已启用: %s", cfg.DetectionSpec)
	} else {
		log.Println("[detection] 定时评估未启用（OPS_DETECTION_SPEC 为空），只能手动试跑")
	}
	if cfg.KubeCheckSpec != "" {
		if err := sched.AddFixed(cfg.KubeCheckSpec, h.CheckKubeClustersForSchedule); err != nil {
			log.Fatalf("容器集群检查 cron 表达式无效(%s): %v", cfg.KubeCheckSpec, err)
		}
		log.Printf("[kube] 定时检查已启用: %s", cfg.KubeCheckSpec)
	} else {
		log.Println("[kube] 定时检查未启用（OPS_KUBE_CHECK_SPEC 为空），只能手动检查")
	}
	if cfg.HostMetricSpec != "" {
		if err := sched.AddFixed(cfg.HostMetricSpec, h.CollectHostMetricsForSchedule); err != nil {
			log.Fatalf("主机指标采集 cron 表达式无效(%s): %v", cfg.HostMetricSpec, err)
		}
		log.Printf("[metric] 定时采集已启用: %s", cfg.HostMetricSpec)
	} else {
		log.Println("[metric] 定时采集未启用（OPS_HOST_METRIC_SPEC 为空），只能手动采集")
	}
	if cfg.OnCallSpec != "" {
		if err := sched.AddFixed(cfg.OnCallSpec, h.EscalateOnCallForSchedule); err != nil {
			log.Fatalf("值班升级 cron 表达式无效(%s): %v", cfg.OnCallSpec, err)
		}
		log.Printf("[oncall] 定时升级已启用: %s", cfg.OnCallSpec)
	} else {
		log.Println("[oncall] 定时升级未启用（OPS_ONCALL_SPEC 为空），只能手动试跑")
	}
	if err := sched.Start(); err != nil {
		log.Fatalf("定时任务加载失败: %v", err)
	}
	defer sched.Stop()

	// 启动时按配置清理过期录像
	h.CleanupRecordings()

	engine := buildRouter(h, cfg, gormDB)

	srv := &http.Server{

		Addr:              cfg.Addr,
		Handler:           engine,
		ReadHeaderTimeout: 10 * time.Second,
	}
	log.Printf("运维平台后端已启动: %s", cfg.Addr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("服务启动失败: %v", err)
	}
}

func buildRouter(h *handler.Handler, cfg *config.Config, gormDB *gorm.DB) *gin.Engine {
	if !cfg.Debug {
		gin.SetMode(gin.ReleaseMode)
	}
	r := gin.New()
	r.Use(gin.Logger(), gin.Recovery())
	r.Use(cors.New(cors.Config{
		AllowOrigins:     cfg.AllowOrigins,
		AllowMethods:     []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowHeaders:     []string{"Origin", "Content-Type", "Authorization"},
		AllowCredentials: true,
		MaxAge:           12 * time.Hour,
	}))

	r.GET("/healthz", func(c *gin.Context) { response.OK(c, gin.H{"status": "ok"}) })

	api := r.Group("/api/v1")
	api.POST("/auth/login", h.Login)
	// 登录页需要的品牌信息，无需鉴权
	api.GET("/public/branding", h.Branding)
	// 告警接入走 Token 鉴权，供外部监控系统直接 POST
	api.POST("/webhooks/alerts/:token", h.ReceiveAlert)

	auth := api.Group("")
	auth.Use(middleware.Auth(cfg.JWTSecret, gormDB), middleware.RequireTOTP(gormDB), middleware.Audit(gormDB))
	{
		auth.GET("/me", h.Profile)
		auth.GET("/me/menus", h.MyMenus)
		auth.PUT("/me/password", h.ChangePassword)

		auth.GET("/dashboard/stats", h.DashboardStats)

		auth.GET("/hosts", h.ListHosts)
		auth.POST("/hosts", middleware.RequirePerm("host:create"), h.CreateHost)
		auth.PUT("/hosts/:id", middleware.RequirePerm("host:update"), h.UpdateHost)
		auth.DELETE("/hosts/:id", middleware.RequirePerm("host:delete"), h.DeleteHost)
		auth.POST("/hosts/:id/check", middleware.RequirePerm("host:check"), h.CheckHost)
		auth.GET("/hosts/metrics", h.ListHostMetrics)
		auth.POST("/hosts/metrics/collect", middleware.RequirePerm("host:check"), h.CollectAllHostMetrics)
		auth.GET("/hosts/:id/metrics", h.HostMetricHistory)
		auth.POST("/hosts/:id/metrics/collect", middleware.RequirePerm("host:check"), h.CollectHostMetric)
		auth.GET("/hosts/:id/terminal", middleware.RequirePerm("terminal:connect"), h.Terminal)

		auth.GET("/hosts/:id/files", middleware.RequirePerm("file:read"), h.ListFiles)
		auth.GET("/hosts/:id/files/download", middleware.RequirePerm("file:read"), h.DownloadFile)
		auth.POST("/hosts/:id/files/upload", middleware.RequirePerm("file:write"), h.UploadFile)
		auth.POST("/hosts/:id/files/mkdir", middleware.RequirePerm("file:write"), h.MakeDir)
		auth.POST("/hosts/:id/files/rename", middleware.RequirePerm("file:write"), h.RenameFile)
		auth.DELETE("/hosts/:id/files", middleware.RequirePerm("file:delete"), h.DeleteFile)
		auth.GET("/file-audits", h.ListFileAudits)

		auth.GET("/scheduler/jobs", h.ListCronJobs)
		auth.POST("/scheduler/jobs", middleware.RequirePerm("cron:manage"), h.CreateCronJob)
		auth.PUT("/scheduler/jobs/:id", middleware.RequirePerm("cron:manage"), h.UpdateCronJob)
		auth.DELETE("/scheduler/jobs/:id", middleware.RequirePerm("cron:manage"), h.DeleteCronJob)
		auth.POST("/scheduler/jobs/:id/run", middleware.RequirePerm("cron:run"), h.RunCronJobNow)

		auth.GET("/exec/jobs", h.ListExecJobs)
		auth.GET("/exec/jobs/:id", h.GetExecJob)
		auth.POST("/exec/jobs", middleware.RequirePerm("exec:run"), h.RunExecJob)

		auth.GET("/sessions", h.ListSessions)
		auth.GET("/sessions/:id", h.GetSession)
		auth.GET("/sessions/:id/replay", middleware.RequirePerm("session:replay"), h.ReplaySession)

		auth.GET("/system/command-rules", h.ListCommandRules)
		auth.POST("/system/command-rules/test", h.TestCommandRule)
		auth.POST("/system/command-rules", middleware.RequirePerm("rule:manage"), h.CreateCommandRule)
		auth.PUT("/system/command-rules/:id", middleware.RequirePerm("rule:manage"), h.UpdateCommandRule)
		auth.DELETE("/system/command-rules/:id", middleware.RequirePerm("rule:manage"), h.DeleteCommandRule)

		auth.GET("/system/users", h.ListUsers)
		auth.POST("/system/users", middleware.RequirePerm("user:create"), h.CreateUser)
		auth.PUT("/system/users/:id", middleware.RequirePerm("user:update"), h.UpdateUser)
		auth.DELETE("/system/users/:id", middleware.RequirePerm("user:delete"), h.DeleteUser)

		auth.GET("/system/roles", h.ListRoles)
		auth.POST("/system/roles", middleware.RequirePerm("role:manage"), h.CreateRole)
		auth.PUT("/system/roles/:id", middleware.RequirePerm("role:manage"), h.UpdateRole)
		auth.DELETE("/system/roles/:id", middleware.RequirePerm("role:manage"), h.DeleteRole)

		auth.GET("/system/menus/tree", h.MenuTree)
		auth.POST("/system/menus", middleware.RequirePerm("menu:manage"), h.CreateMenu)
		auth.PUT("/system/menus/:id", middleware.RequirePerm("menu:manage"), h.UpdateMenu)
		auth.DELETE("/system/menus/:id", middleware.RequirePerm("menu:manage"), h.DeleteMenu)
		auth.GET("/system/audit-logs", h.ListAuditLogs)
		auth.GET("/system/audit-logs/operators", h.AuditOperators)
		auth.GET("/system/audit-logs/export", h.ExportAuditLogs)

		auth.GET("/site-links", h.ListSiteLinks)
		auth.POST("/site-links", middleware.RequirePerm("config:manage"), h.CreateSiteLink)
		auth.PUT("/site-links/:id", middleware.RequirePerm("config:manage"), h.UpdateSiteLink)
		auth.DELETE("/site-links/:id", middleware.RequirePerm("config:manage"), h.DeleteSiteLink)

		auth.GET("/system/email-templates", h.ListEmailTemplates)
		auth.POST("/system/email-templates", middleware.RequirePerm("config:manage"), h.CreateEmailTemplate)
		auth.PUT("/system/email-templates/:id", middleware.RequirePerm("config:manage"), h.UpdateEmailTemplate)
		auth.DELETE("/system/email-templates/:id", middleware.RequirePerm("config:manage"), h.DeleteEmailTemplate)
		auth.POST("/system/email-templates/:id/preview", h.PreviewEmailTemplate)

		auth.GET("/alerts", h.ListAlerts)
		auth.GET("/alerts/stats", h.AlertStats)
		auth.GET("/alerts/situation", h.AlertSituation)
		auth.GET("/alerts/:id", h.GetAlert)
		auth.POST("/alerts/:id/ack", middleware.RequirePerm("alert:handle"), h.AckAlert)
		auth.POST("/alerts/:id/resolve", middleware.RequirePerm("alert:handle"), h.ResolveAlert)

		auth.GET("/alert-sources", h.ListAlertSources)
		auth.POST("/alert-sources", middleware.RequirePerm("source:manage"), h.CreateAlertSource)
		auth.PUT("/alert-sources/:id", middleware.RequirePerm("source:manage"), h.UpdateAlertSource)
		auth.POST("/alert-sources/:id/rotate", middleware.RequirePerm("source:manage"), h.RotateAlertSourceToken)
		auth.DELETE("/alert-sources/:id", middleware.RequirePerm("source:manage"), h.DeleteAlertSource)

		auth.GET("/notify/channels", h.ListNotifyChannels)
		auth.POST("/notify/channels", middleware.RequirePerm("channel:manage"), h.CreateNotifyChannel)
		auth.PUT("/notify/channels/:id", middleware.RequirePerm("channel:manage"), h.UpdateNotifyChannel)
		auth.DELETE("/notify/channels/:id", middleware.RequirePerm("channel:manage"), h.DeleteNotifyChannel)
		auth.POST("/notify/channels/:id/test", middleware.RequirePerm("channel:manage"), h.TestNotifyChannel)

		auth.GET("/notify/routes", h.ListNotifyRoutes)
		auth.POST("/notify/routes", middleware.RequirePerm("route:manage"), h.CreateNotifyRoute)
		auth.PUT("/notify/routes/:id", middleware.RequirePerm("route:manage"), h.UpdateNotifyRoute)
		auth.DELETE("/notify/routes/:id", middleware.RequirePerm("route:manage"), h.DeleteNotifyRoute)
		auth.POST("/notify/routes/test", h.TestNotifyRoute)

		auth.GET("/notify/records", h.ListNotifyRecords)

		auth.GET("/system/announcements", h.ListAnnouncements)
		auth.POST("/system/announcements", middleware.RequirePerm("announcement:manage"), h.CreateAnnouncement)
		auth.PUT("/system/announcements/:id", middleware.RequirePerm("announcement:manage"), h.UpdateAnnouncement)
		auth.DELETE("/system/announcements/:id", middleware.RequirePerm("announcement:manage"), h.DeleteAnnouncement)
		auth.POST("/system/announcements/:id/publish", middleware.RequirePerm("announcement:manage"), h.PublishAnnouncement)
		auth.POST("/system/announcements/:id/unpublish", middleware.RequirePerm("announcement:manage"), h.UnpublishAnnouncement)

		auth.GET("/announcements/published", h.ListPublishedAnnouncements)

		auth.GET("/me/messages", h.ListMessages)
		auth.GET("/me/messages/summary", h.MessageSummary)
		auth.POST("/me/messages/:id/read", h.ReadMessage)
		auth.POST("/me/messages/read-all", h.ReadAllMessages)

		auth.GET("/system/companies", h.ListCompanies)
		auth.POST("/system/companies", middleware.RequirePerm("org:manage"), h.CreateCompany)
		auth.PUT("/system/companies/:id", middleware.RequirePerm("org:manage"), h.UpdateCompany)
		auth.DELETE("/system/companies/:id", middleware.RequirePerm("org:manage"), h.DeleteCompany)

		auth.GET("/system/departments/tree", h.DepartmentTree)
		auth.POST("/system/departments", middleware.RequirePerm("org:manage"), h.CreateDepartment)
		auth.PUT("/system/departments/:id", middleware.RequirePerm("org:manage"), h.UpdateDepartment)
		auth.DELETE("/system/departments/:id", middleware.RequirePerm("org:manage"), h.DeleteDepartment)

		auth.GET("/system/data-permission/diagnose/:id", h.DiagnoseDataScope)

		auth.GET("/resource-grants", h.ListResourceGrants)
		auth.POST("/resource-grants", middleware.RequirePerm("grant:manage"), h.CreateResourceGrant)
		auth.PUT("/resource-grants/:id", middleware.RequirePerm("grant:manage"), h.UpdateResourceGrant)
		auth.DELETE("/resource-grants/:id", middleware.RequirePerm("grant:manage"), h.DeleteResourceGrant)
		auth.GET("/resource-grants/diagnose/:id", h.DiagnoseResourceGrants)

		auth.GET("/system/configs", h.ListConfigs)
		auth.POST("/system/configs", middleware.RequirePerm("config:manage"), h.CreateConfig)
		auth.PUT("/system/configs", middleware.RequirePerm("config:manage"), h.UpdateConfigs)
		auth.DELETE("/system/configs/:id", middleware.RequirePerm("config:manage"), h.DeleteConfig)

		auth.GET("/me/workbench", h.PersonalWorkbench)
		auth.GET("/me/resources", h.MyResources)
		auth.GET("/me/activity", h.MyActivity)
		auth.GET("/me/sessions", h.MySessions)
		auth.GET("/me/exec-jobs", h.MyExecJobs)

		auth.GET("/tags", h.ListTags)
		auth.POST("/tags", middleware.RequirePerm("tag:manage"), h.CreateTag)
		auth.PUT("/tags/:id", middleware.RequirePerm("tag:manage"), h.UpdateTag)
		auth.DELETE("/tags/:id", middleware.RequirePerm("tag:manage"), h.DeleteTag)

		auth.GET("/databases", h.ListDBInstances)
		auth.POST("/databases", middleware.RequirePerm("db:manage"), h.CreateDBInstance)
		auth.PUT("/databases/:id", middleware.RequirePerm("db:manage"), h.UpdateDBInstance)
		auth.DELETE("/databases/:id", middleware.RequirePerm("db:manage"), h.DeleteDBInstance)
		auth.POST("/databases/:id/check", h.CheckDBInstance)

		auth.GET("/fixed-assets", h.ListFixedAssets)
		auth.GET("/fixed-assets/stats", h.FixedAssetStats)
		auth.POST("/fixed-assets", middleware.RequirePerm("asset:manage"), h.CreateFixedAsset)
		auth.PUT("/fixed-assets/:id", middleware.RequirePerm("asset:manage"), h.UpdateFixedAsset)
		auth.DELETE("/fixed-assets/:id", middleware.RequirePerm("asset:manage"), h.DeleteFixedAsset)

		auth.GET("/cloud-accounts", h.ListCloudAccounts)
		auth.POST("/cloud-accounts", middleware.RequirePerm("cloud:manage"), h.CreateCloudAccount)
		auth.PUT("/cloud-accounts/:id", middleware.RequirePerm("cloud:manage"), h.UpdateCloudAccount)
		auth.DELETE("/cloud-accounts/:id", middleware.RequirePerm("cloud:manage"), h.DeleteCloudAccount)

		auth.GET("/inventory/batches", h.ListInventoryBatches)
		auth.GET("/inventory/batches/:id", h.GetInventoryBatch)
		auth.POST("/inventory/batches", middleware.RequirePerm("inventory:manage"), h.CreateInventoryBatch)
		auth.POST("/inventory/batches/:id/finish", middleware.RequirePerm("inventory:manage"), h.FinishInventoryBatch)
		auth.DELETE("/inventory/batches/:id", middleware.RequirePerm("inventory:manage"), h.DeleteInventoryBatch)
		auth.PUT("/inventory/items/:id", middleware.RequirePerm("inventory:manage"), h.UpdateInventoryItem)

		auth.GET("/purchase/orders", h.ListPurchaseOrders)
		auth.GET("/purchase/orders/:id", h.GetPurchaseOrder)
		auth.POST("/purchase/orders", middleware.RequirePerm("purchase:manage"), h.CreatePurchaseOrder)
		auth.PUT("/purchase/orders/:id", middleware.RequirePerm("purchase:manage"), h.UpdatePurchaseOrder)
		auth.DELETE("/purchase/orders/:id", middleware.RequirePerm("purchase:manage"), h.DeletePurchaseOrder)
		auth.POST("/purchase/orders/:id/receive", middleware.RequirePerm("purchase:manage"), h.ReceivePurchaseOrder)

		auth.GET("/build/servers", h.ListBuildServers)
		auth.POST("/build/servers", middleware.RequirePerm("build:manage"), h.CreateBuildServer)
		auth.PUT("/build/servers/:id", middleware.RequirePerm("build:manage"), h.UpdateBuildServer)
		auth.DELETE("/build/servers/:id", middleware.RequirePerm("build:manage"), h.DeleteBuildServer)
		auth.POST("/build/servers/:id/test", middleware.RequirePerm("build:manage"), h.TestBuildServer)

		auth.GET("/build/jobs", h.ListBuildJobs)
		auth.POST("/build/jobs", middleware.RequirePerm("build:manage"), h.CreateBuildJob)
		auth.PUT("/build/jobs/:id", middleware.RequirePerm("build:manage"), h.UpdateBuildJob)
		auth.DELETE("/build/jobs/:id", middleware.RequirePerm("build:manage"), h.DeleteBuildJob)
		auth.POST("/build/jobs/:id/trigger", middleware.RequirePerm("build:run"), h.TriggerBuildJob)

		auth.GET("/build/records", h.ListBuildRecords)
		auth.POST("/build/records/:id/sync", middleware.RequirePerm("build:run"), h.SyncBuildRecord)

		auth.GET("/certificates", h.ListCertificates)
		auth.GET("/certificates/stats", h.CertificateStats)
		auth.POST("/certificates", middleware.RequirePerm("cert:manage"), h.CreateCertificate)
		auth.PUT("/certificates/:id", middleware.RequirePerm("cert:manage"), h.UpdateCertificate)
		auth.DELETE("/certificates/:id", middleware.RequirePerm("cert:manage"), h.DeleteCertificate)
		auth.POST("/certificates/:id/check", middleware.RequirePerm("cert:check"), h.CheckCertificate)
		auth.POST("/certificates/check-all", middleware.RequirePerm("cert:check"), h.CheckAllCertificates)

		auth.GET("/me/totp", h.GetMyTOTP)
		auth.POST("/me/totp/setup", h.SetupMyTOTP)
		auth.POST("/me/totp/confirm", h.ConfirmMyTOTP)
		auth.POST("/me/totp/disable", h.DisableMyTOTP)
		auth.POST("/system/users/:id/totp/reset", middleware.RequirePerm("totp:reset"), h.ResetUserTOTP)

		auth.GET("/monitor/alert-rules/metrics", h.ListAlertRuleMetrics)
		auth.GET("/monitor/alert-rules", h.ListAlertRules)
		auth.POST("/monitor/alert-rules", middleware.RequirePerm("alertrule:manage"), h.CreateAlertRule)
		auth.PUT("/monitor/alert-rules/:id", middleware.RequirePerm("alertrule:manage"), h.UpdateAlertRule)
		auth.DELETE("/monitor/alert-rules/:id", middleware.RequirePerm("alertrule:manage"), h.DeleteAlertRule)
		auth.POST("/monitor/alert-rules/:id/evaluate", middleware.RequirePerm("alertrule:manage"), h.EvaluateAlertRule)

		auth.GET("/monitor/detection-rules/meta", h.ListDetectionMeta)
		auth.GET("/monitor/detection-rules", h.ListDetectionRules)
		auth.POST("/monitor/detection-rules", middleware.RequirePerm("detection:manage"), h.CreateDetectionRule)
		auth.POST("/monitor/detection-rules/preview", middleware.RequirePerm("detection:manage"), h.PreviewDetection)
		auth.PUT("/monitor/detection-rules/:id", middleware.RequirePerm("detection:manage"), h.UpdateDetectionRule)
		auth.DELETE("/monitor/detection-rules/:id", middleware.RequirePerm("detection:manage"), h.DeleteDetectionRule)
		auth.POST("/monitor/detection-rules/:id/evaluate", middleware.RequirePerm("detection:manage"), h.EvaluateDetectionRule)

		auth.GET("/monitor/oncall/candidates", h.OnCallCandidates)
		auth.GET("/monitor/oncall/escalations", h.ListAlertEscalations)
		auth.GET("/monitor/oncall/schedules", h.ListOnCallSchedules)
		auth.POST("/monitor/oncall/schedules", middleware.RequirePerm("oncall:manage"), h.CreateOnCallSchedule)
		auth.PUT("/monitor/oncall/schedules/:id", middleware.RequirePerm("oncall:manage"), h.UpdateOnCallSchedule)
		auth.DELETE("/monitor/oncall/schedules/:id", middleware.RequirePerm("oncall:manage"), h.DeleteOnCallSchedule)
		auth.GET("/monitor/oncall/schedules/:id/preview", h.PreviewOnCall)
		auth.POST("/monitor/oncall/schedules/:id/run", middleware.RequirePerm("oncall:manage"), h.RunOnCallEscalation)
		auth.GET("/monitor/oncall/schedules/:id/overrides", h.ListOnCallOverrides)
		auth.POST("/monitor/oncall/schedules/:id/overrides", middleware.RequirePerm("oncall:manage"), h.CreateOnCallOverride)
		auth.DELETE("/monitor/oncall/schedules/:id/overrides/:overrideId", middleware.RequirePerm("oncall:manage"), h.DeleteOnCallOverride)

		auth.POST("/kube/contexts", middleware.RequirePerm("kube:manage"), h.KubeconfigContexts)
		auth.GET("/kube/clusters", h.ListKubeClusters)
		auth.POST("/kube/clusters", middleware.RequirePerm("kube:manage"), h.CreateKubeCluster)
		auth.PUT("/kube/clusters/:id", middleware.RequirePerm("kube:manage"), h.UpdateKubeCluster)
		auth.DELETE("/kube/clusters/:id", middleware.RequirePerm("kube:manage"), h.DeleteKubeCluster)
		auth.POST("/kube/clusters/:id/check", middleware.RequirePerm("kube:manage"), h.CheckKubeCluster)
		auth.GET("/kube/clusters/:id/nodes", h.KubeNodes)
		auth.GET("/kube/clusters/:id/namespaces", h.KubeNamespaces)
		auth.GET("/kube/clusters/:id/workloads", h.KubeWorkloads)
		auth.GET("/kube/clusters/:id/pods", h.KubePods)
		auth.GET("/kube/clusters/:id/events", h.KubeEvents)
		auth.GET("/kube/resource-kinds", h.KubeResourceKinds)
		auth.GET("/kube/clusters/:id/resources", h.KubeResources)
		auth.GET("/kube/clusters/:id/resource", h.KubeResourceDetail)
		auth.POST("/kube/clusters/:id/resource/apply", middleware.RequirePerm("kube:write"), h.ApplyKubeResource)
		auth.POST("/kube/clusters/:id/resource/scale", middleware.RequirePerm("kube:write"), h.ScaleKubeResource)
		auth.GET("/kube/change-logs", h.ListKubeChangeLogs)

		auth.GET("/monitor/health", h.PlatformHealth)

		auth.GET("/monitor/probes", h.ListProbes)
		auth.POST("/monitor/probes", middleware.RequirePerm("probe:manage"), h.CreateProbe)
		auth.PUT("/monitor/probes/:id", middleware.RequirePerm("probe:manage"), h.UpdateProbe)
		auth.DELETE("/monitor/probes/:id", middleware.RequirePerm("probe:manage"), h.DeleteProbe)
		auth.POST("/monitor/probes/:id/run", middleware.RequirePerm("probe:manage"), h.RunProbe)
		auth.GET("/monitor/probe-records", h.ListProbeRecords)

		auth.GET("/monitor/aggregation/dimensions", h.ListAggregationDimensions)
		auth.GET("/monitor/aggregation/overlaps", h.DetectAggregationOverlaps)
		auth.GET("/monitor/aggregation", h.ListAggregationPolicies)
		auth.POST("/monitor/aggregation", middleware.RequirePerm("aggregation:manage"), h.CreateAggregationPolicy)
		auth.PUT("/monitor/aggregation/:id", middleware.RequirePerm("aggregation:manage"), h.UpdateAggregationPolicy)
		auth.DELETE("/monitor/aggregation/:id", middleware.RequirePerm("aggregation:manage"), h.DeleteAggregationPolicy)
		auth.GET("/monitor/aggregation/:id/preview", h.PreviewAggregation)

		auth.GET("/monitor/topology/resources", h.TopologyResources)
		auth.GET("/monitor/topologies", h.ListTopologies)
		auth.GET("/monitor/topologies/:id", h.GetTopology)
		auth.POST("/monitor/topologies", middleware.RequirePerm("topology:manage"), h.CreateTopology)
		auth.PUT("/monitor/topologies/:id", middleware.RequirePerm("topology:manage"), h.UpdateTopology)
		auth.DELETE("/monitor/topologies/:id", middleware.RequirePerm("topology:manage"), h.DeleteTopology)
		auth.POST("/monitor/topologies/:id/nodes", middleware.RequirePerm("topology:manage"), h.CreateTopologyNode)
		auth.PUT("/monitor/topologies/:id/nodes/:nodeId", middleware.RequirePerm("topology:manage"), h.UpdateTopologyNode)
		auth.DELETE("/monitor/topologies/:id/nodes/:nodeId", middleware.RequirePerm("topology:manage"), h.DeleteTopologyNode)
		auth.POST("/monitor/topologies/:id/layout", middleware.RequirePerm("topology:manage"), h.SaveTopologyLayout)
		auth.POST("/monitor/topologies/:id/edges", middleware.RequirePerm("topology:manage"), h.CreateTopologyEdge)
		auth.DELETE("/monitor/topologies/:id/edges/:edgeId", middleware.RequirePerm("topology:manage"), h.DeleteTopologyEdge)

		auth.GET("/monitor/events", h.ListEvents)
		auth.GET("/monitor/events/stats", h.EventStats)
		auth.GET("/monitor/events/:id", h.GetEvent)
		auth.POST("/monitor/events", middleware.RequirePerm("event:manage"), h.CreateEvent)
		auth.POST("/monitor/events/from-bucket", middleware.RequirePerm("event:manage"), h.CreateEventFromBucket)
		auth.POST("/monitor/events/:id/assign", middleware.RequirePerm("event:manage"), h.AssignEvent)
		auth.POST("/monitor/events/:id/note", middleware.RequirePerm("event:manage"), h.AddEventNote)
		auth.POST("/monitor/events/:id/status", middleware.RequirePerm("event:manage"), h.UpdateEventStatus)
		auth.DELETE("/monitor/events/:id", middleware.RequirePerm("event:manage"), h.DeleteEvent)

		auth.GET("/exec/scripts", h.ListScripts)
		auth.GET("/exec/scripts/categories", h.ListScriptCategories)
		auth.POST("/exec/scripts", middleware.RequirePerm("script:manage"), h.CreateScript)
		auth.PUT("/exec/scripts/:id", middleware.RequirePerm("script:manage"), h.UpdateScript)
		auth.DELETE("/exec/scripts/:id", middleware.RequirePerm("script:manage"), h.DeleteScript)
		auth.POST("/exec/scripts/:id/precheck", h.PrecheckScript)
		auth.POST("/exec/scripts/:id/render", h.RenderScript)
		auth.POST("/exec/scripts/:id/run", middleware.RequirePerm("exec:run"), h.RunScript)

		auth.GET("/hosts/export", h.ExportHosts)
		auth.GET("/hosts/import-template", h.HostImportTemplate)
		auth.POST("/hosts/import", middleware.RequirePerm("host:create"), h.ImportHosts)

		auth.GET("/system/retention", h.RetentionStatus)
		auth.POST("/system/retention/run", middleware.RequirePerm("retention:run"), h.RunRetentionCleanup)
	}

	return r
}
