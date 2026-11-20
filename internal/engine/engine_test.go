package engine

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"cabinet/internal/xq"
)

// ─────────────── fake UCI 引擎 ───────────────

// fakeProc channel 直连的假进程（无 OS 管道）：stdin 收 Engine 命令、
// lines 出引擎输出。channel 版天然支持限时读，无同步管道的读写互等死锁。
type fakeProc struct {
	stdin chan string
	lines chan string // 与 fakeEngine.out 同一 channel
	quit  chan struct{}
}

func (f *fakeProc) WriteLine(line string) error {
	f.stdin <- line
	return nil
}

func (f *fakeProc) ReadLineTimeout(d time.Duration) (string, error) {
	select {
	case l, ok := <-f.lines:
		if !ok {
			return "", io.EOF
		}
		return l, nil
	case <-f.quit:
		return "", io.EOF
	case <-time.After(d):
		return "", errReadTimeout
	}
}

func (f *fakeProc) Quit() error {
	close(f.stdin) // fake loop range stdin 随之退出
	return nil
}

// fakeEngine 模拟皮卡鱼：处理 UCI 命令、可脚本化的搜索行为。
type fakeEngine struct {
	mu          sync.Mutex
	received    []string                      // 收到的全部命令（断言用）
	out         chan string                   // 引擎输出（fakeProc.lines 共享）
	slow        bool                          // true：go 后不发 bestmove，等 stop/超时
	crashOnStop bool                          // true：收到 stop 即关闭输出流（模拟真实皮卡鱼打断时崩溃 EOF）
	muteOnStop  bool                          // true：收到 stop 后彻底静默——不回 bestmove 也不退出（模拟挂死引擎，A1 回归）
	stopScript  func()                        // 非空时接管 stop 处理（打断排干回归脚本：自然收束+迟到回执双发形态）
	goScript    func(goCmd string, nth int32) // 非空时接管 go 处理（智能思考测试脚本；nth 从 1 计）
	goN         atomic.Int32                  // 已收到的 go 次数（goScript 的 nth）
	quit        chan struct{}                 // 关闭后输出流视为结束（out 本身不 close，避免 send panic）
	once        sync.Once
}

func (f *fakeEngine) cmds() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.received...)
}

func (f *fakeEngine) write(line string) {
	select {
	case f.out <- line:
	case <-f.quit:
	}
}

func (f *fakeEngine) handle(line string) {
	f.mu.Lock()
	f.received = append(f.received, line)
	f.mu.Unlock()
	switch {
	case line == "uci":
		f.write("id name fake-pikafish")
		f.write("uciok")
	case line == "isready":
		f.write("readyok")
	case line == "quit":
		f.once.Do(func() { close(f.quit) })
	case line == "stop":
		if f.crashOnStop {
			// 复现线上场景：打断时进程直接崩溃（EOF），bestmove 回执也丢失
			f.once.Do(func() { close(f.quit) })
			return
		}
		if f.muteOnStop {
			// 挂死：既不回 bestmove 也不退出，collect 强停后无界等待的病灶（A1）
			return
		}
		if f.stopScript != nil {
			f.stopScript()
			return
		}
		// 长搜被打断：立即回 bestmove（皮卡鱼行为）
		f.write("bestmove e3e4")
	case len(line) > 3 && line[:3] == "go ":
		if f.goScript != nil {
			n := f.goN.Add(1)
			go f.goScript(line, n)
			return
		}
		if f.slow {
			// 长搜：持续发 info 直到 stop/quit；设寿命上限防测试结束后长期泄漏
			go func() {
				for d := 1; d <= 300; d++ {
					select {
					case <-f.quit:
						return
					default:
					}
					f.write(fmt.Sprintf("info depth %d multipv 1 score cp %d nodes %d nps %d hashfull %d pv h2e2 b9c7", d, 100+d, d*1000, 500000, d))
					time.Sleep(5 * time.Millisecond)
				}
			}()
			return
		}
		// 正常：几条 info 后 bestmove
		f.write("info depth 1 multipv 1 score cp 60 nodes 100 nps 400000 hashfull 1 pv b2e2")
		time.Sleep(2 * time.Millisecond)
		f.write("info depth 8 multipv 1 score cp 120 nodes 50000 nps 500000 hashfull 12 pv h2e2 b9c7 h0g2")
		f.write("info depth 8 multipv 2 score cp 90 nodes 50000 nps 500000 hashfull 12 pv b2e2")
		f.write("bestmove h2e2")
	}
}

func (f *fakeEngine) loop(stdin chan string) {
	for line := range stdin {
		f.handle(line)
	}
}

// startFake 换掉 procStarter，返回 fake 供断言。
func startFake(t *testing.T, slow bool) *fakeEngine {
	t.Helper()
	fe := &fakeEngine{
		out:  make(chan string, 256),
		slow: slow,
		quit: make(chan struct{}),
	}
	p := &fakeProc{stdin: make(chan string, 64), lines: fe.out, quit: fe.quit}
	go fe.loop(p.stdin)

	orig := procStarter
	procStarter = func(opts Options) (proc, error) {
		return p, nil
	}
	t.Cleanup(func() { procStarter = orig })
	return fe
}

// waitState 等待引擎进入指定状态。
func waitState(t *testing.T, e *Engine, events <-chan Event, want string) Event {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev := <-events:
			if ev.Kind == EventState && ev.State == want {
				return ev
			}
		case <-deadline:
			t.Fatalf("等待状态 %s 超时", want)
		}
	}
}

// drainEvents 后台消费事件流，按需回调。
func drainEvents(events <-chan Event, onEv func(Event)) {
	for ev := range events {
		if onEv != nil {
			onEv(ev)
		}
	}
}

func testOpts() Options {
	return Options{Path: "fake", NNUE: "fake.nnue", Threads: 2, HashMB: 64, MultiPV: 2, Depth: 8}
}

func newTestEngine(t *testing.T, slow bool) (*Engine, *fakeEngine, <-chan Event) {
	t.Helper()
	fe := startFake(t, slow)
	e := New(testOpts(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	e.restartDelay = 20 * time.Millisecond
	return e, fe, e.Events()
}

// ─────────────── 测试 ───────────────

// TestEngine_Handshake 握手时序与配置下发：uci→uciok 后才 setoption，最后 isready。
func TestEngine_Handshake(t *testing.T) {
	e, fe, events := newTestEngine(t, false)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	waitState(t, e, events, StateReady)
	got := fe.cmds()
	// 必需命令按序存在（Threads 必须先于 Hash，官方调优要求）
	wantOrder := []string{"uci", "setoption name EvalFile value fake.nnue",
		"setoption name Threads value 2", "setoption name Hash value 64",
		"setoption name MultiPV value 2", "setoption name UCI_ShowWDL value true",
		"ucinewgame", "isready"}
	idx := -1
	for _, w := range wantOrder {
		found := -1
		for i, c := range got {
			if i > idx && c == w {
				found = i
				break
			}
		}
		if found < 0 {
			t.Fatalf("缺少命令 %q，实际: %v", w, got)
		}
		idx = found
	}
}

// TestEngine_SearchFlow 完整搜索流：info 事件 + bestmove 终态，MultiPV 线排序、行棋方判定。
func TestEngine_SearchFlow(t *testing.T) {
	e, _, events := newTestEngine(t, false)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)
	waitState(t, e, events, StateReady)

	fen := "rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR b - - 0 1"
	e.Request(fen)

	deadline := time.After(5 * time.Second)
	var best *Result
	for best == nil {
		select {
		case ev := <-events:
			if ev.Kind != EventBestmove {
				continue
			}
			if ev.Result.Fen != fen {
				continue // 预热（startpos）结果按 FEN 不匹配丢弃，server 同口径
			}
			best = ev.Result
		case <-deadline:
			t.Fatal("等待 bestmove 超时")
		}
	}
	if best.Fen != fen {
		t.Fatalf("Result.Fen 错误: %+v", best)
	}
	if best.BestMove != "h2e2" {
		t.Fatalf("bestmove 错误: %s", best.BestMove)
	}
	if best.RedGo {
		t.Fatal("行棋方为 b（黑），RedGo 应为 false")
	}
	if len(best.Lines) != 2 || best.Lines[0].MultiPV != 1 || best.Lines[1].MultiPV != 2 {
		t.Fatalf("MultiPV 线排序错误: %+v", best.Lines)
	}
	if best.Lines[0].Depth != 8 || best.Lines[0].Nodes != 50000 || best.Lines[0].Hashfull != 12 {
		t.Fatalf("info 指标解析错误: %+v", best.Lines[0])
	}
}

// TestToRedScore 红方视角换算（协议 §4.5 定稿）。
func TestToRedScore(t *testing.T) {
	info := Info{ScoreCP: 120}
	// 红方行棋：原生即红方视角
	cp, mate, hasMate := ToRedScore(info, true)
	if cp != 120 || hasMate {
		t.Fatalf("红方换算错误: cp=%d mate=%d", cp, mate)
	}
	// 黑方行棋：取负
	cp, _, _ = ToRedScore(info, false)
	if cp != -120 {
		t.Fatalf("黑方换算错误: cp=%d", cp)
	}
	// 杀棋：黑方视角 mate=+3（黑将杀红）→ 红方视角 -3
	mi := Info{Mate: 3, HasMate: true}
	cp, mate, hasMate = ToRedScore(mi, false)
	if !hasMate || mate != -3 || cp != 0 {
		t.Fatalf("mate 换算错误: cp=%d mate=%d hasMate=%v", cp, mate, hasMate)
	}
}

// TestToRedWdl WDL 红方视角换算：红方行棋原样，黑方行棋胜/负互换。
func TestToRedWdl(t *testing.T) {
	info := Info{WdlW: 320, WdlD: 560, WdlL: 120, HasWdl: true}
	w, d, l := ToRedWdl(info, true)
	if w != 320 || d != 560 || l != 120 {
		t.Fatalf("红方 WDL 换算错误: %d/%d/%d", w, d, l)
	}
	// 黑方行棋：走子方"胜"即红方"负"，W/L 互换
	w, d, l = ToRedWdl(info, false)
	if w != 120 || d != 560 || l != 320 {
		t.Fatalf("黑方 WDL 换算错误: %d/%d/%d", w, d, l)
	}
	// 未携带 wdl：返回全 0
	w, d, l = ToRedWdl(Info{ScoreCP: 50}, true)
	if w != 0 || d != 0 || l != 0 {
		t.Fatalf("无 wdl 行应返回全 0: %d/%d/%d", w, d, l)
	}
}

// TestEngine_PreemptLatest 最新覆盖：搜索中新请求应触发 stop、旧结果作废、新请求被搜索。
func TestEngine_PreemptLatest(t *testing.T) {
	e, fe, events := newTestEngine(t, true) // slow：长搜，bestmove 只在 stop 后
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)
	waitState(t, e, events, StateReady)

	fen1 := "rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR w - - 0 1"
	fen2 := "2bakab2/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR w - - 0 1"
	e.Request(fen1)
	time.Sleep(30 * time.Millisecond) // 让 fen1 进入搜索
	e.Request(fen2)                   // 打断

	// fake 应收到 stop、fen2 的 position 与 go：打断生效、新请求被搜索
	wantPos := "position fen " + fen2
	deadline := time.After(5 * time.Second)
	for {
		got := fe.cmds()
		if hasCmd(got, "stop") && countPrefix(got, "go depth") >= 2 && hasCmd(got, wantPos) {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("未观察到打断（stop+fen2 重搜），实际: %v", got)
		case <-time.After(20 * time.Millisecond):
		}
	}

	// 消费事件一小段窗口：fen1 的 bestmove 绝不应出现（preempt 作废）
	// （可能收到的是预热 startpos 的收束事件，合法）
	deadline2 := time.After(300 * time.Millisecond)
	for {
		select {
		case ev := <-events:
			if ev.Kind == EventBestmove && ev.Result.Fen == fen1 {
				t.Fatal("被打断的旧搜索结果未被作废（fen1 出现 bestmove 事件）")
			}
		case <-deadline2:
			return
		}
	}
}

// TestEngine_DrainConsumesDelayedStopReceipt 打断排干窗回归（2026-11-14 红队第二轮
// 实锤）：引擎"自然收束 bestmove + 迟到回执"双发形态下，旧排干实现曾在首个
// 100ms 分片静默提前退出——回执漏出排干窗，被下一轮 collect 收作新搜索的结果
// （Result.Fen 贴上新局面下发+沉淀）。修复后排干静默分片等满 300ms 窗，回执必
// 被消化；下发侧伪合法/Lines 非空双守卫兜底（engine_pump.go）。
func TestEngine_DrainConsumesDelayedStopReceipt(t *testing.T) {
	e, fe, events := newTestEngine(t, true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)
	waitState(t, e, events, StateReady)

	// 每次 stop：先回自然收束 bestmove（collect abort 消费），150ms 后再回一条
	// 迟到回执——旧排干在 100ms 静默处早已退出，回执漏进下一轮搜索
	fe.stopScript = func() {
		fe.write("bestmove h2e2")
		go func() {
			time.Sleep(150 * time.Millisecond)
			fe.write("bestmove h2e2")
		}()
	}
	// goScript 的 nth（testOpts 无预热，go 序列=fen1、fen2）：1=fen1 不输出（长搜
	// 等打断）；2=fen2 延迟 200ms 再写真实输出——保证迟到回执在 FIFO 中先于它
	fe.goScript = func(goCmd string, nth int32) {
		if nth != 2 {
			return
		}
		time.Sleep(200 * time.Millisecond)
		fe.write("info depth 8 multipv 1 score cp 120 nodes 1000 nps 1000 hashfull 1 pv b0c2")
		fe.write("bestmove b0c2")
	}

	fen1 := "rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR w - - 0 1"
	fen2 := "2bakab2/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR w - - 0 1"
	e.Request(fen1)
	// 400ms：必须等第一轮"自然收束+迟到回执"完整消化（回执 +150ms）且 fen1 已
	// 进入搜索——过早打断会把第二轮回执与第一轮排干窗重叠成时序纠缠
	time.Sleep(400 * time.Millisecond)
	e.Request(fen2)

	// 终值判定：任何 h2e2 终值事件（无论贴 fen1 还是 fen2）都是回执漏窗；
	// fen2 的唯一终值必须是它自己的 b0c2
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev := <-events:
			if ev.Kind != EventBestmove || ev.Result == nil {
				continue
			}
			if ev.Result.BestMove == "h2e2" {
				t.Fatalf("迟到回执漏出排干窗，被贴上 %s 收作结果", ev.Result.Fen)
			}
			if ev.Result.Fen == fen2 {
				if ev.Result.BestMove != "b0c2" {
					t.Fatalf("fen2 终值被污染: %s", ev.Result.BestMove)
				}
				return // fen2 正确收束，通过
			}
		case <-deadline:
			t.Fatalf("等待 fen2 终值超时，已收命令: %v", fe.cmds())
		}
	}
}

// TestEngine_PreemptResidueAfterCrash 回归：打断标志在进程崩溃重启后必须清理。
// 场景（实测发生）：Request 打断预热（置 preempt+stop）→ 皮卡鱼崩溃 EOF（不回
// bestmove）→ collect 异常退出未消费 preempt → 看门狗重启 → 重启后的首个正常
// 收束被残留标志误判为"被打断"而丢弃 → 建议不广播、不沉淀、无收束帧。
func TestEngine_PreemptResidueAfterCrash(t *testing.T) {
	calls := 0
	orig := procStarter
	t.Cleanup(func() { procStarter = orig })

	procStarter = func(opts Options) (proc, error) {
		calls++
		// 第一实例：预热长搜 + 收到 stop 即崩；其后实例：正常短搜
		fe := &fakeEngine{out: make(chan string, 256), quit: make(chan struct{}),
			slow: calls == 1, crashOnStop: calls == 1}
		p := &fakeProc{stdin: make(chan string, 64), lines: fe.out, quit: fe.quit}
		go fe.loop(p.stdin)
		return p, nil
	}

	e := New(testOpts(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	e.restartDelay = 20 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)
	waitState(t, e, e.Events(), StateReady)

	// 等预热长搜真正跑起来（首个 info 事件），此刻 Request 才会走打断路径
	deadline0 := time.After(5 * time.Second)
	for {
		select {
		case ev := <-e.Events():
			if ev.Kind == EventInfo {
				goto searching
			}
		case <-deadline0:
			t.Fatal("等待预热搜索启动超时")
		}
	}
searching:

	fen := "rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR w - - 0 1"
	e.Request(fen) // 打断预热 → 第一实例崩溃 → 看门狗重启 → 槽内 fen1 被重搜

	// 修复前：重启后收束被残留 preempt 作废，永远等不到 fen1 的 bestmove
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev := <-e.Events():
			if ev.Kind == EventBestmove && ev.Result != nil && ev.Result.Fen == fen {
				if ev.Result.BestMove != "h2e2" {
					t.Fatalf("bestmove 错误: %s", ev.Result.BestMove)
				}
				return // 重启后的正常收束被保留 ✓
			}
		case <-deadline:
			t.Fatalf("重启后未收到 fen1 的 bestmove（preempt 残留 bug 复现），calls=%d", calls)
		}
	}
}

// TestEngine_WatchdogRestart 进程死亡自动重启。
func TestEngine_WatchdogRestart(t *testing.T) {
	calls := 0
	orig := procStarter
	t.Cleanup(func() { procStarter = orig })

	procStarter = func(opts Options) (proc, error) {
		calls++
		if calls == 1 {
			// 第一次：立即死亡（输出流已关闭 → ReadLine 出错 → 握手失败）
			dead := make(chan string)
			close(dead)
			return &fakeProc{stdin: make(chan string, 8), lines: dead, quit: make(chan struct{})}, nil
		}
		fe := &fakeEngine{out: make(chan string, 256), quit: make(chan struct{})}
		p := &fakeProc{stdin: make(chan string, 64), lines: fe.out, quit: fe.quit}
		go fe.loop(p.stdin)
		return p, nil
	}

	e := New(testOpts(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	e.restartDelay = 20 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	sawError, sawReady := false, false
	deadline := time.After(5 * time.Second)
	for !sawError || !sawReady {
		select {
		case ev := <-e.Events():
			if ev.Kind == EventState && ev.State == StateError {
				sawError = true
			}
			if ev.Kind == EventState && ev.State == StateReady {
				sawReady = true
			}
		case <-deadline:
			t.Fatalf("重启未完成: sawError=%v sawReady=%v calls=%d", sawError, sawReady, calls)
		}
	}
	if calls < 2 {
		t.Fatalf("应至少启动两次进程, calls=%d", calls)
	}
}

// TestEngine_SearchTimeout 搜索超时兜底：fake 不回 bestmove，超时后应发 stop。
func TestEngine_SearchTimeout(t *testing.T) {
	e, fe, events := newTestEngine(t, true) // slow：不发 bestmove 直到 stop
	e.searchTimeout = 80 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)
	waitState(t, e, events, StateReady)

	e.Request("rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR w - - 0 1")
	deadline := time.After(5 * time.Second)
	for {
		got := fe.cmds()
		if hasCmd(got, "stop") {
			return // 超时强停生效
		}
		select {
		case <-deadline:
			t.Fatalf("超时未强停，实际: %v", got)
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// TestEngine_StopGraceTimeout 回归（P1）：强停后引擎既不回 bestmove 也不退出
// （挂死）时，collect 必须在第二超时（stopGrace）内报错收场，让 searchLoop
// 返回、看门狗 Quit+重启接管。修复前强停后退化为无界阻塞读，挂死引擎令
// collect/searchLoop 永久卡死，看门狗永远走不到（calls 恒为 1、永远一次 Ready）。
func TestEngine_StopGraceTimeout(t *testing.T) {
	calls := 0
	orig := procStarter
	t.Cleanup(func() { procStarter = orig })

	procStarter = func(opts Options) (proc, error) {
		calls++
		// 第一实例：长搜 + 收 stop 后彻底静默（不回执、不退出）；其后实例：
		// 正常短搜，供看门狗重启后恢复就绪
		fe := &fakeEngine{out: make(chan string, 256), quit: make(chan struct{}),
			slow: calls == 1, muteOnStop: calls == 1}
		p := &fakeProc{stdin: make(chan string, 64), lines: fe.out, quit: fe.quit}
		go fe.loop(p.stdin)
		return p, nil
	}

	e := New(testOpts(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	e.restartDelay = 20 * time.Millisecond
	e.searchTimeout = 80 * time.Millisecond
	e.stopGrace = 200 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	// 判据：第二次 StateReady = 第一实例被第二超时收场 → 看门狗重启 →
	// 第二实例握手就绪。修复前此路不通（永远停在第一次 Ready）。
	deadline := time.After(5 * time.Second)
	readyCount := 0
	for readyCount < 2 {
		select {
		case ev := <-e.Events():
			if ev.Kind == EventState && ev.State == StateReady {
				readyCount++
			}
		case <-deadline:
			t.Fatalf("挂死引擎未被第二超时收场重启: calls=%d readyCount=%d", calls, readyCount)
		case <-time.After(20 * time.Millisecond):
		}
	}
	if calls < 2 {
		t.Fatalf("看门狗应已重启进程, calls=%d", calls)
	}
}

// TestParseInfo UCI info 行解析。
func TestParseInfo(t *testing.T) {
	tokens := strings.Fields("info depth 8 multipv 2 score cp -90 nodes 50000 nps 500000 hashfull 12 pv b2e2 h2e2")
	info, ok := parseInfo(tokens)
	if !ok {
		t.Fatal("合法 info 行应解析成功")
	}
	if info.Depth != 8 || info.MultiPV != 2 || info.ScoreCP != -90 || info.Nodes != 50000 || info.NPS != 500000 || info.Hashfull != 12 {
		t.Fatalf("字段解析错误: %+v", info)
	}
	if len(info.PV) != 2 || info.PV[0] != "b2e2" {
		t.Fatalf("pv 解析错误: %v", info.PV)
	}

	tokens = strings.Fields("info depth 5 score mate 3 pv e3e4")
	info, ok = parseInfo(tokens)
	if !ok || !info.HasMate || info.Mate != 3 {
		t.Fatalf("mate 解析错误: %+v ok=%v", info, ok)
	}

	// wdl：胜/和/负千分比（UCI_ShowWDL 开启），score 与 wdl 同时存在
	tokens = strings.Fields("info depth 8 score cp 34 wdl 320 560 120 pv e2e4")
	info, ok = parseInfo(tokens)
	if !ok || !info.HasWdl || info.WdlW != 320 || info.WdlD != 560 || info.WdlL != 120 {
		t.Fatalf("wdl 解析错误: %+v ok=%v", info, ok)
	}
	if info.ScoreCP != 34 {
		t.Fatalf("wdl 行的 score 丢失: %+v", info)
	}

	// 无 score（NNUE 叙述行）→ 丢弃
	tokens = strings.Fields("info string NNUE evaluation using fake.nnue")
	if _, ok = parseInfo(tokens); ok {
		t.Fatal("string 行应返回 ok=false")
	}
}

// ─────────────── 小工具 ───────────────

func hasCmd(cmds []string, want string) bool {
	for _, c := range cmds {
		if c == want {
			return true
		}
	}
	return false
}

func countPrefix(cmds []string, prefix string) int {
	n := 0
	for _, c := range cmds {
		if len(c) >= len(prefix) && c[:len(prefix)] == prefix {
			n++
		}
	}
	return n
}

// ─────────────── 运行中算力调节（set_engine，协议 §4.10） ───────────────

// TestEngine_TunerRange SetDepth/SetHashMB/SetThreads/SetMultiPV 的范围校验与读接口回显。
func TestEngine_TunerRange(t *testing.T) {
	e := New(testOpts(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	cases := []struct {
		name    string
		set     func() bool
		wantOK  bool
		wantVal int
	}{
		{"depth 合法", func() bool { return e.SetDepth(26) }, true, 26},
		{"depth 越上界", func() bool { return e.SetDepth(41) }, false, 26},
		{"depth 越下界", func() bool { return e.SetDepth(0) }, false, 26},
		{"hash 合法下界", func() bool { return e.SetHashMB(32) }, true, 32},
		{"hash 合法上界", func() bool { return e.SetHashMB(32768) }, true, 32768},
		{"hash 越下界", func() bool { return e.SetHashMB(31) }, false, 32768},
		{"hash 越上界", func() bool { return e.SetHashMB(32769) }, false, 32768},
		{"threads 合法", func() bool { return e.SetThreads(18) }, true, 18},
		{"threads 越上界", func() bool { return e.SetThreads(65) }, false, 18},
		{"threads 越下界", func() bool { return e.SetThreads(-1) }, false, 18},
		{"multipv 合法", func() bool { return e.SetMultiPV(5) }, true, 5},
		{"multipv 越上界", func() bool { return e.SetMultiPV(6) }, false, 5},
		{"multipv 越下界", func() bool { return e.SetMultiPV(0) }, false, 5},
	}
	for _, tc := range cases {
		if got := tc.set(); got != tc.wantOK {
			t.Fatalf("%s: 返回 %v，期望 %v", tc.name, got, tc.wantOK)
		}
	}
	if e.HashMB() != 32768 {
		t.Fatalf("越界调用不应改动目标值，HashMB=%d", e.HashMB())
	}
	if e.Depth() != 26 {
		t.Fatalf("Depth=%d，期望 26", e.Depth())
	}
	if e.Threads() != 18 {
		t.Fatalf("Threads=%d，期望 18", e.Threads())
	}
	if e.MultiPV() != 5 {
		t.Fatalf("MultiPV=%d，期望 5", e.MultiPV())
	}
}

// TestEngine_AutoDefaults Threads/Hash 自动档：0 值解析为逻辑核一半 + 固定 2GiB。
func TestEngine_AutoDefaults(t *testing.T) {
	e := New(Options{Path: "fake", NNUE: "fake.nnue", MultiPV: 3, Depth: 20},
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if want := autoThreads(); e.Threads() != want {
		t.Fatalf("Threads=%d，期望自动档 %d（逻辑核一半）", e.Threads(), want)
	}
	if n := runtime.NumCPU(); n >= 2 && autoThreads() != n/2 {
		t.Fatalf("autoThreads()=%d，期望逻辑核一半 %d", autoThreads(), n/2)
	}
	if e.HashMB() != defaultHashMB {
		t.Fatalf("HashMB=%d，期望自动档 %d", e.HashMB(), defaultHashMB)
	}
	// SetHashMB(0) 同样走自动档
	if !e.SetHashMB(0) {
		t.Fatal("SetHashMB(0) 应被接受（自动档）")
	}
	if e.HashMB() != defaultHashMB {
		t.Fatalf("SetHashMB(0) 后 HashMB=%d，期望 %d", e.HashMB(), defaultHashMB)
	}
	// SetThreads(0) 同样走自动档
	if !e.SetThreads(0) {
		t.Fatal("SetThreads(0) 应被接受（自动档）")
	}
	if want := autoThreads(); e.Threads() != want {
		t.Fatalf("SetThreads(0) 后 Threads=%d，期望 %d", e.Threads(), want)
	}
}

// TestEngine_TakePendingOptionsOrder pending 通用化：三参数按 Threads→Hash→MultiPV
// 顺序生成 setoption，无差异时空列表；take 之后再次调用不重复下发。
func TestEngine_TakePendingOptionsOrder(t *testing.T) {
	e := New(testOpts(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	// 初始状态（与握手配置一致）：无待应用项
	if got := e.takePendingOptions(); len(got) != 0 {
		t.Fatalf("初始不应有待应用项: %v", got)
	}
	if !e.SetThreads(18) || !e.SetHashMB(2048) || !e.SetMultiPV(1) {
		t.Fatal("合法调节被拒绝")
	}
	got := e.takePendingOptions()
	want := []string{
		"setoption name Threads value 18",
		"setoption name Hash value 2048",
		"setoption name MultiPV value 1",
	}
	if len(got) != len(want) {
		t.Fatalf("待应用项=%v，期望 %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("顺序错误：got[%d]=%q，期望 %q", i, got[i], want[i])
		}
	}
	// 已全部生效，再次调用应为空
	if got := e.takePendingOptions(); len(got) != 0 {
		t.Fatalf("take 后不应有残留待应用项: %v", got)
	}
}

// TestEngine_TunerAppliesOnNextSearch 回归：Hash 变更必须在下一轮搜索开始前
// 以 setoption 下发（UCI 禁止搜索中 setoption），且先于 position/go；深度直接
// 体现在 go depth 参数里。
func TestEngine_TunerAppliesOnNextSearch(t *testing.T) {
	e, fe, events := newTestEngine(t, false)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)
	waitState(t, e, events, StateReady)
	go drainEvents(events, nil) // 消费事件流防阻塞

	if !e.SetDepth(26) || !e.SetHashMB(512) || !e.SetThreads(6) || !e.SetMultiPV(1) {
		t.Fatal("合法调节被拒绝")
	}
	e.Request("rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR w - - 0 1")

	deadline := time.After(5 * time.Second)
	for {
		cmds := fe.cmds()
		// Set* 与搜索循环并发：pending 可能在两轮搜索间分批 take（如 Hash 先于
		// Threads 入队被第一轮 take 走）。本测试只断言"生效"——最后一轮 go 之前
		// 三条 setoption 都已发出；单批内的下发顺序由 TakePendingOptionsOrder 专测。
		lastGo := -1
		for i, c := range cmds {
			if c == "go depth 26" {
				lastGo = i
			}
		}
		if lastGo >= 0 {
			mpIdx, hashIdx, thIdx := -1, -1, -1
			for i := lastGo - 1; i >= 0; i-- {
				switch {
				case mpIdx < 0 && cmds[i] == "setoption name MultiPV value 1":
					mpIdx = i
				case hashIdx < 0 && cmds[i] == "setoption name Hash value 512":
					hashIdx = i
				case thIdx < 0 && cmds[i] == "setoption name Threads value 6":
					thIdx = i
				}
			}
			if thIdx >= 0 && hashIdx >= 0 && mpIdx >= 0 {
				return
			}
		}
		select {
		case <-deadline:
			t.Fatalf("调节未在下轮搜索生效，已收命令: %v", cmds)
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// TestEngine_BackoffDelay 看门狗退避：连续快速失败未达阈值用基础间隔，
// 达到后逐次翻倍封顶 30s；超大计数（移位溢出）同样取封顶值。
func TestEngine_BackoffDelay(t *testing.T) {
	e := New(testOpts(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	cases := []struct {
		consec int
		want   time.Duration
	}{
		{0, 1200 * time.Millisecond},
		{4, 1200 * time.Millisecond},  // 未达阈值 5
		{5, 2400 * time.Millisecond},  // 1.2s<<1
		{6, 4800 * time.Millisecond},  // 1.2s<<2
		{7, 9600 * time.Millisecond},  // 1.2s<<3
		{8, 19200 * time.Millisecond}, // 1.2s<<4
		{9, 30 * time.Second},         // 1.2s<<5=38.4s → 封顶
		{50, 30 * time.Second},        // 移位溢出 → 封顶
	}
	for _, tc := range cases {
		if got := e.backoffDelay(tc.consec); got != tc.want {
			t.Fatalf("backoffDelay(%d)=%v，期望 %v", tc.consec, got, tc.want)
		}
	}
}

// TestEngine_IdleTuneFlush 空闲冲刷调参（2026-10-20）：无任何搜索任务时
// SetHashMB 也应立即向引擎发送 setoption——此前参数只能搭"下一轮搜索"的车，
// 引擎刚启动、手机从未连接时没有任何搜索可搭，调 hash 引擎毫无动作（用户实测
// 任务管理器内存纹丝不动）。强断言：setoption 出现且其后没有新的 go——
// 证明是纯空闲冲刷而非搭车搜索。
func TestEngine_IdleTuneFlush(t *testing.T) {
	e, fe, events := newTestEngine(t, false)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	waitState(t, e, events, StateReady)
	// 等预热搜索自然收束（bestmove 事件），避免空闲冲刷与"搜索前生效"路径竞态
	drainDone := make(chan struct{})
	go func() {
		defer close(drainDone)
		for ev := range events {
			if ev.Kind == EventBestmove {
				return
			}
		}
	}()
	select {
	case <-drainDone:
	case <-time.After(5 * time.Second):
		t.Fatal("等待预热 bestmove 超时")
	}

	if !e.SetHashMB(512) {
		t.Fatal("合法 hash 调节应被接受")
	}
	// 空闲冲刷应在百毫秒级把 setoption 发给引擎（100ms 轮询粒度的即时分支）
	deadline := time.After(3 * time.Second)
	for {
		cmds := fe.cmds()
		foundAt := -1
		for i, c := range cmds {
			if c == "setoption name Hash value 512" {
				foundAt = i
				break
			}
		}
		if foundAt >= 0 {
			// 强断言：冲刷不引入任何搜索（其后无 go）
			for _, c := range cmds[foundAt+1:] {
				if strings.HasPrefix(c, "go ") {
					t.Fatalf("空闲冲刷不应触发搜索，setoption 后收到 %q", c)
				}
			}
			return
		}
		select {
		case <-deadline:
			t.Fatalf("空闲冲刷未发生，已收命令: %v", cmds)
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// TestEngine_SameFenTTReuseLimit 同局面连续重搜的 TT 复用上限（2026-11-05）：
// 同 FEN 连发 3 次，前两轮复用置换表（无 Clear Hash），第 3 轮搜索前必须先
// 清表重新思考；换新局面后计数重置，其前两轮不再清。回归场景：反复调参/
// 重发导致建议流被同局面近似行刷屏（复用缓存秒收敛死循环）。
func TestEngine_SameFenTTReuseLimit(t *testing.T) {
	e, fe, events := newTestEngine(t, false)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)
	waitState(t, e, events, StateReady)

	// 等 bestmove 到齐再发下一轮 Request，避免打断（aborted 轮无 bestmove
	// 事件）干扰轮次计数；非本局面的预热结果按 FEN 过滤丢弃。
	waitBest := func(fen string) {
		t.Helper()
		deadline := time.After(5 * time.Second)
		for {
			select {
			case ev := <-events:
				if ev.Kind == EventBestmove && ev.Result.Fen == fen {
					return
				}
			case <-deadline:
				t.Fatalf("等待 bestmove 超时: %s", fen)
			}
		}
	}
	clearHashCount := func() int {
		n := 0
		for _, c := range fe.cmds() {
			if c == "setoption name Clear Hash" {
				n++
			}
		}
		return n
	}

	fen1 := "rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR b - - 0 1"
	e.Request(fen1)
	waitBest(fen1)
	e.Request(fen1)
	waitBest(fen1)
	if n := clearHashCount(); n != 0 {
		t.Fatalf("前两轮同局面重搜应复用 TT（Clear Hash ×%d）", n)
	}
	e.Request(fen1) // 第 3 轮：超出 maxSameFenTTReuse，搜索前先清表
	waitBest(fen1)
	if n := clearHashCount(); n != 1 {
		t.Fatalf("第 3 轮同局面重搜应恰好清表一次，实际 ×%d", n)
	}

	// 换新局面：计数重置，前两轮照常复用，不再追加清表
	fen2 := "rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR w - - 0 1"
	e.Request(fen2)
	waitBest(fen2)
	e.Request(fen2)
	waitBest(fen2)
	if n := clearHashCount(); n != 1 {
		t.Fatalf("换新局面后不应再清表（计数应重置），实际 Clear Hash ×%d", n)
	}
}

// TestEngine_RequestHistory 带历史搜索的语义验证（2026-11-08 vc27 事故回归）：
// RequestHistory 的 UCI 命令必须是 `position fen <起点> moves <序列>`（从起点
// 走到终局面），事件回推的 Result.Fen 恒为终局面；脏链（非法着法/终点不符）
// 必须退化为快照请求——vc27 把终局面当起点、历史当序列，皮卡鱼对非法序列
// 直接终止进程（Illegal move → EOF → 看门狗重启循环，日志实证 26 次）。
func TestEngine_RequestHistory(t *testing.T) {
	e, fe, events := newTestEngine(t, false)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)
	waitState(t, e, events, StateReady)

	waitBest := func(fen string) {
		t.Helper()
		deadline := time.After(5 * time.Second)
		for {
			select {
			case ev := <-events:
				if ev.Kind == EventBestmove && ev.Result.Fen == fen {
					return
				}
			case <-deadline:
				t.Fatalf("等待 bestmove 超时: %s", fen)
			}
		}
	}
	lastPosCmd := func() string {
		cs := fe.cmds()
		for i := len(cs) - 1; i >= 0; i-- {
			if strings.HasPrefix(cs[i], "position ") {
				return cs[i]
			}
		}
		return ""
	}

	// 真实合法链（xq.FenAfter 逐段推演，语义自洽）
	startFen := "rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR w - - 0 1"
	m1, ok1 := xq.FenAfter(startFen, "h2e2")
	m2, ok2 := xq.FenAfter(m1, "b9c7")
	if !ok1 || !ok2 {
		t.Fatalf("FenAfter 推演失败: %v %v", ok1, ok2)
	}
	// 合法链：position 用起点、moves 用全序列；事件 FEN 用终局面
	e.RequestHistory(m2, startFen, []string{"h2e2", "b9c7"})
	waitBest(m2)
	if last := lastPosCmd(); last != "position fen "+startFen+" moves h2e2 b9c7" {
		t.Fatalf("带历史命令应为 position fen <起点> moves <序列>，实际: %s", last)
	}

	// 脏链（第二手 h2 已空 = vc27 实际崩法）：必须退快照
	fe.mu.Lock()
	fe.received = nil
	fe.mu.Unlock()
	e.RequestHistory(m2, startFen, []string{"h2e2", "h2e2"})
	waitBest(m2)
	if last := lastPosCmd(); last != "position fen "+m2 {
		t.Fatalf("脏链应退化为快照命令 position fen <终局面>，实际: %s", last)
	}

	// 终点不符（链只走了一步）：同样退快照
	e.RequestHistory(m2, startFen, []string{"h2e2"})
	waitBest(m2)
	if last := lastPosCmd(); last != "position fen "+m2 {
		t.Fatalf("终点不符应退化为快照命令，实际: %s", last)
	}

	// 快照请求行为不变
	e.Request(m2)
	waitBest(m2)
	if last := lastPosCmd(); last != "position fen "+m2 {
		t.Fatalf("快照命令形态不变，实际: %s", last)
	}
}

// TestEngine_RequestMultiPV 单次多线覆盖（2026-11-09 双击小棋盘"单次三变
// 解析"，协议 §4.4 position.multi_pv_once）：one-shot 请求在 position/go
// 前补发 setoption MultiPV=<pv>，下一条普通请求自动补发恢复配置值——
// "仅此一轮"由 appliedMultiPV 记账保证，配置目标（面板回显）全程不受扰。
func TestEngine_RequestMultiPV(t *testing.T) {
	e, fe, events := newTestEngine(t, false) // testOpts 配置 MultiPV=2
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)
	waitState(t, e, events, StateReady)

	waitBest := func(fen string) {
		t.Helper()
		deadline := time.After(5 * time.Second)
		for {
			select {
			case ev := <-events:
				if ev.Kind == EventBestmove && ev.Result.Fen == fen {
					return
				}
			case <-deadline:
				t.Fatalf("等待 bestmove 超时: %s", fen)
			}
		}
	}
	lastMPOpt := func() string {
		cs := fe.cmds()
		for i := len(cs) - 1; i >= 0; i-- {
			if strings.HasPrefix(cs[i], "setoption name MultiPV") {
				return cs[i]
			}
		}
		return ""
	}
	lastPosCmd := func() string {
		cs := fe.cmds()
		for i := len(cs) - 1; i >= 0; i-- {
			if strings.HasPrefix(cs[i], "position ") {
				return cs[i]
			}
		}
		return ""
	}

	fen1 := "rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR w - - 0 1"
	fen2 := "rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR b - - 0 1"

	// 单次多线：配置 2、请求 3 → setoption 3 后 position/go
	e.RequestMultiPV(fen1, 3)
	waitBest(fen1)
	if got := lastMPOpt(); got != "setoption name MultiPV value 3" {
		t.Fatalf("单次多线请求应补发 setoption MultiPV 3，实际: %q", got)
	}
	if e.MultiPV() != 2 {
		t.Fatalf("配置目标不得被单次覆盖改变: %d", e.MultiPV())
	}

	// 下一条普通请求：自动恢复配置值 2（"单次"语义闭环）
	e.Request(fen2)
	waitBest(fen2)
	if got := lastMPOpt(); got != "setoption name MultiPV value 2" {
		t.Fatalf("普通请求应自动恢复配置线数 2，实际: %q", got)
	}

	// 越界 pv 降级（2026-11-09 审查收口）：不丢弃请求——退化为普通快照搜索
	//（无 MultiPV setoption），配置目标不变；RequestHistoryMultiPV 同口径
	fe.mu.Lock()
	fe.received = nil
	fe.mu.Unlock()
	e.RequestMultiPV(fen1, 6)
	waitBest(fen1)
	for _, c := range fe.cmds() {
		if strings.HasPrefix(c, "setoption name MultiPV") {
			t.Fatalf("越界 pv 不得改引擎线数: %q", c)
		}
	}
	if last := lastPosCmd(); last != "position fen "+fen1 {
		t.Fatalf("越界 pv 应退化为普通快照请求，实际: %s", last)
	}
	e.RequestHistoryMultiPV(fen1, fen1, nil, 0)
	waitBest(fen1)
	if e.MultiPV() != 2 {
		t.Fatalf("越界 pv 不得改配置: %d", e.MultiPV())
	}
}

// TestOptionsDefaultsLocked 引擎兜底默认锁（2026-11-11 用户定）：Options 零值
// 经 withDefaults 后必须是 autoThreads / 2GiB / 单线 / 深度 20 / 无时限。
// 实测推荐档（hash_mb=1024）只写文档，绝不进兜底——算力红线，改动先过用户。
func TestOptionsDefaultsLocked(t *testing.T) {
	o := Options{}.withDefaults()
	if want := autoThreads(); o.Threads != want {
		t.Errorf("Threads 兜底应为 autoThreads()=%d，实际 %d", want, o.Threads)
	}
	if o.HashMB != 2048 {
		t.Errorf("HashMB 兜底应为 2048（自动档），实际 %d", o.HashMB)
	}
	if o.MultiPV != 1 {
		t.Errorf("MultiPV 兜底应为 1（单线最强），实际 %d", o.MultiPV)
	}
	if o.Depth != 20 {
		t.Errorf("Depth 兜底应为 20，实际 %d", o.Depth)
	}
	if o.MoveTimeMS != 0 {
		t.Errorf("MoveTimeMS 兜底应为 0（按深度），实际 %d", o.MoveTimeMS)
	}
}

// TestEngine_TTReuseCumulativeRotation TT 复用计数累计制回归（2026-11-15
// 累计制，替换旧"连续同键"口径）：4 局面轮换循环（A→B→A→B→A）里同键永不
// 连续，旧实现 sameRuns 恒 0、TT 永不清表，秒回旧答案强化建议循环（15:56
// 实锤 nodes 3751~6616，同一局面直搜循环直到三次重复判和）。累计口径：同键
// 第 3 次访问清表，轮换也计入；同键第 2 次访问照常复用（连续口径行为不变，
// TestEngine_SameFenTTReuseLimit 同步回归）。
func TestEngine_TTReuseCumulativeRotation(t *testing.T) {
	e, fe, events := newTestEngine(t, false)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)
	waitState(t, e, events, StateReady)

	// 等 bestmove 到齐再发下一轮 Request，避免打断轮（aborted 轮无 bestmove
	// 事件）干扰访问计数；非本局面的预热结果按 FEN 过滤丢弃
	waitBest := func(fen string) {
		t.Helper()
		deadline := time.After(5 * time.Second)
		for {
			select {
			case ev := <-events:
				if ev.Kind == EventBestmove && ev.Result.Fen == fen {
					return
				}
			case <-deadline:
				t.Fatalf("等待 bestmove 超时: %s", fen)
			}
		}
	}
	clearHashCount := func() int {
		n := 0
		for _, c := range fe.cmds() {
			if c == "setoption name Clear Hash" {
				n++
			}
		}
		return n
	}

	fenA := "rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR b - - 0 1"
	fenB := "rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR w - - 0 1"
	e.Request(fenA) // A visit1（新键）
	waitBest(fenA)
	e.Request(fenB) // B visit1
	waitBest(fenB)
	e.Request(fenA) // A visit2 → 首次复用，不清表
	waitBest(fenA)
	e.Request(fenB) // B visit2 → 首次复用，不清表
	waitBest(fenB)
	if n := clearHashCount(); n != 0 {
		t.Fatalf("轮换前四轮同键各 2 次访问应复用 TT（Clear Hash ×%d）", n)
	}
	e.Request(fenA) // A visit3 → 超出 maxSameFenTTReuse，搜索前清表
	waitBest(fenA)
	if n := clearHashCount(); n != 1 {
		t.Fatalf("同键累计第 3 次访问应清表（轮换口径），实际 ×%d", n)
	}
}

// TestEngine_CollectStalePreemptGen 打断代次制核心语义（2026-11-15，16:05
// "无指导 77.5s"竞态的根治点）：collect 收束只认"preemptGen==自己代次"的
// 打断——指向已收束搜索的残留打断位（旧布尔实现 requeste 判定与置位之间
// 搜索自然收束时的残留形态）对本轮无效；同代次打断照常作废并消费该位。
func TestEngine_CollectStalePreemptGen(t *testing.T) {
	e := New(testOpts(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx := context.Background()
	fen := "rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR w - - 0 1"

	// 预灌两条输出的假进程（info + bestmove，collect 读到收束即返回）
	newProc := func() *fakeProc {
		p := &fakeProc{stdin: make(chan string, 16), lines: make(chan string, 16), quit: make(chan struct{})}
		p.lines <- "info depth 1 multipv 1 score cp 60 nodes 100 nps 400000 hashfull 1 pv h2e2"
		p.lines <- "bestmove h2e2"
		return p
	}

	// 残留代次 99 ≠ 本轮代次 7：bestmove 照常收束（旧实现会被残留位作废，
	// 建议静默丢失）
	e.preemptGen.Store(99)
	res, aborted, err := e.collect(ctx, newProc(), fen, false, 7)
	if err != nil || aborted || res == nil || res.BestMove != "h2e2" {
		t.Fatalf("残留代次不得作废本轮收束: res=%v aborted=%v err=%v", res, aborted, err)
	}

	// 同代次打断：结果作废（aborted），且 Swap 消费后归零
	e.preemptGen.Store(7)
	res, aborted, err = e.collect(ctx, newProc(), fen, false, 7)
	if err != nil || !aborted || res != nil {
		t.Fatalf("同代次打断应作废结果: res=%v aborted=%v err=%v", res, aborted, err)
	}
	if got := e.preemptGen.Load(); got != 0 {
		t.Fatalf("打断位应被消费归零，实际 %d", got)
	}
}
