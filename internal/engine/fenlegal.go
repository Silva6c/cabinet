package engine

import "strings"

// PositionLegal 校验 FEN 局面对引擎是否安全：行棋方一步之内不能吃到对方将/帅
// （含将帅对脸）、双方王都在盘上且位于九宫内、仕不出九宫、相/象不过河。
// 皮卡鱼对这类局面直接退出进程（"CRITICAL ERROR: ... Reason: Unsupported
// position."→ 进程终止，PC 端表现为引擎 EOF 崩溃），看门狗重启后同一局面重发
// 会再崩成死循环（2026-10-01 实测：录屏识别列偏把不照面的车/将拼到同列；第二轮
// 审查实测：将出九宫/仕出九宫/相过河三类同样秒退）。必须在送引擎前拦截——
// 手机端收到错误提示后点"更新"重新识别或走一步棋即可自愈。
//
// 只做"够拦住皮卡鱼退出条件"级校验，不校验子力数量/马炮兵位置等深层规则
// （兵到底线皮卡鱼正常接受）。
func PositionLegal(fen string) bool {
	parts := strings.Fields(fen)
	if len(parts) < 2 {
		return false
	}
	board, ok := parseBoard(parts[0])
	if !ok {
		return false
	}
	if !pieceZonesLegal(board) {
		return false
	}
	redKing, blackKing := findKings(board)
	if redKing == nil || blackKing == nil {
		return false // 缺王：皮卡鱼 position 解析同样会失败
	}
	// 将帅对脸：同列且中间无子——与谁走无关，恒非法
	if redKing.x == blackKing.x && clearBetween(board, redKing.x, blackKing.y, redKing.y) {
		return false
	}
	if parts[1] != "b" {
		return !attacked(board, blackKing.x, blackKing.y, true)
	}
	return !attacked(board, redKing.x, redKing.y, false)
}

type boardGrid struct {
	cells [10][9]byte // [y][x]，y=0 为 FEN 首行（黑方底线），红方在 y=9
}

// pieceZonesLegal 校验子力区域规则：将/帅与仕必须在各自九宫（x 3..5，黑 y 0..2、
// 红 y 7..9），相/象不得过河（黑 y≤4、红 y≥5）。皮卡鱼对违反者报
// "Unsupported position" 并立即退出（2026-10-01 第二轮审查实测三类全中）。
func pieceZonesLegal(b *boardGrid) bool {
	for y := 0; y < 10; y++ {
		for x := 0; x < 9; x++ {
			switch b.cells[y][x] {
			case 'k', 'a': // 黑将/黑仕：黑方九宫
				if x < 3 || x > 5 || y > 2 {
					return false
				}
			case 'K', 'A': // 红帅/红仕：红方九宫
				if x < 3 || x > 5 || y < 7 {
					return false
				}
			case 'b': // 黑象：不过河
				if y > 4 {
					return false
				}
			case 'B': // 红相：不过河
				if y < 5 {
					return false
				}
			}
		}
	}
	return true
}

type kingPos struct{ x, y int }

// isPieceChar FEN 盘面段的合法棋子字符（红黑各 7 种）。switch 常量比较替代
// strings.ContainsRune 的 14 字符线性扫描——每个棋子字符都要过这里。
func isPieceChar(ch rune) bool {
	switch ch {
	case 'k', 'a', 'b', 'n', 'r', 'c', 'p', 'K', 'A', 'B', 'N', 'R', 'C', 'P':
		return true
	}
	return false
}

// parseBoard 解析 FEN 盘面段（9 列 10 行，'/'分隔，数字为连续空格）。
// 就地按 '/' 扫描、不经过 strings.Split：每条 position 消息要走 1~3 次
// （本函数 + 拒收时的翻转复验），切片加 10 个行子串的临时分配纯属浪费。
func parseBoard(s string) (*boardGrid, bool) {
	var b boardGrid
	y, x := 0, 0
	for _, ch := range s {
		switch {
		case ch == '/':
			if x != 9 || y >= 9 {
				return nil, false
			}
			y++
			x = 0
		case ch >= '1' && ch <= '9':
			x += int(ch - '0')
		case isPieceChar(ch):
			if x > 8 {
				return nil, false
			}
			b.cells[y][x] = byte(ch)
			x++
		default:
			return nil, false
		}
	}
	if y != 9 || x != 9 {
		return nil, false
	}
	return &b, true
}

// findKings 单次全盘扫描同时定位双方王（各取扫描序第一个——重复王属非法
// 盘面但 parseBoard 不拦，沿用旧 findKing 的"取第一个"语义）。双王齐即
// 提前退出；旧实现分红黑各扫一遍，最多 2×90 格全扫。
func findKings(b *boardGrid) (red, black *kingPos) {
	for y := 0; y < 10; y++ {
		for x := 0; x < 9; x++ {
			switch b.cells[y][x] {
			case 'K':
				if red == nil {
					red = &kingPos{x, y}
				}
			case 'k':
				if black == nil {
					black = &kingPos{x, y}
				}
			}
		}
		if red != nil && black != nil {
			return red, black
		}
	}
	return red, black
}

// clearBetween 同列 y1..y2 之间（不含端点）是否无子
func clearBetween(b *boardGrid, x, y1, y2 int) bool {
	lo, hi := y1, y2
	if lo > hi {
		lo, hi = hi, lo
	}
	for y := lo + 1; y < hi; y++ {
		if b.cells[y][x] != 0 {
			return false
		}
	}
	return true
}

// attacked 判定 (x,y) 处的王是否被 byRed 方攻击。中国象棋能吃到对方王的只有
// 车马炮兵（将帅对脸已单独判；仕相攻不过河，无需检查）。
func attacked(b *boardGrid, x, y int, byRed bool) bool {
	// 攻击方字符：byRed=红方攻击（大写 R/C/N/P），否则黑方（小写）
	attRook, attCannon, attHorse, attPawn := byte('R'), byte('C'), byte('N'), byte('P')
	if !byRed {
		attRook, attCannon, attHorse, attPawn = 'r', 'c', 'n', 'p'
	}
	inBoard := func(px, py int) bool { return px >= 0 && px < 9 && py >= 0 && py < 10 }

	// 车：四向直线首个子是攻击方车（对脸已单独判，这里只看车）
	for _, d := range [4][2]int{{0, -1}, {0, 1}, {-1, 0}, {1, 0}} {
		px, py := x+d[0], y+d[1]
		for inBoard(px, py) {
			c := b.cells[py][px]
			if c != 0 {
				if c == attRook {
					return true
				}
				break
			}
			px, py = px+d[0], py+d[1]
		}
	}
	// 炮：四向直线上越过恰好一个炮架后的首个子是攻击方炮
	for _, d := range [4][2]int{{0, -1}, {0, 1}, {-1, 0}, {1, 0}} {
		px, py := x+d[0], y+d[1]
		screened := false
		for inBoard(px, py) {
			c := b.cells[py][px]
			if c == 0 {
				px, py = px+d[0], py+d[1]
				continue
			}
			if !screened {
				screened = true // 第一个子是炮架
			} else {
				if c == attCannon {
					return true
				}
				break
			}
			px, py = px+d[0], py+d[1]
		}
	}
	// 马：8 个日字位，蹩马腿（腿在马位朝王方向的主轴邻格）
	for _, d := range [8][2]int{{1, 2}, {2, 1}, {2, -1}, {1, -2}, {-1, -2}, {-2, -1}, {-2, 1}, {-1, 2}} {
		mx, my := x+d[0], y+d[1]
		if !inBoard(mx, my) || b.cells[my][mx] != attHorse {
			continue
		}
		var lx, ly int
		if d[0] == 2 || d[0] == -2 {
			lx, ly = mx-d[0]/2, my
		} else {
			lx, ly = mx, my-d[1]/2
		}
		if b.cells[ly][lx] == 0 {
			return true
		}
	}
	// 兵：红兵攻击其上方一格，过河（兵自身 y<=4）后还攻击左右；黑卒对称
	if byRed {
		if py := y + 1; py < 10 && b.cells[py][x] == attPawn {
			return true
		}
	} else {
		if py := y - 1; py >= 0 && b.cells[py][x] == attPawn {
			return true
		}
	}
	// 两个过河条件按"被攻王的 y"而非兵的 y 判定，在 pieceZonesLegal 通过后
	// 恒真：黑王 y≤2 → 红兵在 y+1≤3 必已过河；红王 y≥7 → 黑卒在 y-1≥6 必
	// 已过河。看似错位实为九宫前提下的等价简写；若将来放宽九宫校验，须同步
	// 复核此处——极易被当成 off-by-one"修正"反而改错。
	if (byRed && y <= 4) || (!byRed && y >= 5) {
		for _, d := range [2]int{-1, 1} {
			if px := x + d; px >= 0 && px < 9 && b.cells[y][px] == attPawn {
				return true
			}
		}
	}
	return false
}
