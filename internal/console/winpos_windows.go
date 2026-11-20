//go:build windows

// winpos_windows.go 主窗位置读写（Windows 实现，2026-11-20 窗口位置记忆）：
// fyne v2.8 Window 接口无 Position/SetPosition（编译期实查），读写只能经
// RunNative 拿 WindowsWindowContext.HWND 调 user32——GetWindowRect 读、
// SetWindowPos 写。与 qr_windows.go 置顶同款原生通路。GetWindowRect 有指针
// 出参（*RECT），vet 对 LazyProc 指针参数的已知误报按 AGENTS.md 跨平台雷区
// 惯例 //nolint:govet 豁免（同 firewall_windows.go 的 ShellExecuteW）。
package console

import "unsafe"

var procGetWindowRect = user32.NewProc("GetWindowRect")

// winRect user32 RECT 布局（GetWindowRect 出参；坐标为屏幕物理像素，多显示
// 器下可为负值——int32 按 API 语义原样读写）。
type winRect struct {
	Left, Top, Right, Bottom int32
}

// getWindowRectPos 窗口左上角屏幕坐标。
func getWindowRectPos(hwnd uintptr) (x, y int32, ok bool) {
	var r winRect
	r1, _, _ := procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&r))) //nolint:govet
	if r1 == 0 {
		return 0, 0, false
	}
	return r.Left, r.Top, true
}

// setWindowXY 仅移动窗口位置（SWP_NOSIZE|SWP_NOZORDER|SWP_NOACTIVATE：不动
// 尺寸/Z 序、不抢焦点）。坐标为负（左/上方副屏）时 int32→uintptr 的高位符号
// 扩展无害：Win32 API 按 32 位 int 解释参数，低 32 位即原值二进制。
func setWindowXY(hwnd uintptr, x, y int32) {
	const (
		swpNoSize     = 0x0001
		swpNoZOrder   = 0x0004
		swpNoActivate = 0x0010
	)
	if r1, _, _ := procSetWindowPos.Call(hwnd, 0, uintptr(int32(x)), uintptr(int32(y)), 0, 0,
		swpNoSize|swpNoZOrder|swpNoActivate); r1 == 0 {
		// 失败静默：恢复失败只是回默认位，无功能影响
		return
	}
}
