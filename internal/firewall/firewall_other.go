//go:build !windows

// firewall_other.go 非 Windows 平台的防火墙存根：Linux/macOS 没有 Windows
// 防火墙可查可设，此存根让 cmd/cabinet 的跨平台调用编译通过（Windows 实现
// 见 firewall_windows.go，靠文件名后缀获得平台约束）。
package firewall

import "errors"

// RuleExists 非 Windows 恒返回 true（"规则已在"）：无 Windows 防火墙拦截，
// 调用方据此跳过"未放行"告警（告警文案本身也仅对 Windows 有意义）。
func RuleExists(_ string) (bool, error) { return true, nil }

// Allow 非 Windows 不支持一键放行；正常流程到不了这里（RuleExists 恒 true
// → 告警不出现 → 放行入口不显示），防御性返回错误而非静默成功。
func Allow(_ string, _ int) error {
	return errors.New("防火墙一键放行仅 Windows 支持")
}
