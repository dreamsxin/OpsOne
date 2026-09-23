package handler

// 执行历史可见性的测试。
//
// 复核时发现的口径不一致：会话里敲的命令单列了 session:view，而**批量下发的 stdout
// 往往更敏感**（`cat` 一个配置文件、证书会整份出现在输出里），却只要 exec:run
// 就能看全平台的。这组测试钉住修正后的口径。

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"ops-platform/server/internal/config"
	"ops-platform/server/internal/model"
)

func newExecScopeHandler(t *testing.T) *Handler {
	t.Helper()
	gin.SetMode(gin.TestMode)
	g, err := gorm.Open(sqlite.Open(t.TempDir()+"/execscope.db"),
		&gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	if err := g.AutoMigrate(&model.ExecJob{}, &model.ExecResult{}, &model.User{}); err != nil {
		t.Fatalf("建表失败: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, err := g.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	// 两个人各下发一次
	g.Create(&model.ExecJob{ID: 1, Name: "自己的", Command: "echo mine",
		CreatedBy: 7, Operator: "me", Status: "finished"})
	g.Create(&model.ExecJob{ID: 2, Name: "别人的", Command: "cat /etc/shadow",
		CreatedBy: 9, Operator: "other", Status: "finished"})
	return New(g, &config.Config{})
}

// execScopeEngine 造一个带指定权限码的请求环境
func execScopeEngine(h *Handler, userID uint, perms ...string) *gin.Engine {
	engine := gin.New()
	engine.Use(func(c *gin.Context) {
		set := map[string]struct{}{}
		for _, code := range perms {
			set[code] = struct{}{}
		}
		c.Set("ctx_user", &model.User{ID: userID, Username: "u"})
		c.Set("ctx_perms", set)
		c.Next()
	})
	engine.GET("/jobs", h.ListExecJobs)
	engine.GET("/jobs/:id", h.GetExecJob)
	return engine
}

// 只有 exec:run 的人：列表里只有自己下发的
func TestExecJobsScopedToSelfWithoutViewPerm(t *testing.T) {
	h := newExecScopeHandler(t)
	rec := httptest.NewRecorder()
	execScopeEngine(h, 7, "exec:run").ServeHTTP(rec,
		httptest.NewRequest(http.MethodGet, "/jobs?pageSize=20", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("自己的历史要看得到（否则下发完看不到结果），实际 %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "echo mine") {
		t.Fatalf("应该看到自己下发的：%s", body)
	}
	if strings.Contains(body, "/etc/shadow") {
		t.Fatalf("不该看到别人下发的命令 —— 批量下发的输出往往比会话更敏感：%s", body)
	}
}

// 有 exec:view 的人：看全部
func TestExecJobsFullWithViewPerm(t *testing.T) {
	h := newExecScopeHandler(t)
	rec := httptest.NewRecorder()
	execScopeEngine(h, 7, "exec:run", "exec:view").ServeHTTP(rec,
		httptest.NewRequest(http.MethodGet, "/jobs?pageSize=20", nil))
	body := rec.Body.String()
	if !strings.Contains(body, "echo mine") || !strings.Contains(body, "/etc/shadow") {
		t.Fatalf("有 exec:view 应该看到全部：%s", body)
	}
}

// 详情：别人的作业返回 404 而不是 403 —— 403 等于告诉对方「这个 ID 存在」
func TestExecJobDetailHidesOthersAs404(t *testing.T) {
	h := newExecScopeHandler(t)
	engine := execScopeEngine(h, 7, "exec:run")

	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/jobs/1", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("自己的作业详情应该能看，实际 %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "echo mine") {
		t.Fatalf("详情里要有命令：%s", rec.Body.String())
	}

	rec = httptest.NewRecorder()
	engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/jobs/2", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("别人的作业应该是 404（不是 403，403 会暴露 ID 存在），实际 %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "/etc/shadow") {
		t.Fatalf("404 的响应里不能带出内容：%s", rec.Body.String())
	}

	// 有 exec:view 时同一个 ID 就能看了
	rec = httptest.NewRecorder()
	execScopeEngine(h, 7, "exec:run", "exec:view").ServeHTTP(rec,
		httptest.NewRequest(http.MethodGet, "/jobs/2", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("有 exec:view 应该能看别人的，实际 %d", rec.Code)
	}
}

// 取不到登录者时给空结果，而不是「不加条件」——配错要表现为看不到东西
func TestExecJobsEmptyWhenNoUser(t *testing.T) {
	h := newExecScopeHandler(t)
	engine := gin.New()
	engine.Use(func(c *gin.Context) {
		c.Set("ctx_perms", map[string]struct{}{"exec:run": {}})
		c.Next()
	})
	engine.GET("/jobs", h.ListExecJobs)
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/jobs", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("不该报错，实际 %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "echo mine") ||
		strings.Contains(rec.Body.String(), "/etc/shadow") {
		t.Fatalf("拿不到登录者时应该是空结果：%s", rec.Body.String())
	}
}
