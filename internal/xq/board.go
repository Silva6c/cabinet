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
// "new 走子方的一步"，此处自然排除），且 newFen 行棋方必须翻转（单步走子后
// 必然换行——不一致说明 FEN 自相矛盾）。
func DiffSingleMove(prevFen, newFen string) (string, bool) {
	mv, moverRed, ok := DiffSingleMoveBoard(prevFen, newFen)
	if !ok {
		return "", false
	}
	_, prevRed, err := book.ParseFEN(prevFen)
	if err != nil {
		return "", false
	}
	// 移动子必须属于 prevFen 行棋方（否则是悔棋/编辑特征）
	if moverRed != prevRed {
		return "", false
	}
	// newFen 行棋方应翻转（单步走子后必然换行）——不一致说明 FEN 自相矛盾
	if _, newRed, err := book.ParseFEN(newFen); err != nil || newRed == prevRed {
		return "", false
	}
	return mv, true
}

// DiffSingleMoveBoard 盘面级单步解释：与 DiffSingleMove 同样的结构判定
// （恰 2 格差、from→to、to=from 原子、伪合法），但**不校验行棋方**——移动方
// 直接取被移子的颜色，伪合法按移动方自身视角校验。服务端历史分类用它区分
// "单步可解释但行棋方未交替"（识别端轮次卡旧值的真错误形态：cur 侧没翻或
// prev 侧就错了）与正常推进；DiffSingleMove 是它的行棋方双检包装。
func DiffSingleMoveBoard(prevFen, newFen string) (mv string, moverRed bool, ok bool) {
	oldB, _, err := book.ParseFEN(prevFen)
	if err != nil {
		return "", false, false
	}
	newB, _, err := book.ParseFEN(newFen)
	if err != nil {
		return "", false, false
	}
	// 收集差异格（盘面段；FEN 后段不参与）
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
		return "", false, false
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
		return "", false, false
	}
	piece := oldB[from.Y][from.X]
	// 移动/吃子后 to 格必须恰为 from 原子（排除"两格各自变化"的置换形态）
	if newB[to.Y][to.X] != piece {
		return "", false, false
	}
	moverRed = IsRed(piece)
	m := Move{From: from, To: to}
	if !PseudoLegal(oldB, moverRed, m) {
		return "", false, false
	}
	return UcciOf(m), moverRed, true
}

// StartBoardFEN 标准初始盘面段（不含轮次字段）：开局跳变放行判定与测试共用。
const StartBoardFEN = "rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR"

// boardPart 取 FEN 的盘面段（首个空格之前）。
func boardPart(fen string) string {
	if i := strings.IndexByte(fen, ' '); i >= 0 {
		return fen[:i]
	}
	return fen
}

// PlausiblePair 回合结构防线判据（2026-11-10 优势崩塌归因）：上一确认局面到
// 本局面必须能被棋理解释，否则是识别错位/漏检拼出的不可能局面——实测案例：
// 兑子两回合被合成一帧但已离场子残留（2 格差="吃自家子"伪非法形状）、三子
// 无落点蒸发（3 格差、子数 -3）被照常分析后沉淀照常出招。返回空串=通过，
// 非空=拒绝原因（供日志）。畸形 FEN 返回空串放行——交给调用方的专用合法性
// 校验拒绝（那边错误消息更准确）。
// 规则（prev/new 行棋方字段参与判定）：
//   - 开局跳变：cur 盘面段与标准初始布局全等即放行（2026-11-12 补，实测 #5
//     误伤——识别端 NEW_GAME 判定失效时，上一局残局 → 新局初始盘面的大跳变
//     无 new_game 标记无放行形态必拒；重新开局是物理合法跳变。严格全等：识别
//     错位连开局都拼不出时不放，交重探）
//   - 同侧（两回合合并）：≥3 格且子数差 ≤1 或 ≤格数-2（每 1 个被吃子至少摊
//     2 格变化——行棋方交替使相邻两步不能链式共享出发格）；≤2 格不可能
//   - 翻转（一步）：0 格=仅轮次翻转（"更新"/重锚重发）；1 格=目的地漏检/
//     幽灵清理/手动增删（既有放行语义）；2 格=三形态之一（非吃子 消失+出现 /
//     吃子 消失+换色 / 悔棋还原 出现+换色，各自过 PseudoLegal——吃自家子在此
//     穿帮）；≥3 格=不可能（一步恰 2 格）
//
// 不校验盘面合法性（将帅照面/子力上限等由协议入口校验负责）。
// ⚠与手机端 AssistBoard.plausiblePair 同源对表：手机端确认时轮次未定，用
// 无侧并集判据（2 格形状 + ≥3 格子数差规则），本函数是带侧严格版。
func PlausiblePair(prevFen, newFen string) string {
	oldB, prevRed, err := book.ParseFEN(prevFen)
	if err != nil {
		return ""
	}
	newB, newRed, err := book.ParseFEN(newFen)
	if err != nil {
		return ""
	}
	var vanishCells, fillCells, swapCells []Vec
	// 跨局重开放行：新局面即标准初始盘面（盘面段全等，轮次字段不参与）
	if boardPart(newFen) == StartBoardFEN {
		return ""
	}
	for y := 0; y < 10; y++ {
		for x := 0; x < 9; x++ {
			o, n := oldB[y][x], newB[y][x]
			if o == n {
				continue
			}
			switch {
			case o != book.Empty && n == book.Empty:
				vanishCells = append(vanishCells, Vec{x, y})
			case o == book.Empty && n != book.Empty:
				fillCells = append(fillCells, Vec{x, y})
			default:
				swapCells = append(swapCells, Vec{x, y})
			}
		}
	}
	cells := len(vanishCells) + len(fillCells) + len(swapCells)
	if prevRed == newRed {
		// 同侧 = 两回合合并。判据（2026-11-10 复审两轮校准）：
		//   0 格（盘面段全等、仅计数器差异）放行：无任何变化即无腐蚀性
		//   1 格放行：识别自纠帧（同格换型误读读回/幽灵清理）——物理上两回合
		//     不可能只净变 1 格，但作为"手机端自纠"是既有的容错语义，与手机端
		//     无侧判据对齐（PC 拒掉会形成"重探重读同盘面→SAME_BOARD 不再上行"
		//     的死端，PC 永远收不到纠正盘面）
		//   2 格放行：同型兑子合并帧——双消失格必须异色（吃子方出发子+回吃方
		//     出发子必互异色），接触格 old==new 不产生 diff（兑车：黑车吃红车、
		//     另一黑车回吃红车，a6 格先黑车后黑车）。同色双消失（纯蒸发）拒绝
		//   ≥3 格：子数差 ≤0（子不会凭空复原）且 ≤格数-1（c 个被吃子对应 c 个
		//     互异移动方出发格 + ≥1 个共享落子格，兑子回吃全落同一格）
		if cells == 0 {
			return ""
		}
		if cells == 1 {
			return ""
		}
		if cells == 2 {
			if len(vanishCells) == 2 && len(fillCells) == 0 && len(swapCells) == 0 &&
				IsRed(oldB[vanishCells[0].Y][vanishCells[0].X]) != IsRed(oldB[vanishCells[1].Y][vanishCells[1].X]) {
				return "" // 同型兑子合并帧（双消失异色）
			}
			return "同侧 2 格差不可能（合法形态仅异色双消失兑子）"
		}
		delta := len(fillCells) - len(vanishCells) // 子数差（负=净减少）
		if delta > 0 {
			return fmt.Sprintf("同侧子数 +%d 不可能（子不会凭空复原）", delta)
		}
		// 每个 c 个被吃子对应 c 个互异移动方出发格 + 至少 1 个共享落子格
		//（兑子回吃全落在同一格）→ cells ≥ 被吃子数 + 1
		if -delta > cells-1 {
			return fmt.Sprintf("同侧子数差 %d 超出 %d 格变化的上限", -delta, cells)
		}
		return ""
	}
	// 翻转 = 一步
	switch cells {
	case 0:
		return "" // 仅轮次翻转：手机"更新"/重锚自愈重发
	case 1:
		return "" // 目的地漏检/幽灵清理/手动增删（既有放行语义，单格残差）
	case 2:
		var from, to Vec
		var restore bool
		switch {
		case len(vanishCells) == 1 && len(fillCells) == 1:
			from, to = vanishCells[0], fillCells[0] // 非吃子
		case len(vanishCells) == 1 && len(swapCells) == 1:
			from, to = vanishCells[0], swapCells[0] // 吃子
		case len(fillCells) == 1 && len(swapCells) == 1:
			from, to = swapCells[0], fillCells[0] // 悔棋还原（反向走子）
			restore = true
		default:
			return fmt.Sprintf("翻转 2 格差非走子形状（消%d 增%d 换%d）",
				len(vanishCells), len(fillCells), len(swapCells))
		}
		// 子型同一性：出发格消失的子必须与落子格出现的子同型（车消失马出现
		// = 子型置换幻影，与走子几何无关的另一种穿门形态）
		if newB[to.Y][to.X] != oldB[from.Y][from.X] {
			return "2 格差子型置换（出发子与落子不一致）"
		}
		moverRed := IsRed(oldB[from.Y][from.X])
		if !PseudoLegal(oldB, moverRed, Move{From: from, To: to}) {
			return "2 格差不构成伪合法走子（疑似幻影/错位）"
		}
		if restore {
			// 复原子必须与移动方异色（悔棋还原的是对方被吃子；同色=幻影形状）
			if v := newB[swapCells[0].Y][swapCells[0].X]; v != book.Empty && IsRed(v) == moverRed {
				return "还原同色子（幻影帧穿帮形状）"
			}
		}
		return ""
	default:
		return fmt.Sprintf("翻转 %d 格差不可能（一步恰 2 格）", cells)
	}
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

// LegalMoves 局面全部合法着法：伪合法目标格过滤"走后己将被攻击/将帅照面"。
// 智能思考的复杂度预判用（engine.countLegalMoves 消费方）；与 engine.PositionLegal
// 的合法性语义一致，但本函数在纯规则包内自足实现（pieceTargets + 显式照面
// 判定），不引入对 engine 的反向依赖。
func LegalMoves(b book.Board, redGo bool) []Move {
	var out []Move
	for y := 0; y < 10; y++ {
		for x := 0; x < 9; x++ {
			p := b[y][x]
			if p == book.Empty || IsRed(p) != redGo {
				continue
			}
			from := Vec{x, y}
			for _, to := range pieceTargets(b, from, p) {
				m := Move{From: from, To: to}
				if leavesKingSafe(b, redGo, m) {
					out = append(out, m)
				}
			}
		}
	}
	return out
}

// leavesKingSafe 应用 m 后己将是否安全：不被对方任一子攻击（对方的可走格=
// 攻击格，炮隔子打/马蹩腿/兵方向规则全部由 pieceTargets 承担）且不与对方将
// 照面（将的 pieceTargets 只有九宫一步、不含照面长距——显式判定补上，
// fenlegal.attacked 同款语义在本包的自足实现）。
func leavesKingSafe(b book.Board, redGo bool, m Move) bool {
	nb := b // [10][9]int 数组值类型：赋值即整盘克隆
	p := nb[m.From.Y][m.From.X]
	nb[m.From.Y][m.From.X] = book.Empty
	nb[m.To.Y][m.To.X] = p
	king, hasKing := findKing(nb, redGo)
	if !hasKing {
		return true // 无王盘面（仅测试构造会出现）：无从被攻击，视为安全
	}
	// 照面：对方将与己将同列且中间全空（ fenlegal.clearBetween 同语义）
	if enemy, ok := findKing(nb, !redGo); ok && enemy.X == king.X {
		facing := true
		lo, hi := enemy.Y, king.Y
		if lo > hi {
			lo, hi = hi, lo
		}
		for mid := lo + 1; mid < hi; mid++ {
			if nb[mid][king.X] != book.Empty {
				facing = false
				break
			}
		}
		if facing {
			return false
		}
	}
	for y2 := 0; y2 < 10; y2++ {
		for x2 := 0; x2 < 9; x2++ {
			p2 := nb[y2][x2]
			if p2 == book.Empty || IsRed(p2) == redGo {
				continue
			}
			for _, t := range pieceTargets(nb, Vec{x2, y2}, p2) {
				if t == *king {
					return false
				}
			}
		}
	}
	return true
}

// findKing 盘面上 red 一方将/帅的位置；不存在返回 false（残缺测试盘面）。
func findKing(b book.Board, red bool) (*Vec, bool) {
	want := book.BJiang
	if red {
		want = book.WShuai
	}
	for y := 0; y < 10; y++ {
		for x := 0; x < 9; x++ {
			if b[y][x] == want {
				return &Vec{x, y}, true
			}
		}
	}
	return nil, false
}
