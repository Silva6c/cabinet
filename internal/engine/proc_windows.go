//go:build windows

// 引擎子进程窗口控制（Windows）：pikafish.exe 是 console 程序，
// windowsgui 父进程启动它时会为其新建控制台窗口（用户看到黑框闪现）。
// 用 CREATE_NO_WINDOW 让子进程不创建任何窗口，0x08000000 即其官方值；
// 不要拿其他"无窗"标志顶替——DETACHED_PROCESS 是 0x00000008 而非 0x4
//（0x4 实为 CREATE_SUSPENDED），且其无控制台语义有破坏 stdio 管道的风险。

package engine

import (
	"os/exec"
	"syscall"
)

// applyProcAttr 平台相关的子进程属性：Windows 下隐藏子进程窗口。
func applyProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: 0x08000000, // CREATE_NO_WINDOW
	}
}

var (
	modkernel32          = syscall.NewLazyDLL("kernel32.dll")
	procOpenProcess      = modkernel32.NewProc("OpenProcess")
	procSetPriorityClass = modkernel32.NewProc("SetPriorityClass")
	procCloseHandle      = modkernel32.NewProc("CloseHandle")
)

const (
	processSetInformation = 0x0200 // PROCESS_SET_INFORMATION
	highPriorityClass     = 0x80   // HIGH_PRIORITY_CLASS
)

// elevateProcPriority 引擎进程启动后提升为高优先级：前台分析场景优先吃 CPU，
// 交互延迟最小化。失败静默——个别受限环境不允许提权，不影响功能。
func elevateProcPriority(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	// 按_pid 打开进程（Go 1.26 的 os.Process 已无公开 Handle 方法），
	// 只申请 PROCESS_SET_INFORMATION 最小权限
	h, _, _ := procOpenProcess.Call(processSetInformation, 0, uintptr(cmd.Process.Pid))
	if h == 0 {
		return
	}
	// syscall.Call 返回 (r1, r2, err) 三值，此处无可恢复语义，显式忽略
	defer func() { _, _, _ = procCloseHandle.Call(h) }()
	_, _, _ = procSetPriorityClass.Call(h, highPriorityClass)
}
