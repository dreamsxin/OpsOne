package handler

import (
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"ops-platform/server/internal/middleware"
	"ops-platform/server/internal/model"
	"ops-platform/server/internal/response"
)

// 处置剧本。
//
// 平台里已经有「告警 → 事件 → 处置 → 复盘 → 改进项」这条链，剧本补的是最后一环反哺：
// 复盘里沉淀下来的「下次这么干」要能在下一次同类告警上被推到眼前，否则经验只存在
// 写过复盘的那个人脑子里。
//
// 三条设计底线：
//  1. 剧本必须能被自动找到 —— 有匹配条件，并且把「为什么推荐它」说清楚，不给不可解释的分数；
//  2. 剧本里的命令必须过命令规则预检 —— 命中拦截规则的剧本不许启用，避免把危险命令
//     以「照着做就行」的姿态摆到值班人面前；真下发时批量执行那层闸门还会再查一次；
//  3. 用过要留结果 —— 解决了 / 部分有效 / 没用。用得多但从不解决问题的剧本要能被看见。

var runbookOutcomeLabels = map[string]string{
	"resolved": "解决了", "partial": "部分有效", "invalid": "没用",
}

// runbookStep 一个处置步骤。command 可为空（纯人工动作，比如「联系业务确认能否重启」）
type runbookStep struct {
	Title   string `json:"title"`
	Detail  string `json:"detail"`
	Command string `json:"command"`
}

func parseRunbookSteps(raw string) []runbookStep {
	steps := []runbookStep{}
	if strings.TrimSpace(raw) != "" {
		_ = json.Unmarshal([]byte(raw), &steps)
	}
	return steps
}

func parseLabelMap(raw string) map[string]string {
	out := map[string]string{}
	if strings.TrimSpace(raw) != "" {
		_ = json.Unmarshal([]byte(raw), &out)
	}
	return out
}

func splitKeywords(raw string) []string {
	items := make([]string, 0, 4)
	for _, part := range strings.Split(raw, ",") {
		if part = strings.TrimSpace(part); part != "" {
			items = append(items, part)
		}
	}
	return items
}

func runbookView(book model.Runbook) gin.H {
	steps := parseRunbookSteps(book.Steps)
	withCommand := 0
	for _, step := range steps {
		if strings.TrimSpace(step.Command) != "" {
			withCommand++
		}
	}
	return gin.H{
		"id": book.ID, "name": book.Name, "category": book.Category, "summary": book.Summary,
		"matchLabels": parseLabelMap(book.MatchLabels), "matchKeywords": book.MatchKeywords,
		"matchSeverity": book.MatchSeverity,
		"steps":         steps, "stepCount": len(steps), "commandSteps": withCommand,
		"precheck": book.Precheck, "rollback": book.Rollback,
		"riskLevel": book.RiskLevel, "enabled": book.Enabled,
		"precheckStatus": book.PrecheckStatus, "precheckHits": book.PrecheckHits,
		"precheckedAt": book.PrecheckedAt,
		"version":      book.Version, "useCount": book.UseCount, "solveCount": book.SolveCount,
		"lastUsedAt": book.LastUsedAt, "lastUsedBy": book.LastUsedBy,
		"creatorName": book.CreatorName, "createdAt": book.CreatedAt, "updatedAt": book.UpdatedAt,
		// generic 通用剧本：没有任何匹配条件，只会作为兜底出现在推荐列表末尾
		"generic": book.MatchLabels == "" && book.MatchKeywords == "" && book.MatchSeverity == "",
	}
}

// ---------- 预检 ----------

// runbookCommands 把所有步骤里的命令拼成多行文本，交给命令规则逐行扫。
// 行号对得上第几步，命中详情里能指回具体步骤。
func runbookCommands(steps []runbookStep) string {
	lines := make([]string, 0, len(steps))
	for _, step := range steps {
		lines = append(lines, strings.ReplaceAll(strings.TrimSpace(step.Command), "\r\n", "\n"))
	}
	return strings.Join(lines, "\n")
}

func (h *Handler) applyRunbookPrecheck(book *model.Runbook) []precheckHit {
	status, hits := h.precheckScript(runbookCommands(parseRunbookSteps(book.Steps)))
	raw, _ := json.Marshal(hits)
	now := time.Now()
	book.PrecheckStatus, book.PrecheckHits, book.PrecheckedAt = status, string(raw), &now
	return hits
}

// ---------- 匹配 ----------

type runbookMatch struct {
	Runbook gin.H    `json:"runbook"`
	Score   int      `json:"score"`
	Reasons []string `json:"reasons"`
}

// matchRunbook 判断一本剧本是否适用于给定的标签/标题/级别，并给出分数与理由。
//
// 规则刻意简单到能一眼看懂：标签命中 +3、关键词命中 +2、级别命中 +1；
// 标签条件要求全部命中（写了两个标签就是「两个都得对上」），命中不全直接不推荐。
func matchRunbook(book model.Runbook, labels map[string]string, title, severity string) (int, []string, bool) {
	score := 0
	reasons := make([]string, 0, 3)

	want := parseLabelMap(book.MatchLabels)
	for key, value := range want {
		got, ok := labels[key]
		if !ok {
			return 0, nil, false
		}
		if value != "*" && !strings.EqualFold(strings.TrimSpace(value), strings.TrimSpace(got)) {
			return 0, nil, false
		}
		score += 3
		if value == "*" {
			reasons = append(reasons, fmt.Sprintf("标签 %s 存在", key))
		} else {
			reasons = append(reasons, fmt.Sprintf("标签 %s=%s 命中", key, got))
		}
	}

	if keywords := splitKeywords(book.MatchKeywords); len(keywords) > 0 {
		hit := ""
		for _, word := range keywords {
			if strings.Contains(strings.ToLower(title), strings.ToLower(word)) {
				hit = word
				break
			}
		}
		if hit == "" {
			return 0, nil, false
		}
		score += 2
		reasons = append(reasons, "标题含关键词「"+hit+"」")
	}

	if book.MatchSeverity != "" {
		if book.MatchSeverity != severity {
			return 0, nil, false
		}
		score += 1
		reasons = append(reasons, "级别 "+severity+" 命中")
	}

	if len(reasons) == 0 {
		reasons = append(reasons, "通用剧本（没有设置匹配条件，作为兜底）")
	}
	return score, reasons, true
}

// matchRunbooksFor 给一批候选标签/标题/级别找剧本，按分数从高到低排。
// 只看启用中的剧本：停用的剧本不会被推荐，但历史使用记录还在。
func (h *Handler) matchRunbooksFor(labels map[string]string, title, severity string) []runbookMatch {
	var books []model.Runbook
	h.DB.Where("enabled = ?", true).Order("id asc").Find(&books)

	matches := make([]runbookMatch, 0, 4)
	for _, book := range books {
		score, reasons, ok := matchRunbook(book, labels, title, severity)
		if !ok {
			continue
		}
		matches = append(matches, runbookMatch{Runbook: runbookView(book), Score: score, Reasons: reasons})
	}
	sort.SliceStable(matches, func(i, j int) bool { return matches[i].Score > matches[j].Score })
	return matches
}

// eventMatchInput 把事件的关联告警合成一组匹配输入：
// 标签取并集（多条告警的标签合起来看），标题用所有告警标题 + 事件标题拼起来，级别取事件级别。
func (h *Handler) eventMatchInput(event model.Event, alerts []model.Alert) (map[string]string, string, string) {
	labels := map[string]string{}
	titles := []string{event.Title}
	for _, alert := range alerts {
		for key, value := range parseLabelMap(alert.Labels) {
			if _, exist := labels[key]; !exist {
				labels[key] = value
			}
		}
		titles = append(titles, alert.Title)
	}
	return labels, strings.Join(titles, " "), event.Severity
}

// MatchRunbooks 给告警或事件推荐剧本。eventId 与 alertId 二选一。
func (h *Handler) MatchRunbooks(c *gin.Context) {
	eventID := uint(parseUint(c.Query("eventId")))
	alertID := uint(parseUint(c.Query("alertId")))

	var labels map[string]string
	var title, severity, target string

	switch {
	case eventID > 0:
		var event model.Event
		if err := h.DB.First(&event, eventID).Error; err != nil {
			response.NotFound(c, "事件不存在")
			return
		}
		var alerts []model.Alert
		if ids := eventAlertIDs(event); len(ids) > 0 {
			h.DB.Where("id IN ?", ids).Find(&alerts)
		}
		labels, title, severity = h.eventMatchInput(event, alerts)
		target = fmt.Sprintf("事件 #%d（含 %d 条关联告警的标签）", event.ID, len(alerts))
	case alertID > 0:
		var alert model.Alert
		if err := h.DB.First(&alert, alertID).Error; err != nil {
			response.NotFound(c, "告警不存在")
			return
		}
		labels, title, severity = parseLabelMap(alert.Labels), alert.Title, alert.Severity
		target = fmt.Sprintf("告警 #%d", alert.ID)
	default:
		response.BadRequest(c, "请指定 eventId 或 alertId")
		return
	}

	matches := h.matchRunbooksFor(labels, title, severity)
	response.OK(c, gin.H{
		"target":  target,
		"input":   gin.H{"labels": labels, "title": title, "severity": severity},
		"matches": matches,
		"note":    "标签条件要求全部命中；标签 +3 / 关键词 +2 / 级别 +1，没有条件的通用剧本排最后",
	})
}

// ---------- CRUD ----------

type runbookReq struct {
	Name          string            `json:"name"`
	Category      string            `json:"category"`
	Summary       string            `json:"summary"`
	MatchLabels   map[string]string `json:"matchLabels"`
	MatchKeywords string            `json:"matchKeywords"`
	MatchSeverity string            `json:"matchSeverity"`
	Steps         []runbookStep     `json:"steps"`
	Precheck      string            `json:"precheck"`
	Rollback      string            `json:"rollback"`
	RiskLevel     string            `json:"riskLevel"`
	Enabled       *bool             `json:"enabled"`
}

func (r *runbookReq) normalize() error {
	r.Name = strings.TrimSpace(r.Name)
	if r.Name == "" {
		return fmt.Errorf("剧本名称不能为空")
	}
	if len(r.Steps) == 0 {
		return fmt.Errorf("至少写一个处置步骤，否则这本剧本对值班的人没有意义")
	}
	if len(r.Steps) > 30 {
		return fmt.Errorf("一本剧本最多 30 步，再长就该拆成几本")
	}
	for idx := range r.Steps {
		r.Steps[idx].Title = strings.TrimSpace(r.Steps[idx].Title)
		r.Steps[idx].Command = strings.TrimSpace(r.Steps[idx].Command)
		if r.Steps[idx].Title == "" {
			return fmt.Errorf("第 %d 步没有写标题", idx+1)
		}
		if strings.Contains(r.Steps[idx].Command, "\n") {
			return fmt.Errorf("第 %d 步的命令包含换行；一步一条命令，多条请拆成多步（预检要按步定位）", idx+1)
		}
	}
	if r.RiskLevel == "" {
		r.RiskLevel = "low"
	}
	if r.RiskLevel != "low" && r.RiskLevel != "medium" && r.RiskLevel != "high" {
		return fmt.Errorf("风险等级只能是 low / medium / high")
	}
	if r.MatchSeverity != "" {
		if _, ok := map[string]bool{"critical": true, "warning": true, "info": true}[r.MatchSeverity]; !ok {
			return fmt.Errorf("匹配级别只能是 critical / warning / info")
		}
	}
	for key := range r.MatchLabels {
		if strings.TrimSpace(key) == "" {
			return fmt.Errorf("匹配标签的键不能为空")
		}
	}
	return nil
}

func (r *runbookReq) fill(book *model.Runbook) {
	labels := ""
	if len(r.MatchLabels) > 0 {
		raw, _ := json.Marshal(r.MatchLabels)
		labels = string(raw)
	}
	steps, _ := json.Marshal(r.Steps)

	book.Name = truncate(r.Name, 128)
	book.Category = truncate(strings.TrimSpace(r.Category), 32)
	book.Summary = truncate(r.Summary, 500)
	book.MatchLabels = labels
	book.MatchKeywords = truncate(strings.TrimSpace(r.MatchKeywords), 255)
	book.MatchSeverity = r.MatchSeverity
	book.Steps = string(steps)
	book.Precheck = r.Precheck
	book.Rollback = r.Rollback
	book.RiskLevel = r.RiskLevel
}

func (h *Handler) ListRunbooks(c *gin.Context) {
	page, size := pageParams(c)
	q := h.DB.Model(&model.Runbook{})
	if category := c.Query("category"); category != "" {
		q = q.Where("category = ?", category)
	}
	if status := c.Query("precheckStatus"); status != "" {
		q = q.Where("precheck_status = ?", status)
	}
	if enabled := c.Query("enabled"); enabled != "" {
		q = q.Where("enabled = ?", enabled == "1" || enabled == "true")
	}
	if kw := strings.TrimSpace(c.Query("keyword")); kw != "" {
		like := "%" + kw + "%"
		q = q.Where("name LIKE ? OR summary LIKE ? OR match_keywords LIKE ?", like, like, like)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		response.Error(c, "查询剧本失败")
		return
	}
	var list []model.Runbook
	if err := q.Order("id desc").Offset((page - 1) * size).Limit(size).Find(&list).Error; err != nil {
		response.Error(c, "查询剧本失败")
		return
	}
	views := make([]gin.H, 0, len(list))
	for _, book := range list {
		views = append(views, runbookView(book))
	}
	response.OKPage(c, views, total, page, size)
}

func (h *Handler) RunbookStats(c *gin.Context) {
	count := func(where string, args ...any) int64 {
		var n int64
		q := h.DB.Model(&model.Runbook{})
		if where != "" {
			q = q.Where(where, args...)
		}
		q.Count(&n)
		return n
	}
	var uses, solved int64
	h.DB.Model(&model.RunbookUse{}).Count(&uses)
	h.DB.Model(&model.RunbookUse{}).Where("outcome = ?", "resolved").Count(&solved)

	response.OK(c, gin.H{
		"total":   count(""),
		"enabled": count("enabled = ?", true),
		"blocked": count("precheck_status = ?", "blocked"),
		"warn":    count("precheck_status = ?", "warn"),
		// generic 通用剧本：没有匹配条件，只能兜底，多了等于推荐失效
		"generic": count("match_labels = '' AND match_keywords = '' AND match_severity = ''"),
		// neverUsed 建了但从来没人用过的
		"neverUsed": count("use_count = 0"),
		"uses":      uses, "solved": solved,
	})
}

func (h *Handler) GetRunbook(c *gin.Context) {
	var book model.Runbook
	if err := h.DB.First(&book, idParam(c)).Error; err != nil {
		response.NotFound(c, "剧本不存在")
		return
	}
	var uses []model.RunbookUse
	h.DB.Where("runbook_id = ?", book.ID).Order("id desc").Limit(50).Find(&uses)
	response.OK(c, gin.H{"runbook": runbookView(book), "uses": uses})
}

func (h *Handler) CreateRunbook(c *gin.Context) {
	var req runbookReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "参数格式有误")
		return
	}
	if err := req.normalize(); err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	user := middleware.CurrentUser(c)
	book := model.Runbook{Version: 1, CreatorName: user.Username, CreatedBy: user.ID}
	req.fill(&book)
	book.Enabled = true
	if req.Enabled != nil {
		book.Enabled = *req.Enabled
	}
	hits := h.applyRunbookPrecheck(&book)
	if book.PrecheckStatus == "blocked" && book.Enabled {
		response.BadRequest(c, "步骤里的命令命中了拦截规则："+blockedRunbookReason(hits)+
			"；请改掉命令，或先存为停用状态")
		return
	}

	wantEnabled := book.Enabled
	if err := h.DB.Create(&book).Error; err != nil {
		response.Error(c, "创建失败")
		return
	}
	// 带 default 的布尔列在 Create 后会被回填成库默认值，显式写回
	book.Enabled = wantEnabled
	h.DB.Model(&model.Runbook{}).Where("id = ?", book.ID).Update("enabled", wantEnabled)

	response.OK(c, gin.H{"runbook": runbookView(book), "precheckHits": hits})
}

func (h *Handler) UpdateRunbook(c *gin.Context) {
	var book model.Runbook
	if err := h.DB.First(&book, idParam(c)).Error; err != nil {
		response.NotFound(c, "剧本不存在")
		return
	}
	var req runbookReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "参数格式有误")
		return
	}
	if err := req.normalize(); err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	before := book.Steps + "|" + book.Precheck + "|" + book.Rollback
	req.fill(&book)
	if req.Enabled != nil {
		book.Enabled = *req.Enabled
	}
	// 处置内容变了才升版本：改个分类不该让历史使用记录的版本号对不上
	if before != book.Steps+"|"+book.Precheck+"|"+book.Rollback {
		book.Version++
	}

	hits := h.applyRunbookPrecheck(&book)
	if book.PrecheckStatus == "blocked" && book.Enabled {
		response.BadRequest(c, "步骤里的命令命中了拦截规则："+blockedRunbookReason(hits)+
			"；请改掉命令，或先改成停用状态")
		return
	}

	if err := h.DB.Save(&book).Error; err != nil {
		response.Error(c, "更新失败")
		return
	}
	h.DB.Model(&model.Runbook{}).Where("id = ?", book.ID).Update("enabled", book.Enabled)
	response.OK(c, gin.H{"runbook": runbookView(book), "precheckHits": hits})
}

func blockedRunbookReason(hits []precheckHit) string {
	if hit := blockingHit(hits); hit != nil {
		return fmt.Sprintf("第 %d 步 %s（规则 %s）", hit.Line, hit.Snippet, hit.Pattern)
	}
	return "命中拦截规则"
}

func (h *Handler) DeleteRunbook(c *gin.Context) {
	var book model.Runbook
	if err := h.DB.First(&book, idParam(c)).Error; err != nil {
		response.NotFound(c, "剧本不存在")
		return
	}
	var uses int64
	h.DB.Model(&model.RunbookUse{}).Where("runbook_id = ?", book.ID).Count(&uses)
	if uses > 0 {
		response.BadRequest(c, fmt.Sprintf(
			"这本剧本已经被用过 %d 次，删掉会让那些事件的处置记录指向空；请改成停用", uses))
		return
	}
	if err := h.DB.Delete(&model.Runbook{}, book.ID).Error; err != nil {
		response.Error(c, "删除失败")
		return
	}
	response.OK(c, nil)
}

// RecheckRunbooks 命令规则改了之后，把所有剧本重新预检一遍。
// 规则是会变的：今天 pass 的剧本明天可能就命中新加的拦截规则。
func (h *Handler) RecheckRunbooks(c *gin.Context) {
	var books []model.Runbook
	if err := h.DB.Find(&books).Error; err != nil {
		response.Error(c, "查询剧本失败")
		return
	}
	changed, nowBlocked := 0, make([]string, 0, 2)
	for i := range books {
		before := books[i].PrecheckStatus
		h.applyRunbookPrecheck(&books[i])
		if books[i].PrecheckStatus != before {
			changed++
		}
		// 已启用的剧本变成 blocked，自动停用并如实报出来：
		// 留着一本「照着做就会被拦」的剧本比停用更糟
		if books[i].PrecheckStatus == "blocked" && books[i].Enabled {
			books[i].Enabled = false
			nowBlocked = append(nowBlocked, books[i].Name)
		}
		h.DB.Model(&model.Runbook{}).Where("id = ?", books[i].ID).Updates(map[string]any{
			"precheck_status": books[i].PrecheckStatus,
			"precheck_hits":   books[i].PrecheckHits,
			"prechecked_at":   books[i].PrecheckedAt,
			"enabled":         books[i].Enabled,
		})
	}
	response.OK(c, gin.H{
		"total": len(books), "changed": changed,
		"disabled": nowBlocked,
		"note":     "命中拦截规则且原本启用的剧本已被自动停用",
	})
}

// PrecheckPendingRunbooks 启动时给还没预检过的剧本补一次预检。
//
// 内置剧本是在 db 层种下的，那一层拿不到 Handler，所以状态是 unknown。
// 一本「命令从没被规则查过」的剧本摆在值班人面前是不负责任的：命令规则是每个部署自己
// 维护的，内置剧本在别人的规则下完全可能是 blocked。所以进程起来就补上。
func (h *Handler) PrecheckPendingRunbooks() {
	var books []model.Runbook
	if err := h.DB.Where("precheck_status = ? OR precheck_status = ''", "unknown").
		Find(&books).Error; err != nil {
		log.Printf("[runbook] 待预检剧本查询失败: %v", err)
		return
	}
	if len(books) == 0 {
		return
	}
	blocked := make([]string, 0, 2)
	for i := range books {
		h.applyRunbookPrecheck(&books[i])
		if books[i].PrecheckStatus == "blocked" && books[i].Enabled {
			books[i].Enabled = false
			blocked = append(blocked, books[i].Name)
		}
		h.DB.Model(&model.Runbook{}).Where("id = ?", books[i].ID).Updates(map[string]any{
			"precheck_status": books[i].PrecheckStatus,
			"precheck_hits":   books[i].PrecheckHits,
			"prechecked_at":   books[i].PrecheckedAt,
			"enabled":         books[i].Enabled,
		})
	}
	log.Printf("[runbook] 已补预检 %d 本剧本", len(books))
	if len(blocked) > 0 {
		log.Printf("[runbook] 以下剧本的命令命中本地拦截规则，已自动停用: %s", strings.Join(blocked, "、"))
	}
}

// ---------- 使用记录 ----------

type runbookUseReq struct {
	EventID   uint   `json:"eventId"`
	AlertID   uint   `json:"alertId"`
	Outcome   string `json:"outcome"`
	DoneSteps []int  `json:"doneSteps"`
	Note      string `json:"note"`
}

// UseRunbook 记一次剧本使用。挂在事件上时会往事件时间线追加一条，
// 这样复盘的时候能看见「当时按哪本剧本、做到哪一步、有没有效」。
func (h *Handler) UseRunbook(c *gin.Context) {
	var book model.Runbook
	if err := h.DB.First(&book, idParam(c)).Error; err != nil {
		response.NotFound(c, "剧本不存在")
		return
	}
	var req runbookUseReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "参数格式有误")
		return
	}
	if _, ok := runbookOutcomeLabels[req.Outcome]; !ok {
		response.BadRequest(c, "处置结果只能是 resolved / partial / invalid")
		return
	}

	var event model.Event
	hasEvent := false
	if req.EventID > 0 {
		if err := h.DB.First(&event, req.EventID).Error; err != nil {
			response.BadRequest(c, "关联事件不存在")
			return
		}
		hasEvent = true
	}
	if req.AlertID > 0 {
		var alert model.Alert
		if err := h.DB.First(&alert, req.AlertID).Error; err != nil {
			response.BadRequest(c, "关联告警不存在")
			return
		}
	}

	steps := parseRunbookSteps(book.Steps)
	done := make([]int, 0, len(req.DoneSteps))
	for _, idx := range req.DoneSteps {
		if idx >= 0 && idx < len(steps) {
			done = append(done, idx)
		}
	}
	doneRaw, _ := json.Marshal(done)

	operator := middleware.CurrentUser(c).Username
	now := time.Now()
	use := model.RunbookUse{
		RunbookID: book.ID, RunbookName: book.Name, Version: book.Version,
		EventID: req.EventID, AlertID: req.AlertID,
		Outcome: req.Outcome, DoneSteps: string(doneRaw),
		Note: truncate(req.Note, 500), Operator: operator,
	}
	if err := h.DB.Create(&use).Error; err != nil {
		response.Error(c, "记录失败")
		return
	}

	updates := map[string]any{
		"use_count": book.UseCount + 1, "last_used_at": &now, "last_used_by": operator,
	}
	if req.Outcome == "resolved" {
		updates["solve_count"] = book.SolveCount + 1
	}
	h.DB.Model(&model.Runbook{}).Where("id = ?", book.ID).Updates(updates)

	if hasEvent {
		content := fmt.Sprintf("按剧本「%s」(v%d) 处置，做了 %d/%d 步，结果：%s",
			book.Name, book.Version, len(done), len(steps), runbookOutcomeLabels[req.Outcome])
		if req.Note != "" {
			content += "；" + truncate(req.Note, 200)
		}
		h.appendEventLog(event.ID, "runbook", content, operator)
	}
	response.OK(c, gin.H{"id": use.ID})
}

// ListRunbookUses 使用记录查询。带 eventId 时用于在事件详情里显示「这次用过哪些剧本」
func (h *Handler) ListRunbookUses(c *gin.Context) {
	page, size := pageParams(c)
	q := h.DB.Model(&model.RunbookUse{})
	if eventID := c.Query("eventId"); eventID != "" {
		q = q.Where("event_id = ?", parseUint(eventID))
	}
	if runbookID := c.Query("runbookId"); runbookID != "" {
		q = q.Where("runbook_id = ?", parseUint(runbookID))
	}
	if outcome := c.Query("outcome"); outcome != "" {
		q = q.Where("outcome = ?", outcome)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		response.Error(c, "查询使用记录失败")
		return
	}
	var list []model.RunbookUse
	if err := q.Order("id desc").Offset((page - 1) * size).Limit(size).Find(&list).Error; err != nil {
		response.Error(c, "查询使用记录失败")
		return
	}
	views := make([]gin.H, 0, len(list))
	for _, use := range list {
		steps := []int{}
		_ = json.Unmarshal([]byte(use.DoneSteps), &steps)
		views = append(views, gin.H{
			"id": use.ID, "runbookId": use.RunbookID, "runbookName": use.RunbookName,
			"version": use.Version, "eventId": use.EventID, "alertId": use.AlertID,
			"outcome": use.Outcome, "outcomeLabel": runbookOutcomeLabels[use.Outcome],
			"doneSteps": steps, "note": use.Note, "operator": use.Operator,
			"createdAt": use.CreatedAt,
		})
	}
	response.OKPage(c, views, total, page, size)
}

// ---------- 从复盘沉淀剧本 ----------

type runbookFromReviewReq struct {
	EventID uint   `json:"eventId" binding:"required"`
	Name    string `json:"name"`
}

// DraftRunbookFromReview 拿事件的复盘内容起一个剧本草稿（不落库，只返回预填内容）。
//
// 刻意不直接创建：复盘里写的是「这次怎么处理的」，剧本要的是「下次遇到同类怎么处理」，
// 中间需要人做一次抽象。平台只负责把材料摆好（止血过程 → 步骤、根因 → 适用说明、
// 告警标签 → 匹配条件），不代替人做这次抽象。
func (h *Handler) DraftRunbookFromReview(c *gin.Context) {
	var req runbookFromReviewReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "请指定事件")
		return
	}
	var event model.Event
	if err := h.DB.First(&event, req.EventID).Error; err != nil {
		response.NotFound(c, "事件不存在")
		return
	}
	var review model.EventReview
	if err := h.DB.Where("event_id = ?", event.ID).First(&review).Error; err != nil {
		response.NotFound(c, "该事件还没有复盘，没有可沉淀的内容")
		return
	}

	var alerts []model.Alert
	if ids := eventAlertIDs(event); len(ids) > 0 {
		h.DB.Where("id IN ?", ids).Find(&alerts)
	}
	labels, _, _ := h.eventMatchInput(event, alerts)
	// 只把能作为分类依据的标签作为匹配条件候选，host/address 这种实例级标签不该进剧本
	suggest := map[string]string{}
	for key, value := range labels {
		switch key {
		case "host", "address", "target", "domain", "instance", "probeId", "certId":
			continue
		default:
			suggest[key] = value
		}
	}

	steps := []runbookStep{}
	for _, line := range strings.Split(review.Mitigation, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			steps = append(steps, runbookStep{Title: truncate(line, 120)})
		}
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = truncate(event.Title, 100) + " 处置剧本"
	}
	response.OK(c, gin.H{
		"draft": gin.H{
			"name":          name,
			"summary":       "来自事件 #" + fmt.Sprint(event.ID) + " 的复盘。根因：" + truncate(review.RootCause, 300),
			"matchLabels":   suggest,
			"matchSeverity": event.Severity,
			"steps":         steps,
			"precheck":      review.DetectGap,
			"rollback":      "",
			"riskLevel":     "medium",
		},
		"note": "步骤是把复盘的「止血过程」按行拆出来的草稿，需要你改成「下次怎么做」；" +
			"host / address / instance 这类实例级标签已从匹配条件里剔除（那是这一次的机器，不是这类问题）",
	})
}
