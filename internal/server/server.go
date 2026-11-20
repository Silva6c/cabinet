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
// 独立成接口便于集成测试注入假引擎）。快照制式：Request 只收终局面。
type EngineBridge interface {
	Events() <-chan engine.Event
	Request(fen string)
	State() string
}

// HistoryEngine 可选的带历史搜索能力（engine.Engine 实现；测试桩可不实现，
// server 按能力断言回退快照制式）。curFen=终局面（事件回推口径），UCI 命令
// 由实现方拼为 `position fen <startFen> moves <moves...>`（vc27 事故教训：
// 终局面当起点 + 历史当序列 = 非法命令，皮卡鱼直接终止进程）。
type HistoryEngine interface {
	RequestHistory(curFen, startFen string, moves []string)
}

// MultiPVOnceEngine 可选的单次多线搜索能力（engine.Engine 实现；测试桩可不
// 实现，server 按能力断言回退常规请求）。协议 §4.4 position.multi_pv_once
// （2026-11-09 双击小棋盘"单次三变解析"）：本条局面以指定线数搜索且仅此
// 一轮——不改引擎配置档位（"路数"持久值不受扰）。与带历史正交：重复风险
// 存在时两能力可叠加（带历史的多线请求）。
type MultiPVOnceEngine interface {
	RequestMultiPV(fen string, pv int)
	RequestHistoryMultiPV(curFen, startFen string, moves []string, pv int)
}

// EngineTuner 运行中可调引擎算力（协议 §4.10 set_engine；engine.Engine 实现）。
// 独立小接口：无此能力的引擎桥（测试桩/无引擎模式）收到 set_engine 时回 not_ready。
type EngineTuner interface {
	SetDepth(d int) bool
	SetMoveTimeMS(ms int) bool // 时间制搜索上限（0 = 深度制，500–20000 = go movetime）
	SetHashMB(mb int) bool
	SetThreads(n int) bool
	SetMultiPV(n int) bool
	SetSmart(on bool) // 智能思考（2026-11-02，仅 PC 面板热切；机制见 engine/smart.go）
	Depth() int
	MoveTimeMS() int
	HashMB() int
	Threads() int
	MultiPV() int
	Smart() bool
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
	// phoneLogs 每会话当前落盘文件（key=会话 ID；2026-11-07 重构）：
	// 此前是 Server 级单路径，新会话接入不触发切换——断线重连后的日志
	// 全部追加进旧文件，文件名日期停留在几天前误导检索；两台手机同时
	// 连接时还共用一个文件。按会话分文件后各局各设备独立成卷。
	// 会话注销（hub Unregister）时清条目，防长跑进程表无限增长。
	phoneLogs map[string]string
	// phoneLogSeq 局号（进程内递增，重启从 1 重计，见 phoneLogFileName）
	phoneLogSeq int
	// histMu/hist 局面历史（2026-11-07 防三次重复判和，per-session）：
	// 攒集/消费/生命周期见 history.go 包头注释
	histMu sync.Mutex
	hist   map[string]*sessionHistory
	// lastFen 最近一次客户端 position 的 fen（"最新覆盖"：结果按 FEN 匹配下发）
	lastFen atomic.Value // string
	// gateRejectPrev/gateRejectCount 结构门自愈放行状态（2026-11-12 vc19 流畅度
	// 治理）：同一 prev FEN 的连续拒绝计数。第 1 次拒回 bad_frame 给手机自愈重探；
	// 第 2 次拒同一 prev = prev 大概率是识别坏盘面/force 污染残留，继续拒只会
	// 死锁（lastFen 停在坏局面，建议断供实测 4.4~27.1s 全靠用户手动"更新"）——
	// 按识别自愈放行，盘面仍过 FENHashValidated/PositionLegal 兜底（见
	// handlePosition 结构门块）。force 帧接受时预置 count=1（force 盘面绕过
	// 结构门可信度未知，下一帧矛盾即达阈值免费自纠，治理 #7/#12 残缺盘面死锁）。
	// 竞态说明：与 lastFen 同为全局单值，多设备并发 position 的错轮争用后果
	// 仅为自愈提前/滞后一帧，无害。
	gateRejectPrev  atomic.Value // string
	gateRejectCount atomic.Int32
	// ownerSession 当前局面的归属连接（最近一次有效 position 的会话 ID）：
	// suggestion 只定向发给它（协议 §4.5 建议路由），多设备互不串台
	ownerSession atomic.Value // string
	// ownerDev 当前局面归属设备的走势键（devKeyOf，与 ownerSession 同点更新）：
	// 走势序列按设备分组后的"焦点设备"= 默认视图；断开连接时**故意不清**——
	// 同一设备重连（新会话）曲线要续上，焦点只随下一条 position 切换
	ownerDev atomic.Value // string
	// engMetrics 最近一次广播的引擎指标（控制台快照读同一份；收束时指标清零）
	engMetrics atomic.Value // EngineStatus
	// pendingSug 采纳比对锚点（最后一条已下发建议的下发快照）：写入侧为
	// enginePump（引擎建议）、各连接 readLoop（桔库/沉淀命中）与云库 goroutine
	//（cloudbook.go），读清侧 handlePosition（evaluateAdoption）——atomic.Value
	// 存不可变 *pendingAdvice 快照。⚠锁契约：所有"load-改-store"与清锚必须持
	// pendingSugMu——原子位只保证单次读写的原子性，护不住 read-modify-write
	// 序列，无锁时并发写者互相整体覆盖（候选集丢条目、rank=1 快照互相抢）。
	pendingSugMu sync.Mutex
	pendingSug   atomic.Value // *pendingAdvice
	// currentGameID 当前对局 ID（复盘闭环按局归组）：手机 NEW_GAME 首帧触发
	// 重新生成；沉淀落库时随条带出。atomic 存 string
	currentGameID atomic.Value // string
	// onAdoption 采纳行为回调（SetAdoptionSink 注册；与 SetSuggestionSink 同款
	// "必须在 go srv.Run 之前注册"的无锁字段契约）
	onAdoption func(rec AdoptionRecord)
	// onTunePersist 算力参数落盘回调（SetTunePersist 注册，nil = 不落盘）
	onTunePersist func(kind string, v int)
	// 走势序列（trend.go，2026-10-30 设备分组）：每设备一条终态分序列——键 =
	// devKeyOf(c)（devid 优先、旧手机回退会话 ID），焦点设备由 ownerDev 指示。
	// enginePump 追加（按归属设备）/ readLoop（new_game）组内重置 / 控制台快照
	// 读——多路并发，trendMu 保护。分组模型与取舍见 trend.go 文件头。
	trendMu     sync.Mutex
	trendGroups map[string]*trendSeries
	// 云库状态（cloudbook.go）：FEN 缓存（含未命中）+ 限流时间戳；
	// cloudEnable 运行时开关（PC 面板切换，SetCloudBookEnable）——配置结构体
	// 的 Enable 只作启动种子与落盘持久化，查询热路径只读本原子位（并发干净）
	cloudMu     sync.Mutex
	cloudCache  map[string]cloudEntry
	lastCloudMs atomic.Int64
	cloudEnable atomic.Bool
	// cloudFirst 默认走云库（2026-10-30"云库优先"开关）：开启后局面先查云库，
	// 命中即终（不查本地不进引擎），未命中回落常规链——语义矩阵见
	// cloudLookupFirst。种子同 cloudEnable，运行时以本原子位为准
	cloudFirst atomic.Bool
	// skipBooks 不走任何库（2026-10-30）：开启后建议链跳过全部库——桔库、
	// 沉淀、云库（含 cloud_first）都不查，每步引擎直搜；只停"查"不停"记"
	// （沉淀照常写入）。优先级高于 cloudFirst：handlePosition 在 cloudLookupFirst
	// 之前拦截。种子注入 New，运行时以本原子位为准
	skipBooks atomic.Bool
	// endgameEnable 残局库查询开关（2026-11-01，cloudbook.go）：独立于
	// cloudEnable——开启后对本地库/沉淀未命中的局面另发 action=query&endgame=1
	// 只查云库残局库（穷举绝对正解）。种子注入 New，运行时以本原子位为准
	endgameEnable atomic.Bool
	// cloudEndpoint/cloudAPIKey 云库端点与 key 的构造期快照：UI 回调只切布尔
	// 开关（原子位），端点/key 无运行期改口——快照成 Server 字段后查询热路径
	// 对 cfg.CloudBook 零读取，杜绝 UI 写布尔位与 WSS goroutine 整结构体拷贝
	// 之间的数据竞争（2026-11-04 审查修复）。改端点走 toml + 重启
	cloudEndpoint string
	cloudAPIKey   string
	// lastEndgameMs 残局查询限流时间戳：与开局查询（lastCloudMs）分表——两者
	// 是不同 action 的独立请求，共享限流会让"每局面先 queryall 占位"把残局
	// 查询永久饿死；各自 1.5s 对公共端点总频率 ≤2次/1.5s，礼貌性可接受
	lastEndgameMs atomic.Int64
	// posGen 局面代次（2026-11-01 显示错位根修）：handlePosition 每接受一条
	// 新局面递增；云库/残局异步回调下发前校验代次未变——同 FEN force 重发
	// （桔库换库后重分析）时 lastFen 校验拦不住的旧异步帧（库命中已定着、
	// 在途云帧仍覆盖显示"云库"）被代次拦下
	posGen atomic.Uint64
	// lastPositionAt 最近一次收到局面的时刻（UnixMilli；0 = 从未）。供
	// memwatch 动态 GC 档位的活跃信号（2026-11-09：对局期不得滞留低 GC
	// 档位，见 memwatch.ActiveProbe）
	lastPositionAt atomic.Int64
	// repAvoidFen/repAvoidGen 引擎终值三次重复规避状态机（2026-11-15 实战
	// 七问；enginePump 单 goroutine 专用字段，无并发访问）：终值着法将判和时
	// 先照发 rep_risk 警示，替代重搜按 2026-11-17 用户拍板改惰性触发（挂起
	// repAvoidDelayMs，停留窗口到点才发起；期间任何新局面/清障路径立即作废）。
	// 绑定局面与代次（posGen 快照）——同局面且代次未变的结果才进替代消费
	// 分支；任何失效路径（结果被最新覆盖丢弃/他局面终值/引擎状态变化）立即
	// 清零，防残留把普通终值误当替代消费（红队致命项修订：裸 FEN 键无清障
	// 会把状态泄漏到下一次触发）
	repAvoidFen string
	repAvoidGen uint64
	// repAvoidSid/repAvoidDue 惰性触发的挂起窗口（同上，pump 专用）：Sid 记
	// 触发时会话归属（到点重搜取历史链用）；Due 为 nil 时 pump select 的到点
	// 分支永不就绪（nil channel 天然禁用），置非 nil 即挂起、clearRepAvoid
	// 置 nil 即停表——到点回调仍在途也无害，fire 侧再按 fen/gen/lastFen 三重
	// 校验后才真正发起重搜
	repAvoidSid string
	repAvoidDue <-chan time.Time
	// 本轮搜索起始时刻（UnixNano，0 = 空闲）：时间制仪表"用时/上限"的数据源。
	// enginePump 在最新局面的首条 info 时起表、任意 bestmove/状态变化时停表——
	// 计在服务端而非引擎包，避免为展示指标动 EngineBridge 接口（测试桩全要跟着改）
	searchStartNanos atomic.Int64
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
		cloudCache:  map[string]cloudEntry{},
		authTimeout: 10 * time.Second,
		idleTimeout: 45 * time.Second,
	}
	s.cloudEnable.Store(cfg.CloudBook.Enable) // 面板开关的启动种子（cfg 值仅此一次进入）
	s.cloudFirst.Store(cfg.CloudBook.CloudFirst)
	s.endgameEnable.Store(cfg.CloudBook.Endgame)
	// 端点/key 构造期快照（运行期只读，见字段注释）：cfg.CloudBook 从此零并发读取
	s.cloudEndpoint = cfg.CloudBook.Endpoint
	s.cloudAPIKey = cfg.CloudBook.APIKey
	s.skipBooks.Store(cfg.Engine.SkipBooks)
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
