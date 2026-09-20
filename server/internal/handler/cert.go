package handler

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"ops-platform/server/internal/middleware"
	"ops-platform/server/internal/model"
	"ops-platform/server/internal/response"
)

const (
	// certProbeTimeout 单次探测（连接 + 握手）的总超时
	certProbeTimeout = 8 * time.Second
	// certCheckConcurrency 批量巡检并发上限，避免一次性打满出口连接
	certCheckConcurrency = 5
	// certAlertSourceName 巡检产生的告警挂在这个内部接入源下
	certAlertSourceName = "证书巡检"
)

// ---------- 探测 ----------

// certProbe 一次探测的结果
type certProbe struct {
	Leaf        *x509.Certificate
	Trusted     bool
	VerifyError string
}

// probeCertificate 连上目标端口读取对端证书。
//
// 这里刻意使用 InsecureSkipVerify：过期、自签名、主机名不匹配的证书同样需要被读出来
// 展示，否则握手直接失败就拿不到任何信息。链校验单独做一次，结果落在 Trusted /
// VerifyError 上，不会把「读得到」混淆成「可信」。
func probeCertificate(domain string, port int, serverName string) (*certProbe, error) {
	addr := net.JoinHostPort(domain, strconv.Itoa(port))
	conn, err := net.DialTimeout("tcp", addr, certProbeTimeout)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(certProbeTimeout)); err != nil {
		return nil, err
	}

	client := tls.Client(conn, &tls.Config{
		ServerName:         serverName,
		InsecureSkipVerify: true,
	})
	if err := client.Handshake(); err != nil {
		return nil, err
	}

	state := client.ConnectionState()
	if len(state.PeerCertificates) == 0 {
		return nil, errors.New("对端没有返回证书")
	}

	leaf := state.PeerCertificates[0]
	probe := &certProbe{Leaf: leaf}

	intermediates := x509.NewCertPool()
	for _, item := range state.PeerCertificates[1:] {
		intermediates.AddCert(item)
	}
	// Roots 留空表示使用操作系统根证书库
	if _, err := leaf.Verify(x509.VerifyOptions{
		DNSName:       serverName,
		Intermediates: intermediates,
	}); err != nil {
		probe.VerifyError = truncate(err.Error(), 240)
	} else {
		probe.Trusted = true
	}
	return probe, nil
}

// daysUntil 剩余天数，向下取整；已过期返回负数
func daysUntil(now, notAfter time.Time) int {
	return int(notAfter.Sub(now).Hours() / 24)
}

// certStatus 按到期时间与提醒阈值判定状态
func certStatus(now, notAfter time.Time, alertDays int) string {
	switch {
	case now.After(notAfter):
		return "expired"
	case daysUntil(now, notAfter) <= alertDays:
		return "expiring"
	default:
		return "valid"
	}
}

func certFingerprint(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.Raw)
	hexStr := strings.ToUpper(hex.EncodeToString(sum[:]))
	parts := make([]string, 0, len(hexStr)/2)
	for i := 0; i+1 < len(hexStr); i += 2 {
		parts = append(parts, hexStr[i:i+2])
	}
	return strings.Join(parts, ":")
}

// checkCertificate 执行一次巡检并回写结果，返回更新后的记录
func (h *Handler) checkCertificate(cert model.Certificate) model.Certificate {
	serverName := cert.ServerName
	if serverName == "" {
		serverName = cert.Domain
	}
	now := time.Now()

	probe, err := probeCertificate(cert.Domain, cert.Port, serverName)
	if err != nil {
		cert.Status = "error"
		cert.ErrorMsg = truncate(err.Error(), 240)
		cert.Trusted = false
		cert.LastCheckAt = &now
		h.DB.Model(&model.Certificate{}).Where("id = ?", cert.ID).Updates(map[string]any{
			"status": cert.Status, "error_msg": cert.ErrorMsg,
			"trusted": false, "last_check_at": &now,
		})
		h.syncCertAlerts(cert)
		return cert
	}

	leaf := probe.Leaf
	notBefore, notAfter := leaf.NotBefore, leaf.NotAfter
	daysLeft := daysUntil(now, notAfter)
	status := certStatus(now, notAfter, cert.AlertDays)

	cert.Issuer = truncate(leaf.Issuer.String(), 240)
	cert.Subject = truncate(leaf.Subject.String(), 240)
	cert.DNSNames = strings.Join(leaf.DNSNames, ",")
	cert.SerialNumber = leaf.SerialNumber.String()
	cert.Fingerprint = certFingerprint(leaf)
	cert.NotBefore, cert.NotAfter = &notBefore, &notAfter
	cert.DaysLeft, cert.Status = daysLeft, status
	cert.Trusted, cert.VerifyError = probe.Trusted, probe.VerifyError
	cert.ErrorMsg, cert.LastCheckAt = "", &now

	h.DB.Model(&model.Certificate{}).Where("id = ?", cert.ID).Updates(map[string]any{
		"issuer": cert.Issuer, "subject": cert.Subject, "dns_names": cert.DNSNames,
		"serial_number": cert.SerialNumber, "fingerprint": cert.Fingerprint,
		"not_before": cert.NotBefore, "not_after": cert.NotAfter,
		"days_left": cert.DaysLeft, "status": cert.Status,
		"trusted": cert.Trusted, "verify_error": cert.VerifyError,
		"error_msg": "", "last_check_at": &now,
	})

	h.syncCertAlerts(cert)
	return cert
}

// ---------- 告警联动 ----------

// certAlertKinds 巡检会产生的三类告警，状态恢复时逐个发恢复通知
var certAlertKinds = []string{"expiring", "expired", "error"}

// syncCertAlerts 把巡检结果送进既有告警通道：命中则告警，恢复则关闭对应告警。
//
// 指纹按「证书 + 类别」固定，所以重复巡检只会累加次数，不会刷屏；
// 证书换新后状态回到 valid，三类告警都会收到 resolved。
func (h *Handler) syncCertAlerts(cert model.Certificate) {
	if !cert.AlertEnabled {
		return
	}
	source, err := h.internalAlertSource(certAlertSourceName)
	if err != nil {
		log.Printf("[cert] 内部告警源不可用，跳过告警: %v", err)
		return
	}

	labels := map[string]string{
		"module": "certificate",
		"certId": strconv.FormatUint(uint64(cert.ID), 10),
		"domain": cert.Domain,
		"port":   strconv.Itoa(cert.Port),
	}

	fire := func(kind, title, summary, severity, value string) {
		h.ingestAlert(source, alertPayload{
			Title: title, Summary: summary, Severity: severity, Value: value,
			Fingerprint: certAlertFingerprint(cert.ID, kind), Labels: labels,
		})
	}
	resolve := func(kind string) {
		h.ingestAlert(source, alertPayload{
			Title: "证书巡检恢复", Status: "resolved",
			Fingerprint: certAlertFingerprint(cert.ID, kind), Labels: labels,
		})
	}

	switch cert.Status {
	case "expired":
		fire("expired",
			fmt.Sprintf("证书已过期：%s", cert.Domain),
			fmt.Sprintf("%s:%d 的证书已于 %s 过期，颁发者 %s", cert.Domain, cert.Port,
				cert.NotAfter.Format("2006-01-02 15:04"), cert.Issuer),
			"critical", strconv.Itoa(cert.DaysLeft))
		resolve("expiring")
		resolve("error")
	case "expiring":
		fire("expiring",
			fmt.Sprintf("证书即将到期：%s", cert.Domain),
			fmt.Sprintf("%s:%d 的证书剩余 %d 天（阈值 %d 天），到期时间 %s", cert.Domain, cert.Port,
				cert.DaysLeft, cert.AlertDays, cert.NotAfter.Format("2006-01-02 15:04")),
			"warning", strconv.Itoa(cert.DaysLeft))
		resolve("expired")
		resolve("error")
	case "error":
		fire("error",
			fmt.Sprintf("证书巡检失败：%s", cert.Domain),
			fmt.Sprintf("%s:%d 探测失败：%s", cert.Domain, cert.Port, cert.ErrorMsg),
			"warning", "")
	default:
		for _, kind := range certAlertKinds {
			resolve(kind)
		}
	}
}

func certAlertFingerprint(certID uint, kind string) string {
	return internalAlertFingerprint(fmt.Sprintf("certificate|%d|%s", certID, kind))
}

// internalAlertFingerprint 平台内部产生的告警统一用它算指纹，
// 保证同一对象重复上报只累加次数
func internalAlertFingerprint(seed string) string {
	sum := sha256.Sum256([]byte(seed))
	return hex.EncodeToString(sum[:20])
}

// internalAlertSource 按名称取出（或创建）平台内部巡检用的告警接入源。
// 它和用户自建的接入源同表，会出现在接入源列表里，token 同样可用于外部推送。
func (h *Handler) internalAlertSource(name string) (*model.AlertSource, error) {
	var source model.AlertSource
	err := h.DB.Where("name = ?", name).First(&source).Error
	if err == nil {
		return &source, nil
	}

	source = model.AlertSource{
		Name: name, Token: newAlertToken(name), Enabled: true,
		Remark: "平台内部巡检自动创建，删除后下次巡检会重新建立",
	}
	if err := h.DB.Create(&source).Error; err != nil {
		return nil, err
	}
	return &source, nil
}

// ---------- 接口 ----------

type certificateReq struct {
	Name         string `json:"name" binding:"required"`
	Domain       string `json:"domain" binding:"required"`
	Port         int    `json:"port"`
	ServerName   string `json:"serverName"`
	AlertDays    int    `json:"alertDays"`
	AlertEnabled *bool  `json:"alertEnabled"`
	DeptID       uint   `json:"deptId"`
	Enabled      *bool  `json:"enabled"`
	Remark       string `json:"remark"`
}

func (req *certificateReq) normalize() error {
	req.Domain = strings.TrimSpace(req.Domain)
	// 常见误填：把整个 URL 粘进来
	if strings.Contains(req.Domain, "://") || strings.Contains(req.Domain, "/") {
		return errors.New("域名只填主机名或 IP，不要带协议与路径")
	}
	if req.Port == 0 {
		req.Port = 443
	}
	if req.Port < 1 || req.Port > 65535 {
		return errors.New("端口需在 1-65535 之间")
	}
	if req.AlertDays <= 0 {
		req.AlertDays = 30
	}
	if req.AlertDays > 365 {
		return errors.New("提醒阈值最多 365 天")
	}
	return nil
}

func (h *Handler) ListCertificates(c *gin.Context) {
	page, size := pageParams(c)
	q := h.applyScope(h.DB.Model(&model.Certificate{}), middleware.CurrentUser(c))
	if status := c.Query("status"); status != "" {
		q = q.Where("status = ?", status)
	}
	if kw := strings.TrimSpace(c.Query("keyword")); kw != "" {
		like := "%" + kw + "%"
		q = q.Where("name LIKE ? OR domain LIKE ? OR issuer LIKE ?", like, like, like)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		response.Error(c, "查询证书失败")
		return
	}
	var list []model.Certificate
	if err := q.Order("id desc").Offset((page - 1) * size).Limit(size).Find(&list).Error; err != nil {
		response.Error(c, "查询证书失败")
		return
	}
	response.OKPage(c, list, total, page, size)
}

// CertificateStats 概览卡片，只统计当前用户可见的证书
func (h *Handler) CertificateStats(c *gin.Context) {
	user := middleware.CurrentUser(c)
	count := func(where ...any) int64 {
		var n int64
		q := h.applyScope(h.DB.Model(&model.Certificate{}), user)
		if len(where) > 0 {
			q = q.Where(where[0], where[1:]...)
		}
		q.Count(&n)
		return n
	}

	response.OK(c, gin.H{
		"total":     count(),
		"valid":     count("status = ?", "valid"),
		"expiring":  count("status = ?", "expiring"),
		"expired":   count("status = ?", "expired"),
		"error":     count("status = ?", "error"),
		"unchecked": count("last_check_at IS NULL"),
		// 只统计真正读到了证书的行：探测失败的行 trusted 恒为 false，不该混进来
		"untrusted": count("status IN (?) AND trusted = ?", []string{"valid", "expiring", "expired"}, false),
	})
}

func (h *Handler) CreateCertificate(c *gin.Context) {
	var req certificateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "名称与域名为必填项")
		return
	}
	if err := req.normalize(); err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	item := model.Certificate{
		Name: req.Name, Domain: req.Domain, Port: req.Port, ServerName: req.ServerName,
		AlertDays: req.AlertDays, AlertEnabled: true, Status: "unknown",
		DeptID: req.DeptID, Enabled: true, Remark: req.Remark,
		CreatedBy: middleware.CurrentUser(c).ID,
	}
	if req.AlertEnabled != nil {
		item.AlertEnabled = *req.AlertEnabled
	}
	if req.Enabled != nil {
		item.Enabled = *req.Enabled
	}
	if err := h.DB.Create(&item).Error; err != nil {
		response.Error(c, "创建失败")
		return
	}

	// 录入即探测一次，避免列表里长期停在「未巡检」
	response.OK(c, h.checkCertificate(item))
}

func (h *Handler) loadCertificateScoped(c *gin.Context) (*model.Certificate, bool) {
	var item model.Certificate
	if err := h.DB.First(&item, idParam(c)).Error; err != nil {
		response.NotFound(c, "证书不存在")
		return nil, false
	}
	if !h.resourceVisible(middleware.CurrentUser(c), item.DeptID, item.CreatedBy) {
		response.NotFound(c, "证书不存在")
		return nil, false
	}
	return &item, true
}

func (h *Handler) UpdateCertificate(c *gin.Context) {
	itemPtr, ok := h.loadCertificateScoped(c)
	if !ok {
		return
	}
	item := *itemPtr

	var req certificateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "参数校验失败")
		return
	}
	if err := req.normalize(); err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	item.Name, item.Domain, item.Port = req.Name, req.Domain, req.Port
	item.ServerName, item.AlertDays = req.ServerName, req.AlertDays
	item.DeptID, item.Remark = req.DeptID, req.Remark
	if req.AlertEnabled != nil {
		item.AlertEnabled = *req.AlertEnabled
	}
	if req.Enabled != nil {
		item.Enabled = *req.Enabled
	}
	if err := h.DB.Save(&item).Error; err != nil {
		response.Error(c, "更新失败")
		return
	}
	response.OK(c, item)
}

func (h *Handler) DeleteCertificate(c *gin.Context) {
	item, ok := h.loadCertificateScoped(c)
	if !ok {
		return
	}
	if err := h.DB.Delete(&model.Certificate{}, item.ID).Error; err != nil {
		response.Error(c, "删除失败")
		return
	}
	// 已产生的告警不跟着删，保留处理痕迹
	response.OK(c, nil)
}

// CheckCertificate 单条巡检
func (h *Handler) CheckCertificate(c *gin.Context) {
	item, ok := h.loadCertificateScoped(c)
	if !ok {
		return
	}
	response.OK(c, h.checkCertificate(*item))
}

// CheckAllCertificates 批量巡检当前用户可见且启用中的证书
func (h *Handler) CheckAllCertificates(c *gin.Context) {
	var list []model.Certificate
	q := h.applyScope(h.DB.Model(&model.Certificate{}), middleware.CurrentUser(c))
	if err := q.Where("enabled = ?", true).Find(&list).Error; err != nil {
		response.Error(c, "查询证书失败")
		return
	}
	if len(list) == 0 {
		response.OK(c, gin.H{"checked": 0, "detail": "没有启用中的证书"})
		return
	}

	summary := h.checkCertificates(list)
	response.OK(c, summary)
}

// checkCertificates 并发巡检一批证书并汇总结果，供接口与定时巡检共用
func (h *Handler) checkCertificates(list []model.Certificate) gin.H {
	var mu sync.Mutex
	counts := map[string]int{}

	sem := make(chan struct{}, certCheckConcurrency)
	var wg sync.WaitGroup
	for i := range list {
		wg.Add(1)
		go func(cert model.Certificate) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			result := h.checkCertificate(cert)
			mu.Lock()
			counts[result.Status]++
			mu.Unlock()
		}(list[i])
	}
	wg.Wait()

	return gin.H{
		"checked":  len(list),
		"valid":    counts["valid"],
		"expiring": counts["expiring"],
		"expired":  counts["expired"],
		"error":    counts["error"],
	}
}

// CheckAllCertificatesForSchedule 定时巡检入口：不分数据范围，处理全部启用中的证书
func (h *Handler) CheckAllCertificatesForSchedule() {
	var list []model.Certificate
	if err := h.DB.Where("enabled = ?", true).Find(&list).Error; err != nil {
		log.Printf("[cert] 定时巡检读取证书失败: %v", err)
		return
	}
	if len(list) == 0 {
		return
	}
	summary := h.checkCertificates(list)
	log.Printf("[cert] 定时巡检完成: 共 %v，正常 %v，将到期 %v，已过期 %v，失败 %v",
		summary["checked"], summary["valid"], summary["expiring"],
		summary["expired"], summary["error"])
}
