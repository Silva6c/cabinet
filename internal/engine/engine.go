// Package engine 是皮卡鱼 UCI 桥：常驻子进程 + 看门狗重启 + 最新覆盖搜索。
// 交互逻辑移植自手机端 reference/AnalysisEngine.kt（经真机验证的握手/打断制式）。
package engine

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Options 引擎配置。
//
// ⚠【雷区】Threads/HashMB/MultiPV/Depth 是用户实测钉死的算力参数（红线，
// 见 internal/config EngineConfig 注释）：未经用户明确要求严禁改默认值。
type Options struct {
	Path       string // 皮卡鱼 exe 路径
	NNUE       string // pikafish.nnue 绝对路径（EvalFile）
	Threads    int    // 0 = 自动（逻辑处理器数的一半）
	HashMB     int    // 0 = 自动（defaultHashMB，2GiB）
	MultiPV    int
	Depth      int          // 固定深度制搜索目标
	MoveTimeMS int          // 时间制搜索上限（毫秒）；>0 时优先于深度生效，0 = 按深度
	Log        *slog.Logger // 子进程 stderr 落日志用（New 填充；测试 fake 可空）
}

// 默认值兜底
func (o Options) withDefaults() Options {
	if o.Threads <= 0 {
		o.Threads = autoThreads()
	}
	if o.HashMB <= 0 {
		o.HashMB = defaultHashMB
	}
	if o.MultiPV <= 0 {
		// 默认单线：MultiPV>1 分散根节点搜索，同深度棋力明显下降（变招是
		// 用户显式开启的功能，引擎兜底默认必须是最强形态）
		o.MultiPV = 1
	}
	if o.Depth <= 0 {
		o.Depth = 20
	}
	if o.MoveTimeMS < 0 {
		o.MoveTimeMS = 0
	}
	return o
}

// autoThreads 自动线程数：逻辑处理器数的一半。引擎全力吃满（官方"最大值减 1~2"）
// 会让整机风扇起飞、其他程序（模拟器/浏览器/IDE）明显卡顿，故默认留一半核给系统，
// 追求极限的用户可在面板/手机端手动调高，下限 1。
func autoThreads() int {
	n := runtime.NumCPU() / 2
	if n < 1 {
		n = 1
	}
	return n
}

// defaultHashMB 自动档置换表大小（MB）：2GiB 对中残局足够（fill 率低，
// 更大收益边际递减），同时为系统与共存程序留足内存。
const defaultHashMB = 2048

// 事件种类：状态变化 / 搜索中信息更新 / 搜索收束（bestmove）/ 引擎危急报错
type EventKind int

const (
	EventState EventKind = iota
	EventInfo
	EventBestmove
	EventCritical
)

// Event 引擎事件，server 的事件泵按协议 §4.5/§4.6 节奏转为 suggestion/status 下发。
// Kind=EventInfo 时 Fen 为该次搜索的请求局面——info 行本身不带局面信息，
// server 靠它与"最新请求 FEN"比对过滤（预热/被打断搜索的中间 info 不得
// 冒充最新请求的结果，协议 §4.5 "最新覆盖"语义）。
type Event struct {
	Kind    EventKind
	State   string  // Kind=EventState：loading/ready/error
	Fen     string  // Kind=EventInfo/EventCritical：本次搜索的请求局面
	Info    *Info   // Kind=EventInfo：当前搜索快照（走子方原生视角）
	Result  *Result // Kind=EventBestmove
	Message string  // Kind=EventCritical：引擎原始报错全文（info string 行）
}

// Result 一次搜索的终态。
type Result struct {
	Fen      string
	RedGo    bool
	BestMove string // UCCI 着法；无着可走时为 "null"
	Lines    []Info // 按 MultiPV 升序
}

// Info 一条搜索线快照。ScoreCP/Mate 为皮卡鱼原生输出（走子方视角），
// 下发前必须经 ToRedScore 换算为红方视角（协议 §4.5）。
type Info struct {
	MultiPV  int
	Depth    int
	ScoreCP  int
	Mate     int // 0 = 非杀棋；正 = 走子方 N 步将杀
	HasMate  bool
	PV       []string
	Nodes    int64
	NPS      int64
	Hashfull int // 千分比 0-1000
	// WDL 胜/和/负概率（UCI_ShowWDL 开启时输出，走子方视角千分比，
	// W+D+L=1000）；HasWdl=false 表示该行未携带。下发前经 ToRedWdl 换算。
	WdlW, WdlD, WdlL int
	HasWdl           bool
}

// 引擎状态（与协议 engine.state 枚举一致）
const (
	StateOffline = "offline" // 引擎桥未启用
	StateLoading = "loading"
	StateReady   = "ready"
	StateError   = "error"
)

// errReadTimeout 限时读在窗口内未等到输出。必须与 io.EOF（输出流关闭=进程
// 退出）严格区分：EOF 走"进程异常→看门狗重启"既有路径；本错误用于"引擎
// 仍在运行但无响应（挂死）"的主动收场判定（collect 第二超时）。
var errReadTimeout = errors.New("限时读超时")

// proc 抽象引擎子进程，测试注入 fake UCI 引擎用。
// 读侧为常驻 reader goroutine + channel：支持限时读——所有带 deadline 的读
// 循环（握手/collect）统一走 ReadLineTimeout，无输出时也能回到 deadline/ctx
// 检查点（阻塞读会让兜底超时永远到不了判定点）。
type proc interface {
	WriteLine(line string) error
	ReadLine() (string, error) // 阻塞读；进程输出流关闭返回错误
	// ReadLineTimeout 限时读一行：d 内无输出返回 errReadTimeout，输出流关闭
	// 返回 io.EOF（两者可区分，见 errReadTimeout 注释）。
	ReadLineTimeout(d time.Duration) (string, error)
	Quit() error // 发 quit 并等待退出
}

// procStarter 启动一个引擎进程。包级变量以便测试替换。
var procStarter = startRealProc

// Engine 皮卡鱼常驻桥。
type Engine struct {
	opts Options
	log  *slog.Logger

	events    chan Event   // 事件流
	pending   chan string  // 最新覆盖请求槽（容量 1，满了替换）
	state     atomic.Value // string: loading/ready/error
	searching atomic.Bool  // 是否在搜索中（Request 据此决定是否发 stop）
	preempt   atomic.Bool  // 外部打断标志：Request 发 stop 时置位，collect 收束时消费
	cur       atomic.Value // proc：当前进程句柄（stop 打断跨线程用）

	// 运行中可调算力（set_engine 远程调节 / 桌面面板调节，协议 §4.10）：
	// depth/moveTimeMS 每轮 go 直接读取（moveTimeMS>0 时时间制优先）；threads/
	// hashMB/multiPV 为目标值，与 applied* 不同时在下一轮搜索开始前（UCI 空闲
	// 窗口）按 Threads→Hash→MultiPV 顺序应用（官方要求 Threads 先于 Hash 设置）。
	depth          atomic.Int64
	moveTimeMS     atomic.Int64 // >0 = 时间制（go movetime），0 = 深度制（go depth）
	tuneMu         sync.Mutex
	threads        int
	appliedThreads int
	hashMB         int
	appliedHash    int
	multiPV        int
	appliedMultiPV int
	pendingClear   bool // Clear Hash 按钮请求（下一轮搜索前或空闲冲刷时执行）
	tuneNotify     chan struct{}
	// 调参待发信号（容量 1，非阻塞发送）：searchLoop 空闲等待时收到即冲刷
	// setoption——此前参数只能搭"下一轮搜索"的车，引擎刚启动、手机从未连接时
	// 没有任何搜索可搭，调 hash 引擎毫无动作（2026-10-20 用户实测：任务管理器
	// 内存纹丝不动，无连接时调参不生效）。

	searchTimeout time.Duration // 单次搜索兜底超时（默认 60s），测试注入短值
	stopGrace     time.Duration // 强停后等待 bestmove 的第二超时（默认 3s），测试注入短值
	restartDelay  time.Duration // 重启退避基准（默认 1.2s），测试注入短值
	done          chan struct{} // Run 退出（含看门狗终态）时关闭；main 等它防引擎进程残留
}

// New 创建引擎桥。调用方以 goroutine 跑 Run。
func New(opts Options, log *slog.Logger) *Engine {
	opts = opts.withDefaults()
	opts.Log = log // startRealProc 把引擎 stderr 逐行导入日志（windowsgui 下无控制台可写）
	e := &Engine{
		opts:          opts,
		log:           log.With("component", "engine"),
		events:        make(chan Event, 64),
		pending:       make(chan string, 1),
		tuneNotify:    make(chan struct{}, 1),
		searchTimeout: 60 * time.Second,
		stopGrace:     3 * time.Second,
		restartDelay:  1200 * time.Millisecond,
		done:          make(chan struct{}),
	}
	e.depth.Store(int64(opts.Depth))
	e.moveTimeMS.Store(int64(opts.MoveTimeMS))
	e.threads, e.appliedThreads = opts.Threads, opts.Threads
	e.hashMB, e.appliedHash = opts.HashMB, opts.HashMB
	e.multiPV, e.appliedMultiPV = opts.MultiPV, opts.MultiPV
	return e
}

// ---------- 运行中算力调节（协议 §4.10，server 经 EngineTuner 调用） ----------

// SetDepth 设置固定深度制搜索目标（1–40），下一次 go depth 生效。
// 返回是否接受（越界拒绝）。
func (e *Engine) SetDepth(d int) bool {
	if d < 1 || d > 40 {
		return false
	}
	e.depth.Store(int64(d))
	e.log.Info("搜索目标深度已调整", "depth", d)
	return true
}

// SetMoveTimeMS 设置时间制搜索上限（毫秒）。0 = 退回深度制（go depth），
// 500–20000 = 每步 go movetime。时间制优先于深度——快棋/跟盘节奏场景下
// 深度制在复杂局面会拖慢出建议（2026-10-28 功能定稿）。
func (e *Engine) SetMoveTimeMS(ms int) bool {
	if ms != 0 && (ms < 500 || ms > 20000) {
		return false
	}
	e.moveTimeMS.Store(int64(ms))
	if ms > 0 {
		e.log.Info("搜索已切换时间制", "movetime_ms", ms)
	} else {
		e.log.Info("搜索已切回深度制", "depth", e.Depth())
	}
	return true
}

// MoveTimeMS 当前时间制上限（0 = 深度制）。
func (e *Engine) MoveTimeMS() int { return int(e.moveTimeMS.Load()) }

// SetHashMB 设置置换表大小（MB）。0 = 自动（defaultHashMB，2GiB）；
// 手动范围 32–32768（对齐皮卡鱼 2026-09 版 Hash 上限 33554432MB，按产品档位收敛）。
// setoption 经空闲冲刷或下一轮搜索开始前发送（二者均为 UCI 空闲窗口），见 searchLoop。
// 返回是否接受。
func (e *Engine) SetHashMB(mb int) bool {
	if mb == 0 {
		mb = defaultHashMB
	} else if mb < 32 || mb > 32768 {
		return false
	}
	e.tuneMu.Lock()
	e.hashMB = mb
	e.tuneMu.Unlock()
	e.notifyTune()
	e.log.Info("Hash 配置已更新（空闲冲刷或下一轮搜索前生效）", "hash_mb", mb)
	return true
}

// Threads 当前线程数配置。
func (e *Engine) Threads() int {
	e.tuneMu.Lock()
	defer e.tuneMu.Unlock()
	return e.threads
}

// notifyTune 非阻塞投递调参信号（容量 1 丢旧保新：待发集合由 takePendingOptions
// 统一计算，重复信号幂等）。searchLoop 空闲等待收到即冲刷。
func (e *Engine) notifyTune() {
	select {
	case e.tuneNotify <- struct{}{}:
	default:
	}
}

// SetThreads 设置引擎线程数（1–64；0 = 自动 = 逻辑处理器数的一半）。
// setoption 经空闲冲刷或下一轮搜索开始前发送（二者均为 UCI 空闲窗口）。返回是否接受。
func (e *Engine) SetThreads(n int) bool {
	if n < 0 || n > 64 {
		return false
	}
	if n == 0 {
		n = autoThreads()
	}
	e.tuneMu.Lock()
	e.threads = n
	e.tuneMu.Unlock()
	e.notifyTune()
	e.log.Info("线程数已更新（空闲冲刷或下一轮搜索前生效）", "threads", n)
	return true
}

// MultiPV 当前多 PV 线数配置。
func (e *Engine) MultiPV() int {
	e.tuneMu.Lock()
	defer e.tuneMu.Unlock()
	return e.multiPV
}

// SetMultiPV 设置多 PV 线数（1–5，皮卡鱼原生上限 128，按产品档位收敛；
// >1 时算力分摊给候选着法，1 = 最强但无变招候选）。setoption 经空闲冲刷或
// 下一轮搜索开始前发送。返回是否接受。
func (e *Engine) SetMultiPV(n int) bool {
	if n < 1 || n > 5 {
		return false
	}
	e.tuneMu.Lock()
	e.multiPV = n
	e.tuneMu.Unlock()
	e.notifyTune()
	e.log.Info("MultiPV 已更新（空闲冲刷或下一轮搜索前生效）", "multi_pv", n)
	return true
}

// Depth 当前搜索目标深度。
func (e *Engine) Depth() int { return int(e.depth.Load()) }

// HashMB 当前 Hash 配置（MB；目标值，可能尚未在引擎内生效）。
func (e *Engine) HashMB() int {
	e.tuneMu.Lock()
	defer e.tuneMu.Unlock()
	return e.hashMB
}

// ClearHash 请求清空置换表（皮卡鱼 Clear Hash button 选项）：置位后经空闲
// 冲刷或下一轮搜索开始前执行（UCI 禁止搜索中 setoption）。
func (e *Engine) ClearHash() {
	e.tuneMu.Lock()
	e.pendingClear = true
	e.tuneMu.Unlock()
	e.notifyTune()
	e.log.Info("已请求清空置换表（空闲冲刷或下一轮搜索前执行）")
}

// takePendingOptions 目标算力与引擎内已生效值不同时，生成待发送的 setoption 行
// （顺序 Clear Hash→Threads→Hash→MultiPV，官方要求 Threads 先于 Hash），并同步
// "已生效"标记；无差异返回 nil。由 searchLoop 在刚取到任务、尚未 position/go
// 的空闲窗口调用（UCI 禁止搜索中 setoption）。
func (e *Engine) takePendingOptions() []string {
	e.tuneMu.Lock()
	defer e.tuneMu.Unlock()
	var out []string
	if e.pendingClear {
		e.pendingClear = false
		out = append(out, "setoption name Clear Hash")
	}
	if e.threads != e.appliedThreads {
		e.appliedThreads = e.threads
		out = append(out, fmt.Sprintf("setoption name Threads value %d", e.threads))
	}
	if e.hashMB != e.appliedHash {
		e.appliedHash = e.hashMB
		out = append(out, fmt.Sprintf("setoption name Hash value %d", e.hashMB))
	}
	if e.multiPV != e.appliedMultiPV {
		e.appliedMultiPV = e.multiPV
		out = append(out, fmt.Sprintf("setoption name MultiPV value %d", e.multiPV))
	}
	return out
}

// Events 返回事件流。
func (e *Engine) Events() <-chan Event { return e.events }

// Done 返回 Run 完全退出的信号（引擎子进程已 Quit）。
// main 优雅关闭时等待它，确保不留 pikafish.exe 僵尸进程。
func (e *Engine) Done() <-chan struct{} { return e.done }

// State 当前引擎状态（loading/ready/error）。
func (e *Engine) State() string {
	if v, ok := e.state.Load().(string); ok {
		return v
	}
	return StateLoading
}

func (e *Engine) setState(s string) {
	if e.State() != s {
		e.state.Store(s)
		e.emit(Event{Kind: EventState, State: s})
	}
}

// emit 事件入队：丢旧保新。隐式契约有三——① 只在 Run goroutine 内被调用
// （setState/searchLoop/collect），"丢一再重试"依赖单生产者才保证必成功，
// EventBestmove/EventCritical 因此永不丢；② 绝不可改为阻塞发送：collect 被
// 阻塞会经 stdout 管道反压拖住引擎进程；③ 64 容量用于吸收 MultiPV info 突发。
func (e *Engine) emit(ev Event) {
	select {
	case e.events <- ev:
	default:
		// 事件流满：丢弃最旧，保新（status 类事件天然被下一条覆盖）
		select {
		case <-e.events:
		default:
		}
		select {
		case e.events <- ev:
		default:
		}
	}
}

// Request 请求分析一个局面：最新覆盖——替换等待槽并打断进行中的搜索。
func (e *Engine) Request(fen string) {
	// 槽满则先取旧再放新，保证只保留最新
	select {
	case <-e.pending:
	default:
	}
	select {
	case e.pending <- fen:
	default:
	}
	// 搜索中则打断：stop 后 bestmove 很快返回，旧结果由 preempt 标志作废。
	// ⚠已知极窄竞态（接受）：本判定与下方 preempt.Store 之间若搜索恰好自然
	// 收束（collect 的 Swap 先消费了 preempt 位），残留标志会作废下一轮搜索
	// 的首个 bestmove——该次建议丢失、待下一次请求覆盖。窗口纳秒级；修复
	// 须连带考虑"pending 已被消费"的补偿重发，只调标志顺序无效。
	if e.searching.Load() {
		e.preempt.Store(true)
		e.stopOnce()
	}
}

// stopOnce 打断当前搜索：向当前进程发 stop（realProc/fake 的 WriteLine 均自带写锁）。
func (e *Engine) stopOnce() {
	if p, ok := e.cur.Load().(proc); ok && p != nil {
		_ = p.WriteLine("stop")
	}
}

// 看门狗退避参数：连续"快速失败"（进程启动后存活不足 10s 就退出，典型原因是
// 坏局面让皮卡鱼崩溃、引擎/NNUE 文件损坏）达到阈值后，重试间隔指数退避至 30s
// 封顶——避免"崩溃→1.2s→重启（重载 NNUE + 分配 2GiB Hash）"死循环拖死整机；
// 保持慢速重试维持自愈能力（文件被修复后可自动恢复）。
const (
	restartFastFailWindow = 10 * time.Second
	restartBackoffMax     = 30 * time.Second
	restartBackoffTrigger = 5
)

// backoffDelay 连续快速失败 n 次时的重试间隔：未达阈值用基础间隔（restartDelay），
// 达到阈值后每次再翻一倍，封顶 restartBackoffMax（移位溢出时同样取封顶值）。
func (e *Engine) backoffDelay(consecFastFails int) time.Duration {
	if consecFastFails < restartBackoffTrigger {
		return e.restartDelay
	}
	d := e.restartDelay << uint(consecFastFails-restartBackoffTrigger+1)
	if d <= 0 || d > restartBackoffMax {
		return restartBackoffMax
	}
	return d
}

// Run 主循环：启动进程 → 握手 → 搜索循环；进程意外退出按退避重启，直至 ctx 取消。
// 连续快速失败（存活 <10s）会触发指数退避，防止坏输入/损坏文件导致无限快速重启。
func (e *Engine) Run(ctx context.Context) {
	defer close(e.done)
	e.setState(StateLoading)
	consecFast := 0 // 连续快速失败计数（存活超过窗口即清零）
	for {
		if ctx.Err() != nil {
			e.setState(StateError)
			return
		}
		startedAt := time.Now()
		p, err := procStarter(e.opts)
		if err != nil {
			e.log.Error("引擎进程启动失败", "err", err)
			e.setState(StateError)
			consecFast++
			if consecFast >= restartBackoffTrigger {
				e.log.Warn("引擎连续启动失败，已延长重试间隔（请检查引擎/NNUE 文件）",
					"consecutive", consecFast, "next_delay", e.backoffDelay(consecFast))
			}
			if !sleepCtx(ctx, e.backoffDelay(consecFast)) {
				return
			}
			continue
		}
		e.cur.Store(p)

		if err := e.handshake(ctx, p); err != nil {
			e.log.Error("引擎握手失败", "err", err)
			_ = p.Quit()
			e.setState(StateError)
			if ctx.Err() != nil {
				return
			}
			consecFast++
			if !sleepCtx(ctx, e.backoffDelay(consecFast)) {
				return
			}
			continue
		}

		e.setState(StateReady)
		// 重启后清残留打断标志：旧进程的 collect 异常退出（如 EOF）不经过
		// 收束路径，preempt 位无人消费；残留会让重启后的首个正常收束被
		// 误判为"被打断"而丢弃（实测：打断预热→进程崩→重启→新搜索结果被丢）。
		e.preempt.Store(false)
		// 新进程的引擎内参数已按 handshake 的当前目标值设置，对齐"已生效"标记
		e.tuneMu.Lock()
		e.appliedThreads, e.appliedHash, e.appliedMultiPV = e.threads, e.hashMB, e.multiPV
		e.tuneMu.Unlock()
		e.log.Info("引擎就绪", "path", e.opts.Path, "threads", e.Threads(),
			"hash_mb", e.HashMB(), "multi_pv", e.MultiPV(), "depth", e.Depth())

		// 预热：加载 NNUE/热哈希。预热局面作为 searchLoop 首轮兜底任务（不占请求槽，
		// 槽内的用户请求优先），结果由 server 按"最新请求 FEN 匹配"自然丢弃。
		err = e.searchLoop(ctx, p, "rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR w - - 0 1")
		if ctx.Err() != nil {
			_ = p.Quit()
			return
		}
		// 进程死亡或搜索循环异常退出：报错并重启。存活超过快速失败窗口视为
		// 正常运行后中断（断电/被杀），退避计数清零
		e.log.Warn("引擎进程退出，看门狗将重启", "err", err)
		_ = p.Quit()
		e.setState(StateLoading)
		if time.Since(startedAt) < restartFastFailWindow {
			consecFast++
			if consecFast >= restartBackoffTrigger {
				e.log.Warn("引擎连续快速崩溃，已延长重试间隔（坏局面或文件损坏嫌疑）",
					"consecutive", consecFast, "next_delay", e.backoffDelay(consecFast))
			}
		} else {
			consecFast = 0
		}
		if !sleepCtx(ctx, e.backoffDelay(consecFast)) {
			return
		}
	}
}

// handshake UCI 握手：uci→uciok、setoption、ucinewgame、isready→readyok。
// 时序与 AnalysisEngine.kt 一致（uciok 之后才允许 setoption）。
func (e *Engine) handshake(ctx context.Context, p proc) error {
	if err := p.WriteLine("uci"); err != nil {
		return fmt.Errorf("发送 uci 失败: %w", err)
	}
	uciok := false
	// 10s 为冷启动兜底：首次启动含杀软扫描 exe/NNUE、EvalFile 载入与 Hash 首次
	// 分配，超时按坏引擎走看门狗退避。勿随意调小——调小＝正常慢机器上永远起不来。
	deadline := time.Now().Add(10 * time.Second)
	for !uciok {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if time.Now().After(deadline) {
			return errors.New("握手超时（uciok 未到）")
		}
		// 限时读：无输出回环让 deadline/ctx 检查接管（阻塞读在引擎静默时永远
		// 到不了上方超时判定，握手兜底失效）。EOF 保持既有路径。
		line, err := p.ReadLineTimeout(200 * time.Millisecond)
		if err != nil {
			if errors.Is(err, errReadTimeout) {
				continue
			}
			return fmt.Errorf("引擎输出关闭: %w", err)
		}
		tokens := strings.Fields(line)
		if len(tokens) == 0 {
			continue
		}
		switch tokens[0] {
		case "uciok":
			uciok = true
		case "id", "option":
			if len(tokens) >= 2 && tokens[1] == "name" {
				e.log.Debug("引擎能力", "line", line)
			}
		}
	}

	// 配置（EvalFile 用绝对路径，工作目录无关）。
	// 顺序按官方要求：Threads 必须先于 Hash 设置（Hash 按 NUMA/线程布局分配）。
	if e.opts.NNUE != "" {
		if err := p.WriteLine("setoption name EvalFile value " + e.opts.NNUE); err != nil {
			return fmt.Errorf("设置 EvalFile 失败: %w", err)
		}
	}
	if err := p.WriteLine(fmt.Sprintf("setoption name Threads value %d", e.Threads())); err != nil {
		return err
	}
	if err := p.WriteLine(fmt.Sprintf("setoption name Hash value %d", e.HashMB())); err != nil {
		return err
	}
	if err := p.WriteLine(fmt.Sprintf("setoption name MultiPV value %d", e.MultiPV())); err != nil {
		return err
	}
	// 胜/和/负概率：info 行输出 wdl W D L（走子方视角千分比），经 suggestion 帧下发
	if err := p.WriteLine("setoption name UCI_ShowWDL value true"); err != nil {
		return err
	}
	if err := p.WriteLine("ucinewgame"); err != nil {
		return err
	}
	if err := p.WriteLine("isready"); err != nil {
		return err
	}
	// 15s 同为冷启动兜底（NNUE 大文件载入后首条 readyok 可能很慢），勿调小。
	deadline = time.Now().Add(15 * time.Second)
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if time.Now().After(deadline) {
			return errors.New("就绪握手超时（readyok 未到）")
		}
		// 限时读：同 uciok 循环，无输出回环让 deadline/ctx 检查接管
		line, err := p.ReadLineTimeout(200 * time.Millisecond)
		if err != nil {
			if errors.Is(err, errReadTimeout) {
				continue
			}
			return fmt.Errorf("引擎输出关闭: %w", err)
		}
		if strings.TrimSpace(line) == "readyok" {
			return nil
		}
	}
}

// searchLoop 搜索主循环：等待请求 → position/go → 收 info/bestmove → 发事件。
// warmup 为预热局面：仅首轮且槽内无用户请求时使用，不与用户请求竞争槽位。
// 返回 nil 表示 ctx 取消；非 nil 表示进程异常（调用方走看门狗重启）。
func (e *Engine) searchLoop(ctx context.Context, p proc, warmup string) error {
	var lineErr error
	first := true
	for {
		// 取最新请求（无任务轮询等待）
		var fen string
		got := false
		if first {
			first = false
			select {
			case fen = <-e.pending: // 用户请求优先
			default:
				fen = warmup // 预热兜底
			}
			got = true
		}
		for !got {
			if ctx.Err() != nil {
				_ = p.WriteLine("quit")
				return nil
			}
			select {
			case fen = <-e.pending:
				got = true
			case <-e.tuneNotify:
				// 空闲冲刷调参（2026-10-20）：无搜索任务时收到调参信号，此刻
				// searching=false 是 UCI 允许 setoption 的空闲窗口，直接发送
				// 立即生效。此前参数只能搭"下一轮搜索"的车——引擎刚启动、
				// 手机从未连接时没有任何搜索可搭，调 hash 引擎毫无动作
				// （用户实测：任务管理器内存纹丝不动）。
				for _, opt := range e.takePendingOptions() {
					if err := p.WriteLine(opt); err != nil {
						_ = p.WriteLine("quit")
						return err // 写失败=进程异常，走看门狗重启（与主循环同契约）
					}
					e.log.Info("引擎参数已空闲生效", "setoption", opt)
				}
			case <-time.After(100 * time.Millisecond):
			}
		}

		// searching 必须先于 position/go 置位：Request 凭它决定是否发 stop+preempt。
		// 若放在 go 之后，窗口内到达的新请求不会打断刚发出的搜索，旧搜索只能等
		// 自然收束，"最新覆盖"会延迟一个 searchTimeout 量级。
		e.searching.Store(true)
		// 待生效的算力变更（set_engine 远程调节 / 桌面面板调节）：此刻刚取到新任务、
		// 尚未 position/go，是 UCI 允许 setoption 的空闲窗口
		for _, opt := range e.takePendingOptions() {
			if err := p.WriteLine(opt); err != nil {
				lineErr = err
				break
			}
			e.log.Info("引擎参数已在搜索前生效", "setoption", opt)
		}
		if lineErr != nil {
			break
		}
		// 全文不拼 moves（单局面快照制式）：引擎按无历史局面评估，同 FEN 结果
		// 可复用（沉淀库按 FEN 查表同口径）；重复局面/长将判例缺历史依据属已知
		// 语义边界。勿顺手补 moves——会破坏快照幂等与沉淀库口径。
		if err := p.WriteLine("position fen " + fen); err != nil {
			lineErr = err
			break
		}
		// 思考模式二选一：时间制（movetime>0）优先——go movetime 自行收束；
		// 深度制 go depth 由 searchTimeout 兜底。打断/最新覆盖机制两制共用。
		goCmd := fmt.Sprintf("go depth %d", e.Depth())
		if mt := e.moveTimeMS.Load(); mt > 0 {
			goCmd = fmt.Sprintf("go movetime %d", mt)
		}
		if err := p.WriteLine(goCmd); err != nil {
			lineErr = err
			break
		}

		result, aborted, err := e.collect(ctx, p, fen)
		e.searching.Store(false)
		if err != nil {
			if ctx.Err() != nil {
				_ = p.WriteLine("quit")
				return nil
			}
			lineErr = err
			break
		}
		if aborted {
			// 被新请求打断：旧搜索的 bestmove 丢弃，pending 里的新请求下一轮处理
			continue
		}
		e.emit(Event{Kind: EventBestmove, Result: result})
	}
	return lineErr
}

// collect 收束一次搜索：读 info/bestmove 直到 bestmove、超时强停或被打断。
// aborted=true 表示期间出现新请求（旧结果作废）。
func (e *Engine) collect(ctx context.Context, p proc, fen string) (*Result, bool, error) {
	lines := make(map[int]*Info)
	deadline := time.Now().Add(e.searchTimeout)
	stopped := false
	for {
		if ctx.Err() != nil {
			return nil, false, ctx.Err()
		}
		// 超时强停兜底：searchTimeout 耗尽仍未收束→发 stop 再给 stopGrace 窗口
		// 收 bestmove（UCI 要求收到 stop 后尽快回 bestmove）。stopped 置位后不再
		// 重复强停。
		if time.Now().After(deadline) {
			if stopped {
				// 第二超时：强停后仍未收束——引擎既不回 bestmove 也不退出
				// （挂死/管道卡死）。此前此处退化为无界阻塞读，collect 永久
				// 卡死、searchLoop 不返回、Run 的看门狗永远走不到重启（P1 实锤：
				// 桥整个瘫痪只能重启进程）。返回错误收场，让 searchLoop 退出、
				// 看门狗 Quit+重启接管；滞留输出不污染下一轮——出错返回时进程
				// 必被 Run 的 p.Quit() 整体回收，下一轮在新进程上起步。
				return nil, false, fmt.Errorf("引擎 stop 后未收束（%s 内无 bestmove，疑似挂死）", e.stopGrace)
			}
			e.log.Warn("搜索超时，强制 stop", "fen", fen)
			_ = p.WriteLine("stop")
			stopped = true
			deadline = time.Now().Add(e.stopGrace)
		}
		// 限时读（分片 200ms）：无输出回环让 ctx/deadline 检查接管——读侧任何
		// 时刻都不越过 deadline 太远，第二超时才可能真正触发；EOF（输出流关闭，
		// 引擎已退出）保持既有路径上抛走看门狗。
		line, err := p.ReadLineTimeout(200 * time.Millisecond)
		if err != nil {
			if errors.Is(err, errReadTimeout) {
				continue
			}
			return nil, false, fmt.Errorf("引擎输出关闭: %w", err)
		}
		tokens := strings.Fields(line)
		if len(tokens) == 0 {
			continue
		}
		switch tokens[0] {
		case "info":
			// info string 叙述行默认无分数会被 parseInfo 丢弃，但皮卡鱼的崩溃
			// 原因（"CRITICAL ERROR: ... Reason: Unsupported position."）正走
			// 此行——不拦住就只剩 "引擎输出关闭: EOF"，死因不可见（第二轮审查
			// P1-2）。上抛事件让 server 定向回 bad_position 并隔离该局面。
			if strings.Contains(line, "CRITICAL ERROR") {
				e.log.Warn("引擎报告危急错误", "detail", line)
				e.emit(Event{Kind: EventCritical, Fen: fen, Message: line})
			}
			if info, ok := parseInfo(tokens); ok {
				lines[info.MultiPV] = info
				e.emit(Event{Kind: EventInfo, Fen: fen, Info: info})
			}
		case "bestmove":
			best := "null"
			if len(tokens) > 1 && tokens[1] != "(none)" {
				best = tokens[1]
			}
			// 外部 Request 打断（发过 stop）→ 本次 bestmove 作废，新请求由主循环处理；
			// 超时强停不算打断（收束的就是当前局面的结果，有效）。
			// 作废前必须排干 stop 回执：引擎对一次 stop 恰回一条 bestmove，
			// 若自然收束与回执先后排队，不排干会被下一轮搜索误读为本轮结果。
			if e.preempt.Swap(false) {
				e.drainStopReceipt(p)
				return nil, true, nil
			}
			return &Result{
				Fen:      fen,
				RedGo:    RedGoOf(fen),
				BestMove: best,
				Lines:    sortedLines(lines),
			}, false, nil
		}
	}
}

// drainStopReceipt 打断后短窗口排干可能排队的 stop 回执 bestmove。
// 引擎对 stop 的回执是同步尽快输出；自然收束与回执之间不会再有 info 行。
// 两层作用缺一不可：除 bestmove 回执外，还要丢弃回执前排队的残 lines（被打断
// 搜索的尾巴 info），保证下一轮 collect 从干净管道起步。300ms 窗口是承重墙：
// 删除或缩短后，极端负载下迟到的 bestmove 会被下一轮 collect 误认作新搜索
// 结果（且 Result.Fen 贴错局面）；100ms 是窗口内的分片读粒度。
func (e *Engine) drainStopReceipt(p proc) {
	deadline := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(deadline) {
		line, err := p.ReadLineTimeout(100 * time.Millisecond)
		if err != nil {
			return // 分片超时或输出流关闭（EOF）都终止排干
		}
		tokens := strings.Fields(line)
		if len(tokens) > 0 && tokens[0] == "bestmove" {
			return // 回执已消化，管道干净
		}
	}
}

// startRealProc 启动真实皮卡鱼子进程。
func startRealProc(opts Options) (proc, error) {
	cmd := exec.Command(opts.Path)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	// stderr 进日志而非 os.Stderr：windowsgui 子系统下父进程无控制台，
	// 该句柄无效（第二轮审查 P1-2——皮卡鱼 stderr 输出原本全部丢失）
	if opts.Log != nil {
		if stderr, err := cmd.StderrPipe(); err == nil {
			go func() {
				sc := bufio.NewScanner(stderr)
				for sc.Scan() {
					opts.Log.Warn("引擎 stderr", "line", sc.Text())
				}
			}()
		} else {
			// 管道建立失败=引擎 stderr 永久丢失（诊断信息黑洞），必须留痕
			opts.Log.Warn("引擎 stderr 管道建立失败，诊断输出将丢失", "err", err)
		}
	}
	// 平台相关子进程属性：Windows 下隐藏 pikafish 的控制台黑窗（CREATE_NO_WINDOW）
	applyProcAttr(cmd)
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("启动 %s 失败: %w", opts.Path, err)
	}
	// 提升引擎进程优先级（Windows：High class；其他平台无操作），
	// 保证前台分析场景的搜索吞吐与交互延迟
	elevateProcPriority(cmd)

	rp := &realProc{
		cmd:   cmd,
		stdin: stdin,
		lines: make(chan string, 256),
	}
	// 64KB 读缓冲：默认 4096 会让每 fill 只吞 8-20 行（syscall ~3-10 次/秒）；
	// 加大后整批读入，行为严格等价（ReadString 按需增长，超长 info 行无截断风险）
	go rp.readLoop(bufio.NewReaderSize(stdout, 65536))
	return rp, nil
}

// realProc 真实进程封装：stdin 写加锁（stop 打断与 Run 线程并发写）；
// stdout 由常驻 reader goroutine 读入 channel，支持限时读与超时排干。
type realProc struct {
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	lines   chan string // 引擎输出行（reader goroutine 产出；关闭表示流结束）
	stdinMu sync.Mutex
}

func (r *realProc) WriteLine(line string) error {
	r.stdinMu.Lock()
	defer r.stdinMu.Unlock()
	_, err := io.WriteString(r.stdin, line+"\n")
	return err
}

func (r *realProc) ReadLine() (string, error) {
	line, ok := <-r.lines
	if !ok {
		return "", io.EOF // 输出流已关闭（进程退出）
	}
	return line, nil
}

func (r *realProc) ReadLineTimeout(d time.Duration) (string, error) {
	select {
	case line, ok := <-r.lines:
		if !ok {
			return "", io.EOF // 输出流已关闭（进程退出）
		}
		return line, nil
	case <-time.After(d):
		return "", errReadTimeout
	}
}

// Quit 关闭链：quit → 关 stdin（EOF 兜底，覆盖引擎不响应 quit 的情况）→
// cmd.Wait 最多等 3s，超时 Kill 防皮卡鱼偶发不退卡死看门狗。Wait 放 goroutine
// 是为了 Kill 后仍能回收资源；Wait 返回后 StdoutPipe 被关闭，readLoop 随之
// EOF 退出（进程生命周期与 stdout EOF 的因果链在此闭环）。
// procQuitKillAfter 进程级 quit 强杀窗口（OS 层兜底，与 Engine.stopGrace 的
// 协议层收束窗口语义独立，值恰好同为 3s）。
const procQuitKillAfter = 3 * time.Second

func (r *realProc) Quit() error {
	_ = r.WriteLine("quit")
	_ = r.stdin.Close()
	done := make(chan struct{})
	go func() { _ = r.cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(procQuitKillAfter):
		_ = r.cmd.Process.Kill()
		<-done
	}
	return nil
}

// readLoop 常驻读引擎输出行进 channel；流结束（进程退出）时关闭 lines。
// 三条隐式契约：① close(lines) 只能由本函数（唯一发送方）执行，Quit 不得
// 代关，否则 send-on-closed panic；② close 后缓冲中残留行仍可被消费，EOF
// 判定天然排在残行之后（不丢最后一批 info）；③ 256 缓冲吸收 info 突发，
// 满时阻塞 reader 反压引擎 stdout——collect 不可长时间离岗。
func (r *realProc) readLoop(reader *bufio.Reader) {
	defer close(r.lines)
	for {
		line, err := reader.ReadString('\n')
		if line != "" {
			r.lines <- strings.TrimRight(line, "\r\n")
		}
		if err != nil {
			return
		}
	}
}

// sleepCtx 可中断睡眠；返回 false 表示 ctx 已取消。
func sleepCtx(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}
