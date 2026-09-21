package config

import (
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
)

// DevJWTSecret 开发默认密钥。生产模式下用它启动会被直接拒绝。
const DevJWTSecret = "dev-only-jwt-secret-change-me"

// DefaultAdminPassword 内置 admin 的初始口令默认值，生产模式下必须改掉
const DefaultAdminPassword = "Admin@123456"

// Config 运行配置，全部来自环境变量，便于本地与容器部署共用。
type Config struct {
	// Env 运行模式：dev（默认，宽松）或 prod。prod 下不安全的配置会让进程拒绝启动，
	// 而不是打一行 warn 就放行 —— 「不改也能跑起来」是上线事故的常见来源。
	Env string

	Addr         string
	DSN          string
	JWTSecret    []byte
	TokenTTLHour int
	AllowOrigins []string
	AdminInitPwd string
	Debug        bool

	// TrustedProxies 可信反向代理的 IP / CIDR 列表。留空表示谁都不信：
	// 此时来源 IP 一律取 TCP 对端地址，X-Forwarded-For 被忽略（免得审计里的 IP 被伪造）。
	// 部署在 nginx 后面时要把 nginx 的地址填进来，否则审计记的是反代的 IP。
	TrustedProxies []string

	// WebDir 前端构建产物（web/dist）目录。非空时后端直接托管这些静态文件并做
	// SPA 回落，单进程即可交付；留空则需要另配 nginx 之类托管前端。
	WebDir string

	// RecordDir 会话录像存放目录（asciinema .cast 文件）
	RecordDir string

	// ShutdownTimeoutSec 优雅退出的最长等待秒数：先停调度与隧道、再等在跑的请求收尾，
	// 超时就强制结束（批量执行是同步请求，等待值太小会把作业截断）。
	ShutdownTimeoutSec int

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

	// SecretKey 凭证库的字段加密密钥（AES-GCM，任意长度口令派生）。留空表示凭据明文存库，
	// 界面上会如实标出来 —— 不能让人以为「有凭证库就等于加密了」。
	// 一旦设过就不要改：改了之后已有密文全部解不开（平台会明确报错而不是当口令错误）。
	SecretKey string

	// ---------- 备份 ----------

	// BackupSpec 自动备份的 cron 表达式。留空表示不自动备份（只能手动跑
	// `ops backup`）。默认 03:00，早于数据留存清理的 03:30 —— 清理是真删且不可逆，
	// 先备份再清理才有回头路。
	BackupSpec string
	// BackupDir 备份落盘目录
	BackupDir string
	// BackupKeep 保留最近多少份备份，超出的按时间从旧到新删除
	BackupKeep int

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

// IsProd 是否生产模式
func (c *Config) IsProd() bool { return c.Env == "prod" }

// Load 读取环境变量。返回的 error 是「配置本身不合法」（比如数字项写了字母），
// 与生产模式下的安全校验（Validate）分开：前者任何模式都不该放过。
func Load() (*Config, error) {
	var errs []string
	fail := func(format string, args ...any) {
		errs = append(errs, fmt.Sprintf(format, args...))
	}
	// envInt / envBool 解析失败时记错误而不是静默用默认值：
	// 配错了 cron 或端口却按默认值跑起来，比起不启动更难排查。
	envInt := func(key string, def int) int {
		v := strings.TrimSpace(os.Getenv(key))
		if v == "" {
			return def
		}
		n, err := strconv.Atoi(v)
		if err != nil {
			fail("%s=%q 不是整数", key, v)
			return def
		}
		return n
	}
	envBool := func(key string, def bool) bool {
		v := strings.TrimSpace(os.Getenv(key))
		if v == "" {
			return def
		}
		b, err := strconv.ParseBool(v)
		if err != nil {
			fail("%s=%q 不是布尔值（可用 true/false/1/0）", key, v)
			return def
		}
		return b
	}

	mode := strings.ToLower(strings.TrimSpace(env("OPS_ENV", "dev")))
	if mode != "dev" && mode != "prod" {
		fail("OPS_ENV=%q 无效，只能是 dev 或 prod", mode)
		mode = "dev"
	}

	cfg := &Config{
		Env:          mode,
		Addr:         env("OPS_ADDR", ":8080"),
		DSN:          env("OPS_DSN", "ops.db"),
		TokenTTLHour: envInt("OPS_TOKEN_TTL_HOUR", 12),
		AllowOrigins: splitList(env("OPS_ALLOW_ORIGINS", "http://localhost:5173")),
		AdminInitPwd: env("OPS_ADMIN_PASSWORD", DefaultAdminPassword),
		// 调试开关跟着模式走：dev 默认开，prod 默认关
		Debug:              envBool("OPS_DEBUG", mode == "dev"),
		TrustedProxies:     splitList(env("OPS_TRUSTED_PROXIES", "")),
		WebDir:             strings.TrimSpace(env("OPS_WEB_DIR", "")),
		RecordDir:          env("OPS_RECORD_DIR", "recordings"),
		ShutdownTimeoutSec: envInt("OPS_SHUTDOWN_TIMEOUT_SEC", 30),
		SSHStrictHostKey:   envBool("OPS_SSH_STRICT_HOST_KEY", false),
		CertCheckSpec:      strings.TrimSpace(env("OPS_CERT_CHECK_SPEC", "0 8 * * *")),
		AlertRuleSpec:      strings.TrimSpace(env("OPS_ALERT_RULE_SPEC", "*/5 * * * *")),
		ProbeSpec:          strings.TrimSpace(env("OPS_PROBE_SPEC", "*/5 * * * *")),
		RetentionSpec:      strings.TrimSpace(env("OPS_RETENTION_SPEC", "30 3 * * *")),
		DetectionSpec:      strings.TrimSpace(env("OPS_DETECTION_SPEC", "*/5 * * * *")),
		KubeCheckSpec:      strings.TrimSpace(env("OPS_KUBE_CHECK_SPEC", "*/5 * * * *")),
		HostMetricSpec:     strings.TrimSpace(env("OPS_HOST_METRIC_SPEC", "*/5 * * * *")),
		OnCallSpec:         strings.TrimSpace(env("OPS_ONCALL_SPEC", "* * * * *")),
		ExposureSpec:       strings.TrimSpace(env("OPS_EXPOSURE_SPEC", "20 4 * * *")),

		SecretKey: strings.TrimSpace(env("OPS_SECRET_KEY", "")),

		BackupSpec: strings.TrimSpace(env("OPS_BACKUP_SPEC", "0 3 * * *")),		BackupDir:  strings.TrimSpace(env("OPS_BACKUP_DIR", "backups")),
		BackupKeep: envInt("OPS_BACKUP_KEEP", 7),

		ForwardBind:       strings.TrimSpace(env("OPS_FORWARD_BIND", "0.0.0.0")),
		ForwardPortMin:    envInt("OPS_FORWARD_PORT_MIN", 30000),
		ForwardPortMax:    envInt("OPS_FORWARD_PORT_MAX", 30099),
		ForwardMax:        envInt("OPS_FORWARD_MAX", 8),
		ForwardTTLMinutes: envInt("OPS_FORWARD_TTL_MINUTES", 120),
		ForwardConnMax:    envInt("OPS_FORWARD_CONN_MAX", 32),
	}

	if cfg.TokenTTLHour < 1 || cfg.TokenTTLHour > 24*30 {
		fail("OPS_TOKEN_TTL_HOUR=%d 不合理，取值范围 1-720 小时", cfg.TokenTTLHour)
	}
	if cfg.ShutdownTimeoutSec < 1 || cfg.ShutdownTimeoutSec > 600 {
		fail("OPS_SHUTDOWN_TIMEOUT_SEC=%d 不合理，取值范围 1-600 秒", cfg.ShutdownTimeoutSec)
	}
	if cfg.BackupSpec != "" && cfg.BackupDir == "" {
		fail("开了自动备份（OPS_BACKUP_SPEC）就必须给 OPS_BACKUP_DIR")
	}
	if cfg.BackupKeep < 1 || cfg.BackupKeep > 365 {
		fail("OPS_BACKUP_KEEP=%d 不合理，取值范围 1-365 份", cfg.BackupKeep)
	}
	if cfg.ForwardPortMin < 1 || cfg.ForwardPortMax > 65535 || cfg.ForwardPortMin > cfg.ForwardPortMax {
		fail("转发端口区间 %d-%d 不合法", cfg.ForwardPortMin, cfg.ForwardPortMax)
	}

	if s := os.Getenv("OPS_JWT_SECRET"); s != "" {
		cfg.JWTSecret = []byte(s)
	} else {
		cfg.JWTSecret = []byte(DevJWTSecret)
	}

	if len(errs) > 0 {
		return nil, fmt.Errorf("配置不合法:\n  - %s", strings.Join(errs, "\n  - "))
	}
	return cfg, nil
}

// Validate 生产模式下的安全底线检查。dev 模式只打 warn，prod 模式返回错误让进程别起来。
func (c *Config) Validate() error {
	type issue struct {
		fatal bool // prod 下是否阻断启动
		text  string
	}
	var found []issue
	add := func(fatal bool, format string, args ...any) {
		found = append(found, issue{fatal: fatal, text: fmt.Sprintf(format, args...)})
	}

	if string(c.JWTSecret) == DevJWTSecret {
		add(true, "OPS_JWT_SECRET 未设置，用的是仓库里公开的开发默认值 —— 任何人都能伪造任意用户的登录令牌")
	} else if len(c.JWTSecret) < 32 {
		add(true, "OPS_JWT_SECRET 只有 %d 字节，太短了，建议 32 字节以上随机串（openssl rand -base64 48）", len(c.JWTSecret))
	}
	if c.AdminInitPwd == DefaultAdminPassword {
		add(true, "OPS_ADMIN_PASSWORD 还是默认口令 %s，首次初始化会用它建 admin", DefaultAdminPassword)
	}
	if c.Debug {
		add(true, "OPS_DEBUG 开着：会输出含参数的 SQL 日志并启用 gin 调试模式")
	}
	if len(c.AllowOrigins) == 0 {
		add(true, "OPS_ALLOW_ORIGINS 为空：CORS 与 WebSocket 的 Origin 校验没有白名单可用")
	}
	for _, o := range c.AllowOrigins {
		if o == "*" {
			add(true, "OPS_ALLOW_ORIGINS 里有 *，配合 AllowCredentials 等于对所有站点开放")
		}
		if strings.Contains(o, "localhost") || strings.Contains(o, "127.0.0.1") {
			add(true, "OPS_ALLOW_ORIGINS 里还留着本机地址 %s，看起来是开发配置", o)
		}
	}
	// 以下是「强烈建议」而不是「必须」：内网自用场景确实可能有意这么配
	if !c.SSHStrictHostKey {
		add(false, "SSH 主机指纹校验已关闭（OPS_SSH_STRICT_HOST_KEY=true 开启）")
	}
	if c.ForwardBind == "0.0.0.0" {
		add(false, "集群服务转发监听 0.0.0.0，隧道本身不做认证；只给平台本机用可设 OPS_FORWARD_BIND=127.0.0.1")
	}
	if len(c.TrustedProxies) == 0 {
		add(false, "OPS_TRUSTED_PROXIES 为空：X-Forwarded-For 一律忽略。部署在反代后面要填反代地址，否则审计记录的是反代 IP")
	}
	if c.BackupSpec == "" {
		add(false, "自动备份未启用（OPS_BACKUP_SPEC 为空），而数据留存清理是真删且不可逆")
	}
	if c.WebDir == "" {
		add(false, "OPS_WEB_DIR 为空：后端不托管前端静态文件，需要另配 nginx 之类")
	}
	if c.SecretKey == "" {
		add(false, "OPS_SECRET_KEY 未设置：凭证库里的口令与私钥以明文落库（页面上会标出来）")
	} else if len(c.SecretKey) < 16 {
		add(false, "OPS_SECRET_KEY 只有 %d 字节，偏短，建议 32 字节以上随机串", len(c.SecretKey))
	}

	var fatals []string
	for _, f := range found {
		if f.fatal && c.IsProd() {
			fatals = append(fatals, f.text)
			continue
		}
		log.Printf("[warn] %s", f.text)
	}
	// 主机凭据当前以明文存库，属于已知并接受的风险，详见 docs/SECURITY.md
	log.Println("[warn] 主机登录凭据以明文存储于数据库，请严格控制数据库文件与备份的访问权限")

	if len(fatals) > 0 {
		return fmt.Errorf("生产模式（OPS_ENV=prod）安全检查未通过:\n  - %s\n修好上面这些再启动；"+
			"确实要带着这些配置跑，就把 OPS_ENV 设回 dev（自负风险）", strings.Join(fatals, "\n  - "))
	}
	return nil
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// splitList 逗号分隔列表，去空白并丢掉空项
func splitList(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if s := strings.TrimSpace(p); s != "" {
			out = append(out, s)
		}
	}
	return out
}
