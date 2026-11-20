// Package memwatch 内存计量：Go 堆与进程工作集快照 + 定期日志 + 堆 profile。
// 2026-11-07 用户反馈"刚打开只吃几百 MB，久开可能 2GB"——现有代码排查无
// 确定性泄漏点（缓存全封顶、建议流 50 条、日志尾部有界、走势按局存 PC 端
// 有界），先铺计量表拿数据再定修复方向：堆涨 = Go 对象问题；堆平而工作集
// 涨 = fyne 纹理 / GPU / cgo 方向；两者都平而任务管理器涨 = 皮卡鱼子进程
// （Hash 配置）或误把子进程算进父进程。控制台日志面板同步显示实时计量。
// 2026-11-08 计量数据实锤 70 分钟堆涨 1.37GB 且代码级泄漏为零——升级为
// "增长即 dump"：堆活对象较上次 dump 涨超阈值时写 heap/goroutine profile
// 到 logs/mem/（滚动保留 2 份），下次复现直接 pprof 定位分配热点。
package memwatch

import (
	"compress/gzip"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"runtime/pprof"
	"strconv"
	"time"
)

// dumpGrowMB 触发 heap profile 的堆在用内存增量阈值（相对上次 dump）。
// 2026-11-09 与 GC 调参（GOGC=50/GOMEMLIMIT=800MB）联动：新稳态水位 ~190MB
// 空载/≤800MB 对局，增长台阶变小——256→128（更早落证），口径改 HeapInuse
// （GOMEMLIMIT 下 HeapAlloc 被连续 GC 压平会漏报增长）。
const dumpGrowMB = 128

// dumpAtLimitMB 逼近内存软上限即 dump 的水位（GOMEMLIMIT 800MB 的 80%，
// 与 cmd/cabinet/main.go 的 SetMemoryLimit 联动——改 limit 须同步此值）。
const dumpAtLimitMB = 640

// 空闲归还参数（见 WatchLoop 内注释）：
const (
	// idleFlatMB 两期间堆水位变化小于此值 = 分配停滞
	idleFlatMB = 10
	// idleFlatGC 两期间 GC 轮次增量上限（停滞期 GC 不会高频跑）
	idleFlatGC = 1
	// idleReleaseFloorMB 低于此堆水位不值得做归还（full GC 代价换不回收益）
	idleReleaseFloorMB = 120
	// releaseFloorHysteresis 归还后的动态地板系数（归还后水位 ×1.2 为新地板，
	// 防"活集合本大"场景下每 2 期空转一次 full GC）
	releaseFloorHysteresis float64 = 1.2
	// GOGCActive/GOGCIdle 动态档位（2026-11-09）：活跃期/空闲期两档——
	// main 启动时以 GOGCActive 预置（SetGCPercent），空闲降档/活跃恢复由
	// WatchLoop 切换；GOGCIdle 只降 GOGC 不降 limit（对局期残留 limit =
	// GC 绞杀尾险，见 WatchLoop 注释）
	GOGCActive = 50
	GOGCIdle   = 20
)

// ActivityProbe 活跃信号钩子（main 接线：server.RecentPositionWithin 的
// 闭包）——对局期不得滞留低 GC 档位，10 分钟采样粒度的滞后窗由它当期
// 消解。nil = 未接线（按非活跃处理，仅影响降档时机不影响正确性）
var ActivityProbe func() bool

// SkipGCTuning GC 调参逃生口（2026-11-09 体检 B2）：用户显式设置了
// GOGC/GOMEMLIMIT 环境变量时 main 置位——WatchLoop 跳过动态档位切换，
// 否则"运维逃生口"承诺会在首个空闲期被 SetGCPercent 静默吞掉
var SkipGCTuning = false

// MemInfo 一次内存计量快照（单位 MB，便于日志与 UI 同口径显示）。
type MemInfo struct {
	// HeapAllocMB Go 堆"活对象+未回收垃圾"字节（runtime 语义）
	HeapAllocMB float64
	// HeapInuseMB 正在使用的 span 字节——与 HeapAlloc 的差值可粗分
	// "垃圾"与"活对象"（2026-11-08 内存排查：区分两者是定位关键）
	HeapInuseMB float64
	// HeapObjects 堆活对象个数
	HeapObjects uint64
	// HeapSysMB Go 堆向 OS 申请的总量（含已释放未归还的空洞）
	HeapSysMB float64
	// SysMB 进程内 Go 运行时向 OS 映射的总量（堆+栈+元数据）
	SysMB float64
	// WorkingSetMB 进程物理内存工作集（含非 Go 分配：fyne 纹理/cgo/引擎桥）。
	// 平台不支持时为 0 且 WorkingSetOK=false
	WorkingSetMB float64
	WorkingSetOK bool
	// NumGC 累计 GC 轮次（判断"涨了有没有被 GC 收走"的辅助线索）
	NumGC uint32
}

// Take 采集一次当前内存计量。绝不返回错误——计量是诊断辅助，
// 任何平台差异都按"缺省项"处理而非失败。
func Take() MemInfo {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	s := MemInfo{
		HeapAllocMB: float64(ms.HeapAlloc) / (1 << 20),
		HeapInuseMB: float64(ms.HeapInuse) / (1 << 20),
		HeapObjects: ms.HeapObjects,
		HeapSysMB:   float64(ms.HeapSys) / (1 << 20),
		SysMB:       float64(ms.Sys) / (1 << 20),
		NumGC:       ms.NumGC,
	}
	if ws, ok := workingSetBytes(); ok {
		s.WorkingSetMB = float64(ws) / (1 << 20)
		s.WorkingSetOK = true
	}
	return s
}

// FormatShort UI 摘要行（日志面板内存计量小字）："内存 堆 128 · 进程 412 MB"。
// 工作集不可得的平台进程段显示"—"。
func FormatShort(s MemInfo) string {
	ws := "—"
	if s.WorkingSetOK {
		ws = strconv.FormatFloat(s.WorkingSetMB, 'f', 0, 64)
	}
	return "内存 堆 " + strconv.FormatFloat(s.HeapAllocMB, 'f', 0, 64) +
		" · 进程 " + ws + " MB"
}

// WatchLoop 每 interval 打一条 INFO 计量日志直到 ctx 取消。启动即打一条
// 基线（对比"刚打开"与"久开"的锚点）。放独立 goroutine 由 main 启动。
// dumpDir 非空时（logs/mem/）：堆活对象较上次 dump 涨超 dumpGrowMB 即写
// heap/goroutine profile（滚动保留 2 份）——2026-11-08 内存排查结论：代码
// 级泄漏为零、增长需 profile 落证，本函数是下次复现时的证据链入口。
func WatchLoop(ctx context.Context, interval time.Duration, dumpDir string) {
	base := Take()
	slog.Info("内存计量启动（基线）",
		"heap_alloc_mb", round1(base.HeapAllocMB),
		"working_set_mb", base.workingSetArg(),
		"interval", interval.String())
	t := time.NewTicker(interval)
	defer t.Stop()
	lastDumpedMB := base.HeapAllocMB              // dump 增量基准（启动即基线，不追历史）
	prevAllocMB := base.HeapAllocMB               // 空闲判定：上期堆水位
	prevGC := base.NumGC                          // 空闲判定：上期 GC 轮次
	idleStreak := 0                               // 连续"分配停滞"期数（连续 2 期才触发归还）
	releaseFloorMB := float64(idleReleaseFloorMB) // 归还触发地板（归还后动态抬升防空转）
	gcLow := false                                // 当前是否处于低 GC 档位（见 GOGCIdle/动态切换）
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s := Take()
			args := []any{
				"heap_alloc_mb", round1(s.HeapAllocMB),
				"heap_inuse_mb", round1(s.HeapInuseMB),
				"heap_objects", s.HeapObjects,
				"heap_sys_mb", round1(s.HeapSysMB),
				"sys_mb", round1(s.SysMB),
				"num_gc", s.NumGC,
			}
			if s.WorkingSetOK {
				args = append(args, "working_set_mb", round1(s.WorkingSetMB))
			}
			slog.Info("内存计量", args...)
			// dump 触发（两条件任一，2026-11-09 与 GC 调参联动改口径）：
			// ① 堆在用内存较上次 dump 涨超 dumpGrowMB（GOMEMLIMIT 下 HeapAlloc
			//   被连续 GC 压平、会漏报增长，HeapInuse 是真实占用水位）；
			// ② HeapAlloc 超过内存软上限的 80%（逼近 limit=GC 连续回收中，
			//   正是最需要 pprof 证据的时刻）。
			// dump 前 GC 一次再取快照（heap profile 反映"上次 GC 时刻"的活
			// 集合，不 GC 会漏窗口期新增）；诊断旁路，STW 感知为零。dump 后
			// 重置基准——连续增长时每个台阶都有落证
			if dumpDir != "" && (s.HeapInuseMB-lastDumpedMB > dumpGrowMB || s.HeapAllocMB > dumpAtLimitMB) {
				if err := dumpProfiles(dumpDir); err != nil {
					slog.Warn("内存 profile 落盘失败", "dir", dumpDir, "err", err)
				} else {
					slog.Info("堆增长超阈值，已落内存 profile", "growth_mb", round1(s.HeapInuseMB-lastDumpedMB))
					lastDumpedMB = s.HeapInuseMB
				}
			}
			// 空闲归还（2026-11-09 用户定稿"内存压缩做到极致"）：分配停滞连续
			// 2 期（对局结束/暂停期）且堆仍高 → FreeOSMemory（full GC +
			// scavenger 全量归还 OS，commit 真降）+ 工作集修剪（显示层数字
			// 同步下降——与"电脑管家 EmptyWorkingSet"同款，但由进程自己、
			// 只在空闲期做，规避对局期缺页开销）。对局期判据自然不满足
			//（分配活跃），零干扰
			delta := s.HeapAllocMB - prevAllocMB
			if delta < 0 {
				delta = -delta
			}
			// 活跃信号优先（对局期不得滞留低 GC 档位——GOGC=20 下每 ~25MB
			// 分配就一轮 GC，fyne 渲染帧预算与 enginePump 都会感受到 CPU
			// 抬升；10 分钟滞后窗由 probe 当期消解）
			if ActivityProbe != nil && ActivityProbe() {
				idleStreak = 0
			} else if delta < idleFlatMB && s.NumGC-prevGC <= idleFlatGC {
				idleStreak++
			} else {
				idleStreak = 0
			}
			// GC 档位动态切换（2026-11-09 内存治理；SkipGCTuning 时跳过——
			// 用户显式设 env 的逃生口优先）：空闲归还时顺手降档
			// GOGC 50→20（空载平台 1.5×L→1.2×L）；活跃/非停滞即恢复 50。
			// ⚠只动 GOGC 不动 SetMemoryLimit——limit 降到 400 残留进对局期
			// 是"GC 绞杀直到下个 tick"的死亡螺旋型尾险，收益（空载本无
			// 分配）趋零，负期望。SetGCPercent 文档明确任意 goroutine 可调
			if SkipGCTuning {
				// 逃生口生效：用户环境变量说了算，动态档位整体不介入
				gcLow = false
			} else if idleStreak >= 2 {
				if !gcLow {
					prev := debug.SetGCPercent(GOGCIdle)
					gcLow = true
					slog.Info("空闲降档 GOGC", "from", prev, "to", GOGCIdle)
				}
			} else if gcLow {
				gcLow = false
				prev := debug.SetGCPercent(GOGCActive)
				slog.Info("活跃恢复 GOGC", "from", prev, "to", GOGCActive)
			}
			if idleStreak >= 2 && s.HeapAllocMB > releaseFloorMB {
				before := s
				debug.FreeOSMemory() // full GC + scavenger 立即归还
				idleStreak = 0       // 归还后重置（下一期重新累计）
				after := Take()
				if trimWorkingSet() {
					after = Take() // 修剪后再取一次工作集读数
				}
				slog.Info("空闲归还完成",
					"before_heap_mb", round1(before.HeapAllocMB),
					"after_heap_mb", round1(after.HeapAllocMB),
					"before_ws_mb", before.workingSetArg(),
					"after_ws_mb", after.workingSetArg())
				// 动态地板（防周期性空转 full GC）：归还后若活集合本身仍高
				//（如对局结束残留的缓存类活对象），以"归还后水位 +20%"为新
				// 地板——堆没涨回这个量就不再归还，白付 full GC；回落到
				// 静态地板以下（长期低位）则恢复静态值
				releaseFloorMB = max(idleReleaseFloorMB, after.HeapAllocMB*releaseFloorHysteresis)
			}
			prevAllocMB = s.HeapAllocMB
			prevGC = s.NumGC
		}
	}
}

// dumpProfiles 写 heap（inuse_space 口径，pprof 直接可读）与 goroutine 两份
// profile 到 dir，gzip 压缩、分类型滚动保留各 2 份。诊断旁路：任何失败只
// 返回错误由调用方记日志，绝不影响主链路。
func dumpProfiles(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	runtime.GC()
	// 毫秒精度：防同秒 dump 互相覆盖（测试连调两次即暴露；真实 10 分钟
	// 间隔不触发，但口径上不该依赖间隔）
	stamp := time.Now().Format("20060102-150405.000")
	if err := writeProfileGz(filepath.Join(dir, "heap-"+stamp+".pprof.gz"), "heap", 0); err != nil {
		return err
	}
	if err := writeProfileGz(filepath.Join(dir, "goroutine-"+stamp+".txt.gz"), "goroutine", 1); err != nil {
		return err
	}
	pruneOldDumps(dir, "heap-", 2)
	pruneOldDumps(dir, "goroutine-", 2)
	return nil
}

// writeProfileGz 单份 profile 写盘（gzip）。临时文件+rename 原子落盘
// （2026-11-08 实录：4 次触发仅 2 份在案——半截文件与"写成功后被误删"
// 都排查过，原子写保证 in-案 文件完整可读；失败即不留残件）。
func writeProfileGz(path, name string, debug int) error {
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	gz := gzip.NewWriter(f)
	werr := pprof.Lookup(name).WriteTo(gz, debug)
	if cerr := gz.Close(); werr == nil {
		werr = cerr
	}
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		_ = os.Remove(tmp)
		return werr
	}
	return os.Rename(tmp, path)
}

// pruneOldDumps 分类型滚动清理（2026-11-09 修复：此前 "*-*.gz" 混排会把
// 上一轮的 goroutine/heap 互相顶掉——11-08 目录里 goroutine 全缺、heap
// 却成对遗留的直接原因）；时间戳命名天然有序，保留最新 keep 份。
func pruneOldDumps(dir, prefix string, keep int) {
	entries, err := filepath.Glob(filepath.Join(dir, prefix+"*.gz"))
	if err != nil || len(entries) <= keep {
		return
	}
	for _, old := range entries[:len(entries)-keep] {
		_ = os.Remove(old)
	}
}

func (s MemInfo) workingSetArg() any {
	if s.WorkingSetOK {
		return round1(s.WorkingSetMB)
	}
	return "不可用"
}

func round1(v float64) float64 {
	return float64(int(v*10+0.5)) / 10
}
