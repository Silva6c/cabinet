// smart.go 智能思考（2026-11-02 用户定稿，仅 PC 端）：按局面与搜索收敛度
// 自适应分配算力——手机端完全无感知、协议零改动（PC 调参面板开关，行为
// 叠加在现行深度制/时间制之上，手机端仍可远程调节制式与档位）。
//
// 两级机制：
//  1. 被迫着法预判（searchLoop 起搜前）：合法着法极少（唯一/≤3）的局面本轮
//     go 预算压到地板——唯一着法跑满 20 层/5 秒是纯浪费，地板即秒出。
//  2. 收敛早停（collect 读 info 流）：主线 PV 首着连续多层稳定且分数平稳即
//     提前 stop 收束。UCI stop 语义下 bestmove 照常返回、Result 形态不变，
//     下发/沉淀链路零感知——只省时间。
//
// ⚠铁律：早停的 stop 绝不置 preempt——preempt 语义是"作废本轮结果"（外部
// Request 打断才用），早停要的恰恰是接受本轮结果；也不碰 aborted 链。
// 取舍：MultiPV>1 只判主线稳定（次线可能偏浅，速度优先，登记）；K/下限/
// 摆动阈为保守起步常量，误判代价=建议深度略浅，开关随时可关。
package engine

import (
	"cabinet/internal/book"
	"cabinet/internal/xq"
)

// 智能思考参数（包内私有，不暴露 UI）。
const (
	// smartStableLayers 主线首着连续稳定层数阈值：皮卡鱼在焦灼局面 bestmove
	// 常在 2~3 个候选间摆动，K=3 过滤掉大多数假稳定。
	smartStableLayers = 3
	// smartMinDepth 允许早停的最低深度：更浅的"稳定"不可信（水平线效应）。
	smartMinDepth = 10
	// smartScoreSwingCP 稳定窗内主线 cp 分数极差上限：分数大幅摆动说明战术
	// 变化仍未清完，不许停。mate 分数不受此限（有独立的 2 层加速判据）。
	smartScoreSwingCP = 60
	// 被迫/极少着法的本轮预算地板：legal==1 用 Forced、legal∈{2,3} 用 Few；
	// 深度制/时间制各取 min(用户值, 地板)。
	smartForcedDepth      = 4
	smartForcedMovetimeMS = 500
	smartFewDepth         = 10
	smartFewMovetimeMS    = 1000
)

// countLegalMoves FEN 的合法着法数；解析失败返回 -1（预判放弃——测试假 FEN
// 与异常输入天然安全）。复杂度量级：全盘 pieceTargets 枚举 <1ms，相对数秒级
// 搜索可忽略。
func countLegalMoves(fen string) int {
	b, redGo, err := book.ParseFEN(fen)
	if err != nil {
		return -1
	}
	return len(xq.LegalMoves(b, redGo))
}

// smartBudget 本轮 go 的预算地板（depthFloor==0 = 不干预，按用户设置）。
// fen 为本局局面；warmup 预热由调用方排除（保预热本意）。
func smartBudget(fen string) (depthFloor, movetimeFloor int) {
	switch n := countLegalMoves(fen); {
	case n < 0:
		return 0, 0
	case n == 1:
		return smartForcedDepth, smartForcedMovetimeMS
	case n <= 3:
		return smartFewDepth, smartFewMovetimeMS
	default:
		return 0, 0
	}
}

// smartTracker 收敛早停状态机。每轮搜索新建（状态随搜索复位）；只喂主线
// info（MultiPV 缺省或 1，调用方过滤）。
type smartTracker struct {
	prevBest  string                 // 上一条主线的 PV 首着（稳定判据）
	stableRun int                    // 主线首着连续不变层数（从 smartMinDepth 起算）
	scores    [smartStableLayers]int // 最近 K 层主线 cp 分数（环形窗）
	scoreN    int                    // 已入窗分数个数（≥K 后环形窗即"最近 K 层"）
	mateRun   int                    // 主线连续 mate 层数（2 层即停的加速判据）
	stopped   bool                   // 本轮已判停（observe 恒 false，stop 恰一次）
}

// observe 喂一条主线 info；返回 true = 早停判据命中（调用方向进程发 stop）。
func (t *smartTracker) observe(info *Info) bool {
	if t.stopped || info.Depth < smartMinDepth {
		return false
	}
	if len(info.PV) == 0 {
		return false // 无 PV 无从判主线稳定
	}
	best := info.PV[0]
	if best != t.prevBest {
		t.prevBest = best
		t.stableRun = 1
		t.scoreN = 0
		t.mateRun = 0
	} else {
		t.stableRun++
	}
	switch {
	case info.HasMate:
		// mate 是绝对结论：深一层只是缩短将杀步数，主线首着稳定 2 层即收束
		t.mateRun++
		if t.stableRun >= 2 && t.mateRun >= 2 {
			t.stopped = true
			return true
		}
	default:
		// cp 分数：入环形窗；主线稳定 K 层且窗内极差 ≤ 阈值才许停
		//（缺分数的 info 行以 0 计入——只会让窗极差变大、更难停，方向安全）
		t.mateRun = 0
		t.scores[t.scoreN%len(t.scores)] = info.ScoreCP
		t.scoreN++
		if t.stableRun >= smartStableLayers && t.scoreN >= smartStableLayers {
			lo, hi := t.scores[0], t.scores[0]
			for _, v := range t.scores {
				if v < lo {
					lo = v
				}
				if v > hi {
					hi = v
				}
			}
			if hi-lo <= smartScoreSwingCP {
				t.stopped = true
				return true
			}
		}
	}
	return false
}
