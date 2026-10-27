package tray

// embeddedIconBytes 测试辅助：直接暴露内嵌字节（校验 LoadIcon 兜底来源）。
func embeddedIconBytes() ([]byte, error) { return embeddedIcon, nil }
