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
	"math"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// trendHeight 曲线带高度；trendCPRange 分数量程（±600cp 顶格，将杀压 ±1500 必顶格）；
// trendGridCP 淡网格档位；trendSegs 平滑插值密度（每对相邻数据点插的微段数）；
// trendFillStep 面积填充竖线步长 px（同时是竖线 StrokeWidth，步长=线宽覆盖无缝）；
// trendPadX 左右留白（曲线/网格/悬停反算共用同一横轴基准，见 hoverIndexAt）。
const (
	trendHeight   = 96
	trendCPRange  = 600.0
	trendGridCP   = 300
	trendSegs     = 8
	trendFillStep = float32(2)
	trendPadX     = float32(10)
)

// 走势曲线配色（2026-10-29 天天象棋风定稿）：优段朱砂、劣段靛青（theme.go token
// 同源），面积填充为同色低 alpha；网格与零线用白色两档透明度分层。
var (
	trendUpStroke   = qiZhuBrand
	trendDownStroke = qiIndigo
	trendUpFill     = color.NRGBA{R: qiZhuBrand.R, G: qiZhuBrand.G, B: qiZhuBrand.B, A: 0x28}
	trendDownFill   = color.NRGBA{R: qiIndigo.R, G: qiIndigo.G, B: qiIndigo.B, A: 0x28}
	trendGridColor  = color.NRGBA{R: 0xFF, G: 0xFF, B: 0xFF, A: 0x12}
	// 零基准线（2026-11-02 用户定稿：半透明白 → 明亮黄）：横贯曲线带的"平"参照
	// 线要一眼可辨，琥珀（qiAmber）是色板里唯一明亮暖色，与优/劣双色面积图
	// （朱砂/靛青）色相均不冲突
	trendZeroColor = qiAmber
	// trendHoverRing 悬停标记色（2026-11-20 悬停 tooltip）：近实心全白——优/劣
	// 双色背景上都清晰，与零线琥珀、曲线朱砂/靛青色相均不冲突
	trendHoverRing = color.NRGBA{R: 0xFF, G: 0xFF, B: 0xFF, A: 0xE6}
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
	// 悬停变化即时刷头部小字（不等 1.5s 周期）：refreshTrend 的悬停分支用
	// 最新 hoverIdx 组装文案；曲线标记圈由 setHover 自己的 Refresh 负责
	c.trendCurve.onHover = func(int) { c.refreshTrend() }
	// 头部行引用留存：显隐按钮排后父级 HBox 须手动 Refresh 重排（fyne 隐藏
	// 子项不会自动触发父级布局，硬留一个按钮宽的空洞到下次窗口重排）
	header := sectionHeader("04", "走势", c.trendWin, c.trendDevBtnsBox)
	c.trendHeaderBox = header
	return bentoTile(container.NewVBox(
		header,
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
		// 头部小字（2026-11-20 悬停 tooltip）：鼠标悬停曲线时显示该步
		// "第 N 条 · 视角分值/胜率"，移出恢复末点胜率——同一信息位，不新增
		// 控件；即时切换由 onHover 回调触发本函数（不等 1.5s 周期）
		idx := -1
		if c.trendCurve != nil {
			idx = c.trendCurve.hoverIdx
		}
		p := snap.Points[n-1]
		hdr := ""
		if idx >= 0 && idx < n {
			p = snap.Points[idx]
			hdr = fmt.Sprintf("第 %d 条 · ", idx+1)
		}
		txt, col := trendPointText(p)
		c.trendWinSet(hdr + txt)
		// 颜色判重：canvas.Text 无判重机制，1.5s 轮询常态是档位没变，等值时
		// 省一次 Refresh（col 恒为包内 NRGBA 常量，接口值比较安全）
		if c.trendWin.Color != col {
			c.trendWin.Color = col
			c.trendWin.Refresh()
		}
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
	// 重建键含 Label：设备昵称改动重连后 Key 不变，漏 Label 会一直显示旧昵称
	optKey := fmt.Sprint(len(devs))
	for _, d := range devs {
		optKey += "|" + d.Key + "|" + d.Label
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
	// 显隐走 Hide/Show 并手动重排父级（直写 Hidden 字段连 repaint 都没有，
	// 且不触发父级 HBox 重排——见 sectionTrend 注）
	if wantHidden := len(devs) <= 1; wantHidden != c.trendDevBtnsBox.Hidden {
		if wantHidden {
			c.trendDevBtnsBox.Hide()
		} else {
			c.trendDevBtnsBox.Show()
		}
		if c.trendHeaderBox != nil {
			c.trendHeaderBox.Refresh()
		}
	}
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
	// hoverIdx 鼠标悬停的数据点索引（-1 = 无悬停）。MouseMoved 高频触发，
	// setHover 仅在索引变化时 Refresh——判重是 trendRenderer 六十帧重建环
	// 雷区的硬要求，等值期间绝不动图元
	hoverIdx int
	// onHover 悬停变化回调（sectionTrend 接线到 Console.refreshTrend：头部
	// 小字即时切换不等 1.5s 周期；曲线标记圈由 setHover 自己的 Refresh 负责）
	onHover func(idx int)
}

func newTrendCurve() *trendCurve {
	t := &trendCurve{hoverIdx: -1}
	t.ExtendBaseWidget(t)
	return t
}

// ── 悬停 tooltip（2026-11-20 用户拍板"悬停轻交互"）：悬停曲线带时头部小字
// 显示该步序号与视角分值、曲线上画白描边标记圈；不引入点击/选择控件，保持
// "被动生长的背景信息"显示纪律（文件头第 3 条）。 ──

// hoverIndexAt 鼠标 x 反算最近数据点索引（xAt 的逆运算）；点数不足或鼠标在
// 左右留白带外返回 -1（留白即视为无悬停目标）。
func (t *trendCurve) hoverIndexAt(mx float32) int {
	n := len(t.pts)
	w := t.Size().Width
	if n < 2 || w <= 2*trendPadX {
		return -1
	}
	if mx < trendPadX || mx > w-trendPadX {
		return -1
	}
	i := int(math.Round(float64((mx - trendPadX) / (w - 2*trendPadX) * float32(n-1))))
	if i < 0 {
		i = 0
	} else if i > n-1 {
		i = n - 1
	}
	return i
}

// setHover 更新悬停索引：变化才 Refresh + 回调（见 hoverIdx 字段注释的判重
// 纪律；MouseMoved 在同一数据点区间内滑动不产生任何重建）。
func (t *trendCurve) setHover(idx int) {
	if idx == t.hoverIdx {
		return
	}
	t.hoverIdx = idx
	t.Refresh() // renderer.Refresh → rebuild：标记圈随最新 hoverIdx 重画
	if t.onHover != nil {
		t.onHover(idx)
	}
}

func (t *trendCurve) MouseIn(ev *desktop.MouseEvent)    { t.setHover(t.hoverIndexAt(ev.Position.X)) }
func (t *trendCurve) MouseMoved(ev *desktop.MouseEvent) { t.setHover(t.hoverIndexAt(ev.Position.X)) }
func (t *trendCurve) MouseOut()                         { t.setHover(-1) }

// setSeries 换序列并重绘（尺寸未变时直接重排图元）。
func (t *trendCurve) setSeries(pts []TrendPoint) {
	t.pts = pts
	t.Refresh()
}

func (t *trendCurve) CreateRenderer() fyne.WidgetRenderer {
	return &trendRenderer{c: t}
}

// trendRenderer 布局缓存：lastSize = 上次全量重建时的尺寸。
//
// ⚠【雷区】同尺寸 Layout 必须幂等（不换图元对象、不置脏）。fyne 每帧
// repaintWindow→EnsureMinSize 后序遍历会把"新图元节点 minSize=0"判成子项
// 尺寸变化并回调 renderer.Layout；若 Layout 无条件 rebuild（换掉全部图元），
// 下一帧又得到一批新节点，Layout 再被回调——形成 60fps 自持重建环。
// 2026-11-11 实测事故：待机占满 20 线程（76%~90%）、活堆 780MB/1260 万对象、
// GC 5.2 次/秒；图元进 fyne 画布缓存（TTL 60s）把 GOMEMLIMIT=800MB 顶死。
// 只有数据变化（Refresh）或尺寸真变（Layout 且 size != lastSize）才允许重建。
type trendRenderer struct {
	c    *trendCurve
	last fyne.Size
}

// rebuild 全量重建图元。层次自底向上：淡网格 → 零线 → 面积填充 → 平滑曲线
// → 数据点圆点。仅在换序列（节流键变化）或尺寸变化时执行，1.5s 刷新轮空转。
func (r *trendRenderer) rebuild(size fyne.Size) {
	c := r.c
	padX, padY := trendPadX, float32(6)
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
		// 悬停标记圈（2026-11-20 悬停 tooltip）：与末点双圈同款三圆结构但全白
		// （外 10 / 挖孔 6 表面色 / 内点 4），叠加在数据点层之上；hoverIdx 越界
		// （换序列后点数变少）静默不画，下次鼠标移动即自校正
		if hi := c.hoverIdx; hi >= 0 && hi < n {
			objs = append(objs,
				dotAt(pts[hi], 10, trendHoverRing),
				dotAt(pts[hi], 6, qiSurface),
				dotAt(pts[hi], 4, trendHoverRing),
			)
		}
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
	padX := trendPadX
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
	var objs []fyne.CanvasObject
	if spacing := (w - 2*padX) / float32(n-1); spacing >= 8 {
		for i := 0; i < n-1; i++ {
			objs = append(objs, dotAt(pts[i], 5, dotColor(pts[i].Y, h)))
		}
	}
	// 末点双圈高亮（外圈 10 / 挖孔 6 / 内点 4）
	objs = append(objs,
		dotAt(pts[n-1], 10, dotColor(pts[n-1].Y, h)),
		dotAt(pts[n-1], 6, qiSurface),
		dotAt(pts[n-1], 4, dotColor(pts[n-1].Y, h)),
	)
	return objs
}

// dotAt 单圆点图元（直径 d 居中于 p）：数据点圆点与悬停标记圈共用
// （canvas.Circle 无描边概念，双圈效果靠三只圆叠出，见 pointDots）。
func dotAt(p fyne.Position, d float32, col color.Color) *canvas.Circle {
	ci := canvas.NewCircle(col)
	ci.Resize(fyne.NewSize(d, d))
	ci.Move(fyne.NewPos(p.X-d/2, p.Y-d/2))
	return ci
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

// trendPointText 单点头部文案与颜色（"红方分值 -0.3"/"我方胜率 62%"）：
// 视角换算（2026-10-29）——手机上报执色（position.my_red）后按**我方视角**
// 展示，我方执黑则分数取负、胜率取补，语义与悬浮窗"胜 x%"一致；旧手机未上报
// 时退回红方视角（引入字段前的行为），文案前缀如实标"红方"。颜色随优劣三档：
// 优玉/劣警示红/胶着琥珀，无 WDL 回灰。末点胜率与悬停态共用（refreshTrend）。
func trendPointText(p TrendPoint) (string, color.Color) {
	cp, wrv, prefix := trendView(p)
	col := qiMuted
	text := fmt.Sprintf("%s分值 %s", prefix, fmtScore(&cp, nil))
	if wrv >= 0 {
		text = fmt.Sprintf("%s胜率 %d%%", prefix, wrv)
		switch {
		case wrv >= 55:
			col = qiJade
		case wrv <= 45:
			col = qiDanger
		default:
			col = qiAmber
		}
	}
	return text, col
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

// Layout 只在尺寸真变时重建一次；同尺寸重复调用是幂等的（见 trendRenderer
// 类型注释里的 fyne 布局回写陷阱）。尺寸变化后由 rebuild 尾部 canvas.Refresh
// 保证重绘。
func (r *trendRenderer) Layout(size fyne.Size) {
	if size == r.last {
		return
	}
	r.last = size
	r.rebuild(size)
}

func (r *trendRenderer) MinSize() fyne.Size           { return fyne.NewSize(200, trendHeight) }
func (r *trendRenderer) Objects() []fyne.CanvasObject { return r.c.objs }

// Refresh 数据变化（setSeries）时按当前尺寸全量重建，并同步 lastSize——随后
// 因图元身份变化触发的 Layout 视为同尺寸，不再二次重建。
func (r *trendRenderer) Refresh() {
	size := r.c.Size()
	r.last = size
	r.rebuild(size)
}

func (r *trendRenderer) Destroy() {}
