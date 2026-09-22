package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"ops-platform/server/internal/config"
	"ops-platform/server/internal/model"
	"ops-platform/server/internal/response"
)

// newKubeGrantTestHandler 装一个只挂「闸门」的引擎。
//
// 刻意不走真的 requireKubeCluster：它会顺带建 K8s 客户端（需要能解析的 kubeconfig），
// 那不是这组测试要验证的东西。路径保持与真实路由一致，因为闸门里要靠
// c.FullPath() 判断这是不是日志 / 写操作接口。
func newKubeGrantTestHandler(t *testing.T, perms ...string) (*Handler, *gin.Engine) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	dir := t.TempDir()
	g, err := gorm.Open(sqlite.Open(dir+"/kubegrant.db"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	if err := g.AutoMigrate(
		&model.KubeGrant{}, &model.KubeCluster{}, &model.User{}, &model.Role{},
		&model.Menu{}, &model.SysConfig{},
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

	permSet := map[string]struct{}{}
	for _, p := range perms {
		permSet[p] = struct{}{}
	}
	engine.Use(func(c *gin.Context) {
		var user model.User
		if h.DB.Preload("Roles").First(&user, 1).Error == nil {
			c.Set("ctx_user", &user)
		}
		c.Set("ctx_perms", permSet)
		c.Next()
	})

	// 闸门探针：与真实路由同名，只跑 kubeGrantCheck
	gate := func(c *gin.Context) {
		var cluster model.KubeCluster
		if err := h.DB.First(&cluster, idParam(c)).Error; err != nil {
			response.NotFound(c, "集群不存在")
			return
		}
		if ok, reason := h.kubeGrantCheck(c, &cluster); !ok {
			response.Forbidden(c, reason)
			return
		}
		response.OK(c, gin.H{"passed": true})
	}
	engine.GET("/kube/clusters/:id/pods", gate)
	engine.GET("/kube/clusters/:id/namespaces", gate)
	engine.GET("/kube/clusters/:id/resources", gate)
	engine.GET("/kube/clusters/:id/pod-logs", gate)
	engine.POST("/kube/clusters/:id/resource/apply", gate)

	engine.GET("/kube/clusters", h.ListKubeClusters)
	engine.GET("/kube/grant-state", h.GetKubeGrantState)
	engine.GET("/kube/grants", h.ListKubeGrants)
	engine.POST("/kube/grants", h.CreateKubeGrant)
	engine.PUT("/kube/grants/:id", h.UpdateKubeGrant)
	engine.DELETE("/kube/grants/:id", h.DeleteKubeGrant)
	engine.GET("/kube/grants/diagnose/:id", h.DiagnoseKubeGrant)
	return h, engine
}

func grantJSON(t *testing.T, engine *gin.Engine, method, path, body string) (int, map[string]any, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	engine.ServeHTTP(rec, req)
	var parsed map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &parsed); err != nil {
		t.Fatalf("响应不是 JSON: %s", rec.Body.String())
	}
	return rec.Code, parsed, rec.Body.String()
}

// seedKubeFixtures 一个用户（带一个角色）+ 两个集群
func seedKubeFixtures(t *testing.T, h *Handler) {
	t.Helper()
	role := model.Role{Name: "运维组", Code: "ops"}
	if err := h.DB.Create(&role).Error; err != nil {
		t.Fatalf("建角色失败: %v", err)
	}
	user := model.User{Username: "zhangsan", Status: 1, Roles: []model.Role{role}}
	if err := h.DB.Create(&user).Error; err != nil {
		t.Fatalf("建用户失败: %v", err)
	}
	h.DB.Create(&model.KubeCluster{Name: "生产集群", Enabled: true})
	h.DB.Create(&model.KubeCluster{Name: "测试集群", Enabled: true})
}

func enableKubeGrant(h *Handler) {
	h.DB.Create(&model.SysConfig{Group: "kube", Key: CfgKubeGrantEnforce, Value: "true", Type: "bool"})
}

// 默认不生效：行为与这一页上线前完全一致
func TestKubeGrantDisabledByDefault(t *testing.T) {
	h, engine := newKubeGrantTestHandler(t)
	seedKubeFixtures(t, h)

	if code, _, raw := grantJSON(t, engine, http.MethodGet, "/kube/clusters/1/pods", ""); code != 200 {
		t.Fatalf("未开启授权时不该被拦: %d %s", code, raw)
	}
	// 日志与写操作同样不受影响
	if code, _, _ := grantJSON(t, engine, http.MethodGet, "/kube/clusters/1/pod-logs", ""); code != 200 {
		t.Fatal("未开启授权时 Pod 日志不该被拦")
	}
	_, parsed, raw := grantJSON(t, engine, http.MethodGet, "/kube/clusters", "")
	list, _ := parsed["data"].([]any)
	if len(list) != 2 {
		t.Fatalf("未开启授权时应能看到全部集群: %s", raw)
	}

	_, parsed, raw = grantJSON(t, engine, http.MethodGet, "/kube/grant-state", "")
	data := parsed["data"].(map[string]any)
	if data["enforced"] != false {
		t.Fatalf("事实条应显示未生效: %s", raw)
	}
	if note, _ := data["activeNote"].(string); !strings.Contains(note, "暂时不影响") {
		t.Fatalf("未生效时要照实说这里配的东西不影响任何人: %v", note)
	}
	if notes, _ := data["notes"].([]any); len(notes) < 7 {
		t.Fatalf("口径说明至少 7 条，实际 %d", len(notes))
	}
}

// 开启后没有授权就完全看不到，且错误信息要说清怎么办
func TestKubeGrantBlocksUngranted(t *testing.T) {
	h, engine := newKubeGrantTestHandler(t)
	seedKubeFixtures(t, h)
	enableKubeGrant(h)

	code, parsed, _ := grantJSON(t, engine, http.MethodGet, "/kube/clusters/1/pods", "")
	if code != 403 {
		t.Fatalf("没有授权应该 403，实际 %d", code)
	}
	msg, _ := parsed["msg"].(string)
	if !strings.Contains(msg, "生产集群") || !strings.Contains(msg, "授权管理") {
		t.Fatalf("错误信息要点名集群并指出去哪里加授权: %v", msg)
	}

	// 集群列表也要跟着空 —— 列出来却一点就 403 更让人困惑
	_, parsed, raw := grantJSON(t, engine, http.MethodGet, "/kube/clusters", "")
	if list, _ := parsed["data"].([]any); len(list) != 0 {
		t.Fatalf("没有任何授权时集群列表应为空: %s", raw)
	}

	// 事实条要数出「几个集群一条授权都没有」
	_, parsed, raw = grantJSON(t, engine, http.MethodGet, "/kube/grant-state", "")
	data := parsed["data"].(map[string]any)
	if data["uncoveredClusters"] != float64(2) {
		t.Fatalf("未覆盖集群数应为 2: %s", raw)
	}
}

// 管理员豁免：否则配错一条授权就能把自己锁死，而这一页本身也在平台里
func TestKubeGrantAdminBypass(t *testing.T) {
	h, engine := newKubeGrantTestHandler(t, kubeAdminPerm)
	seedKubeFixtures(t, h)
	enableKubeGrant(h)

	if code, _, raw := grantJSON(t, engine, http.MethodGet, "/kube/clusters/1/pods", ""); code != 200 {
		t.Fatalf("有 %s 的人不该被拦: %d %s", kubeAdminPerm, code, raw)
	}
	_, parsed, _ := grantJSON(t, engine, http.MethodGet, "/kube/clusters", "")
	if list, _ := parsed["data"].([]any); len(list) != 2 {
		t.Fatal("管理员应能看到全部集群")
	}
}

// 限定了命名空间时，跨命名空间的查询必须被拒绝而不是静默过滤
func TestKubeGrantRejectsCrossNamespace(t *testing.T) {
	h, engine := newKubeGrantTestHandler(t)
	seedKubeFixtures(t, h)
	enableKubeGrant(h)
	h.DB.Create(&model.KubeGrant{
		SubjectType: "user", SubjectID: 1, SubjectName: "zhangsan",
		ClusterID: 1, ClusterName: "生产集群", Namespaces: "app-a,app-b",
	})

	// 不带 namespace = 跨命名空间
	code, parsed, _ := grantJSON(t, engine, http.MethodGet, "/kube/clusters/1/pods", "")
	if code != 403 {
		t.Fatalf("跨命名空间查询应被拒，实际 %d", code)
	}
	msg, _ := parsed["msg"].(string)
	if !strings.Contains(msg, "app-a") || !strings.Contains(msg, "app-b") {
		t.Fatalf("要列出可见的命名空间: %v", msg)
	}
	if !strings.Contains(msg, "静默") {
		t.Fatalf("要说清为什么不静默过滤: %v", msg)
	}

	// 指定了不在清单里的
	code, parsed, _ = grantJSON(t, engine, http.MethodGet, "/kube/clusters/1/pods?namespace=kube-system", "")
	if code != 403 {
		t.Fatalf("越权命名空间应被拒，实际 %d", code)
	}
	if msg, _ := parsed["msg"].(string); !strings.Contains(msg, "kube-system") {
		t.Fatalf("错误信息要点名是哪个命名空间: %v", msg)
	}

	// 清单里的可以过
	if code, _, raw := grantJSON(t, engine, http.MethodGet, "/kube/clusters/1/pods?namespace=app-a", ""); code != 200 {
		t.Fatalf("授权范围内应该通过: %d %s", code, raw)
	}

	// 另一个集群仍然不可见
	if code, _, _ := grantJSON(t, engine, http.MethodGet, "/kube/clusters/2/pods?namespace=app-a", ""); code != 403 {
		t.Fatal("没授权的集群不该因为别的集群有授权而通过")
	}
}

// 不限命名空间的授权可以做跨命名空间查询
func TestKubeGrantAllNamespaces(t *testing.T) {
	h, engine := newKubeGrantTestHandler(t)
	seedKubeFixtures(t, h)
	enableKubeGrant(h)
	h.DB.Create(&model.KubeGrant{
		SubjectType: "user", SubjectID: 1, ClusterID: 1, Namespaces: "",
	})
	if code, _, raw := grantJSON(t, engine, http.MethodGet, "/kube/clusters/1/namespaces", ""); code != 200 {
		t.Fatalf("不限命名空间时应该通过: %d %s", code, raw)
	}
}

// 用户维度与角色维度的授权取并集
func TestKubeGrantUnionOfUserAndRole(t *testing.T) {
	h, engine := newKubeGrantTestHandler(t)
	seedKubeFixtures(t, h)
	enableKubeGrant(h)
	h.DB.Create(&model.KubeGrant{
		SubjectType: "user", SubjectID: 1, ClusterID: 1, Namespaces: "app-a",
	})
	h.DB.Create(&model.KubeGrant{
		SubjectType: "role", SubjectID: 1, ClusterID: 1, Namespaces: "app-b", AllowLogs: true,
	})

	for _, ns := range []string{"app-a", "app-b"} {
		if code, _, raw := grantJSON(t, engine, http.MethodGet,
			"/kube/clusters/1/pods?namespace="+ns, ""); code != 200 {
			t.Fatalf("%s 应该可见（并集）: %d %s", ns, code, raw)
		}
	}
	// 角色那条开了日志，合并后应该能看日志
	if code, _, raw := grantJSON(t, engine, http.MethodGet,
		"/kube/clusters/1/pod-logs?namespace=app-b", ""); code != 200 {
		t.Fatalf("角色授权开了日志，合并后应可见: %d %s", code, raw)
	}
}

// Pod 日志单独一个开关：日志里常带业务数据与密钥
func TestKubeGrantLogsNeedOwnSwitch(t *testing.T) {
	h, engine := newKubeGrantTestHandler(t)
	seedKubeFixtures(t, h)
	enableKubeGrant(h)
	h.DB.Create(&model.KubeGrant{SubjectType: "user", SubjectID: 1, ClusterID: 1})

	// 能看对象
	if code, _, _ := grantJSON(t, engine, http.MethodGet, "/kube/clusters/1/pods", ""); code != 200 {
		t.Fatal("能看到集群对象")
	}
	// 但看不了日志
	code, parsed, _ := grantJSON(t, engine, http.MethodGet, "/kube/clusters/1/pod-logs", "")
	if code != 403 {
		t.Fatalf("没开日志开关应该 403，实际 %d", code)
	}
	if msg, _ := parsed["msg"].(string); !strings.Contains(msg, "密钥") {
		t.Fatalf("要说清为什么日志单独授权: %v", msg)
	}

	// 写操作同理
	code, parsed, _ = grantJSON(t, engine, http.MethodPost, "/kube/clusters/1/resource/apply", `{}`)
	if code != 403 {
		t.Fatalf("没开写开关应该 403，实际 %d", code)
	}
	if msg, _ := parsed["msg"].(string); !strings.Contains(msg, "apply") {
		t.Fatalf("错误信息要点明是写操作: %v", msg)
	}
}

// 资源类型清单
func TestKubeGrantKindFilter(t *testing.T) {
	h, engine := newKubeGrantTestHandler(t)
	seedKubeFixtures(t, h)
	enableKubeGrant(h)
	h.DB.Create(&model.KubeGrant{
		SubjectType: "user", SubjectID: 1, ClusterID: 1, Kinds: "Deployment,Service",
	})

	if code, _, raw := grantJSON(t, engine, http.MethodGet,
		"/kube/clusters/1/resources?kind=Deployment", ""); code != 200 {
		t.Fatalf("清单内的资源类型应通过: %d %s", code, raw)
	}
	code, parsed, _ := grantJSON(t, engine, http.MethodGet, "/kube/clusters/1/resources?kind=Secret", "")
	if code != 403 {
		t.Fatalf("清单外的资源类型应被拒，实际 %d", code)
	}
	if msg, _ := parsed["msg"].(string); !strings.Contains(msg, "Secret") ||
		!strings.Contains(msg, "Deployment") {
		t.Fatalf("要说清被拒的是什么、可见的是什么: %v", msg)
	}
}

// 过期的授权不算
func TestKubeGrantExpired(t *testing.T) {
	h, engine := newKubeGrantTestHandler(t)
	seedKubeFixtures(t, h)
	enableKubeGrant(h)
	past := time.Now().Add(-time.Hour)
	h.DB.Create(&model.KubeGrant{
		SubjectType: "user", SubjectID: 1, ClusterID: 1, ExpiresAt: &past,
	})

	if code, _, _ := grantJSON(t, engine, http.MethodGet, "/kube/clusters/1/pods", ""); code != 403 {
		t.Fatal("过期的授权不该生效")
	}
	_, parsed, raw := grantJSON(t, engine, http.MethodGet, "/kube/grant-state", "")
	if parsed["data"].(map[string]any)["expiredGrants"] != float64(1) {
		t.Fatalf("事实条要数出过期授权: %s", raw)
	}
	// 列表里要标出来，不然人看不出为什么不生效
	_, parsed, raw = grantJSON(t, engine, http.MethodGet, "/kube/grants", "")
	rows := parsed["data"].([]any)
	if rows[0].(map[string]any)["expired"] != true {
		t.Fatalf("过期的授权要在列表里标出来: %s", raw)
	}
}

// 端口转发：集群 ID 在请求体里，走的是另一条校验
func TestKubeForwardGrantCheck(t *testing.T) {
	h, engine := newKubeGrantTestHandler(t)
	seedKubeFixtures(t, h)
	enableKubeGrant(h)
	h.DB.Create(&model.KubeGrant{
		SubjectType: "user", SubjectID: 1, ClusterID: 1, Namespaces: "app-a",
	})

	var cluster model.KubeCluster
	h.DB.First(&cluster, 1)

	// 借一次请求把上下文准备好
	rec := httptest.NewRecorder()
	engine.GET("/probe-forward", func(c *gin.Context) {
		ok, reason := h.kubeForwardGrantCheck(c, &cluster, "app-a")
		if ok {
			t.Error("没开转发开关却通过了")
		}
		if !strings.Contains(reason, "端口转发") {
			t.Errorf("原因要点名端口转发: %v", reason)
		}

		h.DB.Model(&model.KubeGrant{}).Where("id = ?", 1).Update("allow_forward", true)
		if ok, reason := h.kubeForwardGrantCheck(c, &cluster, "app-a"); !ok {
			t.Errorf("开了转发且命名空间在范围内应该通过: %v", reason)
		}
		if ok, reason := h.kubeForwardGrantCheck(c, &cluster, "kube-system"); ok {
			t.Error("越权命名空间的转发应被拒")
		} else if !strings.Contains(reason, "kube-system") {
			t.Errorf("原因要点名命名空间: %v", reason)
		}
		c.Status(http.StatusOK)
	})
	engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/probe-forward", nil))
	if rec.Code != 200 {
		t.Fatalf("探针请求失败: %d", rec.Code)
	}
}

// 创建授权的校验：必须指定集群，不支持「全部集群」
func TestKubeGrantCreateValidation(t *testing.T) {
	h, engine := newKubeGrantTestHandler(t)
	seedKubeFixtures(t, h)

	cases := []struct{ name, body, wantIn string }{
		{"没指定集群", `{"subjectType":"user","subjectId":1}`, "空白支票"},
		{"主体类型非法", `{"subjectType":"group","subjectId":1,"clusterId":1}`, "用户或角色"},
		{"用户不存在", `{"subjectType":"user","subjectId":99,"clusterId":1}`, "用户不存在"},
		{"集群不存在", `{"subjectType":"user","subjectId":1,"clusterId":99}`, "集群不存在"},
	}
	for _, tc := range cases {
		code, parsed, _ := grantJSON(t, engine, http.MethodPost, "/kube/grants", tc.body)
		if code != 400 {
			t.Fatalf("%s 应该被拒，实际 %d", tc.name, code)
		}
		if msg, _ := parsed["msg"].(string); !strings.Contains(msg, tc.wantIn) {
			t.Fatalf("%s 的错误信息不清楚: %v", tc.name, msg)
		}
	}

	// 正常创建：主体名与集群名要回填，命名空间清单去空格
	code, parsed, raw := grantJSON(t, engine, http.MethodPost, "/kube/grants",
		`{"subjectType":"user","subjectId":1,"clusterId":1,"namespaces":" app-a , ,app-b ","allowLogs":true}`)
	if code != 200 {
		t.Fatalf("创建失败: %s", raw)
	}
	data := parsed["data"].(map[string]any)
	if data["subjectName"] != "zhangsan" || data["clusterName"] != "生产集群" {
		t.Fatalf("名字没回填: %s", raw)
	}
	if data["namespaces"] != "app-a,app-b" {
		t.Fatalf("命名空间清单没归一化: %v", data["namespaces"])
	}
	if data["allNamespaces"] != false || data["allKinds"] != true {
		t.Fatalf("「全部」标记不对: %s", raw)
	}
}

// 诊断：把开关、管理员豁免、授权三件事叠起来的结果直接算给人看
func TestKubeGrantDiagnose(t *testing.T) {
	h, engine := newKubeGrantTestHandler(t)
	seedKubeFixtures(t, h)

	// 未开启：全部可见，原因照实说
	_, parsed, raw := grantJSON(t, engine, http.MethodGet, "/kube/grants/diagnose/1", "")
	data := parsed["data"].(map[string]any)
	clusters := data["clusters"].([]any)
	if len(clusters) != 2 {
		t.Fatalf("应该逐个集群给结论: %s", raw)
	}
	first := clusters[0].(map[string]any)
	if first["visible"] != true || !strings.Contains(first["reason"].(string), "授权未生效") {
		t.Fatalf("未开启时的原因不对: %s", raw)
	}

	// 开启 + 只授权一个集群
	enableKubeGrant(h)
	h.DB.Create(&model.KubeGrant{
		SubjectType: "user", SubjectID: 1, ClusterID: 1, Namespaces: "app-a", AllowLogs: true,
	})
	_, parsed, raw = grantJSON(t, engine, http.MethodGet, "/kube/grants/diagnose/1", "")
	data = parsed["data"].(map[string]any)
	clusters = data["clusters"].([]any)
	one := clusters[0].(map[string]any)
	two := clusters[1].(map[string]any)
	if one["visible"] != true || one["namespaces"] != "app-a" || one["allowLogs"] != true {
		t.Fatalf("被授权集群的结论不对: %s", raw)
	}
	if one["kinds"] != "全部" {
		t.Fatalf("没限定资源类型时应显示全部: %v", one["kinds"])
	}
	if two["visible"] != false || !strings.Contains(two["reason"].(string), "没有这个集群") {
		t.Fatalf("未授权集群的结论不对: %s", raw)
	}
}

// 编辑不允许改主体与集群：那等于换了一条授权
func TestKubeGrantUpdateKeepsSubject(t *testing.T) {
	h, engine := newKubeGrantTestHandler(t)
	seedKubeFixtures(t, h)
	grantJSON(t, engine, http.MethodPost, "/kube/grants",
		`{"subjectType":"user","subjectId":1,"clusterId":1,"namespaces":"app-a"}`)

	code, _, raw := grantJSON(t, engine, http.MethodPut, "/kube/grants/1",
		`{"subjectType":"role","subjectId":1,"clusterId":2,"namespaces":"app-c","allowWrite":true}`)
	if code != 200 {
		t.Fatalf("更新失败: %s", raw)
	}
	var item model.KubeGrant
	h.DB.First(&item, 1)
	if item.SubjectType != "user" || item.SubjectID != 1 || item.ClusterID != 1 {
		t.Fatalf("主体与集群不该被改: %+v", item)
	}
	if item.Namespaces != "app-c" || !item.AllowWrite {
		t.Fatalf("可改字段没更新: %+v", item)
	}
}
