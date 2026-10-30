// trend.go 走势面板（2026-10-28 定稿）：当前对局的引擎终态分曲线，插在连接与
// 高级功能之间。显示纪律（用户定稿第 4 条）：被动生长的背景信息——无任何操作
// 控件、曲线带恒定 96px、胜率只占头部一枚小字，不与棋局争夺注意力。
//
// 数据：server 端按对局分段的内存序列（TrendSeries 回调），NEW_GAME 换段由
// 服务端语义保证（手机识别器新局确认 / 旧机从当前时刻起笔），此处只管画。
// 绘制用 canvas 原语自绘（2026-10-29 天天象棋风定稿：Catmull-Rom 平滑 +
// 优/劣双色面积填充 + 数据点圆点 + 淡网格），不引图表库（零体积红线）。
package console

import (
	"fmt"
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// trendHeight 曲线带高度；trendCPRange 分数量程（±600cp 顶格，将杀压 ±1500 必顶格）；
// trendGridCP 淡网格档位；trendSegs 平滑插值密度（每对相邻数据点插的微段数）；
// trendFillStep 面积填充竖线步长 px（同时是竖线 StrokeWidth，步长=线宽覆盖无缝）。
const (
	trendHeight   = 96
	trendCPRange  = 600.0
	trendGridCP   = 300
	trendSegs     = 8
	trendFillStep = float32(2)
)

// 走势曲线配色（2026-10-29 天天象棋风定稿）：优段朱砂、劣段靛青（theme.go token
// 同源），面积填充为同色低 alpha；网格与零线用白色两档透明度分层。
var (
	trendUpStroke   = qiZhuBrand
	trendDownStroke = qiIndigo
	trendUpFill     = color.NRGBA{R: qiZhuBrand.R, G: qiZhuBrand.G, B: qiZhuBrand.B, A: 0x28}
	trendDownFill   = color.NRGBA{R: qiIndigo.R, G: qiIndigo.G, B: qiIndigo.B, A: 0x28}
	trendGridColor  = color.NRGBA{R: 0xFF, G: 0xFF, B: 0xFF, A: 0x12}
	trendZeroColor  = color.NRGBA{R: 0xFF, G: 0xFF, B: 0xFF, A: 0x2E}
)

// sectionTrend 走势分区：头部（当前胜率小字 + 多设备时的设备按钮排）+ 曲线带。
// 设备按钮排仅在有曲线的组 >1 时显示（2026-10-30 分组立项拍板：横向设备按钮，
// 当前设备实心高亮，与 05 高级功能按钮排同一视觉语言；单设备零增重）。
func (c *Console) sectionTrend() fyne.CanvasObject {
	if c.opts.TrendSeries == nil {
		return nil // main 未接线：面板缺席
	}
	c.trendWin = qiText("尚无对局数据", qiSize(theme.SizeNameCaptionText), qiMuted, false, false)
	c.trendWinSet = func(v string) {
		if c.trendWin.Text != v {
			c.trendWin.Text = v
			c.trendWin.Refresh()
		}
	}
	c.trendDevBtnsBox = container.NewHBox()
	c.trendDevBtnsBox.Hide() // 组列表就绪前隐藏（refreshTrend 按 >1 组显隐）
	c.trendDevBtns = map[string]*widget.Button{}
	c.trendCurve = newTrendCurve()
	return bentoTile(container.NewVBox(
		sectionHeader("04", "走势", c.trendWin, c.trendDevBtnsBox),
		c.trendCurve,
	))
}

// refreshTrend 刷新周期回调：同步设备按钮排（组列表变化才重建），拉选中设备
// 序列，键变了才重建曲线（1.5s 节奏下避免每轮重排图元），头部胜率逐轮跟进。
// 重建键含设备键与末点分数/执色——服务端"同局面去重"替换末点（点数不变分数
// 变）时曲线也要原地刷新；切换设备时设备段变化，立即换曲线。
func (c *Console) refreshTrend() {
	if c.opts.TrendSeries == nil || c.trendCurve == nil || c.trendWinSet == nil {
		return
	}
	c.syncTrendDevBtns()
	snap := c.opts.TrendSeries(c.trendDevKey) // 空键 = 焦点设备（单设备常态）
	n := len(snap.Points)
	if n == 0 {
		c.trendWinSet("尚无对局数据——引擎出建议后自动记录")
		c.trendWin.Color = qiMuted
		c.trendWin.Refresh()
	} else {
		last := snap.Points[n-1]
		col := qiMuted
		// 视角换算（2026-10-29）：手机上报执色（position.my_red）后按**我方视角**
		// 展示——我方执黑则分数取负、胜率取补，语义与悬浮窗"胜 x%"一致；旧手机
		// 未上报时退回红方视角（引入字段前的行为），文案前缀如实标"红方"。
		cp, wrv, prefix := trendView(last)
		wr := fmt.Sprintf("%s分值 %s", prefix, fmtScore(&cp, nil))
		if wrv >= 0 {
			wr = fmt.Sprintf("%s胜率 %d%%", prefix, wrv)
			switch {
			case wrv >= 55:
				col = qiJade
			case wrv <= 45:
				col = qiDanger
			default:
				col = qiAmber
			}
		}
		c.trendWinSet(wr)
		c.trendWin.Color = col
		c.trendWin.Refresh()
	}
	key := c.trendDevKey + "|" + fmt.Sprintf("%s|%d", snap.GameID, n)
	if n > 0 {
		last := snap.Points[n-1]
		key = c.trendDevKey + "|" + fmt.Sprintf("%s|%d|%d|%t|%t", snap.GameID, n, last.ScoreCP, last.MyRed, last.MySideKnown)
	}
	if key != c.trendLastKey {
		c.trendLastKey = key
		c.trendCurve.setSeries(snap.Points)
	}
}

// syncTrendDevBtns 同步 04 头部设备按钮排（每个刷新周期调用）：组列表变化才
// 重建按钮（内容键 = 组键拼接，防 1.5s 重建抖动）；选中键失效（组被淘汰或
// **设备断联**——服务端列表只含在线设备，断联即隐藏入口）回退焦点（空键）；
// 组 >1 显示、≤1 隐藏（单设备零增重）。TrendDevices 未接线时恒隐藏（面板恒为
// 焦点设备视图）。
func (c *Console) syncTrendDevBtns() {
	if c.trendDevBtnsBox == nil {
		return
	}
	var devs []TrendDev
	if c.opts.TrendDevices != nil {
		devs = c.opts.TrendDevices()
	}
	// 选中键有效性：空 = 跟随焦点恒有效；非空但组已消失（淘汰/重启清空）→ 回退焦点
	if c.trendDevKey != "" {
		found := false
		for _, d := range devs {
			if d.Key == c.trendDevKey {
				found = true
				break
			}
		}
		if !found {
			c.trendDevKey = "" // 空键 = 焦点设备，语义恒可用
		}
	}
	optKey := fmt.Sprint(len(devs))
	for _, d := range devs {
		optKey += "|" + d.Key
	}
	if c.trendDevBtnsKey == optKey {
		return // 列表未变：高亮由 trendSelectDevice 在切换时同步，无需重排
	}
	c.trendDevBtnsKey = optKey
	c.trendDevBtnsBox.RemoveAll()
	c.trendDevBtns = map[string]*widget.Button{}
	for _, d := range devs {
		dk := d.Key // 闭包捕获当前设备键
		btn := widget.NewButton(d.Label, func() { c.trendSelectDevice(dk) })
		if dk == c.trendDevKey {
			btn.Importance = widget.HighImportance
		}
		c.trendDevBtns[dk] = btn
		c.trendDevBtnsBox.Add(btn)
	}
	c.trendDevBtnsBox.Hidden = len(devs) <= 1
	// 多设备首现：自动选中最近活跃组（服务端按活跃降序返回第一项），
	// 避免无高亮的"悬空"状态；单设备不选（按钮已隐藏）
	if len(devs) > 1 && c.trendDevKey == "" {
		c.trendSelectDevice(devs[0].Key)
		return // trendSelectDevice 内部会拉一次序列
	}
	c.trendDevBtnsBox.Refresh()
}

// trendSelectDevice 切换选中设备（按钮回调，fyne 主 goroutine）：存键、同步
// 按钮高亮、立即拉取该设备曲线（不等下一个 1.5s 周期）。
func (c *Console) trendSelectDevice(key string) {
	c.trendDevKey = key
	for k, b := range c.trendDevBtns {
		if k == key {
			b.Importance = widget.HighImportance
		} else {
			b.Importance = widget.MediumImportance
		}
		b.Refresh()
	}
	c.refreshTrend()
}

// trendCurve 自绘曲线控件：淡网格 + 零线 + 双色面积填充 + 平滑曲线 + 数据点。
// y 轴**我方视角**——上方=我方优，±600cp 顶格；x 轴 = 建议序号（第 1 条建议在
// 左）。视角逐点换算：每个点按它记录时的 my_red 决定是否取负（对局中途换边，
// 旧点仍保持当时视角）。
type trendCurve struct {
	widget.BaseWidget
	objs []fyne.CanvasObject
	pts  []TrendPoint
}

func newTrendCurve() *trendCurve {
	t := &trendCurve{}
	t.ExtendBaseWidget(t)
	return t
}

// setSeries 换序列并重绘（尺寸未变时直接重排图元）。
func (t *trendCurve) setSeries(pts []TrendPoint) {
	t.pts = pts
	t.Refresh()
}

func (t *trendCurve) CreateRenderer() fyne.WidgetRenderer {
	return &trendRenderer{c: t}
}

type trendRenderer struct{ c *trendCurve }

// rebuild 全量重建图元。层次自底向上：淡网格 → 零线 → 面积填充 → 平滑曲线
// → 数据点圆点。仅在换序列（节流键变化）或尺寸变化时执行，1.5s 刷新轮空转。
func (r *trendRenderer) rebuild(size fyne.Size) {
	c := r.c
	padX, padY := float32(10), float32(6)
	w, h := size.Width, size.Height
	objs := make([]fyne.CanvasObject, 0, 64)
	// 淡网格 ±300cp（量程参照；alpha 比零线低一档，不抢曲线）
	for _, cp := range [2]int{trendGridCP, -trendGridCP} {
		g := canvas.NewLine(trendGridColor)
		y := r.c.yAt(cp, h, padY)
		g.Position1 = fyne.NewPos(padX, y)
		g.Position2 = fyne.NewPos(w-padX, y)
		objs = append(objs, g)
	}
	// 零线（均势基准）
	zero := canvas.NewLine(trendZeroColor)
	zero.Position1 = fyne.NewPos(padX, h/2)
	zero.Position2 = fyne.NewPos(w-padX, h/2)
	objs = append(objs, zero)
	if n := len(c.pts); n >= 2 {
		// 数据点 → 屏幕坐标（我方视角逐点换算）
		pts := make([]fyne.Position, n)
		for i := range c.pts {
			pts[i] = fyne.NewPos(r.c.xAt(i, n, w, padX), r.c.yOf(c.pts[i], h, padY))
		}
		objs = append(objs, r.fillBand(pts, w, h)...)
		dense := smoothPath(pts)
		// 曲线本体：平滑后逐微段连线，段色随中点优/劣切换（零线穿越处自然换色）
		for i := 1; i < len(dense); i++ {
			ln := canvas.NewLine(segColor((dense[i-1].Y+dense[i].Y)/2, h))
			ln.StrokeWidth = 2
			ln.Position1 = dense[i-1]
			ln.Position2 = dense[i]
			objs = append(objs, ln)
		}
		objs = append(objs, pointDots(pts, w, h, padX)...)
	}
	c.objs = objs
	canvas.Refresh(c)
}

// smoothPath Catmull-Rom 样条把折线摊成平滑点列（每对相邻点插 trendSegs 段），
// 端点用最近邻点补位控制点——折角圆化是"天天象棋风"观感的关键。
func smoothPath(pts []fyne.Position) []fyne.Position {
	at := func(i int) fyne.Position {
		if i < 0 {
			i = 0
		} else if i >= len(pts) {
			i = len(pts) - 1
		}
		return pts[i]
	}
	dense := make([]fyne.Position, 0, (len(pts)-1)*trendSegs+1)
	for i := 0; i < len(pts)-1; i++ {
		p0, p1, p2, p3 := at(i-1), at(i), at(i+1), at(i+2)
		for s := 0; s < trendSegs; s++ {
			dense = append(dense, catmullRom(p0, p1, p2, p3, float32(s)/trendSegs))
		}
	}
	return append(dense, pts[len(pts)-1])
}

// catmullRom Catmull-Rom 三次样条单段插值（t∈[0,1)，p1→p2，p0/p3 为邻段控制点）。
func catmullRom(p0, p1, p2, p3 fyne.Position, t float32) fyne.Position {
	t2, t3 := t*t, t*t*t
	x := 0.5 * ((2 * p1.X) + (-p0.X+p2.X)*t + (2*p0.X-5*p1.X+4*p2.X-p3.X)*t2 + (-p0.X+3*p1.X-3*p2.X+p3.X)*t3)
	y := 0.5 * ((2 * p1.Y) + (-p0.Y+p2.Y)*t + (2*p0.Y-5*p1.Y+4*p2.Y-p3.Y)*t2 + (-p0.Y+3*p1.Y-3*p2.Y+p3.Y)*t3)
	return fyne.NewPos(x, y)
}

// fillBand 面积填充：每 trendFillStep px 一根竖线（零线 → 曲线 y），色按该 x
// 处的优/劣（零线上方=我方优朱砂、下方=劣靛青）。y 取数据点线性插值——填充
// 是低 alpha 底色，与上层平滑描边 1-2px 的形态差不可见；换来 O(宽度/步长)
// 的固定线数（~460 根），与点数无关，密序列不爆炸。
func (r *trendRenderer) fillBand(pts []fyne.Position, w, h float32) []fyne.CanvasObject {
	n := len(pts)
	if n < 2 {
		return nil
	}
	padX := float32(10)
	zeroY := h / 2
	span := w - 2*padX
	var objs []fyne.CanvasObject
	for x := padX + trendFillStep/2; x < w-padX; x += trendFillStep {
		u := (x - padX) / span * float32(n-1)
		i := int(u)
		if i >= n-1 {
			i = n - 2
		}
		f := u - float32(i)
		y := pts[i].Y + (pts[i+1].Y-pts[i].Y)*f
		if y == zeroY {
			continue // 恰在零线上无面积可言
		}
		col := trendUpFill
		if y > zeroY {
			col = trendDownFill
		}
		ln := canvas.NewLine(col)
		ln.StrokeWidth = trendFillStep
		ln.Position1 = fyne.NewPos(x, zeroY)
		ln.Position2 = fyne.NewPos(x, y)
		objs = append(objs, ln)
	}
	return objs
}

// segColor 曲线微段描边色：段中点在零线上方=优（朱砂）、下方=劣（靛青）。
func segColor(y, h float32) color.Color {
	if y <= h/2 {
		return trendUpStroke
	}
	return trendDownStroke
}

// pointDots 数据点圆点：点距 ≥8px 时逐点标注（更密则圆点糊成粗线，只标末点），
// 圆色随该点优/劣；末点恒标且双圈高亮（描边色外圈 + 底色挖孔 + 实心内点），
// 天天象棋式"当前位置"锚点。canvas.Circle 无描边概念，双圈用三只圆叠出。
func pointDots(pts []fyne.Position, w, h, padX float32) []fyne.CanvasObject {
	n := len(pts)
	if n < 2 {
		return nil
	}
	dot := func(p fyne.Position, d float32, col color.Color) *canvas.Circle {
		ci := canvas.NewCircle(col)
		ci.Resize(fyne.NewSize(d, d))
		ci.Move(fyne.NewPos(p.X-d/2, p.Y-d/2))
		return ci
	}
	var objs []fyne.CanvasObject
	if spacing := (w - 2*padX) / float32(n-1); spacing >= 8 {
		for i := 0; i < n-1; i++ {
			objs = append(objs, dot(pts[i], 5, dotColor(pts[i].Y, h)))
		}
	}
	// 末点双圈高亮（外圈 10 / 挖孔 6 / 内点 4）
	objs = append(objs,
		dot(pts[n-1], 10, dotColor(pts[n-1].Y, h)),
		dot(pts[n-1], 6, qiSurface),
		dot(pts[n-1], 4, dotColor(pts[n-1].Y, h)),
	)
	return objs
}

// dotColor 圆点色：点在零线上方=优（朱砂）、下方=劣（靛青）。
func dotColor(y, h float32) color.Color {
	if y <= h/2 {
		return trendUpStroke
	}
	return trendDownStroke
}

func (t *trendCurve) xAt(i, n int, w, padX float32) float32 {
	if n <= 1 {
		return w - padX
	}
	return padX + (w-2*padX)*float32(i)/float32(n-1)
}

// trendView 把红方视角的走势点换算成展示视角：手机上报执色时按"我方视角"
// （我方执黑 → 分数取负、胜率取补），未上报（旧手机）退回红方视角。
// 返回展示用分数、胜率（-1 = 无 WDL，不取补）与文案前缀（"我方"/"红方"）。
func trendView(p TrendPoint) (cp, winRate int, label string) {
	cp, winRate, label = p.ScoreCP, p.WinRate, "红方"
	if !p.MySideKnown {
		return
	}
	label = "我方"
	if !p.MyRed {
		cp = -cp
		if winRate >= 0 {
			winRate = 100 - winRate
		}
	}
	return
}

// yOf 按该点记录的我方执色换算视角后求 y（我方视角：上=我方优）。
func (t *trendCurve) yOf(p TrendPoint, h, padY float32) float32 {
	cp, _, _ := trendView(p)
	return t.yAt(cp, h, padY)
}

func (t *trendCurve) yAt(cp int, h, padY float32) float32 {
	v := float32(cp) / trendCPRange
	if v > 1 {
		v = 1
	} else if v < -1 {
		v = -1
	}
	return h/2 - v*(h/2-padY)
}

func (r *trendRenderer) Layout(size fyne.Size)        { r.rebuild(size) }
func (r *trendRenderer) MinSize() fyne.Size           { return fyne.NewSize(200, trendHeight) }
func (r *trendRenderer) Objects() []fyne.CanvasObject { return r.c.objs }
func (r *trendRenderer) Refresh()                     { r.rebuild(r.c.Size()) }
func (r *trendRenderer) Destroy()                     {}
