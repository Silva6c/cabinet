//go:build !windows

// 非 Windows 平台无单实例锁实现：端口占用报错兜底（控制台可见）。
package main

// acquireSingleton 占位实现：始终放行（configPath 仅对齐 Windows 版签名）。
func acquireSingleton(silent bool, configPath string) bool { return true }
