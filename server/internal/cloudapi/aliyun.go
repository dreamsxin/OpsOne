// Package cloudapi 是云厂商只读 OpenAPI 的最小客户端。
//
// 为什么不用官方 SDK：我们只需要几个只读的 Describe 动作，
// 而阿里云官方 SDK 一个产品一个模块、一装就是几十兆依赖，
// 还会把「出网拉依赖」变成构建的前置条件。RPC 风格的签名算法本身只有二十行，
// 自己实现更可控，也让整个平台继续保持「只依赖 gin/gorm 那几个库」。
//
// 覆盖范围（本轮）：
//   - ECS DescribeInstances（2014-05-26）
//   - 云解析 DNS DescribeDomains（2015-01-09）
//
// 明确不覆盖：任何写操作。这个包里没有 Create/Delete/Modify，
// 平台拿着 AK/SK 也只能读 —— 云上资源的变更不从这里走。
package cloudapi

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// ErrNoCredential 账号没配 AK/SK。单独成错是因为这在界面上要给出
// 「去云账号页补密钥」而不是「同步失败」的提示。
var ErrNoCredential = errors.New("云账号未配置 AccessKey")

// DefaultTimeout 单次 OpenAPI 调用的超时。云上接口偶发慢，但拖过 20s
// 基本就是网络不通，早失败早给出结论。
const DefaultTimeout = 20 * time.Second

// maxPages 分页保护上限。一次同步最多翻这么多页（ECS 每页 100 条 → 5000 台），
// 避免接口异常返回固定 TotalCount 时无限翻页。
const maxPages = 50

// AliyunClient 阿里云 RPC 风格 OpenAPI 客户端。
type AliyunClient struct {
	AccessKeyID     string
	AccessKeySecret string

	// Endpoint 覆盖接入点。留空时按产品与地域推导；
	// 单测把它指向本地 httptest 桩，所以允许带 scheme 的 http:// 形式。
	Endpoint string

	// HTTP 自定义客户端。留空时用带 DefaultTimeout 的默认客户端。
	HTTP *http.Client
}

func (c *AliyunClient) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: DefaultTimeout}
}

// APIError 云端返回的业务错误。带上 Code 是为了让上层能区分
// 「密钥不对」（InvalidAccessKeyId.NotFound / SignatureDoesNotMatch）
// 和「这个地域没开通」这类可以照实展示给运维的原因。
type APIError struct {
	HTTPStatus int    `json:"-"`
	Code       string `json:"Code"`
	Message    string `json:"Message"`
	RequestID  string `json:"RequestId"`
}

func (e *APIError) Error() string {
	if e.Code == "" {
		return fmt.Sprintf("云端返回 HTTP %d", e.HTTPStatus)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// IsAuthError 是否密钥本身的问题。界面据此提示去改云账号，而不是重试。
func (e *APIError) IsAuthError() bool {
	switch e.Code {
	case "InvalidAccessKeyId.NotFound", "SignatureDoesNotMatch",
		"InvalidAccessKeyId.Inactive", "Forbidden.AccessKeyDisabled",
		"NoPermission", "Forbidden.RAM":
		return true
	}
	return false
}

// percentEncode 按阿里云签名要求做编码：url.QueryEscape 之后
// 把 + * ~ 三处与 RFC3986 不一致的地方修正回来。少这一步签名必然不过。
func percentEncode(s string) string {
	e := url.QueryEscape(s)
	e = strings.ReplaceAll(e, "+", "%20")
	e = strings.ReplaceAll(e, "*", "%2A")
	e = strings.ReplaceAll(e, "%7E", "~")
	return e
}

func nonce() string {
	buf := make([]byte, 12)
	if _, err := rand.Read(buf); err != nil {
		// crypto/rand 失败是极端情况；退回时间戳仍能满足「同一秒内不重复」之外的要求，
		// 真撞上重复云端会报 SignatureNonceUsed，由调用方看到明确错误。
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}

// sign 计算 RPC 风格签名（SignatureVersion 1.0 / HMAC-SHA1）。
//
//	StringToSign = HTTPMethod & percentEncode("/") & percentEncode(规范化查询串)
//	Signature    = Base64(HMAC-SHA1(AccessKeySecret + "&", StringToSign))
func (c *AliyunClient) sign(method string, params map[string]string) string {
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var canonical strings.Builder
	for i, k := range keys {
		if i > 0 {
			canonical.WriteByte('&')
		}
		canonical.WriteString(percentEncode(k))
		canonical.WriteByte('=')
		canonical.WriteString(percentEncode(params[k]))
	}

	stringToSign := method + "&" + percentEncode("/") + "&" + percentEncode(canonical.String())
	mac := hmac.New(sha1.New, []byte(c.AccessKeySecret+"&"))
	mac.Write([]byte(stringToSign))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// baseURL 把接入点整理成带 scheme 的形式。测试桩传 http://127.0.0.1:x 时原样使用。
func (c *AliyunClient) baseURL(fallbackHost string) string {
	ep := strings.TrimSpace(c.Endpoint)
	if ep == "" {
		return "https://" + fallbackHost
	}
	if strings.Contains(ep, "://") {
		return strings.TrimRight(ep, "/")
	}
	return "https://" + strings.TrimRight(ep, "/")
}

// call 发起一次签名后的 GET 请求，并把响应体解到 out。
func (c *AliyunClient) call(ctx context.Context, host, version, action string, extra map[string]string, out any) error {
	if c.AccessKeyID == "" || c.AccessKeySecret == "" {
		return ErrNoCredential
	}

	params := map[string]string{
		"Action":           action,
		"Version":          version,
		"Format":           "JSON",
		"AccessKeyId":      c.AccessKeyID,
		"SignatureMethod":  "HMAC-SHA1",
		"SignatureVersion": "1.0",
		"SignatureNonce":   nonce(),
		"Timestamp":        time.Now().UTC().Format("2006-01-02T15:04:05Z"),
	}
	for k, v := range extra {
		if v != "" {
			params[k] = v
		}
	}

	query := url.Values{}
	for k, v := range params {
		query.Set(k, v)
	}
	query.Set("Signature", c.sign(http.MethodGet, params))

	target := c.baseURL(host) + "/?" + query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	// 1MB 上限：正常分页响应远小于此，超出说明拿到的不是预期的 JSON
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		apiErr := &APIError{HTTPStatus: resp.StatusCode}
		// 错误体解不开也不要吞掉状态码，Error() 会退化成「云端返回 HTTP xxx」
		_ = json.Unmarshal(body, apiErr)
		return apiErr
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("解析 %s 响应失败: %w", action, err)
	}
	return nil
}

// ---------- ECS ----------

// ECSInstance 一台云服务器。字段只取运维台账真用得上的那些。
type ECSInstance struct {
	InstanceID   string
	Name         string
	Status       string // Running | Stopped | Starting | Stopping
	RegionID     string
	ZoneID       string
	InstanceType string
	CPU          int
	MemoryMB     int
	OSName       string
	ChargeType   string // PrePaid（包年包月）| PostPaid（按量）
	VpcID        string
	PrivateIPs   []string
	PublicIPs    []string
	ExpiredAt    *time.Time
	CreatedAt    *time.Time
	Tags         map[string]string
}

type ipList struct {
	IPAddress []string `json:"IpAddress"`
}

type ecsInstanceRaw struct {
	InstanceID         string `json:"InstanceId"`
	InstanceName       string `json:"InstanceName"`
	HostName           string `json:"HostName"`
	Status             string `json:"Status"`
	RegionID           string `json:"RegionId"`
	ZoneID             string `json:"ZoneId"`
	InstanceType       string `json:"InstanceType"`
	Cpu                int    `json:"Cpu"`
	Memory             int    `json:"Memory"`
	OSName             string `json:"OSName"`
	OSNameEn           string `json:"OSNameEn"`
	InstanceChargeType string `json:"InstanceChargeType"`
	ExpiredTime        string `json:"ExpiredTime"`
	CreationTime       string `json:"CreationTime"`
	PublicIPAddress    ipList `json:"PublicIpAddress"`
	InnerIPAddress     ipList `json:"InnerIpAddress"`
	EipAddress         struct {
		IPAddress string `json:"IpAddress"`
	} `json:"EipAddress"`
	VpcAttributes struct {
		VpcID            string `json:"VpcId"`
		PrivateIPAddress ipList `json:"PrivateIpAddress"`
	} `json:"VpcAttributes"`
	Tags struct {
		Tag []struct {
			TagKey   string `json:"TagKey"`
			TagValue string `json:"TagValue"`
		} `json:"Tag"`
	} `json:"Tags"`
}

type describeInstancesResp struct {
	TotalCount int `json:"TotalCount"`
	PageNumber int `json:"PageNumber"`
	PageSize   int `json:"PageSize"`
	Instances  struct {
		Instance []ecsInstanceRaw `json:"Instance"`
	} `json:"Instances"`
}

// ECSHost 推导 ECS 接入点。地域级接入点比中心接入点更靠谱：
// 中心接入点对部分地域会返回 InvalidRegionId。
func ECSHost(regionID string) string {
	if regionID == "" {
		return "ecs.aliyuncs.com"
	}
	return "ecs." + regionID + ".aliyuncs.com"
}

// ListECSInstances 拉取某地域下的全部 ECS 实例（自动翻页）。
//
// regionID 必填：ECS 的实例列表是地域维度的，不传等于只看默认地域，
// 会让「云上有、平台没有」的对照结果凭空少一批机器，比报错更危险。
func (c *AliyunClient) ListECSInstances(ctx context.Context, regionID string) ([]ECSInstance, error) {
	if strings.TrimSpace(regionID) == "" {
		return nil, errors.New("同步 ECS 必须指定地域（云账号上的 Region 为空）")
	}

	const pageSize = 100
	var all []ECSInstance
	for page := 1; page <= maxPages; page++ {
		var resp describeInstancesResp
		err := c.call(ctx, ECSHost(regionID), "2014-05-26", "DescribeInstances", map[string]string{
			"RegionId":   regionID,
			"PageNumber": fmt.Sprint(page),
			"PageSize":   fmt.Sprint(pageSize),
		}, &resp)
		if err != nil {
			return nil, err
		}
		for _, raw := range resp.Instances.Instance {
			all = append(all, convertECS(raw, regionID))
		}
		if len(resp.Instances.Instance) < pageSize || len(all) >= resp.TotalCount {
			break
		}
	}
	return all, nil
}

func convertECS(raw ecsInstanceRaw, fallbackRegion string) ECSInstance {
	item := ECSInstance{
		InstanceID:   raw.InstanceID,
		Name:         firstNonEmpty(raw.InstanceName, raw.HostName, raw.InstanceID),
		Status:       raw.Status,
		RegionID:     firstNonEmpty(raw.RegionID, fallbackRegion),
		ZoneID:       raw.ZoneID,
		InstanceType: raw.InstanceType,
		CPU:          raw.Cpu,
		MemoryMB:     raw.Memory,
		OSName:       firstNonEmpty(raw.OSName, raw.OSNameEn),
		ChargeType:   raw.InstanceChargeType,
		VpcID:        raw.VpcAttributes.VpcID,
		ExpiredAt:    parseAliyunTime(raw.ExpiredTime),
		CreatedAt:    parseAliyunTime(raw.CreationTime),
	}

	// 私网地址：VPC 机器在 VpcAttributes 里，经典网络在 InnerIpAddress 里
	item.PrivateIPs = append(item.PrivateIPs, raw.VpcAttributes.PrivateIPAddress.IPAddress...)
	item.PrivateIPs = append(item.PrivateIPs, raw.InnerIPAddress.IPAddress...)

	// 公网地址：固定公网 IP 与 EIP 是两个字段，都要
	item.PublicIPs = append(item.PublicIPs, raw.PublicIPAddress.IPAddress...)
	if raw.EipAddress.IPAddress != "" {
		item.PublicIPs = append(item.PublicIPs, raw.EipAddress.IPAddress)
	}
	item.PrivateIPs = dedupe(item.PrivateIPs)
	item.PublicIPs = dedupe(item.PublicIPs)

	if len(raw.Tags.Tag) > 0 {
		item.Tags = make(map[string]string, len(raw.Tags.Tag))
		for _, t := range raw.Tags.Tag {
			if t.TagKey != "" {
				item.Tags[t.TagKey] = t.TagValue
			}
		}
	}
	return item
}

// ---------- 云解析 DNS ----------

// DNSDomain 一个托管在云解析里的域名。
//
// 注意这里**没有注册到期时间**：DescribeDomains 是「DNS 托管」视角，
// 到期时间属于域名注册服务（domain.aliyuncs.com）。平台上的到期提醒
// 仍以域名台账里人工登记的到期日为准，见 docs/ROADMAP.md 的说明。
type DNSDomain struct {
	DomainID    string
	DomainName  string
	PunyCode    string
	VersionName string
	RecordCount int
	AliDomain   bool
	DNSServers  []string
	Remark      string
	CreatedAt   *time.Time
}

type describeDomainsResp struct {
	TotalCount int `json:"TotalCount"`
	PageNumber int `json:"PageNumber"`
	PageSize   int `json:"PageSize"`
	Domains    struct {
		Domain []struct {
			DomainID    string `json:"DomainId"`
			DomainName  string `json:"DomainName"`
			PunyCode    string `json:"PunyCode"`
			VersionName string `json:"VersionName"`
			RecordCount int    `json:"RecordCount"`
			AliDomain   bool   `json:"AliDomain"`
			Remark      string `json:"Remark"`
			CreateTime  string `json:"CreateTime"`
			DNSServers  struct {
				DNSServer []string `json:"DnsServer"`
			} `json:"DnsServers"`
		} `json:"Domain"`
	} `json:"Domains"`
}

// DNSHost 云解析的接入点。这个产品只有中心接入点，不分地域。
const DNSHost = "alidns.aliyuncs.com"

// ListDNSDomains 拉取账号下托管的全部域名（自动翻页）。
func (c *AliyunClient) ListDNSDomains(ctx context.Context) ([]DNSDomain, error) {
	const pageSize = 100
	var all []DNSDomain
	for page := 1; page <= maxPages; page++ {
		var resp describeDomainsResp
		err := c.call(ctx, DNSHost, "2015-01-09", "DescribeDomains", map[string]string{
			"PageNumber": fmt.Sprint(page),
			"PageSize":   fmt.Sprint(pageSize),
		}, &resp)
		if err != nil {
			return nil, err
		}
		for _, raw := range resp.Domains.Domain {
			all = append(all, DNSDomain{
				DomainID:    raw.DomainID,
				DomainName:  raw.DomainName,
				PunyCode:    raw.PunyCode,
				VersionName: raw.VersionName,
				RecordCount: raw.RecordCount,
				AliDomain:   raw.AliDomain,
				DNSServers:  raw.DNSServers.DNSServer,
				Remark:      raw.Remark,
				CreatedAt:   parseAliyunTime(raw.CreateTime),
			})
		}
		if len(resp.Domains.Domain) < pageSize || len(all) >= resp.TotalCount {
			break
		}
	}
	return all, nil
}

// ---------- 小工具 ----------

// parseAliyunTime 解析阿里云返回的时间。同一家的不同产品精度不一样
// （ECS 到分钟、有的到秒、有的带毫秒），逐个格式试，解不开返回 nil 而不是零值时间 ——
// 零值时间落到「到期时间」字段上会被当成 1970 年到期，反而制造假告警。
func parseAliyunTime(s string) *time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	layouts := []string{
		"2006-01-02T15:04Z",
		"2006-01-02T15:04:05Z",
		time.RFC3339,
		"2006-01-02T15:04:05.000Z",
		"2006-01-02 15:04:05",
		"2006-01-02",
	}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, s); err == nil {
			return &t
		}
	}
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func dedupe(values []string) []string {
	if len(values) < 2 {
		return values
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, v := range values {
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}
