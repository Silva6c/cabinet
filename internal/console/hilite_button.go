// hilite_button.go 描边高亮按钮（棋韵视觉一期：设计稿 §5.1/§5.2"次级按钮 =
// 半透明底 + 1dp 描边"的 PC 端实现，2026-10-28 用户拍板）。
//
// 为什么不能走主题层：Fyne 原生 Button 的 applyTheme 只设填充色与圆角、从不设
// 描边（v2.8.2 widget/button.go applyTheme），主题语义里按钮没有描边 token——
// 只能在 renderer 层动手。这里用**值嵌入**共享 BaseWidget：覆写 CreateRenderer
// 复用原生渲染树并在最前垫一层描边 Rectangle；hover/focus 事件覆写后转发给
// 原生（保留涟漪/键盘空格/禁用等全部行为），描边色宽与外发光按状态 120ms 过渡
// （设计稿 §4.5 提示性动效档，遵循 ShowAnimations 开关）。
//
// 【已知坑】Button.CreateRenderer 内部 ExtendBaseWidget(嵌入 Button) 会把 super
// 改写成嵌入 Button（button.go CreateRenderer 的副作用），必须在其后再次
// ExtendBaseWidget(b) 修回，否则后续 super 分派（Tapped 的焦点、涟漪动画）漂移。
// 另：canvas.Refresh 只上报重绘不重入 renderer.Refresh，Refresh 链安全。
package console

import (
	"image/color"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/widget"
)

const (
	hiliteDuration = 120 * time.Millisecond // hover/focus 过渡（拍板：提示性动效）
	outlineRadius  = 8                      // 与主题 SizeNameButtonRadius 对齐（两处勿失配）
	glowBlur       = float32(10)            // 主按钮外发光半径（DropShadow）
)

// hiliteMode 视觉模式：描边（次级按钮）/ 外发光（主按钮，原生实心填充不变）。
type hiliteMode int

const (
	hiliteOutline hiliteMode = iota
	hiliteGlow
)

// hiliteButton 带描边高亮的按钮。
type hiliteButton struct {
	widget.Button
	mode     hiliteMode
	hovered  bool
	focused  bool
	progress float32           // 视觉进度 0（静默）→1（激活）；动画插值驱动
	anim     *fyne.Animation   // 进行中的过渡（切换时 Stop 防抖）
	ring     *canvas.Rectangle // 垫底的描边/发光层（glow 模式填充与按钮同色隐形）
}

// newHiliteButton 描边按钮：静默 1dp 弱描边（qiLine2），hover 120ms 过渡到
// 朱砂 1.5dp，focus 2dp 焦点环；底色/文字色沿用原生 Importance 语义。
func newHiliteButton(label string, onTapped func()) *hiliteButton {
	return newHiliteButtonMode(label, onTapped, hiliteOutline)
}

// newGlowButton 外发光按钮（主按钮）：实心填充不变，hover 朱砂外发光
// blur 0→10 过渡——"确认式"动作（放行/配对/安装）的视觉强调。
func newGlowButton(label string, onTapped func()) *hiliteButton {
	return newHiliteButtonMode(label, onTapped, hiliteGlow)
}

func newHiliteButtonMode(label string, onTapped func(), mode hiliteMode) *hiliteButton {
	b := &hiliteButton{mode: mode}
	b.Text = label
	b.OnTapped = onTapped
	b.ExtendBaseWidget(b)
	return b
}

// CreateRenderer 复用原生渲染树 + 垫描边层（坑与规避见文件头）。
func (b *hiliteButton) CreateRenderer() fyne.WidgetRenderer {
	native := b.Button.CreateRenderer() // 副作用：super 被改写成嵌入 Button
	b.ExtendBaseWidget(b)               // 修回 super → hiliteButton

	b.ring = canvas.NewRectangle(qiSurface) // glow：同色垫底不可见，Shadow 溢出可见
	b.ring.CornerRadius = outlineRadius
	if b.mode == hiliteOutline {
		b.ring.StrokeColor, b.ring.StrokeWidth = qiLine2, 1
	} else {
		b.ring.StrokeWidth = 0
	}
	b.applyVisual(b.progress) // 按当前状态初始化视觉——渲染器重建（切主题等）不丢 hover/焦点态
	objects := append([]fyne.CanvasObject{b.ring}, native.Objects()...)
	return &hiliteRenderer{
		objects: objects,
		native:  native,
		ring:    b.ring,
		btn:     b,
	}
}

// MouseIn/MouseOut/Focus 覆写：driver 只调最外层 widget——转发给嵌入 Button
// 保持原生背景混合，再驱动描边/发光过渡。
func (b *hiliteButton) MouseIn(*desktop.MouseEvent) {
	b.hovered = true
	b.Button.MouseIn(nil)
	b.startTransition(true)
}

func (b *hiliteButton) MouseOut() {
	b.hovered = false
	b.Button.MouseOut()
	b.startTransition(false)
}

func (b *hiliteButton) FocusGained() {
	b.focused = true
	b.Button.FocusGained()
	b.applyVisual(b.progress)
}

func (b *hiliteButton) FocusLost() {
	b.focused = false
	b.Button.FocusLost()
	b.applyVisual(b.progress)
}

// startTransition 120ms 过渡到目标态（on=true 激活）；动画关闭时直切。
func (b *hiliteButton) startTransition(on bool) {
	target := float32(0)
	if on {
		target = 1
	}
	if b.anim != nil {
		b.anim.Stop()
		b.anim = nil
	}
	if !fyne.CurrentApp().Settings().ShowAnimations() {
		b.progress = target
		b.applyVisual(target)
		return
	}
	from := b.progress
	b.anim = fyne.NewAnimation(hiliteDuration, func(t float32) {
		b.progress = from + (target-from)*t
		b.applyVisual(b.progress)
	})
	b.anim.Curve = fyne.AnimationEaseOut
	b.anim.Start()
}

// applyVisual 把进度 p（0 静默 → 1 激活）落到描边层；focus 焦点环直切覆盖
// （键盘导航无过渡语义）。glow 模式控制 Shadow，outline 模式控制描边色宽。
func (b *hiliteButton) applyVisual(p float32) {
	if b.ring == nil {
		return
	}
	if b.mode == hiliteOutline {
		if b.focused {
			b.ring.StrokeColor, b.ring.StrokeWidth = qiFocus, 2
		} else {
			// hover 用悬停亮红（设计稿 §4.2 qiZhuLite）+ 2dp——1.5dp 朱砂在
			// 150% DPI 缩放下感知太弱（2026-10-28 真机验收反馈后增强）
			b.ring.StrokeColor = lerpNRGBA(qiLine2, qiZhuLite, p)
			b.ring.StrokeWidth = 1 + p
		}
		b.ring.Refresh()
		return
	}
	// glow：外发光随 p 渐显渐隐
	if p <= 0.01 && !b.focused {
		b.ring.Shadow = canvas.Shadow{}
	} else {
		alpha := float32(0x66) * p
		b.ring.Shadow = canvas.Shadow{
			Color:      color.NRGBA{R: 0xC3, G: 0x54, B: 0x40, A: uint8(alpha)},
			BlurRadius: glowBlur * p,
			Variant:    canvas.DropShadow,
		}
	}
	b.ring.Refresh()
}

// hiliteRenderer 原生渲染树 + 描边层的复合渲染器。Fyne 无公开的 BaseRenderer
// （internal/widget 不可导入），五方法全量自实现；布局/尺寸委托原生。
type hiliteRenderer struct {
	objects []fyne.CanvasObject
	native  fyne.WidgetRenderer
	ring    *canvas.Rectangle
	btn     *hiliteButton
}

func (r *hiliteRenderer) Layout(s fyne.Size) {
	r.native.Layout(s)
	r.ring.Move(fyne.NewPos(0, 0))
	r.ring.Resize(s)
}

func (r *hiliteRenderer) MinSize() fyne.Size { return r.native.MinSize() }

func (r *hiliteRenderer) Objects() []fyne.CanvasObject { return r.objects }

func (r *hiliteRenderer) Destroy() { r.native.Destroy() }

func (r *hiliteRenderer) Refresh() {
	r.btn.applyVisual(r.btn.progress)
	r.native.Refresh() // 末尾：原生刷新含 canvas.Refresh(super)，不重入本 Refresh
}

// lerpNRGBA NRGBA 四通道直插：端点均为包内常量 NRGBA，直插保证 t=0/1 精确
// 还原原色（经 RGBA() 预乘空间插值会在半透明端点上偏色——qiLine2 A=0x29 实测）。
func lerpNRGBA(from, to color.NRGBA, t float32) color.NRGBA {
	mix := func(a, b uint8) uint8 {
		return uint8(float32(a) + (float32(b)-float32(a))*t)
	}
	return color.NRGBA{R: mix(from.R, to.R), G: mix(from.G, to.G), B: mix(from.B, to.B), A: mix(from.A, to.A)}
}
