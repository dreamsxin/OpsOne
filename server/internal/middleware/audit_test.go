package middleware

// 审计脱敏与访问日志脱敏的测试。
//
// 这两处的共同点：**记错了比不记更糟**。审计里抄进一份明文口令，等于又造了一处
// 密钥存储，而审计表的查询权限比密码库宽；访问日志里留下一个有效 JWT，
// 等于把登录态写进了一个按天轮转、保留很久的文件。

import (
	"strings"
	"testing"
)

func TestRedactBodyHidesSecretsKeepsKeys(t *testing.T) {
	body := `{"username":"admin","password":"hunter2","nested":{"accessKeySecret":"ak","port":22},
		"list":[{"privateKey":"-----BEGIN"},{"name":"ok"}],"empty":"","nullish":null}`
	out := redactBody("application/json", []byte(body))

	for _, leaked := range []string{"hunter2", "ak", "-----BEGIN"} {
		if strings.Contains(out, leaked) {
			t.Fatalf("秘密泄漏到审计里了（%q）：%s", leaked, out)
		}
	}
	// 键名必须保留：「这次改了 password」本身是有用的信息
	for _, keep := range []string{"password", "accessKeySecret", "privateKey", "admin", "22", "ok"} {
		if !strings.Contains(out, keep) {
			t.Fatalf("应该保留 %q：%s", keep, out)
		}
	}
	if strings.Count(out, auditRedacted) != 3 {
		t.Fatalf("应该脱敏 3 处（password/accessKeySecret/privateKey），实际：%s", out)
	}
}

// 空值不必替换成占位串：那会让「本来没填」看起来像「填了但被隐藏」
func TestRedactBodyKeepsEmptyAsEmpty(t *testing.T) {
	out := redactBody("application/json", []byte(`{"password":"","token":null}`))
	if strings.Contains(out, auditRedacted) {
		t.Fatalf("空值不该被替换成占位串：%s", out)
	}
}

// 非 JSON 与解析失败都不记内容 —— 无法按字段脱敏时，原样记录的风险更大
func TestRedactBodyRefusesUnparseable(t *testing.T) {
	out := redactBody("multipart/form-data; boundary=x", []byte("...二进制..."))
	if !strings.Contains(out, "非 JSON") {
		t.Fatalf("非 JSON 应该只记类型说明，实际：%s", out)
	}
	out = redactBody("application/json", []byte(`{"password":"hunter2"`))
	if strings.Contains(out, "hunter2") {
		t.Fatalf("解析失败时不能把原文抄进去：%s", out)
	}
}

func TestRedactBodyTruncatesByRunes(t *testing.T) {
	long := `{"note":"` + strings.Repeat("中", 5000) + `"}`
	out := redactBody("application/json", []byte(long))
	if !strings.Contains(out, "已截断") {
		t.Fatal("超长请求体要标注截断")
	}
	if len([]rune(out)) > auditBodyLimit+20 {
		t.Fatalf("截断后仍然过长：%d 字符", len([]rune(out)))
	}
	// 按字符截断而不是字节：按字节切会把中文切成半个字
	for _, r := range out {
		if r == '\uFFFD' {
			t.Fatal("出现了替换字符，说明是按字节截断的")
		}
	}
}

func TestRedactQueryHidesAccessToken(t *testing.T) {
	out := RedactQuery("/api/v1/hosts/1/terminal?access_token=eyJhbGciOi.payload.sig&cols=120")
	if strings.Contains(out, "eyJhbGciOi") {
		t.Fatalf("令牌泄漏到访问日志里了：%s", out)
	}
	if !strings.Contains(out, "cols=120") {
		t.Fatalf("无关参数要保留（排查问题要用）：%s", out)
	}
	if !strings.Contains(out, "/api/v1/hosts/1/terminal?") {
		t.Fatalf("路径本身要保留：%s", out)
	}
}

func TestRedactQueryLeavesPlainPathAlone(t *testing.T) {
	const path = "/api/v1/hosts"
	if out := RedactQuery(path); out != path {
		t.Fatalf("没有查询串时应该原样返回，实际 %q", out)
	}
	const withSafe = "/api/v1/hosts?page=1&pageSize=20"
	if out := RedactQuery(withSafe); out != withSafe {
		t.Fatalf("没有敏感参数时不该改写（避免顺序抖动），实际 %q", out)
	}
}

func TestRedactQueryDropsUnparseable(t *testing.T) {
	out := RedactQuery("/x?%zz=1&access_token=abc")
	if strings.Contains(out, "abc") {
		t.Fatalf("解析失败时整段查询串都该丢掉：%s", out)
	}
	if !strings.Contains(out, "解析失败") {
		t.Fatalf("要说明为什么没有查询串：%s", out)
	}
}
