// console_windows.go Windows 暗色标题栏：fyne 2.8 的标题栏暗色判定读系统
// 注册表（AppsUseLightTheme）而非应用主题——系统浅色模式下墨底窗口顶着
// 一条白色系统标题栏（用户反馈的"白条带"）。这里经 RunNative 拿 HWND 后
// 直接 DwmSetWindowAttribute(DWMWA_USE_IMMERSIVE_DARK_MODE) 强制暗色，
// 标题栏随应用主题走，不再依赖系统模式。
package console

import (
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/driver"
)

// applyDarkTitleBar 对窗口强制 DWM 暗色标题栏（仅 Windows 生效；fyne 测试渲染
// 无原生窗口时静默跳过）。
func applyDarkTitleBar(w fyne.Window) {
	nw, ok := w.(driver.NativeWindow)
	if !ok {
		return
	}
	nw.RunNative(func(ctx any) {
		hc, ok := ctx.(driver.WindowsWindowContext)
		if !ok || hc.HWND == 0 {
			return
		}
		setDWMImmersiveDark(hc.HWND)
		// 【雷区实测 2026-10-03】建窗期（Show 前）设置的暗色会被窗口首绘流程盖回
		// 系统浅色——审计实拍：启动后标题栏仍白，而对同一 HWND 事后重设立即生效
		// （PowerShell 手动调 DwmSetWindowAttribute rc=0、标题栏即时变暗）。
		// 缓存 HWND 后延迟幂等重设兜底；DWM 属性可跨线程、可重复设置。
		for _, d := range []time.Duration{500 * time.Millisecond, 2 * time.Second} {
			hwnd := hc.HWND
			time.AfterFunc(d, func() { setDWMImmersiveDark(hwnd) })
		}
	})
}
