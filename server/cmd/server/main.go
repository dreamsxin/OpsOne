package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"ops-platform/server/internal/backup"
	"ops-platform/server/internal/config"
	"ops-platform/server/internal/db"
	"ops-platform/server/internal/handler"
	"ops-platform/server/internal/instance"
	"ops-platform/server/internal/logx"
	"ops-platform/server/internal/metrics"
	"ops-platform/server/internal/middleware"
	"ops-platform/server/internal/migrate"
	"ops-platform/server/internal/model"
	"ops-platform/server/internal/response"
	"ops-platform/server/internal/scheduler"
)

// version 构建版本，由 -ldflags "-X main.version=..." 注入；没注入就是 dev
var version = "dev"

func main() {
	// 子命令：备份与恢复要能不起服务单独跑（定时任务、故障恢复都需要）
	if len(os.Args) > 1 && !strings.HasPrefix(os.Args[1], "-") {
		switch os.Args[1] {
		case "version":
			fmt.Printf("opsone %s\n", version)
			return
		case "backup":
			runBackupCmd(os.Args[2:])
			return
		case "restore":
			runRestoreCmd(os.Args[2:])
			return
		case "migrate":
			runMigrateCmd(os.Args[2:])
			return
		case "serve":
			// 显式写 serve 与不带子命令等价
		default:
			log.Fatalf("未知子命令 %q，可用: serve / backup / restore / migrate / version", os.Args[1])
		}
	}

	cfg := mustLoadConfig()

	// 日志输出尽早接过来：这之前的几行（配置校验的 warn）只写 stderr，
	// 那是可以接受的 —— 反过来「为了让几行 warn 进文件而在配置还没校验完时就建文件」
	// 才是本末倒置
	logWriter, err := logx.New(logx.Options{
		File: cfg.LogFile, JSON: cfg.LogJSON,
		MaxMB: cfg.LogMaxMB, Keep: cfg.LogKeep, AlsoStderr: cfg.LogStderr,
	})
	if err != nil {
		log.Fatalf("日志初始化失败: %v", err)
	}
	defer func() { _ = logWriter.Close() }()
	log.SetOutput(logWriter)
	// gin 的访问日志与它内部的错误输出也走同一个 writer，否则落文件时会漏掉一半
	gin.DefaultWriter = logWriter
	gin.DefaultErrorWriter = logWriter

	log.Printf("opsone %s 启动中，运行模式 %s", version, cfg.Env)
	log.Printf("[logx] 日志去向：%s", logWriter.Describe())

	gormDB, err := db.Open(cfg.DSN, cfg.Debug, logWriter)
	if err != nil {
		log.Fatalf("数据库连接失败: %v", err)
	}
	if err := db.Migrate(gormDB, version); err != nil {
		log.Fatalf("数据库迁移失败: %v", err)
	}
	if err := db.Seed(gormDB, cfg.AdminInitPwd); err != nil {
		log.Fatalf("初始化数据失败: %v", err)
	}

	// 实例登记必须在任何「假设自己是唯一实例」的收尾动作之前：
	// ResetForwards / FinishShutdown 会把库里「运行中」的隧道与会话标成已结束，
	// 而那些可能正属于另一个活着的实例
	inst, err := instance.Claim(gormDB, instance.Options{
		Addr: cfg.Addr, Version: version,
		AllowMulti: cfg.AllowMultiInstance, LeaseSeconds: cfg.InstanceLeaseSec,
	})
	if err != nil {
		log.Fatalf("实例登记失败: %v", err)
	}

	h := handler.New(gormDB, cfg)
	h.Instance = inst
	// 库里有密文但进程没密钥时提前喊一声，别等到有人点「连接主机」才发现
	h.WarnSealedWithoutKey()
	// 内置剧本是 db 层种的，那里拿不到命令规则预检；进程起来补一次
	h.PrecheckPendingRunbooks()
	if inst.SoleInstance() {
		// 上一轮进程的转发隧道已经随进程消失，档案里别继续写「运行中」
		h.ResetForwards()
		// 同理：上一轮没来得及收尾的会话不该一直显示「进行中」
		h.FinishShutdown("平台重启，会话已随进程结束")
	} else {
		log.Printf("[instance] 还有别的实例活着，跳过「把运行中的隧道与会话标成已结束」——" +
			"那些记录可能正属于对方")
	}

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
	if cfg.ExposureSpec != "" {
		if err := sched.AddFixed(cfg.ExposureSpec, h.ScanExposuresForSchedule); err != nil {
			log.Fatalf("暴露面扫描 cron 表达式无效(%s): %v", cfg.ExposureSpec, err)
		}
		log.Printf("[exposure] 定时扫描已启用: %s", cfg.ExposureSpec)
	} else {
		log.Println("[exposure] 定时扫描未启用（OPS_EXPOSURE_SPEC 为空），只能手动扫描")
	}
	if cfg.AwarenessSpec != "" {
		if err := sched.AddFixed(cfg.AwarenessSpec, h.RemindOverdueAwareness); err != nil {
			log.Fatalf("安全意识逾期提醒 cron 表达式无效(%s): %v", cfg.AwarenessSpec, err)
		}
		log.Printf("[awareness] 逾期提醒已启用: %s", cfg.AwarenessSpec)
	} else {
		log.Println("[awareness] 逾期提醒未启用（OPS_AWARENESS_SPEC 为空），逾期未完成的必修项不会自动催办")
	}
	if cfg.ReviewSpec != "" {
		if err := sched.AddFixed(cfg.ReviewSpec, h.RemindOverdueActionItems); err != nil {
			log.Fatalf("复盘改进项催办 cron 表达式无效(%s): %v", cfg.ReviewSpec, err)
		}
		log.Printf("[review] 改进项逾期催办已启用: %s", cfg.ReviewSpec)
	} else {
		log.Println("[review] 改进项逾期催办未启用（OPS_REVIEW_SPEC 为空），逾期的改进项不会有人被提醒")
	}
	if cfg.ServiceSpec != "" {
		if err := sched.AddFixed(cfg.ServiceSpec, h.CheckServicesForSchedule); err != nil {
			log.Fatalf("主机服务巡检 cron 表达式无效(%s): %v", cfg.ServiceSpec, err)
		}
		log.Printf("[service] 服务巡检已启用: %s", cfg.ServiceSpec)
	} else {
		log.Println("[service] 服务巡检未启用（OPS_SERVICE_SPEC 为空），纳管服务只能手动巡检")
	}
	if cfg.ConfigSpec != "" {
		if err := sched.AddFixed(cfg.ConfigSpec, h.CheckConfigsForSchedule); err != nil {
			log.Fatalf("配置文件巡检 cron 表达式无效(%s): %v", cfg.ConfigSpec, err)
		}
		log.Printf("[config] 配置巡检已启用: %s", cfg.ConfigSpec)
	} else {
		log.Println("[config] 配置巡检未启用（OPS_CONFIG_SPEC 为空），配置被改了不会自动被发现")
	}
	if cfg.SLASpec != "" {
		if err := sched.AddFixed(cfg.SLASpec, h.CheckEventSLAForSchedule); err != nil {
			log.Fatalf("事件 SLA 扫描 cron 表达式无效(%s): %v", cfg.SLASpec, err)
		}
		log.Printf("[sla] 事件 SLA 扫描已启用: %s", cfg.SLASpec)
	} else {
		log.Println("[sla] 事件 SLA 扫描未启用（OPS_SLA_SPEC 为空），响应/恢复超时不会有人被提醒")
	}
	if cfg.SecEventSpec != "" {
		if err := sched.AddFixed(cfg.SecEventSpec, h.CollectSecurityEventsForSchedule); err != nil {
			log.Fatalf("安全事件采集 cron 表达式无效(%s): %v", cfg.SecEventSpec, err)
		}
		log.Printf("[secevent] 安全事件采集已启用: %s", cfg.SecEventSpec)
	} else {
		log.Println("[secevent] 安全事件采集未启用（OPS_SECEVENT_SPEC 为空），拦截与暴露面流水不会进研判台")
	}
	if cfg.CloudSyncSpec != "" {
		if err := sched.AddFixed(cfg.CloudSyncSpec, h.SyncCloudForSchedule); err != nil {
			log.Fatalf("云资源同步 cron 表达式无效(%s): %v", cfg.CloudSyncSpec, err)
		}
		log.Printf("[cloud_sync] 云资源同步已启用: %s（会出网调用阿里云 OpenAPI）", cfg.CloudSyncSpec)
	} else {
		log.Println("[cloud_sync] 云资源同步未启用（OPS_CLOUD_SYNC_SPEC 为空），只能在页面上手动同步")
	}
	if cfg.DomainSpec != "" {
		if err := sched.AddFixed(cfg.DomainSpec, h.CheckAllDomainsForSchedule); err != nil {
			log.Fatalf("域名巡检 cron 表达式无效(%s): %v", cfg.DomainSpec, err)
		}
		dnsFrom := "系统 resolver"
		if cfg.DNSServer != "" {
			dnsFrom = cfg.DNSServer
		}
		log.Printf("[domain] 域名巡检已启用: %s（DNS 走 %s）", cfg.DomainSpec, dnsFrom)
	} else {
		log.Println("[domain] 域名巡检未启用（OPS_DOMAIN_SPEC 为空），解析漂移与注册到期只能手动巡检")
	}
	if cfg.HostLogSpec != "" {
		if err := sched.AddFixed(cfg.HostLogSpec, h.ScanHostLogsForSchedule); err != nil {
			log.Fatalf("主机日志巡检 cron 表达式无效(%s): %v", cfg.HostLogSpec, err)
		}
		log.Printf("[hostlog] 主机日志巡检已启用: %s（允许读取的目录: %s）",
			cfg.HostLogSpec, cfg.LogPathPrefixes)
	} else {
		log.Println("[hostlog] 主机日志巡检未启用（OPS_HOST_LOG_SPEC 为空），日志里新冒出来的错误与日志目录占用只能手动巡检")
	}
	if cfg.VaultSpec != "" {
		if err := sched.AddFixed(cfg.VaultSpec, h.RemindVaultRotationForSchedule); err != nil {
			log.Fatalf("密码库轮换检查 cron 表达式无效(%s): %v", cfg.VaultSpec, err)
		}
		log.Printf("[vault] 口令轮换逾期检查已启用: %s", cfg.VaultSpec)
	} else {
		log.Println("[vault] 口令轮换逾期检查未启用（OPS_VAULT_SPEC 为空），设了轮换周期的口令逾期也不会有人被提醒")
	}
	if cfg.BackupSpec != "" {
		if err := sched.AddFixed(cfg.BackupSpec, h.RunBackupForSchedule); err != nil {
			log.Fatalf("自动备份 cron 表达式无效(%s): %v", cfg.BackupSpec, err)
		}
		log.Printf("[backup] 自动备份已启用: %s → %s（保留 %d 份）", cfg.BackupSpec, cfg.BackupDir, cfg.BackupKeep)
	} else {
		log.Println("[backup] 自动备份未启用（OPS_BACKUP_SPEC 为空），只能手动执行 `ops backup`")
	}
	// 只有 leader 跑调度。standby 上面那二十来个 AddFixed 已经注册进 cron 对象了，
	// 但 cron 没有 Start 就一个都不会触发 —— 接管时直接 Start 即可。
	if inst.Role() == instance.RoleLeader {
		if err := sched.Start(); err != nil {
			log.Fatalf("定时任务加载失败: %v", err)
		}
	} else {
		log.Printf("[instance] standby：定时任务**一个都不跑**（含 %d 个内置固定任务）。"+
			"leader 心跳过期时会自动接管", func() int { _, fixed, _ := sched.Stats(); return fixed }())
	}
	// 心跳，并在接过 leader 时把调度起起来
	inst.Start(func() {
		if err := sched.Start(); err != nil {
			log.Printf("[instance] 接管后启动调度失败: %v", err)
			return
		}
		log.Printf("[instance] 接管完成，定时任务已在本实例开始调度")
	})

	// 启动时按配置清理过期录像
	h.CleanupRecordings()

	engine := buildRouter(h, cfg, gormDB)

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           engine,
		ReadHeaderTimeout: 10 * time.Second,
	}

	// 监听退出信号：SIGTERM 是 systemd / docker stop 的默认信号
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		log.Printf("运维平台后端已启动: %s", cfg.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("服务启动失败: %v", err)
		}
	}()

	sig := <-stop
	log.Printf("收到信号 %v，开始优雅退出（最长等 %d 秒）", sig, cfg.ShutdownTimeoutSec)

	// 顺序是有讲究的：先停调度（别在收尾过程中又起新任务），
	// 再断开 WebSocket 这类劫持连接（Shutdown 不管它们，不断开就只能等超时），
	// 最后等在跑的普通请求（批量执行是同步请求）结束。
	sched.Stop()
	h.Shutdown("平台正在停机，连接已断开")
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(cfg.ShutdownTimeoutSec)*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("[shutdown] HTTP 服务未能在超时内停完: %v", err)
	}
	h.FinishShutdown("平台停机，会话已结束")
	// 注销实例：必须在关库之前。留着那行的话，下次启动会被自己的上一条记录挡住，
	// 而「重启失败，说已经有实例在跑」是最让人恼火的一类故障
	inst.Release()
	if sqlDB, err := gormDB.DB(); err == nil {
		if err := sqlDB.Close(); err != nil {
			log.Printf("[shutdown] 关闭数据库失败: %v", err)
		}
	}
	log.Println("已退出")
}

// mustLoadConfig 读配置并做安全校验，prod 模式下不合格就别启动
func mustLoadConfig() *config.Config {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("%v", err)
	}
	if err := cfg.Validate(); err != nil {
		log.Fatalf("%v", err)
	}
	return cfg
}

// runBackupCmd `ops backup [--env-file 配置] [--out DIR] [--keep N]`
func runBackupCmd(args []string) {
	fs := flag.NewFlagSet("backup", flag.ExitOnError)
	envFile := fs.String("env-file", "", "读取配置文件（如 /etc/opsone/opsone.env）。sudo 会清掉环境变量，定时任务里一般都要带上")
	dsn := fs.String("dsn", "", "数据库文件路径，覆盖 OPS_DSN")
	recordDir := fs.String("record-dir", "", "录像目录，覆盖 OPS_RECORD_DIR")
	out := fs.String("out", "", "备份目录，默认取 OPS_BACKUP_DIR")
	keep := fs.Int("keep", 0, "保留份数，默认取 OPS_BACKUP_KEEP")
	_ = fs.Parse(args)

	if err := loadEnvFile(*envFile); err != nil {
		log.Fatalf("%v", err)
	}
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("%v", err)
	}
	if *dsn != "" {
		cfg.DSN = *dsn
	}
	if *recordDir != "" {
		cfg.RecordDir = *recordDir
	}
	dir := cfg.BackupDir
	if *out != "" {
		dir = *out
	}
	n := cfg.BackupKeep
	if *keep > 0 {
		n = *keep
	}

	// 关键一道闸：库文件不存在就别往下走。
	// db.Open 会顺手建一个空库，那样会「备份成功」出一个空文件，
	// 等到真出事才发现备份是空的 —— 这种失败必须当场报出来。
	if _, statErr := os.Stat(cfg.DSN); statErr != nil {
		log.Fatalf("数据库文件 %s 不存在或不可读（%v）。\n"+
			"如果是通过 sudo/cron 跑的，环境变量很可能没带进来，请加 --env-file /etc/opsone/opsone.env 或 --dsn 指定路径", cfg.DSN, statErr)
	}

	// 备份只读源库，不跑迁移、不做初始化，免得在恢复现场改动数据
	gormDB, err := db.Open(cfg.DSN, false, nil)
	if err != nil {
		log.Fatalf("数据库连接失败: %v", err)
	}
	if err := backup.SanityCheck(gormDB); err != nil {
		log.Fatalf("拒绝备份 %s: %v\n"+
			"多半是配置没读到（sudo / cron 会清掉环境变量），请加 --env-file /etc/opsone/opsone.env 或 --dsn 指定真实库路径", cfg.DSN, err)
	}
	res, err := backup.Run(gormDB, cfg.RecordDir, dir, n, time.Time{})
	if err != nil {
		log.Fatalf("备份失败: %v", err)
	}
	fmt.Printf("备份完成: %s\n", res.Summary())
	fmt.Printf("数据库快照: %s\n", res.DBPath)
	if res.RecordingsPath != "" {
		fmt.Printf("录像归档: %s\n", res.RecordingsPath)
	}
}

// runMigrateCmd `ops migrate status|up [--env-file 配置] [--dsn 库路径]`
//
// 单独成一个子命令，是为了让「先迁移、再起服务」成为可能：
// 升级时先 `ops backup`、再 `ops migrate up` 看清楚跑了哪些步骤，
// 确认没问题才起服务。启动时仍然会自动迁移（单实例部署下这是合理默认），
// 但把它摊开成一个能单独跑、能只读查看的命令，出问题时才有回答问题的地方。
func runMigrateCmd(args []string) {
	action := "status"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		action = args[0]
		args = args[1:]
	}
	if action != "status" && action != "up" {
		log.Fatalf("未知动作 %q，可用: status（只看不改）/ up（应用未执行的步骤）", action)
	}

	fs := flag.NewFlagSet("migrate", flag.ExitOnError)
	envFile := fs.String("env-file", "", "读取配置文件（如 /etc/opsone/opsone.env）")
	dsn := fs.String("dsn", "", "数据库文件路径，覆盖 OPS_DSN")
	_ = fs.Parse(args)

	if err := loadEnvFile(*envFile); err != nil {
		log.Fatalf("%v", err)
	}
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("%v", err)
	}
	if *dsn != "" {
		cfg.DSN = *dsn
	}

	// 与备份同一道闸：库不存在时 db.Open 会顺手建一个空库，
	// 那样 status 会显示「一个全新的空库一切正常」，而人真正想看的是现有那个库
	if _, statErr := os.Stat(cfg.DSN); statErr != nil {
		log.Fatalf("数据库文件 %s 不存在或不可读（%v）。\n"+
			"如果是通过 sudo/cron 跑的，环境变量很可能没带进来，请加 --env-file 或 --dsn 指定路径", cfg.DSN, statErr)
	}
	gormDB, err := db.Open(cfg.DSN, false, nil)
	if err != nil {
		log.Fatalf("数据库连接失败: %v", err)
	}

	if action == "up" {
		if err := db.Migrate(gormDB, version); err != nil {
			log.Fatalf("迁移失败: %v", err)
		}
		fmt.Println("迁移完成。")
	}

	status, err := migrate.Report(gormDB, db.Models())
	if err != nil {
		log.Fatalf("读迁移状态失败: %v", err)
	}
	printMigrateStatus(cfg.DSN, status)
}

func printMigrateStatus(dsn string, st *migrate.Status) {
	fmt.Printf("库文件: %s\n", dsn)
	fmt.Printf("结构版本: %d（这个二进制认到 %d）\n", st.Version, st.KnownVersion)

	if len(st.Applied) == 0 {
		fmt.Println("\n已应用: 无 —— 这个库还没被带迁移记录的版本启动过")
	} else {
		fmt.Println("\n已应用:")
		for _, row := range st.Applied {
			extra := fmt.Sprintf("%dms", row.TookMs)
			if row.Source == "baseline" {
				extra = "新装库，跳过未执行"
			}
			fmt.Printf("  v%-3d %-32s %s  %s  by %s\n", row.Version, row.Name,
				row.AppliedAt.Format("2006-01-02 15:04:05"), extra, row.AppliedBy)
			if row.Note != "" {
				fmt.Printf("       %s\n", row.Note)
			}
		}
	}

	if len(st.Pending) == 0 {
		fmt.Println("\n待应用: 无")
	} else {
		fmt.Println("\n待应用（下次启动或 `ops migrate up` 时执行）:")
		for _, step := range st.Pending {
			fmt.Printf("  v%-3d %s\n       %s\n", step.Version, step.Name, step.Note)
		}
	}

	switch {
	case st.DriftError != "":
		fmt.Printf("\n陈旧列检查: 没查成 —— %s\n", st.DriftError)
	case len(st.Drift) == 0:
		fmt.Println("\n陈旧列: 无（库里的列与模型声明一致）")
	default:
		fmt.Printf("\n陈旧列 %d 个 —— 模型里已经没有、但库里还留着（AutoMigrate 只加不减）。\n"+
			"它们不会让程序出错，但会让人误以为那些字段还在用。\n"+
			"平台**不自动删**：删列不可逆。确认无用后，**先 `ops backup`**，再执行：\n", len(st.Drift))
		for _, item := range st.Drift {
			fmt.Printf("  %s\n", item.SQL)
		}
	}

	fmt.Println("\n注意：迁移**没有回滚**。SQLite 下很多变更要重建表，自动生成的回滚往往是错的，")
	fmt.Println("而一个能跑但把数据弄坏的回滚比没有回滚更危险。回滚路径是 `ops restore` 从升级前的备份恢复，")
	fmt.Println("所以升级流程里「先备份」不是建议而是前提。")
}

// runRestoreCmd `ops restore --db 快照 [--recordings 归档] [--env-file 配置]`
func runRestoreCmd(args []string) {
	fs := flag.NewFlagSet("restore", flag.ExitOnError)
	snapshot := fs.String("db", "", "数据库快照文件（必填）")
	recordings := fs.String("recordings", "", "录像归档 tar.gz（可选）")
	envFile := fs.String("env-file", "", "读取配置文件（如 /etc/opsone/opsone.env）")
	dsn := fs.String("dsn", "", "恢复到哪个数据库文件，覆盖 OPS_DSN")
	recordDir := fs.String("record-dir", "", "录像恢复到哪个目录，覆盖 OPS_RECORD_DIR")
	_ = fs.Parse(args)
	if *snapshot == "" {
		log.Fatalf("用法: ops restore --db backups/opsone-20260921-030000.db [--recordings ...tar.gz] [--env-file /etc/opsone/opsone.env]")
	}

	if err := loadEnvFile(*envFile); err != nil {
		log.Fatalf("%v", err)
	}
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("%v", err)
	}
	if *dsn != "" {
		cfg.DSN = *dsn
	}
	if *recordDir != "" {
		cfg.RecordDir = *recordDir
	}
	moved, err := backup.Restore(*snapshot, cfg.DSN, *recordings, cfg.RecordDir)
	if err != nil {
		log.Fatalf("恢复失败: %v", err)
	}
	fmt.Printf("已把 %s 恢复到 %s\n", *snapshot, cfg.DSN)
	if moved != "" {
		fmt.Printf("原数据库已改名保留: %s\n", moved)
	}
	fmt.Println("注意：恢复必须在平台停机状态下做；现在可以启动服务了")
}

// loadEnvFile 读 KEY=VALUE 形式的配置文件（就是 systemd EnvironmentFile 那种）。
// 已经存在于环境里的变量不覆盖，这样命令行临时指定的仍然生效。
func loadEnvFile(path string) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("读配置文件失败: %w", err)
	}
	for i, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, found := strings.Cut(line, "=")
		if !found {
			return fmt.Errorf("配置文件第 %d 行不是 KEY=VALUE: %s", i+1, line)
		}
		key = strings.TrimSpace(key)
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		if _, exists := os.LookupEnv(key); exists {
			continue
		}
		if err := os.Setenv(key, value); err != nil {
			return err
		}
	}
	return nil
}

func buildRouter(h *handler.Handler, cfg *config.Config, gormDB *gorm.DB) *gin.Engine {
	if !cfg.Debug {
		gin.SetMode(gin.ReleaseMode)
	}
	r := gin.New()
	// 默认谁都不信：来源 IP 取 TCP 对端地址，X-Forwarded-For 被忽略。
	// gin 的默认行为是信任所有代理，那样任何人都能伪造审计里的来源 IP。
	if err := r.SetTrustedProxies(cfg.TrustedProxies); err != nil {
		log.Fatalf("OPS_TRUSTED_PROXIES 配置无效: %v", err)
	}
	// 访问日志用自己的 formatter：gin 默认会把 raw query 拼进 path，
	// 而 WebSocket 放行了 ?access_token=<JWT> —— 那等于把一个有效令牌写进日志文件
	r.Use(middleware.AccessLog(gin.DefaultWriter), gin.Recovery())
	// 指标中间件要在业务路由之前、CORS 之后：CORS 的预检请求也算请求量，
	// 但它不该把耗时直方图拉低（预检永远很快）—— 所以预检由 route=unmatched 兜着
	reg := metrics.Default()
	h.InstallMetrics(reg, version)
	r.Use(metrics.Middleware(reg))
	r.Use(cors.New(cors.Config{
		AllowOrigins:     cfg.AllowOrigins,
		AllowMethods:     []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowHeaders:     []string{"Origin", "Content-Type", "Authorization"},
		AllowCredentials: true,
		MaxAge:           12 * time.Hour,
	}))

	// 存活探针：进程还在就返回 200，不查任何依赖
	r.GET("/healthz", func(c *gin.Context) {
		response.OK(c, gin.H{"status": "ok", "version": version})
	})
	// 就绪探针：真查一次数据库。这里刻意用原生 HTTP 状态码（503）而不是统一响应体，
	// 负载均衡与 k8s 探针看的是状态码。
	r.GET("/readyz", func(c *gin.Context) { readyz(c, gormDB) })

	// 指标与 pprof：令牌为空时**连路由都不注册**，访问是 404 而不是 401 ——
	// 不告诉外面「这里有个被保护的端点」
	if cfg.MetricsToken != "" {
		r.GET("/metrics", handler.MetricsHandler(reg, cfg.MetricsToken))
		log.Println("[metrics] /metrics 已启用（需要 OPS_METRICS_TOKEN 令牌）")
		if cfg.EnablePprof {
			r.GET("/debug/pprof/:profile", handler.PprofHandler(cfg.MetricsToken))
			log.Println("[metrics] /debug/pprof 已启用 —— heap profile 是进程内存快照，" +
				"里面有 SSH 私钥与主机口令，查完请关掉（OPS_PPROF=false）")
		}
	} else {
		log.Println("[metrics] /metrics 未启用（OPS_METRICS_TOKEN 为空），平台不产出 Prometheus 指标")
		if cfg.EnablePprof {
			log.Println("[metrics] OPS_PPROF=true 但没有 OPS_METRICS_TOKEN，pprof 仍然不会开 —— " +
				"不给一个能拉进程内存快照的端点留免鉴权入口")
		}
	}

	api := r.Group("/api/v1")
	api.POST("/auth/login", h.Login)
	// 登录页需要的品牌信息，无需鉴权
	api.GET("/public/branding", h.Branding)
	// 告警接入走 Token 鉴权，供外部监控系统直接 POST
	api.POST("/webhooks/alerts/:token", h.ReceiveAlert)
	// IM 扫码登录：这几个都必须免鉴权（用户此刻还没有令牌）。
	// 安全性靠一次性 state / ticket 与「必须已绑定」这两道，见 docs/SECURITY.md 24
	api.GET("/public/im-logins", h.ListImLoginProviders)
	api.GET("/auth/im/authorize", h.ImAuthorize)
	api.GET("/auth/im/callback", h.ImLoginCallback)
	api.POST("/auth/im/exchange", h.ImLoginExchange)
	// 登录页提示「本平台已接入域账号」，只回一个布尔，不暴露目录地址
	api.GET("/public/ldap-login", h.ListLdapLoginHint)

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
		// 主机体检报告：把已有巡检数据汇总，不触发任何新的 SSH 连接
		auth.GET("/hosts/:id/report", h.GetHostReport)
		auth.GET("/hosts/metrics", h.ListHostMetrics)
		auth.POST("/hosts/metrics/collect", middleware.RequirePerm("host:check"), h.CollectAllHostMetrics)
		auth.GET("/hosts/:id/metrics", h.HostMetricHistory)
		auth.POST("/hosts/:id/metrics/collect", middleware.RequirePerm("host:check"), h.CollectHostMetric)
		// 磁盘占用分析：按需跑 df/du，只读、不落库、不做定时任务
		auth.POST("/hosts/:id/disk-usage", middleware.RequirePerm("file:read"), h.AnalyzeHostDisk)
		auth.GET("/hosts/:id/terminal", middleware.RequirePerm("terminal:connect"), h.Terminal)

		// 主机服务：纳管 systemd unit、对照期望态巡检漂移、启停
		auth.GET("/host-services", h.ListHostServices)
		auth.GET("/host-services/stats", h.HostServiceStats)
		auth.GET("/host-services/actions", h.ListHostServiceActions)
		auth.POST("/host-services/check", middleware.RequirePerm("service:manage"), h.CheckHostServices)
		auth.POST("/host-services", middleware.RequirePerm("service:manage"), h.AdoptHostServices)
		auth.PUT("/host-services/:id", middleware.RequirePerm("service:manage"), h.UpdateHostService)
		auth.DELETE("/host-services/:id", middleware.RequirePerm("service:manage"), h.DeleteHostService)
		auth.POST("/host-services/:id/operate", middleware.RequirePerm("service:control"), h.OperateHostService)
		auth.GET("/hosts/:id/services/discover", middleware.RequirePerm("service:manage"), h.DiscoverHostServices)

		// 配置文件：登记 → 抓基线 → 巡检漂移 → 编辑 → diff → 下发（备份+原子替换+回读）→ 回滚
		auth.GET("/config-files", middleware.RequirePerm("configfile:manage"), h.ListConfigFiles)
		auth.GET("/config-files/stats", middleware.RequirePerm("configfile:manage"), h.ConfigStats)
		auth.GET("/config-files/applies", middleware.RequirePerm("configfile:manage"), h.ListConfigApplies)
		auth.GET("/config-files/:id", middleware.RequirePerm("configfile:manage"), h.GetConfigFile)
		auth.GET("/config-files/:id/diff", middleware.RequirePerm("configfile:manage"), h.DiffConfigFile)
		auth.GET("/config-versions/:id", middleware.RequirePerm("configfile:manage"), h.GetConfigVersion)
		auth.POST("/config-files", middleware.RequirePerm("configfile:manage"), h.CreateConfigFile)
		auth.PUT("/config-files/:id", middleware.RequirePerm("configfile:manage"), h.UpdateConfigFile)
		auth.DELETE("/config-files/:id", middleware.RequirePerm("configfile:manage"), h.DeleteConfigFile)
		auth.POST("/config-files/:id/capture", middleware.RequirePerm("configfile:manage"), h.CaptureConfigFile)
		auth.POST("/config-files/:id/edit", middleware.RequirePerm("configfile:manage"), h.EditConfigVersion)
		auth.POST("/config-files/check", middleware.RequirePerm("configfile:manage"), h.CheckConfigFiles)
		auth.POST("/config-files/:id/apply", middleware.RequirePerm("configfile:apply"), h.ApplyConfigFile)
		auth.POST("/config-files/:id/rollback", middleware.RequirePerm("configfile:apply"), h.RollbackConfigFile)

		// 凭证库：共享登录凭据。密钥永不出接口，轮换一次即对所有引用主机生效
		auth.GET("/credentials", middleware.RequirePerm("credential:manage"), h.ListCredentials)
		auth.GET("/credentials/state", middleware.RequirePerm("credential:manage"), h.GetCredentialState)
		auth.GET("/credentials/:id/hosts", middleware.RequirePerm("credential:manage"), h.ListCredentialHosts)
		auth.POST("/credentials", middleware.RequirePerm("credential:manage"), h.CreateCredential)
		auth.PUT("/credentials/:id", middleware.RequirePerm("credential:manage"), h.UpdateCredential)
		auth.DELETE("/credentials/:id", middleware.RequirePerm("credential:manage"), h.DeleteCredential)
		auth.POST("/credentials/:id/rotate", middleware.RequirePerm("credential:manage"), h.RotateCredential)
		auth.POST("/credentials/:id/hosts", middleware.RequirePerm("credential:manage"), h.BindCredentialHosts)
		auth.POST("/credentials/:id/check", middleware.RequirePerm("credential:check"), h.CheckCredential)

		// 账号密码库 / 2FA 验证码库：明文是给人取走用的，所以「取用」单独一个权限，
		// 且每次取用都留痕（留痕写不进去就不给明文）
		auth.GET("/vault/state", middleware.RequirePerm("vault:manage"), h.GetVaultState)
		auth.GET("/vault/accounts", middleware.RequirePerm("vault:manage"), h.ListVaultAccounts)
		auth.POST("/vault/accounts", middleware.RequirePerm("vault:manage"), h.CreateVaultAccount)
		auth.PUT("/vault/accounts/:id", middleware.RequirePerm("vault:manage"), h.UpdateVaultAccount)
		auth.DELETE("/vault/accounts/:id", middleware.RequirePerm("vault:manage"), h.DeleteVaultAccount)
		auth.POST("/vault/accounts/:id/reveal", middleware.RequirePerm("vault:reveal"), h.RevealVaultAccount)
		auth.POST("/vault/accounts/:id/rotate", middleware.RequirePerm("vault:manage"), h.RotateVaultAccount)
		auth.GET("/vault/accesses", middleware.RequirePerm("vault:manage"), h.ListVaultAccesses)
		auth.GET("/vault/totps", middleware.RequirePerm("vault:manage"), h.ListVaultTOTPs)
		auth.POST("/vault/totps", middleware.RequirePerm("vault:manage"), h.CreateVaultTOTP)
		auth.PUT("/vault/totps/:id", middleware.RequirePerm("vault:manage"), h.UpdateVaultTOTP)
		auth.DELETE("/vault/totps/:id", middleware.RequirePerm("vault:manage"), h.DeleteVaultTOTP)
		auth.POST("/vault/totps/:id/code", middleware.RequirePerm("vault:reveal"), h.CodeVaultTOTP)
		auth.POST("/vault/totps/:id/uri", middleware.RequirePerm("vault:reveal"), h.URIVaultTOTP)

		// 安全意识：学员侧（人人可用，只能看/做自己的那份）
		auth.GET("/security/awareness/my", h.ListMyAwareness)
		auth.GET("/security/awareness/my/:id", h.GetMyAwarenessCourse)
		auth.POST("/security/awareness/my/:id/read", h.ConfirmAwarenessRead)
		auth.POST("/security/awareness/my/:id/quiz", h.SubmitAwarenessQuiz)
		// 安全意识：管理侧
		auth.GET("/security/awareness/courses", h.ListAwarenessCourses)
		auth.POST("/security/awareness/courses", middleware.RequirePerm("awareness:manage"), h.CreateAwarenessCourse)
		auth.PUT("/security/awareness/courses/:id", middleware.RequirePerm("awareness:manage"), h.UpdateAwarenessCourse)
		auth.DELETE("/security/awareness/courses/:id", middleware.RequirePerm("awareness:manage"), h.DeleteAwarenessCourse)
		auth.POST("/security/awareness/courses/:id/publish", middleware.RequirePerm("awareness:manage"), h.PublishAwarenessCourse)
		auth.POST("/security/awareness/courses/:id/unpublish", middleware.RequirePerm("awareness:manage"), h.UnpublishAwarenessCourse)
		auth.POST("/security/awareness/courses/:id/remind", middleware.RequirePerm("awareness:manage"), h.RemindAwarenessCourse)
		auth.GET("/security/awareness/courses/:id/records", middleware.RequirePerm("awareness:manage"), h.ListAwarenessRecords)
		auth.GET("/security/awareness/courses/:id/questions", middleware.RequirePerm("awareness:manage"), h.ListAwarenessQuestions)
		auth.POST("/security/awareness/courses/:id/questions", middleware.RequirePerm("awareness:manage"), h.CreateAwarenessQuestion)
		auth.PUT("/security/awareness/questions/:id", middleware.RequirePerm("awareness:manage"), h.UpdateAwarenessQuestion)
		auth.DELETE("/security/awareness/questions/:id", middleware.RequirePerm("awareness:manage"), h.DeleteAwarenessQuestion)

		// 特征库：特征本身 + 应用到真正在跑的检测点（命令规则 / 暴露面扫描结果）
		auth.GET("/security/signatures", h.ListSignatures)
		auth.POST("/security/signatures", middleware.RequirePerm("signature:manage"), h.CreateSignature)
		auth.PUT("/security/signatures/:id", middleware.RequirePerm("signature:manage"), h.UpdateSignature)
		auth.DELETE("/security/signatures/:id", middleware.RequirePerm("signature:manage"), h.DeleteSignature)
		auth.POST("/security/signatures/:id/apply", middleware.RequirePerm("signature:apply"), h.ApplySignature)
		auth.POST("/security/signatures/:id/revoke", middleware.RequirePerm("signature:apply"), h.RevokeSignature)
		auth.POST("/security/signatures/:id/port-check", h.CheckPortSignature)
		auth.GET("/security/signatures/reconcile", h.ReconcileSignatures)

		// 安全事件研判台：事件只由采集器从已有流水生成，界面上不能手工新建
		auth.GET("/security/events", h.ListSecurityEvents)
		auth.GET("/security/events/stats", h.SecurityEventStats)
		auth.GET("/security/events/:id", h.GetSecurityEvent)
		auth.POST("/security/events/collect", middleware.RequirePerm("secevent:manage"), h.RunSecurityCollect)
		auth.POST("/security/events/triage", middleware.RequirePerm("secevent:manage"), h.TriageSecurityEvents)
		auth.POST("/security/events/:id/note", middleware.RequirePerm("secevent:manage"), h.AddSecurityEventNote)
		auth.POST("/security/events/:id/block", middleware.RequirePerm("secevent:respond"), h.BlockSecurityEventSource)
		auth.GET("/security/event-mutes", h.ListSecurityMutes)
		auth.DELETE("/security/event-mutes/:id", middleware.RequirePerm("secevent:manage"), h.DeleteSecurityMute)
		// 原始数据视图：按 RefTable/RefID 取回没被摘要截断的原始流水行
		auth.GET("/security/events/:id/raw", middleware.RequirePerm("secevent:manage"), h.GetSecurityEventRaw)
		// 升格为事件工单 —— 平台里「派发」唯一诚实的落法（没有对接外部工单系统）
		auth.POST("/security/events/:id/escalate", middleware.RequirePerm("secevent:respond"), h.EscalateSecurityEvent)

		// 安全概览：只聚合已有数据，没采集的项在 gaps 里明说
		auth.GET("/security/overview", h.SecurityOverview)
		// 学习建议：从已有数据算出该改哪个配置，通过就真的去改
		auth.GET("/security/suggestions", h.ListSecuritySuggestions)
		auth.POST("/security/suggestions/apply", middleware.RequirePerm("suggestion:apply"), h.ApplySecuritySuggestions)
		auth.GET("/security/suggestion-dismissals", h.ListSuggestionDismissals)
		auth.DELETE("/security/suggestion-dismissals/:id", middleware.RequirePerm("suggestion:apply"), h.DeleteSuggestionDismissal)

		// 密钥体检与迁移（凭证库页面里的「加密存量数据」走的就是这里）
		auth.GET("/secrets/audit", middleware.RequirePerm("config:manage"), h.GetSecretAudit)
		auth.POST("/secrets/migrate", middleware.RequirePerm("credential:manage"), h.MigrateSecrets)
		// 换密钥 / 取消加密：解开再写回，并把进程内存里的密钥一起切过去
		auth.POST("/secrets/rekey", middleware.RequirePerm("credential:manage"), h.RekeySecrets)

		auth.GET("/hosts/:id/files", middleware.RequirePerm("file:read"), h.ListFiles)
		auth.GET("/hosts/:id/files/download", middleware.RequirePerm("file:read"), h.DownloadFile)
		auth.POST("/hosts/:id/files/upload", middleware.RequirePerm("file:write"), h.UploadFile)
		auth.POST("/hosts/:id/files/mkdir", middleware.RequirePerm("file:write"), h.MakeDir)
		auth.POST("/hosts/:id/files/rename", middleware.RequirePerm("file:write"), h.RenameFile)
		auth.DELETE("/hosts/:id/files", middleware.RequirePerm("file:delete"), h.DeleteFile)
		auth.GET("/file-audits", middleware.RequirePerm("audit:view"), h.ListFileAudits)

		auth.GET("/scheduler/jobs", middleware.RequirePerm("cron:manage"), h.ListCronJobs)
		auth.POST("/scheduler/jobs", middleware.RequirePerm("cron:manage"), h.CreateCronJob)
		auth.PUT("/scheduler/jobs/:id", middleware.RequirePerm("cron:manage"), h.UpdateCronJob)
		auth.DELETE("/scheduler/jobs/:id", middleware.RequirePerm("cron:manage"), h.DeleteCronJob)
		auth.POST("/scheduler/jobs/:id/run", middleware.RequirePerm("cron:run"), h.RunCronJobNow)

		auth.GET("/exec/jobs", middleware.RequirePerm("exec:run"), h.ListExecJobs)
		auth.GET("/exec/jobs/:id", middleware.RequirePerm("exec:run"), h.GetExecJob)
		auth.POST("/exec/jobs", middleware.RequirePerm("exec:run"), h.RunExecJob)
		// 下发闸门：预检与拦截流水
		auth.POST("/exec/precheck", middleware.RequirePerm("exec:run"), h.PrecheckExec)
		auth.GET("/exec/guard-logs", middleware.RequirePerm("audit:view"), h.ListExecGuardLogs)

		auth.GET("/sessions", middleware.RequirePerm("session:view"), h.ListSessions)
		auth.GET("/sessions/commands", middleware.RequirePerm("session:view"), h.SearchSessionCommands)
		auth.GET("/sessions/commands/export", middleware.RequirePerm("session:view"), h.ExportSessionCommands)
		auth.GET("/sessions/:id", middleware.RequirePerm("session:view"), h.GetSession)
		auth.GET("/sessions/:id/commands", middleware.RequirePerm("session:view"), h.ListSessionCommands)
		auth.GET("/sessions/:id/replay", middleware.RequirePerm("session:replay"), h.ReplaySession)

		auth.GET("/system/command-rules", middleware.RequirePerm("rule:manage"), h.ListCommandRules)
		auth.POST("/system/command-rules/test", h.TestCommandRule)
		auth.POST("/system/command-rules", middleware.RequirePerm("rule:manage"), h.CreateCommandRule)
		auth.PUT("/system/command-rules/:id", middleware.RequirePerm("rule:manage"), h.UpdateCommandRule)
		auth.DELETE("/system/command-rules/:id", middleware.RequirePerm("rule:manage"), h.DeleteCommandRule)

		auth.GET("/system/users", middleware.RequirePerm("user:update"), h.ListUsers)
		auth.POST("/system/users", middleware.RequirePerm("user:create"), h.CreateUser)
		auth.PUT("/system/users/:id", middleware.RequirePerm("user:update"), h.UpdateUser)
		auth.DELETE("/system/users/:id", middleware.RequirePerm("user:delete"), h.DeleteUser)

		auth.GET("/system/roles", middleware.RequirePerm("role:manage"), h.ListRoles)
		auth.POST("/system/roles", middleware.RequirePerm("role:manage"), h.CreateRole)
		auth.PUT("/system/roles/:id", middleware.RequirePerm("role:manage"), h.UpdateRole)
		auth.DELETE("/system/roles/:id", middleware.RequirePerm("role:manage"), h.DeleteRole)

		auth.GET("/system/menus/tree", middleware.RequireAnyPerm("menu:manage", "role:manage"), h.MenuTree)
		auth.POST("/system/menus", middleware.RequirePerm("menu:manage"), h.CreateMenu)
		auth.PUT("/system/menus/:id", middleware.RequirePerm("menu:manage"), h.UpdateMenu)
		auth.DELETE("/system/menus/:id", middleware.RequirePerm("menu:manage"), h.DeleteMenu)
		auth.GET("/system/audit-logs", middleware.RequirePerm("audit:view"), h.ListAuditLogs)
		auth.GET("/system/audit-logs/operators", middleware.RequirePerm("audit:view"), h.AuditOperators)
		auth.GET("/system/audit-logs/export", middleware.RequirePerm("audit:view"), h.ExportAuditLogs)

		// 发件邮箱：把「用哪个邮箱发」从全局单例变成可选列表，
		// 一个都没登记时仍走系统配置里那套（既有部署不受影响）
		auth.GET("/notify/mail-state", middleware.RequirePerm("mail:manage"), h.GetMailState)
		auth.GET("/notify/mail-accounts", middleware.RequirePerm("mail:manage"), h.ListMailAccounts)
		auth.POST("/notify/mail-accounts", middleware.RequirePerm("mail:manage"), h.CreateMailAccount)
		auth.PUT("/notify/mail-accounts/:id", middleware.RequirePerm("mail:manage"), h.UpdateMailAccount)
		auth.DELETE("/notify/mail-accounts/:id", middleware.RequirePerm("mail:manage"), h.DeleteMailAccount)
		auth.POST("/notify/mail-accounts/:id/default", middleware.RequirePerm("mail:manage"), h.SetDefaultMailAccount)
		auth.POST("/notify/mail-accounts/:id/test", middleware.RequirePerm("mail:manage"), h.TestMailAccount)
		auth.POST("/notify/mail-accounts/import-global", middleware.RequirePerm("mail:manage"), h.ImportGlobalSMTP)

		// 出口代理：检测是直连 + 走代理各一次的对比
		auth.GET("/network/proxies", middleware.RequireAnyPerm("proxy:manage", "probe:manage"), h.ListEgressProxies)
		auth.POST("/network/proxies", middleware.RequirePerm("proxy:manage"), h.CreateEgressProxy)
		auth.PUT("/network/proxies/:id", middleware.RequirePerm("proxy:manage"), h.UpdateEgressProxy)
		auth.DELETE("/network/proxies/:id", middleware.RequirePerm("proxy:manage"), h.DeleteEgressProxy)
		auth.POST("/network/proxies/:id/check", middleware.RequirePerm("proxy:manage"), h.CheckEgressProxy)
		// 统一出口：哪些模块真的走代理、哪些不走，以及进程实际看到的 HTTP_PROXY
		auth.GET("/network/proxy-egress", middleware.RequirePerm("proxy:manage"), h.GetEgressPolicy)
		auth.PUT("/network/proxy-egress", middleware.RequirePerm("proxy:manage"), h.UpdateEgressPolicy)

		auth.GET("/site-links", h.ListSiteLinks)
		auth.POST("/site-links", middleware.RequirePerm("config:manage"), h.CreateSiteLink)
		auth.PUT("/site-links/:id", middleware.RequirePerm("config:manage"), h.UpdateSiteLink)
		auth.DELETE("/site-links/:id", middleware.RequirePerm("config:manage"), h.DeleteSiteLink)

		auth.GET("/system/email-templates", middleware.RequirePerm("mail:manage"), h.ListEmailTemplates)
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

		auth.GET("/notify/channels", middleware.RequirePerm("channel:manage"), h.ListNotifyChannels)
		auth.POST("/notify/channels", middleware.RequirePerm("channel:manage"), h.CreateNotifyChannel)
		auth.PUT("/notify/channels/:id", middleware.RequirePerm("channel:manage"), h.UpdateNotifyChannel)
		auth.DELETE("/notify/channels/:id", middleware.RequirePerm("channel:manage"), h.DeleteNotifyChannel)
		auth.POST("/notify/channels/:id/test", middleware.RequirePerm("channel:manage"), h.TestNotifyChannel)

		// 跨渠道通知模板（IM / webhook）。变量表是代码里的唯一权威定义，交给前端而不是让它硬编码
		auth.GET("/notify/template-vars", h.ListNotifyTemplateVars)
		auth.GET("/notify/templates", h.ListNotifyTemplates)
		auth.POST("/notify/templates", middleware.RequirePerm("channel:manage"), h.CreateNotifyTemplate)
		auth.PUT("/notify/templates/:id", middleware.RequirePerm("channel:manage"), h.UpdateNotifyTemplate)
		auth.DELETE("/notify/templates/:id", middleware.RequirePerm("channel:manage"), h.DeleteNotifyTemplate)
		auth.POST("/notify/templates/:id/preview", h.PreviewNotifyTemplate)

		auth.GET("/notify/routes", h.ListNotifyRoutes)
		auth.POST("/notify/routes", middleware.RequirePerm("route:manage"), h.CreateNotifyRoute)
		auth.PUT("/notify/routes/:id", middleware.RequirePerm("route:manage"), h.UpdateNotifyRoute)
		auth.DELETE("/notify/routes/:id", middleware.RequirePerm("route:manage"), h.DeleteNotifyRoute)
		auth.POST("/notify/routes/test", h.TestNotifyRoute)

		auth.GET("/notify/records", middleware.RequirePerm("channel:manage"), h.ListNotifyRecords)

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

		// 授权表本身是一份「谁能进哪些机器」的地图，列表要权限；
		// 诊断接口允许查自己（handler 里判断），查别人才要权限
		auth.GET("/resource-grants", middleware.RequirePerm("grant:manage"), h.ListResourceGrants)
		auth.POST("/resource-grants", middleware.RequirePerm("grant:manage"), h.CreateResourceGrant)
		auth.PUT("/resource-grants/:id", middleware.RequirePerm("grant:manage"), h.UpdateResourceGrant)
		auth.DELETE("/resource-grants/:id", middleware.RequirePerm("grant:manage"), h.DeleteResourceGrant)
		auth.GET("/resource-grants/diagnose/:id", h.DiagnoseResourceGrants)

		auth.GET("/system/configs", middleware.RequirePerm("config:manage"), h.ListConfigs)
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

		// 数据库只读查询：元数据浏览 + 只读 SQL + 流水
		auth.GET("/databases/:id/schemas", middleware.RequirePerm("db:query"), h.ListDBSchemas)
		auth.GET("/databases/:id/tables", middleware.RequirePerm("db:query"), h.ListDBTables)
		auth.GET("/databases/:id/columns", middleware.RequirePerm("db:query"), h.DescribeDBTable)
		auth.POST("/databases/:id/query", middleware.RequirePerm("db:query"), h.RunDBQuery)
		auth.POST("/databases/:id/query/export", middleware.RequirePerm("db:query"), h.ExportDBQuery)
		auth.POST("/databases/query/check", middleware.RequirePerm("db:query"), h.CheckDBQueryStatement)
		auth.GET("/databases/query-logs", middleware.RequirePerm("audit:view"), h.ListDBQueryLogs)

		auth.GET("/fixed-assets", h.ListFixedAssets)
		auth.GET("/fixed-assets/stats", h.FixedAssetStats)
		auth.POST("/fixed-assets", middleware.RequirePerm("asset:manage"), h.CreateFixedAsset)
		auth.PUT("/fixed-assets/:id", middleware.RequirePerm("asset:manage"), h.UpdateFixedAsset)
		auth.DELETE("/fixed-assets/:id", middleware.RequirePerm("asset:manage"), h.DeleteFixedAsset)

		auth.GET("/cloud-accounts", middleware.RequirePerm("cloud:manage"), h.ListCloudAccounts)
		auth.POST("/cloud-accounts", middleware.RequirePerm("cloud:manage"), h.CreateCloudAccount)
		auth.PUT("/cloud-accounts/:id", middleware.RequirePerm("cloud:manage"), h.UpdateCloudAccount)
		auth.DELETE("/cloud-accounts/:id", middleware.RequirePerm("cloud:manage"), h.DeleteCloudAccount)
		auth.POST("/cloud-accounts/:id/sync", middleware.RequirePerm("cloud:sync"), h.RunCloudSync)

		auth.GET("/cloud-resources", h.ListCloudResources)
		auth.GET("/cloud-resources/stats", h.CloudResourceStats)
		auth.GET("/cloud-resources/drift", h.CloudDrift)
		auth.POST("/cloud-resources/:id/adopt", middleware.RequirePerm("cloud:adopt"), h.AdoptCloudResource)
		auth.GET("/cloud-sync-runs", h.ListCloudSyncRuns)

		auth.GET("/domains", h.ListDomains)
		auth.GET("/domains/stats", h.DomainStats)
		auth.POST("/domains", middleware.RequirePerm("domain:manage"), h.CreateDomain)
		auth.PUT("/domains/:id", middleware.RequirePerm("domain:manage"), h.UpdateDomain)
		auth.DELETE("/domains/:id", middleware.RequirePerm("domain:manage"), h.DeleteDomain)
		auth.POST("/domains/import-cloud", middleware.RequirePerm("domain:manage"), h.ImportCloudDomains)
		auth.POST("/domains/:id/check", middleware.RequirePerm("domain:check"), h.CheckDomain)
		auth.POST("/domains/check-all", middleware.RequirePerm("domain:check"), h.CheckAllDomains)

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

		auth.GET("/build/servers", middleware.RequirePerm("build:manage"), h.ListBuildServers)
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

		// 防火墙策略：读真机规则、平台登记、对账、预检、下发、回滚。
		// 下发与回滚都走 RunOnHosts，命令规则拦截与生产二次确认自动继承。
		auth.GET("/firewall/state", middleware.RequirePerm("firewall:manage"), h.GetFirewallState)
		auth.GET("/firewall/rules", middleware.RequirePerm("firewall:manage"), h.ListFirewallRules)
		auth.POST("/firewall/rules", middleware.RequirePerm("firewall:manage"), h.CreateFirewallRule)
		auth.PUT("/firewall/rules/:id", middleware.RequirePerm("firewall:manage"), h.UpdateFirewallRule)
		auth.DELETE("/firewall/rules/:id", middleware.RequirePerm("firewall:manage"), h.DeleteFirewallRule)
		auth.POST("/firewall/rules/adopt", middleware.RequirePerm("firewall:manage"), h.AdoptFirewallRule)
		auth.POST("/firewall/precheck", middleware.RequirePerm("firewall:manage"), h.PrecheckFirewall)
		auth.POST("/firewall/apply", middleware.RequirePerm("firewall:apply"), h.ApplyFirewall)
		auth.GET("/firewall/snapshots", middleware.RequirePerm("firewall:manage"), h.ListFirewallSnapshots)
		auth.GET("/firewall/snapshots/:id", middleware.RequirePerm("firewall:manage"), h.GetFirewallSnapshot)
		auth.POST("/firewall/snapshots/:id/rollback", middleware.RequirePerm("firewall:apply"), h.RollbackFirewall)
		auth.GET("/firewall/cleanup", middleware.RequirePerm("firewall:manage"), h.FirewallCleanup)
		auth.GET("/firewall/groups", middleware.RequirePerm("firewall:manage"), h.ListFirewallGroups)
		auth.POST("/firewall/groups", middleware.RequirePerm("firewall:manage"), h.SaveFirewallGroup)
		auth.PUT("/firewall/groups/:id", middleware.RequirePerm("firewall:manage"), h.SaveFirewallGroup)
		auth.DELETE("/firewall/groups/:id", middleware.RequirePerm("firewall:manage"), h.DeleteFirewallGroup)
		auth.POST("/firewall/groups/:id/dispatch", middleware.RequirePerm("firewall:manage"), h.DispatchFirewallGroup)

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
		// 告警规则的版本历史 / 字段级差异 / 回滚 / 误删恢复。
		// 查看只需登录；改回去是写操作，与改规则同一个权限码
		// 规则版本：告警规则 / 检测规则 / 聚合策略共用一套，target 参数区分。
		// 不做策略审批，做的是改动留痕 + 一键回滚（理由见 rule_version.go 开头）
		auth.GET("/monitor/rule-version-targets", h.ListRuleVersionTargets)
		auth.GET("/monitor/rule-versions", h.ListRuleVersions)
		auth.GET("/monitor/rule-versions/diff", h.DiffRuleVersions)
		auth.GET("/monitor/rule-versions/:id", h.GetRuleVersion)
		auth.POST("/monitor/rule-versions/rollback", middleware.RequirePerm("alertrule:manage"), h.RollbackRule)
		auth.POST("/monitor/rule-versions/:id/restore", middleware.RequirePerm("alertrule:manage"), h.RestoreRuleVersion)

		// 值班大屏：只聚合已有数据
		auth.GET("/monitor/wallboard", h.Wallboard)

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
		// 容器平台授权：一条授权同时限定集群 + 命名空间 + 资源类型，
		// 闸门在 requireKubeCluster 一处。默认不生效（配置项 kube.grant_enforce）
		auth.GET("/kube/grant-state", h.GetKubeGrantState)
		auth.GET("/kube/grants", middleware.RequirePerm("kubegrant:manage"), h.ListKubeGrants)
		auth.POST("/kube/grants", middleware.RequirePerm("kubegrant:manage"), h.CreateKubeGrant)
		auth.PUT("/kube/grants/:id", middleware.RequirePerm("kubegrant:manage"), h.UpdateKubeGrant)
		auth.DELETE("/kube/grants/:id", middleware.RequirePerm("kubegrant:manage"), h.DeleteKubeGrant)
		auth.GET("/kube/grants/diagnose/:id", h.DiagnoseKubeGrant)
		auth.GET("/kube/clusters", h.ListKubeClusters)
		auth.POST("/kube/clusters", middleware.RequirePerm("kube:manage"), h.CreateKubeCluster)
		auth.PUT("/kube/clusters/:id", middleware.RequirePerm("kube:manage"), h.UpdateKubeCluster)
		auth.DELETE("/kube/clusters/:id", middleware.RequirePerm("kube:manage"), h.DeleteKubeCluster)
		auth.POST("/kube/clusters/:id/check", middleware.RequirePerm("kube:manage"), h.CheckKubeCluster)
		auth.GET("/kube/clusters/:id/nodes", h.KubeNodes)
		auth.GET("/kube/clusters/:id/namespaces", h.KubeNamespaces)
		auth.GET("/kube/clusters/:id/workloads", h.KubeWorkloads)
		auth.GET("/kube/clusters/:id/pods", h.KubePods)
		auth.GET("/kube/clusters/:id/pod-logs", middleware.RequirePerm("kube:manage"), h.KubePodLogs)
		auth.GET("/kube/clusters/:id/events", h.KubeEvents)
		auth.GET("/kube/resource-kinds", h.KubeResourceKinds)
		auth.GET("/kube/clusters/:id/resources", h.KubeResources)
		auth.GET("/kube/clusters/:id/resource", middleware.RequirePerm("kube:manage"), h.KubeResourceDetail)
		auth.GET("/kube/clusters/:id/resource/events", h.KubeObjectEvents)
		auth.GET("/kube/clusters/:id/resource/pods", h.KubeRelatedPods)
		auth.GET("/kube/clusters/:id/pod", h.KubePodDetail)
		auth.GET("/kube/clusters/:id/capacity", h.KubeCapacity)
		// 按类型的专用页，全部只读：Helm 应用、自定义资源、Gateway API、RBAC 账户
		auth.GET("/kube/clusters/:id/helm-releases", h.KubeHelmReleases)
		auth.GET("/kube/clusters/:id/crds", h.KubeCRDs)
		auth.GET("/kube/clusters/:id/crd-resources", h.KubeCRDResources)
		auth.GET("/kube/clusters/:id/crd-resource", middleware.RequirePerm("kube:manage"), h.KubeCRDResourceDetail)
		auth.GET("/kube/clusters/:id/gateway-routes", h.KubeGatewayRoutes)
		auth.GET("/kube/clusters/:id/rbac", middleware.RequirePerm("kube:manage"), h.KubeRBAC)
		auth.GET("/kube/clusters/:id/node-inventory", h.KubeNodeInventory)
		auth.GET("/kube/clusters/:id/namespace-inventory", h.KubeNamespaceInventory)
		auth.POST("/kube/clusters/:id/resource/apply", middleware.RequirePerm("kube:write"), h.ApplyKubeResource)
		auth.POST("/kube/clusters/:id/resource/scale", middleware.RequirePerm("kube:write"), h.ScaleKubeResource)
		auth.POST("/kube/clusters/:id/resource/restart", middleware.RequirePerm("kube:write"), h.RestartKubeWorkload)
		auth.GET("/kube/change-logs", middleware.RequirePerm("kube:manage"), h.ListKubeChangeLogs)
		auth.GET("/kube/forwards", middleware.RequirePerm("kube:forward"), h.ListKubeForwards)
		auth.POST("/kube/forwards", middleware.RequirePerm("kube:forward"), h.CreateKubeForward)
		auth.DELETE("/kube/forwards/:id", middleware.RequirePerm("kube:forward"), h.CloseKubeForward)

		auth.GET("/ai/upstreams", middleware.RequirePerm("model:manage"), h.ListModelUpstreams)
		auth.POST("/ai/upstreams", middleware.RequirePerm("model:manage"), h.CreateModelUpstream)
		auth.PUT("/ai/upstreams/:id", middleware.RequirePerm("model:manage"), h.UpdateModelUpstream)
		auth.DELETE("/ai/upstreams/:id", middleware.RequirePerm("model:manage"), h.DeleteModelUpstream)
		auth.POST("/ai/upstreams/:id/check", middleware.RequirePerm("model:manage"), h.CheckModelUpstream)
		// 网关入口：调用方按 Alias 请求，平台转发到池子里的上游
		auth.POST("/ai/chat/completions", middleware.RequirePerm("model:call"), h.ChatCompletion)
		auth.GET("/ai/calls", middleware.RequirePerm("model:manage"), h.ListModelCalls)
		auth.GET("/ai/calls/export", middleware.RequirePerm("model:manage"), h.ExportModelCalls)
		auth.GET("/ai/usage", middleware.RequirePerm("model:manage"), h.ModelUsage)

		auth.GET("/ai/agents", h.ListAgentConfigs)
		auth.POST("/ai/agents", middleware.RequirePerm("agent:manage"), h.CreateAgentConfig)
		auth.PUT("/ai/agents/:id", middleware.RequirePerm("agent:manage"), h.UpdateAgentConfig)
		auth.DELETE("/ai/agents/:id", middleware.RequirePerm("agent:manage"), h.DeleteAgentConfig)
		auth.POST("/ai/agents/:id/run", middleware.RequirePerm("agent:run"), h.RunAgent)
		auth.GET("/ai/agent-runs", h.ListAgentRuns)
		auth.GET("/ai/agent-runs/:id", h.GetAgentRun)

		auth.GET("/system/im/apps", middleware.RequirePerm("im:manage"), h.ListImApps)
		auth.POST("/system/im/apps", middleware.RequirePerm("im:manage"), h.CreateImApp)
		auth.PUT("/system/im/apps/:id", middleware.RequirePerm("im:manage"), h.UpdateImApp)
		auth.DELETE("/system/im/apps/:id", middleware.RequirePerm("im:manage"), h.DeleteImApp)
		auth.POST("/system/im/apps/:id/check", middleware.RequirePerm("im:manage"), h.CheckImApp)
		auth.POST("/system/im/apps/:id/sync", middleware.RequirePerm("im:sync"), h.SyncImApp)
		auth.GET("/system/im/sync-runs", middleware.RequirePerm("im:manage"), h.ListImSyncRuns)
		auth.GET("/system/im/accounts", middleware.RequirePerm("im:manage"), h.ListImAccounts)
		auth.DELETE("/system/im/accounts/:id", middleware.RequirePerm("im:manage"), h.UnbindImAccount)

		// LDAP / AD 账号接入
		auth.GET("/system/ldap/servers", middleware.RequirePerm("ldap:manage"), h.ListLdapServers)
		auth.POST("/system/ldap/servers", middleware.RequirePerm("ldap:manage"), h.CreateLdapServer)
		auth.PUT("/system/ldap/servers/:id", middleware.RequirePerm("ldap:manage"), h.UpdateLdapServer)
		auth.DELETE("/system/ldap/servers/:id", middleware.RequirePerm("ldap:manage"), h.DeleteLdapServer)
		auth.POST("/system/ldap/servers/:id/check", middleware.RequirePerm("ldap:manage"), h.CheckLdapServer)
		auth.GET("/system/ldap/servers/:id/users", middleware.RequirePerm("ldap:manage"), h.SearchLdapUsers)
		auth.POST("/system/ldap/try-login", middleware.RequirePerm("ldap:manage"), h.TryLdapLogin)
		auth.GET("/system/ldap/accounts", middleware.RequirePerm("ldap:manage"), h.ListLdapAccounts)
		auth.POST("/system/ldap/accounts", middleware.RequirePerm("ldap:manage"), h.BindLdapAccount)
		auth.DELETE("/system/ldap/accounts/:id", middleware.RequirePerm("ldap:manage"), h.UnbindLdapAccount)

		// API 令牌（服务账号）
		auth.GET("/me/whoami", h.WhoAmI)
		auth.GET("/system/api-tokens", middleware.RequirePerm("token:manage"), h.ListApiTokens)
		auth.GET("/system/api-tokens/scopes", middleware.RequirePerm("token:manage"), h.ListApiTokenScopes)
		auth.POST("/system/api-tokens", middleware.RequirePerm("token:manage"), h.CreateApiToken)
		auth.PUT("/system/api-tokens/:id", middleware.RequirePerm("token:manage"), h.UpdateApiToken)
		auth.POST("/system/api-tokens/:id/rotate", middleware.RequirePerm("token:manage"), h.RotateApiToken)
		auth.POST("/system/api-tokens/:id/revoke", middleware.RequirePerm("token:manage"), h.RevokeApiToken)
		auth.DELETE("/system/api-tokens/:id", middleware.RequirePerm("token:manage"), h.DeleteApiToken)

		auth.GET("/monitor/health", h.PlatformHealth)

		auth.GET("/monitor/probes", h.ListProbes)
		auth.POST("/monitor/probes", middleware.RequirePerm("probe:manage"), h.CreateProbe)
		auth.PUT("/monitor/probes/:id", middleware.RequirePerm("probe:manage"), h.UpdateProbe)
		auth.DELETE("/monitor/probes/:id", middleware.RequirePerm("probe:manage"), h.DeleteProbe)
		auth.POST("/monitor/probes/:id/run", middleware.RequirePerm("probe:manage"), h.RunProbe)
		auth.GET("/monitor/probe-records", h.ListProbeRecords)

		auth.GET("/monitor/exposures", h.ListExposureTargets)
		auth.POST("/monitor/exposures", middleware.RequirePerm("exposure:manage"), h.CreateExposureTarget)
		auth.PUT("/monitor/exposures/:id", middleware.RequirePerm("exposure:manage"), h.UpdateExposureTarget)
		auth.DELETE("/monitor/exposures/:id", middleware.RequirePerm("exposure:manage"), h.DeleteExposureTarget)
		auth.POST("/monitor/exposures/:id/scan", middleware.RequirePerm("exposure:manage"), h.ScanExposureTarget)
		auth.GET("/monitor/exposure-scans", h.ListExposureScans)

		auth.GET("/monitor/metric-sources", middleware.RequirePerm("source:manage"), h.ListMetricSources)
		auth.POST("/monitor/metric-sources", middleware.RequirePerm("metric:manage"), h.CreateMetricSource)
		auth.PUT("/monitor/metric-sources/:id", middleware.RequirePerm("metric:manage"), h.UpdateMetricSource)
		auth.DELETE("/monitor/metric-sources/:id", middleware.RequirePerm("metric:manage"), h.DeleteMetricSource)
		auth.POST("/monitor/metric-sources/:id/check", middleware.RequirePerm("metric:manage"), h.CheckMetricSource)
		auth.GET("/monitor/metrics/query", h.QueryMetricInstant)
		auth.GET("/monitor/metrics/query-range", h.QueryMetricRange)
		auth.GET("/monitor/metrics/names", h.ListMetricNames)
		auth.GET("/monitor/metrics/saved", h.ListSavedMetricQueries)
		auth.POST("/monitor/metrics/saved", middleware.RequirePerm("metric:manage"), h.CreateSavedMetricQuery)
		auth.DELETE("/monitor/metrics/saved/:id", middleware.RequirePerm("metric:manage"), h.DeleteSavedMetricQuery)

		auth.GET("/monitor/log-sources", middleware.RequirePerm("source:manage"), h.ListLogSources)
		auth.POST("/monitor/log-sources", middleware.RequirePerm("log:manage"), h.CreateLogSource)
		auth.PUT("/monitor/log-sources/:id", middleware.RequirePerm("log:manage"), h.UpdateLogSource)
		auth.DELETE("/monitor/log-sources/:id", middleware.RequirePerm("log:manage"), h.DeleteLogSource)
		auth.POST("/monitor/log-sources/:id/check", middleware.RequirePerm("log:manage"), h.CheckLogSource)
		auth.GET("/monitor/logs/query", h.QueryLogs)
		auth.GET("/monitor/logs/labels", h.ListLogLabels)

		auth.GET("/monitor/trace-sources", middleware.RequirePerm("source:manage"), h.ListTraceSources)
		auth.POST("/monitor/trace-sources", middleware.RequirePerm("trace:manage"), h.CreateTraceSource)
		auth.PUT("/monitor/trace-sources/:id", middleware.RequirePerm("trace:manage"), h.UpdateTraceSource)
		auth.DELETE("/monitor/trace-sources/:id", middleware.RequirePerm("trace:manage"), h.DeleteTraceSource)
		auth.POST("/monitor/trace-sources/:id/check", middleware.RequirePerm("trace:manage"), h.CheckTraceSource)
		auth.GET("/monitor/traces/services", h.ListTraceServices)
		auth.GET("/monitor/traces/search", h.SearchTraces)
		auth.GET("/monitor/traces/detail/:traceId", h.GetTrace)

		// 主机日志：不依赖 Loki，直接 SSH 读主机上的文件。全部是只读命令。
		auth.GET("/monitor/host-logs/meta", h.HostLogMeta)
		auth.POST("/monitor/host-logs/view", middleware.RequirePerm("hostlog:view"), h.ViewHostLog)
		auth.GET("/monitor/host-log-targets", middleware.RequirePerm("hostlog:view"), h.ListHostLogTargets)
		auth.POST("/monitor/host-log-targets", middleware.RequirePerm("hostlog:manage"), h.CreateHostLogTarget)
		auth.PUT("/monitor/host-log-targets/:id", middleware.RequirePerm("hostlog:manage"), h.UpdateHostLogTarget)
		auth.DELETE("/monitor/host-log-targets/:id", middleware.RequirePerm("hostlog:manage"), h.DeleteHostLogTarget)
		auth.POST("/monitor/host-log-targets/:id/scan", middleware.RequirePerm("hostlog:manage"), h.ScanHostLogTarget)
		auth.POST("/monitor/host-log-targets/scan-all", middleware.RequirePerm("hostlog:manage"), h.ScanAllHostLogTargets)
		auth.GET("/monitor/host-log-scans", middleware.RequirePerm("hostlog:view"), h.ListHostLogScans)
		auth.GET("/monitor/log-usage", middleware.RequirePerm("hostlog:view"), h.ListLogUsage)
		auth.POST("/monitor/log-usage/collect", middleware.RequirePerm("hostlog:manage"), h.CollectLogUsage)

		auth.GET("/monitor/aggregation/dimensions", h.ListAggregationDimensions)
		auth.GET("/monitor/aggregation/overlaps", h.DetectAggregationOverlaps)
		auth.GET("/monitor/aggregation", h.ListAggregationPolicies)
		auth.POST("/monitor/aggregation", middleware.RequirePerm("aggregation:manage"), h.CreateAggregationPolicy)
		auth.PUT("/monitor/aggregation/:id", middleware.RequirePerm("aggregation:manage"), h.UpdateAggregationPolicy)
		auth.DELETE("/monitor/aggregation/:id", middleware.RequirePerm("aggregation:manage"), h.DeleteAggregationPolicy)
		auth.GET("/monitor/aggregation/:id/preview", h.PreviewAggregation)

		// 告警静默 / 维护窗口：只拦外发通知，告警照常入库
		auth.GET("/monitor/silences", h.ListAlertSilences)
		auth.POST("/monitor/silences", middleware.RequirePerm("silence:manage"), h.CreateAlertSilence)
		auth.POST("/monitor/silences/preview", h.PreviewAlertSilence)
		auth.PUT("/monitor/silences/:id", middleware.RequirePerm("silence:manage"), h.UpdateAlertSilence)
		auth.POST("/monitor/silences/:id/end", middleware.RequirePerm("silence:manage"), h.EndAlertSilence)
		auth.DELETE("/monitor/silences/:id", middleware.RequirePerm("silence:manage"), h.DeleteAlertSilence)
		auth.GET("/monitor/silences/:id/hits", h.ListAlertSilenceHits)

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

		// 事件复盘：复盘本体、证据汇聚、改进项跟踪
		auth.GET("/monitor/events/:id/review", h.GetEventReview)
		auth.GET("/monitor/events/:id/review/export", h.ExportEventReview)
		auth.GET("/monitor/events/:id/evidence", h.GetEventEvidence)
		auth.POST("/monitor/events/:id/review", middleware.RequirePerm("review:manage"), h.SaveEventReview)
		auth.POST("/monitor/events/:id/review/archive", middleware.RequirePerm("review:manage"), h.ArchiveEventReview)
		auth.POST("/monitor/events/:id/review/reopen", middleware.RequirePerm("review:manage"), h.ReopenEventReview)
		auth.POST("/monitor/events/:id/action-items", middleware.RequirePerm("review:manage"), h.CreateActionItem)
		auth.GET("/monitor/reviews", h.ListReviews)
		auth.GET("/monitor/reviews/stats", h.ReviewStats)
		auth.GET("/monitor/action-items", h.ListActionItems)
		auth.PUT("/monitor/action-items/:id", middleware.RequirePerm("review:manage"), h.UpdateActionItem)
		auth.POST("/monitor/action-items/:id/done", middleware.RequirePerm("review:manage"), h.FinishActionItem)
		auth.DELETE("/monitor/action-items/:id", middleware.RequirePerm("review:manage"), h.DeleteActionItem)

		// 处置剧本：按告警/事件推荐、使用留痕、从复盘沉淀草稿
		auth.GET("/monitor/runbooks", h.ListRunbooks)
		auth.GET("/monitor/runbooks/stats", h.RunbookStats)
		auth.GET("/monitor/runbooks/match", h.MatchRunbooks)
		auth.GET("/monitor/runbooks/:id", h.GetRunbook)
		auth.POST("/monitor/runbooks", middleware.RequirePerm("runbook:manage"), h.CreateRunbook)
		auth.PUT("/monitor/runbooks/:id", middleware.RequirePerm("runbook:manage"), h.UpdateRunbook)
		auth.DELETE("/monitor/runbooks/:id", middleware.RequirePerm("runbook:manage"), h.DeleteRunbook)
		auth.POST("/monitor/runbooks/recheck", middleware.RequirePerm("runbook:manage"), h.RecheckRunbooks)
		auth.POST("/monitor/runbooks/:id/use", middleware.RequirePerm("runbook:manage"), h.UseRunbook)
		auth.GET("/monitor/runbook-uses", h.ListRunbookUses)
		auth.POST("/monitor/runbooks/draft-from-review", middleware.RequirePerm("runbook:manage"), h.DraftRunbookFromReview)

		// 监控设置：事件 SLA 目标与超时提醒
		auth.GET("/monitor/sla-settings", h.GetSLASettings)
		auth.PUT("/monitor/sla-settings", middleware.RequirePerm("sla:manage"), h.UpdateSLASettings)
		auth.POST("/monitor/sla-settings/scan", middleware.RequirePerm("sla:manage"), h.RunEventSLACheck)

		auth.GET("/exec/scripts", h.ListScripts)
		auth.GET("/exec/scripts/categories", h.ListScriptCategories)
		auth.POST("/exec/scripts", middleware.RequirePerm("script:manage"), h.CreateScript)
		auth.PUT("/exec/scripts/:id", middleware.RequirePerm("script:manage"), h.UpdateScript)
		auth.DELETE("/exec/scripts/:id", middleware.RequirePerm("script:manage"), h.DeleteScript)
		auth.POST("/exec/scripts/:id/precheck", h.PrecheckScript)
		auth.POST("/exec/scripts/:id/render", h.RenderScript)
		auth.POST("/exec/scripts/:id/run", middleware.RequirePerm("exec:run"), h.RunScript)

		auth.GET("/hosts/export", middleware.RequirePerm("host:update"), h.ExportHosts)
		auth.GET("/hosts/import-template", h.HostImportTemplate)
		auth.POST("/hosts/import", middleware.RequirePerm("host:create"), h.ImportHosts)

		auth.GET("/system/retention", middleware.RequirePerm("config:manage"), h.RetentionStatus)
		auth.POST("/system/retention/run", middleware.RequirePerm("retention:run"), h.RunRetentionCleanup)
	}

	serveWeb(r, cfg)
	return r
}

// serveWeb 托管前端构建产物。配了 OPS_WEB_DIR 就单进程交付（不用再配 nginx），
// 没配则保持原样：只有 API，前端另行托管。
func serveWeb(r *gin.Engine, cfg *config.Config) {
	if cfg.WebDir == "" {
		return
	}
	index := filepath.Join(cfg.WebDir, "index.html")
	if _, err := os.Stat(index); err != nil {
		log.Fatalf("OPS_WEB_DIR=%s 里没有 index.html，请指向 web/dist 目录", cfg.WebDir)
	}
	if st, err := os.Stat(filepath.Join(cfg.WebDir, "assets")); err == nil && st.IsDir() {
		r.Static("/assets", filepath.Join(cfg.WebDir, "assets"))
	}
	for _, name := range []string{"favicon.ico", "favicon.svg", "robots.txt"} {
		p := filepath.Join(cfg.WebDir, name)
		if _, err := os.Stat(p); err == nil {
			r.StaticFile("/"+name, p)
		}
	}
	// SPA 回落：前端是 history 路由，刷新 /monitor/alerts 这类地址也要拿到 index.html。
	// /api 前缀不回落，否则拼错的接口会收到一坨 HTML 而不是 JSON 404。
	r.NoRoute(func(c *gin.Context) {
		p := c.Request.URL.Path
		if strings.HasPrefix(p, "/api/") {
			response.NotFound(c, "接口不存在")
			return
		}
		if c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead {
			response.NotFound(c, "路径不存在")
			return
		}
		c.File(index)
	})
	log.Printf("前端静态文件由后端托管: %s", cfg.WebDir)
}

// readyz 就绪探针：连得上库、且能查到最基础的表才算就绪。
// 光 Ping 不够 —— SQLite 的 Ping 不碰表，迁移没跑完也会返回成功。
func readyz(c *gin.Context, gormDB *gorm.DB) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
	defer cancel()

	fail := func(reason string) {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"status": "unready", "reason": reason, "version": version,
		})
	}
	sqlDB, err := gormDB.DB()
	if err != nil {
		fail("数据库句柄不可用: " + err.Error())
		return
	}
	if err := sqlDB.PingContext(ctx); err != nil {
		fail("数据库 ping 失败: " + err.Error())
		return
	}
	var n int64
	if err := gormDB.WithContext(ctx).Model(&model.User{}).Count(&n).Error; err != nil {
		fail("用户表不可读（迁移未完成？）: " + err.Error())
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok", "users": n, "version": version})
}
