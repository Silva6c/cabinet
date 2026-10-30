// 本文件是桔库的门面层：.obk 打开（SQLite 只读）、单库查询与多库优先级。
// Zobrist 查询键与 vmove 解码契约在 zobrist.go，FEN 解析在 fen.go。

package book

import (
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// MoveRec 库内一条候选着法（对齐 上游 BookData 字段）。
type MoveRec struct {
	// Move UCCI 着法（起点在前，如 "h2e2"；由 VmoveToUcci 解码 vmove 所得，
	// 可直接按标准 UCCI 消费——协议 suggestion 帧与 moveCN 中文翻译同此口径）
	Move string
	// Score 库评分（vscore）
	Score int
	// WinRate 胜率（vwin 列；Java 侧按 double 读取）
	WinRate float64
	// Draw 和棋数（vdraw）
	Draw int
	// Lost 负局数（vlost）
	Lost int
}

// QueryResult 单库查询结果。
type QueryResult struct {
	// Hit 是否命中
	Hit bool
	// BestMove 最佳着法（排序后首条；未命中为空）
	BestMove string
	// Moves 全部候选（≤5 条，SQL 已按 vscore desc, vwin desc 排序）
	Moves []MoveRec
}

// Book 一个已打开的桔库（.obk，zip 解压后的 SQLite）。
type Book struct {
	// Name 库名（suggestion.source 的 "book:<名>" 用，取文件名去扩展名）
	Name string
	// Entries 库内总行数（打开时一次 count，控制台库区显示；库文件静态故不刷新）
	Entries int
	db      *sql.DB
	// stmt 常驻预编译查询：modernc 驱动无跨调用语句缓存，每次 db.Query 都完整
	// prepare/finalize；预编译后查询只剩参数绑定+索引点查（SQL 文本逐字不变）
	stmt *sql.Stmt
}

// Store 多库管理：按配置顺序查询，命中即返回（优先级语义）。
type Store struct {
	books []*Book
	log   *slog.Logger
}

// querySQL 逐字对齐 上游 openbook/BHOpenBook.java:27-32 的查询
// （原版用 String.format 拼 vkey，语义等同参数绑定；排序与 limit 逐字一致）。
const querySQL = "select * from bhobk where vvalid = 1 and vkey = ? order by vscore desc, vwin desc limit 5"

// OpenBook 打开单个 .obk（须为已解压的 SQLite 文件；zip 包请先行解压）。
// 以只读模式打开（file:...?mode=ro），库文件永不写入。只读的原因：库文件
// 视为不可变资产，热加载会用新文件整体替换旧 .obk——可写连接产生的
// journal/-wal/-shm 附属文件会与替换竞争；只读同时从驱动层杜绝本进程写坏库。
// 库的更新只走"替换文件→热重建"一条路。
func OpenBook(path string, logger *slog.Logger) (*Book, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(abs); err != nil {
		return nil, fmt.Errorf("库文件不存在: %w", err)
	}
	// Windows 路径反斜杠转正斜杠（file: URI 兼容）
	uri := "file:" + strings.ReplaceAll(abs, "\\", "/") + "?mode=ro"
	db, err := sql.Open("sqlite", uri)
	if err != nil {
		return nil, fmt.Errorf("打开库失败: %w", err)
	}
	// 立即探活：确认是桔库 schema（bhobk 表存在），坏文件早失败
	var n int
	if err := db.QueryRow("select count(*) from sqlite_master where type='table' and name='bhobk'").Scan(&n); err != nil || n == 0 {
		_ = db.Close()
		return nil, fmt.Errorf("不是有效的桔库（缺 bhobk 表）: %s", path)
	}
	name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	// 条数只做展示用：count 一次（主键索引扫，很快），失败不致命（显示 0）
	var entries int
	if err := db.QueryRow("select count(*) from bhobk").Scan(&entries); err != nil {
		if logger != nil {
			logger.Warn("桔库条数统计失败（显示为 0）", "path", path, "err", err)
		}
		entries = 0
	}
	if logger != nil {
		logger.Info("桔库已打开（只读）", "name", name, "path", abs, "entries", entries)
	}
	stmt, err := db.Prepare(querySQL)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("预编译桔库查询失败: %w", err)
	}
	return &Book{Name: name, Entries: entries, db: db, stmt: stmt}, nil
}

// Close 关闭库连接。
func (b *Book) Close() error {
	if b.stmt != nil {
		_ = b.stmt.Close()
	}
	return b.db.Close()
}

// Query 按哈希查询单库（vkey 为带符号 int64，与 SQLite INTEGER 同型）。
func (b *Book) Query(hash int64) (QueryResult, error) {
	var res QueryResult
	rows, err := b.stmt.Query(hash)
	if err != nil {
		return res, fmt.Errorf("桔库查询失败: %w", err)
	}
	defer func() { _ = rows.Close() }()
	// select * 列序铁闸的落点：Scan 目标顺序 = bhobk 建表列序
	// id,vkey,vmove,vscore,vwin,vdraw,vlost,vvalid,vmemo,vindex。Java 上游按
	// cursor.getColumnIndex("列名") 取值、天然免疫列序，Go 这里按位置 Scan——
	// 桔库 schema 视为冻结，任何列增删/换序都会整行错位且无报错。
	for rows.Next() {
		var (
			id, vkey, vmove, vscore int64
			vwin                    float64
			vdraw, vlost, vvalid    int64
			vmemo                   []byte
			vindex                  sql.NullInt64
		)
		if err := rows.Scan(&id, &vkey, &vmove, &vscore, &vwin, &vdraw, &vlost, &vvalid, &vmemo, &vindex); err != nil {
			return res, fmt.Errorf("桔库行读取失败: %w", err)
		}
		res.Moves = append(res.Moves, MoveRec{
			Move:    VmoveToUcci(int(vmove)),
			Score:   int(vscore),
			WinRate: vwin,
			Draw:    int(vdraw),
			Lost:    int(vlost),
		})
	}
	// 迭代中途 I/O 错误时部分候选行不得当完整命中返回：出错=未命中，
	// 上层按未命中走引擎兜底（与"静默查空"协议语义一致）
	if err := rows.Err(); err != nil {
		return res, fmt.Errorf("桔库行迭代失败: %w", err)
	}
	res.Hit = len(res.Moves) > 0
	if res.Hit {
		res.BestMove = res.Moves[0].Move
	}
	return res, nil
}

// OpenStore 按顺序打开多个库（配置顺序即优先级）。
func OpenStore(logger *slog.Logger, paths ...string) (*Store, error) {
	s := &Store{log: logger}
	for _, p := range paths {
		if strings.TrimSpace(p) == "" {
			continue
		}
		b, err := OpenBook(p, logger)
		if err != nil {
			// 单库坏/缺不拖垮整体：记日志跳过（多库场景其余库仍可用）
			if logger != nil {
				logger.Warn("桔库打开失败，跳过", "path", p, "err", err)
			}
			continue
		}
		s.books = append(s.books, b)
	}
	return s, nil
}

// Close 关闭全部库。
func (s *Store) Close() {
	for _, b := range s.books {
		_ = b.Close()
	}
}

// Len 已成功打开的库数量。
func (s *Store) Len() int { return len(s.books) }

// BookStat 单库的静态信息（控制台库区显示）。
type BookStat struct {
	Name    string // 库名（文件名去 .obk 扩展名）
	Entries int    // 库内总行数
}

// Stats 当前库集合的逐库信息（按打开顺序 = 配置优先级）。
func (s *Store) Stats() []BookStat {
	out := make([]BookStat, 0, len(s.books))
	for _, b := range s.books {
		out = append(out, BookStat{Name: b.Name, Entries: b.Entries})
	}
	return out
}

// Query 按配置顺序查询各库，首个命中即返回（优先级语义），
// 附带命中库名（suggestion.source 的 "book:<库名>" 用）。
func (s *Store) Query(hash int64) (QueryResult, string, error) {
	for _, b := range s.books {
		res, err := b.Query(hash)
		if err != nil {
			if s.log != nil {
				s.log.Warn("桔库查询出错，继续下一库", "book", b.Name, "err", err)
			}
			continue
		}
		if res.Hit {
			return res, b.Name, nil
		}
	}
	return QueryResult{}, "", nil
}

// FENHashValidated FEN → 查询键，附带合法性校验（入引擎前用）：
// 双方将帅必须在场且不照面——皮卡鱼对将帅照面等非法局面可能异常退出。
func FENHashValidated(fen string) (int64, bool, error) {
	b, redGo, err := ParseFEN(fen)
	if err != nil {
		return 0, false, err
	}
	if err := ValidateFEN(b); err != nil {
		return 0, false, err
	}
	return GetZobrist(b, redGo), redGo, nil
}

// ValidateFEN 局面合法性基本校验：双方将帅必须在场且不照面（同列无隔子）。
func ValidateFEN(b Board) error {
	type pos struct{ x, y int }
	var red, black pos
	found := 0
	for y := 0; y < 10; y++ {
		for x := 0; x < 9; x++ {
			switch b[y][x] {
			case WShuai:
				red = pos{x, y}
				found++
			case BJiang:
				black = pos{x, y}
				found++
			}
		}
	}
	if found != 2 {
		return fmt.Errorf("将帅缺一不可（发现 %d 个）", found)
	}
	if red.x == black.x {
		blocked := false
		for y := min(red.y, black.y) + 1; y < max(red.y, black.y); y++ {
			if b[y][red.x] != Empty {
				blocked = true
				break
			}
		}
		if !blocked {
			return fmt.Errorf("两将照面（同列 %d 无隔子）", red.x)
		}
	}
	return nil
}
