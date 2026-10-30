package console

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"fyne.io/fyne/v2/theme"
)

// TestFmtInt 千分位。
func TestFmtInt(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, "0"}, {999, "999"}, {1000, "1,000"}, {1234567, "1,234,567"},
	}
	for _, c := range cases {
		if got := fmtInt(c.in); got != c.want {
			t.Errorf("fmtInt(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestFmtNPS 速率主数字自适应单位（单位 "n/s" 由界面后缀小字显示）。
func TestFmtNPS(t *testing.T) {
	if got := fmtNPS(0); got != "—" {
		t.Errorf("fmtNPS(0) = %q", got)
	}
	if got := fmtNPS(500); got != "500" {
		t.Errorf("fmtNPS(500) = %q", got)
	}
	if got := fmtNPS(923_400); got != "923.4K" {
		t.Errorf("fmtNPS(923400) = %q", got)
	}
	if got := fmtNPS(8_523_000); got != "8.52M" {
		t.Errorf("fmtNPS(8523000) = %q", got)
	}
}

// TestFmtCompactN 节点紧凑显示。
func TestFmtCompactN(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, "0"}, {999, "999"}, {12_400, "12.4K"},
		{12_400_000, "12.4M"}, {1_000_000, "1M"},
	}
	for _, c := range cases {
		if got := fmtCompactN(c.in); got != c.want {
			t.Errorf("fmtCompactN(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestFmtHashUsed 哈希占用 "已用/配置" 短式。
func TestFmtHashUsed(t *testing.T) {
	cases := []struct {
		hashfull, mb int
		want         string
	}{
		{0, 512, "—"}, {128, 0, "—"}, // 配置缺失 / 尚未报告占用
		{250, 512, "128M/512M"}, // 512×0.25
		{128, 2048, "262M/2G"},  // 大容量走 G 短式（2048×12.8%≈262）
	}
	for _, c := range cases {
		if got := fmtHashUsed(c.hashfull, c.mb); got != c.want {
			t.Errorf("fmtHashUsed(%d,%d) = %q, want %q", c.hashfull, c.mb, got, c.want)
		}
	}
}

// TestFpShort 指纹分组短式：剔除前缀/分隔符，前 12 hex 分 3 组 + 末 4 位。
func TestFpShort(t *testing.T) {
	full := "9f3c71ad84be2c1f5d6e7a8b9c0d1e2f3a4b5c6d7e8f9a0b1c2d3e4f5a6b7c8"
	want := "9f3c 71ad 84be … b7c8"
	if got := fpShort(full); got != want {
		t.Errorf("fpShort = %q, want %q", got, want)
	}
	if got := fpShort("SHA256:" + full); got != want {
		t.Errorf("fpShort(带前缀) = %q, want %q", got, want)
	}
	if got := fpShort("abc123"); got != "abc123" {
		t.Errorf("短指纹应原样返回, got %q", got)
	}
}

// TestFenBrief "最后 position"列内容：FEN 摘要 / 时间回退 / 双空返回空串
// （UI 层 orDash 再补占位）。
func TestFenBrief(t *testing.T) {
	tm := time.Date(2026, 10, 2, 20, 33, 6, 0, time.Local)
	if got := fenBrief("rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR w - - 0 1", tm); got != "rnbakabnr/9/1c5c…" {
		t.Errorf("fenBrief(长FEN) = %q", got)
	}
	if got := fenBrief("", tm); got != "20:33:06" {
		t.Errorf("fenBrief(空FEN) = %q", got)
	}
	if got := fenBrief("", time.Time{}); got != "" {
		t.Errorf("fenBrief(双空) = %q", got)
	}
}

// TestFmtScore 红方视角分数显示。
func TestFmtScore(t *testing.T) {
	cp260, cpM110, mate3, mateM2 := 260, -110, 3, -2
	cases := []struct {
		cp, mate *int
		want     string
	}{
		{nil, nil, "—"},
		{&cp260, nil, "+2.60"},
		{&cpM110, nil, "-1.10"},
		{nil, &mate3, "M3"},
		{nil, &mateM2, "-M2"},
		{&cp260, &mate3, "M3"}, // 杀棋优先
	}
	for _, c := range cases {
		if got := fmtScore(c.cp, c.mate); got != c.want {
			t.Errorf("fmtScore(%v,%v) = %q, want %q", c.cp, c.mate, got, c.want)
		}
	}
}

// TestFmtState 状态中文化。
func TestFmtState(t *testing.T) {
	for in, want := range map[string]string{
		"loading": "加载中", "ready": "就绪", "error": "错误", "offline": "未启用", "": "—",
	} {
		if got := fmtState(in); got != want {
			t.Errorf("fmtState(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestFmtRTT RTT 显示。
func TestFmtRTT(t *testing.T) {
	if got := fmtRTT(-1); got != "—" {
		t.Errorf("fmtRTT(-1) = %q", got)
	}
	if got := fmtRTT(12); got != "12 ms" {
		t.Errorf("fmtRTT(12) = %q", got)
	}
}

// TestFmtTime 时间显示：零值显示占位符。
func TestFmtTime(t *testing.T) {
	if got := fmtTime(time.Time{}); got != "—" {
		t.Errorf("fmtTime(zero) = %q", got)
	}
	tm := time.Date(2026, 9, 10, 15, 4, 5, 0, time.Local)
	if got := fmtTime(tm); got != "15:04:05" {
		t.Errorf("fmtTime = %q", got)
	}
}

// TestTailLines 日志尾部读取：只取最后 n 行，长文件不读全量。
func TestTailLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cabinet.log")

	// 20 行日志 → 取最后 15 行
	content := ""
	for i := 1; i <= 20; i++ {
		content += "2026-09-10 line" + string(rune('0'+i/10)) + string(rune('0'+i%10)) + "\n"
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	lines := tailLines(path, 15)
	if len(lines) != 15 {
		t.Fatalf("应取 15 行, got %d", len(lines))
	}
	if lines[0] != "2026-09-10 line06" {
		t.Fatalf("首行应为第 6 行, got %q", lines[0])
	}
	if lines[14] != "2026-09-10 line20" {
		t.Fatalf("末行应为第 20 行, got %q", lines[14])
	}

	// 不存在文件：空切片（UI 显示占位）
	if lines := tailLines(filepath.Join(dir, "nope.log"), 15); lines != nil {
		t.Fatalf("缺失文件应返回 nil, got %v", lines)
	}

	// 短文件：全量返回
	if err := os.WriteFile(path, []byte("a\nb\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if lines := tailLines(path, 15); len(lines) != 2 || lines[0] != "a" || lines[1] != "b" {
		t.Fatalf("短文件应全量返回, got %v", lines)
	}
}

// TestParseLogLine slog TextHandler 行解析：带引号（含转义）与中文无引号两形态，
// 时间取时分秒段；解析失败 ok=false。
func TestParseLogLine(t *testing.T) {
	e, ok := parseLogLine(`time=2026-10-29T02:34:03.686+08:00 level=INFO msg="WSS 服务启动" addr=:9527`)
	if !ok || e.time != "02:34:03" || e.level != "INFO" || e.msg != "WSS 服务启动" || e.rest != "addr=:9527" {
		t.Fatalf("带引号行解析不符: %+v ok=%v", e, ok)
	}
	e, ok = parseLogLine(`time=2026-10-29T02:34:03.686+08:00 level=INFO msg=收到局面 fen=abc def=1`)
	if !ok || e.msg != "收到局面" || e.rest != "fen=abc def=1" {
		t.Fatalf("无引号中文行解析不符: %+v ok=%v", e, ok)
	}
	// 带引号 + 内嵌转义引号（错误串实录形态）：反转义还原原始值
	e, ok = parseLogLine(`time=2026-10-29T02:34:03.686+08:00 level=INFO msg="Get \"https://api.github.com\": EOF"`)
	if !ok || e.msg != `Get "https://api.github.com": EOF` {
		t.Fatalf("转义引号行解析不符: %+v ok=%v", e, ok)
	}
	e, ok = parseLogLine(`time=2026-10-29T02:34:03.686+08:00 level=WARN msg="最近局面引擎不安全，跳过重放（等新局面覆盖）" fen=x`)
	if !ok || e.level != "WARN" || e.msg != "最近局面引擎不安全，跳过重放（等新局面覆盖）" {
		t.Fatalf("WARN 行解析不符: %+v ok=%v", e, ok)
	}
	if _, ok := parseLogLine("乱七八糟不是日志行"); ok {
		t.Fatal("异构行应解析失败")
	}
}

// TestCompactLogLines 批量解析 + 垃圾行剔除：Fyne 翻译失败三连与缩进续行，
// 异构行原样透传（time/level 为空）。
func TestCompactLogLines(t *testing.T) {
	lines := []string{
		`time=2026-10-29T02:34:03.243+08:00 level=INFO msg="Fyne error:  Translation failure"`,
		`time=2026-10-29T02:34:03.243+08:00 level=INFO msg="  Cause: message \"(Select one)\" not found in language \"zh-Hans\""`,
		`time=2026-10-29T02:34:03.243+08:00 level=INFO msg="  At: C:/Users/12567/go/pkg/mod/fyne.io/fyne/v2@v2.8.2/lang/lang.go:99"`,
		`time=2026-10-29T02:34:03.686+08:00 level=INFO msg=引擎状态 state=ready`,
		"半行异构内容",
	}
	got := compactLogLines(lines)
	if len(got) != 2 {
		t.Fatalf("应剔除 3 行垃圾剩 2 行, got %d: %+v", len(got), got)
	}
	if got[0].msg != "引擎状态" || got[1].time != "" || got[1].msg != "半行异构内容" {
		t.Fatalf("保留行内容不符: %+v", got)
	}
}

// TestLogLineText 紧凑行文本：结构化行带时间级别前缀，异构行原样。
func TestLogLineText(t *testing.T) {
	if got := logLineText(logEntry{time: "02:34:03", level: "INFO", msg: "WSS 服务启动", rest: "addr=:9527"}); got != "02:34:03 INFO  WSS 服务启动 addr=:9527" {
		t.Fatalf("结构化行不符: %q", got)
	}
	if got := logLineText(logEntry{msg: "原样"}); got != "原样" {
		t.Fatalf("异构行应原样: %q", got)
	}
}

// TestLogLevelColor 级别着色走主题机制：WARN/ERROR 为棋韵自定义色名。
func TestLogLevelColor(t *testing.T) {
	if got := logLevelColor("WARN"); got != qiColorLogWarn {
		t.Fatalf("WARN 应映射 qiLogWarn, got %v", got)
	}
	if got := logLevelColor("ERROR"); got != qiColorLogError {
		t.Fatalf("ERROR 应映射 qiLogError, got %v", got)
	}
	if got := logLevelColor("INFO"); got != theme.ColorNameForeground {
		t.Fatalf("INFO 应走前景色, got %v", got)
	}
}

// TestIcon 内嵌图标资源：非空 + PNG 魔数。
func TestIcon(t *testing.T) {
	res := Icon()
	if res == nil || len(res.Content()) == 0 {
		t.Fatal("内嵌图标不应为空")
	}
	c := res.Content()
	if c[0] != 0x89 || c[1] != 'P' || c[2] != 'N' || c[3] != 'G' {
		t.Fatal("内嵌图标应为 PNG 格式")
	}
}
