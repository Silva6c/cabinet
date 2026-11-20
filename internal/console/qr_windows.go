//go:build windows

// qr_windows.go 扫码弹窗的主窗临时置顶（Windows 实现，2026-11-18 用户拍板
// "应用内对话框+扫码期间置顶、关弹窗解除"）：user32 SetWindowPos 挂
// HWND_TOPMOST / 撤回 HWND_NOTOPMOST。fyne 的 desktop.RequestAlwaysOnTop 是
// 单向 API（glfw Floating=True 无解除接口），"临时置顶+解除"只能走 HWND 原生。
// 调用惯例与 console_windows.go 的 DWM 暗色标题栏同款：RunNative 拿
// WindowsWindowContext.HWND 后设置；SetWindowPos 无指针参数，数值惯用法
// 无 unsafe.Pointer，vet 不涉 LazyProc 误报（见 AGENTS.md 跨平台雷区）。
package console

import "syscall"

var user32 = syscall.NewLazyDLL("user32.dll")
var procSetWindowPos = user32.NewProc("SetWindowPos")

// setWindowTopMost 主窗 Z 序置顶/撤顶。SWP_NOMOVE|SWP_NOSIZE|SWP_NOACTIVATE：
// 只动 Z 序，不动位置尺寸、不抢焦点（手机端可能正在前台对准屏幕）。
func setWindowTopMost(hwnd uintptr, top bool) {
	const (
		hwndTopMost   = ^uintptr(0)     // HWND_TOPMOST (-1)
		hwndNoTopMost = ^uintptr(0) - 1 // HWND_NOTOPMOST (-2)
		swpNoSize     = 0x0001
		swpNoMove     = 0x0002
		swpNoActivate = 0x0010
	)
	after := uintptr(hwndNoTopMost)
	if top {
		after = hwndTopMost
	}
	if r1, _, _ := procSetWindowPos.Call(hwnd, after, 0, 0, 0, 0,
		swpNoSize|swpNoMove|swpNoActivate); r1 == 0 {
		// 失败静默：置顶失败只是扫码期间可能被遮挡，弹窗本身照常可用
		return
	}
}
