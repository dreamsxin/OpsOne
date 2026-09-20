package handler

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"golang.org/x/crypto/ssh"

	"ops-platform/server/internal/bastion"
	"ops-platform/server/internal/middleware"
	"ops-platform/server/internal/model"
	"ops-platform/server/internal/response"
	"ops-platform/server/internal/sshx"
)

type wsMessage struct {
	Type string `json:"type"` // input | resize
	Data string `json:"data"`
	Cols int    `json:"cols"`
	Rows int    `json:"rows"`
}

// upgrader 校验 Origin，防止跨站 WebSocket 劫持（令牌通过查询参数传递，必须做这层校验）
func (h *Handler) upgrader() *websocket.Upgrader {
	return &websocket.Upgrader{
		ReadBufferSize:  4096,
		WriteBufferSize: 4096,
		CheckOrigin: func(r *http.Request) bool {
			origin := r.Header.Get("Origin")
			if origin == "" {
				return true // 非浏览器客户端
			}
			return slices.Contains(h.Cfg.AllowOrigins, origin)
		},
	}
}

// safeConn 串行化 WebSocket 写入：输出协程与输入协程都会写
type safeConn struct {
	mu   sync.Mutex
	conn *websocket.Conn
	rec  *bastion.Recorder
}

// Write 把数据发给浏览器并写入录像
func (w *safeConn) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.rec != nil {
		_, _ = w.rec.Write(p)
	}
	if err := w.conn.WriteMessage(websocket.TextMessage, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

// Notice 输出平台自身的提示信息（同样进录像，回放时可见）
func (w *safeConn) Notice(text string) {
	_, _ = w.Write([]byte("\r\n\x1b[33m[平台] " + text + "\x1b[0m\r\n"))
}

func (w *safeConn) Alert(text string) {
	_, _ = w.Write([]byte("\r\n\x1b[31m[平台] " + text + "\x1b[0m\r\n"))
}

// Terminal 打开目标主机的交互式 SSH 会话，全程录像并审计命令
func (h *Handler) Terminal(c *gin.Context) {
	hostPtr, ok := h.loadHostScoped(c)
	if !ok {
		return
	}
	host := *hostPtr

	cols, _ := strconv.Atoi(c.DefaultQuery("cols", "120"))
	rows, _ := strconv.Atoi(c.DefaultQuery("rows", "30"))
	if cols < 20 || cols > 500 {
		cols = 120
	}
	if rows < 5 || rows > 200 {
		rows = 30
	}

	operator := middleware.CurrentUser(c)
	session := model.Session{
		HostID: host.ID, HostName: host.Name, Address: host.Address, LoginUser: host.Username,
		UserID: operator.ID, Username: operator.Username, ClientIP: c.ClientIP(),
		ViaProxy: h.proxyLabel(&host), Status: "active", StartedAt: time.Now(),
	}
	if err := h.DB.Create(&session).Error; err != nil {
		response.Error(c, "会话创建失败")
		return
	}

	conn, err := h.upgrader().Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		h.finishSession(&session, "error", "WebSocket 握手失败")
		return
	}
	defer conn.Close()

	out := &safeConn{conn: conn}
	if recorder, rErr := bastion.NewRecorder(h.Cfg.RecordDir, session.ID, cols, rows); rErr != nil {
		log.Printf("[terminal] 录像初始化失败: %v", rErr)
		session.ErrorMsg = "录像未启用: " + rErr.Error()
	} else {
		out.rec = recorder
		session.RecordPath = recorder.Path()
		h.DB.Model(&session).Update("record_path", session.RecordPath)
		defer recorder.Close()
	}

	auditor := bastion.NewAuditor(h.loadRules())

	if session.ViaProxy != "" {
		out.Notice("经跳板机连接: " + session.ViaProxy)
	}

	client, err := sshx.Dial(h.target(&host), 20*time.Second)
	if err != nil {
		out.Alert("SSH 连接失败: " + err.Error())
		h.finishSession(&session, "error", err.Error())
		return
	}
	defer client.Close()

	sshSession, err := client.NewSession()
	if err != nil {
		out.Alert("会话创建失败: " + err.Error())
		h.finishSession(&session, "error", err.Error())
		return
	}
	defer sshSession.Close()

	stdin, err := sshSession.StdinPipe()
	if err != nil {
		out.Alert("输入通道创建失败")
		h.finishSession(&session, "error", err.Error())
		return
	}
	stdout, err := sshSession.StdoutPipe()
	if err != nil {
		out.Alert("输出通道创建失败")
		h.finishSession(&session, "error", err.Error())
		return
	}
	sshSession.Stderr = out

	modes := ssh.TerminalModes{ssh.ECHO: 1, ssh.TTY_OP_ISPEED: 14400, ssh.TTY_OP_OSPEED: 14400}
	if err := sshSession.RequestPty("xterm-256color", rows, cols, modes); err != nil {
		out.Alert("PTY 申请失败: " + err.Error())
		h.finishSession(&session, "error", err.Error())
		return
	}
	if err := sshSession.Shell(); err != nil {
		out.Alert("Shell 启动失败: " + err.Error())
		h.finishSession(&session, "error", err.Error())
		return
	}

	// SSH 输出 -> 浏览器 + 录像
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := stdout.Read(buf)
			if n > 0 {
				if _, wErr := out.Write(buf[:n]); wErr != nil {
					return
				}
			}
			if err != nil {
				if err != io.EOF {
					log.Printf("[terminal] 读取输出失败: %v", err)
				}
				out.Notice("会话已结束")
				_ = conn.Close()
				return
			}
		}
	}()

	// 浏览器输入 -> 审计 -> SSH
	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			break
		}
		var msg wsMessage
		if json.Unmarshal(raw, &msg) != nil {
			continue
		}

		switch msg.Type {
		case "input":
			forward, decisions := auditor.Feed([]byte(msg.Data))
			for _, d := range decisions {
				h.recordCommand(&session, out, d)
			}
			if len(forward) > 0 {
				if _, err := stdin.Write(forward); err != nil {
					break
				}
			}
		case "resize":
			if msg.Cols > 0 && msg.Rows > 0 {
				_ = sshSession.WindowChange(msg.Rows, msg.Cols)
			}
		}
	}

	h.finishSession(&session, "closed", session.ErrorMsg)
}

// loadRules 读取启用中的命令规则并编译，非法正则跳过并告警
func (h *Handler) loadRules() []bastion.Rule {
	var rows []model.CommandRule
	if err := h.DB.Where("enabled = ?", true).Order("id asc").Find(&rows).Error; err != nil {
		log.Printf("[terminal] 加载命令规则失败: %v", err)
		return nil
	}

	rules := make([]bastion.Rule, 0, len(rows))
	for _, row := range rows {
		re, err := regexp.Compile(row.Pattern)
		if err != nil {
			log.Printf("[terminal] 命令规则 %d 正则非法，已跳过: %v", row.ID, err)
			continue
		}
		rules = append(rules, bastion.Rule{
			ID: row.ID, Pattern: re, Action: row.Action, Description: row.Description,
		})
	}
	return rules
}

// recordCommand 落库单条命令，命中阻断时提示用户
func (h *Handler) recordCommand(session *model.Session, out *safeConn, d bastion.Decision) {
	offset := int64(0)
	if out.rec != nil {
		offset = out.rec.Elapsed()
	}
	entry := model.SessionCommand{
		SessionID: session.ID, Command: d.Command, Risk: d.Risk,
		RuleID: d.RuleID, OffsetMs: offset,
	}
	if err := h.DB.Create(&entry).Error; err != nil {
		log.Printf("[terminal] 命令落库失败: %v", err)
	}

	session.CommandCount++
	switch d.Risk {
	case bastion.RiskBlocked:
		session.BlockedCount++
		out.Alert(fmt.Sprintf("命令已被拦截: %s（规则: %s）", d.Command, ruleLabel(d)))
	case bastion.RiskWarn:
		out.Notice(fmt.Sprintf("高风险命令已记录（规则: %s）", ruleLabel(d)))
	}
}

func ruleLabel(d bastion.Decision) string {
	if d.RuleDesc != "" {
		return d.RuleDesc
	}
	return fmt.Sprintf("#%d", d.RuleID)
}

// finishSession 收尾写入会话统计
func (h *Handler) finishSession(session *model.Session, status, errMsg string) {
	now := time.Now()
	session.Status = status
	session.EndedAt = &now
	session.DurationMs = now.Sub(session.StartedAt).Milliseconds()
	if errMsg != "" {
		session.ErrorMsg = truncate(errMsg, 240)
	}
	err := h.DB.Model(session).
		Select("status", "ended_at", "duration_ms", "error_msg", "command_count", "blocked_count").
		Updates(session).Error
	if err != nil {
		log.Printf("[terminal] 会话收尾失败: %v", err)
	}
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}
