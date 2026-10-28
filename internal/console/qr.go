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
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
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

// showQRDialog 连接二维码弹窗：下拉选网卡 IP（显示网卡名，默认选中真实物理
// 网卡而非 VMware 等虚拟网卡）→ 实时重绘二维码；下方附纯文本地址与令牌。
func (c *Console) showQRDialog() {
	ifaces := localIPv4s()
	if len(ifaces) == 0 {
		dialog.NewInformation("连接二维码", "未找到可用的局域网 IPv4 地址，请检查网络连接。", c.win).Show()
		return
	}
	port := bindPort(c.opts.BindAddr)

	img := &canvas.Image{}
	img.SetMinSize(fyne.NewSize(300, 300))
	img.FillMode = canvas.ImageFillContain
	addrLabel := widget.NewLabel("")
	addrLabel.Wrapping = fyne.TextWrapBreak
	hint := widget.NewLabel("用手机系统相机扫码 → 点击弹出的“内阁”链接，连接参数自动填入。\n连不上时：①确认电脑防火墙已放行 9527 端口（scripts/allow-9527.bat 管理员运行）；②下拉换真实网卡 IP（WLAN/以太网）重扫。")
	hint.Wrapping = fyne.TextWrapWord

	// 下拉项带网卡名，用户一眼能区分真实网卡与 VMware 等虚拟网卡
	items := make([]string, len(ifaces))
	for i, f := range ifaces {
		items[i] = f.IP + "（" + f.Name + "）"
	}

	sel := widget.NewSelect(items, nil)
	update := func(idx int) {
		if idx < 0 || idx >= len(ifaces) {
			return
		}
		payload := BuildConnectURL(ifaces[idx].IP, port, c.opts.ConnectToken, c.opts.CertFingerprint)
		q, err := qrcode.New(payload, qrcode.Medium)
		if err != nil {
			return
		}
		img.Image = q.Image(300)
		img.Refresh()
		fpText := c.opts.CertFingerprint
		if fpText == "" {
			fpText = "（未启用锁定）"
		}
		addrLabel.SetText(fmt.Sprintf("地址：%s:%d/ws\n令牌：%s\n证书指纹：%s",
			ifaces[idx].IP, port, c.opts.ConnectToken, fpText))
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

	content := container.NewVBox(sel, img, addrLabel, hint)
	d := dialog.NewCustom("连接二维码", "关闭", content, c.win)
	d.Resize(fyne.NewSize(460, 560))
	d.Show()
}
