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
	auth.Use(middleware.Auth(cfg.JWTSecret, gormDB), middleware.Audit(gormDB))
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
	}

	return r
}
