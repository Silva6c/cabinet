// Package tray 是 Windows 托盘：图标 + 动态状态行 + 打开日志目录 + 退出。
// fyne.io/systray 的消息循环须独占一个 OS 线程——main 让 fyne 主控制台占
// 主 goroutine，本包 Run 放在锁 OS 线程的独立 goroutine 里跑（见 Run 注释）。
package tray

import (
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"time"

	"fyne.io/systray"
)

//go:embed cabinet.ico
var embeddedIcon []byte

// started 标记托盘消息循环已启动：Quit() 仅在此为真时才调 systray.Quit()
// （未 Run 时调 systray.Quit 行为未定义；-no-tray 模式下 main 误调 Quit 必须无害）。
var started atomic.Bool

// LoadIcon 托盘图标：dir 下 assets/icon/cabinet.ico 优先（可选覆盖，
// 便于换图标不发版；dir 传 exe 所在目录即与 CWD 无关，assets 属程序资源、
// 不进便携包 appdata\），缺失/读取失败时用内嵌资源兜底——图标永不丢。
// dir 为空串时按 CWD 相对解析（旧调用兼容）。
func LoadIcon(dir string) []byte {
	if b, err := os.ReadFile(filepath.Join(dir, "assets", "icon", "cabinet.ico")); err == nil && len(b) > 0 {
		return b
	}
	return embeddedIcon
}

// Quit 幂等退出托盘消息循环（让 tray.Run 返回）。
// 消息循环未启动（-no-tray）时是无害空操作。
// 服务异常退出路径经此收敛；托盘菜单退出在消息循环内直接调 systray.Quit
// （循环必已启动），不经本函数。
func Quit() {
	if started.Load() {
		systray.Quit()
	}
}

// Options 托盘配置。
type Options struct {
	Icon    []byte        // .ico 内容（用 LoadIcon 获取）
	Version string        // 显示版本（tooltip 与状态行首行统一 "内阁 cabinet vX.Y.Z"）
	LogDir  string        // "打开日志目录"指向的绝对路径
	Status  func() string // 动态状态行（引擎/库/连接数；nil 则固定"运行中"）
	OnReady func()        // 托盘启动成功回调（打自检日志）
	OnExit  func()        // 点退出：先回调（取消 ctx），再退出消息循环
	// OnActivate 左键点击/双击托盘图标（显示并聚焦主控制台；nil 无操作）
	OnActivate func()
	// OnShowQR "连接二维码"菜单（弹出扫码配对窗口；nil 隐藏该菜单项）
	OnShowQR func()
}

// Run 阻塞运行托盘直至退出（systray 内部消息循环）。
// 注意：与 fyne 主窗口共存时，main 把本函数放在锁 OS 线程的独立 goroutine 里跑
// （fyne 的 app.Run 占主 goroutine；Windows 的 win32 消息循环可跑在任意线程，
// 但创建窗口与 GetMessage 必须同线程，故 LockOSThread）。
func Run(opts Options) {
	// stop 收编全部消费 goroutine（2s ticker + 四个菜单项 ClickedCh 循环）：
	// systray.Quit 不关闭 ClickedCh（库源码仅 item.Remove() 会 close），
	// Run 返回时统一 close，goroutine 全量退出——修复历史"靠进程退出兜底"的
	// 按设计泄漏（2026-10-27 现代软件规范轮）。
	stop := make(chan struct{})
	defer close(stop)
	systray.Run(func() {
		systray.SetIcon(opts.Icon)
		systray.SetTooltip(fmt.Sprintf("内阁 cabinet %s", opts.Version))
		// 左键单击/双击 = 唤出主控制台（Windows 上 tapped 同时覆盖单击与双击）
		if opts.OnActivate != nil {
			systray.SetOnTapped(opts.OnActivate)
		}

		// 状态区：首行固定名称版本（与 tooltip 统一），次行动态指标
		mTitle := systray.AddMenuItem(fmt.Sprintf("内阁 cabinet %s", opts.Version), "")
		mTitle.Disable()
		mStatus := systray.AddMenuItem("启动中…", "")
		mStatus.Disable()
		refresh := func() {
			if opts.Status == nil {
				mStatus.SetTitle("运行中")
				return
			}
			// Status 取数（EngineState/Clients 均为内存读）；防一层 panic 拖垮托盘
			func() {
				defer func() { _ = recover() }()
				mStatus.SetTitle(opts.Status())
			}()
		}
		refresh() // 首次立即刷新，不停留"启动中…"
		// 信息区（标题/状态，均禁用态）与操作区之间分隔——原生菜单无分组样式，
		// 分隔线是唯一可用的视觉层级手段
		systray.AddSeparator()
		// 生命周期：全部消费 goroutine 经 stop 收编（见 Run 开头注释），
		// tray.Run 返回（defer close(stop)）后零残留
		go func() {
			t := time.NewTicker(2 * time.Second)
			defer t.Stop()
			for {
				select {
				case <-t.C:
					refresh()
				case <-stop:
					return
				}
			}
		}()

		mPanel := systray.AddMenuItem("打开面板", "显示主控制台窗口")
		mLog := systray.AddMenuItem("打开日志目录", "定位 logs 目录")
		var mQR *systray.MenuItem
		if opts.OnShowQR != nil {
			mQR = systray.AddMenuItem("连接二维码", "扫码把连接参数填入手机端")
		}
		systray.AddSeparator()
		mQuit := systray.AddMenuItem("退出", "优雅停止服务")
		go func() {
			for {
				select {
				case <-mPanel.ClickedCh:
					if opts.OnActivate != nil {
						opts.OnActivate()
					}
				case <-stop:
					return
				}
			}
		}()
		if mQR != nil {
			go func() {
				for {
					select {
					case <-mQR.ClickedCh:
						opts.OnShowQR()
					case <-stop:
						return
					}
				}
			}()
		}
		go func() {
			for {
				select {
				case <-mLog.ClickedCh:
					// 目录不存在先创建，避免 explorer 弹错
					_ = os.MkdirAll(opts.LogDir, 0o755)
					// explorer 打开目录（失败仅影响体验，不影响服务）
					_ = exec.Command("explorer", opts.LogDir).Start()
				case <-stop:
					return
				}
			}
		}()
		go func() {
			for {
				select {
				case <-mQuit.ClickedCh:
					if opts.OnExit != nil {
						opts.OnExit() // 取消 ctx → 全链优雅关闭
					}
					started.Store(true)
					systray.Quit() // 必须退出消息循环，否则进程变僵尸
				case <-stop:
					return
				}
			}
		}()

		started.Store(true)
		if opts.OnReady != nil {
			opts.OnReady()
		}
	}, nil)
}
