// qr.go 连接二维码：把 WSS 地址 + 配对令牌 + 证书指纹编码为 cabinet://connect 链接并
// 以二维码展示，手机端"扫一扫"自动填入连接参数（真机联调免手输 64 位令牌）。
//
// 二维码内容（手机端 Manifest 的 intent-filter 按此解析）：
//
//	cabinet://connect?u=<url 编码的 wss://IP:端口/ws>&k=<配对令牌>&f=<证书指纹 hex>
//
// f 为可选参数（PC 证书的 SHA-256 指纹，64 位小写 hex）：手机端写入后 TLS 握手
// 锁定该指纹，不匹配即断开。令牌明文出现在二维码里：仅限本机屏幕展示，不要截图外传。
package console

import (
	"fmt"
	"image/color"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/driver"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
	"github.com/skip2/go-qrcode"
)

// BuildConnectURL 生成二维码 payload。host 为局域网 IPv4，port 为 WSS 端口；
// fingerprint 非空时附加 f 参数（证书 SHA-256 指纹 hex），为空省略（手机端不锁定）。
//
// 跨端契约：手机端 AssistActivity.parseConnectText 按 key 取参（Uri.parse 后
// getQueryParameter("u"/"k"/"f")），顺序不敏感但固定 u→k→f 便于人工核对。
// u 中的 ":" "/" 必须 query 转义否则解析错位；k/f 虽是 hex 明文仍统一
// QueryEscape——防御 Android getQueryParameter 把 '+' 解码为空格的语义
// （未来换令牌字符集时不再踩坑）。
func BuildConnectURL(host string, port int, token, fingerprint string) string {
	wss := fmt.Sprintf("wss://%s:%d/ws", host, port)
	s := fmt.Sprintf("cabinet://connect?u=%s&k=%s",
		url.QueryEscape(wss), url.QueryEscape(token))
	if fingerprint != "" {
		s += "&f=" + url.QueryEscape(fingerprint)
	}
	return s
}

// bindPort 从配置 Bind（如 ":9527"）解析端口；解析失败回退默认 9527。
// ⚠回退值 9527 必须与 config.go 默认 Bind（":9527"）、scripts/allow-9527.bat
// 的防火墙规则、internal/firewall 的规则名推导（cabinet-wss-<端口>）保持
// 一致——四处任一改动需同步，否则二维码指向的端口被防火墙拦截且无任何
// 报错（2026-10-17 起防火墙有告警横幅兜底，但仍以改对为准）。
func bindPort(bind string) int {
	_, port, err := net.SplitHostPort(bind)
	if err != nil {
		return 9527
	}
	n, err := strconv.Atoi(port)
	if err != nil || n <= 0 {
		return 9527
	}
	return n
}

// ifaceAddr 一个候选网卡地址：IP + 网卡名（下拉框展示，帮助用户识别虚拟网卡）。
type ifaceAddr struct {
	IP   string
	Name string
}

// localIPv4s 本机非环回网卡的 IPv4 地址。排序原则：
//  1. 真实物理网卡优先（网卡名含 WLAN/Wi-Fi/以太网/Ethernet 等）——
//     VMware VMnet/Docker/代理 TUN 等虚拟网卡手机根本连不上，不能排前面；
//  2. 同级按私网段（192.168 > 10. > 172.16–31 > 其他）。
func localIPv4s() []ifaceAddr {
	var out []ifaceAddr
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok || ipn.IP.To4() == nil {
				continue
			}
			out = append(out, ifaceAddr{IP: ipn.IP.String(), Name: ifc.Name})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		ri, rj := ifaceRank(out[i]), ifaceRank(out[j])
		if ri != rj {
			return ri < rj
		}
		return privRank(out[i].IP) < privRank(out[j].IP)
	})
	return out
}

// ifaceRank 网卡排序优先级：物理网卡名（WLAN/Wi-Fi/以太网/Ethernet/本地连接）
// 最优先；其余（VMware/WSL/Docker/TUN 等虚拟网卡）靠后。
func ifaceRank(a ifaceAddr) int {
	name := a.Name
	for _, key := range []string{"WLAN", "Wi-Fi", "WiFi", "以太网", "Ethernet", "本地连接"} {
		if strings.Contains(name, key) {
			return 0
		}
	}
	return 1
}

// privRank 私网段优先级（越小越优先）。
func privRank(ip string) int {
	b := net.ParseIP(ip).To4()
	if b == nil {
		return 4
	}
	switch {
	case b[0] == 192 && b[1] == 168:
		return 0
	case b[0] == 10:
		return 1
	case b[0] == 172 && b[1] >= 16 && b[1] <= 31:
		return 2
	default:
		return 3
	}
}

// 二维码弹窗尺寸（2026-11-17 扫码提速 + 2026-11-19 三次精修）：
// payload≈190B 在 ECC Medium 下是 QR V11（61×61 模块），原 300px 显示只有
// ~4.3px/模块——手机必须凑近 20-30cm 且正对屏幕才能解出（A/B 实测"扫码
// 特别费劲"的第一性原因）。420px ~6.0px/模块，普通对坐距离即可命中——
// 实测收益，三次形态重构均保留不缩。对话框 540×620（2026-11-19 vc73 验收
// "还是很挤"→整页导航：主页/子页共享同一分配区域，切换无尺寸跳变）。
const (
	qrImgPx     = 420
	qrDlgWidth  = 540
	qrDlgHeight = 620
)

// showQRDialog 连接二维码应用内对话框（2026-11-18 回归主窗内 Dialog；2026-11-19
// 三次精修：整页导航——主页/网卡页/令牌页三页互斥切换，子页不再绘制二维码，
// 用户方案原话"点开就不要绘制二维码了，直接大大方方展示内容，给个返回键"）：
//   - 页面栈：pageStack 叠三页，showPage 全 Hide 后 Show 目标页（fyne Stack
//     只绘制可见层；对话框 Resize 定死尺寸→可见页铺满分配区域，三页统一
//     尺寸无跳变）。每页 Border(底=按钮排, 中心=内容区)——按钮排恒在页底；
//   - 主页 = 一行 muted 副标 + 420px 白底朱砂描边码卡片 + 三按钮（网卡设置/
//     令牌详情/关闭）；网卡页 = 标题+下拉（选中即重绘主页二维码与配对地址）；
//     令牌页 = 地址/令牌/指纹三行各带复制按钮（app.Clipboard 写剪贴板，
//     "已复制✓" 2s 经 fyne.Do 恢复；无指纹时指纹复制禁用）；子页底部单按钮
//     「‹ 返回」回主页；
//   - 单例：qrDialog 非 nil 即已开着，托盘/面板重复点击一律无操作；
//   - 无按钮 Dialog（NewCustomWithoutButtons）：模态遮罩挡外部点击，关闭仅
//     走自绘按钮 → Dismiss → SetOnClosed；
//   - 扫码期间主窗临时置顶（topMostMainWindow，fyne RequestAlwaysOnTop 单向
//     API 无解除能力，走 HWND 原生 SetWindowPos）；
//   - 配对成功（认证会话数 0→1）由 refresh→pairingWatch 自动关窗并提示。
func (c *Console) showQRDialog() {
	if c.qrDialog != nil {
		return // 已开=无操作（单例拍板）
	}
	ifaces := localIPv4s()
	if len(ifaces) == 0 {
		dialog.NewInformation("连接二维码", "未找到可用的局域网 IPv4 地址，请检查网络连接。", c.win).Show()
		return
	}
	port := bindPort(c.opts.BindAddr)

	// 二维码白底卡片：canvas 白底 + 12 圆角 + 朱砂描边（棋韵卡片档）。
	// NewStack 的 min 尺寸取子项最大 = img 的 420px，Rectangle 无 min 自动铺满。
	qrCard := canvas.NewRectangle(color.NRGBA{R: 0xFF, G: 0xFF, B: 0xFF, A: 0xFF})
	qrCard.CornerRadius = 12
	qrCard.StrokeColor = qiZhuBrand
	qrCard.StrokeWidth = 1.5
	img := &canvas.Image{}
	img.SetMinSize(fyne.NewSize(qrImgPx, qrImgPx))
	img.FillMode = canvas.ImageFillContain

	subtitle := widget.NewRichText()
	subtitle.Wrapping = fyne.TextWrapWord
	subtitle.Segments = []widget.RichTextSegment{
		&widget.TextSegment{Text: "对准扫码 · 配对成功后本弹窗自动关闭",
			Style: widget.RichTextStyle{ColorName: theme.ColorNamePlaceHolder, Alignment: fyne.TextAlignCenter}},
	}

	// 网卡页内容：下拉项带网卡名，用户一眼能区分真实网卡与 VMware 等虚拟
	// 网卡；选中立即重绘主页二维码（返回主页即见新码）。
	items := make([]string, len(ifaces))
	for i, f := range ifaces {
		items[i] = f.IP + "（" + f.Name + "）"
	}
	sel := widget.NewSelect(items, nil)
	// 配对地址行先于 update 声明（update 闭包随网卡切换刷新它）
	addrVal := widget.NewLabel(fmt.Sprintf("wss://%s:%d/ws", ifaces[0].IP, port))
	addrVal.Wrapping = fyne.TextWrapBreak
	update := func(idx int) {
		if idx < 0 || idx >= len(ifaces) {
			return
		}
		payload := BuildConnectURL(ifaces[idx].IP, port, c.opts.ConnectToken, c.opts.CertFingerprint)
		q, err := qrcode.New(payload, qrcode.Medium)
		if err != nil {
			return
		}
		img.Image = q.Image(qrImgPx)
		img.Refresh()
		// 配对地址行同步随网卡刷新（令牌页复制按钮经 addrVal.Text 延迟取值）
		addrVal.SetText(fmt.Sprintf("wss://%s:%d/ws", ifaces[idx].IP, port))
	}
	sel.OnChanged = func(s string) {
		for i, it := range items {
			if it == s {
				update(i)
				return
			}
		}
	}
	// 默认选中排序后的第一项（物理网卡优先，见 localIPv4s 排序）
	update(0)
	sel.SetSelected(items[0])

	// 令牌页内容：地址/令牌/指纹三行各带复制按钮。地址行（2026-11-19 用户
	// 反馈"wss 链接漏了"补上）为完整 wss URI——与二维码 payload 的 u 参数
	// 一致，复制后可直接粘贴进手机端"电脑地址"栏（手机端校验以 wss:// 开头，
	// 见 AssistConfig/RemoteAnalysisClient），随网卡切换动态刷新（声明与刷新
	// 见上方网卡区）。64 位 hex 值 TextWrapBreak 逐字断行（横滚根治的同类
	// 纪律）；中文标签禁等宽。
	copyBtn := func(val func() string, enabled bool) *widget.Button {
		b := widget.NewButton("复制", nil)
		if !enabled {
			b.Disable() // 无指纹锁定时复制无意义
		}
		b.OnTapped = func() {
			c.app.Clipboard().SetContent(val()) // OnTapped 回调天然在 fyne 主 goroutine；延迟取值支持地址随网卡刷新
			b.SetText("已复制✓")
			// AfterFunc 回调在后台 goroutine，SetText 触发刷新必须投递主 goroutine
			time.AfterFunc(2*time.Second, func() {
				fyne.Do(func() { b.SetText("复制") })
			})
		}
		return b
	}
	fpText := c.opts.CertFingerprint
	if fpText == "" {
		fpText = "（未启用锁定）"
	}
	tokVal := widget.NewLabel(c.opts.ConnectToken)
	tokVal.Wrapping = fyne.TextWrapBreak
	fpVal := widget.NewLabel(fpText)
	fpVal.Wrapping = fyne.TextWrapBreak
	tokRows := container.NewVBox(
		container.NewBorder(nil, nil, widget.NewLabel("配对地址"), copyBtn(func() string { return addrVal.Text }, true), addrVal),
		container.NewBorder(nil, nil, widget.NewLabel("配对令牌"), copyBtn(func() string { return c.opts.ConnectToken }, true), tokVal),
		container.NewBorder(nil, nil, widget.NewLabel("证书指纹"), copyBtn(func() string { return c.opts.CertFingerprint }, c.opts.CertFingerprint != ""), fpVal),
	)

	// 三页构建（d 先声明后赋值，closeBtn 闭包延迟求值）。每页 Border(底=按钮
	// 排, 中心=内容)，按钮排恒在页底；子页内容整页大大方方展示。
	var d dialog.Dialog
	pageTitle := func(s string) fyne.CanvasObject {
		l := widget.NewLabel(s)
		l.TextStyle = fyne.TextStyle{Bold: true}
		l.Alignment = fyne.TextAlignCenter
		return l
	}
	netBtn := widget.NewButton("网卡设置", nil)
	tokBtn := widget.NewButton("令牌详情", nil)
	closeBtn := widget.NewButton("关闭", func() { d.Dismiss() })
	var mainPage, netPage, tokPage fyne.CanvasObject
	var pageStack *fyne.Container
	showPage := func(p fyne.CanvasObject) {
		for _, o := range pageStack.Objects { // pageStack 闭包延迟求值（下方赋值）
			o.Hide()
		}
		p.Show()
	}
	mainPage = container.NewBorder(nil,
		container.NewGridWithColumns(3, netBtn, tokBtn, closeBtn), nil, nil,
		container.NewCenter(container.NewVBox(subtitle, container.NewStack(qrCard, img))))
	netPage = container.NewBorder(nil,
		container.NewGridWithColumns(1, widget.NewButton("‹ 返回", func() { showPage(mainPage) })), nil, nil,
		container.NewVBox(pageTitle("选择网卡"), container.NewPadded(sel)))
	tokPage = container.NewBorder(nil,
		container.NewGridWithColumns(1, widget.NewButton("‹ 返回", func() { showPage(mainPage) })), nil, nil,
		container.NewVBox(pageTitle("令牌详情"), tokRows))
	netBtn.OnTapped = func() { showPage(netPage) }
	tokBtn.OnTapped = func() { showPage(tokPage) }

	pageStack = container.NewStack(mainPage, netPage, tokPage)
	netPage.Hide() // 初始仅主页可见（Stack 多层叠加，Hide 隐藏非当前页）
	tokPage.Hide()

	content := pageStack
	d = dialog.NewCustomWithoutButtons("手机扫码配对", content, c.win)
	d.Resize(fyne.NewSize(qrDlgWidth, qrDlgHeight))
	d.SetOnClosed(func() {
		if c.qrDialog == d {
			c.qrDialog = nil
		}
		// 解除扫码置顶：自绘"关闭"与配对成功自动关（pairingWatch→Dismiss）
		// 两条路径统一在此收口；SetWindowPos 幂等，重复调用无副作用
		c.topMostMainWindow(false)
	})
	c.qrDialog = d
	c.topMostMainWindow(true) // Show 后置顶：扫码期间其它窗口盖不上主窗
	d.Show()
}

// topMostMainWindow 扫码期间主窗临时置顶/解除（2026-11-18 用户拍板）。
// 经 RunNative 拿 WindowsWindowContext.HWND 调 setWindowTopMost（Windows 实现
// 见 qr_windows.go，非 Windows 静默无操作）；fyne 测试渲染无原生窗口时
// RunNative 的 ctx 断言失败同样静默跳过——与 applyDarkTitleBar 同款容错。
func (c *Console) topMostMainWindow(top bool) {
	nw, ok := c.win.(driver.NativeWindow)
	if !ok {
		return
	}
	nw.RunNative(func(ctx any) {
		hc, ok := ctx.(driver.WindowsWindowContext)
		if !ok || hc.HWND == 0 {
			return
		}
		setWindowTopMost(hc.HWND, top)
	})
}

// pairingWatch 配对成功自动关窗（2026-11-17 扫码五件套）：二维码弹窗开着时
// 监视认证会话数 0→1 的跳变——手机配对成功即自动关窗+提示，用户不必切回
// 电脑确认"扫没扫上"。由 refresh（fyne 主 goroutine）调用，qrDialog/lastAuthed
// 仅主 goroutine 访问，无并发问题。设备已在线（authed≥1）时开窗再连第二台
// 不会触发关窗（无 0→1 跳变），弹窗保持手动关闭语义。
func (c *Console) pairingWatch(authed int) {
	if c.qrDialog == nil {
		c.lastAuthed = authed
		return
	}
	if c.lastAuthed == 0 && authed > 0 {
		d := c.qrDialog
		c.qrDialog = nil // 先清引用：Dismiss 触发 SetOnClosed 时判等已不成立，不重复处理
		d.Dismiss()      // 程序化关闭（fyne Dialog 无 Close 方法），SetOnClosed 链自动解除扫码置顶
		dialog.NewInformation("配对成功", "手机已连接本机（主窗口“连接”区可见设备），二维码弹窗已自动关闭。", c.win).Show()
	}
	c.lastAuthed = authed
}
