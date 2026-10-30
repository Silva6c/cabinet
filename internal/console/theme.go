// theme.go 深色棋韵主题：墨底 + 象牙前景 + 朱砂点缀（Fyne Theme 接口实现，
// 覆盖配色与字级；字体/图标沿用默认主题）。
package console

import (
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"
)

// 棋韵配色：墨底、象牙前景、朱砂主色（与手机端同一套语义 token）。
// 朱砂·按 #C35440 专用于白字实心底：白字对比 4.5:1。
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
	qiAmber  = color.NRGBA{R: 0xFF, G: 0xC1, B: 0x07, A: 0xFF} // 琥珀（候选/等待）
	qiJade   = color.NRGBA{R: 0x81, G: 0xC7, B: 0x84, A: 0xFF} // 玉字（就绪文字）
	qiDanger = color.NRGBA{R: 0xFF, G: 0x52, B: 0x52, A: 0xFF} // 警示红

	// 走势曲线劣势段（2026-10-29 天天象棋风定稿：优=朱砂/劣=靛青双色面积图；
	// 仅图形用不承载文字，对比度按图形可见性选档）
	qiIndigo = color.NRGBA{R: 0x64, G: 0x9E, B: 0xD2, A: 0xFF} // 靛青

	// 描边两档（分层用，不用阴影；与手机端 qi_line1/2 同值）：
	// line1 进度条轨道 / line2 卡片与按钮边界。
	// （原 line3 高光档从未被使用，2026-10-29 死代码清扫删除——需要时从
	// git 历史取回，值 0xFFFFFF @ A:0x47）
	qiLine1 = color.NRGBA{R: 0xFF, G: 0xFF, B: 0xFF, A: 0x1A}
	qiLine2 = color.NRGBA{R: 0xFF, G: 0xFF, B: 0xFF, A: 0x29}

	// 朱砂双色阶（设计稿 §4.2：品牌/悬停与"实心按钮底"两职分离；
	// 此前 #C35440 一个值包办，悬停亮档与品牌强调无处安放）
	qiZhuBrand = color.NRGBA{R: 0xD0, G: 0x5A, B: 0x44, A: 0xFF} // 品牌朱砂（强调描边/文字）
	qiZhuLite  = color.NRGBA{R: 0xE2, G: 0x70, B: 0x5A, A: 0xFF} // 悬停亮红
	qiPressed  = color.NRGBA{R: 0xA8, G: 0x43, B: 0x33, A: 0xFF} // 按下加深（涟漪色）
	qiGlow     = color.NRGBA{R: 0xC3, G: 0x54, B: 0x40, A: 0x66} // 主按钮外发光（朱砂 40%）

	// 警示横幅（此前内联硬编码 #FF5252 A:0x20，游离于 token 体系外）
	qiDangerBanner = color.NRGBA{R: 0xFF, G: 0x52, B: 0x52, A: 0x20}
	qiDangerSoft   = color.NRGBA{R: 0xFF, G: 0x52, B: 0x52, A: 0x66}
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
	case theme.ColorNamePressed:
		return qiPressed // 点击涟漪色：朱砂加深（原默认灰在朱砂实心上发闷）
	case qiColorLogWarn:
		return qiAmber // 日志 WARN 行（琥珀）
	case qiColorLogError:
		return qiDanger // 日志 ERROR 行（警示红）
	default:
		return t.Theme.Color(n, v)
	}
}

// Font 覆盖正体/粗体两槽为 Noto Sans SC 子集（fontassets.go，SIL OFL 声明见
// THIRD-PARTY-NOTICES.md §6b）；等宽/斜体槽走默认（取舍依据见 fontassets.go）。
func (t *qiTheme) Font(style fyne.TextStyle) fyne.Resource {
	if r := fontFor(style); r != nil {
		return r
	}
	return t.Theme.Font(style)
}

// meterSizeName 引擎仪表大数字的主题字号名（RichText 的 SizeName 引用）。
const meterSizeName fyne.ThemeSizeName = "qiMeter"

// 日志级别专用主题色名（RichText 段落 ColorName 引用，经 Color() 分发到棋韵
// 语义色——自定义色名走主题机制而非游离色值，与 meterSizeName 同款套路）。
const (
	qiColorLogWarn  fyne.ThemeColorName = "qiLogWarn"  // WARN → 琥珀
	qiColorLogError fyne.ThemeColorName = "qiLogError" // ERROR → 警示红
)

// Size 覆盖字号与圆角：仪表大数字 22pt、正文 13pt、标签 11pt（棋韵字级阶梯，
// 与手机端 11/13/15/22 四档对齐）；圆角对齐设计稿四级 8/12/16/20（§4.4）——
// 按钮/菜单/弹窗统一 8、卡片/对话框 12（bentoTile 自绘处同步 12，两处勿失配）。
func (t *qiTheme) Size(n fyne.ThemeSizeName) float32 {
	switch n {
	case meterSizeName:
		return 22
	case theme.SizeNameText:
		return 13
	case theme.SizeNameCaptionText:
		return 11
	case theme.SizeNameButtonRadius:
		return 8
	case theme.SizeNameCardRadius:
		return 12
	case theme.SizeNameDialogRadius:
		return 12
	case theme.SizeNameMenuRadius:
		return 8
	case theme.SizeNamePopupRadius:
		return 8
	case theme.SizeNameInputRadius:
		return 8
	}
	return t.Theme.Size(n)
}
