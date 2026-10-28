//go:build windows

// Package firewall Windows 防火墙放行规则检测与一键放行。
//
// 场景：手机端经 WSS 连 PC 的 9527 端口；Windows 首次弹"允许访问"对话框时
// 用户点了取消，防火墙会静默拦截入站连接——监听侧毫无感知，手机端只见
// "连接超时（…或电脑防火墙未放行 9527）"。本包提供：
//   - Plan：由 bind 地址推导端口/规则名，并判定是否适用告警（仅通配监听）
//   - RuleExists：只读 netsh 查询规则是否存在（普通用户权限即可执行）
//   - Allow：经 ShellExecuteW runas 动词触发 UAC 提权执行 netsh 添加规则
//
// 规则名与端口的同步契约（四处）见 internal/console/qr.go bindPort 注释
// （config 默认 Bind、scripts/allow-9527.bat、qr 回退端口、本包规则名推导）
// ——本包不硬编码端口，一律由调用方从 cfg.Bind 推导传入。
//
// 【windows-only】本包只有 _windows 实现（netsh/ShellExecuteW 均系统专属），
// 与 cmd/cabinet/singleton_windows.go 同惯例不设跨平台 stub；测试文件同理。
package firewall

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"syscall"
	"unsafe"
)

// hideWindowProcAttr netsh 等 console 子系统的隐藏属性：本程序以 windowsgui
// 子系统运行（无控制台），Windows 会为 console 子进程新建 conhost 窗口——
// 不加此属性时每次启动的 netsh 检测都会"闪一个黑框"（2026-10-18 实测）。
// HideWindow 与 CREATE_NO_WINDOW(0x08000000) 双保险，与 engine 包
// proc_windows.go 同款写法。
var hideWindowProcAttr = &syscall.SysProcAttr{
	HideWindow:    true,
	CreationFlags: 0x08000000, // CREATE_NO_WINDOW
}

// RuleExists 查询放行规则是否已存在。netsh show rule 对不存在的规则返回
// 退出码 1（"没有与指定标准相匹配的规则"），存在返回 0——以退出码判定，
// 不解析输出文本（netsh 输出随系统语言变化，文本解析脆弱）。
// 局限（已知且接受）：规则存在但被禁用 / profile 与当前网络不匹配时，
// 本检测无法覆盖，靠手机端"连接超时"文案与 scripts/allow-9527.bat 兜底。
func RuleExists(ruleName string) (bool, error) {
	if strings.TrimSpace(ruleName) == "" {
		return false, fmt.Errorf("规则名为空")
	}
	cmd := exec.Command("netsh", "advfirewall", "firewall", "show", "rule", "name="+ruleName)
	cmd.SysProcAttr = hideWindowProcAttr
	if err := cmd.Run(); err != nil {
		// 退出码 1 = 规则不存在（正常查询结果，非错误）；其他错误如实上抛
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return false, nil
		}
		return false, fmt.Errorf("netsh 查询失败: %w", err)
	}
	return true, nil
}

// Allow 以 UAC 提权添加放行规则：ShellExecuteW 的 "runas" 动词让 Windows
// 弹 UAC 确认框，用户点"是"后以管理员身份执行 netsh 添加规则。
// 返回 nil 表示 UAC 弹窗已成功拉起（放行结果异步——调用方应延时复查
// RuleExists 确认）；用户拒绝 UAC 或其他失败返回错误。
func Allow(ruleName string, port int) error {
	if strings.TrimSpace(ruleName) == "" {
		return fmt.Errorf("规则名为空")
	}
	args := fmt.Sprintf(`advfirewall firewall add rule name="%s" dir=in action=allow protocol=TCP localport=%d`,
		ruleName, port)
	// netsh 在 System32，用绝对路径避免 PATH 差异（微软商店应用环境等）
	netsh := `C:\Windows\System32\netsh.exe`

	verb, err := utf16Ptr("runas")
	if err != nil {
		return err
	}
	file, err := utf16Ptr(netsh)
	if err != nil {
		return err
	}
	params, err := utf16Ptr(args)
	if err != nil {
		return err
	}
	// nShowCmd=SW_HIDE(0)：尝试隐藏提权 netsh 的短暂 console 窗（ShellExecuteW
	// 对 console 子进程的窗口控制不保证生效，但不会影响 UAC 确认框本身——
	// 那是 consent.exe 的 GUI，与子进程窗口形态无关）
	//nolint:govet // syscall 惯用法：指针→uintptr 转换在 Call 表达式内完成，
	// 是 Windows API（ShellExecuteW）契约要求的合法形态，无悬空风险
	ret, _, _ := procShellExecuteW.Call(
		0,
		uintptr(unsafe.Pointer(verb)),   //nolint:govet
		uintptr(unsafe.Pointer(file)),   //nolint:govet
		uintptr(unsafe.Pointer(params)), //nolint:govet
		0, 0,
	)
	// ShellExecuteW 惯例：返回值 > 32 = 成功；否则为 SE_ERR_* 错误码
	//（含用户在 UAC 框点"否"的 SE_ERR_ACCESSDENIED=5）
	if ret <= 32 {
		return fmt.Errorf("提权启动 netsh 失败（ShellExecuteW 返回 %d）——可在 UAC 弹窗选择\"是\"，或右键管理员运行 scripts/allow-9527.bat", ret)
	}
	return nil
}

var (
	modshell32        = syscall.NewLazyDLL("shell32.dll")
	procShellExecuteW = modshell32.NewProc("ShellExecuteW")
)

// utf16Ptr 生成以 NUL 结尾的 UTF-16 指针（ShellExecuteW 的 LPCTSTR 参数）。
func utf16Ptr(s string) (uintptr, error) {
	p, err := syscall.UTF16PtrFromString(s)
	if err != nil {
		return 0, err
	}
	return uintptr(unsafe.Pointer(p)), nil
}
