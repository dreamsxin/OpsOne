package handler

// 登录失败锁定与登录审计的测试。
//
// 审计发现这两件都没有：口令可以无限试，而且失败一次都不留痕 ——
// 「有人在爆破某个账号」事后查不出来。

import (
	"strings"
	"testing"
	"time"
)

func TestLoginGateLocksAfterMaxFail(t *testing.T) {
	gate := &loginGate{items: map[string]*loginAttempt{}}
	key := loginGateKey("Admin", "10.0.0.5")
	now := time.Now()

	for i := 1; i <= 4; i++ {
		fails, locked := gate.fail(key, 5, 15*time.Minute, now)
		if locked {
			t.Fatalf("第 %d 次就锁上了，阈值是 5", i)
		}
		if fails != i {
			t.Fatalf("第 %d 次的计数应该是 %d，实际 %d", i, i, fails)
		}
		if left := gate.check(key, now); left != 0 {
			t.Fatalf("没到阈值不该锁，实际还要等 %v", left)
		}
	}

	if _, locked := gate.fail(key, 5, 15*time.Minute, now); !locked {
		t.Fatal("第 5 次应该锁上")
	}
	left := gate.check(key, now)
	if left <= 14*time.Minute || left > 15*time.Minute {
		t.Fatalf("锁定时长应该接近 15 分钟，实际 %v", left)
	}
	// 锁定期过了要自动解锁，不需要人工干预
	if left := gate.check(key, now.Add(16*time.Minute)); left != 0 {
		t.Fatalf("过了锁定期应该自动解锁，实际还要等 %v", left)
	}
}

// 用户名大小写与首尾空格不该绕过计数
func TestLoginGateKeyNormalizesUsername(t *testing.T) {
	if loginGateKey(" Admin ", "1.2.3.4") != loginGateKey("admin", "1.2.3.4") {
		t.Fatal("大小写/空格不同的用户名应该算同一个计数桶，否则换个写法就能绕过")
	}
	if loginGateKey("admin", "1.2.3.4") == loginGateKey("admin", "1.2.3.5") {
		t.Fatal("不同来源 IP 要分开计数，否则一个人输错几次会把别人锁在外面")
	}
}

// 登录成功要清掉计数：前四次输错、第五次对了，不该留着一个「差一次就锁」的状态
func TestLoginGateSuccessClearsCount(t *testing.T) {
	gate := &loginGate{items: map[string]*loginAttempt{}}
	key := loginGateKey("admin", "10.0.0.5")
	now := time.Now()
	for i := 0; i < 4; i++ {
		gate.fail(key, 5, time.Minute, now)
	}
	gate.success(key)
	if fails, locked := gate.fail(key, 5, time.Minute, now); fails != 1 || locked {
		t.Fatalf("成功登录后计数应该归零，实际 fails=%d locked=%v", fails, locked)
	}
}

// 锁上之后计数归零：解锁后再错一次不应该立刻又被锁
func TestLoginGateResetsAfterLock(t *testing.T) {
	gate := &loginGate{items: map[string]*loginAttempt{}}
	key := loginGateKey("admin", "10.0.0.5")
	now := time.Now()
	for i := 0; i < 5; i++ {
		gate.fail(key, 5, time.Minute, now)
	}
	later := now.Add(2 * time.Minute)
	if fails, locked := gate.fail(key, 5, time.Minute, later); fails != 1 || locked {
		t.Fatalf("解锁后第一次失败应该只是第 1 次，实际 fails=%d locked=%v", fails, locked)
	}
}

func TestLockMessageIsActionable(t *testing.T) {
	if msg := lockMessage(30 * time.Second); !strings.Contains(msg, "30 秒") {
		t.Fatalf("不足一分钟要给秒数，实际 %q", msg)
	}
	// 向上取整：还剩 61 秒说「2 分钟」比说「1 分钟」诚实
	if msg := lockMessage(61 * time.Second); !strings.Contains(msg, "2 分钟") {
		t.Fatalf("应该向上取整到 2 分钟，实际 %q", msg)
	}
}
