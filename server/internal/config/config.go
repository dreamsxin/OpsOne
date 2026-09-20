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
