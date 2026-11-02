package console

// advpanel_test.go 05 高级面板统一盒高回归（2026-11-02 用户定稿）：五个面板
// 便当盒的 MinSize 高度必须恒等于 advPanelH，与数据形态（库数量等）无关——
// 盒高漂移即有人动了 wrapBento/vboxFlex/fixedHeight 的统一契约。纯 MinSize
// 断言，software 画布渲染但零文件输出，日常 go test 零副作用。

import (
	"testing"
	"time"

	"fyne.io/fyne/v2/test"

	"cabinet/internal/update"
)

// TestAdvPanelsUniformHeight 面板全量形态（操作区+开关行+更新区都在）下，
// 五面板盒高恒等 advPanelH；开局库面板在 1–9 库（超出旧 240 封顶的形态）下
// 盒高不变——多库只增加盒内滚动，不撑大便当盒。
func TestAdvPanelsUniformHeight(t *testing.T) {
	a := test.NewApp()
	a.Settings().SetTheme(newQiTheme())

	opts := demoOptions()
	now := time.Now()
	// 补齐库管理/三开关/更新回调：面板缺行时测的是残形态，不算数
	opts.BookImportName = func(string) string { return "" }
	opts.BookImportFile = func(string) string { return "" }
	opts.BookMove = func(string, int) string { return "" }
	opts.BookToggle = func(string, bool) string { return "" }
	opts.CloudFirstGet = func() bool { return false }
	opts.CloudFirstSet = func(bool) string { return "" }
	opts.EndgameGet = func() bool { return false }
	opts.EndgameSet = func(bool) string { return "" }
	opts.SkipBooksGet = func() bool { return false }
	opts.SkipBooksSet = func(bool) string { return "" }
	opts.UpdateSnapshot = func() update.Snapshot {
		return update.Snapshot{State: update.StateIdle, Message: "尚未检查"}
	}
	opts.UpdateCheck = func() string { return "" }
	opts.UpdateDownloadInstall = func() string { return "" }
	opts.UpdateCancel = func() {}
	opts.UpdateAutoCheckGet = func() bool { return false }
	opts.UpdateAutoCheckSet = func(bool) string { return "" }

	c := &Console{
		opts:          opts,
		engCells:      map[string]func(string){},
		engSufCells:   map[string]func(string){},
		engLabelCells: map[string]func(string){},
	}
	_ = c.buildContent()
	c.refresh()

	for _, name := range []string{"tuner", "suggestions", "books", "log", "update"} {
		p := c.advPanels[name]
		if p == nil {
			t.Fatalf("面板 %s 未构建（回调接线缺失？）", name)
		}
		if h := p.MinSize().Height; h != advPanelH {
			t.Errorf("%s 盒高 %.1f ≠ 统一值 %.0f", name, h, advPanelH)
		}
	}

	// 库数不变性：列表区弹性吸收，盒高与行数解耦
	for _, n := range []int{1, 2, 3, 5, 9} {
		books := make([]BookRow, n)
		for i := range books {
			books[i] = BookRow{Name: string(rune('A'+i)) + "库", Entries: 1000, ModTime: now, Enabled: true}
		}
		c.opts.Books = func() []BookRow { return books }
		c.bookSig = "" // 强制重建列表行（签名比对会跳过同形态刷新）
		c.refresh()
		if h := c.advPanels["books"].MinSize().Height; h != advPanelH {
			t.Errorf("%d 库时开局库面板盒高 %.1f ≠ 统一值 %.0f（列表区应盒内滚动而非撑高）",
				n, h, advPanelH)
		}
	}
}
