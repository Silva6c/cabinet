package server

import (
	"context"
	"crypto/tls"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"cabinet/internal/book"
	"cabinet/internal/config"
	"cabinet/internal/engine"
	"cabinet/internal/xq"

	"github.com/gorilla/websocket"
)

// fakeBridge 假引擎桥：Request 时回放一段脚本化的引擎事件（info→bestmove）。
type fakeBridge struct {
	mu       sync.Mutex
	fens     []string
	histReqs []histReq
	pvReqs   []pvReq
	state    atomic.Value
	events   chan engine.Event
	// Tuner 状态（set_engine 测试）：值语义与 engine.Engine 一致（构造即有初值）
	depth      atomic.Int64
	moveTimeMS atomic.Int64
	hash       atomic.Int64
	threads    atomic.Int64
	multiPV    atomic.Int64
	smart      atomic.Bool
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

func (fb *fakeBridge) SetMoveTimeMS(ms int) bool {
	if ms != 0 && (ms < 500 || ms > 20000) {
		return false
	}
	fb.moveTimeMS.Store(int64(ms))
	return true
}

func (fb *fakeBridge) SetHashMB(mb int) bool {
	if mb == 0 {
		mb = 16384 // 自动档典型值
	} else if mb < 32 || mb > 10240 {
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

func (fb *fakeBridge) Depth() int      { return int(fb.depth.Load()) }
func (fb *fakeBridge) MoveTimeMS() int { return int(fb.moveTimeMS.Load()) }
func (fb *fakeBridge) HashMB() int     { return int(fb.hash.Load()) }
func (fb *fakeBridge) Threads() int    { return int(fb.threads.Load()) }
func (fb *fakeBridge) MultiPV() int    { return int(fb.multiPV.Load()) }

// SetSmart / Smart 智能思考开关（EngineTuner 接口 2026-11-02 新增成员）：
// 假桥存独立原子位，供 smart kind 的 apply/no-op 检测断言用
func (fb *fakeBridge) SetSmart(on bool) { fb.smart.Store(on) }
func (fb *fakeBridge) Smart() bool      { return fb.smart.Load() }

func (fb *fakeBridge) Request(fen string) {
	fb.mu.Lock()
	fb.fens = append(fb.fens, fen)
	fb.mu.Unlock()
	if fb.onRequest != nil {
		go fb.onRequest(fb, fen)
	}
}

// RequestHistory 实现 HistoryEngine（测试能力分流用）：记录 (起点, 终局面,
// moves) 三元组，不触发搜索应答。
func (fb *fakeBridge) RequestHistory(curFen, startFen string, moves []string) {
	fb.mu.Lock()
	fb.histReqs = append(fb.histReqs, histReq{curFen: curFen, startFen: startFen, moves: moves})
	fb.mu.Unlock()
}

// histReq 一次带历史请求的记录（RequestHistory 分流断言用）。
type histReq struct {
	curFen   string
	startFen string
	moves    []string
}

// RequestMultiPV / RequestHistoryMultiPV 实现 MultiPVOnceEngine（单次多线
// 分流断言用）：记录线数与（带历史时的）三元组，不触发搜索应答。
func (fb *fakeBridge) RequestMultiPV(fen string, pv int) {
	fb.mu.Lock()
	fb.pvReqs = append(fb.pvReqs, pvReq{fen: fen, pv: pv})
	fb.mu.Unlock()
}

func (fb *fakeBridge) RequestHistoryMultiPV(curFen, startFen string, moves []string, pv int) {
	fb.mu.Lock()
	fb.pvReqs = append(fb.pvReqs, pvReq{fen: curFen, startFen: startFen, moves: moves, pv: pv})
	fb.mu.Unlock()
}

// pvReq 一次单次多线请求的记录（RequestMultiPV 分流断言用；startFen 空 =
// 快照制式）。
type pvReq struct {
	fen      string
	startFen string
	moves    []string
	pv       int
}

func (fb *fakeBridge) emit(ev engine.Event) {
	fb.events <- ev
}

// replayBestFor 取该局面行棋方的合法着法（回放与断言共用同一枚举，保证回放
// 着法恒合法——归属守卫 2026-11-14 起对"终值着法对本局面非法"拒收不下发）。
// 红行棋开局盘面默认 h2e2（中炮，历史断言兼容），不合法时退回枚举首步。
func replayBestFor(fen string) string {
	if _, ok := xq.FenAfter(fen, "h2e2"); ok {
		return "h2e2"
	}
	bd, redGo, err := book.ParseFEN(fen)
	if err != nil {
		return "h2e2"
	}
	if ms := xq.LegalMoves(bd, redGo); len(ms) > 0 {
		return xq.UcciOf(ms[0])
	}
	return "h2e2"
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
	best := replayBestFor(fen)
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

// TestServer_ResultGuard_IllegalMove 归属守卫回归（2026-11-14 红队第二轮）：
// 打断排干漏窗的"旧结果贴新 FEN"两形态——中间 info 与 bestmove 终值——着法
// 对本局面非法（红方行棋局面里下发黑车着法 a9a8）时不得下发不沉淀。
// 守卫零误伤由既有 suggestion 测试群隐式回归（全部正常建议都过同一闸）。
// ⚠gorilla 读取在 i/o 超时后进入粘滞错误态，本测以超时退出收尾，不做后续读。
func TestServer_ResultGuard_IllegalMove(t *testing.T) {
	fb := newFakeBridge(engine.StateReady)
	url, _ := startTestServerWithBridge(t, fb)
	ws := dialWS(t, url)
	defer ws.Close()
	handshakeClient(t, ws)

	fen := "rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR w - - 0 1"
	_ = ws.WriteJSON(Position{frameBase: frame(TypePosition), Fen: fen, RedGo: true})
	time.Sleep(50 * time.Millisecond)

	// 中间 info：着法 a9a8（黑车着法，UCCI rank '9'=黑方底线）对红方行棋非法
	fb.emit(engine.Event{Kind: engine.EventInfo, Fen: fen, Info: &engine.Info{
		MultiPV: 1, Depth: 9, ScoreCP: 500, PV: []string{"a9a8", "b9c7"},
		Nodes: 999, NPS: 1000, Hashfull: 50,
	}})
	// 终值：Lines 非空但着法非法（排干漏窗的旧结果贴新 FEN 的可下发行）
	fb.emit(engine.Event{Kind: engine.EventBestmove, Result: &engine.Result{
		Fen: fen, RedGo: true, BestMove: "a9a8",
		Lines: []engine.Info{{MultiPV: 1, Depth: 9, ScoreCP: 500, PV: []string{"a9a8"}}},
	}})

	// 窗口内绝不允许出现 suggestion（status 收束帧等无关帧放行）
	assertNoSuggestion(t, ws, 700*time.Millisecond)
}

// TestServer_ResultGuard_EmptyLines 归属守卫回归：终值无 info 线（迟到回执被
// 贴新 FEN 的典型形态——零值 Info 会产出 depth=0/cp=0 建议并污染沉淀复播）
// 不下发不沉淀。守卫零误伤由既有 suggestion 测试群隐式回归。
func TestServer_ResultGuard_EmptyLines(t *testing.T) {
	fb := newFakeBridge(engine.StateReady)
	url, _ := startTestServerWithBridge(t, fb)
	ws := dialWS(t, url)
	defer ws.Close()
	handshakeClient(t, ws)

	fen := "rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR w - - 0 1"
	_ = ws.WriteJSON(Position{frameBase: frame(TypePosition), Fen: fen, RedGo: true})
	time.Sleep(50 * time.Millisecond)

	fb.emit(engine.Event{Kind: engine.EventBestmove, Result: &engine.Result{
		Fen: fen, RedGo: true, BestMove: "h2e2", Lines: nil,
	}})
	assertNoSuggestion(t, ws, 700*time.Millisecond)
}

// assertNoSuggestion 窗口内收到任何 suggestion 帧即失败（status/error 等无关帧放行）。
func assertNoSuggestion(t *testing.T, ws *websocket.Conn, window time.Duration) {
	t.Helper()
	_ = ws.SetReadDeadline(time.Now().Add(window))
	defer ws.SetReadDeadline(time.Time{})
	for {
		_, data, err := ws.ReadMessage()
		if err != nil {
			return // 超时退出：窗口内无 suggestion，通过
		}
		if m, perr := ParseFrame(data); perr == nil {
			if _, ok := m.(*Suggestion); ok {
				t.Fatalf("污染建议被下发: %s", data)
			}
		}
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
			if m.BestMove != replayBestFor(fen) {
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

// TestServer_SetEngineHashClamp hash_mb 产品档钳位回归（2026-11-14 红队第二轮）：
// 历史协议口径 32768 超产品档（控制台/手机端 10240）3.2 倍，一帧即 32GB 置换表
// 分配→readyok 超时看门狗重启循环且落盘持久化。服务端钳 10240：产品档上限
// 恰好接受，32768/10241 整帧拒绝。
func TestServer_SetEngineHashClamp(t *testing.T) {
	fb := newFakeBridge(engine.StateReady)
	url, _ := startTestServerWithBridge(t, fb)
	ws := dialWS(t, url)
	defer ws.Close()
	authClient(t, ws)

	// 10240=产品档上限：接受
	h10240 := 10240
	_ = ws.WriteJSON(SetEngine{frameBase: frame(TypeSetEngine), HashMB: &h10240})
	waitFor(t, time.Second, func() bool { return fb.HashMB() == 10240 })

	// 32768（历史口径上限）与 10241：整帧拒绝，tuner 值不变
	for _, bad := range []int{32768, 10241} {
		hbad := bad
		_ = ws.WriteJSON(SetEngine{frameBase: frame(TypeSetEngine), HashMB: &hbad})
		_ = ws.SetReadDeadline(time.Now().Add(2 * time.Second))
		var sawBadFrame bool
		for !sawBadFrame {
			m := recv(t, ws, "error 回执")
			ef, ok := m.(*ErrorFrame)
			if !ok {
				continue // 跳过广播 status 帧
			}
			if ef.Code != CodeBadFrame {
				t.Fatalf("hash_mb=%d 期望 error(bad_frame)，得到 %s（%s）", bad, ef.Code, ef.Message)
			}
			sawBadFrame = true
		}
		_ = ws.SetReadDeadline(time.Time{})
	}
	if fb.HashMB() != 10240 {
		t.Fatalf("越界帧不应改动 hash: %d", fb.HashMB())
	}
}

// TestServer_PositionMultiPVOnce 单次多线路由（协议 §4.4 multi_pv_once，
// 2026-11-09 双击小棋盘"单次三变解析"）：合法值隐含 force 直达引擎（pvReqs 记录 pv=3，
// 不经桔库/沉淀）；越界值按缺省处理走常规路径——两条服务端路由语义都在
// handlePosition 层钉住（引擎层越界降级另有单测）。
func TestServer_PositionMultiPVOnce(t *testing.T) {
	fb := newFakeBridge(engine.StateReady)
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

	fenRed := "rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR w - - 0 1"
	// 合法 multi_pv_once=3：隐含 force，直达引擎单次多线请求
	pv3 := 3
	_ = ws.WriteJSON(Position{frameBase: frame(TypePosition), Fen: fenRed, RedGo: true, MultiPVOnce: &pv3})
	waitFor(t, time.Second, func() bool {
		fb.mu.Lock()
		defer fb.mu.Unlock()
		return len(fb.pvReqs) >= 1
	})
	fb.mu.Lock()
	pr := fb.pvReqs[0]
	fb.mu.Unlock()
	if pr.pv != 3 || pr.fen != fenRed || pr.startFen != "" {
		t.Fatalf("合法 multi_pv_once 应直达引擎单次多线快照: %+v", pr)
	}

	// 越界 multi_pv_once=9：按缺省处理（Warn），退常规路径——引擎收普通快照
	fenBlack := "rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR b - - 0 1"
	pv9 := 9
	_ = ws.WriteJSON(Position{frameBase: frame(TypePosition), Fen: fenBlack, RedGo: false, MultiPVOnce: &pv9})
	waitFor(t, time.Second, func() bool {
		fb.mu.Lock()
		defer fb.mu.Unlock()
		return len(fb.fens) >= 1
	})
	fb.mu.Lock()
	n := len(fb.pvReqs)
	fb.mu.Unlock()
	if n != 1 {
		t.Fatalf("越界 multi_pv_once 不得走单次多线, got %d", n)
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
	// 命中键用红行棋开局盘面：沉淀行的 BestMove h2e2 只对该局面合法（归属守卫
	// 2026-11-14 起复播行同样过伪合法闸）——键选黑行棋盘面会被守卫正确拦下
	sedKey := "rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR w - - 0 1"
	srv.SetSedimentLookup(func(fen string) (SuggestionRecord, bool) {
		if fen == sedKey {
			return SuggestionRecord{Source: "engine", FEN: fen, BestMove: "h2e2",
				ScoreCP: &cp150, PV: []string{"h2e2", "b9c7"}, Depth: 20}, true
		}
		return SuggestionRecord{}, false
	})

	ws := dialWS(t, url)
	authClient(t, ws)

	// 命中局面：回 suggestion，引擎零请求
	fenHit := sedKey
	_ = ws.WriteJSON(Position{frameBase: frame(TypePosition), Fen: fenHit, RedGo: true})
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

	// 未命中局面：照常进引擎（取炮二平五后局面——与命中键同盘面会被 sediment
	// 命中，且须过结构门，用合法后继）
	fenMiss := "rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C2C4/9/RNBAKABNR b - - 0 1"
	_ = ws.WriteJSON(Position{frameBase: frame(TypePosition), Fen: fenMiss, RedGo: false})
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

// TestTrendMySide 我方执色（2026-10-29）：position.my_red 上报后，走势点记录
// 执色（控制台据此按我方视角画线）；旧手机不传时点标"未告知"、展示退回红方视角。
func TestTrendMySide(t *testing.T) {
	fb := newFakeBridge(engine.StateReady)
	fb.onRequest = replayNormalSearch
	url, cleanup, srv := startTestServerWithBridgeS(t, fb)
	defer cleanup()
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

	// 三个不同盘面（开局→红炮二平五→黑跳马）：2026-10-29 起同局面去重
	// （trendAppend 替换末点），2026-11-14 起去重键为盘面段——仅动 FEN 计数器
	// 不再构成不同局面，执色语义须在"不同盘面累加"的前提下验证
	fens := []string{
		"rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR w - - 0 1",
		"rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C2C4/9/RNBAKABNR b - - 0 1",
		"r1bakabnr/9/1cn4c1/p1p1p1p1p/9/9/P1P1P1P1P/1C2C4/9/RNBAKABNR w - - 0 1",
	}
	// 旧手机形态：不带 my_red → 点标记"未告知"（控制台展示红方视角）
	_ = ws.WriteJSON(Position{frameBase: frame(TypePosition), Fen: fens[0], RedGo: true})
	waitFor(t, 2*time.Second, func() bool { _, pts := srv.TrendSnapshotFor(""); return len(pts) == 1 })
	if _, pts := srv.TrendSnapshotFor(""); pts[0].MySideKnown {
		t.Fatalf("未上报 my_red 时点不应标记已知：%+v", pts[0])
	}

	// 执黑上报：后续点固化 MyRed=false + 已知
	black := false
	_ = ws.WriteJSON(Position{frameBase: frame(TypePosition), Fen: fens[1], RedGo: true, MyRed: &black})
	waitFor(t, 2*time.Second, func() bool { _, pts := srv.TrendSnapshotFor(""); return len(pts) == 2 })
	if _, pts := srv.TrendSnapshotFor(""); !pts[1].MySideKnown || pts[1].MyRed != false {
		t.Fatalf("执黑上报后点应为已知+执黑：%+v", pts[1])
	}

	// 同值不误报：换局面再报一次执黑，新点执色保持执黑
	_ = ws.WriteJSON(Position{frameBase: frame(TypePosition), Fen: fens[2], RedGo: true, MyRed: &black})
	waitFor(t, 2*time.Second, func() bool { _, pts := srv.TrendSnapshotFor(""); return len(pts) == 3 })
	if _, pts := srv.TrendSnapshotFor(""); !pts[2].MySideKnown || pts[2].MyRed != false {
		t.Fatalf("执色应持续保持执黑：%+v", pts[2])
	}
}

// TestTrendDedupSameFen 同局面去重（2026-10-29）：同一 FEN 重复评估（切算力参数
// reSearchLatest / 手机补发重搜）替换末点而非追加——曲线"每局面一个点"，切参数
// 不再产生波动锯齿；不同局面才累加。分组键任意（去重是组内行为）。
func TestTrendDedupSameFen(t *testing.T) {
	s := &Server{}
	s.trendAppend("d:test", TrendPoint{TsMs: 1, ScoreCP: 100, Fen: "fenA"})
	s.trendAppend("d:test", TrendPoint{TsMs: 2, ScoreCP: 130, Fen: "fenA"}) // 同局面重评估 → 替换
	s.trendAppend("d:test", TrendPoint{TsMs: 3, ScoreCP: -40, Fen: "fenB"}) // 新局面 → 追加
	s.trendAppend("d:test", TrendPoint{TsMs: 4, ScoreCP: -60, Fen: "fenB"}) // 同局面再评 → 替换
	if _, pts := s.TrendSnapshotFor("d:test"); len(pts) != 2 {
		t.Fatalf("同局面应去重为每局面一点，得 %d 点：%+v", len(pts), pts)
	}
	_, pts := s.TrendSnapshotFor("d:test")
	if pts[0].ScoreCP != 130 || pts[0].TsMs != 2 {
		t.Fatalf("末点应被最新评估替换：%+v", pts[0])
	}
	if pts[1].ScoreCP != -60 {
		t.Fatalf("第二局面应保持最新分：%+v", pts[1])
	}
}

// TestTrendPerDevice 走势按设备分组（2026-10-30 立项）：两台"手机"（hello 自报
// 不同 devid）各自发局面——序列独立互不混、焦点随最新 position 切换；同 devid
// 重连（新会话）曲线续接；TrendDevices 返回两组且标签带昵称与 devid 短码。
// 分组键取舍与信任模型见 trend.go 文件头。
func TestTrendPerDevice(t *testing.T) {
	fb := newFakeBridge(engine.StateReady)
	fb.onRequest = replayNormalSearch
	url, cleanup, srv := startTestServerWithBridgeS(t, fb)
	defer cleanup()

	// dialDevice 完整握手并自报设备身份（devid 须过 16-32 hex 校验）；
	// testToken 单一配对模型下两"设备"同 token——分组键只看 devid。
	const (
		devA = "aaaabbbbccccddddeeeeffff00001111"
		devB = "11112222333344445555666677778888"
	)
	dialDevice := func(devid, name string) *websocket.Conn {
		ws := dialWS(t, url)
		if _, ok := recv(t, ws, "服务器 hello").(*Hello); !ok {
			t.Fatal("首帧应为 hello")
		}
		_ = ws.WriteJSON(Hello{frameBase: frame(TypeHello), Client: ClientName, Ver: "0.1.0",
			Devid: devid, Name: name, Caps: []string{CapV1}})
		_ = ws.WriteJSON(Auth{frameBase: frame(TypeAuth), Token: testToken})
		if _, ok := recv(t, ws, "welcome").(*Welcome); !ok {
			t.Fatal("期望 welcome")
		}
		return ws
	}
	wsA := dialDevice(devA, "测试机A")
	defer wsA.Close()
	wsB := dialDevice(devB, "测试机B")
	defer wsB.Close()

	fenA := "rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR w - - 0 1"
	// fenA2 须为真不同盘面（2026-11-14 走势去重键=盘面段，仅动 FEN 计数器会与
	// fenA 折叠成一点）：炮二平五后局面。fenB 播种 B 设备的组（同盘面即可，
	// 去重是组内行为，不跨组）
	fenA2 := "rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C2C4/9/RNBAKABNR b - - 0 1"
	fenB := "rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR w - - 2 3"

	// A 先发：焦点 = A，序列只有 A 的点
	_ = wsA.WriteJSON(Position{frameBase: frame(TypePosition), Fen: fenA, RedGo: true})
	waitFor(t, 2*time.Second, func() bool {
		_, pts := srv.TrendSnapshotFor("")
		return len(pts) == 1 && pts[0].Fen == fenA
	})
	devs := srv.TrendDevices()
	if len(devs) != 1 || devs[0].Key != "d:"+devA {
		t.Fatalf("A 上报后应只有一组：%+v", devs)
	}

	// B 后发：焦点切到 B，B 组独立成线；A 组不受影响（关键语义：不再混一条线）
	_ = wsB.WriteJSON(Position{frameBase: frame(TypePosition), Fen: fenB, RedGo: true})
	waitFor(t, 2*time.Second, func() bool { return len(srv.TrendDevices()) == 2 })
	_, ptsFocus := srv.TrendSnapshotFor("") // 焦点 = 最新 position 来源 B
	if len(ptsFocus) != 1 || ptsFocus[0].Fen != fenB {
		t.Fatalf("焦点应随最新 position 切到 B：%+v", ptsFocus)
	}
	_, ptsA := srv.TrendSnapshotFor("d:" + devA)
	if len(ptsA) != 1 || ptsA[0].Fen != fenA {
		t.Fatalf("A 组曲线应独立保留：%+v", ptsA)
	}
	// 列表最近活跃在前：B 最新 → 第一项是 B；标签带昵称与 devid 短码
	devs = srv.TrendDevices()
	if devs[0].Key != "d:"+devB || !strings.Contains(devs[0].Label, "测试机B") {
		t.Fatalf("设备列表应最近活跃在前且带昵称：%+v", devs)
	}

	// A 同 devid 重连（新会话 ID）：曲线续接而非清零——分组键是设备不是会话
	_ = wsA.Close()
	// 断联即隐藏入口（2026-10-30 用户定稿）：A 掉线后设备列表只剩 B
	waitFor(t, 2*time.Second, func() bool { return len(srv.TrendDevices()) == 1 })
	// 组未删：断联期间曲线仍可按键回看
	if _, pts := srv.TrendSnapshotFor("d:" + devA); len(pts) != 1 {
		t.Fatalf("断联后组应保留供回看：%+v", pts)
	}
	wsA2 := dialDevice(devA, "测试机A")
	defer wsA2.Close()
	// 重连握手完成（尚未发局面）：设备在线即恢复按钮入口
	waitFor(t, 2*time.Second, func() bool { return len(srv.TrendDevices()) == 2 })
	_ = wsA2.WriteJSON(Position{frameBase: frame(TypePosition), Fen: fenA2, RedGo: false})
	waitFor(t, 2*time.Second, func() bool {
		_, pts := srv.TrendSnapshotFor("d:" + devA)
		return len(pts) == 2
	})
	_, ptsA2 := srv.TrendSnapshotFor("d:" + devA)
	if ptsA2[0].Fen != fenA || ptsA2[1].Fen != fenA2 {
		t.Fatalf("重连后曲线应续接（旧点保留 + 新点追加）：%+v", ptsA2)
	}
}

// TestTrendSeries 走势序列（2026-10-28 定稿）：引擎终态逐点追加；NEW_GAME
// 重置换段——覆盖定稿情形 A（新开局清零）与 C（撤离重开由 NEW_GAME 兜住）。
func TestTrendSeries(t *testing.T) {
	fb := newFakeBridge(engine.StateReady)
	fb.onRequest = replayNormalSearch
	url, cleanup, srv := startTestServerWithBridgeS(t, fb)
	defer cleanup()
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

	snap := func() (string, []TrendPoint) { return srv.TrendSnapshotFor("") }
	fen := "rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR w - - 0 1"
	_ = ws.WriteJSON(Position{frameBase: frame(TypePosition), Fen: fen, RedGo: true})
	waitFor(t, 2*time.Second, func() bool { _, pts := snap(); return len(pts) == 1 })
	game, pts := snap()
	if pts[0].ScoreCP != 120 || pts[0].WinRate != -1 {
		t.Fatalf("走势点=%+v, want 红方 +120 且无 WDL(-1)", pts[0])
	}
	if game == "" {
		t.Log("旧手机形态：无 new_game 标记，对局 ID 空（曲线仍累积）")
	}

	// A/C：NEW_GAME → 换段（清零后重搜立刻补新点；若未重置这里会是 2 点）
	oldGame, _ := snap()
	_ = ws.WriteJSON(Position{frameBase: frame(TypePosition), Fen: fen, RedGo: true, NewGame: true})
	waitFor(t, 2*time.Second, func() bool {
		g, pts := snap()
		return g != oldGame && len(pts) == 1
	})
}

// TestServer_TurnStuckReanchor 同侧单步重锚（2026-11-10 加强轮）：识别端轮次
// 卡旧值产出"子已走、行棋方没翻"的合法 FEN——回 bad_position{turn_flip_legal}
// 自愈回环（手机翻转重发，fen 回显/建议过滤一致性保住），引擎不收错轮次帧；
// "prev 侧卡旧值"形态（移动子属当前行棋方，cur 反而正确）不干预照常接受。
func TestServer_TurnStuckReanchor(t *testing.T) {
	fb := newFakeBridge(engine.StateReady)
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

	start := "rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR w - - 0 1"
	prevFen, ok1 := xq.FenAfter(start, "h2e2") // 红炮平中 → 黑回合（正确轮次）
	if !ok1 {
		t.Fatal("FenAfter 推演失败")
	}
	_ = ws.WriteJSON(Position{frameBase: frame(TypePosition), Fen: prevFen, RedGo: false})
	waitFor(t, time.Second, func() bool {
		fb.mu.Lock()
		defer fb.mu.Unlock()
		return len(fb.fens) >= 1
	})

	// 形态 A：黑走完但侧卡在 b（与 prev 同侧、单步可解释、移动子属 prev 侧黑）
	curRaw, ok2 := xq.FenAfter(prevFen, "b9c7")
	if !ok2 {
		t.Fatal("FenAfter 推演失败")
	}
	curStuck := strings.Replace(curRaw, " w - - 0 1", " b - - 0 1", 1)
	if curStuck == curRaw {
		t.Fatal("行棋方改写失败，用例无效")
	}
	_ = ws.WriteJSON(Position{frameBase: frame(TypePosition), Fen: curStuck, RedGo: false})
	_ = ws.SetReadDeadline(time.Now().Add(2 * time.Second))
	ef, ok := recv(t, ws, "重锚 error").(*ErrorFrame)
	if !ok || ef.Code != CodeBadPosition || !ef.TurnFlipLegal {
		t.Fatalf("卡旧值帧应回 bad_position{turn_flip_legal=true}: %+v", ef)
	}
	fb.mu.Lock()
	n := len(fb.fens)
	fb.mu.Unlock()
	if n != 1 {
		t.Fatalf("卡旧值帧不得进引擎, got %d", n)
	}

	// 手机翻转重发正确帧 → 正常进引擎
	_ = ws.WriteJSON(Position{frameBase: frame(TypePosition), Fen: curRaw, RedGo: true})
	waitFor(t, time.Second, func() bool {
		fb.mu.Lock()
		defer fb.mu.Unlock()
		return len(fb.fens) >= 2 && fb.fens[len(fb.fens)-1] == curRaw
	})

	// 形态 B（2026-11-10 vc47 起转结构门管辖）：prev 侧卡旧值重放帧=同侧
	// 2 格差（相对已接受的 curRaw 是"回退"）——重锚不触发（移动子属当前
	// 行棋方），结构门按"同侧 2 格差不可能"拒绝回 bad_frame 自愈重探。
	// （悔棋的真实回退是翻转帧还原形态，PlausiblePair 放行，不受影响）
	prevStuck := strings.Replace(prevFen, " b - - 0 1", " w - - 0 1", 1)
	_ = ws.WriteJSON(Position{frameBase: frame(TypePosition), Fen: prevStuck, RedGo: true})
	_ = ws.SetReadDeadline(time.Now().Add(2 * time.Second))
	ef2, ok2 := recv(t, ws, "形态 B error").(*ErrorFrame)
	if !ok2 || ef2.Code != CodeBadFrame {
		t.Fatalf("形态 B 同侧 2 格差应回 bad_frame: %+v", ef2)
	}
	_ = ws.SetReadDeadline(time.Time{})
	fb.mu.Lock()
	n2 := len(fb.fens)
	fb.mu.Unlock()
	if n2 != 2 {
		t.Fatalf("形态 B 帧不得进引擎, got %d", n2)
	}
	// 同局面重发（fen==prev）跳过一切防线，照常进引擎（补发/重搜语义）
	_ = ws.WriteJSON(Position{frameBase: frame(TypePosition), Fen: curRaw, RedGo: true})
	waitFor(t, time.Second, func() bool {
		fb.mu.Lock()
		defer fb.mu.Unlock()
		return len(fb.fens) >= 3 && fb.fens[len(fb.fens)-1] == curRaw
	})
}

// TestServer_StructureGate 回合结构防线（2026-11-10 优势崩塌归因）：不可能
// 局面（幽灵帧"吃自家子"形状）回 bad_frame 自愈重探且不进引擎；manual/force
// 跳过（手动编辑语义/显式重分析意图）；合法两步合并照常接受。
func TestServer_StructureGate(t *testing.T) {
	fb := newFakeBridge(engine.StateReady)
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

	ghostPrev := "3rkab2/4a2R1/4bc3/p1R1C4/1c3rpnp/9/P4C2P/2N1B4/4A4/2BAK4 w - - 0 1"
	ghostCur := "3rkab2/4a2R1/4br3/p1R1C4/1c4pnp/9/P4C2P/2N1B4/4A4/2BAK4 w - - 0 1"
	_ = ws.WriteJSON(Position{frameBase: frame(TypePosition), Fen: ghostPrev, RedGo: true})
	waitFor(t, time.Second, func() bool {
		fb.mu.Lock()
		defer fb.mu.Unlock()
		return len(fb.fens) >= 1
	})

	// 幽灵帧（同侧 2 格差=吃自家子伪非法形状）→ bad_frame，不进引擎
	_ = ws.WriteJSON(Position{frameBase: frame(TypePosition), Fen: ghostCur, RedGo: true})
	_ = ws.SetReadDeadline(time.Now().Add(2 * time.Second))
	ef, ok := recv(t, ws, "结构门 error").(*ErrorFrame)
	if !ok || ef.Code != CodeBadFrame {
		t.Fatalf("幽灵帧应回 bad_frame: %+v", ef)
	}
	fb.mu.Lock()
	n := len(fb.fens)
	fb.mu.Unlock()
	if n != 1 {
		t.Fatalf("结构门拒绝帧不得进引擎, got %d", n)
	}

	// manual 跳过：同一幽灵帧带手动标记 → 照常接受（手动编辑语义）
	_ = ws.WriteJSON(Position{frameBase: frame(TypePosition), Fen: ghostCur, RedGo: true, Manual: true})
	waitFor(t, time.Second, func() bool {
		fb.mu.Lock()
		defer fb.mu.Unlock()
		return len(fb.fens) >= 2 && fb.fens[len(fb.fens)-1] == ghostCur
	})

	// force 跳过：显式重分析意图不拦
	_ = ws.WriteJSON(Position{frameBase: frame(TypePosition), Fen: ghostPrev, RedGo: true, Force: true})
	waitFor(t, time.Second, func() bool {
		fb.mu.Lock()
		defer fb.mu.Unlock()
		return len(fb.fens) >= 3 && fb.fens[len(fb.fens)-1] == ghostPrev
	})
}

// TestServer_StructureGate_MergePass 合法两步合并（PC:41454 真实帧）照常接受：
// 结构门只拒棋理不可能的过渡，正常兑子两回合并帧（cells=4、子数差 -1）放行。
func TestServer_StructureGate_MergePass(t *testing.T) {
	fb := newFakeBridge(engine.StateReady)
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

	mergePrev := "1r2kab2/4a4/4br1cn/p1nRp1p2/8p/2c1P1P2/PC6P/4C4/N8/2BAKABR1 w - - 0 1"
	mergeCur := "1r2kab2/4a4/4br1cn/p1R1p1p2/2c5p/4P1P2/PC6P/4C4/N8/2BAKABR1 w - - 0 1"
	_ = ws.WriteJSON(Position{frameBase: frame(TypePosition), Fen: mergePrev, RedGo: true})
	waitFor(t, time.Second, func() bool {
		fb.mu.Lock()
		defer fb.mu.Unlock()
		return len(fb.fens) >= 1
	})
	_ = ws.WriteJSON(Position{frameBase: frame(TypePosition), Fen: mergeCur, RedGo: true})
	waitFor(t, time.Second, func() bool {
		fb.mu.Lock()
		defer fb.mu.Unlock()
		return len(fb.fens) >= 2 && fb.fens[len(fb.fens)-1] == mergeCur
	})
}

// TestServer_StructureGate_Heal 结构门自愈放行（2026-11-12 断供治理）：同一
// prev 连续第 2 次拒按"识别自愈"放行——第 1 次拒挡住幻影当下出招，手机重发
// 对账/重探后仍矛盾时放行防死锁（此前断供 4.4~27.1s 全靠手动"更新"）；
// force 帧接受时预置计数=1（force 盘面绕过结构门可信度未知），下一帧矛盾即
// 免费自纠直接放行（#7/#12 残缺盘面死锁治理）。
func TestServer_StructureGate_Heal(t *testing.T) {
	fb := newFakeBridge(engine.StateReady)
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

	ghostPrev := "3rkab2/4a2R1/4bc3/p1R1C4/1c3rpnp/9/P4C2P/2N1B4/4A4/2BAK4 w - - 0 1"
	ghostCur := "3rkab2/4a2R1/4br3/p1R1C4/1c4pnp/9/P4C2P/2N1B4/4A4/2BAK4 w - - 0 1"
	_ = ws.WriteJSON(Position{frameBase: frame(TypePosition), Fen: ghostPrev, RedGo: true})
	waitFor(t, time.Second, func() bool {
		fb.mu.Lock()
		defer fb.mu.Unlock()
		return len(fb.fens) >= 1
	})

	// 第 1 次拒 → bad_frame，不进引擎
	_ = ws.WriteJSON(Position{frameBase: frame(TypePosition), Fen: ghostCur, RedGo: true})
	_ = ws.SetReadDeadline(time.Now().Add(2 * time.Second))
	if ef, ok := recv(t, ws, "结构门第 1 次拒绝").(*ErrorFrame); !ok || ef.Code != CodeBadFrame {
		t.Fatalf("第 1 次矛盾帧应回 bad_frame: %+v", ef)
	}
	fb.mu.Lock()
	n := len(fb.fens)
	fb.mu.Unlock()
	if n != 1 {
		t.Fatalf("第 1 次拒绝帧不得进引擎, got %d", n)
	}

	// 第 2 次拒同一 prev（模拟手机重发对账）→ 按识别自愈放行，进引擎
	_ = ws.WriteJSON(Position{frameBase: frame(TypePosition), Fen: ghostCur, RedGo: true})
	waitFor(t, time.Second, func() bool {
		fb.mu.Lock()
		defer fb.mu.Unlock()
		return len(fb.fens) >= 2 && fb.fens[len(fb.fens)-1] == ghostCur
	})

	// force 预置：force 帧接受后回退到 ghostPrev，下一帧矛盾直接放行（免费自纠）
	_ = ws.WriteJSON(Position{frameBase: frame(TypePosition), Fen: ghostPrev, RedGo: true, Force: true})
	waitFor(t, time.Second, func() bool {
		fb.mu.Lock()
		defer fb.mu.Unlock()
		return len(fb.fens) >= 3 && fb.fens[len(fb.fens)-1] == ghostPrev
	})
	_ = ws.WriteJSON(Position{frameBase: frame(TypePosition), Fen: ghostCur, RedGo: true})
	waitFor(t, time.Second, func() bool {
		fb.mu.Lock()
		defer fb.mu.Unlock()
		return len(fb.fens) >= 4 && fb.fens[len(fb.fens)-1] == ghostCur
	})
}

// ---- 引擎终值三次重复规避（2026-11-15 实战七问：15:56 引擎直搜循环判和）----

// recvSuggestion 扫描到达帧流，取第一条满足 pred 的 suggestion（status 广播
// 等杂帧穿插跳过；读超时即 Fatal——以收到目标帧为终点）。
func recvSuggestion(t *testing.T, ws *websocket.Conn, what string, pred func(*Suggestion) bool) *Suggestion {
	t.Helper()
	_ = ws.SetReadDeadline(time.Now().Add(3 * time.Second))
	for {
		_, data, err := ws.ReadMessage()
		if err != nil {
			t.Fatalf("等待 %s 失败: %v", what, err)
		}
		m, perr := ParseFrame(data)
		if perr != nil {
			continue
		}
		if sug, ok := m.(*Suggestion); ok && pred(sug) {
			return sug
		}
	}
}

// repAvoidSeed 布景：发一局面 → 等会话历史建账 → 注入 occ（出现序列），
// 使 fen 走 h2e2 的落局面 Q1 已出现 2 次（pump 判定三次重复的前提）。
// 直接注入 occ 是同包测试缝——真实链路里 occ 由 notePositionFen 从连续
// 局面自攒（见 history_test.go 的链路级回归）。
func repAvoidSeed(t *testing.T, srv *Server, ws *websocket.Conn, fen string) {
	t.Helper()
	_ = ws.WriteJSON(Position{frameBase: frame(TypePosition), Fen: fen, RedGo: true})
	var sid string
	deadline := time.After(2 * time.Second)
	for {
		srv.histMu.Lock()
		for id := range srv.hist {
			sid = id
			break
		}
		srv.histMu.Unlock()
		if sid != "" {
			break
		}
		select {
		case <-deadline:
			t.Fatal("会话历史未建立")
		case <-time.After(10 * time.Millisecond):
		}
	}
	Q1, ok := xq.FenAfter(fen, "h2e2")
	if !ok {
		t.Fatal("h2e2 推演失败")
	}
	srv.histMu.Lock()
	srv.hist[sid] = &sessionHistory{fens: []string{fen}, occ: []string{Q1, fen, Q1}}
	srv.histMu.Unlock()
}

// waitPVReq 等待替代重搜请求（RequestMultiPV）到位并返回线数。
func waitPVReq(t *testing.T, fb *fakeBridge, want int) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		fb.mu.Lock()
		n := len(fb.pvReqs)
		var last pvReq
		if n > 0 {
			last = fb.pvReqs[n-1]
		}
		fb.mu.Unlock()
		if n >= want {
			_ = last
			return
		}
		select {
		case <-deadline:
			t.Fatalf("替代重搜未发起, pvReqs=%d", n)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// waitRepAvoidClear 等待 repAvoid 状态机清障（pump 单 goroutine 专属字段，
// 测试只读断言；门禁 go test 不带 -race）。
func waitRepAvoidClear(t *testing.T, srv *Server) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for srv.repAvoidFen != "" {
		select {
		case <-deadline:
			t.Fatalf("repAvoid 未清障: %q", srv.repAvoidFen)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// TestServer_RepAvoidAlternativeResearched 引擎终值三次重复规避主流程
// （2026-11-15 实战七问；2026-11-17 惰性+追加式修订）：终值着法走着即第三次
// 重复 → 原值带 rep_risk 照发（即时警示不等重搜）+ 替代重搜挂起，停留窗口
// （测试注入 200ms）到点才发起 4 线重搜；收束后追加式下发——主选 h2e2 保持
// multipv=1 且仍带 rep_risk 不变脸，替代线 b2e2 原序落 multipv=2；状态机
// 消费即清障，清障后普通终值不得再次触发重搜（防重搜风暴）。
func TestServer_RepAvoidAlternativeResearched(t *testing.T) {
	oldDelay := repAvoidDelay
	repAvoidDelay = 200 * time.Millisecond
	defer func() { repAvoidDelay = oldDelay }()

	fb := newFakeBridge(engine.StateReady)
	url, cleanup, srv := startTestServerWithBridgeS(t, fb)
	defer cleanup()
	ws := dialWS(t, url)
	defer ws.Close()
	handshakeClient(t, ws)

	fen := "rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR w - - 0 1"
	repAvoidSeed(t, srv, ws, fen)

	// 引擎终值（best=h2e2，occ 中其落局面已 2 次）：只发 bestmove 事件
	//（info 路径无重复判定，避开干扰帧）
	fb.emit(engine.Event{Kind: engine.EventBestmove, Result: &engine.Result{
		Fen: fen, RedGo: true, BestMove: "h2e2",
		Lines: []engine.Info{{MultiPV: 1, Depth: 8, ScoreCP: 5, PV: []string{"h2e2", "b9c7"}}},
	}})
	_ = recvSuggestion(t, ws, "rep_risk 终值", func(g *Suggestion) bool {
		return g.BestMove == "h2e2" && g.RepRisk
	})

	// 惰性：警示已发但窗口未到点，重搜不得提前发起
	time.Sleep(80 * time.Millisecond)
	fb.mu.Lock()
	nEarly := len(fb.pvReqs)
	fb.mu.Unlock()
	if nEarly != 0 {
		t.Fatalf("停留窗口内不得提前重搜（惰性触发）, pvReqs=%d", nEarly)
	}
	waitPVReq(t, fb, 1)
	if pr := fb.pvReqs[0]; pr.pv != 4 || pr.fen != fen {
		t.Fatalf("替代重搜应为 4 线同局面: %+v", pr)
	}

	// 替代重搜收束：追加式——主选 h2e2 保持 rank1+警示，b2e2 原序 rank2 追加
	fb.emit(engine.Event{Kind: engine.EventBestmove, Result: &engine.Result{
		Fen: fen, RedGo: true, BestMove: "h2e2",
		Lines: []engine.Info{
			{MultiPV: 1, Depth: 20, ScoreCP: 5, PV: []string{"h2e2", "b9c7"}},
			{MultiPV: 2, Depth: 20, ScoreCP: 2, PV: []string{"b2e2", "h0g2"}},
		},
	}})
	sug2 := recvSuggestion(t, ws, "主选保持", func(g *Suggestion) bool {
		return g.BestMove == "h2e2" && g.Multipv == 1 && g.RepRisk
	})
	_ = sug2
	sug3 := recvSuggestion(t, ws, "替代线追加", func(g *Suggestion) bool {
		return g.BestMove == "b2e2" && g.Multipv == 2 && !g.RepRisk
	})
	_ = sug3
	waitRepAvoidClear(t, srv)

	// 状态已清：后续同局面普通终值照常下发，不得再次触发重搜
	fb.emit(engine.Event{Kind: engine.EventBestmove, Result: &engine.Result{
		Fen: fen, RedGo: true, BestMove: "b2e2",
		Lines: []engine.Info{{MultiPV: 1, Depth: 20, ScoreCP: 2, PV: []string{"b2e2"}}},
	}})
	sug4 := recvSuggestion(t, ws, "清障后终值", func(g *Suggestion) bool {
		return g.BestMove == "b2e2" && g.Multipv == 1 && !g.RepRisk
	})
	_ = sug4
	fb.mu.Lock()
	n := len(fb.pvReqs)
	fb.mu.Unlock()
	if n != 1 {
		t.Fatalf("清障后不得再次触发重搜（防风暴）, pvReqs=%d", n)
	}
}

// TestServer_RepAvoidAllLinesRepeat 替代重搜全重复兜底（2026-11-15；2026-11-17
// 追加式修订）：全部线的首着都落向已二现局面 → 主选（rank1）保留 rep_risk
// 照发、其余重复线照常追加候选（不静默，红队第三条路："第 3 次强制变招"以
// 不变脸+不静默为底线），且 startRepAvoid 同局面同代次防重入——不得发起
// 第二次重搜。
func TestServer_RepAvoidAllLinesRepeat(t *testing.T) {
	oldDelay := repAvoidDelay
	repAvoidDelay = 200 * time.Millisecond
	defer func() { repAvoidDelay = oldDelay }()

	fb := newFakeBridge(engine.StateReady)
	url, cleanup, srv := startTestServerWithBridgeS(t, fb)
	defer cleanup()
	ws := dialWS(t, url)
	defer ws.Close()
	handshakeClient(t, ws)

	fen := "rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR w - - 0 1"
	repAvoidSeed(t, srv, ws, fen)
	// 追加 b2e2 的落局面也二现（全部线重复的布景）
	B2, ok := xq.FenAfter(fen, "b2e2")
	if !ok {
		t.Fatal("b2e2 推演失败")
	}
	srv.histMu.Lock()
	sid := ""
	for id := range srv.hist {
		sid = id
	}
	srv.hist[sid] = &sessionHistory{fens: []string{fen}, occ: []string{Q1Of(fen), fen, Q1Of(fen), B2, B2}}
	srv.histMu.Unlock()

	fb.emit(engine.Event{Kind: engine.EventBestmove, Result: &engine.Result{
		Fen: fen, RedGo: true, BestMove: "h2e2",
		Lines: []engine.Info{{MultiPV: 1, Depth: 8, ScoreCP: 5, PV: []string{"h2e2", "b9c7"}}},
	}})
	sug1 := recvSuggestion(t, ws, "rep_risk 终值", func(g *Suggestion) bool {
		return g.BestMove == "h2e2" && g.RepRisk
	})
	_ = sug1
	waitPVReq(t, fb, 1)

	// 替代重搜收束：两线首着都重复 → 保留原终值，不再重搜
	fb.emit(engine.Event{Kind: engine.EventBestmove, Result: &engine.Result{
		Fen: fen, RedGo: true, BestMove: "h2e2",
		Lines: []engine.Info{
			{MultiPV: 1, Depth: 20, ScoreCP: 5, PV: []string{"h2e2", "b9c7"}},
			{MultiPV: 2, Depth: 20, ScoreCP: 2, PV: []string{"b2e2", "h0g2"}},
		},
	}})
	sug2 := recvSuggestion(t, ws, "全重复保留原值", func(g *Suggestion) bool {
		return g.BestMove == "h2e2" && g.RepRisk && g.Multipv == 1
	})
	_ = sug2
	waitRepAvoidClear(t, srv)
	fb.mu.Lock()
	n := len(fb.pvReqs)
	fb.mu.Unlock()
	if n != 1 {
		t.Fatalf("全重复兜底不得发起第二次重搜（防风暴）, pvReqs=%d", n)
	}
}

// TestServer_RepAvoidLazyCancelledByMove 惰性取消（2026-11-17 用户拍板）：
// 警示照发后、停留窗口到点前用户已走子（新局面接受 → posGen 递增 → 代次
// 不符，他局面终值同时清障）——到点校验不过，替代重搜不得发起。这是"快
// 节奏对局零打扰零清 TT"承诺的回归锚。
func TestServer_RepAvoidLazyCancelledByMove(t *testing.T) {
	oldDelay := repAvoidDelay
	repAvoidDelay = 300 * time.Millisecond
	defer func() { repAvoidDelay = oldDelay }()

	fb := newFakeBridge(engine.StateReady)
	url, cleanup, srv := startTestServerWithBridgeS(t, fb)
	defer cleanup()
	ws := dialWS(t, url)
	defer ws.Close()
	handshakeClient(t, ws)

	fen := "rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR w - - 0 1"
	repAvoidSeed(t, srv, ws, fen)

	fb.emit(engine.Event{Kind: engine.EventBestmove, Result: &engine.Result{
		Fen: fen, RedGo: true, BestMove: "h2e2",
		Lines: []engine.Info{{MultiPV: 1, Depth: 8, ScoreCP: 5, PV: []string{"h2e2"}}},
	}})
	_ = recvSuggestion(t, ws, "rep_risk 终值", func(g *Suggestion) bool {
		return g.BestMove == "h2e2" && g.RepRisk
	})

	// 窗口内用户走子：红 h2e2 + 黑 b9c7 → 新局面（posGen 递增）
	Q1 := Q1Of(fen)
	fen2, ok := xq.FenAfter(Q1, "b9c7")
	if !ok {
		t.Fatal("b9c7 推演失败")
	}
	_ = ws.WriteJSON(Position{frameBase: frame(TypePosition), Fen: fen2, RedGo: true})
	// 等新局面被接受（lastFen 对齐）再发终值——否则 pump 门控会把 fen2
	// 终值当旧结果丢弃，测不到"走子取消"路径
	deadline := time.After(2 * time.Second)
	for {
		if want, _ := srv.lastFen.Load().(string); want == fen2 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("新局面未被接受")
		case <-time.After(10 * time.Millisecond):
		}
	}
	// 新局面终值到达（他局面 → 清障点"局面已切换"）
	fb.emit(engine.Event{Kind: engine.EventBestmove, Result: &engine.Result{
		Fen: fen2, RedGo: true, BestMove: "b0c2",
		Lines: []engine.Info{{MultiPV: 1, Depth: 8, ScoreCP: 5, PV: []string{"b0c2"}}},
	}})
	_ = recvSuggestion(t, ws, "新局面终值", func(g *Suggestion) bool {
		return g.Fen == fen2 && g.BestMove == "b0c2" && !g.RepRisk
	})
	waitRepAvoidClear(t, srv)

	// 原窗口早已过点：重搜不得发起（快节奏走子零打扰）
	time.Sleep(500 * time.Millisecond)
	fb.mu.Lock()
	n := len(fb.pvReqs)
	fb.mu.Unlock()
	if n != 0 {
		t.Fatalf("走子后替代重搜必须取消（惰性触发）, pvReqs=%d", n)
	}
}

// Q1Of 局面 fen 走 h2e2 的落局面（repAvoidSeed/全重复布景共用）。
func Q1Of(fen string) string {
	q, _ := xq.FenAfter(fen, "h2e2")
	return q
}
