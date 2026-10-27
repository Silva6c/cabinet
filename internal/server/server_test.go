package server

import (
	"crypto/tls"
	"io"
	"log/slog"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"cabinet/internal/config"
	"cabinet/internal/engine"
)

const testToken = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// startTestServer 在随机端口起真实 TLS WSS 服务，返回 wss:// 地址与清理函数。
func startTestServer(t *testing.T, authTimeout time.Duration) (string, func()) {
	return startTestServerFull(t, authTimeout, 45*time.Second)
}

// startTestServerFull 允许同时定制鉴权窗口与空闲死链窗口。
func startTestServerFull(t *testing.T, authTimeout, idleTimeout time.Duration) (string, func()) {
	t.Helper()
	dir := t.TempDir()
	cert, err := EnsureCert(dir)
	if err != nil {
		t.Fatalf("测试证书生成失败: %v", err)
	}
	cfg := config.Default()
	cfg.Bind = "127.0.0.1:0"
	cfg.Token = testToken
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	srv := New(cfg, cert, logger, nil) // nil 引擎 = M0 echo 行为
	srv.authTimeout = authTimeout      // 测试注入短窗口加速
	srv.idleTimeout = idleTimeout

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	hs := &http.Server{
		Handler: srv.Handler(),
		TLSConfig: &tls.Config{
			Certificates: []tls.Certificate{cert},
			MinVersion:   tls.VersionTLS12,
		},
	}
	go hs.ServeTLS(ln, "", "") // 证书已注入 TLSConfig
	cleanup := func() { _ = hs.Close() }
	t.Cleanup(cleanup)
	return "wss://" + ln.Addr().String() + "/ws", cleanup
}

// dialWS 以跳过自签校验的方式连接测试服务器。
func dialWS(t *testing.T, url string) *websocket.Conn {
	t.Helper()
	d := websocket.Dialer{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
	ws, resp, err := d.Dial(url, nil)
	if err != nil {
		t.Fatalf("连接失败: %v (http=%v)", err, resp)
	}
	t.Cleanup(func() { _ = ws.Close() })
	return ws
}

// recv 限时读一帧并解码为消息结构。
func recv(t *testing.T, ws *websocket.Conn, what string) any {
	t.Helper()
	_ = ws.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, data, err := ws.ReadMessage()
	if err != nil {
		t.Fatalf("读取 %s 失败: %v", what, err)
	}
	msg, err := ParseFrame(data)
	if err != nil {
		t.Fatalf("解码 %s 失败: %v (帧: %s)", what, err, data)
	}
	return msg
}

// TestFullHandshakeAndEcho 完整链路：PC 先发 hello → 客户端 hello/auth → welcome
// → position→status → ping→pong → bye→bye（协议 §3/§8）。
func TestFullHandshakeAndEcho(t *testing.T) {
	url, _ := startTestServer(t, 10*time.Second)
	ws := dialWS(t, url)

	// 1. 连接建立后 PC 先发 hello（协议钉死时序的第一步）
	h, ok := recv(t, ws, "服务器 hello").(*Hello)
	if !ok {
		t.Fatalf("首帧应为 hello, got %T", h)
	}
	if h.Server != ServerName || h.Ver != Version || len(h.Caps) != 1 || h.Caps[0] != CapV1 {
		t.Fatalf("服务器 hello 字段不符: %+v", h)
	}

	// 2. 客户端 hello
	if err := ws.WriteJSON(Hello{frameBase: frame(TypeHello), Client: ClientName, Ver: "0.1.0", Caps: []string{CapV1}}); err != nil {
		t.Fatal(err)
	}

	// 3. auth 正确令牌
	if err := ws.WriteJSON(Auth{frameBase: frame(TypeAuth), Token: testToken}); err != nil {
		t.Fatal(err)
	}

	// 4. welcome
	wl, ok := recv(t, ws, "welcome").(*Welcome)
	if !ok {
		t.Fatalf("期望 welcome, got %T", wl)
	}
	if wl.SessionID == "" || wl.Engine.State != engine.StateOffline || len(wl.Books) != 0 {
		t.Fatalf("welcome 字段不符: %+v", wl)
	}

	// 5. position → status（M0 echo 基准）
	pos := Position{
		frameBase: frame(TypePosition),
		Fen:       "rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR w - - 0 1",
		RedGo:     true,
		History:   []string{},
	}
	if err := ws.WriteJSON(pos); err != nil {
		t.Fatal(err)
	}
	st, ok := recv(t, ws, "status").(*Status)
	if !ok {
		t.Fatalf("期望 status, got %T", st)
	}
	if st.Engine.State != engine.StateOffline {
		t.Fatalf("status.engine.state 应为 offline: %+v", st)
	}

	// 6. ping → pong（ts 原样回传）
	if err := ws.WriteJSON(Ping{frameBase: frame(TypePing), Ts: 1757459200123}); err != nil {
		t.Fatal(err)
	}
	pg, ok := recv(t, ws, "pong").(*Pong)
	if !ok {
		t.Fatalf("期望 pong, got %T", pg)
	}
	if pg.Ts != 1757459200123 {
		t.Fatalf("pong.ts 应原样回传: %d", pg.Ts)
	}

	// 7. bye → bye
	if err := ws.WriteJSON(Bye{frameBase: frame(TypeBye)}); err != nil {
		t.Fatal(err)
	}
	if b, ok := recv(t, ws, "bye").(*Bye); !ok {
		t.Fatalf("期望 bye, got %T", b)
	}
}

// TestAuthFailed_KicksConnection 令牌错误 → error(auth_failed) 且连接被断开。
func TestAuthFailed_KicksConnection(t *testing.T) {
	url, _ := startTestServer(t, 10*time.Second)
	ws := dialWS(t, url)

	if _, ok := recv(t, ws, "服务器 hello").(*Hello); !ok {
		t.Fatal("首帧应为 hello")
	}
	_ = ws.WriteJSON(Hello{frameBase: frame(TypeHello), Client: ClientName, Ver: "0.1.0", Caps: []string{CapV1}})
	_ = ws.WriteJSON(Auth{frameBase: frame(TypeAuth), Token: "wrong-token"})

	ef, ok := recv(t, ws, "error").(*ErrorFrame)
	if !ok {
		t.Fatalf("期望 error 帧, got %T", ef)
	}
	if ef.Code != CodeAuthFailed {
		t.Fatalf("错误码应为 auth_failed: %s", ef.Code)
	}
	// 连接应被服务器关闭：后续读取很快出错
	_ = ws.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, _, err := ws.ReadMessage(); err == nil {
		t.Fatal("auth_failed 后连接应被关闭，但仍读到了数据")
	}
}

// TestHandshakeTimeout_KicksIdleConnection 连接后不完成鉴权，超过窗口应被踢出
// （先收 error(auth_required) 再断开；协议 §3 + 10s 读超时）。
func TestHandshakeTimeout_KicksIdleConnection(t *testing.T) {
	url, _ := startTestServer(t, 300*time.Millisecond) // 短窗口加速测试
	ws := dialWS(t, url)

	if _, ok := recv(t, ws, "服务器 hello").(*Hello); !ok {
		t.Fatal("首帧应为 hello")
	}
	// 不发 hello/auth，干等窗口超时
	_ = ws.SetReadDeadline(time.Now().Add(5 * time.Second))
	// 服务器先回 error(auth_required)，随后关闭连接；两件事的到达顺序以读到的为准
	var sawAuthRequired, sawClose bool
	for i := 0; i < 2 && !sawClose; i++ {
		_, data, err := ws.ReadMessage()
		if err != nil {
			sawClose = true
			break
		}
		msg, perr := ParseFrame(data)
		if perr == nil {
			if ef, ok := msg.(*ErrorFrame); ok && ef.Code == CodeAuthRequired {
				sawAuthRequired = true
			}
		}
	}
	if !sawClose {
		t.Fatal("鉴权窗口超时后连接应被关闭")
	}
	if !sawAuthRequired {
		t.Fatal("超时踢出前应回 error(auth_required)")
	}
}

// TestIdleTimeout_KicksSilentConnection 会话期死链检测（协议 §4.7）：
// 鉴权完成后连续 idleTimeout 未收到任何帧 → 服务器主动断开。
func TestIdleTimeout_KicksSilentConnection(t *testing.T) {
	url, _ := startTestServerFull(t, 10*time.Second, 300*time.Millisecond)
	ws := dialWS(t, url)

	if _, ok := recv(t, ws, "服务器 hello").(*Hello); !ok {
		t.Fatal("首帧应为 hello")
	}
	_ = ws.WriteJSON(Hello{frameBase: frame(TypeHello), Client: ClientName, Ver: "0.1.0", Caps: []string{CapV1}})
	_ = ws.WriteJSON(Auth{frameBase: frame(TypeAuth), Token: testToken})
	if wl, ok := recv(t, ws, "welcome").(*Welcome); !ok {
		t.Fatalf("期望 welcome, got %T", wl)
	}

	// 完成握手后保持沉默：超过 idle 窗口应被服务器断开
	_ = ws.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, _, err := ws.ReadMessage(); err == nil {
		t.Fatal("空闲超时后连接应被服务器断开，但仍读到了数据")
	}
}

// TestIdleTimeout_ResetByTraffic 空闲超时只计连续静默：周期发 ping 保活的连接不被误踢。
func TestIdleTimeout_ResetByTraffic(t *testing.T) {
	url, _ := startTestServerFull(t, 10*time.Second, 400*time.Millisecond)
	ws := dialWS(t, url)

	if _, ok := recv(t, ws, "服务器 hello").(*Hello); !ok {
		t.Fatal("首帧应为 hello")
	}
	_ = ws.WriteJSON(Hello{frameBase: frame(TypeHello), Client: ClientName, Ver: "0.1.0", Caps: []string{CapV1}})
	_ = ws.WriteJSON(Auth{frameBase: frame(TypeAuth), Token: testToken})
	if _, ok := recv(t, ws, "welcome").(*Welcome); !ok {
		t.Fatal("期望 welcome")
	}

	// 每 150ms 发一个 ping（< 400ms idle 窗口），持续 1.2s 后连接应仍然存活
	for i := 0; i < 8; i++ {
		time.Sleep(150 * time.Millisecond)
		if err := ws.WriteJSON(Ping{frameBase: frame(TypePing), Ts: int64(i)}); err != nil {
			t.Fatalf("保活发送失败: %v", err)
		}
		if _, ok := recv(t, ws, "pong").(*Pong); !ok {
			t.Fatal("期望 pong")
		}
	}
}

// TestBusinessFrameBeforeAuth 鉴权前发业务帧 → error(auth_required)，连接保持，
// 完成握手后仍可正常使用（协议 §3：提示但不踢）。
func TestBusinessFrameBeforeAuth(t *testing.T) {
	url, _ := startTestServer(t, 10*time.Second)
	ws := dialWS(t, url)

	if _, ok := recv(t, ws, "服务器 hello").(*Hello); !ok {
		t.Fatal("首帧应为 hello")
	}
	// 未握手直接发 position
	_ = ws.WriteJSON(Position{frameBase: frame(TypePosition), Fen: "test", RedGo: true})
	ef, ok := recv(t, ws, "error").(*ErrorFrame)
	if !ok || ef.Code != CodeAuthRequired {
		t.Fatalf("期望 error(auth_required), got %+v", ef)
	}
	// 补完握手，应能正常建立会话
	_ = ws.WriteJSON(Hello{frameBase: frame(TypeHello), Client: ClientName, Ver: "0.1.0", Caps: []string{CapV1}})
	_ = ws.WriteJSON(Auth{frameBase: frame(TypeAuth), Token: testToken})
	if wl, ok := recv(t, ws, "welcome").(*Welcome); !ok {
		t.Fatalf("期望 welcome, got %T", wl)
	}
}

// TestRedWdlPercent 归一化：三项恒合计 100（独立舍入会出现 99/101）；黑方行棋胜负互换；无 wdl 全 nil。
func TestRedWdlPercent(t *testing.T) {
	// 真实引擎输出形态：千分比合计 1000，独立舍入会撞 101（95/897/8 → 10/90/1）
	info := engine.Info{HasWdl: true, WdlW: 95, WdlD: 897, WdlL: 8}
	w, d, l := redWdlPercent(&info, true)
	if *w != 10 || *d != 89 || *l != 1 || *w+*d+*l != 100 {
		t.Fatalf("红方行棋 95/897/8‰ 应归一化为 10/89/1, got %d/%d/%d", *w, *d, *l)
	}
	// 黑方行棋：走子方"胜"即红方"负"
	w2, d2, l2 := redWdlPercent(&info, false)
	if *w2 != 1 || *d2 != 89 || *l2 != 10 || *w2+*d2+*l2 != 100 {
		t.Fatalf("黑方行棋应胜负互换且合计 100, got %d/%d/%d", *w2, *d2, *l2)
	}
	// 全 0 和棋形态不出现负和项
	info0 := engine.Info{HasWdl: true, WdlW: 0, WdlD: 1000, WdlL: 0}
	w3, d3, l3 := redWdlPercent(&info0, true)
	if *w3 != 0 || *d3 != 100 || *l3 != 0 {
		t.Fatalf("纯和棋应为 0/100/0, got %d/%d/%d", *w3, *d3, *l3)
	}
	// 极端分布（2026-10-18 修复回归）：胜负独立舍入双双进位（995/0/5 → 100/1）
	// 时压缩较大一侧，三项仍恒合计 100（旧实现 pd 钳 0 会输出 100/0/1 合计 101）
	infoX := engine.Info{HasWdl: true, WdlW: 995, WdlD: 0, WdlL: 5}
	wx, dx, lx := redWdlPercent(&infoX, true)
	if *wx+*dx+*lx != 100 || *dx < 0 {
		t.Fatalf("极端分布应恒合计 100, got %d/%d/%d", *wx, *dx, *lx)
	}
	if *wx != 99 || *lx != 1 || *dx != 0 {
		t.Fatalf("995/0/5‰ 应压胜侧为 99/0/1, got %d/%d/%d", *wx, *dx, *lx)
	}
	// 引擎未输出 wdl：三项均 nil（详情窗胜率行隐藏）
	if w4, d4, l4 := redWdlPercent(&engine.Info{}, true); w4 != nil || d4 != nil || l4 != nil {
		t.Fatalf("无 wdl 应全 nil, got %v/%v/%v", w4, d4, l4)
	}
}
