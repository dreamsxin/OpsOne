package fwx

import (
	"strings"
	"testing"
)

// 真机输出样本：CentOS 7 firewalld + 底层 iptables 计数
const sampleFirewalld = `### backend
firewalld running
### firewalld
public (active)
  target: default
  icmp-block-inversion: no
  interfaces: eth0
  sources:
  services: dhcpv6-client ssh
  ports: 8080/tcp 9090-9095/udp
  protocols:
  forward: no
  masquerade: no
  forward-ports:
  source-ports:
  icmp-blocks:
  rich rules:
	rule family="ipv4" source address="10.0.1.0/24" port port="3306" protocol="tcp" accept
	rule family="ipv4" source address="1.2.3.4" port port="22" protocol="tcp" drop
### ufw
### iptables-in
Chain INPUT (policy DROP 12 packets, 800 bytes)
num   pkts bytes target     prot opt in     out     source               destination
1     1024 65536 ACCEPT     tcp  --  *      *       10.0.1.0/24          0.0.0.0/0            tcp dpt:3306
2        0     0 ACCEPT     tcp  --  *      *       0.0.0.0/0            0.0.0.0/0            tcp dpt:8080
3       12   800 DROP       tcp  --  *      *       1.2.3.4              0.0.0.0/0            tcp dpt:22
4      2K   1M  LOG        all  --  *      *       0.0.0.0/0            0.0.0.0/0            LOG flags 0 level 4
### iptables-out
Chain OUTPUT (policy ACCEPT 0 packets, 0 bytes)
num   pkts bytes target     prot opt in     out     source               destination
`

const sampleUFW = `### backend
ufw running
### firewalld
### ufw
Status: active

     To                         Action      From
     --                         ------      ----
[ 1] 22/tcp                     ALLOW IN    Anywhere
[ 2] 3306/tcp                   ALLOW IN    10.0.1.0/24
[ 3] 8080                       DENY IN     Anywhere
[ 4] 53/udp                     ALLOW OUT   Anywhere (out)
### iptables-in
### iptables-out
`

const sampleBare = `### backend
iptables running
### firewalld
### ufw
### iptables-in
Chain INPUT (policy ACCEPT 0 packets, 0 bytes)
num   pkts bytes target     prot opt in     out     source               destination
1      500  40000 ACCEPT    tcp  --  *      *       0.0.0.0/0            0.0.0.0/0            tcp dpt:80
### iptables-out
`

func findRule(t *testing.T, st State, key string) Rule {
	t.Helper()
	for _, r := range st.Rules {
		if r.Key() == key {
			return r
		}
	}
	t.Fatalf("没找到规则 %s，实际有：%v", key, keys(st))
	return Rule{}
}

func keys(st State) []string {
	out := make([]string, 0, len(st.Rules))
	for _, r := range st.Rules {
		out = append(out, r.Key())
	}
	return out
}

func TestParseFirewalld(t *testing.T) {
	st := Parse(sampleFirewalld)
	if st.Backend != BackendFirewalld || !st.Active || !st.Persistent {
		t.Fatalf("后端识别错了: %+v", st)
	}
	if st.DefaultPolicy["in"] != "drop" {
		t.Errorf("默认策略应为 drop，实际 %q", st.DefaultPolicy["in"])
	}
	// services 两条 + ports 两条 + rich rules 两条
	if len(st.Rules) != 6 {
		t.Fatalf("规则条数应为 6，实际 %d：%v", len(st.Rules), keys(st))
	}

	mysql := findRule(t, st, "in/accept/tcp/10.0.1.0/24/3306")
	if mysql.Hits != 1024 || mysql.Bytes != 65536 || !mysql.HasCounter {
		t.Errorf("rich rule 没贴上 iptables 计数: %+v", mysql)
	}
	blocked := findRule(t, st, "in/drop/tcp/1.2.3.4/22")
	if blocked.Hits != 12 {
		t.Errorf("drop 规则计数不对: %+v", blocked)
	}
	// ports: 8080/tcp 命中 0 次，但确实有计数器
	port8080 := findRule(t, st, "in/accept/tcp/any/8080")
	if !port8080.HasCounter || port8080.Hits != 0 {
		t.Errorf("8080 应该有计数器且命中 0：%+v", port8080)
	}
	// service 规则在 iptables 里对不上指纹，应该标成没有计数器，
	// 不能让界面把「读不到」显示成「命中 0」
	ssh := findRule(t, st, "in/accept/all/any/ssh")
	if ssh.HasCounter {
		t.Errorf("service 规则不该有计数器：%+v", ssh)
	}
	// LOG 这类非放行/拦截的目标不进规则表
	for _, r := range st.Rules {
		if strings.Contains(r.Raw, "LOG") {
			t.Errorf("LOG 规则不该进表：%+v", r)
		}
	}
	// 端口区间里的冒号要归一成横线
	if _, err := AddCommand(BackendFirewalld, Spec{Direction: DirIn, Action: ActionAccept, Protocol: "udp", Port: "9090-9095"}); err != nil {
		t.Errorf("区间端口应该能生成命令: %v", err)
	}
}

func TestParseUFW(t *testing.T) {
	st := Parse(sampleUFW)
	if st.Backend != BackendUFW || !st.Active {
		t.Fatalf("后端识别错了: %+v", st)
	}
	if len(st.Rules) != 4 {
		t.Fatalf("规则条数应为 4，实际 %d：%v", len(st.Rules), keys(st))
	}
	findRule(t, st, "in/accept/tcp/any/22")
	findRule(t, st, "in/accept/tcp/10.0.1.0/24/3306")
	findRule(t, st, "in/drop/all/any/8080")
	out := findRule(t, st, "out/accept/udp/any/53")
	if out.Direction != DirOut {
		t.Errorf("ALLOW OUT 应该是出方向: %+v", out)
	}
	// ufw status 不给计数，必须如实标出来
	for _, r := range st.Rules {
		if r.HasCounter {
			t.Errorf("ufw 规则不该有计数器：%+v", r)
		}
	}
}

func TestParseBareIptables(t *testing.T) {
	st := Parse(sampleBare)
	if st.Backend != BackendIptables {
		t.Fatalf("后端识别错了: %+v", st)
	}
	if st.Persistent {
		t.Error("裸 iptables 必须标成不持久化")
	}
	if !strings.Contains(st.Note, "重启即失效") {
		t.Errorf("提示里要说明不持久化，实际 %q", st.Note)
	}
	r := findRule(t, st, "in/accept/tcp/any/80")
	if r.Hits != 500 {
		t.Errorf("计数不对: %+v", r)
	}
}

func TestParseNoBackend(t *testing.T) {
	st := Parse("### backend\nnone stopped\n### firewalld\n### ufw\n### iptables-in\n### iptables-out\n")
	if st.Backend != BackendNone || len(st.Rules) != 0 {
		t.Fatalf("空环境应该没有规则: %+v", st)
	}
	if _, err := AddCommand(st.Backend, Spec{Direction: DirIn, Action: ActionAccept, Protocol: "tcp", Port: "22"}); err == nil {
		t.Error("没有后端时必须拒绝生成命令")
	}
}

// 真机上撞到的情况：ufw 装了但没 enable，真正在拦包的是 iptables（k3s 写的规则）。
// 早先的实现会把它判成 ufw，规则读出来是空的、还宣称「下发后自动持久化」。
const sampleUFWInstalledButIdle = `### backend
firewalld absent
ufw stopped
iptables running
### firewalld
### ufw
Status: inactive
### iptables-in
Chain INPUT (policy ACCEPT 0 packets, 0 bytes)
num   pkts bytes target     prot opt in     out     source               destination
1      210 16000 ACCEPT     tcp  --  *      *       0.0.0.0/0            0.0.0.0/0            tcp dpt:6443
2        0     0 DROP       udp  --  *      *       192.168.0.0/16       0.0.0.0/0            udp dpt:53
### iptables-out
Chain OUTPUT (policy ACCEPT 0 packets, 0 bytes)
num   pkts bytes target     prot opt in     out     source               destination
`

func TestParseUFWInstalledButNotEnabled(t *testing.T) {
	st := Parse(sampleUFWInstalledButIdle)
	if st.Backend != BackendIptables {
		t.Fatalf("ufw 没启用时生效的是 iptables，实际判成 %q", st.Backend)
	}
	if st.Persistent {
		t.Error("裸 iptables 必须标成不持久化")
	}
	if !strings.Contains(st.Note, "ufw 已安装但未启用") {
		t.Errorf("必须点名「装了但没启用」，实际提示：%q", st.Note)
	}
	if st.Installed["ufw"] != "stopped" || st.Installed["iptables"] != "running" {
		t.Errorf("探测结果没如实带出来: %+v", st.Installed)
	}
	if len(st.Rules) != 2 {
		t.Fatalf("应该读到 iptables 的 2 条规则，实际 %d：%v", len(st.Rules), keys(st))
	}
	r := findRule(t, st, "in/accept/tcp/any/6443")
	if r.Hits != 210 || !r.HasCounter {
		t.Errorf("计数没带上: %+v", r)
	}
	// 下发要用 iptables 的命令，不能用 ufw 的
	cmd, err := AddCommand(st.Backend, Spec{Direction: DirIn, Action: ActionAccept, Protocol: "tcp", Port: "9999"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(cmd, "iptables ") {
		t.Errorf("命令应该走 iptables，实际 %q", cmd)
	}
}

func TestParseFirewalldStoppedFallsBack(t *testing.T) {
	st := Parse(`### backend
firewalld stopped
ufw absent
iptables running
### firewalld
### ufw
### iptables-in
Chain INPUT (policy DROP 0 packets, 0 bytes)
num   pkts bytes target     prot opt in     out     source               destination
1        5   300 ACCEPT     tcp  --  *      *       0.0.0.0/0            0.0.0.0/0            tcp dpt:22
### iptables-out
`)
	if st.Backend != BackendIptables {
		t.Fatalf("firewalld 没跑时应回落到 iptables，实际 %q", st.Backend)
	}
	if !strings.Contains(st.Note, "firewalld 已安装但未启用") {
		t.Errorf("提示里要点名 firewalld：%q", st.Note)
	}
	if st.DefaultPolicy["in"] != "drop" {
		t.Errorf("默认策略应为 drop，实际 %q", st.DefaultPolicy["in"])
	}
}

func TestParseCountAbbrev(t *testing.T) {
	cases := map[string]int64{"0": 0, "500": 500, "2K": 2000, "1M": 1_000_000, "1.5G": 1_500_000_000, "": 0, "abc": 0}
	for in, want := range cases {
		if got := parseCount(in); got != want {
			t.Errorf("parseCount(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestAddCommandPerBackend(t *testing.T) {
	spec := Spec{Direction: DirIn, Action: ActionAccept, Protocol: "tcp", Source: "10.0.1.0/24", Port: "3306"}

	fw, err := AddCommand(BackendFirewalld, spec)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"--permanent", "--add-rich-rule", `source address="10.0.1.0/24"`, `port port="3306"`, "firewall-cmd --reload"} {
		if !strings.Contains(fw, want) {
			t.Errorf("firewalld 命令缺 %q: %s", want, fw)
		}
	}

	uf, err := AddCommand(BackendUFW, spec)
	if err != nil {
		t.Fatal(err)
	}
	if uf != "ufw --force allow in from 10.0.1.0/24 to any port 3306 proto tcp" {
		t.Errorf("ufw 命令不对: %s", uf)
	}

	ipt, err := AddCommand(BackendIptables, spec)
	if err != nil {
		t.Fatal(err)
	}
	if ipt != "iptables -I INPUT -p tcp -s 10.0.1.0/24 --dport 3306 -j ACCEPT" {
		t.Errorf("iptables 命令不对: %s", ipt)
	}

	del, err := DeleteCommand(BackendIptables, spec)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(del, "iptables -D INPUT") {
		t.Errorf("删除命令应该用 -D: %s", del)
	}
}

// 这些值最终会拼进 shell，必须在生成命令前就拦掉
func TestSpecRejectsInjection(t *testing.T) {
	bad := []Spec{
		{Direction: DirIn, Action: ActionAccept, Protocol: "tcp", Port: "22", Source: "10.0.0.1; rm -rf /"},
		{Direction: DirIn, Action: ActionAccept, Protocol: "tcp", Port: "22 && reboot"},
		{Direction: DirIn, Action: ActionAccept, Protocol: "tcp", Service: "ssh`id`"},
		{Direction: DirIn, Action: ActionAccept, Protocol: "tcp$(id)", Port: "22"},
		{Direction: "sideways", Action: ActionAccept, Protocol: "tcp", Port: "22"},
		{Direction: DirIn, Action: "nuke", Protocol: "tcp", Port: "22"},
		{Direction: DirIn, Action: ActionAccept, Protocol: "tcp"}, // 既没端口也没服务
	}
	for _, s := range bad {
		if cmd, err := AddCommand(BackendIptables, s); err == nil {
			t.Errorf("应该被拒绝的入参却生成了命令: %+v -> %s", s, cmd)
		}
	}
}

func TestSudoWrap(t *testing.T) {
	cmd := "firewall-cmd --permanent --add-rich-rule='x' && firewall-cmd --reload"
	got := SudoWrap(cmd, "ops", true)
	want := "sudo -n firewall-cmd --permanent --add-rich-rule='x' && sudo -n firewall-cmd --reload"
	if got != want {
		t.Errorf("每段都要提权\n got: %s\nwant: %s", got, want)
	}
	if SudoWrap(cmd, "root", true) != cmd {
		t.Error("root 账号不该加 sudo")
	}
	if SudoWrap(cmd, "ops", false) != cmd {
		t.Error("关掉开关后不该加 sudo")
	}
	if got := SudoWrap("sudo -n ufw status", "ops", true); got != "sudo -n ufw status" {
		t.Errorf("已经带 sudo 的不该再包一层: %s", got)
	}
}
