// Package book 是桔库（.obk）的 Go 移植：Zobrist 查询键计算 + SQLite 只读查询。
// 权威参照为 上游 原版 Java 实现（tools/vector-gen/src/ 下的只读拷贝），
// 哈希与查询 SQL 必须逐字一致——错一位则全库静默查空。
// 一致性由 testdata/ju-book-vectors.json（Java 端生成的双端比对向量）固化测试。
package book

// 棋子编号（逐字对齐 上游 gamelogic/Piece.java，zobristTable 的 (piece-1) 索引依赖它）。
const (
	Empty = 0
	// 红方 1-7
	WShuai = 1 // K 帅
	WShi   = 2 // A 仕
	WXiang = 3 // B 相
	WMa    = 4 // N 马
	WJu    = 5 // R 车
	WPao   = 6 // C 炮
	WBing  = 7 // P 兵
	// 黑方 8-14
	BJiang = 8  // k 将
	BShi   = 9  // a 士
	BXiang = 10 // b 象
	BMa    = 11 // n 马
	BJu    = 12 // r 车
	BPao   = 13 // c 炮
	BZu    = 14 // p 卒
)

// Board 10 行 × 9 列的棋盘数组，board[y][x]，行序自上而下（黑方底线在前），
// 与 上游 Board.piece 完全一致（Zobrist.getZobristFromBoard 的入参）。
type Board [10][9]int

// GetZobrist 计算局面查询键（逐字移植 Zobrist.getZobristFromBoard）：
// 逐非空子 `zobrist ^= zobristTable[(piece-1)*256 + c90[x+y*9]]`，
// 红方行棋再异或 zobristPlayer。返回带符号 int64（SQLite vkey 同型）。
func GetZobrist(b Board, redGo bool) int64 {
	var z int64
	for y := 0; y < 10; y++ {
		for x := 0; x < 9; x++ {
			piece := b[y][x]
			if piece != Empty {
				index := x + y*9
				square := c90[index]
				z ^= zobristTable[(piece-1)*256+square]
			}
		}
	}
	if redGo {
		z ^= zobristPlayer
	}
	return z
}

// VmoveToUcci 解码 vmove 为 UCCI 着法（逐字移植 Zobrist.getMoveFromVmove）：
// 低 8 位为起点格子编码、高 8 位为终点，输出"起点坐标 + 终点坐标"——即标准
// UCCI 的起点、终点顺序，与引擎/协议直接同向（向量测试 startpos_red 的
// "h2c2" = 红炮 h2→c2 可证）。位义与拼串顺序都是冻结契约：vmove 由 Java 端
// 生成器按同一位义写入，两端逐字对表——改动任一侧都会同时破坏向量测试
// 与全库着法解码。
func VmoveToUcci(vmove int) string {
	first := vmove >> 8
	second := vmove & 0x00ff
	return coordName(second) + coordName(first)
}

// coordNames 反查表：init 时按 c90 一次性构建（square → 坐标名）。遇重复编码
// 保留首个（与线性扫描"返回第一个命中"严格等价）。
var coordNames = func() [90]string {
	var t [90]string
	for i, c := range c90 {
		if c >= 0 && c < 90 && t[c] == "" {
			t[c] = san90[i]
		}
	}
	return t
}()

// coordName 格子编码 → 坐标名（c90 → san90 反查；逐字语义对齐 Java coordMap）。
// 常态走 O(1) 反查表；越界/未覆盖的异常值退回线性扫描兜底（旧行为返回 ""）。
func coordName(square int) string {
	if square >= 0 && square < 90 {
		if name := coordNames[square]; name != "" {
			return name
		}
	}
	for i, c := range c90 {
		if c == square {
			return san90[i]
		}
	}
	return ""
}
