package server

import (
	"bytes"
	"io"
	"log/slog"
	"strings"
	"testing"

	"cabinet/internal/engine"
	"cabinet/internal/xq"
)

// 历史链测试共用局面（自洽生成：FenAfter 机械搬子不校验伪合法，序列稳定）
var (
	histStart = "rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR w - - 0 1"
)

// TestNotePositionFenDiffSequence 正常单步链：notePositionFen 逐局面推进，
// moves 序列与 FEN 序列一一对应；非单步跳变整段重置（宁丢历史不留脏序列）。
// 链上着法由 DiffSingleMove 与 FenAfter 的同一 PseudoLegal 推出/复验——
// 两端同盘面同行棋方，"diff 接受、FenAfter 拒绝"的分叉面不存在。
func TestNotePositionFenDiffSequence(t *testing.T) {
	s := &Server{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	afterCannon, ok := xq.FenAfter(histStart, "h2e2")
	if !ok {
		t.Fatal("FenAfter 推演失败")
	}
	afterHorse, ok := xq.FenAfter(afterCannon, "b9c7")
	if !ok {
		t.Fatal("FenAfter 推演失败")
	}

	s.notePositionFen("s1", histStart, true)
	s.notePositionFen("s1", afterCannon, false)
	s.notePositionFen("s1", afterHorse, false)
	// 断言走生产口径 historyForSession（三元组）：movesForSession 已随
	// 条件带历史改造退役（deadcode 2026-11-09 报告唯一不可达函数）
	curFen, startFen, got, ok := s.historyForSession("s1")
	if !ok || len(got) != 2 || got[0] != "h2e2" || got[1] != "b9c7" {
		t.Fatalf("moves 序列不符: %v %v", got, ok)
	}
	if startFen != histStart || curFen != afterHorse {
		t.Fatalf("起点/终局面不符: %s → %s", startFen, curFen)
	}

	// 非单步跳变（跳帧/换局）：历史重置为单元素
	s.notePositionFen("s1", histStart, false)
	if _, _, mv, ok := s.historyForSession("s1"); ok || len(mv) != 0 {
		t.Fatalf("跳变后历史应重置为空: %v %v", mv, ok)
	}

	// 注销清理
	s.dropHistory("s1")
	if _, _, mv, ok := s.historyForSession("s1"); ok || len(mv) != 0 {
		t.Fatal("注销后历史应清空")
	}
}

// TestMoveCausesTripleRep 三次重复规避判定：目标局面已在历史出现 2 次，
// 走一步即第三次重复 → true；别的着法/无历史/推演失败 → false。
// 用例序列为合法单步往返链（FenAfter 带伪合法校验，着法方向必须轮次正确）。
func TestMoveCausesTripleRep(t *testing.T) {
	s := &Server{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	m1, ok := xq.FenAfter(histStart, "b2e2") // 红炮当头 → 黑回合
	if !ok {
		t.Fatal("FenAfter 推演失败")
	}
	m2, ok := xq.FenAfter(m1, "b9c7") // 黑马 → 红回合
	if !ok {
		t.Fatal("FenAfter 推演失败")
	}

	// 注入历史（同包测试缝）：局面 m2 已出现 2 次，当前局面 m1（黑回合）。
	// 黑走 b9c7 → 到达 m2（历史已出现 2 次）= 第三次重复判和。
	// moveCausesTripleRep 读 occ（2026-11-15 解耦），注入同步带 occ
	s.histMu.Lock()
	fens := []string{histStart, m1, m2, histStart, m1, m2, m1}
	s.hist = map[string]*sessionHistory{
		"s1": {fens: fens, occ: append([]string(nil), fens...)},
	}
	s.histMu.Unlock()

	if !s.moveCausesTripleRep("s1", m1, "b9c7") {
		t.Fatal("b9c7 应判定为三次重复（m2 已出现 2 次）")
	}
	// e6e5 黑卒挺起：新局面不在历史 → 放行
	if s.moveCausesTripleRep("s1", m1, "e6e5") {
		t.Fatal("e6e5 不应判定为三次重复")
	}
	// 无历史会话放行
	if s.moveCausesTripleRep("none", m1, "b9c7") {
		t.Fatal("无历史会话应放行")
	}
}

// TestNotePositionFenIdenticalResend identical 重发短路（2026-11-09 日志归因）：
// 补发/重搜重放同局面此前走 diff 失败→整段重置，防判和历史链被无谓清空；
// 短路后历史保持、Debug 降级留痕。
func TestNotePositionFenIdenticalResend(t *testing.T) {
	var buf bytes.Buffer
	s := &Server{log: slog.New(slog.NewTextHandler(&buf,
		&slog.HandlerOptions{Level: slog.LevelDebug}))}
	afterCannon, _ := xq.FenAfter(histStart, "h2e2")
	afterHorse, _ := xq.FenAfter(afterCannon, "b9c7")

	s.notePositionFen("s1", histStart, true)
	s.notePositionFen("s1", afterCannon, false)
	s.notePositionFen("s1", afterHorse, false)
	// 重放末局面（补发形态）：历史链必须原样保持
	s.notePositionFen("s1", afterHorse, false)
	curFen, startFen, got, ok := s.historyForSession("s1")
	if !ok || len(got) != 2 || startFen != histStart || curFen != afterHorse {
		t.Fatalf("重发后历史链应保持: moves=%v ok=%v", got, ok)
	}
	if !strings.Contains(buf.String(), "局面重发") {
		t.Fatalf("重发应 Debug 留痕: %s", buf.String())
	}
	if strings.Contains(buf.String(), "疑似轮次识别错误") {
		t.Fatalf("重发不得再打轮次告警: %s", buf.String())
	}
}

// TestNotePositionFenClassification 多格跳变三分类（2026-11-09 日志归因）：
// 3~4 格差+同侧 = 两步合并（Info"两步合并"，不冒充轮次错误）；更大乱差 =
// 识别抖动（Debug）；单步同侧才保留轮次 Warn（既有用例覆盖）。
func TestNotePositionFenClassification(t *testing.T) {
	var buf bytes.Buffer
	s := &Server{log: slog.New(slog.NewTextHandler(&buf,
		&slog.HandlerOptions{Level: slog.LevelDebug}))}

	// 两步合并：红炮平中 + 黑马上右（4 格差），两侧轮次都是 w（同侧签名）
	step1, ok1 := xq.FenAfter(histStart, "h2e2")
	mergeFen, ok2 := xq.FenAfter(step1, "b9c7")
	if !ok1 || !ok2 {
		t.Fatal("FenAfter 推演失败")
	}
	s.notePositionFen("s1", histStart, true)
	s.notePositionFen("s1", mergeFen, false)
	if !strings.Contains(buf.String(), "两步合并") {
		t.Fatalf("4 格同侧差应归因两步合并: %s", buf.String())
	}
	if strings.Contains(buf.String(), "疑似轮次识别错误") {
		t.Fatalf("两步合并不得打轮次告警: %s", buf.String())
	}
	if _, _, mv, ok := s.historyForSession("s1"); ok || len(mv) != 0 {
		t.Fatalf("合并跳变后历史应重置: %v", mv)
	}

	// 识别抖动：3 步跳变（6 格差），Debug 降级
	buf.Reset()
	s.notePositionFen("s1", histStart, false)
	three, ok := xq.FenAfter(mergeFen, "e3e4") // 红挺兵（ucci e3=FEN 行 6 红兵）
	if !ok {
		t.Fatal("FenAfter 推演失败")
	}
	s.notePositionFen("s1", three, false)
	if !strings.Contains(buf.String(), "识别抖动") {
		t.Fatalf("大乱差应归因识别抖动: %s", buf.String())
	}
	if strings.Contains(buf.String(), "疑似轮次识别错误") {
		t.Fatalf("识别抖动不得打轮次告警: %s", buf.String())
	}
}

// TestNotePositionFenUndoReset 悔棋/编辑特征（2026-11-10 append 收紧回归）：
// 回退局面 = 移动子属"当前"行棋方而非上一局面行棋方——引擎链回放的 FenAfter
// 校验移动子归属必败，必须重置不留无用链（vc45 中间版曾误 append）。
func TestNotePositionFenUndoReset(t *testing.T) {
	var buf bytes.Buffer
	s := &Server{log: slog.New(slog.NewTextHandler(&buf,
		&slog.HandlerOptions{Level: slog.LevelDebug}))}
	afterCannon, _ := xq.FenAfter(histStart, "h2e2")
	afterHorse, _ := xq.FenAfter(afterCannon, "b9c7")

	s.notePositionFen("s1", histStart, true)
	s.notePositionFen("s1", afterCannon, false)
	s.notePositionFen("s1", afterHorse, false)
	// 悔棋回退：afterHorse(w) → afterCannon(b)，黑马退回 b9 = 移动子黑 ∉ prev 侧红
	s.notePositionFen("s1", afterCannon, false)
	if _, _, mv, ok := s.historyForSession("s1"); ok || len(mv) != 0 {
		t.Fatalf("悔棋回退应重置历史: %v", mv)
	}
	if !strings.Contains(buf.String(), "悔棋/编辑特征") {
		t.Fatalf("悔棋特征应 Debug 留痕: %s", buf.String())
	}
}

// boardDiffCells 辅助自检（expandBoardSeg 的 90 格展开与数字段语义）
func TestBoardDiffCells(t *testing.T) {
	if n := boardDiffCells(histStart, histStart); n != 0 {
		t.Fatalf("同局面应 0 格差: %d", n)
	}
	afterCannon, _ := xq.FenAfter(histStart, "h2e2")
	if n := boardDiffCells(histStart, afterCannon); n != 2 {
		t.Fatalf("单步应 2 格差: %d", n)
	}
	if n := boardDiffCells("bad", histStart); n != -1 {
		t.Fatalf("畸形 FEN 应 -1: %d", n)
	}
}

// TestSameSideDoubleMoveWarn 同色连走检测（轮次识别错误证据链，2026-11-07
// 用户反馈"经常出现轮次识别错误"）：连续两局面行棋方相同 = 同一方连走，
// notePositionFen 应打出 Warn 告警日志。
func TestSameSideDoubleMoveWarn(t *testing.T) {
	var buf bytes.Buffer
	s := &Server{log: slog.New(slog.NewTextHandler(&buf, nil))}
	afterCannon, _ := xq.FenAfter(histStart, "h2e2")

	s.notePositionFen("s1", histStart, true)
	// 人工构造同色连走：把 afterCannon 的行棋方段改回 w（正常应翻转为 b）——
	// 模拟识别端轮次推导错误（movedSide 歧义卡旧值等）
	sameSideCannon := strings.Replace(afterCannon, " b - - 0 1", " w - - 0 1", 1)
	if sameSideCannon == afterCannon {
		t.Fatal("行棋方改写失败，用例无效")
	}
	s.notePositionFen("s1", sameSideCannon, false)
	// 从 sameSideCannon（行棋方 w）单步推进：红挺兵。其前一局面
	// sameSideCannon 行棋方同为 w → 同一方连走两步 → 告警
	next, ok := xq.FenAfter(sameSideCannon, "e3e4")
	if !ok {
		t.Fatal("FenAfter 推演失败")
	}
	s.notePositionFen("s1", next, false)
	if !strings.Contains(buf.String(), "疑似轮次识别错误") {
		t.Fatalf("同色连走应打出告警日志，实际: %s", buf.String())
	}

	// 形态 B（2026-11-09 日志归因补测）：prev 侧卡旧值——上一帧就该走黑但写
	// 成红先，黑走出子后 cur 侧正确（红先），同侧跳变同样必须告警
	buf.Reset()
	s.histMu.Lock()
	s.hist = nil
	s.histMu.Unlock()
	m1, _ := xq.FenAfter(histStart, "b2e2") // 红炮当头 → 黑回合
	// 模拟 prev 侧卡旧值：把 m1 的行棋方改回 w（真实轮次是黑）
	stuckPrev := strings.Replace(m1, " b - - 0 1", " w - - 0 1", 1)
	if stuckPrev == m1 {
		t.Fatal("行棋方改写失败，用例无效")
	}
	cur, _ := xq.FenAfter(m1, "b9c7") // 黑马走出（cur 侧正确=红先）
	s.notePositionFen("s2", stuckPrev, true)
	s.notePositionFen("s2", cur, false)
	if !strings.Contains(buf.String(), "疑似轮次识别错误") {
		t.Fatalf("prev 侧卡旧值（形态 B）应告警: %s", buf.String())
	}
}

// TestHasRepetitionRisk 条件带历史的判据（2026-11-09 棋力归因定稿）：链上
// 任一局面出现 ≥2 次（已 2-fold）= 有重复风险；无重复链/空链 = false。
func TestHasRepetitionRisk(t *testing.T) {
	s := &Server{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if s.hasRepetitionRisk("none") {
		t.Fatal("空会话应无风险")
	}
	afterCannon, _ := xq.FenAfter(histStart, "h2e2")
	s.histMu.Lock()
	s.hist = map[string]*sessionHistory{"s1": {fens: []string{histStart, afterCannon}}}
	s.histMu.Unlock()
	if s.hasRepetitionRisk("s1") {
		t.Fatal("无重复链应无风险")
	}
	s.histMu.Lock()
	s.hist["s1"] = &sessionHistory{fens: []string{histStart, afterCannon, histStart}}
	s.histMu.Unlock()
	if !s.hasRepetitionRisk("s1") {
		t.Fatal("链上出现 2-fold 应有风险")
	}
}

// TestRequestEngineHistoryRouting 分流验证（条件带历史）：无重复风险恒走
// 快照 Request（引擎输入与 vc19 同口径）；有 2-fold 风险才走 RequestHistory。
func TestRequestEngineHistoryRouting(t *testing.T) {
	fb := newFakeBridge(engine.StateReady)
	s := &Server{log: slog.New(slog.NewTextHandler(io.Discard, nil)), eng: fb}
	afterCannon, _ := xq.FenAfter(histStart, "h2e2")

	// 无历史：快照
	s.requestEngineHistory(histStart, "s1", 0)
	// 有历史但链上无重复：仍快照（棋力与 vc19 同口径）
	s.histMu.Lock()
	s.hist = map[string]*sessionHistory{"s1": {fens: []string{histStart, afterCannon}, moves: []string{"h2e2"}}}
	s.histMu.Unlock()
	s.requestEngineHistory(afterCannon, "s1", 0)
	if n := len(fb.histReqs); n != 0 {
		t.Fatalf("无重复风险不得走带历史请求, got %d", n)
	}
	if n := len(fb.fens); n != 2 {
		t.Fatalf("无风险应两次快照请求, got %d", n)
	}

	// 链上出现 2-fold：切带历史
	cur, _ := xq.FenAfter(afterCannon, "b9c7")
	s.histMu.Lock()
	s.hist["s1"] = &sessionHistory{fens: []string{histStart, afterCannon, histStart, afterCannon, cur},
		moves: []string{"h2e2", "b9c7", "e2h2", "c7b9"}}
	s.histMu.Unlock()
	s.requestEngineHistory(cur, "s1", 0)
	if n := len(fb.histReqs); n != 1 {
		t.Fatalf("2-fold 风险应走带历史请求, got %d", n)
	}
	hr := fb.histReqs[0]
	if hr.startFen != histStart || hr.curFen != cur || len(hr.moves) != 4 {
		t.Fatalf("带历史请求三元组不符: %+v", hr)
	}

	// 单次多线分流（2026-11-09 双击小棋盘"单次三变解析"，协议 §4.4
	// multi_pv_once）：链上已 2-fold → 单次多线**带历史**请求（两能力叠加，
	// 线数意图与防判和历史同时保留）
	s.requestEngineHistory(cur, "s1", 3)
	if n := len(fb.pvReqs); n != 1 {
		t.Fatalf("pvOnce>0 应走单次多线请求, got %d", n)
	}
	pr := fb.pvReqs[0]
	if pr.pv != 3 || pr.fen != cur || pr.startFen != histStart || len(pr.moves) != 4 {
		t.Fatalf("单次多线带历史请求不符: %+v", pr)
	}

	// 清空重复风险：单次多线退快照制式（线数意图保留、历史不带）
	s.histMu.Lock()
	s.hist["s1"] = &sessionHistory{fens: []string{histStart, afterCannon}, moves: []string{"h2e2"}}
	s.histMu.Unlock()
	s.requestEngineHistory(afterCannon, "s1", 3)
	if n := len(fb.pvReqs); n != 2 {
		t.Fatalf("第二次单次多线请求未记录, got %d", n)
	}
	if pr := fb.pvReqs[1]; pr.pv != 3 || pr.fen != afterCannon || pr.startFen != "" || len(pr.moves) != 0 {
		t.Fatalf("无风险应走单次多线快照请求: %+v", pr)
	}

	// 防御分支组合（2026-11-09 审查补测）：历史链终点 ≠ 请求局面（ok=false）
	// + pvOnce>0 → 退单次快照——历史不可信时不带历史，线数意图保留
	s.histMu.Lock()
	s.hist["s1"] = &sessionHistory{fens: []string{histStart, afterCannon}, moves: []string{"h2e2"}}
	s.histMu.Unlock()
	s.requestEngineHistory(histStart, "s1", 3)
	if n := len(fb.pvReqs); n != 3 {
		t.Fatalf("防御分支应走单次多线快照, got %d", n)
	}
	if pr := fb.pvReqs[2]; pr.startFen != "" || pr.fen != histStart || len(pr.moves) != 0 {
		t.Fatalf("防御分支应不带历史: %+v", pr)
	}

	// 引擎桥无 MultiPVOnceEngine 能力（pvlessBridge）：忽略 pvOnce 走常规分流
	pb := &pvlessBridge{}
	s2 := &Server{log: slog.New(slog.NewTextHandler(io.Discard, nil)), eng: pb}
	s2.requestEngineHistory(histStart, "none", 3)
	if len(pb.fens) != 1 || len(pb.histReqs) != 0 {
		t.Fatalf("无能力桥 + 无历史应退快照: fens=%d hist=%d", len(pb.fens), len(pb.histReqs))
	}
	cur2, _ := xq.FenAfter(afterCannon, "b9c7")
	s2.histMu.Lock()
	s2.hist = map[string]*sessionHistory{"s1": {fens: []string{histStart, afterCannon, histStart, afterCannon, cur2},
		moves: []string{"h2e2", "b9c7", "e2h2", "c7b9"}}}
	s2.histMu.Unlock()
	s2.requestEngineHistory(cur2, "s1", 3)
	if len(pb.histReqs) != 1 {
		t.Fatalf("无能力桥 + 2-fold 应走常规带历史请求（降级不丢防判和）: %d", len(pb.histReqs))
	}
}

// pvlessBridge 无单次多线能力的引擎桥（仅 EngineBridge+HistoryEngine）：验证
// requestEngineHistory 对不支持 MultiPVOnceEngine 的桥忽略 pvOnce——降级走
// 常规分流而非报错（协议 §7 宽容语义的引擎侧对应）。单 goroutine 调用，免锁。
type pvlessBridge struct {
	fens     []string
	histReqs []histReq
}

func (b *pvlessBridge) Events() <-chan engine.Event { return nil }
func (b *pvlessBridge) State() string               { return engine.StateReady }
func (b *pvlessBridge) Request(fen string)          { b.fens = append(b.fens, fen) }
func (b *pvlessBridge) RequestHistory(curFen, startFen string, moves []string) {
	b.histReqs = append(b.histReqs, histReq{curFen: curFen, startFen: startFen, moves: moves})
}

// TestOccSurvivesChainReset 判和记忆与搜索链解耦（2026-11-15 实战七问核心
// 回归，16:08/15:34 实锤场景回放）：搜索链被"两步合并"重置后，occ 出现序列
// 保留——moveCausesTripleRep 仍能判出"走着即第三次重复"，沉淀/库/引擎三路
// 闸不再被识别抖动致盲；hasRepetitionRisk 保持读链（红队 A1：occ 判出重复
// 而链已重置时带历史=喂假历史，超避让复发），两者读取侧不对称是设计内。
func TestOccSurvivesChainReset(t *testing.T) {
	s := &Server{log: slog.New(slog.NewTextHandler(io.Discard, nil))}

	// 合法循环链（不经过起始阵型——起始阵型兜底会把回开局盘面当换局重置，
	// 语义如此）：S →(h2e2) Q1 →(b9c7) R1，此后 4 步循环 R1 →(b0c2) M1
	// →(c7e8) M2 →(c2b0) M3 →(e8c7) R1。循环两圈后 occ 中 R1/M1/M2/M3 各 2 次
	Q1, _ := xq.FenAfter(histStart, "h2e2")
	R1, _ := xq.FenAfter(Q1, "b9c7")
	M1, ok1 := xq.FenAfter(R1, "b0c2")
	M2, ok2 := xq.FenAfter(M1, "c7e8")
	M3, ok3 := xq.FenAfter(M2, "c2b0")
	back, ok4 := xq.FenAfter(M3, "e8c7")
	if !ok1 || !ok2 || !ok3 || !ok4 || back != R1 {
		t.Fatalf("循环链推演失败: %v %v %v %v back==R1: %v", ok1, ok2, ok3, ok4, back == R1)
	}
	if !s.noteOccSequence(t, "s1", []string{histStart, Q1, R1, M1, M2, M3, R1, M1, M2, M3, R1}) {
		t.Fatal("循环链构造失败")
	}
	if !s.moveCausesTripleRep("s1", R1, "b0c2") {
		t.Fatal("循环两圈后 b0c2 应判定三次重复（M1 已出现 2 次）")
	}

	// 两步合并重置（R1 → M2 直接跳变：4 格同侧差）：链清成单元素，
	// occ 保留全部出现记录并追加本局面
	s.notePositionFen("s1", M2, false)
	if _, _, mv, ok := s.historyForSession("s1"); ok || len(mv) != 0 {
		t.Fatalf("合并帧后链应重置: %v", mv)
	}
	if !s.moveCausesTripleRep("s1", M2, "c2b0") {
		t.Fatal("链重置后判和记忆应保留（M3 已出现 2 次），c2b0 应判三次重复")
	}
	// hasRepetitionRisk 保持链口径：链已重置（无重复）→ false，
	// 即使 occ 判出重复也不给引擎带历史（不对称是设计内，防假历史）
	if s.hasRepetitionRisk("s1") {
		t.Fatal("hasRepetitionRisk 必须读链不读 occ（链已重置应 false）")
	}
}

// noteOccSequence 测试缝：按单步链逐局面注入并验证每步都走"正常单步推进"
// 路径（轮次交替、单步可解释）——非法序列会让测试假绿。
func (s *Server) noteOccSequence(t *testing.T, session string, fens []string) bool {
	t.Helper()
	for i, f := range fens {
		s.notePositionFen(session, f, i == 0)
	}
	s.histMu.Lock()
	defer s.histMu.Unlock()
	h := s.hist[session]
	return h != nil && len(h.fens) == len(fens) && len(h.occ) == len(fens)
}

// TestOccIdenticalResendNotCounted identical 重发不追加 occ（2026-11-15）：
// 补发/force 重搜重放同局面不产生新的"出现"，否则同一局面被计数两次假性
// 逼近判和阈值。
func TestOccIdenticalResendNotCounted(t *testing.T) {
	s := &Server{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	Q1, _ := xq.FenAfter(histStart, "h2e2")
	s.notePositionFen("s1", histStart, true)
	s.notePositionFen("s1", Q1, false)
	s.histMu.Lock()
	n1 := len(s.hist["s1"].occ)
	s.histMu.Unlock()
	// 重放末局面 ×3（welcome 补发/force 重发形态）
	for i := 0; i < 3; i++ {
		s.notePositionFen("s1", Q1, false)
	}
	s.histMu.Lock()
	n2 := len(s.hist["s1"].occ)
	s.histMu.Unlock()
	if n1 != 2 || n2 != 2 {
		t.Fatalf("identical 重发不得追加 occ: before=%d after=%d", n1, n2)
	}
}

// TestOccClearedOnStartBoard 起始阵型兜底清 occ（2026-11-15 红队 A2）：
// 旧版手机/手动拼局不发 NEW_GAME——盘面回到起始阵型即按换局处理，上一局
// 的判和记忆不得跨局污染。
func TestOccClearedOnStartBoard(t *testing.T) {
	s := &Server{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	Q1, _ := xq.FenAfter(histStart, "h2e2")
	R1, _ := xq.FenAfter(Q1, "b9c7")
	M1, ok1 := xq.FenAfter(R1, "b0c2")
	M2, ok2 := xq.FenAfter(M1, "c7e8")
	M3, ok3 := xq.FenAfter(M2, "c2b0")
	back, ok4 := xq.FenAfter(M3, "e8c7")
	if !ok1 || !ok2 || !ok3 || !ok4 || back != R1 {
		t.Fatalf("循环链推演失败: %v %v %v %v", ok1, ok2, ok3, ok4)
	}

	// 上一局循环两圈，occ 里 M1/R1 已 2 次
	if !s.noteOccSequence(t, "s1", []string{histStart, Q1, R1, M1, M2, M3, R1, M1, M2, M3, R1}) {
		t.Fatal("循环链构造失败")
	}
	// 换局（无 NEW_GAME 标记）：直接回到起始阵型
	s.notePositionFen("s1", histStart, false)
	s.histMu.Lock()
	occ := len(s.hist["s1"].occ)
	s.histMu.Unlock()
	if occ != 1 {
		t.Fatalf("起始阵型应重置 occ，实际 %d 条", occ)
	}
	// 新局前两步不得被上一局记忆误判为重复
	s.notePositionFen("s1", Q1, false)
	s.notePositionFen("s1", R1, false)
	if s.moveCausesTripleRep("s1", R1, "b0c2") {
		t.Fatal("跨局记忆不得误判新局着法为三次重复（M1 新局仅 1 次）")
	}
}
