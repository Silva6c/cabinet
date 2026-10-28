// Package store 是对局与建议的 SQLite 沉淀（报告 §4 的 store 模块）。
// M1c 范围：建议记录（引擎终值 + 桔库命中）异步批量落库；
// 完整对局聚合（按对局 ID 归并）后续里程碑扩展。
package store

import (
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite" // 纯 Go 驱动：免 CGO，单二进制交叉编译的关键。
	// 驱动名 "sqlite" 与 DSN 的 _pragma=key(val) 语法均为该驱动专有（mattn 是
	// "sqlite3"、参数名也不同）——换驱动不是改一行 import，全部 pragma 落地
	// 方式需重做。
)

// SuggestionRec 待沉淀的建议记录。
type SuggestionRec struct {
	TS        time.Time
	Source    string // "engine" 或 "book:<库名>"
	FEN       string
	BestMove  string
	ScoreCP   *int
	ScoreMate *int
	PV        []string
	Depth     int
	GameID    string // 所属对局（2026-10-17 复盘闭环；旧记录/无局上下文为空）
}

// AdoptionRec 采纳行为事件（2026-10-17 采纳率统计）：一条已下发建议在下一个
// 确认局面到来时的结果。MatchKind 口径：primary=走了建议主选；candidate=走了
// 候选之一（CandRank 为槽位 1 基）；miss=走了别的着法。无法单步解释的比对
// （悔棋/手动编辑/多步跳变/新局）不产生事件——宁缺勿错，无效样本会污染统计。
type AdoptionRec struct {
	TS        time.Time
	FEN       string // 建议对应的局面
	Suggested string // 建议主选（UCCI）
	Actual    string // 实际走子（UCCI）
	MatchKind string // primary / candidate / miss
	CandRank  int    // candidate 命中的槽位（1 基；primary 恒 1）
	Source    string
	Depth     int
	ScoreCP   *int
	ScoreMate *int
}

// Store 沉淀库：单写 goroutine + 缓冲 channel，绝不阻塞协议主流程。
type Store struct {
	db  *sql.DB
	ch  chan SuggestionRec
	ach chan AdoptionRec
	log *slog.Logger
	wg  sync.WaitGroup
	// chMu 保护 closed 标志与两条 channel 的 close 动作：向已关闭 channel
	// 发送会 panic，单靠 select default 防不了关闭态——迟到的 Save（引擎
	// 异步回调）与 Close 的竞态由此闭环。⚠channel 本身**永不置 nil**：
	// writeLoop 启动时一次性拷贝局部引用做 nil-channel 摘除，若 Close 置
	// nil 早于 writeLoop 首次调度，拷贝到 nil 会跳过整个排干循环、缓冲内
	// 数据整批蒸发（实测过）。closed 标志承担全部关闭态语义。
	chMu   sync.RWMutex
	closed bool
}

// schema 建表语句（建议记录表 + 采纳事件表）。
// ⚠game_id 索引不在此处：idx_suggestions_game 引用的列在**旧库**上由 Open 的
// ALTER 迁移补齐，若索引混在本常量里会先于迁移执行、旧库直接"建表失败"退出
// （2026-10-18 桌面端打不开的根因）。索引在 Open 迁移完成后单独建。
const schema = `
CREATE TABLE IF NOT EXISTS suggestions (
	id        INTEGER PRIMARY KEY AUTOINCREMENT,
	ts        TEXT    NOT NULL,          -- RFC3339 毫秒
	session   TEXT    NOT NULL,
	source    TEXT    NOT NULL,          -- engine / book:<库名>
	fen       TEXT    NOT NULL,
	best_move TEXT    NOT NULL,
	score_cp  INTEGER,                   -- 红方视角，可空
	score_mate INTEGER,                  -- 正=红将杀黑，可空
	pv        TEXT    NOT NULL,          -- 空格分隔 UCCI 序列
	depth     INTEGER NOT NULL DEFAULT 0,
	game_id   TEXT    NOT NULL DEFAULT '' -- 所属对局（复盘闭环按局折叠的分组键）
);
CREATE INDEX IF NOT EXISTS idx_suggestions_ts ON suggestions(ts);
CREATE INDEX IF NOT EXISTS idx_suggestions_fen ON suggestions(fen);
-- 采纳事件表（2026-10-17 采纳率统计）：一条已下发建议在下一个确认局面
-- 到来时的结果，一行一个事件。写入侧 server.go evaluateAdoption（比对与
-- 无效排除清单见彼处）；只增不改，统计口径见 store.AdoptionRec 注释。
CREATE TABLE IF NOT EXISTS adoptions (
	id        INTEGER PRIMARY KEY AUTOINCREMENT,
	ts        TEXT    NOT NULL,          -- RFC3339 毫秒（比对发生时刻，非建议下发时刻）
	fen       TEXT    NOT NULL,          -- 建议对应的局面（= 比对时的上一局面）
	suggested TEXT    NOT NULL,          -- 建议主选（UCCI 4 字符）
	actual    TEXT    NOT NULL,          -- 实际走子（UCCI 4 字符，diff 推导）
	match_kind TEXT   NOT NULL,          -- primary(主选采纳) / candidate(候选命中) / miss(走出别的)
	candidate_rank INTEGER NOT NULL DEFAULT 0,  -- candidate 命中的槽位 1 基；primary 恒 1；miss 0
	source    TEXT    NOT NULL,          -- 建议来源（engine / book:<名>，随主选帧快照）
	depth     INTEGER NOT NULL DEFAULT 0,  -- 建议深度（随主选帧快照）
	score_cp  INTEGER,                     -- 建议分数（红方视角，可空）
	score_mate INTEGER                     -- 建议将杀步数（可空）
);
CREATE INDEX IF NOT EXISTS idx_adoptions_ts ON adoptions(ts);
`

// Open 打开（不存在则创建）沉淀库并启动写入 goroutine。
func Open(path string, logger *slog.Logger) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("创建数据目录失败: %w", err)
	}
	abs, _ := filepath.Abs(path)
	// pragma 走 DSN 落到每条连接：synchronous 是 per-connection 属性，经 Exec
	// 只落在池里恰好一条连接上（writeLoop 的 Begin 可能取到别的连接→退回 FULL
	// 多 fsync）；busy_timeout 消除控制台清空与写入并发时的 SQLITE_BUSY。
	db, err := sql.Open("sqlite", "file:"+strings.ReplaceAll(abs, "\\", "/")+
		"?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("打开沉淀库失败: %w", err)
	}
	// Exec 兜底保留：老版本驱动或 DSN 参数解析异常时仍保证 WAL
	if _, err := db.Exec("pragma journal_mode=WAL; pragma synchronous=NORMAL;"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("设置 WAL 失败: %w", err)
	}
	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("建表失败: %w", err)
	}
	// game_id 列迁移（2026-10-17 复盘闭环）：CREATE TABLE IF NOT EXISTS 不会给
	// 已存在的旧表加列——探测列缺失后 ALTER 补齐。旧库兼容性沿用 session 列
	// 的历史包袱语义：NOT NULL DEFAULT '' 保证旧 INSERT 语义不变。
	// 【以后给 suggestions/adoptions 加列照抄本模式】：
	//   1. schema 字符串里加列（新库直接建，带 DEFAULT 保持旧 INSERT 兼容）
	//   2. Open 里 QueryRow("select 新列 ... limit 1") 探测——空表也返回无错
	//   3. 失败即 ALTER TABLE ADD COLUMN，失败即中止启动（迁移失败比静默坏数据好）
	//   4. ⚠引用新列的索引/约束不能写进 schema 常量（会先于本迁移执行、旧库
	//      直接炸），迁移完成后在此单独 CREATE INDEX IF NOT EXISTS
	if _, err := db.Exec("select game_id from suggestions limit 1"); err != nil {
		if _, err := db.Exec("alter table suggestions add column game_id TEXT NOT NULL DEFAULT ''"); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("game_id 列迁移失败: %w", err)
		}
		if logger != nil {
			logger.Info("沉淀库迁移：suggestions 补建 game_id 列")
		}
	}
	// game_id 索引：列已保证存在（新库 schema 自带、旧库上方 ALTER 补齐），
	// 按局折叠 group by game_id 的查询索引（Games/RecentByGame）
	if _, err := db.Exec("CREATE INDEX IF NOT EXISTS idx_suggestions_game ON suggestions(game_id)"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("game_id 索引创建失败: %w", err)
	}
	s := &Store{
		db:  db,
		ch:  make(chan SuggestionRec, 1024),
		ach: make(chan AdoptionRec, 1024),
		log: logger,
	}
	s.wg.Add(1)
	go s.writeLoop()
	if logger != nil {
		logger.Info("沉淀库就绪", "path", abs)
	}
	return s, nil
}

// Save 异步落一条建议记录（缓冲满则丢弃并告警，不影响主流程）。
//
// 与 Close 的同步：chMu 读锁内判空发送——Close 置 nil 后迟到的 Save
// （含引擎异步回调触发的建议）静默丢弃而非 panic。允许 Close 后仍持有
// Store 引用的调用方安全续用，无生命周期顺序要求。
func (s *Store) Save(rec SuggestionRec) {
	if s == nil {
		return
	}
	if rec.TS.IsZero() {
		rec.TS = time.Now()
	}
	s.chMu.RLock()
	defer s.chMu.RUnlock()
	if s.closed {
		if s.log != nil {
			s.log.Debug("沉淀库已关闭，丢弃一条迟到记录", "fen", rec.FEN)
		}
		return
	}
	select {
	case s.ch <- rec:
	default:
		if s.log != nil {
			s.log.Warn("沉淀队列已满，丢弃一条记录", "fen", rec.FEN)
		}
	}
}

// SaveAdoption 异步落一条采纳事件（与 Save 同款关闭态同步）。
func (s *Store) SaveAdoption(rec AdoptionRec) {
	if s == nil {
		return
	}
	if rec.TS.IsZero() {
		rec.TS = time.Now()
	}
	s.chMu.RLock()
	defer s.chMu.RUnlock()
	if s.closed {
		if s.log != nil {
			s.log.Debug("沉淀库已关闭，丢弃一条迟到的采纳事件", "fen", rec.FEN)
		}
		return
	}
	select {
	case s.ach <- rec:
	default:
		if s.log != nil {
			s.log.Warn("采纳事件队列已满，丢弃一条", "fen", rec.FEN)
		}
	}
}

// pruneTSFormat 与 writeLoop 落库格式一致：同格式本地时区字符串的字典序=时间序，
// PruneBefore 靠这一点用文本比较裁剪。
const pruneTSFormat = "2006-01-02T15:04:05.000Z07:00"

// PruneBefore 删除 cutoff 之前的沉淀记录，返回删除条数（服务启动时裁剪一次，
// 防库文件无界增长；90 天前的对局记录已无复用价值）。采纳事件同口径裁剪。
func (s *Store) PruneBefore(cutoff time.Time) (int64, error) {
	res, err := s.db.Exec("delete from suggestions where ts < ?", cutoff.Format(pruneTSFormat))
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	if res2, err2 := s.db.Exec("delete from adoptions where ts < ?", cutoff.Format(pruneTSFormat)); err2 == nil {
		if m, _ := res2.RowsAffected(); m > 0 {
			n += m
		}
	}
	return n, nil
}

// Close 停止接收并等待写入 goroutine 清空队列。
// 两条 channel 必须同关：writeLoop 在 select 上双通道取数，只关 s.ch 会让
// goroutine 永远卡在 ach 分支等待、wg.Wait() 死锁。chMu 写锁内置 closed 标志
// 后关闭，与 Save/SaveAdoption 的"检查+发送"互斥——迟到的 Save 永远不会
// 向已关闭 channel 发送（panic）。channel 不置 nil（原因见 struct 注释）。
// 幂等：重复 Close 直接走 wg.Wait 与 db.Close（均幂等）。
func (s *Store) Close() error {
	if s == nil {
		return nil
	}
	s.chMu.Lock()
	if !s.closed {
		s.closed = true
		close(s.ch)
		close(s.ach)
	}
	s.chMu.Unlock()
	s.wg.Wait()
	return s.db.Close()
}

// 两条 INSERT 常量：flushBatch 的 Prepare 入参（列序与建表 schema 一一对应）
const insertSuggestionSQL = "insert into suggestions(ts,session,source,fen,best_move,score_cp,score_mate,pv,depth,game_id) values(?,?,?,?,?,?,?,?,?,?)"

const insertAdoptionSQL = "insert into adoptions(ts,fen,suggested,actual,match_kind,candidate_rank,source,depth,score_cp,score_mate) values(?,?,?,?,?,?,?,?,?,?)"

// flushBatch 批量事务写入骨架（flushSug/flushAdopt 的公共部分）：
// Begin→Prepare→逐条 Exec→Commit。best-effort：任何失败只告警、整批放弃、
// 绝不重试——重试会让 writeLoop 停滞、channel 涨满，Save 侧开始丢弃，坏一个
// 点拖垮整条链路；沉淀是旁路数据，宁丢不阻。batch 的清空由调用方负责
// （无条件放弃语义；历史版本 Prepare 失败分支漏清空、与注释意图不符，已对齐）。
func flushBatch[T any](s *Store, label, insertSQL string, batch []T, values func(T) []any) {
	if len(batch) == 0 {
		return
	}
	tx, err := s.db.Begin()
	if err != nil {
		if s.log != nil {
			s.log.Warn(label+"事务开启失败，丢弃一批", "err", err, "n", len(batch))
		}
		return
	}
	stmt, err := tx.Prepare(insertSQL)
	if err != nil {
		_ = tx.Rollback()
		if s.log != nil {
			s.log.Warn(label+"预编译失败，丢弃一批", "err", err, "n", len(batch))
		}
		return
	}
	for _, r := range batch {
		if _, err := stmt.Exec(values(r)...); err != nil && s.log != nil {
			s.log.Warn(label+"写入失败", "err", err)
		}
	}
	_ = stmt.Close()
	if err := tx.Commit(); err != nil && s.log != nil {
		s.log.Warn(label+"提交失败", "err", err)
	}
}

// writeLoop 单写者：攒批事务写入（批量 64 条或 500ms 刷新一次）。
//
// flush 是 best-effort：Begin/单条 Exec/Commit 任一步失败都只告警或静默
// 回滚，整批或单条放弃、绝不重试——重试会让 writeLoop 停滞、channel 涨满，
// Save 侧开始丢弃，坏一个点拖垮整条链路。沉淀是旁路数据，宁丢不阻。
func (s *Store) writeLoop() {
	defer s.wg.Done()
	// 64 ≈ 一局对局的建议量级（每步 1~3 条 × 数十步），足以把一波搜索建议
	// 合并进同一事务摊薄提交开销；500ms 是崩溃丢失窗口上限，也是读侧可见
	// 延迟上限（控制台轮询无感）。纯经验值，调大前先想清丢失窗口。
	const batchLimit = 64
	const flushInterval = 500 * time.Millisecond
	batch := make([]SuggestionRec, 0, batchLimit)
	abatch := make([]AdoptionRec, 0, batchLimit)
	// flushSug/flushAdopt 共用 flushBatch 骨架（Begin→Prepare→逐条 Exec→Commit）。
	// 两表同事务不划算——表间无一致性要求，分开提交减少互相拖累。
	flushSug := func() {
		flushBatch(s, "沉淀", insertSuggestionSQL, batch,
			func(r SuggestionRec) []any {
				// session 列为历史遗留：生产链路无会话数据（恒空串），列保留兼容旧库
				// （旧 schema NOT NULL 无默认值，INSERT 必须继续提供）
				return []any{r.TS.Format(pruneTSFormat), "", r.Source, r.FEN, r.BestMove,
					nullInt(r.ScoreCP), nullInt(r.ScoreMate), joinPV(r.PV), r.Depth, r.GameID}
			})
		batch = batch[:0]
	}
	flushAdopt := func() {
		flushBatch(s, "采纳事件", insertAdoptionSQL, abatch,
			func(r AdoptionRec) []any {
				return []any{r.TS.Format(pruneTSFormat), r.FEN, r.Suggested, r.Actual,
					r.MatchKind, r.CandRank, r.Source, r.Depth,
					nullInt(r.ScoreCP), nullInt(r.ScoreMate)}
			})
		abatch = abatch[:0]
	}

	ticker := time.NewTicker(flushInterval)
	defer ticker.Stop()
	// 双通道退出语义（nil-channel 惯用法）：关闭的 channel 立即就绪 ok=false，
	// 若 select 随机先选中它就 return，另一条 channel 里的未排空数据会整批
	// 丢失（随机测试失败实证过）——改为置 nil 摘除分支，两条都关闭后才退出
	// 循环并做最终冲刷。nil channel 永久阻塞，select 不会再选中它。
	sugCh, achCh := s.ch, s.ach
	for sugCh != nil || achCh != nil {
		select {
		case rec, ok := <-sugCh:
			if !ok {
				sugCh = nil
				continue
			}
			batch = append(batch, rec)
			if len(batch) >= batchLimit {
				flushSug()
			}
		case rec, ok := <-achCh:
			if !ok {
				achCh = nil
				continue
			}
			abatch = append(abatch, rec)
			if len(abatch) >= batchLimit {
				flushAdopt()
			}
		case <-ticker.C:
			flushSug()
			flushAdopt()
		}
	}
	// 最终冲刷：退出循环前 ticker 周期内到达的尾部数据（Close 时刻 < 500ms 的尾巴）
	flushSug()
	flushAdopt()
}

func nullInt(p *int) any {
	if p == nil {
		return nil
	}
	return *p
}

func joinPV(pv []string) string { return strings.Join(pv, " ") }
