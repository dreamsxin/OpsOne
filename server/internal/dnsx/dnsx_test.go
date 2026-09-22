package dnsx

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestNormalizeName(t *testing.T) {
	cases := map[string]string{
		"Example.COM":                "example.com",
		"example.com.":               "example.com",
		"https://example.com/a/b":    "example.com",
		"http://example.com?q=1":     "example.com",
		"  www.example.com  ":        "www.example.com",
		"example.com#frag":           "example.com",
		"":                           "",
		"https://example.com:8443/x": "example.com:8443",
	}
	for in, want := range cases {
		if got := NormalizeName(in); got != want {
			t.Errorf("NormalizeName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormalizeServer(t *testing.T) {
	cases := map[string]string{
		"":                   "",
		"223.5.5.5":          "223.5.5.5:53",
		"223.5.5.5:53":       "223.5.5.5:53",
		"2001:db8::1":        "[2001:db8::1]:53",
		"[2001:db8::1]:5353": "[2001:db8::1]:5353",
		"dns.internal":       "dns.internal:53",
	}
	for in, want := range cases {
		if got := normalizeServer(in); got != want {
			t.Errorf("normalizeServer(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSplitList(t *testing.T) {
	got := SplitList(" 1.1.1.1, 2.2.2.2;3.3.3.3\n4.4.4.4\t")
	want := []string{"1.1.1.1", "2.2.2.2", "3.3.3.3", "4.4.4.4"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("SplitList = %v, want %v", got, want)
	}
	if len(SplitList("   ")) != 0 {
		t.Error("全空白应当切出空列表")
	}
}

func TestDiffSets(t *testing.T) {
	// 期望为空 = 不核对，永远没有差异
	if m, e := DiffSets(nil, []string{"1.1.1.1"}); m != nil || e != nil {
		t.Errorf("期望为空时不该报差异, got %v / %v", m, e)
	}

	missing, extra := DiffSets([]string{"1.1.1.1", "2.2.2.2"}, []string{"2.2.2.2", "3.3.3.3"})
	if strings.Join(missing, ",") != "1.1.1.1" {
		t.Errorf("missing = %v", missing)
	}
	if strings.Join(extra, ",") != "3.3.3.3" {
		t.Errorf("extra = %v", extra)
	}

	// 大小写与空白不该被当成差异
	if m, e := DiffSets([]string{" NS1.Example.COM "}, []string{"ns1.example.com"}); len(m)+len(e) != 0 {
		t.Errorf("大小写/空白不该算差异, got %v / %v", m, e)
	}
}

func TestDescribeDiff(t *testing.T) {
	if got := DescribeDiff("NS", nil, nil); got != "" {
		t.Errorf("没差异时应当返回空串, got %q", got)
	}
	if got := DescribeDiff("解析地址", []string{"1.1.1.1"}, nil); got != "解析地址 少了 1.1.1.1" {
		t.Errorf("got %q", got)
	}
	if got := DescribeDiff("NS", nil, []string{"a", "b"}); got != "NS 多了 a、b" {
		t.Errorf("got %q", got)
	}
	if got := DescribeDiff("NS", []string{"x"}, []string{"y"}); got != "NS 少了 x，多了 y" {
		t.Errorf("got %q", got)
	}
}

// TestLookupLocalhost 走真实解析路径但不需要联网：localhost 由系统 hosts / resolver 解析。
// 这条用例保证 Lookup 真的在查、并且「没有 CNAME」被正确归一成空串
// （标准库在没有 CNAME 时会把查询名原样回来，直接存下去就成了一条假的 CNAME 记录）。
func TestLookupLocalhost(t *testing.T) {
	res, err := Resolver{Timeout: 3 * time.Second}.Lookup(context.Background(), "localhost")
	if err != nil {
		t.Fatalf("解析 localhost 失败: %v", err)
	}
	if !res.Resolved() {
		t.Fatalf("localhost 应当能解析出地址, IPErr=%q", res.IPErr)
	}
	found := false
	for _, ip := range res.IPs {
		if ip == "127.0.0.1" || ip == "::1" {
			found = true
		}
	}
	if !found {
		t.Errorf("localhost 的解析结果里应当有回环地址, got %v", res.IPs)
	}
	if res.CNAME != "" {
		t.Errorf("localhost 没有 CNAME，应当是空串而不是 %q", res.CNAME)
	}
	if res.CostMS < 0 {
		t.Error("耗时不该是负数")
	}
}

func TestLookupEmptyName(t *testing.T) {
	if _, err := (Resolver{}).Lookup(context.Background(), "  "); err != ErrNoName {
		t.Fatalf("空域名应当返回 ErrNoName, got %v", err)
	}
}

// TestLookupDeadServerFailsFast 指向一个不监听的地址时必须快速失败，
// 而不是挂到调用方的超时上 —— 批量巡检里一条卡住就会拖垮整轮。
func TestLookupDeadServerFailsFast(t *testing.T) {
	start := time.Now()
	res, err := Resolver{
		Server:  "127.0.0.1:1",
		Timeout: 900 * time.Millisecond,
	}.Lookup(context.Background(), "example.com")
	if err != nil {
		t.Fatalf("Lookup 本身不该返回错误（每类记录各记自己的错）: %v", err)
	}
	if res.Resolved() {
		t.Fatal("不该从一个死掉的 DNS 服务器拿到结果")
	}
	if res.IPErr == "" {
		t.Error("应当记下 A/AAAA 查询的错误")
	}
	// 三类记录是并发查的，总耗时应当接近单次超时而不是三倍
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("耗时 %v，超时控制没生效（或三类记录被串行查了）", elapsed)
	}
	if res.Server != "127.0.0.1:1" {
		t.Errorf("应当回显实际使用的 DNS 服务器, got %q", res.Server)
	}
}
