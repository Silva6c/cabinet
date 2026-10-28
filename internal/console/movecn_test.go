// movecn_test.go 中文纵线记法转换的锚点与边界。
package console

import "testing"

const (
	// 起始局面（红先）
	cnStartFEN = "rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR w - - 0 1"
	// 同盘面黑先行
	cnStartBlack = "rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR b - - 0 1"
	// mockphone 冒烟用的出谱中局（红先）
	cnMidFEN = "r1bakabnr/9/nc5c1/p1p1p1p1p/9/4P4/P1P12P1P/1C5C1/9/RNBAKABNR w - - 0 1"
	// 红方双炮同列（7 路，行 7 与行 5）：前炮消歧
	cnTwinCannon = "9/9/9/9/9/7C1/9/7C1/9/4K4 w - - 0 1"
	// 红方双兵同列（1 路，行 4 与行 6）：前兵消歧
	cnTwinPawn = "9/9/9/9/P8/9/P8/9/9/4K4 w - - 0 1"
)

func TestMoveCN(t *testing.T) {
	cases := []struct {
		name string
		fen  string
		move string
		want string
	}{
		// 锚点：皮卡鱼实测 bestmove h2e2 ⇔ 炮二平五（2026-10-03 mockphone 冒烟）
		{"红炮平中（锚点）", cnMidFEN, "h2e2", "炮二平五"},
		{"另一门炮平中", cnStartFEN, "b2e2", "炮八平五"},
		{"红马直进", cnStartFEN, "b0c2", "马八进七"},
		{"黑马直进", cnStartBlack, "h9g7", "马八进七"},
		{"红车横移", cnStartFEN, "a0b0", "车九平八"},
		{"红车直进", cnStartFEN, "a0a1", "车九进一"},
		{"红兵挺进", cnStartFEN, "c3c4", "兵七进一"},
		{"黑炮平中", cnStartBlack, "b7e7", "炮二平五"},
		{"红仕斜进", cnStartFEN, "f0e1", "仕四进五"},
		{"同列双炮取前", cnTwinCannon, "h4e4", "前炮平五"},
		{"同列双炮取后", cnTwinCannon, "h2e2", "后炮平五"},
		{"同列双兵取前", cnTwinPawn, "a5a6", "前兵进一"},
		{"着法过短", cnMidFEN, "h2e", ""},
		{"纵线越界", cnMidFEN, "z2e2", ""},
		{"目标纵线越界", cnMidFEN, "h2z2", ""},
		{"起点无子", cnMidFEN, "a5a6", ""},
		{"行棋方不符（红先走黑子）", cnMidFEN, "h7e7", ""},
		{"FEN 行数不对", "9/9/9 w", "h2e2", ""},
		{"FEN 缺行棋方", "rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR", "h2e2", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := moveCN(c.fen, c.move); got != c.want {
				t.Fatalf("moveCN(%q, %q) = %q，期望 %q", c.fen, c.move, got, c.want)
			}
		})
	}
}

// TestPVCN 整条 PV 的中文记法（复盘"变招"列 2026-10-17）：三步标准开局链
// 逐步演算；中途断链（非法着法）回退原文并停止。
func TestPVCN(t *testing.T) {
	got := pvCN(cnStartFEN, []string{"h2e2", "h9g7", "h0g2"})
	want := "炮二平五、马八进七、马二进三"
	if got != want {
		t.Fatalf("PV 中文链 = %q, want %q", got, want)
	}
	// 断链：第二着非法（黑马 h9 走 h6 不存在），第一着照常、回退原文停止
	got2 := pvCN(cnStartFEN, []string{"h2e2", "h9h6", "h0g2"})
	if got2 != "炮二平五、h9h6" {
		t.Fatalf("断链 PV = %q", got2)
	}
	// 空 PV：空串
	if pvCN(cnStartFEN, nil) != "" {
		t.Fatal("空 PV 应为空串")
	}
}
