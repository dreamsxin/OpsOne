package handler

import (
	"errors"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"ops-platform/server/internal/fwx"
	"ops-platform/server/internal/middleware"
	"ops-platform/server/internal/model"
	"ops-platform/server/internal/response"
)

// 安全事件的四个来源。都是平台已经在产生的只追加流水表 ——
// 研判台不提供手工新建事件，没有真实来源的事件等于自己给自己造活干。
const (
	secSourceExecGuard = "exec-guard" // 下发闸门拦下的高危命令
	secSourceTerminal  = "terminal"   // Web 终端里被拦下的命令
	secSourceExposure  = "exposure"   // 真机 TCP 探测到的未登记开放端口
	secSourceAuthz     = "authz"      // 越权被拒的写操作
)

var secSourceLabels = map[string]string{
	secSourceExecGuard: "下发闸门",
	secSourceTerminal:  "Web 终端",
	secSourceExposure:  "暴露面扫描",
	secSourceAuthz:     "越权拦截",
}

// secSourceTables 采集游标对应的原始表，顺便当成「来源合法性」的白名单
var secSourceTables = map[string]string{
	secSourceExecGuard: "exec_guard_logs",
	secSourceTerminal:  "session_commands",
	secSourceExposure:  "exposure_scans",
	secSourceAuthz:     "audit_logs",
}

// 研判状态。new 之外全部算「人已经看过」，采集器不会把它们拉回 new。
const (
	secStatusNew           = "new"
	secStatusInvestigating = "investigating"
	secStatusConfirmed     = "confirmed"
	secStatusFalsePositive = "false-positive"
	secStatusIgnored       = "ignored"
	secStatusHandled       = "handled"
)

var secStatusLabels = map[string]string{
	secStatusNew:           "待研判",
	secStatusInvestigating: "研判中",
	secStatusConfirmed:     "确认威胁",
	secStatusFalsePositive: "误报",
	secStatusIgnored:       "忽略",
	secStatusHandled:       "已处置",
}

// secClosedStatuses 终态：结案之后再命中只累加 HitsAfterClose，不改状态
var secClosedStatuses = map[string]bool{
	secStatusFalsePositive: true,
	secStatusIgnored:       true,
	secStatusHandled:       true,
}

// secTerminalNeedsVerdict 这几个状态必须写结论才能落。
//
// 「误报 / 忽略 / 已处置」都是让这条线索从值班视野里消失的动作，
// 不写清为什么就等于把证据直接扔了。
var secTerminalNeedsVerdict = map[string]bool{
	secStatusFalsePositive: true,
	secStatusIgnored:       true,
	secStatusHandled:       true,
}

// secCollectBatch 单个来源一次最多消费多少行。
// 第一次跑会面对存量全表，不设上限的话一次采集能跑很久并且一口气造出几千条事件。
const secCollectBatch = 500

// secEvidenceMax 证据摘要上限。命令原文可能很长，但研判看的是前几行。
const secEvidenceMax = 2000

// ---------- 采集游标 ----------

// secCursorKey 每个来源记「上次消费到哪个 ID」。
//
// 用 ID 而不是时间戳当水位：这四张表都是只追加 + 自增主键，按 ID 推进不会因为
// 时钟回拨或同一秒内多行而漏采或重采。
func secCursorKey(source string) string {
	return "security.cursor_" + strings.ReplaceAll(source, "-", "_")
}

func (h *Handler) secCursor(source string) uint {
	raw := strings.TrimSpace(h.configString(secCursorKey(source), "0"))
	n, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0
	}
	return uint(n)
}

func (h *Handler) setSecCursor(source string, id uint) error {
	key := secCursorKey(source)
	value := strconv.FormatUint(uint64(id), 10)
	var exist model.SysConfig
	if err := h.DB.Where("`key` = ?", key).First(&exist).Error; err == nil {
		return h.DB.Model(&exist).Update("value", value).Error
	}
	return h.DB.Create(&model.SysConfig{
		Group: "security", Key: key, Value: value, Type: "int",
		Label:   "安全事件采集游标：" + secSourceLabels[source],
		Remark:  "采集器消费到的原始流水 ID，由程序维护；手工改小会重采，改大会漏采",
		Builtin: false,
	}).Error
}

// ---------- 候选事件 ----------

// secCandidate 采集器产出的一条候选。落库前还要过指纹去重与误报白名单。
type secCandidate struct {
	Source   string
	Title    string
	Severity string
	Actor    string
	ActorIP  string
	Target   string
	Port     string
	Protocol string
	Evidence string
	RefTable string
	RefID    uint
	SeenAt   time.Time
	// FingerKey 参与指纹的业务键（规则、端口、接口路径这类）。
	// 不用 RefID：那样每一行流水都是新事件，去重就没了。
	FingerKey string
}

// secFingerprint 指纹只认「谁 / 从哪 / 对谁 / 干了什么」，不含时间与流水 ID。
// 哈希直接复用告警那边的 internalAlertFingerprint（sha256 截断到 40 位 hex）。
func secFingerprint(c secCandidate) string {
	raw := strings.Join([]string{c.Source, c.Actor, c.ActorIP, c.Target, c.Port, c.FingerKey}, "|")
	return internalAlertFingerprint(raw)
}

// ---------- 四个采集器 ----------

// collectExecGuard 下发闸门拦下的高危命令。
//
// 只取 action = block 的行：exec_guard_logs 里 status=blocked 还包含「目标里有生产主机
// 需要二次确认」这种流程拦截，那是工作流保护不是安全事件，混进来会把研判台变成噪音场。
func (h *Handler) collectExecGuard() ([]secCandidate, uint) {
	cursor := h.secCursor(secSourceExecGuard)
	var rows []model.ExecGuardLog
	if err := h.DB.Where("id > ? AND status = ? AND action = ?", cursor, "blocked", "block").
		Order("id asc").Limit(secCollectBatch).Find(&rows).Error; err != nil {
		log.Printf("[secevent] 读下发闸门流水失败: %v", err)
		return nil, cursor
	}
	out := make([]secCandidate, 0, len(rows))
	for _, row := range rows {
		cursor = row.ID
		evidence := fmt.Sprintf("命令原文：\n%s\n\n目标主机：%s（共 %d 台，其中生产 %d 台）\n命中规则：%s\n拦截说明：%s\n下发入口：%s",
			row.Command, orDefault(row.HostNames, "（未记录）"), row.HostCount, row.ProdCount,
			orDefault(row.Pattern, "（未记录）"), row.Reason, row.Source)
		out = append(out, secCandidate{
			Source: secSourceExecGuard, Severity: "critical",
			Title:    "高危命令下发被拦：" + truncate(row.Reason, 160),
			Actor:    row.Username,
			ActorIP:  row.ClientIP,
			Target:   truncate(row.HostNames, 240),
			Protocol: "ssh",
			Evidence: truncate(evidence, secEvidenceMax),
			RefTable: secSourceTables[secSourceExecGuard], RefID: row.ID,
			SeenAt: row.CreatedAt, FingerKey: row.Pattern,
		})
	}
	return out, cursor
}

// collectTerminal Web 终端里被拦下的命令。
// 客户端 IP、目标地址、登录账号都在父表 sessions 上，所以要 join 一次。
func (h *Handler) collectTerminal() ([]secCandidate, uint) {
	cursor := h.secCursor(secSourceTerminal)
	type row struct {
		ID        uint
		Command   string
		RuleID    uint
		RuleDesc  string
		CreatedAt time.Time
		Username  string
		ClientIP  string
		HostName  string
		Address   string
		LoginUser string
	}
	var rows []row
	err := h.DB.Table("session_commands as sc").
		Select(`sc.id as id, sc.command as command, sc.rule_id as rule_id, sc.rule_desc as rule_desc,
			sc.created_at as created_at, s.username as username, s.client_ip as client_ip,
			s.host_name as host_name, s.address as address, s.login_user as login_user`).
		Joins("LEFT JOIN sessions as s ON s.id = sc.session_id").
		Where("sc.id > ? AND sc.risk = ?", cursor, "blocked").
		Order("sc.id asc").Limit(secCollectBatch).Scan(&rows).Error
	if err != nil {
		log.Printf("[secevent] 读终端命令流水失败: %v", err)
		return nil, cursor
	}
	out := make([]secCandidate, 0, len(rows))
	for _, r := range rows {
		cursor = r.ID
		target := r.HostName
		if r.Address != "" {
			target = fmt.Sprintf("%s(%s)", r.HostName, r.Address)
		}
		evidence := fmt.Sprintf("命令原文：\n%s\n\n登录账号：%s\n命中规则：%s",
			r.Command, orDefault(r.LoginUser, "（未记录）"), orDefault(r.RuleDesc, "（未记录）"))
		key := r.RuleDesc
		if key == "" {
			key = strconv.FormatUint(uint64(r.RuleID), 10)
		}
		out = append(out, secCandidate{
			Source: secSourceTerminal, Severity: "critical",
			Title:    "终端高危命令被拦：" + truncate(orDefault(r.RuleDesc, r.Command), 160),
			Actor:    r.Username,
			ActorIP:  r.ClientIP,
			Target:   truncate(target, 240),
			Protocol: "ssh",
			Evidence: truncate(evidence, secEvidenceMax),
			RefTable: secSourceTables[secSourceTerminal], RefID: r.ID,
			SeenAt: r.CreatedAt, FingerKey: key,
		})
	}
	return out, cursor
}

// portSignatureHit 一条未登记端口命中了哪些 port 类特征。
//
// 这是特征库 port 类特征第一个真正的消费方：在此之前它只能靠人点「按扫描结果核对」
// 看一眼，结果连库都不落。现在同一批判断依据会跟着安全事件一起沉下来。
type portSignatureHit struct {
	Names    []string
	Severity string // 命中特征里最高的那个级别
}

// loadPortSignatures 把启用中的 port 类特征展开成 端口 -> 特征 的索引
func (h *Handler) loadPortSignatures() map[int]portSignatureHit {
	index := map[int]portSignatureHit{}
	var sigs []model.Signature
	if err := h.DB.Where("kind = ? AND enabled = ?", sigKindPort, true).Find(&sigs).Error; err != nil {
		return index
	}
	rank := map[string]int{"low": 1, "medium": 2, "high": 3}
	for _, sig := range sigs {
		ports, err := parsePortSpec(sig.Pattern)
		if err != nil {
			continue
		}
		for _, p := range ports {
			hit := index[p]
			hit.Names = append(hit.Names, sig.Name)
			if rank[sig.Severity] > rank[hit.Severity] {
				hit.Severity = sig.Severity
			}
			index[p] = hit
		}
	}
	return index
}

// sigSeverityToEvent 特征库的 high/medium/low 换成事件级别
func sigSeverityToEvent(sev string) string {
	switch sev {
	case "high":
		return "critical"
	case "low":
		return "info"
	default:
		return "warning"
	}
}

// collectExposure 真机扫出来的未登记开放端口。
//
// 一个端口一条事件（而不是把一次扫描的端口清单塞成一条）：22 开着和 8080 开着
// 是两件要分别判断的事，合成一条就只能一起判，判完还不知道判的是哪个。
func (h *Handler) collectExposure() ([]secCandidate, uint) {
	cursor := h.secCursor(secSourceExposure)
	var scans []model.ExposureScan
	if err := h.DB.Where("id > ? AND unexpected <> ''", cursor).
		Order("id asc").Limit(secCollectBatch).Find(&scans).Error; err != nil {
		log.Printf("[secevent] 读暴露面扫描记录失败: %v", err)
		return nil, cursor
	}
	if len(scans) == 0 {
		return nil, cursor
	}

	targetIDs := make([]uint, 0, len(scans))
	for _, s := range scans {
		targetIDs = append(targetIDs, s.TargetID)
	}
	var targets []model.ExposureTarget
	h.DB.Where("id IN ?", targetIDs).Find(&targets)
	byID := map[uint]model.ExposureTarget{}
	for _, t := range targets {
		byID[t.ID] = t
	}

	sigIndex := h.loadPortSignatures()
	out := make([]secCandidate, 0, len(scans))
	for _, scan := range scans {
		cursor = scan.ID
		target := byID[scan.TargetID]
		addr := target.Address
		if addr == "" {
			// 目标已经被删了：扫描记录还在，如实标出来而不是丢掉这条证据
			addr = fmt.Sprintf("（目标 #%d 已删除）", scan.TargetID)
		}
		ports, err := parsePortSpec(scan.Unexpected)
		if err != nil {
			continue
		}
		for _, port := range ports {
			severity := "warning"
			extra := ""
			if hit, ok := sigIndex[port]; ok {
				severity = sigSeverityToEvent(hit.Severity)
				extra = "\n命中特征库：" + strings.Join(hit.Names, "、")
			}
			evidence := fmt.Sprintf("目标：%s（%s）\n本次扫描开放端口：%s\n登记基线：%s\n扫描时间：%s%s",
				orDefault(target.Name, "—"), addr,
				orDefault(scan.OpenPorts, "（无）"), orDefault(target.Baseline, "（未登记基线，任何开放端口都会提示）"),
				scan.CreatedAt.Format("2006-01-02 15:04:05"), extra)
			out = append(out, secCandidate{
				Source: secSourceExposure, Severity: severity,
				Title:    fmt.Sprintf("未登记端口对外开放：%s:%d", addr, port),
				Target:   addr,
				Port:     strconv.Itoa(port),
				Protocol: "tcp",
				Evidence: truncate(evidence, secEvidenceMax),
				RefTable: secSourceTables[secSourceExposure], RefID: scan.ID,
				SeenAt: scan.CreatedAt, FingerKey: "port",
			})
		}
	}
	return out, cursor
}

// collectAuthz 越权被拒的写操作。
//
// 审计中间件跳过 GET，所以这里只能看到写操作被拒 —— 读接口的越权尝试平台
// 现在查不到，这是已知的空白（见 ROADMAP），不在界面上假装覆盖了。
func (h *Handler) collectAuthz() ([]secCandidate, uint) {
	cursor := h.secCursor(secSourceAuthz)
	var rows []model.AuditLog
	if err := h.DB.Where("id > ? AND status = ?", cursor, 403).
		Order("id asc").Limit(secCollectBatch).Find(&rows).Error; err != nil {
		log.Printf("[secevent] 读操作审计失败: %v", err)
		return nil, cursor
	}
	out := make([]secCandidate, 0, len(rows))
	for _, row := range rows {
		cursor = row.ID
		who := row.Username
		if row.TokenID > 0 {
			who = fmt.Sprintf("%s（API 令牌 %s）", row.Username, row.TokenName)
		}
		evidence := fmt.Sprintf("请求：%s %s\n路由：%s\n发起方：%s\n来源 IP：%s",
			row.Method, row.Path, orDefault(row.Action, "—"), orDefault(who, "（未登录）"),
			orDefault(row.IP, "（未记录）"))
		out = append(out, secCandidate{
			Source: secSourceAuthz, Severity: "warning",
			Title:    fmt.Sprintf("越权访问被拒：%s %s", row.Method, truncate(row.Action, 160)),
			Actor:    row.Username,
			ActorIP:  row.IP,
			Target:   truncate(orDefault(row.Action, row.Path), 240),
			Protocol: "http",
			Evidence: truncate(evidence, secEvidenceMax),
			RefTable: secSourceTables[secSourceAuthz], RefID: row.ID,
			SeenAt: row.CreatedAt, FingerKey: row.Method + " " + row.Action,
		})
	}
	return out, cursor
}

// ---------- 落库 ----------

// secCollectStat 一次采集的结果，给界面和日志用
type secCollectStat struct {
	Created int `json:"created"`
	Updated int `json:"updated"`
	Rehit   int `json:"rehit"`
	Muted   int `json:"muted"`
}

// upsertSecCandidate 一条候选落库。
//
// 返回 outcome（created / updated / rehit / muted）与 error。error 非空时调用方
// 必须停下来并且**不要**把游标推过这一行 —— 游标只前进，推过去就等于这条线索
// 永久丢失，而流水表是只追加的，没有第二次机会。
func (h *Handler) upsertSecCandidate(cand secCandidate) (string, error) {
	fp := secFingerprint(cand)
	seen := cand.SeenAt
	if seen.IsZero() {
		seen = time.Now()
	}

	// 误报白名单优先：命中就只记「又挡掉一次」，不建事件也不复活旧事件。
	// 计数用 SQL 自增而不是读出来加一：cron 与手动采集可能同时在跑，
	// 读-改-写会丢更新，而这个数字是判断「是不是当初判错了」的唯一依据。
	var mute model.SecurityEventMute
	err := h.DB.Where("fingerprint = ?", fp).First(&mute).Error
	if err == nil {
		if err := h.DB.Model(&model.SecurityEventMute{}).Where("id = ?", mute.ID).
			Updates(map[string]any{
				"hit_count":   gorm.Expr("hit_count + 1"),
				"last_hit_at": &seen,
			}).Error; err != nil {
			return "", fmt.Errorf("更新误报白名单命中数: %w", err)
		}
		return "muted", nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		// 查库本身出错（连接断了、表锁住了）不能当成「不在白名单」继续往下走
		return "", fmt.Errorf("查询误报白名单: %w", err)
	}

	var exist model.SecurityEvent
	err = h.DB.Where("fingerprint = ?", fp).First(&exist).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		event := model.SecurityEvent{
			Fingerprint: fp, Source: cand.Source, Title: truncate(cand.Title, 250),
			Severity: normalizeSeverity(cand.Severity),
			Actor:    cand.Actor, ActorIP: cand.ActorIP,
			Target: cand.Target, Port: cand.Port, Protocol: cand.Protocol,
			Evidence: cand.Evidence, RefTable: cand.RefTable, RefID: cand.RefID,
			HitCount: 1, FirstSeenAt: seen, LastSeenAt: seen, Status: secStatusNew,
		}
		if err := h.DB.Create(&event).Error; err != nil {
			return "", fmt.Errorf("写入安全事件: %w", err)
		}
		h.appendSecLog(event.ID, "collect",
			fmt.Sprintf("由「%s」采集到（原始记录 %s#%d）", secSourceLabels[cand.Source], cand.RefTable, cand.RefID),
			"系统")
		return "created", nil
	}
	if err != nil {
		return "", fmt.Errorf("查询安全事件: %w", err)
	}

	// 已经有了：累加次数、刷新最近一次与证据（证据取最新一条，旧的在原始流水里还能查）
	updates := map[string]any{
		"hit_count":  gorm.Expr("hit_count + 1"),
		"evidence":   cand.Evidence,
		"ref_table":  cand.RefTable,
		"ref_id":     cand.RefID,
		"updated_at": time.Now(),
	}
	if seen.After(exist.LastSeenAt) {
		updates["last_seen_at"] = seen
	}
	if seen.Before(exist.FirstSeenAt) {
		updates["first_seen_at"] = seen
	}

	outcome := "updated"
	if secClosedStatuses[exist.Status] {
		// 结案了还在响：不改状态（那是人的判断），但必须让人看见
		updates["hits_after_close"] = gorm.Expr("hits_after_close + 1")
		outcome = "rehit"
	}
	if err := h.DB.Model(&model.SecurityEvent{}).Where("id = ?", exist.ID).
		Updates(updates).Error; err != nil {
		return "", fmt.Errorf("更新安全事件: %w", err)
	}
	if outcome == "rehit" {
		h.appendSecLog(exist.ID, "rehit",
			fmt.Sprintf("已结案（%s）之后又命中，累计 %d 次。判错了还是还在继续，需要人再看一眼",
				secStatusLabels[exist.Status], exist.HitsAfterClose+1), "系统")
	}
	return outcome, nil
}

// appendSecLog 追加一条处置链路记录
func (h *Handler) appendSecLog(eventID uint, action, content, operator string) {
	h.DB.Create(&model.SecurityEventLog{
		EventID: eventID, Action: action,
		Content: truncate(content, 490), Operator: operator,
	})
}

// secCollectMu 采集器的进程级串行锁。
//
// cron（OPS_SECEVENT_SPEC）与手动「立即采集」调的是同一段逻辑，而采集是
// 「读游标 → 消费 → 写游标」：两条路径同时跑会读到同一个游标、重复消费同一批流水，
// 后写的游标还可能把先写的覆盖回去。这里用 TryLock 让第二个调用直接退出而不是排队 ——
// 排队只会把同一批数据再消费一遍。
var secCollectMu sync.Mutex

// CollectSecurityEvents 走一遍四个来源。
// 第二个返回值为 false 表示「上一次采集还在跑，本次没执行」。
func (h *Handler) CollectSecurityEvents() (secCollectStat, bool) {
	if !secCollectMu.TryLock() {
		return secCollectStat{}, false
	}
	defer secCollectMu.Unlock()

	stat := secCollectStat{}
	type collector struct {
		source string
		run    func() ([]secCandidate, uint)
	}
	for _, c := range []collector{
		{secSourceExecGuard, h.collectExecGuard},
		{secSourceTerminal, h.collectTerminal},
		{secSourceExposure, h.collectExposure},
		{secSourceAuthz, h.collectAuthz},
	} {
		cands, cursor := c.run()
		for _, cand := range cands {
			outcome, err := h.upsertSecCandidate(cand)
			if err != nil {
				// 停在出错这一行之前：游标只前进，推过去这条线索就永久丢了。
				// 用 RefID-1 而不是上一条候选的 RefID —— 暴露面来源一行扫描会展开成
				// 多条候选（一个端口一条），停在同一个 RefID 会跳过剩下的端口。
				log.Printf("[secevent] 消费 %s#%d 失败，游标停在此之前等下轮重试: %v",
					cand.RefTable, cand.RefID, err)
				if cand.RefID > 0 {
					cursor = cand.RefID - 1
				}
				break
			}
			switch outcome {
			case "created":
				stat.Created++
			case "updated":
				stat.Updated++
			case "rehit":
				stat.Rehit++
			case "muted":
				stat.Muted++
			}
		}
		// 被白名单挡掉的也算消费过了，游标照推，不然下次还要再看一遍
		if err := h.setSecCursor(c.source, cursor); err != nil {
			log.Printf("[secevent] %s 游标写入失败（下轮会重采这批并重复累加命中数）: %v",
				secSourceLabels[c.source], err)
		}
	}
	if stat.Created > 0 || stat.Rehit > 0 {
		log.Printf("[secevent] 采集完成：新建 %d 条，累加 %d 条，结案后又命中 %d 条，白名单挡掉 %d 次",
			stat.Created, stat.Updated, stat.Rehit, stat.Muted)
	}
	return stat, true
}

// CollectSecurityEventsForSchedule cron 入口
func (h *Handler) CollectSecurityEventsForSchedule() {
	if _, ran := h.CollectSecurityEvents(); !ran {
		log.Println("[secevent] 上一次采集还在跑，本轮跳过")
	}
}

// RunSecurityCollect 手动采集一次
func (h *Handler) RunSecurityCollect(c *gin.Context) {
	stat, ran := h.CollectSecurityEvents()
	if !ran {
		response.BadRequest(c, "上一次采集还在跑，稍等一下再点")
		return
	}
	response.OK(c, stat)
}

// ---------- 查询 ----------

func secEventView(event model.SecurityEvent) gin.H {
	return gin.H{
		"id": event.ID, "fingerprint": event.Fingerprint,
		"source": event.Source, "sourceLabel": secSourceLabels[event.Source],
		"title": event.Title, "severity": event.Severity,
		"actor": event.Actor, "actorIp": event.ActorIP,
		"target": event.Target, "port": event.Port, "protocol": event.Protocol,
		"refTable": event.RefTable, "refId": event.RefID,
		"eventId":  event.EventID,
		"hitCount": event.HitCount, "hitsAfterClose": event.HitsAfterClose,
		"firstSeenAt": event.FirstSeenAt, "lastSeenAt": event.LastSeenAt,
		"status": event.Status, "statusLabel": secStatusLabels[event.Status],
		"verdict": event.Verdict, "owner": event.Owner,
		"closedAt": event.ClosedAt, "closedBy": event.ClosedBy,
		"createdAt": event.CreatedAt,
	}
}

func (h *Handler) ListSecurityEvents(c *gin.Context) {
	page, size := pageParams(c)
	q := h.DB.Model(&model.SecurityEvent{})
	if v := c.Query("status"); v != "" {
		if v == "open" {
			// 「待办」= 还没结案的，值班最常用的一档
			q = q.Where("status IN ?", []string{secStatusNew, secStatusInvestigating, secStatusConfirmed})
		} else {
			q = q.Where("status = ?", v)
		}
	}
	if v := c.Query("source"); v != "" {
		q = q.Where("source = ?", v)
	}
	if v := c.Query("severity"); v != "" {
		q = q.Where("severity = ?", v)
	}
	if v := strings.TrimSpace(c.Query("actorIp")); v != "" {
		q = q.Where("actor_ip = ?", v)
	}
	if v := strings.TrimSpace(c.Query("keyword")); v != "" {
		like := "%" + v + "%"
		q = q.Where("title LIKE ? OR actor LIKE ? OR target LIKE ? OR evidence LIKE ?",
			like, like, like, like)
	}
	// 结案后又命中的，值班要能一键捞出来
	if c.Query("rehit") == "1" {
		q = q.Where("hits_after_close > 0")
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		response.Error(c, "查询安全事件失败")
		return
	}
	var list []model.SecurityEvent
	// 先按「结案后又命中」排，再按最近一次命中：这两样最该先看
	if err := q.Order("hits_after_close desc, last_seen_at desc, id desc").
		Offset((page - 1) * size).Limit(size).Find(&list).Error; err != nil {
		response.Error(c, "查询安全事件失败")
		return
	}
	views := make([]gin.H, 0, len(list))
	for _, item := range list {
		views = append(views, secEventView(item))
	}
	response.OKPage(c, views, total, page, size)
}

func (h *Handler) SecurityEventStats(c *gin.Context) {
	count := func(where string, args ...any) int64 {
		var n int64
		q := h.DB.Model(&model.SecurityEvent{})
		if where != "" {
			q = q.Where(where, args...)
		}
		q.Count(&n)
		return n
	}
	var muteCount, muteHits int64
	h.DB.Model(&model.SecurityEventMute{}).Count(&muteCount)
	h.DB.Model(&model.SecurityEventMute{}).Select("COALESCE(SUM(hit_count),0)").Scan(&muteHits)

	bySource := make([]gin.H, 0, len(secSourceTables))
	sources := make([]string, 0, len(secSourceTables))
	for src := range secSourceTables {
		sources = append(sources, src)
	}
	sort.Strings(sources)
	for _, src := range sources {
		bySource = append(bySource, gin.H{
			"source": src, "label": secSourceLabels[src],
			"total":  count("source = ?", src),
			"open":   count("source = ? AND status IN ?", src, []string{secStatusNew, secStatusInvestigating, secStatusConfirmed}),
			"cursor": h.secCursor(src),
			"table":  secSourceTables[src],
		})
	}

	response.OK(c, gin.H{
		"total":         count(""),
		"new":           count("status = ?", secStatusNew),
		"investigating": count("status = ?", secStatusInvestigating),
		"confirmed":     count("status = ?", secStatusConfirmed),
		"handled":       count("status = ?", secStatusHandled),
		"falsePositive": count("status = ?", secStatusFalsePositive),
		"ignored":       count("status = ?", secStatusIgnored),
		"critical":      count("severity = ? AND status IN ?", "critical", []string{secStatusNew, secStatusInvestigating, secStatusConfirmed}),
		"rehit":         count("hits_after_close > 0"),
		"muteCount":     muteCount,
		"muteHits":      muteHits,
		"bySource":      bySource,
	})
}

// GetSecurityEvent 详情：事件本体 + 处置链路 + 原始证据 + 同源地址的其它事件
func (h *Handler) GetSecurityEvent(c *gin.Context) {
	var event model.SecurityEvent
	if err := h.DB.First(&event, idParam(c)).Error; err != nil {
		response.NotFound(c, "安全事件不存在")
		return
	}
	var logs []model.SecurityEventLog
	h.DB.Where("event_id = ?", event.ID).Order("id asc").Find(&logs)

	// 同一个源地址还干过什么 —— 研判最常问的一句
	related := make([]gin.H, 0, 8)
	if event.ActorIP != "" {
		var others []model.SecurityEvent
		h.DB.Where("actor_ip = ? AND id <> ?", event.ActorIP, event.ID).
			Order("last_seen_at desc").Limit(10).Find(&others)
		for _, o := range others {
			related = append(related, secEventView(o))
		}
	}

	var mute model.SecurityEventMute
	muted := h.DB.Where("fingerprint = ?", event.Fingerprint).First(&mute).Error == nil

	response.OK(c, gin.H{
		"event": secEventView(event), "logs": logs,
		"evidence": event.Evidence,
		"related":  related,
		"muted":    muted,
		"muteHits": mute.HitCount,
		// 口径写在接口里，页面直接显示
		"notes": []string{
			"安全事件只由采集器从平台已有的流水里生成，界面上不能手工新建 —— 没有原始记录的事件无法研判",
			"同一个「谁 / 从哪 / 对谁 / 干了什么」只留一条，重复只累加命中次数",
			"结案之后又命中不会把状态改回待研判，但会单独计数并在链路上留痕",
			"判为误报会把这条指纹加入白名单，之后采集器直接跳过，但仍然记录挡掉了多少次",
		},
	})
}

// ---------- 研判 ----------

type secTriageReq struct {
	// IDs 支持批量。研判台上「这一屏都是同一个来源的噪音」是常态，
	// 一条条点等于逼人不用。
	IDs     []uint `json:"ids"`
	Status  string `json:"status"`
	Verdict string `json:"verdict"`
	Owner   string `json:"owner"`
	// Mute 判为误报时是否同时加入白名单（默认是）
	Mute *bool `json:"mute"`
}

// TriageSecurityEvents 批量改研判状态
func (h *Handler) TriageSecurityEvents(c *gin.Context) {
	var req secTriageReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "参数格式有误")
		return
	}
	if len(req.IDs) == 0 {
		response.BadRequest(c, "请至少选择一条安全事件")
		return
	}
	if len(req.IDs) > 200 {
		response.BadRequest(c, "一次最多处理 200 条")
		return
	}
	if _, ok := secStatusLabels[req.Status]; !ok {
		response.BadRequest(c, "状态只能是 new / investigating / confirmed / false-positive / ignored / handled")
		return
	}
	verdict := strings.TrimSpace(req.Verdict)
	if secTerminalNeedsVerdict[req.Status] && verdict == "" {
		response.BadRequest(c, fmt.Sprintf("判为「%s」会让这条线索从值班视野里消失，必须写清结论",
			secStatusLabels[req.Status]))
		return
	}
	if req.Owner != "" {
		var user model.User
		if err := h.DB.Where("username = ?", req.Owner).First(&user).Error; err != nil {
			response.BadRequest(c, "研判负责人不存在: "+req.Owner)
			return
		}
	}

	// 先去重再比对存在性：详情页按钮与列表勾选可能指向同一条，
	// 直接用 len(events) != len(req.IDs) 会把「有重复」误判成「有不存在的」
	ids := make([]uint, 0, len(req.IDs))
	seen := map[uint]bool{}
	for _, id := range req.IDs {
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}

	var events []model.SecurityEvent
	if err := h.DB.Where("id IN ?", ids).Find(&events).Error; err != nil {
		response.Error(c, "查询安全事件失败")
		return
	}
	if len(events) != len(ids) {
		found := map[uint]bool{}
		for _, e := range events {
			found[e.ID] = true
		}
		missing := make([]string, 0, len(ids)-len(events))
		for _, id := range ids {
			if !found[id] {
				missing = append(missing, strconv.FormatUint(uint64(id), 10))
			}
		}
		response.BadRequest(c, "这些安全事件不存在: #"+strings.Join(missing, " #"))
		return
	}

	operator := middleware.CurrentUser(c).Username
	now := time.Now()
	mute := req.Status == secStatusFalsePositive
	if req.Mute != nil {
		mute = *req.Mute && req.Status == secStatusFalsePositive
	}

	changed, muted := 0, 0
	failed := make([]string, 0)
	for _, event := range events {
		updates := map[string]any{"status": req.Status}
		// 只在真的填了结论时才写：转「研判中」不强制填结论，
		// 无条件覆盖会把之前写好的研判结论清空
		if verdict != "" {
			updates["verdict"] = verdict
		}
		if req.Owner != "" {
			updates["owner"] = req.Owner
		}
		if secClosedStatuses[req.Status] {
			updates["closed_at"], updates["closed_by"] = &now, operator
		} else {
			// 重新打开：清掉结案信息，也把「结案后又命中」的计数归零
			// —— 它衡量的是「结案之后」，重新开着就没有这个概念了
			updates["closed_at"], updates["closed_by"] = nil, ""
			updates["hits_after_close"] = 0
		}
		if err := h.DB.Model(&model.SecurityEvent{}).Where("id = ?", event.ID).
			Updates(updates).Error; err != nil {
			log.Printf("[secevent] 更新事件 #%d 研判状态失败: %v", event.ID, err)
			failed = append(failed, strconv.FormatUint(uint64(event.ID), 10))
			continue
		}
		changed++

		content := fmt.Sprintf("研判状态 %s -> %s", secStatusLabels[event.Status], secStatusLabels[req.Status])
		if verdict != "" {
			content += "：" + verdict
		}
		h.appendSecLog(event.ID, "status", content, operator)

		if mute {
			if h.muteFingerprint(event, verdict, operator) {
				muted++
				h.appendSecLog(event.ID, "respond",
					"已加入误报白名单，采集器之后跳过这个指纹（挡掉的次数仍会记录）", operator)
			}
		}
	}
	response.OK(c, gin.H{"changed": changed, "muted": muted, "failed": failed})
}

// muteFingerprint 把一条事件的指纹加入白名单。已经在里面就不动。
func (h *Handler) muteFingerprint(event model.SecurityEvent, reason, operator string) bool {
	var exist model.SecurityEventMute
	if err := h.DB.Where("fingerprint = ?", event.Fingerprint).First(&exist).Error; err == nil {
		return false
	}
	err := h.DB.Create(&model.SecurityEventMute{
		Fingerprint: event.Fingerprint, Source: event.Source,
		Title: truncate(event.Title, 250), Reason: reason, Operator: operator,
	}).Error
	if err != nil {
		log.Printf("[secevent] 写入误报白名单失败: %v", err)
		return false
	}
	return true
}

type secNoteReq struct {
	Content string `json:"content" binding:"required"`
}

// AddSecurityEventNote 追加一条处置记录
func (h *Handler) AddSecurityEventNote(c *gin.Context) {
	var event model.SecurityEvent
	if err := h.DB.First(&event, idParam(c)).Error; err != nil {
		response.NotFound(c, "安全事件不存在")
		return
	}
	var req secNoteReq
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.Content) == "" {
		response.BadRequest(c, "处置记录内容不能为空")
		return
	}
	h.appendSecLog(event.ID, "note", req.Content, middleware.CurrentUser(c).Username)
	response.OK(c, nil)
}

// ---------- 响应处置：生成封禁规则草稿 ----------

type secBlockReq struct {
	// HostID 把封禁规则挂到哪台机器；GroupID 非 0 时挂到安全组
	HostID  uint `json:"hostId"`
	GroupID uint `json:"groupId"`
	// Port 要封的端口。留空时按事件自己的端口推，推不出来会报错要求明确填 ——
	// 底层规则模型（iptables / ufw / firewalld 三家共有的那部分表达）要求 tcp 规则
	// 必须带端口，做不出「封这个地址的所有端口」这一条，所以这里不能替人瞎猜。
	Port string `json:"port"`
}

// BlockSecurityEventSource 从安全事件生成一条「丢弃该源地址」的防火墙规则草稿。
//
// 刻意只登记不下发：下发要走防火墙自己的 firewall:apply + 下发闸门（命令规则复检、
// 生产主机二次确认、拦截留痕）。研判台如果能直接改真机规则，就等于绕过了那套闸门 ——
// 一个误判就能把自己的运维通道封掉。
func (h *Handler) BlockSecurityEventSource(c *gin.Context) {
	var event model.SecurityEvent
	if err := h.DB.First(&event, idParam(c)).Error; err != nil {
		response.NotFound(c, "安全事件不存在")
		return
	}
	if strings.TrimSpace(event.ActorIP) == "" {
		response.BadRequest(c, "这条事件没有源地址，生不出封禁规则（暴露面与部分定时任务来源本来就没有来源 IP）")
		return
	}

	var req secBlockReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "参数格式有误")
		return
	}
	if req.GroupID == 0 && req.HostID == 0 {
		response.BadRequest(c, "请选择要挂到哪台主机或哪个安全组")
		return
	}
	if req.GroupID == 0 {
		if _, ok := h.loadHostForFirewall(c, req.HostID); !ok {
			return
		}
	}

	// 端口：优先用人填的，其次用事件自己的，再其次按协议推。推不出来就明说，
	// 不替人猜 —— 猜错了会封掉一条谁都没想封的通道。
	port := strings.TrimSpace(req.Port)
	if port == "" {
		port = strings.TrimSpace(event.Port)
	}
	if port == "" && event.Protocol == "ssh" {
		port = "22"
	}
	if port == "" {
		response.BadRequest(c, "请指定要封的端口。底层规则模型（iptables / ufw / firewalld 三家共有的那部分表达）"+
			"要求 tcp 规则必须带端口，做不出「封这个地址的所有端口」这一条")
		return
	}

	ruleReq := firewallRuleReq{
		HostID: req.HostID, GroupID: req.GroupID,
		Direction: fwx.DirIn, Action: "drop", Protocol: "tcp",
		Source: strings.TrimSpace(event.ActorIP), Port: port,
		Description: truncate(fmt.Sprintf("安全事件 #%d 封禁：%s", event.ID, event.Title), 250),
		Lifecycle:   "temporary",
		// 临时规则给 7 天：封错了会自己过期，比永久规则安全。
		// 时间格式跟防火墙页面提交的一致（不是 RFC3339），否则 normalize 会拒。
		ExpiresAt: time.Now().Add(7 * 24 * time.Hour).Format("2006-01-02 15:04:05"),
	}
	spec, expires, err := ruleReq.normalize()
	if err != nil {
		response.BadRequest(c, "生成规则失败："+err.Error())
		return
	}

	operator := middleware.CurrentUser(c).Username
	rule := model.FirewallRule{
		HostID: ruleReq.HostID, GroupID: ruleReq.GroupID,
		Direction: spec.Direction, Action: spec.Action, Protocol: spec.Protocol,
		Source: spec.Source, Port: spec.Port, Service: spec.Service,
		RuleKey: specKey(spec), Description: ruleReq.Description, Owner: operator,
		Lifecycle: ruleReq.Lifecycle, ExpiresAt: expires,
		Origin: "platform", State: "pending", Enabled: true,
		CreatedBy: operator,
	}
	if err := h.DB.Create(&rule).Error; err != nil {
		response.Error(c, "规则登记失败")
		return
	}

	h.appendSecLog(event.ID, "respond",
		fmt.Sprintf("已生成封禁规则草稿 #%d：丢弃来自 %s 的 %s 端口流量（临时规则，7 天后到期）。**还没下发到真机**，要生效请到「安全合规 → 防火墙策略」确认并下发",
			rule.ID, spec.Source, spec.Port), operator)

	response.OK(c, gin.H{
		"rule": rule,
		"message": fmt.Sprintf("已登记规则草稿 #%d，尚未下发。到「防火墙策略」核对后再下发，下发仍走命令规则与生产确认",
			rule.ID),
	})
}

// ---------- 误报白名单 ----------

func (h *Handler) ListSecurityMutes(c *gin.Context) {
	page, size := pageParams(c)
	q := h.DB.Model(&model.SecurityEventMute{})
	if v := c.Query("source"); v != "" {
		q = q.Where("source = ?", v)
	}
	var total int64
	q.Count(&total)
	var list []model.SecurityEventMute
	// 挡得最多的排前面：挡得异常多通常说明当初判错了
	if err := q.Order("hit_count desc, id desc").
		Offset((page - 1) * size).Limit(size).Find(&list).Error; err != nil {
		response.Error(c, "查询误报白名单失败")
		return
	}
	views := make([]gin.H, 0, len(list))
	for _, item := range list {
		views = append(views, gin.H{
			"id": item.ID, "fingerprint": item.Fingerprint,
			"source": item.Source, "sourceLabel": secSourceLabels[item.Source],
			"title": item.Title, "reason": item.Reason,
			"hitCount": item.HitCount, "lastHitAt": item.LastHitAt,
			"operator": item.Operator, "createdAt": item.CreatedAt,
		})
	}
	response.OKPage(c, views, total, page, size)
}

// DeleteSecurityMute 撤销白名单。之后同样的事再发生会重新建事件。
func (h *Handler) DeleteSecurityMute(c *gin.Context) {
	var mute model.SecurityEventMute
	if err := h.DB.First(&mute, idParam(c)).Error; err != nil {
		response.NotFound(c, "白名单条目不存在")
		return
	}
	if err := h.DB.Delete(&mute).Error; err != nil {
		response.Error(c, "撤销失败")
		return
	}
	// 对应的事件如果还在，把它拉回待研判：白名单撤了就说明当初的误报判断不成立
	var event model.SecurityEvent
	if err := h.DB.Where("fingerprint = ?", mute.Fingerprint).First(&event).Error; err == nil {
		if err := h.DB.Model(&model.SecurityEvent{}).Where("id = ?", event.ID).
			Updates(map[string]any{
				"status": secStatusNew, "closed_at": nil, "closed_by": "", "hits_after_close": 0,
			}).Error; err != nil {
			// 白名单已经删了但事件没拉回来，是个半成品状态，必须让人知道
			response.Error(c, fmt.Sprintf("白名单已撤销，但事件 #%d 没能拉回待研判，请手工改一下", event.ID))
			return
		}
		h.appendSecLog(event.ID, "status",
			fmt.Sprintf("误报白名单被撤销（期间挡掉 %d 次），重新回到待研判", mute.HitCount),
			middleware.CurrentUser(c).Username)
	}
	response.OK(c, gin.H{"revoked": mute.Fingerprint, "blockedHits": mute.HitCount})
}
