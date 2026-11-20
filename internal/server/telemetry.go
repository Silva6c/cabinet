package server

import (
	crand "crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// recordSuggestion 沉淀回调（nil 安全）：把已下发建议转交注册的 sink 落库。
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

// 手机日志卫生预算（2026-11-14 红队第二轮）：已鉴权端可循环发 log 帧刷盘——
// ①行内嵌 \n 可伪造任意时间戳/内容行；②64KB 帧上限最坏 ~2.2 万行（单帧落盘
// 0.5-1.1MB）；③切卷特征串每帧触发新文件+全目录扫描。三重兜底：行清洗 +
// 每帧行数配额 + 目录容量预算（切卷匹配保持 Contains——手机端真实分界行带
// 自身日志前缀，收紧到行首会破坏功能）。
const (
	phoneLogMaxLinesPerFrame = 400
	phoneLogDirBudget        = 512 << 20 // 512MB：正常按局分卷 3 天远用不满
)

// sanitizePhoneLogLine 手机日志行清洗：剥离行内控制字符（\r\n、C0 区、DEL）。
// 手机端按行上行，但被控端可在行内嵌 \n 伪造任意内容行；剥离仅影响诊断
// 文件，零诊断损失（\t 保留——无法伪造行边界）。
func sanitizePhoneLogLine(line string) string {
	return strings.Map(func(r rune) rune {
		if (r < 0x20 && r != '\t') || r == 0x7f {
			return -1
		}
		return r
	}, line)
}

// enforcePhoneLogBudget 手机日志目录容量兜底：3 天时间裁剪拦不住"高频切卷+
// 大帧"刷盘，目录超预算时按修改时间从旧到新删除直到回落。当前写入文件是最
// 新卷，旧序删除天然避开。仅诊断文件，删除无损对局链路。
func enforcePhoneLogBudget(dir string, maxBytes int64) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	type logFile struct {
		path  string
		size  int64
		mtime time.Time
	}
	var files []logFile
	var total int64
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, "phone-") || !strings.HasSuffix(name, ".log") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, logFile{filepath.Join(dir, name), info.Size(), info.ModTime()})
		total += info.Size()
	}
	if total <= maxBytes {
		return
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mtime.Before(files[j].mtime) })
	for _, f := range files {
		if total <= maxBytes {
			return
		}
		if os.Remove(f.path) == nil {
			total -= f.size
		}
	}
}

// pruneOldPhoneLogs 清理 3 天前的手机日志（2026-11-07 用户定"只保留 3 天"，
// 与 PC 主日志 MaxAgeDays 同口径；切新局时顺手执行，按文件修改时间）
func pruneOldPhoneLogs(dir string) {
	cutoff := time.Now().AddDate(0, 0, -3)
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
	if s.phoneLogs == nil {
		s.phoneLogs = map[string]string{}
	}
	// 每帧行数配额（2026-11-14 红队第二轮，见顶部常量注释）：正常 2s 批量上行
	// 几十行，超配部分丢弃（诊断文件，宁缺勿刷盘）
	lines := m.Lines
	if len(lines) > phoneLogMaxLinesPerFrame {
		lines = lines[:phoneLogMaxLinesPerFrame]
	}
	// 切新文件的两个触发：①本会话手机端上行"新局开始"分界行（切局卷）；
	// ②本会话尚无文件（首帧/2026-11-07 重构后按会话分卷——旧实现单路径
	// 跨会话共用，断线重连后日志持续追加进几天前文件名的旧文件）
	if s.phoneLogs[c.ID] == "" || phoneLogHasGameSplit(lines) {
		s.phoneLogSeq++
		s.phoneLogs[c.ID] = filepath.Join(dir, phoneLogFileName(time.Now(), s.phoneLogSeq))
		// 顺手 prune（2026-11-09 体检 H4：只在新局分界时清理会漏掉"反复
		// 连接但从不打局"的会话——每会话建一卷、无人清理持续堆积）
		pruneOldPhoneLogs(dir)
		enforcePhoneLogBudget(dir, phoneLogDirBudget)
	}
	logPath := s.phoneLogs[c.ID] // 本帧写入目标（切卷逻辑已定，锁内读取安全）
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
	for _, raw := range lines {
		b.WriteString(now)
		b.WriteString(" [")
		b.WriteString(tag)
		b.WriteString("] ")
		b.WriteString(sanitizePhoneLogLine(raw))
		b.WriteByte('\n')
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		c.log.Debug("手机日志目录创建失败", "err", err)
		return
	}
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		c.log.Debug("手机日志文件打开失败", "err", err)
		return
	}
	defer func() { _ = f.Close() }()
	if _, err := f.WriteString(b.String()); err != nil {
		c.log.Debug("手机日志写入失败", "err", err)
	}
}
