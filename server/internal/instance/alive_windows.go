//go:build windows

package instance

import "os"

// processAlive 本机上这个 PID 的进程还在不在。
//
// Windows 上 os.FindProcess 会真的去 OpenProcess，进程不存在时返回错误 ——
// 与 Unix 那边「FindProcess 永远成功」的语义不同，所以两边分开实现。
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	_ = proc.Release()
	return true
}
