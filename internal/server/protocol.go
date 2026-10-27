// Package server 实现内阁 WSS 服务端：协议编解码、令牌鉴权、自签证书、连接管理。
// 协议以 docs/protocol-v1.md 为准，本文件中的消息结构与字段名逐字对应协议定稿。
package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// 协议常量（协议 v1，见 docs/protocol-v1.md §1/§2）
const (
	// ProtoVersion 当前协议版本，帧内 v 字段恒为此值
	ProtoVersion = 1
	// CapV1 v1 能力标识，hello 的 caps 数组内容
	CapV1 = "v1"
	// ServerName PC 端 hello 的 server 字段固定值
	ServerName = "cabinet"
	// ClientName 手机端 hello 的 client 字段约定值
	ClientName = "cabinet-phone"
	// MaxFrameSize 单帧上限（字节），超出直接断开
	MaxFrameSize = 64 << 10
)

// Version 内阁软件版本（hello 的 ver 字段）。
// 用 var 而非 const：正式构建由 scripts/build-exe.bat / Makefile 经
// -ldflags "-X cabinet/internal/server.Version=..." 注入真实版本
// （vc=git 提交数 + HEAD 构建日期）；源码态默认值仅供开发调试。
var Version = "0.1.0-dev"

// 消息类型（协议 §4，字段名逐字定死）
const (
	TypeHello      = "hello"
	TypeAuth       = "auth"
	TypeWelcome    = "welcome"
	TypePosition   = "position"
	TypeSuggestion = "suggestion"
	TypeStatus     = "status"
	TypePing       = "ping"
	TypePong       = "pong"
	TypeError      = "error"
	TypeBye        = "bye"
	TypeSetEngine  = "set_engine"
	TypeLog        = "log"
)

// 错误码（协议 §5，枚举定死）
const (
	CodeBadJSON        = "bad_json"
	CodeBadFrame       = "bad_frame"
	CodeUnsupportedVer = "unsupported_version"
	CodeAuthRequired   = "auth_required"
	CodeAuthFailed     = "auth_failed"
	CodeNotReady       = "not_ready"
	CodeRateLimited    = "rate_limited"
	CodeBadPosition    = "bad_position"
	CodeNoMoves        = "no_moves" // 引擎判定本局面无着可走（绝杀/困毙或识别拼出假局面）
	CodeInternal       = "internal"
)

// frameBase 所有帧共用的外层字段。作为嵌入 struct 放在各消息首字段，
// JSON 序列化时 v、t 展开在最前，与协议示例一致。
type frameBase struct {
	V int    `json:"v"`
	T string `json:"t"`
}

// frame 构造填好 v/t 的 frameBase。
func frame(t string) frameBase {
	return frameBase{V: ProtoVersion, T: t}
}

// ---- 消息结构（字段名与 docs/protocol-v1.md §4 逐字一致） ----
//
// ⚠任何字段增删/更名/omitempty 调整，必须先跑 TestSuggestionGoldenByteExact
// （protocol_bench_test.go 逐字节兼容锚）——手机端按当前字节形态上线，golden
// 变红即破坏旧端解析，属协议破坏性变更。

// Hello 握手帧（双向）。PC 端填 Server、手机端填 Client，二者互斥。
// Devid/Name 为手机端可选自报字段（2026-10-28 设备昵称功能）：omitempty 保证
// server 端 hello 序列化形态不变（TestEncodeFieldOrder 逐字节锚），旧端忽略。
type Hello struct {
	frameBase
	Client string   `json:"client,omitempty"`
	Server string   `json:"server,omitempty"`
	Ver    string   `json:"ver"`
	Caps   []string `json:"caps"`
	Devid  string   `json:"devid,omitempty"`
	Name   string   `json:"name,omitempty"`
}

// Auth 令牌鉴权帧（手机→PC）。
type Auth struct {
	frameBase
	Token string `json:"token"`
}

// EngineState welcome/status 中共用的引擎状态对象。Depth/HashMB 仅 welcome 填写：
// 手机端按钮显示的是 PC 实际算力值（2026-10-02 用户反馈"手机显示 0.25g、电脑显示
// 2048"——此前手机只能显示本地默认档，与 PC 脱节），status 帧不重复携带。
type EngineState struct {
	State  string `json:"state"`
	Depth  int    `json:"depth,omitempty"`
	HashMB int    `json:"hash_mb,omitempty"`
}

// Welcome 会话建立帧（PC→手机，auth 成功后）。
type Welcome struct {
	frameBase
	SessionID string      `json:"session_id"`
	Engine    EngineState `json:"engine"`
	// Books 刻意无 omitempty 且必须非 nil（协议 §4.3 必填）：nil 会序列化成
	// null，手机端按数组解析会炸；bookNames 保证空库也返回 []。
	// 勿为"统一风格"补 omitempty。
	Books []string `json:"books"`
}

// Position 局面上行帧（手机→PC）。History 为可选 UCCI 着法序列，M0/M1 只透传。
type Position struct {
	frameBase
	Fen     string   `json:"fen"`
	RedGo   bool     `json:"red_go"`
	History []string `json:"history,omitempty"`
	// Force 强制重新分析（手机端"更新"按钮置位）：跳过桔库/沉淀缓存直接进引擎
	// 重搜——否则同局面重发会被桔库命中短路，手机端刚调好的多路变招（MultiPV
	// 各线候选）拿不回来（2026-10-01 用户实测反馈"电脑变招手机没有跟上"）
	Force bool `json:"force,omitempty"`
	// NewGame 新局标记（手机端 BoardTracker NEW_GAME 确认后的首帧置位，2026-10-17
	// 复盘闭环）：PC 据此开启新 game_id，建议沉淀按局归组。可选字段旧端忽略。
	NewGame bool `json:"new_game,omitempty"`
}

// Score 红方统一视角的评分：CP 与 Mate 只出现其一（协议 §4.5）。
type Score struct {
	// CP 厘兵分，正=红方优
	CP *int `json:"cp,omitempty"`
	// Mate N 步将杀，正=红方将杀黑方，负=黑方将杀红方
	Mate *int `json:"mate,omitempty"`
}

// Suggestion 建议下行帧（PC→手机）。
type Suggestion struct {
	frameBase
	Source string `json:"source"`
	// Fen 本建议对应的局面（2026-10-02）：手机端据此丢弃迟到旧建议——旧建议
	// 对着新棋盘渲染会误报"轮次可能识别有误"并压掉箭头。
	Fen      string   `json:"fen,omitempty"`
	BestMove string   `json:"bestmove"`
	Score    *Score   `json:"score,omitempty"`
	PV       []string `json:"pv,omitempty"`
	Depth    int      `json:"depth,omitempty"`
	// WDL 胜/和/负概率（红方视角百分比；引擎未输出时缺省）。可选字段，
	// 旧端自动忽略（协议 §7）。
	WdlW *int `json:"wdl_w,omitempty"`
	WdlD *int `json:"wdl_d,omitempty"`
	WdlL *int `json:"wdl_l,omitempty"`
	// MultiPV 线序号（1=最优线；MultiPV=1 或沉淀命中时缺省）。
	// 引擎多线与桔库多候选（候选唤醒 2026-10-17：库内多候选逐条发帧）都按
	// 线序号标注；手机端据此按"每个线序号一个槽位"聚合：同序号新着法覆盖
	// 旧着法，掉出当前前 N 名的着法即被淘汰，候选总数恒等于 PC 当前报出的线数。
	Multipv int `json:"multipv,omitempty"`
	// Cached 该建议来自沉淀库缓存（同局面历史终值直接回放，未进引擎现算）。
	// 仅沉淀命中时置 true；引擎/桔库路径零值省略（omitempty），
	// 旧端自动忽略（协议 §7）。悬浮窗据此显示"缓存"弱标记。
	// 注意：保持 omitempty 形态，TestSuggestionGoldenByteExact 的逐字节
	// golden 锚样本中本字段恒为零值，锚不变。
	Cached bool `json:"cached,omitempty"`
}

// ScoreCP 取厘兵分（红方视角；无则 nil）。
func (m Suggestion) ScoreCP() *int {
	if m.Score == nil {
		return nil
	}
	return m.Score.CP
}

// ScoreMate 取将杀步数（正=红将杀黑；无则 nil）。
func (m Suggestion) ScoreMate() *int {
	if m.Score == nil {
		return nil
	}
	return m.Score.Mate
}

// EngineStatus status 帧中的引擎搜索指标（仅搜索中才有值）。
type EngineStatus struct {
	State    string `json:"state"`
	Depth    int    `json:"depth,omitempty"`
	Nodes    int64  `json:"nodes,omitempty"`
	NPS      int64  `json:"nps,omitempty"`
	Hashfull int    `json:"hashfull,omitempty"` // 千分比 0–1000
}

// Status 引擎状态帧（PC→手机），M0 用作 position 的链路确认回显。
type Status struct {
	frameBase
	Engine EngineStatus `json:"engine"`
	RTTMs  *int64       `json:"rtt_ms,omitempty"`
}

// Ping 心跳帧。Ts 为发送方 Unix 毫秒时间戳。
type Ping struct {
	frameBase
	Ts int64 `json:"ts"`
}

// Pong 心跳应答。Ts 必须原样回传收到的值，发送方以 now-ts 计算 RTT。
type Pong struct {
	frameBase
	Ts int64 `json:"ts"`
}

// ErrorFrame 错误帧。名为 ErrorFrame 以免与 error 类型混淆。
type ErrorFrame struct {
	frameBase
	Code    string `json:"code"`
	Message string `json:"message"`
	// Fen 触发本错误的局面回显（2026-10-02）：手机端只处理与当前局面一致的
	// 错误——迟到旧错误若不丢弃，会对新局面错翻轮次或误挂"识别异常"。
	Fen string `json:"fen,omitempty"`
	// TurnFlipLegal 仅 bad_position 使用：true=把走子方 w/b 翻转后该局面即通过
	// 引擎安全校验（被将军方必须行棋是唯一自洽解释，翻转即正确答案）。
	// 手机端据此自动纠正走子方并重发，无需用户点"更新"。
	TurnFlipLegal bool `json:"turn_flip_legal,omitempty"`
}

// Bye 优雅断开帧（双向）。
type Bye struct {
	frameBase
}

// SetEngine 远程调节引擎算力帧（手机→PC，协议 §4.10）。
// 全部字段可选（只改要改的字段），指针类型区分"未传"与零值——threads=0
// 是合法值（自动档）。范围校验（depth 1–40、hash_mb 0/32–32768、
// threads 0/1–64、multi_pv 1–5）在 server.handleSetEngine 做，任一字段
// 越界整帧拒绝。旧端忽略新增字段（协议 §7，无需升版本）。
type SetEngine struct {
	frameBase
	Depth   *int `json:"depth,omitempty"`
	HashMB  *int `json:"hash_mb,omitempty"`
	Threads *int `json:"threads,omitempty"`
	MultiPV *int `json:"multi_pv,omitempty"`
}

// Log 手机端诊断日志上行帧（手机→PC，协议 §4.11）。
// Lines 为手机端预格式化的日志行（时间戳/级别/tag/消息已在端上拼好），
// PC 端按接收顺序原样落盘到 logs/phone/ 指定文件夹，供真机问题排查。
// 纯诊断辅助：解析失败/缺字段不回错误帧（与日志的宽容语义一致）。
type Log struct {
	frameBase
	Lines []string `json:"lines"`
}

// ---- 编解码 ----

// ErrUnknownType 表示帧类型不在协议 v1 枚举内：按协议应静默忽略（记日志）。
var ErrUnknownType = errors.New("未知消息类型")

// ErrBadJSON / ErrBadFrame / ErrUnsupportedVersion 与协议错误码一一对应，
// 由调用方决定回 error 帧与是否断开。
var (
	ErrBadJSON        = errors.New(CodeBadJSON)
	ErrBadFrame       = errors.New(CodeBadFrame)
	ErrUnsupportedVer = errors.New(CodeUnsupportedVer)
)

// DecodeFrame 拆出帧头（v/t）。raw 是完整原始 JSON，供二次解码到具体消息结构。
// 校验规则（协议 §2）：非法 JSON → ErrBadJSON；缺 t → ErrBadFrame；v 缺失或 ≠1 → ErrUnsupportedVer。
//
// 先头后体两段解析：帧级校验（JSON 合法、有 t、v=1）只看外层即可全部完成，
// 错误码判定顺序与协议 §2/§5 一一对应，之后才按 t 选结构二次解码。raw 即
// data 本身（RawMessage 零拷贝别名），二次解码侧不得修改 data。
func DecodeFrame(data []byte) (head frameBase, raw json.RawMessage, err error) {
	if err := json.Unmarshal(data, &head); err != nil {
		return frameBase{}, nil, fmt.Errorf("%w: %v", ErrBadJSON, err)
	}
	if head.T == "" {
		return frameBase{}, nil, fmt.Errorf("%w: 缺少 t 字段", ErrBadFrame)
	}
	if head.V != ProtoVersion {
		return frameBase{}, nil, fmt.Errorf("%w: v=%d", ErrUnsupportedVer, head.V)
	}
	return head, json.RawMessage(data), nil
}

// frameFactories 帧类型注册表：类型字符串 → 空消息构造器。
// 新增消息类型在此注册一行，ParseFrame 主体不动；golden 兼容锚测试
// （protocol_bench_test.go）守护字段序列化形态。
var frameFactories = map[string]func() any{
	TypeHello:      func() any { return &Hello{} },
	TypeAuth:       func() any { return &Auth{} },
	TypeWelcome:    func() any { return &Welcome{} },
	TypePosition:   func() any { return &Position{} },
	TypeSuggestion: func() any { return &Suggestion{} },
	TypeStatus:     func() any { return &Status{} },
	TypePing:       func() any { return &Ping{} },
	TypePong:       func() any { return &Pong{} },
	TypeError:      func() any { return &ErrorFrame{} },
	TypeBye:        func() any { return &Bye{} },
	TypeSetEngine:  func() any { return &SetEngine{} },
	TypeLog:        func() any { return &Log{} },
}

// ParseFrame 解码一帧并返回对应消息结构指针；类型未知时返回 ErrUnknownType，
// 由调用方按协议静默忽略。字段级错误（缺必填等）由各调用方按需校验，此处不深查，
// 与"旧端忽略未知字段"的宽容反序列化语义一致。
func ParseFrame(data []byte) (any, error) {
	head, raw, err := DecodeFrame(data)
	if err != nil {
		return nil, err
	}
	newMsg, ok := frameFactories[head.T]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownType, head.T)
	}
	m := newMsg()
	return m, json.Unmarshal(raw, m)
}

// sendJSON 在写锁保护下发送一条消息帧（gorilla 连接不允许多 gorilla 并发写）。
// 任何消息结构体的 frameBase 都已通过构造赋值填好 v/t。
func sendJSON(c *websocket.Conn, mu *sync.Mutex, msg any) error {
	b, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("序列化帧失败: %w", err)
	}
	mu.Lock()
	defer mu.Unlock()
	return writeFrame(c, b)
}

// sendRaw 在写锁保护下直接发送已序列化的消息字节。广播场景 marshal 一次
// 复用（结构体序列化是确定性的，N 台设备收到的字节与逐个 marshal 完全一致）。
func sendRaw(c *websocket.Conn, mu *sync.Mutex, b []byte) error {
	mu.Lock()
	defer mu.Unlock()
	return writeFrame(c, b)
}

// writeTimeout 应用层帧写超时：gorilla 写默认零 deadline=永不超时，慢/半死
// 客户端（TCP 黑洞）会无限阻塞 enginePump 单 goroutine——所有设备的建议
// 全停且引擎事件被静默丢弃（这是"建议突然不来"的历史根因候选）。3 秒覆盖
// 移动网络抖动，又不至于卡泵太久。
const writeTimeout = 3 * time.Second

// writeFrame 统一写收口（调用方须持有连接写锁）：每次写前设超时；写失败
// （超时或连接坏）即关闭连接——readLoop 走正常错误分支注销，手机端按既有
// 指数退避自动重连。没有这一步，超时后连接仍挂着，泵每帧仍要等满 3 秒。
func writeFrame(c *websocket.Conn, b []byte) error {
	if err := c.SetWriteDeadline(time.Now().Add(writeTimeout)); err != nil {
		_ = c.Close() // 设 deadline 失败说明连接已坏，关闭无害（幂等）
		return err
	}
	if err := c.WriteMessage(websocket.TextMessage, b); err != nil {
		_ = c.Close()
		return err
	}
	return nil
}
