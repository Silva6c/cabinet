//go:build windows

package main

import "syscall"

// init 将控制台输出代码页切到 UTF-8：slog 日志输出为 UTF-8，
// 中文 Windows 的 conhost 默认 GBK 代码页（936）会显示乱码。
func init() {
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	proc := kernel32.NewProc("SetConsoleOutputCP")
	_, _, _ = proc.Call(65001) // CP_UTF8；失败（如输出重定向）无碍
}
