package handler

import (
	"log"
	"sync"
	"time"

	"ops-platform/server/internal/backup"
	"ops-platform/server/internal/model"
)

// ---------- 活跃终端登记 ----------
//
// Web 终端是被劫持（hijack）的 WebSocket 连接，http.Server.Shutdown 不认识它，
// 只会一直等到超时。所以进程内自己登记一份，停机时主动通知并关掉，
// 让每个会话的 goroutine 走正常收尾路径（写 sessions 结束状态、关录像文件）。

type terminalRegistry struct {
	mu    sync.Mutex
	items map[uint]*safeConn
}

func newTerminalRegistry() *terminalRegistry {
	return &terminalRegistry{items: map[uint]*safeConn{}}
}

func (r *terminalRegistry) add(sessionID uint, conn *safeConn) {
	r.mu.Lock()
	r.items[sessionID] = conn
	r.mu.Unlock()
}

func (r *terminalRegistry) remove(sessionID uint) {
	r.mu.Lock()
	delete(r.items, sessionID)
	r.mu.Unlock()
}

func (r *terminalRegistry) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.items)
}

func (r *terminalRegistry) snapshot() map[uint]*safeConn {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[uint]*safeConn, len(r.items))
	for id, c := range r.items {
		out[id] = c
	}
	return out
}

// ActiveTerminals 当前进程内的终端会话数（平台健康页用）
func (h *Handler) ActiveTerminals() int { return h.terminals.count() }

// CloseAllTerminals 通知并关闭所有终端连接，返回关掉的条数
func (h *Handler) CloseAllTerminals(reason string) int {
	items := h.terminals.snapshot()
	for _, conn := range items {
		conn.Alert(reason)
		conn.Close()
	}
	return len(items)
}

// CloseAllForwards 关闭所有转发隧道并把计数写回档案，返回关掉的条数
func (h *Handler) CloseAllForwards(reason string) int {
	items := h.forwards.snapshot()
	for _, t := range items {
		h.closeForward(t, reason)
	}
	return len(items)
}

// FinishActiveSessions 兜底：把库里仍标着 active 的会话收尾。
// 正常情况下每个会话的 goroutine 自己会写，这里只处理「进程没来得及等它们」的残留，
// 免得列表里留下永远在线的幽灵会话。
func (h *Handler) FinishActiveSessions(reason string) int64 {
	now := time.Now()
	res := h.DB.Model(&model.Session{}).
		Where("status = ?", "active").
		Updates(map[string]any{
			"status":    "closed",
			"error_msg": reason,
			"ended_at":  &now,
		})
	if res.Error != nil {
		log.Printf("[shutdown] 收尾会话状态失败: %v", res.Error)
		return 0
	}
	return res.RowsAffected
}

// Shutdown 进程停机时的有序收尾。调用方负责先停掉 HTTP 监听。
func (h *Handler) Shutdown(reason string) {
	if n := h.CloseAllTerminals(reason); n > 0 {
		log.Printf("[shutdown] 已通知并关闭 %d 个 Web 终端会话", n)
	}
	if n := h.CloseAllForwards(reason); n > 0 {
		log.Printf("[shutdown] 已关闭 %d 条转发隧道", n)
	}
}

// FinishShutdown 在 HTTP 服务停下来之后调用，处理最后的残留
func (h *Handler) FinishShutdown(reason string) {
	if n := h.FinishActiveSessions(reason); n > 0 {
		log.Printf("[shutdown] 兜底收尾 %d 条仍标为进行中的会话", n)
	}
}

// ---------- 定时备份 ----------

// RunBackupForSchedule 定时备份入口。备份失败要能在平台健康页看出来，
// 所以成功与失败都记一条内存运行记录。
func (h *Handler) RunBackupForSchedule() {
	res, err := backup.Run(h.DB, h.Cfg.RecordDir, h.Cfg.BackupDir, h.Cfg.BackupKeep, time.Time{})
	if err != nil {
		log.Printf("[backup] 定时备份失败: %v", err)
		h.markFixedRun("backup", "失败: "+truncate(err.Error(), 200))
		return
	}
	log.Printf("[backup] 定时备份完成: %s", res.Summary())
	h.markFixedRun("backup", res.Summary())
}
