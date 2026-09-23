//go:build windows

package instance

import "syscall"

// processAlive 本机上这个 PID 的进程还在不在。
//
// Windows 上这件事比 Unix 绕：**句柄能打开不等于进程还活着**。
// 只要还有人持着那个进程的句柄（比如 PowerShell 的 `Start-Process -PassThru`、
// 或者父进程没回收子进程），PID 就一直能被 OpenProcess 打开 ——
// 于是「进程已经被杀了，但记录被判定为还活着」，僵尸实例记录就清不掉。
//
// 这是实测踩出来的：先用 os.FindProcess 判断，结果被杀掉的后端仍然被认为活着，
// 重启照旧被实例互斥拦住。真正的判断是**等它 0 秒**：
// 已经退出的进程对象处于「已触发」状态，WaitForSingleObject 立刻返回 WAIT_OBJECT_0。
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	// 两个权限都要：
	//   PROCESS_QUERY_LIMITED_INFORMATION 才能对别的用户跑起来的进程查状态；
	//   **SYNCHRONIZE 才能 WaitForSingleObject** —— 少了它 Wait 会失败，
	//   于是走到「查不出来按还活着处理」，僵尸记录又清不掉了（这一条是实测踩出来的：
	//   只申请 QUERY_LIMITED_INFORMATION 时重启仍然被实例互斥拦住）。
	const (
		processQueryLimitedInformation = 0x1000
		synchronize                    = 0x00100000
	)
	handle, err := syscall.OpenProcess(processQueryLimitedInformation|synchronize, false, uint32(pid))
	if err != nil {
		return false // 连句柄都打不开：进程不存在
	}
	defer func() { _ = syscall.CloseHandle(handle) }()

	event, err := syscall.WaitForSingleObject(handle, 0)
	if err != nil {
		// 查不出来时按「还活着」处理：宁可这次起不来，也不要两个实例同时跑调度
		return true
	}
	return event != syscall.WAIT_OBJECT_0
}
