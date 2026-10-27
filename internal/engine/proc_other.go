//go:build !windows

// 非 Windows 平台无"子进程控制台窗口"问题，空实现保持调用点统一。

package engine

import "os/exec"

// applyProcAttr 平台相关的子进程属性（非 Windows 无需处理）。
func applyProcAttr(cmd *exec.Cmd) {}

// elevateProcPriority 进程优先级提升（非 Windows 暂不处理，Linux 下
// nice -n -5 需 CAP_SYS_NICE，桌面场景收益有限）。
func elevateProcPriority(cmd *exec.Cmd) {}
