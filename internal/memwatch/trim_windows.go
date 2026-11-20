//go:build windows

package memwatch

import "golang.org/x/sys/windows"

// trimWorkingSet 主动修剪本进程工作集（物理内存页整体转 standby）。
// "电脑管家 EmptyWorkingSet"的进程内正规同款：commit/虚拟占用不变，页面
// 访问时软缺页秒回——只在空闲归还路径调用（对局期调用会换来无意义的缺页
// 开销），让任务管理器/管家的显示数字与真实归还后的水位一致。
// 失败返回 false——显示层修剪是锦上添花，绝不影响主链路。
func trimWorkingSet() bool {
	err := windows.SetProcessWorkingSetSizeEx(
		windows.CurrentProcess(),
		^uintptr(0), // (SIZE_T)-1, -1 = 最大化收缩（OS 按需修剪全部可换页）
		^uintptr(0),
		0,
	)
	return err == nil
}
