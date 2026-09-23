//go:build !windows

package instance

import (
	"os"
	"syscall"
)

// processAlive 本机上这个 PID 的进程还在不在。
//
// Unix 上 os.FindProcess 对任何 PID 都返回成功，所以要靠「发 0 号信号」来探活：
// 进程不存在返回 ESRCH；存在但不属于当前用户返回 EPERM —— 那也是「还活着」。
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = proc.Signal(syscall.Signal(0))
	if err == nil {
		return true
	}
	return err == os.ErrPermission || err == syscall.EPERM
}
