package server

import (
	"cabinet/internal/engine"
	"context"
	"time"
)

// enginePump 消费引擎事件，按协议节奏下发：
//   - 状态变化 → 立即广播 status（协议 §4.6）
//   - 搜索中 depth 变化或 bestmove 变化 → 立即定向下发 suggestion 给局面归属连接（§4.5）
//   - 搜索中其余 info → 500ms 节流广播 status（带实时指标）
//   - bestmove 终态 → 定向终值 suggestion + 广播 status
//
// suggestion 仅当结果 FEN 与最近客户端 position 一致时下发（"最新覆盖"幂等，
// 同时丢弃预热结果与被打断搜索的残留）；status 是引擎全局状态，保持广播。
//
// 并发约束：本函数是引擎事件唯一消费者，必须保持单 goroutine——事件须按
// 引擎发出顺序应用，lastDepth/lastBest 门控与 status 广播的单调序都依赖
// 这一顺序；并行消费会丢事件、乱序，并与门控状态竞争。
func (s *Server) enginePump(ctx context.Context) {
	var (
		lastDepth    = -1
		lastBest     string
		lastStatusAt time.Time
	)
	events := s.eng.Events()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-events:
			if !ok {
				return
			}
			switch ev.Kind {
			case engine.EventState:
				s.log.Info("引擎状态", "state", ev.State)
				s.searchStartNanos.Store(0) // 状态变化（重启/就绪）= 无进行中搜索
				s.hub.Broadcast(Status{frameBase: frame(TypeStatus), Engine: EngineStatus{State: ev.State}})

			case engine.EventInfo:
				if ev.Info == nil {
					continue
				}
				// "最新覆盖"（协议 §4.5）：中间 info 只属于它那次搜索的请求局面。
				// 与最新请求不符（预热、崩溃重启后的兜底搜索、已被覆盖的旧请求）
				// 一律不广播不缓存——否则会冒充最新局面的建议误导手机端。
				// want 在门控处单次读取后向下传：buildSuggestion 内不再二次
				// Load——两次读取之间 handlePosition 可能 Store 新 fen，旧搜索
				// 的着法会配上新局面 FEN 下发（手机端迟到过滤随之失效）。
				want, _ := s.lastFen.Load().(string)
				if want == "" || ev.Fen != want {
					continue
				}
				info := ev.Info
				// 时间制仪表计时（server 侧停表，见 searchStartNanos 注）：
				// 最新局面的首条 info 起表；已计时则保持（同一轮搜索的后续 info）
				if s.searchStartNanos.Load() == 0 {
					s.searchStartNanos.Store(time.Now().UnixNano())
				}
				best := bestOfLine(info)
				// 门控细节：Depth>0 排除不带 depth 的杂项 info（否则 0≠-1 每条
				// 误判为变化）；用 != 而非 > 是因 depth 并非严格单调（多线交错/
				// 引擎内部重启），漏推比多推更伤——多推只是重复帧，客户端以
				// 最新为准（§4.5 允许）。bestmove 终态后重置 lastDepth/lastBest
				// （见下方收束分支），下一轮搜索首条 info 才能照常推送。
				depthChanged := info.Depth > 0 && info.Depth != lastDepth
				bestChanged := best != "" && best != lastBest
				if depthChanged || bestChanged {
					// 协议 §4.5：depth 提升或 bestmove 变化即推（定向给局面归属连接）
					lastDepth, lastBest = info.Depth, best
					if sug, ok := s.buildSuggestion(info, want); ok {
						s.sendSuggestionToOwner(sug)
					}
					// depth 变化同时放宽 status 节流（§4.6：depth 变化也推 status）
					if depthChanged {
						lastStatusAt = time.Now()
						s.broadcastSearchStatus(info)
					}
					continue
				}
				// §4.6：非变化事件 500ms 节流
				if time.Since(lastStatusAt) >= 500*time.Millisecond {
					lastStatusAt = time.Now()
					s.broadcastSearchStatus(info)
				}

			case engine.EventCritical:
				// 皮卡鱼对未拦截住的非法局面报 CRITICAL ERROR 后立即退出进程
				//（看门狗会重启）。响应三件事：日志已由引擎层记 WARN；给局面
				// 归属连接回显错误（手机端不再"静默无建议"）；隔离 lastFen——
				// 看门狗重启 + set_engine 重放都会重新喂这个局面，不清会再崩
				//（2026-10-01 第二轮审查 P1-1 后果链）。
				s.log.Warn("引擎危急报错，隔离该局面", "fen", ev.Fen, "detail", ev.Message)
				if ev.Fen != "" {
					if want, _ := s.lastFen.Load().(string); want == ev.Fen {
						s.lastFen.Store("")
					}
					s.sendErrorToOwner(CodeBadPosition,
						"引擎拒绝当前局面并重启——点『更新』翻转走子方，或走一步棋自愈", ev.Fen)
				}

			case engine.EventBestmove:
				// 搜索物理收束即停表（旧局面/被覆盖请求的 bestmove 同样算收束——
				// 若只在"最新"分支停，下一轮搜索首条 info 会沿用旧起点）
				s.searchStartNanos.Store(0)
				r := ev.Result
				if r == nil {
					continue
				}
				if want, _ := s.lastFen.Load().(string); want == "" || r.Fen != want {
					s.log.Debug("丢弃非最新请求的结果", "result_fen", r.Fen)
					continue
				}
				if sug, ok := s.buildSuggestionFromResult(r); ok {
					s.sendSuggestionToOwner(sug)
					s.recordSuggestion(SuggestionRecord{
						Source: sug.Source, FEN: r.Fen, BestMove: sug.BestMove,
						ScoreCP: sug.ScoreCP(), ScoreMate: sug.ScoreMate(),
						PV: sug.PV, Depth: sug.Depth,
					})
					// 走势序列：每局面一个终态点，按设备分组追加——归属 = 最新
					// position 来源设备（ownerDev，与建议下发同源）；组内同局面
					// 重搜（切参数/补发）替换末点不追加。空键防御性不落点：上方
					// lastFen 门控已保证走到这里必有归属（trend.go 文件头）。
					if key, _ := s.ownerDev.Load().(string); key != "" {
						if pt, ok := trendPointFromSug(r.Fen, sug.ScoreCP(), sug.ScoreMate(), sug.WdlW); ok {
							s.trendAppend(key, pt)
						}
					}
				} else if r.BestMove == "null" {
					// "null" = 引擎无合法着法（2026-10-24 实锤）：真皮卡鱼对绝杀/
					// 困毙局面瞬间回 bestmove (none)，此前整条链路静默——不下发、
					// 不沉淀、无日志，手机端停在"轮到我方走"空等（用户误判通信
					// 故障）。现在明确告知终局：文案不咬死绝杀——残局识别漏子也
					// 能拼出"假无着"局面，给用户核对棋盘的自查出口。
					s.log.Info("引擎判定本局面无着可走，已下发终局提示", "fen", r.Fen)
					s.sendErrorToOwner(CodeNoMoves,
						"无着可走：已绝杀/困毙或棋面识别有误，请核对棋盘", r.Fen)
				}
				// 收束帧：nil 组出的仅含 state 的 status 是协议 §4.6 定稿的
				// "本轮建议收束信号"（depth/nodes/nps/hashfull 缺省），手机端
				// 以此判定本轮候选结束——不是可省的指标清零帧，勿删。
				s.broadcastSearchStatus(nil)
				lastDepth, lastBest = -1, ""
			}
		}
	}
}

// buildSuggestion 由搜索中 info 构造 suggestion。分数换算唯一入口是
// engine.ToRedScore（走子方视角→红方，含 mate 符号取负，见 engine/uci.go）：
// 下发侧换算一次后，沉淀写入与沉淀复用（handlePosition 步骤 2）都是红方
// 视角，全链路禁止二次取负。info 事件不带 FEN，want 由 enginePump 门控处
// 单次读取 lastFen 后传入（单一事实源）——本函数内不再二次 Load，防门控与
// 构造之间 lastFen 被新 position 覆盖导致旧着法配新局面；空 want 防御仅兜
// 未来新增调用方（现有门控已保证非空）。
func (s *Server) buildSuggestion(info *engine.Info, want string) (Suggestion, bool) {
	if want == "" {
		return Suggestion{}, false // 无客户端请求过：预热等结果不广播
	}
	redGo := engine.RedGoOf(want)
	cp, mate, hasMate := engine.ToRedScore(*info, redGo)
	sug := Suggestion{
		frameBase: frame(TypeSuggestion),
		Source:    "engine",
		Fen:       want,
		BestMove:  bestOfLine(info),
		PV:        info.PV,
		Depth:     info.Depth,
		Multipv:   info.MultiPV,
	}
	if sug.BestMove == "" {
		return Suggestion{}, false
	}
	if hasMate {
		sug.Score = &Score{Mate: &mate}
	} else {
		sug.Score = &Score{CP: &cp}
	}
	sug.WdlW, sug.WdlD, sug.WdlL = redWdlPercent(info, redGo)
	return sug, true
}

// buildSuggestionFromResult 由 bestmove 终态构造 suggestion。
func (s *Server) buildSuggestionFromResult(r *engine.Result) (Suggestion, bool) {
	// "null" = 皮卡鱼无着可走/非法局面兜底（engine.go 终态解析约定）：既不能
	// 下发（手机端会渲染出一条"null"建议）也不能沉淀（否则沉淀命中回放会
	// 长期复播这条垃圾行——2026-10-18 实测日志出现 depth=0 best=null 的复播）
	if r.BestMove == "" || r.BestMove == "null" {
		return Suggestion{}, false
	}
	var line *engine.Info
	if len(r.Lines) > 0 {
		line = &r.Lines[0]
	}
	info := engine.Info{}
	if line != nil {
		info = *line
	}
	sug := Suggestion{
		frameBase: frame(TypeSuggestion),
		Source:    "engine",
		Fen:       r.Fen,
		BestMove:  r.BestMove,
		PV:        info.PV,
		Depth:     info.Depth,
		// 终值只发第一线（Lines 已按 MultiPV 升序），序号恒 1
		Multipv: 1,
	}
	cp, mate, hasMate := engine.ToRedScore(info, r.RedGo)
	if hasMate {
		sug.Score = &Score{Mate: &mate}
	} else {
		sug.Score = &Score{CP: &cp}
	}
	sug.WdlW, sug.WdlD, sug.WdlL = redWdlPercent(&info, r.RedGo)
	return sug, true
}

// redWdlPercent 引擎 WDL（走子方视角千分比）→ 红方视角百分比，供 suggestion 帧下发；
// 引擎未输出 wdl 时三项均 nil。胜/负独立四舍五入、和取 100-w-l：三项独立舍入会出现
// 合计 99/101 的"概率不以 100 收尾"（如 95/897/8‰ → 10/90/1），观感即"胜率算错"。
// 极端分布（如 995/0/5‰ 舍入后 100/1）先压缩胜或负保证 w+l ≤ 100，再取差值——
// 否则 pd 钳 0 会产出合计 101（2026-10-18 实测修复）。
func redWdlPercent(info *engine.Info, redGo bool) (w, d, l *int) {
	if info == nil || !info.HasWdl {
		return nil, nil, nil
	}
	rw, _, rl := engine.ToRedWdl(*info, redGo)
	// 脏输入防御：合法引擎输出恒满足 0≤w,l 且 w+l≤1000，但 uci 解析对非法
	// 数字静默置 0、透传不校验——负值/超界值会经换算产出负百分比。入口
	// 统一钳回合法域，保证下游换算的数学前提（正常路径零影响）。
	if rw < 0 {
		rw = 0
	} else if rw > 1000 {
		rw = 1000
	}
	if rl < 0 {
		rl = 0
	} else if rl > 1000 {
		rl = 1000
	}
	if rw+rl > 1000 { // 极端脏输入：压缩较大侧回到合法域
		if rw > rl {
			rw = 1000 - rl
		} else {
			rl = 1000 - rw
		}
	}
	pw := (rw + 5) / 10 // 千分比四舍五入到百分比（胜/负独立舍入）
	pl := (rl + 5) / 10
	if pw+pl > 100 { // 双双进位越过 100：压缩较大的一侧，牺牲其 0.5‰ 内的精度
		if pw > pl {
			pw = 100 - pl
		} else {
			pl = 100 - pw
		}
	}
	pd := 100 - pw - pl // 和取差值，三项恒合计 100
	return &pw, &pd, &pl
}

// broadcastSearchStatus 广播搜索中（或刚收束）的引擎指标 status。
// info 为 nil 表示刚收束（指标清零只留状态）。同一份数据缓存给控制台快照。
// 限频责任在调用方（enginePump：depth 变化即推、其余 500ms 一次，§4.6 定稿
// 值非可调参数），本函数自身不限频——勿在此加节流造成双重限频。
func (s *Server) broadcastSearchStatus(info *engine.Info) {
	st := EngineStatus{State: s.EngineState()}
	if info != nil {
		st.Depth = info.Depth
		st.Nodes = info.Nodes
		st.NPS = info.NPS
		st.Hashfull = info.Hashfull
	}
	s.engMetrics.Store(st)
	s.hub.Broadcast(Status{frameBase: frame(TypeStatus), Engine: st})
}

// bestOfLine 取该搜索线首着法（bestmove 候选）。
func bestOfLine(info *engine.Info) string {
	if len(info.PV) > 0 {
		return info.PV[0]
	}
	return ""
}
