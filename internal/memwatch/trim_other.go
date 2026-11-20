//go:build !windows

package memwatch

// trimWorkingSet 非 Windows 存根：签名与 Windows 实现逐字对表（跨平台雷区
// 纪律）。类 Unix 平台 FreeOSMemory 的 scavenger（madvise DONTNEED）已即
// 时归还物理页，无需额外工作集修剪。
func trimWorkingSet() bool {
	return false
}
