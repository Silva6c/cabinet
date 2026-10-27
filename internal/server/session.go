package server

import (
	"errors"
	"net"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin:     func(*http.Request) bool { return true },
}

// serveWS 单连接生命周期：upgrade → PC 先发 hello → 鉴权窗口 → 主循环。
func (s *Server) serveWS(w http.ResponseWriter, r *http.Request) {
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		// Upgrade 失败时已自行写入 HTTP 错误响应
		return
	}
	c := NewConn(ws, s.log)
	s.hub.Register(c)
	defer func() {
		s.hub.Unregister(c)
		// 局面归属悬挂清理（2026-10-24 排查缺口）：ownerSession 记的是会话 ID
		// 快照，Unregister 只删 hub map——死会话 ID 悬挂期间引擎算出的建议全部
		// 在 SendToSession 静默丢弃，新会话连上后、发 position 前始终无建议可收
		// （"重启手机几次都收不到"的帮凶之一）。只清归属不清 lastFen：局面本
		// 身仍有效，新会话发 position 或 reSearchLatest 时照常用。
		if id, _ := s.ownerSession.Load().(string); id == c.ID {
			s.ownerSession.Store("")
		}
	}()
	defer func() { _ = c.Close() }()

	ws.SetReadLimit(MaxFrameSize)

	// 协议 §3（钉死）：连接建立后 PC 先发 hello
	if err := c.Send(Hello{frameBase: frame(TypeHello), Server: ServerName, Ver: Version, Caps: []string{CapV1}}); err != nil {
		c.log.Warn("发送服务器 hello 失败", "err", err)
		return
	}

	if !s.handshake(c, ws) {
		return
	}
	go s.pingLoop(c, ws) // PC 主动心跳测 RTT（协议 §4.7）；连接断开时 Send 失败自退
	s.readLoop(c, ws)
}

// pingLoop PC 侧心跳：每 15s 发一次 ping，客户端 pong 回执由 readLoop 记 RTT。
// 同时活跃的双向心跳让 45s 死链检测对"客户端不再上行"的链路也能感知。
func (s *Server) pingLoop(c *Conn, ws *websocket.Conn) {
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	for range t.C {
		if err := c.Send(Ping{frameBase: frame(TypePing), Ts: time.Now().UnixMilli()}); err != nil {
			return // 连接已断（服务关闭/对端离线）
		}
	}
}

// handshake 鉴权窗口：authTimeout 内完成 hello+auth；超时/令牌错误/版本不支持均断开。
// 返回是否成功建立会话。
//
// 独立于 readLoop 的原因：两套读超时制度不能混用——本循环挂 10s 硬窗口
// （逐帧不重置），鉴权通过后 readLoop 才以 45s 空闲超时接管。鉴权边界靠
// 循环隔离实现：未通过本循环的连接到不了任何业务 handler，无需在各
// handler 散布 Authed 检查；协议 §3 的帧白名单（hello/auth 以外一律
// auth_required）也只在本循环成立。
func (s *Server) handshake(c *Conn, ws *websocket.Conn) bool {
	// 未完成鉴权的连接一律挂 10s 读超时：超时未完成即踢出
	_ = ws.SetReadDeadline(time.Now().Add(s.authTimeout))
	for {
		_, data, err := ws.ReadMessage()
		if err != nil {
			// 鉴权窗口超时：按协议 §3 先回 error 再断开
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				_ = c.Send(ErrorFrame{frameBase: frame(TypeError), Code: CodeAuthRequired, Message: "握手超时，请在 10 秒内完成 hello + auth"})
				c.log.Warn("握手超时，踢出未鉴权连接")
			} else {
				c.log.Info("握手期间连接中断", "err", err)
			}
			return false
		}
		msg, perr := ParseFrame(data)
		if perr != nil {
			if errors.Is(perr, ErrUnknownType) {
				c.log.Debug("握手期间忽略未知类型帧", "frame", truncateForLog(data))
				continue
			}
			_ = c.Send(errorFrameFor(perr))
			if errors.Is(perr, ErrUnsupportedVer) {
				return false // 协议 §5：版本不支持必须断开
			}
			continue
		}
		switch m := msg.(type) {
		case *Hello:
			if m.Client != "" {
				c.Client, c.Ver = m.Client, m.Ver
				c.log.Info("客户端 hello", "client", m.Client, "ver", m.Ver, "caps", m.Caps)
			}
		case *Auth:
			if !ValidateToken(m.Token, s.cfg.Token) {
				_ = c.Send(ErrorFrame{frameBase: frame(TypeError), Code: CodeAuthFailed, Message: "配对令牌不正确"})
				c.log.Warn("鉴权失败：令牌不匹配")
				return false
			}
			c.authed.Store(true)
			_ = ws.SetReadDeadline(time.Time{}) // 鉴权完成，清除读超时
			// welcome 带上引擎当前算力（depth/hash_mb）：手机端按钮显示与循环
			// 起点从此与 PC 真实值一致（此前手机只能显示本地默认档，出现
			// "手机 0.25g / 电脑 2048"的脱节——2026-10-02 用户反馈）
			var engDepth, engHashMB int
			if tuner, ok := s.eng.(EngineTuner); ok {
				engDepth, engHashMB = tuner.Depth(), tuner.HashMB()
			}
			if err := c.Send(Welcome{
				frameBase: frame(TypeWelcome),
				SessionID: c.ID,
				Engine:    EngineState{State: s.EngineState(), Depth: engDepth, HashMB: engHashMB},
				Books:     s.bookNames(),
			}); err != nil {
				c.log.Warn("发送 welcome 失败", "err", err)
				return false
			}
			c.log.Info("鉴权通过，会话建立", "client", c.Client, "ver", c.Ver)
			return true
		default:
			// 握手窗口内的业务帧：提示但不踢，由 10s 窗口兜底（协议 §3）
			_ = c.Send(ErrorFrame{frameBase: frame(TypeError), Code: CodeAuthRequired, Message: "请先完成握手（hello + auth）"})
		}
	}
}

// readLoop 会话主循环：ping→pong、position→status（M0 echo 基准）、bye→bye。
// 死链检测：每帧后重置空闲读超时，连续 idleTimeout 未收到任何帧即断开（协议 §4.7）。
func (s *Server) readLoop(c *Conn, ws *websocket.Conn) {
	_ = ws.SetReadDeadline(time.Now().Add(s.idleTimeout))
	for {
		_, data, err := ws.ReadMessage()
		if err != nil {
			var ne net.Error
			switch {
			case errors.As(err, &ne) && ne.Timeout():
				c.log.Warn("空闲超时未收到任何帧，判定死链断开", "idle_timeout", s.idleTimeout)
			case errors.Is(err, net.ErrClosed) ||
				websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway):
				c.log.Info("连接关闭")
			default:
				c.log.Info("连接中断", "err", err)
			}
			return
		}
		_ = ws.SetReadDeadline(time.Now().Add(s.idleTimeout))
		msg, perr := ParseFrame(data)
		if perr != nil {
			if errors.Is(perr, ErrUnknownType) {
				c.log.Debug("忽略未知类型帧", "frame", truncateForLog(data))
				continue
			}
			_ = c.Send(errorFrameFor(perr))
			if errors.Is(perr, ErrUnsupportedVer) {
				return
			}
			continue
		}
		switch m := msg.(type) {
		case *Ping:
			_ = c.Send(Pong{frameBase: frame(TypePong), Ts: m.Ts})
		case *Pong:
			c.OnPong(m.Ts) // PC 主动 ping 的回执：记 RTT 供控制台显示
		case *Position:
			c.TouchPosition()
			s.handlePosition(c, m)
		case *SetEngine:
			s.handleSetEngine(c, m)
		case *Log:
			s.handleLogFrame(c, m)
		case *Bye:
			_ = c.Send(Bye{frameBase: frame(TypeBye)})
			c.log.Info("对端请求断开（bye）")
			return
		case *Hello, *Welcome, *Auth, *Suggestion, *Status, *ErrorFrame:
			c.log.Debug("忽略本端不应发送的帧", "type", frameTypeName(msg))
		}
	}
}

// bookNames 当前已启用开局库名列表（welcome.books 回填用）：无库/未加载返回
// 空切片（非 nil，JSON 序列化仍为 []）。此前恒硬编码空数组——库里 266 万条
// 记录 welcome 却报空，客户端无从展示库状态（第二轮审查 P1-3）。
