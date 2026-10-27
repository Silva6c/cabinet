// dwm_windows.go DWM 调用（与 console_windows.go 分离：纯 syscall 无 fyne 依赖，
// 便于单测 mock 结构对齐）。
package console

import (
	"syscall"
	"unsafe"
)

var (
	dwmapi                    = syscall.NewLazyDLL("dwmapi.dll")
	procDwmSetWindowAttribute = dwmapi.NewProc("DwmSetWindowAttribute")
)

// dwmwaUseImmersiveDarkMode DWM 属性 20（旧版 Windows 10 build < 18985 为 19，
// 两个值都设置一次以兼容）。
const dwmwaUseImmersiveDarkMode = 20

// setDWMImmersiveDark 强制窗口标题栏暗色（on=true）。失败静默——标题栏颜色
// 属外观增强，不影响功能（旧版 Windows 不支持时保持系统默认）。
func setDWMImmersiveDark(hwnd uintptr) {
	on := uint32(1)
	for _, attr := range []uint32{dwmwaUseImmersiveDarkMode, 19} {
		// 外观增强调用，失败保持系统默认即可，显式忽略返回
		_, _, _ = procDwmSetWindowAttribute.Call(
			hwnd,
			uintptr(attr),
			uintptr(unsafe.Pointer(&on)),
			unsafe.Sizeof(on),
		)
	}
}
