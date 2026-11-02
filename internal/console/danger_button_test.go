package console

// danger_button_test.go 悬停变红按钮的回归（2026-11-02 vc15 真机实测"悬停
// 不红"后的根修验证）：
//  1. **真实事件链**——test.MoveMouse 走 driver 完整分发（hover 查找 →
//     MouseIn/MouseOut），不再用"直接调方法"式模拟（后者证明逻辑、证明不了
//     事件可达）。
//  2. **像素断言**——悬停态用 software 画布采按钮中心像素，验证渲染层真的
//     画出红。字段/事件断言都替代不了它：vc15 的失效恰是" Importance 字段
//     切到位、画面被主题 hover blend 盖掉"（qiTheme 的 Hover 色不透明，
//     Danger 红悬停瞬间被全盖——根因见 dangerHoverBtn 类型注释）。
//
// 坐标说明：fyne 的 Position 是相对直接父容器的，绝对坐标须沿父链累加
// （Scroll 的 Content 不是 Container，要单独递归——walk 已处理）。

import (
	"image/color"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/driver/software"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"
)

func TestDangerButtonHoverRed(t *testing.T) {
	a := test.NewApp()
	a.Settings().SetTheme(newQiTheme())

	opts := demoOptions()
	opts.BookImportName = func(string) string { return "" }
	opts.BookImportFile = func(string) string { return "" }
	opts.BookMove = func(string, int) string { return "" }
	opts.BookToggle = func(string, bool) string { return "" }
	opts.BookRemove = func(string) string { return "" }
	opts.CloudFirstGet = func() bool { return false }
	opts.CloudFirstSet = func(bool) string { return "" }
	opts.EndgameGet = func() bool { return false }
	opts.EndgameSet = func(bool) string { return "" }
	opts.SkipBooksGet = func() bool { return false }
	opts.SkipBooksSet = func(bool) string { return "" }

	c := &Console{
		opts:          opts,
		engCells:      map[string]func(string){},
		engSufCells:   map[string]func(string){},
		engLabelCells: map[string]func(string){},
	}
	content := c.buildContent()
	c.refresh()
	c.selectedBook = "桔库"
	c.syncBookActionButtons()
	c.rebuildBookRows()
	c.advStack.Add(c.advPanels["books"])

	w := a.NewWindow("t")
	w.SetContent(content)
	w.Resize(fyne.NewSize(1020, 900)) // 触发布局，Position/Size 就绪

	var hover *dangerHoverBtn
	var absX, absY float32
	var findPos func(o fyne.CanvasObject, dx, dy float32)
	findPos = func(o fyne.CanvasObject, dx, dy float32) {
		p := o.Position()
		if d, ok := o.(*dangerHoverBtn); ok && hover == nil {
			hover = d
			absX, absY = dx+p.X, dy+p.Y
			return
		}
		if sc, ok := o.(*container.Scroll); ok {
			findPos(sc.Content, dx+p.X, dy+p.Y)
			return
		}
		if box, ok := o.(*fyne.Container); ok {
			for _, k := range box.Objects {
				findPos(k, dx+p.X, dy+p.Y)
			}
		}
	}
	findPos(content, 0, 0)
	if hover == nil {
		t.Fatal("内容树未找到 dangerHoverBtn（未上树？）")
	}
	if c.bookDelBtn != &hover.Button {
		t.Fatal("bookDelBtn 未指向共享 Button 结构（启停会落到副本上）")
	}
	size := hover.Size()
	if size.Width <= 0 || size.Height <= 0 {
		t.Fatalf("按钮未布局: abs=(%.0f,%.0f) size=%v", absX, absY, size)
	}
	center := fyne.NewPos(absX+size.Width/2, absY+size.Height/2)

	// 真实事件：移入 → 警示罩显（红罩 alpha>0）；移开 → 隐；再移入 → 往返稳定
	test.MoveMouse(w.Canvas(), center)
	if a8 := alphaOf(hover.tint.FillColor); a8 == 0 {
		t.Fatalf("真实 MouseIn 后警示罩未显（alpha=0）")
	}
	test.MoveMouse(w.Canvas(), fyne.NewPos(2, 2))
	if a8 := alphaOf(hover.tint.FillColor); a8 != 0 {
		t.Fatalf("真实 MouseOut 后警示罩未隐（alpha=%d）", a8)
	}
	test.MoveMouse(w.Canvas(), center)
	if a8 := alphaOf(hover.tint.FillColor); a8 == 0 {
		t.Fatalf("二次 MouseIn 后警示罩未显（往返失效）")
	}

	// 像素断言：悬停态渲染红色系（R 显著高于 G/B），移出后恢复非红
	cx, cy := int(center.X), int(center.Y)
	sample := func() (uint8, uint8, uint8) {
		cv := software.NewCanvas()
		cv.SetContent(content)
		cv.Resize(fyne.NewSize(1020, 900))
		img := cv.Capture()
		// 中心可能是文字像素：竖向多点采样取 R 最高的一个（文字白 R≈G≈B，
		// 不会干扰红底判定）
		var r8, g8, b8 uint8
		best := uint32(0)
		for _, dy := range []int{-8, -4, 0, 4, 8} {
			pr, pg, pb, _ := img.At(cx, cy+dy).RGBA()
			if pr > best {
				best, r8, g8, b8 = pr, uint8(pr>>8), uint8(pg>>8), uint8(pb>>8)
			}
		}
		return r8, g8, b8
	}
	r8, g8, b8 := sample()
	isRed := r8 > 120 && r8 > g8+40 && r8 > b8+40
	if !isRed {
		t.Fatalf("悬停态按钮中心像素 = #%02X%02X%02X，不是红色系（渲染层没画出警示红）", r8, g8, b8)
	}
	test.MoveMouse(w.Canvas(), fyne.NewPos(2, 2))
	r8, g8, b8 = sample()
	if isRedLike(r8, g8, b8) {
		t.Fatalf("移出后按钮中心像素 = #%02X%02X%02X，仍为红色系（红罩未恢复）", r8, g8, b8)
	}
}

// isRedLike 红色系判定：R 显著高于 G/B（排除深灰背景与白字）。
func isRedLike(r8, g8, b8 uint8) bool {
	return r8 > 120 && r8 > g8+40 && r8 > b8+40
}

func alphaOf(c color.Color) uint8 {
	if c == nil {
		return 0
	}
	_, _, _, a := c.RGBA()
	return uint8(a >> 8)
}

// TestDangerButtonDisabledNoRed 禁用态悬停不变红（不可删时无警示语义）。
func TestDangerButtonDisabledNoRed(t *testing.T) {
	a := test.NewApp()
	a.Settings().SetTheme(newQiTheme())
	b := newDangerButton("删除", func() {})
	b.ExtendBaseWidget(b)
	b.Disable()
	b.MouseIn(nil)
	if a8 := alphaOf(b.tint.FillColor); a8 != 0 {
		t.Fatalf("禁用态 MouseIn 后警示罩 alpha=%d，应保持透明", a8)
	}
	b.MouseOut()
	if a8 := alphaOf(b.tint.FillColor); a8 != 0 {
		t.Fatalf("禁用态 MouseOut 后警示罩 alpha=%d，应保持透明", a8)
	}
	if b.Importance != widget.MediumImportance {
		t.Fatalf("Importance 应恒为 Medium（不参与主题 blend）：%d", b.Importance)
	}
}
