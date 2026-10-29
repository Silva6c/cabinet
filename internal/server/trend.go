// trend.go 走势序列（2026-10-28 功能定稿）：当前对局的引擎终态分序列，供控制台
// "走势"面板画折线。纯内存、按对局 ID 分段——NEW_GAME（手机识别器确认新局）触发
// 重置；旧手机不发标记时序列仍累积（game_id 空串），曲线表现为"从当前时刻起笔"。
// 跨局历史不在这里：沉淀库按 game_id 归组（store.suggestions），职责分离。
package server

import "time"

// TrendPoint 走势单点（红方视角；控制台经 TrendSnapshot 拷贝消费）。
type TrendPoint struct {
	TsMs    int64 // 建议下发时刻（毫秒）
	ScoreCP int   // 红方视角厘兵；将杀压成 ±1500（曲线同尺）
	WinRate int   // 红方胜率百分比（引擎 WDL；未输出为 -1）
}

const (
	trendCap    = 240 // 序列上限（约一整局的建议数）
	trendTrimTo = 160 // 触顶后回收到该长度（避免逐点搬移）
)

// trendAppend 追加终态分点（engine_pump 收束帧；与 readLoop 的重置并发，全程持锁）。
func (s *Server) trendAppend(pt TrendPoint) {
	s.trendMu.Lock()
	defer s.trendMu.Unlock()
	s.trendPts = append(s.trendPts, pt)
	if n := len(s.trendPts); n > trendCap {
		s.trendPts = append([]TrendPoint(nil), s.trendPts[n-trendTrimTo:]...)
	}
}

// trendReset 新局边界：换对局 ID 并清空序列（position 处理 new_game 标记时调用）。
func (s *Server) trendReset(gameID string) {
	s.trendMu.Lock()
	defer s.trendMu.Unlock()
	s.trendGame = gameID
	s.trendPts = nil
}

// TrendSnapshot 当前对局 ID 与序列拷贝（控制台 1.5s 刷新回调读；nil 语义由调用方兜底）。
func (s *Server) TrendSnapshot() (string, []TrendPoint) {
	s.trendMu.Lock()
	defer s.trendMu.Unlock()
	game, _ := s.currentGameID.Load().(string)
	if s.trendGame != "" {
		game = s.trendGame
	}
	pts := make([]TrendPoint, len(s.trendPts))
	copy(pts, s.trendPts)
	return game, pts
}

// trendPointFromSug 由终态建议构造走势点（分数/胜率均红方视角，engine_pump 已换算）。
func trendPointFromSug(scoreCP, scoreMate *int, winRate *int) (TrendPoint, bool) {
	pt := TrendPoint{TsMs: time.Now().UnixMilli(), WinRate: -1}
	switch {
	case scoreCP != nil:
		pt.ScoreCP = *scoreCP
	case scoreMate != nil:
		pt.ScoreCP = *scoreMate * 1500 // 将杀映射到曲线量程外（±1500），视觉即"顶格"
	default:
		return TrendPoint{}, false
	}
	if winRate != nil {
		pt.WinRate = *winRate
	}
	return pt, true
}
