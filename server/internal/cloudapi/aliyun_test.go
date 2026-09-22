package cloudapi

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"testing"
)

// TestSignKnownVector 用阿里云签名文档里那个示例请求校验实现。
//
// 这条用例的价值在于它是**外部基准**：不是拿我们自己的实现验自己的实现。
// 时间戳里的冒号要被二次编码（%253A）、参数按字典序、Signature 自己不参与签名 ——
// 这三点任何一处写错，这里就会红。
func TestSignKnownVector(t *testing.T) {
	c := &AliyunClient{AccessKeyID: "testid", AccessKeySecret: "testsecret"}
	params := map[string]string{
		"AccessKeyId":      "testid",
		"Action":           "DescribeRegions",
		"Format":           "XML",
		"SignatureMethod":  "HMAC-SHA1",
		"SignatureNonce":   "3ee8c1b8-83d3-44af-a94f-4e0ad82fd6cf",
		"SignatureVersion": "1.0",
		"Timestamp":        "2016-02-23T12:46:24Z",
		"Version":          "2014-05-26",
	}
	const want = "OLeaidS1JvxuMvnyHOwuJ+uX5qY="
	if got := c.sign(http.MethodGet, params); got != want {
		t.Fatalf("签名与官方示例不一致:\n got = %s\nwant = %s", got, want)
	}
}

func TestPercentEncode(t *testing.T) {
	cases := map[string]string{
		"2016-02-23T12:46:24Z": "2016-02-23T12%3A46%3A24Z",
		"a b":                  "a%20b",
		"a*b":                  "a%2Ab",
		"a~b":                  "a~b",
		"/":                    "%2F",
	}
	for in, want := range cases {
		if got := percentEncode(in); got != want {
			t.Errorf("percentEncode(%q) = %q, want %q", in, got, want)
		}
	}
}

// signingStub 起一个本地桩，按阿里云的规则重算签名后才返回 body。
// 签名不对就回 SignatureDoesNotMatch —— 和真云端一样的失败方式。
func signingStub(t *testing.T, secret string, handler func(q url.Values) (int, string)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if !stubSignatureOK(q, secret) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"Code":"SignatureDoesNotMatch","Message":"签名不匹配"}`))
			return
		}
		status, body := handler(q)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func stubSignatureOK(q url.Values, secret string) bool {
	given := q.Get("Signature")
	if given == "" {
		return false
	}
	keys := make([]string, 0, len(q))
	for k := range q {
		if k == "Signature" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, percentEncode(k)+"="+percentEncode(q.Get(k)))
	}
	sts := "GET&" + percentEncode("/") + "&" + percentEncode(strings.Join(parts, "&"))
	mac := hmac.New(sha1.New, []byte(secret+"&"))
	mac.Write([]byte(sts))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil)) == given
}

const ecsOnePage = `{
  "TotalCount": 2, "PageNumber": 1, "PageSize": 100,
  "Instances": { "Instance": [
    {
      "InstanceId": "i-aaa", "InstanceName": "web-01", "Status": "Running",
      "RegionId": "cn-hangzhou", "ZoneId": "cn-hangzhou-b",
      "InstanceType": "ecs.g6.large", "Cpu": 2, "Memory": 8192,
      "OSName": "CentOS 7.9 64位", "InstanceChargeType": "PrePaid",
      "ExpiredTime": "2026-12-01T16:00Z", "CreationTime": "2025-01-01T02:00Z",
      "VpcAttributes": { "VpcId": "vpc-1", "PrivateIpAddress": { "IpAddress": ["172.16.0.10"] } },
      "PublicIpAddress": { "IpAddress": ["47.1.2.3"] },
      "EipAddress": { "IpAddress": "47.9.9.9" },
      "Tags": { "Tag": [ { "TagKey": "env", "TagValue": "prod" } ] }
    },
    {
      "InstanceId": "i-bbb", "HostName": "db-01", "Status": "Stopped",
      "RegionId": "cn-hangzhou", "InstanceType": "ecs.c6.xlarge",
      "InstanceChargeType": "PostPaid", "ExpiredTime": "",
      "InnerIpAddress": { "IpAddress": ["10.0.0.5"] }
    }
  ] }
}`

func TestListECSInstances(t *testing.T) {
	srv := signingStub(t, "sk", func(q url.Values) (int, string) {
		if q.Get("Action") != "DescribeInstances" {
			t.Errorf("Action = %q", q.Get("Action"))
		}
		if q.Get("RegionId") != "cn-hangzhou" {
			t.Errorf("RegionId = %q", q.Get("RegionId"))
		}
		if q.Get("Version") != "2014-05-26" {
			t.Errorf("Version = %q", q.Get("Version"))
		}
		return http.StatusOK, ecsOnePage
	})

	c := &AliyunClient{AccessKeyID: "ak", AccessKeySecret: "sk", Endpoint: srv.URL}
	list, err := c.ListECSInstances(context.Background(), "cn-hangzhou")
	if err != nil {
		t.Fatalf("拉取失败: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("实例数 = %d, want 2", len(list))
	}

	first := list[0]
	if first.Name != "web-01" || first.Status != "Running" {
		t.Errorf("第一台解析错: %+v", first)
	}
	if got := strings.Join(first.PrivateIPs, ","); got != "172.16.0.10" {
		t.Errorf("私网 IP = %q", got)
	}
	// 固定公网 IP 与 EIP 是两个字段，都要收进来
	if got := strings.Join(first.PublicIPs, ","); got != "47.1.2.3,47.9.9.9" {
		t.Errorf("公网 IP = %q, want 47.1.2.3,47.9.9.9", got)
	}
	if first.ExpiredAt == nil || first.ExpiredAt.Year() != 2026 {
		t.Errorf("到期时间解析错: %v", first.ExpiredAt)
	}
	if first.Tags["env"] != "prod" {
		t.Errorf("标签解析错: %v", first.Tags)
	}

	second := list[1]
	// 没有 InstanceName 时退到 HostName
	if second.Name != "db-01" {
		t.Errorf("第二台名字 = %q, want db-01", second.Name)
	}
	// 经典网络的内网地址在 InnerIpAddress 里
	if got := strings.Join(second.PrivateIPs, ","); got != "10.0.0.5" {
		t.Errorf("经典网络内网 IP = %q", got)
	}
	// ExpiredTime 为空串时必须是 nil，不能是零值时间：
	// 零值会被当成 1970 年到期，在页面上变成一条假的到期告警
	if second.ExpiredAt != nil {
		t.Errorf("按量付费实例不该有到期时间, got %v", second.ExpiredAt)
	}
}

func TestListECSInstancesRequiresRegion(t *testing.T) {
	c := &AliyunClient{AccessKeyID: "ak", AccessKeySecret: "sk"}
	if _, err := c.ListECSInstances(context.Background(), "  "); err == nil {
		t.Fatal("地域为空时应当直接报错，不能静默按默认地域查")
	}
}

func TestAuthErrorIsRecognised(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"Code":"InvalidAccessKeyId.NotFound","Message":"Specified access key is not found.","RequestId":"REQ-1"}`))
	}))
	defer srv.Close()

	c := &AliyunClient{AccessKeyID: "ak", AccessKeySecret: "sk", Endpoint: srv.URL}
	_, err := c.ListECSInstances(context.Background(), "cn-hangzhou")
	if err == nil {
		t.Fatal("期望报错")
	}
	apiErr, ok := err.(*APIError)
	if !ok {
		t.Fatalf("错误类型 = %T, want *APIError", err)
	}
	if !apiErr.IsAuthError() {
		t.Errorf("%s 应当被识别为密钥问题", apiErr.Code)
	}
	if !strings.Contains(apiErr.Error(), "InvalidAccessKeyId.NotFound") {
		t.Errorf("错误信息应带上 Code: %s", apiErr.Error())
	}
}

func TestBadSignatureRejectedByStub(t *testing.T) {
	// 桩用 sk 验签，客户端拿 wrong-sk 签 —— 必须失败。
	// 这条用例保证上面那些「成功」的用例不是因为桩没在验签
	srv := signingStub(t, "sk", func(url.Values) (int, string) { return http.StatusOK, ecsOnePage })
	c := &AliyunClient{AccessKeyID: "ak", AccessKeySecret: "wrong-sk", Endpoint: srv.URL}
	_, err := c.ListECSInstances(context.Background(), "cn-hangzhou")
	if err == nil {
		t.Fatal("密钥不对时不该成功")
	}
	if apiErr, ok := err.(*APIError); !ok || apiErr.Code != "SignatureDoesNotMatch" {
		t.Fatalf("期望 SignatureDoesNotMatch, got %v", err)
	}
}

func TestMissingCredential(t *testing.T) {
	c := &AliyunClient{Endpoint: "http://127.0.0.1:1"}
	if _, err := c.ListDNSDomains(context.Background()); err != ErrNoCredential {
		t.Fatalf("期望 ErrNoCredential, got %v", err)
	}
}

const domainsOnePage = `{
  "TotalCount": 1, "PageNumber": 1, "PageSize": 100,
  "Domains": { "Domain": [
    {
      "DomainId": "dom-1", "DomainName": "example.com", "PunyCode": "example.com",
      "VersionName": "免费版", "RecordCount": 12, "AliDomain": true,
      "CreateTime": "2024-01-01T00:00Z", "Remark": "对外站点",
      "DnsServers": { "DnsServer": ["ns1.alidns.com", "ns2.alidns.com"] }
    }
  ] }
}`

func TestListDNSDomains(t *testing.T) {
	srv := signingStub(t, "sk", func(q url.Values) (int, string) {
		if q.Get("Action") != "DescribeDomains" {
			t.Errorf("Action = %q", q.Get("Action"))
		}
		if q.Get("Version") != "2015-01-09" {
			t.Errorf("Version = %q", q.Get("Version"))
		}
		return http.StatusOK, domainsOnePage
	})

	c := &AliyunClient{AccessKeyID: "ak", AccessKeySecret: "sk", Endpoint: srv.URL}
	list, err := c.ListDNSDomains(context.Background())
	if err != nil {
		t.Fatalf("拉取失败: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("域名数 = %d, want 1", len(list))
	}
	d := list[0]
	if d.DomainName != "example.com" || d.RecordCount != 12 || !d.AliDomain {
		t.Errorf("解析错: %+v", d)
	}
	if len(d.DNSServers) != 2 {
		t.Errorf("DNS 服务器 = %v", d.DNSServers)
	}
}

func TestParseAliyunTime(t *testing.T) {
	if parseAliyunTime("") != nil {
		t.Error("空串应当返回 nil")
	}
	if parseAliyunTime("not-a-time") != nil {
		t.Error("解不开的时间应当返回 nil，不能退化成零值时间")
	}
	for _, s := range []string{
		"2026-12-01T16:00Z", "2026-12-01T16:00:05Z", "2026-12-01 16:00:05", "2026-12-01",
	} {
		if got := parseAliyunTime(s); got == nil || got.Year() != 2026 {
			t.Errorf("parseAliyunTime(%q) = %v", s, got)
		}
	}
}

func TestECSHost(t *testing.T) {
	if got := ECSHost("cn-hangzhou"); got != "ecs.cn-hangzhou.aliyuncs.com" {
		t.Errorf("ECSHost = %q", got)
	}
	if got := ECSHost(""); got != "ecs.aliyuncs.com" {
		t.Errorf("ECSHost(空) = %q", got)
	}
}
