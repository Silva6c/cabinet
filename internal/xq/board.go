// Package xq 棋盘走子规则纯函数（PC 端唯一事实源）。
//
// 棋子编码与 Board 类型直接复用 internal/book（Java 端权威移植），本包只做
// "走子"维度的计算，服务两处消费方：
//   - DiffSingleMove：两个相邻确认局面的盘面段 diff，恰 2 格差异且能被唯一
//     一步伪合法走子解释时返回该着法（采纳率行为信号 2026-10-17）
//   - FenAfter：FEN 应用一步着法得新 FEN（复盘 PV 全链中文翻译的地基——
//     console/movecn.go 只能译"给定局面的第一步"，整条 PV 需要逐步演算）
//
// 伪合法走子语义与手机端 AssistBoard.pseudoMoves 同源同义：不查"送将/被将"
// （走子后己王是否被攻击）——采纳比对与 PV 翻译都是展示层用途，历史终值
// 的 PV 由引擎自己保证；将帅照面步不生成（照面局面在协议入口已被
// ValidateFEN 拒绝，不构成有效输入）。
package xq

import (
	"fmt"
	"strings"

	"cabinet/internal/book"
)

// Vec 棋盘格坐标（x=0..8 列自左向右，y=0..9 行自上而下，y=0 为黑方底线）。
type Vec struct{ X, Y int }

// Move 一步走子（from→to；to 有敌子即吃子）。
type Move struct{ From, To Vec }

// IsRed 棋子是否红方（book 编码 1-7 红、8-14 黑）。
func IsRed(p int) bool { return p >= 1 && p <= 7 }

// UcciOf 着法 → UCCI 4 字符坐标串（"h2e2"：起点 rank 在前，与引擎/协议一致）。
// rank 与 FEN 行号相反：rank = 9 - y。
func UcciOf(m Move) string {
	return fmt.Sprintf("%c%d%c%d", 'a'+m.From.X, 9-m.From.Y, 'a'+m.To.X, 9-m.To.Y)
}

// ParseUcci UCCI 4 字符坐标串 → 着法；非法输入返回 false。
func ParseUcci(s string) (Move, bool) {
	if len(s) != 4 {
		return Move{}, false
	}
	fx, tx := s[0]-'a', s[2]-'a'
	fy, ty := '9'-s[1], '9'-s[3]
	// 坐标运算是无符号 byte：下溢回绕成大值，只会被下方上界比较捕获
	//（如 '9'-'z' = 201 > 9），故只查上界即可覆盖全部非法输入
	if fx > 8 || tx > 8 || fy > 9 || ty > 9 {
		return Move{}, false
	}
	return Move{From: Vec{int(fx), int(fy)}, To: Vec{int(tx), int(ty)}}, true
}

// isOwn 目标格是否己方子（己方子不可落）。
func isOwn(b book.Board, redGo bool, v Vec) bool {
	p := b[v.Y][v.X]
	return p != book.Empty && IsRed(p) == redGo
}

// pieceTargets 单子目标格枚举（按子力规则；不含照面步）。
func pieceTargets(b book.Board, from Vec, p int) []Vec {
	red := IsRed(p)
	var out []Vec
	add := func(v Vec) bool {
		// 越界或己方子 → 停；空/敌子 → 可落
		if v.X < 0 || v.X > 8 || v.Y < 0 || v.Y > 9 || isOwn(b, red, v) {
			return false
		}
		out = append(out, v)
		return true
	}
	// 九宫内判定（帅仕专用）
	inPalace := func(v Vec) bool {
		return v.X >= 3 && v.X <= 5 && ((red && v.Y >= 7) || (!red && v.Y <= 2))
	}
	switch p {
	case book.WShuai, book.BJiang: // 帅/将：九宫内直走一步
		for _, d := range [][2]int{{0, 1}, {0, -1}, {1, 0}, {-1, 0}} {
			v := Vec{from.X + d[0], from.Y + d[1]}
			if inPalace(v) {
				add(v)
			}
		}
	case book.WShi, book.BShi: // 仕/士：九宫内斜走一步
		for _, d := range [][2]int{{1, 1}, {1, -1}, {-1, 1}, {-1, -1}} {
			v := Vec{from.X + d[0], from.Y + d[1]}
			if inPalace(v) {
				add(v)
			}
		}
	case book.WXiang, book.BXiang: // 相/象：田字斜走两步、不过河、塞象眼
		for _, d := range [][2]int{{2, 2}, {2, -2}, {-2, 2}, {-2, -2}} {
			v := Vec{from.X + d[0], from.Y + d[1]}
			// 过河约束：红相不动出 y<5，黑象不动出 y>4
			if v.X < 0 || v.X > 8 || v.Y < 0 || v.Y > 9 {
				continue
			}
			if red && v.Y < 5 || !red && v.Y > 4 {
				continue
			}
			// 象眼 = 田字中心
			eye := Vec{from.X + d[0]/2, from.Y + d[1]/2}
			if b[eye.Y][eye.X] != book.Empty {
				continue
			}
			add(v)
		}
	case book.WMa, book.BMa: // 马：日字八向、蹩马腿（先直一格处有子即蹩）
		type hop struct{ leg, to [2]int }
		for _, h := range []hop{
			{[2]int{0, 1}, [2]int{1, 2}}, {[2]int{0, 1}, [2]int{-1, 2}},
			{[2]int{0, -1}, [2]int{1, -2}}, {[2]int{0, -1}, [2]int{-1, -2}},
			{[2]int{1, 0}, [2]int{2, 1}}, {[2]int{1, 0}, [2]int{2, -1}},
			{[2]int{-1, 0}, [2]int{-2, 1}}, {[2]int{-1, 0}, [2]int{-2, -1}},
		} {
			legV := Vec{from.X + h.leg[0], from.Y + h.leg[1]}
			if legV.X < 0 || legV.X > 8 || legV.Y < 0 || legV.Y > 9 ||
				b[legV.Y][legV.X] != book.Empty {
				continue
			}
			add(Vec{from.X + h.to[0], from.Y + h.to[1]})
		}
	case book.WJu, book.BJu: // 车：四向直线滑动
		for _, d := range [][2]int{{0, 1}, {0, -1}, {1, 0}, {-1, 0}} {
			v := Vec{from.X + d[0], from.Y + d[1]}
			for add(v) { // add=false（越界/己方子）或吃子后停止推进
				p2 := b[v.Y][v.X]
				if p2 != book.Empty {
					break // 吃到敌子即停
				}
				v = Vec{v.X + d[0], v.Y + d[1]}
			}
		}
	case book.WPao, book.BPao: // 炮：直线平移（不吃）+ 隔一子吃
		for _, d := range [][2]int{{0, 1}, {0, -1}, {1, 0}, {-1, 0}} {
			v := Vec{from.X + d[0], from.Y + d[1]}
			screen := false
			for v.X >= 0 && v.X <= 8 && v.Y >= 0 && v.Y <= 9 {
				p2 := b[v.Y][v.X]
				if !screen {
					if p2 == book.Empty {
						out = append(out, v) // 平移段
					} else {
						screen = true // 遇第一个子（炮架）
					}
				} else if p2 != book.Empty {
					if !isOwn(b, red, v) {
						out = append(out, v) // 隔子吃敌
					}
					break
				}
				v = Vec{v.X + d[0], v.Y + d[1]}
			}
		}
	case book.WBing, book.BZu: // 兵/卒：前进一步；过河后可横走
		fwd := -1 // 红兵 y 减小为前进
		if !red {
			fwd = 1
		}
		add(Vec{from.X, from.Y + fwd})
		crossed := (red && from.Y <= 4) || (!red && from.Y >= 5)
		if crossed {
			add(Vec{from.X - 1, from.Y})
			add(Vec{from.X + 1, from.Y})
		}
	}
	return out
}

// PseudoLegal m 是否为 b 局面下 redGo 一方的一步伪合法走子。
func PseudoLegal(b book.Board, redGo bool, m Move) bool {
	if m.From.X < 0 || m.From.X > 8 || m.From.Y < 0 || m.From.Y > 9 ||
		m.To.X < 0 || m.To.X > 8 || m.To.Y < 0 || m.To.Y > 9 {
		return false
	}
	p := b[m.From.Y][m.From.X]
	if p == book.Empty || IsRed(p) != redGo {
		return false
	}
	if isOwn(b, redGo, m.To) {
		return false
	}
	for _, t := range pieceTargets(b, m.From, p) {
		if t == m.To {
			return true
		}
	}
	return false
}

// DiffSingleMove 推导 prevFen → newFen 的实际走子：盘面段逐格 diff 恰 2 格差异
// 且被唯一一步伪合法走子解释时返回 UCCI 着法；悔棋/手动编辑/多格变化/轮次
// 翻转等一切无法单步解释的情形返回 false（调用方按"无效比对"处理）。
// 行棋方一致性也验证：实际走子方必须是 prevFen 的行棋方（悔棋的特征即
// "new 走子方的一步"，此处自然排除）。
func DiffSingleMove(prevFen, newFen string) (string, bool) {
	oldB, prevRed, err := book.ParseFEN(prevFen)
	if err != nil {
		return "", false
	}
	newB, newRed, err := book.ParseFEN(newFen)
	if err != nil {
		return "", false
	}
	// 收集差异格（盘面段；FEN 后段不参与——走子方/回合计入显式校验）
	var diffs []Vec
	for y := 0; y < 10; y++ {
		for x := 0; x < 9; x++ {
			if oldB[y][x] != newB[y][x] {
				diffs = append(diffs, Vec{x, y})
			}
		}
	}
	// 恰 2 格：from（旧有新空）+ to（新盘与我盘不同、且新盘该格 = from 原子）
	if len(diffs) != 2 {
		return "", false
	}
	a, b2 := diffs[0], diffs[1]
	// 确定 from：旧盘有子、新盘为空的那一格；两格都不满足即非单步移动
	pickFrom := func(v Vec) bool {
		return oldB[v.Y][v.X] != book.Empty && newB[v.Y][v.X] == book.Empty
	}
	var from, to Vec
	switch {
	case pickFrom(a) && !pickFrom(b2):
		from, to = a, b2
	case pickFrom(b2) && !pickFrom(a):
		from, to = b2, a
	default:
		return "", false
	}
	piece := oldB[from.Y][from.X]
	// 移动/吃子后 to 格必须恰为 from 原子（排除"两格各自变化"的置换形态）
	if newB[to.Y][to.X] != piece {
		return "", false
	}
	// 走子方一致性：子必须属于 prevFen 行棋方（否则是悔棋/编辑特征）
	if IsRed(piece) != prevRed {
		return "", false
	}
	m := Move{From: from, To: to}
	if !PseudoLegal(oldB, prevRed, m) {
		return "", false
	}
	// newFen 行棋方应翻转（单步走子后必然换行）——不一致说明 FEN 自相矛盾
	if newRed == prevRed {
		return "", false
	}
	return UcciOf(m), true
}

// FenAfter 在 fen 上应用一步 ucci 得新 FEN：行棋方翻转、盘面段重算、
// 无吃子位/回合计数等后段原样保留（PV 中文翻译只消费盘面段与行棋方，
// 计数精度无展示语义）。着法非法（无子/不属于行棋方/违反子力规则）返回 false。
func FenAfter(fen, ucci string) (string, bool) {
	b, redGo, err := book.ParseFEN(fen)
	if err != nil {
		return "", false
	}
	m, ok := ParseUcci(ucci)
	if !ok {
		return "", false
	}
	if !PseudoLegal(b, redGo, m) {
		return "", false
	}
	b[m.To.Y][m.To.X] = b[m.From.Y][m.From.X]
	b[m.From.Y][m.From.X] = book.Empty
	parts := strings.Split(strings.TrimSpace(fen), " ")
	if len(parts) != 6 {
		return "", false
	}
	side := "b"
	if !redGo { // 走完一步翻转
		side = "w"
	}
	parts[0] = serializeBoard(b)
	parts[1] = side
	return strings.Join(parts, " "), true
}

// serializeBoard 盘面段序列化（xqbase 风格：连续空格计数，红大写黑小写）。
func serializeBoard(b book.Board) string {
	var sb strings.Builder
	for y := 0; y < 10; y++ {
		run := 0
		if y > 0 {
			sb.WriteByte('/')
		}
		for x := 0; x < 9; x++ {
			p := b[y][x]
			if p == book.Empty {
				run++
				continue
			}
			if run > 0 {
				sb.WriteByte(byte('0' + run))
				run = 0
			}
			sb.WriteByte(fenChar(p))
		}
		if run > 0 {
			sb.WriteByte(byte('0' + run))
		}
	}
	return sb.String()
}

// fenChar 棋子编码 → FEN 字符（红大写黑小写；book.ParseFEN 的逆映射子集）。
func fenChar(p int) byte {
	switch p {
	case book.WShuai:
		return 'K'
	case book.WShi:
		return 'A'
	case book.WXiang:
		return 'B'
	case book.WMa:
		return 'N'
	case book.WJu:
		return 'R'
	case book.WPao:
		return 'C'
	case book.WBing:
		return 'P'
	case book.BJiang:
		return 'k'
	case book.BShi:
		return 'a'
	case book.BXiang:
		return 'b'
	case book.BMa:
		return 'n'
	case book.BJu:
		return 'r'
	case book.BPao:
		return 'c'
	case book.BZu:
		return 'p'
	}
	return '.' // 不可达：编码非法
}
