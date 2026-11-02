package xq

// legal_moves_test.go LegalMoves 合法着法枚举回归（2026-11-02 智能思考预判的
// 地基）：三已知事实锚——初始局面红方恒 44 步（文献公认值，兵挡车/马蹩腿/
// 炮无屏不吃全要算对才算得出）；黑车贴脸将军且黑卒封住一侧逃位的局面恰 1 步
// 应将（垫子/吃子/移王全被排干的极限构造）；车在双将同列的遮挡位横移必照面
// 被滤、沿线移动保留。

import (
	"testing"

	"cabinet/internal/book"
)

func countLegal(t *testing.T, fen string, redGo bool) []Move {
	t.Helper()
	b, _, err := book.ParseFEN(fen)
	if err != nil {
		t.Fatalf("ParseFEN(%q): %v", fen, err)
	}
	return LegalMoves(b, redGo)
}

func TestLegalMovesInitial44(t *testing.T) {
	ms := countLegal(t, "rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR w - - 0 1", true)
	if len(ms) != 44 {
		t.Fatalf("初始局面红方应 44 步（兵挡车/马蹩腿/炮无屏不吃全要算对）: %d", len(ms))
	}
}

func TestLegalMovesSingleForced(t *testing.T) {
	// 黑车 e9 顺列将军红帅 e0（中间全空）；黑卒 d8 攻 d0 封死左逃位；红方
	// 仅帅一子——e1 仍在将线上被滤，d0 被卒攻击，唯一着法 = 帅 e0→f0。
	ms := countLegal(t, "3k5/9/9/9/9/9/9/9/3p5/4K4 w - - 0 1", true)
	if len(ms) != 1 {
		t.Fatalf("唯一应将局面应恰 1 步: %d %+v", len(ms), ms)
	}
	if ms[0].To.X != 5 || ms[0].To.Y != 9 {
		t.Fatalf("唯一着法应为帅 e0→f0: %+v", ms[0])
	}
}

func TestLegalMovesFacingFiltered(t *testing.T) {
	// 红车 e5 在双将同列（黑将 e9 / 红帅 e0）中间遮挡：车的横移离开 e 列即
	// 照面必须被滤、沿 e 列滑行（含进吃黑将格）保留；帅横移出照面列合法。
	ms := countLegal(t, "4k4/9/9/9/4R4/9/9/9/9/4K4 w - - 0 1", true)
	rookMoves := 0
	for _, m := range ms {
		if m.From.X == 4 && m.From.Y == 4 {
			rookMoves++
			if m.To.X != 4 {
				t.Fatalf("照面位车的横移未滤: %+v", m)
			}
		}
	}
	if rookMoves == 0 {
		t.Fatal("车沿线滑行不应被滤")
	}
}
