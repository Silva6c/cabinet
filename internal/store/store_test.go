package store

import (
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// TestSaveAndCount 建议沉淀写读往返：异步落库、关闭清队列、重开可读。
func TestSaveAndCount(t *testing.T) {
	path := filepath.Join(t.TempDir(), "suggestions.db")
	s, err := Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}

	cp := 120
	s.Save(SuggestionRec{
		Source: "engine", FEN: "rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR w - - 0 1",
		BestMove: "b2e2", ScoreCP: &cp, PV: []string{"b2e2", "b9c7"}, Depth: 20,
	})
	s.Save(SuggestionRec{
		Source: "book:桔库", FEN: "rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR w - - 0 1",
		BestMove: "h2c2", Depth: 0,
	})
	s.Save(SuggestionRec{ // 空 score 指针路径
		Source: "book:桔库", FEN: "fen-x", BestMove: "a0a1",
	})

	// Close 触发队列清空
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	// Close 幂等
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	// 重开验证持久化
	s2, err := Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	// 条数断言：同包直接查表（文件下方字段抽查同款风格）
	var n int
	if err := s2.db.QueryRow("select count(*) from suggestions").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("沉淀条数不符: got %d want 3", n)
	}

	// 字段抽查：查 book 一条确认分数/着法落库
	var best, source string
	var scoreCP any
	if err := s2.db.QueryRow(
		"select best_move, source, score_cp from suggestions where best_move='h2c2'").
		Scan(&best, &source, &scoreCP); err != nil {
		t.Fatal(err)
	}
	if best != "h2c2" || source != "book:桔库" || scoreCP != nil {
		t.Fatalf("字段不符: best=%q source=%q cp=%v", best, source, scoreCP)
	}
}

// TestQueueOverflow 缓冲满不阻塞（快速灌满队列后 Close 清空，不死锁）。
func TestQueueOverflow(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "s.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		for i := 0; i < 5000; i++ { // 远超缓冲 1024
			s.Save(SuggestionRec{Source: "engine", FEN: "f", BestMove: "a1a2", TS: time.Now()})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Save 在队列满时阻塞了主流程（应丢弃）")
	}
	_ = s.Close()
}

// TestSaveAfterClose Close 后迟到的 Save/SaveAdoption 必须静默丢弃而非 panic：
// 引擎异步回调可能晚于服务关闭触达 Store，这是 chMu 关闭态保护的行为锚。
func TestSaveAfterClose(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "late.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s.Save(SuggestionRec{FEN: "fen-late", BestMove: "h2e2"}) // 不得 panic
	s.SaveAdoption(AdoptionRec{FEN: "fen-late"})             // 不得 panic
	if err := s.Close(); err != nil {                        // 重复 Close 幂等
		t.Fatal(err)
	}
	s.Save(SuggestionRec{FEN: "fen-late2", BestMove: "h2e2"}) // 仍不得 panic
}

// TestSaveCloseRace 并发 Save 与 Close 的竞态锚：迟到 Save 永不 panic，
// Close 前已入队的数据不因关闭时序丢失（writeLoop 局部拷贝 × 关闭态的
// 历史教训见 Store struct 注释）。-race 下跑才有效力。
func TestSaveCloseRace(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "race.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				s.Save(SuggestionRec{FEN: fmt.Sprintf("fen-%d-%d", i, j), BestMove: "h2e2"})
			}
		}(i)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		time.Sleep(time.Millisecond) // 让部分 Save 先入队，制造迟到 Save 与 Close 交错
		_ = s.Close()
	}()
	wg.Wait()
	_ = s.Close() // 幂等兜底
}
