package server

import (
	"sync"
	"testing"
	"time"

	"cabinet/internal/engine"
)

// 采纳比对测试夹具：P1（开局红行棋，建议 h2e2 中炮）→ 实际走红走不同着法
// 的后续局面。FEN 与着法链均与 internal/xq 的测试同源（已过双端核对）。
const (
	adoptP1     = "rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR w - - 0 1"
	adoptP2Can  = "rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/13C2C1/9/RNBAKABNR b - - 0 1" // 红走 b2e2（h2 炮仍在）
	adoptP2Miss = "rnbakabnr/9/1c5c1/p1p1p1p1p/9/4P4/P1P3P1P/1C5C1/9/RNBAKABNR b - - 0 1"  // 红走 e3e4（行6 x4 空、行5 x4 P）
	adoptP2Prim = "rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C2C4/9/RNBAKABNR b - - 0 1"  // 红走 h2e2
)

// anchor 构造锚点：P1 已下发主选 + 可选候选（模拟引擎 MultiPV/桔库候选唤醒）。
func anchor(moves map[string]int) *pendingAdvice {
	return &pendingAdvice{fen: adoptP1, moves: moves, source: "engine", depth: 20}
}

// collect 注册事件收集器（测试单 goroutine 串行调用，无锁字段安全）。
func collect(srv *Server) *[]AdoptionRecord {
	got := &[]AdoptionRecord{}
	srv.onAdoption = func(r AdoptionRecord) { *got = append(*got, r) }
	return got
}

// TestEvaluateAdoption_Primary 实际走子 == 建议主选 → primary。
func TestEvaluateAdoption_Primary(t *testing.T) {
	srv := &Server{}
	srv.lastFen.Store(adoptP1)
	srv.pendingSug.Store(anchor(map[string]int{"h2e2": 1, "b2e2": 2, "h0g2": 3}))
	got := collect(srv)

	srv.evaluateAdoption(adoptP2Prim)
	if len(*got) != 1 {
		t.Fatalf("应产出 1 条事件, got %d", len(*got))
	}
	e := (*got)[0]
	if e.MatchKind != "primary" || e.Actual != "h2e2" || e.Suggested != "h2e2" || e.CandRank != 1 {
		t.Fatalf("primary 事件不符: %+v", e)
	}
	// 锚点消费即清：再比对不应重复产出
	srv.evaluateAdoption(adoptP2Prim)
	if len(*got) != 1 {
		t.Fatalf("锚点应已清空, got %d", len(*got))
	}
}

// TestEvaluateAdoption_Candidate 实际走子 ∈ 候选（槽位 2）→ candidate。
func TestEvaluateAdoption_Candidate(t *testing.T) {
	srv := &Server{}
	srv.lastFen.Store(adoptP1)
	srv.pendingSug.Store(anchor(map[string]int{"h2e2": 1, "b2e2": 2}))
	got := collect(srv)

	srv.evaluateAdoption(adoptP2Can)
	if len(*got) != 1 {
		t.Fatalf("应产出 1 条事件, got %d", len(*got))
	}
	e := (*got)[0]
	if e.MatchKind != "candidate" || e.Actual != "b2e2" || e.CandRank != 2 {
		t.Fatalf("candidate 事件不符: %+v", e)
	}
}

// TestEvaluateAdoption_Miss 实际走子不在候选集 → miss（Suggested 仍为主选）。
func TestEvaluateAdoption_Miss(t *testing.T) {
	srv := &Server{}
	srv.lastFen.Store(adoptP1)
	srv.pendingSug.Store(anchor(map[string]int{"h2e2": 1}))
	got := collect(srv)

	srv.evaluateAdoption(adoptP2Miss)
	if len(*got) != 1 {
		t.Fatalf("应产出 1 条事件, got %d", len(*got))
	}
	e := (*got)[0]
	if e.MatchKind != "miss" || e.Actual != "e3e4" || e.Suggested != "h2e2" {
		t.Fatalf("miss 事件不符: %+v", e)
	}
}

// TestEvaluateAdoption_ResendAndInvalid 重发保留锚点、开局/多格变化清锚。
func TestEvaluateAdoption_ResendAndInvalid(t *testing.T) {
	srv := &Server{}
	srv.lastFen.Store(adoptP1)
	srv.pendingSug.Store(anchor(map[string]int{"h2e2": 1}))
	got := collect(srv)

	// 重发/补发（同局面重到）：不比对、锚点保留
	srv.evaluateAdoption(adoptP1)
	if len(*got) != 0 {
		t.Fatalf("重发不应产出事件, got %d", len(*got))
	}
	if srv.pendingSug.Load() == nil {
		t.Fatal("重发后锚点应保留")
	}

	// 多格变化（手动编辑/多步跳变）：清锚、无事件
	srv.evaluateAdoption("rnbakab1r/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/4C4/9/RNBAKABNR b - - 0 1")
	if len(*got) != 0 {
		t.Fatalf("多格变化不应产出事件, got %d", len(*got))
	}
	if p, _ := srv.pendingSug.Load().(*pendingAdvice); p != nil {
		t.Fatal("无法单步解释应清锚")
	}

	// 新一局（开局盘面段，重开常见形态=黑先行）：清锚、无事件
	srv.pendingSug.Store(anchor(map[string]int{"h2e2": 1}))
	srv.evaluateAdoption("rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR b - - 0 1")
	if p, _ := srv.pendingSug.Load().(*pendingAdvice); p != nil {
		t.Fatal("新局应清锚")
	}
}

// TestSendSuggestionAnchor 锚点维护：多帧累加、局面切换重建、rank 缺省按 1、
// 副选帧不覆盖主选分数快照。
func TestSendSuggestionAnchor(t *testing.T) {
	srv := &Server{}
	mk := func(fen, mv string, multipv int) Suggestion {
		return Suggestion{frameBase: frame(TypeSuggestion), Source: "engine",
			Fen: fen, BestMove: mv, Multipv: multipv}
	}
	srv.sendSuggestionToOwner(mk(adoptP1, "h2e2", 1))
	srv.sendSuggestionToOwner(mk(adoptP1, "b2e2", 2))
	srv.sendSuggestionToOwner(mk(adoptP1, "h0g2", 3))
	pend, _ := srv.pendingSug.Load().(*pendingAdvice)
	if pend == nil || len(pend.moves) != 3 || pend.moves["h2e2"] != 1 || pend.moves["h0g2"] != 3 {
		t.Fatalf("锚点应累计 3 条候选: %+v", pend)
	}

	// 沉淀命中帧（无 Multipv、带分数）：同局面 → 候选集不变；rank 缺省按 1
	scp := 150
	srv.sendSuggestionToOwner(Suggestion{frameBase: frame(TypeSuggestion), Source: "engine",
		Fen: adoptP1, BestMove: "h2e2", Score: &Score{CP: &scp}, Cached: true})
	pend, _ = srv.pendingSug.Load().(*pendingAdvice)
	if pend.scoreCP == nil || *pend.scoreCP != 150 {
		t.Fatalf("rank1 帧应刷新分数快照: %+v", pend)
	}

	// 局面切换：锚点重建（旧候选清空）
	srv.sendSuggestionToOwner(mk(adoptP2Prim, "b9c7", 1))
	pend, _ = srv.pendingSug.Load().(*pendingAdvice)
	if pend.fen != adoptP2Prim || len(pend.moves) != 1 {
		t.Fatalf("局面切换应重建锚点: %+v", pend)
	}
}

// TestTuneEnginePersist 算力落盘回调（2026-10-18）：TuneEngine（控制台面板
// 链路）校验+应用成功后触发 onTunePersist；校验失败不触发。
// 手机 set_engine 链路的落盘回归见 TestHandleSetEnginePersist。
func TestTuneEnginePersist(t *testing.T) {
	fb := newFakeBridge(engine.StateReady)
	url, _, srv := startTestServerWithBridgeS(t, fb)
	_ = url
	var got []string
	srv.SetTunePersist(func(kind string, v int) { got = append(got, kind+"="+itoa(v)) })

	if err := srv.TuneEngine("hash", 512); err != nil {
		t.Fatalf("合法 hash 调节应成功: %v", err)
	}
	if len(got) != 1 || got[0] != "hash=512" {
		t.Fatalf("落盘回调未按预期触发: %v", got)
	}
	// 同值重复调节静默（2026-10-29 日志降噪）：值未变不落盘
	if err := srv.TuneEngine("hash", 512); err != nil {
		t.Fatalf("同值调节应静默成功: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("同值调节不应重复落盘: %v", got)
	}
	// 非法值：校验拦截，不触发落盘
	if err := srv.TuneEngine("hash", 999999); err == nil {
		t.Fatal("超范围 hash 应被拒绝")
	}
	if len(got) != 1 {
		t.Fatalf("校验失败不应落盘: %v", got)
	}
}

// TestHandleSetEnginePersist 手机 set_engine 链路落盘（2026-10-18 修复回归）：
// 此前该链路只 applyTune 不触发 onTunePersist——悬浮窗调的参数重启即被 toml
// 旧值打回（用户用手机救回的 hash=10240 没被记住，"棋力反复变弱"的推手）。
// 修复后每个成功应用的字段都触发落盘，与面板链路同权；越界整帧拒绝不落盘。
func TestHandleSetEnginePersist(t *testing.T) {
	fb := newFakeBridge(engine.StateReady)
	fb.onRequest = replayNormalSearch
	url, _, srv := startTestServerWithBridgeS(t, fb)
	ws := dialWS(t, url)
	defer ws.Close()
	// 收集器必须持锁：回调在 WSS 读 goroutine 触发，断言在测试主 goroutine 读
	// ——-race 下裸切片是数据竞争（生产代码的锁在 main 侧，测试自证自身纪律）
	var mu sync.Mutex
	var got []string
	srv.SetTunePersist(func(kind string, v int) {
		mu.Lock()
		got = append(got, kind+"="+itoa(v))
		mu.Unlock()
	})

	if _, ok := recv(t, ws, "服务器 hello").(*Hello); !ok {
		t.Fatal("首帧应为 hello")
	}
	_ = ws.WriteJSON(Hello{frameBase: frame(TypeHello), Client: ClientName, Ver: "0.1.0", Caps: []string{CapV1}})
	_ = ws.WriteJSON(Auth{frameBase: frame(TypeAuth), Token: testToken})
	if _, ok := recv(t, ws, "welcome").(*Welcome); !ok {
		t.Fatal("期望 welcome")
	}

	// 手机悬浮窗切 hash 到 10240（用户实测的自救值）：应用后须落盘
	h10240 := 10240
	_ = ws.WriteJSON(SetEngine{frameBase: frame(TypeSetEngine), HashMB: &h10240})
	waitFor(t, time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(got) == 1 && got[0] == "hash=10240"
	})

	// 越界整帧拒绝：不应用也不落盘
	h999999 := 999999
	_ = ws.WriteJSON(SetEngine{frameBase: frame(TypeSetEngine), HashMB: &h999999})
	_ = ws.SetReadDeadline(time.Now().Add(2 * time.Second))
	defer ws.SetReadDeadline(time.Time{})
	for {
		m := recv(t, ws, "error 回执")
		ef, ok := m.(*ErrorFrame)
		if !ok {
			continue // 跳过重搜触发的 suggestion/status 帧
		}
		if ef.Code != CodeBadFrame {
			t.Fatalf("期望 error(bad_frame)，得到 %s（%s）", ef.Code, ef.Message)
		}
		break
	}
	// 同值回显静默（2026-10-29 日志降噪，典型场景：手机每次重连回显线程档）：
	// 全帧无实变 → 不落盘、不打日志、不重搜。无回执可等，靠时窗观察"没有发生"
	_ = ws.WriteJSON(SetEngine{frameBase: frame(TypeSetEngine), HashMB: &h10240})
	time.Sleep(300 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 {
		t.Fatalf("越界帧与同值回显均不应落盘: %v", got)
	}
}

// itoa 测试用小工具（避免为格式化一个数字引入 strconv 的重复 import 噪音）。
func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var b [20]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
