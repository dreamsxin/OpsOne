package bastion

import (
	"regexp"
	"testing"
)

func newTestAuditor() *Auditor {
	return NewAuditor([]Rule{
		{ID: 1, Pattern: regexp.MustCompile(`^\s*(shutdown|reboot)\b`), Action: "block", Description: "关机或重启"},
		{ID: 2, Pattern: regexp.MustCompile(`\biptables\s+-F\b`), Action: "warn", Description: "清空防火墙规则"},
	})
}

// 逐字符输入普通命令：原样转发，回车时产出一条 normal 判定
func TestFeedNormalCommand(t *testing.T) {
	a := newTestAuditor()
	var forwarded string
	for _, ch := range []string{"l", "s", " ", "-", "l", "\r"} {
		out, decisions := a.Feed([]byte(ch))
		forwarded += string(out)
		if ch != "\r" && len(decisions) > 0 {
			t.Fatalf("回车前不应产生判定，输入 %q 得到 %+v", ch, decisions)
		}
		if ch == "\r" {
			if len(decisions) != 1 {
				t.Fatalf("期望 1 条判定，实际 %d", len(decisions))
			}
			if decisions[0].Command != "ls -l" || decisions[0].Risk != RiskNormal {
				t.Fatalf("判定不符: %+v", decisions[0])
			}
		}
	}
	if forwarded != "ls -l\r" {
		t.Fatalf("转发内容不符: %q", forwarded)
	}
}

// 命中 block 规则：不转发回车，改发 Ctrl-U 清行
func TestFeedBlockedCommand(t *testing.T) {
	a := newTestAuditor()
	out, decisions := a.Feed([]byte("shutdown -h now\r"))

	if len(decisions) != 1 || decisions[0].Risk != RiskBlocked {
		t.Fatalf("期望拦截判定，实际 %+v", decisions)
	}
	if decisions[0].RuleID != 1 {
		t.Fatalf("期望命中规则 1，实际 %d", decisions[0].RuleID)
	}
	if len(out) == 0 || out[len(out)-1] != ctrlU {
		t.Fatalf("拦截时应以 Ctrl-U 结尾，实际 %q", out)
	}
	for _, b := range out {
		if b == '\r' {
			t.Fatal("拦截时不应转发回车")
		}
	}
}

// 命中 warn 规则：照常执行，只标记风险
func TestFeedWarnCommand(t *testing.T) {
	a := newTestAuditor()
	out, decisions := a.Feed([]byte("iptables -F\r"))

	if len(decisions) != 1 || decisions[0].Risk != RiskWarn {
		t.Fatalf("期望告警判定，实际 %+v", decisions)
	}
	if out[len(out)-1] != '\r' {
		t.Fatalf("告警规则应照常转发回车，实际 %q", out)
	}
}

// 退格要同步修正命令缓冲，否则审计到的命令会和实际执行的不一致
func TestFeedBackspace(t *testing.T) {
	a := newTestAuditor()
	a.Feed([]byte("shutdownX"))
	a.Feed([]byte{backspace})
	_, decisions := a.Feed([]byte("\r"))

	if len(decisions) != 1 || decisions[0].Command != "shutdown" {
		t.Fatalf("退格后命令应为 shutdown，实际 %+v", decisions)
	}
	if decisions[0].Risk != RiskBlocked {
		t.Fatalf("退格还原后的命令应被拦截，实际 %s", decisions[0].Risk)
	}
}

// 方向键等 CSI 序列不能混进命令缓冲
func TestFeedIgnoresEscapeSequence(t *testing.T) {
	a := newTestAuditor()
	a.Feed([]byte("ls"))
	a.Feed([]byte{escape, '[', 'A'})
	_, decisions := a.Feed([]byte("\r"))

	if len(decisions) != 1 || decisions[0].Command != "ls" {
		t.Fatalf("控制序列不应进入命令，实际 %+v", decisions)
	}
}

// Ctrl-C 放弃当前行
func TestFeedCtrlCResetsBuffer(t *testing.T) {
	a := newTestAuditor()
	a.Feed([]byte("reboot"))
	a.Feed([]byte{ctrlC})
	_, decisions := a.Feed([]byte("\r"))

	if len(decisions) != 0 {
		t.Fatalf("Ctrl-C 后回车不应产生判定，实际 %+v", decisions)
	}
}
