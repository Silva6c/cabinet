// movecn.go ICCS 坐标着法 → 中文纵线记法（设计稿 07：建议流"着法"列以中文记法
// 朱砂色呈现，如 h2e2 → 炮二平五）。纯展示转换：任何解析失败都返回空串，
// 调用方回退原始坐标，绝不显示错误的记法。
package console

import (
	"sort"
	"strings"
)

// cnDigits 中文数字：纵线号（1-9）与直行子进退步数共用
var cnDigits = [...]string{"一", "二", "三", "四", "五", "六", "七", "八", "九"}

// 红黑棋子名（简化字，与设计稿一致；FEN 大写=红、小写=黑）
var cnPieceName = map[byte]string{
	'R': "车", 'N': "马", 'B': "相", 'A': "仕", 'K': "帅", 'C': "炮", 'P': "兵",
	'r': "车", 'n': "马", 'b': "象", 'a': "士", 'k': "将", 'c': "炮", 'p': "卒",
}

// moveCN 把 "h2e2" 形式的 ICCS 着法按 FEN 局面转为中文纵线记法（"炮二平五"）。
//
// 坐标约定（皮卡鱼/UCCI）：盘面段首行 = 黑方底线，列 a-i 自左向右；ICCS 纵行号
// 0-9 以红方底线为 0（fen 行号 = 9 - 纵行号）。红方纵线号 = 9 - 列号（自红方
// 右侧数 1-9），黑方纵线号 = 列号 + 1（自黑方右侧数）。实测锚点：中炮开局
// bestmove h2e2 ⇔ 炮二平五（h 列红方 2 路）。
func moveCN(fen, move string) string {
	fields := strings.Fields(fen)
	if len(fields) < 2 || len(move) != 4 {
		return ""
	}
	rows := strings.Split(fields[0], "/")
	if len(rows) != 10 {
		return ""
	}
	var board [10][10]byte // [行][列]，0 = 空；行 0 = 黑方底线（盘面段首行）
	for i, row := range rows {
		col := 0
		for k := 0; k < len(row); k++ {
			ch := row[k]
			if ch >= '1' && ch <= '9' {
				col += int(ch - '0')
			} else {
				if col > 8 {
					return ""
				}
				board[i][col] = ch
				col++
			}
		}
		if col != 9 {
			return ""
		}
	}
	side := fields[1]
	if side != "w" && side != "b" {
		return ""
	}
	cf, rf, ct, rt := int(move[0]-'a'), int(move[1]-'0'), int(move[2]-'a'), int(move[3]-'0')
	if cf > 8 || ct > 8 || rf > 9 || rt > 9 {
		return ""
	}
	fromRow, toRow := 9-int(rf), 9-int(rt)
	pc := board[fromRow][cf]
	if pc == 0 {
		return "" // 起点无子
	}
	red := side == "w"
	if red != (pc >= 'A' && pc <= 'Z') {
		return "" // 行棋方与起点棋子颜色不符
	}
	name, ok := cnPieceName[pc]
	if !ok {
		return ""
	}
	// 纵线号：红 9-列，黑 列+1
	fileNum := func(c int) int {
		if red {
			return 9 - c
		}
		return c + 1
	}

	// 同列同名子消歧（前/中/后）：有前缀时省略起始纵线号
	prefix := ""
	var same []int
	for r2 := 0; r2 < 10; r2++ {
		if r2 != fromRow && board[r2][cf] == pc {
			same = append(same, r2)
		}
	}
	if len(same) > 0 {
		order := append([]int{fromRow}, same...)
		// 排序 = 按推进深度：红方行号小者更深入（前），黑方行号大者为前
		if red {
			sort.Ints(order)
		} else {
			sort.Sort(sort.Reverse(sort.IntSlice(order)))
		}
		if len(order) > 3 {
			return "" // 四子同列极端罕见，回退坐标
		}
		tags := []string{"前", "后"}
		if len(order) == 3 {
			tags = []string{"前", "中", "后"}
		}
		for i, r2 := range order {
			if r2 == fromRow {
				prefix = tags[i]
				break
			}
		}
	}

	var b strings.Builder
	if prefix != "" {
		b.WriteString(prefix)
	}
	b.WriteString(name)
	if prefix == "" {
		b.WriteString(cnDigits[fileNum(cf)-1])
	}
	if fromRow == toRow { // 横移：平 + 目标纵线
		b.WriteString("平")
		b.WriteString(cnDigits[fileNum(ct)-1])
	} else {
		forward := (red && toRow < fromRow) || (!red && toRow > fromRow)
		if forward {
			b.WriteString("进")
		} else {
			b.WriteString("退")
		}
		if pc == 'A' || pc == 'a' || pc == 'B' || pc == 'b' || pc == 'N' || pc == 'n' {
			// 斜走子（仕/相/马）：进退 + 目标纵线（马不走直线，纵线记法也报目标线）
			b.WriteString(cnDigits[fileNum(ct)-1])
		} else {
			// 直行子（车炮帅兵）：进退 + 步数
			steps := toRow - fromRow
			if steps < 0 {
				steps = -steps
			}
			b.WriteString(cnDigits[steps-1])
		}
	}
	return b.String()
}
