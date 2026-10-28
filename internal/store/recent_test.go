package store

import (
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"
)

// TestRecent 建议流读取：沉淀 3 条后按时间倒序取最近 N 条。
// Save 是异步批量写，测试用 Close 触发清队列后重开验证落库内容。
func TestRecent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "suggestions.db")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	st, err := Open(path, logger)
	if err != nil {
		t.Fatal(err)
	}
	cp260 := 260
	mate3 := 3
	base := time.Now()
	st.Save(SuggestionRec{TS: base.Add(-2 * time.Second), Source: "engine", FEN: "fen1", BestMove: "h2e2", ScoreCP: &cp260, Depth: 18})
	st.Save(SuggestionRec{TS: base.Add(-1 * time.Second), Source: "book:桔库", FEN: "fen2", BestMove: "b0c2", Depth: 0})
	st.Save(SuggestionRec{TS: base, Source: "engine", FEN: "fen3", BestMove: "e3e4", ScoreMate: &mate3, Depth: 20})
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	// 重开验证（确认批量落库成功）
	st2, err := Open(path, logger)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	rows, err := st2.Recent(2)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("应取回 2 条, got %d", len(rows))
	}
	// 倒序：最新在前
	if rows[0].BestMove != "e3e4" || rows[1].BestMove != "b0c2" {
		t.Fatalf("倒序不符: %s, %s", rows[0].BestMove, rows[1].BestMove)
	}
	if rows[0].ScoreMate == nil || *rows[0].ScoreMate != 3 {
		t.Fatalf("mate 分数不符: %+v", rows[0].ScoreMate)
	}
	// 全量：3 条，第一条为库命中（无分数）
	all, err := st2.Recent(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("应取回 3 条, got %d", len(all))
	}
	book := all[1]
	if book.Source != "book:桔库" || book.ScoreCP != nil || book.ScoreMate != nil {
		t.Fatalf("库命中行不符: %+v", book)
	}
}

// TestLookupByFEN 沉淀命中查询：精确匹配 FEN 取最新一条；未命中 (nil,nil)。
func TestLookupByFEN(t *testing.T) {
	path := filepath.Join(t.TempDir(), "suggestions.db")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	st, err := Open(path, logger)
	if err != nil {
		t.Fatal(err)
	}
	cp120 := 120
	st.Save(SuggestionRec{Source: "engine", FEN: "fenA", BestMove: "h2e2", ScoreCP: &cp120, Depth: 20, PV: []string{"h2e2", "b9c7"}})
	st.Save(SuggestionRec{Source: "engine", FEN: "fenA", BestMove: "e3e4", Depth: 22})
	_ = st.Close()

	st2, err := Open(path, logger)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()

	// 命中：取最新一条（id 大的 e3e4，depth 22）
	row, err := st2.LookupByFEN("fenA")
	if err != nil || row == nil {
		t.Fatalf("应命中 fenA: row=%v err=%v", row, err)
	}
	if row.BestMove != "e3e4" || row.Depth != 22 {
		t.Fatalf("应取最新一条: %+v", row)
	}
	// 第二条无 PV；第一条有 PV 不应串
	if len(row.PV) > 0 {
		t.Fatalf("第二条不应带 PV: %+v", row)
	}
	// 未命中：(nil, nil)
	row, err = st2.LookupByFEN("fen-not-exists")
	if err != nil || row != nil {
		t.Fatalf("未命中应为 (nil,nil): row=%v err=%v", row, err)
	}
}

// TestPruneBefore 保留期裁剪：只删 cutoff 之前的记录。
func TestPruneBefore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "suggestions.db")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	st, err := Open(path, logger)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	st.Save(SuggestionRec{TS: now.AddDate(0, 0, -120), Source: "engine", FEN: "old", BestMove: "h2e2", Depth: 18})
	st.Save(SuggestionRec{TS: now, Source: "engine", FEN: "new", BestMove: "e3e4", Depth: 20})
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	st2, err := Open(path, logger)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	n, err := st2.PruneBefore(now.AddDate(0, 0, -90))
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("应删 1 条, got %d", n)
	}
	if row, _ := st2.LookupByFEN("old"); row != nil {
		t.Fatal("旧记录应被删除")
	}
	if row, _ := st2.LookupByFEN("new"); row == nil {
		t.Fatal("新记录应保留")
	}
}

// TestCountToday 今日计数：同日沉淀 2 条计 2；昨日 1 条不计入。
func TestCountToday(t *testing.T) {
	path := filepath.Join(t.TempDir(), "suggestions.db")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	st, err := Open(path, logger)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	st.Save(SuggestionRec{TS: midnight.Add(1 * time.Minute), Source: "engine", FEN: "t1", BestMove: "h2e2", Depth: 20})
	st.Save(SuggestionRec{TS: now, Source: "engine", FEN: "t2", BestMove: "e3e4", Depth: 20})
	st.Save(SuggestionRec{TS: midnight.Add(-1 * time.Minute), Source: "engine", FEN: "t3", BestMove: "b0c2", Depth: 20})
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st2, err := Open(path, logger)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	n, err := st2.CountToday()
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("今日应计 2 条（昨日 1 条不计），got %d", n)
	}
}
