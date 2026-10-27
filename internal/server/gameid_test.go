package server

import (
	"strings"
	"sync"
	"testing"
	"time"

	"cabinet/internal/engine"
)

// TestServer_NewGameID 复盘闭环 game_id 链路（2026-10-17）：new_game 首帧生成
// 新对局 ID，引擎终态沉淀的记录按局归组；无标记的后续局面沿用当前局 ID。
// 收集器持锁：sink 在 enginePump goroutine 触发、断言在测试主 goroutine 读，
// 裸切片在 -race 下是数据竞争（2026-10-18 加固）。
func TestServer_NewGameID(t *testing.T) {
	fb := newFakeBridge(engine.StateReady)
	fb.onRequest = replayNormalSearch
	url, _, srv := startTestServerWithBridgeS(t, fb)

	var mu sync.Mutex
	var recorded []SuggestionRecord
	srv.SetSuggestionSink(func(rec SuggestionRecord) {
		mu.Lock()
		recorded = append(recorded, rec)
		mu.Unlock()
	})

	ws := dialWS(t, url)
	authClient(t, ws)

	// 第一局：new_game 首帧 → 引擎终态沉淀 → GameID 应非空且前缀合法
	fenA := "rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR w - - 0 1"
	_ = ws.WriteJSON(Position{frameBase: frame(TypePosition), Fen: fenA, RedGo: true, NewGame: true})
	waitFor(t, 2*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(recorded) > 0
	})
	mu.Lock()
	if len(recorded) == 0 {
		mu.Unlock()
		t.Fatal("首局建议未沉淀")
	}
	idA := recorded[0].GameID
	mu.Unlock()
	if !strings.HasPrefix(idA, "game-") {
		t.Fatalf("game_id 前缀不符: %q", idA)
	}

	// 同局后续局面（不带标记）：沿用当前局 ID
	fenB := "rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C2C4/9/RNBAKABNR b - - 0 1"
	_ = ws.WriteJSON(Position{frameBase: frame(TypePosition), Fen: fenB, RedGo: false})
	waitFor(t, 2*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(recorded) > 1
	})
	mu.Lock()
	last := recorded[len(recorded)-1]
	mu.Unlock()
	if last.GameID != idA {
		t.Fatalf("无标记局面应沿用当前局 ID: %q != %q", last.GameID, idA)
	}

	// 第二局：再次 new_game → 新 ID 且不同
	fenC := "rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR b - - 0 1"
	_ = ws.WriteJSON(Position{frameBase: frame(TypePosition), Fen: fenC, RedGo: false, NewGame: true})
	waitFor(t, 2*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(recorded) > 2 && recorded[len(recorded)-1].GameID != ""
	})
	mu.Lock()
	idC := recorded[len(recorded)-1].GameID
	mu.Unlock()
	if idC == idA {
		t.Fatalf("第二局应生成新 game_id: %q == %q", idC, idA)
	}
}
