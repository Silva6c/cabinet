// Package console 是内阁主控制台窗口（fyne/v2）：
// 与托盘共存的 GUI 面板：基础区（引擎实时/连接）常显，
// 高级面板（引擎调参/建议流/开局库/日志）经按钮排展开（一次一个），
// 1.5s 自动刷新。取数全部经 main 注入的只读快照回调，不阻塞协议主流程。
//
// 线程模型：fyne 要求 app.Run 占主 goroutine；跨 goroutine 的 UI 更新
// 一律走 fyne.Do（v2.6+ 的线程安全投递）。Show/Quit 供托盘侧调用。
package console

import (
	_ "embed"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"sync/atomic"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"cabinet/internal/update"
)

// openDir 资源管理器打开目录（目录不存在先创建；失败仅影响体验）。
// 放在 console 包内避免每个 UI 侧重复实现。
func openDir(dir string) {
	_ = os.MkdirAll(dir, 0o755)
	_ = exec.Command("explorer", dir).Start()
}

//go:embed cabinet.png
var embeddedIcon []byte

// Icon 内嵌的窗口/任务栏图标（PNG；主程序无需依赖磁盘路径）。
func Icon() fyne.Resource {
	return fyne.NewStaticResource("cabinet.png", embeddedIcon)
}

// ---------- 快照行结构（中立展示模型，main 负责从各模块适配） ----------

// EngineStat 引擎实时区一行快照。
type EngineStat struct {
	State       string
	Depth       int
	Nodes       int64
	NPS         int64
	Hashfull    int // 千分比 0-1000
	HashMB      int // 配置值；0 = 引擎自定
	Threads     int // 配置值；0 = 自动
	MultiPV     int
	TargetDepth int
}

// ClientRow 连接区一行快照。
type ClientRow struct {
	Session string // 已缩略的会话 ID
	FullID  string // 完整会话 ID（断开会话用，不显示）
	Devid   string // hello 自报设备稳定标识（空 = 旧端未上报）
	Name    string // hello 自报设备昵称（空 = 旧端未上报）
	Client  string
	Ver     string
	Authed  bool
	RTTMs   int64 // -1 = 尚未测得
	Path    string
	LastPos time.Time
	Fen     string // 最近 position 的 FEN（"最后 position"列摘要显示；空 = 未发过）
}

// BookRow 库区一行快照。
type BookRow struct {
	Name    string
	Entries int
	ModTime time.Time
}

// SugRow 建议流一行快照。
type SugRow struct {
	TS        time.Time
	Source    string
	FEN       string   // 产生建议的局面（沉淀库已存，透传展示；旧记录可能为空）
	BestMove  string   // 主选着法（UCCI）
	PV        []string // 变招序列（UCCI；桔库命中无 PV）
	Depth     int
	ScoreCP   *int
	ScoreMate *int
}

// TuneState 引擎可调参数当前目标值（协议 §4.10；0 = 自动档）。
type TuneState struct {
	Depth   int
	HashMB  int // 0 = 自动
	Threads int // 0 = 自动
	MultiPV int
	Ready   bool // 引擎是否就绪（未就绪时调参控件禁用）
}

// Options 控制台配置与数据源（均为只读回调，刷新时调用）。
type Options struct {
	Version string
	Icon    fyne.Resource // 任务栏/窗口图标；nil 用内嵌 cabinet.png
	LogDir  string        // "打开日志目录"目标
	LogFile string        // 日志尾部 tail 的文件路径
	// ConnectToken / BindAddr / CertFingerprint 连接二维码用：
	// 配对令牌、WSS 监听地址（如 ":9527"）与证书 SHA-256 指纹（空 = 不锁定，手机端不校验）
	ConnectToken    string
	BindAddr        string
	CertFingerprint string
	// KickSession 手动断开指定会话（"断开会话"按钮；nil = 按钮置灰并提示不可用）
	KickSession func(sessionID string) bool
	Snapshot    func() (EngineStat, []ClientRow) // 引擎实时区 + 连接区
	Books       func() []BookRow                 // 库区
	Recent      func(n int) []SugRow             // 建议流（最近 n 条）
	// CountToday 今日沉淀条数（建议流标题行"今日 N 条"）；nil = 显示 0
	CountToday func() int
	// ClearSuggestions 清空全部沉淀建议（"清空沉淀"按钮；nil = 按钮报错提示）
	ClearSuggestions func() error
	// TunerGet 当前算力参数（调参区回显；nil = 引擎桥不可调，调参区隐藏）
	TunerGet func() TuneState
	// TunerSet 调节参数，kind 取 "depth"/"hash"/"threads"/"multipv"，v 语义与
	// 协议 §4.10 一致（threads/hash 的 0 = 自动档）。返回错误消息（空串 = 成功）。
	TunerSet func(kind string, v int) string
	// TunerClearHash 清空置换表（返回错误消息，空串 = 已受理）
	TunerClearHash func() string
	// FirewallStatus 防火墙告警查询：非空 = 横幅告警文案（TCP 端口被防火墙
	// 静默拦截）；空串 = 不告警。nil = 功能不可用（横幅不出现）。
	// 刷新循环 1.5s 调一次——实现侧必须读内存缓存，禁止每次拉 netsh 子进程。
	FirewallStatus func() string
	// FirewallAllow 一键放行（UAC 提权加防火墙规则），返回错误消息（空 = 已受理；
	// 放行结果异步，状态经后续刷新周期体现）
	FirewallAllow func() string
	// FirewallDismiss 用户点"不再提示"（持久化去重在实现侧）
	FirewallDismiss func()
	// ── 检查更新面板（UpdateSnapshot==nil = 面板整体缺席，按钮自动隐藏）──
	// UpdateSnapshot 更新状态快照：刷新循环 1.5s 调一次——实现侧必须读内存，
	// HTTP 一律在实现侧 goroutine（同 FirewallStatus 禁令）。
	UpdateSnapshot func() update.Snapshot
	// UpdateCheck 发起一次检查（实现侧异步），返回提示串（空 = 已受理）
	UpdateCheck func() string
	// UpdateDownloadInstall 发起"下载并安装"（调用即用户已确认；实现侧异步：
	// 下载→校验→解出 .new→自动重启），返回提示串（空 = 已受理）
	UpdateDownloadInstall func() string
	// UpdateCancel 取消进行中的下载
	UpdateCancel func()
	// UpdateAutoCheckGet 启动自检开关当前值（面板构建时读取一次；默认关）
	UpdateAutoCheckGet func() bool
	// UpdateAutoCheckSet 修改启动自检开关（实现侧持久化 console-state.json），
	// 返回错误消息（空串 = 成功）
	UpdateAutoCheckSet func(on bool) string
}

// console 单例：Run 后由托盘侧经 Show/Quit 访问。
var inst *Console

// Console 主窗口实例。
type Console struct {
	opts Options
	app  fyne.App
	win  fyne.Window

	running atomic.Bool

	// 引擎区单元格：普通 kv 格与大数字格统一为 setter 映射（刷新时按 key 回填）；
	// 大数字格另带后缀小字 setter（"/24"、"n/s"）
	engCells    map[string]func(string)
	engSufCells map[string]func(string)
	// 连接区（v4 矮便当盒 2026-10-20：表头恒显+横排六列卡片行；widget.Table
	// 与两版卡片形态均退役——点行/点空白取消选中的交互保留）
	clientRowsBox  *fyne.Container // 数据行列表容器（rebuildClientRows/syncClientRows 维护）
	clientListSetH func(float32)   // 行列表可视区调高（行数少随内容、100px 封顶内部滚动）
	clientRows     []ClientRow
	connCountSet   func(string)  // "N 台"（canvas.Text setter）
	fpChip         *fpChip       // 证书指纹分组短式（只读核对；完整值生成二维码用）
	kickBtn        *hiliteButton // 断开选中会话（选中行后可用；glow 外发光主按钮）
	selectedID     string        // 当前选中行的完整会话 ID
	// 建议流
	sugTable *widget.Table
	sugRows  []SugRow
	sugCount *widget.Label
	// 库区 / 日志区
	bookLabel *widget.Label
	logLabel  *widget.Label // 等宽字体日志尾部
	logScroll *container.Scroll

	// 调参区（引擎桥可调时才有）
	tunerSelects map[string]*widget.Select // kind → 下拉框
	tunerBusy    atomic.Bool               // 刷新回写 Selected 时抑制 OnChanged 回环

	// 高级面板折叠：基础区（引擎/连接）常显，其余经按钮展开（一次一个）
	advStack    *fyne.Container              // 当前展开面板的容器（空 = 全部收起）
	advBtns     map[string]*widget.Button    // 高级面板切换按钮（name → 按钮）
	advPanels   map[string]fyne.CanvasObject // 面板内容（提前构建；不可用面板缺省）
	advCurrent  string                       // 当前展开的面板名（空 = 收起）
	contentRoot fyne.CanvasObject            // 根内容 vbox（buildContent 建好即存；
	// foldedHeight 靠它实测内容最小高——窗口高自适应内容的依据）

	// 头部状态 chip（引擎/库/连接，与手机端同一套状态语言）
	chipEngine *statusChip
	chipBook   *statusChip
	chipConn   *statusChip

	// 防火墙告警横幅（头部与 bento 之间；仅告警时显示，见 firewallBanner）
	firewallBox  *fyne.Container
	firewallText *canvas.Text
	firewallBusy atomic.Bool // 一键放行进行中（防重复点击）

	// 窗口高度过渡（方向二：面板展开/收起 150ms 缓动；连续点击 Stop 防抖）
	sizeAnim    *fyne.Animation
	lastWinSize fyne.Size // 上一次目标尺寸（动画插值起点）

	// 开局库 tile（折叠态常显：收录局面大数字 + 今日命中率 + 明细；均为 canvas.Text setter）
	bookHeadSet  func(string)  // tile 头部右侧（库名 · 热加载）
	bookTotalSet func(string)  // 收录局面大数字
	bookRateSet  func(string)  // 今日命中率数值（"38%"；无样本 "—"）
	bookSubSet   func(string)  // 最近更新
	bookBarSet   func(float32) // 命中率细条取值（0–1）

	// 检查更新面板（main 接线后才有；控件在 sectionUpdate 构建）
	updateText      *widget.Label       // 状态结果行（文案由 Checker 生成）
	updateBtn       *hiliteButton       // 立即检查
	updateDlBtn     *hiliteButton       // 下载并安装（确认式；仅可安装时显示）
	updateCancelBtn *hiliteButton       // 取消下载（仅下载中显示）
	updateBar       *widget.ProgressBar // 下载进度（仅下载中显示）
	updateAutoChk   *widget.Check       // 启动自检开关
}

// 窗口默认尺寸（2026-10-18 实测重校"调到刚好"）：
//   - 宽 960 固定：建议流 7 列合计 920（90+130+190+100+250+90+70）+ 左右呼吸
//     边距与竖向滚动条占位（旧值 900 连最右"深度"列都在可视区外）
//   - 高不再用固定值：foldedHeight 按根内容实测最小高自适应（旧固定 570 比内容
//     高 ~65px，底部留出一条空白"下巴"——用户实测反馈后改掉）
var (
	winSizeFolded = fyne.NewSize(960, 570) // 高仅作 contentRoot 未就绪时的兜底
	// rootGap 根布局纵向间距（buildContent 的 vbox 与 cols2 共用视觉语言；
	// expandedHeight/foldedHeight 计算时引用同一常量，防两处魔数静默失配）
	rootGap float32 = 12
)

// foldedHeight 折叠态窗口高 = 根内容实测最小高 + 一个 rootGap 呼吸。内容增高
// （将来加区块）时窗口自动跟上，矮时刚好包住——底部不再有固定余量空白。
func (c *Console) foldedHeight() float32 {
	if c.contentRoot == nil {
		return winSizeFolded.Height
	}
	return float32(math.Ceil(float64(c.contentRoot.MinSize().Height))) + rootGap
}

// Run 构建并显示主窗口，阻塞运行 fyne 消息循环直至 Quit。
// 必须在主 goroutine 调用（fyne/GLF 平台约束）。
func Run(opts Options) {
	if opts.Icon == nil {
		opts.Icon = Icon()
	}
	c := &Console{opts: opts, engCells: map[string]func(string){}, engSufCells: map[string]func(string){}}
	inst = c

	c.app = app.New()
	// 深色棋韵主题（悬浮窗同源的深色暖调；在创建窗口前设置）
	c.app.Settings().SetTheme(newQiTheme())
	c.app.SetIcon(opts.Icon)
	c.win = c.app.NewWindow(fmt.Sprintf("内阁 cabinet %s", opts.Version))
	c.build()

	// 折叠态窗口：高度按内容实测自适应（foldedHeight），基础区常显无余量空白
	c.win.Resize(fyne.NewSize(winSizeFolded.Width, c.foldedHeight()))
	c.lastWinSize = fyne.NewSize(winSizeFolded.Width, c.foldedHeight())
	// 点 × 隐藏回托盘，不退出（退出只能走托盘菜单）
	c.win.SetCloseIntercept(func() { c.win.Hide() })
	c.win.Show() // 服务启动时窗口即显示
	// 标题栏随应用主题暗色（不依赖系统浅/深模式）。必须在 Show 之后：fyne 建窗
	// 时 GL viewport 尚未创建，RunNative 回调拿到 HWND=0 静默空过（雷区实测见
	// console_windows.go——这也是 vc94 后白条带复现的根因）
	applyDarkTitleBar(c.win)

	c.running.Store(true)
	c.refresh() // 首次立即填充，不停留空面板
	go c.refreshLoop()
	c.app.Run() // 阻塞直至 Quit
	c.running.Store(false)
}

// Show 显示并聚焦主窗口（托盘左键/双击/"打开面板"菜单；跨 goroutine 安全）。
func Show() {
	c := inst
	if c == nil || !c.running.Load() {
		return
	}
	fyne.Do(func() {
		c.win.Show()
		c.win.RequestFocus()
	})
}

// Quit 退出 fyne 消息循环（服务收尾联动；跨 goroutine 安全，未运行时空操作）。
func Quit() {
	c := inst
	if c == nil || !c.running.Load() {
		return
	}
	fyne.Do(func() { c.app.Quit() })
}

// ShowQR 显示并聚焦主窗口、弹出连接二维码（托盘"连接二维码"菜单；
// 跨 goroutine 安全，未运行时空操作）。
func ShowQR() {
	c := inst
	if c == nil || !c.running.Load() {
		return
	}
	fyne.Do(func() {
		c.win.Show()
		c.win.RequestFocus()
		c.showQRDialog()
	})
}

// refreshLoop 定时刷新（1.5s）；取数回调均为只读内存操作，不阻塞 UI。
// ticker 跑在后台 goroutine，直接调 refresh 会违反 fyne 单线程 UI 约束；
// fyne.Do 把 refresh 投递到 fyne 主 goroutine 串行执行（v2.6+ 唯一安全的
// 跨 goroutine UI 入口），refresh 及其内部全部 setter 因此无需再加锁。
// 1.5s：状态变化肉眼可感知与空转开销的折中。
func (c *Console) refreshLoop() {
	t := time.NewTicker(1500 * time.Millisecond)
	defer t.Stop()
	for range t.C {
		if !c.running.Load() {
			return
		}
		fyne.Do(c.refresh)
	}
}

// build 组装各分区（便当盒布局，棋韵 §5.3）：
// 头部（图标/标题/三枚状态 chip）→ 两列 bento（01 引擎 tile / 02 开局库 tile）
// → 03 连接 tile（全宽）→ 04 高级功能按钮排 → 展开面板。整体放进滚动容器。
func (c *Console) build() {
	c.win.SetContent(c.buildContent())
}

// buildContent 组装根内容（窗口与截图测试共用；测试用 software 画布渲染同一棵树）
func (c *Console) buildContent() fyne.CanvasObject {
	c.tunerSelects = map[string]*widget.Select{}
	c.advBtns = map[string]*widget.Button{}
	c.advPanels = map[string]fyne.CanvasObject{}
	c.advStack = container.NewStack()
	root := container.New(&vbox{gap: rootGap},
		c.headerBar(),
		c.firewallBanner(),
		container.New(&cols2{gap: rootGap}, c.sectionEngine(), c.tileBooks()),
		c.sectionClients(),
		c.sectionAdvancedBar(),
		c.advStack,
	)
	// 根 vbox 引用存起来：foldedHeight 据此实测内容最小高（窗口高自适应，
	// 底部不再留余量空白——2026-10-18 用户反馈"高级功能下面有条下巴"）
	c.contentRoot = root
	return container.NewScroll(root)
}

// firewallBanner 防火墙告警横幅（头部与 bento 之间）：仅当 opts.FirewallStatus
// 返回非空文案时显示。场景是 Windows 首次"允许访问"弹窗被用户点取消后，
// 防火墙静默拦截手机入站连接——监听侧毫无感知，横幅给出可见出口。
// 淡红底 + 警示红文案 + 两个动作按钮；默认 Hide，由 refreshFirewall 控制。
func (c *Console) firewallBanner() fyne.CanvasObject {
	c.firewallText = qiText("", qiSize(theme.SizeNameText), qiDanger, false, false)
	allow := newGlowButton("一键放行", c.onFirewallAllow)
	allow.Importance = widget.HighImportance
	dismiss := widget.NewButton("不再提示", c.onFirewallDismiss)
	dismiss.Importance = widget.LowImportance
	bg := canvas.NewRectangle(qiDangerBanner)
	bg.CornerRadius = 8
	bg.StrokeColor = qiDangerSoft
	bg.StrokeWidth = 1
	inner := container.NewStack(bg, container.NewHBox(
		container.New(&pad{left: 2, right: 10}, c.firewallText), allow, dismiss))
	c.firewallBox = container.NewStack(container.New(&pad{top: 2, right: 4, bottom: 2, left: 4}, inner))
	c.firewallBox.Hide()
	return c.firewallBox
}

// refreshFirewall 每个刷新周期同步横幅可见性与文案（FirewallStatus 必须是
// 读内存缓存的轻调用——实现侧 netsh 只查一次 + 放行后复查，见 firewallWatch）。
func (c *Console) refreshFirewall() {
	if c.firewallBox == nil || c.firewallText == nil || c.opts.FirewallStatus == nil {
		return
	}
	msg := c.opts.FirewallStatus()
	switch {
	case msg == "":
		if !c.firewallBox.Hidden {
			c.firewallBox.Hide()
		}
	case c.firewallText.Text != msg || c.firewallBox.Hidden:
		c.firewallText.Text = msg
		c.firewallText.Refresh()
		c.firewallBox.Show()
	}
}

// onFirewallAllow 一键放行：UAC 提权由实现侧完成（本层只反馈结果）。
// 放行是异步弹窗，成功反馈只说明 UAC 已拉起，告警消失以复查结果为准。
func (c *Console) onFirewallAllow() {
	if c.opts.FirewallAllow == nil || !c.firewallBusy.CompareAndSwap(false, true) {
		return
	}
	defer c.firewallBusy.Store(false)
	if msg := c.opts.FirewallAllow(); msg != "" {
		dialog.ShowError(errors.New(msg), c.win)
		return
	}
	dialog.ShowInformation("已发起放行",
		"请在弹出的 UAC 确认框选择“是”。\n放行生效后本告警会自动消失。", c.win)
	c.refreshFirewall()
}

// onFirewallDismiss 用户点"不再提示"：持久化去重在实现侧（按端口记忆，
// 规则被删除后下次启动仍会重新告警——静默拦截场景始终有出口）。
func (c *Console) onFirewallDismiss() {
	if c.opts.FirewallDismiss != nil {
		c.opts.FirewallDismiss()
	}
	c.firewallBox.Hide()
}

// headerBar 顶部头：图标（炭面圆角小方衬托，裸图无落点）＋ 微标/标题 ＋ 右侧
// 三枚状态 chip ＋ 底部 1dp 细线（设计稿 §5.1 头部细线，折叠/展开恒显的骨架线）
func (c *Console) headerBar() fyne.CanvasObject {
	icon := canvas.NewImageFromResource(c.opts.Icon)
	icon.FillMode = canvas.ImageFillContain
	iconBg := canvas.NewRectangle(qiSurface)
	iconBg.CornerRadius = 8
	micro := qiText("内阁 · CABINET", qiSize(theme.SizeNameCaptionText), qiMuted, false, false)
	title := qiText("内阁 · 控制台", 22, qiForeground, false, true)
	left := container.NewHBox(
		container.NewStack(iconBg, container.New(padAll(4), container.New(sizedBox{w: 26, h: 26}, icon))),
		container.NewVBox(micro, title),
	)
	c.chipEngine = newStatusChip()
	c.chipBook = newStatusChip()
	c.chipConn = newStatusChip()
	right := container.NewHBox(
		centerV(c.chipEngine.object()), centerV(c.chipBook.object()), centerV(c.chipConn.object()),
	)
	// 底部细线：fixedHeight 锁 1dp（Rectangle MinSize 为 0，需容器给高度）
	return container.NewBorder(nil, container.New(&fixedHeight{h: 1}, canvas.NewRectangle(qiLine1)), left, right)
}

// toggleAdvanced 复用预建面板对象：RemoveAll 后重新 Add 同一 CanvasObject，
// 在 fyne 单父约束下合法；面板内部状态（表格选中、日志滚动位置）因此跨
// 开关保留。勿改成每次 toggle 重建——丢状态，且按钮高亮/nil 过滤需重做
// （advPanels 在 sectionAdvancedBar 预构建正是为此）。
//
// toggleAdvanced 展开/收起高级面板：已展开同一面板则收起；否则切换到新面板。
// 同步窗口两档尺寸并高亮当前按钮。
func (c *Console) toggleAdvanced(name string) {
	if c.advCurrent == name {
		c.advCurrent = ""
		c.advStack.RemoveAll()
	} else {
		c.advCurrent = name
		c.advStack.RemoveAll()
		c.advStack.Add(c.advPanels[name])
	}
	for n, b := range c.advBtns {
		if n == c.advCurrent {
			b.Importance = widget.HighImportance
		} else {
			b.Importance = widget.MediumImportance
		}
		b.Refresh()
	}
	if c.advCurrent != "" {
		c.animateWinSize(c.expandedHeight())
		c.refresh() // 新面板立即填充（含日志滚底），不等下一个刷新周期
	} else {
		c.animateWinSize(fyne.NewSize(winSizeFolded.Width, c.foldedHeight()))
	}
}

// animateWinSize 窗口尺寸 150ms 缓动过渡（方向二精选：面板展开/收起不再瞬变）。
// 连续点击时 Stop 上一段防抖；ShowAnimations 关闭（无障碍）直切。
func (c *Console) animateWinSize(to fyne.Size) {
	if c.sizeAnim != nil {
		c.sizeAnim.Stop()
		c.sizeAnim = nil
	}
	from := c.lastWinSize
	c.lastWinSize = to
	if !c.app.Settings().ShowAnimations() {
		c.win.Resize(to)
		return
	}
	c.sizeAnim = fyne.NewAnimation(150*time.Millisecond, func(t float32) {
		c.win.Resize(fyne.NewSize(
			from.Width+(to.Width-from.Width)*t,
			from.Height+(to.Height-from.Height)*t,
		))
	})
	c.sizeAnim.Curve = fyne.AnimationEaseOut
	c.sizeAnim.Start()
}

// expandedHeight 展开态窗口 = 根内容实测最小高 + 一个 rootGap 呼吸。
// ⚠展开面板挂进 advStack 后 contentRoot 的 MinSize 已自然包含面板高度
// （2026-10-18 修复：此前用动态 foldedHeight 当基准再加一遍面板——而 folded
// 在 Add 后同样已含面板，面板被算两次，窗口多出一大截空白下巴）。
// 内容或面板增高时窗口自动跟上，"展开被裁一块要手动拉"的复发路径就此断掉。
// 高度向上取整：窗口必须是整数像素，否则截图/渲染边界出现 1px 漂移。
func (c *Console) expandedHeight() fyne.Size {
	h := winSizeFolded.Height
	if c.contentRoot != nil {
		h = float32(math.Ceil(float64(c.contentRoot.MinSize().Height))) + rootGap
	}
	return fyne.NewSize(winSizeFolded.Width, h)
}

// sectionTitle 分区标题（加粗；extra 放标题行右侧的补充控件）。
func sectionTitle(title string, extra ...fyne.CanvasObject) fyne.CanvasObject {
	t := &widget.Label{Text: title, TextStyle: fyne.TextStyle{Bold: true}}
	if len(extra) == 0 {
		return t
	}
	return container.NewHBox(t, container.NewHBox(extra...))
}

// kvCell "标签+值"横向单元格（引擎 tile 明细行，设计稿 §06 kvrow）：
// 标签 11pt 灰居左，值 13pt 粗居右同行。值含中文（"线程 8 · 变招 3"），
// 禁用等宽——mono 族的 CJK 回退与 sans 异形（见 statBig 雷区注释）。
func kvCell(key string) (fyne.CanvasObject, func(string)) {
	val, set := qiDynamic("—", qiSize(theme.SizeNameText), qiForeground, false, true)
	return container.NewHBox(qiCaption(key), val), set
}
