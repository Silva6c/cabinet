// Package library 是库目录管理器：监听配置的库目录（books_dir，默认
// library/books），自动解压 .obk.zip、在库文件增删/更新时热重建
// book.Store 并回调（server 无缝切换，不重启服务）。
package library

import (
	"archive/zip"
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"

	"cabinet/internal/book"
)

// Manager 库目录管理器。
type Manager struct {
	dir string
	log *slog.Logger

	mu    sync.Mutex
	store *book.Store
	// onChange 库集合重建完成后的回调（server.SetBook；nil 忽略）
	onChange func(*book.Store)

	// watchReady Run 建立 fsnotify 监听后关闭的一次性信号。inotify/热加载
	// 语义：Add 之前落在目录里的文件不会产生事件（Linux inotify 不回放），
	// 调用方须等本信号再写文件才能保证被感知（测试同步点；生产毫秒级窗口
	// 内的漏事件由 reload 全量 unzipAll 在下次事件时自愈，无实际影响）。
	// readyOnce 保证 Run 只 close 一次，可安全并发等待。
	watchReady chan struct{}
	readyOnce  sync.Once
}

// New 创建管理器并立即做一次初始加载。
func New(dir string, logger *slog.Logger, onChange func(*book.Store)) (*Manager, error) {
	m := &Manager{dir: dir, log: logger, onChange: onChange, watchReady: make(chan struct{})}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	if err := m.reload("初始加载"); err != nil {
		return m, err
	}
	return m, nil
}

// Store 当前库集合（热替换下始终可用）。
func (m *Manager) Store() *book.Store {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.store
}

// BookInfo 单库的展示快照（控制台库区一行）。
type BookInfo struct {
	Name    string    // 库名（文件名去 .obk）
	Entries int       // 库内总行数
	ModTime time.Time // 库文件修改时间（热加载状态参照）
}

// Close 关闭当前库集合并置空（进程收尾用）。Store() 此后返回 nil——
// 与"热替换下始终可用"的约定一致：Close 只应发生在服务停摆之后。
func (m *Manager) Close() {
	m.mu.Lock()
	old := m.store
	m.store = nil
	m.mu.Unlock()
	if old != nil {
		old.Close()
	}
}

// Books 当前目录内全部 .obk 的展示快照（按文件名排序）。
// 条数来自当前 Store（打开时缓存的 count）；文件修改时间现场 Stat，反映热加载参照。
func (m *Manager) Books() []BookInfo {
	entries := map[string]int{}
	if st := m.Store(); st != nil {
		for _, s := range st.Stats() {
			entries[s.Name] = s.Entries
		}
	}
	paths, err := filepath.Glob(filepath.Join(m.dir, "*.obk"))
	if err != nil {
		return nil
	}
	out := make([]BookInfo, 0, len(paths))
	for _, p := range paths {
		name := strings.TrimSuffix(filepath.Base(p), ".obk")
		info := BookInfo{Name: name, Entries: entries[name]}
		if fi, err := os.Stat(p); err == nil {
			info.ModTime = fi.ModTime()
		}
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Run 监听目录直至 ctx 取消：zip 解压 + 事件防抖重载。
func (m *Manager) Run(ctx context.Context) error {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer func() { _ = w.Close() }()
	if err := w.Add(m.dir); err != nil {
		return err
	}
	// 监听已就绪：此后落盘的文件事件必被感知（见 watchReady 注释）
	m.readyOnce.Do(func() { close(m.watchReady) })
	if m.log != nil {
		m.log.Info("库目录监听中", "dir", m.dir)
	}

	// 防抖 800ms 是"静默判据"：编辑器/解压器/下载器的连续写事件间隔通常小于
	// 它，静默达标即视为一轮变更结束。短了会把一轮变更拆成多次 reload（每次
	// 全量重开库），长了热加载滞后可感。解压写 .obk 的二级事件会再排一轮防抖，
	// 下一轮经 unzipIfNeeded 判"已最新"空转收敛，不担心无限循环。
	var (
		debounce *time.Timer
		mu       sync.Mutex
	)
	schedule := func() {
		mu.Lock()
		defer mu.Unlock()
		if debounce != nil {
			debounce.Stop()
		}
		debounce = time.AfterFunc(800*time.Millisecond, func() {
			if err := m.reload("目录变更"); err != nil && m.log != nil {
				m.log.Warn("库重载失败", "err", err)
			}
		})
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case ev, ok := <-w.Events:
			if !ok {
				return nil
			}
			if ev.Has(fsnotify.Create) || ev.Has(fsnotify.Write) || ev.Has(fsnotify.Remove) || ev.Has(fsnotify.Rename) {
				// zip 落地即解压（解压自身又触发事件 → 防抖合并）
				if strings.HasSuffix(strings.ToLower(ev.Name), ".obk.zip") {
					if err := m.unzipIfNeeded(ev.Name); err != nil && m.log != nil {
						m.log.Warn("zip 解压失败", "file", ev.Name, "err", err)
					}
				}
				schedule()
			}
		case err := <-w.Errors:
			if err != nil && m.log != nil {
				m.log.Warn("库目录监听错误", "err", err)
			}
		}
	}
}

// reload 重建库集合：解压 zip → 关旧 → 开新 → 回调。
func (m *Manager) reload(reason string) error {
	if err := m.unzipAll(); err != nil && m.log != nil {
		m.log.Warn("zip 批量解压部分失败", "err", err)
	}

	// 多库优先级在此悄悄成形：books_dir 是"一个目录"而非列表，Glob 按文件名
	// 字典序返回、直接决定 book.Store 的查询顺序（首库命中即短路）。想调库
	// 优先级只能改文件名——这是配置面语义，不要在代码里加权重或二次排序。
	paths, err := filepath.Glob(filepath.Join(m.dir, "*.obk"))
	if err != nil {
		return err
	}
	// 防御性后缀收口：Glob 模式已限定 *.obk，此处按后缀（大小写不敏感）再筛
	// 一道，防模式语义变化混入临时/杂项文件——真正的入库集合以它为准。
	var clean []string
	for _, p := range paths {
		if !strings.HasSuffix(strings.ToLower(p), ".obk") {
			continue
		}
		clean = append(clean, p)
	}
	store, err := book.OpenStore(m.log, clean...)
	if err != nil {
		return err
	}

	// 先换指针、后关旧库：任何时刻 Store() 返回的都是已打开可用的库。代价是
	// old.Close() 时握着旧引用的在途查询会收到 "database is closed"——按未命中
	// 走引擎兜底，属可接受降级；不得反过来先关旧再换新，那会出现一段返回
	// 已关闭库的空窗，期间所有查询直接报错。
	m.mu.Lock()
	old := m.store
	m.store = store
	m.mu.Unlock()
	if old != nil {
		old.Close()
	}
	if m.onChange != nil {
		m.onChange(store)
	}
	if m.log != nil {
		m.log.Info("库集合已重载", "reason", reason, "books", store.Len())
	}
	return nil
}

// unzipAll 解压目录下全部 .obk.zip（对应 .obk 已存在且不旧于 zip 时跳过）。
func (m *Manager) unzipAll() error {
	zips, err := filepath.Glob(filepath.Join(m.dir, "*.obk.zip"))
	if err != nil {
		return err
	}
	for _, z := range zips {
		if err := m.unzipIfNeeded(z); err != nil {
			return err
		}
	}
	return nil
}

// unzipIfNeeded zip 内的 SQLite 比现有 .obk 新（或 .obk 缺失）才解压。
//
// ⚠已知非原子：unzip 用 os.Create 原地截断覆盖旧 .obk，拷贝中途崩溃/断电
// 会把库写成半成品，且旧 Store 的只读句柄此时还开着该文件（reload 先解压
// 后关旧库）。接受此风险的前提是"配套 zip 是真源，坏了可重解压"；对无 zip
// 的裸 .obk 勿做任何覆盖写。要修就上"临时文件+rename"（Windows 上 rename
// 目标被占用会失败，需先关旧库句柄）。
func (m *Manager) unzipIfNeeded(zipPath string) error {
	out := strings.TrimSuffix(zipPath, ".zip")
	if st, err := os.Stat(out); err == nil {
		if zs, err2 := os.Stat(zipPath); err2 == nil && !zs.ModTime().After(st.ModTime()) {
			return nil // 已是最新
		}
	}
	return unzip(zipPath, m.dir)
}

// unzip 解压 zip 到 dir（只取其中的 SQLite 条目，跳过 macOS 垃圾目录）。
func unzip(zipPath, dir string) error {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer func() { _ = r.Close() }()
	for _, f := range r.File {
		// 拒绝路径穿越（条目按裸名落盘）；macOS 垃圾目录整族跳过——必须判
		// 全路径前缀而非 Base：__MACOSX/real.obk 的 Base 是 real.obk，会与
		// 真条目同名覆盖（2026-10-27 单测实证），仅点前缀过滤挡不住它
		if strings.HasPrefix(strings.ToLower(f.Name), "__macosx/") {
			continue
		}
		name := filepath.Base(f.Name)
		if name == "" || strings.HasPrefix(name, ".") {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		out, err := os.Create(filepath.Join(dir, name))
		if err != nil {
			_ = rc.Close()
			return err
		}
		_, err = io.Copy(out, rc)
		// 中间清理路径的 Close 错误无从上报（主错误以 io.Copy 为准），显式忽略
		_ = rc.Close()
		_ = out.Close()
		if err != nil {
			return err
		}
	}
	return nil
}
