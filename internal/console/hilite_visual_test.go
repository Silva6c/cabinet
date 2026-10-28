package console

import (
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"
)

// TestHiliteMouseTransition 事件接线：MouseIn/MouseOut 正确置态并启停过渡
// 动画（视觉像素由 TestHiliteVisualShot 渲染验收）。
func TestHiliteMouseTransition(t *testing.T) {
	test.NewApp()
	test.ApplyTheme(t, newQiTheme())
	b := newHiliteButton("x", nil)

	b.MouseIn(&desktop.MouseEvent{})
	if !b.hovered {
		t.Fatal("MouseIn 后 hovered 应为 true")
	}
	if b.anim == nil {
		t.Fatal("MouseIn 后应启动过渡动画")
	}
	b.anim.Stop()

	b.MouseOut()
	if b.hovered {
		t.Fatal("MouseOut 后 hovered 应为 false")
	}
}

// TestHiliteVisualShot 渲染 hover/发光状态矩阵成图（目视验收：
// outline 静默→hover 朱砂描边→focus 焦点环；glow 静默→hover 外发光）。
// 需 CABINET_SHOT=1（与控制台截图同一开关）；产物 docs/design/hilite-states.png。
func TestHiliteVisualShot(t *testing.T) {
	if os.Getenv("CABINET_SHOT") == "" {
		t.Skip("需 CABINET_SHOT=1")
	}
	test.NewApp()
	test.ApplyTheme(t, newQiTheme())

	// mk 注入目标态（progress 直定，跳过动画——渲染验收要确定性像素）
	mk := func(label string, mode hiliteMode, imp widget.Importance, activate, focus bool) fyne.CanvasObject {
		var b *hiliteButton
		if mode == hiliteOutline {
			b = newHiliteButton(label, nil)
		} else {
			b = newGlowButton(label, nil)
		}
		b.Importance = imp
		if focus {
			b.focused = true
		}
		p := float32(0)
		if activate {
			p = 1
		}
		b.progress = p
		return b
	}

	row1 := container.NewHBox(
		mk("静默", hiliteOutline, widget.MediumImportance, false, false),
		mk("悬停", hiliteOutline, widget.MediumImportance, true, false),
		mk("焦点", hiliteOutline, widget.MediumImportance, false, true),
	)
	row2 := container.NewHBox(
		mk("实心静默", hiliteGlow, widget.HighImportance, false, false),
		mk("发光悬停", hiliteGlow, widget.HighImportance, true, false),
	)
	cap := qiCaption("描边按钮：静默 / hover 朱砂 1.5dp / focus 焦点环 —— 主按钮：实心 / hover 朱砂外发光")
	content := container.NewVBox(
		container.New(padAll(10)), // 顶部留白：给 glow 光晕溢出空间
		row1,
		container.New(padAll(8)),
		row2,
		container.New(padAll(8)),
		cap,
	)

	w := test.NewWindow(content)
	w.Resize(fyne.NewSize(560, 170))
	img := w.Canvas().Capture()

	// 输出相对仓根：go test 的 CWD 是包目录，向上两层回仓根
	out := filepath.Join("..", "..", "docs", "design", "hilite-states.png")
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(out)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
}

// TestGlowShadowRenders 发光层渲染冒烟：glow 激活态经软件画布渲染不 panic、
// 按钮区域像素发生变化的直观前置于视觉验收（防止 Shadow 被渲染器跳过的回归）。
func TestGlowShadowRenders(t *testing.T) {
	test.NewApp()
	test.ApplyTheme(t, newQiTheme())

	mk := func(activate bool) fyne.CanvasObject {
		b := newGlowButton("安装", nil)
		b.Importance = widget.HighImportance
		p := float32(0)
		if activate {
			p = 1
		}
		b.progress = p
		return container.New(padAll(10), b)
	}
	base := test.NewWindow(mk(false))
	base.Resize(fyne.NewSize(160, 80))
	active := test.NewWindow(mk(true))
	active.Resize(fyne.NewSize(160, 80))

	baseImg := base.Canvas().Capture()
	activeImg := active.Canvas().Capture()
	b1 := baseImg.Bounds()
	diff := 0
	for y := 0; y < b1.Dy(); y++ {
		for x := 0; x < b1.Dx(); x++ {
			r1, g1, bl1, _ := baseImg.At(x, y).RGBA()
			r2, g2, bl2, _ := activeImg.At(x, y).RGBA()
			if r1 != r2 || g1 != g2 || bl1 != bl2 {
				diff++
			}
		}
	}
	if diff == 0 {
		t.Fatal("发光激活态与静默态逐像素一致——Shadow 未参与渲染（回归）")
	}
}
