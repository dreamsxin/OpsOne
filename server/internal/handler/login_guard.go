package handler

// 登录失败限速与登录审计。
//
// # 审计发现的两个缺口
//
//  1. **登录完全没有失败次数限制**：没有计数、没有锁定、没有延迟、没有验证码、
//     没有 IP 限速。口令错了直接 401 返回，可以无限试。
//  2. **登录行为不进审计表**：`POST /auth/login` 在 auth 组**之外**，
//     审计中间件根本不经过它。成功登录只更新 `users.last_login_at`，
//     **失败登录零留痕** —— 也就是说「有人在爆破某个账号」这件事，事后查不出来。
//
// # 计数放内存而不是库
//
// 锁定状态是**短时**信息（默认 15 分钟），写库意味着每次登录失败都产生一次写事务，
// 而登录接口恰好是被爆破时压力最大的地方 —— 那会把 SQLite 的写锁变成放大器。
// 代价说清楚：**进程重启后计数归零**，而且多实例时各自计数。
// 在单实例内网部署下这个取舍是划算的；真需要跨实例的锁定得换存储。
//
// 留痕走另一条路：失败登录**照样写审计表**（那是一条每次都值得的写入，
// 而且爆破的痕迹必须留下），只是计数不落库。

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"ops-platform/server/internal/model"
)

// loginGate 登录失败计数器
type loginGate struct {
	mu    sync.Mutex
	items map[string]*loginAttempt
}

type loginAttempt struct {
	Fails    int
	LockedTo time.Time
	// LastFail 用来过期清理：很久没失败过的条目没必要留着
	LastFail time.Time
}

var loginGuard = &loginGate{items: map[string]*loginAttempt{}}

// loginGateKey 按「用户名 + 来源 IP」计数。
//
// 只按用户名会让一个人从一个 IP 输错几次就把别人也锁在外面（同名爆破 = 拒绝服务）；
// 只按 IP 则挡不住从一个 IP 轮着试不同用户名。两者合起来才是常见做法。
func loginGateKey(username, ip string) string {
	return strings.ToLower(strings.TrimSpace(username)) + "|" + ip
}

// checkLoginLock 返回「还要等多久」。零值表示没被锁。
func (g *loginGate) check(key string, now time.Time) time.Duration {
	g.mu.Lock()
	defer g.mu.Unlock()
	item := g.items[key]
	if item == nil || item.LockedTo.Before(now) {
		return 0
	}
	return item.LockedTo.Sub(now)
}

// fail 记一次失败，达到阈值就锁。返回当前失败次数与是否刚刚被锁上。
func (g *loginGate) fail(key string, maxFail int, lock time.Duration, now time.Time) (int, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.sweepLocked(now)

	item := g.items[key]
	if item == nil {
		item = &loginAttempt{}
		g.items[key] = item
	}
	item.Fails++
	item.LastFail = now
	if item.Fails >= maxFail {
		item.LockedTo = now.Add(lock)
		// 锁上之后把计数归零：解锁后再错一次不应该立刻又被锁
		item.Fails = 0
		return maxFail, true
	}
	return item.Fails, false
}

// success 登录成功，清掉这个 key 的计数
func (g *loginGate) success(key string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.items, key)
}

// sweepLocked 清掉一小时内既没失败也没在锁定中的条目，避免 map 无限长
func (g *loginGate) sweepLocked(now time.Time) {
	if len(g.items) < 1024 {
		return
	}
	for key, item := range g.items {
		if item.LockedTo.After(now) {
			continue
		}
		if now.Sub(item.LastFail) > time.Hour {
			delete(g.items, key)
		}
	}
}

// auditLogin 把一次登录尝试写进审计表。
//
// 登录接口在 auth 组之外，中间件管不到，所以这里显式写。
// **成功与失败都写** —— 只记成功的话，爆破痕迹正好是被丢掉的那一半。
func (h *Handler) auditLogin(c *gin.Context, username string, user *model.User, ok bool, detail string) {
	status := 401
	if ok {
		status = 200
	}
	entry := model.AuditLog{
		Method:   c.Request.Method,
		Path:     c.Request.URL.Path,
		Action:   "auth:login",
		Status:   status,
		IP:       c.ClientIP(),
		Username: username,
		Detail:   detail,
	}
	if user != nil {
		entry.UserID = user.ID
		// 用库里的用户名而不是请求里的：大小写/空格差异会让按人检索漏掉
		entry.Username = user.Username
	}
	// 审计写失败不能影响登录本身
	_ = h.DB.Create(&entry).Error
}

// loginLockSettings 读锁定策略。放配置项而不是写死：
// 内网自用的团队可能觉得 5 次太紧，而对外暴露的实例可能想设 3 次。
func (h *Handler) loginLockSettings() (int, time.Duration) {
	maxFail := h.configInt(CfgLoginMaxFail, 5)
	if maxFail < 1 {
		// 0 或负数当成「不限制」—— 显式关掉是允许的，但要有人主动配成那样
		return 0, 0
	}
	minutes := h.configInt(CfgLoginLockMinutes, 15)
	if minutes < 1 {
		minutes = 1
	}
	return maxFail, time.Duration(minutes) * time.Minute
}

func lockMessage(left time.Duration) string {
	seconds := int(left.Seconds())
	if seconds < 60 {
		return fmt.Sprintf("登录失败次数过多，请 %d 秒后再试", seconds)
	}
	return fmt.Sprintf("登录失败次数过多，请 %d 分钟后再试", (seconds+59)/60)
}
