// fynel10n.go Fyne 内置翻译补丁（控制台垃圾日志根治之一）。
//
// Fyne v2.8.2 的 zh-Hans 内置包（lang/translations/base.zh_Hans.json）缺
// Select 占位词条 "(Select one)"：每个 Select 控件创建时本地化查找失败，
// 经 stdlib log → slog 桥在日志文件打 3 行 "Translation failure" 噪音
// （实测每次启动 15 行，正好刷满日志面板尾部——2026-10-29 用户实录）。
// 此处按 fyne lang 包公开 API 补注册缺失词条（"请选择"），从源头消除；
// 顺带让下拉占位符显示中文。注册失败无功能损益（维持原报错行为）。
package console

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/lang"
)

// patchTranslations 补齐 Fyne 内置 zh-Hans 翻译缺失的控件词条。
// JSON 格式与内置 base.*.json 同构（message ID → 译文，平铺键值）。
func patchTranslations() {
	const zhPatch = `{"(Select one)":"请选择"}`
	_ = lang.AddTranslationsForLocale([]byte(zhPatch), fyne.Locale("zh-Hans"))
}
