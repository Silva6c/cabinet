//go:build windows

package update

import "testing"

// TestParseProxyServer IE ProxyServer 两种形态解析。
func TestParseProxyServer(t *testing.T) {
	cases := []struct {
		in   string
		want string // 期望 URL.String()；空 = nil（直连）
	}{
		{"127.0.0.1:7890", "http://127.0.0.1:7890"},
		{"192.168.1.10", "http://192.168.1.10:80"},
		{"http=1.1.1.1:80;https=2.2.2.2:443", "http://2.2.2.2:443"}, // 分协议式取 https
		{"ftp=3.3.3.3:21;http=1.1.1.1:80", "http://1.1.1.1:80"},     // 无 https 退 http
		{"http://proxy.local:8888", "http://proxy.local:8888"},      // 带 scheme 兼容
		{"", ""},
		{"   ", ""},
		{"https=", ""}, // 分协议式空分量
	}
	for _, c := range cases {
		u := parseProxyServer(c.in)
		got := ""
		if u != nil {
			got = u.String()
		}
		if got != c.want {
			t.Errorf("parseProxyServer(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestReadSystemProxySmoke 冒烟：真实注册表读取不 panic（启用与否随环境，无断言）。
func TestReadSystemProxySmoke(t *testing.T) {
	_ = readSystemProxy()
}
