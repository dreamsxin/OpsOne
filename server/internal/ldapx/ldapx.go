// Package ldapx 封装平台需要的那一小部分 LDAP 能力：建连、按用户名查条目、
// 用条目 DN 校验口令、以及给管理界面用的搜索与连通性检查。
//
// 刻意不做的事：不做目录同步（那是「IM 组织同步」那条路）、不做组到角色的
// 映射、不改写目录（平台永远只读 LDAP）。
package ldapx

import (
	"crypto/tls"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/go-ldap/ldap/v3"
)

// 加密方式
const (
	EncNone     = "none"     // 明文 389
	EncLDAPS    = "ldaps"    // 636 上直接 TLS
	EncStartTLS = "starttls" // 389 上协商升级
)

// DefaultUserFilter OpenLDAP 常见形态；AD 一般改成
// (&(objectClass=user)(sAMAccountName=%s))
const DefaultUserFilter = "(&(objectClass=inetOrgPerson)(uid=%s))"

// Server 一台目录服务器的连接参数
type Server struct {
	Host         string
	Port         int
	Encryption   string
	SkipVerify   bool
	BindDN       string // 服务账号，留空表示匿名（只影响搜索，不影响登录校验）
	BindPassword string
	BaseDN       string
	UserFilter   string // 必须含一个 %s，替换为转义后的登录名
	AttrNickname string // 显示名属性，如 displayName / cn
	AttrEmail    string // 邮箱属性，如 mail
	TimeoutSec   int
}

// Entry 目录里的一个人
type Entry struct {
	DN       string `json:"dn"`
	UID      string `json:"uid"`
	Nickname string `json:"nickname"`
	Email    string `json:"email"`
}

func (s Server) timeout() time.Duration {
	if s.TimeoutSec <= 0 || s.TimeoutSec > 60 {
		return 8 * time.Second
	}
	return time.Duration(s.TimeoutSec) * time.Second
}

func (s Server) port() int {
	if s.Port > 0 {
		return s.Port
	}
	if s.Encryption == EncLDAPS {
		return 636
	}
	return 389
}

func (s Server) addr() string {
	return net.JoinHostPort(s.Host, fmt.Sprint(s.port()))
}

// URL 供界面展示用，别在日志里带上口令
func (s Server) URL() string {
	scheme := "ldap"
	if s.Encryption == EncLDAPS {
		scheme = "ldaps"
	}
	return scheme + "://" + s.addr()
}

// Dial 建立连接。调用方负责 Close。
func Dial(s Server) (*ldap.Conn, error) {
	if strings.TrimSpace(s.Host) == "" {
		return nil, fmt.Errorf("没有填服务器地址")
	}
	tlsCfg := &tls.Config{ServerName: s.Host, InsecureSkipVerify: s.SkipVerify} //nolint:gosec // 内网自签证书常见，是否跳过校验由管理员显式选择

	var conn *ldap.Conn
	var err error
	if s.Encryption == EncLDAPS {
		conn, err = ldap.DialURL("ldaps://"+s.addr(),
			ldap.DialWithTLSConfig(tlsCfg),
			ldap.DialWithDialer(&net.Dialer{Timeout: s.timeout()}))
	} else {
		conn, err = ldap.DialURL("ldap://"+s.addr(),
			ldap.DialWithDialer(&net.Dialer{Timeout: s.timeout()}))
	}
	if err != nil {
		return nil, err
	}
	conn.SetTimeout(s.timeout())

	if s.Encryption == EncStartTLS {
		if err := conn.StartTLS(tlsCfg); err != nil {
			conn.Close()
			return nil, fmt.Errorf("StartTLS 升级失败: %w", err)
		}
	}
	return conn, nil
}

// bindService 用服务账号绑定；没配就走匿名
func bindService(conn *ldap.Conn, s Server) error {
	if strings.TrimSpace(s.BindDN) == "" {
		return conn.UnauthenticatedBind("")
	}
	return conn.Bind(s.BindDN, s.BindPassword)
}

// Filter 把登录名安全地拼进过滤器。
//
// 转义是必须的：`*)(uid=*` 这类输入会改变过滤器语义，等于 LDAP 注入。
func Filter(filter, username string) string {
	if !strings.Contains(filter, "%s") {
		filter = DefaultUserFilter
	}
	return strings.ReplaceAll(filter, "%s", ldap.EscapeFilter(username))
}

// FilterWildcard 供「列一批人」用：关键字照样转义，但两侧的 * 是我们自己加的，
// 不能被转义掉。
//
// 这里踩过一次坑：搜索原先直接用 Filter(f, "*")，而 EscapeFilter 会把 * 转成
// \2a，于是过滤器变成「uid 等于字面量 *」，真实目录里一个人都搜不出来。
func FilterWildcard(filter, keyword string) string {
	if !strings.Contains(filter, "%s") {
		filter = DefaultUserFilter
	}
	pattern := "*"
	if kw := strings.TrimSpace(keyword); kw != "" {
		pattern = "*" + ldap.EscapeFilter(kw) + "*"
	}
	return strings.ReplaceAll(filter, "%s", pattern)
}

func (s Server) attrs() []string {
	attrs := []string{"dn"}
	for _, a := range []string{s.AttrNickname, s.AttrEmail} {
		if strings.TrimSpace(a) != "" {
			attrs = append(attrs, a)
		}
	}
	return attrs
}

func (s Server) toEntry(e *ldap.Entry, uid string) Entry {
	item := Entry{DN: e.DN, UID: uid}
	if s.AttrNickname != "" {
		item.Nickname = e.GetAttributeValue(s.AttrNickname)
	}
	if s.AttrEmail != "" {
		item.Email = e.GetAttributeValue(s.AttrEmail)
	}
	return item
}

// Lookup 按用户名精确查一个人。没找到返回 (nil, nil)，区别于「查询本身失败」。
func Lookup(s Server, username string) (*Entry, error) {
	conn, err := Dial(s)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if err := bindService(conn, s); err != nil {
		return nil, fmt.Errorf("服务账号绑定失败: %w", err)
	}

	req := ldap.NewSearchRequest(s.BaseDN, ldap.ScopeWholeSubtree, ldap.NeverDerefAliases,
		2, int(s.timeout().Seconds()), false, Filter(s.UserFilter, username), s.attrs(), nil)
	res, err := conn.Search(req)
	if err != nil {
		return nil, err
	}
	if len(res.Entries) == 0 {
		return nil, nil
	}
	if len(res.Entries) > 1 {
		// 过滤器写得太宽会命中多个人，这时候「用哪个 DN 校验口令」是没法猜的
		return nil, fmt.Errorf("用户过滤器命中了 %d 个条目，请收紧 userFilter", len(res.Entries))
	}
	entry := s.toEntry(res.Entries[0], username)
	return &entry, nil
}

// Search 关键字搜索，给管理界面挑人用
func Search(s Server, keyword string, limit int) ([]Entry, error) {
	conn, err := Dial(s)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if err := bindService(conn, s); err != nil {
		return nil, fmt.Errorf("服务账号绑定失败: %w", err)
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}

	filter := FilterWildcard(s.UserFilter, keyword)
	attrs := append(s.attrs(), loginAttr(s.UserFilter))
	req := ldap.NewSearchRequest(s.BaseDN, ldap.ScopeWholeSubtree, ldap.NeverDerefAliases,
		limit, int(s.timeout().Seconds()), false, filter, attrs, nil)
	res, err := conn.Search(req)
	if err != nil && !ldap.IsErrorWithCode(err, ldap.LDAPResultSizeLimitExceeded) {
		return nil, err
	}
	if res == nil {
		return nil, err
	}

	attr := loginAttr(s.UserFilter)
	items := make([]Entry, 0, len(res.Entries))
	for _, e := range res.Entries {
		items = append(items, s.toEntry(e, e.GetAttributeValue(attr)))
	}
	return items, nil
}

// loginAttr 从过滤器里猜登录名属性（`(uid=%s)` → uid），猜不出按 uid。
//
// 只用于搜索结果里回显「登录名」，登录校验本身不依赖它。
func loginAttr(filter string) string {
	idx := strings.Index(filter, "=%s")
	if idx <= 0 {
		return "uid"
	}
	head := filter[:idx]
	if pos := strings.LastIndexAny(head, "(&|!"); pos >= 0 {
		head = head[pos+1:]
	}
	head = strings.TrimSpace(head)
	if head == "" {
		return "uid"
	}
	return head
}

// Authenticate 用条目 DN 和口令做一次真实 bind。
//
// **口令为空一律拒绝**：LDAP 的「unauthenticated bind」在很多目录上会返回成功，
// 如果把空口令直接交给 bind，任何人填个存在的用户名、口令留空就能登录进来。
func Authenticate(s Server, dn, password string) error {
	if strings.TrimSpace(dn) == "" {
		return fmt.Errorf("没有可用的目录条目（DN 为空）")
	}
	if password == "" {
		return fmt.Errorf("口令不能为空")
	}
	conn, err := Dial(s)
	if err != nil {
		return err
	}
	defer conn.Close()
	return conn.Bind(dn, password)
}

// Ping 连通性检查：建连 + 服务账号绑定 + 在 BaseDN 下数一下人
func Ping(s Server) (int, error) {
	conn, err := Dial(s)
	if err != nil {
		return 0, err
	}
	defer conn.Close()
	if err := bindService(conn, s); err != nil {
		return 0, fmt.Errorf("服务账号绑定失败: %w", err)
	}
	if strings.TrimSpace(s.BaseDN) == "" {
		return 0, fmt.Errorf("没有填 Base DN")
	}

	req := ldap.NewSearchRequest(s.BaseDN, ldap.ScopeWholeSubtree, ldap.NeverDerefAliases,
		200, int(s.timeout().Seconds()), false, FilterWildcard(s.UserFilter, ""), []string{"dn"}, nil)
	res, err := conn.Search(req)
	if err != nil {
		if ldap.IsErrorWithCode(err, ldap.LDAPResultSizeLimitExceeded) && res != nil {
			return len(res.Entries), nil
		}
		return 0, err
	}
	return len(res.Entries), nil
}
