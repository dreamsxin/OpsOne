package handler

// 布尔默认值陷阱的行为测试。
//
// model 包里那个 AST 测试守的是「别再加这个标签」；这一组守的是「用户关掉的开关
// 真的关掉了」—— 走真实的创建接口，然后直接读库看那一列到底是什么。
//
// 为什么要走接口而不是直接 Create：这个 bug 只在 GORM 的 Create 路径上出现，
// 而每个模块的 handler 都有自己一套「先默认 true 再按 *bool 覆盖」的组装逻辑，
// 只有连着一起跑才能证明这条链路是通的。

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"ops-platform/server/internal/config"
	"ops-platform/server/internal/model"
)

func newBoolDefaultTestHandler(t *testing.T) (*Handler, *gin.Engine) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	dir := t.TempDir()
	g, err := gorm.Open(sqlite.Open(dir+"/booldefault.db"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	if err := g.AutoMigrate(
		&model.Certificate{}, &model.Probe{}, &model.ProbeRecord{}, &model.ExposureTarget{},
		&model.LdapServer{}, &model.Alert{}, &model.AlertSource{},
		&model.User{}, &model.Role{}, &model.SysConfig{},
	); err != nil {
		t.Fatalf("建表失败: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, err := g.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})

	h := New(g, &config.Config{})
	engine := gin.New()
	engine.Use(func(c *gin.Context) {
		c.Set("ctx_user", &model.User{ID: 1, Username: "admin"})
		c.Set("ctx_perms", map[string]struct{}{})
		c.Next()
	})
	engine.POST("/security/certificates", h.CreateCertificate)
	engine.POST("/monitor/probes", h.CreateProbe)
	engine.POST("/security/exposures", h.CreateExposureTarget)
	engine.POST("/system/ldap-servers", h.CreateLdapServer)
	return h, engine
}

func boolPost(t *testing.T, engine *gin.Engine, path, body string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	engine.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

// 证书：建的时候就把巡检与告警关掉，两个开关都要真的是关的
func TestCertificateDisabledOnCreateSticks(t *testing.T) {
	h, engine := newBoolDefaultTestHandler(t)
	code, raw := boolPost(t, engine, "/security/certificates",
		`{"name":"内网证书","domain":"internal.corp","port":443,"enabled":false,"alertEnabled":false}`)
	if code != 200 {
		t.Fatalf("创建失败: %d %s", code, raw)
	}

	var item model.Certificate
	if err := h.DB.First(&item, 1).Error; err != nil {
		t.Fatalf("读回失败: %v", err)
	}
	if item.Enabled {
		t.Error("enabled=false 被翻回了 true（gorm default 陷阱）")
	}
	if item.AlertEnabled {
		t.Error("alertEnabled=false 被翻回了 true")
	}

	// 不传这两个字段时仍然要默认开启 —— 去掉标签不能把默认值也弄丢
	if code, raw := boolPost(t, engine, "/security/certificates",
		`{"name":"默认证书","domain":"default.corp","port":443}`); code != 200 {
		t.Fatalf("创建失败: %s", raw)
	}
	var second model.Certificate
	h.DB.First(&second, 2)
	if !second.Enabled || !second.AlertEnabled {
		t.Errorf("没传开关时应该默认开启: %+v", second)
	}
}

// 拨测：这一处原来靠「插完再 Updates 一次」绕过，现在靠模型本身
func TestProbeDisabledOnCreateSticks(t *testing.T) {
	h, engine := newBoolDefaultTestHandler(t)
	// 目标指向一个必然连不上的地址，避免测试去打真网络
	code, raw := boolPost(t, engine, "/monitor/probes",
		`{"name":"内部健康检查","type":"http","target":"http://127.0.0.1:1/healthz",
		  "timeoutSec":1,"enabled":false,"alertEnabled":false}`)
	if code != 200 {
		t.Fatalf("创建失败: %d %s", code, raw)
	}

	var item model.Probe
	h.DB.First(&item, 1)
	if item.Enabled {
		t.Error("enabled=false 被翻回了 true")
	}
	if item.AlertEnabled {
		t.Error("alertEnabled=false 被翻回了 true")
	}

	if code, raw := boolPost(t, engine, "/monitor/probes",
		`{"name":"默认拨测","type":"tcp","target":"127.0.0.1:1","timeoutSec":1}`); code != 200 {
		t.Fatalf("创建失败: %s", raw)
	}
	var second model.Probe
	h.DB.First(&second, 2)
	if !second.Enabled || !second.AlertEnabled {
		t.Errorf("没传开关时应该默认开启: %+v", second)
	}
}

// 暴露面扫描目标
func TestExposureTargetDisabledOnCreateSticks(t *testing.T) {
	h, engine := newBoolDefaultTestHandler(t)
	code, raw := boolPost(t, engine, "/security/exposures",
		`{"name":"边界机","address":"203.0.113.7","ports":"22,443","enabled":false,"alertEnabled":false}`)
	if code != 200 {
		t.Fatalf("创建失败: %d %s", code, raw)
	}
	var item model.ExposureTarget
	h.DB.First(&item, 1)
	if item.Enabled || item.AlertEnabled {
		t.Errorf("两个开关都该是关的: %+v", item)
	}
}

// LDAP：这一条是安全相关的 —— 「关掉域账号登录」被翻回开启意味着登录入口还开着
func TestLdapLoginDisabledOnCreateSticks(t *testing.T) {
	h, engine := newBoolDefaultTestHandler(t)
	code, raw := boolPost(t, engine, "/system/ldap-servers",
		`{"name":"公司域","host":"dc.corp","port":389,"encryption":"none",
		  "baseDN":"dc=corp","userFilter":"(sAMAccountName=%s)",
		  "enabled":true,"loginEnabled":false}`)
	if code != 200 {
		t.Fatalf("创建失败: %d %s", code, raw)
	}

	var item model.LdapServer
	h.DB.First(&item, 1)
	if !item.Enabled {
		t.Error("enabled=true 却存成了 false")
	}
	if item.LoginEnabled {
		t.Error("loginEnabled=false 被翻回了 true —— 域账号登录入口仍然开着")
	}

	// 反过来：不传 loginEnabled 时保持默认开启
	if code, raw := boolPost(t, engine, "/system/ldap-servers",
		`{"name":"备用域","host":"dc2.corp","port":389,"encryption":"none",
		  "baseDN":"dc=corp","userFilter":"(sAMAccountName=%s)"}`); code != 200 {
		t.Fatalf("创建失败: %s", raw)
	}
	var second model.LdapServer
	h.DB.First(&second, 2)
	if !second.Enabled || !second.LoginEnabled {
		t.Errorf("没传开关时应该默认开启: %+v", second)
	}
}

// 顺带确认响应体里回的也是关掉的状态：前端列表直接用它渲染开关
func TestCreateResponseReflectsDisabled(t *testing.T) {
	_, engine := newBoolDefaultTestHandler(t)
	_, raw := boolPost(t, engine, "/security/certificates",
		`{"name":"内网证书","domain":"internal.corp","port":443,"enabled":false}`)

	var parsed struct {
		Data struct {
			Enabled bool `json:"enabled"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		t.Fatalf("响应不是 JSON: %s", raw)
	}
	if parsed.Data.Enabled {
		t.Fatalf("响应里 enabled 应该是 false（否则界面上开关会一闪又弹回去）: %s", raw)
	}
}
