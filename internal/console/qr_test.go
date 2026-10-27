package console

import (
	"net/url"
	"testing"
)

// TestBuildConnectURL 二维码 payload：f 指纹参数非空才携带（手机端据此决定是否锁定）。
func TestBuildConnectURL(t *testing.T) {
	// 带指纹：u/k/f 三参数齐全，f 原样（64 位 hex 不含需转义字符）
	fp := "aabb00112233445566778899aabbccddeeff00112233445566778899aabbccdd"
	got := BuildConnectURL("192.168.1.10", 9527, "tok<en>&x", fp)
	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("payload 非法: %v", got)
	}
	if u.Scheme != "cabinet" || u.Host != "connect" {
		t.Fatalf("scheme/host 不符: %s", got)
	}
	if q := u.Query(); q.Get("k") != "tok<en>&x" || q.Get("f") != fp {
		t.Fatalf("query 解码不符: %v", q)
	}
	if wss := q2wss(t, u.Query().Get("u")); wss != "wss://192.168.1.10:9527/ws" {
		t.Fatalf("u 参数不符: %s", wss)
	}

	// 空指纹：不携带 f 参数（手机端维持联调模式）
	got = BuildConnectURL("192.168.1.10", 9527, "tok", "")
	u, err = url.Parse(got)
	if err != nil {
		t.Fatalf("payload 非法: %v", got)
	}
	if u.Query().Get("f") != "" {
		t.Fatalf("空指纹不应带 f 参数: %s", got)
	}
}

// q2wss 解回 wss 地址（仅测试断言用）。
func q2wss(t *testing.T, encoded string) string {
	t.Helper()
	dec, err := url.QueryUnescape(encoded)
	if err != nil {
		t.Fatalf("u 参数解码失败: %v", err)
	}
	return dec
}
