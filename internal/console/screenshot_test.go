// screenshot_test.go 控制台截图工具（支撑 docs/console-screenshot.png 重拍）：
// 用 fyne 的 software 画布渲染真实控件树（含注入的演示数据），无窗口、无 GL，
// 产出折叠态 PNG。默认跳过——需显式 CABINET_SHOT=1，日常 go test 零副作用：
//
//	CABINET_SHOT=1 go test ./internal/console -run TestConsoleScreenshot -v
package console

import (
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/driver/software"
	"fyne.io/fyne/v2/test"
)

// demoOptions 演示数据：贴近真实运行态（引擎搜索中/一台手机在线/库已加载/少量沉淀），
// 只用于截图与人工验收，不连接任何服务。
func demoOptions() Options {
	now := time.Now()
	return Options{
		Version:         "0.1.0",
		Icon:            Icon(),
		LogDir:          "logs",
		CertFingerprint: "9f3c71ad84be2c1f5d6e7a8b9c0d1e2f3a4b5c6d7e8f9a0b1c2d3e4f5a6b7c8",
		Snapshot: func() (EngineStat, []ClientRow) {
			st := EngineStat{
				State: "ready", Depth: 20, Nodes: 12_400_000, NPS: 1_800_000,
				Hashfull: 128, HashMB: 512, Threads: 8, MultiPV: 3, TargetDepth: 24,
			}
			// 两台设备展示卡片列表两种形态：已鉴权（玉点 + FEN 摘要）与
			// 未鉴权（琥珀点 + 名字降级灰）
			rows := []ClientRow{{
				Session: "3f2a…c9", FullID: "3f2a1b8c9d0e1f2a3b4c5d6e7f8a9b0c",
				Client: "Android · 内阁", Ver: "v1", Authed: true,
				RTTMs: 12, Path: "LAN",
				Fen:     "r1ba1/9/1c2c4/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR b - - 0 1",
				LastPos: now.Add(-30 * time.Second),
			}, {
				Session: "88cc…01", FullID: "88cc41d0e1f2a3b4c5d6e7f8a9b0c3f2",
				Client: "Android · 内阁", Ver: "v1", Authed: false,
				RTTMs: -1, Path: "LAN",
				LastPos: now.Add(-2 * time.Second),
			}}
			return st, rows
		},
		Books: func() []BookRow {
			return []BookRow{{Name: "桔库", Entries: 12345, ModTime: now.Add(-72 * time.Hour)}}
		},
		Recent: func(n int) []SugRow {
			cp80, cp12 := 80, -12
			return []SugRow{
				{TS: now.Add(-90 * time.Second), Source: "book:桔库", FEN: "rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR w", BestMove: "h2e2", Depth: 0},
				{TS: now.Add(-40 * time.Second), Source: "engine", FEN: "rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR w", BestMove: "b2e2", ScoreCP: &cp80, Depth: 20},
				{TS: now.Add(-12 * time.Second), Source: "engine", FEN: "rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR b", BestMove: "h9g7", ScoreCP: &cp12, Depth: 22},
			}
		},
		CountToday: func() int { return 3 },
		TunerGet: func() TuneState {
			return TuneState{Depth: 20, HashMB: 512, Threads: 8, MultiPV: 3, Ready: true}
		},
		TunerSet:       func(kind string, v int) string { return "" },
		TunerClearHash: func() string { return "" },
	}
}

// TestConsoleScreenshot 渲染折叠态（宽度 960、高度按内容实测自适应）到
// docs/console-screenshot.png。
func TestConsoleScreenshot(t *testing.T) {
	if os.Getenv("CABINET_SHOT") == "" {
		t.Skip("设置 CABINET_SHOT=1 才生成截图（避免日常测试写仓库文件）")
	}
	cv, c := newShotConsoleAll()
	captureConsole(t, cv, fyne.NewSize(winSizeFolded.Width, c.foldedHeight()),
		filepath.Join("..", "..", "docs", "console-screenshot.png"))
}

// TestConsoleScreenshotExpanded 渲染展开态（建议流面板，对应设计稿 07）到
// docs/console-screenshot-expanded.png——折叠/展开两态都作为"交付即设计"
// 的对照基准。展开高度用 expandedHeight 同款计算（截图 Console 的 win 为 nil，
// 不能走 toggleAdvanced——里面会 Resize 真窗口）。
func TestConsoleScreenshotExpanded(t *testing.T) {
	if os.Getenv("CABINET_SHOT") == "" {
		t.Skip("设置 CABINET_SHOT=1 才生成截图（避免日常测试写仓库文件）")
	}
	cv, c := newShotConsoleAll()
	c.advStack.Add(c.advPanels["suggestions"])
	c.advCurrent = "suggestions"
	captureConsole(t, cv, c.expandedHeight(), filepath.Join("..", "..", "docs", "console-screenshot-expanded.png"))
}

// newShotConsole 构造仅渲染折叠态的截图画布。
func newShotConsole(t *testing.T) software.WindowlessCanvas {
	t.Helper()
	cv, _ := newShotConsoleAll()
	return cv
}

// newShotConsoleAll 构造截图 Console 与画布（test.NewApp + 棋韵主题 + 演示数据）。
func newShotConsoleAll() (software.WindowlessCanvas, *Console) {
	a := test.NewApp()
	a.Settings().SetTheme(newQiTheme())

	c := &Console{
		opts:        demoOptions(),
		engCells:    map[string]func(string){},
		engSufCells: map[string]func(string){},
	}
	content := c.buildContent()
	c.refresh()

	cv := software.NewCanvas()
	cv.SetContent(content)
	return cv, c
}

// captureConsole 按窗口尺寸渲染 Console 内容树并写出 PNG（尺寸不符即失败）。
func captureConsole(t *testing.T, cv software.WindowlessCanvas, size fyne.Size, out string) {
	t.Helper()
	cv.Resize(size)
	img := cv.Capture()

	// 内容总高诊断（人工核对是否溢出；溢出时应压缩密度而非加滚动条）
	if sc, ok := cv.Content().(*container.Scroll); ok {
		if root, ok := sc.Content.(*fyne.Container); ok {
			t.Logf("内容总高 %.1f（窗口 %.0f×%.0f）", root.MinSize().Height, size.Width, size.Height)
		}
	}

	f, err := os.Create(out)
	if err != nil {
		t.Fatalf("创建 %s 失败: %v", out, err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatalf("编码 PNG 失败: %v", err)
	}
	b := img.Bounds()
	t.Logf("已写入 %s（%dx%d）", out, b.Dx(), b.Dy())
	if b.Dx() != int(size.Width) || b.Dy() != int(size.Height) {
		t.Fatalf("截图尺寸与窗口不一致：%v vs %v", b, size)
	}
}
