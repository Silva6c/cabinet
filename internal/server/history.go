// per-session 局面历史（2026-11-07 防三次重复判和，用户定稿"第 3 次强制
// 变招"）：手机端上报 position 的 history 字段恒空（RemoteAnalysisClient
// 硬编码 emptyList），PC 端从连续局面 diff 自行攒集。消费点：
//  1. 引擎 Request 带 moves——皮卡鱼知道重复局面，重复线打和棋分、bestmove
//     自动避让（治本）
//  2. 桔库/沉淀/云库/残局命中下发前的三次重复预演过滤——库着法若形成第三次
//     重复局面则跳过/警示（治标，用户主诉"沉淀库重复走 3 次以上自动判和"的
//     直接解）
//  3. 引擎终值三次重复规避（2026-11-15 实战七问补全：引擎路径此前无任何
//     规避，均势残局直搜循环 e3f5↔f5e3 直到判和）——终值着法将判和时带
//     rep_risk 警示照发并触发一次性 4 线替代重搜（engine_pump.go repAvoid
//     状态机），本文件只供 occ 出现序列与 moveCausesTripleRep 判定
//  4. 轮次错误检测（2026-11-07 用户反馈"经常出现"的证据链，2026-11-09 日志
//     归因后三分类）：仅"单步可解释但行棋方未交替"才 Warn（真错误）；identical
//     重发 Debug、两步合并 Info、识别抖动 Debug——告警只留给真信号
//
// 2026-11-15 结构调整：fens/moves（搜索链，要求可解释推进）与 occ（判和
// 出现序列，不要求）解耦——链重置不再清空判和记忆，分工与陷阱见
// sessionHistory 注释。
package server

import (
	"strings"

	"cabinet/internal/xq"
)

// maxHistoryLen 局面历史上限（含当前）。一整局超不过 200 手，取 240 兜住
// "满 100 步开外仍在走"的长局；超限从头丢弃（丢头部 = 丢最早的重复记录，
// 影响可忽略——判和关切的是近期重复）。
const maxHistoryLen = 240

// sessionHistory 单会话对局历史，两套并行数据（2026-11-15 实战七问解耦）：
//   - fens/moves：相邻局面 diff 推出的搜索链（moves[i] 由 fens[i]→fens[i+1]
//     推得，len(moves)=len(fens)-1），供引擎 position moves 回放——链的连续性
//     是硬约束，无法解释的跳变必须整段重置；
//   - occ：局面出现序列（只记"哪个局面出现过"，不要求可解释推进）——判和
//     记忆。重置搜索链的跳变（两步合并/识别抖动/悔棋）不产生脏 occ（跳变
//     前后都是真实出现过的盘面），照常追加；若把判和记忆也挂在 fens 上，
//     一次识别抖动就把"已二现"清成"从没见过"，三次重复规避（库/沉淀/引擎
//     三路闸）集体失明——16:08 实锤 19s 内 3 次重置、沉淀循环重放 d0e0↔e0d0
//     直到判和。
//
// ⚠读取侧分工（2026-11-15 红队 A1）：moveCausesTripleRep（库/沉淀/云/终值
// 三次重复闸 + 替代重搜选择）读 occ；hasRepetitionRisk（是否给引擎带历史）
// 保持读 fens——occ 判出重复而搜索链已重置时，链上根本没有那个重复局面，
// 带历史=把"说有重复、链上却无"的假历史喂给皮卡鱼，反触发"超避让"换招
// （vc31 棋力下降的机制）。occ 只做闸门判定，不做引擎输入。
type sessionHistory struct {
	fens  []string
	moves []string
	occ   []string
}

// newSessionHistory 新建会话历史：首条局面同时进搜索链与出现序列。
func newSessionHistory(fen string) *sessionHistory {
	return &sessionHistory{fens: []string{fen}, occ: []string{fen}}
}

// occAppend 追加一条局面出现记录并维持上限（丢头 = 丢最早的出现记录，判和
// 关切的是近期重复）。调用方持 histMu，无并发。
func occAppend(occ []string, fen string) []string {
	occ = append(occ, fen)
	if len(occ) > maxHistoryLen {
		occ = occ[len(occ)-maxHistoryLen:]
	}
	return occ
}

// notePositionFen 记一条已通过合法性校验的局面（handlePosition 校验段之后
// 调用）。isNewGame 为手机 NEW_GAME 标记（新局从零攒）。
// 推进规则（2026-11-09 日志归因后三分类，此前一律 diff 失败→重置+告警）：
//   - identical 重发（补发/重搜重放同局面）→ 短路 return：历史链保持（此前
//     走 diff 失败→整段重置，防判和记忆被无谓清空；还占当时 78 条"轮次错误"
//     告警的 23%，全紧跟引擎就绪自动补发），Debug 留痕；
//   - 单步推进 → 追加；行棋方未交替 Warn（真轮次错误的唯一残留入口）；
//   - 多格跳变 → 按差格数分类：3~4 格 = 两步合并（对手回手击穿确认窗口，
//     我方着法未上行——"停在上一帧"主诉在 PC 侧的投影，Info 留痕）；
//     其余 = 识别抖动（Debug）。两者都整段重置——无法解释的序列宁可丢历史
//     （退化为无历史快照搜索）也不留脏序列污染重复判定。
func (s *Server) notePositionFen(sessionID, fen string, isNewGame bool) {
	s.histMu.Lock()
	defer s.histMu.Unlock()
	if s.hist == nil {
		s.hist = map[string]*sessionHistory{}
	}
	h := s.hist[sessionID]
	if h == nil || isNewGame {
		s.hist[sessionID] = newSessionHistory(fen)
		return
	}
	prev := h.fens[len(h.fens)-1]
	// identical 重发短路：补发（welcome/not_ready）/重搜（set_engine/force 同
	// 局面）重放同一局面——历史不动、建议链照走，只降级留痕。occ 同样不追加
	//（重发不产生新的出现，否则同一局面被计数两次假性逼近判和）
	if fen == prev {
		s.log.Debug("局面重发（补发/重搜），历史链保持", "fen", fen)
		return
	}
	// 开局阵型兜底（2026-11-15 红队 A2）：手机 NEW_GAME 标记只有识别器发得
	// 出（isStartLike），旧版手机/手动拼局/中途接入不发——盘面回到起始阵型
	// 即按换局处理，搜索链与 occ 一并重置，上一局的判和记忆不得跨局污染。
	// 中局 32 子全部归位在真实对局中不可达（evaluateAdoption 已用同一判据
	// 清采纳锚点），误清风险可忽略。
	if boardSegOf(fen) == startBoardSeg {
		s.log.Debug("盘面回到起始阵型（旧端换局兜底），历史与出现记录重置", "fen", fen)
		s.hist[sessionID] = newSessionHistory(fen)
		return
	}
	prevSide, curSide := fenSide(prev), fenSide(fen)
	// 盘面级单步解释（不校验行棋方）：子真走了且几何合法 = 物理事实，轮次
	// 字段对不对另判——这是真轮次错误与普通跳变的分界
	mv, moverRed, explainable := xq.DiffSingleMoveBoard(prev, fen)
	if !explainable {
		cells := boardDiffCells(prev, fen)
		if cells >= 3 && cells <= 4 && prevSide == curSide {
			// 两步合并签名：连续同侧 + 恰 3~4 格差（两次异色走子；3 格=第二手
			// 吃掉第一手的落子）。正常对局行棋方必交替，同侧多格差几乎必为
			// "我方着法没赶上确认、被对手回手候选覆盖"——Info 归因留痕
			s.log.Info("中间局面未上行（两步合并），历史重置", "cells", cells, "prev", prev, "cur", fen)
		} else {
			// 2 格但非走子结构（漏检+幻影）或 ≥5 格乱差 = 识别抖动（漏检振荡/
			// 动画中间态），Debug 降级——常 Warn 会淹没真信号
			s.log.Debug("局面非单步跳变（识别抖动），历史重置", "cells", cells, "prev", prev, "cur", fen)
		}
		// 搜索链重置但判和记忆保留：跳变前后都是真实出现过的盘面（occ 语义
		// 不要求可解释推进），occ 追加本局面后带上历史
		s.hist[sessionID] = &sessionHistory{fens: []string{fen}, occ: occAppend(h.occ, fen)}
		return
	}
	prevRed := prevSide == "w"
	switch {
	case prevSide != "" && curSide != "" && prevSide != curSide && moverRed == prevRed:
		// 正常单步推进：行棋方正确交替且移动子属上一局面行棋方
		h.fens = append(h.fens, fen)
		h.moves = append(h.moves, mv)
		h.occ = occAppend(h.occ, fen)
		if len(h.fens) > maxHistoryLen {
			h.fens = h.fens[len(h.fens)-maxHistoryLen:]
			h.moves = h.moves[len(h.moves)-(maxHistoryLen-1):]
		}
		return
	case prevSide != "" && prevSide == curSide:
		// 单步可解释但行棋方未交替 = 真轮次识别错误（子走了、侧没翻）。
		// 经 handlePosition 同侧重锚（回 bad_position 自愈回环）后仍到达此处
		// 的残量：prev 侧卡旧值形态（移动子属当前行棋方，cur 反而正确——错误
		// 在上一帧）与重锚翻转后非法的回退。移动子颜色是物理事实，据此可归因。
		// 历史重置（同侧序列喂引擎链回放必败，宁丢历史不留脏序列）
		s.log.Warn("疑似轮次识别错误：局面行棋方未交替",
			"side", prevSide, "mover_red", moverRed, "prev", prev, "cur", fen)
		s.hist[sessionID] = &sessionHistory{fens: []string{fen}, occ: occAppend(h.occ, fen)}
		return
	default:
		// 移动子不属于上一局面行棋方但轮次翻转 = 悔棋/手动编辑特征
		//（引擎链回放的 FenAfter 校验移动子归属必败，重置不留无用链）
		s.log.Debug("单步解释成立但移动子与上一行棋方矛盾（悔棋/编辑特征），历史重置",
			"mover_red", moverRed, "prev", prev, "cur", fen)
		s.hist[sessionID] = &sessionHistory{fens: []string{fen}, occ: occAppend(h.occ, fen)}
	}
}

// boardDiffCells 比较两个 FEN 的盘面段（第 1 段），返回 90 格展开后内容不同
// 的格子数。畸形 FEN（行数/格数不对）返回 -1，调用方按泛化分类处理。
func boardDiffCells(prevFen, curFen string) int {
	p, ok1 := expandBoardSeg(prevFen)
	c, ok2 := expandBoardSeg(curFen)
	if !ok1 || !ok2 {
		return -1
	}
	n := 0
	for i := range p {
		if p[i] != c[i] {
			n++
		}
	}
	return n
}

// expandBoardSeg 展开 FEN 盘面段为 90 格序列（数字展开为空格占位 '.'）。
func expandBoardSeg(fen string) ([]byte, bool) {
	seg := strings.SplitN(strings.TrimSpace(fen), " ", 2)[0]
	rows := strings.Split(seg, "/")
	if len(rows) != 10 {
		return nil, false
	}
	out := make([]byte, 0, 90)
	for _, r := range rows {
		for i := 0; i < len(r); i++ {
			ch := r[i]
			if ch >= '1' && ch <= '9' {
				for k := 0; k < int(ch-'0'); k++ {
					out = append(out, '.')
				}
			} else {
				out = append(out, ch)
			}
		}
	}
	if len(out) != 90 {
		return nil, false
	}
	return out, true
}

// fenSide FEN 行棋方段（"w"/"b"；畸形 FEN 返回空串不判错）。
func fenSide(fen string) string {
	parts := strings.SplitN(fen, " ", 2)
	if len(parts) < 2 || parts[1] == "" {
		return ""
	}
	return parts[1][:1]
}

// boardSegOf FEN 盘面段（首段，展开比对前的原始段）。
func boardSegOf(fen string) string {
	return strings.SplitN(strings.TrimSpace(fen), " ", 2)[0]
}

// historyForSession 引擎带历史请求的完整三元组：startFen=会话首个局面
// （UCI position 的 fen 部分），moves=从它走到当前局面的序列，curFen=当前
// 终局面。ok=false 表示无历史（调用方退快照 Request）。
// ⚠调用语义（vc27 事故教训）：UCI `position fen <F> moves <m...>` 是"从 F
// 走到终局面"——startFen 必须是历史起点而非当前局面。
func (s *Server) historyForSession(sessionID string) (curFen, startFen string, moves []string, ok bool) {
	s.histMu.Lock()
	defer s.histMu.Unlock()
	h := s.hist[sessionID]
	if h == nil || len(h.moves) == 0 {
		return "", "", nil, false
	}
	out := make([]string, len(h.moves))
	copy(out, h.moves)
	return h.fens[len(h.fens)-1], h.fens[0], out, true
}

// hasRepetitionRisk 报告会话链上是否已存在重复局面（任一局面出现 ≥2 次
// = 已 2-fold）。**条件带历史的核心判据**（2026-11-09 用户反馈"棋力疑似
// 下降"归因定稿）：皮卡鱼对"搜索树上转置到链上任一已出现局面"的着法按
// 和棋 0 分处理（真判和需 3 次）——链上无重复时带历史等于无谓的"超避让"
// （优势着法被误判走向和棋而换招，vc31 棋力下降的机制）；链上已有 2-fold
// 时避让才有正当性（下一步即可能 3-fold 判和，正是"第 3 次强制变招"要的
// 行为）。平时走快照，引擎输入与 vc19 逐字节同口径。
func (s *Server) hasRepetitionRisk(sessionID string) bool {
	s.histMu.Lock()
	defer s.histMu.Unlock()
	h := s.hist[sessionID]
	if h == nil {
		return false
	}
	seen := make(map[string]struct{}, len(h.fens))
	for _, f := range h.fens {
		if _, dup := seen[f]; dup {
			return true
		}
		seen[f] = struct{}{}
	}
	return false
}

// requestEngineHistory 按能力与风险下发引擎请求：
//   - 引擎桥支持 HistoryEngine、会话有可用历史、**且链上已出现重复局面**
//     （hasRepetitionRisk）→ 带历史制式（皮卡鱼避让三次判和，"第 3 次强制
//     变招"）；
//   - 其余一切情况 → 快照制式（与 vc19 引擎输入逐字节同口径，棋力无偏）。
//
// pvOnce 是单次多线覆盖（协议 §4.4 position.multi_pv_once，2026-11-09 双击
// 小棋盘"单次三变解析"）：>0 且引擎桥支持 MultiPVOnceEngine 时按指定线数
// 发起（历史随行判定照旧——两者正交可叠加）；引擎桥不支持（测试桩/旧桥）
// 忽略线数走常规请求。
//
// fen 是当前局面（终局面口径，与 lastFen/建议帧一致）；sessionID 是提交
// 局面的连接（历史归属，多设备各攒各的）。
func (s *Server) requestEngineHistory(fen, sessionID string, pvOnce int) {
	if s.eng == nil {
		return
	}
	// 历史随行判定（条件带历史，见 hasRepetitionRisk）：链上已 2-fold 才带
	// 历史，平时快照——引擎输入与 vc19 同口径，棋力无偏
	useHistory := s.hasRepetitionRisk(sessionID)
	// 提交留痕（2026-11-15 观测补齐）：请求提交此前零日志——16:05"无指导
	// 77.5s"窗口无法区分"请求没提交"与"结果被丢"，排障黑洞
	s.log.Info("引擎请求已提交", "fen", fen, "use_history", useHistory, "pv_once", pvOnce)
	var startFen string
	var moves []string
	if useHistory {
		curFen, sf, mv, ok := s.historyForSession(sessionID)
		// 防御：历史链终局面与请求局面不符（理论不可达——notePositionFen
		// 先于本调用记录了 fen；不成立说明状态错位，退快照保平安）
		if ok && curFen != fen {
			ok = false
		}
		startFen, moves = sf, mv
		useHistory = ok
	}
	if pvOnce > 0 {
		if mo, capable := s.eng.(MultiPVOnceEngine); capable {
			if useHistory {
				mo.RequestHistoryMultiPV(fen, startFen, moves, pvOnce)
			} else {
				mo.RequestMultiPV(fen, pvOnce)
			}
			return
		}
		// 引擎桥无单次多线能力：忽略 pvOnce 继续常规分流（§7 宽容语义的
		// 引擎侧对应——降级为普通重搜，功能无损）
	}
	if useHistory {
		if he, capable := s.eng.(HistoryEngine); capable {
			he.RequestHistory(fen, startFen, moves)
			return
		}
	}
	s.eng.Request(fen)
}

// moveCausesTripleRep 预演"当前局面走 move 后形成的新局面"是否已出现过
// ≥2 次（走着即第三次重复=判和）。读 occ（出现序列，2026-11-15 起与搜索链
// 解耦——两步合并/识别抖动的链重置不再清空判和记忆，16:08 实锤根因）；occ
// 含当前局面（notePositionFen 先记后查），出现计数天然把"刚走过一轮回到该
// 局面"算进去。
// 推演失败（非法着法/畸形 FEN）一律放行——过滤是尽力而为的保守动作，
// 不因推演问题拦正常建议。
func (s *Server) moveCausesTripleRep(sessionID, curFen, move string) bool {
	next, ok := xq.FenAfter(curFen, move)
	if !ok {
		return false
	}
	s.histMu.Lock()
	defer s.histMu.Unlock()
	h := s.hist[sessionID]
	if h == nil {
		return false
	}
	n := 0
	for _, f := range h.occ {
		if f == next {
			n++
		}
	}
	return n >= 2
}

// dropHistory 会话注销时清历史（session.go 注销路径调用），防长跑进程
// map 无限增长。
func (s *Server) dropHistory(sessionID string) {
	s.histMu.Lock()
	delete(s.hist, sessionID)
	s.histMu.Unlock()
}
