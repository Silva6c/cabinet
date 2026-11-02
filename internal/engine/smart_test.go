package engine

// smart_test.go 智能思考两级机制的六态回归（2026-11-02）：
//  1. 稳定主线 → stop 恰一次且 bestmove 照常收束（早停路径端到端）
//  2. 摆动主线 → 不早停，自然收束
//  3. 开关关闭 → 永不早停（关 = 现行行为不变）
//  4. mate 稳定 → 2 层即停（杀棋加速判据）
//  5. 唯一着法 → 本轮 go 预算压地板（go depth 4），warmup 不受影响
//  6. warmup 预热豁免早停（同脚本下 warmup 无 stop、真实请求有）
//
// 全部走 fakeProc 真实命令管道（WriteLine 断言）+ 事件流（EventBestmove），
// 与生产路径同构。带 goScript 的脚本天然跑两轮：第 1 轮恒为 warmup（智能
// 豁免），断言都针对第 2 轮（用户 Request）。

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

const (
	smartStableFEN   = "rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR b - - 0 1"
	smartForcedFEN   = "3k5/9/9/9/9/9/9/9/3p5/4K4 w - - 0 1" // 与 xq/legal_moves_test 同款唯一应将局面
	smartStopBest    = "e3e4"                                // fake 对 stop 的回执着法
	smartScriptBest  = "h2e2"                                // 脚本尾包/摆动局的自然收束着法
	smartTestTimeout = 5 * time.Second
)

// smartRun 启动引擎 Run 并注册测试清理（六态共用的启动样板）。
func smartRun(t *testing.T, e *Engine) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go e.Run(ctx)
}

// smartStableStream 输出 depth from..to 的主线 info（同一 PV 首着；mate=true
// 时发 mate 分数）。
func smartStableStream(fe *fakeEngine, from, to, cp int, mate bool) {
	for d := from; d <= to; d++ {
		if mate {
			fe.write(fmt.Sprintf("info depth %d multipv 1 score mate 3 nodes 1000 nps 500000 hashfull 5 pv h2e2 b9c7", d))
		} else {
			fe.write(fmt.Sprintf("info depth %d multipv 1 score cp %d nodes 1000 nps 500000 hashfull 5 pv h2e2 b9c7", d, cp))
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// smartResult 等待指定局面的 bestmove 结果。
func smartResult(t *testing.T, events <-chan Event, fen string) *Result {
	t.Helper()
	deadline := time.After(smartTestTimeout)
	for {
		select {
		case ev := <-events:
			if ev.Kind == EventBestmove && ev.Result.Fen == fen {
				return ev.Result
			}
		case <-deadline:
			t.Fatalf("等待 %s 的 bestmove 超时", fen)
		}
	}
}

// smartWaitWarmup 等 warmup 预热起搜（cmds 出现首条 go）并给足其自然收束的
// 时间——此后 Request 必落在第 2 轮（warm=false、nth=2），消除"Request 抢在
// 首轮 select 之前入槽导致 warmup 被跳过"的启动竞态。
func smartWaitWarmup(t *testing.T, fe *fakeEngine) {
	t.Helper()
	deadline := time.After(smartTestTimeout)
	for {
		for _, c := range fe.cmds() {
			if strings.HasPrefix(c, "go ") {
				time.Sleep(50 * time.Millisecond) // 脚本轮 3-6 层 × 2ms，50ms 足够自然收束
				return
			}
		}
		select {
		case <-deadline:
			t.Fatal("warmup 预热未起搜")
		default:
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// smartCmdCount 统计 fake 收到的指定命令条数。
func smartCmdCount(fe *fakeEngine, cmd string) int {
	n := 0
	for _, c := range fe.cmds() {
		if c == cmd {
			n++
		}
	}
	return n
}

func TestSmartEarlyStopStable(t *testing.T) {
	e, fe, events := newTestEngine(t, false)
	e.searchTimeout = smartTestTimeout // 须在 Run 启动前写：collect 读它，启动后写=数据竞争
	smartRun(t, e)
	e.SetSmart(true)
	fe.goScript = func(goCmd string, nth int32) {
		smartStableStream(fe, 10, 15, 120, false)
		fe.write("bestmove " + smartScriptBest) // 兜底尾包：早停命中时排在 stop 回执之后（不会被读到当结果）
	}
	waitState(t, e, events, StateReady)
	smartWaitWarmup(t, fe)
	e.Request(smartStableFEN)
	res := smartResult(t, events, smartStableFEN)
	// 早停命中 = stop 先到 → fake 回执 e3e4；早停失效则读到脚本尾包 h2e2
	if res.BestMove != smartStopBest {
		t.Fatalf("早停路径应收 stop 回执着法 %s，got %s", smartStopBest, res.BestMove)
	}
	time.Sleep(50 * time.Millisecond) // 尾包余量排空后再断言 stop 恰一次
	if n := smartCmdCount(fe, "stop"); n != 1 {
		t.Fatalf("stop 应恰一次，got %d: %v", n, fe.cmds())
	}
}

func TestSmartEarlyStopOscillating(t *testing.T) {
	e, fe, events := newTestEngine(t, false)
	e.searchTimeout = smartTestTimeout // 须在 Run 启动前写：collect 读它，启动后写=数据竞争
	smartRun(t, e)
	e.SetSmart(true)
	fe.goScript = func(goCmd string, nth int32) {
		if nth == 1 { // warmup：智能豁免，自然收束
			smartStableStream(fe, 10, 12, 100, false)
			fe.write("bestmove " + smartScriptBest)
			return
		}
		// 主线在两个候选间逐层摆动：stableRun 恒 1，永不满足 K=3
		for d := 10; d <= 19; d++ {
			best := "h2e2"
			if d%2 == 1 {
				best = "b2e2"
			}
			fe.write(fmt.Sprintf("info depth %d multipv 1 score cp 120 nodes 1000 nps 500000 hashfull 5 pv %s b9c7", d, best))
			time.Sleep(2 * time.Millisecond)
		}
		fe.write("bestmove " + smartScriptBest)
	}
	waitState(t, e, events, StateReady)
	smartWaitWarmup(t, fe)
	e.Request(smartStableFEN)
	res := smartResult(t, events, smartStableFEN)
	if res.BestMove != smartScriptBest {
		t.Fatalf("摆动局面应自然收束: %s", res.BestMove)
	}
	if n := smartCmdCount(fe, "stop"); n != 0 {
		t.Fatalf("摆动主线不应早停，stop=%d: %v", n, fe.cmds())
	}
}

func TestSmartOffNeverStops(t *testing.T) {
	e, fe, events := newTestEngine(t, false)
	e.searchTimeout = smartTestTimeout // 须在 Run 启动前写：collect 读它，启动后写=数据竞争
	smartRun(t, e)
	// 不调 SetSmart：默认关。稳定流与测试 1 完全同款
	fe.goScript = func(goCmd string, nth int32) {
		smartStableStream(fe, 10, 15, 120, false)
		fe.write("bestmove " + smartScriptBest)
	}
	waitState(t, e, events, StateReady)
	smartWaitWarmup(t, fe)
	e.Request(smartStableFEN)
	if e.Smart() {
		t.Fatal("Smart() 默认应为 false")
	}
	res := smartResult(t, events, smartStableFEN)
	if res.BestMove != smartScriptBest {
		t.Fatalf("开关关闭应自然收束: %s", res.BestMove)
	}
	if n := smartCmdCount(fe, "stop"); n != 0 {
		t.Fatalf("开关关闭永不早停，stop=%d", n)
	}
}

func TestSmartMateStable(t *testing.T) {
	e, fe, events := newTestEngine(t, false)
	e.searchTimeout = smartTestTimeout // 须在 Run 启动前写：collect 读它，启动后写=数据竞争
	smartRun(t, e)
	e.SetSmart(true)
	fe.goScript = func(goCmd string, nth int32) {
		if nth == 1 { // warmup：智能豁免，自然收束
			smartStableStream(fe, 10, 12, 100, false)
			fe.write("bestmove " + smartScriptBest)
			return
		}
		// mate 分数 + 同一首着：d=11 即达"稳定 2 层"判据；不写尾包防重复收束
		smartStableStream(fe, 10, 15, 0, true)
	}
	waitState(t, e, events, StateReady)
	smartWaitWarmup(t, fe)
	e.Request(smartStableFEN)
	res := smartResult(t, events, smartStableFEN)
	if res.BestMove != smartStopBest {
		t.Fatalf("mate 稳定应收 stop 回执着法: %s", res.BestMove)
	}
	if n := smartCmdCount(fe, "stop"); n != 1 {
		t.Fatalf("mate 早停 stop 应恰一次，got %d", n)
	}
}

func TestSmartForcedFloor(t *testing.T) {
	e, fe, events := newTestEngine(t, false)
	e.searchTimeout = smartTestTimeout // 须在 Run 启动前写：collect 读它，启动后写=数据竞争
	smartRun(t, e)
	e.SetSmart(true)
	// 不设 goScript：默认 go 处理正常搜完。用户深度 8（testOpts），唯一着法
	// 局面地板 4 → 用户请求走 go depth 4；warmup（初始局面 44 着法，不触发
	// 地板）恒 go depth 8——顺带断言 warmup 不受地板影响。
	waitState(t, e, events, StateReady)
	smartWaitWarmup(t, fe)
	e.Request(smartForcedFEN)
	smartResult(t, events, smartForcedFEN)
	var sawFloor, sawFull bool
	for _, c := range fe.cmds() {
		switch c {
		case "go depth 4":
			sawFloor = true
		case "go depth 8":
			sawFull = true
		}
	}
	if !sawFloor {
		t.Fatalf("唯一着法局面未压地板（应含 go depth 4）: %v", fe.cmds())
	}
	if !sawFull {
		t.Fatal("warmup 预热不应受地板影响（应含 go depth 8）")
	}
}

func TestSmartWarmupExempt(t *testing.T) {
	e, fe, events := newTestEngine(t, false)
	e.searchTimeout = smartTestTimeout // 须在 Run 启动前写：collect 读它，启动后写=数据竞争
	smartRun(t, e)
	e.SetSmart(true)
	fe.goScript = func(goCmd string, nth int32) {
		if nth == 1 {
			// warmup：稳定流 + 尾包——豁免正确时全程无 stop、自然收束
			smartStableStream(fe, 10, 15, 120, false)
			fe.write("bestmove " + smartScriptBest)
			return
		}
		smartStableStream(fe, 10, 15, 120, false) // 真实请求：早停必中（无尾包）
	}
	waitState(t, e, events, StateReady)
	smartWaitWarmup(t, fe)
	e.Request(smartStableFEN)
	res := smartResult(t, events, smartStableFEN)
	if res.BestMove != smartStopBest {
		t.Fatalf("真实请求应收 stop 回执着法: %s", res.BestMove)
	}
	// 判别：第一条 stop 必须晚于第二条 position（warmup 全程无 stop）
	cmds := fe.cmds()
	secondPos, posSeen := -1, 0
	stopAt := -1
	for i, c := range cmds {
		if strings.HasPrefix(c, "position fen ") {
			posSeen++
			if posSeen == 2 {
				secondPos = i
			}
		}
		if c == "stop" && stopAt < 0 {
			stopAt = i
		}
	}
	if stopAt < 0 || stopAt < secondPos {
		t.Fatalf("warmup 未豁免早停（首条 stop 在第 2 次 position 之前）: %v", cmds)
	}
}
