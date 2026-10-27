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

// makeBook 构造最小合法桔库：book.OpenBook 只校验 bhobk 表存在（列序铁闸在
// 查询侧 Scan 时生效，本 helper 只需表名与列名对齐 schema）。
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

	m, err := New(dir, nil, nil)
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

// TestNewReloadStore 初始加载与展示快照：Store 可用、Books() 返回排序快照。
func TestNewReloadStore(t *testing.T) {
	dir := t.TempDir()
	makeBook(t, filepath.Join(dir, "b2.obk"))
	makeBook(t, filepath.Join(dir, "b1.obk"))

	m, err := New(dir, nil, nil)
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

// TestRunDebounceReload 真链路：Run 监听中落一个 .obk.zip → 自动解压 →
// 800ms 防抖合并 → reload → onChange 回调携带含新库的 Store → ctx 取消正常返回。
func TestRunDebounceReload(t *testing.T) {
	dir := t.TempDir()
	var fired atomic.Int32
	makeBook(t, filepath.Join(dir, "b1.obk")) // 预置一库，热加载后应共存两库
	m, err := New(dir, nil, func(*book.Store) { fired.Add(1) })
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
