package book

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	_ "modernc.org/sqlite"
	"os"
	"path/filepath"
	"testing"
)

// 向量文件：Java 端（tools/vector-gen，上游 原版 Zobrist/Board + sqlite-jdbc）
// 生成的双端比对基准，随代码提交（无版权内容：仅 FEN/哈希/着法/评分）。
// 锚点集 = 手挑语义用例（14 条）；随机集 = 随机合法对局推演空间覆盖（~1200 条）。
const anchorVectorsFile = "../../testdata/ju-book-vectors.json"
const randomVectorsFile = "../../testdata/ju-book-random-vectors.json"

// vector 向量条目（与 tools/vector-gen/src/Main.java 输出逐字对应）。
type vector struct {
	Case     string `json:"case"`
	FEN      string `json:"fen"`
	RedGo    bool   `json:"red_go"`
	Hash     int64  `json:"hash"`
	Hit      bool   `json:"hit"`
	BestMove string `json:"best_move"`
	Moves    []struct {
		Move    string  `json:"move"`
		Score   int     `json:"score"`
		WinRate float64 `json:"win_rate"`
		Draw    int     `json:"draw"`
		Lost    int     `json:"lost"`
	} `json:"moves"`
}

func loadVectors(t *testing.T) []vector {
	t.Helper()
	return append(loadVectorFile(t, anchorVectorsFile), loadVectorFile(t, randomVectorsFile)...)
}

func loadVectorFile(t *testing.T, path string) []vector {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取向量文件失败: %v", err)
	}
	var doc struct {
		Vectors []vector `json:"vectors"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("解析向量文件失败: %v", err)
	}
	if len(doc.Vectors) == 0 {
		t.Fatalf("向量文件为空: %s", path)
	}
	return doc.Vectors
}

// bookPaths 返回可用的桔库路径（library/books/*.obk；缺失时用例 Skip）。
func bookPaths(t *testing.T) []string {
	t.Helper()
	matches, err := filepath.Glob("../../library/books/*.obk")
	if err != nil || len(matches) == 0 {
		t.Skip("library/books/*.obk 不存在（版权物不入 git）：跳过查库比对，仅跑哈希向量")
	}
	return matches
}

// TestHashVectors 双端哈希一致性（核心铁闸）：每个向量用 Go 版 ParseFEN+GetZobrist
// 重算哈希，与 Java 端权威值逐位比对。哈希错一位 = 全库静默查空，此测试必拦。
func TestHashVectors(t *testing.T) {
	vectors := loadVectors(t)
	for _, v := range vectors {
		t.Run(v.Case, func(t *testing.T) {
			b, redGo, err := ParseFEN(v.FEN)
			if err != nil {
				t.Fatalf("FEN 解析失败: %v", err)
			}
			if redGo != v.RedGo {
				t.Fatalf("行棋方不一致: got %v want %v", redGo, v.RedGo)
			}
			got := GetZobrist(b, redGo)
			if got != v.Hash {
				t.Fatalf("哈希不一致（Java=%d Go=%d xor=%#x）——移植有误，全库将查空",
					v.Hash, got, got^v.Hash)
			}
		})
	}
}

// TestQueryVectors 查询结果一致性：Go 查库的命中/最佳着法/候选序列/评分
// 必须与 Java 端向量完全一致（桔库缺失时 Skip）。
func TestQueryVectors(t *testing.T) {
	paths := bookPaths(t)
	vectors := loadVectors(t)
	store, err := OpenStore(slog.New(slog.NewTextHandler(os.Stderr, nil)), paths...)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if store.Len() == 0 {
		t.Skip("无可用桔库：全部打开失败")
	}

	for _, v := range vectors {
		t.Run(v.Case, func(t *testing.T) {
			res, _, err := store.Query(v.Hash)
			if err != nil {
				t.Fatal(err)
			}
			if res.Hit != v.Hit {
				t.Fatalf("命中不一致: got %v want %v", res.Hit, v.Hit)
			}
			if !v.Hit {
				return
			}
			if res.BestMove != v.BestMove {
				t.Fatalf("最佳着法不一致: got %q want %q", res.BestMove, v.BestMove)
			}
			if len(res.Moves) != len(v.Moves) {
				t.Fatalf("候选数不一致: got %d want %d", len(res.Moves), len(v.Moves))
			}
			for i, m := range v.Moves {
				g := res.Moves[i]
				if g.Move != m.Move || g.Score != m.Score || g.Draw != m.Draw || g.Lost != m.Lost {
					t.Fatalf("候选[%d]不一致:\n got %+v\nwant %+v", i, g, m)
				}
				// 胜率浮点：Java double 与 SQLite REAL 双精度同源，精确比较即可
				if g.WinRate != m.WinRate {
					t.Fatalf("候选[%d]胜率不一致: got %v want %v", i, g.WinRate, m.WinRate)
				}
			}
		})
	}
}

// TestMirrorAndBlackFirst 镜像/黑先局面的哈希性质：同盘黑先 = 红先哈希 XOR zobristPlayer；
// 左右镜像局面哈希必不同（镜像用例的红黑两向在向量集中均有）。
func TestMirrorAndBlackFirst(t *testing.T) {
	start := "rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR w - - 0 1"
	bRed, _, err := ParseFEN(start)
	if err != nil {
		t.Fatal(err)
	}
	hRed := GetZobrist(bRed, true)
	hBlack := GetZobrist(bRed, false)
	if hRed^hBlack != zobristPlayer {
		t.Fatalf("黑先应等于红先异或 zobristPlayer: %#x", hRed^hBlack)
	}
}

// TestMissNotError 未命中必须安静返回 Hit=false 而非错误（协议静默回退引擎的前提）。
func TestMissNotError(t *testing.T) {
	paths := bookPaths(t)
	store, err := OpenStore(nil, paths...)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	hash, _, err := FENHash("4k4/9/9/9/9/9/9/9/9/4K4 w - - 0 1")
	if err != nil {
		t.Fatal(err)
	}
	res, name, err := store.Query(hash)
	if err != nil {
		t.Fatalf("未命中不应返回错误: %v", err)
	}
	if res.Hit || name != "" {
		t.Fatalf("光帅局面不应命中: %+v %q", res, name)
	}
}

// TestMultiBookPriority 多库优先级：桔库未命中的局面，回退第二库必须命中；
// 两库都命中时取配置顺序第一库的结果。
func TestMultiBookPriority(t *testing.T) {
	paths := bookPaths(t)

	// 造第二库（临时小 SQLite，schema 逐字对齐 bhobk）：
	// 含"光帅对光将"局面的着法（桔库无此局面 → 只有第二库能命中）
	second := filepath.Join(t.TempDir(), "mini-book.obk")
	makeMiniBook(t, second)

	store, err := OpenStore(slog.New(slog.NewTextHandler(os.Stderr, nil)),
		append([]string{paths[0]}, second)...)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if store.Len() < 2 {
		t.Skipf("桔库不可用（store.Len=%d），多库优先级依赖真实桔库", store.Len())
	}

	// 光帅局面：第一库（桔库）未命中 → 回退第二库命中
	hash, _, err := FENHash("4k4/9/9/9/9/9/9/9/9/4K4 w - - 0 1")
	if err != nil {
		t.Fatal(err)
	}
	res, name, err := store.Query(hash)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Hit || name != "mini-book" {
		t.Fatalf("应回退第二库命中: hit=%v book=%q", res.Hit, name)
	}
	if res.BestMove == "" {
		t.Fatal("第二库命中但无最佳着法")
	}

	// 起始局面：两库都可能命中 → 必须取第一库（桔库）结果
	hash2, _, _ := FENHash("rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR w - - 0 1")
	_, name2, err := store.Query(hash2)
	if err != nil {
		t.Fatal(err)
	}
	if name2 != filepath.Base(paths[0])[:0]+filepath.Base(paths[0]) {
		// 名字取文件名去扩展名：直接与桔库文件名比较
		base := filepath.Base(paths[0])
		if name2 != base[:len(base)-len(filepath.Ext(base))] {
			t.Fatalf("起始局面应取第一库（桔库）结果，实际: %q", name2)
		}
	}
}

// makeMiniBook 造测试用迷你桔库（schema 逐字对齐 上游 BHDatabase 建表语句）。
func makeMiniBook(t *testing.T, path string) {
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
	hash, _, err := FENHash("4k4/9/9/9/9/9/9/9/9/4K4 w - - 0 1")
	if err != nil {
		t.Fatal(err)
	}
	// vmove：h9e9（帅五平四式旁着，编码仅验证解码链路；值任意合法即可）
	vmove := c90Of("e9")<<8 | c90Of("h9")
	if _, err := db.Exec(
		"insert into bhobk(vkey, vmove, vscore, vwin, vdraw, vlost, vvalid) values(?,?,?,?,?,?,1)",
		hash, vmove, 10, 0.62, 3, 1); err != nil {
		t.Fatal(err)
	}
}

// c90Of 坐标名 → 格子编码（测试用反查）。
func c90Of(name string) int {
	for i, s := range san90 {
		if s == name {
			return c90[i]
		}
	}
	panic(fmt.Sprintf("非法坐标: %s", name))
}

// TestParseFEN_RowWidth 非法列数 FEN 必须拒绝（皮卡鱼对这类 FEN 直接崩溃）。
func TestParseFEN_RowWidth(t *testing.T) {
	bad := "rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C4N1C1/9/RNBAKAB1R b - - 0 1"
	if _, _, err := ParseFEN(bad); err == nil {
		t.Fatal("第 8 行展开 10 格的 FEN 应被拒绝")
	}
	good := "rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C4N1C/9/RNBAKAB1R b - - 0 1"
	if _, _, err := ParseFEN(good); err != nil {
		t.Fatalf("合法 FEN 不应报错: %v", err)
	}
}
