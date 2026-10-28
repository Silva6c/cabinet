//go:build !windows

package update

import (
	"net/http"
	"net/url"
)

// proxyFunc 非 Windows 平台（CI/联调）：环境变量代理（HTTP(S)_PROXY）。
// 与 proxy_windows.go 签名逐字对表（跨平台雷区纪律）。
func proxyFunc() func(*http.Request) (*url.URL, error) {
	return http.ProxyFromEnvironment
}
