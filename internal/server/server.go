package server

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"cabinet/internal/book"
	"cabinet/internal/config"
	"cabinet/internal/engine"
)

// EngineBridge server 依赖的引擎能力小接口（engine.Engine 天然满足；
// 独立成接口便于集成测试注入假引擎）。
type EngineBridge interface {
	Events() <-chan engine.Event
	Request(fen string)
	State() string
}

// EngineTuner 运行中可调引擎算力（协议 §4.10 set_engine；engine.Engine 实现）。
// 独立小接口：无此能力的引擎桥（测试桩/无引擎模式）收到 set_engine 时回 not_ready。
type EngineTuner interface {
	SetDepth(d int) bool
	SetHashMB(mb int) bool
	SetThreads(n int) bool
	SetMultiPV(n int) bool
	Depth() int
	HashMB() int
	Threads() int
	MultiPV() int
	// ClearHash 请求引擎清空置换表（皮卡鱼 Clear Hash button 选项）；
	// 在下一轮搜索开始前的空闲窗口执行。
	ClearHash()
}

// Server 内阁 WSS 服务端：握手 + 鉴权 + 引擎桥 + 桔库集成。
type Server struct {
	cfg  *config.Config
	log  *slog.Logger
	hub  *Hub
	cert tls.Certificate
	eng  EngineBridge // 可为 nil（引擎未启用，退化为 M0 echo 行为）

	// book 当前桔库集合（atomic：热加载可随时整体替换）
	book atomic.Value // *book.Store

	// onSuggestion 建议下发回调（沉淀用；nil 忽略）。在建议下发后同步调用。
	onSuggestion func(rec SuggestionRecord)

	// sedimentLookup 沉淀库查询回调（nil = 未启用）：桔库未命中后查历史建议，
	// 同局面直接复用引擎终值，省一次数秒级重搜。
	sedimentLookup func(fen string) (SuggestionRecord, bool)

	// authTimeout 鉴权握手窗口，协议定死 10s（协议 §3）；测试注入短值加速
	authTimeout time.Duration
	// idleTimeout 会话期死链检测：连续这么久未收到客户端任何帧即断开，
	// 窗口期内纯 ping/pong 也算活动
	// 协议 §4.7 定 45s（3 个心跳周期）；测试注入短值加速
	idleTimeout time.Duration
	// phoneLogMu 手机日志落盘互斥（多连接并发收 log 帧时串行化文件追加）
	phoneLogMu sync.Mutex
	// phoneLogPath/phoneLogSeq 单局分卷落盘：当前局文件路径与局号（进程内递增），
	// 收到手机端"新局开始"分界行即切换下一局文件（一局一文件，检索按局定位）
	phoneLogPath string
	phoneLogSeq  int
	// lastFen 最近一次客户端 position 的 fen（"最新覆盖"：结果按 FEN 匹配下发）
	lastFen atomic.Value // string
	// ownerSession 当前局面的归属连接（最近一次有效 position 的会话 ID）：
	// suggestion 只定向发给它（协议 §4.5 建议路由），多设备互不串台
	ownerSession atomic.Value // string
	// engMetrics 最近一次广播的引擎指标（控制台快照读同一份；收束时指标清零）
	engMetrics atomic.Value // EngineStatus
	// pendingSug 采纳比对锚点（最后一条已下发建议的下发快照）：写入侧为
	// enginePump（引擎建议）与 readLoop（桔库/沉淀命中分支）两个 goroutine，
	// 读侧 handlePosition——atomic.Value 存不可变 *pendingAdvice 快照，无锁
	pendingSug atomic.Value // *pendingAdvice
	// currentGameID 当前对局 ID（复盘闭环按局归组）：手机 NEW_GAME 首帧触发
	// 重新生成；沉淀落库时随条带出。atomic 存 string
	currentGameID atomic.Value // string
	// onAdoption 采纳行为回调（SetAdoptionSink 注册；与 SetSuggestionSink 同款
	// "必须在 go srv.Run 之前注册"的无锁字段契约）
	onAdoption func(rec AdoptionRecord)
	// onTunePersist 算力参数落盘回调（SetTunePersist 注册，nil = 不落盘）
	onTunePersist func(kind string, v int)
}

// pendingAdvice 采纳比对锚点快照：某一下发局面已报出的候选集（着法 → 槽位，
// 1=主选）。三条下发路径（桔库/沉淀/引擎）的帧在此汇聚累计——引擎 MultiPV
// 多线逐帧到、桔库候选唤醒逐帧到，锚点按局面归组，局面切换即重建。
type pendingAdvice struct {
	fen       string
	moves     map[string]int
	source    string
	depth     int
	scoreCP   *int
	scoreMate *int
}

// SuggestionRecord 一条已下发建议的沉淀记录（store 落库用）。
type SuggestionRecord struct {
	Source    string
	FEN       string
	BestMove  string
	ScoreCP   *int
	ScoreMate *int
	PV        []string
	Depth     int
	GameID    string // 所属对局（复盘按局归组；空 = 无局上下文的旧形态记录）
}

// SetBook 热替换桔库集合（热加载完成后调用；传 nil 表示清空）。
func (s *Server) SetBook(store *book.Store) {
	s.book.Store(store)
}

// SetSuggestionSink 注册建议沉淀回调。
func (s *Server) SetSuggestionSink(fn func(rec SuggestionRecord)) {
	s.onSuggestion = fn
}

// AdoptionRecord 一条采纳行为事件（store 落库用；与 SuggestionRecord 同款
// "server 定义记录、main 桥接转换"模式）。MatchKind 口径见 store.AdoptionRec。
type AdoptionRecord struct {
	FEN       string
	Suggested string
	Actual    string
	MatchKind string // primary / candidate / miss
	CandRank  int
	Source    string
	Depth     int
	ScoreCP   *int
	ScoreMate *int
}

// SetAdoptionSink 注册采纳行为回调（nil = 不统计；须在 go srv.Run 之前注册）。
func (s *Server) SetAdoptionSink(fn func(rec AdoptionRecord)) {
	s.onAdoption = fn
}

// SetTunePersist 注册算力参数落盘回调（2026-10-18）：TuneEngine 校验并应用
// 成功后调用（kind ∈ depth/hash/threads/multipv，v 为已应用的值）——实现侧
// 把值写回配置文件，重启后保持用户上次的选择（悬浮窗"哈希"按钮/面板下拉
// 两条链路都经 TuneEngine，天然全覆盖）。nil = 不落盘（重启回默认值）。
// ⚠ 回调在请求 goroutine 内同步执行，实现侧必须轻（写小 toml 可接受，
// 禁止做重 IO）；须在 go srv.Run 之前注册（与 SetSuggestionSink 同款契约）。
func (s *Server) SetTunePersist(fn func(kind string, v int)) {
	s.onTunePersist = fn
}

// SetSedimentLookup 注册沉淀库查询回调（main 从 store 适配注入；nil = 未启用）。
func (s *Server) SetSedimentLookup(fn func(fen string) (SuggestionRecord, bool)) {
	s.sedimentLookup = fn
}

// New 创建服务端实例。eng 可为 nil（M0 echo 行为）。
func New(cfg *config.Config, cert tls.Certificate, logger *slog.Logger, eng EngineBridge) *Server {
	s := &Server{
		cfg:         cfg,
		log:         logger,
		hub:         NewHub(logger),
		cert:        cert,
		eng:         eng,
		authTimeout: 10 * time.Second,
		idleTimeout: 45 * time.Second,
	}
	return s
}

// EngineState 当前引擎状态（单一事实来源：引擎桥非 nil 时直接查引擎）。
func (s *Server) EngineState() string {
	if s.eng != nil {
		return s.eng.State()
	}
	return engine.StateOffline
}

// Clients 当前活跃连接数（托盘状态行用）。
func (s *Server) Clients() int { return s.hub.Count() }

// ConsoleKick 控制台手动断开指定会话（管理操作）：先 bye 后优雅关闭，
// 手机端按断线处理自动重连（不是拉黑——令牌有效随时可再连）。
// 返回会话是否存在（false 通常是已自行断开，控制台刷新后列表即同步）。
func (s *Server) ConsoleKick(sessionID string) bool {
	return s.hub.Disconnect(sessionID, "控制台手动断开")
}

// Handler 返回 HTTP 路由：/ws 为 WSS 端点，/healthz 供存活探测。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/ws", s.serveWS)
	return mux
}

// Run 启动 WSS 服务直至 ctx 取消，优雅关闭（含踢掉全部活跃连接）。
func (s *Server) Run(ctx context.Context) error {
	if s.cfg.Token == "" {
		return errors.New("配置 token 为空，拒绝无令牌运行（可用 cabinet -gen-token 生成后写入配置）")
	}
	// 引擎事件泵：按协议 §4.5/§4.6 节奏把引擎事件转为 suggestion/status 下发
	if s.eng != nil {
		go s.enginePump(ctx)
	}
	hs := &http.Server{
		Addr:    s.cfg.Bind,
		Handler: s.Handler(),
		TLSConfig: &tls.Config{
			Certificates: []tls.Certificate{s.cert},
			MinVersion:   tls.VersionTLS12,
		},
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		s.log.Info("WSS 服务启动", "addr", s.cfg.Bind)
		errCh <- hs.ListenAndServeTLS("", "") // 证书已注入 TLSConfig，参数留空
	}()

	select {
	case <-ctx.Done():
		s.log.Info("收到退出信号，正在关闭服务")
		s.hub.CloseAll()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := hs.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("优雅关闭失败: %w", err)
		}
		return nil
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("HTTP 服务异常退出: %w", err)
	}
}

// upgrader 升级器。自签证书 + 应用层令牌鉴权下 Origin 校验对非浏览器端无意义，
// 全部放行；M5 安全加固再评估。
