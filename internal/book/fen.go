package book

import (
	"fmt"
	"strconv"
	"strings"
)

// ParseFEN 解析 xqbase 风格 FEN 为棋盘数组与行棋方。
// 语义逐字对齐 上游 Board.restoreFromFEN：
//   - 第 1 段自上而下（y=0 为黑方底线），数字为连续空格；
//   - 第 2 段 w/r 均视为红方行棋，b 为黑方；
//   - 其余段（无吃子位、rounds 等）解析但不参与哈希。
func ParseFEN(fen string) (Board, bool, error) {
	b := Board{}
	fen = strings.TrimSpace(fen)
	// 缺省尾部补齐（Java 版同款宽容：仅到行棋方为止）
	if strings.HasSuffix(fen, "w") || strings.HasSuffix(fen, "b") {
		fen += " - - 0 1"
	}
	parts := strings.Split(fen, " ")
	if len(parts) != 6 {
		return b, false, fmt.Errorf("FEN 应为 6 段: %q", fen)
	}
	var redGo bool
	switch strings.ToLower(parts[1]) {
	case "w", "r":
		redGo = true
	case "b":
		redGo = false
	default:
		return b, false, fmt.Errorf("FEN 行棋方非法: %q", parts[1])
	}
	if _, err := strconv.Atoi(parts[5]); err != nil {
		return b, false, fmt.Errorf("FEN 回合数非法: %q", parts[5])
	}

	// 逐行解析：每行展开后必须恰好 9 格——列数不合法的 FEN 皮卡鱼会直接崩溃
	//（实测 "Invalid FEN" 即进程 EOF），必须在入口挡住（协议 §4.4 防呆）。
	rows := strings.Split(parts[0], "/")
	if len(rows) != 10 {
		return b, false, fmt.Errorf("FEN 应为 10 行, 实为 %d: %q", len(rows), parts[0])
	}
	for yi, row := range rows {
		x := 0
		for _, c := range row {
			switch {
			case c >= '1' && c <= '9':
				x += int(c - '0')
			default:
				if x > 8 {
					return b, false, fmt.Errorf("FEN 第 %d 行越界: %q", yi+1, row)
				}
				p, ok := pieceFromFENChar(c)
				if !ok {
					return b, false, fmt.Errorf("FEN 非法棋子字符: %q", string(c))
				}
				b[yi][x] = p
				x++
			}
		}
		if x != 9 {
			return b, false, fmt.Errorf("FEN 第 %d 行展开应为 9 格, 实为 %d: %q", yi+1, x, row)
		}
	}
	return b, redGo, nil
}

// pieceFromFENChar FEN 字符 → 棋子编号（对齐 Piece.java 的字母约定）。
func pieceFromFENChar(c rune) (int, bool) {
	switch c {
	case 'K':
		return WShuai, true
	case 'A':
		return WShi, true
	case 'B':
		return WXiang, true
	case 'N':
		return WMa, true
	case 'R':
		return WJu, true
	case 'C':
		return WPao, true
	case 'P':
		return WBing, true
	case 'k':
		return BJiang, true
	case 'a':
		return BShi, true
	case 'b':
		return BXiang, true
	case 'n':
		return BMa, true
	case 'r':
		return BJu, true
	case 'c':
		return BPao, true
	case 'p':
		return BZu, true
	}
	return Empty, false
}
