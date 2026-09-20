// Package bastion 提供 Web 终端的会话录像与命令审计能力。
package bastion

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Recorder 以 asciinema v2 格式记录终端输出，便于网页回放。
// 只记录输出流，不记录键盘输入，避免把口令这类不回显的内容落盘。
type Recorder struct {
	mu     sync.Mutex
	file   *os.File
	path   string
	start  time.Time
	closed bool
}

// NewRecorder 在 dir 下创建 session-<id>.cast
func NewRecorder(dir string, sessionID uint, cols, rows int) (*Recorder, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("创建录像目录失败: %w", err)
	}
	path := filepath.Join(dir, fmt.Sprintf("session-%d.cast", sessionID))
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o640)
	if err != nil {
		return nil, fmt.Errorf("创建录像文件失败: %w", err)
	}

	start := time.Now()
	header := map[string]any{
		"version":   2,
		"width":     cols,
		"height":    rows,
		"timestamp": start.Unix(),
		"env":       map[string]string{"TERM": "xterm-256color", "SHELL": "/bin/sh"},
	}
	line, err := json.Marshal(header)
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	if _, err := file.Write(append(line, '\n')); err != nil {
		_ = file.Close()
		return nil, err
	}

	return &Recorder{file: file, path: path, start: start}, nil
}

func (r *Recorder) Path() string {
	return r.path
}

// Elapsed 距会话开始的毫秒数
func (r *Recorder) Elapsed() int64 {
	return time.Since(r.start).Milliseconds()
}

// Write 记录一段终端输出
func (r *Recorder) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return len(p), nil
	}

	event, err := json.Marshal([]any{time.Since(r.start).Seconds(), "o", string(p)})
	if err != nil {
		return 0, err
	}
	if _, err := r.file.Write(append(event, '\n')); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (r *Recorder) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	r.closed = true
	return r.file.Close()
}
