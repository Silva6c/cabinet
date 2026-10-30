package console

import "testing"

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
