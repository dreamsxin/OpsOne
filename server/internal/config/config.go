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
