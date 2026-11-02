// cloudbook.go 云端开局库（可选，2026-10-28 定稿）：桔库与沉淀双未命中时的
// 外部开局库补充。端点默认预设 chessdb.cn（免费公开基建，免 key）；自带 API
// 的使用者改 endpoint/api_key。隐私口径：开启 = 同意把对局局面 FEN 发往端点
// （configs/cabinet.example.toml 同款措辞），默认关由用户自行开启。
//
// 行为纪律：异步（不阻塞引擎搜索，引擎随后以最新覆盖语义收束）、限流
// （≥1.5s 一次，开局/残局各自一份时间戳）、FEN 缓存（含未命中，防重复打
// 外网；残局键加 "eg|" 前缀隔离）、失败静默（与检查更新同款契约）、代次
// 校验（posGen：同 FEN 重发后旧异步帧不再追认下发）。开局命中以
// "cloud:<端点域名>" 前缀 source 下发——复用 suggestion 帧，旧手机零改动；
// 不带分数/PV/深度：chessdb 的 score 视角语义未考证，与桔库"不猜"口径一致
// （库命中=绿箭头直显，无需分数）。
//
// 残局库（2026-11-01）：action=query&endgame=1 独立请求只取云库残局库
// （穷举绝对正解；chessdb 的 egtb: 前缀仅在 query 系返回，queryall 不混合），
// 命中以 "endgame:<域名>" 前缀下发（§7 值域扩展），独立开关/限流/缓存，
// 与开局查询完全解耦。展示语义（已有定着不覆盖、双箭头第二意见）在手机端。
package server

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"cabinet/internal/config"
)

type cloudEntry struct {
	hit  bool
	move string
}

const (
	cloudThrottleMs   = 1500 // 查询最小间隔（对公开免费端点的礼貌限速）
	cloudCacheCap     = 512  // FEN 缓存条数上限（满了整表清空，简单且够用）
	cloudBodyLimit    = 64 << 10
	cloudQueryTimeout = 3 * time.Second
)

// cloudHTTPClient 云库查询共用 HTTP 客户端（包级单例）：http.Client 内部维护
// 连接池，复用空闲连接免去每次查询重建 TCP+TLS 握手；超时恒按 cloudQueryTimeout。
var cloudHTTPClient = &http.Client{Timeout: cloudQueryTimeout}

// SetCloudBookEnable / CloudBookEnabled 运行时开关（PC 面板"云库查询"勾选框）。
// cfg.CloudBook.Enable 只在 New 时作为种子进入，此后运行时以本原子位为准，
// 落盘持久化由 main 的 Set 回调负责（与算力落盘同锁同路）。
func (s *Server) SetCloudBookEnable(on bool) { s.cloudEnable.Store(on) }

func (s *Server) CloudBookEnabled() bool { return s.cloudEnable.Load() }

// SetCloudFirst / CloudFirstEnabled "默认走云库"开关（PC 面板开局库面板勾选框；
// 2026-10-30 用户定稿）。语义矩阵见 cloudLookupFirst。
func (s *Server) SetCloudFirst(on bool) { s.cloudFirst.Store(on) }

func (s *Server) CloudFirstEnabled() bool { return s.cloudFirst.Load() }

// SetEndgameEnable / EndgameEnabled 残局库查询开关（2026-11-01，PC 面板勾选框）。
// 独立于云库开关：开启后本地库/沉淀未命中的局面另发 action=query&endgame=1
// 只查云库残局库（穷举绝对正解，与开局查询是两个独立请求）。cfg.CloudBook.Endgame
// 只作启动种子，运行时以本原子位为准（与 cloudEnable 同款）。
func (s *Server) SetEndgameEnable(on bool) { s.endgameEnable.Store(on) }

func (s *Server) EndgameEnabled() bool { return s.endgameEnable.Load() }

// cloudLookupFirst 云库优先路径（"默认走云库"核心，2026-10-30）：cloud_first
// 开启时每条局面先查云库——
//   - 缓存命中 → 同步下发，返回 true（局面建议已由云库定论，调用方 return；
//     本地桔库/沉淀与引擎都不走，与桔库命中"命中即 return"同构——省算力正是
//     "默认走云库"的收益，需要引擎深度分析的用户用 force 或关掉本开关）
//   - 缓存未命中（已知云库没有）→ 返回 false，调用方立即走常规链（零延迟）
//   - 发起异步查询 → 返回 true；结果回来：命中/未命中/失败都先校验"局面
//     仍最新"（延迟窗内可能已被新 position 取代——那样新局面自己会走自己
//     的链，这里放弃即可，沿用"最新覆盖"语义）再执行 deliver / fallback
//     回落常规链
//   - 限流窗口内 → 返回 false（立即常规链自愈，快速连续走子时不空等）
//
// gen 为 handlePosition 的局面代次（posGen）：异步回调下发前校验未变——
// 同 FEN force 重发（如换库后重分析）时 lastFen 校验拦不住的旧异步帧被
// 代次拦下（库命中已定着，在途云帧不得再覆盖显示）。
//
// 返回 true = 建议来源已由云库接手（调用方 return）；false = 云库无法定论，
// 调用方立即走常规链。fallback 在云库 goroutine 里执行——常规链全程只用
// ownerSession/lastFen/引擎 Request 等并发安全设施，不直接依赖 Conn 存活。
func (s *Server) cloudLookupFirst(fen string, gen uint64, fallback func()) bool {
	if !s.cloudEnable.Load() || !s.cloudFirst.Load() {
		return false // 总开关或优先开关未开：完全现状行为
	}
	cb := s.cfg.CloudBook
	if cb.Endpoint == "" {
		return false
	}
	if e, ok := s.cloudCached(fen); ok {
		if e.hit {
			// 缓存快路径与异步回调同纪律：下发前过 stillCurrent——多设备并行
			// 下慢一步的 handlePosition 不得把 A 局面的建议发给已被 B 接管的
			// owner。过期命中静默丢弃且维持返回 true（过期局面不再起引擎
			// 白搜；新局面自会走自己的链）
			if s.stillCurrent(fen, gen) {
				s.cloudDeliver(cb, fen, e)
			}
			return true
		}
		return false
	}
	now := time.Now().UnixMilli()
	if !s.tryThrottle(&s.lastCloudMs, now) {
		return false
	}
	go func() {
		e, err := s.cloudQuery(cb, fen)
		if err != nil {
			// 查询失败（网络抖动/限流/5xx）≠ 端点答"没有"：失败不入缓存——
			// 入了就是把该 FEN 永久钉死为未命中（缓存 512 条才整表清空），
			// 网络恢复后也不再查，功能静默退化。防打爆由限流承担，失败
			// 不缓存没有副作用。
			s.log.Info("云库查询失败（静默，回落常规链）", "err", err)
			if s.stillCurrent(fen, gen) {
				fallback()
			}
			return
		}
		s.cloudStore(fen, e)
		if e.hit {
			// 命中前复查"局面仍最新"：与下方未命中分支对齐——异步窗内换局
			// 则放弃下发（新局面自会走自己的链，过期建议只会误导）。代次
			// 校验（posGen）在 lastFen 之上再拦一道同 FEN 重发的旧帧
			if s.stillCurrent(fen, gen) {
				s.cloudDeliver(cb, fen, e)
			}
			return
		}
		if s.stillCurrent(fen, gen) {
			fallback()
		}
	}()
	return true
}

// cloudLookupAsync 桔库/沉淀双未命中后的云库补充（fire-and-forget，失败静默）。
// gen 语义同 cloudLookupFirst。
func (s *Server) cloudLookupAsync(fen string, gen uint64) {
	if !s.cloudEnable.Load() {
		return // 运行时开关（PC 面板切换）：查询热路径只读原子位，不碰 cfg
	}
	cb := s.cfg.CloudBook
	if cb.Endpoint == "" {
		return
	}
	if e, ok := s.cloudCached(fen); ok {
		// 缓存命中（含未命中）直接消费，不限流；下发同样过 stillCurrent
		// （缓存只是跳过网络，不跳过"最新覆盖"复查——与异步回调对齐）
		if s.stillCurrent(fen, gen) {
			s.cloudDeliver(cb, fen, e)
		}
		return
	}
	now := time.Now().UnixMilli()
	if !s.tryThrottle(&s.lastCloudMs, now) {
		return // 限流窗口内放弃本次（下一局面再试）
	}
	go func() {
		e, err := s.cloudQuery(cb, fen)
		if err != nil {
			// 失败不入缓存（理由见 cloudLookupFirst：失败≠端点答"没有"）
			s.log.Info("云库查询失败（静默）", "err", err)
			return
		}
		s.cloudStore(fen, e)
		// 异步窗（查询可达数秒）内可能已被新 position 取代：过期命中不再
		// 下发，与 cloudLookupFirst 的"最新覆盖"复查对齐（miss 本就被
		// cloudDeliver 拦下，一起包住无碍）
		if s.stillCurrent(fen, gen) {
			s.cloudDeliver(cb, fen, e)
		}
	}()
}

// endgameLookupAsync 残局库补充（2026-11-01）：本地库/沉淀双未命中后的独立
// 异步查询——action=query&endgame=1 只取云库残局库（穷举绝对正解）。与开局
// 云库查询（cloudLookupAsync）完全独立：独立开关、独立限流、独立缓存键；
// cloud_first 优先路径不查残局（残局是补充语义，不参与"命中即终"的抢占）。
// 命中帧 source="endgame:<域名>"；手机端已有定着时不覆盖、仅登记为第二意见
// （双箭头/一致文案），无定着时到达即定着——展示语义在手机端，PC 照常下发。
func (s *Server) endgameLookupAsync(fen string, gen uint64) {
	if !s.endgameEnable.Load() {
		return // 运行时开关（PC 面板切换）：查询热路径只读原子位，不碰 cfg
	}
	cb := s.cfg.CloudBook
	if cb.Endpoint == "" {
		return
	}
	if e, ok := s.cloudCachedEndgame(fen); ok {
		// 缓存命中直接消费，不限流；下发过 stillCurrent（与云库缓存路径同口径）
		if s.stillCurrent(fen, gen) {
			s.endgameDeliver(cb, fen, e)
		}
		return
	}
	now := time.Now().UnixMilli()
	if !s.tryThrottle(&s.lastEndgameMs, now) {
		return // 限流窗口内放弃本次（下一局面再试）
	}
	go func() {
		e, err := s.endgameQuery(cb, fen)
		if err != nil {
			// 失败不入缓存（理由见 cloudLookupFirst：失败≠端点答"没有"）
			s.log.Info("残局库查询失败（静默）", "err", err)
			return
		}
		s.cloudStoreEndgame(fen, e)
		if s.stillCurrent(fen, gen) {
			s.endgameDeliver(cb, fen, e)
		}
	}()
}

// stillCurrent 局面仍最新判定（云库/残局异步回调的统一下发闸）：代次未变
// 且 lastFen 未变。lastFen 单独拦"换局面"；代次在 lastFex 之上再拦一道
// "同 FEN 重发"（force 重分析）——旧局面已在途的异步帧不得追认到新一轮。
func (s *Server) stillCurrent(fen string, gen uint64) bool {
	if s.posGen.Load() != gen {
		return false
	}
	want, _ := s.lastFen.Load().(string)
	return want == fen
}

// tryThrottle 限流窗口占位：CAS 循环实现原子的 check-then-set。直接
// "Load 判间隔 + Store"会被两个查询入口并发交错——两路都看到窗口外并同时
// Store，限流被绕过；CAS 失败说明他人刚占位，重读重判即可。
// 返回 true = 本次占位成功（准许查询）。开局查询与残局查询各持一份时间戳
// （lastCloudMs / lastEndgameMs），互不挤占。
func (s *Server) tryThrottle(last *atomic.Int64, now int64) bool {
	for {
		v := last.Load()
		if now-v < cloudThrottleMs {
			return false
		}
		if last.CompareAndSwap(v, now) {
			return true
		}
	}
}

// cloudDeliver 命中下发：走 sendSuggestionToOwner（与引擎建议同一条最新覆盖路由）。
// 调用方须先校验 lastFen == fen（异步查询窗可达数秒，局面可能已被新 position
// 取代）——PC 侧不主动发过期局面，不依赖手机端迟到过滤兜底。
func (s *Server) cloudDeliver(cb config.CloudBookConfig, fen string, e cloudEntry) {
	if !e.hit || e.move == "" || e.move == "null" {
		return
	}
	host := cb.Endpoint
	if u, err := url.Parse(cb.Endpoint); err == nil && u.Host != "" {
		host = u.Host
	}
	s.sendSuggestionToOwner(Suggestion{
		frameBase: frame(TypeSuggestion),
		Source:    "cloud:" + host,
		Fen:       fen,
		BestMove:  e.move,
		Multipv:   1,
	})
	s.log.Info("云库命中", "endpoint", host, "fen", fen, "best", e.move)
}

// endgameDeliver 残局命中下发：source="endgame:<域名>"（协议 §7 值域扩展，
// 2026-11-01 登记）。帧形态与 cloud 帧同口径（无 score/pv/depth、multipv 1、
// fen 恒带）；旧手机 APP 不识别该前缀会落"暂定"分支白等收束帧——手机端
// fromEndgame 判定必须随本功能同步发版。
func (s *Server) endgameDeliver(cb config.CloudBookConfig, fen string, e cloudEntry) {
	if !e.hit || e.move == "" || e.move == "null" {
		return
	}
	host := cb.Endpoint
	if u, err := url.Parse(cb.Endpoint); err == nil && u.Host != "" {
		host = u.Host
	}
	s.sendSuggestionToOwner(Suggestion{
		frameBase: frame(TypeSuggestion),
		Source:    "endgame:" + host,
		Fen:       fen,
		BestMove:  e.move,
		Multipv:   1,
	})
	s.log.Info("残局库命中", "endpoint", host, "fen", fen, "best", e.move)
}

// cloudQuery 请求端点并解析 queryall 形状的响应：管道分隔的多条
// "move:xxx,score:n,winrate:f,note:..." 记录取 score 最高者；
// unknown/error 等无 move 形态一律视为未命中。
func (s *Server) cloudQuery(cb config.CloudBookConfig, fen string) (cloudEntry, error) {
	// Endpoint 先 Parse 再 Query().Set 组参：字符串拼接在端点自带查询串时
	// 会拼出 "...?a=b?action=queryall" 的畸形 URL；Parse+Set 对已有参数是
	// 合并语义，带不带查询串的端点写法都成立
	u, err := url.Parse(cb.Endpoint)
	if err != nil {
		return cloudEntry{}, fmt.Errorf("端点 URL 解析失败: %w", err)
	}
	q := u.Query()
	q.Set("action", "queryall")
	q.Set("board", fen)
	if cb.APIKey != "" {
		q.Set("key", cb.APIKey)
	}
	u.RawQuery = q.Encode()
	resp, err := cloudHTTPClient.Get(u.String())
	if err != nil {
		return cloudEntry{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return cloudEntry{}, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, cloudBodyLimit))
	if err != nil {
		return cloudEntry{}, err
	}
	if !strings.Contains(string(body), "move:") {
		return cloudEntry{}, nil // unknown/error 形态 = 未命中（缓存住，防重复打）
	}
	best, bestScore := "", -1<<60
	for _, rec := range strings.Split(string(body), "|") {
		mv, sc, hasScore := "", 0, false
		for _, kv := range strings.Split(strings.TrimSpace(rec), ",") {
			p := strings.SplitN(kv, ":", 2)
			if len(p) != 2 {
				continue
			}
			switch p[0] {
			case "move":
				mv = strings.TrimSpace(p[1])
			case "score":
				if n, err := strconv.Atoi(strings.TrimSpace(p[1])); err == nil {
					sc, hasScore = n, true
				}
			}
		}
		if mv != "" && hasScore && sc > bestScore {
			best, bestScore = mv, sc
		}
	}
	if best == "" {
		return cloudEntry{}, nil
	}
	return cloudEntry{hit: true, move: best}, nil
}

// endgameQuery 残局库查询（2026-11-01）：action=query + endgame=1 只取云库
// 残局库（chessdb 规范：egtb: 前缀仅在 query 系返回，queryall 不混合返回
// 残局数据——故残局走独立请求而非复用 queryall 解析）。egtbmetric 固定 dtm
// （文档默认；DTC 到吃子/转换口径暂不暴露配置，需要时再立键）。
// 响应形态：命中为 "egtb:<着法>"（可管道分隔多条，取首条——残局库每局面
// 一条正解）；unknown / checkmate / stalemate / error 等无 egtb: 形态一律
// 视为未命中（checkmate/stalemate 是局面状态说明，无着法可下发）。
func (s *Server) endgameQuery(cb config.CloudBookConfig, fen string) (cloudEntry, error) {
	u, err := url.Parse(cb.Endpoint)
	if err != nil {
		return cloudEntry{}, fmt.Errorf("端点 URL 解析失败: %w", err)
	}
	q := u.Query()
	q.Set("action", "query")
	q.Set("endgame", "1")
	q.Set("egtbmetric", "dtm")
	q.Set("board", fen)
	if cb.APIKey != "" {
		q.Set("key", cb.APIKey)
	}
	u.RawQuery = q.Encode()
	resp, err := cloudHTTPClient.Get(u.String())
	if err != nil {
		return cloudEntry{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return cloudEntry{}, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, cloudBodyLimit))
	if err != nil {
		return cloudEntry{}, err
	}
	for _, rec := range strings.Split(string(body), "|") {
		rec = strings.TrimSpace(rec)
		if mv, ok := strings.CutPrefix(rec, "egtb:"); ok {
			mv = strings.TrimSpace(mv)
			if mv != "" && mv != "null" {
				return cloudEntry{hit: true, move: mv}, nil
			}
		}
	}
	return cloudEntry{}, nil
}

// cloudCached / cloudStore FEN 缓存（含未命中；满则整表清空）。
// 键 = FEN；残局查询经 endgameKey 加 "eg|" 前缀——同一 FEN 的开局结果与
// 残局结果互不覆盖（hit/miss 语义不同，共用一键会互相冲刷）。
func (s *Server) cloudCached(fen string) (cloudEntry, bool) {
	s.cloudMu.Lock()
	defer s.cloudMu.Unlock()
	e, ok := s.cloudCache[fen]
	return e, ok
}

func (s *Server) cloudStore(fen string, e cloudEntry) {
	s.cloudMu.Lock()
	defer s.cloudMu.Unlock()
	if len(s.cloudCache) >= cloudCacheCap {
		s.cloudCache = map[string]cloudEntry{}
	}
	s.cloudCache[fen] = e
}

// endgameKey 残局缓存键：前缀隔离开局/残局两类结果（前缀含 "|"，与 FEN
// 字符集不相交，不会与真 FEN 键混淆）。
func endgameKey(fen string) string { return "eg|" + fen }

func (s *Server) cloudCachedEndgame(fen string) (cloudEntry, bool) {
	s.cloudMu.Lock()
	defer s.cloudMu.Unlock()
	e, ok := s.cloudCache[endgameKey(fen)]
	return e, ok
}

func (s *Server) cloudStoreEndgame(fen string, e cloudEntry) {
	s.cloudMu.Lock()
	defer s.cloudMu.Unlock()
	if len(s.cloudCache) >= cloudCacheCap {
		s.cloudCache = map[string]cloudEntry{}
	}
	s.cloudCache[endgameKey(fen)] = e
}
