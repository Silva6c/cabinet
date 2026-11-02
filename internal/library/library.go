// Package library 是库目录管理器：监听配置的库目录（books_dir，默认
// library/books），自动解压 .obk.zip、在库文件增删/更新时热重建
// book.Store 并回调（server 无缝切换，不重启服务）。
package library

import (
	"archive/zip"
	"context"
	"fmt"
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
	// order 用户自定义优先级（库名列表，首位最先查）；disabled 禁用库名集合。
	// 来源 = 配置 library.order/library.disabled，SetOrdering 热更新；
	// reload 与 Books() 读它们须持 mu（fsnotify goroutine 与控制台回调并发）。
	order    []string
	disabled map[string]bool

	// watchReady Run 建立 fsnotify 监听后关闭的一次性信号。inotify/热加载
	// 语义：Add 之前落在目录里的文件不会产生事件（Linux inotify 不回放），
	// 调用方须等本信号再写文件才能保证被感知（测试同步点；生产毫秒级窗口
	// 内的漏事件由 reload 全量 unzipAll 在下次事件时自愈，无实际影响）。
	// readyOnce 保证 Run 只 close 一次，可安全并发等待。
	watchReady chan struct{}
	readyOnce  sync.Once
}

// New 创建管理器并立即做一次初始加载。order/disabled 为初始优先级与禁用
// 名单（语义同 SetOrdering；调用方传配置初值）。
func New(dir string, order, disabled []string, logger *slog.Logger, onChange func(*book.Store)) (*Manager, error) {
	m := &Manager{
		dir:        dir,
		log:        logger,
		onChange:   onChange,
		watchReady: make(chan struct{}),
		disabled:   map[string]bool{},
	}
	m.setOrderingLocked(order, disabled)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	if err := m.reloadEx("初始加载", nil); err != nil {
		return m, err
	}
	return m, nil
}

// SetOrdering 热更新库优先级与禁用名单并触发重载。order 为库名（文件名去
// .obk，与 BookInfo.Name 同口径）按优先级排列；disabled 为禁用库名。目录里
// 有但不在 order 中的库按文件名字典序追加尾部（新放入的库自动可用、排在
// 已有顺序之后）。控制台调整后由 main 回调调用（改配置 + 落盘在 main 侧）。
func (m *Manager) SetOrdering(order, disabled []string) error {
	m.mu.Lock()
	m.setOrderingLocked(order, disabled)
	m.mu.Unlock()
	return m.reloadEx("优先级/启停更新", nil)
}

// setOrderingLocked 写入排序状态（调用方须持 m.mu）。
func (m *Manager) setOrderingLocked(order, disabled []string) {
	m.order = append([]string(nil), order...)
	m.disabled = make(map[string]bool, len(disabled))
	for _, n := range disabled {
		m.disabled[n] = true
	}
}

// bookNameOf 库名 = 文件名去 .obk 后缀。后缀剥离必须大小写不敏感：Windows
// 文件系统不区分大小写，X.OBK 是合法库文件，TrimSuffix(".obk") 会剥不掉
// （落成 "X.OBK" 名字，配置 order/禁用/删除全部错位）——全仓库名口径统一走此函数。
func bookNameOf(p string) string {
	base := filepath.Base(p)
	return strings.TrimSuffix(base, filepath.Ext(base))
}

// sortBookPaths 把库路径按用户优先级排序：order 中出现的按其位次在前，
// 未出现的按文件名字典序追加尾部。只排序不剔除——禁用过滤由调用方做
// （reload 剔除禁用不加载；Books() 保留禁用仅展示）。order 匹配用库名
// （文件名去 .obk 后缀，与 BookInfo.Name 同口径）。
func sortBookPaths(paths []string, order []string) []string {
	rank := make(map[string]int, len(order))
	for i, n := range order {
		rank[n] = i
	}
	type item struct {
		path string
		rank int // order 位次；未列入 = len(order)（尾部区），区内按名字排序
	}
	items := make([]item, 0, len(paths))
	for _, p := range paths {
		name := bookNameOf(p)
		r, ok := rank[name]
		if !ok {
			r = len(order)
		}
		items = append(items, item{path: p, rank: r})
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].rank != items[j].rank {
			return items[i].rank < items[j].rank
		}
		return items[i].path < items[j].path
	})
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.path)
	}
	return out
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
	// Enabled 是否参与查询（用户禁用 = 只展示不加载，Entries 恒 0）
	Enabled bool
	// Status 非空 = 打开失败原因（坏库警示：该库不进查询链，控制台显示）
	Status string
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

// Books 当前目录内全部 .obk 的展示快照（按用户优先级排序 = 查询顺序，
// 未列入 order 的按文件名字典序排尾部；禁用库照常列出标记 Enabled=false）。
// 条数来自当前 Store（打开时缓存的 count，禁用/坏库无条数显示 0）；文件
// 修改时间现场 Stat，反映热加载参照；Status 来自 Store.Errors()。
func (m *Manager) Books() []BookInfo {
	m.mu.Lock()
	order := m.order
	disabled := m.disabled
	m.mu.Unlock()

	entries := map[string]int{}
	failed := map[string]string{}
	if st := m.Store(); st != nil {
		for _, s := range st.Stats() {
			entries[s.Name] = s.Entries
		}
		for _, e := range st.Errors() {
			failed[e.Name] = e.Err
		}
	}
	// 目录枚举 + EqualFold 过滤（Glob 模式大小写敏感，X.OBK 会被静默漏掉）
	ents, err := os.ReadDir(m.dir)
	if err != nil {
		return nil
	}
	var paths []string
	for _, ent := range ents {
		if !ent.IsDir() && strings.EqualFold(filepath.Ext(ent.Name()), ".obk") {
			paths = append(paths, filepath.Join(m.dir, ent.Name()))
		}
	}
	out := make([]BookInfo, 0, len(paths))
	for _, p := range sortBookPaths(paths, order) {
		name := bookNameOf(p)
		info := BookInfo{
			Name:    name,
			Entries: entries[name],
			Enabled: !disabled[name],
			Status:  failed[name],
		}
		if fi, err := os.Stat(p); err == nil {
			info.ModTime = fi.ModTime()
		}
		out = append(out, info)
	}
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
			if err := m.reloadEx("目录变更", nil); err != nil && m.log != nil {
				m.log.Warn("库重载失败", "err", err)
			}
		})
	}

	for {
		select {
		case <-ctx.Done():
			// 停掉在途防抖：放任触发会在 Close 置 store=nil 后重新打开全部库
			//（新 Store 无人关闭=句柄泄漏）并在服务收尾后再回调 onChange——
			// "Close 后 Store() 返回 nil"契约由此破坏
			mu.Lock()
			if debounce != nil {
				debounce.Stop()
				debounce = nil
			}
			mu.Unlock()
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

// reloadEx 重建库集合：解压 zip → 关旧 → 开新 → 回调；exclude 中的库名（文件
// 名去 .obk）即使文件在目录里也不进本轮 Store（RemoveBook 删除前卸载专用：
// Windows 上被打开的 SQLite 文件删不掉，须先关连接再删文件）。
func (m *Manager) reloadEx(reason string, exclude map[string]bool) error {
	if err := m.unzipAll(); err != nil && m.log != nil {
		m.log.Warn("zip 批量解压部分失败", "err", err)
	}

	// 目录枚举 + 后缀过滤（EqualFold）：filepath.Glob 的模式匹配全平台大小写
	// 敏感，"*.obk" 会静默漏掉手工放入的 X.OBK（Windows 文件系统本身不区分
	// 大小写，用户视角就是"库里看不到"）——入库集合以 EqualFold 过滤为准
	ents, err := os.ReadDir(m.dir)
	if err != nil {
		return err
	}
	var clean []string
	for _, ent := range ents {
		if ent.IsDir() || !strings.EqualFold(filepath.Ext(ent.Name()), ".obk") {
			continue
		}
		clean = append(clean, filepath.Join(m.dir, ent.Name()))
	}
	// 多库优先级（2026-11-01 用户定稿：控制台可调）：用户 order 位次在前、
	// 未列入的字典序排尾部，禁用库剔除不加载（ Books() 仍展示）。原"纯字典序
	// 即优先级"的约定自此升级——文件名字典序只决定尾部区次序。
	m.mu.Lock()
	ordered := sortBookPaths(clean, m.order)
	enabled := make([]string, 0, len(ordered))
	for _, p := range ordered {
		name := bookNameOf(p)
		if m.disabled[name] || exclude[name] {
			continue
		}
		enabled = append(enabled, p)
	}
	m.mu.Unlock()
	store, err := book.OpenStore(m.log, enabled...)
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

// RemoveBook 删除库（2026-11-02 控制台"删除"按钮）：先卸载（重建 Store 时
// 排除该库、关其连接——Windows 上被打开的 SQLite 文件删不掉），再删库文件与
// 全部同名附属（-wal/-shm 与同名 .obk.zip——zip 必须一并删，否则下轮 reload
// 的 unzipAll 会重新解压出库文件"复活"）。删除失败立即恢复重载（文件还在就
// 照常加载，不把"删了一半"留成常态）。成功后不再补 reload：卸载轮的 Store
// 已等价于删除后的目录集合，UI 与查询即刻一致，zip 收尾由 fsnotify 的防抖
// reload 自然收敛。name 必须是裸库名（文件名去 .obk），带路径分隔符或 ..
// 一律拒绝（防穿越，与 unzip 同语言）。
func (m *Manager) RemoveBook(name string) error {
	if name == "" || strings.ContainsAny(name, `/\`) ||
		strings.Contains(name, "..") {
		return fmt.Errorf("非法库名: %q", name)
	}
	// 纵深复核：拼出的每个删除目标都必须真落在库目录内（未来 name 语义或
	// 目录约定变化时，这道闸仍然挡在文件系统前面）
	for _, suf := range []string{".obk", ".obk-wal", ".obk-shm", ".obk.zip"} {
		rel, err := filepath.Rel(m.dir, filepath.Join(m.dir, name+suf))
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
			return fmt.Errorf("删除目标越出库目录: %q", name)
		}
	}

	// 先卸载：旧 Store 关闭后文件句柄释放，删除才可能成功（Windows 文件锁）
	if err := m.reloadEx("删除前卸载", map[string]bool{name: true}); err != nil {
		return err
	}
	var firstErr error
	for _, suf := range []string{".obk", ".obk-wal", ".obk-shm", ".obk.zip"} {
		if err := os.Remove(filepath.Join(m.dir, name+suf)); err != nil &&
			!os.IsNotExist(err) && firstErr == nil {
			firstErr = err
		}
	}
	if firstErr != nil {
		if err := m.reloadEx("删除失败恢复", nil); err != nil && m.log != nil {
			m.log.Warn("删除失败后的恢复重载也失败", "err", err)
		}
		return fmt.Errorf("删除库文件失败（已恢复该库）: %w", firstErr)
	}
	if m.log != nil {
		m.log.Info("库已删除", "book", name)
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
		// 显式边界复核（防穿越第二道闸）：目标路径相对目标目录不得带 .. 前缀。
		// 上方 Base 净化已保证落盘恒在 dir 内，这道闸是纵深——未来有人把
		// Base 净化改掉时它仍在（CodeQL 的 go/zipslip 不识别 Base 净化，
		// 同时靠行内抑制收口）
		dest := filepath.Join(dir, name)
		if rel, rerr := filepath.Rel(dir, dest); rerr != nil ||
			rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
			return fmt.Errorf("zip 条目路径越界: %q", f.Name)
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		out, err := os.Create(dest) // codeql[go/zipslip] 双重防穿越：Base 净化 + Rel 边界复核
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
