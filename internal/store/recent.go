// recent.go 建议流的读取侧：控制台"建议流"分区按时间倒序取最近 N 条。
package store

import (
	"database/sql"
	"errors"
	"strings"
	"time"
)

// SuggestionRow 一条已沉淀建议的展示行（不含 FEN/PV 全文，控制台用精简字段；
// FEN/PV 保留在结构中便于后续扩展）。
type SuggestionRow struct {
	TS        time.Time
	Source    string // "engine" 或 "book:<库名>"
	FEN       string
	BestMove  string
	ScoreCP   *int
	ScoreMate *int
	PV        []string
	Depth     int
}

// LookupByFEN 精确匹配 FEN 取最近一条沉淀建议（沉淀命中用：同局面复用历史
// 引擎终值，省一次数秒级重搜）。未命中返回 (nil, nil)；查询错误才返回 err。
func (s *Store) LookupByFEN(fen string) (*SuggestionRow, error) {
	row := s.db.QueryRow(
		"select ts,source,fen,best_move,score_cp,score_mate,pv,depth from suggestions where fen = ? order by id desc limit 1", fen)
	var (
		r        SuggestionRow
		ts       string
		cp, mate sql.NullInt64
		pv       string
	)
	if err := row.Scan(&ts, &r.Source, &r.FEN, &r.BestMove, &cp, &mate, &pv, &r.Depth); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	if t, err := time.Parse(pruneTSFormat, ts); err == nil {
		r.TS = t
	}
	if cp.Valid {
		v := int(cp.Int64)
		r.ScoreCP = &v
	}
	if mate.Valid {
		v := int(mate.Int64)
		r.ScoreMate = &v
	}
	if pv != "" {
		r.PV = strings.Fields(pv)
	}
	return &r, nil
}

// CountToday 今日沉淀条数（控制台建议流标题行"今日 N 条"）。
// ts 以 RFC3339 本地时区文本存储，同机部署时区偏移恒定（中国无夏令时），
// 与本地零点的字典序比较等价于时间比较。
func (s *Store) CountToday() (int, error) {
	now := time.Now()
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	since := midnight.Format(pruneTSFormat)
	var n int
	err := s.db.QueryRow("select count(*) from suggestions where ts >= ?", since).Scan(&n)
	return n, err
}

// DeleteAll 清空全部建议沉淀与采纳事件（控制台"清空沉淀"按钮用；测试数据
// 清理等场景）。采纳事件依附于建议存在，一并清空保持口径一致。
func (s *Store) DeleteAll() error {
	if _, err := s.db.Exec("delete from adoptions"); err != nil {
		return err
	}
	_, err := s.db.Exec("delete from suggestions")
	return err
}

// Recent 按沉淀顺序倒序取最近 n 条建议（id 自增 = 沉淀顺序，比文本时间排序可靠）。
func (s *Store) Recent(n int) ([]SuggestionRow, error) {
	rows, err := s.db.Query(
		"select ts,source,fen,best_move,score_cp,score_mate,pv,depth from suggestions order by id desc limit ?", n)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []SuggestionRow
	for rows.Next() {
		var (
			r        SuggestionRow
			ts       string
			cp, mate sql.NullInt64
			pv       string
		)
		if err := rows.Scan(&ts, &r.Source, &r.FEN, &r.BestMove, &cp, &mate, &pv, &r.Depth); err != nil {
			return out, err
		}
		if t, err := time.Parse(pruneTSFormat, ts); err == nil {
			r.TS = t
		}
		if cp.Valid {
			v := int(cp.Int64)
			r.ScoreCP = &v
		}
		if mate.Valid {
			v := int(mate.Int64)
			r.ScoreMate = &v
		}
		if pv != "" {
			r.PV = strings.Fields(pv)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
