// cloudbook.go 云端开局库（可选，2026-10-28 定稿）：桔库与沉淀双未命中时的
// 外部开局库补充。端点默认预设 chessdb.cn（免费公开基建，免 key）；自带 API
// 的使用者改 endpoint/api_key。隐私口径：开启 = 同意把对局局面 FEN 发往端点
// （configs/cabinet.example.toml 同款措辞），默认关由用户自行开启。
//
// 行为纪律：异步（不阻塞引擎搜索，引擎随后以最新覆盖语义收束）、限流
// （≥1.5s 一次）、FEN 缓存（含未命中，防重复打外网）、失败静默（与检查更新
// 同款契约）。命中以 "cloud:<端点域名>" 前缀 source 下发——复用 suggestion 帧，
// 旧手机零改动；不带分数/PV/深度：chessdb 的 score 视角语义未考证，与桔库
// "不猜"口径一致（库命中=绿箭头直显，无需分数）。
package server

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
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
// 返回 true = 建议来源已由云库接手（调用方 return）；false = 云库无法定论，
// 调用方立即走常规链。fallback 在云库 goroutine 里执行——常规链全程只用
// ownerSession/lastFen/引擎 Request 等并发安全设施，不直接依赖 Conn 存活。
func (s *Server) cloudLookupFirst(fen string, fallback func()) bool {
	if !s.cloudEnable.Load() || !s.cloudFirst.Load() {
		return false // 总开关或优先开关未开：完全现状行为
	}
	cb := s.cfg.CloudBook
	if cb.Endpoint == "" {
		return false
	}
	if e, ok := s.cloudCached(fen); ok {
		if e.hit {
			s.cloudDeliver(cb, fen, e)
			return true
		}
		return false
	}
	now := time.Now().UnixMilli()
	if !s.tryCloudThrottle(now) {
		return false
	}
	go func() {
		e, err := s.cloudQuery(cb, fen)
		if err != nil {
			s.log.Info("云库查询失败（静默，回落常规链）", "err", err)
			e = cloudEntry{}
		}
		s.cloudStore(fen, e)
		if e.hit {
			// 命中前复查"局面仍最新"：与下方未命中分支对齐——异步窗内换局
			// 则放弃下发（新局面自会走自己的链，过期建议只会误导）
			if want, _ := s.lastFen.Load().(string); want == fen {
				s.cloudDeliver(cb, fen, e)
			}
			return
		}
		if want, _ := s.lastFen.Load().(string); want == fen {
			fallback()
		}
	}()
	return true
}

// cloudLookupAsync 桔库/沉淀双未命中后的云库补充（fire-and-forget，失败静默）。
func (s *Server) cloudLookupAsync(fen string) {
	if !s.cloudEnable.Load() {
		return // 运行时开关（PC 面板切换）：查询热路径只读原子位，不碰 cfg
	}
	cb := s.cfg.CloudBook
	if cb.Endpoint == "" {
		return
	}
	if e, ok := s.cloudCached(fen); ok {
		s.cloudDeliver(cb, fen, e) // 缓存命中（含未命中）直接消费，不限流
		return
	}
	now := time.Now().UnixMilli()
	if !s.tryCloudThrottle(now) {
		return // 限流窗口内放弃本次（下一局面再试）
	}
	go func() {
		e, err := s.cloudQuery(cb, fen)
		if err != nil {
			s.log.Info("云库查询失败（静默）", "err", err)
			e = cloudEntry{}
		}
		s.cloudStore(fen, e)
		// 异步窗（查询可达数秒）内可能已被新 position 取代：过期命中不再
		// 下发，与 cloudLookupFirst 的"最新覆盖"复查对齐（miss 本就被
		// cloudDeliver 拦下，一起包住无碍）
		if want, _ := s.lastFen.Load().(string); want == fen {
			s.cloudDeliver(cb, fen, e)
		}
	}()
}

// tryCloudThrottle 限流窗口占位：CAS 循环实现原子的 check-then-set。直接
// "Load 判间隔 + Store"会被两个查询入口（cloudLookupFirst/Async）并发交错
// ——两路都看到窗口外并同时 Store，限流被绕过；CAS 失败说明他人刚占位，
// 重读重判即可。返回 true = 本次占位成功（准许查询）。
func (s *Server) tryCloudThrottle(now int64) bool {
	for {
		last := s.lastCloudMs.Load()
		if now-last < cloudThrottleMs {
			return false
		}
		if s.lastCloudMs.CompareAndSwap(last, now) {
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

// cloudCached / cloudStore FEN 缓存（含未命中；满则整表清空）。
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
