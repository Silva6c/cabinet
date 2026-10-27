//go:build !console

// 默认构建（windowsgui 托盘版）：无控制台，stdout 句柄无效或指向无人读的
// 管道——写入会出错或阻塞，且 io.MultiWriter 在前一个 writer 出错时会跳过
// 后续 writer，导致文件日志静默丢失（实测）。故只写文件。
package logging

// consoleOutput 是否同时输出到控制台。
const consoleOutput = false
