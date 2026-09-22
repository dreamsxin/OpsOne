package k8s

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ---------- 运行时构造的资源类型 ----------

func TestApiRootOf(t *testing.T) {
	cases := map[string]string{
		"v1":                           "/api/v1",
		"apps/v1":                      "/apis/apps/v1",
		"gateway.networking.k8s.io/v1": "/apis/gateway.networking.k8s.io/v1",
		"apiextensions.k8s.io/v1":      "/apis/apiextensions.k8s.io/v1",
	}
	for in, want := range cases {
		if got := apiRootOf(in); got != want {
			t.Errorf("apiRootOf(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestNewReadOnlyKindAlwaysReadOnly 运行时构造的类型必须是只读的：
// 平台不懂自定义资源的语义，误改一条 Gateway 的代价远大于省下的方便。
func TestNewReadOnlyKindAlwaysReadOnly(t *testing.T) {
	kind := NewReadOnlyKind("argoproj.io/v1alpha1", "applications", "Application", "Argo CD", true)
	if !kind.ReadOnly {
		t.Fatal("运行时构造的类型必须只读")
	}
	if kind.Scalable || kind.Restartable {
		t.Error("不该带可扩容 / 可重启标记")
	}
	if kind.APIRoot() != "/apis/argoproj.io/v1alpha1" {
		t.Errorf("apiRoot = %q", kind.APIRoot())
	}
	// 这条是关键：包外拿不到 apiRoot，所以必须由这个构造函数填对
	if kind.APIRoot() == "" {
		t.Fatal("apiRoot 为空会让路径拼成 /namespaces/x/applications")
	}
}

func TestKindForCRD(t *testing.T) {
	info := CRDInfo{
		Name: "applications.argoproj.io", Group: "argoproj.io", Kind: "Application",
		Plural: "applications", Namespaced: true,
		ServedVersions: []string{"v1alpha1", "v1beta1"}, StorageVersion: "v1beta1",
	}

	// 不指定版本 → 用 storage 版本
	kind, err := KindForCRD(info, "")
	if err != nil {
		t.Fatalf("构造失败: %v", err)
	}
	if kind.APIVersion != "argoproj.io/v1beta1" {
		t.Errorf("默认应当用 storage 版本, got %q", kind.APIVersion)
	}

	// 指定一个 served 的版本 → 用它
	kind, err = KindForCRD(info, "v1alpha1")
	if err != nil || kind.APIVersion != "argoproj.io/v1alpha1" {
		t.Errorf("指定版本没生效: %q / %v", kind.APIVersion, err)
	}

	// 指定一个未在服务的版本 → 必须报错而不是拼出一个会 404 的路径
	if _, err := KindForCRD(info, "v2"); err == nil {
		t.Error("未 served 的版本应当被拒绝")
	} else if !strings.Contains(err.Error(), "v1alpha1") {
		t.Errorf("错误信息应当列出可选版本: %v", err)
	}

	// 没有任何 served 版本 → 报错
	if _, err := KindForCRD(CRDInfo{Name: "x.io", Group: "x.io", Plural: "xs"}, ""); err == nil {
		t.Error("没有 served 版本时应当报错")
	}
	// 缺 group / plural → 报错
	if _, err := KindForCRD(CRDInfo{Name: "x", StorageVersion: "v1"}, ""); err == nil {
		t.Error("缺 group/plural 时应当报错")
	}
}

// ---------- CRD 发现 ----------

const crdListPayload = `{"items":[
  {"metadata":{"name":"httproutes.gateway.networking.k8s.io","creationTimestamp":"2026-09-01T10:00:00Z"},
   "spec":{"group":"gateway.networking.k8s.io","scope":"Namespaced",
     "names":{"kind":"HTTPRoute","plural":"httproutes","singular":"httproute","shortNames":["hr"]},
     "versions":[{"name":"v1beta1","served":true,"storage":false},
                 {"name":"v1","served":true,"storage":true}]},
   "status":{"conditions":[{"type":"NamesAccepted","status":"True"},{"type":"Established","status":"True"}]}},
  {"metadata":{"name":"applications.argoproj.io"},
   "spec":{"group":"argoproj.io","scope":"Namespaced",
     "names":{"kind":"Application","plural":"applications","categories":["argo"]},
     "versions":[{"name":"v1alpha1","served":true,"storage":true}]},
   "status":{"conditions":[{"type":"Established","status":"True"}]}},
  {"metadata":{"name":"brokens.example.com"},
   "spec":{"group":"example.com","scope":"Cluster",
     "names":{"kind":"Broken","plural":"brokens"},
     "versions":[{"name":"v1","served":true,"storage":true}]},
   "status":{"conditions":[{"type":"Established","status":"False"}]}}
]}`

func TestListCRDs(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		fmt.Fprint(w, crdListPayload)
	}))
	defer srv.Close()
	client := newTestClient(t, srv.URL)

	crds, err := client.ListCRDs(context.Background())
	if err != nil {
		t.Fatalf("读 CRD 失败: %v", err)
	}
	if gotPath != "/apis/apiextensions.k8s.io/v1/customresourcedefinitions" {
		t.Fatalf("路径不对: %s", gotPath)
	}
	if len(crds) != 3 {
		t.Fatalf("CRD 数 = %d, want 3", len(crds))
	}

	// 按组名排序：argoproj.io < example.com < gateway.networking.k8s.io
	if crds[0].Group != "argoproj.io" {
		t.Errorf("应当按组排序, 第一个是 %q", crds[0].Group)
	}

	byKind := map[string]CRDInfo{}
	for _, item := range crds {
		byKind[item.Kind] = item
	}

	route := byKind["HTTPRoute"]
	// served 与 storage 必须分开：混在一起会让人查错版本
	if strings.Join(route.ServedVersions, ",") != "v1beta1,v1" {
		t.Errorf("served 版本 = %v", route.ServedVersions)
	}
	if route.StorageVersion != "v1" {
		t.Errorf("storage 版本 = %q, want v1", route.StorageVersion)
	}
	if !route.Namespaced || route.Scope != "Namespaced" {
		t.Errorf("作用域解析错: %+v", route)
	}
	if route.KnownAs == "" || !strings.Contains(route.KnownAs, "Gateway API") {
		t.Errorf("认得的组应当给出说明, got %q", route.KnownAs)
	}
	if !route.Established {
		t.Error("Established=True 应当被识别")
	}
	if route.CreatedAt.IsZero() {
		t.Error("创建时间没解出来")
	}
	if strings.Join(route.ShortNames, ",") != "hr" {
		t.Errorf("短名 = %v", route.ShortNames)
	}

	// 未就绪的 CRD 必须能被看出来 —— 那是 CRD 没装好，不是查不到东西
	if byKind["Broken"].Established {
		t.Error("Established=False 应当为 false")
	}
	if byKind["Broken"].Namespaced {
		t.Error("Cluster 作用域不该是 namespaced")
	}
	// 认不出的组 KnownAs 为空，但不影响浏览
	if byKind["Broken"].KnownAs != "" {
		t.Errorf("认不出的组 KnownAs 应当为空, got %q", byKind["Broken"].KnownAs)
	}
}

// ---------- Helm ----------

// buildHelmSecretData 造一个真实形状的 Helm release 载荷：
// JSON → gzip → base64（Helm 自己那层）→ base64（k8s Secret 那层）
func buildHelmSecretData(t *testing.T, payload string, useGzip, doubleBase64 bool) string {
	t.Helper()
	raw := []byte(payload)
	if useGzip {
		var buf bytes.Buffer
		zw := gzip.NewWriter(&buf)
		if _, err := zw.Write(raw); err != nil {
			t.Fatal(err)
		}
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
		raw = buf.Bytes()
	}
	if doubleBase64 {
		raw = []byte(base64.StdEncoding.EncodeToString(raw))
	}
	return base64.StdEncoding.EncodeToString(raw)
}

const helmPayload = `{"name":"my-nginx","namespace":"web","version":3,
 "info":{"status":"deployed","description":"Upgrade complete","notes":"访问地址：http://x",
   "first_deployed":"2026-08-01T10:00:00.123456789Z","last_deployed":"2026-09-20T11:30:00Z"},
 "chart":{"metadata":{"name":"nginx","version":"15.1.2","appVersion":"1.25.3"}}}`

// TestDecodeHelmSecret 三种编码形态都要能解：
// gzip + 双层 base64（Helm 3 常见）、不 gzip、只有单层 base64（老版本）。
func TestDecodeHelmSecret(t *testing.T) {
	cases := []struct {
		name         string
		useGzip      bool
		doubleBase64 bool
	}{
		{"gzip+双层base64", true, true},
		{"不gzip+双层base64", false, true},
		{"gzip+单层base64", true, false},
		{"不gzip+单层base64", false, false},
	}
	for _, tc := range cases {
		obj := map[string]any{
			"metadata": map[string]any{"name": "sh.helm.release.v1.my-nginx.v3", "namespace": "web"},
			"data":     map[string]any{"release": buildHelmSecretData(t, helmPayload, tc.useGzip, tc.doubleBase64)},
		}
		release, ok := decodeHelmSecret(obj)
		if !ok {
			t.Errorf("%s: 解不开", tc.name)
			continue
		}
		if release.Name != "my-nginx" || release.Revision != 3 || release.Status != "deployed" {
			t.Errorf("%s: 字段不对 %+v", tc.name, release)
		}
		if release.Chart != "nginx" || release.ChartVer != "15.1.2" || release.AppVersion != "1.25.3" {
			t.Errorf("%s: chart 信息不对 %+v", tc.name, release)
		}
		if release.SecretName != "sh.helm.release.v1.my-nginx.v3" {
			t.Errorf("%s: Secret 名没带上", tc.name)
		}
		if release.FirstAt.IsZero() || release.UpdatedAt.IsZero() {
			t.Errorf("%s: 时间没解出来 %+v", tc.name, release)
		}
		if !release.HasNotes {
			t.Errorf("%s: NOTES 有内容时应当为 true", tc.name)
		}
	}
}

func TestDecodeHelmSecretRejectsGarbage(t *testing.T) {
	for _, obj := range []map[string]any{
		{},
		{"data": map[string]any{}},
		{"data": map[string]any{"release": ""}},
		{"data": map[string]any{"release": "!!!not-base64!!!"}},
		// 能解开 base64 但不是 JSON
		{"data": map[string]any{"release": base64.StdEncoding.EncodeToString([]byte("hello"))}},
	} {
		if _, ok := decodeHelmSecret(obj); ok {
			t.Errorf("坏数据不该被当成 release: %v", obj)
		}
	}
	// 能解成 JSON 但没有 name → 也不算
	noName := map[string]any{"data": map[string]any{
		"release": buildHelmSecretData(t, `{"version":1}`, false, false)}}
	if _, ok := decodeHelmSecret(noName); ok {
		t.Error("没有 name 的载荷不该被当成 release")
	}
}

// TestListHelmReleasesKeepsLatestRevision 同一个 release 的多个 revision 只留最新那个，
// 但要报出一共有几个历史版本；并且必须用 fieldSelector 让 API Server 筛
// （拉全部 Secret 回来在大集群里是几十兆的传输）。
func TestListHelmReleasesKeepsLatestRevision(t *testing.T) {
	v1 := strings.Replace(helmPayload, `"version":3`, `"version":1`, 1)
	v2 := strings.Replace(helmPayload, `"version":3`, `"version":2`, 1)
	other := `{"name":"redis","namespace":"web","version":1,
	 "info":{"status":"failed","description":"install failed"},
	 "chart":{"metadata":{"name":"redis","version":"18.0.0","appVersion":"7.2"}}}`

	var gotQuery, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.Path, r.URL.RawQuery
		fmt.Fprintf(w, `{"items":[
      {"metadata":{"name":"sh.helm.release.v1.my-nginx.v1","namespace":"web"},"data":{"release":%q}},
      {"metadata":{"name":"sh.helm.release.v1.my-nginx.v3","namespace":"web"},"data":{"release":%q}},
      {"metadata":{"name":"sh.helm.release.v1.my-nginx.v2","namespace":"web"},"data":{"release":%q}},
      {"metadata":{"name":"sh.helm.release.v1.redis.v1","namespace":"web"},"data":{"release":%q}},
      {"metadata":{"name":"unrelated","namespace":"web"},"data":{"other":"x"}}
    ]}`,
			buildHelmSecretData(t, v1, true, true),
			buildHelmSecretData(t, helmPayload, true, true),
			buildHelmSecretData(t, v2, true, true),
			buildHelmSecretData(t, other, true, true))
	}))
	defer srv.Close()
	client := newTestClient(t, srv.URL)

	releases, err := client.ListHelmReleases(context.Background(), "web")
	if err != nil {
		t.Fatalf("读 release 失败: %v", err)
	}
	if gotPath != "/api/v1/namespaces/web/secrets" {
		t.Fatalf("路径不对: %s", gotPath)
	}
	if !strings.Contains(gotQuery, "fieldSelector=type%3Dhelm.sh%2Frelease.v1") {
		t.Fatalf("必须用 fieldSelector 让 API Server 筛: %s", gotQuery)
	}
	if len(releases) != 2 {
		t.Fatalf("release 数 = %d, want 2（nginx + redis）", len(releases))
	}

	byName := map[string]HelmRelease{}
	for _, item := range releases {
		byName[item.Name] = item
	}
	nginx := byName["my-nginx"]
	if nginx.Revision != 3 {
		t.Errorf("应当留最新 revision, got %d", nginx.Revision)
	}
	if nginx.Revisions != 3 {
		t.Errorf("历史版本数 = %d, want 3", nginx.Revisions)
	}
	if byName["redis"].Status != "failed" {
		t.Errorf("失败的 release 也要列出来, got %q", byName["redis"].Status)
	}
	// 不是 Helm release 的那条被跳过，而不是让整次读取失败
	if _, bad := byName["unrelated"]; bad {
		t.Error("非 release 的 Secret 不该出现")
	}
}

// ---------- RBAC 反查 ----------

func rbacStubServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	serveSAs := func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"items":[
      {"metadata":{"name":"deployer","namespace":"ops","creationTimestamp":"2026-09-01T10:00:00Z"},
       "secrets":[{"name":"deployer-token"}]},
      {"metadata":{"name":"reader","namespace":"ops"}},
      {"metadata":{"name":"leftover","namespace":"ops"}},
      {"metadata":{"name":"broken","namespace":"ops"}}
    ]}`)
	}
	// 指定命名空间时走 /namespaces/ops/serviceaccounts，不指定时走全集群路径，两个都要伺候
	mux.HandleFunc("/api/v1/namespaces/ops/serviceaccounts", serveSAs)
	mux.HandleFunc("/api/v1/serviceaccounts", serveSAs)
	mux.HandleFunc("/apis/rbac.authorization.k8s.io/v1/roles", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"items":[
      {"metadata":{"name":"pod-reader","namespace":"ops"},
       "rules":[{"apiGroups":[""],"resources":["pods"],"verbs":["get","list","watch"]}]}
    ]}`)
	})
	mux.HandleFunc("/apis/rbac.authorization.k8s.io/v1/clusterroles", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"items":[
      {"metadata":{"name":"cluster-admin"},
       "rules":[{"apiGroups":["*"],"resources":["*"],"verbs":["*"]}]},
      {"metadata":{"name":"secret-writer"},
       "rules":[{"apiGroups":[""],"resources":["secrets"],"verbs":["get","create","update"],
                 "resourceNames":["app-tls"]}]}
    ]}`)
	})
	mux.HandleFunc("/apis/rbac.authorization.k8s.io/v1/rolebindings", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"items":[
      {"metadata":{"name":"read-pods","namespace":"ops"},
       "roleRef":{"kind":"Role","name":"pod-reader"},
       "subjects":[{"kind":"ServiceAccount","name":"reader"}]},
      {"metadata":{"name":"dangling","namespace":"ops"},
       "roleRef":{"kind":"Role","name":"does-not-exist"},
       "subjects":[{"kind":"ServiceAccount","name":"broken"}]}
    ]}`)
	})
	mux.HandleFunc("/apis/rbac.authorization.k8s.io/v1/clusterrolebindings", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"items":[
      {"metadata":{"name":"deployer-admin"},
       "roleRef":{"kind":"ClusterRole","name":"cluster-admin"},
       "subjects":[{"kind":"ServiceAccount","name":"deployer","namespace":"ops"},
                   {"kind":"User","name":"alice"}]},
      {"metadata":{"name":"reader-secrets"},
       "roleRef":{"kind":"ClusterRole","name":"secret-writer"},
       "subjects":[{"kind":"ServiceAccount","name":"reader","namespace":"ops"}]}
    ]}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestRBACReverseLookup(t *testing.T) {
	srv := rbacStubServer(t)
	client := newTestClient(t, srv.URL)

	snap, err := client.RBAC(context.Background(), "ops")
	if err != nil {
		t.Fatalf("读 RBAC 失败: %v", err)
	}
	if len(snap.ServiceAccounts) != 4 {
		t.Fatalf("SA 数 = %d, want 4", len(snap.ServiceAccounts))
	}
	if snap.RoleCount != 1 || snap.ClusterRoleCount != 2 ||
		snap.BindingCount != 2 || snap.ClusterBindingCount != 2 {
		t.Errorf("计数不对: %+v", snap)
	}
	// 引用了不存在的 Role 的 binding 要被单独数出来
	if snap.OrphanBindings != 1 {
		t.Errorf("孤儿绑定数 = %d, want 1", snap.OrphanBindings)
	}

	// 排序：cluster-admin 的排最前，这是最该被一眼看到的
	if snap.ServiceAccounts[0].Name != "deployer" {
		t.Errorf("cluster-admin 的 SA 应当排最前, got %q", snap.ServiceAccounts[0].Name)
	}

	byName := map[string]ServiceAccountView{}
	for _, item := range snap.ServiceAccounts {
		byName[item.Name] = item
	}

	// deployer：被 ClusterRoleBinding 绑到 cluster-admin
	deployer := byName["deployer"]
	if !deployer.ClusterAdmin {
		t.Error("deployer 应当被标为 cluster-admin")
	}
	if !deployer.Wildcard {
		t.Error("cluster-admin 的通配规则应当把 Wildcard 置真")
	}
	if len(deployer.Bindings) != 1 || deployer.Bindings[0].Scope != "整个集群" {
		t.Errorf("授权范围不对: %+v", deployer.Bindings)
	}
	if deployer.CreatedAt.IsZero() {
		t.Error("创建时间没解出来")
	}
	if strings.Join(deployer.Secrets, ",") != "deployer-token" {
		t.Errorf("挂载的 Secret = %v", deployer.Secrets)
	}

	// reader：一条 RoleBinding（命名空间级）+ 一条 ClusterRoleBinding。
	// 后者是关键 —— 只读本命名空间的 binding 会漏掉它
	reader := byName["reader"]
	if len(reader.Bindings) != 2 {
		t.Fatalf("reader 应当有 2 条授权, got %d", len(reader.Bindings))
	}
	if reader.ClusterAdmin || reader.Wildcard {
		t.Error("reader 不该被标成 cluster-admin 或通配")
	}
	// 动词汇总要去重并排序，能一眼看出「能不能写」
	if strings.Join(reader.VerbSummary, ",") != "create,get,list,update,watch" {
		t.Errorf("动词汇总 = %v", reader.VerbSummary)
	}
	var sawNamespaceScope, sawClusterScope bool
	for _, ref := range reader.Bindings {
		switch ref.BindingKind {
		case "RoleBinding":
			sawNamespaceScope = ref.Scope == "命名空间 ops"
			if len(ref.Rules) != 1 || ref.Rules[0].Resources[0] != "pods" {
				t.Errorf("RoleBinding 的规则没查到: %+v", ref.Rules)
			}
		case "ClusterRoleBinding":
			sawClusterScope = true
			if len(ref.Rules) != 1 || len(ref.Rules[0].ResourceNames) != 1 {
				t.Errorf("ClusterRole 的 resourceNames 没带上: %+v", ref.Rules)
			}
		}
	}
	if !sawNamespaceScope || !sawClusterScope {
		t.Error("两种 binding 都要反查到")
	}

	// leftover：没有任何授权 —— 清理时先看这一批
	if len(byName["leftover"].Bindings) != 0 {
		t.Error("leftover 不该有授权")
	}
	// broken：绑了但 Role 不存在，表现是「绑了却什么权限都没有」，必须显式标出来
	broken := byName["broken"]
	if len(broken.Bindings) != 1 {
		t.Fatalf("broken 应当有 1 条授权, got %d", len(broken.Bindings))
	}
	if !broken.Bindings[0].Missing {
		t.Error("引用了不存在的 Role 必须标 Missing")
	}
	if len(broken.Bindings[0].Rules) != 0 {
		t.Error("Role 不存在时规则应当为空")
	}
}

// TestRBACIgnoresNonServiceAccountSubjects User / Group 主体在这个平台里没有对应对象，
// 反查时要跳过而不是造出一个假的 SA。
func TestRBACIgnoresNonServiceAccountSubjects(t *testing.T) {
	srv := rbacStubServer(t)
	client := newTestClient(t, srv.URL)
	snap, err := client.RBAC(context.Background(), "ops")
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range snap.ServiceAccounts {
		if item.Name == "alice" {
			t.Fatal("User 主体不该变成一个 ServiceAccount")
		}
	}
}

// ---------- Gateway API ----------

func TestListGatewayRoutes(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		fmt.Fprint(w, `{"items":[
      {"metadata":{"name":"web","namespace":"ops"},
       "spec":{"parentRefs":[{"name":"public-gw","sectionName":"https"},
                             {"name":"internal-gw","namespace":"infra"}],
         "hostnames":["www.example.com","example.com"],
         "rules":[
           {"matches":[{"path":{"type":"PathPrefix","value":"/api"},"method":"POST"}],
            "backendRefs":[{"name":"api-svc","port":8080,"weight":90},
                           {"name":"api-canary","port":8080,"weight":10}]},
           {"backendRefs":[{"name":"legacy","namespace":"old","port":80}]},
           {"matches":[{"path":{"value":"/static"}}],"backendRefs":[]}
         ]}},
      {"metadata":{"name":"bare","namespace":"ops"},"spec":{}}
    ]}`)
	}))
	defer srv.Close()
	client := newTestClient(t, srv.URL)

	routes, err := client.ListGatewayRoutes(context.Background(), "v1", "ops")
	if err != nil {
		t.Fatalf("读 HTTPRoute 失败: %v", err)
	}
	if gotPath != "/apis/gateway.networking.k8s.io/v1/namespaces/ops/httproutes" {
		t.Fatalf("路径不对: %s", gotPath)
	}
	if len(routes) != 2 {
		t.Fatalf("路由数 = %d", len(routes))
	}

	web := routes[1] // 排序后 bare < web
	if web.Name != "web" {
		web = routes[0]
	}
	if strings.Join(web.Hostnames, ",") != "www.example.com,example.com" {
		t.Errorf("域名 = %v", web.Hostnames)
	}
	// parentRefs：同命名空间只给名字，跨命名空间要带上前缀，监听器要标出来
	joined := strings.Join(web.Gateways, " | ")
	if !strings.Contains(joined, "public-gw（监听器 https）") {
		t.Errorf("监听器没标出来: %s", joined)
	}
	if !strings.Contains(joined, "infra/internal-gw") {
		t.Errorf("跨命名空间的 Gateway 要带前缀: %s", joined)
	}

	if len(web.Rules) != 3 {
		t.Fatalf("规则数 = %d", len(web.Rules))
	}
	// 规则 1：匹配条件与后端权重
	if !strings.Contains(web.Rules[0].Matches[0], "路径 PathPrefix /api") ||
		!strings.Contains(web.Rules[0].Matches[0], "方法 POST") {
		t.Errorf("匹配条件没说清: %v", web.Rules[0].Matches)
	}
	backends := strings.Join(web.Rules[0].Backends, " | ")
	if !strings.Contains(backends, "api-svc:8080 权重 90") {
		t.Errorf("后端没说清: %s", backends)
	}
	// 规则 2：跨命名空间后端要提醒需要 ReferenceGrant
	if !strings.Contains(web.Rules[1].Backends[0], "ReferenceGrant") {
		t.Errorf("跨命名空间后端要提醒: %v", web.Rules[1].Backends)
	}
	// 规则 3：没有后端 = 这条规则不会把流量送到任何地方，要说出来
	if !strings.Contains(web.Rules[2].Backends[0], "不会把流量送到任何地方") {
		t.Errorf("空后端要说明: %v", web.Rules[2].Backends)
	}
	// path 没写 type 时，Gateway API 的默认是 PathPrefix，要显式写出来
	if !strings.Contains(web.Rules[2].Matches[0], "PathPrefix") {
		t.Errorf("缺省 path type 应当补成 PathPrefix: %v", web.Rules[2].Matches)
	}
}

// TestDescribeRouteMatchesDefaults 没有 matches 时 Gateway API 的语义是
// 「匹配所有请求」—— 界面上留空会让人以为这条路由没配好。
func TestDescribeRouteMatchesDefaults(t *testing.T) {
	got := describeRouteMatches(nil)
	if len(got) != 1 || !strings.Contains(got[0], "所有请求") {
		t.Errorf("空 matches 应当显式说明: %v", got)
	}
}
