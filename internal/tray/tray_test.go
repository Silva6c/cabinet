package tray

import "testing"

// TestLoadIcon 图标加载：内嵌资源永远兜底非空；磁盘覆盖文件存在时优先且内容一致。
// dir 传空串 = 按 CWD 相对（go test 的 cwd 为包目录 internal/tray/，assets/icon/
// 不存在 → 命中内嵌）。
func TestLoadIcon(t *testing.T) {
	icon := LoadIcon("")
	if len(icon) == 0 {
		t.Fatal("LoadIcon 返回空（内嵌资源未生效）")
	}
	if icon[0] != 0x00 || icon[1] != 0x00 || icon[2] != 0x01 || icon[3] != 0x00 {
		t.Fatal("图标不是合法 .ico 魔数（00 00 01 00）")
	}
	// 工作目录在仓库根时（go test 的 cwd 为包目录 internal/tray/），
	// assets/icon/ 不存在 → 应命中内嵌；其大小应与嵌入的 cabinet.ico 一致
	embedded, err := embeddedIconBytes()
	if err != nil {
		t.Fatal(err)
	}
	if len(icon) != len(embedded) {
		t.Fatalf("图标大小不符: got %d want %d", len(icon), len(embedded))
	}
}
