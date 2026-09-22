// Package dnsx 是一个带超时、可指定 DNS 服务器的解析封装。
//
// 为什么要单独一个包：仓库里此前只有一处裸 `net.LookupHost`
// （`handler/exposure.go`），超时完全靠系统 resolver 的默认值 ——
// 那在「解析一批域名」的场景里会把一次巡检拖成几十秒。
// 这里统一挂 context 超时，并允许指定权威/公共 DNS 服务器：
// 域名核对经常需要**绕过**内网 DNS 的覆写（内网把生产域名指到测试地址是常见做法），
// 否则核对出来的是内网视角，跟外部用户看到的不是一回事。
//
// 只用标准库：`net.Resolver` 足够查 A/AAAA/CNAME/NS，不引入 miekg/dns。
// 代价是查不了 TXT 之外的原始记录、也拿不到 TTL 与权威标志 —— 用不到就不引。
package dnsx

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"
	"time"
)

// DefaultTimeout 单次解析的总超时。域名核对是批量操作，
// 单条卡久了整轮巡检就没法在一个可接受的时间内结束。
// 8s 与证书巡检的探测超时保持一致：实测有些网络下 A 记录查询会明显慢于 NS，
// 太短会把「网络慢」误报成「解析超时」。
const DefaultTimeout = 8 * time.Second

// Resolver 一次解析用的配置。零值可用（走系统 resolver + 默认超时）。
type Resolver struct {
	// Server 指定 DNS 服务器，形如 `223.5.5.5` 或 `223.5.5.5:53`。
	// 留空表示用系统配置的 resolver。
	Server string
	// Timeout 单次解析的超时，<=0 时用 DefaultTimeout。
	Timeout time.Duration
}

func (r Resolver) timeout() time.Duration {
	if r.Timeout > 0 {
		return r.Timeout
	}
	return DefaultTimeout
}

// normalizeServer 给 DNS 服务器地址补默认端口 53。
// IPv6 的方括号交给 net.JoinHostPort 处理，不自己拼字符串。
func normalizeServer(server string) string {
	server = strings.TrimSpace(server)
	if server == "" {
		return ""
	}
	// 已经是 host:port / [v6]:port 就原样用
	if _, _, err := net.SplitHostPort(server); err == nil {
		return server
	}
	return net.JoinHostPort(server, "53")
}

// netResolver 按配置构造标准库解析器。
// 指定了 Server 时必须 PreferGo + 自定义 Dial，否则 cgo resolver 会忽略我们给的地址。
func (r Resolver) netResolver() *net.Resolver {
	server := normalizeServer(r.Server)
	if server == "" {
		return net.DefaultResolver
	}
	return &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			d := net.Dialer{Timeout: r.timeout()}
			return d.DialContext(ctx, network, server)
		},
	}
}

// Result 一次解析的结果。
//
// 每类记录各自记错误而不是整体失败：NS 查不到不该让已经查到的 A 记录作废，
// 「解析得到什么」与「哪一类查不了」要分别说清楚。
type Result struct {
	// IPs A / AAAA 记录，已排序去重
	IPs []string
	// CNAME 规范名（不带末尾点）。目标本身不是 CNAME 时为空串 ——
	// 标准库在没有 CNAME 时会把查询名原样回来，这里已经归一掉了
	CNAME string
	// NS 权威服务器（不带末尾点），已排序去重
	NS []string

	// IPErr / CNAMEErr / NSErr 各类记录自己的错误信息，空串表示成功
	IPErr    string
	CNAMEErr string
	NSErr    string

	// Server 实际使用的 DNS 服务器，空串表示系统 resolver。回显给界面看，
	// 免得「为什么核对结果和我 dig 的不一样」变成一个查不清的问题
	Server string
	// CostMS 整次解析耗时
	CostMS int64
}

// Resolved 是否至少拿到了一条地址记录
func (res Result) Resolved() bool { return len(res.IPs) > 0 }

// ErrNoName 目标为空
var ErrNoName = errors.New("域名为空")

// Lookup 并发查 A/AAAA、CNAME、NS 三类记录。
//
// 三类并发是有意的：串行做三次查询，超时在最坏情况下会叠成三倍。
func (r Resolver) Lookup(ctx context.Context, name string) (Result, error) {
	name = normalizeName(name)
	if name == "" {
		return Result{}, ErrNoName
	}

	start := time.Now()
	res := Result{Server: strings.TrimSpace(r.Server)}
	resolver := r.netResolver()

	ctx, cancel := context.WithTimeout(ctx, r.timeout())
	defer cancel()

	type ipOut struct {
		ips []string
		err error
	}
	ipCh := make(chan ipOut, 1)
	cnameCh := make(chan struct {
		cname string
		err   error
	}, 1)
	nsCh := make(chan struct {
		ns  []string
		err error
	}, 1)

	go func() {
		addrs, err := resolver.LookupIPAddr(ctx, name)
		out := ipOut{err: err}
		for _, a := range addrs {
			out.ips = append(out.ips, a.IP.String())
		}
		ipCh <- out
	}()
	go func() {
		cname, err := resolver.LookupCNAME(ctx, name)
		cnameCh <- struct {
			cname string
			err   error
		}{cname, err}
	}()
	go func() {
		records, err := resolver.LookupNS(ctx, name)
		var list []string
		for _, ns := range records {
			list = append(list, strings.TrimSuffix(ns.Host, "."))
		}
		nsCh <- struct {
			ns  []string
			err error
		}{list, err}
	}()

	ipRes := <-ipCh
	res.IPs = dedupeSorted(ipRes.ips)
	if ipRes.err != nil {
		res.IPErr = cleanDNSError(ipRes.err)
	}

	cnameRes := <-cnameCh
	if cnameRes.err != nil {
		res.CNAMEErr = cleanDNSError(cnameRes.err)
	} else {
		cname := strings.TrimSuffix(cnameRes.cname, ".")
		// 没有 CNAME 时标准库回的是查询名本身，那不是一条 CNAME 记录
		if !strings.EqualFold(cname, name) {
			res.CNAME = cname
		}
	}

	nsRes := <-nsCh
	res.NS = dedupeSorted(nsRes.ns)
	if nsRes.err != nil {
		res.NSErr = cleanDNSError(nsRes.err)
	}

	res.CostMS = time.Since(start).Milliseconds()
	return res, nil
}

// normalizeName 去掉常见误填：协议、路径、末尾点、大小写
func normalizeName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	if idx := strings.Index(name, "://"); idx >= 0 {
		name = name[idx+3:]
	}
	if idx := strings.IndexAny(name, "/?#"); idx >= 0 {
		name = name[:idx]
	}
	name = strings.TrimSuffix(name, ".")
	return strings.ToLower(name)
}

// NormalizeName 导出版本，供调用方在落库前统一域名写法
func NormalizeName(name string) string { return normalizeName(name) }

// cleanDNSError 把标准库那串带地址的错误压成人能看的一句话。
// 原文形如 `lookup example.com on 10.0.0.1:53: no such host`，
// 里面的 resolver 地址对排查有用但不该顶在列表页上。
func cleanDNSError(err error) string {
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		switch {
		case dnsErr.IsNotFound:
			return "没有这条记录"
		case dnsErr.IsTimeout:
			return "解析超时"
		default:
			return dnsErr.Err
		}
	}
	return err.Error()
}

func dedupeSorted(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

// DiffSets 对比期望与实际，返回「少了哪些」「多了哪些」。
// 两边都做了归一（去空、排序、忽略大小写），期望为空时不判定漂移。
func DiffSets(expect, actual []string) (missing, extra []string) {
	if len(expect) == 0 {
		return nil, nil
	}
	want := map[string]struct{}{}
	for _, v := range expect {
		if v = strings.ToLower(strings.TrimSpace(v)); v != "" {
			want[v] = struct{}{}
		}
	}
	got := map[string]struct{}{}
	for _, v := range actual {
		if v = strings.ToLower(strings.TrimSpace(v)); v != "" {
			got[v] = struct{}{}
		}
	}
	for v := range want {
		if _, ok := got[v]; !ok {
			missing = append(missing, v)
		}
	}
	for v := range got {
		if _, ok := want[v]; !ok {
			extra = append(extra, v)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	return missing, extra
}

// SplitList 把逗号/空格/换行分隔的配置串切成列表
func SplitList(raw string) []string {
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\n' || r == '\r' || r == '\t' || r == ';'
	})
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}

// DescribeDiff 把 missing/extra 拼成一句人话，给界面直接展示
func DescribeDiff(label string, missing, extra []string) string {
	switch {
	case len(missing) == 0 && len(extra) == 0:
		return ""
	case len(extra) == 0:
		return fmt.Sprintf("%s 少了 %s", label, strings.Join(missing, "、"))
	case len(missing) == 0:
		return fmt.Sprintf("%s 多了 %s", label, strings.Join(extra, "、"))
	default:
		return fmt.Sprintf("%s 少了 %s，多了 %s",
			label, strings.Join(missing, "、"), strings.Join(extra, "、"))
	}
}
