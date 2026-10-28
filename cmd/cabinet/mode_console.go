//go:build console

// console 构建（cabinet-console.exe，go build -tags console）：纯服务模式，
// 默认无托盘无主窗口——控制台日志就是界面。仍可用 -no-tray=false 反转。

package main

// defaultNoTray -no-tray 标志的默认值：console 版默认纯服务。
const defaultNoTray = true
