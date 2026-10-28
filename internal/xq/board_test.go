package xq

import (
	"testing"

	"cabinet/internal/book"
)

const startFen = "rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR w - - 0 1"

// countFrom 统计起点为 v 的伪合法走子数。
func countFrom(b book.Board, redGo bool, v Vec) int {
	n := 0
	for _, m := range PseudoMoves(b, redGo) {
		if m.From == v {
			n++
		}
	}
	return n
}

// TestPseudoMoves_Start 开局各子着法数（逐子手核，不依赖"开局总数"这类口口
// 相传的数字）。y 轴：y=0 黑底、y=9 红底；红兵前进 = y 减小（rank=9-y 增大）。
//   - 兵 5×1（未过河只进不横；前进格行 5 全空）
//   - 马 2×2（b9→a7/c7；d8 被 c9 相蹩腿、a7 方向 b8 空不蹩——蹩马腿）
//   - 车 2×2（a9→a8/a7 后被 a6 红兵挡；横被 b9 马挡）
//   - 炮 2×12（b2：横 6 + 竖下平移 4 + 隔 b4? 无——竖下 y=6..3 空 4 格后
//     y=2 b7 黑炮可吃 1、再被相断；竖上仅 b1 平移 1，b0 己马架后出界）
//   - 帅 1（e1 宫心；d9/f9 被仕占）、仕 1（e1；c1 出宫被拒）
//   - 相 2+2（c9→a7/e7 均红半场；g9→e7/i7）
func TestPseudoMoves_Start(t *testing.T) {
	b, redGo, err := book.ParseFEN(startFen)
	if err != nil {
		t.Fatal(err)
	}
	if !redGo {
		t.Fatal("开局应为红方行棋")
	}
	cases := []struct {
		name string
		v    Vec
		want int
	}{
		{"红兵 a6 进", Vec{0, 6}, 1},
		{"b9 马", Vec{1, 9}, 2},
		{"h9 马", Vec{7, 9}, 2},
		{"a9 车", Vec{0, 9}, 2},
		{"i9 车", Vec{8, 9}, 2},
		{"b2 炮", Vec{1, 7}, 12},
		{"h2 炮", Vec{7, 7}, 12},
		{"帅 e9", Vec{4, 9}, 1},
		{"仕 d9", Vec{3, 9}, 1},
		{"相 c9", Vec{2, 9}, 2},
		{"相 g9", Vec{6, 9}, 2},
	}
	for _, c := range cases {
		if got := countFrom(b, redGo, c.v); got != c.want {
			t.Errorf("%s (%v): got %d want %d", c.name, c.v, got, c.want)
		}
	}
}

// TestPseudoLegal_Boundaries 边界语义：蹩马腿/塞象眼/出宫/照面步不生成。
func TestPseudoLegal_Boundaries(t *testing.T) {
	b, redGo, _ := book.ParseFEN(startFen)
	// h9 马去 f8：腿 (6,9)=g9 相 → 蹩
	if PseudoLegal(b, redGo, Move{Vec{7, 9}, Vec{5, 8}}) {
		t.Error("h9 马→f8 应被 g9 相蹩腿拒绝")
	}
	// 仕 d9 出宫（c1 = x2 越出 x3-5 宫带）拒绝
	if PseudoLegal(b, redGo, Move{Vec{3, 9}, Vec{2, 8}}) {
		t.Error("仕出九宫应拒绝")
	}
	// 塞象眼：e2 相去 c0 的眼 (3,8)=d1 被红兵占 → 拒
	eyeFen := "4k4/9/9/9/9/9/9/4B4/3P5/4K4 w - - 0 1"
	eb, ered, err := book.ParseFEN(eyeFen)
	if err != nil {
		t.Fatal(err)
	}
	if PseudoLegal(eb, ered, Move{Vec{4, 7}, Vec{2, 9}}) {
		t.Error("塞象眼应拒绝")
	}
	if !PseudoLegal(eb, ered, Move{Vec{4, 7}, Vec{6, 9}}) {
		t.Error("另一向象步（眼 f1 空）应合法")
	}
	// 帅照面步不生成：构造对脸局面（合法局面里不会出现，直接调 PseudoLegal 防双保险）
	faceFen := "4k4/9/9/9/9/9/9/9/9/4K4 w - - 0 1"
	fb, fred, _ := book.ParseFEN(faceFen)
	if PseudoLegal(fb, fred, Move{Vec{4, 9}, Vec{4, 0}}) {
		t.Error("帅将照面步不应生成")
	}
}

// TestUcci_RoundTrip UcciOf/ParseUcci 恒等与 rank 翻转正确性。
func TestUcci_RoundTrip(t *testing.T) {
	for _, s := range []string{"h2e2", "a0a9", "i9i0", "b2b0"} {
		m, ok := ParseUcci(s)
		if !ok {
			t.Fatalf("%s 解析失败", s)
		}
		if got := UcciOf(m); got != s {
			t.Errorf("UcciOf(ParseUcci(%q)) = %q", s, got)
		}
	}
	if _, ok := ParseUcci("h2e"); ok {
		t.Error("短串应拒绝")
	}
	if _, ok := ParseUcci("j2e2"); ok {
		t.Error("越界列应拒绝")
	}
}

// TestDiffSingleMove 中炮开局一步的 diff 推导与无效形态排除。
func TestDiffSingleMove(t *testing.T) {
	after := "rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C2C4/9/RNBAKABNR b - - 0 1"
	got, ok := DiffSingleMove(startFen, after)
	if !ok || got != "h2e2" {
		t.Fatalf("应推导出 h2e2: got (%q,%v)", got, ok)
	}
	// 悔棋形态：new 的行棋方与 prev 相同（走子方未翻转）→ 拒
	if _, ok := DiffSingleMove(startFen, "rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C2C4/9/RNBAKABNR w - - 0 1"); ok {
		t.Error("行棋方未翻转的两格差异应拒绝（更新轮次/编辑特征）")
	}
	// 多格变化（手动编辑）→ 拒
	multi := "rnbakab1r/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C2C4/9/RNBAKABNR b - - 0 1"
	if _, ok := DiffSingleMove(startFen, multi); ok {
		t.Error("多格变化应拒绝")
	}
	// 三格以上（吃子+走子混合）→ 拒
	if _, ok := DiffSingleMove(startFen, "rnbakabnr/9/1c5c1/p1p1p1p1p/9/4P4/8P/1C2C4/9/RNBAKABNR b - - 0 1"); ok {
		t.Error("非单步形态应拒绝")
	}
}

// TestFenAfter 应用一步得新 FEN（行棋方翻转、盘面段重算、后段保留）。
func TestFenAfter(t *testing.T) {
	got, ok := FenAfter(startFen, "h2e2")
	// h2 炮平中 → 行 7 "1C5C1" 变为 x1 C、x4 C、x7 空：1空 C 2空 C 4空 = "1C2C4"
	want := "rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C2C4/9/RNBAKABNR b - - 0 1"
	if !ok || got != want {
		t.Fatalf("FenAfter(h2e2) = (%q,%v), want %q", got, ok, want)
	}
	// 迭代两步：黑跳左马（b9=(1,0) 黑马——黑底 rank 9 对应 y=0——到 c7=(2,2)，
	// 腿 (1,1) 空）验证链式演算
	next, ok := FenAfter(got, "b9c7")
	if !ok {
		t.Fatal("黑方 b9c7 应合法")
	}
	// 行 0 黑马离位 "r1bakabnr"；行 2 原 "1c5c1"（x1 黑炮），马落 x2 = "1cn4c1"
	want2 := "r1bakabnr/9/1cn4c1/p1p1p1p1p/9/9/P1P1P1P1P/1C2C4/9/RNBAKABNR w - - 0 1"
	if next != want2 {
		t.Fatalf("第二步 FEN = %q, want %q", next, want2)
	}
	// 非法着法（开局 e9 帅直出宫）拒绝
	if _, ok := FenAfter(startFen, "e9e6"); ok {
		t.Error("帅出宫应拒绝")
	}
}
