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

// SetCloudBookEnable / CloudBookEnabled 运行时开关（PC 面板"云库查询"勾选框）。
// cfg.CloudBook.Enable 只在 New 时作为种子进入，此后运行时以本原子位为准，
// 落盘持久化由 main 的 Set 回调负责（与算力落盘同锁同路）。
func (s *Server) SetCloudBookEnable(on bool) { s.cloudEnable.Store(on) }

func (s *Server) CloudBookEnabled() bool { return s.cloudEnable.Load() }

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
	if now-s.lastCloudMs.Load() < cloudThrottleMs {
		return // 限流窗口内放弃本次（下一局面再试）
	}
	s.lastCloudMs.Store(now)
	go func() {
		e, err := s.cloudQuery(cb, fen)
		if err != nil {
			s.log.Info("云库查询失败（静默）", "err", err)
			e = cloudEntry{}
		}
		s.cloudStore(fen, e)
		s.cloudDeliver(cb, fen, e)
	}()
}

// cloudDeliver 命中下发：走 sendSuggestionToOwner（与引擎建议同一条最新覆盖路由，
// 3s 查询窗内换局则随新局面覆盖，无害）。
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
	q := cb.Endpoint + "?action=queryall&board=" + url.QueryEscape(fen)
	if cb.APIKey != "" {
		q += "&key=" + url.QueryEscape(cb.APIKey)
	}
	client := &http.Client{Timeout: cloudQueryTimeout}
	resp, err := client.Get(q)
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
