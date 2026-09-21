package config

import (
	"strings"
	"testing"
)

// 生产模式下这些默认值必须挡住启动：它们全都是「不改也能跑」的坑
func TestValidateProdRejectsUnsafeDefaults(t *testing.T) {
	t.Setenv("OPS_ENV", "prod")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load 失败: %v", err)
	}
	err = cfg.Validate()
	if err == nil {
		t.Fatal("生产模式带着默认密钥与默认口令竟然通过了校验")
	}
	for _, want := range []string{"OPS_JWT_SECRET", "OPS_ADMIN_PASSWORD", "OPS_ALLOW_ORIGINS"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("错误信息里没提到 %s: %v", want, err)
		}
	}
}

// prod 模式下 OPS_DEBUG 默认应当是关的；显式打开要被拒
func TestValidateProdRejectsDebug(t *testing.T) {
	t.Setenv("OPS_ENV", "prod")
	t.Setenv("OPS_JWT_SECRET", strings.Repeat("k", 48))
	t.Setenv("OPS_ADMIN_PASSWORD", "Str0ng-Pwd-For-Test")
	t.Setenv("OPS_ALLOW_ORIGINS", "https://ops.example.com")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load 失败: %v", err)
	}
	if cfg.Debug {
		t.Error("prod 模式下 OPS_DEBUG 未设置时应当默认关闭")
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("配置齐全却没通过校验: %v", err)
	}

	t.Setenv("OPS_DEBUG", "true")
	cfg, err = Load()
	if err != nil {
		t.Fatalf("Load 失败: %v", err)
	}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "OPS_DEBUG") {
		t.Fatalf("prod 模式开 DEBUG 应当被拒，实际: %v", err)
	}
}

// 密钥太短同样不行：32 字节以下的 HS256 密钥没有意义
func TestValidateProdRejectsShortSecret(t *testing.T) {
	t.Setenv("OPS_ENV", "prod")
	t.Setenv("OPS_JWT_SECRET", "short-secret")
	t.Setenv("OPS_ADMIN_PASSWORD", "Str0ng-Pwd-For-Test")
	t.Setenv("OPS_ALLOW_ORIGINS", "https://ops.example.com")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load 失败: %v", err)
	}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "字节") {
		t.Fatalf("短密钥应当被拒，实际: %v", err)
	}
}

// CORS 白名单里留着 localhost 是典型的「开发配置上了生产」
func TestValidateProdRejectsLocalhostOrigin(t *testing.T) {
	t.Setenv("OPS_ENV", "prod")
	t.Setenv("OPS_JWT_SECRET", strings.Repeat("k", 48))
	t.Setenv("OPS_ADMIN_PASSWORD", "Str0ng-Pwd-For-Test")
	t.Setenv("OPS_ALLOW_ORIGINS", "https://ops.example.com,http://localhost:5173")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load 失败: %v", err)
	}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "localhost") {
		t.Fatalf("白名单里的 localhost 应当被拒，实际: %v", err)
	}
}

// dev 模式一切照旧：只打 warn，不阻断，本地开箱即用
func TestValidateDevAllowsDefaults(t *testing.T) {
	t.Setenv("OPS_ENV", "dev")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load 失败: %v", err)
	}
	if !cfg.Debug {
		t.Error("dev 模式下 OPS_DEBUG 应当默认开启")
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("dev 模式不该阻断: %v", err)
	}
}

// 数字项写错不能静默用默认值：配错了却按默认跑起来更难排查
func TestLoadRejectsMalformedNumbers(t *testing.T) {
	t.Setenv("OPS_TOKEN_TTL_HOUR", "twelve")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "OPS_TOKEN_TTL_HOUR") {
		t.Fatalf("非法整数应当报错，实际: %v", err)
	}
}

func TestLoadRejectsMalformedBool(t *testing.T) {
	t.Setenv("OPS_SSH_STRICT_HOST_KEY", "yes-please")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "OPS_SSH_STRICT_HOST_KEY") {
		t.Fatalf("非法布尔值应当报错，实际: %v", err)
	}
}

func TestLoadRejectsBadEnvMode(t *testing.T) {
	t.Setenv("OPS_ENV", "production")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "OPS_ENV") {
		t.Fatalf("OPS_ENV 只认 dev/prod，实际: %v", err)
	}
}

// 取值范围校验：TTL 与优雅退出时长写成 0 或负数都不合理
func TestLoadRejectsOutOfRange(t *testing.T) {
	t.Setenv("OPS_TOKEN_TTL_HOUR", "0")
	if _, err := Load(); err == nil {
		t.Fatal("TTL=0 应当被拒")
	}
	t.Setenv("OPS_TOKEN_TTL_HOUR", "12")
	t.Setenv("OPS_SHUTDOWN_TIMEOUT_SEC", "-1")
	if _, err := Load(); err == nil {
		t.Fatal("优雅退出时长为负应当被拒")
	}
}
