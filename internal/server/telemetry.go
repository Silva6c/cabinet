package server

import (
	crand "crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func (s *Server) recordSuggestion(rec SuggestionRecord) {
	// GameID 在此统一取当前局 ID（调用点无须各自传）：NEW_GAME 首帧已切换
	// currentGameID，本条建议必然归属新局
	if id, ok := s.currentGameID.Load().(string); ok {
		rec.GameID = id
	}
	if s.onSuggestion != nil {
		s.onSuggestion(rec)
	}
}

// newGameID 生成对局 ID："game-<时刻>-<短随机>"——时刻便于人眼排序与日志
// 检索，短随机防同秒重开撞 ID。
func newGameID() string {
	return "game-" + time.Now().Format("20060102-150405") + "-" + randomHex4()
}

// randomHex4 4 位十六进制随机串（crypto/rand；失败回退纳秒计数）。
func randomHex4() string {
	var b [2]byte
	if _, err := crand.Read(b[:]); err != nil {
		return fmt.Sprintf("%04x", time.Now().UnixNano()%0xffff)
	}
	return fmt.Sprintf("%02x%02x", b[0], b[1])
}

// phoneGameSplitMarker 手机端"新局开始"分界行特征串：识别到开局阵型确认
// （BoardTracker NEW_GAME）时上行，PC 落盘检测到即切换下一局日志文件。
const phoneGameSplitMarker = "===== 新局开始"

// phoneLogHasGameSplit 一批日志行里是否含新局分界行
func phoneLogHasGameSplit(lines []string) bool {
	for _, line := range lines {
		if strings.Contains(line, phoneGameSplitMarker) {
			return true
		}
	}
	return false
}

// phoneLogFileName 单局日志文件名：phone-<局开始日期时刻>-局<序号>.log。
// 序号为 PC 进程内递增，重启后从 1 重计——文件名含开始时刻，跨重启不冲突。
func phoneLogFileName(now time.Time, seq int) string {
	return fmt.Sprintf("phone-%s-局%d.log", now.Format("20060102-150405"), seq)
}

// pruneOldPhoneLogs 清理 7 天前的手机日志（切新局时顺手执行，按文件修改时间）
func pruneOldPhoneLogs(dir string) {
	cutoff := time.Now().AddDate(0, 0, -7)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, "phone-") || !strings.HasSuffix(name, ".log") {
			continue
		}
		if info, err := e.Info(); err == nil && info.ModTime().Before(cutoff) {
			_ = os.Remove(filepath.Join(dir, name))
		}
	}
}

// handleLogFrame 手机端诊断日志落盘（协议 §4.11）：单局分卷——手机端识别到
// 新对局时上行"===== 新局开始"分界行，检测到即切换新文件
// phone-<开始时刻>-局<N>.log（2026-10-01 用户反馈：单日单文件混多局检索困难，
// 一局一文件后按局定位）。同批分界行之前的几行（2s 上行窗口内）会落进新局
// 文件头，诊断用途可接受。纯诊断辅助：写失败只记调试日志，绝不影响对局链路。
func (s *Server) handleLogFrame(c *Conn, m *Log) {
	if len(m.Lines) == 0 {
		return
	}
	s.phoneLogMu.Lock()
	defer s.phoneLogMu.Unlock()
	dir := filepath.Join(filepath.Dir(s.cfg.Log.File), "phone")
	if phoneLogHasGameSplit(m.Lines) {
		s.phoneLogSeq++
		s.phoneLogPath = filepath.Join(dir, phoneLogFileName(time.Now(), s.phoneLogSeq))
		pruneOldPhoneLogs(dir)
	} else if s.phoneLogPath == "" {
		s.phoneLogSeq = 1
		s.phoneLogPath = filepath.Join(dir, phoneLogFileName(time.Now(), s.phoneLogSeq))
	}
	var b strings.Builder
	now := time.Now().Format("15:04:05.000")
	short := c.ID
	if len(short) > 8 {
		short = short[:8]
	}
	// 会话标记带设备昵称（2026-10-28 设备昵称功能）：多台手机各自落盘时一眼
	// 分清归属；旧手机端未上报昵称时保持纯会话 ID 形态不变
	tag := short
	if name := c.Identity().Name; name != "" {
		tag = short + "/" + name
	}
	for _, line := range m.Lines {
		b.WriteString(now)
		b.WriteString(" [")
		b.WriteString(tag)
		b.WriteString("] ")
		b.WriteString(line)
		b.WriteByte('\n')
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		c.log.Debug("手机日志目录创建失败", "err", err)
		return
	}
	f, err := os.OpenFile(s.phoneLogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		c.log.Debug("手机日志文件打开失败", "err", err)
		return
	}
	defer func() { _ = f.Close() }()
	if _, err := f.WriteString(b.String()); err != nil {
		c.log.Debug("手机日志写入失败", "err", err)
	}
}

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
