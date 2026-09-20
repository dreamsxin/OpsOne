package bastion

import (
	"regexp"
	"strings"
)

// 风险等级
const (
	RiskNormal  = "normal"
	RiskWarn    = "warn"
	RiskBlocked = "blocked"
)

// Rule 已编译的命令规则
type Rule struct {
	ID          uint
	Pattern     *regexp.Regexp
	Action      string // block | warn
	Description string
}

// Decision 一条命令的审计结论
type Decision struct {
	Command  string
	Risk     string
	RuleID   uint
	RuleDesc string
}

// 终端控制字符
const (
	ctrlC     = 0x03
	ctrlD     = 0x04
	ctrlU     = 0x15
	backspace = 0x7f
	escape    = 0x1b
)

// 转义序列解析状态
const (
	escNone  = 0
	escSeen  = 1 // 刚读到 ESC，等待判断是两字符序列还是 CSI
	escInCSI = 2 // 处于 CSI/SS3 序列中，等待 @-~ 区间的终止字符
)

// Auditor 从终端输入流中还原命令行并按规则判定。
//
// 这是 PTY 层面的启发式审计：无法感知 Tab 补全的结果，也拦不住 vim 的 :!cmd、
// base64 解码执行等绕过方式。定位是记录与阻断常见误操作，不是强制访问控制。
type Auditor struct {
	rules    []Rule
	buf      []rune
	escState int
}

func NewAuditor(rules []Rule) *Auditor {
	return &Auditor{rules: rules}
}

// Feed 处理一段来自浏览器的输入。
// 返回应当真正转发给 SSH 的字节，以及本段输入产生的命令判定。
// 命中阻断规则时，返回的字节以 Ctrl-U 结尾用于清空当前行，且丢弃本段剩余输入。
func (a *Auditor) Feed(input []byte) (forward []byte, decisions []Decision) {
	forward = make([]byte, 0, len(input)+1)

	for _, r := range string(input) {
		switch a.escState {
		case escSeen:
			forward = appendRune(forward, r)
			// ESC [ 是 CSI，ESC O 是 SS3，其余按两字符序列处理
			if r == '[' || r == 'O' {
				a.escState = escInCSI
			} else {
				a.escState = escNone
			}
			continue
		case escInCSI:
			forward = appendRune(forward, r)
			if r >= '@' && r <= '~' {
				a.escState = escNone
			}
			continue
		}

		switch r {
		case '\r', '\n':
			cmd := strings.TrimSpace(string(a.buf))
			a.buf = a.buf[:0]
			if cmd == "" {
				forward = appendRune(forward, r)
				continue
			}

			decision := a.match(cmd)
			decisions = append(decisions, decision)
			if decision.Risk == RiskBlocked {
				forward = append(forward, ctrlU)
				return forward, decisions
			}
			forward = appendRune(forward, r)

		case backspace, '\b':
			if len(a.buf) > 0 {
				a.buf = a.buf[:len(a.buf)-1]
			}
			forward = appendRune(forward, r)

		case ctrlC, ctrlD, ctrlU:
			a.buf = a.buf[:0]
			forward = appendRune(forward, r)

		case escape:
			a.escState = escSeen
			forward = appendRune(forward, r)

		case '\t':
			// 补全结果只存在于远端，本地缓冲无法跟踪，跳过不记录
			forward = appendRune(forward, r)

		default:
			if r >= 0x20 {
				a.buf = append(a.buf, r)
			}
			forward = appendRune(forward, r)
		}
	}
	return forward, decisions
}

func (a *Auditor) match(cmd string) Decision {
	for _, rule := range a.rules {
		if rule.Pattern.MatchString(cmd) {
			risk := RiskWarn
			if rule.Action == "block" {
				risk = RiskBlocked
			}
			return Decision{Command: cmd, Risk: risk, RuleID: rule.ID, RuleDesc: rule.Description}
		}
	}
	return Decision{Command: cmd, Risk: RiskNormal}
}

func appendRune(dst []byte, r rune) []byte {
	return append(dst, string(r)...)
}
