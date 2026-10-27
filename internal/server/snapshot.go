package server

import (
	"time"
)

// EngineSnap 引擎实时指标快照（控制台引擎区显示）。
// Depth/Nodes/NPS/Hashfull 为最近一次广播的搜索指标（收束后清零）；
// HashMB/Threads/MultiPV/TargetDepth 为配置值（0 = 引擎自定，显示层兜底）。
type EngineSnap struct {
	State       string
	Depth       int
	Nodes       int64
	NPS         int64
	Hashfull    int
	HashMB      int
	Threads     int
	MultiPV     int
	TargetDepth int
}

// ClientSnap 一个连接的展示快照（控制台连接区一行）。
// RTTMs 为 PC 主动 ping 测得的往返延迟（毫秒）；-1 = 尚未测得。
// Path 目前恒为 "LAN"，M4 中继上线后区分 LAN/中继。
type ClientSnap struct {
	SessionID   string // 缩略（前 8 hex）
	FullID      string // 完整会话 ID（控制台"断开会话"按此定位，不显示）
	Client      string // hello 自报标识，未鉴权时为空
	Ver         string
	Authed      bool
	RTTMs       int64
	Path        string
	LastPos     time.Time // 最近一次 position 时间；零值 = 未发过
	Fen         string    // 最近一次 position 的 FEN（控制台"最后 position"列摘要显示）
	ConnectedAt time.Time
}

// ConsoleSnapshot 控制台一次刷新所需的全部服务端只读数据。
type ConsoleSnapshot struct {
	Engine  EngineSnap
	Clients []ClientSnap
}

// ConsoleSnapshot 组装当前引擎与连接快照（只读内存读，可安全并发调用）。
func (s *Server) ConsoleSnapshot() ConsoleSnapshot {
	// 全部显示动态值（set_engine 远程调节 / 面板调节后的当前目标）；
	// 引擎桥不支持调节时回退配置值
	snap := ConsoleSnapshot{
		Engine: EngineSnap{State: s.EngineState()},
	}
	if t, ok := s.eng.(EngineTuner); ok {
		snap.Engine.Threads = t.Threads()
		snap.Engine.MultiPV = t.MultiPV()
		snap.Engine.HashMB = t.HashMB()
		snap.Engine.TargetDepth = t.Depth()
	} else {
		snap.Engine.Threads = s.cfg.Engine.Threads
		snap.Engine.MultiPV = s.cfg.Engine.MultiPV
		snap.Engine.HashMB = s.cfg.Engine.HashMB
		snap.Engine.TargetDepth = s.cfg.Engine.Depth
	}
	if m, ok := s.engMetrics.Load().(EngineStatus); ok {
		snap.Engine.Depth = m.Depth
		snap.Engine.Nodes = m.Nodes
		snap.Engine.NPS = m.NPS
		snap.Engine.Hashfull = m.Hashfull
	}
	snap.Clients = s.hub.Snapshots()
	return snap
}
