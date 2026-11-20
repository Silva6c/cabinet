//go:build !windows

// winpos_other.go 主窗位置读写的非 Windows 空存根：与 winpos_windows.go
// 签名逐字对表（AGENTS.md 跨平台雷区——存根签名错本机全绿、只在 CI 炸）。
// 本功能仅 Windows 桌面交付，此存根只为跨平台编译通过。
package console

// getWindowRectPos 非 Windows 无操作。
func getWindowRectPos(hwnd uintptr) (x, y int32, ok bool) { return 0, 0, false }

// setWindowXY 非 Windows 无操作。
func setWindowXY(hwnd uintptr, x, y int32) {}
