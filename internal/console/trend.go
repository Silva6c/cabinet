// trend.go 走势面板（2026-10-28 定稿）：当前对局的引擎终态分折线，插在连接与
// 高级功能之间。显示纪律（用户定稿第 4 条）：被动生长的背景信息——无任何操作
// 控件、曲线带恒定 96px、胜率只占头部一枚小字，不与棋局争夺注意力。
//
// 数据：server 端按对局分段的内存序列（TrendSeries 回调），NEW_GAME 换段由
// 服务端语义保证（手机识别器新局确认 / 旧机从当前时刻起笔），此处只管画。
// 绘制用 canvas.Line 自绘，不引图表库（零体积红线）。
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

// trendHeight 曲线带高度；trendCPRange 分数量程（±600cp 顶格，将杀压 ±1500 必顶格）。
const (
	trendHeight  = 96
	trendCPRange = 600.0
)

// sectionTrend 走势分区：头部（标题 + 当前胜率小字）+ 折线带。
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
	c.trendCurve = newTrendCurve()
	return bentoTile(container.NewVBox(
		sectionHeader("04", "走势", c.trendWin),
		c.trendCurve,
	))
}

// refreshTrend 刷新周期回调：拉序列快照，键变了才重建折线（1.5s 节奏下避免
// 每轮重排线对象），头部胜率逐轮跟进。
func (c *Console) refreshTrend() {
	if c.opts.TrendSeries == nil || c.trendCurve == nil || c.trendWinSet == nil {
		return
	}
	snap := c.opts.TrendSeries()
	n := len(snap.Points)
	if n == 0 {
		c.trendWinSet("尚无对局数据——引擎出建议后自动记录")
		c.trendWin.Color = qiMuted
		c.trendWin.Refresh()
	} else {
		last := snap.Points[n-1]
		col := qiMuted
		wr := fmt.Sprintf("分值 %s", fmtScore(&last.ScoreCP, nil))
		if last.WinRate >= 0 {
			wr = fmt.Sprintf("胜率 %d%%", last.WinRate)
			switch {
			case last.WinRate >= 55:
				col = qiJade
			case last.WinRate <= 45:
				col = qiDanger
			default:
				col = qiAmber
			}
		}
		c.trendWinSet(wr)
		c.trendWin.Color = col
		c.trendWin.Refresh()
	}
	key := fmt.Sprintf("%s|%d", snap.GameID, n)
	if key != c.trendLastKey {
		c.trendLastKey = key
		c.trendCurve.setSeries(snap.Points)
	}
}

// trendCurve 自绘折线控件：零线 + 相邻点连线。y 轴红方视角——上方=红优，
// ±600cp 顶格；x 轴 = 建议序号（第 1 条建议在左）。
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

// setSeries 换序列并重绘（尺寸未变时直接重排线对象）。
func (t *trendCurve) setSeries(pts []TrendPoint) {
	t.pts = pts
	t.Refresh()
}

func (t *trendCurve) CreateRenderer() fyne.WidgetRenderer {
	return &trendRenderer{c: t}
}

type trendRenderer struct{ c *trendCurve }

func (r *trendRenderer) rebuild(size fyne.Size) {
	c := r.c
	padX, padY := float32(10), float32(6)
	w, h := size.Width, size.Height
	objs := make([]fyne.CanvasObject, 0, len(c.pts)+1)
	// 零线（均势基准）
	zero := canvas.NewLine(color.NRGBA{R: 0xFF, G: 0xFF, B: 0xFF, A: 0x2E})
	zero.Position1 = fyne.NewPos(padX, h/2)
	zero.Position2 = fyne.NewPos(w-padX, h/2)
	objs = append(objs, zero)
	// 折线：相邻点连线；y = 中线 - cp 比例（红优在上）
	if n := len(c.pts); n >= 2 {
		stroke := color.NRGBA(qiZhuBrand)
		for i := 1; i < n; i++ {
			ln := canvas.NewLine(stroke)
			ln.StrokeWidth = 2
			ln.Position1 = fyne.NewPos(r.c.xAt(i-1, n, w, padX), r.c.yAt(c.pts[i-1].ScoreCP, h, padY))
			ln.Position2 = fyne.NewPos(r.c.xAt(i, n, w, padX), r.c.yAt(c.pts[i].ScoreCP, h, padY))
			objs = append(objs, ln)
		}
	}
	c.objs = objs
	canvas.Refresh(c)
}

func (t *trendCurve) xAt(i, n int, w, padX float32) float32 {
	if n <= 1 {
		return w - padX
	}
	return padX + (w-2*padX)*float32(i)/float32(n-1)
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
