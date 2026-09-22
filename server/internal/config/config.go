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

// DefaultSecretKey 演示用的字段加密默认密钥。
//
// 与 DevJWTSecret / DefaultAdminPassword 同一性质：写在仓库里，人人可见，
// 只适合演示与本地开发 —— 所以 prod 模式下带着它启动会被直接拒绝。
// 有这个默认值是为了让「带演示数据的库」开箱即用：演示库里的凭据就是用它加密的。
const DefaultSecretKey = "demo"

// SecretOffValues 显式关闭字段加密的取值。
//
// 默认密钥不为空，所以「留空」不再等于「不加密」，必须有一个显式说法。
// 明文落库是被支持的选择（内网自用、不想额外保管一把密钥），照实标注而不是拦着不让用。
var SecretOffValues = []string{"off", "none", "plain", "disabled"}

// normalizeSecretKey 把显式关闭的写法归一成空串
func normalizeSecretKey(raw string) string {
	v := strings.TrimSpace(raw)
	for _, off := range SecretOffValues {
		if strings.EqualFold(v, off) {
			return ""
		}
	}
	return v
}

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

	// AwarenessSpec 安全意识逾期提醒的 cron 表达式。留空表示不自动催办
	//（必修项仍然可用，只是逾期不会有人被提醒，完成率靠人去翻）。
	AwarenessSpec string

	// ReviewSpec 复盘改进项逾期催办的 cron 表达式。留空表示不自动催办
	//（改进项仍然可以跟踪，只是逾期不会有人被提醒，等于复盘写完就烂在系统里）。
	ReviewSpec string

	// ServiceSpec 主机服务巡检的 cron 表达式。留空表示不自动巡检
	//（纳管的服务仍能手动巡检，只是「该跑的没跑」不会自动被发现）。
	// 每次巡检会对有纳管服务的主机各开一次 SSH，节奏不宜太密。
	ServiceSpec string

	// ConfigSpec 配置文件巡检的 cron 表达式。留空表示不自动巡检
	//（仍能手动巡检，只是「配置被人改了」不会自动被发现）。
	// 每个文件一次 SFTP 读取，节奏不宜太密。
	ConfigSpec string

	// SLASpec 事件 SLA 扫描的 cron 表达式。留空表示不自动提醒
	//（界面上的「距 SLA 还有多久」照样准确，只是超时了不会有人被叫，
	// 等于 SLA 变成一个事后才有人发现的数字）。
	// 只查库不连主机，可以跑得比巡检密。
	SLASpec string

	// SecEventSpec 安全事件采集的 cron 表达式。留空表示不自动采集
	//（研判台上可以手动「立即采集」，但没人点就等于四个来源的流水没人看）。
	// 只读本地库、按 ID 水位推进，跑得密一点也不贵。
	SecEventSpec string

	// CloudSyncSpec 云资源同步的 cron 表达式。**默认留空 = 不自动同步**，
	// 与其它巡检项不同 —— 这一项会真的出网调阿里云 OpenAPI，
	// 默认就开等于替用户决定了「这台机器可以访问公网、也愿意消耗云 API 配额」。
	// 页面上随时能手动「立即同步」，要自动就显式配一个（建议 0 */6 * * *）。
	CloudSyncSpec string

	// DomainSpec 域名巡检（DNS 核对 + 注册到期）的 cron 表达式。
	// 留空表示不做定时巡检，只能在界面上手动触发 —— 那样「解析被人改了」
	// 和「域名快到期了」都只能靠人想起来去点一下。
	DomainSpec string

	// DNSServer 域名核对用的 DNS 服务器（如 223.5.5.5 或 8.8.8.8:53）。
	// 留空表示用系统配置的 resolver。
	//
	// 为什么要能指定：内网 DNS 经常把生产域名覆写到测试地址，用它核对出来的是
	// 内网视角，跟外部用户看到的不是一回事。想核对「外面看到的解析」就指一个公共 DNS。
	DNSServer string

	// HostLogSpec 主机日志巡检（关键字扫描 + 日志目录占用采集）的 cron 表达式。
	// 留空表示不做定时巡检，只能在界面上手动触发 —— 那样日志里新冒出来的 ERROR
	// 和「日志目录快把盘写满了」都只能靠人想起来去点一下。
	// 每个监控点一次 SSH，节奏不宜太密。
	HostLogSpec string

	// LogPathPrefixes 允许读取的日志目录前缀，逗号分隔，默认 `/var/log`。
	//
	// 这是「主机日志」模块唯一真正的安全边界：路径由人在界面上填，
	// 没有这道白名单，有这个页面权限的人就能读主机上任意文件 ——
	// 包括 /etc/shadow 与主机私钥。放开新目录时想清楚这一点。
	LogPathPrefixes string

	// SecretKey 凭据字段的加密密钥（AES-GCM，任意长度口令派生）。
	// 默认值是演示密钥 demo（见 DefaultSecretKey），所以**默认是加密的** ——
	// 这样带演示数据的库文件开箱即可用，prod 模式下带着 demo 启动会被拒绝。
	// 想要明文落库（被支持的选择）用 OPS_SECRET_KEY=off，界面上会标成「不加密存储」。
	// 要换密钥或退回明文，走凭证库页的「更换密钥 / 取消加密」（先备份、再逐行重写、
	// 进程内密钥热切换），不要直接改这个值：直接改等于让已有密文全部解不开。
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
		AwarenessSpec:      strings.TrimSpace(env("OPS_AWARENESS_SPEC", "0 9 * * *")),
		ReviewSpec:         strings.TrimSpace(env("OPS_REVIEW_SPEC", "10 9 * * *")),
		ServiceSpec:        strings.TrimSpace(env("OPS_SERVICE_SPEC", "*/10 * * * *")),
		ConfigSpec:         strings.TrimSpace(env("OPS_CONFIG_SPEC", "*/30 * * * *")),
		SLASpec:            strings.TrimSpace(env("OPS_SLA_SPEC", "*/2 * * * *")),
		SecEventSpec:       strings.TrimSpace(env("OPS_SECEVENT_SPEC", "*/5 * * * *")),
		CloudSyncSpec:      strings.TrimSpace(env("OPS_CLOUD_SYNC_SPEC", "")),
		DomainSpec:         strings.TrimSpace(env("OPS_DOMAIN_SPEC", "0 8 * * *")),
		DNSServer:          strings.TrimSpace(env("OPS_DNS_SERVER", "")),
		HostLogSpec:        strings.TrimSpace(env("OPS_HOST_LOG_SPEC", "*/15 * * * *")),
		LogPathPrefixes:    strings.TrimSpace(env("OPS_LOG_PATH_PREFIXES", "/var/log")),

		SecretKey: normalizeSecretKey(env("OPS_SECRET_KEY", DefaultSecretKey)),

		BackupSpec: strings.TrimSpace(env("OPS_BACKUP_SPEC", "0 3 * * *")), BackupDir: strings.TrimSpace(env("OPS_BACKUP_DIR", "backups")),
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
		add(false, "OPS_SECRET_KEY 显式关闭了加密：凭据以明文落库（这是被允许的配置，页面上会标成「不加密存储」）")
	} else if c.SecretKey == DefaultSecretKey {
		add(true, "OPS_SECRET_KEY 还是演示默认值 %q —— 它写在仓库里，等于没加密；"+
			"换成随机串时不要直接改这个变量，走「凭证库 → 密钥加密体检 → 更换密钥」，否则已有密文解不开", DefaultSecretKey)
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
