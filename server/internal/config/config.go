package config

import (
	"log"
	"os"
	"strconv"
	"strings"
)

// Config 运行配置，全部来自环境变量，便于本地与容器部署共用。
type Config struct {
	Addr         string
	DSN          string
	JWTSecret    []byte
	TokenTTLHour int
	AllowOrigins []string
	AdminInitPwd string
	Debug        bool

	// RecordDir 会话录像存放目录（asciinema .cast 文件）
	RecordDir string

	// SSHStrictHostKey 为 true 时校验主机公钥：首次连接记录指纹（TOFU），
	// 之后指纹变化即拒绝连接。默认关闭，便于批量纳管未预置指纹的内网主机。
	SSHStrictHostKey bool

	// CertCheckSpec 证书巡检的 cron 表达式（标准五段）。留空表示不做定时巡检，
	// 只能在界面上手动触发。
	CertCheckSpec string

	// AlertRuleSpec 告警规则评估的 cron 表达式。留空表示不做定时评估，
	// 规则只能在界面上手动试跑。
	AlertRuleSpec string

	// ProbeSpec 拨测执行的 cron 表达式。留空表示不做定时拨测，
	// 只能在界面上手动拨测。所有拨测共用这一个节奏。
	ProbeSpec string

	// RetentionSpec 数据留存清理的 cron 表达式。留空表示不自动清理，
	// 只能在「数据留存」页面手动执行。
	RetentionSpec string

	// DetectionSpec 检测规则评估的 cron 表达式。留空表示不做定时评估，
	// 规则只能在界面上手动试跑。
	DetectionSpec string

	// KubeCheckSpec 容器集群连通性检查的 cron 表达式。留空表示不做定时检查，
	// 只能在界面上手动检查。
	KubeCheckSpec string

	// HostMetricSpec 主机性能采集的 cron 表达式。留空表示不做定时采集，
	// 只能在界面上手动采集。采集走 SSH，节奏太密会给主机和平台都加压。
	HostMetricSpec string

	// OnCallSpec 值班升级扫描的 cron 表达式。留空表示不自动叫人，
	// 只能在界面上手动试跑。升级判定是分钟级的，默认每分钟扫一次。
	OnCallSpec string

	// ExposureSpec 暴露面扫描的 cron 表达式。留空表示不自动扫，
	// 只能在界面上手动扫。扫描会对目标发起大量 TCP 连接，默认每天一次。
	ExposureSpec string

	// ---------- 集群服务转发 ----------

	// ForwardBind 转发隧道的监听地址。默认 0.0.0.0（同网段的人都连得上），
	// 想只给平台本机用就设 127.0.0.1。隧道本身不做认证，见 docs/SECURITY.md。
	ForwardBind string
	// ForwardPortMin / ForwardPortMax 允许占用的监听端口区间（含两端）
	ForwardPortMin int
	ForwardPortMax int
	// ForwardMax 同时存在的隧道数上限
	ForwardMax int
	// ForwardTTLMinutes 隧道存活时长上限（分钟），到点自动关闭，防止忘了关
	ForwardTTLMinutes int
	// ForwardConnMax 单条隧道允许的并发连接数上限：每条连接都要向 API Server
	// 单开一条 WebSocket，不设上限会把 API Server 当成压测目标
	ForwardConnMax int
}

func Load() *Config {
	cfg := &Config{
		Addr:             env("OPS_ADDR", ":8080"),
		DSN:              env("OPS_DSN", "ops.db"),
		TokenTTLHour:     envInt("OPS_TOKEN_TTL_HOUR", 12),
		AllowOrigins:     strings.Split(env("OPS_ALLOW_ORIGINS", "http://localhost:5173"), ","),
		AdminInitPwd:     env("OPS_ADMIN_PASSWORD", "Admin@123456"),
		Debug:            env("OPS_DEBUG", "true") == "true",
		RecordDir:        env("OPS_RECORD_DIR", "recordings"),
		SSHStrictHostKey: env("OPS_SSH_STRICT_HOST_KEY", "false") == "true",
		CertCheckSpec:    strings.TrimSpace(env("OPS_CERT_CHECK_SPEC", "0 8 * * *")),
		AlertRuleSpec:    strings.TrimSpace(env("OPS_ALERT_RULE_SPEC", "*/5 * * * *")),
		ProbeSpec:        strings.TrimSpace(env("OPS_PROBE_SPEC", "*/5 * * * *")),
		RetentionSpec:    strings.TrimSpace(env("OPS_RETENTION_SPEC", "30 3 * * *")),
		DetectionSpec:    strings.TrimSpace(env("OPS_DETECTION_SPEC", "*/5 * * * *")),
		KubeCheckSpec:    strings.TrimSpace(env("OPS_KUBE_CHECK_SPEC", "*/5 * * * *")),
		HostMetricSpec:   strings.TrimSpace(env("OPS_HOST_METRIC_SPEC", "*/5 * * * *")),
		OnCallSpec:       strings.TrimSpace(env("OPS_ONCALL_SPEC", "* * * * *")),
		ExposureSpec:     strings.TrimSpace(env("OPS_EXPOSURE_SPEC", "20 4 * * *")),

		ForwardBind:       strings.TrimSpace(env("OPS_FORWARD_BIND", "0.0.0.0")),
		ForwardPortMin:    envInt("OPS_FORWARD_PORT_MIN", 30000),
		ForwardPortMax:    envInt("OPS_FORWARD_PORT_MAX", 30099),
		ForwardMax:        envInt("OPS_FORWARD_MAX", 8),
		ForwardTTLMinutes: envInt("OPS_FORWARD_TTL_MINUTES", 120),
		ForwardConnMax:    envInt("OPS_FORWARD_CONN_MAX", 32),
	}

	if cfg.ForwardPortMin < 1 || cfg.ForwardPortMax > 65535 || cfg.ForwardPortMin > cfg.ForwardPortMax {
		log.Printf("[warn] 转发端口区间 %d-%d 不合法，回退到 30000-30099",
			cfg.ForwardPortMin, cfg.ForwardPortMax)
		cfg.ForwardPortMin, cfg.ForwardPortMax = 30000, 30099
	}
	if cfg.ForwardBind == "0.0.0.0" {
		log.Println("[warn] 集群服务转发监听 0.0.0.0，隧道本身不做认证，能连到该端口的人等同于能访问被转发的服务；" +
			"可设置 OPS_FORWARD_BIND=127.0.0.1 只给平台本机使用")
	}

	if s := os.Getenv("OPS_JWT_SECRET"); s != "" {
		cfg.JWTSecret = []byte(s)
	} else {
		log.Println("[warn] OPS_JWT_SECRET 未设置，使用开发默认值，请勿用于生产环境")
		cfg.JWTSecret = []byte("dev-only-jwt-secret-change-me")
	}

	// 主机凭据当前以明文存库，属于已知并接受的风险，详见 docs/SECURITY.md
	log.Println("[warn] 主机登录凭据以明文存储于数据库，请严格控制数据库文件与备份的访问权限")

	if !cfg.SSHStrictHostKey {
		log.Println("[warn] SSH 主机指纹校验已关闭，可设置 OPS_SSH_STRICT_HOST_KEY=true 开启")
	}

	return cfg
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}
