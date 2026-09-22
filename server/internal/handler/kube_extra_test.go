package handler

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"ops-platform/server/internal/config"
	"ops-platform/server/internal/model"
)

// 这一组是端到端的：起一个**假 API Server**，把集群 kubeconfig 指向它，
// 然后打真实的 HTTP 路由。链路上每一段都是真的（gin → handler → k8s 客户端 → 解码），
// 只有集群那一端是桩 —— 没有真集群时这是能做到的最完整验证。

// fakeAPIServer 一个只伺候这一轮用到的那几个路径的假 API Server
func fakeAPIServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	// --- CRD ---
	mux.HandleFunc("/apis/apiextensions.k8s.io/v1/customresourcedefinitions",
		func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"items":[
      {"metadata":{"name":"httproutes.gateway.networking.k8s.io"},
       "spec":{"group":"gateway.networking.k8s.io","scope":"Namespaced",
         "names":{"kind":"HTTPRoute","plural":"httproutes"},
         "versions":[{"name":"v1","served":true,"storage":true}]},
       "status":{"conditions":[{"type":"Established","status":"True"}]}},
      {"metadata":{"name":"applications.argoproj.io"},
       "spec":{"group":"argoproj.io","scope":"Namespaced",
         "names":{"kind":"Application","plural":"applications"},
         "versions":[{"name":"v1alpha1","served":true,"storage":true}]},
       "status":{"conditions":[{"type":"Established","status":"True"}]}},
      {"metadata":{"name":"halfbaked.example.com"},
       "spec":{"group":"example.com","scope":"Cluster",
         "names":{"kind":"HalfBaked","plural":"halfbakeds"},
         "versions":[{"name":"v1","served":true,"storage":true}]},
       "status":{"conditions":[{"type":"Established","status":"False"}]}}
    ]}`)
		})

	// --- 自定义资源实例 ---
	mux.HandleFunc("/apis/argoproj.io/v1alpha1/namespaces/ops/applications",
		func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"items":[
      {"metadata":{"name":"web","namespace":"ops","creationTimestamp":"2026-09-01T10:00:00Z"},
       "spec":{"project":"default"},"status":{"sync":{"status":"Synced"}}}
    ]}`)
		})
	mux.HandleFunc("/apis/argoproj.io/v1alpha1/namespaces/ops/applications/web",
		func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"apiVersion":"argoproj.io/v1alpha1","kind":"Application",
      "metadata":{"name":"web","namespace":"ops","managedFields":[{"manager":"argocd"}]},
      "spec":{"project":"default"},
      "status":{"sync":{"status":"Synced"},"health":{"status":"Healthy"}}}`)
		})

	// --- Gateway API ---
	mux.HandleFunc("/apis/gateway.networking.k8s.io/v1/namespaces/ops/httproutes",
		func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"items":[
      {"metadata":{"name":"web","namespace":"ops"},
       "spec":{"parentRefs":[{"name":"public-gw"}],"hostnames":["www.example.com"],
         "rules":[{"matches":[{"path":{"type":"PathPrefix","value":"/api"}}],
                   "backendRefs":[{"name":"api-svc","port":8080}]}]}}
    ]}`)
		})

	// --- Helm release Secret ---
	mux.HandleFunc("/api/v1/namespaces/ops/secrets", func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("fieldSelector"); got != "type=helm.sh/release.v1" {
			t.Errorf("Helm 列表必须带 fieldSelector，实际 %q", got)
		}
		payload := map[string]any{
			"name": "my-nginx", "namespace": "ops", "version": 2,
			"info": map[string]any{
				"status": "deployed", "description": "Upgrade complete",
				"last_deployed": "2026-09-20T11:30:00Z",
			},
			"chart": map[string]any{"metadata": map[string]any{
				"name": "nginx", "version": "15.1.2", "appVersion": "1.25.3"}},
		}
		raw, _ := json.Marshal(payload)
		var buf bytes.Buffer
		zw := gzip.NewWriter(&buf)
		_, _ = zw.Write(raw)
		_ = zw.Close()
		inner := base64.StdEncoding.EncodeToString(buf.Bytes())
		outer := base64.StdEncoding.EncodeToString([]byte(inner))
		fmt.Fprintf(w, `{"items":[
      {"metadata":{"name":"sh.helm.release.v1.my-nginx.v2","namespace":"ops"},
       "type":"helm.sh/release.v1","data":{"release":%q}}
    ]}`, outer)
	})

	// --- RBAC ---
	mux.HandleFunc("/api/v1/namespaces/ops/serviceaccounts", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"items":[{"metadata":{"name":"deployer","namespace":"ops"}}]}`)
	})
	mux.HandleFunc("/apis/rbac.authorization.k8s.io/v1/roles", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"items":[]}`)
	})
	mux.HandleFunc("/apis/rbac.authorization.k8s.io/v1/clusterroles", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"items":[{"metadata":{"name":"cluster-admin"},
      "rules":[{"apiGroups":["*"],"resources":["*"],"verbs":["*"]}]}]}`)
	})
	mux.HandleFunc("/apis/rbac.authorization.k8s.io/v1/rolebindings", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"items":[]}`)
	})
	mux.HandleFunc("/apis/rbac.authorization.k8s.io/v1/clusterrolebindings", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"items":[{"metadata":{"name":"deployer-admin"},
      "roleRef":{"kind":"ClusterRole","name":"cluster-admin"},
      "subjects":[{"kind":"ServiceAccount","name":"deployer","namespace":"ops"}]}]}`)
	})

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("假 API Server 收到未预期的请求: %s", r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func newKubeExtraTestHandler(t *testing.T) (*Handler, *gin.Engine) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	dir := t.TempDir()
	g, err := gorm.Open(sqlite.Open(dir+"/kube.db"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	if err := g.AutoMigrate(&model.KubeCluster{}, &model.User{}); err != nil {
		t.Fatalf("建表失败: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, err := g.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})

	srv := fakeAPIServer(t)
	h := New(g, &config.Config{SecretKey: "test-key"})
	// kubeconfig 加密落库（与生产一致），巡检时再解回来
	cluster := model.KubeCluster{
		Name: "fake", Server: srv.URL, Enabled: true,
		Kubeconfig: h.sealSecret(fmt.Sprintf(`apiVersion: v1
clusters:
- cluster: {server: %s}
  name: c
contexts:
- context: {cluster: c, user: u}
  name: ctx
current-context: ctx
users:
- name: u
  user: {token: fake-token}
`, srv.URL)),
	}
	if err := g.Create(&cluster).Error; err != nil {
		t.Fatalf("建集群失败: %v", err)
	}

	engine := gin.New()
	engine.Use(func(c *gin.Context) {
		c.Set("ctx_user", &model.User{ID: 1, Username: "admin"})
		c.Next()
	})
	engine.GET("/kube/clusters/:id/helm-releases", h.KubeHelmReleases)
	engine.GET("/kube/clusters/:id/crds", h.KubeCRDs)
	engine.GET("/kube/clusters/:id/crd-resources", h.KubeCRDResources)
	engine.GET("/kube/clusters/:id/crd-resource", h.KubeCRDResourceDetail)
	engine.GET("/kube/clusters/:id/gateway-routes", h.KubeGatewayRoutes)
	engine.GET("/kube/clusters/:id/rbac", h.KubeRBAC)
	return h, engine
}

func kubeJSON(t *testing.T, engine *gin.Engine, path string) (int, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	var parsed map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &parsed); err != nil {
		t.Fatalf("响应不是 JSON: %s", rec.Body.String())
	}
	return rec.Code, parsed
}

func TestKubeHelmReleasesEndToEnd(t *testing.T) {
	_, engine := newKubeExtraTestHandler(t)
	code, resp := kubeJSON(t, engine, "/kube/clusters/1/helm-releases?namespace=ops")
	if code != http.StatusOK {
		t.Fatalf("返回 %d: %v", code, resp)
	}
	data := resp["data"].(map[string]any)
	releases := data["releases"].([]any)
	if len(releases) != 1 {
		t.Fatalf("release 数 = %d", len(releases))
	}
	item := releases[0].(map[string]any)
	if item["name"] != "my-nginx" || item["chart"] != "nginx" || item["chartVersion"] != "15.1.2" {
		t.Errorf("解出来的 release 不对: %v", item)
	}
	if item["revision"].(float64) != 2 {
		t.Errorf("revision = %v", item["revision"])
	}
	// 边界说明必须随接口一起给出来，不靠前端硬编码
	notes := data["notes"].([]any)
	joined := fmt.Sprint(notes...)
	for _, want := range []string{"secret driver", "只读", "values"} {
		if !strings.Contains(joined, want) {
			t.Errorf("notes 里应当提到 %q: %s", want, joined)
		}
	}
}

func TestKubeCRDsEndToEnd(t *testing.T) {
	_, engine := newKubeExtraTestHandler(t)
	code, resp := kubeJSON(t, engine, "/kube/clusters/1/crds")
	if code != http.StatusOK {
		t.Fatalf("返回 %d: %v", code, resp)
	}
	data := resp["data"].(map[string]any)
	if data["total"].(float64) != 3 {
		t.Errorf("CRD 数 = %v", data["total"])
	}
	// 未就绪的 CRD 要单独数出来 —— 那是 CRD 没装好，不是查不到东西
	if data["notEstablished"].(float64) != 1 {
		t.Errorf("未就绪数 = %v", data["notEstablished"])
	}
	// 装了 Gateway API 就要能被认出来，并报出实际版本
	if data["gatewayAPI"] != "v1" {
		t.Errorf("Gateway API 版本 = %v, want v1", data["gatewayAPI"])
	}
}

func TestKubeCRDResourcesEndToEnd(t *testing.T) {
	_, engine := newKubeExtraTestHandler(t)
	code, resp := kubeJSON(t, engine,
		"/kube/clusters/1/crd-resources?crd=applications.argoproj.io&namespace=ops")
	if code != http.StatusOK {
		t.Fatalf("返回 %d: %v", code, resp)
	}
	data := resp["data"].(map[string]any)
	if data["kind"] != "Application" || data["apiVersion"] != "argoproj.io/v1alpha1" {
		t.Errorf("类型信息不对: %v", data)
	}
	items := data["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["name"] != "web" {
		t.Errorf("实例不对: %v", items)
	}
}

// TestKubeCRDNotEstablishedRejected 未就绪的 CRD 要在查实例之前就被拦下并说清原因，
// 而不是让人看到一个莫名的 404。
func TestKubeCRDNotEstablishedRejected(t *testing.T) {
	_, engine := newKubeExtraTestHandler(t)
	code, resp := kubeJSON(t, engine, "/kube/clusters/1/crd-resources?crd=halfbaked.example.com")
	if code != http.StatusBadRequest {
		t.Fatalf("未就绪的 CRD 应当被拒, got %d: %v", code, resp)
	}
	if !strings.Contains(resp["msg"].(string), "Established") {
		t.Errorf("错误信息要点明是 CRD 自己没就绪: %v", resp["msg"])
	}
}

func TestKubeCRDUnknownRejected(t *testing.T) {
	_, engine := newKubeExtraTestHandler(t)
	if code, _ := kubeJSON(t, engine, "/kube/clusters/1/crd-resources?crd=nope.example.com"); code != http.StatusNotFound {
		t.Errorf("集群里没有的 CRD 应当 404, got %d", code)
	}
	if code, _ := kubeJSON(t, engine, "/kube/clusters/1/crd-resources"); code != http.StatusBadRequest {
		t.Error("不传 crd 参数应当被拒")
	}
}

// TestKubeCRDResourceDetailKeepsStatus 自定义资源的 status 往往才是要看的那部分，
// 所以详情**不能**像可编辑资源那样把 status 清掉。
func TestKubeCRDResourceDetailKeepsStatus(t *testing.T) {
	_, engine := newKubeExtraTestHandler(t)
	code, resp := kubeJSON(t, engine,
		"/kube/clusters/1/crd-resource?crd=applications.argoproj.io&namespace=ops&name=web")
	if code != http.StatusOK {
		t.Fatalf("返回 %d: %v", code, resp)
	}
	data := resp["data"].(map[string]any)
	yamlText := data["yaml"].(string)
	if !strings.Contains(yamlText, "status:") || !strings.Contains(yamlText, "Synced") {
		t.Errorf("详情必须保留 status:\n%s", yamlText)
	}
	if !strings.Contains(data["note"].(string), "status") {
		t.Errorf("note 要说明保留了 status: %v", data["note"])
	}
}

func TestKubeGatewayRoutesEndToEnd(t *testing.T) {
	_, engine := newKubeExtraTestHandler(t)
	code, resp := kubeJSON(t, engine, "/kube/clusters/1/gateway-routes?namespace=ops")
	if code != http.StatusOK {
		t.Fatalf("返回 %d: %v", code, resp)
	}
	data := resp["data"].(map[string]any)
	if data["installed"] != true {
		t.Fatal("装了 Gateway API 应当报 installed=true")
	}
	if data["version"] != "v1" {
		t.Errorf("版本 = %v", data["version"])
	}
	routes := data["routes"].([]any)
	if len(routes) != 1 {
		t.Fatalf("路由数 = %d", len(routes))
	}
	route := routes[0].(map[string]any)
	rules := route["rules"].([]any)
	rule := rules[0].(map[string]any)
	backends := fmt.Sprint(rule["backends"].([]any)...)
	if !strings.Contains(backends, "api-svc:8080") {
		t.Errorf("后端没解出来: %s", backends)
	}
}

func TestKubeRBACEndToEnd(t *testing.T) {
	_, engine := newKubeExtraTestHandler(t)
	code, resp := kubeJSON(t, engine, "/kube/clusters/1/rbac?namespace=ops")
	if code != http.StatusOK {
		t.Fatalf("返回 %d: %v", code, resp)
	}
	data := resp["data"].(map[string]any)
	accounts := data["accounts"].([]any)
	if len(accounts) != 1 {
		t.Fatalf("SA 数 = %d", len(accounts))
	}
	sa := accounts[0].(map[string]any)
	// 关键：命名空间里的 SA 被 ClusterRoleBinding 绑到 cluster-admin，必须被看出来
	if sa["clusterAdmin"] != true {
		t.Error("被 ClusterRoleBinding 绑到 cluster-admin 的 SA 必须标出来")
	}
	if sa["wildcard"] != true {
		t.Error("通配符权限应当被识别")
	}
	stats := data["stats"].(map[string]any)
	if stats["clusterAdmin"].(float64) != 1 {
		t.Errorf("统计不对: %v", stats)
	}
}

// TestKubeExtraRejectsMissingCluster 集群不存在时给 404，而不是 500
func TestKubeExtraRejectsMissingCluster(t *testing.T) {
	_, engine := newKubeExtraTestHandler(t)
	for _, path := range []string{
		"/kube/clusters/999/helm-releases",
		"/kube/clusters/999/crds",
		"/kube/clusters/999/rbac",
		"/kube/clusters/999/gateway-routes",
	} {
		if code, _ := kubeJSON(t, engine, path); code != http.StatusNotFound {
			t.Errorf("%s 应当 404, got %d", path, code)
		}
	}
}
