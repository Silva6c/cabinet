package server

import (
	"crypto/tls"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"cabinet/internal/config"
	"cabinet/internal/engine"
)

// flipTurnFen 与 bad_position 自动纠正闭环单测（2026-10-02）：
// 吃子步走子方卡错导致 PositionLegal 拒收，PC 端拒收前验证"翻转轮次后合法"
// 并在错误帧带标记，手机端据此自动纠正重发——不再等用户点"更新"。

func TestFlipTurnFen(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"红走翻黑", "3akR3/4a4/9/9/9/9/9/9/4A4/3K5 w - - 0 1", "3akR3/4a4/9/9/9/9/9/9/4A4/3K5 b - - 0 1"},
		{"黑走翻红", "9/9/9/9/9/9/9/9/4A4/3K5 b - - 0 1", "9/9/9/9/9/9/9/9/4A4/3K5 w - - 0 1"},
		{"无走子段原样返回", "3akR3/9/9/9/9/9/9/9/9/3K5", "3akR3/9/9/9/9/9/9/9/9/3K5"},
	}
	for _, c := range cases {
		if got := flipTurnFen(c.in); got != c.want {
			t.Errorf("%s: flipTurnFen(%q)=%q 期望 %q", c.name, c.in, got, c.want)
		}
	}
}

// 2026-10-02 10:07 实拒实例：红车吃黑车贴脸将后走子方仍标 w，被拒；
// 翻转为 b（被将军方行棋，唯一自洽解释）应通过 PositionLegal。
func TestFlipTurnLegalOnRealRejectedFen(t *testing.T) {
	fen := "3akR3/4a4/4b2c1/p1R5p/4p1p2/9/Pr2n1P1P/B3C1N2/9/3AKAB2 w - - 0 1"
	if engine.PositionLegal(fen) {
		t.Fatal("该 FEN 走子方为 w（红走）且红车正吃黑将,应被拒收")
	}
	flipped := flipTurnFen(fen)
	if !strings.HasSuffix(flipped, "b - - 0 1") {
		t.Fatalf("翻转后走子段应为 b,实际 %q", flipped)
	}
	if !engine.PositionLegal(flipped) {
		t.Fatal("翻转为 b(黑行棋)后应通过引擎安全校验")
	}
}

// fakeTunerEngine 最小引擎桩：满足 EngineBridge + EngineTuner（引擎桥集成测试注入）。
type fakeTunerEngine struct {
	events     chan engine.Event
	depth      int
	moveTimeMS int
	hashMB     int
}

func (f *fakeTunerEngine) Events() <-chan engine.Event { return f.events }
func (f *fakeTunerEngine) Request(fen string)          {}
func (f *fakeTunerEngine) State() string               { return engine.StateReady }
func (f *fakeTunerEngine) SetDepth(d int) bool         { f.depth = d; return true }
func (f *fakeTunerEngine) SetMoveTimeMS(ms int) bool   { f.moveTimeMS = ms; return true }
func (f *fakeTunerEngine) SetHashMB(mb int) bool       { f.hashMB = mb; return true }
func (f *fakeTunerEngine) SetThreads(n int) bool       { return true }
func (f *fakeTunerEngine) SetMultiPV(n int) bool       { return true }
func (f *fakeTunerEngine) Depth() int                  { return f.depth }
func (f *fakeTunerEngine) MoveTimeMS() int             { return f.moveTimeMS }
func (f *fakeTunerEngine) HashMB() int                 { return f.hashMB }
func (f *fakeTunerEngine) Threads() int                { return 0 }
func (f *fakeTunerEngine) MultiPV() int                { return 0 }
func (f *fakeTunerEngine) ClearHash()                  {}

// TestWelcomeEngineConfigAndBadPositionFenEcho 端到端验证 2026-10-02 两个协议扩展：
//  1. welcome 帧携带引擎实际算力（depth/hash_mb）——手机端按钮显示与 PC 一致；
//  2. bad_position 错误帧回显触发局面 fen + turn_flip_legal——手机端据此丢弃
//     迟到旧错误（不再错翻新局面）并自动纠正走子方。
func TestWelcomeEngineConfigAndBadPositionFenEcho(t *testing.T) {
	dir := t.TempDir()
	cert, err := EnsureCert(dir)
	if err != nil {
		t.Fatalf("测试证书生成失败: %v", err)
	}
	cfg := config.Default()
	cfg.Bind = "127.0.0.1:0"
	cfg.Token = testToken
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	eng := &fakeTunerEngine{events: make(chan engine.Event, 8), depth: 20, hashMB: 2048, moveTimeMS: 3000}
	srv := New(cfg, cert, logger, eng)
	srv.authTimeout = 10 * time.Second

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

	ws := dialWS(t, "wss://"+ln.Addr().String()+"/ws")
	if _, ok := recv(t, ws, "服务器 hello").(*Hello); !ok {
		t.Fatal("首帧应为 hello")
	}
	if err := ws.WriteJSON(Hello{frameBase: frame(TypeHello), Client: ClientName, Ver: "0.1.0", Caps: []string{CapV1}}); err != nil {
		t.Fatal(err)
	}
	if err := ws.WriteJSON(Auth{frameBase: frame(TypeAuth), Token: testToken}); err != nil {
		t.Fatal(err)
	}
	wl, ok := recv(t, ws, "welcome").(*Welcome)
	if !ok {
		t.Fatalf("期望 welcome, got %T", wl)
	}
	if wl.Engine.Depth != 20 || wl.Engine.HashMB != 2048 {
		t.Fatalf("welcome 应携带引擎实际算力 depth=20 hash_mb=2048, got %+v", wl.Engine)
	}
	// 思考模式同步（2026-10-28 补链）：movetime_ms 指针必填（0=深度制也传到）
	if wl.Engine.MoveTimeMS == nil || *wl.Engine.MoveTimeMS != 3000 {
		t.Fatalf("welcome 应携带思考模式 movetime_ms=3000, got %+v", wl.Engine.MoveTimeMS)
	}

	// 走子方标错的局面：错误帧必须回显 fen（手机端丢弃迟到旧错误的依据）
	fen := "3akR3/4a4/4b2c1/p1R5p/4p1p2/9/Pr2n1P1P/B3C1N2/9/3AKAB2 w - - 0 1"
	if err := ws.WriteJSON(Position{frameBase: frame(TypePosition), Fen: fen, RedGo: true}); err != nil {
		t.Fatal(err)
	}
	e, ok := recv(t, ws, "error").(*ErrorFrame)
	if !ok {
		t.Fatalf("期望 error, got %T", e)
	}
	if e.Code != CodeBadPosition {
		t.Fatalf("应为 bad_position, got %q (%s)", e.Code, e.Message)
	}
	if e.Fen != fen {
		t.Fatalf("错误帧应回显触发局面 fen=%q, got %q", fen, e.Fen)
	}
	if !e.TurnFlipLegal {
		t.Fatal("该局面翻转走子方后合法, turn_flip_legal 应为 true")
	}
}
