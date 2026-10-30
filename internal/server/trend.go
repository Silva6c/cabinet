// trend.go 走势序列（2026-10-28 功能定稿；2026-10-30 设备分组重构）：每个设备
// 一条引擎终态分序列，供控制台"走势"面板画曲线。纯内存、按对局 ID 分段——
// NEW_GAME（该设备手机识别器确认新局）触发该组重置；旧手机不发标记时该组序列
// 仍累积（game_id 空串），曲线表现为"从当前时刻起笔"。
//
// ── 为什么按设备分组（2026-10-30 立项）─────────────────────────────────
// 全局单序列在多连接下语义错误：两台设备先后/并行使用时，曲线会把不同对局的
// 评估点混在一条线上——连"换机"（旧机断开、新机接续，都没有 new_game 标记）
// 都会把新机的对局接在旧机曲线后面。语义错误的功能比缺功能更糟。分组后各看
// 各的曲线；引擎仍是共享单实例，焦点 = 最新上报 position 的设备（抢占式算力
// 分配，见 ownerDev），建议下发本来就按归属连接定向（§4.5），不随本次改动。
//
// ── 分组键的三个候选与取舍（设计记录，勿轻易翻案）─────────────────────
//   - token：不成立。本仓是单一 token 配对模型——二维码给所有设备扫同一个
//     token，auth 恒时校验通过后即丢弃，token 标识"配对资格"而非"设备"。
//   - 会话 ID：不成立。每条连接 newSessionID 随机生成，重连即新会话，曲线
//     每断一次就断一截。
//   - devid（采用）：hello 自报设备标识，手机端首启随机生成并持久化——
//     重装/清数据 = 新身份，语义自洽；同一台手机重连、重发，曲线连续。
//
// 信任模型：devid 是自报字段可伪造，但准入由配对 token 把守——伪造的后果仅
// 是"多出一条假设备的曲线"，不构成越权（内网自用场景足够）。旧手机缺 devid
// 时回退会话 ID（退化为"每会话一条"的历史行为）；键加 "d:"/"s:" 前缀防两个
// 命名空间撞形（16-32 hex devid 与 16 hex 会话 ID 形态相同）。
//
// 跨局历史不在这里：沉淀库按 game_id 归组（store.suggestions），职责分离。
// 已知瑕疵（登记账本，设备列另立项）：沉淀的 GameID 归属取全局 currentGameID
// （最新 new_game），多设备并行下 B 的建议可能记到 A 的新局——走势分组解决
// 展示层，沉淀层等"SQLite 加设备列"立项一并处理。
package server

import (
	"sort"
	"strings"
	"time"
)

// TrendPoint 走势单点（分数/胜率恒为红方视角；我方执色随点记录，控制台换算展示）。
type TrendPoint struct {
	TsMs    int64 // 建议下发时刻（毫秒）
	ScoreCP int   // 红方视角厘兵；将杀压成 ±1500（曲线同尺）
	WinRate int   // 红方胜率百分比（引擎 WDL；未输出为 -1）
	// MyRed/MySideKnown 该点追加时的我方执色（该设备 position.my_red 上报）。
	// 控制台按"我方视角"画曲线：MySideKnown && !MyRed 时取负/取补；Known=false
	// （旧手机不传）退回红方视角，与 2026-10-29 之前的展示一致。
	MyRed       bool
	MySideKnown bool
	// Fen 该点局面（2026-10-29 同局面去重键，纯内存字段不上协议）：同一局面被
	// 重复评估——切算力参数 reSearchLatest、手机 welcome/not_ready 补发触发重搜
	// ——替换末点而非追加，曲线不再长锯齿（"每局面一个终态点"即本语义）。
	Fen string
}

// trendSeries 单设备的走势组：一条序列 + 该设备的对局边界、执色记忆与展示名。
// 生命周期：该设备首条 position 到达时创建，进程退出才消失——断线不删（重连
// 续看曲线），设备组数由 trendGroupCap 封顶。
type trendSeries struct {
	gameID string       // 该设备当前对局 ID（NEW_GAME 换段；空 = 未发过标记）
	name   string       // 该设备最近一次自报昵称（hello name；断开后保留供组标签显示）
	pts    []TrendPoint // 终态分序列（红方视角，组内同局面去重）
	// myRed/mySideOK 该组我方执色（该设备 position.my_red 上报；拒帧也记——
	// 下一条合法局面的走势点带上正确视角）
	myRed      bool
	mySideOK   bool
	lastActive time.Time // 最近一次追加/换段/上报（组数超限时淘汰最久不活跃者）
}

const (
	trendCap      = 240 // 单组序列上限（约一整局的建议数）
	trendTrimTo   = 160 // 触顶后回收到该长度（避免逐点搬移）
	trendGroupCap = 12  // 组上限：配对设备天然有限，封顶只为防失控（伪造 devid 也刷
	// 不爆内存）；触顶淘汰最久不活跃组，焦点组永不淘汰（正在看的曲线被淘汰会让
	// 视图凭空清空）。
)

// devKeyOf 连接的走势分组键：hello 自报 devid（"d:" 前缀）；旧手机缺 devid 回退
// 会话 ID（"s:" 前缀，每会话一条 = 2026-10-30 之前的历史行为）。前缀防两个命名
// 空间撞形。取舍依据见文件头"分组键的三个候选"。
func devKeyOf(c *Conn) string {
	if id := c.Identity(); id.Devid != "" {
		return "d:" + id.Devid
	}
	return "s:" + c.ID
}

// trendGroupOf 取（或建）设备组；组数触顶时淘汰最久未活跃组（焦点组永不淘汰）。
// 惰性初始化 map（Server 零值可用，测试直构 &Server{} 不经 NewServer）。
func (s *Server) trendGroupOf(key string) *trendSeries {
	if s.trendGroups == nil {
		s.trendGroups = map[string]*trendSeries{}
	}
	if g, ok := s.trendGroups[key]; ok {
		return g
	}
	if len(s.trendGroups) >= trendGroupCap {
		focus, _ := s.ownerDev.Load().(string)
		var oldestKey string
		var oldest time.Time
		for k, cand := range s.trendGroups {
			if k == focus {
				continue // 焦点组是当前默认视图，淘汰它 = 视图凭空清空
			}
			if oldestKey == "" || cand.lastActive.Before(oldest) {
				oldestKey, oldest = k, cand.lastActive
			}
		}
		if oldestKey != "" {
			delete(s.trendGroups, oldestKey)
		}
	}
	g := &trendSeries{lastActive: time.Now()}
	s.trendGroups[key] = g
	return g
}

// trendNoteDevice 刷新设备组昵称与活跃时间（每条 position 顺带调用；断开后组
// 标签仍显示最后昵称，重连即续上）。
func (s *Server) trendNoteDevice(key, name string) {
	s.trendMu.Lock()
	defer s.trendMu.Unlock()
	g := s.trendGroupOf(key)
	if name != "" {
		g.name = name
	}
	g.lastActive = time.Now()
}

// trendSetMySide 记录该设备我方执色（每条带 my_red 的 position 到达时调用；
// 拒帧也记——即使本帧因识别瑕疵被拒，下一条合法局面的走势点也能带上正确视角）。
func (s *Server) trendSetMySide(key string, red bool) {
	s.trendMu.Lock()
	defer s.trendMu.Unlock()
	g := s.trendGroupOf(key)
	g.myRed = red
	g.mySideOK = true
	g.lastActive = time.Now()
}

// trendReset 设备内新局边界：换对局 ID 并清空该组序列（该设备 position 带
// new_game 标记时调用；组间互不影响——A 开新局不清 B 的曲线）。
func (s *Server) trendReset(key, gameID string) {
	s.trendMu.Lock()
	defer s.trendMu.Unlock()
	g := s.trendGroupOf(key)
	g.gameID = gameID
	g.pts = nil
	g.lastActive = time.Now()
}

// trendAppend 记录终态分点到指定设备组（engine_pump 收束帧；key = ownerDev，
// 与建议下发同源归属）。同局面去重：组内末点 FEN 相同（切算力参数重搜/补发
// 重搜同一局面）→ 替换末点，最新评估原地刷新、曲线不长锯齿；不同局面才追加。
// 空键防御性不落点（无归属无曲线；正常路径 position 已填 ownerDev，门控保证
// 走到这里必有归属）。
func (s *Server) trendAppend(key string, pt TrendPoint) {
	if key == "" {
		return
	}
	s.trendMu.Lock()
	defer s.trendMu.Unlock()
	g := s.trendGroupOf(key)
	g.lastActive = time.Now()
	// 我方执色随点固化：追加时刻该组已知值即该点视角（对局中途换边也能逐点自洽）
	pt.MyRed = g.myRed
	pt.MySideKnown = g.mySideOK
	if n := len(g.pts); n > 0 && pt.Fen != "" && g.pts[n-1].Fen == pt.Fen {
		g.pts[n-1] = pt
		return
	}
	g.pts = append(g.pts, pt)
	if n := len(g.pts); n > trendCap {
		g.pts = append([]TrendPoint(nil), g.pts[n-trendTrimTo:]...)
	}
}

// TrendDevInfo 控制台设备切换列表的一项（Key 回传 TrendSnapshotFor 取该组曲线；
// Label 为按钮文本）。
type TrendDevInfo struct {
	Key   string // 设备键（devKeyOf 形态："d:"+devid 或 "s:"+会话 ID）
	Label string // 展示名：昵称·devid 前 4 / 会话 短码（无昵称时）
}

// TrendDevices 当前**在线**设备的走势组，最近活跃在前（控制台设备按钮排数据
// 源；按钮顺序稳定 = 刷新周期不跳动）。断联设备的组保留在内存（曲线可回看、
// 重连续接）但**不进列表**——按钮即入口，断联隐藏入口（2026-10-30 用户定稿），
// 重连后按钮与曲线一起回来。先取在线集合再持 trendMu：hub 锁与走势锁不做
// 嵌套，杜绝锁序耦合。
func (s *Server) TrendDevices() []TrendDevInfo {
	online := s.hub.OnlineDeviceKeys()
	s.trendMu.Lock()
	defer s.trendMu.Unlock()
	type entry struct {
		info TrendDevInfo
		at   time.Time
	}
	entries := make([]entry, 0, len(online))
	for k, g := range s.trendGroups {
		if !online[k] {
			continue // 断联设备：入口隐藏（组保留，重连即回）
		}
		entries = append(entries, entry{TrendDevInfo{Key: k, Label: trendLabel(k, g.name)}, g.lastActive})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].at.After(entries[j].at) })
	out := make([]TrendDevInfo, len(entries))
	for i, e := range entries {
		out[i] = e.info
	}
	return out
}

// trendLabel 组展示名：昵称·键短码（devid 前 4 / 会话 ID 前 4）。无昵称的
// devid 组直接显示短码；会话键（旧手机）加"会话"字样与设备键区分。
func trendLabel(key, name string) string {
	short := strings.TrimPrefix(strings.TrimPrefix(key, "d:"), "s:")
	if len(short) > 4 {
		short = short[:4]
	}
	if strings.HasPrefix(key, "s:") {
		return "会话 " + short
	}
	if name != "" {
		return name + "·" + short
	}
	return short
}

// TrendSnapshotFor 指定设备组的序列快照；空键 = 焦点设备（引擎焦点，即最新
// 上报 position 的来源——控制台默认视图同款语义）。键未知（组被淘汰/从未
// 上报）→ 空快照，调用方兜底。
func (s *Server) TrendSnapshotFor(key string) (string, []TrendPoint) {
	if key == "" {
		key, _ = s.ownerDev.Load().(string)
	}
	s.trendMu.Lock()
	defer s.trendMu.Unlock()
	g, ok := s.trendGroups[key]
	if !ok {
		return "", nil
	}
	pts := make([]TrendPoint, len(g.pts))
	copy(pts, g.pts)
	return g.gameID, pts
}

// trendPointFromSug 由终态建议构造走势点（分数/胜率均红方视角，engine_pump 已换算；
// fen 作同局面去重键，见 trendAppend）。
func trendPointFromSug(fen string, scoreCP, scoreMate *int, winRate *int) (TrendPoint, bool) {
	pt := TrendPoint{TsMs: time.Now().UnixMilli(), WinRate: -1, Fen: fen}
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
