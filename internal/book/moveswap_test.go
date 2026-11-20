package book

import (
	"database/sql"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// vmove 形态择优回归（2026-11-04 定案）：同 schema 的第三方 .obk 拼库存在
// 混合批次——原局面键批次（着法原坐标系、位义对调）与左右镜像局面键批次
// （着法镜像坐标系、需列翻转还原）。2026-11-03 版按"库级位义锁存"只覆盖
// 原键批次，镜像键批次静默丢失；本版双键并查 + 按键来源限定形态域 + 逐行
// 择优。实测数据见提交说明（狂刀库原键 77/77、镜像键 40/40 全合法）。
//
// 测试局面统一用非对称光帅局（黑将 d9、红帅 e0，红走）：
// "3k5/9/9/9/9/9/9/9/9/4K4 w - - 0 1"。真实合法着法：e0f0（帅五平四）、
// e0d0（帅五平六）——d0/f0 均空，且局面左右非对称（镜像键≠原键）。
// 着法编码（c90 格子编码，见 zobrist_table.go）：
//   - 正序（桔库契约）：vmove = 终点<<8 | 起点
//   - 对调（狂刀类实测位义）：vmove = 起点<<8 | 终点

// asymFEN 测试局面：非对称光帅局，红走。
const asymFEN = "3k5/9/9/9/9/9/9/9/9/4K4 w - - 0 1"

// asymBoard 非对称光帅局盘面与轮次。
func asymBoard(t *testing.T) (Board, bool) {
	t.Helper()
	bd, redGo, err := ParseFEN(asymFEN)
	if err != nil {
		t.Fatal(err)
	}
	return bd, redGo
}

// asymHash 原局面查询键；asymMirrorHash 左右镜像局面键（两者必不等）。
func asymHash(t *testing.T) int64 {
	t.Helper()
	bd, redGo := asymBoard(t)
	return GetZobrist(bd, redGo)
}

func asymMirrorHash(t *testing.T) int64 {
	t.Helper()
	bd, redGo := asymBoard(t)
	if GetZobrist(mirrorBoard(bd), redGo) == GetZobrist(bd, redGo) {
		t.Fatal("测试局面应为左右非对称（镜像键≠原键）")
	}
	return GetZobrist(mirrorBoard(bd), redGo)
}

// makeBookWithMoves 造测试用迷你桔库（schema 与 makeMiniBook 同源），
// moves 为 (vkey, vmove) 对——混合批次用例需要两把键各插行。
func makeBookWithMoves(t *testing.T, path string, moves [][2]int64) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE bhobk([id] INTEGER PRIMARY KEY AUTOINCREMENT,
                   [vkey] INTEGER,
                   [vmove] INTEGER,
                   [vscore] INTEGER,
                   [vwin] INTEGER,
                   [vdraw] INTEGER,
                   [vlost] INTEGER,
                   [vvalid] INTEGER,
                   [vmemo] BLOB,
                   [vindex] INTEGER)`); err != nil {
		t.Fatal(err)
	}
	for _, m := range moves {
		if _, err := db.Exec(
			"insert into bhobk(vkey, vmove, vscore, vwin, vdraw, vlost, vvalid) values(?,?,?,?,?,?,1)",
			m[0], m[1], 10, 0.6, 3, 1); err != nil {
			t.Fatal(err)
		}
	}
}

// queryAsym 非对称光帅局面查询 helper：返回单库查询结果。
func queryAsym(t *testing.T, b *Book) QueryResult {
	t.Helper()
	bd, redGo := asymBoard(t)
	res, err := b.Query(GetZobrist(bd, redGo), bd, redGo)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// vmoveOf 着法（from, to 坐标名）按指定位义编码。
func vmoveOf(from, to string, swapped bool) int {
	code := func(name string) int {
		for i, s := range san90 {
			if s == name {
				return c90[i]
			}
		}
		panic("非法坐标: " + name)
	}
	if swapped { // 狂刀类：高 8 位=起点
		return code(from)<<8 | code(to)
	}
	return code(to)<<8 | code(from) // 桔库契约：低 8 位=起点
}

// TestVmoveSwapDecode 对调位义批次（原键、原坐标系）：幻影着法（f0e0，
// 起点空格）不得出现，择优后应为真实着法。
func TestVmoveSwapDecode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "swap-book.obk")
	// 对调位义若被误按桔库契约解码得 "f0e0"——起点 f0 是空格（2026-11-02
	// 狂刀库"轮次可能识别有误"的原始形态）
	makeBookWithMoves(t, path, [][2]int64{{asymHash(t), int64(vmoveOf("e0", "f0", true))}})
	b, err := OpenBook(path, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	res := queryAsym(t, b)
	if !res.Hit || res.BestMove != "e0f0" {
		t.Fatalf("对调批次应择优解码 e0f0: hit=%v best=%q", res.Hit, res.BestMove)
	}
}

// TestVmovePlainOrderDecode 正序批次（原键、桔库契约）：对调形态判据失败
// 自然落正序，行为与历史逐字节一致。
func TestVmovePlainOrderDecode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plain-book.obk")
	makeBookWithMoves(t, path, [][2]int64{{asymHash(t), int64(vmoveOf("e0", "f0", false))}})
	b, err := OpenBook(path, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	res := queryAsym(t, b)
	if !res.Hit || res.BestMove != "e0f0" {
		t.Fatalf("正序批次应按桔库契约解码 e0f0: hit=%v best=%q", res.Hit, res.BestMove)
	}
}

// TestMirrorKeyBatch 镜像键批次：着法按镜像坐标系存储（原着法 e0d0 的镜像
// 坐标 e0f0）——原键查不到、镜像键命中后列翻转还原回 e0d0。
func TestMirrorKeyBatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mirror-book.obk")
	mk := asymMirrorHash(t)
	// 存镜像坐标系着法 e0→f0（对调位义）：直接解码得 e0f0，列翻转还原得
	// e0d0 才是批次真实着法（e0d0 也是真实可走的合法着法，双保险无冲突）
	makeBookWithMoves(t, path, [][2]int64{{mk, int64(vmoveOf("e0", "f0", true))}})
	b, err := OpenBook(path, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	res := queryAsym(t, b)
	if !res.Hit {
		t.Fatal("镜像键批次应被双键并查命中")
	}
	if res.BestMove != "e0d0" {
		t.Fatalf("镜像坐标系着法应列翻转还原为 e0d0, got %q", res.BestMove)
	}
}

// TestMixedBatches 混合批次（拼库实态）：原键行 + 镜像键行同库各自正确
// 解码；两批次给出同一着法时（拼库重叠）按着法去重。
func TestMixedBatches(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mixed-book.obk")
	mk := asymMirrorHash(t)
	// 批次 A：原键、对调、e0f0；批次 B：镜像键、对调、镜像坐标 e0f0（还原=e0d0）
	makeBookWithMoves(t, path, [][2]int64{
		{asymHash(t), int64(vmoveOf("e0", "f0", true))},
		{mk, int64(vmoveOf("e0", "f0", true))},
	})
	b, err := OpenBook(path, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	res := queryAsym(t, b)
	if !res.Hit {
		t.Fatal("混合批次应命中")
	}
	if len(res.Moves) != 2 {
		t.Fatalf("两批次不同着法应保留 2 条, got %d: %+v", len(res.Moves), res.Moves)
	}
	hasF0, hasD0 := false, false
	for _, m := range res.Moves {
		switch m.Move {
		case "e0f0":
			hasF0 = true
		case "e0d0":
			hasD0 = true
		}
	}
	if !hasF0 || !hasD0 {
		t.Fatalf("应同时含原坐标系与还原着法: %+v", res.Moves)
	}

	// 重叠批次：两批次给出同一着法（镜像坐标 e0d0 还原回 e0f0 = 批次 A 着法）
	path2 := filepath.Join(t.TempDir(), "overlap-book.obk")
	makeBookWithMoves(t, path2, [][2]int64{
		{asymHash(t), int64(vmoveOf("e0", "f0", true))},
		{mk, int64(vmoveOf("e0", "d0", true))}, // 镜像坐标 e0d0 → 还原 e0f0
	})
	b2, err := OpenBook(path2, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer b2.Close()
	res2 := queryAsym(t, b2)
	if len(res2.Moves) != 1 || res2.BestMove != "e0f0" {
		t.Fatalf("重叠着法应去重为 1 条 e0f0, got %+v", res2.Moves)
	}
}

// TestDirtyMoveFiltered 脏行过滤：两形态判据都不通过的行（起点非行棋方
// 棋子）一律丢弃；全脏库按未命中回落（上层走引擎兜底）。
func TestDirtyMoveFiltered(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dirty-book.obk")
	// "d9d9"：起点 d9 是黑将格（红走局面下非行棋方棋子），两形态皆脏；
	// 合法行 e0f0 对照保留
	dirty := int64(vmoveOf("d9", "d9", true))
	clean := int64(vmoveOf("e0", "f0", true))
	makeBookWithMoves(t, path, [][2]int64{{asymHash(t), dirty}, {asymHash(t), clean}})
	b, err := OpenBook(path, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	res := queryAsym(t, b)
	if !res.Hit {
		t.Fatal("混合脏行库应保留合法行命中")
	}
	if res.BestMove != "e0f0" || len(res.Moves) != 1 {
		t.Fatalf("脏行应被过滤只留 e0f0, got %+v", res.Moves)
	}

	// 全脏库：未命中（协议语义：宁缺勿错，回落沉淀/引擎）
	path2 := filepath.Join(t.TempDir(), "all-dirty.obk")
	makeBookWithMoves(t, path2, [][2]int64{{asymHash(t), dirty}})
	b2, err := OpenBook(path2, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer b2.Close()
	if res2 := queryAsym(t, b2); res2.Hit {
		t.Fatalf("全脏库应未命中, got %+v", res2)
	}
}

// TestSymFenSingleLookup 对称局面：镜像键=原键，只查一次、结果正确
// （光帅对光将全对称局面——历史行为的主路径回归）。
func TestSymFenSingleLookup(t *testing.T) {
	fen := "4k4/9/9/9/9/9/9/9/9/4K4 w - - 0 1" // 黑将 e9、红帅 e0，全对称
	path := filepath.Join(t.TempDir(), "sym-book.obk")
	bd, redGo, err := ParseFEN(fen)
	if err != nil {
		t.Fatal(err)
	}
	k := GetZobrist(bd, redGo)
	// 红帅 e0 平 f0（照面解除，真实合法）
	makeBookWithMoves(t, path, [][2]int64{{k, int64(vmoveOf("e0", "f0", true))}})
	b, err := OpenBook(path, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	res, err := b.Query(k, bd, redGo)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Hit || res.BestMove != "e0f0" || len(res.Moves) != 1 {
		t.Fatalf("对称局面应命中且去重为 1 条 e0f0: %+v", res)
	}
}
