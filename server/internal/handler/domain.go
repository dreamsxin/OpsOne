package handler

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"ops-platform/server/internal/dnsx"
	"ops-platform/server/internal/middleware"
	"ops-platform/server/internal/model"
	"ops-platform/server/internal/response"
)

const (
	// domainCheckConcurrency 并发核对多少个域名。DNS 查询很轻，但并发太高
	// 会被一些 resolver 当成异常流量限速，5 和证书巡检保持一致
	domainCheckConcurrency = 5
	// domainAlertSourceName 内部告警接入源名称
	domainAlertSourceName = "域名巡检"
)

// ---------- 巡检核心 ----------

// checkDomain 核对一个域名：查 DNS、对期望、算到期、关联证书，回写并联动告警。
//
// 三件事刻意分开判断：DNS 漂移与注册到期是两类完全不同的问题
// （前者是「解析被改了」，后者是「忘续费了」），混成一个 status 就没法分别告警。
func (h *Handler) checkDomain(domain model.Domain) model.Domain {
	now := time.Now()
	resolver := dnsx.Resolver{Server: h.dnsServer()}

	res, err := resolver.Lookup(context.Background(), domain.Name)
	updates := map[string]any{
		"last_check_at": &now,
		"dns_server":    res.Server,
	}

	if err != nil {
		updates["dns_status"] = "error"
		updates["dns_detail"] = truncate("解析失败: "+err.Error(), 500)
		domain.DNSStatus, domain.DNSDetail = "error", updates["dns_detail"].(string)
	} else {
		status, detail := judgeDNS(domain, res)
		updates["resolved_ips"] = truncate(strings.Join(res.IPs, ","), 250)
		updates["resolved_cname"] = truncate(res.CNAME, 120)
		updates["resolved_ns"] = truncate(strings.Join(res.NS, ","), 250)
		updates["dns_status"] = status
		updates["dns_detail"] = truncate(detail, 500)

		domain.ResolvedIPs = updates["resolved_ips"].(string)
		domain.ResolvedCNAME = updates["resolved_cname"].(string)
		domain.ResolvedNS = updates["resolved_ns"].(string)
		domain.DNSStatus, domain.DNSDetail = status, updates["dns_detail"].(string)
	}
	domain.DNSServer = res.Server
	domain.LastCheckAt = &now

	// 到期状态
	expireStatus, daysLeft := domainExpireStatus(now, domain.ExpiresAt, domain.AlertDays)
	updates["expire_status"], updates["days_left"] = expireStatus, daysLeft
	domain.ExpireStatus, domain.DaysLeft = expireStatus, daysLeft

	// 按 SAN 关联证书
	certCount, certNames, minDays := h.matchCertificates(domain.Name)
	updates["cert_count"] = certCount
	updates["cert_names"] = truncate(strings.Join(certNames, ","), 250)
	updates["cert_min_days_left"] = minDays
	domain.CertCount, domain.CertNames, domain.CertMinDaysLeft = certCount, updates["cert_names"].(string), minDays

	// 用 map 更新而不是 Save：Save 是全列写回，会把人正在编辑的期望值覆盖成读出来的旧值
	h.DB.Model(&model.Domain{}).Where("id = ?", domain.ID).Updates(updates)

	h.syncDomainAlerts(domain)
	return domain
}

// judgeDNS 把解析结果与期望值对出一个状态 + 一句人话。
//
// 期望值留空 = 不核对那一类。三类都没填时状态是 nocheck，不是 ok ——
// 「没检查」和「检查通过」在值班视角里完全不是一回事。
func judgeDNS(domain model.Domain, res dnsx.Result) (status, detail string) {
	expectIPs := dnsx.SplitList(domain.ExpectIPs)
	expectNS := dnsx.SplitList(domain.ExpectNS)
	expectCNAME := strings.ToLower(strings.TrimSpace(domain.ExpectCNAME))
	hasExpect := len(expectIPs) > 0 || len(expectNS) > 0 || expectCNAME != ""

	// 一条记录都查不到：域名可能压根不存在或已被停止解析。
	// 只在「什么都没有」时才判 unresolved —— 只有 NS、没有 A 是很多域名的正常形态
	// （没做网站的域名、只用来发邮件的域名），把它判成异常就是制造噪音
	if len(res.IPs) == 0 && res.CNAME == "" && len(res.NS) == 0 {
		reason := firstNonBlank(res.IPErr, res.NSErr, res.CNAMEErr, "没有返回任何记录")
		return "unresolved", "什么记录都查不到：" + reason
	}

	var parts []string
	drift := false

	if len(expectIPs) > 0 {
		missing, extra := dnsx.DiffSets(expectIPs, res.IPs)
		if d := dnsx.DescribeDiff("解析地址", missing, extra); d != "" {
			parts = append(parts, d)
			drift = true
		}
	}
	if expectCNAME != "" {
		got := strings.ToLower(res.CNAME)
		if got != expectCNAME {
			shown := got
			if shown == "" {
				shown = "（没有 CNAME 记录）"
			}
			parts = append(parts, fmt.Sprintf("CNAME 期望 %s，实际 %s", expectCNAME, shown))
			drift = true
		}
	}
	if len(expectNS) > 0 {
		missing, extra := dnsx.DiffSets(expectNS, res.NS)
		if d := dnsx.DescribeDiff("NS", missing, extra); d != "" {
			parts = append(parts, d)
			drift = true
		}
	}

	// 查询本身的错误单独附上：NS 查不到不该让已经对上的 A 记录作废，
	// 但也不能默默吞掉
	var notes []string
	if res.IPErr != "" && len(expectIPs) == 0 {
		notes = append(notes, "A/AAAA 查询: "+res.IPErr)
	}
	if res.NSErr != "" {
		notes = append(notes, "NS 查询: "+res.NSErr)
	}
	if res.CNAMEErr != "" {
		notes = append(notes, "CNAME 查询: "+res.CNAMEErr)
	}

	switch {
	case drift:
		detail = strings.Join(parts, "；")
		if len(notes) > 0 {
			detail += "（" + strings.Join(notes, "；") + "）"
		}
		return "drift", detail
	case !hasExpect:
		detail = "没有填写任何期望值，只记录了实际解析结果"
		if len(notes) > 0 {
			detail += "（" + strings.Join(notes, "；") + "）"
		}
		return "nocheck", detail
	default:
		detail = "与期望一致"
		if len(notes) > 0 {
			detail += "（" + strings.Join(notes, "；") + "）"
		}
		return "ok", detail
	}
}

// domainExpireStatus 算注册到期状态。
//
// ExpiresAt 为空返回 unknown 而不是 valid：没登记到期日不等于还很久，
// 那正是最容易忘续费的一批域名，界面上要能单独筛出来。
func domainExpireStatus(now time.Time, expiresAt *time.Time, alertDays int) (string, int) {
	if expiresAt == nil {
		return "unknown", 0
	}
	if alertDays <= 0 {
		alertDays = 30
	}
	days := daysUntil(now, *expiresAt)
	switch {
	case now.After(*expiresAt):
		return "expired", days
	case days <= alertDays:
		return "expiring", days
	default:
		return "valid", days
	}
}

// matchCertificates 按证书的 SAN 反查这个域名用到了哪些证书。
//
// 匹配规则：SAN 完全等于该域名、或是它的子域、或是覆盖它的通配符。
// 在 Go 里做而不是写 SQL LIKE：SAN 是逗号分隔的一列，SQL 里判「子域」
// 会写成一串容易漏的 LIKE，而证书总量是百量级，全读出来比较更清楚也更准。
func (h *Handler) matchCertificates(name string) (count int, names []string, minDaysLeft int) {
	name = dnsx.NormalizeName(name)
	if name == "" {
		return 0, nil, 0
	}

	var certs []model.Certificate
	h.DB.Select("id, name, dns_names, domain, not_after, days_left").Find(&certs)

	minDaysLeft = 0
	first := true
	for _, cert := range certs {
		sans := dnsx.SplitList(cert.DNSNames)
		if len(sans) == 0 && cert.Domain != "" {
			// 还没巡检过的证书没有 SAN，退回用探测目标凑一个
			sans = []string{cert.Domain}
		}
		if !sansCoverDomain(sans, name) {
			continue
		}
		count++
		names = append(names, cert.Name)
		if first || cert.DaysLeft < minDaysLeft {
			minDaysLeft, first = cert.DaysLeft, false
		}
	}
	if count == 0 {
		return 0, nil, 0
	}
	return count, names, minDaysLeft
}

// sansCoverDomain SAN 列表里是否有一条覆盖了目标域名
func sansCoverDomain(sans []string, name string) bool {
	for _, san := range sans {
		san = dnsx.NormalizeName(san)
		if san == "" {
			continue
		}
		if san == name {
			return true
		}
		// 子域：cert 上有 api.example.com，域名台账里是 example.com
		if strings.HasSuffix(san, "."+name) {
			return true
		}
		// 通配符：*.example.com 覆盖 example.com 的一级子域，也算这个域名在用
		if strings.HasPrefix(san, "*.") && strings.TrimPrefix(san, "*.") == name {
			return true
		}
	}
	return false
}

// dnsServer 取核对用的 DNS 服务器。空串表示用系统 resolver。
func (h *Handler) dnsServer() string {
	if h.Cfg == nil {
		return ""
	}
	return h.Cfg.DNSServer
}

// ---------- 告警联动 ----------

// domainAlertKinds 域名巡检会产生的三类告警
var domainAlertKinds = []string{"expiring", "expired", "dns_drift"}

// syncDomainAlerts 与证书巡检同一套做法：指纹按「域名 + 类别」固定，
// 重复巡检只累加次数；状态恢复时对另外几类发 resolved。
//
// 为什么 DNS 漂移也走告警而不是只发站内消息：解析被改掉通常要有人立刻知道，
// 而站内消息没人盯。到期提醒同理，续费是有截止日期的事。
func (h *Handler) syncDomainAlerts(domain model.Domain) {
	if !domain.AlertEnabled {
		return
	}
	source, err := h.internalAlertSource(domainAlertSourceName)
	if err != nil {
		log.Printf("[domain] 内部告警源不可用，跳过告警: %v", err)
		return
	}

	labels := map[string]string{
		"module":   "domain",
		"domainId": strconv.FormatUint(uint64(domain.ID), 10),
		"domain":   domain.Name,
	}
	fire := func(kind, title, summary, severity, value string) {
		h.ingestAlert(source, alertPayload{
			Title: title, Summary: summary, Severity: severity, Value: value,
			Fingerprint: domainAlertFingerprint(domain.ID, kind), Labels: labels,
		})
	}
	resolve := func(kind string) {
		h.ingestAlert(source, alertPayload{
			Title: "域名巡检恢复", Status: "resolved",
			Fingerprint: domainAlertFingerprint(domain.ID, kind), Labels: labels,
		})
	}

	// 到期：unknown（没填到期日）不告警 —— 那是台账没补全，不是域名出事，
	// 界面上用「待补到期日」单独提示，不往值班的告警列表里塞
	switch domain.ExpireStatus {
	case "expired":
		fire("expired",
			fmt.Sprintf("域名已过期：%s", domain.Name),
			fmt.Sprintf("%s 的注册已于 %s 到期，注册商 %s",
				domain.Name, formatDay(domain.ExpiresAt), domainOrNotSet(domain.Registrar)),
			"critical", strconv.Itoa(domain.DaysLeft))
		resolve("expiring")
	case "expiring":
		fire("expiring",
			fmt.Sprintf("域名即将到期：%s", domain.Name),
			fmt.Sprintf("%s 剩余 %d 天（阈值 %d 天），到期日 %s，注册商 %s，自动续费%s",
				domain.Name, domain.DaysLeft, domain.AlertDays, formatDay(domain.ExpiresAt),
				domainOrNotSet(domain.Registrar), yesNo(domain.AutoRenew)),
			"warning", strconv.Itoa(domain.DaysLeft))
		resolve("expired")
	default:
		resolve("expired")
		resolve("expiring")
	}

	// DNS：只有 drift 与 unresolved 告警。nocheck 不告警（没填期望值是配置问题，
	// 不是故障），error 归到 unresolved 同一类，避免类别过细
	switch domain.DNSStatus {
	case "drift":
		fire("dns_drift",
			fmt.Sprintf("域名解析与期望不符：%s", domain.Name),
			fmt.Sprintf("%s 的解析结果和登记的期望值对不上：%s", domain.Name, domain.DNSDetail),
			"critical", "")
	case "unresolved", "error":
		fire("dns_drift",
			fmt.Sprintf("域名解析异常：%s", domain.Name),
			fmt.Sprintf("%s %s", domain.Name, domain.DNSDetail),
			"warning", "")
	default:
		resolve("dns_drift")
	}
}

func domainAlertFingerprint(domainID uint, kind string) string {
	return internalAlertFingerprint(fmt.Sprintf("domain|%d|%s", domainID, kind))
}

func formatDay(t *time.Time) string {
	if t == nil {
		return "未登记"
	}
	return t.Format("2006-01-02")
}

func domainOrNotSet(v string) string {
	if strings.TrimSpace(v) == "" {
		return "未登记"
	}
	return v
}

func yesNo(v bool) string {
	if v {
		return "已开"
	}
	return "未开"
}

func firstNonBlank(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// resolveDomainAlerts 把某个域名名下的三类告警全部置为恢复。
// 删除台账时要做这一步，否则会留下一条指向已不存在对象的告警。
func (h *Handler) resolveDomainAlerts(domain model.Domain) {
	source, err := h.internalAlertSource(domainAlertSourceName)
	if err != nil {
		return
	}
	labels := map[string]string{
		"module":   "domain",
		"domainId": strconv.FormatUint(uint64(domain.ID), 10),
		"domain":   domain.Name,
	}
	for _, kind := range domainAlertKinds {
		h.ingestAlert(source, alertPayload{
			Title: "域名台账已删除", Status: "resolved",
			Fingerprint: domainAlertFingerprint(domain.ID, kind), Labels: labels,
		})
	}
}

// ---------- 批量与定时 ----------

// checkDomains 并发核对一批域名，返回各状态计数
func (h *Handler) checkDomains(list []model.Domain) gin.H {
	var (
		wg     sync.WaitGroup
		mu     sync.Mutex
		counts = map[string]int{}
	)
	sem := make(chan struct{}, domainCheckConcurrency)

	for _, item := range list {
		wg.Add(1)
		go func(d model.Domain) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			after := h.checkDomain(d)
			mu.Lock()
			counts["checked"]++
			counts["dns_"+after.DNSStatus]++
			counts["expire_"+after.ExpireStatus]++
			mu.Unlock()
		}(item)
	}
	wg.Wait()

	return gin.H{
		"checked":    counts["checked"],
		"dnsOk":      counts["dns_ok"],
		"dnsDrift":   counts["dns_drift"],
		"dnsBad":     counts["dns_unresolved"] + counts["dns_error"],
		"dnsNoCheck": counts["dns_nocheck"],
		"expiring":   counts["expire_expiring"],
		"expired":    counts["expire_expired"],
		"noExpiry":   counts["expire_unknown"],
	}
}

// CheckAllDomainsForSchedule 定时巡检入口。不走数据范围，全量启用的域名都查。
func (h *Handler) CheckAllDomainsForSchedule() {
	var list []model.Domain
	if err := h.DB.Where("enabled = ?", true).Find(&list).Error; err != nil {
		log.Printf("[domain] 定时巡检读取域名失败: %v", err)
		return
	}
	if len(list) == 0 {
		return
	}
	summary := h.checkDomains(list)
	info := fmt.Sprintf("共 %v，解析一致 %v，漂移 %v，解析异常 %v，将到期 %v，已过期 %v，待补到期日 %v",
		summary["checked"], summary["dnsOk"], summary["dnsDrift"], summary["dnsBad"],
		summary["expiring"], summary["expired"], summary["noExpiry"])
	h.markFixedRun("domain", info)
	log.Printf("[domain] 定时巡检完成: %s", info)
}

// ---------- 接口 ----------

type domainReq struct {
	Name         string `json:"name" binding:"required"`
	Registrar    string `json:"registrar"`
	RegisteredAt string `json:"registeredAt"` // YYYY-MM-DD，空串表示不登记
	ExpiresAt    string `json:"expiresAt"`
	AutoRenew    *bool  `json:"autoRenew"`
	Owner        string `json:"owner"`
	Purpose      string `json:"purpose"`
	ExpectIPs    string `json:"expectIps"`
	ExpectCNAME  string `json:"expectCname"`
	ExpectNS     string `json:"expectNs"`
	AlertDays    int    `json:"alertDays"`
	AlertEnabled *bool  `json:"alertEnabled"`
	DeptID       uint   `json:"deptId"`
	Enabled      *bool  `json:"enabled"`
	Remark       string `json:"remark"`
}

func (req *domainReq) normalize() error {
	req.Name = dnsx.NormalizeName(req.Name)
	if req.Name == "" {
		return errors.New("请填写域名")
	}
	// 常见误填：把 URL 或带端口的地址粘进来。NormalizeName 已经剥掉协议与路径，
	// 剩下还带冒号就说明是 host:port
	if strings.Contains(req.Name, ":") {
		return errors.New("域名不要带端口")
	}
	if !strings.Contains(req.Name, ".") {
		return errors.New("这看起来不是一个域名")
	}
	if req.AlertDays <= 0 {
		req.AlertDays = 30
	}
	if req.AlertDays > 365 {
		return errors.New("提醒阈值最多 365 天")
	}
	return nil
}

// parseDay 解析 YYYY-MM-DD。空串返回 nil（表示「不登记」而不是零值时间）——
// 零值时间会被当成 1970 年到期，凭空造出一条已过期告警
func parseDay(raw string) (*time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	t, err := time.ParseInLocation("2006-01-02", raw, time.Local)
	if err != nil {
		return nil, fmt.Errorf("日期格式应为 YYYY-MM-DD: %s", raw)
	}
	return &t, nil
}

func (h *Handler) ListDomains(c *gin.Context) {
	page, size := pageParams(c)
	q := h.applyScope(h.DB.Model(&model.Domain{}), middleware.CurrentUser(c))
	if v := c.Query("dnsStatus"); v != "" {
		q = q.Where("dns_status = ?", v)
	}
	if v := c.Query("expireStatus"); v != "" {
		q = q.Where("expire_status = ?", v)
	}
	if v := c.Query("registrar"); v != "" {
		q = q.Where("registrar = ?", v)
	}
	if v := c.Query("source"); v != "" {
		q = q.Where("source = ?", v)
	}
	if kw := strings.TrimSpace(c.Query("keyword")); kw != "" {
		like := "%" + strings.ToLower(kw) + "%"
		q = q.Where("name LIKE ? OR owner LIKE ? OR purpose LIKE ? OR resolved_ips LIKE ?",
			like, like, like, like)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		response.Error(c, "查询域名失败")
		return
	}
	var list []model.Domain
	if err := q.Order("id desc").Offset((page - 1) * size).Limit(size).Find(&list).Error; err != nil {
		response.Error(c, "查询域名失败")
		return
	}
	response.OKPage(c, list, total, page, size)
}

// DomainStats 概览卡片：按 DNS 状态与到期状态分别计数。
func (h *Handler) DomainStats(c *gin.Context) {
	user := middleware.CurrentUser(c)
	count := func(field, value string) int64 {
		var n int64
		h.applyScope(h.DB.Model(&model.Domain{}), user).
			Where(field+" = ?", value).Count(&n)
		return n
	}
	var total int64
	h.applyScope(h.DB.Model(&model.Domain{}), user).Count(&total)

	response.OK(c, gin.H{
		"total":        total,
		"dnsOk":        count("dns_status", "ok"),
		"dnsDrift":     count("dns_status", "drift"),
		"dnsUnresolve": count("dns_status", "unresolved") + count("dns_status", "error"),
		"dnsNoCheck":   count("dns_status", "nocheck"),
		"dnsUnknown":   count("dns_status", "unknown"),
		"expiring":     count("expire_status", "expiring"),
		"expired":      count("expire_status", "expired"),
		"noExpiry":     count("expire_status", "unknown"),
		// 这句是这一页最该被记住的一条：到期日是人填的，没填就什么都提醒不了
		"note": "注册到期日只能人工登记 —— 云解析接口是 DNS 托管视角，给不出注册到期时间。" +
			"「待补到期日」那一栏里的域名，平台对它们的续费一无所知",
	})
}

func (h *Handler) CreateDomain(c *gin.Context) {
	var req domainReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "请填写域名")
		return
	}
	if err := req.normalize(); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	registeredAt, err := parseDay(req.RegisteredAt)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	expiresAt, err := parseDay(req.ExpiresAt)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	item := model.Domain{
		Name: req.Name, Registrar: req.Registrar,
		RegisteredAt: registeredAt, ExpiresAt: expiresAt,
		Owner: req.Owner, Purpose: req.Purpose,
		ExpectIPs: req.ExpectIPs, ExpectCNAME: dnsx.NormalizeName(req.ExpectCNAME),
		ExpectNS:  req.ExpectNS,
		AlertDays: req.AlertDays, AlertEnabled: true,
		DeptID: req.DeptID, CreatedBy: middleware.CurrentUser(c).ID,
		Source: "manual", Enabled: true, Remark: req.Remark,
		DNSStatus: "unknown", ExpireStatus: "unknown",
	}
	if req.AutoRenew != nil {
		item.AutoRenew = *req.AutoRenew
	}
	if req.AlertEnabled != nil {
		item.AlertEnabled = *req.AlertEnabled
	}
	if req.Enabled != nil {
		item.Enabled = *req.Enabled
	}
	// 填了到期日就立刻算出到期状态，不等第一次巡检 ——
	// 否则刚登记完一个下周就到期的域名，列表里还显示「待补到期日」
	item.ExpireStatus, item.DaysLeft = domainExpireStatus(time.Now(), expiresAt, req.AlertDays)

	if err := h.DB.Create(&item).Error; err != nil {
		response.BadRequest(c, "创建失败，域名可能已存在")
		return
	}
	response.OK(c, item)
}

func (h *Handler) loadDomainScoped(c *gin.Context) (*model.Domain, bool) {
	var item model.Domain
	if err := h.DB.First(&item, idParam(c)).Error; err != nil {
		response.NotFound(c, "域名不存在")
		return nil, false
	}
	if !h.resourceVisible(middleware.CurrentUser(c), item.DeptID, item.CreatedBy) {
		response.NotFound(c, "域名不存在")
		return nil, false
	}
	return &item, true
}

func (h *Handler) UpdateDomain(c *gin.Context) {
	itemPtr, ok := h.loadDomainScoped(c)
	if !ok {
		return
	}

	var req domainReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "参数校验失败")
		return
	}
	if err := req.normalize(); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	registeredAt, err := parseDay(req.RegisteredAt)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	expiresAt, err := parseDay(req.ExpiresAt)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	updates := map[string]any{
		"name": req.Name, "registrar": req.Registrar,
		"registered_at": registeredAt, "expires_at": expiresAt,
		"owner": req.Owner, "purpose": req.Purpose,
		"expect_ips": req.ExpectIPs, "expect_cname": dnsx.NormalizeName(req.ExpectCNAME),
		"expect_ns": req.ExpectNS, "alert_days": req.AlertDays,
		"dept_id": req.DeptID, "remark": req.Remark,
	}
	if req.AutoRenew != nil {
		updates["auto_renew"] = *req.AutoRenew
	}
	if req.AlertEnabled != nil {
		updates["alert_enabled"] = *req.AlertEnabled
	}
	if req.Enabled != nil {
		updates["enabled"] = *req.Enabled
	}
	// 改了到期日就立刻重算到期状态，不等下一次巡检 ——
	// 否则「刚把到期日填上，列表里还显示待补」看着像没保存成功
	expireStatus, daysLeft := domainExpireStatus(time.Now(), expiresAt, req.AlertDays)
	updates["expire_status"], updates["days_left"] = expireStatus, daysLeft

	if err := h.DB.Model(&model.Domain{}).Where("id = ?", itemPtr.ID).Updates(updates).Error; err != nil {
		response.BadRequest(c, "更新失败，域名可能已存在")
		return
	}

	var updated model.Domain
	h.DB.First(&updated, itemPtr.ID)
	// 到期状态变了要同步告警（比如刚续费完，应当立刻 resolved）
	h.syncDomainAlerts(updated)
	response.OK(c, updated)
}

func (h *Handler) DeleteDomain(c *gin.Context) {
	item, ok := h.loadDomainScoped(c)
	if !ok {
		return
	}
	if err := h.DB.Delete(&model.Domain{}, item.ID).Error; err != nil {
		response.Error(c, "删除失败")
		return
	}
	// 删掉台账就把它名下的告警一并恢复，免得留一条指向已不存在对象的告警
	h.resolveDomainAlerts(*item)
	response.OK(c, nil)
}

// CheckDomain 单条巡检
func (h *Handler) CheckDomain(c *gin.Context) {
	item, ok := h.loadDomainScoped(c)
	if !ok {
		return
	}
	response.OK(c, h.checkDomain(*item))
}

// CheckAllDomains 批量巡检（只查当前用户数据范围内、启用中的域名）
func (h *Handler) CheckAllDomains(c *gin.Context) {
	var list []model.Domain
	q := h.applyScope(h.DB.Model(&model.Domain{}), middleware.CurrentUser(c)).
		Where("enabled = ?", true)
	if err := q.Find(&list).Error; err != nil {
		response.Error(c, "读取域名失败")
		return
	}
	if len(list) == 0 {
		response.BadRequest(c, "没有启用中的域名可巡检")
		return
	}
	response.OK(c, h.checkDomains(list))
}

// ImportCloudDomains 把云资源同步回来的托管域名导入台账。
//
// 这是「云资源同步」与域名台账之间唯一的自动通道，而且只带一样东西：域名本身。
// 注册商、注册与到期日**一律留空**给人补 —— 云解析接口给不出这些，
// 替它们填一个猜出来的值比留空危险得多。
func (h *Handler) ImportCloudDomains(c *gin.Context) {
	var cloudDomains []model.CloudResource
	if err := h.DB.Where("resource_type = ? AND gone = ?", "domain", false).
		Find(&cloudDomains).Error; err != nil {
		response.Error(c, "读取云上域名失败")
		return
	}
	if len(cloudDomains) == 0 {
		response.BadRequest(c, "云资源清单里没有域名，先在「云资源同步」页同步一次")
		return
	}

	var existing []model.Domain
	h.DB.Select("name").Find(&existing)
	have := make(map[string]struct{}, len(existing))
	for _, d := range existing {
		have[d.Name] = struct{}{}
	}

	user := middleware.CurrentUser(c)
	var created []string
	var skipped []string
	for _, res := range cloudDomains {
		name := dnsx.NormalizeName(res.ResourceID)
		if name == "" {
			continue
		}
		if _, ok := have[name]; ok {
			skipped = append(skipped, name)
			continue
		}
		item := model.Domain{
			Name: name, Owner: "", AlertDays: 30, AlertEnabled: true,
			Source: "cloud", CloudResourceID: res.ID,
			CreatedBy: user.ID, Enabled: true,
			DNSStatus: "unknown", ExpireStatus: "unknown",
			Remark: fmt.Sprintf("从云资源同步导入（%s）", res.Provider),
		}
		if err := h.DB.Create(&item).Error; err != nil {
			continue
		}
		have[name] = struct{}{}
		created = append(created, name)
	}

	response.OK(c, gin.H{
		"created": created,
		"skipped": skipped,
		"note": "只带进来域名本身。注册商与到期日云解析接口给不出，" +
			"需要逐个补 —— 没填到期日的域名平台对它的续费一无所知",
	})
}
