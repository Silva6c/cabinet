//go:build !windows

package memwatch

// workingSetBytes 非 Windows 平台的进程工作集查询存根：签名与 Windows 实现
// 逐字对表（跨平台雷区纪律）。工作集缺省 (0,false)，FormatShort 显示"—"。
func workingSetBytes() (uint64, bool) {
	return 0, false
}
