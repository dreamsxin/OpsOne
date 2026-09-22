package handler

import (
	"strings"
	"testing"
)

func TestShellQuoteEscapesSingleQuote(t *testing.T) {
	// 路径来自用户输入并且要拼进 shell 命令，单引号必须转义，否则可以注入命令
	cases := map[string]string{
		"/etc/nginx/nginx.conf": `'/etc/nginx/nginx.conf'`,
		"/tmp/a b/c.conf":       `'/tmp/a b/c.conf'`,
		`/tmp/it's.conf`:        `'/tmp/it'\''s.conf'`,
		`/tmp/x';rm -rf /;'`:    `'/tmp/x'\'';rm -rf /;'\'''`,
	}
	for in, want := range cases {
		if got := shellQuote(in); got != want {
			t.Fatalf("shellQuote(%q) = %q，期望 %q", in, got, want)
		}
	}
}

func TestShellQuoteBlocksInjection(t *testing.T) {
	// 构造一个想跳出引号执行别的命令的路径。转义正确的判据是结构性的：
	// 把所有 '\'' （转义后的字面单引号）拿掉之后，剩下的单引号必须正好是首尾那一对 ——
	// 说明中间没有任何地方真的脱离了引号保护。
	for _, evil := range []string{
		`/etc/x'; touch /tmp/pwned; echo '`,
		`/etc/x' && rm -rf / #`,
		`/etc/'`,
		`'''`,
	} {
		quoted := shellQuote(evil)
		if !strings.HasPrefix(quoted, "'") || !strings.HasSuffix(quoted, "'") {
			t.Fatalf("必须整体被单引号包住: %s", quoted)
		}
		stripped := strings.ReplaceAll(quoted, `'\''`, "")
		if n := strings.Count(stripped, "'"); n != 2 {
			t.Fatalf("剩余单引号应只有首尾一对，实际 %d 个：%s（原始 %q）", n, stripped, evil)
		}
	}
}

func TestDiffLineCount(t *testing.T) {
	if n := diffLineCount("a\nb\nc", "a\nb\nc"); n != 0 {
		t.Fatalf("完全相同应为 0，实际 %d", n)
	}
	// 改一行 = 删一行 + 加一行
	if n := diffLineCount("a\nb\nc", "a\nB\nc"); n != 2 {
		t.Fatalf("改一行应为 2，实际 %d", n)
	}
	if n := diffLineCount("a\nb", "a\nb\nc"); n != 1 {
		t.Fatalf("多一行应为 1，实际 %d", n)
	}
	// 只换顺序不算差异：这是刻意的，配置里行顺序变了但内容没变，
	// 行集合差给 0，真要看顺序去看 diff 文本
	if n := diffLineCount("a\nb", "b\na"); n != 0 {
		t.Fatalf("只换顺序按行集合差应为 0，实际 %d", n)
	}
}

func TestUnifiedDiffMarksBothSides(t *testing.T) {
	out := unifiedDiff("listen 80;\nroot /var/www;\n", "listen 8080;\nroot /var/www;\n", "基线 v1", "真机现状")
	if !strings.Contains(out, "--- 基线 v1") || !strings.Contains(out, "+++ 真机现状") {
		t.Fatalf("缺少文件头:\n%s", out)
	}
	if !strings.Contains(out, "-   1 listen 80;") {
		t.Fatalf("缺少删除行:\n%s", out)
	}
	if !strings.Contains(out, "+   1 listen 8080;") {
		t.Fatalf("缺少新增行:\n%s", out)
	}
	if !strings.Contains(out, " root /var/www;") {
		t.Fatalf("相同行应作为上下文保留:\n%s", out)
	}
}

func TestUnifiedDiffFoldsLongIdenticalRuns(t *testing.T) {
	same := strings.Repeat("same line\n", 30)
	out := unifiedDiff(same+"old\n", same+"new\n", "a", "b")
	if !strings.Contains(out, "...") {
		t.Fatalf("长段相同内容应被折叠:\n%s", out[:200])
	}
	if strings.Count(out, "same line") > 6 {
		t.Fatalf("折叠后不该还有几十行相同内容:\n%s", out[:300])
	}
	if !strings.Contains(out, "-") || !strings.Contains(out, "+") {
		t.Fatal("折叠不能把真正的差异也吃掉")
	}
}

func TestUnifiedDiffHandlesEmptySides(t *testing.T) {
	// 文件原本不存在（空）→ 第一次铺配置
	out := unifiedDiff("", "listen 80;\n", "空", "新内容")
	if !strings.Contains(out, "+") {
		t.Fatalf("从空到有内容应全是新增:\n%s", out)
	}
	out2 := unifiedDiff("listen 80;\n", "", "旧内容", "空")
	if !strings.Contains(out2, "-") {
		t.Fatalf("从有内容到空应全是删除:\n%s", out2)
	}
}

func TestSha256HexStable(t *testing.T) {
	// 空内容的 sha256 是固定值，用它验证没有把 hash 算错（比如漏了内容）
	if got := sha256Hex(nil); got != "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
		t.Fatalf("空内容 hash 不对: %s", got)
	}
	a, b := sha256Hex([]byte("listen 80;\n")), sha256Hex([]byte("listen 80;"))
	if a == b {
		t.Fatal("结尾换行必须影响 hash，否则「只差一个换行」的漂移会被漏掉")
	}
}

func TestShort12(t *testing.T) {
	if got := short12("abcdef"); got != "abcdef" {
		t.Fatalf("短于 12 位应原样返回，实际 %q", got)
	}
	if got := short12(strings.Repeat("a", 64)); len(got) != 12 {
		t.Fatalf("应截到 12 位，实际 %d 位", len(got))
	}
}

func TestConfigFileReqNormalize(t *testing.T) {
	h := newRunbookTestHandler(t) // 只用到 DB 查负责人，复用现成的测试库

	bad := []configFileReq{
		{Path: "etc/nginx.conf"}, // 相对路径
		{Path: "/"},              // 根目录
		{Path: "/etc/a.conf", ReloadAction: "bounce"}, // 非法动作
	}
	for _, req := range bad {
		r := req
		if _, err := r.normalize(h); err == nil {
			t.Fatalf("应被拒: %+v", req)
		}
	}
	// 「/etc/nginx/」这类路径 path.Clean 之后就是一个看起来合法的路径，
	// 光看字符串分不出是目录还是文件 —— 这一层不猜，抓取时读到目录会如实报错
	dir := configFileReq{Path: "/etc/nginx/"}
	if cleaned, err := dir.normalize(h); err != nil || cleaned != "/etc/nginx" {
		t.Fatalf("目录形态的路径应规范化为 %q 并放过，实际 %q err=%v", "/etc/nginx", cleaned, err)
	}

	ok := configFileReq{Path: "/etc//nginx/../nginx/nginx.conf", ReloadUnit: "nginx"}
	cleaned, err := ok.normalize(h)
	if err != nil {
		t.Fatalf("合法输入被拒: %v", err)
	}
	if cleaned != "/etc/nginx/nginx.conf" {
		t.Fatalf("路径应被规范化，实际 %q", cleaned)
	}
	// unit 不带后缀是最常见的写漏，自动补 .service
	if ok.ReloadUnit != "nginx.service" {
		t.Fatalf("unit 应补全后缀，实际 %q", ok.ReloadUnit)
	}
	if ok.ReloadAction != "reload" {
		t.Fatalf("reload 动作应默认 reload，实际 %q", ok.ReloadAction)
	}
}

func TestConfigDriftLabelsCoverAllStates(t *testing.T) {
	// 判定状态必须都有中文说明，否则页面会显示英文枚举
	for _, state := range []string{"ok", "drift", "missing", "no-desired",
		"too-large", "binary", "error", "unknown"} {
		if configDriftLabels[state] == "" {
			t.Fatalf("状态 %s 没有中文标签", state)
		}
	}
}
