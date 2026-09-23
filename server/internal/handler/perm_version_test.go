package handler

// 权限版本化的测试。
//
// 这几条守的是「权限改错了查不出来、也改不回去」这一类问题，
// 以及一条更要紧的：**版本化机制自己不能成为提权路径**。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"ops-platform/server/internal/config"
	"ops-platform/server/internal/middleware"
	"ops-platform/server/internal/model"
)

func newPermVersionHandler(t *testing.T) *Handler {
	t.Helper()
	g, err := gorm.Open(sqlite.Open(t.TempDir()+"/perm.db"),
		&gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	if err := g.AutoMigrate(&model.Role{}, &model.Menu{}, &model.RuleVersion{},
		&model.ResourceGrant{}, &model.KubeGrant{}, &model.User{}); err != nil {
		t.Fatalf("建表失败: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, err := g.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	// 两个按钮权限，用来验证 authCodes 的 diff
	g.Create(&model.Menu{ID: 10, Title: "下发命令", Type: "button", AuthCode: "exec:run"})
	g.Create(&model.Menu{ID: 11, Title: "生产执行", Type: "button", AuthCode: "exec:prod"})
	return New(g, &config.Config{})
}

// 每个 target 都必须填 WritePerm：漏填会让 allowRuleWrite 走到「拒绝」，
// 那比放行安全，但更应该在测试里先被发现
func TestEveryTargetHasWritePerm(t *testing.T) {
	for _, spec := range ruleTargetSpecs {
		if spec.WritePerm == "" {
			t.Errorf("target %q（%s）没填 WritePerm —— 回滚权限码写死在路由上"+
				"就意味着「拿一个业务权限能改另一类东西」", spec.Target, spec.Label)
		}
		if spec.Label == "" || len(spec.Fields) == 0 {
			t.Errorf("target %q 的 Label / Fields 不能空", spec.Target)
		}
	}
	// 权限类 target 的读也要权限：快照内容就是权限配置本身
	for _, target := range []string{ruleTargetRole, ruleTargetResourceGrnt, ruleTargetKubeGrant} {
		spec, ok := ruleSpecOf(target)
		if !ok {
			t.Fatalf("%s 没注册进 ruleTargetSpecs", target)
		}
		if spec.ReadPerm == "" {
			t.Errorf("%s 的 ReadPerm 为空 —— 角色有哪些按钮码、谁被授权了哪台主机，"+
				"这份东西是侦察材料，读也该要权限", target)
		}
	}
}

// 角色快照要包含菜单与派生的权限码，且顺序稳定（否则每次保存都多出一版）
func TestRoleSnapshotIsStable(t *testing.T) {
	h := newPermVersionHandler(t)
	role := model.Role{ID: 5, Code: "ops", Name: "运维", DataScope: "dept"}
	h.DB.Create(&role)
	h.bindMenus(&role, []uint{11, 10}) // 故意倒序绑

	row, ok := roleSnapshotLoad(h, 5)
	if !ok {
		t.Fatal("角色快照读不到")
	}
	if row.Snapshot["authCodes"] != "exec:prod,exec:run" {
		t.Fatalf("authCodes 应该排序后输出，实际 %v", row.Snapshot["authCodes"])
	}
	ids, _ := row.Snapshot["menuIds"].([]uint)
	if len(ids) != 2 || ids[0] != 10 || ids[1] != 11 {
		t.Fatalf("menuIds 应该升序，实际 %v", ids)
	}

	_, hash1 := marshalSnapshot(row.Snapshot, ruleFieldsOf(t, ruleTargetRole))
	row2, _ := roleSnapshotLoad(h, 5)
	_, hash2 := marshalSnapshot(row2.Snapshot, ruleFieldsOf(t, ruleTargetRole))
	if hash1 != hash2 {
		t.Fatal("同一份权限两次读出来 hash 不同 —— 那会让每次保存都多出一版空改动")
	}
}

func ruleFieldsOf(t *testing.T, target string) []ruleField {
	t.Helper()
	spec, ok := ruleSpecOf(target)
	if !ok {
		t.Fatalf("%s 没注册", target)
	}
	return spec.Fields
}

// 回滚要把权限**整份替换**回去，而不是只补差集
func TestRoleRollbackReplacesMenus(t *testing.T) {
	h := newPermVersionHandler(t)
	role := model.Role{ID: 5, Code: "ops", Name: "运维", DataScope: "dept"}
	h.DB.Create(&role)
	h.bindMenus(&role, []uint{10})
	if !h.recordVersion(ruleTargetRole, 5, "created", "", "tester") {
		t.Fatal("第一版没记上")
	}

	// 模拟一次「顺手勾上生产执行 + 把数据范围放到 all」
	h.DB.Model(&model.Role{}).Where("id = ?", 5).Update("data_scope", "all")
	h.DB.First(&role, 5)
	h.bindMenus(&role, []uint{10, 11})
	if !h.recordVersion(ruleTargetRole, 5, "edited", "", "tester") {
		t.Fatal("第二版没记上")
	}

	var v1 model.RuleVersion
	h.DB.Where("target = ? AND target_id = ? AND version = 1", ruleTargetRole, 5).First(&v1)
	var snapshot map[string]any
	if err := json.Unmarshal([]byte(v1.Content), &snapshot); err != nil {
		t.Fatalf("快照解不开: %v", err)
	}
	spec, _ := ruleSpecOf(ruleTargetRole)
	updates, err := spec.ToUpdates(snapshot)
	if err != nil {
		t.Fatalf("ToUpdates 失败: %v", err)
	}
	if err := spec.Apply(h, 5, updates); err != nil {
		t.Fatalf("回滚失败: %v", err)
	}

	var after model.Role
	h.DB.Preload("Menus").First(&after, 5)
	if after.DataScope != "dept" {
		t.Fatalf("数据范围应该回到 dept，实际 %s", after.DataScope)
	}
	if len(after.Menus) != 1 || after.Menus[0].ID != 10 {
		t.Fatalf("菜单应该整份替换回只有 10，实际 %+v —— "+
			"只补差集的话回滚留不掉本该去掉的权限", after.Menus)
	}
}

// 删除要留一版，而且那一版能把角色恢复出来
func TestRoleDeleteLeavesRestorableVersion(t *testing.T) {
	h := newPermVersionHandler(t)
	role := model.Role{ID: 6, Code: "temp", Name: "临时角色", DataScope: "self"}
	h.DB.Create(&role)
	h.bindMenus(&role, []uint{10, 11})

	if !h.recordVersionBeforeDelete(ruleTargetRole, 6, "tester") {
		t.Fatal("删前应该留一版")
	}
	h.DB.Delete(&model.Role{}, 6)

	var deleted model.RuleVersion
	if err := h.DB.Where("target = ? AND source = ?", ruleTargetRole, "deleted").
		First(&deleted).Error; err != nil {
		t.Fatalf("找不到删除版: %v", err)
	}
	if deleted.TargetName != "临时角色" {
		t.Fatalf("版本里要冗余存名字，否则删完读不懂，实际 %q", deleted.TargetName)
	}

	var snapshot map[string]any
	json.Unmarshal([]byte(deleted.Content), &snapshot)
	spec, _ := ruleSpecOf(ruleTargetRole)
	updates, err := spec.ToUpdates(snapshot)
	if err != nil {
		t.Fatalf("ToUpdates 失败: %v", err)
	}
	newID, err := spec.Create(h, updates, 1)
	if err != nil {
		t.Fatalf("恢复失败: %v", err)
	}
	var restored model.Role
	h.DB.Preload("Menus").First(&restored, newID)
	if len(restored.Menus) != 2 {
		t.Fatalf("恢复出来的角色应该带回两个菜单，实际 %d", len(restored.Menus))
	}
	if !strings.HasPrefix(restored.Code, "restored_") {
		t.Fatalf("恢复出来的角色编码要是占位值（提醒人去改），实际 %q", restored.Code)
	}
}

// 授权的动作清单非法时必须让回滚失败，**不能兜成 `*`**：
// 那会把「这一版记了个坏值」变成「回滚后权限放大到全部」
func TestGrantRollbackRefusesBadActions(t *testing.T) {
	_, err := grantSnapshotToUpdates(map[string]any{
		"subjectId": float64(1), "resourceId": float64(2),
		"actions": "terminal,不存在的动作",
	})
	if err == nil {
		t.Fatal("非法动作必须让回滚失败，而不是兜成 *")
	}
	if !strings.Contains(err.Error(), "不合法") {
		t.Fatalf("错误要说清是动作清单的问题，实际: %v", err)
	}
}

// 自定义部门范围却没有部门清单：回滚会让角色什么都看不到，应该拒绝
func TestRoleRollbackRefusesEmptyCustomScope(t *testing.T) {
	_, err := roleSnapshotToUpdates(map[string]any{
		"name": "某角色", "dataScope": "custom", "dataDeptIds": "[]",
	})
	if err == nil {
		t.Fatal("custom 范围但部门为空时应该拒绝回滚")
	}
	if !strings.Contains(err.Error(), "什么都看不到") {
		t.Fatalf("要说清后果，实际: %v", err)
	}
}

// 没有对应权限码的人既不能读也不能回滚权限类版本
func TestPermVersionNeedsItsOwnPerm(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := newPermVersionHandler(t)
	role := model.Role{ID: 5, Code: "ops", Name: "运维", DataScope: "dept"}
	h.DB.Create(&role)
	h.recordVersion(ruleTargetRole, 5, "created", "", "tester")

	newEngine := func(perms ...string) *gin.Engine {
		engine := gin.New()
		engine.Use(func(c *gin.Context) {
			set := map[string]struct{}{}
			for _, code := range perms {
				set[code] = struct{}{}
			}
			c.Set("ctx_user", &model.User{ID: 1, Username: "u"})
			c.Set("ctx_perms", set)
			c.Next()
		})
		engine.GET("/versions", h.ListRuleVersions)
		engine.POST("/rollback", h.RollbackRule)
		return engine
	}

	// 只有告警规则权限的人：读不到角色历史，也回滚不了
	engine := newEngine("alertrule:manage")
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/versions?target=role&targetId=5", nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("没有 role:manage 不该读到角色变更历史，实际 %d %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	engine.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/rollback",
		strings.NewReader(`{"target":"role","ruleId":5,"versionId":1}`)))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("拿告警规则权限不该能回滚角色 —— 那就是一条提权路径，实际 %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "role:manage") {
		t.Fatalf("错误要点名缺哪个权限，实际 %s", rec.Body.String())
	}

	// 有 role:manage 的人可以读
	rec = httptest.NewRecorder()
	newEngine("role:manage").ServeHTTP(rec,
		httptest.NewRequest(http.MethodGet, "/versions?target=role&targetId=5", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("有 role:manage 应该能读，实际 %d %s", rec.Code, rec.Body.String())
	}
	_ = middleware.Perms
}
