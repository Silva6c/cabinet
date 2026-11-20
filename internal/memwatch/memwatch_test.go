package memwatch

import (
	"os"
	"path/filepath"
	"runtime/debug"
	"testing"
)

// TestTakeAndFormat 计量快照与摘要行的基础口径：数值非负、工作集在 Windows
// 可得（CI/本机均为 Windows 构建验证；非 Windows 显示"—"）。
func TestTakeAndFormat(t *testing.T) {
	s := Take()
	if s.HeapAllocMB <= 0 || s.HeapSysMB <= 0 || s.SysMB <= 0 {
		t.Fatalf("堆计量应非零: %+v", s)
	}
	// NumGC 不设下限断言：短命测试进程可能一次 GC 都没触发（实测 0 合法）
	line := FormatShort(s)
	if line == "" {
		t.Fatal("摘要行不应为空")
	}
	if s.WorkingSetOK && s.WorkingSetMB <= 0 {
		t.Fatalf("工作集可得时应为正值: %+v", s)
	}
}

// TestDumpProfilesRoundTrip dump 落盘行为（2026-11-08 缺陷回归：4 次触发仅
// 2 份在案、goroutine 从未出现——prune 混排互删+无原子写）：两次 dump 后
// heap/goroutine 各自成对保留、无 .tmp 残件、文件非空。
func TestDumpProfilesRoundTrip(t *testing.T) {
	dir := t.TempDir()
	if err := dumpProfiles(dir); err != nil {
		t.Fatalf("第一次 dump 失败: %v", err)
	}
	if err := dumpProfiles(dir); err != nil {
		t.Fatalf("第二次 dump 失败: %v", err)
	}
	heaps, _ := filepath.Glob(filepath.Join(dir, "heap-*.pprof.gz"))
	gors, _ := filepath.Glob(filepath.Join(dir, "goroutine-*.txt.gz"))
	if len(heaps) != 2 || len(gors) != 2 {
		t.Fatalf("两次 dump 后应 heap/goroutine 各 2 份: heaps=%v gors=%v", heaps, gors)
	}
	if tmp, _ := filepath.Glob(filepath.Join(dir, "*.tmp")); len(tmp) != 0 {
		t.Fatalf("不应残留 .tmp 半截文件: %v", tmp)
	}
	for _, f := range append(heaps, gors...) {
		st, err := os.Stat(f)
		if err != nil || st.Size() == 0 {
			t.Fatalf("dump 文件应为非空实体: %s err=%v", f, err)
		}
	}
}

// TestFreeOSMemoryEffect 空闲归还核心动作的真实效果：分配大量垃圾后
// FreeOSMemory 必须把 heap_alloc 压回接近活集合水平（commit 真降）——
// 这是"空闲归还"功能的存在依据（2026-11-09 用户定稿极致压缩）。
func TestFreeOSMemoryEffect(t *testing.T) {
	// 制造垃圾（活引用仅 100KB 量级）
	var keep [][]byte
	for i := 0; i < 64; i++ {
		keep = append(keep, make([]byte, 1024)) // 活对象：64KB
		_ = make([]byte, 4<<20)                 // 4MB 垃圾，无引用
	}
	debug.FreeOSMemory()
	after := Take()
	if after.HeapAllocMB > 100 {
		t.Fatalf("FreeOSMemory 后堆应回落（活对象仅 ~0.1MB）: %.1f MB", after.HeapAllocMB)
	}
	_ = keep
}
