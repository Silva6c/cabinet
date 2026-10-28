//go:build !windows

// console_other.go 非 Windows 平台的暗色标题栏空实现：Linux/macOS 的标题栏
// 主题由桌面环境/系统设置管理，无 DWM 可调；此存根仅让 console.go 的调用
// 跨平台编译通过（Windows 实现见 console_windows.go，靠文件名后缀获得平台约束）。
package console

import "fyne.io/fyne/v2"

// applyDarkTitleBar 非 Windows 无操作。
func applyDarkTitleBar(_ fyne.Window) {}
