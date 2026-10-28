package firewall

import "testing"

// TestPlan bind 地址推导：仅通配监听适用防火墙告警；回环/具体 IP 不适用
// （手机本来就连不上，告警防火墙反而误导）；解析失败不适用（配置错误由
// 监听路径上报）。
func TestPlan(t *testing.T) {
	cases := []struct {
		bind      string
		wantPort  int
		wantRule  string
		wantApply bool
	}{
		{bind: ":9527", wantPort: 9527, wantRule: "cabinet-wss-9527", wantApply: true},
		{bind: "0.0.0.0:8080", wantPort: 8080, wantRule: "cabinet-wss-8080", wantApply: true},
		{bind: "[::]:9527", wantPort: 9527, wantRule: "cabinet-wss-9527", wantApply: true},
		{bind: "127.0.0.1:9527", wantPort: 0, wantRule: "", wantApply: false},
		{bind: "192.168.1.5:9527", wantPort: 0, wantRule: "", wantApply: false},
		{bind: "no-port", wantPort: 0, wantRule: "", wantApply: false},
		{bind: "", wantPort: 0, wantRule: "", wantApply: false},
	}
	for _, c := range cases {
		port, rule, apply := Plan(c.bind)
		if port != c.wantPort || rule != c.wantRule || apply != c.wantApply {
			t.Errorf("Plan(%q) = (%d,%q,%v), want (%d,%q,%v)",
				c.bind, port, rule, apply, c.wantPort, c.wantRule, c.wantApply)
		}
	}
}
