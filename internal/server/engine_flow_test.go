package server

import (
	"context"
	"crypto/tls"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"cabinet/internal/config"
	"cabinet/internal/engine"

	"github.com/gorilla/websocket"
)

// fakeBridge 假引擎桥：Request 时回放一段脚本化的引擎事件（info→bestmove）。
type fakeBridge struct {
	mu     sync.Mutex
	fens   []string
	state  atomic.Value
	events chan engine.Event
	// Tuner 状态（set_engine 测试）：值语义与 engine.Engine 一致（构造即有初值）
	depth   atomic.Int64
	hash    atomic.Int64
	threads atomic.Int64
	multiPV atomic.Int64
	// onRequest 由测试注入的事件脚本：fen → 事件序列（后台发送）
	onRequest func(fb *fakeBridge, fen string)
}

func newFakeBridge(state string) *fakeBridge {
	fb := &fakeBridge{events: make(chan engine.Event, 64)}
	fb.state.Store(state)
	fb.depth.Store(20)   // 与 config.Default 的 depth 对齐，贴近真实引擎初值
	fb.hash.Store(256)   // 显式 hash（自动档语义见 engine 包）
	fb.threads.Store(18) // 自动档计算后的典型值
	fb.multiPV.Store(3)
	return fb
}

func (fb *fakeBridge) Events() <-chan engine.Event { return fb.events }
func (fb *fakeBridge) State() string               { return fb.state.Load().(string) }

// ---- EngineTuner（协议 §4.10；范围校验与 engine.Engine 相同） ----

func (fb *fakeBridge) SetDepth(d int) bool {
	if d < 1 || d > 40 {
		return false
	}
	fb.depth.Store(int64(d))
	return true
}

func (fb *fakeBridge) SetHashMB(mb int) bool {
	if mb == 0 {
		mb = 16384 // 自动档典型值
	} else if mb < 32 || mb > 32768 {
		return false
	}
	fb.hash.Store(int64(mb))
	return true
}

func (fb *fakeBridge) SetThreads(n int) bool {
	if n == 0 {
		n = 18
	} else if n < 1 || n > 64 {
		return false
	}
	fb.threads.Store(int64(n))
	return true
}

func (fb *fakeBridge) SetMultiPV(n int) bool {
	if n < 1 || n > 5 {
		return false
	}
	fb.multiPV.Store(int64(n))
	return true
}

func (fb *fakeBridge) ClearHash() {}

func (fb *fakeBridge) Depth() int   { return int(fb.depth.Load()) }
func (fb *fakeBridge) HashMB() int  { return int(fb.hash.Load()) }
func (fb *fakeBridge) Threads() int { return int(fb.threads.Load()) }
func (fb *fakeBridge) MultiPV() int { return int(fb.multiPV.Load()) }

func (fb *fakeBridge) Request(fen string) {
	fb.mu.Lock()
	fb.fens = append(fb.fens, fen)
	fb.mu.Unlock()
	if fb.onRequest != nil {
		go fb.onRequest(fb, fen)
	}
}

func (fb *fakeBridge) emit(ev engine.Event) {
	fb.events <- ev
}

// replayNormalSearch 模拟一次正常搜索：两条 info（depth 递增）+ bestmove。
// fen 的行棋方决定分数换算基准（皮卡鱼原生走子方视角 cp=120 / mate）。
func replayNormalSearch(fb *fakeBridge, fen string) {
	redGo := engine.RedGoOf(fen)
	nativeCp := 120
	if !redGo {
		// fake 直接给"原生走子方视角"值；红方视角换算由 server 完成
		nativeCp = 120 // 黑方行棋时原生（黑视角）+120 → 红方视角应为 -120
	}
	best := "h2e2"
	fb.emit(engine.Event{Kind: engine.EventInfo, Fen: fen, Info: &engine.Info{
		MultiPV: 1, Depth: 5, ScoreCP: nativeCp, PV: []string{best, "b9c7"},
		Nodes: 1234, NPS: 400000, Hashfull: 7,
	}})
	fb.emit(engine.Event{Kind: engine.EventInfo, Fen: fen, Info: &engine.Info{
		MultiPV: 1, Depth: 8, ScoreCP: nativeCp, PV: []string{best, "b9c7", "h0g2"},
		Nodes: 50000, NPS: 500000, Hashfull: 12,
	}})
	fb.emit(engine.Event{Kind: engine.EventBestmove, Result: &engine.Result{
		Fen: fen, RedGo: redGo, BestMove: best,
		Lines: []engine.Info{{
			MultiPV: 1, Depth: 8, ScoreCP: nativeCp,
			PV: []string{best, "b9c7", "h0g2"}, Nodes: 50000, NPS: 500000, Hashfull: 12,
		}},
	}})
}

// TestServer_StaleInfoFiltered 回归：与最新请求不符的中间 info（预热、崩溃重启
// 后的兜底搜索）不得冒充最新局面的建议广播给客户端（协议 §4.5 "最新覆盖"）。
func TestServer_StaleInfoFiltered(t *testing.T) {
	fb := newFakeBridge(engine.StateReady)
	url, _ := startTestServerWithBridge(t, fb)
	ws := dialWS(t, url)

	if _, ok := recv(t, ws, "服务器 hello").(*Hello); !ok {
		t.Fatal("首帧应为 hello")
	}
	_ = ws.WriteJSON(Hello{frameBase: frame(TypeHello), Client: ClientName, Ver: "0.1.0", Caps: []string{CapV1}})
	_ = ws.WriteJSON(Auth{frameBase: frame(TypeAuth), Token: testToken})
	if _, ok := recv(t, ws, "welcome").(*Welcome); !ok {
		t.Fatal("期望 welcome")
	}

	// 客户端请求 FEN_A，但引擎实际在搜 FEN_B（模拟重启后的预热兜底）
	fenA := "rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR b - - 0 1"
	fenB := "rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR w - - 0 1"
	_ = ws.WriteJSON(Position{frameBase: frame(TypePosition), Fen: fenA, RedGo: false})
	time.Sleep(50 * time.Millisecond) // 让 lastFen 先落

	// fake 直接发 FEN_B 的 info（不带 Request，等价于引擎自发搜索）
	fb.emit(engine.Event{Kind: engine.EventInfo, Fen: fenB, Info: &engine.Info{
		MultiPV: 1, Depth: 9, ScoreCP: 500, PV: []string{"e3e4"},
		Nodes: 999, NPS: 1000, Hashfull: 50,
	}})

	// 短窗口内不应收到任何 suggestion / status 指标帧
	_ = ws.SetReadDeadline(time.Now().Add(700 * time.Millisecond))
	_, data, err := ws.ReadMessage()
	if err == nil {
		t.Fatalf("陈旧 info 被误广播: %s", data)
	}
}

// TestServer_EngineFlow 端到端：握手（welcome 带引擎状态）→ position →
// suggestion（红方视角换算）+ status（真实搜索指标）→ bestmove 终值。
func TestServer_EngineFlow(t *testing.T) {
	fb := newFakeBridge(engine.StateReady)
	fb.onRequest = replayNormalSearch
	url, _ := startTestServerWithBridge(t, fb)
	ws := dialWS(t, url)

	// 握手
	if _, ok := recv(t, ws, "服务器 hello").(*Hello); !ok {
		t.Fatal("首帧应为 hello")
	}
	_ = ws.WriteJSON(Hello{frameBase: frame(TypeHello), Client: ClientName, Ver: "0.1.0", Caps: []string{CapV1}})
	_ = ws.WriteJSON(Auth{frameBase: frame(TypeAuth), Token: testToken})
	wl, ok := recv(t, ws, "welcome").(*Welcome)
	if !ok {
		t.Fatalf("期望 welcome, got %T", wl)
	}
	if wl.Engine.State != engine.StateReady {
		t.Fatalf("welcome.engine.state 应为 ready: %+v", wl)
	}

	// 发局面（黑方行棋 → 红方视角分数应取负）
	fen := "rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR b - - 0 1"
	_ = ws.WriteJSON(Position{frameBase: frame(TypePosition), Fen: fen, RedGo: false})

	// 收 suggestion 与 status，逐项断言（deadline 内循环收帧）
	var sawSug *Suggestion
	var sawSugWithRealMetrics bool
	var sawStatusWithMetrics bool
	deadline := time.Now().Add(5 * time.Second)
	for sawSug == nil || !sawSugWithRealMetrics || !sawStatusWithMetrics {
		if time.Now().After(deadline) {
			t.Fatalf("收帧不全: suggestion=%v metrics_in_sug=%v metrics_in_status=%v",
				sawSug != nil, sawSugWithRealMetrics, sawStatusWithMetrics)
		}
		_ = ws.SetReadDeadline(time.Now().Add(3 * time.Second))
		_, data, err := ws.ReadMessage()
		if err != nil {
			t.Fatalf("读帧失败（sug=%v status=%v）: %v", sawSug != nil, sawStatusWithMetrics, err)
		}
		msg, perr := ParseFrame(data)
		if perr != nil {
			t.Fatalf("解析失败: %v (%s)", perr, data)
		}
		switch m := msg.(type) {
		case *Suggestion:
			if m.BestMove != "h2e2" {
				t.Fatalf("suggestion.bestmove 错误: %+v", m)
			}
			if m.Score == nil || m.Score.CP == nil {
				t.Fatalf("suggestion.score 缺失: %+v", m)
			}
			// 黑方行棋：原生(黑视角)+120 → 红方视角 -120（协议 §4.5 红方统一视角）
			if *m.Score.CP != -120 {
				t.Fatalf("红方视角换算错误: cp=%d 期望 -120", *m.Score.CP)
			}
			if m.Depth == 8 && len(m.PV) == 3 {
				sawSug = m
				sawSugWithRealMetrics = true
			} else if sawSug == nil {
				sawSug = m // depth 5 的中途推送也算 suggestion 到达
			}
		case *Status:
			if m.Engine.Depth == 8 && m.Engine.Nodes == 50000 && m.Engine.NPS == 500000 && m.Engine.Hashfull == 12 {
				sawStatusWithMetrics = true // 真实搜索指标到达（协议 §4.6）
			}
		case *ErrorFrame:
			t.Fatalf("收到 error 帧: %+v", m)
		}
	}
	if sawSug == nil {
		t.Fatal("未收到 suggestion")
	}
	if !sawSugWithRealMetrics {
		t.Fatal("未收到 depth=8 的终值 suggestion")
	}
	if !sawStatusWithMetrics {
		t.Fatal("未收到带真实搜索指标的 status（depth/nodes/nps/hashfull）")
	}
}

// TestServer_EngineNotReady 引擎未就绪：position → error(not_ready)（协议 §5/§6）。
func TestServer_EngineNotReady(t *testing.T) {
	fb := newFakeBridge(engine.StateLoading)
	url, _ := startTestServerWithBridge(t, fb)
	ws := dialWS(t, url)

	if _, ok := recv(t, ws, "服务器 hello").(*Hello); !ok {
		t.Fatal("首帧应为 hello")
	}
	_ = ws.WriteJSON(Hello{frameBase: frame(TypeHello), Client: ClientName, Ver: "0.1.0", Caps: []string{CapV1}})
	_ = ws.WriteJSON(Auth{frameBase: frame(TypeAuth), Token: testToken})
	if wl, ok := recv(t, ws, "welcome").(*Welcome); !ok || wl.Engine.State != engine.StateLoading {
		t.Fatalf("welcome.engine.state 应为 loading: %+v", wl)
	}

	_ = ws.WriteJSON(Position{frameBase: frame(TypePosition), Fen: "rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR w - - 0 1", RedGo: true})
	ef, ok := recv(t, ws, "error").(*ErrorFrame)
	if !ok || ef.Code != CodeNotReady {
		t.Fatalf("期望 error(not_ready), got %+v", ef)
	}
}

// TestServer_StateBroadcast 引擎状态变化 → 立即广播 status（协议 §4.6）。
func TestServer_StateBroadcast(t *testing.T) {
	fb := newFakeBridge(engine.StateLoading)
	url, _ := startTestServerWithBridge(t, fb)
	ws := dialWS(t, url)

	if _, ok := recv(t, ws, "服务器 hello").(*Hello); !ok {
		t.Fatal("首帧应为 hello")
	}
	_ = ws.WriteJSON(Hello{frameBase: frame(TypeHello), Client: ClientName, Ver: "0.1.0", Caps: []string{CapV1}})
	_ = ws.WriteJSON(Auth{frameBase: frame(TypeAuth), Token: testToken})
	if _, ok := recv(t, ws, "welcome").(*Welcome); !ok {
		t.Fatal("期望 welcome")
	}

	// 引擎转入 ready → 客户端应收到 status{state:"ready"}
	fb.state.Store(engine.StateReady)
	fb.emit(engine.Event{Kind: engine.EventState, State: engine.StateReady})
	st, ok := recv(t, ws, "status").(*Status)
	if !ok || st.Engine.State != engine.StateReady {
		t.Fatalf("期望 status(ready), got %+v", st)
	}
}

// startTestServerWithBridge 起带假引擎桥的测试服务器。
func startTestServerWithBridge(t *testing.T, fb *fakeBridge) (string, func()) {
	url, cleanup, _ := startTestServerWithBridgeS(t, fb)
	return url, cleanup
}

// startTestServerWithBridgeS 同上，另返回 Server 实例（需要注入回调的测试用）。
func startTestServerWithBridgeS(t *testing.T, fb *fakeBridge) (string, func(), *Server) {
	t.Helper()
	dir := t.TempDir()
	cert, err := EnsureCert(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Bind = "127.0.0.1:0"
	cfg.Token = testToken
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	srv := New(cfg, cert, logger, fb)
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
	// 单独启动引擎事件泵（Run 内同款；测试不走 Run 以便控制端口）
	ctx, cancel := context.WithCancel(context.Background())
	go srv.enginePump(ctx)
	cleanup := func() {
		cancel()
		_ = hs.Close()
	}
	t.Cleanup(cleanup)
	return "wss://" + ln.Addr().String() + "/ws", cleanup, srv
}

// TestServer_SetEngine 远程调算力（协议 §4.10）：合法帧逐字段应用并以最近
// 局面重新请求（新算力立即生效）；越界帧整帧拒绝（error{bad_frame}）且不改任何字段。
func TestServer_SetEngine(t *testing.T) {
	fb := newFakeBridge(engine.StateReady)
	fb.onRequest = replayNormalSearch
	url, _ := startTestServerWithBridge(t, fb)
	ws := dialWS(t, url)
	defer ws.Close()

	if _, ok := recv(t, ws, "服务器 hello").(*Hello); !ok {
		t.Fatal("首帧应为 hello")
	}
	_ = ws.WriteJSON(Hello{frameBase: frame(TypeHello), Client: ClientName, Ver: "0.1.0", Caps: []string{CapV1}})
	_ = ws.WriteJSON(Auth{frameBase: frame(TypeAuth), Token: testToken})
	if _, ok := recv(t, ws, "welcome").(*Welcome); !ok {
		t.Fatal("期望 welcome")
	}

	// 先上行一个局面（落 lastFen），再发合法 set_engine
	fen := "rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR w - - 0 1"
	_ = ws.WriteJSON(Position{frameBase: frame(TypePosition), Fen: fen, RedGo: true})
	waitFor(t, time.Second, func() bool {
		fb.mu.Lock()
		defer fb.mu.Unlock()
		return len(fb.fens) >= 1
	})
	d26, h512, th6, mp1 := 26, 512, 6, 1
	_ = ws.WriteJSON(SetEngine{frameBase: frame(TypeSetEngine), Depth: &d26, HashMB: &h512, Threads: &th6, MultiPV: &mp1})
	waitFor(t, time.Second, func() bool {
		return fb.Depth() == 26 && fb.HashMB() == 512 && fb.Threads() == 6 && fb.MultiPV() == 1
	})
	// 应用后以最近局面重新请求
	waitFor(t, time.Second, func() bool {
		fb.mu.Lock()
		defer fb.mu.Unlock()
		return len(fb.fens) >= 2 && fb.fens[len(fb.fens)-1] == fen
	})

	// 越界：整帧拒绝（回 error{bad_frame}），tuner 值不变
	d99 := 99
	_ = ws.WriteJSON(SetEngine{frameBase: frame(TypeSetEngine), Depth: &d99})
	_ = ws.SetReadDeadline(time.Now().Add(2 * time.Second))
	defer ws.SetReadDeadline(time.Time{})
	for {
		m := recv(t, ws, "error 回执")
		ef, ok := m.(*ErrorFrame)
		if !ok {
			continue // 跳过 set_engine 触发的 suggestion/status 帧
		}
		if ef.Code != CodeBadFrame {
			t.Fatalf("期望 error(bad_frame)，得到 %s（%s）", ef.Code, ef.Message)
		}
		break
	}
	if fb.Depth() != 26 || fb.HashMB() != 512 {
		t.Fatalf("越界帧不应改动任何字段: depth=%d hash=%d", fb.Depth(), fb.HashMB())
	}
}

// TestServer_RejectIllegalFEN 将帅照面等非法局面 → error(bad_frame)，
// 绝不转发引擎（皮卡鱼对非法局面可能异常退出）。
func TestServer_RejectIllegalFEN(t *testing.T) {
	fb := newFakeBridge(engine.StateReady)
	fb.onRequest = replayNormalSearch
	url, _ := startTestServerWithBridge(t, fb)
	ws := dialWS(t, url)

	if _, ok := recv(t, ws, "服务器 hello").(*Hello); !ok {
		t.Fatal("首帧应为 hello")
	}
	_ = ws.WriteJSON(Hello{frameBase: frame(TypeHello), Client: ClientName, Ver: "0.1.0", Caps: []string{CapV1}})
	_ = ws.WriteJSON(Auth{frameBase: frame(TypeAuth), Token: testToken})
	if _, ok := recv(t, ws, "welcome").(*Welcome); !ok {
		t.Fatal("期望 welcome")
	}

	// 将帅照面（同列 e 无隔子）
	_ = ws.WriteJSON(Position{frameBase: frame(TypePosition),
		Fen: "4k4/9/9/9/9/9/9/9/9/4K4 w - - 0 1", RedGo: true})
	ef, ok := recv(t, ws, "error").(*ErrorFrame)
	if !ok || ef.Code != CodeBadFrame {
		t.Fatalf("期望 error(bad_frame), got %+v", ef)
	}
	// 合法局面仍可正常走引擎
	_ = ws.WriteJSON(Position{frameBase: frame(TypePosition),
		Fen: "rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR w - - 0 1", RedGo: true})
	sug, ok := recv(t, ws, "suggestion").(*Suggestion)
	if !ok || sug.Source != "engine" {
		t.Fatalf("合法局面应走引擎: %+v", sug)
	}
}

// authClient 双连接测试用：完成 hello/auth/welcome 握手三步。
func authClient(t *testing.T, ws *websocket.Conn) {
	t.Helper()
	if _, ok := recv(t, ws, "服务器 hello").(*Hello); !ok {
		t.Fatal("首帧应为 hello")
	}
	_ = ws.WriteJSON(Hello{frameBase: frame(TypeHello), Client: ClientName, Ver: "0.1.0", Caps: []string{CapV1}})
	_ = ws.WriteJSON(Auth{frameBase: frame(TypeAuth), Token: testToken})
	if _, ok := recv(t, ws, "welcome").(*Welcome); !ok {
		t.Fatal("期望 welcome")
	}
}

// TestServer_SuggestionRoutedToOwner 建议路由（协议 §4.5）：A 发局面后
// suggestion 只定向发给 A；B 只能收到 status 广播，绝不能收到 A 局面的建议
// （多设备各下各的棋，串台会直接误导走子）。
func TestServer_SuggestionRoutedToOwner(t *testing.T) {
	fb := newFakeBridge(engine.StateReady)
	fb.onRequest = replayNormalSearch
	url, _ := startTestServerWithBridge(t, fb)

	wsA := dialWS(t, url)
	authClient(t, wsA)
	wsB := dialWS(t, url)
	authClient(t, wsB)

	fen := "rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR b - - 0 1"
	_ = wsA.WriteJSON(Position{frameBase: frame(TypePosition), Fen: fen, RedGo: false})

	// A：限时内必须收到 suggestion
	gotSug := false
	deadline := time.Now().Add(5 * time.Second)
	for !gotSug {
		if time.Now().After(deadline) {
			t.Fatal("A 未收到 suggestion（定向路由失效）")
		}
		_ = wsA.SetReadDeadline(time.Now().Add(3 * time.Second))
		_, data, err := wsA.ReadMessage()
		if err != nil {
			t.Fatalf("A 读帧失败: %v", err)
		}
		if m, perr := ParseFrame(data); perr == nil {
			if _, ok := m.(*Suggestion); ok {
				gotSug = true
			}
		}
	}

	// B：收帧 700ms（覆盖引擎重放窗口）——status 广播允许出现，suggestion 不允许
	_ = wsB.SetReadDeadline(time.Now().Add(700 * time.Millisecond))
	for {
		_, data, err := wsB.ReadMessage()
		if err != nil {
			break // 超时 = 窗口期内没有更多帧，符合预期
		}
		if m, perr := ParseFrame(data); perr == nil {
			if _, ok := m.(*Suggestion); ok {
				t.Fatalf("B 收到了 A 局面的建议（串台）: %s", data)
			}
		}
	}
}

// TestServer_SedimentHit 沉淀命中：桔库未查到、沉淀回调命中时直接回历史终值，
// 引擎桥不得收到请求（省一次重搜）；未命中时照常进引擎。
func TestServer_SedimentHit(t *testing.T) {
	fb := newFakeBridge(engine.StateReady)
	fb.onRequest = replayNormalSearch
	url, _, srv := startTestServerWithBridgeS(t, fb)
	cp150 := 150
	srv.SetSedimentLookup(func(fen string) (SuggestionRecord, bool) {
		if fen == "rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR b - - 0 1" {
			return SuggestionRecord{Source: "engine", FEN: fen, BestMove: "h2e2",
				ScoreCP: &cp150, PV: []string{"h2e2", "b9c7"}, Depth: 20}, true
		}
		return SuggestionRecord{}, false
	})

	ws := dialWS(t, url)
	authClient(t, ws)

	// 命中局面：回 suggestion，引擎零请求
	fenHit := "rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR b - - 0 1"
	_ = ws.WriteJSON(Position{frameBase: frame(TypePosition), Fen: fenHit, RedGo: false})
	var got *Suggestion
	deadline := time.Now().Add(3 * time.Second)
	for got == nil {
		if time.Now().After(deadline) {
			t.Fatal("沉淀命中未下发 suggestion")
		}
		_ = ws.SetReadDeadline(time.Now().Add(2 * time.Second))
		_, data, err := ws.ReadMessage()
		if err != nil {
			t.Fatalf("读帧失败: %v", err)
		}
		if m, perr := ParseFrame(data); perr == nil {
			if s, ok := m.(*Suggestion); ok {
				got = s
			}
		}
	}
	if got.Source != "engine" || got.BestMove != "h2e2" || got.Depth != 20 {
		t.Fatalf("沉淀命中字段不符: %+v", got)
	}
	if !got.Cached {
		t.Fatalf("沉淀命中应带 cached 标记: %+v", got)
	}
	if got.Score == nil || got.Score.CP == nil || *got.Score.CP != 150 {
		t.Fatalf("沉淀命中应携带历史终值分数: %+v", got)
	}
	fb.mu.Lock()
	engineReqs := len(fb.fens)
	fb.mu.Unlock()
	if engineReqs != 0 {
		t.Fatalf("命中时引擎不应被请求, got %d", engineReqs)
	}

	// 未命中局面：照常进引擎
	fenMiss := "rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR w - - 0 1"
	_ = ws.WriteJSON(Position{frameBase: frame(TypePosition), Fen: fenMiss, RedGo: true})
	waitFor(t, 2*time.Second, func() bool {
		fb.mu.Lock()
		defer fb.mu.Unlock()
		return len(fb.fens) >= 1
	})
}

// authClientID 握手并返回 welcome 的完整会话 ID（断开会话用例定位目标）。
func authClientID(t *testing.T, ws *websocket.Conn) string {
	t.Helper()
	if _, ok := recv(t, ws, "服务器 hello").(*Hello); !ok {
		t.Fatal("首帧应为 hello")
	}
	_ = ws.WriteJSON(Hello{frameBase: frame(TypeHello), Client: ClientName, Ver: "0.1.0", Caps: []string{CapV1}})
	_ = ws.WriteJSON(Auth{frameBase: frame(TypeAuth), Token: testToken})
	w, ok := recv(t, ws, "welcome").(*Welcome)
	if !ok {
		t.Fatal("期望 welcome")
	}
	return w.SessionID
}

// TestServer_ConsoleKickSession 控制台手动断开会话：目标连接被关闭（先 bye 后关闭），
// 其余会话不受影响；断开不存在的会话返回 false。
func TestServer_ConsoleKickSession(t *testing.T) {
	fb := newFakeBridge(engine.StateReady)
	fb.onRequest = replayNormalSearch
	url, _, srv := startTestServerWithBridgeS(t, fb)

	wsA := dialWS(t, url)
	idA := authClientID(t, wsA)
	wsB := dialWS(t, url)
	authClientID(t, wsB)

	// 断开不存在的会话：false
	if srv.ConsoleKick("nonexistent0000") {
		t.Fatal("不存在的会话应返回 false")
	}

	// 断开 A：true，且 A 的连接随后被关闭（bye 或读错误都算优雅关闭路径）
	if !srv.ConsoleKick(idA) {
		t.Fatal("断开已有会话应返回 true")
	}
	closedA := false
	deadline := time.Now().Add(3 * time.Second)
	for !closedA {
		if time.Now().After(deadline) {
			t.Fatal("A 的连接未被关闭")
		}
		_ = wsA.SetReadDeadline(time.Now().Add(2 * time.Second))
		_, data, err := wsA.ReadMessage()
		if err != nil {
			break // 读错误 = 连接已关
		}
		if m, perr := ParseFrame(data); perr == nil {
			if _, ok := m.(*Bye); ok {
				closedA = true // 先收到 bye 帧也算
			}
		}
	}

	// B 不受影响：发局面仍能收到 suggestion
	fen := "rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR b - - 0 1"
	_ = wsB.WriteJSON(Position{frameBase: frame(TypePosition), Fen: fen, RedGo: false})
	gotSug := false
	deadline = time.Now().Add(5 * time.Second)
	for !gotSug {
		if time.Now().After(deadline) {
			t.Fatal("B 受断开 A 影响（应照常工作）")
		}
		_ = wsB.SetReadDeadline(time.Now().Add(3 * time.Second))
		_, data, err := wsB.ReadMessage()
		if err != nil {
			t.Fatalf("B 读帧失败: %v", err)
		}
		if m, perr := ParseFrame(data); perr == nil {
			if _, ok := m.(*Suggestion); ok {
				gotSug = true
			}
		}
	}
}

// TestServer_ConsoleKickAsync 回归（A4）：Disconnect 的发送/关闭收尾必须在
// 独立 goroutine 执行——控制台在 fyne UI 线程同步调用 ConsoleKick，半死客户端
// 的 bye 写要耗满 3s 写超时、Close 控制帧再占 1s deadline，同步执行会把整个
// 界面冻结 3-4s。判据：占住连接写锁（确定性地模拟卡死的写路径）后 ConsoleKick
// 仍立即返回；释放写锁后收尾照常完成（bye/关闭语义不因异步而破坏）。
func TestServer_ConsoleKickAsync(t *testing.T) {
	fb := newFakeBridge(engine.StateReady)
	url, _, srv := startTestServerWithBridgeS(t, fb)

	ws := dialWS(t, url)
	authClientID(t, ws)

	// 包内测试直查 hub：取出唯一连接
	srv.hub.mu.Lock()
	var target *Conn
	for c := range srv.hub.conns {
		target = c
	}
	srv.hub.mu.Unlock()
	if target == nil {
		t.Fatal("hub 中应有活跃连接")
	}

	// 占住写锁：后续 bye 写只能阻塞在锁上（sendJSON 先取锁再写，卡点确定）
	target.writeMu.Lock()

	done := make(chan bool, 1)
	go func() { done <- srv.ConsoleKick(target.ID) }()
	select {
	case ok := <-done:
		if !ok {
			t.Fatal("断开已有会话应返回 true")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ConsoleKick 被发送路径同步阻塞（收尾应为异步）")
	}

	// 放行收尾：bye + Close 控制帧 + 关闭应随后完成，客户端读到连接关闭
	target.writeMu.Unlock()
	closed := false
	deadline := time.Now().Add(3 * time.Second)
	for !closed && time.Now().Before(deadline) {
		_ = ws.SetReadDeadline(time.Now().Add(1 * time.Second))
		if _, _, err := ws.ReadMessage(); err != nil {
			closed = true // 读错误 = 连接已关闭
		}
	}
	if !closed {
		t.Fatal("释放写锁后连接未被关闭（异步收尾未完成）")
	}
}

// TestServer_NoMovesHint 回归：引擎判定无着可走（绝杀/困毙，bestmove (none)
// 在 engine.go 归一为 "null"）不得静默——必须给归属会话下发 error 帧
// （code=no_moves + fen 回显），手机端状态条才能提示终局。2026-10-24 通信
// 故障排查：此前该路径全程静默（不下发/不沉淀/无日志），用户空等建议误判
// 通信故障（实锤局面 5k3/.../2BK5 w 真皮卡鱼瞬间回 bestmove (none)）。
func TestServer_NoMovesHint(t *testing.T) {
	fb := newFakeBridge(engine.StateReady)
	url, _ := startTestServerWithBridge(t, fb)
	ws := dialWS(t, url)

	if _, ok := recv(t, ws, "服务器 hello").(*Hello); !ok {
		t.Fatal("首帧应为 hello")
	}
	_ = ws.WriteJSON(Hello{frameBase: frame(TypeHello), Client: ClientName, Ver: "0.1.0", Caps: []string{CapV1}})
	_ = ws.WriteJSON(Auth{frameBase: frame(TypeAuth), Token: testToken})
	if _, ok := recv(t, ws, "welcome").(*Welcome); !ok {
		t.Fatal("期望 welcome")
	}

	// 2026-10-24 实锤的绝杀局面（黑双车锁死红九宫，轮红无合法着法）
	fen := "5k3/4a4/5a2b/p5p1p/9/P5P2/4P3P/B7c/3rr4/2BK5 w - - 0 1"
	_ = ws.WriteJSON(Position{frameBase: frame(TypePosition), Fen: fen, RedGo: true})
	time.Sleep(100 * time.Millisecond) // 等 handlePosition 落 lastFen/ownerSession（"最新覆盖"过滤依赖）
	// 引擎回无着可走终态（绕过 Request 脚本，直接 emit——与真皮卡鱼行为等价）
	fb.emit(engine.Event{Kind: engine.EventBestmove, Result: &engine.Result{
		Fen: fen, RedGo: true, BestMove: "null",
	}})

	// 应收到 no_moves 错误帧；收束 status 帧可能先到，循环滤过
	deadline := time.Now().Add(5 * time.Second)
	for {
		if time.Now().After(deadline) {
			t.Fatal("无着可走未收到 no_moves 提示帧（链路仍静默）")
		}
		_ = ws.SetReadDeadline(time.Now().Add(3 * time.Second))
		_, data, err := ws.ReadMessage()
		if err != nil {
			t.Fatalf("读帧失败: %v", err)
		}
		m, perr := ParseFrame(data)
		if perr != nil {
			continue
		}
		if ef, ok := m.(*ErrorFrame); ok {
			if ef.Code != CodeNoMoves {
				t.Fatalf("错误码应为 no_moves, got %q (%s)", ef.Code, ef.Message)
			}
			if ef.Fen != fen {
				t.Fatalf("错误帧应回显局面, got fen=%q", ef.Fen)
			}
			return
		}
	}
}

// TestServer_OwnerSessionClearedOnDisconnect 回归：会话断开（含 45s 判死、
// 手机重启）后 ownerSession 不得悬挂指向死会话——否则引擎算出的建议在
// SendToSession 静默丢弃，新会话连上后、发 position 前始终无建议可收
// （2026-10-24 通信故障排查缺口）。lastFen 不清：局面仍有效，新会话发
// position 或 reSearchLatest 照常用。
func TestServer_OwnerSessionClearedOnDisconnect(t *testing.T) {
	fb := newFakeBridge(engine.StateReady)
	url, cleanup, srv := startTestServerWithBridgeS(t, fb)
	defer cleanup()
	ws := dialWS(t, url)

	if _, ok := recv(t, ws, "服务器 hello").(*Hello); !ok {
		t.Fatal("首帧应为 hello")
	}
	_ = ws.WriteJSON(Hello{frameBase: frame(TypeHello), Client: ClientName, Ver: "0.1.0", Caps: []string{CapV1}})
	_ = ws.WriteJSON(Auth{frameBase: frame(TypeAuth), Token: testToken})
	if _, ok := recv(t, ws, "welcome").(*Welcome); !ok {
		t.Fatal("期望 welcome")
	}

	fen := "rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR w - - 0 1"
	_ = ws.WriteJSON(Position{frameBase: frame(TypePosition), Fen: fen, RedGo: true})
	time.Sleep(100 * time.Millisecond)
	if id, _ := srv.ownerSession.Load().(string); id == "" {
		t.Fatal("position 后 ownerSession 应已归属本会话")
	}

	// 断开：serveWS defer 应清空悬挂归属
	_ = ws.Close()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if id, _ := srv.ownerSession.Load().(string); id == "" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("会话断开后 ownerSession 仍悬挂: %v", srv.ownerSession.Load())
		}
		time.Sleep(20 * time.Millisecond)
	}
}
