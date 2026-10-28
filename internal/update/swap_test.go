package update

import (
	"os"
	"path/filepath"
	"testing"
)

// mustWrite 测试辅助：写文件并断言成功。
func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestApply 换入成功：cur=新内容，old=旧内容，.new 消失。
func TestApply(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "app.exe")
	mustWrite(t, exe, "OLD-VERSION")
	mustWrite(t, exe+".new", "NEW-VERSION")

	if err := Apply(dir, "app.exe"); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got := mustRead(t, exe); got != "NEW-VERSION" {
		t.Fatalf("cur = %q, want NEW-VERSION", got)
	}
	if got := mustRead(t, exe+".old"); got != "OLD-VERSION" {
		t.Fatalf("old = %q, want OLD-VERSION", got)
	}
	if _, err := os.Stat(exe + ".new"); !os.IsNotExist(err) {
		t.Fatal(".new 换入后应消失")
	}
}

// TestApplyRollback 第二步失败（.new 缺失）→ 自动回滚，cur 恢复旧内容。
func TestApplyRollback(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "app.exe")
	mustWrite(t, exe, "OLD-VERSION")
	// .new 故意不写 → 第二步 rename 必败

	if err := Apply(dir, "app.exe"); err == nil {
		t.Fatal("缺 .new 时 Apply 应报错")
	}
	if got := mustRead(t, exe); got != "OLD-VERSION" {
		t.Fatalf("回滚后 cur = %q, want OLD-VERSION", got)
	}
	if _, err := os.Stat(exe + ".old"); !os.IsNotExist(err) {
		t.Fatal("回滚后 .old 应已改回（不存在）")
	}
}

// TestCleanupOnStart_Recover 换到一半中断（cur 缺失、.old 在）→ 自愈恢复。
func TestCleanupOnStart_Recover(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "app.exe.old"), "OLD-VERSION")
	// cur 故意缺失

	if !CleanupOnStart(dir, "app.exe") {
		t.Fatal("应报告执行了恢复")
	}
	if got := mustRead(t, filepath.Join(dir, "app.exe")); got != "OLD-VERSION" {
		t.Fatalf("恢复后 cur = %q", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "app.exe.old")); !os.IsNotExist(err) {
		t.Fatal("恢复后 .old 应消失")
	}
}

// TestCleanupOnStart_Residue 正常路径：全部残留删除、.old 保留（延迟删）。
func TestCleanupOnStart_Residue(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "app.exe")
	mustWrite(t, exe, "CURRENT")
	mustWrite(t, exe+".new", "NEW")
	mustWrite(t, filepath.Join(dir, updateZipPart), "half")
	mustWrite(t, filepath.Join(dir, updateZip), "done")
	mustWrite(t, filepath.Join(dir, updateProbe), "probe")
	mustWrite(t, exe+".old", "OLD")

	if CleanupOnStart(dir, "app.exe") {
		t.Fatal("exe 完好时不应报告恢复")
	}
	for _, residue := range []string{"app.exe.new", updateZipPart, updateZip, updateProbe} {
		if _, err := os.Stat(filepath.Join(dir, residue)); !os.IsNotExist(err) {
			t.Fatalf("残留 %s 未清理", residue)
		}
	}
	if got := mustRead(t, exe+".old"); got != "OLD" {
		t.Fatal(".old 应保留到服务稳定运行后（RemoveOldBackup）")
	}
	if got := mustRead(t, exe); got != "CURRENT" {
		t.Fatal("exe 不应被动")
	}
}

// TestRemoveOldBackup 备份删除（存在删、不存在静默）。
func TestRemoveOldBackup(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "app.exe")
	mustWrite(t, exe+".old", "OLD")
	RemoveOldBackup(dir, "app.exe")
	if _, err := os.Stat(exe + ".old"); !os.IsNotExist(err) {
		t.Fatal(".old 应被删除")
	}
	RemoveOldBackup(dir, "app.exe") // 不存在：静默，不得 panic
}

// TestWritable 可写目录通过；只读场景难跨平台模拟，至少覆盖不存在的目录报错。
func TestWritable(t *testing.T) {
	if err := Writable(t.TempDir()); err != nil {
		t.Fatalf("可写目录应通过: %v", err)
	}
	if err := Writable(filepath.Join(t.TempDir(), "not-exist-dir")); err == nil {
		t.Fatal("不存在目录应报错")
	}
}
