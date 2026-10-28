package console

import (
	"image/color"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/theme"
)

// TestLerpColor 颜色插值：端点精确、中点介于两者之间。
func TestLerpColor(t *testing.T) {
	if got := lerpNRGBA(qiLine2, qiPrimary, 0); got != qiLine2 {
		t.Fatalf("t=0 应等于起点, got %v", got)
	}
	if got := lerpNRGBA(qiLine2, qiPrimary, 1); got != qiPrimary {
		t.Fatalf("t=1 应等于终点, got %v", got)
	}
	mid := lerpNRGBA(qiLine2, qiZhuLite, 0.5)
	if mid.R <= qiZhuLite.R || mid.R >= qiLine2.R { // 红通道须在两端口之间（白→亮红）
		t.Fatalf("中点红通道越界: %v", mid)
	}
}

// TestHiliteOutlineVisual outline 模式视觉进度：静默弱描边 / 激活朱砂加宽 /
// focus 焦点环直切覆盖。
func TestHiliteOutlineVisual(t *testing.T) {
	test.NewApp()
	test.ApplyTheme(t, newQiTheme())
	b := newHiliteButton("测试", func() { t.Fatal("不应触发") })
	r := b.CreateRenderer() // ring 在 CreateRenderer 中创建
	defer r.Destroy()

	b.applyVisual(0)
	if got, ok := b.ring.StrokeColor.(color.NRGBA); !ok || got != qiLine2 {
		t.Fatalf("静默描边 = %v, want qiLine2", b.ring.StrokeColor)
	}
	if b.ring.StrokeWidth != 1 {
		t.Fatalf("静默描边宽 = %v, want 1", b.ring.StrokeWidth)
	}

	b.applyVisual(1)
	if got, ok := b.ring.StrokeColor.(color.NRGBA); !ok || got != qiZhuLite {
		t.Fatalf("激活描边 = %v, want qiZhuLite", b.ring.StrokeColor)
	}
	if b.ring.StrokeWidth != 2 {
		t.Fatalf("激活描边宽 = %v, want 2", b.ring.StrokeWidth)
	}

	b.focused = true
	b.applyVisual(0)
	if got, ok := b.ring.StrokeColor.(color.NRGBA); !ok || got != qiFocus {
		t.Fatalf("focus 描边 = %v, want qiFocus", b.ring.StrokeColor)
	}
	if b.ring.StrokeWidth != 2 {
		t.Fatalf("focus 描边宽 = %v, want 2", b.ring.StrokeWidth)
	}
}

// TestHiliteGlowVisual glow 模式：静默无 Shadow，激活朱砂外发光（blur 与 alpha 随进度）。
func TestHiliteGlowVisual(t *testing.T) {
	test.NewApp()
	test.ApplyTheme(t, newQiTheme())
	b := newGlowButton("测试", func() { t.Fatal("不应触发") })
	r := b.CreateRenderer()
	defer r.Destroy()

	b.applyVisual(0)
	if b.ring.Shadow.BlurRadius != 0 {
		t.Fatalf("静默态不应有发光, blur=%v", b.ring.Shadow.BlurRadius)
	}

	b.applyVisual(1)
	if b.ring.Shadow.BlurRadius != glowBlur {
		t.Fatalf("激活发光 blur = %v, want %v", b.ring.Shadow.BlurRadius, glowBlur)
	}
	if got, ok := b.ring.Shadow.Color.(color.NRGBA); !ok || got != qiGlow {
		t.Fatalf("发光色 = %v, want qiGlow", b.ring.Shadow.Color)
	}
	if b.ring.Shadow.Variant != canvas.DropShadow {
		t.Fatalf("Variant = %v, want DropShadow", b.ring.Shadow.Variant)
	}
}

// TestHiliteRenderer 复合渲染器：描边层垫底、布局铺满、MinSize 沿用原生。
func TestHiliteRenderer(t *testing.T) {
	test.NewApp()
	test.ApplyTheme(t, newQiTheme())
	b := newHiliteButton("描边按钮", func() {})
	r := b.CreateRenderer()

	if objs := r.Objects(); len(objs) == 0 || objs[0] != b.ring {
		t.Fatal("描边层应垫在渲染树最前")
	}
	size := fyne.NewSize(120, 36)
	r.Layout(size)
	if b.ring.Size() != size {
		t.Fatalf("描边层应铺满按钮: got %v", b.ring.Size())
	}
	if r.MinSize().Width < 60 {
		t.Fatalf("MinSize 异常收缩: %v", r.MinSize())
	}
	r.Destroy()
}

// TestQiThemeRadii 圆角 token 体系（设计稿四级 8/12 对齐；bentoTile 自绘 12 与
// CardRadius 同值，两处勿失配）。
func TestQiThemeRadii(t *testing.T) {
	th := newQiTheme()
	cases := map[fyne.ThemeSizeName]float32{
		theme.SizeNameButtonRadius: 8,
		theme.SizeNameCardRadius:   12,
		theme.SizeNameDialogRadius: 12,
		theme.SizeNameMenuRadius:   8,
		theme.SizeNamePopupRadius:  8,
		theme.SizeNameInputRadius:  8,
	}
	for name, want := range cases {
		if got := th.Size(name); got != want {
			t.Errorf("Size(%s) = %v, want %v", name, got, want)
		}
	}
}
