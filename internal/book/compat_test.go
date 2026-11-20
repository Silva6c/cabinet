package book

import (
	"bytes"
	"database/sql"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// 兼容层回归（2026-11-05 定案，目标"什么格式都能正常兼容"）：.obk 只是
// 扩展名，文件实际是 SQLite 库，生态多系变体（兵河 bhobk / 鹏飞 pfbook /
// 自造库）在表名、列序、缺列、vkey 存储形态、vvalid 语义上各不相同。
// 本文件锚定打开时探测与查询退化的每条路径；两态位义/镜像批次语义在
// moveswap_test.go，双端哈希在 book_test.go。
// 统一测试局面 = asymFEN（非对称光帅局），合法着法 e0f0（对调位义编码）。

// makeCustomBook 造自定义 schema 的测试库：ddl 与 inserts 逐条执行（SQL
// 字面量直写，键/着法值由用例格式化）。
func makeCustomBook(t *testing.T, path string, ddl string, inserts ...string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(ddl); err != nil {
		t.Fatal(err)
	}
	for _, q := range inserts {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
}

// openOK 打开测试库（失败即 Fatal；调用方负责 Close）。
func openOK(t *testing.T, path string) *Book {
	t.Helper()
	b, err := OpenBook(path, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// asymVmoveOf 对调位义编码的着法值（int，供 SQL 字面量拼接）。
func asymVmoveOf(from, to string) string {
	return strconv.Itoa(vmoveOf(from, to, true))
}

// TestTextKeyLookup TEXT 键库：兵河系工具以 %I64u 把超 int64 上限的键写成
// 无符号十进制文本——bind int64 永远匹配不上，是 2026-11-05 前的"整库静默
// 查空"盲区。TEXT 亲和列 + 文本字面量，探测后按 keyText 绑定命中。
func TestTextKeyLookup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "text-key.obk")
	keyText := strconv.FormatUint(uint64(asymHash(t)), 10)
	makeCustomBook(t, path, `CREATE TABLE bhobk([id] INTEGER PRIMARY KEY AUTOINCREMENT,
		[vkey] TEXT, [vmove] INTEGER, [vscore] INTEGER, [vwin] INTEGER,
		[vdraw] INTEGER, [vlost] INTEGER, [vvalid] INTEGER, [vmemo] BLOB, [vindex] INTEGER)`,
		"insert into bhobk(vkey, vmove, vscore, vwin, vvalid) values('"+keyText+"', "+
			asymVmoveOf("e0", "f0")+", 10, 60, 1)",
	)
	b := openOK(t, path)
	defer b.Close()
	res := queryAsym(t, b)
	if !res.Hit || res.BestMove != "e0f0" {
		t.Fatalf("TEXT 键库应命中 e0f0: hit=%v best=%q", res.Hit, res.BestMove)
	}
}

// TestMixedKeyStorage 混合存储类库（两代建库工具混写的实态）：无类型列按
// 插入值原样存储，同键 INTEGER 行与 TEXT 行共存——探测后双形态 OR 绑定，
// 两行都命中（旧行为只命中 INTEGER 行，TEXT 行静默丢失）。
func TestMixedKeyStorage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mixed-key.obk")
	h := asymHash(t)
	makeCustomBook(t, path, `CREATE TABLE bhobk([id] INTEGER PRIMARY KEY AUTOINCREMENT,
		[vkey], [vmove], [vscore], [vwin], [vdraw], [vlost], [vvalid], [vmemo], [vindex])`,
		// 同一把键两种存储形态各一行（着法不同，验证两行都到）
		"insert into bhobk(vkey, vmove, vscore, vwin, vvalid) values("+
			strconv.FormatInt(h, 10)+", "+asymVmoveOf("e0", "f0")+", 10, 60, 1)",
		"insert into bhobk(vkey, vmove, vscore, vwin, vvalid) values('"+
			strconv.FormatUint(uint64(h), 10)+"', "+asymVmoveOf("e0", "d0")+", 10, 60, 1)",
	)
	b := openOK(t, path)
	defer b.Close()
	res := queryAsym(t, b)
	if !res.Hit || len(res.Moves) != 2 {
		t.Fatalf("混合键库两行都应命中: hit=%v n=%d %+v", res.Hit, len(res.Moves), res.Moves)
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
		t.Fatalf("int64 与文本两种绑定都应达: %+v", res.Moves)
	}
}

// TestColumnOrderTolerance 列序打乱 + 无类型列：查询按列名拼装、列序免疫
// （旧版 select * 位置扫描下这种库打开即拒或整行错位）。
func TestColumnOrderTolerance(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shuffled.obk")
	h := asymHash(t)
	makeCustomBook(t, path, `CREATE TABLE bhobk([vvalid], [vwin], [vmove], [vmemo],
		[vkey], [vindex], [vscore], [id] INTEGER PRIMARY KEY AUTOINCREMENT, [vdraw], [vlost])`,
		"insert into bhobk(vvalid, vwin, vmove, vkey, vscore) values(1, 60, "+
			asymVmoveOf("e0", "f0")+", "+strconv.FormatInt(h, 10)+", 10)",
	)
	b := openOK(t, path)
	defer b.Close()
	res := queryAsym(t, b)
	if !res.Hit || res.BestMove != "e0f0" {
		t.Fatalf("乱序列库应按列名命中 e0f0: hit=%v best=%q", res.Hit, res.BestMove)
	}
}

// TestOptionalColumnsMissing 精简库（缺 vmemo/vindex/vdraw/vlost）：排序退化
// 到 vscore，Draw/Lost 字段零值，命中不受影响。
func TestOptionalColumnsMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "slim.obk")
	h := asymHash(t)
	makeCustomBook(t, path, `CREATE TABLE bhobk([id] INTEGER PRIMARY KEY AUTOINCREMENT,
		[vkey] INTEGER, [vmove] INTEGER, [vscore] INTEGER, [vwin] INTEGER, [vvalid] INTEGER)`,
		"insert into bhobk(vkey, vmove, vscore, vwin, vvalid) values("+
			strconv.FormatInt(h, 10)+", "+asymVmoveOf("e0", "f0")+", 10, 60, 1)",
	)
	b := openOK(t, path)
	defer b.Close()
	res := queryAsym(t, b)
	if !res.Hit || res.BestMove != "e0f0" {
		t.Fatalf("精简库应命中: hit=%v best=%q", res.Hit, res.BestMove)
	}
	if m := res.Moves[0]; m.Draw != 0 || m.Lost != 0 {
		t.Fatalf("缺列字段应零值: %+v", m)
	}
}

// TestExtraColumns 带新列的未来变体（标准 10 列 + vtag）：多列不拒收，
// 按必需列探测照常命中。
func TestExtraColumns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "extra-col.obk")
	h := asymHash(t)
	makeCustomBook(t, path, `CREATE TABLE bhobk([id] INTEGER PRIMARY KEY AUTOINCREMENT,
		[vkey] INTEGER, [vmove] INTEGER, [vscore] INTEGER, [vwin] INTEGER,
		[vdraw] INTEGER, [vlost] INTEGER, [vvalid] INTEGER, [vmemo] BLOB,
		[vindex] INTEGER, [vtag] TEXT)`,
		"insert into bhobk(vkey, vmove, vscore, vwin, vvalid) values("+
			strconv.FormatInt(h, 10)+", "+asymVmoveOf("e0", "f0")+", 10, 60, 1)",
	)
	b := openOK(t, path)
	defer b.Close()
	if res := queryAsym(t, b); !res.Hit || res.BestMove != "e0f0" {
		t.Fatalf("带新列的库应命中: hit=%v best=%q", res.Hit, res.BestMove)
	}
}

// TestVvalidAllZero vvalid 全 0 库（语义反转或全禁用标记）：忽略有效性过滤
// 命中（宁滥勿丢，结构判据兜底），并在 Warn 登记。
func TestVvalidAllZero(t *testing.T) {
	path := filepath.Join(t.TempDir(), "valid0.obk")
	h := asymHash(t)
	makeCustomBook(t, path, `CREATE TABLE bhobk([id] INTEGER PRIMARY KEY AUTOINCREMENT,
		[vkey] INTEGER, [vmove] INTEGER, [vscore] INTEGER, [vwin] INTEGER, [vvalid] INTEGER)`,
		"insert into bhobk(vkey, vmove, vscore, vwin, vvalid) values("+
			strconv.FormatInt(h, 10)+", "+asymVmoveOf("e0", "f0")+", 10, 60, 0)",
	)
	b := openOK(t, path)
	defer b.Close()
	if res := queryAsym(t, b); !res.Hit || res.BestMove != "e0f0" {
		t.Fatalf("vvalid 全 0 库应忽略过滤命中: hit=%v best=%q", res.Hit, res.BestMove)
	}
	if !strings.Contains(b.Warn, "vvalid") {
		t.Fatalf("vvalid 全 0 应进 Warn: %q", b.Warn)
	}
}

// TestDirtyRowSkipped 行级容错：同键两行中一行 vmove 存 TEXT 垃圾，只丢该行
// 合法行照常命中（旧行为任一行 Scan 失败整键毒死=静默查空）。
func TestDirtyRowSkipped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dirty-row.obk")
	h := asymHash(t)
	makeCustomBook(t, path, `CREATE TABLE bhobk([id] INTEGER PRIMARY KEY AUTOINCREMENT,
		[vkey] INTEGER, [vmove], [vscore] INTEGER, [vwin] INTEGER, [vvalid] INTEGER)`,
		"insert into bhobk(vkey, vmove, vscore, vwin, vvalid) values("+
			strconv.FormatInt(h, 10)+", 'xx', 10, 60, 1)",
		"insert into bhobk(vkey, vmove, vscore, vwin, vvalid) values("+
			strconv.FormatInt(h, 10)+", "+asymVmoveOf("e0", "f0")+", 10, 60, 1)",
	)
	b := openOK(t, path)
	defer b.Close()
	res := queryAsym(t, b)
	if !res.Hit || res.BestMove != "e0f0" || len(res.Moves) != 1 {
		t.Fatalf("脏行应只丢该行，合法行命中: hit=%v %+v", res.Hit, res.Moves)
	}
}

// TestMissingIndexWarn 无 vkey 索引库：打开成功但 Warn 登记全表扫描警示；
// 建了 vkey 索引的同型库无警示。
func TestMissingIndexWarn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "no-idx.obk")
	h := asymHash(t)
	ddl := `CREATE TABLE bhobk([id] INTEGER PRIMARY KEY AUTOINCREMENT,
		[vkey] INTEGER, [vmove] INTEGER, [vscore] INTEGER, [vwin] INTEGER, [vvalid] INTEGER)`
	ins := "insert into bhobk(vkey, vmove, vscore, vwin, vvalid) values(" +
		strconv.FormatInt(h, 10) + ", " + asymVmoveOf("e0", "f0") + ", 10, 60, 1)"
	makeCustomBook(t, path, ddl, ins)
	b := openOK(t, path)
	if !strings.Contains(b.Warn, "vkey 索引") {
		t.Fatalf("无索引库应警示: %q", b.Warn)
	}
	b.Close()

	path2 := filepath.Join(t.TempDir(), "with-idx.obk")
	makeCustomBook(t, path2, ddl, ins, "CREATE INDEX idx_vkey ON bhobk(vkey)")
	b2 := openOK(t, path2)
	defer b2.Close()
	if b2.Warn != "" {
		t.Fatalf("有 vkey 索引的库不应有警示: %q", b2.Warn)
	}
}

// TestPfbookTable 鹏飞系 pfbook 表名：列名与兵河系相同，表名探测后按同键
// 体系接入（明文库）。
func TestPfbookTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pengfei.obk")
	h := asymHash(t)
	makeCustomBook(t, path, `CREATE TABLE pfbook([id] INTEGER PRIMARY KEY AUTOINCREMENT,
		[vkey] INTEGER, [vmove] INTEGER, [vscore] INTEGER, [vwin] INTEGER, [vvalid] INTEGER)`,
		"insert into pfbook(vkey, vmove, vscore, vwin, vvalid) values("+
			strconv.FormatInt(h, 10)+", "+asymVmoveOf("e0", "f0")+", 10, 60, 1)",
	)
	b := openOK(t, path)
	defer b.Close()
	if b.Table != "pfbook" {
		t.Fatalf("应选中 pfbook 表: %q", b.Table)
	}
	if res := queryAsym(t, b); !res.Hit || res.BestMove != "e0f0" {
		t.Fatalf("pfbook 库应命中: hit=%v best=%q", res.Hit, res.BestMove)
	}
}

// TestNotSQLite 非 SQLite 文件：错误明示"无法作为 SQLite"（旧版把坏文件与
// 缺表吞成同一句话，用户无法区分）。
func TestNotSQLite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "garbage.obk")
	if err := os.WriteFile(path, []byte("this is not a sqlite database at all"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := OpenBook(path, nil)
	if err == nil || !strings.Contains(err.Error(), "无法作为 SQLite") {
		t.Fatalf("非 SQLite 文件应报驱动级错误: %v", err)
	}
}

// TestNoCompatibleTable 有表但缺必需列：错误列出实际表名（用户可自查）。
func TestNoCompatibleTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wrong.obk")
	makeCustomBook(t, path, "CREATE TABLE foo(a, b)")
	_, err := OpenBook(path, nil)
	if err == nil || !strings.Contains(err.Error(), "无兼容的开局表") || !strings.Contains(err.Error(), "foo") {
		t.Fatalf("缺必需列应列出实际表: %v", err)
	}
}

// TestZeroHitWarn 连续零命中告警：≥100 次查询零命中打一次日志（华弈2024 式
// "哈希体系不同/加密库"静默查空的显性化），此后不重复刷屏。
func TestZeroHitWarn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.obk")
	makeCustomBook(t, path, `CREATE TABLE bhobk([id] INTEGER PRIMARY KEY AUTOINCREMENT,
		[vkey] INTEGER, [vmove] INTEGER, [vscore] INTEGER, [vwin] INTEGER, [vvalid] INTEGER)`)
	var buf bytes.Buffer
	b, err := OpenBook(path, slog.New(slog.NewTextHandler(&buf, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	for i := 0; i < 100; i++ {
		queryAsym(t, b)
	}
	if n := strings.Count(buf.String(), "零命中"); n != 1 {
		t.Fatalf("达阈值应恰好告警一次: %d 条", n)
	}
	for i := 0; i < 20; i++ {
		queryAsym(t, b)
	}
	if n := strings.Count(buf.String(), "零命中"); n != 1 {
		t.Fatalf("告警只打一次不刷屏: %d 条", n)
	}
}
