package handler

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"ops-platform/server/internal/middleware"
	"ops-platform/server/internal/model"
	"ops-platform/server/internal/response"
	"ops-platform/server/internal/sshx"
)

// 服务器配置文件管理。
//
// 照抄防火墙那套「期望态 vs 真机现读 + 收敛 + 快照回滚」，把对象从规则集换成文件内容。
// 几条不肯让步的底线：
//
//  1. 基线从真机抓，不让人从零写。从零写出来的第一版下发就会把机器打坏。
//  2. 下发前必须先把真机现状存成一版 —— 那是回滚点。没有回滚点就不许下发。
//  3. 远端也留一份备份文件（<path>.opsone.<ts>.bak）。平台挂了人还能自己在机器上拷回去。
//  4. 替换必须原子：SFTP 写临时文件 → chmod --reference 保权限 → mv -f。
//     直接往原文件写是在赌「写的过程中没人读」。
//  5. 写完必须回读校验 hash，不一致就算失败。
//  6. 改完配置要 reload 才生效 —— 「改了但没 reload」是最常见的「改了却没生效」，
//     所以把 reload 的服务登记进来，下发后顺手做掉并如实报结果。
//
// 不做：模板渲染与变量替换（那是发布系统的活）、目录级别的管理、二进制文件。

const (
	// configMaxBytes 可管文件大小上限。配置文件超过这个量级基本不是「配置」了，
	// 而且内容要整份进库、要 diff、要走接口，太大会拖垮页面
	configMaxBytes = 512 * 1024
	// configReadTimeout 单文件读取超时
	configReadTimeout = 20 * time.Second
	// configApplyTimeout 下发动作的命令超时（备份 + chmod + mv + 回读）
	configApplyTimeout = 60
	// configCheckConcurrency 巡检并发，与批量执行是独立池子
	configCheckConcurrency = 6

	configAlertSourceName = "配置巡检"
)

var configDriftLabels = map[string]string{
	"ok": "一致", "drift": "内容不一致", "missing": "文件不存在",
	"no-desired": "还没定基线", "too-large": "超出可管大小", "binary": "不是文本文件",
	"error": "读取失败", "unknown": "未巡检",
}

// ---------- 远端读写 ----------

type remoteFileContent struct {
	Content string
	Hash    string
	Size    int64
	Mode    string
	Mtime   time.Time
	// Kind ok | missing | too-large | binary
	Kind string
	Err  string
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// readRemoteConfig 通过 SFTP 读一个远端文本文件。
//
// 三种「读到了但不能管」的情况分别返回，不混成一个 error：
// 文件不存在 / 超出大小上限 / 不是文本（含 NUL 字节）。
func (h *Handler) readRemoteConfig(host *model.Host, filePath string) remoteFileContent {
	out := remoteFileContent{Kind: "error"}
	sess, err := h.openSFTP(host)
	if err != nil {
		out.Err = err.Error()
		return out
	}
	defer sess.Close()

	info, err := sess.client.Stat(filePath)
	if err != nil {
		// SFTP 的 not-exist 错误文案各实现不一，这里按关键字判定，判不出就当读失败
		msg := err.Error()
		if strings.Contains(msg, "not exist") || strings.Contains(msg, "no such file") ||
			strings.Contains(msg, "SSH_FX_NO_SUCH_FILE") {
			out.Kind, out.Err = "missing", "文件不存在"
			return out
		}
		out.Err = msg
		return out
	}
	if info.IsDir() {
		out.Kind, out.Err = "missing", "这是一个目录，不是文件"
		return out
	}
	out.Size = info.Size()
	out.Mode = fmt.Sprintf("%04o", info.Mode().Perm())
	out.Mtime = info.ModTime()
	if info.Size() > configMaxBytes {
		out.Kind = "too-large"
		out.Err = fmt.Sprintf("文件 %d 字节，超过可管上限 %d 字节", info.Size(), configMaxBytes)
		return out
	}

	f, err := sess.client.Open(filePath)
	if err != nil {
		out.Err = err.Error()
		return out
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, configMaxBytes+1))
	if err != nil {
		out.Err = err.Error()
		return out
	}
	if bytes.IndexByte(data, 0) >= 0 {
		out.Kind, out.Err = "binary", "文件含 NUL 字节，不是文本文件，平台不管二进制内容"
		return out
	}
	out.Kind, out.Content, out.Hash = "ok", string(data), sha256Hex(data)
	out.Size = int64(len(data))
	return out
}

// writeRemoteTemp 把内容写到远端临时文件，返回临时文件路径。
// 只写不替换 —— 替换那一步要走 RunOnHosts 以便过下发闸门并留痕。
//
// 优先写到目标文件同目录（同一文件系统，mv 才是原子的）；同目录写不进去
// （/etc 这种只有 root 能写的目录）就退到 /tmp，替换时由 shell 侧带 sudo 搬过去。
// 退到 /tmp 意味着跨文件系统 mv 不再原子，所以要如实告诉调用方。
func (h *Handler) writeRemoteTemp(host *model.Host, filePath, content string) (string, bool, error) {
	sess, err := h.openSFTP(host)
	if err != nil {
		return "", false, err
	}
	defer sess.Close()

	write := func(target string) error {
		f, err := sess.client.Create(target)
		if err != nil {
			return err
		}
		if _, err := f.Write([]byte(content)); err != nil {
			_ = f.Close()
			return err
		}
		return f.Close()
	}

	sameDir := filePath + ".opsone.tmp"
	if err := write(sameDir); err == nil {
		return sameDir, true, nil
	}
	fallback := "/tmp/opsone-config-" + sha256Hex([]byte(filePath))[:16] + ".tmp"
	if err := write(fallback); err != nil {
		return "", false, fmt.Errorf("临时文件写不进去（%s 与 %s 都失败）：%w", sameDir, fallback, err)
	}
	return fallback, false, nil
}

// ---------- diff ----------

// diffLineCount 算两份内容差多少行（新增 + 删除）。
// 用最朴素的「行集合差」而不是 LCS：这个数字只是给人一个「差得多不多」的量感，
// 真要看差异去拿 unified diff。
func diffLineCount(a, b string) int {
	countOf := func(s string) map[string]int {
		m := map[string]int{}
		for _, line := range strings.Split(s, "\n") {
			m[line]++
		}
		return m
	}
	ma, mb := countOf(a), countOf(b)
	diff := 0
	for line, n := range ma {
		if m := mb[line]; n > m {
			diff += n - m
		}
	}
	for line, n := range mb {
		if m := ma[line]; n > m {
			diff += n - m
		}
	}
	return diff
}

// unifiedDiff 生成一份够用的差异文本。
//
// 刻意不实现完整的 Myers diff：这里按行做最长公共子序列的简化版（等长逐行比 + 尾部增删），
// 目的是让人看清「哪几行不一样」。要做精确 patch 应该在机器上跑 diff，平台不假装自己是 git。
func unifiedDiff(oldText, newText, oldLabel, newLabel string) string {
	oldLines := strings.Split(oldText, "\n")
	newLines := strings.Split(newText, "\n")

	var b strings.Builder
	fmt.Fprintf(&b, "--- %s\n+++ %s\n", oldLabel, newLabel)

	max := len(oldLines)
	if len(newLines) > max {
		max = len(newLines)
	}
	same := 0
	for i := 0; i < max; i++ {
		var o, n string
		hasO, hasN := i < len(oldLines), i < len(newLines)
		if hasO {
			o = oldLines[i]
		}
		if hasN {
			n = newLines[i]
		}
		switch {
		case hasO && hasN && o == n:
			same++
			// 连续相同的行折叠，只在差异附近保留上下文
			if same <= 3 {
				fmt.Fprintf(&b, " %s\n", o)
			} else if same == 4 {
				b.WriteString("...\n")
			}
		default:
			same = 0
			if hasO {
				fmt.Fprintf(&b, "-%4d %s\n", i+1, o)
			}
			if hasN {
				fmt.Fprintf(&b, "+%4d %s\n", i+1, n)
			}
		}
	}
	return b.String()
}

// ---------- 巡检 ----------

func (h *Handler) desiredVersion(file model.ConfigFile) *model.ConfigVersion {
	if file.DesiredVersionID == 0 {
		return nil
	}
	var v model.ConfigVersion
	if err := h.DB.First(&v, file.DesiredVersionID).Error; err != nil {
		return nil
	}
	return &v
}

// checkConfigFile 现读真机内容并回填漂移判定
func (h *Handler) checkConfigFile(host model.Host, file *model.ConfigFile) {
	now := time.Now()
	got := h.readRemoteConfig(&host, file.Path)

	updates := map[string]any{"last_check_at": &now, "last_error": ""}
	setDrift := func(drift, detail string) {
		updates["drift"], updates["drift_detail"] = drift, truncate(detail, 250)
		file.Drift, file.DriftDetail = drift, truncate(detail, 250)
	}

	switch got.Kind {
	case "missing":
		setDrift("missing", "机器上找不到这个文件")
		updates["actual_hash"], updates["actual_size"] = "", 0
		updates["diff_lines"] = -1
	case "too-large", "binary":
		setDrift(got.Kind, got.Err)
		updates["actual_size"], updates["actual_mode"] = got.Size, got.Mode
		updates["diff_lines"] = -1
	case "ok":
		updates["actual_hash"], updates["actual_size"] = got.Hash, got.Size
		updates["actual_mode"], updates["actual_mtime"] = got.Mode, &got.Mtime
		desired := h.desiredVersion(*file)
		if desired == nil {
			setDrift("no-desired", "还没定基线，先「抓取当前内容为基线」")
			updates["diff_lines"] = -1
		} else if desired.Hash == got.Hash {
			setDrift("ok", "")
			updates["diff_lines"] = 0
		} else {
			lines := diffLineCount(desired.Content, got.Content)
			setDrift("drift", fmt.Sprintf("与基线 v%d 不一致，差 %d 行", desired.Version, lines))
			updates["diff_lines"] = lines
		}
	default:
		// 读失败：不清空上次采到的实际态，清空会让人以为文件变空了
		setDrift("error", "读取失败，实际态沿用上次结果")
		updates["last_error"] = truncate(got.Err, 250)
		file.LastError = truncate(got.Err, 250)
	}

	h.DB.Model(&model.ConfigFile{}).Where("id = ?", file.ID).Updates(updates)
	file.LastCheckAt = &now
	h.syncConfigAlerts(*file, host)
}

var configAlertKinds = []string{"drift", "missing", "error"}

func configAlertFingerprint(fileID uint, kind string) string {
	return fmt.Sprintf("config:%d:%s", fileID, kind)
}

func (h *Handler) syncConfigAlerts(file model.ConfigFile, host model.Host) {
	if !file.AlertEnabled {
		return
	}
	source, err := h.internalAlertSource(configAlertSourceName)
	if err != nil {
		log.Printf("[config] 内部告警源不可用，跳过告警: %v", err)
		return
	}
	labels := map[string]string{
		"module": "config",
		"fileId": strconv.FormatUint(uint64(file.ID), 10),
		"host":   host.Name,
		"path":   file.Path,
	}
	severity := "warning"
	if file.Critical {
		severity = "critical"
	}
	fire := func(kind, title, summary string) {
		h.ingestAlert(source, alertPayload{
			Title: title, Summary: summary, Severity: severity,
			Fingerprint: configAlertFingerprint(file.ID, kind), Labels: labels,
		})
	}
	resolve := func(kind string) {
		h.ingestAlert(source, alertPayload{
			Title: "配置恢复一致：" + file.Path, Status: "resolved",
			Fingerprint: configAlertFingerprint(file.ID, kind), Labels: labels,
		})
	}

	switch file.Drift {
	case "drift":
		fire("drift", fmt.Sprintf("配置被改动：%s@%s", file.Path, host.Name),
			fmt.Sprintf("%s 上的 %s %s。负责人 %s", host.Name, file.Path, file.DriftDetail, orDash(file.Owner)))
	case "missing":
		fire("missing", fmt.Sprintf("配置文件不存在：%s@%s", file.Path, host.Name),
			fmt.Sprintf("%s 上找不到 %s", host.Name, file.Path))
	case "error":
		fire("error", fmt.Sprintf("配置巡检失败：%s@%s", file.Path, host.Name),
			fmt.Sprintf("%s 读取 %s 失败：%s", host.Name, file.Path, file.LastError))
	default:
		for _, kind := range configAlertKinds {
			resolve(kind)
		}
		return
	}
	for _, kind := range configAlertKinds {
		if kind != file.Drift {
			resolve(kind)
		}
	}
}

// CheckConfigsForSchedule 定时巡检所有登记的配置文件
func (h *Handler) CheckConfigsForSchedule() {
	var files []model.ConfigFile
	if err := h.DB.Find(&files).Error; err != nil {
		log.Printf("[config] 巡检对象查询失败: %v", err)
		return
	}
	if len(files) == 0 {
		h.markFixedRun("config", "没有登记的配置文件")
		return
	}

	hostIDs := map[uint]bool{}
	for _, f := range files {
		hostIDs[f.HostID] = true
	}
	var hosts []model.Host
	h.DB.Where("id IN ?", keysOf(hostIDs)).Find(&hosts)
	hostByID := map[uint]model.Host{}
	for _, host := range hosts {
		hostByID[host.ID] = host
	}

	var wg sync.WaitGroup
	var mu sync.Mutex
	sem := make(chan struct{}, configCheckConcurrency)
	drifted := 0

	for i := range files {
		host, ok := hostByID[files[i].HostID]
		if !ok {
			continue
		}
		wg.Add(1)
		go func(file *model.ConfigFile, host model.Host) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			h.checkConfigFile(host, file)
			if file.Drift != "ok" {
				mu.Lock()
				drifted++
				mu.Unlock()
			}
		}(&files[i], host)
	}
	wg.Wait()

	h.markFixedRun("config", fmt.Sprintf("%d 个文件，漂移 %d", len(files), drifted))
	if drifted > 0 {
		log.Printf("[config] 巡检完成: %d 个文件，其中 %d 个漂移", len(files), drifted)
	}
}

// ---------- 接口 ----------

func configFileView(file model.ConfigFile, hostName string, desired *model.ConfigVersion) gin.H {
	view := gin.H{
		"id": file.ID, "hostId": file.HostID, "hostName": hostName,
		"path": file.Path, "name": file.Name, "category": file.Category,
		"owner": file.Owner, "critical": file.Critical,
		"reloadUnit": file.ReloadUnit, "reloadAction": file.ReloadAction,
		"alertEnabled": file.AlertEnabled, "remark": file.Remark,
		"desiredVersionId": file.DesiredVersionID, "versionSeq": file.VersionSeq,
		"actualHash": file.ActualHash, "actualSize": file.ActualSize,
		"actualMode": file.ActualMode, "actualMtime": file.ActualMtime,
		"diffLines": file.DiffLines,
		"drift":     file.Drift, "driftLabel": configDriftLabels[file.Drift],
		"driftDetail": file.DriftDetail,
		"lastCheckAt": file.LastCheckAt, "lastError": file.LastError,
		"createdAt": file.CreatedAt, "updatedAt": file.UpdatedAt,
	}
	if desired != nil {
		view["desiredVersion"] = desired.Version
		view["desiredHash"] = desired.Hash
		view["desiredSize"] = desired.Size
	} else {
		view["desiredVersion"] = 0
	}
	return view
}

func (h *Handler) ListConfigFiles(c *gin.Context) {
	page, size := pageParams(c)
	q := h.DB.Model(&model.ConfigFile{})
	if hostID := c.Query("hostId"); hostID != "" {
		q = q.Where("host_id = ?", parseUint(hostID))
	}
	if drift := c.Query("drift"); drift != "" {
		if drift == "problem" {
			q = q.Where("drift NOT IN ?", []string{"ok", "unknown"})
		} else {
			q = q.Where("drift = ?", drift)
		}
	}
	if c.Query("critical") == "1" {
		q = q.Where("critical = ?", true)
	}
	if category := c.Query("category"); category != "" {
		q = q.Where("category = ?", category)
	}
	if kw := strings.TrimSpace(c.Query("keyword")); kw != "" {
		like := "%" + kw + "%"
		q = q.Where("path LIKE ? OR name LIKE ?", like, like)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		response.Error(c, "查询配置文件失败")
		return
	}
	var list []model.ConfigFile
	if err := q.Order("critical desc, id asc").Offset((page - 1) * size).Limit(size).
		Find(&list).Error; err != nil {
		response.Error(c, "查询配置文件失败")
		return
	}

	hostIDs := make([]uint, 0, len(list))
	versionIDs := make([]uint, 0, len(list))
	for _, f := range list {
		hostIDs = append(hostIDs, f.HostID)
		if f.DesiredVersionID > 0 {
			versionIDs = append(versionIDs, f.DesiredVersionID)
		}
	}
	names := h.hostNames(hostIDs)
	desiredByID := map[uint]model.ConfigVersion{}
	if len(versionIDs) > 0 {
		var versions []model.ConfigVersion
		// 列表不需要正文，省掉大字段
		h.DB.Select("id, file_id, version, hash, size, source, created_at").
			Where("id IN ?", versionIDs).Find(&versions)
		for _, v := range versions {
			desiredByID[v.ID] = v
		}
	}

	views := make([]gin.H, 0, len(list))
	for _, f := range list {
		var desired *model.ConfigVersion
		if v, ok := desiredByID[f.DesiredVersionID]; ok {
			desired = &v
		}
		views = append(views, configFileView(f, names[f.HostID], desired))
	}
	response.OKPage(c, views, total, page, size)
}

func (h *Handler) ConfigStats(c *gin.Context) {
	count := func(where string, args ...any) int64 {
		var n int64
		q := h.DB.Model(&model.ConfigFile{})
		if where != "" {
			q = q.Where(where, args...)
		}
		q.Count(&n)
		return n
	}
	var hostCount, versionCount int64
	h.DB.Model(&model.ConfigFile{}).Distinct("host_id").Count(&hostCount)
	h.DB.Model(&model.ConfigVersion{}).Count(&versionCount)

	response.OK(c, gin.H{
		"total": count(""), "hosts": hostCount, "versions": versionCount,
		"ok":        count("drift = ?", "ok"),
		"drift":     count("drift = ?", "drift"),
		"missing":   count("drift = ?", "missing"),
		"noDesired": count("desired_version_id = ?", 0),
		"error":     count("drift = ?", "error"),
		"unknown":   count("drift = ?", "unknown"),
		"critical":  count("critical = ?", true),
		"criticalDrift": count("critical = ? AND drift NOT IN ?", true,
			[]string{"ok", "unknown"}),
		// noReload 有基线但没登记 reload 服务的：改完可能不生效，值得提醒
		"noReload": count("reload_unit = '' AND desired_version_id > 0"),
	})
}

type configFileReq struct {
	HostID       uint   `json:"hostId"`
	Path         string `json:"path"`
	Name         string `json:"name"`
	Category     string `json:"category"`
	Owner        string `json:"owner"`
	Critical     bool   `json:"critical"`
	ReloadUnit   string `json:"reloadUnit"`
	ReloadAction string `json:"reloadAction"`
	AlertEnabled *bool  `json:"alertEnabled"`
	Remark       string `json:"remark"`
	// Capture 登记时立刻抓一份真机内容当基线。默认 true ——
	// 让人从零写基线，第一次下发就会把机器打坏
	Capture *bool `json:"capture"`
}

func (r *configFileReq) normalize(h *Handler) (string, error) {
	cleaned, err := cleanRemotePath(r.Path)
	if err != nil {
		return "", err
	}
	if cleaned == "/" {
		return "", fmt.Errorf("请填写具体的文件路径")
	}
	if r.ReloadAction == "" {
		r.ReloadAction = "reload"
	}
	if r.ReloadAction != "reload" && r.ReloadAction != "restart" {
		return "", fmt.Errorf("reload 动作只能是 reload 或 restart")
	}
	if r.ReloadUnit != "" && !strings.Contains(r.ReloadUnit, ".") {
		// systemd unit 都带后缀，不带的十成是写漏了
		r.ReloadUnit += ".service"
	}
	if r.Owner != "" {
		var user model.User
		if err := h.DB.Where("username = ?", r.Owner).First(&user).Error; err != nil {
			return "", fmt.Errorf("负责人不存在: %s", r.Owner)
		}
	}
	return cleaned, nil
}

// CreateConfigFile 登记一个配置文件，默认同时抓一份真机内容当基线 v1
func (h *Handler) CreateConfigFile(c *gin.Context) {
	var req configFileReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "参数格式有误")
		return
	}
	cleaned, err := req.normalize(h)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	var host model.Host
	if err := h.DB.First(&host, req.HostID).Error; err != nil {
		response.BadRequest(c, "主机不存在")
		return
	}
	var exist model.ConfigFile
	if err := h.DB.Where("host_id = ? AND path = ?", host.ID, cleaned).First(&exist).Error; err == nil {
		response.BadRequest(c, "这台主机上的这个路径已经登记过了")
		return
	}

	user := middleware.CurrentUser(c)
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = path.Base(cleaned)
	}
	file := model.ConfigFile{
		HostID: host.ID, Path: cleaned, Name: truncate(name, 64),
		Category: truncate(strings.TrimSpace(req.Category), 32),
		Owner:    req.Owner, Critical: req.Critical,
		ReloadUnit: truncate(req.ReloadUnit, 128), ReloadAction: req.ReloadAction,
		AlertEnabled: true, Remark: truncate(req.Remark, 250),
		Drift: "unknown", DiffLines: -1, CreatedBy: user.ID,
	}
	if req.AlertEnabled != nil {
		file.AlertEnabled = *req.AlertEnabled
	}
	if err := h.DB.Create(&file).Error; err != nil {
		response.Error(c, "登记失败")
		return
	}
	// diff_lines 先置成 -1 表示「还没比对过」——这一条是 Create 里给不了的
	// （零值 0 会被读成「没有差异」）。critical / alert_enabled 一起写只是顺带
	h.DB.Model(&model.ConfigFile{}).Where("id = ?", file.ID).Updates(map[string]any{
		"critical": file.Critical, "alert_enabled": file.AlertEnabled,
		"diff_lines": -1,
	})

	body := gin.H{"id": file.ID}
	capture := true
	if req.Capture != nil {
		capture = *req.Capture
	}
	if capture {
		version, err := h.captureConfigVersion(&file, host, user.Username, "登记时抓取为基线", true)
		// 登记时的这次抓取定义了 v1，是最该留痕的一次：之后所有「一致/不一致」
		// 都是相对它说的，必须回答得出「基线是谁在什么时候从哪台机器抓的」
		record := model.ConfigApply{
			FileID: file.ID, HostID: host.ID, HostName: host.Name, Path: file.Path,
			Action: "capture", Operator: user.Username, ClientIP: c.ClientIP(),
		}
		if err != nil {
			record.Status, record.Detail = "failed", truncate("登记时抓基线失败："+err.Error(), 480)
			body["captureError"] = err.Error()
			body["note"] = "已登记，但没抓到真机内容当基线：" + err.Error()
		} else {
			record.Status, record.ToVersionID = "success", version.ID
			record.Detail = fmt.Sprintf("登记时抓取为基线 v%d（%d 字节）", version.Version, version.Size)
			body["baselineVersion"] = version.Version
			body["baselineHash"] = version.Hash
		}
		h.DB.Create(&record)
	} else {
		body["note"] = "已登记但没有基线。定基线前只会告诉你「还没定基线」，不会判漂移也不能下发"
	}
	h.checkConfigFile(host, &file)
	body["drift"] = file.Drift
	response.OK(c, body)
}

// captureConfigVersion 从真机抓一份内容存成新版本。setDesired 决定是否同时设为期望版本。
func (h *Handler) captureConfigVersion(file *model.ConfigFile, host model.Host,
	operator, note string, setDesired bool) (*model.ConfigVersion, error) {
	got := h.readRemoteConfig(&host, file.Path)
	if got.Kind != "ok" {
		return nil, fmt.Errorf("%s", orDash(got.Err))
	}

	version := model.ConfigVersion{
		FileID: file.ID, Version: file.VersionSeq + 1, Source: "captured",
		Content: got.Content, Hash: got.Hash, Size: got.Size, Mode: got.Mode,
		Note: truncate(note, 250), Operator: operator,
	}
	if err := h.DB.Create(&version).Error; err != nil {
		return nil, fmt.Errorf("存版本失败: %w", err)
	}
	updates := map[string]any{"version_seq": version.Version}
	if setDesired {
		updates["desired_version_id"] = version.ID
		file.DesiredVersionID = version.ID
	}
	h.DB.Model(&model.ConfigFile{}).Where("id = ?", file.ID).Updates(updates)
	file.VersionSeq = version.Version
	return &version, nil
}

type configFileUpdateReq struct {
	Name         string `json:"name"`
	Category     string `json:"category"`
	Owner        string `json:"owner"`
	Critical     *bool  `json:"critical"`
	ReloadUnit   string `json:"reloadUnit"`
	ReloadAction string `json:"reloadAction"`
	AlertEnabled *bool  `json:"alertEnabled"`
	Remark       string `json:"remark"`
}

func (h *Handler) UpdateConfigFile(c *gin.Context) {
	var file model.ConfigFile
	if err := h.DB.First(&file, idParam(c)).Error; err != nil {
		response.NotFound(c, "配置文件不存在")
		return
	}
	var req configFileUpdateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "参数格式有误")
		return
	}
	if req.Owner != "" {
		var user model.User
		if err := h.DB.Where("username = ?", req.Owner).First(&user).Error; err != nil {
			response.BadRequest(c, "负责人不存在: "+req.Owner)
			return
		}
	}
	action := req.ReloadAction
	if action == "" {
		action = "reload"
	}
	if action != "reload" && action != "restart" {
		response.BadRequest(c, "reload 动作只能是 reload 或 restart")
		return
	}
	unit := strings.TrimSpace(req.ReloadUnit)
	if unit != "" && !strings.Contains(unit, ".") {
		unit += ".service"
	}

	updates := map[string]any{
		"category": truncate(strings.TrimSpace(req.Category), 32),
		"owner":    req.Owner, "remark": truncate(req.Remark, 250),
		"reload_unit": truncate(unit, 128), "reload_action": action,
	}
	if req.Name != "" {
		updates["name"] = truncate(req.Name, 64)
	}
	if req.Critical != nil {
		updates["critical"] = *req.Critical
	}
	if req.AlertEnabled != nil {
		updates["alert_enabled"] = *req.AlertEnabled
	}
	if err := h.DB.Model(&model.ConfigFile{}).Where("id = ?", file.ID).
		Updates(updates).Error; err != nil {
		response.Error(c, "更新失败")
		return
	}
	response.OK(c, nil)
}

// DeleteConfigFile 取消登记。版本与下发留痕一并删掉 —— 版本只对这个文件有意义。
func (h *Handler) DeleteConfigFile(c *gin.Context) {
	var file model.ConfigFile
	if err := h.DB.First(&file, idParam(c)).Error; err != nil {
		response.NotFound(c, "配置文件不存在")
		return
	}
	var applies int64
	h.DB.Model(&model.ConfigApply{}).Where("file_id = ? AND action <> ?", file.ID, "capture").
		Count(&applies)
	if applies > 0 && c.Query("force") != "1" {
		response.BadRequest(c, fmt.Sprintf(
			"这个文件被下发过 %d 次，删掉会连历史版本一起丢，之后无法回滚；确认要删请带 force=1", applies))
		return
	}

	h.DB.Where("file_id = ?", file.ID).Delete(&model.ConfigVersion{})
	h.DB.Where("file_id = ?", file.ID).Delete(&model.ConfigApply{})
	if err := h.DB.Delete(&model.ConfigFile{}, file.ID).Error; err != nil {
		response.Error(c, "删除失败")
		return
	}
	var host model.Host
	if h.DB.First(&host, file.HostID).Error == nil {
		file.Drift = "ok"
		h.syncConfigAlerts(file, host)
	}
	response.OK(c, gin.H{"note": "已取消登记，历史版本与下发留痕一并删除"})
}

// GetConfigFile 详情：文件 + 版本列表（不含正文）+ 下发留痕
func (h *Handler) GetConfigFile(c *gin.Context) {
	var file model.ConfigFile
	if err := h.DB.First(&file, idParam(c)).Error; err != nil {
		response.NotFound(c, "配置文件不存在")
		return
	}
	var host model.Host
	h.DB.First(&host, file.HostID)

	var versions []model.ConfigVersion
	h.DB.Select("id, file_id, version, source, hash, size, mode, note, operator, created_at").
		Where("file_id = ?", file.ID).Order("version desc").Limit(50).Find(&versions)
	var applies []model.ConfigApply
	h.DB.Where("file_id = ?", file.ID).Order("id desc").Limit(50).Find(&applies)

	response.OK(c, gin.H{
		"file":     configFileView(file, host.Name, h.desiredVersion(file)),
		"versions": versions,
		"applies":  applies,
	})
}

// GetConfigVersion 取某一版的正文
func (h *Handler) GetConfigVersion(c *gin.Context) {
	var version model.ConfigVersion
	if err := h.DB.First(&version, idParam(c)).Error; err != nil {
		response.NotFound(c, "版本不存在")
		return
	}
	response.OK(c, version)
}

// CaptureConfigFile 抓一份真机现状。setDesired=1 时同时作为新基线。
func (h *Handler) CaptureConfigFile(c *gin.Context) {
	var file model.ConfigFile
	if err := h.DB.First(&file, idParam(c)).Error; err != nil {
		response.NotFound(c, "配置文件不存在")
		return
	}
	var host model.Host
	if err := h.DB.First(&host, file.HostID).Error; err != nil {
		response.BadRequest(c, "主机不存在")
		return
	}

	operator := middleware.CurrentUser(c).Username
	setDesired := c.Query("setDesired") == "1"
	note := "手动抓取"
	if setDesired {
		note = "手动抓取并设为新基线"
	}
	version, err := h.captureConfigVersion(&file, host, operator, note, setDesired)
	apply := model.ConfigApply{
		FileID: file.ID, HostID: host.ID, HostName: host.Name, Path: file.Path,
		Action: "capture", Operator: operator, ClientIP: c.ClientIP(),
	}
	if err != nil {
		apply.Status, apply.Detail = "failed", truncate(err.Error(), 480)
		h.DB.Create(&apply)
		response.BadRequest(c, "抓取失败："+err.Error())
		return
	}
	apply.Status, apply.ToVersionID = "success", version.ID
	apply.Detail = fmt.Sprintf("抓取为 v%d（%d 字节）", version.Version, version.Size)
	h.DB.Create(&apply)

	h.checkConfigFile(host, &file)
	response.OK(c, gin.H{
		"version": version.Version, "versionId": version.ID, "hash": version.Hash,
		"setDesired": setDesired, "drift": file.Drift,
	})
}

type configEditReq struct {
	Content string `json:"content"`
	Note    string `json:"note"`
	// SetDesired 是否把这一版设为期望基线，默认 true
	SetDesired *bool `json:"setDesired"`
}

// EditConfigVersion 在平台上编辑内容存成新版本（不下发）。
// 编辑与下发刻意分成两步：写完先看 diff，再决定要不要真推到机器上。
func (h *Handler) EditConfigVersion(c *gin.Context) {
	var file model.ConfigFile
	if err := h.DB.First(&file, idParam(c)).Error; err != nil {
		response.NotFound(c, "配置文件不存在")
		return
	}
	var req configEditReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "参数格式有误")
		return
	}
	if len(req.Content) > configMaxBytes {
		response.BadRequest(c, fmt.Sprintf("内容 %d 字节，超过上限 %d 字节",
			len(req.Content), configMaxBytes))
		return
	}
	if strings.IndexByte(req.Content, 0) >= 0 {
		response.BadRequest(c, "内容含 NUL 字节，平台只管文本配置")
		return
	}

	operator := middleware.CurrentUser(c).Username
	mode := file.ActualMode
	if desired := h.desiredVersion(file); desired != nil && mode == "" {
		mode = desired.Mode
	}
	version := model.ConfigVersion{
		FileID: file.ID, Version: file.VersionSeq + 1, Source: "edited",
		Content: req.Content, Hash: sha256Hex([]byte(req.Content)),
		Size: int64(len(req.Content)), Mode: mode,
		Note: truncate(req.Note, 250), Operator: operator,
	}
	if err := h.DB.Create(&version).Error; err != nil {
		response.Error(c, "存版本失败")
		return
	}
	updates := map[string]any{"version_seq": version.Version}
	setDesired := true
	if req.SetDesired != nil {
		setDesired = *req.SetDesired
	}
	if setDesired {
		updates["desired_version_id"] = version.ID
	}
	h.DB.Model(&model.ConfigFile{}).Where("id = ?", file.ID).Updates(updates)

	response.OK(c, gin.H{
		"version": version.Version, "versionId": version.ID, "hash": version.Hash,
		"setDesired": setDesired,
		"note":       "只存了版本，没有下发。确认 diff 后再点「下发」",
	})
}

// DiffConfigFile 真机现状 vs 期望基线，或指定两版之间
func (h *Handler) DiffConfigFile(c *gin.Context) {
	var file model.ConfigFile
	if err := h.DB.First(&file, idParam(c)).Error; err != nil {
		response.NotFound(c, "配置文件不存在")
		return
	}

	loadVersion := func(raw string) *model.ConfigVersion {
		if raw == "" {
			return nil
		}
		var v model.ConfigVersion
		if h.DB.Where("id = ? AND file_id = ?", parseUint(raw), file.ID).First(&v).Error != nil {
			return nil
		}
		return &v
	}

	// 两版之间比
	if left, right := loadVersion(c.Query("from")), loadVersion(c.Query("to")); left != nil && right != nil {
		response.OK(c, gin.H{
			"mode":      "version",
			"left":      gin.H{"label": fmt.Sprintf("v%d", left.Version), "hash": left.Hash},
			"right":     gin.H{"label": fmt.Sprintf("v%d", right.Version), "hash": right.Hash},
			"same":      left.Hash == right.Hash,
			"diffLines": diffLineCount(left.Content, right.Content),
			"diff": unifiedDiff(left.Content, right.Content,
				fmt.Sprintf("v%d", left.Version), fmt.Sprintf("v%d", right.Version)),
		})
		return
	}

	// 默认：期望基线 vs 真机现状（现读，不用缓存）
	desired := h.desiredVersion(file)
	if desired == nil {
		response.BadRequest(c, "还没定基线，先「抓取当前内容为基线」")
		return
	}
	var host model.Host
	if err := h.DB.First(&host, file.HostID).Error; err != nil {
		response.BadRequest(c, "主机不存在")
		return
	}
	got := h.readRemoteConfig(&host, file.Path)
	if got.Kind != "ok" {
		response.BadRequest(c, "读取真机内容失败："+orDash(got.Err))
		return
	}

	response.OK(c, gin.H{
		"mode":      "actual",
		"left":      gin.H{"label": fmt.Sprintf("基线 v%d", desired.Version), "hash": desired.Hash},
		"right":     gin.H{"label": "真机现状", "hash": got.Hash, "mode": got.Mode, "size": got.Size},
		"same":      desired.Hash == got.Hash,
		"diffLines": diffLineCount(desired.Content, got.Content),
		"diff": unifiedDiff(desired.Content, got.Content,
			fmt.Sprintf("基线 v%d", desired.Version), "真机现状 "+host.Name),
		"note": "右侧是刚刚现读的真机内容，不是缓存",
	})
}

// ---------- 下发与回滚 ----------

type configApplyReq struct {
	// VersionID 要下发的版本。为空则下发当前期望基线
	VersionID uint `json:"versionId"`
	// Confirm 关键配置必须把路径抄一遍
	Confirm string `json:"confirm"`
	// ConfirmProd 生产主机确认，交给下发闸门判定
	ConfirmProd bool `json:"confirmProd"`
	// Reload 是否下发后 reload 关联服务，默认按文件登记的来（有 unit 就 reload）
	Reload *bool `json:"reload"`
	// SetDesired 回滚时是否把目标版本设为新基线，默认 true ——
	// 否则回滚完机器和基线又不一致，下次巡检立刻报漂移
	SetDesired *bool `json:"setDesired"`
}

// ApplyConfigFile 把某一版内容推到机器上。
//
// 顺序是不可调换的：
//
//	现读真机 → 存成 pre-apply 版本（回滚点）→ SFTP 写临时文件 →
//	RunOnHosts 做「备份 + 保权限 + 原子 mv」→ 回读校验 hash → 可选 reload → 重新判漂移
//
// 中间任何一步失败都留痕，并且已经产生的 pre-apply 版本不删 —— 它是回滚依据。
func (h *Handler) ApplyConfigFile(c *gin.Context) {
	h.applyOrRollback(c, "apply")
}

// RollbackConfigFile 回滚到某个历史版本。和下发同一条路径，只是语义不同：
// 必须显式指定版本，且默认把该版设为新基线。
func (h *Handler) RollbackConfigFile(c *gin.Context) {
	h.applyOrRollback(c, "rollback")
}

func (h *Handler) applyOrRollback(c *gin.Context, action string) {
	var file model.ConfigFile
	if err := h.DB.First(&file, idParam(c)).Error; err != nil {
		response.NotFound(c, "配置文件不存在")
		return
	}
	var host model.Host
	if err := h.DB.First(&host, file.HostID).Error; err != nil {
		response.BadRequest(c, "主机不存在")
		return
	}
	var req configApplyReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "参数格式有误")
		return
	}

	operator := middleware.CurrentUser(c)
	apply := model.ConfigApply{
		FileID: file.ID, HostID: host.ID, HostName: host.Name, Path: file.Path,
		Action: action, Operator: operator.Username, ClientIP: c.ClientIP(),
		ReloadStatus: "skipped",
	}
	fail := func(status, msg string) {
		apply.Status, apply.Detail = status, truncate(msg, 480)
		h.DB.Create(&apply)
		response.BadRequest(c, msg)
	}

	// 目标版本
	var target model.ConfigVersion
	if action == "rollback" {
		if req.VersionID == 0 {
			fail("blocked", "回滚必须指定要回到哪一版")
			return
		}
	}
	if req.VersionID > 0 {
		if err := h.DB.Where("id = ? AND file_id = ?", req.VersionID, file.ID).
			First(&target).Error; err != nil {
			fail("blocked", "目标版本不存在或不属于这个文件")
			return
		}
	} else {
		desired := h.desiredVersion(file)
		if desired == nil {
			fail("blocked", "还没定基线，没有可下发的内容")
			return
		}
		target = *desired
	}
	apply.ToVersionID = target.ID

	// 关键配置要抄路径确认
	if file.Critical && strings.TrimSpace(req.Confirm) != file.Path {
		fail("blocked", fmt.Sprintf("「%s」是关键配置，%s 前请把路径抄进确认框：%s",
			file.Name, action, file.Path))
		return
	}

	// 1) 现读真机，存成回滚点
	got := h.readRemoteConfig(&host, file.Path)
	switch got.Kind {
	case "ok":
		pre := model.ConfigVersion{
			FileID: file.ID, Version: file.VersionSeq + 1, Source: "pre-apply",
			Content: got.Content, Hash: got.Hash, Size: got.Size, Mode: got.Mode,
			Note:     fmt.Sprintf("%s 前的真机现状（回滚点）", action),
			Operator: operator.Username,
		}
		if err := h.DB.Create(&pre).Error; err != nil {
			fail("failed", "存回滚点失败："+err.Error())
			return
		}
		h.DB.Model(&model.ConfigFile{}).Where("id = ?", file.ID).
			Update("version_seq", pre.Version)
		file.VersionSeq = pre.Version
		apply.FromVersionID = pre.ID
		if got.Hash == target.Hash {
			apply.Status = "success"
			apply.Detail = "真机内容与目标版本一致，没有需要改的"
			apply.VerifyHash = got.Hash
			h.DB.Create(&apply)
			h.checkConfigFile(host, &file)
			response.OK(c, gin.H{
				"status": "success", "changed": false,
				"note": "真机内容已经和目标版本一致，没有下发任何东西",
			})
			return
		}
	case "missing":
		// 文件不存在也允许下发（第一次铺配置），但要如实说明没有回滚点
		apply.Detail = "下发前真机上没有这个文件，没有回滚点"
	default:
		fail("blocked", "下发前读不到真机现状，拒绝下发（没有回滚点不动机器）："+orDash(got.Err))
		return
	}

	// 2) SFTP 写临时文件
	tmp, atomic, err := h.writeRemoteTemp(&host, file.Path, target.Content)
	if err != nil {
		fail("failed", err.Error())
		return
	}

	// 3) 备份 + 保权限 + 原子替换，走 RunOnHosts 以便过闸门并留痕。
	//
	// sudo 用不用由远端自己判断：目标文件（或它的目录）当前账号写得进去就不用 sudo。
	// 平台侧猜不准 —— /home 下的文件不需要 root，/etc 下的需要。
	stamp := time.Now().Format("20060102-150405")
	backup := fmt.Sprintf("%s.opsone.%s.bak", file.Path, stamp)
	quoted, quotedTmp, quotedBak := shellQuote(file.Path), shellQuote(tmp), shellQuote(backup)
	sudoLine := "SUDO=''"
	if h.serviceSudo() && host.Username != "root" {
		sudoLine = fmt.Sprintf(
			"SUDO=''\nif [ -e %s ] && [ ! -w %s ]; then SUDO='sudo -n'; fi\n"+
				"if [ ! -e %s ] && [ ! -w \"$(dirname %s)\" ]; then SUDO='sudo -n'; fi",
			quoted, quoted, quoted, quoted)
	}
	command := strings.Join([]string{
		"set -e",
		sudoLine,
		// 原文件存在才备份；-p 保留权限与时间戳
		fmt.Sprintf("if [ -f %s ]; then $SUDO cp -p %s %s; fi", quoted, quoted, quotedBak),
		// 权限跟随原文件；原文件不存在时用目标版本记录的 mode，再退回 0644
		fmt.Sprintf("if [ -f %s ]; then $SUDO chmod --reference=%s %s; else $SUDO chmod %s %s; fi",
			quoted, quoted, quotedTmp, orDefault(target.Mode, "0644"), quotedTmp),
		fmt.Sprintf("$SUDO mv -f %s %s", quotedTmp, quoted),
	}, "\n")

	job, err := h.RunOnHosts(c.Request.Context(), ExecRequest{
		Name:        fmt.Sprintf("配置%s: %s@%s", action, file.Path, host.Name),
		Command:     command,
		Timeout:     configApplyTimeout,
		HostIDs:     []uint{host.ID},
		UserID:      operator.ID,
		Operator:    operator.Username,
		Source:      "manual",
		ConfirmProd: req.ConfirmProd,
		ClientIP:    c.ClientIP(),
	})
	if err != nil {
		// 被闸门拦下：临时文件留在机器上会变垃圾，尽力清掉
		h.cleanupRemoteTemp(&host, tmp)
		fail("blocked", err.Error())
		return
	}
	apply.ExecJobID = job.ID
	if job.FailedNum > 0 {
		var results []model.ExecResult
		h.DB.Where("job_id = ?", job.ID).Find(&results)
		detail := ""
		if len(results) > 0 {
			detail = strings.TrimSpace(results[0].Stdout + "\n" + results[0].Stderr)
		}
		h.cleanupRemoteTemp(&host, tmp)
		fail("failed", "替换失败："+truncate(detail, 300))
		return
	}

	// 4) 回读校验
	after := h.readRemoteConfig(&host, file.Path)
	apply.VerifyHash = after.Hash
	apply.BackupPath = backup
	if after.Kind != "ok" || after.Hash != target.Hash {
		apply.Status = "failed"
		apply.Detail = fmt.Sprintf("回读校验不通过：期望 hash %s，实际 %s（%s）。"+
			"远端备份在 %s", short12(target.Hash), short12(after.Hash), orDash(after.Err), backup)
		h.DB.Create(&apply)
		h.checkConfigFile(host, &file)
		response.Error(c, apply.Detail)
		return
	}
	apply.Status = "success"

	// 落一版 applied：回读到的内容就是当前真机内容，之后巡检会拿它对照
	applied := model.ConfigVersion{
		FileID: file.ID, Version: file.VersionSeq + 1, Source: "applied",
		Content: after.Content, Hash: after.Hash, Size: after.Size, Mode: after.Mode,
		Note:     fmt.Sprintf("%s v%d 后回读", action, target.Version),
		Operator: operator.Username,
	}
	h.DB.Create(&applied)
	h.DB.Model(&model.ConfigFile{}).Where("id = ?", file.ID).
		Update("version_seq", applied.Version)
	file.VersionSeq = applied.Version

	// 回滚默认把目标版设为新基线，否则下次巡检立刻报漂移
	setDesired := action == "rollback"
	if req.SetDesired != nil {
		setDesired = *req.SetDesired
	}
	if setDesired {
		h.DB.Model(&model.ConfigFile{}).Where("id = ?", file.ID).
			Update("desired_version_id", target.ID)
		file.DesiredVersionID = target.ID
	}

	// 5) reload 关联服务：改了配置不 reload 等于没改
	doReload := file.ReloadUnit != ""
	if req.Reload != nil {
		doReload = *req.Reload && file.ReloadUnit != ""
	}
	if doReload {
		status, detail := h.reloadConfigUnit(c.Request.Context(), host, file, operator)
		apply.ReloadStatus, apply.ReloadDetail = status, truncate(detail, 250)
	} else if file.ReloadUnit == "" {
		apply.ReloadDetail = "没有登记 reload 服务，这次改动可能要人自己让它生效"
	}

	apply.Detail = fmt.Sprintf("下发 v%d 成功（%d 字节），备份 %s", target.Version, target.Size, backup)
	if !atomic {
		// 临时文件落在 /tmp，跨文件系统的 mv 是「拷贝 + 删除」，不是原子替换
		apply.Detail += "；目标目录不可写，临时文件经 /tmp 搬运，替换非原子"
	}
	h.DB.Create(&apply)
	h.checkConfigFile(host, &file)

	response.OK(c, gin.H{
		"status": "success", "changed": true,
		"version": target.Version, "verifyHash": after.Hash,
		"backupPath": backup, "execJobId": job.ID, "atomic": atomic,
		"reloadStatus": apply.ReloadStatus, "reloadDetail": apply.ReloadDetail,
		"drift": file.Drift, "setDesired": setDesired,
	})
}

// reloadConfigUnit 让配置生效。走 RunOnHosts，同样过闸门。
func (h *Handler) reloadConfigUnit(ctx context.Context, host model.Host,
	file model.ConfigFile, operator *model.User) (string, string) {
	prefix := ""
	if h.serviceSudo() && host.Username != "root" {
		prefix = "sudo -n "
	}
	verb := file.ReloadAction
	if verb == "reload" {
		// reload-or-restart：不支持 reload 的 unit 会退回 restart，
		// 比直接 reload 失败后什么都没发生要好
		verb = "reload-or-restart"
	}
	command := fmt.Sprintf("%ssystemctl %s %s", prefix, verb, file.ReloadUnit)

	job, err := h.RunOnHosts(ctx, ExecRequest{
		Name:     fmt.Sprintf("配置生效: %s %s@%s", verb, file.ReloadUnit, host.Name),
		Command:  command,
		Timeout:  configApplyTimeout,
		HostIDs:  []uint{host.ID},
		UserID:   operator.ID,
		Operator: operator.Username,
		Source:   "manual",
		// 配置已经落地了，这一步不该因为「没确认生产」而卡住；
		// 真正的风险闸门在替换那一步已经过了
		ConfirmProd: true,
	})
	if err != nil {
		return "failed", err.Error()
	}
	if job.FailedNum > 0 {
		var results []model.ExecResult
		h.DB.Where("job_id = ?", job.ID).Find(&results)
		detail := ""
		if len(results) > 0 {
			detail = strings.TrimSpace(results[0].Stdout + "\n" + results[0].Stderr)
		}
		return "failed", fmt.Sprintf("%s %s 失败：%s", verb, file.ReloadUnit, truncate(detail, 200))
	}
	return "success", fmt.Sprintf("%s %s 成功", verb, file.ReloadUnit)
}

// cleanupRemoteTemp 尽力清掉遗留的临时文件。失败只记日志：
// 这是收尾动作，不该把主流程的错误信息盖掉。也不走 RunOnHosts ——
// 这是平台自己的收尾，不该在执行历史里留一条「rm」让人以为有人删了东西。
func (h *Handler) cleanupRemoteTemp(host *model.Host, tmp string) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	res := sshx.Run(ctx, h.target(host), fmt.Sprintf("rm -f %s", shellQuote(tmp)))
	if res.Status != "success" {
		log.Printf("[config] 清理临时文件 %s 失败: %s", tmp, strings.TrimSpace(res.Stderr))
	}
}

// CheckConfigFiles 手动巡检：按主机或全部
func (h *Handler) CheckConfigFiles(c *gin.Context) {
	q := h.DB.Model(&model.ConfigFile{})
	if hostID := c.Query("hostId"); hostID != "" {
		q = q.Where("host_id = ?", parseUint(hostID))
	}
	var files []model.ConfigFile
	if err := q.Find(&files).Error; err != nil {
		response.Error(c, "查询配置文件失败")
		return
	}
	if len(files) == 0 {
		response.OK(c, gin.H{"total": 0, "drifted": 0, "note": "没有匹配的登记文件"})
		return
	}

	hostIDs := map[uint]bool{}
	for _, f := range files {
		hostIDs[f.HostID] = true
	}
	var hosts []model.Host
	h.DB.Where("id IN ?", keysOf(hostIDs)).Find(&hosts)
	hostByID := map[uint]model.Host{}
	for _, host := range hosts {
		hostByID[host.ID] = host
	}

	drifted := 0
	for i := range files {
		host, ok := hostByID[files[i].HostID]
		if !ok {
			continue
		}
		h.checkConfigFile(host, &files[i])
		if files[i].Drift != "ok" {
			drifted++
		}
	}
	response.OK(c, gin.H{"total": len(files), "drifted": drifted})
}

func (h *Handler) ListConfigApplies(c *gin.Context) {
	page, size := pageParams(c)
	q := h.DB.Model(&model.ConfigApply{})
	if fileID := c.Query("fileId"); fileID != "" {
		q = q.Where("file_id = ?", parseUint(fileID))
	}
	if hostID := c.Query("hostId"); hostID != "" {
		q = q.Where("host_id = ?", parseUint(hostID))
	}
	if action := c.Query("action"); action != "" {
		q = q.Where("action = ?", action)
	}
	if status := c.Query("status"); status != "" {
		q = q.Where("status = ?", status)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		response.Error(c, "查询留痕失败")
		return
	}
	var list []model.ConfigApply
	if err := q.Order("id desc").Offset((page - 1) * size).Limit(size).Find(&list).Error; err != nil {
		response.Error(c, "查询留痕失败")
		return
	}
	response.OKPage(c, list, total, page, size)
}

// ---------- 小工具 ----------

// shellQuote 用单引号包住路径，内部单引号按 shell 规则转义。
// 路径来自用户输入且要拼进 shell 命令，必须转义。
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func short12(hash string) string {
	if len(hash) <= 12 {
		return hash
	}
	return hash[:12]
}
