// Package logging 基于 slog + lumberjack 的日志初始化：控制台与文件双写、按大小滚动。
package logging

import (
	"fmt"
	"io"
	"log/slog"
	"os"

	"gopkg.in/natefinch/lumberjack.v2"

	"cabinet/internal/config"
)

// Setup 按配置初始化 slog 默认 Logger 并返回。
// File 为空时只写控制台；否则控制台+文件双写，文件按 MaxSizeMB/MaxBackups 滚动。
func Setup(cfg config.LogConfig) (*slog.Logger, error) {
	var level slog.Level
	switch cfg.Level {
	case "debug":
		level = slog.LevelDebug
	case "info":
		level = slog.LevelInfo
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		return nil, fmt.Errorf("未知日志级别 %q（可选 debug/info/warn/error）", cfg.Level)
	}

	// 权威输出是文件；控制台仅在 console 构建下启用（windowsgui 进程的
	// stdout 无效，MultiWriter 前置失败会吞掉文件写入，见 console_off.go）
	var w io.Writer = &lumberjack.Logger{
		Filename:   cfg.File,
		MaxSize:    cfg.MaxSizeMB,
		MaxBackups: cfg.MaxBackups,
		Compress:   true,
	}
	if consoleOutput {
		w = io.MultiWriter(os.Stdout, w)
	}

	logger := slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(logger)
	return logger, nil
}
