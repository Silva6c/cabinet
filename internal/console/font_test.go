package console

import (
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
)

// TestFontDispatch 字体槽分发：正体（Medium 档）/粗体走嵌入资源，等宽/斜体走默认主题。
func TestFontDispatch(t *testing.T) {
	test.NewApp()
	test.ApplyTheme(t, newQiTheme())
	th := newQiTheme()

	if got := th.Font(fyne.TextStyle{}); got != fontQiMedium {
		t.Fatalf("正体应走嵌入 Medium（默认槽字重升档）, got %v", got)
	}
	if got := th.Font(fyne.TextStyle{Bold: true}); got != fontQiBold {
		t.Fatalf("粗体应走嵌入 Bold, got %v", got)
	}
	if got := th.Font(fyne.TextStyle{Monospace: true}); got == fontQiMedium || got == fontQiBold {
		t.Fatal("等宽槽应走默认主题（时间戳对齐优先）")
	}
	if got := th.Font(fyne.TextStyle{Italic: true}); got == fontQiMedium {
		t.Fatal("斜体槽应走默认主题（中文无斜体）")
	}
	if th.Font(fyne.TextStyle{}) == nil || string(th.Font(fyne.TextStyle{}).Content()) == "" {
		t.Fatal("嵌入字体资源内容为空")
	}
}
