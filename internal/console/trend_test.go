package console

import (
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/test"
)

// TestTrendView 走势视角换算（2026-10-29）：手机上报执色（position.my_red）后
// 按我方视角展示——执黑取负/取补；旧手机未上报退回红方视角；无 WDL(-1) 不取补。
func TestTrendView(t *testing.T) {
	cases := []struct {
		name      string
		p         TrendPoint
		wantCP    int
		wantWR    int
		wantLabel string
	}{
		{"执红原样", TrendPoint{ScoreCP: 120, WinRate: 62, MyRed: true, MySideKnown: true}, 120, 62, "我方"},
		{"执黑翻转", TrendPoint{ScoreCP: 120, WinRate: 62, MyRed: false, MySideKnown: true}, -120, 38, "我方"},
		{"执黑负分转正", TrendPoint{ScoreCP: -300, WinRate: 30, MyRed: false, MySideKnown: true}, 300, 70, "我方"},
		{"执黑无WDL只翻分数", TrendPoint{ScoreCP: -80, WinRate: -1, MyRed: false, MySideKnown: true}, 80, -1, "我方"},
		{"未上报退回红方", TrendPoint{ScoreCP: 120, WinRate: 62}, 120, 62, "红方"},
		{"未上报负分", TrendPoint{ScoreCP: -45, WinRate: -1}, -45, -1, "红方"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cp, wr, label := trendView(tc.p)
			if cp != tc.wantCP || wr != tc.wantWR || label != tc.wantLabel {
				t.Fatalf("trendView=%d/%d/%s, want %d/%d/%s",
					cp, wr, label, tc.wantCP, tc.wantWR, tc.wantLabel)
			}
		})
	}
}

// TestTrendCurveYOf 执黑时曲线方向必须翻转：同一分数，我方执黑应画在零线下方
// （我方劣势），执红画在上方——即"红优"不再被误当"我优"。
func TestTrendCurveYOf(t *testing.T) {
	c := newTrendCurve()
	const h, padY = 96, 6
	red := c.yOf(TrendPoint{ScoreCP: 300, MyRed: true, MySideKnown: true}, h, padY)
	black := c.yOf(TrendPoint{ScoreCP: 300, MyRed: false, MySideKnown: true}, h, padY)
	if !(red < h/2 && black > h/2) {
		t.Fatalf("执红 y=%.1f 应在上方(%.1f 以下)，执黑 y=%.1f 应在下方（零线 %.1f）",
			red, float64(h)/2, black, float64(h)/2)
	}
	// 未知执色：沿用红方视角（与字段引入前一致）
	if unknown := c.yOf(TrendPoint{ScoreCP: 300}, h, padY); unknown != red {
		t.Fatalf("未上报执色 y=%.1f，应与红方视角一致(%.1f)", unknown, red)
	}
}

// testTrendPoints 构造 n 点走势序列（分数在 ±200 内起伏，填充落在零线两侧）。
func testTrendPoints(n int) []TrendPoint {
	pts := make([]TrendPoint, n)
	for i := range pts {
		pts[i] = TrendPoint{
			ScoreCP:     (i%5 - 2) * 100,
			WinRate:     50,
			MyRed:       true,
			MySideKnown: true,
		}
	}
	return pts
}

// countFillLines 数面积填充竖线（低 alpha 的优/劣填充色）。
func countFillLines(objs []fyne.CanvasObject) int {
	n := 0
	for _, o := range objs {
		ln, ok := o.(*canvas.Line)
		if !ok {
			continue
		}
		if ln.StrokeColor == trendUpFill || ln.StrokeColor == trendDownFill {
			n++
		}
	}
	return n
}

// countCurveSegs 数平滑曲线微段（不透明描边色）。
func countCurveSegs(objs []fyne.CanvasObject) int {
	n := 0
	for _, o := range objs {
		ln, ok := o.(*canvas.Line)
		if !ok {
			continue
		}
		if ln.StrokeColor == trendUpStroke || ln.StrokeColor == trendDownStroke {
			n++
		}
	}
	return n
}

// TestTrendRendererLayoutIdempotent 锁定走势渲染生命周期（2026-11-11 待机
// CPU 事故回归）：同尺寸 Layout 必须幂等——不换图元对象、不置脏。否则 fyne
// 每帧 EnsureMinSize 会把"新节点 minSize=0"判成子项尺寸变化并再回调 Layout，
// 与 rebuild 互相触发形成 60fps 自持重建环（活堆 780MB、GC 全核空转）。
func TestTrendRendererLayoutIdempotent(t *testing.T) {
	tc := newTrendCurve()
	tc.pts = testTrendPoints(6)
	tc.Resize(fyne.NewSize(600, trendHeight))
	r := test.WidgetRenderer(tc).(*trendRenderer)
	if len(r.Objects()) == 0 {
		t.Fatal("首次布局后应有图元")
	}
	first := append([]fyne.CanvasObject(nil), r.Objects()...)

	// ① 同尺寸连续 Layout：图元数量与元素指针身份都不变
	r.Layout(fyne.NewSize(600, trendHeight))
	r.Layout(fyne.NewSize(600, trendHeight))
	again := r.Objects()
	if len(again) != len(first) {
		t.Fatalf("同尺寸 Layout 后图元数量变化: %d → %d", len(first), len(again))
	}
	for i := range first {
		if first[i] != again[i] {
			t.Fatalf("同尺寸 Layout 不得重建图元（第 %d 个对象身份变化）", i)
		}
	}

	// ② 尺寸变化：真重建一次，填充竖线数量随宽度增加
	fillBefore := countFillLines(first)
	tc.Resize(fyne.NewSize(900, trendHeight))
	resized := r.Objects()
	fillAfter := countFillLines(resized)
	if fillAfter <= fillBefore {
		t.Fatalf("宽度 600→900 应增加填充竖线: %d → %d", fillBefore, fillAfter)
	}

	// ③ setSeries 改数据：曲线重建，微段数随点数增加
	segsBefore := countCurveSegs(resized)
	tc.setSeries(testTrendPoints(12))
	segsAfter := countCurveSegs(r.Objects())
	if segsAfter <= segsBefore {
		t.Fatalf("点数 6→12 应增加曲线微段: %d → %d", segsBefore, segsAfter)
	}
}
