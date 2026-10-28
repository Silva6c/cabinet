//go:build windows

package update

import (
	"net/http"
	"net/url"
	"strings"

	"golang.org/x/sys/windows/registry"
)

// proxyFunc 返回 HTTP Transport 代理选择函数：环境变量优先（HTTP(S)_PROXY，
// 通用惯例），无环境变量时回落 Windows 系统代理（IE 设置——用户的"全局代理"
// 即系统级，Go 默认 Transport 不读它，必须显式接；GFW 下直连 api.github.com
// 不通，这一步决定功能可用性）。系统代理启动时读一次缓存（低频检查无需实时）。
func proxyFunc() func(*http.Request) (*url.URL, error) {
	sysProxy := readSystemProxy()
	return func(req *http.Request) (*url.URL, error) {
		if u, err := http.ProxyFromEnvironment(req); err == nil && u != nil {
			return u, nil
		}
		return sysProxy, nil
	}
}

// readSystemProxy 读注册表 IE 代理设置；未启用/读失败返回 nil（直连）。
// 只读不写——绿色软件红线：注册表只读，绝不写入。
func readSystemProxy() *url.URL {
	k, err := registry.OpenKey(registry.CURRENT_USER,
		`Software\Microsoft\Windows\CurrentVersion\Internet Settings`, registry.QUERY_VALUE)
	if err != nil {
		return nil
	}
	defer func() { _ = k.Close() }()
	enable, _, err := k.GetIntegerValue("ProxyEnable")
	if err != nil || enable == 0 {
		return nil
	}
	s, _, err := k.GetStringValue("ProxyServer")
	if err != nil {
		return nil
	}
	return parseProxyServer(s)
}

// parseProxyServer 解析 IE ProxyServer 值的两种形态：
//   - "host:port" 全局式（url.Parse 会把 host 误判为 scheme，须直接构造）
//   - "ftp=…;http=…;https=…" 分协议式（优先 https 分量，退 http）
//
// 解析不出返回 nil（直连，失败静默）。
func parseProxyServer(s string) *url.URL {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	if strings.Contains(s, "=") { // 分协议式
		var httpPart string
		for _, part := range strings.Split(s, ";") {
			kv := strings.SplitN(part, "=", 2)
			if len(kv) != 2 {
				continue
			}
			switch strings.TrimSpace(strings.ToLower(kv[0])) {
			case "https":
				if u := hostPortURL(kv[1]); u != nil {
					return u
				}
			case "http":
				httpPart = kv[1]
			}
		}
		return hostPortURL(httpPart)
	}
	return hostPortURL(s)
}

// hostPortURL 把 "host[:port]" 或带 scheme 的代理串包成代理 URL。
func hostPortURL(s string) *url.URL {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	if strings.Contains(s, "://") {
		if u, err := url.Parse(s); err == nil && u.Host != "" {
			return u
		}
		return nil
	}
	if !strings.Contains(s, ":") {
		s += ":80" // 无端口的裸 host 走 80
	}
	return &url.URL{Scheme: "http", Host: s}
}
