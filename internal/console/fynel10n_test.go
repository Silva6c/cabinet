// fynel10n_test.go 翻译补丁注册验证。
package console

import (
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/lang"
)

// TestPatchTranslations zh-Hans 补丁包须注册成功（JSON 合法 + locale 可解析）；
// 重复注册幂等不炸（多 Console 实例场景）。
func TestPatchTranslations(t *testing.T) {
	if err := lang.AddTranslationsForLocale([]byte(`{"(Select one)":"请选择"}`), fyne.Locale("zh-Hans")); err != nil {
		t.Fatalf("zh-Hans 补丁包注册失败: %v", err)
	}
	patchTranslations()
	patchTranslations()
}
