package handler

import (
	"testing"

	"ops-platform/server/internal/model"
)

// 真机 `ss -lntpH` 与 `systemctl list-units` 的典型输出，用来验证解析。
// 采集命令在远端已经把它们改写成 U|F|L|C 四种前缀，这里喂的就是改写后的形态。
const sampleServiceRaw = `U|nginx.service|loaded|active|running|A high performance web server
U|sshd.service|loaded|active|running|OpenBSD Secure Shell server
U|mysql.service|loaded|inactive|dead|MySQL Community Server
U|ghost.service|not-found|inactive|dead|ghost.service
F|nginx.service|enabled
F|sshd.service|enabled
F|mysql.service|disabled
F|systemd-tmpfiles-setup.service|static
L|80|1234
L|80|1235
L|443|1235
L|22|900
L|9000|4321
C|1234|nginx.service
C|1235|nginx.service
C|900|sshd.service
`

func TestParseHostServicesMapsPortsViaCgroup(t *testing.T) {
	snap := parseHostServices(sampleServiceRaw)
	if !snap.Systemd {
		t.Fatal("有 U/F 行就说明目标机器有 systemd")
	}

	nginx := snap.Units["nginx.service"]
	if nginx == nil {
		t.Fatal("nginx.service 没解析出来")
	}
	if nginx.ActiveState != "active" || nginx.SubState != "running" {
		t.Fatalf("nginx 状态解析错误: %+v", nginx)
	}
	if nginx.EnableState != "enabled" {
		t.Fatalf("开机自启状态应从 F 行取，实际 %q", nginx.EnableState)
	}
	if nginx.Description != "A high performance web server" {
		t.Fatalf("描述解析错误: %q", nginx.Description)
	}
	// master(1234) 与 worker(1235) 的端口都要归到 nginx 名下，并去重排序
	if len(nginx.Ports) != 2 || nginx.Ports[0] != 80 || nginx.Ports[1] != 443 {
		t.Fatalf("端口应为 [80 443]，实际 %v", nginx.Ports)
	}
	if len(nginx.PIDs) != 2 {
		t.Fatalf("应记下两个持有端口的进程，实际 %v", nginx.PIDs)
	}

	if ports := snap.Units["sshd.service"].Ports; len(ports) != 1 || ports[0] != 22 {
		t.Fatalf("sshd 端口应为 [22]，实际 %v", ports)
	}
	// 9000 的进程 4321 没有 cgroup 映射，不该被硬塞给任何服务
	for name, u := range snap.Units {
		for _, p := range u.Ports {
			if p == 9000 {
				t.Fatalf("对不上 unit 的端口不该被归给 %s", name)
			}
		}
	}
	if snap.Units["mysql.service"].ActiveState != "inactive" {
		t.Fatal("停着的服务也要出现在清单里（list-units --all）")
	}
	if snap.Units["ghost.service"].LoadState != "not-found" {
		t.Fatal("not-found 的 unit 要如实保留状态")
	}
}

func TestParseHostServicesNoSystemd(t *testing.T) {
	snap := parseHostServices("NOSYSTEMD\n")
	if snap.Systemd {
		t.Fatal("没有 systemctl 时必须报 Systemd=false，不能假装采到了空清单")
	}
	if len(snap.Units) != 0 {
		t.Fatalf("不该有任何 unit，实际 %d 个", len(snap.Units))
	}
}

func TestParseHostServicesToleratesMissingPieces(t *testing.T) {
	// 只有 list-units（没有 ss，也没有 list-unit-files）
	snap := parseHostServices("U|nginx.service|loaded|active|running|web\n")
	if !snap.Systemd {
		t.Fatal("缺少其它段落不应让整次解析失败")
	}
	u := snap.Units["nginx.service"]
	if u.EnableState != "" || len(u.Ports) != 0 {
		t.Fatalf("采不到的信息应留空而不是编造: %+v", u)
	}
	// 垃圾行不应导致 panic 或污染结果
	snap2 := parseHostServices("garbage\nU|\nL|abc|def\nC|x\n")
	if len(snap2.Units) > 1 {
		t.Fatalf("垃圾行不该造出 unit: %v", snap2.Units)
	}
}

func TestEvalDriftInactive(t *testing.T) {
	svc := model.HostService{ExpectActive: true, ExpectEnabled: true}
	drift, detail := evalDrift(svc, &unitState{
		LoadState: "loaded", ActiveState: "failed", SubState: "failed", EnableState: "enabled",
	})
	if drift != "inactive" {
		t.Fatalf("期望在跑但实际 failed 应判 inactive，实际 %q", drift)
	}
	if detail == "" {
		t.Fatal("漂移必须带上「实际是什么」，否则人不知道发生了什么")
	}
}

func TestEvalDriftUnexpectedActive(t *testing.T) {
	// 登记为「这个服务就该停着」，它在跑反而是漂移
	svc := model.HostService{ExpectActive: false, ExpectEnabled: false}
	drift, _ := evalDrift(svc, &unitState{
		LoadState: "loaded", ActiveState: "active", SubState: "running", EnableState: "disabled",
	})
	if drift != "unexpected" {
		t.Fatalf("期望停着却在跑应判 unexpected，实际 %q", drift)
	}
}

func TestEvalDriftStaticUnitIsNotDisabled(t *testing.T) {
	// static / indirect 这类 unit 本来就不能 enable，不该被判成「该自启没自启」
	svc := model.HostService{ExpectActive: true, ExpectEnabled: true}
	for _, state := range []string{"static", "indirect", "generated", "enabled-runtime", "transient", "alias"} {
		drift, _ := evalDrift(svc, &unitState{
			LoadState: "loaded", ActiveState: "active", SubState: "running", EnableState: state,
		})
		if drift != "ok" {
			t.Fatalf("EnableState=%s 不该算漂移，实际 %q", state, drift)
		}
	}
	drift, detail := evalDrift(svc, &unitState{
		LoadState: "loaded", ActiveState: "active", SubState: "running", EnableState: "disabled",
	})
	if drift != "disabled" {
		t.Fatalf("真的 disabled 才算漂移，实际 %q", drift)
	}
	if detail == "" {
		t.Fatal("要说明实际的自启状态")
	}
}

func TestEvalDriftMissing(t *testing.T) {
	svc := model.HostService{ExpectActive: true, ExpectEnabled: true}
	if drift, _ := evalDrift(svc, nil); drift != "missing" {
		t.Fatalf("机器上没有这个 unit 应判 missing，实际 %q", drift)
	}
	if drift, _ := evalDrift(svc, &unitState{LoadState: "not-found"}); drift != "missing" {
		t.Fatal("not-found 也算 missing")
	}
	if drift, _ := evalDrift(svc, &unitState{LoadState: "masked"}); drift != "missing" {
		t.Fatal("masked 的 unit systemd 不会加载，等同不存在")
	}
}

func TestEvalDriftEmptyEnableStateIsNotDrift(t *testing.T) {
	// 采集时 list-unit-files 那一段失败会让 EnableState 为空。
	// 空值意味着「没采到」，不能当成 disabled 来报警。
	svc := model.HostService{ExpectActive: true, ExpectEnabled: true}
	drift, _ := evalDrift(svc, &unitState{
		LoadState: "loaded", ActiveState: "active", SubState: "running", EnableState: "",
	})
	if drift != "ok" {
		t.Fatalf("自启状态没采到时不该判漂移，实际 %q", drift)
	}
}

func TestStateTextIsReadable(t *testing.T) {
	if got := stateText(nil); got != "not-found" {
		t.Fatalf("unit 不存在时应写 not-found，实际 %q", got)
	}
	got := stateText(&unitState{ActiveState: "active", SubState: "running", EnableState: "enabled"})
	if got != "active/running,enabled" {
		t.Fatalf("状态文本格式不对: %q", got)
	}
}

func TestServiceActionsRiskyClassification(t *testing.T) {
	// stop / restart / disable 会让服务不可用或开机不再拉起，必须算高危
	for _, action := range []string{"stop", "restart", "disable"} {
		if !serviceActions[action] {
			t.Fatalf("%s 应被标为高危动作（关键服务上要二次确认）", action)
		}
	}
	for _, action := range []string{"start", "reload", "enable"} {
		if serviceActions[action] {
			t.Fatalf("%s 不该要求二次确认", action)
		}
	}
	if _, ok := serviceActions["mask"]; ok {
		t.Fatal("没实现的动作不该出现在白名单里")
	}
}
