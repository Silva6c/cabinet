// cabinet 是"内阁"电脑端后台服务的入口：
// WSS 服务端 + 自签证书 + 令牌鉴权 + 皮卡鱼引擎桥 + 桔库（热加载）+ 沉淀 + 托盘。
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
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
	"cabinet/internal/update"
)

func main() {
	// 更新残留清理与中断自愈（绿色软件零残留）：换到一半断电/杀软拦截的场景
	// 在此恢复原程序。放在单实例锁之前——恢复与本次是否双开无关。
	if dir, name, err := update.ExeSelf(); err == nil {
		if update.CleanupOnStart(dir, name) {
			fmt.Println("检测到上次更新中断，已恢复原程序")
		}
	}

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
				Path:       cfg.Engine.Path,
				NNUE:       nnue,
				Threads:    cfg.Engine.Threads,
				HashMB:     cfg.Engine.HashMB,
				MultiPV:    cfg.Engine.MultiPV,
				Depth:      cfg.Engine.Depth,
				MoveTimeMS: cfg.Engine.MoveTimeMS,
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

	// 配置落盘互斥（唯一一把）：凡"改 cfg 字段 + 快照 Save(configPath)"的落盘
	// 链必须全程持它——Config.Save 的临时文件名固定为 configPath+".tmp"，若
	// 不同链各持一把锁，并发时两路会对同一个 .tmp 交错 Create/写/rename，toml
	// 被写坏后下次启动 Load 失败服务起不来。现有两条链：算力调参（下方
	// SetTunePersist 回调，手机 WSS 读 goroutine 与 fyne UI 并发进入）与控制台
	// 三开关（云库/云库优先/不走任何库，fyne UI goroutine，见 console.Run 选项）。
	// 锁 + Config.Save 原子写（tmp+rename）双保险。
	var configPersistMu sync.Mutex
	// 算力参数落盘（2026-10-18）：悬浮窗/面板切换后记住选择，重启以落盘
	// 值为准。⚠必须走快照 + UnresolvePaths：直接 Save 内存态会把
	// ResolvePaths 固化的绝对路径写回 toml、破坏便携性（root.go 铁律）；
	// 内存态同步更新使下次落盘基于新值。回调不依赖沉淀库，注册在 if st != nil
	// 之外——沉淀关闭（store.enable=false）时调参同样要能记住选择，否则
	// 悬浮窗/面板调的参数重启即被 toml 旧值打回（tune.go"重启后以落盘值为准"）。
	// ⚠并发契约：本回调可从手机 WSS 读 goroutine（每个连接一个，支持多手机）
	// 与 fyne UI goroutine（面板 TunerSet）并发进入，必须全程持 configPersistMu
	// ——否则 cfg 字段写与 snapshot 全字段读构成数据竞争，且与控制台三开关
	// 的落盘链在同一个 .tmp 上交错写坏文件。
	srv.SetTunePersist(func(kind string, v int) {
		configPersistMu.Lock()
		defer configPersistMu.Unlock()
		snapshot := *cfg
		switch kind {
		case "depth":
			snapshot.Engine.Depth = v
			cfg.Engine.Depth = v
		case "movetime":
			snapshot.Engine.MoveTimeMS = v
			cfg.Engine.MoveTimeMS = v
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

	// 更新检查器（免 token 匿名查 GitHub releases；仓库已公开，直查生效——
	// 共享代理出口下匿名配额可能间歇 403，失败静默可重试）。启动自检默认关
	// （console-state.json 持久化），开则异步查一次——不轮询，下次启动再查；
	// 结果在控制台"检查更新"面板查看。
	upd := update.New("Silva6c/cabinet", server.Version, logger)
	if cs, err := loadConsoleState(fwPath); err == nil && cs.UpdateAutoCheck {
		go upd.Check(ctx)
	}

	// 上一版备份延迟清理：服务成功运行 60s 后删 cabinet.exe.old（启动早期
	// 保留回退手段——新 exe 若启动即崩，用户仍可从 .old 手动恢复）。
	if exeDir, exeName, e := update.ExeSelf(); e == nil {
		go func() {
			time.Sleep(60 * time.Second)
			update.RemoveOldBackup(exeDir, exeName)
		}()
	}

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

	// finishShutdown 收尾 + 更新接力：若更新已就绪（用户点了"下载并安装"，
	// 新 exe 已校验并解出为 cabinet.exe.new），在服务与引擎全部停止后换入并
	// 拉起新进程。换入失败已由 Apply 内部回滚——本进程映像在内存，文件系统
	// 变化不影响继续以旧版本收尾。
	finishShutdown := func(srvR error, haveSrvR bool) error {
		runErr := awaitShutdown(srvR, haveSrvR)
		if upd.Snapshot().State != update.StateReadyReboot {
			return runErr
		}
		dir, exeName, err := update.ExeSelf()
		if err != nil {
			logger.Error("更新换入失败：无法定位当前程序", "err", err)
			return runErr
		}
		if err := update.Apply(dir, exeName); err != nil {
			logger.Error("更新换入失败（已回滚），保持当前版本", "err", err)
			return runErr
		}
		logger.Info("更新换入完成，拉起新版本", "tag", upd.Snapshot().LatestTag)
		if err := relaunchSelf(dir, exeName); err != nil {
			logger.Error("新程序拉起失败——新版本已就位，请手动启动", "err", err)
		}
		return runErr
	}

	if noTray {
		err := <-srvErr // -no-tray：信号（Ctrl+C）→ ctx cancel → Run 返回（无窗口无托盘纯服务）
		return finishShutdown(err, true)
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
	// 三个开关回调的落盘互斥共用上方 configPersistMu（与算力落盘同一把锁：
	// Save 的 .tmp 文件名固定，各持一把锁并发时会交错写坏 toml）
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
				MoveTimeMS: s.Engine.MoveTimeMS, SearchMS: s.Engine.SearchMS,
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
		TrendSeries: func(devKey string) console.TrendSnapshot {
			// 空键 = 焦点设备（最新上报 position 的来源，单设备常态）；分组模型
			// 见 server trend.go——每设备一条序列，互不混线
			game, pts := srv.TrendSnapshotFor(devKey)
			out := make([]console.TrendPoint, len(pts))
			for i, p := range pts {
				out[i] = console.TrendPoint{
					TsMs: p.TsMs, ScoreCP: p.ScoreCP, WinRate: p.WinRate,
					MyRed: p.MyRed, MySideKnown: p.MySideKnown,
				}
			}
			return console.TrendSnapshot{GameID: game, Points: out}
		},
		TrendDevices: func() []console.TrendDev {
			infos := srv.TrendDevices() // 有曲线的设备组，最近活跃在前
			out := make([]console.TrendDev, len(infos))
			for i, d := range infos {
				out[i] = console.TrendDev{Key: d.Key, Label: d.Label}
			}
			return out
		},
		CloudBookEndpoint: cfg.CloudBook.Endpoint,
		CloudBookGet:      srv.CloudBookEnabled,
		CloudBookSet: func(on bool) string {
			// 立即生效（服务端原子位）+ 落盘 toml（快照 + UnresolvePaths，
			// 与算力落盘同款便携性铁律）
			configPersistMu.Lock()
			defer configPersistMu.Unlock()
			srv.SetCloudBookEnable(on)
			cfg.CloudBook.Enable = on
			snapshot := *cfg
			snapshot.UnresolvePaths(resourceRoot)
			if err := snapshot.Save(configPath); err != nil {
				logger.Warn("云库开关落盘失败", "enable", on, "err", err)
				return fmt.Sprintf("已切换但落盘失败: %v", err)
			}
			logger.Info("云库开关已切换", "enable", on)
			return ""
		},
		// 默认走云库（2026-10-30）：高级面板→开局库的勾选框；语义矩阵见
		// cloudLookupFirst——开启后云库命中即终（不查本地不进引擎）
		CloudFirstGet: srv.CloudFirstEnabled,
		CloudFirstSet: func(on bool) string {
			configPersistMu.Lock()
			defer configPersistMu.Unlock()
			srv.SetCloudFirst(on)
			cfg.CloudBook.CloudFirst = on
			snapshot := *cfg
			snapshot.UnresolvePaths(resourceRoot)
			if err := snapshot.Save(configPath); err != nil {
				logger.Warn("云库优先开关落盘失败", "cloud_first", on, "err", err)
				return fmt.Sprintf("已切换但落盘失败: %v", err)
			}
			logger.Info("云库优先开关已切换", "cloud_first", on)
			return ""
		},
		// 不走任何库（2026-10-30）：高级面板→开局库的勾选框；开启后建议链跳过
		// 桔库/沉淀/云库（含 cloud_first），每步引擎直搜——拦截位置与语义见
		// handlePosition
		SkipBooksGet: srv.SkipBooksEnabled,
		SkipBooksSet: func(on bool) string {
			configPersistMu.Lock()
			defer configPersistMu.Unlock()
			srv.SetSkipBooks(on)
			cfg.Engine.SkipBooks = on
			snapshot := *cfg
			snapshot.UnresolvePaths(resourceRoot)
			if err := snapshot.Save(configPath); err != nil {
				logger.Warn("不走任何库开关落盘失败", "skip_books", on, "err", err)
				return fmt.Sprintf("已切换但落盘失败: %v", err)
			}
			logger.Info("不走任何库开关已切换", "skip_books", on)
			return ""
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
				Depth:      engConcrete.Depth(),
				MoveTimeMs: engConcrete.MoveTimeMS(),
				HashMB:     engConcrete.HashMB(),
				Threads:    engConcrete.Threads(),
				MultiPV:    engConcrete.MultiPV(),
				Ready:      srv.EngineState() == engine.StateReady,
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
		// 检查更新六回调：快照只读内存（刷新循环禁 HTTP 铁律）；下载安装 =
		// 用户确认（点击即授权），就绪后走 stop() 优雅关闭 → finishShutdown
		// 换入接力重启。换入/回滚/残留清理细节见 internal/update 包。
		UpdateSnapshot: upd.Snapshot,
		UpdateCheck: func() string {
			if msg := upd.CanCheck(); msg != "" {
				return msg
			}
			go upd.Check(ctx)
			return ""
		},
		UpdateDownloadInstall: func() string {
			if !upd.CanInstall() {
				return "当前没有可安装的更新（请先检查更新）"
			}
			go func() {
				upd.DownloadInstall(ctx)
				if upd.Snapshot().State == update.StateReadyReboot {
					logger.Info("更新就绪，触发重启应用")
					stop() // 优雅关闭全链 → finishShutdown 换入接力
				}
			}()
			return ""
		},
		UpdateCancel: upd.Cancel,
		UpdateAutoCheckGet: func() bool {
			cs, err := loadConsoleState(fwPath)
			return err == nil && cs.UpdateAutoCheck
		},
		UpdateAutoCheckSet: func(on bool) string {
			if err := setConsoleStateAutoCheck(fwPath, on); err != nil {
				return err.Error()
			}
			logger.Info("启动自检更新开关已更新", "on", on)
			return ""
		},
	})

	// fyne 消息循环已退出（托盘退出/服务异常联动）：等全链收尾后进程自然终止
	return finishShutdown(nil, false)
}

// relaunchSelf 拉起新版本进程：带 CABINET_UPDATE_RELAUNCH=1 标记，触发
// 单实例锁的短重试窗口（新旧进程交接竞态保护，正常双开行为不变）。
func relaunchSelf(dir, exeName string) error {
	cmd := exec.Command(filepath.Join(dir, exeName))
	cmd.Env = append(os.Environ(), "CABINET_UPDATE_RELAUNCH=1")
	cmd.Dir = dir
	cmd.Stdout = os.Stdout // console 构建的调试输出接力；windowsgui 下无控制台无碍
	cmd.Stderr = os.Stderr
	return cmd.Start()
}
