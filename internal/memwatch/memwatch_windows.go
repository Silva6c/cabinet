//go:build windows

package memwatch

import (
	"syscall"
	"unsafe"
)

// psapi.dll 的 GetProcessMemoryInfo：x/sys/windows（v0.49）未封装该 API，
// 走 LazyProc 惯用法（同 internal/server/firewall_windows.go 的 ShellExecuteW）。
var (
	procGetProcessMemoryInfo = syscall.NewLazyDLL("psapi.dll").NewProc("GetProcessMemoryInfo")
)

// processMemoryCounters 对齐 PSAPI 的 PROCESS_MEMORY_COUNTERS（x64 下全部
// 计数字段为 uintptr=8 字节，只消费 workingSetSize）。
type processMemoryCounters struct {
	cb                         uint32
	pageFaultCount             uint32
	peakWorkingSetSize         uintptr
	workingSetSize             uintptr
	quotaPeakPagedPoolUsage    uintptr
	quotaPagedPoolUsage        uintptr
	quotaPeakNonPagedPoolUsage uintptr
	quotaNonPagedPoolUsage     uintptr
	pagefileUsage              uintptr
	peakPagefileUsage          uintptr
}

// workingSetBytes 进程物理内存工作集（含非 Go 分配：fyne 纹理/cgo/NNUE 映射）。
// 查自身进程用 GetCurrentProcess 伪句柄无需额外权限；失败返回 (0,false)——
// 计量是诊断辅助，绝不影响主链路。
func workingSetBytes() (uint64, bool) {
	var m processMemoryCounters
	m.cb = uint32(unsafe.Sizeof(m))
	h, err := syscall.GetCurrentProcess()
	if err != nil {
		return 0, false
	}
	r1, _, _ := procGetProcessMemoryInfo.Call(
		uintptr(h),
		uintptr(unsafe.Pointer(&m)),
		uintptr(unsafe.Sizeof(m)),
	)
	if r1 == 0 {
		return 0, false
	}
	return uint64(m.workingSetSize), true
}
