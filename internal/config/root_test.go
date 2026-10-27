package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestResolveResourceRoot_TwoBranches 资源根两分支（root.go 锚定规则）：
// exe 目录下有 appdata\ → 资源根 = appdata\（便携包布局）；
// 没有 → 资源根 = exe 目录本身（开发目录布局，零迁移成本回落）。
func TestResolveResourceRoot_TwoBranches(t *testing.T) {
	exeDir := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(exeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// 无 appdata：回落 exe 目录本身
	if got := resolveResourceRoot(exeDir); got != exeDir {
		t.Fatalf("无 appdata 时应回落 exe 目录: got %q want %q", got, exeDir)
	}
	// 有 appdata：资源根 = appdata（目录才算，同名文件不算）
	appdata := filepath.Join(exeDir, AppdataDirName)
	if err := os.MkdirAll(appdata, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := resolveResourceRoot(exeDir); got != appdata {
		t.Fatalf("有 appdata 时资源根应为 appdata: got %q want %q", got, appdata)
	}
	if err := os.Remove(appdata); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(appdata, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := resolveResourceRoot(exeDir); got != exeDir {
		t.Fatalf("appdata 是同名文件时不应认作便携布局: got %q want %q", got, exeDir)
	}
}

// TestResolvePath 相对路径按资源根解析；绝对路径（当前平台语义）原样透传；空串保持空串。
func TestResolvePath(t *testing.T) {
	root := filepath.Join(t.TempDir(), "appdata")
	abs := filepath.Join(t.TempDir(), "elsewhere", "pikafish.exe")
	cases := []struct{ in, want string }{
		{"library/engine/pikafish.exe", filepath.Join(root, "library", "engine", "pikafish.exe")},
		{"cert", filepath.Join(root, "cert")},
		{abs, abs}, // 绝对路径透传（含盘符原样使用，不拼资源根）
		{"", ""},   // 空值语义由字段自行处理，此处不代劳
	}
	for _, c := range cases {
		if got := ResolvePath(root, c.in); got != c.want {
			t.Errorf("ResolvePath(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestResolvePaths_OldTomlCompat 旧配置兼容保证（验收"旧 toml 零改动"的单测化）：
// 旧版 cabinet.toml 的相对路径写法原样内嵌，经 Load + ResolvePaths 后所有路径
// 都应解析到资源根下的等价位置——这正是"升级只换 exe、旧配置一个字不改"的根基。
func TestResolvePaths_OldTomlCompat(t *testing.T) {
	// 与改造前 configs/cabinet.toml 完全同构（token 无关紧要，随便填）
	oldToml := `
bind = ":9527"
token = "x"

[tls]
  cert_dir = "cert"

[log]
  level = "info"
  file = "logs/cabinet.log"
  max_size_mb = 10
  max_backups = 5

[engine]
enable = true
path = "library/engine/pikafish.exe"
nnue = "library/nnue/pikafish.nnue"
threads = 16
hash_mb = 10240
multi_pv = 1
depth = 20

[library]
  books_dir = "library/books"

[store]
  enable = true
  path = "data/suggestions.db"
`
	path := filepath.Join(t.TempDir(), "cabinet.toml")
	if err := os.WriteFile(path, []byte(oldToml), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, created, err := Load(path)
	if err != nil || created {
		t.Fatalf("Load 异常: created=%v err=%v", created, err)
	}
	root := filepath.Join(t.TempDir(), "portable", AppdataDirName)
	cfg.ResolvePaths(root)

	want := map[string]string{
		"tls.cert_dir":  filepath.Join(root, "cert"),
		"log.file":      filepath.Join(root, "logs", "cabinet.log"),
		"engine.path":   filepath.Join(root, "library", "engine", "pikafish.exe"),
		"engine.nnue":   filepath.Join(root, "library", "nnue", "pikafish.nnue"),
		"library.books": filepath.Join(root, "library", "books"),
		"store.path":    filepath.Join(root, "data", "suggestions.db"),
	}
	got := map[string]string{
		"tls.cert_dir":  cfg.TLS.CertDir,
		"log.file":      cfg.Log.File,
		"engine.path":   cfg.Engine.Path,
		"engine.nnue":   cfg.Engine.NNUE,
		"library.books": cfg.Library.BooksDir,
		"store.path":    cfg.Store.Path,
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s = %q, want %q", k, got[k], w)
		}
	}
}

// TestResolvePaths_AbsolutePassthrough 配置里写绝对路径时原样使用，不被资源根改写
// （用户把引擎/库放到盘上其他位置的合法用法）。
func TestResolvePaths_AbsolutePassthrough(t *testing.T) {
	cfg := Default()
	absEngine := filepath.Join(t.TempDir(), "engines", "pikafish.exe")
	cfg.Engine.Path = absEngine
	cfg.ResolvePaths(filepath.Join(t.TempDir(), "appdata"))
	if cfg.Engine.Path != absEngine {
		t.Fatalf("绝对路径应透传: got %q want %q", cfg.Engine.Path, absEngine)
	}
}

// TestResolvePaths_EmptyFieldsBackfill 旧 toml 显式写空的路径键应回填默认值
// （Config 头部长期约束：Go 零值不满足时显式赋默认，"升级只换 exe"永远成立）。
func TestResolvePaths_EmptyFieldsBackfill(t *testing.T) {
	tomlStr := `
bind = ":9527"
token = "x"

[engine]
enable = true
path = ""
nnue = ""

[library]
  books_dir = ""

[store]
  enable = true
  path = ""
`
	path := filepath.Join(t.TempDir(), "cabinet.toml")
	if err := os.WriteFile(path, []byte(tomlStr), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	def := Default()
	if cfg.Engine.Path != def.Engine.Path || cfg.Engine.NNUE != def.Engine.NNUE {
		t.Fatalf("engine 空路径应回填默认: %+v", cfg.Engine)
	}
	if cfg.Library.BooksDir != def.Library.BooksDir || cfg.Store.Path != def.Store.Path {
		t.Fatalf("library/store 空路径应回填默认: books=%q store=%q", cfg.Library.BooksDir, cfg.Store.Path)
	}
}

// TestExeDir 基本断言：可取得绝对路径（appdata 两分支的注入测试已在
// resolveResourceRoot 层覆盖，此处验证真实 os.Executable 链路可用）。
func TestExeDir(t *testing.T) {
	dir, err := ExeDir()
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(dir) {
		t.Fatalf("ExeDir 应为绝对路径: %q", dir)
	}
}

// TestUnresolvePaths RoundTrip：ResolvePaths 固化的绝对路径经 UnresolvePaths
// 反解回相对形态（算力落盘的便携安全前提，2026-10-18）。资源根外的用户自配
// 绝对路径原样保留（不猜测意图）。
func TestUnresolvePaths(t *testing.T) {
	root := t.TempDir()
	cfg := Default()
	cfg.ResolvePaths(root)
	if !filepath.IsAbs(cfg.Engine.Path) {
		t.Fatalf("前置条件：ResolvePaths 后应为绝对路径: %q", cfg.Engine.Path)
	}

	cfg.Engine.HashMB = 512 // 模拟用户切换后的快照
	cfg.UnresolvePaths(root)

	if cfg.Engine.Path != filepath.ToSlash(filepath.Join("library", "engine", "pikafish.exe")) {
		t.Errorf("engine.path 应反解回相对: %q", cfg.Engine.Path)
	}
	if cfg.Log.File != filepath.ToSlash(filepath.Join("logs", "cabinet.log")) {
		t.Errorf("log.file 应反解回相对: %q", cfg.Log.File)
	}
	if cfg.Engine.HashMB != 512 {
		t.Errorf("算力字段不应被反解触碰: %d", cfg.Engine.HashMB)
	}

	// 资源根外的绝对路径：原样保留
	cfg2 := Default()
	outside := filepath.Join(filepath.Dir(root), "elsewhere", "pikafish.exe")
	cfg2.Engine.Path = outside
	cfg2.UnresolvePaths(root)
	if cfg2.Engine.Path != filepath.Clean(outside) {
		t.Errorf("根外路径应原样保留: %q", cfg2.Engine.Path)
	}
}

// TestUnresolvePaths_MatchesResolveFields 字段完备性对账：UnresolvePaths 与
// ResolvePaths 必须覆盖同一组字段——新增路径字段两处同步加行（root.go 的
// 评审铁律在这里用测试钉死，防单向遗漏）。
func TestUnresolvePaths_MatchesResolveFields(t *testing.T) {
	root := t.TempDir()
	cfg := Default()
	cfg.Store.Path = filepath.Join(root, "sub", "db.sqlite") // 根内深一层
	cfg.ResolvePaths(root)
	for _, p := range []string{cfg.TLS.CertDir, cfg.Log.File, cfg.Engine.Path,
		cfg.Engine.NNUE, cfg.Library.BooksDir, cfg.Store.Path} {
		if !filepath.IsAbs(p) {
			t.Fatalf("前置条件失败：应为绝对路径 %q", p)
		}
	}
	cfg.UnresolvePaths(root)
	for name, p := range map[string]string{
		"tls.cert_dir":  cfg.TLS.CertDir,
		"log.file":      cfg.Log.File,
		"engine.path":   cfg.Engine.Path,
		"engine.nnue":   cfg.Engine.NNUE,
		"library.books": cfg.Library.BooksDir,
		"store.path":    cfg.Store.Path,
	} {
		if filepath.IsAbs(p) {
			t.Errorf("%s 反解后仍为绝对路径: %q", name, p)
		}
	}
}
