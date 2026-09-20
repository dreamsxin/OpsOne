package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"text/template"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"ops-platform/server/internal/middleware"
	"ops-platform/server/internal/model"
	"ops-platform/server/internal/response"
)

// Agent：读平台自己的数据 → 拼上下文 → 交给模型 → 存下结论。
//
// 刻意不做的事：**不执行任何命令、不改任何东西**。在一个能连生产机器的平台里
// 放一个会自己动手的 Agent，收益远不及风险；让它只负责「看数据、给结论」，
// 人看完结论再决定动不动手，这条边界写进了 docs/SECURITY.md。
//
// 上下文都来自平台已有的表，不新增采集：
//   alert           最近的告警（可按级别筛）
//   host_metric     某台主机最近的采样
//   exec_job        某个执行作业的逐台结果（失败的优先）
//   session_command 某次会话敲过的命令
//   none            只用运行时填的说明

const (
	// agentContextMaxChars 上下文字符上限。提示词太长不仅贵，还会让模型忽略中间内容
	agentContextMaxChars = 24000
	// agentPromptMaxChars 渲染后的完整提示词上限
	agentPromptMaxChars = 32000
	// agentOutputMaxChars 结论存库的上限，防止某次异常输出把库撑爆
	agentOutputMaxChars = 20000
	// agentRunTimeout 单次运行的总超时：拼上下文 + 调模型
	agentRunTimeout = 5 * time.Minute
)

// agentDataSources 允许的上下文来源，以及是否需要指定目标对象
var agentDataSources = map[string]struct {
	label       string
	needsTarget bool
}{
	"none":            {"不取平台数据", false},
	"alert":           {"最近告警", false},
	"host_metric":     {"主机指标", true},
	"exec_job":        {"执行作业结果", true},
	"session_command": {"会话命令", true},
}

// ---------- 配置维护 ----------

type agentConfigReq struct {
	Name           string   `json:"name"`
	Alias          string   `json:"alias"`
	DataSource     string   `json:"dataSource"`
	MaxItems       int      `json:"maxItems"`
	SystemPrompt   string   `json:"systemPrompt"`
	PromptTemplate string   `json:"promptTemplate"`
	Temperature    *float64 `json:"temperature"`
	MaxTokens      int      `json:"maxTokens"`
	Enabled        *bool    `json:"enabled"`
	Remark         string   `json:"remark"`
}

func (r *agentConfigReq) normalize() error {
	r.Name = strings.TrimSpace(r.Name)
	r.Alias = strings.TrimSpace(r.Alias)
	r.DataSource = strings.ToLower(strings.TrimSpace(r.DataSource))
	r.SystemPrompt = strings.TrimSpace(r.SystemPrompt)
	r.PromptTemplate = strings.TrimSpace(r.PromptTemplate)

	if r.Name == "" {
		return fmt.Errorf("名称不能为空")
	}
	if r.Alias == "" {
		return fmt.Errorf("请选择模型（资源池里的 Alias）")
	}
	if r.DataSource == "" {
		r.DataSource = "none"
	}
	if _, ok := agentDataSources[r.DataSource]; !ok {
		return fmt.Errorf("数据来源 %s 不支持", r.DataSource)
	}
	if r.PromptTemplate == "" {
		return fmt.Errorf("提示词模板不能为空")
	}
	// 模板语法当场校验，别等到运行时才炸
	if _, err := template.New("agent").Parse(r.PromptTemplate); err != nil {
		return fmt.Errorf("提示词模板语法有问题: %v", err)
	}
	if r.DataSource != "none" && !strings.Contains(r.PromptTemplate, "{{.context}}") {
		// 取了数据却没往提示词里放，是最容易犯的错：模型什么也看不到
		return fmt.Errorf("选了数据来源，模板里要用 {{.context}} 把数据放进去")
	}
	if r.MaxItems == 0 {
		r.MaxItems = 20
	}
	if r.MaxItems < 1 || r.MaxItems > 200 {
		return fmt.Errorf("上下文条数需在 1 到 200 之间")
	}
	if r.MaxTokens == 0 {
		r.MaxTokens = 800
	}
	if r.MaxTokens < 32 || r.MaxTokens > 8192 {
		return fmt.Errorf("回复长度上限需在 32 到 8192 之间")
	}
	if r.Temperature != nil && (*r.Temperature < 0 || *r.Temperature > 2) {
		return fmt.Errorf("temperature 需在 0 到 2 之间")
	}
	return nil
}

// requireAlias Alias 必须在资源池里真有可用上游，否则存下来也是一跑就失败
func (h *Handler) requireAlias(alias string) error {
	var count int64
	if err := h.DB.Model(&model.ModelUpstream{}).
		Where("alias = ? AND enabled = ?", alias, true).Count(&count).Error; err != nil {
		return fmt.Errorf("校验模型失败")
	}
	if count == 0 {
		return fmt.Errorf("模型 %s 在资源池里没有启用的上游", alias)
	}
	return nil
}

func (h *Handler) ListAgentConfigs(c *gin.Context) {
	var list []model.AgentConfig
	q := h.DB.Order("id desc")
	if source := strings.TrimSpace(c.Query("dataSource")); source != "" {
		q = q.Where("data_source = ?", source)
	}
	if err := q.Find(&list).Error; err != nil {
		response.Error(c, "查询 Agent 失败")
		return
	}
	sources := make([]gin.H, 0, len(agentDataSources))
	for key, meta := range agentDataSources {
		sources = append(sources, gin.H{
			"key": key, "label": meta.label, "needsTarget": meta.needsTarget,
		})
	}
	response.OK(c, gin.H{"list": list, "dataSources": sources})
}

func (h *Handler) CreateAgentConfig(c *gin.Context) {
	var req agentConfigReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "参数错误")
		return
	}
	if err := req.normalize(); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	if err := h.requireAlias(req.Alias); err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	agent := model.AgentConfig{
		Name: req.Name, Alias: req.Alias, DataSource: req.DataSource,
		MaxItems: req.MaxItems, SystemPrompt: req.SystemPrompt,
		PromptTemplate: req.PromptTemplate, MaxTokens: req.MaxTokens,
		Remark: req.Remark, CreatedBy: middleware.CurrentUser(c).ID,
	}
	if req.Temperature != nil {
		agent.Temperature = *req.Temperature
	}
	wantEnabled := true
	if req.Enabled != nil {
		wantEnabled = *req.Enabled
	}
	agent.Enabled = wantEnabled

	if err := h.DB.Create(&agent).Error; err != nil {
		response.Error(c, "创建失败")
		return
	}
	// enabled 带 gorm default，Create 后会被回填成 true，显式关掉的要写回去
	if !wantEnabled {
		h.DB.Model(&model.AgentConfig{}).Where("id = ?", agent.ID).
			Updates(map[string]any{"enabled": false})
		agent.Enabled = false
	}
	response.OK(c, agent)
}

func (h *Handler) UpdateAgentConfig(c *gin.Context) {
	id := idParam(c)
	var agent model.AgentConfig
	if err := h.DB.First(&agent, id).Error; err != nil {
		response.NotFound(c, "Agent 不存在")
		return
	}
	var req agentConfigReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "参数错误")
		return
	}
	if err := req.normalize(); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	if err := h.requireAlias(req.Alias); err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	updates := map[string]any{
		"name": req.Name, "alias": req.Alias, "data_source": req.DataSource,
		"max_items": req.MaxItems, "system_prompt": req.SystemPrompt,
		"prompt_template": req.PromptTemplate, "max_tokens": req.MaxTokens,
		"remark": req.Remark,
	}
	if req.Temperature != nil {
		updates["temperature"] = *req.Temperature
	}
	if req.Enabled != nil {
		updates["enabled"] = *req.Enabled
	}
	if err := h.DB.Model(&model.AgentConfig{}).Where("id = ?", agent.ID).
		Updates(updates).Error; err != nil {
		response.Error(c, "更新失败")
		return
	}
	h.DB.First(&agent, agent.ID)
	response.OK(c, agent)
}

func (h *Handler) DeleteAgentConfig(c *gin.Context) {
	id := idParam(c)
	if err := h.DB.Delete(&model.AgentConfig{}, id).Error; err != nil {
		response.Error(c, "删除失败")
		return
	}
	// 运行记录不跟着删：结论要能回看，哪怕 Agent 本身已经不用了
	response.OK(c, nil)
}

// ---------- 上下文拼装 ----------

// agentContext 一次运行的上下文
type agentContext struct {
	Text      string
	Items     int
	Truncated bool
	// Summary 一句人话，说明这次到底喂了什么进去
	Summary string
}

// buildAgentContext 按数据来源从平台自己的表里取数据。
//
// 全部是只读查询，且都带 limit —— Agent 的上下文是给模型看的，不是导数据。
func (h *Handler) buildAgentContext(agent *model.AgentConfig, targetID uint,
	severity string) (*agentContext, error) {
	limit := agent.MaxItems
	if limit <= 0 {
		limit = 20
	}

	switch agent.DataSource {
	case "none":
		return &agentContext{Summary: "未取平台数据"}, nil

	case "alert":
		q := h.DB.Model(&model.Alert{}).Where("status <> ?", "resolved")
		if severity != "" {
			q = q.Where("severity = ?", severity)
		}
		var alerts []model.Alert
		if err := q.Order("last_seen_at desc").Limit(limit + 1).Find(&alerts).Error; err != nil {
			return nil, fmt.Errorf("读取告警失败")
		}
		truncated := len(alerts) > limit
		if truncated {
			alerts = alerts[:limit]
		}
		var buf strings.Builder
		buf.WriteString("当前未恢复的告警（最近在前）:\n")
		for i, item := range alerts {
			labels := compactLabels(item.Labels)
			buf.WriteString(fmt.Sprintf("%d. [%s][%s] %s | %s | 累计%d次 | 最近 %s",
				i+1, item.Severity, item.Status, item.Title,
				truncate(item.Summary, 200), item.Count,
				item.LastSeenAt.Format("01-02 15:04")))
			if item.Value != "" {
				buf.WriteString(" | 值 " + item.Value)
			}
			if labels != "" {
				buf.WriteString(" | 标签 " + labels)
			}
			buf.WriteString("\n")
		}
		return &agentContext{
			Text: buf.String(), Items: len(alerts), Truncated: truncated,
			Summary: fmt.Sprintf("%d 条未恢复告警", len(alerts)),
		}, nil

	case "host_metric":
		var host model.Host
		if err := h.DB.First(&host, targetID).Error; err != nil {
			return nil, fmt.Errorf("主机不存在")
		}
		var metrics []model.HostMetric
		if err := h.DB.Where("host_id = ?", targetID).
			Order("id desc").Limit(limit + 1).Find(&metrics).Error; err != nil {
			return nil, fmt.Errorf("读取主机指标失败")
		}
		truncated := len(metrics) > limit
		if truncated {
			metrics = metrics[:limit]
		}
		if len(metrics) == 0 {
			return nil, fmt.Errorf("主机 %s 还没有采集到指标", host.Name)
		}
		var buf strings.Builder
		buf.WriteString(fmt.Sprintf("主机 %s（%s）最近的采样，新的在前：\n",
			host.Name, host.Address))
		buf.WriteString("时间 | CPU% | 内存% | 磁盘最高% | load1/5/15 | 进程 | TCP | 状态\n")
		for _, m := range metrics {
			line := fmt.Sprintf("%s | %.1f | %.1f | %.1f(%s) | %.2f/%.2f/%.2f | %d | %d | %s",
				m.CreatedAt.Format("01-02 15:04"), m.CPUPercent, m.MemPercent,
				m.DiskMaxPercent, m.DiskMaxMount, m.Load1, m.Load5, m.Load15,
				m.ProcCount, m.TCPConn, m.Status)
			if m.Error != "" {
				line += " | 采集错误: " + truncate(m.Error, 120)
			}
			buf.WriteString(line + "\n")
		}
		buf.WriteString(fmt.Sprintf("（该主机 %d 核）\n", metrics[0].CPUCores))
		return &agentContext{
			Text: buf.String(), Items: len(metrics), Truncated: truncated,
			Summary: fmt.Sprintf("主机 %s 的 %d 条采样", host.Name, len(metrics)),
		}, nil

	case "exec_job":
		var job model.ExecJob
		if err := h.DB.First(&job, targetID).Error; err != nil {
			return nil, fmt.Errorf("执行作业不存在")
		}
		var results []model.ExecResult
		// 失败的排前面：要分析的正是它们
		if err := h.DB.Where("job_id = ?", targetID).
			Order("CASE WHEN status = 'success' THEN 1 ELSE 0 END asc, id asc").
			Limit(limit + 1).Find(&results).Error; err != nil {
			return nil, fmt.Errorf("读取执行结果失败")
		}
		truncated := len(results) > limit
		if truncated {
			results = results[:limit]
		}
		var buf strings.Builder
		buf.WriteString(fmt.Sprintf("执行作业「%s」：状态 %s，共 %d 台，成功 %d，失败 %d\n命令: %s\n\n",
			job.Name, job.Status, job.Total, job.SuccessNum, job.FailedNum,
			truncate(job.Command, 500)))
		for i, item := range results {
			buf.WriteString(fmt.Sprintf("%d. %s(%s) 状态=%s 退出码=%d 耗时=%dms\n",
				i+1, item.HostName, item.Address, item.Status, item.ExitCode, item.CostMs))
			if out := strings.TrimSpace(item.Stdout); out != "" {
				buf.WriteString("   stdout: " + truncate(oneLine(out), 600) + "\n")
			}
			if errText := strings.TrimSpace(item.Stderr); errText != "" {
				buf.WriteString("   stderr: " + truncate(oneLine(errText), 600) + "\n")
			}
		}
		return &agentContext{
			Text: buf.String(), Items: len(results), Truncated: truncated,
			Summary: fmt.Sprintf("作业「%s」的 %d 台结果", job.Name, len(results)),
		}, nil

	case "session_command":
		var session model.Session
		if err := h.DB.First(&session, targetID).Error; err != nil {
			return nil, fmt.Errorf("会话不存在")
		}
		var commands []model.SessionCommand
		if err := h.DB.Where("session_id = ?", targetID).
			Order("offset_ms asc").Limit(limit + 1).Find(&commands).Error; err != nil {
			return nil, fmt.Errorf("读取会话命令失败")
		}
		truncated := len(commands) > limit
		if truncated {
			commands = commands[:limit]
		}
		if len(commands) == 0 {
			return nil, fmt.Errorf("这次会话没有记录到命令")
		}
		var buf strings.Builder
		buf.WriteString(fmt.Sprintf("会话 #%d：%s 以 %s 登录 %s(%s)，状态 %s\n按时间顺序敲过的命令：\n",
			session.ID, session.Username, session.LoginUser, session.HostName,
			session.Address, session.Status))
		for i, item := range commands {
			line := fmt.Sprintf("%d. [+%.1fs] %s", i+1, float64(item.OffsetMs)/1000,
				truncate(item.Command, 300))
			if item.Risk != "normal" {
				line += fmt.Sprintf("  <== %s", item.Risk)
				if item.RuleDesc != "" {
					line += "（命中规则：" + item.RuleDesc + "）"
				}
			}
			buf.WriteString(line + "\n")
		}
		return &agentContext{
			Text: buf.String(), Items: len(commands), Truncated: truncated,
			Summary: fmt.Sprintf("会话 #%d 的 %d 条命令", session.ID, len(commands)),
		}, nil
	}
	return nil, fmt.Errorf("数据来源 %s 不支持", agent.DataSource)
}

// oneLine 把多行输出压成一行，便于塞进提示词
func oneLine(text string) string {
	replaced := strings.ReplaceAll(text, "\r\n", " / ")
	replaced = strings.ReplaceAll(replaced, "\n", " / ")
	return strings.Join(strings.Fields(replaced), " ")
}

// compactLabels 把告警标签的 JSON 压成 k=v 串
func compactLabels(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	var labels map[string]any
	if err := json.Unmarshal([]byte(raw), &labels); err != nil {
		return truncate(raw, 120)
	}
	parts := make([]string, 0, len(labels))
	for key, value := range labels {
		parts = append(parts, fmt.Sprintf("%s=%v", key, value))
	}
	return truncate(strings.Join(parts, ","), 200)
}

// renderAgentPrompt 渲染提示词。模板只认 context 与 input 两个变量。
func renderAgentPrompt(tmplText, contextText, input string) (string, error) {
	tpl, err := template.New("agent").Parse(tmplText)
	if err != nil {
		return "", fmt.Errorf("提示词模板语法有问题: %v", err)
	}
	var buf bytes.Buffer
	vars := map[string]string{"context": contextText, "input": input}
	if err := tpl.Execute(&buf, vars); err != nil {
		return "", fmt.Errorf("提示词渲染失败: %v", err)
	}
	prompt := buf.String()
	if len(prompt) > agentPromptMaxChars {
		// 宁可截断也不要把超长提示词发出去：又贵又容易让模型忽略中间内容
		prompt = prompt[:agentPromptMaxChars] + "\n...（提示词过长已截断）"
	}
	return prompt, nil
}

// ---------- 运行 ----------

type agentRunReq struct {
	// TargetID 数据来源需要目标时必填（主机 / 作业 / 会话的 ID）
	TargetID uint `json:"targetId"`
	// Severity 只对 alert 来源有效，限定级别
	Severity string `json:"severity"`
	// Input 运行时补充的说明，会替换模板里的 {{.input}}
	Input string `json:"input"`
}

func (h *Handler) RunAgent(c *gin.Context) {
	id := idParam(c)
	var agent model.AgentConfig
	if err := h.DB.First(&agent, id).Error; err != nil {
		response.NotFound(c, "Agent 不存在")
		return
	}
	if !agent.Enabled {
		response.BadRequest(c, "该 Agent 已停用")
		return
	}
	var req agentRunReq
	if err := c.ShouldBindJSON(&req); err != nil && err.Error() != "EOF" {
		response.BadRequest(c, "参数错误")
		return
	}
	req.Input = strings.TrimSpace(req.Input)
	req.Severity = strings.TrimSpace(req.Severity)

	meta := agentDataSources[agent.DataSource]
	if meta.needsTarget && req.TargetID == 0 {
		response.BadRequest(c, "这个数据来源需要指定目标（"+meta.label+"）")
		return
	}

	operator := middleware.CurrentUser(c)
	run := model.AgentRun{
		AgentID: agent.ID, AgentName: agent.Name, Alias: agent.Alias,
		DataSource: agent.DataSource, TargetID: req.TargetID,
		Input: truncate(req.Input, 2000),
	}
	if operator != nil {
		run.UserID, run.Username = operator.ID, operator.Username
	}

	// 上下文取不到就直接失败并落一条记录：别让「跑了但没数据」变成静默的空结论
	ctxData, err := h.buildAgentContext(&agent, req.TargetID, req.Severity)
	if err != nil {
		run.RunStatus = "failed"
		run.ErrorMsg = truncate(err.Error(), 480)
		h.DB.Create(&run)
		response.BadRequest(c, err.Error())
		return
	}
	contextText := ctxData.Text
	if len(contextText) > agentContextMaxChars {
		contextText = contextText[:agentContextMaxChars] + "\n...（上下文过长已截断）"
		ctxData.Truncated = true
	}
	run.ContextItems = ctxData.Items
	run.ContextChars = len(contextText)
	run.ContextTruncated = ctxData.Truncated

	prompt, err := renderAgentPrompt(agent.PromptTemplate, contextText, req.Input)
	if err != nil {
		run.RunStatus = "failed"
		run.ErrorMsg = truncate(err.Error(), 480)
		h.DB.Create(&run)
		response.BadRequest(c, err.Error())
		return
	}

	messages := make([]chatMessage, 0, 2)
	if agent.SystemPrompt != "" {
		messages = append(messages, chatMessage{Role: "system", Content: agent.SystemPrompt})
	}
	messages = append(messages, chatMessage{Role: "user", Content: prompt})
	chatReq := &chatRequest{Model: agent.Alias, Messages: messages, MaxTokens: agent.MaxTokens}
	if agent.Temperature > 0 {
		temp := agent.Temperature
		chatReq.Temperature = &temp
	}

	runCtx, cancel := context.WithTimeout(c.Request.Context(), agentRunTimeout)
	defer cancel()
	outcome, err := h.dispatchChat(runCtx, chatReq, "agent", operator, c.ClientIP())
	if err != nil {
		run.RunStatus = "failed"
		run.ErrorMsg = truncate(err.Error(), 480)
		h.DB.Create(&run)
		response.Error(c, err.Error())
		return
	}

	run.RunStatus = "success"
	run.Output = truncate(outcome.Result.Content, agentOutputMaxChars)
	run.CallID = outcome.Record.ID
	run.PromptTokens = outcome.Result.Usage.PromptTokens
	run.CompletionTokens = outcome.Result.Usage.CompletionTokens
	run.TotalTokens = outcome.Result.Usage.TotalTokens
	run.Cost = outcome.Record.Cost
	run.LatencyMs = outcome.Result.LatencyMs
	if err := h.DB.Create(&run).Error; err != nil {
		response.Error(c, "结论落库失败")
		return
	}

	response.OK(c, gin.H{
		"run":            run,
		"prompt":         prompt,
		"upstreamName":   outcome.Upstream.Name,
		"model":          outcome.Upstream.Model,
		"contextSummary": ctxData.Summary,
	})
}

// ---------- 运行记录 ----------

func (h *Handler) ListAgentRuns(c *gin.Context) {
	page, size := pageParams(c)
	q := h.DB.Model(&model.AgentRun{})
	if raw := strings.TrimSpace(c.Query("agentId")); raw != "" {
		id, err := strconv.Atoi(raw)
		if err != nil || id <= 0 {
			response.BadRequest(c, "agentId 不合法")
			return
		}
		q = q.Where("agent_id = ?", id)
	}
	if status := strings.TrimSpace(c.Query("status")); status != "" {
		q = q.Where("run_status = ?", status)
	}
	if source := strings.TrimSpace(c.Query("dataSource")); source != "" {
		q = q.Where("data_source = ?", source)
	}
	if username := strings.TrimSpace(c.Query("username")); username != "" {
		q = q.Where("username LIKE ?", "%"+username+"%")
	}
	start, err := parseAuditTime(c.Query("start"), false)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	end, err := parseAuditTime(c.Query("end"), true)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	if start != nil {
		q = q.Where("created_at >= ?", *start)
	}
	if end != nil {
		q = q.Where("created_at <= ?", *end)
	}
	q = q.Session(&gorm.Session{})

	var total int64
	if err := q.Count(&total).Error; err != nil {
		response.Error(c, "查询运行记录失败")
		return
	}
	// 列表不带 output：结论动辄几千字，列表页没必要拖着它走
	var rows []model.AgentRun
	if err := q.Omit("output").Order("id desc").
		Offset((page - 1) * size).Limit(size).Find(&rows).Error; err != nil {
		response.Error(c, "查询运行记录失败")
		return
	}
	response.OK(c, gin.H{"list": rows, "total": total, "page": page, "pageSize": size})
}

func (h *Handler) GetAgentRun(c *gin.Context) {
	id := idParam(c)
	var run model.AgentRun
	if err := h.DB.First(&run, id).Error; err != nil {
		response.NotFound(c, "运行记录不存在")
		return
	}
	response.OK(c, run)
}
