package ldapx

import "testing"

// 过滤器拼接必须转义，否则是 LDAP 注入
func TestFilterEscapes(t *testing.T) {
	got := Filter("(&(objectClass=inetOrgPerson)(uid=%s))", "zhang*)(uid=admin")
	if got == "(&(objectClass=inetOrgPerson)(uid=zhang*)(uid=admin))" {
		t.Fatalf("特殊字符没有被转义: %s", got)
	}
	want := `(&(objectClass=inetOrgPerson)(uid=zhang\2a\29\28uid=admin))`
	if got != want {
		t.Fatalf("转义结果不对\n got: %s\nwant: %s", got, want)
	}
}

// 过滤器里没有占位符时回落到默认值，避免把用户名丢掉后变成「匹配所有人」
func TestFilterFallsBackWhenNoPlaceholder(t *testing.T) {
	got := Filter("(objectClass=user)", "zhangsan")
	if got != "(&(objectClass=inetOrgPerson)(uid=zhangsan))" {
		t.Fatalf("应回落到默认过滤器，实际 %s", got)
	}
}

// 搜索用的通配符不能被转义掉（真实目录上踩过：这么写一条都搜不出来）
func TestFilterWildcardKeepsStar(t *testing.T) {
	all := FilterWildcard("(&(objectClass=inetOrgPerson)(uid=%s))", "")
	if all != "(&(objectClass=inetOrgPerson)(uid=*))" {
		t.Fatalf("列全部时应保留 *，实际 %s", all)
	}
	kw := FilterWildcard("(&(objectClass=inetOrgPerson)(uid=%s))", "zhang")
	if kw != "(&(objectClass=inetOrgPerson)(uid=*zhang*))" {
		t.Fatalf("关键字两侧应加 *，实际 %s", kw)
	}
	// 关键字本身仍然要转义
	inj := FilterWildcard("(&(objectClass=inetOrgPerson)(uid=%s))", "a)(uid=admin")
	if inj != `(&(objectClass=inetOrgPerson)(uid=*a\29\28uid=admin*))` {
		t.Fatalf("关键字没有被转义: %s", inj)
	}
}

func TestLoginAttr(t *testing.T) {
	cases := map[string]string{
		"(&(objectClass=inetOrgPerson)(uid=%s))":   "uid",
		"(&(objectClass=user)(sAMAccountName=%s))": "sAMAccountName",
		"(userPrincipalName=%s)":                   "userPrincipalName",
		"(objectClass=user)":                       "uid",
	}
	for filter, want := range cases {
		if got := loginAttr(filter); got != want {
			t.Fatalf("%s → %s，期望 %s", filter, got, want)
		}
	}
}

// 空口令必须在建连之前就被拒：很多目录会把空口令当「匿名绑定」并返回成功
func TestAuthenticateRejectsEmptyPassword(t *testing.T) {
	// 地址故意填一个不存在的端口：如果代码先去建连，这里会报连接错误而不是口令错误
	s := Server{Host: "127.0.0.1", Port: 1, BaseDN: "dc=x"}
	err := Authenticate(s, "uid=someone,dc=x", "")
	if err == nil {
		t.Fatal("空口令必须拒绝")
	}
	if err.Error() != "口令不能为空" {
		t.Fatalf("应在建连前就拒绝空口令，实际: %v", err)
	}
	if err := Authenticate(s, "", "x"); err == nil {
		t.Fatal("DN 为空必须拒绝")
	}
}

func TestServerDefaults(t *testing.T) {
	s := Server{Host: "ldap.example.com", Encryption: EncLDAPS}
	if s.port() != 636 {
		t.Fatalf("ldaps 默认端口应为 636，实际 %d", s.port())
	}
	if s.URL() != "ldaps://ldap.example.com:636" {
		t.Fatalf("URL 不对: %s", s.URL())
	}
	plain := Server{Host: "ldap.example.com"}
	if plain.port() != 389 || plain.URL() != "ldap://ldap.example.com:389" {
		t.Fatalf("明文默认值不对: %d %s", plain.port(), plain.URL())
	}
	if plain.timeout().Seconds() != 8 {
		t.Fatalf("默认超时应为 8 秒，实际 %v", plain.timeout())
	}
}
