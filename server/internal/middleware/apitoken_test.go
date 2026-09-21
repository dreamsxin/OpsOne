package middleware

import "testing"

// 权限交集：令牌授予的权限码不可能超过归属人实际拥有的
func TestIntersectScopes(t *testing.T) {
	owner := map[string]struct{}{"exec:run": {}, "host:manage": {}}
	got := IntersectScopes([]string{"exec:run", "db:query", " host:manage ", ""}, owner)
	if _, ok := got["exec:run"]; !ok {
		t.Fatal("归属人有的权限应该保留")
	}
	if _, ok := got["db:query"]; ok {
		t.Fatal("归属人没有的权限必须被剔掉")
	}
	// 两端空格要容错，空串不该进集合
	if _, ok := got["host:manage"]; !ok {
		t.Fatal("带空格的权限码应被 trim 后匹配")
	}
	if len(got) != 2 {
		t.Fatalf("交集应为 2 项，实际 %v", got)
	}
}

func TestIPAllowed(t *testing.T) {
	cases := []struct {
		allow, ip string
		want      bool
	}{
		{"", "10.1.2.3", true},                             // 不配白名单 = 不限制
		{"10.1.2.3", "10.1.2.3", true},                     // 单个 IP
		{"10.1.2.3", "10.1.2.4", false},                    // 不在名单里
		{"10.1.0.0/16", "10.1.9.9", true},                  // CIDR
		{"10.1.0.0/16", "10.2.0.1", false},                 // CIDR 外
		{"127.0.0.1, 192.168.1.0/24", "192.168.1.7", true}, // 多条目
		{"not-an-ip", "10.1.2.3", false},                   // 写错的条目不该放开
		{"10.1.0.0/16", "", false},                         // 取不到来源 IP 时按拒绝
	}
	for _, c := range cases {
		if got := IPAllowed(c.allow, c.ip); got != c.want {
			t.Fatalf("IPAllowed(%q, %q) = %v，期望 %v", c.allow, c.ip, got, c.want)
		}
	}
}

// 哈希不可逆、前缀只露一小截
func TestHashAndPrefix(t *testing.T) {
	plain := ApiTokenPrefix + "abcdefghijklmnopqrstuvwxyz0123456789"
	hash := HashAPIToken(plain)
	if len(hash) != 64 {
		t.Fatalf("应是 SHA-256 十六进制，实际长度 %d", len(hash))
	}
	if hash == plain || len(hash) >= len(plain) && hash[:len(ApiTokenPrefix)] == ApiTokenPrefix {
		t.Fatal("哈希里不该出现明文特征")
	}
	if HashAPIToken(plain) != hash {
		t.Fatal("同一明文的哈希必须稳定")
	}
	if HashAPIToken(plain+"x") == hash {
		t.Fatal("不同明文不能哈希成同一个值")
	}

	prefix := APITokenPrefixOf(plain)
	if len(prefix) != 12 || prefix != plain[:12] {
		t.Fatalf("前缀应是明文前 12 位，实际 %q", prefix)
	}
	if len(prefix) >= len(plain) {
		t.Fatal("前缀不能等于整个明文")
	}
}

func TestScopeList(t *testing.T) {
	if got := ScopeList(`["a","b"]`); len(got) != 2 || got[0] != "a" {
		t.Fatalf("解析失败: %v", got)
	}
	// 坏数据不能 panic，也不能变成「全部权限」
	for _, raw := range []string{"", "   ", "not json", "{}"} {
		if got := ScopeList(raw); len(got) != 0 {
			t.Fatalf("坏数据 %q 应解析成空列表，实际 %v", raw, got)
		}
	}
}
