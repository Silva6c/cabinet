package server

import (
	"strings"
	"time"

	"cabinet/internal/book"
	"cabinet/internal/engine"
	"cabinet/internal/xq"
)

// bookNames 当前已启用开局库名列表（welcome.books 回填用）：无库/未加载返回
// 空切片（非 nil，JSON 序列化仍为 []）。此前恒硬编码空数组——库里 266 万条
// 记录 welcome 却报空，客户端无从展示库状态（第二轮审查 P1-3）。
func (s *Server) bookNames() []string {
	store, _ := s.book.Load().(*book.Store)
	if store == nil {
		return []string{}
	}
	stats := store.Stats()
	names := make([]string, 0, len(stats))
	for _, st := range stats {
		names = append(names, st.Name)
	}
	return names
}

// sendSuggestionToOwner 建议定向下发（协议 §4.5 路由）：suggestion 与具体局面
// 绑定，只发给提交该局面的连接——多设备同时对局时各收各的建议，互不串台。
// 归属连接已断开或尚未记录时丢弃（宁可不发、不可错发）。
func (s *Server) sendSuggestionToOwner(sug Suggestion) {
	// 采纳比对锚点维护（2026-10-17）：本函数是三条下发路径（桔库/沉淀/引擎）
	// 的唯一汇聚点——沉淀命中路径不落库但同样建立锚点，采纳统计不漏来源。
	// 同局面多帧（MultiPV 多线/桔库多候选）逐帧累加候选集；局面切换重建。
	// rank 缺省（桔库/沉淀/旧协议帧）按槽位 1；主选帧（rank 1）刷新来源与
	// 分数快照（副选帧桔库不带分，不能反向覆盖主选分数）。
	// load-改-store 全程持 pendingSugMu：enginePump/各 readLoop/云库 goroutine
	// 多路并发进入，无锁时并发写者互相整体覆盖（候选集丢条目、rank=1 快照
	// 互相抢）。网络写在锁外——SendToSession 是慢路径，不能拖住其他下发方。
	s.pendingSugMu.Lock()
	pend, _ := s.pendingSug.Load().(*pendingAdvice)
	if pend == nil || pend.fen != sug.Fen {
		pend = &pendingAdvice{fen: sug.Fen, moves: map[string]int{}}
	}
	rank := sug.Multipv
	if rank <= 0 {
		rank = 1
	}
	pend.moves[sug.BestMove] = rank
	if rank == 1 {
		pend.source = sug.Source
		pend.depth = sug.Depth
		pend.scoreCP = sug.ScoreCP()
		pend.scoreMate = sug.ScoreMate()
	}
	s.pendingSug.Store(pend)
	s.pendingSugMu.Unlock()

	// 下发留痕（2026-11-15 观测补齐）：建议下发此前零日志——16:05"无指导
	// 77.5s"窗口无法对账"PC 到底发没发"。MultiPV 多线每轮 ≤5 帧，量级无害
	s.log.Info("建议下发", "source", sug.Source, "best", sug.BestMove,
		"fen", sug.Fen, "depth", sug.Depth, "rank", rank)

	if id, ok := s.ownerSession.Load().(string); ok && id != "" {
		s.hub.SendToSession(id, sug)
	}
}

// startBoardSeg 标准开局盘面段（与手机端 AssistBoard.canonicalStart 同源）。
// PC 端按精确盘面段比对近似手机的 isStartLike（后者带 (9,7) 红马错位容错）——
// 错位形态识别不出新局，宁可漏判不误判：漏判走 DiffSingleMove 通道（单步解释
// 失败即清锚），误判才会丢真实样本。
const startBoardSeg = "rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR"

// evaluateAdoption 采纳比对（handlePosition 调用；必须在 lastFen.Store 覆盖
// 之前取 prev）。无效形态一律清锚宁缺勿错：
//   - prev==newFen：重发/补发/force 前奏——锚点保留，等真实变化
//   - 开局盘面段：新一局（换局重开）——清锚
//   - DiffSingleMove 失败：悔棋/手动编辑/多步跳变——清锚
//
// 有效形态产出 primary/candidate/miss 事件，无论结果锚点即清（一条建议只判一次）。
//
// ⚠已知瑕疵（2026-11-02 审查登记，暂不修）：pendingSug/lastFen 是全局单值，
// 多设备并行各下一盘棋时锚点会串台——B 的下一局面按 A 的建议锚比对，多数以
// DiffSingleMove 失败清锚收场（丢采纳统计），同形巧合时产出归属错误的事件。
// 两台设备看同一盘棋（现产品主场景）行为正确。修复 = 锚点按 devKeyOf 分组
// （照抄 trendGroups 模式），与"SQLite 沉淀加设备列"同族，届时一并立项。
func (s *Server) evaluateAdoption(newFen string) {
	// 读锚与清锚（clear 也是一次写入）与 sendSuggestionToOwner 的锚点维护段
	// 同持 pendingSugMu：无锁时"读-判-清"会与迟到的建议写入交错——锚点被清
	// 后又复活、或写入被清丢。onAdoption 回调（SQLite 落库）在锁外执行。
	s.pendingSugMu.Lock()
	prev, _ := s.lastFen.Load().(string)
	pend, _ := s.pendingSug.Load().(*pendingAdvice)
	clear := func() { s.pendingSug.Store((*pendingAdvice)(nil)) }
	if pend == nil || len(pend.moves) == 0 {
		s.pendingSugMu.Unlock()
		return
	}
	if pend.fen != prev {
		// 锚点局面 != 上一确认局面：建议下发后没等到"下一局面"就换了归属
		//（罕见竞态）——不比对
		clear()
		s.pendingSugMu.Unlock()
		return
	}
	if prev == newFen {
		// 重发/补发/force 前奏：锚点保留，等真实变化
		s.pendingSugMu.Unlock()
		return
	}
	seg, _, hasSeg := strings.Cut(newFen, " ")
	if hasSeg && seg == startBoardSeg {
		clear()
		s.pendingSugMu.Unlock()
		return
	}
	actual, ok := xq.DiffSingleMove(prev, newFen)
	if !ok {
		clear()
		s.pendingSugMu.Unlock()
		return
	}
	rank, hit := pend.moves[actual]
	kind := "miss"
	if hit {
		if rank == 1 {
			kind = "primary"
		} else {
			kind = "candidate"
		}
	}
	suggested := ""
	for mv, r := range pend.moves {
		if r == 1 {
			suggested = mv
			break
		}
	}
	clear()
	s.pendingSugMu.Unlock()
	if s.onAdoption == nil || suggested == "" {
		return
	}
	s.onAdoption(AdoptionRecord{
		FEN: pend.fen, Suggested: suggested, Actual: actual, MatchKind: kind,
		CandRank: rank, Source: pend.source, Depth: pend.depth,
		ScoreCP: pend.scoreCP, ScoreMate: pend.scoreMate,
	})
}

// sendErrorToOwner 错误帧定向下发（路由同 §4.5）：引擎危急报错等局面级错误
// 只通知归属连接，不广播打扰其他设备。
func (s *Server) sendErrorToOwner(code, message, fen string) {
	if id, ok := s.ownerSession.Load().(string); ok && id != "" {
		s.hub.SendToSession(id, ErrorFrame{frameBase: frame(TypeError), Code: code, Message: message, Fen: fen})
	}
}

// flipTurnFen 返回把走子段 w/b 互换的 FEN 复本，用于校验"翻转轮次后是否合法"。
func flipTurnFen(fen string) string {
	parts := strings.Split(strings.TrimSpace(fen), " ")
	if len(parts) < 2 {
		return fen
	}
	switch parts[1] {
	case "w":
		parts[1] = "b"
	case "b":
		parts[1] = "w"
	}
	return strings.Join(parts, " ")
}

// handlePosition 局面上行：先查桔库（命中=绿箭头直显，source="book:<名>"），
// 未命中再转发引擎（最新覆盖）。引擎与库都不可用时按协议回 not_ready / echo。
// handlePosition 局面上行主入口。改建议组装逻辑前先读本段总览——
//
// 校验（FENHashValidated → PositionLegal，任一失败回 error 并 return）
//
//	↓ new_game 标记 → 生成对局 ID    ↓ 采纳比对（取 prev，须在 lastFen 覆盖前）
//
// lastFen/ownerSession 记账
//
//	↓
//
// 【建议路径】默认常规链（regularSuggestionPath，命中即 return）：
//  0. force=true（手机"更新"）→ 跳过库直达引擎（调参重搜 reSearchLatest 同理直达）
//  1. 桔库命中 → 逐候选下发（Multipv 1..N）→ return
//  2. 沉淀命中 → 历史终值下发（带 cached 标记）→ return
//  3. 云库（可选，默认关）→ 异步补充：不 return 不阻塞，命中帧被引擎结果收束
//  4. 引擎路径 → eng.Request → enginePump 异步回推
//
// 【云库优先】（2026-10-30"默认走云库"，cloud_first 开启时插队）：局面先查云库
// ——命中即终（不查本地不进引擎，与 1/2 同构）；未命中/限流回落常规链，异步
// 未定论由 fallback 接手（语义矩阵见 cloudLookupFirst）。force/调参重搜不受影响。
//
// 【横向契约】三条路径都经 sendSuggestionToOwner 出口——采纳比对锚点
// （pendingAdvice）与"沉淀命中也要建锚"的语义都靠这一点保证，新增下发
// 路径必须走同一出口；recordSuggestion（沉淀落库）只挂桔库主选与引擎终态，
// 沉淀命中不落库（复用原行），新增路径要落库时注意别放大旧数据。
func (s *Server) handlePosition(c *Conn, m *Position) {
	// client_history_len：手机上报 position 帧自带的 history 字段（现行手机
	// 端恒空列表——PC 端局面历史是自攒的，见 history.go；字段保留只为协议
	// 兼容，改名以正视听，避免排查时误当"PC 历史长度"）
	// fen 截断落日志（2026-11-14 红队第二轮日志收敛）：帧上限 64KB，畸形超大
	// fen 原样落日志可刷掉真实日志；正常 FEN <100 字符不受截断影响
	c.log.Info("收到局面", "fen", truncateForLog([]byte(m.Fen)), "red_go", m.RedGo, "client_history_len", len(m.History))
	// 设备键与走势侧信息（2026-10-30 走势按设备分组）：每条 position 顺带刷新
	// 该组昵称（断开后组标签仍显示最后昵称）与我方执色——与局面合法性无关的
	// 纯展示侧信息，先记下，即使本帧因识别瑕疵被拒也不影响
	devKey := devKeyOf(c)
	s.trendNoteDevice(devKey, c.Identity().Name)
	if m.MyRed != nil {
		s.trendSetMySide(devKey, *m.MyRed)
	}

	// 同侧单步重锚（2026-11-10 加强轮）：识别端轮次卡旧值（movedSide 噪声失败
	// 保旧值）会产出"子已走、行棋方没翻"的 FEN——盘面合法时 PC 拦不住，照常
	// 按错轮次分析（8:11 错误指导#1 的直接根源；下方 turn_flip_legal 既有闸
	// 只救"走子方能吃将"的子集）。盘面级单步解释成立时移动子颜色是物理事实：
	// 仅纠正"cur 侧卡旧值"形态（移动子属上一局面行棋方）——回 bad_position
	// {turn_flip_legal} 既有自愈回环，手机翻转重发后 fen 回显/建议过滤/轮次
	// 大字天然一致（PC 静默改写会被手机"迟到建议过滤"按 fen 不匹配整批丢弃，
	// 端到端断链，禁走）；"prev 侧卡旧值"形态（移动子属当前行棋方）当前局面
	// 本来就对，不干预。翻转后非法（盘面另有损坏）维持原状走既有路径。
	// 手动模式手机端跳过自动翻转（onError 分支自守），编辑语义不受扰。
	if !m.NewGame {
		if prev, _ := s.lastFen.Load().(string); prev != "" && prev != m.Fen &&
			fenSide(prev) != "" && fenSide(prev) == fenSide(m.Fen) {
			if _, moverRed, ok := xq.DiffSingleMoveBoard(prev, m.Fen); ok && moverRed == (fenSide(prev) == "w") {
				flipLegal := engine.PositionLegal(flipTurnFen(m.Fen))
				c.log.Info("识别轮次疑似卡旧值（同侧单步），回 bad_position 自愈回环",
					"fen", m.Fen, "turn_flip_legal", flipLegal)
				if flipLegal {
					_ = c.Send(ErrorFrame{frameBase: frame(TypeError), Code: CodeBadPosition,
						Message:       "识别轮次卡旧值（子已走、行棋方未翻）——已验证翻转轮次合法，自动纠正重发",
						Fen:           m.Fen,
						TurnFlipLegal: true})
					return
				}
			}
		}
	}

	// 回合结构防线（2026-11-10 优势崩塌归因）：棋理不变量——上一确认局面到
	// 本局面必须能被"一步（2 格三形态）/两回合合并（≥3 格+子数差上限）/单格
	// 残差（目的地漏检/幽灵清理/手动增删）"解释，否则是识别错位/漏检拼出的
	// 不可能局面（实测两处断崖：兑子幽灵帧"吃自家子"形状、三子无落点蒸发
	// 被照常分析后沉淀照常出招——走势崩塌的直接根源）。回 bad_frame 走手机
	// 自愈重探瀑布（重新定位常可修复确定性误读；配额用尽出人工核对提示）。
	// NEW_GAME/force（显式重分析意图，含"更新"按钮与自愈重发）/manual（手动
	// 编辑语义，编辑增量不受棋理约束）跳过；判据细节见 xq.PlausiblePair。
	// ⚠置于重锚之后：同侧 2 格差若被单步合法解释属轮次卡旧值（重锚回
	// bad_position 自愈），走到这里的多为隐含走子伪非法的幽灵帧。
	if !m.NewGame && !m.Force && !m.Manual {
		if prev, _ := s.lastFen.Load().(string); prev != "" && prev != m.Fen {
			if reason := xq.PlausiblePair(prev, m.Fen); reason != "" {
				anchored, _ := s.gateRejectPrev.Load().(string)
				if anchored == prev && s.gateRejectCount.Load() >= 1 {
					// 连续第 2 次拒同一 prev → 按识别自愈放行（2026-11-12 断供
					// 治理）：第 1 次拒已挡住幻影当下出招（bad_frame 触发过重发
					// 对账+重探），重探/后续帧仍是同形态矛盾说明 prev 大概率是
					// 识别坏盘面或 force 污染残留——继续拒只会死锁（lastFen 停
					// 在坏局面，建议断供实测 4.4~27.1s 全靠手动"更新"）。合法性
					// 仍由下方 FENHashValidated/PositionLegal 兜底；真坏盘面拦
					// 在"当下"的目的已达成，链路存活优先。红队复审口径：拒绝
					// 按规则正确，但后果真实的死锁必须有自动出口。
					c.log.Warn("结构门连续拒绝同一 prev，按识别自愈放行",
						"reason", reason, "prev", prev, "cur", m.Fen)
					s.gateRejectPrev.Store("")
					s.gateRejectCount.Store(0)
				} else {
					if anchored != prev {
						s.gateRejectPrev.Store(prev)
					}
					s.gateRejectCount.Store(1)
					c.log.Warn("局面结构与上一确认局面矛盾（疑似识别错位/漏检），回 bad_frame 自愈重探",
						"reason", reason, "prev", prev, "cur", m.Fen)
					_ = c.Send(ErrorFrame{frameBase: frame(TypeError), Code: CodeBadFrame,
						Message: "局面结构与上一局面矛盾（疑似识别错位/漏检）——正在自动重新识别，请核对棋盘",
						Fen:     m.Fen})
					return
				}
			} else {
				// 门通过：链路自愈完成，清拒绝状态（计数不跨局面延续）
				s.gateRejectPrev.Store("")
				s.gateRejectCount.Store(0)
			}
		}
	}

	// 0. FEN 合法性防呆：将帅照面等非法局面会让皮卡鱼异常退出，必须拦截。
	// 算出的查询键 hash 与盘面/行棋方留给下方桔库查询复用——同一 FEN 二次
	// ParseFEN+Zobrist（90 格逐字符解析）是纯浪费，每条 position 消息省一半
	// 解析开销；盘面/行棋方供 vmove 位义仲裁（2026-11-03 狂刀类反向库修复）
	hash, bd, redGo, hashErr := book.FENHashValidated(m.Fen)
	if hashErr != nil {
		_ = c.Send(ErrorFrame{frameBase: frame(TypeError), Code: CodeBadFrame,
			Message: "局面非法（" + hashErr.Error() + "）", Fen: m.Fen})
		c.log.Warn("拒绝非法局面", "fen", m.Fen, "err", hashErr)
		return
	}
	// 0.1 引擎安全校验：走子方能一步吃到对方将/帅（车/炮直瞄等）同样会让皮卡鱼
	// 退出进程（"King can be captured"→EOF 崩溃循环），照面校验拦不住这类盘面——
	// 识别列偏/漏子时真实合法的棋面会被拼成这种局面（2026-10-01 残局实测）
	if !engine.PositionLegal(m.Fen) {
		// 拒收前先验证"翻转走子方后是否合法"：真实对局中非走子方的王被将军
		// 是不可能局面（被将军方必须行棋），翻转即唯一自洽解释。走子方卡错
		// 的主因是吃子步轮次未翻转（2026-10-02 实测 10/10 拒收皆此因）——
		// 翻转后合法时错误帧带标记，手机端自动纠正重发，不再停下来等用户
		// 点『更新』；标记为 false 时盘面本身有错（遮挡漏识别等），只能提示
		flipLegal := engine.PositionLegal(flipTurnFen(m.Fen))
		_ = c.Send(ErrorFrame{frameBase: frame(TypeError), Code: CodeBadPosition,
			Message:       "局面识别异常（走子方能吃将或将帅仕相位置不合法）",
			Fen:           m.Fen,
			TurnFlipLegal: flipLegal})
		c.log.Warn("拒绝引擎不安全局面", "fen", m.Fen, "turn_flip_legal", flipLegal)
		return
	}
	// 通过校验即记为当前局面归属：此后的库命中/引擎建议都定向回本连接；走势
	// 焦点设备同步切到本连接的设备键（ownerDev 是走势默认视图，见 trend.go）。
	// ⚠顺序不可提前：被拒局面若先占 ownerSession/lastFen，会冒充"最新局面"
	// 承接引擎结果，且 set_engine 重放（reSearchLatest）会把坏局面反复喂回引擎。
	s.ownerSession.Store(c.ID)
	s.ownerDev.Store(devKey)

	// 采纳比对（2026-10-17）：必须在下方 lastFen.Store 覆盖前调——prev 取自
	// lastFen，覆盖后就拿不到上一局面了。
	s.evaluateAdoption(m.Fen)

	// 新局边界（复盘闭环 2026-10-17）：手机 NEW_GAME 确认后的首帧带 new_game
	// 标记——生成新对局 ID，此后该局的建议沉淀全部按局归组。兜底：手机旧版本
	// 不发标记，PC 端不自行猜测开局（识别错位容忍语义只在手机端有），旧端记录
	// 落 game_id 空串、不参与按局折叠（宁缺勿错）。
	if m.NewGame {
		id := newGameID()
		// currentGameID 是全局单值（沉淀落库归属）：多设备并行下 B 的建议可能
		// 记到 A 的新局——已知瑕疵，等"SQLite 加设备列"立项一并处理（trend.go
		// 文件头有记）；走势侧不受影响（trendReset 按设备组内换段）。
		s.currentGameID.Store(id)
		s.trendReset(devKey, id) // 该设备走势换段（A：新开局清零；C：撤离重开由识别器 NEW_GAME 兜住）
		c.log.Info("新局开始（手机 NEW_GAME），对局 ID 已生成", "game_id", id, "fen", m.Fen)
	}

	// lastFen 记最新局面，与建议来源无关：set_engine 调参后的 reSearchLatest 以此
	// 为准——桔库/沉淀命中若不记，调参后要么把旧局面重搜、要么（lastFen 为空，
	// 如 PC 重启后首个局面恰好桔库命中）根本不触发重搜，MultiPV 多线候选拿不到
	// （2026-10-01 实测"电脑变招手机没有跟上"根因之一）
	s.lastFen.Store(m.Fen)
	// force 盘面绕过结构门（显式重分析意图），可信度未知：预置拒绝计数=1，
	// 下一帧与本盘面矛盾即达"连续 2 次"阈值按识别自愈放行——用户点"更新"
	// 接受的残缺盘面（实测拦截 #7/#12）不再把后续修正帧死锁在结构门外
	if m.Force {
		s.gateRejectPrev.Store(m.Fen)
		s.gateRejectCount.Store(1)
	}
	// per-Conn 副本：控制台"最后 position"列按连接显示各自最近局面
	c.lastFen.Store(m.Fen)
	// 局面代次（2026-11-01 显示错位根修）：每接受一条局面递增，云库/残局异步
	// 回调下发前校验（stillCurrent）——同 FEN force 重发（换库后重分析等）时，
	// 旧局面在途的云帧被代次拦下，不再追认覆盖已定着的库建议
	gen := s.posGen.Add(1)
	s.lastPositionAt.Store(time.Now().UnixMilli())

	// 局面历史攒集（2026-11-07 防三次重复判和）：合法性已过闸才入历史——
	// 坏局面绝不污染序列。同色连走告警/着法 diff/重置策略见 history.go
	s.notePositionFen(c.ID, m.Fen, m.NewGame)

	// 单次多线解析（2026-11-09 双击小棋盘"单次三变解析"，协议 §4.4
	// multi_pv_once）：本条局面的引擎搜索以指定线数执行且仅此一轮——不改
	// 引擎配置档位（"路数"按钮的持久值不受扰）。越界值按缺省处理（Warn
	// 留痕）：position 是主链路帧，辅助字段非法不拦局面本身
	pvOnce := 0
	if m.MultiPVOnce != nil {
		if v := *m.MultiPVOnce; v >= 1 && v <= 5 {
			pvOnce = v
		} else {
			c.log.Warn("multi_pv_once 越界，按缺省处理", "value", v)
		}
	}

	// force=true（手机端"更新"）跳过桔库/沉淀直接进引擎：用户显式要求重新分析，
	// 尤其刚调过 MultiPV 等算力参数时，库命中短路会让新参数拿不到多线候选。
	// pvOnce>0（单次多线解析）同路直达引擎——库命中（多为主选单条）会让
	// "三变"拿不到引擎多线候选，语义即失效
	if m.Force || pvOnce > 0 {
		if s.eng != nil && s.EngineState() == engine.StateReady {
			c.log.Info("强制重新分析（跳过桔库/沉淀，直接进引擎）", "fen", m.Fen,
				"force", m.Force, "multi_pv_once", pvOnce)
			s.requestEngineHistory(m.Fen, c.ID, pvOnce)
			return
		}
		// 引擎不可用则继续走常规路径兜底（库命中总比没有强）。单次多线意图
		// 在此丢弃且无兜底形态，Warn 留痕防"点了没反应"的排障盲区
		if pvOnce > 0 {
			c.log.Warn("multi_pv_once 请求时引擎不可用，回落常规路径（线数意图丢弃）",
				"fen", m.Fen, "engine_state", s.EngineState())
		}
	}

	// "不走任何库"（skip_books，2026-10-30 用户定稿）：桔库/沉淀/云库（含
	// cloud_first 优先路径）全部跳过，每步引擎直搜——纯引擎出招的持续策略。
	// 与 force 的区别：force 是单步显式重搜（引擎不可用回落库兜底），本开关是
	// 持续策略（引擎不可用如实回 not_ready，不悄悄走库——用户明说"不走任何
	// 库"）。只停"查"不停"记"：引擎建议照常沉淀入库，建议流与历史复用数据
	// 不断供。优先级高于 cloud_first（故在 cloudLookupFirst 之前拦截）。
	if s.SkipBooksEnabled() {
		c.log.Info("不走任何库：跳过桔库/沉淀/云库，直接进引擎", "fen", m.Fen)
		s.enginePath(c, m)
		return
	}

	// "默认走云库"（2026-10-30 用户定稿）：cloud_first 开启时先查云库——命中
	// 即终（不查本地不进引擎，省算力正是收益；需要引擎深度分析用 force 或关掉
	// 开关），未命中/限流回落常规链，异步未定论由 fallback 接手。语义矩阵见
	// cloudLookupFirst；force 已在上方 return（显式重新分析恒达引擎）。
	if s.cloudLookupFirst(m.Fen, gen, func() { s.regularSuggestionPath(c, m, hash, bd, redGo, gen) }) {
		return
	}
	s.regularSuggestionPath(c, m, hash, bd, redGo, gen)
}

// deliverableMove 建议下发着法合法性闸（2026-11-14 红队第二轮）：桔库/沉淀
// 复播/云库/残局四路非引擎建议统一校验着法对本局面伪合法——坏库行（狂刀类
// 拼库脏数据）过去只有归属判据（movePlausible：起点是行棋方子、终点不是行
// 棋方子），云库此前零校验，"走到不到的格子"会以绿箭头直显并入沉淀复播。
// 引擎建议的同类守卫在 engine_pump.go（同款 FenAfter）。µs 级开销，正解/正常
// 建议恒合法，零误伤。
func deliverableMove(fen, ucci string) bool {
	_, ok := xq.FenAfter(fen, ucci)
	return ok
}

// regularSuggestionPath 常规建议链（桔库 → 沉淀 → 云库/残局补充 → 引擎），
// handlePosition 的默认路径；云库优先模式的回落路径也走这里（异步回调，局面
// 仍最新时）。gen 为局面代次（stillCurrent 用），见 handlePosition；bd/redGo
// 为已校验局面（桔库 vmove 位义仲裁用，见 book.Book.Query）。
func (s *Server) regularSuggestionPath(c *Conn, m *Position, hash int64, bd book.Board, redGo bool, gen uint64) {
	// 1. 桔库优先：命中即下发确定性建议（协议 §4.5 允许 score/pv/depth 缺省——
	//    库命中=绿箭头直显，无需分数；vscore 的视角语义未考证，不猜）。
	//    hash 已在入口算好（FENHashValidated，此处直接复用）
	//    桔库候选唤醒（2026-10-17）：库层查询返回原键行 ≤5 条 + 镜像键同构
	//    候选补充（各自 vscore 降序，去重后 ≤10 条，原键恒在前），逐条按
	//    multipv 槽位下发——手机端聚合器按"每个线序号一个
	//    槽位"聚合的既有机制直接吃下，悬浮窗"变招"按钮在桔库多候选局面即可
	//    循环切换，零协议/零端上聚合改动。实测真实桔库仅 0.81% 局面有多候选，
	//    多发帧的开销可忽略；单候选局面仍只发 1 帧（Multipv=1，与旧行为等价）。
	//    候选帧不携带分数（vscore 视角未考证，不猜）；沉淀库只记主选（不放大
	//    旧候选数据——沉淀复用走 LookupByFEN 单条路径）。
	if store, _ := s.book.Load().(*book.Store); store != nil {
		if res, bookName, err := store.Query(hash, bd, redGo); err == nil && res.Hit {
			skipped := 0
			for i, mv := range res.Moves {
				// 着法合法性闸（2026-11-14 红队第二轮）：库行脏数据对本局面
				// 走不到的直接丢弃，不计入下方三次重复规避的 skipped——非法
				// 行没有"退回主选"的产品语义（宁无建议不发非法着法）
				if !deliverableMove(m.Fen, mv.Move) {
					c.log.Warn("桔库候选着法对本局面非法，丢弃", "book", bookName, "move", mv.Move, "fen", m.Fen)
					continue
				}
				// 三次重复规避（2026-11-07 防判和）：该候选走着即形成第三次
				// 重复局面 → 跳过发帧（多候选场景自然换下一名=强制变招）。
				// 全部候选被滤时退回主选照发——过滤是尽力而为，宁给"可能判和
				// 的建议"不给"没有建议"（用户可自行变招）
				if s.moveCausesTripleRep(c.ID, m.Fen, mv.Move) {
					skipped++
					continue
				}
				sug := Suggestion{
					frameBase: frame(TypeSuggestion),
					Source:    "book:" + bookName,
					Fen:       m.Fen,
					BestMove:  mv.Move,
					Multipv:   i + 1,
				}
				s.sendSuggestionToOwner(sug)
			}
			if skipped > 0 && skipped == len(res.Moves) {
				// 全滤：退回主选帧（Multipv=1），日志留痕；主选若非法（脏库行）
				// 同样不发
				if !deliverableMove(m.Fen, res.Moves[0].Move) {
					c.log.Warn("桔库主选着法对本局面非法，不退回", "book", bookName, "best", res.BestMove, "fen", m.Fen)
				} else {
					s.sendSuggestionToOwner(Suggestion{
						frameBase: frame(TypeSuggestion),
						Source:    "book:" + bookName,
						Fen:       m.Fen,
						BestMove:  res.Moves[0].Move,
						Multipv:   1,
					})
				}
			}
			if skipped > 0 {
				c.log.Info("桔库候选三次重复规避", "skipped", skipped, "candidates", len(res.Moves), "fen", m.Fen)
			}
			if deliverableMove(m.Fen, res.BestMove) {
				s.recordSuggestion(SuggestionRecord{
					Source: "book:" + bookName, FEN: m.Fen, BestMove: res.BestMove,
				})
			} else {
				c.log.Warn("桔库主选着法对本局面非法，不沉淀", "book", bookName, "best", res.BestMove, "fen", m.Fen)
			}
			c.log.Info("开局库命中", "book", bookName, "best", res.BestMove, "candidates", len(res.Moves))
			return
		}
	}

	// 2. 沉淀命中：桔库未查到时复用历史建议终值（同局面秒回，省一次数秒级重搜）。
	//    Score/PV/Depth 是当时的真实终值（分数已是红方视角，直接下发）；
	//    source 沿用原记录（engine 或 book:<名>，换库后命中桔库记录也能如实显示），
	//    日志如实标注 cache_hit。WDL 不沉淀（终值帧才有，这里缺省，旧端按可选字段忽略）。
	if s.sedimentLookup != nil {
		// BestMove 守卫同时排除 "null"：历史版本曾把引擎无着可走的终值
		// （best=null, depth=0）沉淀入库，读侧防御保证存量垃圾行不被复播
		if rec, ok := s.sedimentLookup(m.Fen); ok && rec.BestMove != "" && rec.BestMove != "null" {
			// 着法合法性闸（2026-11-14 红队第二轮）：守卫上线前的存量行可能含
			// "旧结果贴错 FEN"的垃圾行，复播前校验伪合法——非法行转引擎现算
			if !deliverableMove(m.Fen, rec.BestMove) {
				c.log.Warn("沉淀行着法对本局面非法（存量垃圾行），放弃复播转引擎现算",
					"best", rec.BestMove, "fen", m.Fen)
			} else if s.moveCausesTripleRep(c.ID, m.Fen, rec.BestMove) {
				c.log.Info("沉淀命中但着法将形成三次重复，放弃复播转引擎现算",
					"best", rec.BestMove, "fen", m.Fen)
			} else {
				sug := Suggestion{
					frameBase: frame(TypeSuggestion),
					Source:    rec.Source,
					Fen:       m.Fen,
					BestMove:  rec.BestMove,
					PV:        rec.PV,
					Depth:     rec.Depth,
					// 缓存标记：source 可能沿用原记录的 "book:<名>"（手机端 fromBook
					// 分支渲染），因此缓存标记独立于来源判断，避免 book 来源的
					// 沉淀命中帧漏标。
					Cached: true,
				}
				if rec.ScoreCP != nil {
					sug.Score = &Score{CP: rec.ScoreCP}
				} else if rec.ScoreMate != nil {
					sug.Score = &Score{Mate: rec.ScoreMate}
				}
				s.sendSuggestionToOwner(sug)
				c.log.Info("沉淀命中（复用历史引擎终值，未进引擎）",
					"depth", rec.Depth, "best", rec.BestMove, "cache_hit", true)
				return
			}
		}
	}

	// 3. 云库（可选，默认关）：桔库/沉淀双未命中后的外部补充——异步不阻塞
	//    引擎搜索，命中帧随后被引擎建议以最新覆盖语义收束（cloud_first 开启时
	//    本步多为缓存未命中的 no-op：云库已在 regularSuggestionPath 之前查过）
	s.cloudLookupAsync(m.Fen, gen)

	// 3.5 残局库（可选，默认关，2026-11-01）：独立于云库开关的穷举绝对正解
	//    补充——独立请求/独立限流/独立缓存，命中以 endgame:<域名> 前缀下发；
	//    展示语义（不覆盖定着、双箭头第二意见）在手机端
	s.endgameLookupAsync(m.Fen, gen)

	s.enginePath(c, m)
}

// SetSkipBooks / SkipBooksEnabled "不走任何库"开关（PC 高级面板→开局库勾选框；
// config engine.skip_books 作启动种子，main 闭包热切+落盘）。语义与拦截位置见
// handlePosition 内注释。
func (s *Server) SetSkipBooks(on bool) { s.skipBooks.Store(on) }

// SkipBooksEnabled 当前是否跳过全部库直搜引擎。
func (s *Server) SkipBooksEnabled() bool { return s.skipBooks.Load() }

// RecentPositionWithin 报告最近 d 内是否收到过局面（memwatch 动态 GC 档位
// 的活跃信号：对局期不得滞留低 GC 档位，见 memwatch.ActivityProbe）。
func (s *Server) RecentPositionWithin(d time.Duration) bool {
	ats := s.lastPositionAt.Load()
	if ats == 0 {
		return false
	}
	return time.Since(time.UnixMilli(ats)) < d
}

// enginePath 建议链终点：把局面交给引擎搜索。各库未命中（常规链）/被跳过
// （skip_books）时的最终去路。引擎未启用回 status echo（M0 基准语义）、未就绪
// 回 not_ready——两个分支自 regularSuggestionPath 尾部抽取（内容逐字一致）。
func (s *Server) enginePath(c *Conn, m *Position) {
	if s.eng == nil {
		// M0 echo 基准（引擎未启用）：回 status 确认链路
		_ = c.Send(Status{frameBase: frame(TypeStatus), Engine: EngineStatus{State: s.EngineState()}})
		return
	}
	if s.EngineState() != engine.StateReady {
		// Warn 留痕（2026-11-15 观测补齐：not_ready 分支此前 PC 零日志，只有
		// 手机端单向报错——排障时 PC 侧无痕）
		c.log.Warn("引擎未就绪，回 not_ready", "state", s.EngineState(), "fen", m.Fen)
		_ = c.Send(ErrorFrame{frameBase: frame(TypeError), Code: CodeNotReady,
			Message: "引擎尚未就绪，就绪后将自动广播 status，届时可重发局面"})
		return
	}
	// lastFen 已在 handlePosition 入口记录（桔库/沉淀命中路径同样记录）；
	// 历史随行按能力下发（2026-11-08 语义修正版，见 history.go 注释）
	s.requestEngineHistory(m.Fen, c.ID, 0)
}
