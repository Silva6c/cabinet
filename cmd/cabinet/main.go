// cabinet 是"内阁"电脑端后台服务的入口：
// WSS 服务端 + 自签证书 + 令牌鉴权 + 皮卡鱼引擎桥 + 桔库（热加载）+ 沉淀 + 托盘。
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"time"

	"cabinet/internal/config"
	"cabinet/internal/console"
	"cabinet/internal/engine"
	"cabinet/internal/library"
	"cabinet/internal/logging"
	"cabinet/internal/server"
	"cabinet/internal/store"
	"cabinet/internal/tray"
)

func main() {
	// ⚠【问题标明 2026-10-01】发布构建必须带 -ldflags "-H windowsgui"（README §构建）：
	// 裸 go build 产物是 console 子系统（PE Subsystem=CUI），双击 exe 会弹黑框。
	// 默认构建日志只写 logs/cabinet.log（console_off.go），windowsgui 下 stdout
	// 不可见无碍；pikafish 子进程黑框已由 CREATE_NO_WINDOW 单独处理。
	// 资源根锚定（绿色便携化 2026-10-07）：全部资源按"exe 目录（有 appdata\ 则
	// 取 appdata\）"解析，与启动时的 CWD 无关——任务计划/开机自启/任意目录命令行
	// 启动均资源命中。规则详见 internal/config/root.go 头注释。
	// 注：flag 默认值携带资源根绝对路径；用户显式传 -config 相对路径时仍按 CWD
	// 解析（经典 CLI 语义，行为同改造前）。
	resourceRoot := config.DiscoverResourceRoot()
	configPath := flag.String("config", filepath.Join(resourceRoot, "configs", "cabinet.toml"), "配置文件路径")
	genToken := flag.Bool("gen-token", false, "生成 64 字符随机令牌后退出（写入配置的 token 字段）")
	noTray := flag.Bool("no-tray", defaultNoTray, "不启动托盘与主窗口（调试/服务方式运行；console 构建默认 true）")
	flag.Parse()

	if *genToken {
		t, err := server.GenerateToken()
		if err != nil {
			fmt.Fprintln(os.Stderr, "生成令牌失败:", err)
			os.Exit(1)
		}
		fmt.Println(t)
		return
	}

	// 单实例锁：必须在一切初始化（引擎/SQLite/端口）之前判定——双开的第二个
	// 实例会静默死在端口占用上，用户只会觉得"点开没反应"。锁名按配置文件派生：
	// 同配置互斥，不同配置（测试/联调）可并行第二实例
	if !acquireSingleton(*noTray, *configPath) {
		return
	}

	if err := run(*configPath, resourceRoot, *noTray); err != nil {
		fmt.Fprintln(os.Stderr, "cabinet 退出:", err)
		os.Exit(1)
	}
}

// run 组装并运行全部组件：配置 → 日志 → 令牌 → 证书 → 引擎 → 沉淀 → 库管理 → WSS → 托盘。
// resourceRoot 为资源根（exe 目录或便携包 appdata\），配置内相对路径一律按它解析。
func run(configPath, resourceRoot string, noTray bool) error {
	cfg, created, err := config.Load(configPath)
	if err != nil {
		return err
	}
	if created {
		fmt.Printf("已生成默认配置 %s（首次运行会自动生成随机令牌，手机端配对即用它）\n", configPath)
	}

	// 令牌策略：首次生成随机令牌并持久化；已存在但为空则拒绝启动
	if cfg.Token == "" {
		t, err := server.GenerateToken()
		if err != nil {
			return fmt.Errorf("生成随机令牌失败: %w", err)
		}
		cfg.Token = t
		if err := cfg.Save(configPath); err != nil {
			return fmt.Errorf("回写令牌失败: %w", err)
		}
		fmt.Println("已生成随机配对令牌并写入配置（token 字段）")
	}

	// 路径锚定：把配置内的相对路径按资源根就地解析为绝对路径，此后所有组件
	// （日志/证书/引擎/沉淀/桔库）拿到的都是绝对路径，与 CWD 无关。
	// ⚠ 必须放在上面的 cfg.Save（token 回写）之后——Save 会把内存态整体写回
	// toml，先解析会把绝对路径固化进配置文件、破坏便携性（root.go 铁律）。
	cfg.ResolvePaths(resourceRoot)

	logger, err := logging.Setup(cfg.Log)
	if err != nil {
		return err
	}
	slog.Info("内阁启动", "config", configPath, "created", created)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cert, err := server.EnsureCert(cfg.TLS.CertDir)
	if err != nil {
		return err
	}
	// 证书指纹：手机端配对锁定依据（二维码 f 参数 + 控制台"② 连接"区展示）
	certFP, err := server.CertificateFingerprint(cert)
	if err != nil {
		return err
	}
	slog.Info("证书就绪", "cert_dir", cfg.TLS.CertDir, "sha256", certFP)

	// 引擎桥装配：启用且 exe 存在才启动；缺失则告警并退化为无引擎模式。
	// 保留具体类型 engConcrete 的原因：server 只经 TuneEngine/ClearHash 暴露
	// "写"路径（EngineTuner 断言在 server 内部），面板回显所需的 Depth/HashMB/
	// Threads/MultiPV 只读未走 server API，直接读 *engine.Engine 原子字段。
	// engConcrete==nil 同时覆盖"配置未启用"与"exe 不存在"两条退化路径。
	var eng server.EngineBridge
	var engConcrete *engine.Engine
	var engDone <-chan struct{}
	if cfg.Engine.Enable {
		if _, err := os.Stat(cfg.Engine.Path); err != nil {
			slog.Warn("引擎可执行文件不存在，服务将以无引擎模式运行（position 回 not_ready）",
				"path", cfg.Engine.Path)
		} else {
			nnue, _ := filepath.Abs(cfg.Engine.NNUE)
			engConcrete = engine.New(engine.Options{
				Path:    cfg.Engine.Path,
				NNUE:    nnue,
				Threads: cfg.Engine.Threads,
				HashMB:  cfg.Engine.HashMB,
				MultiPV: cfg.Engine.MultiPV,
				Depth:   cfg.Engine.Depth,
			}, logger)
			eng = engConcrete
			engDone = engConcrete.Done()
			go engConcrete.Run(ctx)
			slog.Info("引擎桥已启动", "path", cfg.Engine.Path, "nnue", nnue)
		}
	}

	// 沉淀库（建议记录，异步批量写）
	var st *store.Store
	if cfg.Store.Enable {
		if st, err = store.Open(cfg.Store.Path, logger); err != nil {
			return fmt.Errorf("沉淀库打开失败: %w", err)
		}
		defer func() { _ = st.Close() }()
		// 启动时裁剪 90 天前的记录：防库文件无界增长（旧对局已无复用价值）
		if n, perr := st.PruneBefore(time.Now().AddDate(0, 0, -90)); perr == nil && n > 0 {
			logger.Info("沉淀库裁剪完成", "days", 90, "deleted", n)
		}
	}

	// WSS 服务端
	srv := server.New(cfg, cert, logger, eng)
	// ⚠SetSuggestionSink/SetSedimentLookup 是 server 的无锁普通字段写（SetBook
	// 为 atomic.Value），必须全部保持在 go srv.Run 之前——goroutine 启动即
	// happens-before 边；顺序一旦后移就是数据竞争，且启动初期产生的建议会因
	// sink 未注册而静默丢失。
	if st != nil {
		srv.SetSuggestionSink(func(rec server.SuggestionRecord) {
			st.Save(store.SuggestionRec{
				Source: rec.Source, FEN: rec.FEN, BestMove: rec.BestMove,
				ScoreCP: rec.ScoreCP, ScoreMate: rec.ScoreMate, PV: rec.PV, Depth: rec.Depth,
				GameID: rec.GameID,
			})
		})
		// 采纳行为事件（2026-10-17）：建议下发后的实际走子比对结果落库，
		// 长期统计建议采纳率（引用方：控制台复盘/棋手指标，第二梯队 #8）
		srv.SetAdoptionSink(func(rec server.AdoptionRecord) {
			st.SaveAdoption(store.AdoptionRec{
				FEN: rec.FEN, Suggested: rec.Suggested, Actual: rec.Actual,
				MatchKind: rec.MatchKind, CandRank: rec.CandRank,
				Source: rec.Source, Depth: rec.Depth,
				ScoreCP: rec.ScoreCP, ScoreMate: rec.ScoreMate,
			})
		})
		// 算力参数落盘（2026-10-18）：悬浮窗/面板切换后记住选择，重启以落盘
		// 值为准。⚠必须走快照 + UnresolvePaths：直接 Save 内存态会把
		// ResolvePaths 固化的绝对路径写回 toml、破坏便携性（root.go 铁律）；
		// 内存态同步更新使下次落盘基于新值。
		// ⚠并发契约（2026-10-18 加固）：本回调可从手机 WSS 读 goroutine（每个
		// 连接一个，支持多手机）与 fyne UI goroutine（面板 TunerSet）并发进入，
		// 必须全程持锁——否则 cfg 字段写与 snapshot 全字段读构成数据竞争，
		// 且两次并发 os.Create 截断同一 toml 会交错写坏文件（下次启动直接
		// 起不来）。锁 + Config.Save 原子写（tmp+rename）双保险。
		var tunePersistMu sync.Mutex
		srv.SetTunePersist(func(kind string, v int) {
			tunePersistMu.Lock()
			defer tunePersistMu.Unlock()
			snapshot := *cfg
			switch kind {
			case "depth":
				snapshot.Engine.Depth = v
				cfg.Engine.Depth = v
			case "hash":
				snapshot.Engine.HashMB = v
				cfg.Engine.HashMB = v
			case "threads":
				snapshot.Engine.Threads = v
				cfg.Engine.Threads = v
			case "multipv":
				snapshot.Engine.MultiPV = v
				cfg.Engine.MultiPV = v
			default:
				return
			}
			snapshot.UnresolvePaths(resourceRoot)
			if err := snapshot.Save(configPath); err != nil {
				logger.Warn("算力参数落盘失败（重启后将回默认值）", "kind", kind, "value", v, "err", err)
				return
			}
			logger.Info("算力参数已落盘（重启后保持）", "kind", kind, "value", v)
		})
		// 沉淀命中：桔库未查到的局面复用历史引擎终值（同局面秒回）
		srv.SetSedimentLookup(func(fen string) (server.SuggestionRecord, bool) {
			row, lerr := st.LookupByFEN(fen)
			if lerr != nil || row == nil {
				return server.SuggestionRecord{}, false
			}
			return server.SuggestionRecord{
				Source: row.Source, FEN: row.FEN, BestMove: row.BestMove,
				ScoreCP: row.ScoreCP, ScoreMate: row.ScoreMate, PV: row.PV, Depth: row.Depth,
			}, true
		})
	}

	// 库目录管理器（初始加载 + 热加载）
	libMgr, err := library.New(cfg.Library.BooksDir, logger, srv.SetBook)
	if err != nil {
		return fmt.Errorf("库目录管理器初始化失败: %w", err)
	}
	srv.SetBook(libMgr.Store())
	// Run 仅在 ctx 取消时返回 nil，其余返回值是监听失效等真实故障，静默丢弃
	// 会让桔库热更新无声失效（2026-10-27 规范轮补日志）
	go func() {
		defer libMgr.Close() // Run 返回即监听终止，关闭库集合释放 SQLite 句柄
		if err := libMgr.Run(ctx); err != nil && ctx.Err() == nil {
			logger.Error("桔库目录监听异常退出", "err", err)
		}
	}()

	// WSS 服务后台运行；Run 返回（正常关闭或异常）都上报，stop() 联动托盘。
	// srvErr 缓冲 1 且全程只允许一个消费者；srvClosed 是"服务已收尾"广播信号。
	srvErr := make(chan error, 1)
	srvClosed := make(chan struct{})
	go func() {
		err := srv.Run(ctx)
		srvErr <- err
		close(srvClosed)
		if err != nil {
			stop() // 服务异常退出 → 连带结束托盘
		}
	}()

	// 防火墙告警：检测放行规则缺失（首次弹窗点"取消"会静默拦截手机连接），
	// 控制台横幅一键放行（UAC）。状态文件与沉淀库同在资源根 data\ 下，
	// 只承载"不再提示"去重，不写 toml 主配置（便携性铁律，见 ResolvePaths 注释）。
	fwPath := filepath.Join(resourceRoot, "data", "console-state.json")
	fw := startFirewallWatch(cfg.Bind, fwPath, logger)

	// 服务收尾联动托盘与主窗口：服务结束（srvClosed）或 ctx 取消（菜单退出/信号）
	// 都兜底退出两个消息循环，保证 tray.Run / console.Run 必然返回、进程不僵尸
	//（tray.Quit / console.Quit 均幂等，未启动时空操作）。
	// 必须用关闭信号而非 select 读 srvErr：后者会与真正的消费方（noTray 主路径
	// / awaitShutdown）抢消息，抢走后主路径永久阻塞、收尾路径白等 6s 超时
	//（2026-10-01 第二轮审查 P2-1）。
	go func() {
		select {
		case <-srvClosed:
		case <-ctx.Done():
		}
		tray.Quit()
		console.Quit()
	}()

	// awaitShutdown 等待全链收尾：服务停止 + 引擎进程退出（防 pikafish.exe 僵尸）。
	// srvR/haveSrvR：调用方已消费的 srvErr 结果（noTray 主路径）原样传入，
	// 避免二次消费——channel 取空后只能干等超时并打出误导性的
	// "等待服务关闭超时"（服务其实早已关闭）。
	awaitShutdown := func(srvR error, haveSrvR bool) error {
		runErr := srvR
		if !haveSrvR {
			select {
			case runErr = <-srvErr:
			case <-time.After(6 * time.Second):
				logger.Warn("等待服务关闭超时")
			}
		}
		if engDone != nil {
			select {
			case <-engDone: // 引擎子进程已 Quit
			case <-time.After(8 * time.Second):
				logger.Warn("等待引擎进程退出超时（可能残留 pikafish.exe，请手动检查）")
			}
		}
		return runErr
	}

	if noTray {
		err := <-srvErr // -no-tray：信号（Ctrl+C）→ ctx cancel → Run 返回（无窗口无托盘纯服务）
		return awaitShutdown(err, true)
	}

	logDir, _ := filepath.Abs(filepath.Dir(cfg.Log.File))
	logFile, _ := filepath.Abs(cfg.Log.File)

	// 托盘在锁 OS 线程的独立 goroutine（win32 消息循环须同线程创建窗口+收消息）；
	// fyne 主控制台占主 goroutine（GLF 平台约束）。左键/双击图标 = 唤出面板。
	go func() {
		runtime.LockOSThread()
		exeDir, _ := config.ExeDir() // 失败传空串：LoadIcon 回落 CWD 相对再兜底内嵌，同旧行为
		tray.Run(tray.Options{
			Icon:    tray.LoadIcon(exeDir), // exe 目录 assets/icon/ 优先，内嵌资源兜底
			Version: server.Version,
			LogDir:  logDir,
			Status: func() string {
				return fmt.Sprintf("引擎:%s · 库:%d · 连接:%d",
					srv.EngineState(), libMgr.Store().Len(), srv.Clients())
			},
			OnReady: func() {
				logger.Info("托盘已启动（如未见图标请检查任务栏溢出区 ^）")
			},
			OnActivate: console.Show,
			OnShowQR:   console.ShowQR,
			OnExit:     func() { stop() },
		})
	}()

	// 主控制台窗口：启动即显示；× 隐藏回托盘；退出只能走托盘菜单
	console.Run(console.Options{
		Version:         server.Version,
		LogDir:          logDir,
		LogFile:         logFile,
		ConnectToken:    cfg.Token,
		BindAddr:        cfg.Bind,
		CertFingerprint: certFP,
		Snapshot: func() (console.EngineStat, []console.ClientRow) {
			s := srv.ConsoleSnapshot()
			eng := console.EngineStat{
				State: s.Engine.State, Depth: s.Engine.Depth, Nodes: s.Engine.Nodes,
				NPS: s.Engine.NPS, Hashfull: s.Engine.Hashfull, HashMB: s.Engine.HashMB,
				Threads: s.Engine.Threads, MultiPV: s.Engine.MultiPV, TargetDepth: s.Engine.TargetDepth,
			}
			clients := make([]console.ClientRow, 0, len(s.Clients))
			for _, cl := range s.Clients {
				clients = append(clients, console.ClientRow{
					Session: cl.SessionID, FullID: cl.FullID, Devid: cl.Devid, Name: cl.Name,
					Client: cl.Client, Ver: cl.Ver, Authed: cl.Authed,
					RTTMs: cl.RTTMs, Path: cl.Path, LastPos: cl.LastPos, Fen: cl.Fen,
				})
			}
			return eng, clients
		},
		KickSession: func(sessionID string) bool {
			ok := srv.ConsoleKick(sessionID)
			if !ok {
				slog.Info("控制台断开会话：目标不存在（可能已自行断开）", "session", sessionID)
			}
			return ok
		},
		Books: func() []console.BookRow {
			bs := libMgr.Books()
			out := make([]console.BookRow, 0, len(bs))
			for _, b := range bs {
				out = append(out, console.BookRow{Name: b.Name, Entries: b.Entries, ModTime: b.ModTime})
			}
			return out
		},
		Recent: func(n int) []console.SugRow {
			if st == nil {
				return nil
			}
			recs, err := st.Recent(n)
			if err != nil {
				return nil
			}
			out := make([]console.SugRow, 0, len(recs))
			for _, r := range recs {
				out = append(out, console.SugRow{
					TS: r.TS, Source: r.Source, FEN: r.FEN, BestMove: r.BestMove,
					PV: r.PV, ScoreCP: r.ScoreCP, ScoreMate: r.ScoreMate, Depth: r.Depth,
				})
			}
			return out
		},
		CountToday: func() int {
			if st == nil {
				return 0
			}
			n, err := st.CountToday()
			if err != nil {
				logger.Warn("今日沉淀计数失败", "err", err)
				return 0
			}
			return n
		},
		ClearSuggestions: func() error {
			if st == nil {
				return fmt.Errorf("沉淀库未启用（配置 store.enable）")
			}
			if err := st.DeleteAll(); err != nil {
				return fmt.Errorf("清空沉淀失败: %w", err)
			}
			logger.Info("建议沉淀已清空（控制台操作）")
			return nil
		},
		TunerGet: func() console.TuneState {
			if engConcrete == nil {
				return console.TuneState{}
			}
			return console.TuneState{
				Depth:   engConcrete.Depth(),
				HashMB:  engConcrete.HashMB(),
				Threads: engConcrete.Threads(),
				MultiPV: engConcrete.MultiPV(),
				Ready:   srv.EngineState() == engine.StateReady,
			}
		},
		TunerSet: func(kind string, v int) string {
			if engConcrete == nil {
				return "引擎未启用"
			}
			// 走 TuneEngine 统一通道：校验+应用+reSearchLatest+落盘回调
			//（SetTunePersist，切换记住选择 2026-10-18）都在那一层完成
			if err := srv.TuneEngine(kind, v); err != nil {
				return err.Error()
			}
			logger.Info("面板调节引擎算力", "kind", kind, "value", v)
			return ""
		},
		TunerClearHash: func() string {
			if engConcrete == nil {
				return "引擎未启用"
			}
			if err := srv.ClearHash(); err != nil {
				return err.Error()
			}
			return ""
		},
		// 防火墙横幅三回调：Status 空串 = 不告警；Allow 空串 = 已受理
		//（UAC 异步，复查结果经 fw 内部状态在后续刷新周期体现）
		FirewallStatus: func() string {
			return fw.Status(fwPath)
		},
		FirewallAllow: func() string {
			return fw.Allow(logger)
		},
		FirewallDismiss: func() {
			fw.DismissFirewall(fwPath, logger)
		},
	})

	// fyne 消息循环已退出（托盘退出/服务异常联动）：等全链收尾后进程自然终止
	return awaitShutdown(nil, false)
}
