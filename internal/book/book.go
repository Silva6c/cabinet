// 本文件是开局库门面层：.obk 打开（SQLite 只读、多系格式兼容探测）、单库
// 查询与多库优先级。Zobrist 查询键与 vmove 解码契约在 zobrist.go，FEN 解析
// 在 fen.go。

package book

import (
	"database/sql"
	"fmt"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
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
	// Moves 全部候选（原键行 ≤5 条在前 + 镜像键补充候选在后，各自按
	// vscore desc 排序；去重后最多 10 条，见 Query）
	Moves []MoveRec
}

// Book 一个已打开的开局库（.obk，zip 解压后的 SQLite；打开时动态探测格式
// 并定格查询，见 OpenBook 兼容层说明）。
type Book struct {
	// Name 库名（suggestion.source 的 "book:<名>" 用，取文件名去扩展名）
	Name string
	// Entries 库内总行数（打开时一次 count，控制台库区显示；库文件静态故不刷新）
	Entries int
	// Table 实际采用的库表（兵河系 bhobk / 鹏飞系 pfbook / 其他含必需列的
	// 兼容表；控制台库区展示来源）
	Table string
	// Warn 打开时探测到的非致命问题（无 vkey 索引 / vvalid 全 0 已忽略过滤），
	// 查询照常进行；控制台库区缀在状态列
	Warn string

	db   *sql.DB
	stmt *sql.Stmt
	log  *slog.Logger

	// selCols 查询列清单（自拼自 Scan，顺序即 Scan 顺序；首列恒 vmove），
	// 只含消费列——vmemo 等大列不再逐行物化（2026-11-05 前是 select * 全列
	// 物化，vmemo BLOB 每行分配从不消费）。idx* 为 -1 = 列缺席（第三方库
	// 缺评分列时对应字段零值参与排序/展示）
	selCols                            []string
	idxScore, idxWin, idxDraw, idxLost int

	// keyMode vkey 键绑定形态（打开时按 typeof(vkey) 采样定格）
	keyMode keyMode
	// filterValid false = 采样未见任何 vvalid=1 行（语义反转或全无效库）——
	// 查询忽略 vvalid 过滤，宁滥勿丢（movePlausible 结构判据兜底）
	filterValid bool

	// queries/hits 查询诊断计数：queries 达阈值仍零命中时打一次告警——
	// 华弈2024 式"哈希体系不同/加密库"整库静默查空的显性化（此前用户侧
	// 完全无感）
	queries    atomic.Int64
	hits       atomic.Int64
	zeroWarned atomic.Bool
}

// keyMode vkey 键绑定形态（打开时采样 typeof(vkey) 定格）。
type keyMode int

const (
	// keyInt 数值键（INTEGER/REAL 存储类）：bind int64——Zobrist 键的位模式
	// 与存储位模式一致（无符号/有符号只是解释差异）；REAL 大键精度丢失属
	// 死数据（命中不了，宁缺勿错）。空表/采样无键行也落此默认形态
	keyInt keyMode = iota
	// keyText 文本键：bind 无符号十进制字符串（兵河系工具 %I64u 写法——
	// 超 int64 上限的键以十进制文本存储，bind int64 永远匹配不上，是此前
	// 最大的"整库静默查空"盲区）
	keyText
	// keyDual 混合存储类：vkey = ? or vkey = ? 双形态绑定（int64 + 文本，
	// 一次索引点查各走一边），覆盖拼库两代工具混写的实态
	keyDual
)

// zeroHitWarnAt 零命中告警阈值（次）：库的正常命中面因库而异，取 100 排除
// "库太小/局面太偏"的误报。
const zeroHitWarnAt = 100

// BookError 打开失败的库及其原因（控制台库区坏库警示用）。
type BookError struct {
	// Name 库名（文件名去 .obk 后缀，与 Book.Name 同口径）
	Name string
	// Err 失败原因（含路径与具体校验错）
	Err string
}

// quoteIdent SQLite 标识符引用：双引号包裹、内部双引号加倍。表名/列名来自
// 库文件目录（sqlite_master/pragma），防御性引用杜绝怪名破出语句边界。
func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// listTables 列出库内全部表名。文件损坏/加密/非 SQLite 时在此报驱动原始
// 错误（旧版把该错误吞成"缺 bhobk 表"，用户无法区分坏文件与缺表）。
func listTables(db *sql.DB) ([]string, error) {
	rows, err := db.Query("select name from sqlite_master where type='table'")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		names = append(names, n)
	}
	return names, rows.Err()
}

// tableColumns 读取表的列名清单（pragma table_info，表名经防御性引用）。
func tableColumns(db *sql.DB, table string) ([]string, error) {
	rows, err := db.Query("pragma table_info(" + quoteIdent(table) + ")")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var cols []string
	for rows.Next() {
		// table_info 行：cid, name, type, notnull, dflt_value, pk——后四列只丢弃
		var cid int
		var name, typ string
		var notnull int
		var dflt, pk any
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			return nil, err
		}
		cols = append(cols, name)
	}
	return cols, rows.Err()
}

// pickCompatTable 从库目录选兼容表：兵河系 bhobk 优先，其次鹏飞系 pfbook，
// 再按目录序找含必需列的表——列名是唯一判据（SQLite 标识符不分大小写），
// 表名与列序都不拘。全部不匹配时错误列出实际表名，帮用户区分"坏文件"与
// "缺兼容表"。
func pickCompatTable(db *sql.DB) (string, []string, error) {
	tables, err := listTables(db)
	if err != nil {
		return "", nil, fmt.Errorf("无法作为 SQLite 数据库读取（文件损坏/加密/非库文件）: %w", err)
	}
	// 候选顺序：两系已知表名优先，其余按目录序殿后（去重）
	seen := map[string]bool{}
	ordered := make([]string, 0, len(tables)+2)
	for _, t := range append([]string{"bhobk", "pfbook"}, tables...) {
		if !seen[t] {
			seen[t] = true
			ordered = append(ordered, t)
		}
	}
	var probed []string
	for _, t := range ordered {
		cols, err := tableColumns(db, t)
		if err != nil {
			continue // 目录里的表读不出列（异常文件）——试下一个
		}
		probed = append(probed, t)
		hasVkey, hasVmove := false, false
		for _, c := range cols {
			switch {
			case strings.EqualFold(c, "vkey"):
				hasVkey = true
			case strings.EqualFold(c, "vmove"):
				hasVmove = true
			}
		}
		// 必需列仅 vkey/vmove：vvalid/vscore/vwin/vdraw/vlost 全部可选缺席
		// （对应过滤/排序/展示退化，见 openCompat 拼装）
		if hasVkey && hasVmove {
			return t, cols, nil
		}
	}
	return "", nil, fmt.Errorf("库中无兼容的开局表（需含 vkey、vmove 列）——实际表: %s",
		strings.Join(probed, ", "))
}

// keyProbe 采样探测结果。
type keyProbe struct {
	mode     keyMode
	anyValid bool // 采样行中存在 vvalid=1
}

// probeKeyStorage 采样探测 vkey 存储形态与 vvalid 语义：顺序扫前 4000 行
// （limit 无 order by = 顺序扫，毫秒级）聚合 typeof(vkey) 与 vvalid 标志——
// 全表聚合要整库扫描，采样对"主导形态"判定足够；混合形态一律双绑定兜底。
func probeKeyStorage(db *sql.DB, table, vkeyCol string, hasVvalid bool, vvalidCol string) (keyProbe, error) {
	inner := "select " + quoteIdent(vkeyCol)
	if hasVvalid {
		inner += ", " + quoteIdent(vvalidCol)
	}
	inner += " from " + quoteIdent(table) + " limit 4000"
	q := "select typeof(" + quoteIdent(vkeyCol) + "), count(*)"
	if hasVvalid {
		q += ", max(case when " + quoteIdent(vvalidCol) + " = 1 then 1 else 0 end)"
	}
	q += " from (" + inner + ") group by 1"
	rows, err := db.Query(q)
	if err != nil {
		return keyProbe{}, err
	}
	defer func() { _ = rows.Close() }()
	var hasText, hasInt bool
	p := keyProbe{mode: keyInt}
	for rows.Next() {
		var typ string
		var cnt int64
		var validFlag int
		dest := []any{&typ, &cnt}
		if hasVvalid {
			dest = append(dest, &validFlag)
		}
		if err := rows.Scan(dest...); err != nil {
			return keyProbe{}, err
		}
		switch typ {
		case "text":
			hasText = true
		case "integer", "real":
			hasInt = true
		}
		if validFlag == 1 {
			p.anyValid = true
		}
	}
	if err := rows.Err(); err != nil {
		return keyProbe{}, err
	}
	switch {
	case hasText && hasInt:
		p.mode = keyDual
	case hasText:
		p.mode = keyText
	}
	return p, nil
}

// countEntries 库内总行数（控制台展示用；失败不致命返回 0）。
func countEntries(db *sql.DB, table string) int {
	var n int
	if err := db.QueryRow("select count(*) from " + quoteIdent(table)).Scan(&n); err != nil {
		return 0
	}
	return n
}

// hasVkeyIndex 检查是否存在以 vkey 为首列的索引（无索引 = 每次查询全表扫，
// 第三方自造库常见；控制台警示提示建索引，不阻塞打开）。
func hasVkeyIndex(db *sql.DB, table, vkeyCol string) (bool, error) {
	rows, err := db.Query("pragma index_list(" + quoteIdent(table) + ")")
	if err != nil {
		return false, err
	}
	defer func() { _ = rows.Close() }()
	var idxNames []string
	for rows.Next() {
		// index_list 列数随 SQLite 版本有 3~5 列差异（seq,name,unique[,origin[,partial]]）
		// ——按实际列数 Scan，只取 name（第 2 列）
		cols, err := rows.Columns()
		if err != nil {
			return false, err
		}
		vals := make([]any, len(cols))
		ptrs := make([]any, len(vals))
		for i := range ptrs {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return false, err
		}
		if len(vals) > 1 {
			if s, ok := vals[1].(string); ok {
				idxNames = append(idxNames, s)
			}
		}
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	for _, idx := range idxNames {
		irows, err := db.Query("pragma index_info(" + quoteIdent(idx) + ")")
		if err != nil {
			continue
		}
		ok := false
		for irows.Next() {
			// index_info 行：seqno,cid,name——name 为索引列名（表达式列为 NULL）
			cols, err := irows.Columns()
			if err != nil {
				break
			}
			vals := make([]any, len(cols))
			ptrs := make([]any, len(vals))
			for i := range ptrs {
				ptrs[i] = &vals[i]
			}
			if err := irows.Scan(ptrs...); err != nil {
				break
			}
			if len(vals) > 2 {
				if s, isStr := vals[2].(string); isStr && strings.EqualFold(s, vkeyCol) {
					ok = true
					break
				}
			}
		}
		_ = irows.Close()
		if ok {
			return true, nil
		}
	}
	return false, nil
}

// OpenBook 打开单个 .obk（须为已解压的 SQLite 文件；zip 包请先行解压）。
// 以只读模式打开（file:...?mode=ro），库文件永不写入。只读的原因：库文件
// 视为不可变资产，热加载会用新文件整体替换旧 .obk——可写连接产生的
// journal/-wal/-shm 附属文件会与替换竞争；只读同时从驱动层杜绝本进程写坏库。
// 库的更新只走"替换文件→热重建"一条路。
//
// 兼容层（2026-11-05 定案，目标"什么格式都能正常兼容"）：.obk 只是扩展名，
// 文件实际是 SQLite 库，生态存在多系变体——兵河系 bhobk 表（桔库/狂刀/象棋
// 名手）、鹏飞系 pfbook 表（列名相同，vmove 可被 EncryptMove 加密）。打开时
// 逐项探测并定格一条预编译查询：
//   - 表名：bhobk → pfbook → 任意含 vkey+vmove 列的表；
//   - 列：按列名取列（列序免疫），必需列仅 vkey/vmove，其余列缺席即退化
//     （vvalid 缺=不过滤、vscore/vwin 缺=不排序、vdraw/vlost 缺=零值）；
//   - vkey 存储形态：INTEGER/REAL → bind int64；TEXT（兵河工具 %I64u 无符号
//     十进制写法，此前整库静默查空的盲区）→ bind 文本；混合 → 双形态 OR 绑定；
//   - vvalid 语义：采样无任何 vvalid=1 行时忽略该过滤（宁滥勿丢）。
//
// 已知不可兼容（宁缺勿错，命中为 0 回落引擎，零命中达阈值时日志告警）：
// EncryptMove 加密库（算法未公开）、Zobrist 随机表不同的库（如华弈2024）。
func OpenBook(path string, logger *slog.Logger) (*Book, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(abs); err != nil {
		return nil, fmt.Errorf("库文件不存在: %w", err)
	}
	// Windows 路径反斜杠转正斜杠（file: URI 兼容）
	// cache_size 1MB + 连接池 1/1（2026-11-09 内存治理：N 个桔库各一条
	// sql.DB，默认 2MB 页缓存/连接 × 无界池；只读点查 1MB 足够，总量封顶）
	uri := "file:" + strings.ReplaceAll(abs, "\\", "/") + "?mode=ro&_pragma=cache_size(-1000)"
	db, err := sql.Open("sqlite", uri)
	if err != nil {
		return nil, fmt.Errorf("打开库失败: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	bk, err := openCompat(db, abs, logger)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return bk, nil
}

// openCompat 打开流程主体：选兼容表 → 探测键形态/vvalid 语义 → 拼装并预编译
// 查询。任何失败由 OpenBook 统一关闭连接。
func openCompat(db *sql.DB, abs string, logger *slog.Logger) (*Book, error) {
	table, cols, err := pickCompatTable(db)
	if err != nil {
		return nil, err
	}
	// 列名按库内实际写法引用（SQLite 标识符不分大小写，EqualFold 匹配）
	find := func(name string) string {
		for _, c := range cols {
			if strings.EqualFold(c, name) {
				return c
			}
		}
		return ""
	}
	vkeyCol := find("vkey")
	vmoveCol := find("vmove")
	vvalidCol := find("vvalid")
	// 消费列清单：vmove 必选 + 评分四列按缺席退化（排序与字段零值同步退化）
	selCols := []string{vmoveCol}
	idxScore, idxWin, idxDraw, idxLost := -1, -1, -1, -1
	for _, c := range []struct {
		name string
		idx  *int
	}{{"vscore", &idxScore}, {"vwin", &idxWin}, {"vdraw", &idxDraw}, {"vlost", &idxLost}} {
		if real := find(c.name); real != "" {
			*c.idx = len(selCols)
			selCols = append(selCols, real)
		}
	}
	probe, err := probeKeyStorage(db, table, vkeyCol, vvalidCol != "", vvalidCol)
	if err != nil {
		return nil, fmt.Errorf("探测库格式失败: %w", err)
	}
	// 查询拼装（打开时一次，预编译常驻）：与上游 BHOpenBook 查询同语义
	// （排序/limit 一致），键条件、vvalid 过滤与排序列按探测结果退化。
	// 双形态键条件必须加括号：SQL 里 AND 优先级高于 OR，裸写会变成
	// "a or b and c" = "a or (b and c)"
	where := quoteIdent(vkeyCol) + " = ?"
	if probe.mode == keyDual {
		where = "(" + where + " or " + quoteIdent(vkeyCol) + " = ?)"
	}
	if vvalidCol != "" && probe.anyValid {
		where += " and " + quoteIdent(vvalidCol) + " = 1"
	}
	q := "select "
	for i, c := range selCols {
		if i > 0 {
			q += ", "
		}
		q += quoteIdent(c)
	}
	q += " from " + quoteIdent(table) + " where " + where
	var order []string
	if idxScore >= 0 {
		order = append(order, quoteIdent(selCols[idxScore])+" desc")
	}
	if idxWin >= 0 {
		order = append(order, quoteIdent(selCols[idxWin])+" desc")
	}
	if len(order) > 0 {
		q += " order by " + strings.Join(order, ", ")
	}
	q += " limit 5"
	stmt, err := db.Prepare(q)
	if err != nil {
		return nil, fmt.Errorf("预编译开局库查询失败: %w", err)
	}
	name := strings.TrimSuffix(filepath.Base(abs), filepath.Ext(abs))
	entries := countEntries(db, table)
	b := &Book{
		Name: name, Entries: entries, Table: table,
		db: db, stmt: stmt, log: logger,
		selCols: selCols, idxScore: idxScore, idxWin: idxWin, idxDraw: idxDraw, idxLost: idxLost,
		keyMode: probe.mode, filterValid: vvalidCol != "" && probe.anyValid,
	}
	// 非致命警示（控制台状态列展示；查询照常）。空库不提 vvalid（无行可采样
	// 不代表语义反转）
	var warns []string
	if ok, err := hasVkeyIndex(db, table, vkeyCol); err == nil && !ok {
		warns = append(warns, "未建 vkey 索引（查询将全表扫描）")
	}
	if entries > 0 && vvalidCol != "" && !probe.anyValid {
		warns = append(warns, "vvalid 全 0，已忽略有效性过滤")
	}
	b.Warn = strings.Join(warns, "；")
	if logger != nil {
		logger.Info("开局库已打开（只读）", "name", name, "table", table,
			"key_mode", probe.mode, "path", abs, "entries", entries)
		if b.Warn != "" {
			logger.Warn("开局库格式警示", "name", name, "warn", b.Warn)
		}
	}
	return b, nil
}

// Close 关闭库连接。
func (b *Book) Close() error {
	if b.stmt != nil {
		_ = b.stmt.Close()
	}
	return b.db.Close()
}

// rawMoveRec 查询的中间行：先按两把键收集原始 vmove 与各字段，再逐行择优
// 解码组装 MoveRec（两形态都不合坐标语义的脏行直接丢弃，不进结果）。
type rawMoveRec struct {
	vmove      int
	score      int
	winRate    float64
	draw, lost int64
	// mir 行来源坐标系：false=原局面键命中（着法按原坐标系存储），
	// true=镜像局面键命中（着法按镜像坐标系存储，解码需列翻转还原）。
	// 键与着法坐标系必然一致——跨域解码的坐标判据必失败，故形态域
	// 按本标记限定，见 Query。
	mir bool
}

// Query 按局面查询单库，返回经形态择优与脏行过滤后的候选（原键行优先、
// 组内 vscore 降序）。查询策略（2026-11-04 定案，针对同 schema 第三方拼库
// 的实测形态）：
//   - 双键并查：原局面键 + 左右镜像局面键（对称局面两键相等只查一次）。
//     狂刀类拼库混合两种批次——原键行与镜像键行各占约 2/3、1/3 命中面
//     （实测 77/77、40/40 全合法），只查原键会静默丢掉镜像批次；镜像局面
//     也是任意库的天然收录对象（左右同构的另一个真实局面），命中即提供
//     同构建议兜底（镜像保持全部棋规，其正解还原后在本局面必然合法）。
//   - 形态域限定：原键行只在"不镜像"域解码、镜像键行只在"列翻转还原"域
//     解码——键与着法坐标系必然一致，跨域解码的坐标判据必失败。
//   - 位义逐行择优：先对调（狂刀类主流，"高 8 位=起点"）后正序（桔库契约，
//     "低 8 位=起点"），以 movePlausible 双判据裁定；桔库对调形态判据失败
//     自然落正序，行为与历史一致。
//   - 排序与脏行过滤：原键行恒排镜像行前（BestMove 语义与单键时代一致，
//     镜像建议只做补充候选）；两形态判据都不通过的行（幻影着法/库内脏
//     数据）直接丢弃，候选归零=未命中回落引擎，宁缺勿错——错误的着法
//     下发后只会触发手机端"轮次可能识别有误"防线。
//
// 诊断计数：每局面查询计一次，连续零命中达阈值时日志告警（见 zeroHitWarnAt）。
func (b *Book) Query(hash int64, bd Board, redGo bool) (QueryResult, error) {
	var res QueryResult
	raws := b.rawQuery(hash, false)
	// 镜像键并查：左右镜像局面（红黑不换、轮次不变）。对称局面两键相等，
	// 重复查询只会得到同一批行，跳过
	if mhash := GetZobrist(mirrorBoard(bd), redGo); mhash != hash {
		raws = append(raws, b.rawQuery(mhash, true)...)
	}
	seen := map[string]bool{}
	var origMoves, mirMoves []MoveRec
	for _, r := range raws {
		move := ""
		for _, swap := range [...]bool{true, false} {
			cand := decodeMoveVariant(r.vmove, swap, r.mir)
			if movePlausible(bd, redGo, cand) {
				move = cand
				break
			}
		}
		// 同局面双键命中给出同一着法时（拼库重叠/对称着法）只保留首条，
		// 避免同一着法占用两个 multipv 槽位让"变招"循环重复
		if move == "" || seen[move] {
			continue
		}
		seen[move] = true
		rec := MoveRec{
			Move:    move,
			Score:   r.score,
			WinRate: r.winRate,
			Draw:    int(r.draw),
			Lost:    int(r.lost),
		}
		if r.mir {
			mirMoves = append(mirMoves, rec)
		} else {
			origMoves = append(origMoves, rec)
		}
	}
	// 双键分组排序（组内与单键 SQL 的 order by 同语义；stable 保持同分行
	// 的库内次序），原键行恒排镜像行前——镜像局面是本局面的左右同构（镜像
	// 保持全部棋规，其正解还原后在本局面必然合法），但只是同构建议兜底，
	// 本局面自己的库行永远优先，BestMove 语义与单键时代一致
	sortMoves := func(ms []MoveRec) {
		sort.SliceStable(ms, func(i, j int) bool {
			if ms[i].Score != ms[j].Score {
				return ms[i].Score > ms[j].Score
			}
			return ms[i].WinRate > ms[j].WinRate
		})
	}
	sortMoves(origMoves)
	sortMoves(mirMoves)
	res.Moves = append(origMoves, mirMoves...)
	res.Hit = len(res.Moves) > 0
	if res.Hit {
		res.BestMove = res.Moves[0].Move
	}
	// 零命中告警：只告一次（zeroWarned 单向翻转），库打开后前 zeroHitWarnAt
	// 次查询是观察窗——华弈2024 式"哈希体系不同"整库静默查空在此显性化
	b.queries.Add(1)
	if res.Hit {
		b.hits.Add(1)
	} else if b.queries.Load() >= zeroHitWarnAt && b.hits.Load() == 0 &&
		b.zeroWarned.CompareAndSwap(false, true) && b.log != nil {
		b.log.Warn("开局库连续零命中——疑似哈希体系不兼容（如华弈2024 新表）或为加密库",
			"book", b.Name, "table", b.Table, "queries", b.queries.Load())
	}
	return res, nil
}

// rawQuery 单键查询原始行（兼容层：键绑定形态与列清单按打开时探测执行）。
// 出错（连接/迭代失败）返回 nil=该键未命中：上层按未命中走引擎兜底，与
// "静默查空"协议语义一致（错误日志不打——查询错误在单库多键场景下
// 会放大刷屏，且引擎兜底链路自有观测）。行级容错：单行 Scan 失败只丢该行
// （第三方库常见个别行的 vmove/vscore 存成 TEXT/垃圾值），不再整键毒死。
func (b *Book) rawQuery(hash int64, mir bool) []rawMoveRec {
	var rows *sql.Rows
	var err error
	switch b.keyMode {
	case keyDual:
		rows, err = b.stmt.Query(hash, strconv.FormatUint(uint64(hash), 10))
	case keyText:
		rows, err = b.stmt.Query(strconv.FormatUint(uint64(hash), 10))
	default:
		rows, err = b.stmt.Query(hash)
	}
	if err != nil {
		return nil
	}
	defer func() { _ = rows.Close() }()
	// Scan 目标 = 自拼 selCols 顺序（打开时按列名定格，库表列序怎么排都不
	// 影响——旧版 select * 位置扫描的"列序铁闸"契约已被按名取列取代）
	vals := make([]any, len(b.selCols))
	ptrs := make([]any, len(vals))
	for i := range ptrs {
		ptrs[i] = &vals[i]
	}
	var raws []rawMoveRec
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			continue // 行级容错：脏行（存储类异常）跳过
		}
		vm, ok := scanInt(vals[0])
		if !ok {
			continue // vmove 非整数：整行不可解码
		}
		rec := rawMoveRec{vmove: int(vm), mir: mir}
		if b.idxScore >= 0 {
			if n, ok2 := scanInt(vals[b.idxScore]); ok2 {
				rec.score = int(n)
			}
		}
		if b.idxWin >= 0 {
			if f, ok2 := scanFloat(vals[b.idxWin]); ok2 {
				rec.winRate = f
			}
		}
		if b.idxDraw >= 0 {
			if n, ok2 := scanInt(vals[b.idxDraw]); ok2 {
				rec.draw = n
			}
		}
		if b.idxLost >= 0 {
			if n, ok2 := scanInt(vals[b.idxLost]); ok2 {
				rec.lost = n
			}
		}
		raws = append(raws, rec)
	}
	if err := rows.Err(); err != nil {
		return nil
	}
	return raws
}

// scanInt 宽容取整：INTEGER 直取、整数 REAL 转换、十进制文本解析——第三方
// 库的数值列存储类五花八门（INTEGER/REAL/TEXT 混写），解析不了=脏行由调用
// 方丢弃。非整数 REAL（精度残缺的大键等）按失败处理。
func scanInt(v any) (int64, bool) {
	switch x := v.(type) {
	case int64:
		return x, true
	case float64:
		if x == math.Trunc(x) && x >= math.MinInt64 && x < math.MaxInt64 {
			return int64(x), true
		}
	case string:
		if n, err := strconv.ParseInt(strings.TrimSpace(x), 10, 64); err == nil {
			return n, true
		}
	case []byte:
		if n, err := strconv.ParseInt(strings.TrimSpace(string(x)), 10, 64); err == nil {
			return n, true
		}
	}
	return 0, false
}

// scanFloat 宽容取浮点（vwin 胜率列：REAL/INTEGER/文本都收，同 scanInt 口径）。
func scanFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case int64:
		return float64(x), true
	case float64:
		return x, true
	case string:
		if f, err := strconv.ParseFloat(strings.TrimSpace(x), 64); err == nil {
			return f, true
		}
	case []byte:
		if f, err := strconv.ParseFloat(strings.TrimSpace(string(x)), 64); err == nil {
			return f, true
		}
	}
	return 0, false
}

// mirrorBoard 左右镜像盘面（列 x → 8-x，红黑不换、轮次语义不变）。
func mirrorBoard(b Board) Board {
	var m Board
	for y := 0; y < 10; y++ {
		for x := 0; x < 9; x++ {
			m[y][8-x] = b[y][x]
		}
	}
	return m
}

// swapVmove vmove 高低字节对调（反向位义库的解码：高 8 位=起点）。
// 与位 0-65535 域内的正序值一一对应，重复对调还原。
func swapVmove(v int) int {
	return (v&0xff)<<8 | (v >> 8 & 0xff)
}

// decodeMoveVariant 按形态解码 vmove 为 UCCI 着法：swap=高低字节对调
// （狂刀类"高 8 位=起点"）；mir=列翻转还原（镜像键行的着法按镜像坐标系
// 存储，a-i 列与原局面相反）。
func decodeMoveVariant(v int, swap, mir bool) string {
	if swap {
		v = swapVmove(v)
	}
	s := VmoveToUcci(v)
	if !mir {
		return s
	}
	return flipMoveCols(s)
}

// flipMoveCols UCCI 着法的列镜像（a↔i … d↔f，行不变）：镜像坐标系着法
// 还原回原坐标系用。
func flipMoveCols(ucci string) string {
	if len(ucci) != 4 {
		return ucci
	}
	flip := func(c byte) byte {
		if c >= 'a' && c <= 'i' {
			return 'a' + 'i' - c
		}
		return c
	}
	return string([]byte{flip(ucci[0]), ucci[1], flip(ucci[2]), ucci[3]})
}

// movePlausible 着法形态判据（双条件）：起点格必须是行棋方棋子，且终点格
// 不得是行棋方棋子（空格或对方棋子）。数学基础：真实着法两条件恒真；
// 错误形态（位义对调弄反时起点=落点）要么起点为空格/对方子（条件一失败），
// 要么两端都是行棋方棋子（条件二失败），两形态不可能同时通过——除非着法
// 两端恰好都在对称列上（此时两种形态解码同格等价，任取其一无差）。
// ucci 坐标 → 盘面：列 a-i → x 0-8，行 0-9 → y=9-行（san90 口径：a9 为
// y=0 黑方底线、a0 为 y=9 红方底线）。空串/越界（VmoveToUcci 对非法
// vmove 的产物）一律 false。
func movePlausible(bd Board, redGo bool, ucci string) bool {
	if len(ucci) != 4 {
		return false
	}
	fx, frow := int(ucci[0]-'a'), int(ucci[1]-'0')
	tx, trow := int(ucci[2]-'a'), int(ucci[3]-'0')
	if fx < 0 || fx > 8 || tx < 0 || tx > 8 || frow < 0 || frow > 9 || trow < 0 || trow > 9 {
		return false
	}
	p := bd[9-frow][fx]
	if p == Empty {
		return false
	}
	isRed := p >= WShuai && p <= WBing
	if isRed != redGo {
		return false
	}
	q := bd[9-trow][tx]
	if q == Empty {
		return true
	}
	qRed := q >= WShuai && q <= WBing
	return qRed != redGo
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
			// 单库坏/缺不拖垮整体：记日志跳过（多库场景其余库仍可用），
			// 同时记入 failed 供控制台库区显示坏库原因（2026-11-01 坏库反馈）
			name := strings.TrimSuffix(filepath.Base(p), filepath.Ext(p))
			s.failed = append(s.failed, BookError{Name: name, Err: err.Error()})
			if logger != nil {
				logger.Warn("开局库打开失败，跳过", "path", p, "err", err)
			}
			continue
		}
		s.books = append(s.books, b)
	}
	return s, nil
}

// Store 多库管理：按配置顺序查询，命中即返回（优先级语义）。
type Store struct {
	books []*Book
	log   *slog.Logger
	// failed 打开失败的库（坏库上报：不进查询链，但控制台库区要能显示原因）
	failed []BookError
}

// Close 关闭全部库。
func (s *Store) Close() {
	for _, b := range s.books {
		_ = b.Close()
	}
}

// Len 已成功打开的库数量。
func (s *Store) Len() int { return len(s.books) }

// Errors 打开失败的库清单（坏库上报；不随查询热路径变动，库集合重建时刷新）。
func (s *Store) Errors() []BookError { return s.failed }

// BookStat 单库的静态信息（控制台库区显示）。
type BookStat struct {
	Name    string // 库名（文件名去 .obk 扩展名）
	Entries int    // 库内总行数
	Table   string // 实际采用的表名（bhobk/pfbook/其他兼容表）
	Warn    string // 打开时探测到的非致命警示（无索引/忽略 vvalid 过滤）；空=无
}

// Stats 当前库集合的逐库信息（按打开顺序 = 配置优先级）。
func (s *Store) Stats() []BookStat {
	out := make([]BookStat, 0, len(s.books))
	for _, b := range s.books {
		out = append(out, BookStat{Name: b.Name, Entries: b.Entries, Table: b.Table, Warn: b.Warn})
	}
	return out
}

// Query 按配置顺序查询各库，首个命中即返回（优先级语义），
// 附带命中库名（suggestion.source 的 "book:<库名>" 用）。
// bd/redGo 透传各库做 vmove 位义仲裁，见 Book.Query。
func (s *Store) Query(hash int64, bd Board, redGo bool) (QueryResult, string, error) {
	for _, b := range s.books {
		res, err := b.Query(hash, bd, redGo)
		if err != nil {
			if s.log != nil {
				s.log.Warn("开局库查询出错，继续下一库", "book", b.Name, "err", err)
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
// 一并返回盘面与行棋方：桔库查询的 vmove 位义仲裁需要局面上下文
// （见 Book.Query），调用方不必二次 ParseFEN。
func FENHashValidated(fen string) (int64, Board, bool, error) {
	b, redGo, err := ParseFEN(fen)
	if err != nil {
		return 0, Board{}, false, err
	}
	if err := ValidateFEN(b); err != nil {
		return 0, Board{}, false, err
	}
	return GetZobrist(b, redGo), b, redGo, nil
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
