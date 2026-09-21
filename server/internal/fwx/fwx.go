// Package fwx 把主机上的防火墙状态读成平台能对账的结构，并把平台规则翻译成真机命令。
//
// 设计取舍（和 host_metric 一致的路子）：远端只跑「原样输出」的命令，
// 所有解析都在 Go 里做，这样解析逻辑能脱离 SSH 单测，换发行版也只用改 fixture。
//
// 三种后端按「谁在真正管规则」的优先级识别：firewalld > ufw > 裸 iptables。
// 只读到 iptables 说明这台机器没装规则管理器，改规则不会持久化，
// 这一点必须往上报，不能让用户以为下发完就永久生效了。
package fwx

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Backend 规则管理后端
const (
	BackendFirewalld = "firewalld"
	BackendUFW       = "ufw"
	BackendIptables  = "iptables"
	BackendNone      = "none"
)

// 方向与动作，统一成平台口径（真机各家写法不同，都归一到这几个值）
const (
	DirIn  = "in"
	DirOut = "out"

	ActionAccept = "accept"
	ActionDrop   = "drop"
	ActionReject = "reject"
)

// Rule 一条归一化后的防火墙规则。
//
// Hits/Bytes 只有 iptables 系（含 firewalld 的底层链）能给出真实计数，
// ufw status 不带计数，这时 HasCounter=false —— 界面上要能区分
// 「命中 0 次」和「这台机器压根没有命中计数」。
type Rule struct {
	Direction string `json:"direction"`
	Action    string `json:"action"`
	Protocol  string `json:"protocol"` // tcp | udp | icmp | all
	Source    string `json:"source"`   // CIDR，any 表示不限
	Dest      string `json:"dest"`     // 目的地址，通常是本机
	Port      string `json:"port"`     // 22 | 80,443 | 6000-6010 | 空表示不限端口
	Service   string `json:"service"`  // firewalld 的 service 名（ssh/dhcpv6-client…），与 Port 二选一

	Hits       int64  `json:"hits"`
	Bytes      int64  `json:"bytes"`
	HasCounter bool   `json:"hasCounter"`
	Raw        string `json:"raw"`
	// Index 在该后端里的序号，删除 ufw / iptables 规则时要用
	Index int `json:"index"`
}

// Key 规则指纹：平台期望规则与真机实际规则靠它对账。
// 刻意不含计数、序号、描述这些会变的东西。
func (r Rule) Key() string {
	src := strings.TrimSpace(r.Source)
	if src == "" {
		src = "any"
	}
	target := r.Service
	if target == "" {
		target = normalizePortSpec(r.Port)
		if target == "" {
			target = "any"
		}
	}
	proto := strings.ToLower(strings.TrimSpace(r.Protocol))
	if proto == "" || proto == "0" {
		proto = "all"
	}
	return strings.Join([]string{r.Direction, r.Action, proto, src, target}, "/")
}

// normalizePortSpec 把 "443, 80" 这类写法排序去空格，保证指纹稳定
func normalizePortSpec(port string) string {
	port = strings.TrimSpace(port)
	if port == "" {
		return ""
	}
	parts := strings.Split(port, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}

// State 一次读取的结果
type State struct {
	Backend string `json:"backend"`
	// Active 后端是否在运行（firewalld 装了但没起，规则就是不生效的）
	Active bool `json:"active"`
	// Persistent 改动是否会持久化。裸 iptables 为 false
	Persistent bool   `json:"persistent"`
	Note       string `json:"note"`
	// Installed 各后端的探测结果：absent 没装 | stopped 装了没跑 | running 在跑。
	// 界面需要它来解释「为什么不是用 ufw 管的」
	Installed map[string]string `json:"installed"`
	Rules     []Rule            `json:"rules"`
	// DefaultPolicy 链默认策略，判断「没有显式放行时会不会被拒」要看它
	DefaultPolicy map[string]string `json:"defaultPolicy"`
}


// ReadScript 返回读取命令。防火墙相关命令都要 root，非 root 账号下每条都要提权，
// 所以这里按需在每条特权命令前插 sudo -n，而不是整段包一次。
func ReadScript(useSudo bool) string {
	prefix := ""
	if useSudo {
		prefix = "sudo -n "
	}
	return strings.ReplaceAll(readTemplate, "{{S}}", prefix)
}

// readTemplate 一条命令读完所有需要的信息。
//
// backend 段把三种后端的「装没装、跑没跑」全都报上来，由 Go 侧决定谁才是真正生效的那个。
// 早先这里在 shell 里直接挑一个后端，结果装了 ufw 但没 enable 的机器被判成 ufw，
// 规则读出来是空的、还宣称「下发后自动持久化」—— 而真正在拦包的是 iptables 链。
// 这种「看起来在管、其实不生效」正是最该避免的假象。
//
// 两处 grep 必须锚定，别问我怎么知道的：
//   - "Status: inactive" 里含有 "active"，`grep -i active` 会把停用的 ufw 判成启用
//   - firewall-cmd --state 停止时输出 "not running"，`grep running` 同样会误判
const readTemplate = `
echo '### backend'
if command -v firewall-cmd >/dev/null 2>&1; then
  if {{S}}firewall-cmd --state 2>/dev/null | tr -d '[:space:]' | grep -qx running; then echo 'firewalld running'; else echo 'firewalld stopped'; fi
else
  echo 'firewalld absent'
fi
if command -v ufw >/dev/null 2>&1; then
  if {{S}}ufw status 2>/dev/null | head -n1 | grep -qiE '^status:[[:space:]]*active'; then echo 'ufw running'; else echo 'ufw stopped'; fi
else
  echo 'ufw absent'
fi
if command -v iptables >/dev/null 2>&1; then echo 'iptables running'; else echo 'iptables absent'; fi
echo '### firewalld'
{{S}}firewall-cmd --list-all 2>/dev/null
echo '### ufw'
{{S}}ufw status numbered 2>/dev/null
echo '### iptables-in'
{{S}}iptables -n -v -L INPUT --line-numbers 2>/dev/null
echo '### iptables-out'
{{S}}iptables -n -v -L OUTPUT --line-numbers 2>/dev/null
`




// Parse 解析 ReadScript 的输出
func Parse(out string) State {
	sections := splitSections(out)
	st := State{Backend: BackendNone, DefaultPolicy: map[string]string{}, Installed: map[string]string{}}

	// backend 段每行是「名字 状态」，Go 侧自己决定谁才是生效的那个
	for _, line := range strings.Split(sections["backend"], "\n") {
		f := strings.Fields(line)
		if len(f) == 2 {
			st.Installed[f[0]] = f[1]
		}
	}
	// 兼容早期只报一行的输出
	if len(st.Installed) == 0 {
		if f := strings.Fields(strings.TrimSpace(sections["backend"])); len(f) == 2 {
			st.Installed[f[0]] = f[1]
		}
	}

	// 生效优先级：跑着的 firewalld > 跑着的 ufw > iptables。
	// 装了但没跑的管理器一律不算生效 —— 它的配置文件里有什么都不影响当前包过滤。
	idle := []string{}
	for _, name := range []string{BackendFirewalld, BackendUFW} {
		switch st.Installed[name] {
		case "running":
			if st.Backend == BackendNone {
				st.Backend, st.Active = name, true
			}
		case "stopped":
			idle = append(idle, name)
		}
	}
	if st.Backend == BackendNone && st.Installed[BackendIptables] == "running" {
		st.Backend, st.Active = BackendIptables, true
	}

	// iptables 段无论如何都解析：firewalld / ufw 的规则最终也落在 iptables 链上，
	// 命中计数只能从这里拿
	counters := map[string]Rule{}
	for _, dir := range []struct{ key, dir string }{{"iptables-in", DirIn}, {"iptables-out", DirOut}} {
		rules, policy := parseIptables(sections[dir.key], dir.dir)
		if policy != "" {
			st.DefaultPolicy[dir.dir] = policy
		}
		for _, r := range rules {
			if prev, ok := counters[r.Key()]; ok {
				// 同指纹多条时累加计数，否则「命中」会随机取一条
				prev.Hits += r.Hits
				prev.Bytes += r.Bytes
				counters[r.Key()] = prev
				continue
			}
			counters[r.Key()] = r
		}
	}

	switch st.Backend {
	case BackendFirewalld:
		st.Persistent = true
		st.Rules = parseFirewalld(sections["firewalld"])
		st.Note = "firewalld 管理规则，下发使用 --permanent 并 reload"
	case BackendUFW:
		st.Persistent = true
		st.Rules = parseUFW(sections["ufw"])
		st.Note = "ufw 管理规则，下发后自动持久化"
	case BackendIptables:
		st.Persistent = false
		for _, r := range counters {
			st.Rules = append(st.Rules, r)
		}
		st.Note = "这台机器由裸 iptables 链在拦包，规则重启即失效；要持久化需装 iptables-persistent 或启用 firewalld"
	default:
		st.Note = "未检测到 firewalld / ufw / iptables，无法读取或下发规则"
	}
	// 装了但没启用的管理器必须点名：否则用户会以为规则是它在管
	if len(idle) > 0 {
		st.Note += fmt.Sprintf("。注意：%s 已安装但未启用，它的配置不影响当前包过滤",
			strings.Join(idle, " / "))
	}

	// 把 iptables 的计数贴到 firewalld / ufw 读出来的规则上
	for i := range st.Rules {
		if c, ok := counters[st.Rules[i].Key()]; ok {
			st.Rules[i].Hits = c.Hits
			st.Rules[i].Bytes = c.Bytes
			st.Rules[i].HasCounter = true
		}
	}
	sort.SliceStable(st.Rules, func(i, j int) bool {
		if st.Rules[i].Direction != st.Rules[j].Direction {
			return st.Rules[i].Direction < st.Rules[j].Direction
		}
		return st.Rules[i].Index < st.Rules[j].Index
	})
	return st
}


func splitSections(out string) map[string]string {
	sections := map[string]string{}
	current := ""
	var buf []string
	flush := func() {
		if current != "" {
			sections[current] = strings.Join(buf, "\n")
		}
		buf = nil
	}
	for _, line := range strings.Split(strings.ReplaceAll(out, "\r\n", "\n"), "\n") {
		if strings.HasPrefix(line, "### ") {
			flush()
			current = strings.TrimSpace(strings.TrimPrefix(line, "### "))
			continue
		}
		buf = append(buf, line)
	}
	flush()
	return sections
}

var (
	// 1      123  9876 ACCEPT  tcp  --  *  *  10.0.0.0/8  0.0.0.0/0  tcp dpt:3306
	reIptablesRow = regexp.MustCompile(`^\s*(\d+)\s+(\S+)\s+(\S+)\s+(\S+)\s+(\S+)\s+\S+\s+(\S+)\s+(\S+)\s+(\S+)\s+(\S+)\s*(.*)$`)
	rePolicy      = regexp.MustCompile(`policy\s+(\w+)`)
	reDport       = regexp.MustCompile(`dpts?:([0-9,:]+)`)
	// [ 1] 3306/tcp  ALLOW IN  10.0.0.0/8
	reUFWRow = regexp.MustCompile(`^\[\s*(\d+)\]\s+(.+?)\s{2,}(ALLOW|DENY|REJECT|LIMIT)\s+(IN|OUT)\s*(.*)$`)
	// rule family="ipv4" source address="10.0.0.0/8" port port="3306" protocol="tcp" accept
	reRichSource = regexp.MustCompile(`source\s+address="([^"]+)"`)
	reRichPort   = regexp.MustCompile(`port\s+port="([^"]+)"`)
	reRichProto  = regexp.MustCompile(`protocol="([^"]+)"`)
	reRichAction = regexp.MustCompile(`\s(accept|drop|reject)\s*$`)
)

func parseIptables(text, dir string) ([]Rule, string) {
	var rules []Rule
	policy := ""
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "Chain ") {
			if m := rePolicy.FindStringSubmatch(line); m != nil {
				policy = strings.ToLower(m[1])
			}
			continue
		}
		if strings.Contains(line, "pkts bytes target") {
			continue
		}
		m := reIptablesRow.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		action := iptablesAction(m[4])
		if action == "" {
			// LOG / RETURN / 自定义链跳转不是放行或拦截，不进规则表
			continue
		}
		idx, _ := strconv.Atoi(m[1])
		r := Rule{
			Direction:  dir,
			Action:     action,
			Protocol:   normalizeProto(m[5]),
			Source:     normalizeAddr(m[8]),
			Dest:       normalizeAddr(m[9]),
			Hits:       parseCount(m[2]),
			Bytes:      parseCount(m[3]),
			HasCounter: true,
			Raw:        strings.TrimSpace(line),
			Index:      idx,
		}
		if pm := reDport.FindStringSubmatch(m[10]); pm != nil {
			r.Port = strings.ReplaceAll(pm[1], ":", "-")
		}
		rules = append(rules, r)
	}
	return rules, policy
}

func iptablesAction(target string) string {
	switch strings.ToUpper(target) {
	case "ACCEPT":
		return ActionAccept
	case "DROP":
		return ActionDrop
	case "REJECT":
		return ActionReject
	default:
		return ""
	}
}

func parseUFW(text string) []Rule {
	var rules []Rule
	for _, line := range strings.Split(text, "\n") {
		m := reUFWRow.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		idx, _ := strconv.Atoi(m[1])
		to := strings.TrimSpace(m[2])
		action := ActionAccept
		switch strings.ToUpper(m[3]) {
		case "DENY":
			action = ActionDrop
		case "REJECT":
			action = ActionReject
		}
		dir := DirIn
		if strings.EqualFold(m[4], "OUT") {
			dir = DirOut
		}
		port, proto := splitUFWTarget(to)
		rules = append(rules, Rule{
			Direction: dir,
			Action:    action,
			Protocol:  proto,
			Source:    normalizeAddr(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(m[5]), "(out)"))),
			Port:      port,
			Raw:       strings.TrimSpace(line),
			Index:     idx,
		})
	}
	return rules
}

// splitUFWTarget 把 "3306/tcp" / "80" / "OpenSSH" 拆成端口与协议
func splitUFWTarget(to string) (port, proto string) {
	to = strings.TrimSpace(to)
	if to == "" {
		return "", "all"
	}
	if i := strings.LastIndex(to, "/"); i > 0 {
		p := strings.ToLower(to[i+1:])
		if p == "tcp" || p == "udp" {
			return to[:i], p
		}
	}
	if _, err := strconv.Atoi(strings.SplitN(to, ":", 2)[0]); err == nil {
		return to, "all"
	}
	// 应用配置名（OpenSSH 之类），按 service 处理，端口留空
	return "", "all"
}

func parseFirewalld(text string) []Rule {
	var rules []Rule
	idx := 0
	next := func() int { idx++; return idx }
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "services:"):
			for _, svc := range strings.Fields(strings.TrimPrefix(trimmed, "services:")) {
				rules = append(rules, Rule{
					Direction: DirIn, Action: ActionAccept, Protocol: "all",
					Source: "any", Service: svc, Raw: "services: " + svc, Index: next(),
				})
			}
		case strings.HasPrefix(trimmed, "ports:"):
			for _, p := range strings.Fields(strings.TrimPrefix(trimmed, "ports:")) {
				port, proto := p, "all"
				if i := strings.LastIndex(p, "/"); i > 0 {
					port, proto = p[:i], strings.ToLower(p[i+1:])
				}
				rules = append(rules, Rule{
					Direction: DirIn, Action: ActionAccept, Protocol: proto,
					Source: "any", Port: strings.ReplaceAll(port, ":", "-"),
					Raw: "ports: " + p, Index: next(),
				})
			}
		case strings.HasPrefix(trimmed, "rule "):
			r := Rule{Direction: DirIn, Action: ActionAccept, Protocol: "all", Source: "any", Raw: trimmed, Index: next()}
			if m := reRichSource.FindStringSubmatch(trimmed); m != nil {
				r.Source = normalizeAddr(m[1])
			}
			if m := reRichPort.FindStringSubmatch(trimmed); m != nil {
				r.Port = strings.ReplaceAll(m[1], ":", "-")
			}
			if m := reRichProto.FindStringSubmatch(trimmed); m != nil {
				r.Protocol = normalizeProto(m[1])
			}
			if m := reRichAction.FindStringSubmatch(trimmed); m != nil {
				switch m[1] {
				case "drop":
					r.Action = ActionDrop
				case "reject":
					r.Action = ActionReject
				}
			}
			rules = append(rules, r)
		}
	}
	return rules
}

func normalizeProto(p string) string {
	p = strings.ToLower(strings.TrimSpace(p))
	switch p {
	case "", "all", "0", "--":
		return "all"
	case "icmp", "icmpv6", "ipv6-icmp":
		return "icmp"
	}
	return p
}

func normalizeAddr(a string) string {
	a = strings.TrimSpace(a)
	switch a {
	case "", "0.0.0.0/0", "::/0", "Anywhere", "anywhere", "*":
		return "any"
	}
	return a
}

// parseCount 认 iptables 的 K/M/G 缩写（-v 输出在数值大时会缩写）
func parseCount(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	mult := int64(1)
	switch s[len(s)-1] {
	case 'K':
		mult, s = 1000, s[:len(s)-1]
	case 'M':
		mult, s = 1000_000, s[:len(s)-1]
	case 'G':
		mult, s = 1000_000_000, s[:len(s)-1]
	case 'T':
		mult, s = 1000_000_000_000, s[:len(s)-1]
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return int64(f * float64(mult))
}

// ---------- 命令生成 ----------

// Spec 平台侧想要的一条规则，用来生成命令
type Spec struct {
	Direction string
	Action    string
	Protocol  string
	Source    string
	Port      string
	Service   string
}

// AddCommand 生成新增规则的命令。返回的第二个值是不支持的原因。
func AddCommand(backend string, s Spec) (string, error) {
	if err := s.validate(); err != nil {
		return "", err
	}
	switch backend {
	case BackendFirewalld:
		return firewalldCommand(s, true), nil
	case BackendUFW:
		return ufwCommand(s, true), nil
	case BackendIptables:
		return iptablesCommand(s, true), nil
	default:
		return "", fmt.Errorf("主机上没有可用的防火墙后端，无法下发")
	}
}

// DeleteCommand 生成删除规则的命令
func DeleteCommand(backend string, s Spec) (string, error) {
	if err := s.validate(); err != nil {
		return "", err
	}
	switch backend {
	case BackendFirewalld:
		return firewalldCommand(s, false), nil
	case BackendUFW:
		return ufwCommand(s, false), nil
	case BackendIptables:
		return iptablesCommand(s, false), nil
	default:
		return "", fmt.Errorf("主机上没有可用的防火墙后端，无法下发")
	}
}

var (
	reCIDR = regexp.MustCompile(`^[0-9a-fA-F:.]+(/\d{1,3})?$`)
	rePort = regexp.MustCompile(`^\d{1,5}(-\d{1,5})?$`)
	reSvc  = regexp.MustCompile(`^[a-zA-Z0-9_.-]+$`)
)

// validate 兼做注入防护：这些值最终会拼进 shell 命令，
// 只放行 CIDR / 端口 / 服务名这三种形状，其它一律拒绝。
func (s Spec) validate() error {
	if s.Direction != DirIn && s.Direction != DirOut {
		return fmt.Errorf("方向只能是 in 或 out")
	}
	switch s.Action {
	case ActionAccept, ActionDrop, ActionReject:
	default:
		return fmt.Errorf("动作只能是 accept / drop / reject")
	}
	switch normalizeProto(s.Protocol) {
	case "tcp", "udp", "icmp", "all":
	default:
		return fmt.Errorf("协议只支持 tcp / udp / icmp / all")
	}
	if s.Source != "" && s.Source != "any" && !reCIDR.MatchString(s.Source) {
		return fmt.Errorf("来源必须是 IP 或 CIDR")
	}
	if s.Port != "" && !rePort.MatchString(s.Port) {
		return fmt.Errorf("端口必须是数字或 start-end 区间")
	}
	if s.Service != "" && !reSvc.MatchString(s.Service) {
		return fmt.Errorf("服务名只能是字母数字与 . _ -")
	}
	if s.Port == "" && s.Service == "" && normalizeProto(s.Protocol) != "icmp" {
		return fmt.Errorf("必须指定端口或服务名")
	}
	return nil
}

func firewalldCommand(s Spec, add bool) string {
	verb := "--add-rich-rule"
	if !add {
		verb = "--remove-rich-rule"
	}
	proto := normalizeProto(s.Protocol)
	parts := []string{`rule family="ipv4"`}
	if s.Source != "" && s.Source != "any" {
		parts = append(parts, fmt.Sprintf(`source address="%s"`, s.Source))
	}
	switch {
	case s.Service != "":
		parts = append(parts, fmt.Sprintf(`service name="%s"`, s.Service))
	case proto == "icmp":
		parts = append(parts, `protocol value="icmp"`)
	default:
		if proto == "all" {
			proto = "tcp"
		}
		parts = append(parts, fmt.Sprintf(`port port="%s" protocol="%s"`, strings.ReplaceAll(s.Port, "-", "-"), proto))
	}
	if s.Direction == DirOut {
		// firewalld 的 rich rule 默认管入方向，出方向要显式声明
		parts = append(parts, `direction="out"`)
	}
	parts = append(parts, s.Action)
	rich := strings.Join(parts, " ")
	return fmt.Sprintf("firewall-cmd --permanent %s='%s' && firewall-cmd --reload", verb, rich)
}

func ufwCommand(s Spec, add bool) string {
	proto := normalizeProto(s.Protocol)
	action := "allow"
	switch s.Action {
	case ActionDrop:
		action = "deny"
	case ActionReject:
		action = "reject"
	}
	var b strings.Builder
	b.WriteString("ufw --force ")
	if !add {
		b.WriteString("delete ")
	}
	b.WriteString(action)
	if s.Direction == DirOut {
		b.WriteString(" out")
	} else {
		b.WriteString(" in")
	}
	if s.Source != "" && s.Source != "any" {
		b.WriteString(" from " + s.Source)
	} else {
		b.WriteString(" from any")
	}
	target := s.Port
	if target == "" && s.Service != "" {
		target = s.Service
	}
	if target != "" {
		b.WriteString(" to any port " + strings.ReplaceAll(target, "-", ":"))
	}
	if proto == "tcp" || proto == "udp" {
		b.WriteString(" proto " + proto)
	}
	return b.String()
}

func iptablesCommand(s Spec, add bool) string {
	chain := "INPUT"
	if s.Direction == DirOut {
		chain = "OUTPUT"
	}
	op := "-I"
	if !add {
		op = "-D"
	}
	target := "ACCEPT"
	switch s.Action {
	case ActionDrop:
		target = "DROP"
	case ActionReject:
		target = "REJECT"
	}
	proto := normalizeProto(s.Protocol)
	if proto == "all" {
		proto = "tcp"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "iptables %s %s -p %s", op, chain, proto)
	if s.Source != "" && s.Source != "any" {
		b.WriteString(" -s " + s.Source)
	}
	if s.Port != "" && proto != "icmp" {
		b.WriteString(" --dport " + strings.ReplaceAll(s.Port, "-", ":"))
	}
	b.WriteString(" -j " + target)
	return b.String()
}

// SudoWrap 非 root 账号下给命令加免密 sudo 前缀。
// 防火墙命令全都要 root，拿不到权限时宁可失败并如实报错，也不假装下发成功。
func SudoWrap(command, username string, enabled bool) string {
	if !enabled || username == "root" || strings.TrimSpace(command) == "" {
		return command
	}
	// 每个 && 分段都要各自提权，否则 reload 那一半会以普通用户执行
	segments := strings.Split(command, "&&")
	for i, seg := range segments {
		seg = strings.TrimSpace(seg)
		if seg != "" && !strings.HasPrefix(seg, "sudo ") {
			seg = "sudo -n " + seg
		}
		segments[i] = seg
	}
	return strings.Join(segments, " && ")
}
