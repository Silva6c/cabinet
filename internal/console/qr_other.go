//go:build !windows

// qr_other.go 非 Windows 平台的扫码置顶空实现：Linux/macOS 的窗口层级由桌面
// 环境管理，本功能仅服务 Windows 部署位。此存根仅让 qr.go 跨平台编译通过
// （Windows 实现见 qr_windows.go，签名逐字对表——AGENTS.md 跨平台雷区纪律：
// 存根签名错本机全绿、只在 CI 炸，改存根后须双 GOOS 构建验证）。
package console

// setWindowTopMost 非 Windows 无操作。
func setWindowTopMost(_ uintptr, _ bool) {}
