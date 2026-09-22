package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"

	"ops-platform/server/internal/config"
	"ops-platform/server/internal/model"
)

// ---------- 假上游：按各家公开文档的形状回话 ----------

// newFakeWecom 企业微信：GET 取 token / 部门 / 成员，错误藏在 200 的 errcode 里
func newFakeWecom(t *testing.T, errcode int) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/cgi-bin/gettoken", func(w http.ResponseWriter, r *http.Request) {
		if errcode != 0 {
			// 关键：密钥错时企微也返回 HTTP 200
			_, _ = w.Write([]byte(`{"errcode":40001,"errmsg":"invalid credential"}`))
			return
		}
		_, _ = w.Write([]byte(`{"errcode":0,"errmsg":"ok","access_token":"tok-1"}`))
	})
	mux.HandleFunc("/cgi-bin/department/list", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"errcode":0,"department":[
			{"id":1,"parentid":0,"name":"总公司","order":1},
			{"id":2,"parentid":1,"name":"运维部","order":1},
			{"id":3,"parentid":2,"name":"基础架构组","order":1}]}`))
	})
	mux.HandleFunc("/cgi-bin/user/list", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("department_id") {
		case "2":
			_, _ = w.Write([]byte(`{"errcode":0,"userlist":[
				{"userid":"zhangsan","name":"张三","email":"zhangsan@corp.com","mobile":"13800000001","department":[2],"status":1},
				{"userid":"lisi","name":"李四","email":"","mobile":"","department":[2,3],"status":2}]}`))
		case "3":
			// 李四同时挂在 3 上：去重后应该只有一条，且部门合并
			_, _ = w.Write([]byte(`{"errcode":0,"userlist":[
				{"userid":"lisi","name":"李四","department":[3],"status":2},
				{"userid":"wangwu","name":"王五","department":[3],"status":5}]}`))
		default:
			_, _ = w.Write([]byte(`{"errcode":0,"userlist":[]}`))
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestWecomDirectoryParsesAndSurfacesErrcode(t *testing.T) {
	srv := newFakeWecom(t, 0)
	dir, err := imDirectoryFor(channelWecom, srv.URL, nil)
	if err != nil {
		t.Fatalf("取适配器失败: %v", err)
	}
	ctx := context.Background()

	token, err := dir.token(ctx, "corp", "secret")
	if err != nil || token != "tok-1" {
		t.Fatalf("取 token 失败: %v / %q", err, token)
	}
	depts, err := dir.departments(ctx, token, "1")
	if err != nil {
		t.Fatalf("取部门失败: %v", err)
	}
	if len(depts) != 3 || depts[2].Name != "基础架构组" || depts[2].ParentID != "2" {
		t.Fatalf("部门解析不对: %+v", depts)
	}
	people, err := dir.users(ctx, token, "2")
	if err != nil {
		t.Fatalf("取成员失败: %v", err)
	}
	if len(people) != 2 || people[0].ID != "zhangsan" || people[0].Mobile != "13800000001" {
		t.Fatalf("成员解析不对: %+v", people)
	}
	// status=2（已禁用）要算成不在职
	if people[1].Active {
		t.Fatalf("status=2 应该判为不在职: %+v", people[1])
	}

	// 密钥错时企微返回 HTTP 200 + errcode，必须当失败处理并带上原话
	bad := newFakeWecom(t, 40001)
	badDir, _ := imDirectoryFor(channelWecom, bad.URL, nil)
	if _, err := badDir.token(ctx, "corp", "wrong"); err == nil {
		t.Fatal("errcode 非 0 时应该报错")
	} else if !strings.Contains(err.Error(), "40001") ||
		!strings.Contains(err.Error(), "invalid credential") {
		t.Fatalf("报错没带上上游原话: %v", err)
	}
}

func TestFeishuDirectoryUsesCodeAndSkipsResigned(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/open-apis/auth/v3/tenant_access_token/internal",
		func(w http.ResponseWriter, r *http.Request) {
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["app_secret"] != "secret" {
				// 飞书用 code 而不是 errcode，同样是 200
				_, _ = w.Write([]byte(`{"code":99991663,"msg":"app secret invalid"}`))
				return
			}
			_, _ = w.Write([]byte(`{"code":0,"msg":"ok","tenant_access_token":"t-feishu"}`))
		})
	mux.HandleFunc("/open-apis/contact/v3/departments", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer t-feishu" {
			t.Errorf("飞书通讯录要用 Bearer 头，实际 %q", r.Header.Get("Authorization"))
		}
		if r.URL.Query().Get("page_token") == "" {
			_, _ = w.Write([]byte(`{"code":0,"data":{"has_more":true,"page_token":"p2","items":[
				{"department_id":"d1","parent_department_id":"0","name":"研发中心"}]}}`))
			return
		}
		_, _ = w.Write([]byte(`{"code":0,"data":{"has_more":false,"items":[
			{"department_id":"d2","parent_department_id":"d1","name":"平台组"}]}}`))
	})
	mux.HandleFunc("/open-apis/contact/v3/users/find_by_department",
		func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"code":0,"data":{"has_more":false,"items":[
				{"open_id":"ou_1","name":"在职的","email":"a@corp.com","department_ids":["d2"],
				 "status":{"is_activated":true,"is_resigned":false}},
				{"open_id":"ou_2","name":"离职的","department_ids":["d2"],
				 "status":{"is_activated":true,"is_resigned":true}}]}}`))
		})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	dir, _ := imDirectoryFor(channelFeishu, srv.URL, nil)
	ctx := context.Background()
	if _, err := dir.token(ctx, "app", "wrong"); err == nil ||
		!strings.Contains(err.Error(), "99991663") {
		t.Fatalf("飞书的 code 错误没被识别: %v", err)
	}
	token, err := dir.token(ctx, "app", "secret")
	if err != nil {
		t.Fatalf("取 token 失败: %v", err)
	}
	// 分页要翻完
	depts, err := dir.departments(ctx, token, "0")
	if err != nil || len(depts) != 2 {
		t.Fatalf("分页没翻完: %v / %+v", err, depts)
	}
	people, err := dir.users(ctx, token, "d2")
	if err != nil {
		t.Fatalf("取成员失败: %v", err)
	}
	if len(people) != 2 {
		t.Fatalf("成员数不对: %+v", people)
	}
	if !people[0].Active || people[1].Active {
		t.Fatalf("离职标记没解出来: %+v", people)
	}
}

func TestDingtalkDirectoryWalksTreeAndPages(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/gettoken", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"errcode":0,"access_token":"t-ding"}`))
	})
	mux.HandleFunc("/topapi/v2/department/listsub", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		// 钉钉只给直接子部门，适配器要自己一层层往下走
		switch body["dept_id"] {
		case "1":
			_, _ = w.Write([]byte(`{"errcode":0,"result":[{"dept_id":10,"parent_id":1,"name":"技术部"}]}`))
		case "10":
			_, _ = w.Write([]byte(`{"errcode":0,"result":[{"dept_id":11,"parent_id":10,"name":"SRE"}]}`))
		default:
			_, _ = w.Write([]byte(`{"errcode":0,"result":[]}`))
		}
	})
	mux.HandleFunc("/topapi/v2/user/list", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["cursor"] == float64(0) {
			_, _ = w.Write([]byte(`{"errcode":0,"result":{"has_more":true,"next_cursor":1,"list":[
				{"userid":"u1","name":"第一页","dept_id_list":[11],"active":true}]}}`))
			return
		}
		_, _ = w.Write([]byte(`{"errcode":0,"result":{"has_more":false,"list":[
			{"userid":"u2","name":"第二页","dept_id_list":[11],"active":false}]}}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	dir, _ := imDirectoryFor(channelDingTalk, srv.URL, nil)
	ctx := context.Background()
	token, err := dir.token(ctx, "key", "secret")
	if err != nil {
		t.Fatalf("取 token 失败: %v", err)
	}
	depts, err := dir.departments(ctx, token, "1")
	if err != nil {
		t.Fatalf("取部门失败: %v", err)
	}
	if len(depts) != 2 || depts[1].Name != "SRE" || depts[1].ParentID != "10" {
		t.Fatalf("树没走完: %+v", depts)
	}
	people, err := dir.users(ctx, token, "11")
	if err != nil {
		t.Fatalf("取成员失败: %v", err)
	}
	if len(people) != 2 || people[1].ID != "u2" || people[1].Active {
		t.Fatalf("游标分页不对: %+v", people)
	}
}

// ---------- 同步语义 ----------

func newImTestHandler(t *testing.T) (*Handler, *gin.Engine) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	g, err := gorm.Open(sqlite.Open("file::memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开内存库失败: %v", err)
	}
	if err := g.AutoMigrate(&model.ImApp{}, &model.ImAccount{}, &model.ImSyncRun{},
		&model.Company{}, &model.Department{}, &model.User{}, &model.Role{}); err != nil {
		t.Fatalf("建表失败: %v", err)
	}
	h := New(g, &config.Config{})
	engine := gin.New()
	engine.Use(func(c *gin.Context) {
		c.Set("ctx_user", &model.User{ID: 1, Username: "admin"})
		c.Next()
	})
	engine.POST("/system/im/apps/:id/sync", h.SyncImApp)
	return h, engine
}

// seedImApp 造一个指向假企微的应用
func seedImApp(t *testing.T, h *Handler, base string) model.ImApp {
	t.Helper()
	company := model.Company{Name: "测试公司", Code: "test"}
	if err := h.DB.Create(&company).Error; err != nil {
		t.Fatalf("建公司失败: %v", err)
	}
	role := model.Role{Code: "viewer", Name: "只读"}
	if err := h.DB.Create(&role).Error; err != nil {
		t.Fatalf("建角色失败: %v", err)
	}
	app := model.ImApp{
		Name: "企微", Provider: channelWecom, CorpID: "corp", AppSecret: "secret",
		BaseURL: base, RootDeptID: "1", TargetCompanyID: company.ID,
		DefaultRoleID: role.ID, DisableMissing: true, Enabled: true, AppStatus: "unknown",
	}
	if err := h.DB.Create(&app).Error; err != nil {
		t.Fatalf("建应用失败: %v", err)
	}
	return app
}

func fetchAndApply(t *testing.T, h *Handler, app *model.ImApp, dryRun bool) *imSyncReport {
	t.Helper()
	current, err := h.fetchDirectory(context.Background(), app)
	if err != nil {
		t.Fatalf("拉通讯录失败: %v", err)
	}
	report, err := h.applySync(current, dryRun)
	if err != nil {
		t.Fatalf("同步失败: %v", err)
	}
	return report
}

func TestFetchDirectoryDeduplicatesUsers(t *testing.T) {
	srv := newFakeWecom(t, 0)
	h, _ := newImTestHandler(t)
	app := seedImApp(t, h, srv.URL)

	current, err := h.fetchDirectory(context.Background(), &app)
	if err != nil {
		t.Fatalf("拉通讯录失败: %v", err)
	}
	if len(current.depts) != 3 {
		t.Fatalf("部门数不对: %d", len(current.depts))
	}
	// 李四挂在 2 和 3 两个部门下，去重后只应有一条，部门合并
	if len(current.users) != 3 {
		t.Fatalf("成员应去重成 3 人，实际 %d: %+v", len(current.users), current.users)
	}
	var lisi *imUser
	for i := range current.users {
		if current.users[i].ID == "lisi" {
			lisi = &current.users[i]
		}
	}
	if lisi == nil || len(lisi.DeptIDs) != 2 {
		t.Fatalf("重复成员的部门没合并: %+v", lisi)
	}
}

func TestApplySyncDryRunWritesNothing(t *testing.T) {
	srv := newFakeWecom(t, 0)
	h, _ := newImTestHandler(t)
	app := seedImApp(t, h, srv.URL)

	report := fetchAndApply(t, h, &app, true)
	if !report.DryRun || report.DeptCreated != 3 || report.UserCreated != 3 {
		t.Fatalf("预演报告不对: %+v", report)
	}
	if len(report.Changes) == 0 {
		t.Fatal("预演应该列出将要发生的改动")
	}

	// 预演的重点：一行都不能写
	var depts, users, bindings int64
	h.DB.Model(&model.Department{}).Count(&depts)
	h.DB.Model(&model.User{}).Count(&users)
	h.DB.Model(&model.ImAccount{}).Count(&bindings)
	if depts != 0 || users != 0 || bindings != 0 {
		t.Fatalf("预演写库了: depts=%d users=%d bindings=%d", depts, users, bindings)
	}
}

func TestApplySyncCreatesTreeAndUsers(t *testing.T) {
	srv := newFakeWecom(t, 0)
	h, _ := newImTestHandler(t)
	app := seedImApp(t, h, srv.URL)

	report := fetchAndApply(t, h, &app, false)
	if report.DeptCreated != 3 || report.UserCreated != 3 {
		t.Fatalf("同步结果不对: %+v", report)
	}

	// 部门树的父子关系要真的接上，Code 里记着 IM 侧的 id
	var infra model.Department
	if err := h.DB.Where("code = ?", imDeptCode(channelWecom, "3")).First(&infra).Error; err != nil {
		t.Fatalf("没找到同步出来的部门: %v", err)
	}
	var ops model.Department
	h.DB.Where("code = ?", imDeptCode(channelWecom, "2")).First(&ops)
	if infra.ParentID != ops.ID {
		t.Fatalf("父子关系没接上: infra.parent=%d ops.id=%d", infra.ParentID, ops.ID)
	}
	if infra.CompanyID != app.TargetCompanyID {
		t.Fatalf("部门没挂到目标公司下: %+v", infra)
	}

	// 用户：主部门取第一个、口令是随机的、默认角色只在新建时给
	var zhangsan model.User
	if err := h.DB.Where("username = ?", "zhangsan").First(&zhangsan).Error; err != nil {
		t.Fatalf("没建出用户: %v", err)
	}
	if zhangsan.Nickname != "张三" || zhangsan.Email != "zhangsan@corp.com" {
		t.Fatalf("用户资料不对: %+v", zhangsan)
	}
	if zhangsan.DeptID != ops.ID {
		t.Fatalf("主部门不对: %d != %d", zhangsan.DeptID, ops.ID)
	}
	if zhangsan.PasswordHash == "" {
		t.Fatal("口令哈希不能为空（列是 not null）")
	}
	// 随机口令：不能是可猜的固定值
	for _, guess := range []string{"", "123456", "password", "zhangsan"} {
		if bcrypt.CompareHashAndPassword([]byte(zhangsan.PasswordHash), []byte(guess)) == nil {
			t.Fatalf("新建用户的口令可以被猜到: %q", guess)
		}
	}
	var roles []model.Role
	h.DB.Model(&zhangsan).Association("Roles").Find(&roles)
	if len(roles) != 1 || roles[0].Code != "viewer" {
		t.Fatalf("默认角色没给上: %+v", roles)
	}

	// IM 侧已禁用/退出企业的人，建出来就是停用状态
	var wangwu model.User
	h.DB.Where("username = ?", "wangwu").First(&wangwu)
	if wangwu.Status != 0 {
		t.Fatalf("退出企业的人应该建成停用: %+v", wangwu)
	}

	// 绑定关系带上部门路径，便于人工核对
	var binding model.ImAccount
	h.DB.Where("im_user_id = ?", "zhangsan").First(&binding)
	if binding.UserID != zhangsan.ID || binding.Provider != channelWecom {
		t.Fatalf("绑定关系不对: %+v", binding)
	}
	if !strings.Contains(binding.ImDeptPath, "运维部") {
		t.Fatalf("部门路径没记上: %q", binding.ImDeptPath)
	}
	if binding.ImMobile != "13800000001" {
		t.Fatalf("手机号没落到绑定表: %+v", binding)
	}
}

func TestApplySyncIsIdempotentAndKeepsLocalChanges(t *testing.T) {
	srv := newFakeWecom(t, 0)
	h, _ := newImTestHandler(t)
	app := seedImApp(t, h, srv.URL)

	fetchAndApply(t, h, &app, false)

	// 管理员手工把张三的角色换成 admin、改了昵称
	adminRole := model.Role{Code: "admin", Name: "管理员"}
	h.DB.Create(&adminRole)
	var zhangsan model.User
	h.DB.Where("username = ?", "zhangsan").First(&zhangsan)
	if err := h.DB.Model(&zhangsan).Association("Roles").Replace([]model.Role{adminRole}); err != nil {
		t.Fatalf("改角色失败: %v", err)
	}
	originalHash := zhangsan.PasswordHash

	// 再同步一次：不该重复建人，也不该动角色与口令
	report := fetchAndApply(t, h, &app, false)
	if report.UserCreated != 0 || report.DeptCreated != 0 {
		t.Fatalf("第二次同步不该再新建: %+v", report)
	}
	var users int64
	h.DB.Model(&model.User{}).Count(&users)
	if users != 3 {
		t.Fatalf("用户被重复创建: %d", users)
	}

	h.DB.Where("username = ?", "zhangsan").First(&zhangsan)
	var roles []model.Role
	h.DB.Model(&zhangsan).Association("Roles").Find(&roles)
	if len(roles) != 1 || roles[0].Code != "admin" {
		t.Fatalf("同步把手工调过的角色抹平了: %+v", roles)
	}
	if zhangsan.PasswordHash != originalHash {
		t.Fatal("同步动了口令")
	}
}

func TestApplySyncBindsExistingUsernameInsteadOfCreating(t *testing.T) {
	srv := newFakeWecom(t, 0)
	h, _ := newImTestHandler(t)
	app := seedImApp(t, h, srv.URL)

	// 平台里已经有一个同名账号（手工建的），且有自己的角色
	existing := model.User{Username: "zhangsan", PasswordHash: "local-hash",
		Nickname: "本地建的张三", Status: 1}
	if err := h.DB.Create(&existing).Error; err != nil {
		t.Fatalf("建本地用户失败: %v", err)
	}

	report := fetchAndApply(t, h, &app, false)
	if report.UserBound != 1 {
		t.Fatalf("应该绑定而不是新建: %+v", report)
	}
	var count int64
	h.DB.Model(&model.User{}).Where("username = ?", "zhangsan").Count(&count)
	if count != 1 {
		t.Fatalf("同名账号被重复创建: %d", count)
	}
	var binding model.ImAccount
	h.DB.Where("im_user_id = ?", "zhangsan").First(&binding)
	if binding.UserID != existing.ID {
		t.Fatalf("绑定指向的不是已有账号: %+v", binding)
	}
	// 绑定当次不改资料（下一次同步才按 update 走），口令一定不动
	var after model.User
	h.DB.First(&after, existing.ID)
	if after.PasswordHash != "local-hash" {
		t.Fatal("绑定时动了本地账号的口令")
	}
}

func TestApplySyncDisablesMissingButNeverDeletes(t *testing.T) {
	srv := newFakeWecom(t, 0)
	h, _ := newImTestHandler(t)
	app := seedImApp(t, h, srv.URL)
	fetchAndApply(t, h, &app, false)

	// 造一个「IM 侧已经查不到」的绑定：离职的人
	gone := model.User{Username: "leaver", PasswordHash: "x", Status: 1}
	h.DB.Create(&gone)
	h.DB.Create(&model.ImAccount{AppID: app.ID, Provider: channelWecom,
		ImUserID: "leaver", UserID: gone.ID, Username: gone.Username})
	// 再造一个绑到内置 admin(id=1) 的，验证永不停用自己
	admin := model.User{Username: "builtin-admin", PasswordHash: "x", Status: 1}
	h.DB.Create(&admin) // 内存库里第一条用户 id 可能不是 1，显式指定
	h.DB.Model(&model.User{}).Where("id = ?", admin.ID).Update("id", 1)
	h.DB.Create(&model.ImAccount{AppID: app.ID, Provider: channelWecom,
		ImUserID: "ghost-admin", UserID: 1, Username: "builtin-admin"})

	report := fetchAndApply(t, h, &app, false)
	if report.UserDisabled != 1 {
		t.Fatalf("应该只停用 1 个人（离职那个）: %+v", report)
	}
	var after model.User
	if err := h.DB.Where("username = ?", "leaver").First(&after).Error; err != nil {
		t.Fatalf("离职的人被删掉了，应该只停用: %v", err)
	}
	if after.Status != 0 {
		t.Fatalf("离职的人没被停用: %+v", after)
	}
	var builtin model.User
	h.DB.First(&builtin, 1)
	if builtin.Status != 1 {
		t.Fatalf("内置管理员被停用了: %+v", builtin)
	}

	// 关掉开关后不再停用
	h.DB.Model(&model.ImApp{}).Where("id = ?", app.ID).
		Updates(map[string]any{"disable_missing": false})
	h.DB.First(&app, app.ID)
	h.DB.Model(&model.User{}).Where("username = ?", "leaver").Update("status", 1)
	report = fetchAndApply(t, h, &app, false)
	if report.UserDisabled != 0 {
		t.Fatalf("关掉开关后不该停用任何人: %+v", report)
	}
}

func TestApplySyncUpdatesProfileAndRestoresStatus(t *testing.T) {
	srv := newFakeWecom(t, 0)
	h, _ := newImTestHandler(t)
	app := seedImApp(t, h, srv.URL)
	fetchAndApply(t, h, &app, false)

	// 有人被手工停用了，但 IM 侧仍在职：下次同步要恢复启用
	h.DB.Model(&model.User{}).Where("username = ?", "zhangsan").
		Updates(map[string]any{"status": 0, "nickname": "旧名字", "email": "old@corp.com"})

	report := fetchAndApply(t, h, &app, false)
	if report.UserUpdated < 1 {
		t.Fatalf("应该有资料更新: %+v", report)
	}
	var user model.User
	h.DB.Where("username = ?", "zhangsan").First(&user)
	if user.Nickname != "张三" || user.Email != "zhangsan@corp.com" {
		t.Fatalf("资料没同步回来: %+v", user)
	}
	if user.Status != 1 {
		t.Fatalf("IM 侧在职的人应该恢复启用: %+v", user)
	}
}

func TestSyncImAppRecordsRunAndRejectsDisabled(t *testing.T) {
	srv := newFakeWecom(t, 0)
	h, engine := newImTestHandler(t)
	app := seedImApp(t, h, srv.URL)

	code, body := postModelJSON(t, engine, "/system/im/apps/"+itoa(app.ID)+"/sync",
		`{"dryRun":true}`)
	if code != http.StatusOK || body["code"] != float64(0) {
		t.Fatalf("预演接口失败: %d %+v", code, body)
	}
	data, _ := body["data"].(map[string]any)
	run, _ := data["run"].(map[string]any)
	if run["dryRun"] != true || run["status"] != "success" {
		t.Fatalf("同步记录不对: %+v", run)
	}
	if run["deptCreated"] != float64(3) || run["userCreated"] != float64(3) {
		t.Fatalf("同步记录里的计数不对: %+v", run)
	}
	var runs int64
	h.DB.Model(&model.ImSyncRun{}).Count(&runs)
	if runs != 1 {
		t.Fatalf("预演也应该留一条记录: %d", runs)
	}

	// 停用的应用不能同步
	h.DB.Model(&model.ImApp{}).Where("id = ?", app.ID).
		Updates(map[string]any{"enabled": false})
	_, body = postModelJSON(t, engine, "/system/im/apps/"+itoa(app.ID)+"/sync", `{}`)
	if body["code"] == float64(0) {
		t.Fatalf("停用的应用不该能同步: %+v", body)
	}
	if msg, _ := body["msg"].(string); !strings.Contains(msg, "已停用") {
		t.Fatalf("报错不明确: %q", msg)
	}
}

func TestSyncImAppRecordsFailure(t *testing.T) {
	// 密钥错：错误藏在 200 的 errcode 里
	srv := newFakeWecom(t, 40001)
	h, engine := newImTestHandler(t)
	app := seedImApp(t, h, srv.URL)

	_, body := postModelJSON(t, engine, "/system/im/apps/"+itoa(app.ID)+"/sync", `{}`)
	if body["code"] == float64(0) {
		t.Fatalf("凭据错时不该成功: %+v", body)
	}
	if msg, _ := body["msg"].(string); !strings.Contains(msg, "40001") {
		t.Fatalf("没带上上游错误码: %q", msg)
	}
	var run model.ImSyncRun
	if err := h.DB.Order("id desc").First(&run).Error; err != nil {
		t.Fatalf("失败也该留记录: %v", err)
	}
	if run.SyncStatus != "failed" || !strings.Contains(run.ErrorMsg, "40001") {
		t.Fatalf("失败记录不对: %+v", run)
	}
}

func TestImAppReqNormalize(t *testing.T) {
	req := imAppReq{Name: " 企微 ", Provider: " WeCom ", CorpID: " corp ",
		TargetCompanyID: 1, BaseURL: "https://x/ "}
	if err := req.normalize(); err != nil {
		t.Fatalf("合法请求被拒: %v", err)
	}
	// 企微/钉钉默认从 1 开始，飞书从 0
	if req.RootDeptID != "1" || req.Provider != channelWecom || req.BaseURL != "https://x" {
		t.Fatalf("归一化不对: %+v", req)
	}
	feishu := imAppReq{Name: "飞书", Provider: channelFeishu, CorpID: "app", TargetCompanyID: 1}
	if err := feishu.normalize(); err != nil || feishu.RootDeptID != "0" {
		t.Fatalf("飞书默认起点应为 0: %v / %q", err, feishu.RootDeptID)
	}

	cases := []struct {
		name string
		req  imAppReq
		want string
	}{
		{"没名字", imAppReq{Provider: channelWecom, CorpID: "c", TargetCompanyID: 1}, "名称"},
		{"类型不支持", imAppReq{Name: "a", Provider: "slack", CorpID: "c",
			TargetCompanyID: 1}, "wecom / dingtalk / feishu"},
		{"没企业标识", imAppReq{Name: "a", Provider: channelWecom, TargetCompanyID: 1}, "企业标识"},
		{"没选公司", imAppReq{Name: "a", Provider: channelWecom, CorpID: "c"}, "哪个公司"},
		{"地址没协议", imAppReq{Name: "a", Provider: channelWecom, CorpID: "c",
			TargetCompanyID: 1, BaseURL: "qyapi.weixin.qq.com"}, "http:// 或 https://"},
	}
	for _, tc := range cases {
		local := tc.req
		err := local.normalize()
		if err == nil {
			t.Fatalf("%s 应该报错", tc.name)
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s 的报错是 %q，期望包含 %q", tc.name, err, tc.want)
		}
	}
}
