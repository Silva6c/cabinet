// fontassets.go 棋韵 UI 中文字体（Noto Sans SC 子集，SIL OFL 1.1，声明见
// THIRD-PARTY-NOTICES.md §6b）：覆盖 ASCII + GB2312 全表共 7543 码点，
// 中黑/粗体两槽——嵌入后 UI 中文观感不再依赖系统字体回退（各机"两张脸"问题）。
//
// 默认槽选 Medium 而非 Regular（2026-10-28 用户反馈"较细的字换粗一点"）：
// Noto Regular 的笔画在深底小字号下偏纤细，整体抬到 Medium 档；粗体槽仍为
// Bold，与 Medium 的对比度足够区分层级。
//
// 槽位取舍：斜体/粗斜走默认（中文无斜体传统，英文斜体保持原生）；**等宽槽
// 走默认**——Noto Sans SC 拉丁为比例宽，覆盖等宽槽会破坏日志时间戳/指纹列的
// 等宽对齐，等宽区中文暂走系统回退（混排一致性若不可接受，后续批评估
// 更纱黑体 Sarasa Mono SC 等宽中文）。
package console

import (
	_ "embed"

	"fyne.io/fyne/v2"
)

//go:embed fontassets/NotoSansSC-Medium.subset.ttf
var fontQiMediumData []byte

//go:embed fontassets/NotoSansSC-Bold.subset.ttf
var fontQiBoldData []byte

var (
	fontQiMedium = fyne.NewStaticResource("NotoSansSC-Medium.subset.ttf", fontQiMediumData)
	fontQiBold   = fyne.NewStaticResource("NotoSansSC-Bold.subset.ttf", fontQiBoldData)
)

// fontFor 按 TextStyle 分发嵌入字体；返回 nil = 该槽走 Fyne 默认主题字体。
func fontFor(style fyne.TextStyle) fyne.Resource {
	switch {
	case style.Monospace:
		return nil // 时间戳/指纹列等宽对齐优先（见文件头）
	case style.Italic:
		return nil // 中文无斜体传统（粗斜=Bold+Italic 同样命中此分支）；英文斜体保持原生
	case style.Bold:
		return fontQiBold
	default:
		return fontQiMedium
	}
}
