package server

import (
	"crypto/tls"
	"io"
	"log/slog"
	"net"
	"net/http"
	"testing"
	"time"

	"cabinet/internal/config"
	"cabinet/internal/engine"
)

// startSnapshotServer 起测试服务并返回 Server 本体（快照测试要直读内部状态）。
func startSnapshotServer(t *testing.T) (*Server, string) {
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
	srv := New(cfg, cert, logger, nil)
	srv.authTimeout = 10 * time.Second
	srv.idleTimeout = 45 * time.Second

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
	go hs.ServeTLS(ln, "", "")
	t.Cleanup(func() { _ = hs.Close() })
	return srv, "wss://" + ln.Addr().String() + "/ws"
}

// onlyConn 返回当前唯一连接（快照测试场景固定单连接）。
func onlyConn(t *testing.T, srv *Server) *Conn {
	t.Helper()
	srv.hub.mu.Lock()
	defer srv.hub.mu.Unlock()
	if len(srv.hub.conns) != 1 {
		t.Fatalf("应恰有 1 个连接, got %d", len(srv.hub.conns))
	}
	for c := range srv.hub.conns {
		return c
	}
	return nil
}

// waitFor 轮询等待条件成立（快照是异步更新的观测值）。
func waitFor(t *testing.T, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("等待条件超时")
}

// TestConsoleSnapshot 控制台快照：连接信息（鉴权/客户端/路径/最后position时间）
// 与引擎指标缓存（broadcastSearchStatus 的最新一份数据）。
func TestConsoleSnapshot(t *testing.T) {
	srv, url := startSnapshotServer(t)
	ws := dialWS(t, url)

	// 握手：PC hello → 客户端 hello + auth → welcome
	if _, ok := recv(t, ws, "服务器 hello").(*Hello); !ok {
		t.Fatal("首帧应为 hello")
	}
	_ = ws.WriteJSON(Hello{frameBase: frame(TypeHello), Client: ClientName, Ver: "9.9.9", Caps: []string{CapV1}})
	_ = ws.WriteJSON(Auth{frameBase: frame(TypeAuth), Token: testToken})
	if _, ok := recv(t, ws, "welcome").(*Welcome); !ok {
		t.Fatal("期望 welcome")
	}

	// 上行一个局面：LastPos 应被记录
	_ = ws.WriteJSON(Position{frameBase: frame(TypePosition),
		Fen:   "rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR w - - 0 1",
		RedGo: true, History: []string{}})
	waitFor(t, time.Second, func() bool {
		snap := srv.ConsoleSnapshot()
		return len(snap.Clients) == 1 && !snap.Clients[0].LastPos.IsZero()
	})

	// 未跑 pingLoop 前 RTT 未测得（-1）；直接驱动 OnPong 验证 RTT 记录
	onlyConn(t, srv).OnPong(time.Now().UnixMilli() - 12)
	snap := srv.ConsoleSnapshot()
	if len(snap.Clients) != 1 {
		t.Fatalf("应恰有 1 个连接, got %d", len(snap.Clients))
	}
	cl := snap.Clients[0]
	if !cl.Authed || cl.Client != ClientName || cl.Ver != "9.9.9" {
		t.Fatalf("连接快照字段不符: %+v", cl)
	}
	if cl.RTTMs != 12 {
		t.Fatalf("RTT 应为 12ms, got %d", cl.RTTMs)
	}
	if cl.Path != "LAN" {
		t.Fatalf("路径应为 LAN, got %q", cl.Path)
	}
	if cl.SessionID == "" || len(cl.SessionID) <= 8 {
		t.Fatalf("会话 ID 应为缩略格式（>8 字符）: %q", cl.SessionID)
	}

	// 引擎指标缓存：最新一次广播的搜索指标（收束为 nil 时指标清零）
	srv.broadcastSearchStatus(&engine.Info{Depth: 12, Nodes: 1234, NPS: 50000, Hashfull: 32})
	snap = srv.ConsoleSnapshot()
	if snap.Engine.Depth != 12 || snap.Engine.Nodes != 1234 || snap.Engine.NPS != 50000 || snap.Engine.Hashfull != 32 {
		t.Fatalf("引擎指标缓存不符: %+v", snap.Engine)
	}
	// 默认配置（DefaultEngine）：Threads 0 自动档、HashMB 256（2026-10-18
	// 默认最低档 + 切换落盘）、MultiPV 1（单线最强形态，变招手动开），
	// 面板负责把 0 显示为"自动"
	if snap.Engine.HashMB != 256 || snap.Engine.Threads != 0 || snap.Engine.MultiPV != 1 || snap.Engine.TargetDepth != 20 {
		t.Fatalf("引擎配置快照应为默认值: %+v", snap.Engine)
	}
	srv.broadcastSearchStatus(nil)
	snap = srv.ConsoleSnapshot()
	if snap.Engine.Depth != 0 || snap.Engine.Nodes != 0 || snap.Engine.State != engine.StateOffline {
		t.Fatalf("收束后指标应清零: %+v", snap.Engine)
	}
}
