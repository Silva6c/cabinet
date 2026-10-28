// Package firewall 的跨平台部分：防火墙参数推导是纯 bind 地址解析逻辑，
// 与平台无关（实际执行放行——netsh——才是 Windows 专属，见 firewall_windows.go）。
// 测试 firewall_test.go 亦因此可在任意平台运行。
package firewall

import (
	"fmt"
	"net"
	"strconv"
)

// ruleNamePrefix 放行规则名前缀；完整规则名 = "cabinet-wss-<端口>"，
// 与 scripts/allow-9527.bat 的 add/show 查重名一致。
const ruleNamePrefix = "cabinet-wss-"

// Plan 由 bind 地址推导防火墙参数：端口、规则名、告警是否适用。
// 仅通配监听（":9527"、"0.0.0.0:9527"、"[::]:9527"）需要外部可达，
// 绑定回环/具体 IP 时手机本来就连不上（另一类配置问题），不适用防火墙告警。
// bind 解析失败（配置非法）同样返回不适用——错误由监听路径自行上报。
func Plan(bind string) (port int, ruleName string, applicable bool) {
	host, portStr, err := net.SplitHostPort(bind)
	if err != nil {
		return 0, "", false
	}
	p, err := strconv.Atoi(portStr)
	if err != nil || p <= 0 || p > 65535 {
		return 0, "", false
	}
	switch host {
	case "", "0.0.0.0", "::", "[::]":
		return p, fmt.Sprintf("%s%d", ruleNamePrefix, p), true
	}
	// 回环/具体 IP：不适用告警，端口也不外泄（防御调用方误用）
	return 0, "", false
}
