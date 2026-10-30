package server

import (
	"cabinet/internal/book"
	"cabinet/internal/engine"
	"cabinet/internal/xq"
	"strings"
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
	c.log.Info("收到局面", "fen", m.Fen, "red_go", m.RedGo, "history_len", len(m.History))
	// 设备键与走势侧信息（2026-10-30 走势按设备分组）：每条 position 顺带刷新
	// 该组昵称（断开后组标签仍显示最后昵称）与我方执色——与局面合法性无关的
	// 纯展示侧信息，先记下，即使本帧因识别瑕疵被拒也不影响
	devKey := devKeyOf(c)
	s.trendNoteDevice(devKey, c.Identity().Name)
	if m.MyRed != nil {
		s.trendSetMySide(devKey, *m.MyRed)
	}

	// 0. FEN 合法性防呆：将帅照面等非法局面会让皮卡鱼异常退出，必须拦截。
	// 算出的查询键 hash 留给下方桔库查询复用——同一 FEN 二次 ParseFEN+Zobrist
	//（90 格逐字符解析）是纯浪费，每条 position 消息省一半解析开销
	hash, _, hashErr := book.FENHashValidated(m.Fen)
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
	// per-Conn 副本：控制台"最后 position"列按连接显示各自最近局面
	c.lastFen.Store(m.Fen)

	// force=true（手机端"更新"）跳过桔库/沉淀直接进引擎：用户显式要求重新分析，
	// 尤其刚调过 MultiPV 等算力参数时，库命中短路会让新参数拿不到多线候选
	if m.Force {
		c.log.Info("强制重新分析（跳过桔库/沉淀，直接进引擎）", "fen", m.Fen)
		if s.eng != nil && s.EngineState() == engine.StateReady {
			s.eng.Request(m.Fen)
			return
		}
		// 引擎不可用则继续走常规路径兜底（库命中总比没有强）
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
	if s.cloudLookupFirst(m.Fen, func() { s.regularSuggestionPath(c, m, hash) }) {
		return
	}
	s.regularSuggestionPath(c, m, hash)
}

// regularSuggestionPath 常规建议链（桔库 → 沉淀 → 云库补充 → 引擎），handlePosition
// 的默认路径；云库优先模式的回落路径也走这里（异步回调，局面仍最新时）。内容与
// 抽取前逐字一致（2026-10-30 仅搬移），横向契约见文件头。
func (s *Server) regularSuggestionPath(c *Conn, m *Position, hash int64) {
	// 1. 桔库优先：命中即下发确定性建议（协议 §4.5 允许 score/pv/depth 缺省——
	//    库命中=绿箭头直显，无需分数；vscore 的视角语义未考证，不猜）。
	//    hash 已在入口算好（FENHashValidated 与 FENHash 同一输入同一键）
	//    桔库候选唤醒（2026-10-17）：库层查询本就返回 ≤5 条候选（vscore 降序，
	//    SQL 已排序），逐条按 multipv 槽位下发——手机端聚合器按"每个线序号一个
	//    槽位"聚合的既有机制直接吃下，悬浮窗"变招"按钮在桔库多候选局面即可
	//    循环切换，零协议/零端上聚合改动。实测真实桔库仅 0.81% 局面有多候选，
	//    多发帧的开销可忽略；单候选局面仍只发 1 帧（Multipv=1，与旧行为等价）。
	//    候选帧不携带分数（vscore 视角未考证，不猜）；沉淀库只记主选（不放大
	//    旧候选数据——沉淀复用走 LookupByFEN 单条路径）。
	if store, _ := s.book.Load().(*book.Store); store != nil {
		if res, bookName, err := store.Query(hash); err == nil && res.Hit {
			for i, mv := range res.Moves {
				sug := Suggestion{
					frameBase: frame(TypeSuggestion),
					Source:    "book:" + bookName,
					Fen:       m.Fen,
					BestMove:  mv.Move,
					Multipv:   i + 1,
				}
				s.sendSuggestionToOwner(sug)
			}
			s.recordSuggestion(SuggestionRecord{
				Source: "book:" + bookName, FEN: m.Fen, BestMove: res.BestMove,
			})
			c.log.Info("桔库命中", "book", bookName, "best", res.BestMove, "candidates", len(res.Moves))
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

	// 3. 云库（可选，默认关）：桔库/沉淀双未命中后的外部补充——异步不阻塞
	//    引擎搜索，命中帧随后被引擎建议以最新覆盖语义收束（cloud_first 开启时
	//    本步多为缓存未命中的 no-op：云库已在 regularSuggestionPath 之前查过）
	s.cloudLookupAsync(m.Fen)

	s.enginePath(c, m)
}

// SetSkipBooks / SkipBooksEnabled "不走任何库"开关（PC 高级面板→开局库勾选框；
// config engine.skip_books 作启动种子，main 闭包热切+落盘）。语义与拦截位置见
// handlePosition 内注释。
func (s *Server) SetSkipBooks(on bool) { s.skipBooks.Store(on) }

// SkipBooksEnabled 当前是否跳过全部库直搜引擎。
func (s *Server) SkipBooksEnabled() bool { return s.skipBooks.Load() }

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
		_ = c.Send(ErrorFrame{frameBase: frame(TypeError), Code: CodeNotReady,
			Message: "引擎尚未就绪，就绪后将自动广播 status，届时可重发局面"})
		return
	}
	// lastFen 已在 handlePosition 入口记录（桔库/沉淀命中路径同样记录）
	s.eng.Request(m.Fen)
}
