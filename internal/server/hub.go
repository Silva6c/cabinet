package server

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

// Conn 一条已完成 upgrade 的 WebSocket 连接的会话包装。
// M0 仅维护标识与写锁；多手机隔离/排队（session 模块）在 M1 细化。
type Conn struct {
	// ID 会话标识（welcome 的 session_id），16 hex 字符
	ID string
	// ws 底层连接
	ws *websocket.Conn
	// writeMu gorilla 连接不允许多 goroutine 并发写，所有写经 sendJSON 加锁
	writeMu sync.Mutex
	// authed 是否已完成 auth 握手。atomic.Bool：写侧仅握手 goroutine 置 true
	// 一次，读侧（Broadcast/SendToSession/Snapshots）与写侧无同步边——历史版本
	// 依赖"最坏读到旧值只影响一帧"的弱一致论证，2026-10-27 按原注释规划加固为
	// atomic.Bool（数据竞争在 -race 下是硬错误，弱一致不豁免）。
	authed atomic.Bool
	// ident 对端自报身份（hello 帧内容，见 identity）。atomic.Value 整体原子
	// 换：写侧仅握手 goroutine（重复 hello 后到覆盖先到），读侧为控制台刷新/
	// 日志落盘/管理日志——此前 Client/Ver 是普通 string 跨 goroutine 无同步边
	// （-race 硬错误隐患，与 authed 加固同理），2026-10-28 随 devid/name 引入
	// 一并收编
	ident atomic.Value // 恒存 identity 结构体（Identity/SetIdentity 读写）
	// log 本连接专属日志字段
	log *slog.Logger

	// 以下为控制台快照的只读观测值，atomic 保证跨 goroutine 读安全：
	// 写侧为 readLoop/handlePosition/pingLoop 所在 goroutine，读侧为控制台刷新。
	connectedAt atomic.Int64 // 建立连接时刻（Unix 毫秒）
	lastPosAt   atomic.Int64 // 最近一次 position 时刻（Unix 毫秒；0 = 未发过）
	lastFen     atomic.Value // 最近一次 position 的 fen（string；控制台"最后 position"列）
	rttMs       atomic.Int64 // 最近一次 PC ping → pong 往返毫秒（-1 = 尚未测得）
}

// identity 对端自报身份（hello 帧自报字段快照）。Devid/Name 为 2026-10-28
// 设备昵称功能新增（协议 §4.1 可选字段）；Name 已在握手期经 sanitizeName
// 规整、Devid 经 devidRegexp 校验，此处存的是展示就绪值。
type identity struct {
	Client string
	Ver    string
	Devid  string
	Name   string
}

// SetIdentity 握手期写入对端身份（整体原子换，后到覆盖先到）。
func (c *Conn) SetIdentity(i identity) { c.ident.Store(i) }

// Identity 读当前身份快照（未收到客户端 hello 时返回零值）。
func (c *Conn) Identity() identity {
	if v, ok := c.ident.Load().(identity); ok {
		return v
	}
	return identity{}
}

// NewConn 创建会话包装并生成会话 ID。
func NewConn(ws *websocket.Conn, logger *slog.Logger) *Conn {
	id := newSessionID()
	c := &Conn{
		ID:  id,
		ws:  ws,
		log: logger.With("session", id),
	}
	c.connectedAt.Store(time.Now().UnixMilli())
	c.rttMs.Store(-1)
	return c
}

// TouchPosition 记录最近一次 position 时刻（控制台连接区显示）。
func (c *Conn) TouchPosition() { c.lastPosAt.Store(time.Now().UnixMilli()) }

// OnPong 收到对端 pong：PC 主动 ping 测得的 RTT（协议 §4.7）。
func (c *Conn) OnPong(ts int64) {
	rtt := time.Now().UnixMilli() - ts
	if rtt < 0 {
		rtt = 0
	}
	c.rttMs.Store(rtt)
}

// Send 以写锁保护发送一条消息帧。
func (c *Conn) Send(msg any) error {
	return sendJSON(c.ws, &c.writeMu, msg)
}

// Close 关闭底层连接（幂等）。
func (c *Conn) Close() error {
	return c.ws.Close()
}

// Hub 维护当前全部活跃连接。全局单锁设计：conns 增删查共用一把 mu，锁内
// 只做 map 操作，网络写一律在锁外（先快照/找目标再 Send）——慢客户端绝不
// 能拖死 Register/Unregister/Count（握手与控制台都会卡）。竞态边界：快照
// 之后目标可能已被并发关闭，发送失败仅记 debug，属预期不重试。
type Hub struct {
	mu    sync.Mutex
	conns map[*Conn]struct{}
	log   *slog.Logger
}

// NewHub 创建连接管理中心。
func NewHub(logger *slog.Logger) *Hub {
	return &Hub{conns: make(map[*Conn]struct{}), log: logger}
}

// Register 登记连接。
func (h *Hub) Register(c *Conn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.conns[c] = struct{}{}
	h.log.Info("连接登记", "session", c.ID, "active", len(h.conns))
}

// Unregister 注销连接（不存在则静默）。
func (h *Hub) Unregister(c *Conn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.conns[c]; ok {
		delete(h.conns, c)
		h.log.Info("连接注销", "session", c.ID, "active", len(h.conns))
	}
}

// Count 当前活跃连接数。
func (h *Hub) Count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.conns)
}

// OnlineDeviceKeys 当前在线连接的走势设备键集合（devKeyOf 形态："d:"+devid /
// "s:"+会话 ID）。走势面板的设备按钮只显示在线设备——断联即隐藏入口
// （2026-10-30 用户定稿）；组数据本身保留在内存可回看，重连后按钮与曲线
// 一起回来（trend.go 组生命周期）。
func (h *Hub) OnlineDeviceKeys() map[string]bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	keys := make(map[string]bool, len(h.conns))
	for c := range h.conns {
		keys[devKeyOf(c)] = true
	}
	return keys
}

// Snapshots 全部连接的展示快照（按连接时间升序；控制台连接区用）。
func (h *Hub) Snapshots() []ClientSnap {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]ClientSnap, 0, len(h.conns))
	for c := range h.conns {
		ident := c.Identity()
		snap := ClientSnap{
			SessionID:   shortID(c.ID),
			FullID:      c.ID, // 管理操作（断开会话）按完整 ID 定位
			Client:      ident.Client,
			Ver:         ident.Ver,
			Devid:       ident.Devid,
			Name:        ident.Name,
			Authed:      c.authed.Load(),
			RTTMs:       c.rttMs.Load(),
			Path:        "LAN", // M4 中继上线后按连接来源区分
			ConnectedAt: time.UnixMilli(c.connectedAt.Load()),
		}
		if v := c.lastPosAt.Load(); v > 0 {
			snap.LastPos = time.UnixMilli(v)
		}
		if v, ok := c.lastFen.Load().(string); ok {
			snap.Fen = v // 控制台"最后 position"列显示 FEN 摘要（空 = 未发过）
		}
		out = append(out, snap)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ConnectedAt.Before(out[j].ConnectedAt) })
	return out
}

// Disconnect 手动断开指定会话（控制台管理操作）：先发应用层 bye 帧（协议 §5，
// 手机端按"对端 bye"记录），再发 websocket Close 控制帧并关闭——readLoop 走
// 正常关闭分支注销。未鉴权的连接同样可踢（卡死在握手的连接不该等 10s 超时）。
// 返回会话是否存在；注销由连接自身 goroutine 完成，此处只负责关。
//
// 发送与关闭在独立 goroutine 异步执行：目标若是半死客户端（TCP 黑洞），bye
// 写会耗满 3s 写超时、Close 控制帧再占 1s deadline——控制台"断开会话"在 fyne
// UI 线程同步调用到这里，同步执行会把整个界面冻结 3-4s（A4 实锤）。异步化的
// 取舍：修在 hub 侧保护了本方法的全部调用方（修在 ConsoleKick 侧只保护控制台
// 一条路）；代价是"返回 true 时连接尚未关闭"——查表保持同步保证返回值可靠，
// 关闭收尾的先后由 readLoop 的注销路径幂等兜底。竞态边界与 Broadcast 同款：
// goroutine 启动前已解锁、不持有 h.mu；目标可能被并发 CloseAll/重复 Disconnect
// 抢先关闭，冗余发送/关闭仅得到无害错误（写锁 writeMu 保证不并发写）。
func (h *Hub) Disconnect(sessionID, reason string) bool {
	h.mu.Lock()
	var target *Conn
	for c := range h.conns {
		if c.ID == sessionID {
			target = c
			break
		}
	}
	h.mu.Unlock()
	if target == nil {
		return false
	}
	go func() {
		_ = target.Send(Bye{frameBase: frame(TypeBye)})
		deadline := time.Now().Add(time.Second)
		_ = target.ws.WriteControl(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, reason), deadline)
		_ = target.Close()
		h.log.Info("手动断开会话", "session", target.ID, "client", target.Identity().Client, "reason", reason)
	}()
	return true
}

// CloseAll 关闭全部活跃连接（服务优雅关闭时调用）。
func (h *Hub) CloseAll() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.conns {
		_ = c.Close()
		delete(h.conns, c)
	}
}

// Broadcast 向全部已鉴权连接广播一条消息帧（单连接失败不中断其余）。
// marshal 一次复用：状态帧随引擎 depth 提升高频广播（一次搜索 20+ 次），
// 逐连接重复 json.Marshal 同一结构体是线性浪费。
func (h *Hub) Broadcast(msg any) {
	h.mu.Lock()
	conns := make([]*Conn, 0, len(h.conns))
	for c := range h.conns {
		if c.authed.Load() {
			conns = append(conns, c)
		}
	}
	h.mu.Unlock()
	b, err := json.Marshal(msg)
	if err != nil {
		h.log.Warn("广播帧序列化失败", "err", err)
		return
	}
	for _, c := range conns {
		if err := sendRaw(c.ws, &c.writeMu, b); err != nil {
			h.log.Debug("广播发送失败", "session", c.ID, "err", err)
		}
	}
}

// SendToSession 定向发送一条消息帧给指定会话（建议路由用：多设备各下各的棋，
// suggestion 只回给出局面的连接，避免 A 收到 B 局面的建议照着走错棋）。
// 会话不存在或未鉴权（已断开）时静默丢弃，返回是否已发送。
func (h *Hub) SendToSession(sessionID string, msg any) bool {
	h.mu.Lock()
	var target *Conn
	for c := range h.conns {
		if c.ID == sessionID && c.authed.Load() {
			target = c
			break
		}
	}
	h.mu.Unlock()
	if target == nil {
		h.log.Debug("定向发送目标不存在，丢弃", "session", sessionID)
		return false
	}
	if err := target.Send(msg); err != nil {
		h.log.Debug("定向发送失败", "session", target.ID, "err", err)
		return false
	}
	return true
}

// shortID 会话 ID 缩略（控制台显示用，前 8 hex + …）。
func shortID(id string) string {
	if len(id) > 8 {
		return id[:8] + "…"
	}
	return id
}

// newSessionID 生成 16 hex 字符的会话 ID。
func newSessionID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		// 随机源失败极罕见；退化用时间 ID，仅影响可读性不影响安全（令牌另走 crypto/rand）
		return fmt.Sprintf("%016x", time.Now().UnixMilli())
	}
	return hex.EncodeToString(b)
}
