package library

import (
	"archive/zip"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"cabinet/internal/book"
)

// makeBook 构造最小合法桔库：列名与列序对齐 bhobk schema（OpenBook 的
// PRAGMA 校验查列名/列序不查类型，TEXT 列可过——查询侧才会因类型失真）。
func makeBook(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	const schema = `CREATE TABLE bhobk (
		id INTEGER PRIMARY KEY, vkey TEXT, vmove TEXT, vscore INTEGER,
		vwin INTEGER, vdraw INTEGER, vlost INTEGER, vvalid INTEGER,
		vmemo TEXT, vindex INTEGER)`
	if _, err := db.Exec(schema); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO bhobk(vkey, vmove, vscore) VALUES ('fen', 'h2e2', 30)`); err != nil {
		t.Fatal(err)
	}
}

// makeZip 构造测试 zip（files: 条目名 → 内容字节）。
func makeZip(t *testing.T, path string, files map[string][]byte) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w := zip.NewWriter(f)
	for name, content := range files {
		fw, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fw.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestUnzipFilters 解压过滤语义：正条目解出、__MACOSX 与点前缀垃圾跳过、
// 路径穿越条目按裸名落盘（不逃出目标目录）。
func TestUnzipFilters(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "lib.obk.zip")
	makeZip(t, zipPath, map[string][]byte{
		"real.obk":           []byte("real-content"),
		"__MACOSX/real.obk":  []byte("macos-garbage"),
		".hidden":            []byte("hidden"),
		"../evil.obk":        []byte("escape"),
		"sub/dir/nested.obk": []byte("nested"),
	})
	if err := unzip(zipPath, dir); err != nil {
		t.Fatal(err)
	}
	// real.obk 内容正确
	b, err := os.ReadFile(filepath.Join(dir, "real.obk"))
	if err != nil || string(b) != "real-content" {
		t.Fatalf("real.obk 应解出: %q err=%v", b, err)
	}
	// 垃圾条目不落地；嵌套/穿越条目按 Base 裸名落在 dir 根（防逃逸语义，
	// 内容保留：evil.obk 落 dir 内而非上一级）
	for _, gone := range []string{"__MACOSX", ".hidden"} {
		if _, err := os.Stat(filepath.Join(dir, gone)); err == nil {
			t.Fatalf("%s 不应存在", gone)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "nested.obk")); err != nil {
		t.Fatalf("嵌套条目应按裸名落盘: %v", err)
	}
	// 穿越条目不得逃出 dir
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "evil.obk")); err == nil {
		t.Fatal("路径穿越条目逃出了目标目录")
	}
}

// TestUnzipIfNeeded_Mtime mtime 判断：.obk 不旧于 zip 时跳过，zip 更新时重解压。
func TestUnzipIfNeeded_Mtime(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "lib.obk.zip")
	makeZip(t, zipPath, map[string][]byte{"lib.obk": []byte("v1")})
	time.Sleep(20 * time.Millisecond) // 确保 .obk mtime 严格晚于 zip
	makeBook(t, filepath.Join(dir, "lib.obk"))

	m, err := New(dir, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	// zip 旧于 .obk：初始加载与显式 unzipIfNeeded 均跳过（内容保持 SQLite 库）
	if err := m.unzipIfNeeded(zipPath); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "lib.obk"))
	if err != nil || len(b) == 0 || string(b) == "v1" {
		t.Fatalf("旧 zip 不应覆盖新 .obk: %q err=%v", b, err)
	}
	// zip 刷新为最新：应触发重解压覆盖
	time.Sleep(20 * time.Millisecond)
	makeZip(t, zipPath, map[string][]byte{"lib.obk": []byte("v3")})
	if err := m.unzipIfNeeded(zipPath); err != nil {
		t.Fatal(err)
	}
	b, err = os.ReadFile(filepath.Join(dir, "lib.obk"))
	if err != nil || string(b) != "v3" {
		t.Fatalf("新 zip 应覆盖旧 .obk: %q err=%v", b, err)
	}
}

// TestOrderingAndDisable 用户优先级排序 + 单库禁用：order 中的库按位次在前
// （未列入的字典序排尾部），禁用库不进查询链但 Books() 照常展示标记
// Enabled=false；SetOrdering 热更新即时生效。
func TestOrderingAndDisable(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"a1", "b2", "c3"} {
		makeBook(t, filepath.Join(dir, n+".obk"))
	}
	m, err := New(dir, []string{"c3"}, []string{"b2"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()

	books := m.Books()
	if len(books) != 3 {
		t.Fatalf("禁用库仍应展示: %+v", books)
	}
	if books[0].Name != "c3" || books[1].Name != "a1" || books[2].Name != "b2" {
		t.Fatalf("应 order 位次在前、其余字典序排尾: %+v", books)
	}
	if !books[0].Enabled || !books[1].Enabled {
		t.Fatalf("c3/a1 应为启用: %+v", books)
	}
	if books[2].Enabled {
		t.Fatalf("b2 应为禁用: %+v", books[2])
	}
	if got := m.Store().Len(); got != 2 {
		t.Fatalf("禁用库不应进查询链: %d", got)
	}

	// 热更新：a1 置顶 + 解禁 b2 → 三库全加载，a1 首位
	if err := m.SetOrdering([]string{"a1"}, nil); err != nil {
		t.Fatal(err)
	}
	books = m.Books()
	if books[0].Name != "a1" {
		t.Fatalf("SetOrdering 后 a1 应首位: %+v", books)
	}
	if got := m.Store().Len(); got != 3 {
		t.Fatalf("解除禁用后三库全加载: %d", got)
	}
}

// TestBadBookStatus 坏库上报：列序不符的假桔库打开失败——Books() 显示
// Status（原因可读）、不进查询链，其余库照常可用。
func TestBadBookStatus(t *testing.T) {
	dir := t.TempDir()
	makeBook(t, filepath.Join(dir, "good.obk"))
	// 坏库：vkey/vmove 两列换位（列名齐全但顺序错，Scan 会整行错位的那类）
	db, err := sql.Open("sqlite", filepath.Join(dir, "bad.obk"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE bhobk (
		id INTEGER PRIMARY KEY, vmove INTEGER, vkey INTEGER, vscore INTEGER,
		vwin INTEGER, vdraw INTEGER, vlost INTEGER, vvalid INTEGER,
		vmemo BLOB, vindex INTEGER)`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	m, err := New(dir, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if m.Store() == nil || m.Store().Len() != 1 {
		t.Fatalf("坏库应被跳过、好库照常打开: %+v", m.Store())
	}
	var bad, good *BookInfo
	for i := range m.Books() {
		b := m.Books()[i]
		switch b.Name {
		case "bad":
			bad = &m.Books()[i]
		case "good":
			good = &m.Books()[i]
		}
	}
	if bad == nil || bad.Status == "" {
		t.Fatalf("坏库应带 Status 原因: %+v", bad)
	}
	if good == nil || good.Status != "" {
		t.Fatalf("好库不应带 Status: %+v", good)
	}
}

// TestNewReloadStore 初始加载与展示快照：Store 可用、Books() 返回排序快照。
func TestNewReloadStore(t *testing.T) {
	dir := t.TempDir()
	makeBook(t, filepath.Join(dir, "b2.obk"))
	makeBook(t, filepath.Join(dir, "b1.obk"))

	m, err := New(dir, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close() // 释放 SQLite 句柄，否则 t.TempDir 清理被占用文件阻塞
	if m.Store() == nil {
		t.Fatal("初始加载后 Store 不应为 nil")
	}
	books := m.Books()
	if len(books) != 2 {
		t.Fatalf("应发现 2 个库: %+v", books)
	}
	if books[0].Name != "b1" || books[1].Name != "b2" {
		t.Fatalf("应按文件名排序: %+v", books)
	}
	if books[0].Entries <= 0 {
		t.Fatalf("条数应来自 Store 缓存: %+v", books[0])
	}
}

// TestRemoveBook 删除库：先卸载（Store 不再含该库）→ 本体与同名附属
// （-wal/-shm/.obk.zip）一并删除——zip 必须删，否则下轮 reload 的 unzipAll
// 会重新解压出库文件"复活"；其余库照常可用；路径穿越/空库名拒绝且不动文件。
func TestRemoveBook(t *testing.T) {
	dir := t.TempDir()
	// zip 先于 .obk 落盘（mtime 更旧）：初始 reload 的 unzipIfNeeded 跳过，
	// 库本体保持合法 SQLite 而不被 zip 里的垃圾条目覆盖
	makeZip(t, filepath.Join(dir, "doomed.obk.zip"), map[string][]byte{"doomed.obk": []byte("junk")})
	time.Sleep(20 * time.Millisecond)
	makeBook(t, filepath.Join(dir, "doomed.obk"))
	makeBook(t, filepath.Join(dir, "keep.obk"))
	for _, f := range []string{"doomed.obk-wal", "doomed.obk-shm"} {
		if err := os.WriteFile(filepath.Join(dir, f), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	m, err := New(dir, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if got := m.Store().Len(); got != 2 {
		t.Fatalf("初始应加载两库: %d", got)
	}

	if err := m.RemoveBook("doomed"); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"doomed.obk", "doomed.obk-wal", "doomed.obk-shm", "doomed.obk.zip"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err == nil {
			t.Fatalf("%s 应已删除", f)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "keep.obk")); err != nil {
		t.Fatalf("keep.obk 不应被误删: %v", err)
	}
	if got := m.Store().Len(); got != 1 {
		t.Fatalf("删除后 Store 应只剩一库: %d", got)
	}
	books := m.Books()
	if len(books) != 1 || books[0].Name != "keep" {
		t.Fatalf("删除后展示快照应只剩 keep: %+v", books)
	}

	// 非法库名拒绝（防穿越纵深）：报错且现有库完好
	for _, bad := range []string{"", "../evil", `sub\dir`, "a/../b"} {
		if err := m.RemoveBook(bad); err == nil {
			t.Fatalf("非法库名 %q 应被拒绝", bad)
		}
	}
	if got := m.Store().Len(); got != 1 {
		t.Fatalf("拒绝后 Store 不应变化: %d", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "keep.obk")); err != nil {
		t.Fatalf("拒绝后 keep.obk 应完好: %v", err)
	}
}

// TestRunDebounceReload 真链路：Run 监听中落一个 .obk.zip → 自动解压 →
// 800ms 防抖合并 → reload → onChange 回调携带含新库的 Store → ctx 取消正常返回。
func TestRunDebounceReload(t *testing.T) {
	dir := t.TempDir()
	var fired atomic.Int32
	makeBook(t, filepath.Join(dir, "b1.obk")) // 预置一库，热加载后应共存两库
	m, err := New(dir, nil, nil, nil, func(*book.Store) { fired.Add(1) })
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- m.Run(ctx) }()

	// 等 Run 的监听就绪再写文件：inotify 不回放 w.Add 前的目录事件，
	// 写早了变更永久丢失（CI Linux runner 冷启动慢，曾致 fired 恒 1）
	select {
	case <-m.watchReady:
	case <-time.After(5 * time.Second):
		t.Fatal("Run 监听未就绪")
	}

	// 合法库打成 .obk.zip 落入监听目录
	data, err := os.ReadFile(filepath.Join(dir, "b1.obk"))
	if err != nil {
		t.Fatal(err)
	}
	makeZip(t, filepath.Join(dir, "newbook.obk.zip"), map[string][]byte{"newbook.obk": data})

	// New 的初始 reload 已回调 1 次，热加载成功后应为 2
	deadline := time.Now().Add(5 * time.Second)
	for fired.Load() < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("onChange 未触发: fired=%d", fired.Load())
		}
		time.Sleep(50 * time.Millisecond)
	}
	if got := m.Store().Len(); got != 2 {
		t.Fatalf("热加载后应有 2 个库（b1+newbook）: %d", got)
	}
	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Run 应正常返回 nil: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run 未随 ctx 取消返回")
	}
}

// TestBookNameCaseInsensitive 大写扩展名（X.OBK）与小写同权：Glob 模式全平台
// 大小写敏感，旧实现 "*.obk" 会静默漏掉 X.OBK（Windows 用户视角"库里看不到"），
// TrimSuffix(".obk") 又会把它剥成 "X.OBK" 名字——枚举+EqualFold 统一口径后的
// 回归锚（2026-11-02 审查发现）。
func TestBookNameCaseInsensitive(t *testing.T) {
	dir := t.TempDir()
	makeBook(t, filepath.Join(dir, "X.OBK"))
	makeBook(t, filepath.Join(dir, "plain.obk"))

	m, err := New(dir, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if m.Store() == nil || m.Store().Len() != 2 {
		t.Fatalf("大写 .OBK 应与 .obk 同权加载: %d", m.Store().Len())
	}
	books := m.Books()
	if len(books) != 2 {
		t.Fatalf("应发现 2 个库: %+v", books)
	}
	byName := map[string]string{}
	for _, b := range books {
		byName[b.Name] = "seen"
	}
	if byName["X"] == "" || byName["plain"] == "" {
		t.Fatalf("后缀剥离应大小写不敏感（X 而非 X.OBK）: %+v", books)
	}
}
