// theme.go 深色棋韵主题：墨底 + 象牙前景 + 朱砂点缀（Fyne Theme 接口实现，
// 覆盖配色与字级；字体/图标沿用默认主题）。
package console

import (
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"
)

// 棋韵配色：墨底、象牙前景、朱砂主色（与手机端同一套语义 token，
// 见 docs/美学设计研究.md §4.2）。朱砂·按 #C35440 专用于白字实心底：白字对比 4.5:1。
var (
	qiBackground = color.NRGBA{R: 0x14, G: 0x17, B: 0x1C, A: 0xFF} // 墨底
	qiSurface    = color.NRGBA{R: 0x1B, G: 0x1E, B: 0x24, A: 0xFF} // 炭面（菜单/弹层/tile）
	qiForeground = color.NRGBA{R: 0xE8, G: 0xE6, B: 0xE1, A: 0xFF} // 象牙（主前景）
	qiPrimary    = color.NRGBA{R: 0xC3, G: 0x54, B: 0x40, A: 0xFF} // 朱砂·按（实心按钮底）
	qiHover      = color.NRGBA{R: 0x26, G: 0x2A, B: 0x31, A: 0xFF}
	qiSelection  = color.NRGBA{R: 0x3A, G: 0x2A, B: 0x26, A: 0xFF} // 选中：暗红
	qiSeparator  = color.NRGBA{R: 0x2E, G: 0x32, B: 0x3A, A: 0xFF}
	qiDisabled   = color.NRGBA{R: 0x56, G: 0x5B, B: 0x64, A: 0xFF}
	qiFocus      = color.NRGBA{R: 0x3A, G: 0x42, B: 0x50, A: 0xFF}
	qiInputBg    = color.NRGBA{R: 0x10, G: 0x12, B: 0x16, A: 0xFF}
	qiMuted      = color.NRGBA{R: 0x9A, G: 0x9E, B: 0xA6, A: 0xFF} // 次级文字

	// 状态语义色（与手机端同一套语言：玉=就绪 / 琥珀=等待 / 警示=错误）
	qiAmber   = color.NRGBA{R: 0xFF, G: 0xC1, B: 0x07, A: 0xFF} // 琥珀（候选/等待）
	qiJade    = color.NRGBA{R: 0x81, G: 0xC7, B: 0x84, A: 0xFF} // 玉字（就绪文字）
	qiJadeDot = color.NRGBA{R: 0x4C, G: 0xAF, B: 0x50, A: 0xFF} // 玉点
	qiDanger  = color.NRGBA{R: 0xFF, G: 0x52, B: 0x52, A: 0xFF} // 警示红

	// 描边两档（分层用，不用阴影；与手机端 qi_line1/2 同值）
	qiLine1 = color.NRGBA{R: 0xFF, G: 0xFF, B: 0xFF, A: 0x1A}
	qiLine2 = color.NRGBA{R: 0xFF, G: 0xFF, B: 0xFF, A: 0x29}
)

// qiTheme 深色棋韵主题（内嵌默认主题，覆盖配色与字号）。
type qiTheme struct {
	fyne.Theme
}

// newQiTheme 构造棋韵主题。
func newQiTheme() fyne.Theme {
	return &qiTheme{Theme: theme.DefaultTheme()}
}

// Color 覆盖全部配色（未列出的沿用默认）。深浅变体统一走棋韵深色
// （应用定位即深色面板，不跟随系统切亮色）。
func (t *qiTheme) Color(n fyne.ThemeColorName, v fyne.ThemeVariant) color.Color {
	switch n {
	case theme.ColorNameBackground:
		return qiBackground
	case theme.ColorNameForeground:
		return qiForeground
	case theme.ColorNamePrimary:
		return qiPrimary
	case theme.ColorNameHover:
		return qiHover
	case theme.ColorNameSelection:
		return qiSelection
	case theme.ColorNameSeparator:
		return qiSeparator
	case theme.ColorNameDisabled:
		return qiDisabled
	case theme.ColorNameFocus:
		return qiFocus
	case theme.ColorNameInputBackground:
		return qiInputBg
	case theme.ColorNameButton:
		return qiSurface // 按钮深底（默认浅灰底在深色主题上文字对比度差）
	case theme.ColorNameDisabledButton:
		return qiInputBg
	case theme.ColorNameMenuBackground:
		return qiSurface
	case theme.ColorNameOverlayBackground:
		return qiSurface
	case theme.ColorNameHeaderBackground:
		return qiSurface
	case theme.ColorNamePlaceHolder:
		return qiMuted
	case theme.ColorNameShadow:
		return color.NRGBA{R: 0x00, G: 0x00, B: 0x00, A: 0x66}
	case theme.ColorNameScrollBar:
		return color.NRGBA{R: 0xE8, G: 0xE6, B: 0xE1, A: 0x28}
	case theme.ColorNameWarning:
		return qiAmber // 修现状漏覆盖：仪表"深度/目标"此前走默认警告色，不随棋韵体系
	case theme.ColorNameError:
		return qiDanger
	case theme.ColorNameSuccess:
		return qiJade
	case theme.ColorNameForegroundOnPrimary:
		return color.NRGBA{R: 0xFF, G: 0xFF, B: 0xFF, A: 0xFF} // 主按钮白字（#C35440 上 4.5:1）
	default:
		return t.Theme.Color(n, v)
	}
}

// meterSizeName 引擎仪表大数字的主题字号名（RichText 的 SizeName 引用）。
const meterSizeName fyne.ThemeSizeName = "qiMeter"

// Size 覆盖字号：仪表大数字 22pt、正文 13pt、标签 11pt（棋韵字级阶梯，
// 与手机端 11/13/15/22 四档对齐；其余沿用默认）。
func (t *qiTheme) Size(n fyne.ThemeSizeName) float32 {
	switch n {
	case meterSizeName:
		return 22
	case theme.SizeNameText:
		return 13
	case theme.SizeNameCaptionText:
		return 11
	}
	return t.Theme.Size(n)
}
