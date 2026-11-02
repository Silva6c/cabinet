// widgets.go 棋韵控件组合（便当盒 tile / 状态 chip / 分区头 / 文本与布局小件）：
// 全部用 canvas 图元 + 容器组合实现——Fyne 无软阴影与模糊，分层靠 1dp 描边
// （qiLine1/2）与间距；不引入自定义 renderer，维护成本优先。
//
// 【密度关键】密集区域（tile 标签/数值/分区头）一律用 canvas.Text：
// 实测 widget.Label 高度 ≈ 字号像素 + 16（内边距），几行叠加就把 900×540
// 挤出滚动条；canvas.Text 高度即字高（11pt≈15px / 22pt≈30px）。
package console

import (
	"fmt"
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// ---------- 文本小件 ----------

// qiSize 读取当前主题字号（canvas.Text 需要显式尺寸）
func qiSize(name fyne.ThemeSizeName) float32 {
	return fyne.CurrentApp().Settings().Theme().Size(name)
}

// qiText 构造 canvas.Text（字色/字号/等宽显式给出）
func qiText(text string, size float32, col color.Color, mono, bold bool) *canvas.Text {
	t := canvas.NewText(text, col)
	t.TextSize = size
	t.TextStyle = fyne.TextStyle{Monospace: mono, Bold: bold}
	return t
}

// qiCaption 小号弱化文字（11pt 灰；tile 内标签/图注统一用它）
func qiCaption(text string) *canvas.Text {
	return qiText(text, qiSize(theme.SizeNameCaptionText), qiMuted, false, false)
}

// qiDynamic 可更新 canvas.Text：返回对象与 setter（重复值不触发重绘）
func qiDynamic(text string, size float32, col color.Color, mono, bold bool) (*canvas.Text, func(string)) {
	t := qiText(text, size, col, mono, bold)
	set := func(v string) {
		if t.Text == v {
			return
		}
		t.Text = v
		t.Refresh()
	}
	return t, set
}

// ---------- 布局小件 ----------

// pad 内边距容器（container.NewPadded 固定 4px，tile 需要 12–14px 呼吸感）
type pad struct{ top, right, bottom, left float32 }

func padAll(v float32) *pad { return &pad{top: v, right: v, bottom: v, left: v} }

func (p *pad) Layout(objs []fyne.CanvasObject, size fyne.Size) {
	for _, o := range objs {
		o.Move(fyne.NewPos(p.left, p.top))
		o.Resize(fyne.NewSize(size.Width-p.left-p.right, size.Height-p.top-p.bottom))
	}
}

func (p *pad) MinSize(objs []fyne.CanvasObject) fyne.Size {
	var w, h float32
	for _, o := range objs {
		s := o.MinSize()
		if s.Width > w {
			w = s.Width
		}
		if s.Height > h {
			h = s.Height
		}
	}
	return fyne.NewSize(w+p.left+p.right, h+p.top+p.bottom)
}

// vbox 纵向堆叠（自定义间距；Fyne 的 VBox 间距固定为主题 padding=4，不够透气）
type vbox struct{ gap float32 }

func (v *vbox) Layout(objs []fyne.CanvasObject, size fyne.Size) {
	y := float32(0)
	for _, o := range objs {
		// 隐藏对象不占位：防火墙横幅默认 Hide，此前仍按 MinSize 计入布局，
		// 折叠态窗口凭空吃掉一行高度（2026-10-18"高级功能被裁一块"的主因之一）
		if !o.Visible() {
			continue
		}
		h := o.MinSize().Height
		o.Move(fyne.NewPos(0, y))
		o.Resize(fyne.NewSize(size.Width, h))
		y += h + v.gap
	}
}

func (v *vbox) MinSize(objs []fyne.CanvasObject) fyne.Size {
	var w, h float32
	first := true
	for _, o := range objs {
		if !o.Visible() {
			continue
		}
		s := o.MinSize()
		if s.Width > w {
			w = s.Width
		}
		h += s.Height
		if !first {
			h += v.gap
		}
		first = false
	}
	return fyne.NewSize(w, h)
}

// vboxFlex 纵向堆叠 + 弹性子项：布局同 vbox，但第 flex 个子项额外吸收
// 容器高减去其余子项最小高（与间距）的剩余部分——统一盒高（advPanelH）下
// 让滚动/列表区填满盒子而不是在盒底留白条。flex 索引按可见子项序列计；
// MinSize 与 vbox 一致（弹性只影响分配不影响最小高，盒不会被裁）。
type vboxFlex struct {
	gap  float32
	flex int
}

func (v *vboxFlex) Layout(objs []fyne.CanvasObject, size fyne.Size) {
	// 可见子项最小高合计（间距只算可见相邻对），得出弹性子项可吸收的余量
	mins := make([]float32, len(objs))
	total := float32(0)
	nvis := 0
	for i, o := range objs {
		if !o.Visible() {
			continue
		}
		mins[i] = o.MinSize().Height
		total += mins[i]
		if nvis > 0 {
			total += v.gap
		}
		nvis++
	}
	extra := size.Height - total
	if extra < 0 {
		extra = 0
	}
	y := float32(0)
	for i, o := range objs {
		if !o.Visible() {
			continue
		}
		h := mins[i]
		if i == v.flex {
			h += extra // 弹性子项吸收剩余高（其最小高已含在 total 里）
		}
		o.Move(fyne.NewPos(0, y))
		o.Resize(fyne.NewSize(size.Width, h))
		y += h + v.gap
	}
}

func (v *vboxFlex) MinSize(objs []fyne.CanvasObject) fyne.Size {
	var w, h float32
	first := true
	for _, o := range objs {
		if !o.Visible() {
			continue
		}
		s := o.MinSize()
		if s.Width > w {
			w = s.Width
		}
		h += s.Height
		if !first {
			h += v.gap
		}
		first = false
	}
	return fyne.NewSize(w, h)
}

// sizedBox 固定最小尺寸容器（状态点/图标等小件）
type sizedBox struct{ w, h float32 }

func (s sizedBox) Layout(objs []fyne.CanvasObject, size fyne.Size) {
	for _, o := range objs {
		o.Move(fyne.NewPos(0, 0))
		o.Resize(size)
	}
}

func (s sizedBox) MinSize([]fyne.CanvasObject) fyne.Size { return fyne.NewSize(s.w, s.h) }

// cols2 两等分列（bento 行）：每列宽度恒定为 (宽-gap)/2。
// 不用 container.NewGridWithColumns：fyne 网格以"最大子项最小宽"撑开列宽，
// 宽内容会把整行挤出窗口造成横向滚动；bento 要求列宽由窗口决定、内容自行收敛。
type cols2 struct{ gap float32 }

func (g *cols2) Layout(objs []fyne.CanvasObject, size fyne.Size) {
	w := (size.Width - g.gap) / 2
	x := float32(0)
	for _, o := range objs {
		if !o.Visible() {
			continue
		}
		o.Move(fyne.NewPos(x, 0))
		o.Resize(fyne.NewSize(w, size.Height))
		x += w + g.gap
	}
}

// MinSize 只承诺"半列可读宽"×2：列宽实际由窗口驱动，内容超宽时收敛而非撑爆
func (g *cols2) MinSize(objs []fyne.CanvasObject) fyne.Size {
	var h float32
	for _, o := range objs {
		if s := o.MinSize(); s.Height > h {
			h = s.Height
		}
	}
	return fyne.NewSize(2*220+g.gap, h)
}

// ---------- 组合件 ----------

// bentoTile 便当盒 tile：圆角炭面 + 1dp 描边 + 内边距（描边分层代替阴影）。
// 内边距 8（2026-10-30 用户定稿 12→8：设计哲学清爽简洁，减边框占地让信息
// 更多；圆角 12 与主题 SizeNameCardRadius 同步，theme.go 两处勿失配）。
func bentoTile(content fyne.CanvasObject) fyne.CanvasObject {
	rect := canvas.NewRectangle(qiSurface)
	rect.CornerRadius = 12
	rect.StrokeColor = qiLine2
	rect.StrokeWidth = 1
	return container.NewStack(rect, container.New(padAll(8), content))
}

// advPanelH 05 高级面板统一盒高（2026-11-02 用户定稿：五个面板便当盒改成
// 统一大小，取值 = 此前最高面板（日志 196）稍大 → 220）。实现是"最小高"语义：
// Stack 里垫不可见占位把盒撑到统一高，内容 min 超过它仍随内容长（绝不裁字）；
// 各面板的滚动/列表区经 vboxFlex 弹性吸收剩余高（库多时盒内滚动，盒高恒定）。
// 减 16 = bentoTile 的 padAll(8) 上下内边距（占位垫在内容层里）。
const advPanelH float32 = 220

// wrapBento 高级面板统一包便当盒卡片——展开态与折叠态 tile 同语言
// （此前四面板是裸 VBox，展开后质感骤降）；nil 穿透（面板不可用语义不变）。
// 占位只贡献最小高不绘制（sizedBox 作布局、无子对象：Layout 空转，MinSize
// 即占位尺寸），盒高恒为 advPanelH 与内容多少无关。
func wrapBento(p fyne.CanvasObject) fyne.CanvasObject {
	if p == nil {
		return nil
	}
	return bentoTile(container.NewStack(
		container.New(sizedBox{w: 10, h: advPanelH - 16}), p))
}

// sectionHeader 分区头：编号（朱砂等宽小字，设计稿 §06 tile-hd .num）＋ 标题 ＋
// 右侧挂件（自动两端对齐）
func sectionHeader(num, title string, extra ...fyne.CanvasObject) fyne.CanvasObject {
	numTxt := qiText(num, qiSize(theme.SizeNameCaptionText), qiPrimary, true, false)
	titleTxt := qiText(title, 16, qiForeground, false, true)
	row := container.NewHBox(numTxt, titleTxt)
	if len(extra) > 0 {
		row.Add(layout.NewSpacer())
		for _, e := range extra {
			row.Add(e)
		}
	}
	return row
}

// statBig 大数字（22pt sans Bold）＋ 可选后缀小字 ＋ 小标签。
// 【雷区】不用等宽字体渲染大数字：mono 族的 CJK 回退字形与 sans 族不是同一张脸，
// "就绪"这类含中文的状态字会与全文异形（2026-10-02 实测教训）；数字对齐靠
// 值格式收敛（fmtCompactN 紧凑短式）而非字体族。
// 返回标签/主值/后缀三个 setter（后缀空串隐藏，如 "n/s"、"/24"；标签可动态改——
// 时间制仪表"深度 / 目标"→"用时 / 上限"）。
func statBig(label string, col color.Color) (fyne.CanvasObject, func(string), func(string), func(string)) {
	num, set := qiDynamic("—", qiSize(meterSizeName), col, false, true)
	suf := qiText("", qiSize(theme.SizeNameCaptionText), qiMuted, false, false)
	setSuf := func(v string) {
		if suf.Text == v {
			return
		}
		suf.Text = v
		suf.Refresh()
	}
	lab := qiCaption(label)
	setLabel := func(v string) {
		if lab.Text == v {
			return
		}
		lab.Text = v
		lab.Refresh()
	}
	return container.NewVBox(lab, container.NewHBox(num, suf)), setLabel, set, setSuf
}

// fpChip 证书指纹分组短式 chip（设计稿 §06：1dp 描边圆角 + 内嵌等宽 hex）。
// 只读核对用（完整指纹生成二维码时原样使用），样式对齐 statusChip 的胶囊语言。
type fpChip struct {
	box fyne.CanvasObject
	txt *canvas.Text
}

func (c *fpChip) object() fyne.CanvasObject { return c.box }

func newFpChip() *fpChip {
	txt := qiText("—", qiSize(theme.SizeNameCaptionText), qiForeground, true, false)
	rect := canvas.NewRectangle(qiInputBg)
	rect.CornerRadius = 6
	rect.StrokeColor = qiLine2
	rect.StrokeWidth = 1
	content := container.New(
		&pad{top: 4, right: 10, bottom: 4, left: 10},
		txt,
	)
	return &fpChip{box: container.NewStack(rect, content), txt: txt}
}

// set 更新指纹文案；空串 = 未启用锁定（灰占位说明）
func (c *fpChip) set(text string) {
	if text == "" {
		c.txt.Color = qiMuted
		text = "未启用锁定——手机端不校验证书"
	} else {
		c.txt.Color = qiForeground
		text = "SHA-256 " + text
	}
	if c.txt.Text != text {
		c.txt.Text = text
		c.txt.Refresh()
	}
	c.txt.Refresh()
}

// hbar 细进度条（高 6）：fyne 的 ProgressBar 默认太厚，与 tile 的密度不符。
// 轨道 + 按比例宽度的填充。
type hbar struct {
	frac        float32
	track, fill *canvas.Rectangle
}

func (h *hbar) Layout(objs []fyne.CanvasObject, size fyne.Size) {
	if len(objs) < 2 {
		return
	}
	objs[0].Move(fyne.NewPos(0, 0))
	objs[0].Resize(size)
	objs[1].Move(fyne.NewPos(0, 0))
	objs[1].Resize(fyne.NewSize(size.Width*h.frac, size.Height))
}

func (h *hbar) MinSize([]fyne.CanvasObject) fyne.Size { return fyne.NewSize(120, 6) }

// newHBar 构造细进度条：返回组合对象与 setter（0–1）
func newHBar() (fyne.CanvasObject, func(float32)) {
	track := canvas.NewRectangle(qiLine1)
	track.CornerRadius = 3
	fill := canvas.NewRectangle(qiPrimary)
	fill.CornerRadius = 3
	h := &hbar{track: track, fill: fill}
	box := container.New(h, track, fill)
	set := func(v float32) {
		if v < 0 {
			v = 0
		}
		if v > 1 {
			v = 1
		}
		if h.frac == v {
			return
		}
		h.frac = v
		box.Refresh() // 触发重排重绘（Layout 读 frac 计算填充宽度）
	}
	return box, set
}

// ---------- 连接区横排行（表格式便当盒，2026-10-20 v3） ----------

// clientColW 连接区横排列宽（尾列"最后 position"锁 260——fen 摘要 16 字符
// 等宽约 140px，260 给足余量。尾列不能靠 HBox 弹性：带截断的 Label MinSize
// 只报最小宽，HBox 逐项按 MinSize 排布不做弹性分配，不锁宽就只剩一个省略号
// ——v3/v4 两轮实测）。
// 2026-10-28 设备昵称功能加"设备"列（第二列）：窗口 960px，新总宽 820+24
// 边距仍在可用范围内；客户端列收窄（cabinet-phone 等宽 ~110px 足够）。
// 表头行与数据行共用此列宽，保证竖向对齐。
var clientColW = []float32{110, 120, 130, 70, 70, 60, 260}

// clientHeaderRow 表头行（公用设施，用户要求"会话/客户端等排在最上面，和之前
// 类似"）：七列标题横排、恒显不随数据行滚动。七列全部 sizedBox 锁宽（含尾列，
// 与数据行共用 clientColW——锁宽缘由见其注释）。
func clientHeaderRow() fyne.CanvasObject {
	titles := []string{"会话", "设备", "客户端", "版本", "RTT", "路径", "最后 position"}
	box := container.NewHBox()
	for i, t := range titles {
		txt := qiText(t, qiSize(theme.SizeNameCaptionText), qiMuted, false, true)
		box.Add(container.New(sizedBox{w: clientColW[i], h: 14}, txt))
	}
	// 左右 12 对齐 tile 内边距（bentoTile 的 padAll(12)）
	return container.New(&pad{left: 12, right: 12, top: 2, bottom: 2}, box)
}

// clientRow 会话数据行（v4 矮便当盒，2026-10-20）：一行一台设备，横排七列与
// 表头同构对齐（2026-10-28 加"设备"列，昵称功能）；行底恢复便当盒卡片质感
// （qiSurface 圆角底 + qiLine2 细描边，选中换朱砂描边——v2 卡片语言），行高
// ~30px 压到最矮。整行 Tappable：点击
// 回调 onTap(fullID)，选中态由 sectionClients 统一维护。列表装入自适应滚动
// 区（行数少时随内容、超过 100px 封顶内部滚动）——设备再多也只在"03 连接"
// 内部滚动，不挤兑其他区块（2026-10-20 用户要求）。
// 演进注记：v2 三行卡片被否（"不如初版表格顺眼、预留空间不够"）→ v3 素表格
// 行被否（"信息呢？便当盒呢？"——列宽锁死失效致六列塌缩成省略号+丢了卡片
// 质感）→ v4 矮便当盒行 + 修 widthBox 上树 bug。
type clientRow struct {
	widget.BaseWidget
	fullID string
	onTap  func(fullID string)

	// 【雷区变体】content/contentBox 是 *fyne.Container——statusChip 的"不可嵌
	// Container"雷区针对纯 CanvasObject 组合壳（无 renderer 则绘制遍历不进入）；
	// 本组件是真 widget（BaseWidget + CreateRenderer 返回该树），可以安全持有。
	contentBox *fyne.Container   // renderer 根（Stack：卡片底 + 横排单元格）
	content    *canvas.Rectangle // 卡片底（qiSurface；选中态换描边色/粗细）
	labels     []*widget.Label   // 七列值（顺序同表头；Truncation 截断保列对齐）
}

// widthBox 只锁宽度的布局（高度取孩子自然高）：连接区数据列对齐用——单元格
// 文本再长也不撑开列（配合 Label.Truncation 省略号），表头与数据行恒对齐。
// 【雷】容器与孩子必须整体上树：v3 曾从 container.New 返回值里抽 label 单独
// 使用，widthBox 布局根本没挂进 UI 树，而带 Truncation 的 Label MinSize 只报
// 一个字符宽——六列全部塌缩成省略号（2026-10-20 用户实测截图暴露）。
type widthBox struct{ w float32 }

func (w *widthBox) Layout(objs []fyne.CanvasObject, size fyne.Size) {
	for _, o := range objs {
		o.Move(fyne.NewPos(0, 0))
		o.Resize(fyne.NewSize(w.w, size.Height))
	}
}

// MinSize 宽度锁死，高度向孩子要（带 Truncation 的 Label 高度正常、宽度报
// 最小值——高度不能学它报 0，否则行体塌缩）。
func (w *widthBox) MinSize(objs []fyne.CanvasObject) fyne.Size {
	h := float32(0)
	for _, o := range objs {
		if s := o.MinSize(); s.Height > h {
			h = s.Height
		}
	}
	return fyne.NewSize(w.w, h)
}

// clientCell 固定宽单元格：widthBox 锁列宽（MinSize 不随文本涨），Label 的
// Truncation 在可用宽内画省略号（canvas.Text 无 Truncation 且 MinSize 随全文，
// 溢出绘制会串列——故数据行用 Label）。返回容器整体与 label 引用——容器上树
// 布局才生效，label 引用供回填文本（见 widthBox 雷注）。
func clientCell(txt string, mono, bold bool, w float32) (fyne.CanvasObject, *widget.Label) {
	lbl := &widget.Label{Text: txt, Truncation: fyne.TextTruncateEllipsis}
	lbl.TextStyle = fyne.TextStyle{Monospace: mono, Bold: bold}
	return container.New(&widthBox{w: w}, lbl), lbl
}

func newClientRow(onTap func(fullID string)) *clientRow {
	r := &clientRow{onTap: onTap}
	// UI 树在构造期建好（statusChip 同款模式）：set 可能在挂树/renderer 创建
	// 之前被调用（Add 后立即回填），惰性构建会在 set 里踩到 nil
	r.content = canvas.NewRectangle(qiSurface)
	r.content.CornerRadius = 8
	r.content.StrokeColor = qiLine2
	r.content.StrokeWidth = 1
	box := container.NewHBox()
	for i := 0; i < 7; i++ {
		// 列样式沿用旧表：会话/RTT/最后 position 低强调（灰调），设备/客户端名常规亮色
		// （Label 无 Color 字段，灰/亮用 Importance 两档——Low=disabled 灰）
		mono := i == 0 || i == 4 || i == 6
		// 七列全部锁宽（含尾列，见 clientColW 注）——单元格容器整体上树，
		// 抽 label 单独上树等于放弃锁宽布局（v3 六列塌缩省略号的根因）
		cell, lbl := clientCell("—", mono, i == 2, clientColW[i])
		if mono {
			lbl.Importance = widget.LowImportance
		} else {
			lbl.Importance = widget.MediumImportance
		}
		r.labels = append(r.labels, lbl)
		box.Add(cell)
	}
	padded := container.New(&pad{top: 5, right: 12, bottom: 5, left: 12}, box)
	r.contentBox = container.NewStack(r.content, padded)
	r.ExtendBaseWidget(r)
	return r
}

// Tapped 整行点击：交由 sectionClients 维护单选（再点同行 = 取消选中）
func (r *clientRow) Tapped(*fyne.PointEvent) {
	if r.onTap != nil {
		r.onTap(r.fullID)
	}
}

// CreateRenderer 渲染器只包构造期建好的内容树（自绘 canvas 图元，色值为包内常量）
func (r *clientRow) CreateRenderer() fyne.WidgetRenderer {
	return widget.NewSimpleRenderer(r.contentBox)
}

// setText 判重更新：widget.Label.SetText **无**等值守卫（v2.8.2 label.go 恒
// Refresh——2026-11-02 审查修正，此前注释误以为自带判重）。1.5s 轮询常态是
// 字段没变，先比对可把每行 7 次 SetText + 1 次 Refresh 的常态重绘压到零。
// 返回是否发生了变更（调用方累计决定是否整行 Refresh）。
func (r *clientRow) setText(l *widget.Label, v string) bool {
	if l.Text == v {
		return false
	}
	l.SetText(v)
	return true
}

// set 回填一行：row 数据 + 选中态（朱砂描边加粗——v2 卡片同款语言，不用旧
// Table 的整行暗红底，太重）。全字段判重：无变化零重绘。
func (r *clientRow) set(row ClientRow, selected bool) {
	r.fullID = row.FullID
	changed := false
	stroke := color.Color(qiLine2)
	r.content.StrokeWidth = 1
	if selected {
		stroke = qiPrimary
		r.content.StrokeWidth = 1.5
	}
	if r.content.StrokeColor != stroke {
		r.content.StrokeColor = stroke
		changed = true
	}

	// 设备（列 1，2026-10-28 昵称功能）：昵称·devid前4；旧手机端未上报昵称时
	// 退 "—"（会话列已有短 ID 可辨）。有昵称则恒带 devid 后缀，两台同型号
	// 默认名撞名时也能分清
	dev := row.Name
	if dev != "" && len(row.Devid) >= 4 {
		dev += "·" + row.Devid[:4]
	}
	if dev == "" {
		dev = "—"
	}

	// 客户端名（列 2）：未鉴权降级灰（旧表"（未鉴权）"语义；Label 用 Importance
	// 两档表达灰/亮——Low=disabled 灰，Medium=前景亮）
	name := row.Client
	wantImp := widget.MediumImportance
	switch {
	case !row.Authed && name == "":
		name = "（未鉴权）"
		wantImp = widget.LowImportance
	case !row.Authed:
		name += "（未鉴权）"
		wantImp = widget.LowImportance
	case name == "":
		name = "—"
	}
	if r.labels[2].Importance != wantImp {
		r.labels[2].Importance = wantImp
		r.labels[2].Refresh()
		changed = true
	}
	changed = r.setText(r.labels[0], row.Session) || changed
	changed = r.setText(r.labels[1], dev) || changed
	changed = r.setText(r.labels[2], name) || changed
	changed = r.setText(r.labels[3], orDash(row.Ver)) || changed
	changed = r.setText(r.labels[4], fmtRTT(row.RTTMs)) || changed
	changed = r.setText(r.labels[5], orDash(row.Path)) || changed
	// 最后 position：FEN 摘要（尚未发过局面退最近活动时间），旧表列语义
	changed = r.setText(r.labels[6], orDash(fenBrief(row.Fen, row.LastPos))) || changed

	if changed {
		r.Refresh()
	}
}

// ---------- 库区横排行（2026-11-01，仿 03 连接便当盒行） ----------

// bookColW 库区横排列宽：序号（优先级位次）/库名/条数/状态/文件时间。
// 2026-11-02 用户反馈"便当盒有点太大"：库名 300→260 收窄，5 列合计 642。
// 超长名 Truncation 截断，与连接区同款"锁宽+省略号"语言。
var bookColW = []float32{32, 260, 80, 90, 180}

// bookHeaderRow 库区表头行（与连接区同构：五列锁宽标题恒显不随行滚动）。
func bookHeaderRow() fyne.CanvasObject {
	titles := []string{"#", "库名", "条数", "状态", "文件时间"}
	box := container.NewHBox()
	for i, t := range titles {
		txt := qiText(t, qiSize(theme.SizeNameCaptionText), qiMuted, false, true)
		box.Add(container.New(sizedBox{w: bookColW[i], h: 14}, txt))
	}
	return container.New(&pad{left: 12, right: 12, top: 2, bottom: 2}, box)
}

// bookRow 库数据行（仿 clientRow v4 矮便当盒）：行底 qiSurface 圆角 8 + 描边
// 选中换朱砂。整行 Tappable：点击回调 onTap(name)（sectionBooks 维护单选；
// 坏库行点击弹完整原因 dialog）。状态列 canvas.Text 着色三态（2026-11-02
// 用户定稿）：启用=玉绿高亮 / 已禁用=警示红 / 加载失败=警示红。
type bookRow struct {
	widget.BaseWidget
	name  string
	onTap func(name string)

	contentBox *fyne.Container
	content    *canvas.Rectangle
	// labels 四列 Label：序号/库名/条数/时间（状态列独立 statusText 着色）
	labels []*widget.Label
	status *canvas.Text
}

func newBookRow(onTap func(name string)) *bookRow {
	r := &bookRow{onTap: onTap}
	// UI 树构造期建好（clientRow 同款：set 可能在挂树前被调用）
	r.content = canvas.NewRectangle(qiSurface)
	r.content.CornerRadius = 8
	r.content.StrokeColor = qiLine2
	r.content.StrokeWidth = 1
	box := container.NewHBox()
	for i := 0; i < 5; i++ {
		if i == 3 {
			// 状态列：canvas.Text 直控颜色（Label 无色字段，Importance 两档
			// 表达不了绿/红高亮），短文本无截断需求，widthBox 锁宽保列对齐
			r.status = qiText("启用", qiSize(theme.SizeNameText), qiJade, false, false)
			box.Add(container.New(&widthBox{w: bookColW[i]}, r.status))
			continue
		}
		// 序号/条数/时间等宽数字对齐；库名常规亮色
		mono := i == 0 || i == 2 || i == 4
		cell, lbl := clientCell("—", mono, false, bookColW[i])
		if mono {
			lbl.Importance = widget.LowImportance
		} else {
			lbl.Importance = widget.MediumImportance
		}
		r.labels = append(r.labels, lbl)
		box.Add(cell)
	}
	padded := container.New(&pad{top: 4, right: 12, bottom: 4, left: 12}, box)
	r.contentBox = container.NewStack(r.content, padded)
	r.ExtendBaseWidget(r)
	return r
}

// Tapped 整行点击：交由 sectionBooks 维护单选
func (r *bookRow) Tapped(*fyne.PointEvent) {
	if r.onTap != nil {
		r.onTap(r.name)
	}
}

// CreateRenderer 渲染器只包构造期建好的内容树
func (r *bookRow) CreateRenderer() fyne.WidgetRenderer {
	return widget.NewSimpleRenderer(r.contentBox)
}

// set 回填一行：位次（1-based 优先级）+ 快照 + 选中态（clientRow 同款朱砂描边）。
// labels 映射：0=序号 1=库名 2=条数 3=时间（状态在 r.status）。
func (r *bookRow) set(pos int, row BookRow, selected bool) {
	r.name = row.Name
	stroke := color.Color(qiLine2)
	r.content.StrokeWidth = 1
	if selected {
		stroke = qiPrimary
		r.content.StrokeWidth = 1.5
	}
	if r.content.StrokeColor != stroke {
		r.content.StrokeColor = stroke
	}

	// 状态三态着色（2026-11-02 用户定稿：启用绿 / 禁用红 / 坏库红）
	status := "启用"
	st := qiJade
	switch {
	case row.Status != "":
		status = "✕ 加载失败"
		st = qiDanger
	case !row.Enabled:
		status = "已禁用"
		st = qiDanger
	}
	if r.status.Text != status {
		r.status.Text = status
	}
	if r.status.Color != st {
		r.status.Color = st
	}
	entries := fmtInt(int64(row.Entries))
	if !row.Enabled || row.Status != "" {
		entries = "—" // 禁用/坏库未加载，条数无从统计
	}
	r.labels[0].SetText(fmt.Sprintf("%d", pos))
	r.labels[1].SetText(row.Name)
	r.labels[2].SetText(entries)
	r.labels[3].SetText(fmtFullTime(row.ModTime))
	r.Refresh()
}

// infoChip 圆形 ⓘ 提示标（2026-11-02 用户定稿）：注解行尾的更多信息入口——
// 悬停描边变亮 + 指针光标，点击弹说明窗（fyne 无内置 tooltip，悬停浮层需
// 自管窗口定位，点击弹 dialog 是可靠路径）。构造期定文案，无动态更新需求。
type infoChip struct {
	widget.BaseWidget
	title string
	body  string
	win   fyne.Window

	circle *canvas.Circle
	mark   *canvas.Text
	box    *fyne.Container
}

func newInfoChip(title, body string, win fyne.Window) *infoChip {
	c := &infoChip{title: title, body: body, win: win}
	c.circle = canvas.NewCircle(color.NRGBA{A: 0x00})
	c.circle.StrokeColor = qiMuted
	c.circle.StrokeWidth = 1
	c.mark = qiText("i", qiSize(theme.SizeNameCaptionText), qiMuted, false, true)
	// i 字居中：圆 18px、字宽 ~7px，手调偏移由 Layout 摆放（不引布局类型）
	c.box = container.NewStack(c.circle, c.mark)
	c.ExtendBaseWidget(c)
	return c
}

// Tapped 点击：弹详细说明（标题即注解主题）
func (c *infoChip) Tapped(*fyne.PointEvent) {
	dialog.ShowInformation(c.title, c.body, c.win)
}

// MouseIn/MouseOut 悬停反馈：描边亮起（无 tooltip 浮层的最小可用语义）
func (c *infoChip) MouseIn(*desktop.MouseEvent) {
	c.circle.StrokeColor = qiForeground
	c.circle.StrokeWidth = 1.5
	canvas.Refresh(c.circle)
}

func (c *infoChip) MouseMoved(*desktop.MouseEvent) {}

func (c *infoChip) MouseOut() {
	c.circle.StrokeColor = qiMuted
	c.circle.StrokeWidth = 1
	canvas.Refresh(c.circle)
}

// Cursor 悬停显示手型指针（可点击暗示）
func (c *infoChip) Cursor() desktop.Cursor { return desktop.PointerCursor }

// CreateRenderer 渲染器只包构造期建好的内容树
func (c *infoChip) CreateRenderer() fyne.WidgetRenderer {
	return widget.NewSimpleRenderer(c.box)
}

// MinSize 锁 18×18（圆标统一尺寸；Resize 居中摆字）
func (c *infoChip) MinSize() fyne.Size {
	return fyne.NewSize(18, 18)
}

func (c *infoChip) Resize(s fyne.Size) {
	c.BaseWidget.Resize(s)
	c.circle.Move(fyne.NewPos(0, 0))
	c.circle.Resize(fyne.NewSize(18, 18))
	// "i" 水平居中、垂直略上（canvas.Text 的字形基线偏下）
	c.mark.Resize(c.mark.MinSize())
	c.mark.Move(fyne.NewPos((18-c.mark.MinSize().Width)/2, 2))
}

// tapCatcher 透明点击捕获层（连接区"点击空白取消选中"）：铺在卡片列表底层

// dangerHoverBtn 危险动作按钮：平时与普通按钮同形态（不抢视觉），悬停叠
// 半透明红罩变红——"删除库"这类破坏性动作的视觉警示（2026-11-02 用户定稿：
// "红色太刺眼，挪上去才变红"）。
//
// 【为什么不用 Importance 切换】fyne 原生按钮渲染 = importance 底色与 hover
// blend 层混合（applyTheme 把 background 与 theme.ColorNameHover 做 blend）
// ——qiTheme 的 Hover 色是**不透明**深灰 #262A31，悬停瞬间把 Danger 红整个
// 盖掉（渲染像素实测 #23262C≈Hover 色，2026-11-02 vc15 真机"悬停不红"根因；
// 字段 Importance 切换、Refresh 链路逐环排查均正常）。原生默认主题的 hover
// 色带 alpha 才没这问题；改主题 Hover 的 alpha 是全局行为变更（所有按钮
// hover 观感），故走局部路线：renderer 持有 tint 层（hiliteButton 验证过的
// 模式）直控显隐，Importance 恒 Medium 不碰 blend。
//
// 【结构铁律】值嵌入 widget.Button + 构造期 ExtendBaseWidget(b)（hiliteButton
// 同款模式）：**指针嵌入 *widget.Button 会让 fyne 的 super/渲染路径漂移到
// 嵌入 Button，布局错乱、悬停事件收不到**（2026-11-02 渲染快照实证——
// "删除"按钮漂浮到操作行上方）。
type dangerHoverBtn struct {
	widget.Button
	tint *canvas.Rectangle // 悬停警示罩（半透明红；MouseIn 显、MouseOut 隐）
}

// dangerTint 悬停罩色：警示红半透明——叠在深灰按钮底上呈暗红、白字仍可读。
var dangerTint = color.NRGBA{R: 0xFF, G: 0x52, B: 0x52, A: 0x50}

// newDangerButton 构造悬停变红的危险动作按钮。
func newDangerButton(label string, onTap func()) *dangerHoverBtn {
	b := &dangerHoverBtn{}
	b.Text = label
	b.OnTapped = onTap
	b.Importance = widget.MediumImportance
	b.ExtendBaseWidget(b)
	return b
}

// CreateRenderer 复用原生渲染树 + 顶层叠警示罩（tint 在 objects 末尾=画在
// 最上层，原生文字/涟漪透出半透明红）。
func (b *dangerHoverBtn) CreateRenderer() fyne.WidgetRenderer {
	native := b.Button.CreateRenderer()
	b.ExtendBaseWidget(b) // super 修回（原生 CreateRenderer 的 ExtendBaseWidget 副作用防御）

	b.tint = canvas.NewRectangle(color.NRGBA{A: 0}) // 初始全透明
	b.tint.CornerRadius = 8                         // 与主题按钮圆角同款
	return &dangerHoverRenderer{
		native: native,
		tint:   b.tint,
		objects: append(append([]fyne.CanvasObject{}, native.Objects()...),
			b.tint),
	}
}

// MouseIn/MouseOut 悬停显隐警示罩；禁用态不变红（不可删时无警示语义）。
// 原生 hover 状态透传（背景微亮的原生反馈保留）。tint 显隐走自身 Refresh
// （renderer 持有对象直改，不依赖按钮重绘——Importance 路线已被主题 blend
// 否决，见类型注释）。
func (b *dangerHoverBtn) MouseIn(*desktop.MouseEvent) {
	b.Button.MouseIn(nil)
	if b.tint != nil && !b.Disabled() {
		b.tint.FillColor = dangerTint
		b.tint.Refresh()
	}
}

func (b *dangerHoverBtn) MouseOut() {
	b.Button.MouseOut()
	if b.tint != nil {
		b.tint.FillColor = color.NRGBA{A: 0}
		b.tint.Refresh()
	}
}

// dangerHoverRenderer 原生渲染树 + 顶层警示罩的复合渲染器（hiliteRenderer
// 同款五方法结构；布局/尺寸委托原生）。
type dangerHoverRenderer struct {
	native  fyne.WidgetRenderer
	tint    *canvas.Rectangle
	objects []fyne.CanvasObject
}

func (r *dangerHoverRenderer) Layout(s fyne.Size) {
	r.native.Layout(s)
	r.tint.Move(fyne.NewPos(0, 0))
	r.tint.Resize(s)
}

func (r *dangerHoverRenderer) MinSize() fyne.Size { return r.native.MinSize() }

func (r *dangerHoverRenderer) Objects() []fyne.CanvasObject { return r.objects }

func (r *dangerHoverRenderer) Destroy() { r.native.Destroy() }

func (r *dangerHoverRenderer) Refresh() { r.native.Refresh() }

// tapCatcher 透明点击捕获层（连接区"点击空白取消选中"）：铺在卡片列表底层
// （container.NewStack(catcher, cards)——卡片在上层自己收 tap；点卡片之间/
// 列表下方的空白时卡片不命中，fyne 命中测试穿透到本层。fyne 的 tap 只投给
// z 序最上层 Tappable 且不冒泡，普通 Container 不拦截——源码取证 2026-10-18）。
type tapCatcher struct {
	widget.BaseWidget
	onTap func()
}

func newTapCatcher(onTap func()) *tapCatcher {
	t := &tapCatcher{onTap: onTap}
	t.ExtendBaseWidget(t)
	return t
}

// Tapped 空白点击：常用于清除列表选中态
func (t *tapCatcher) Tapped(*fyne.PointEvent) {
	if t.onTap != nil {
		t.onTap()
	}
}

func (t *tapCatcher) CreateRenderer() fyne.WidgetRenderer {
	// Spacer MinSize(0,0)：捕获层不贡献最小尺寸，避免撑大连接区
	return widget.NewSimpleRenderer(layout.NewSpacer())
}
