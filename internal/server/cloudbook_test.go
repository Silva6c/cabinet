package server

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"fmt"

	"github.com/gorilla/websocket"

	"cabinet/internal/config"
	"cabinet/internal/engine"
)

// TestCloudQueryParse queryall 形状解析：多记录取 score 最高者；unknown
// /error 形态一律未命中（缓存住防重复打外网）。
func TestCloudQueryParse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("board") {
		case "FEN_A":
			_, _ = w.Write([]byte("move:h2e2,score:25,winrate:54.08|move:b2e2,score:60,winrate:51.0"))
		case "FEN_B":
			_, _ = w.Write([]byte("unknown:unknown"))
		default:
			_, _ = w.Write([]byte("error:bad request"))
		}
	}))
	defer srv.Close()
	s := &Server{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	cb := config.CloudBookConfig{Enable: true, Endpoint: srv.URL}

	e, err := s.cloudQuery(cb, "FEN_A")
	if err != nil || !e.hit || e.move != "b2e2" {
		t.Fatalf("多记录应取最高分 b2e2, got %+v err=%v", e, err)
	}
	if e, err = s.cloudQuery(cb, "FEN_B"); err != nil || e.hit {
		t.Fatalf("unknown 应为未命中, got %+v err=%v", e, err)
	}
	if e, err = s.cloudQuery(cb, "FEN_C"); err != nil || e.hit {
		t.Fatalf("error 形态应为未命中, got %+v err=%v", e, err)
	}
}

// TestCloudLookupEndToEnd 开关开启时：桔库/沉淀双未命中 → 云库帧以
// cloud:<域名> source 下发（suggestion 帧复用，旧手机零改动）。
func TestCloudLookupEndToEnd(t *testing.T) {
	cloudHits := make(chan struct{}, 8)
	cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cloudHits <- struct{}{}
		_, _ = w.Write([]byte("move:h2e2,score:25"))
	}))
	defer cloud.Close()

	fb := newFakeBridge(engine.StateReady)
	fb.onRequest = replayNormalSearch
	url, cleanup, srv := startTestServerWithBridgeS(t, fb)
	defer cleanup()
	srv.cfg.CloudBook = config.CloudBookConfig{Enable: true, Endpoint: cloud.URL}
	srv.SetCloudBookEnable(true) // 运行时开关与 cfg 解耦（PC 面板可热切，2026-10-29）

	ws := dialWS(t, url)
	defer ws.Close()
	if _, ok := recv(t, ws, "服务器 hello").(*Hello); !ok {
		t.Fatal("首帧应为 hello")
	}
	_ = ws.WriteJSON(Hello{frameBase: frame(TypeHello), Client: ClientName, Ver: "0.1.0", Caps: []string{CapV1}})
	_ = ws.WriteJSON(Auth{frameBase: frame(TypeAuth), Token: testToken})
	if _, ok := recv(t, ws, "welcome").(*Welcome); !ok {
		t.Fatal("期望 welcome")
	}

	fen := "rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR w - - 0 1"
	_ = ws.WriteJSON(Position{frameBase: frame(TypePosition), Fen: fen, RedGo: true})

	select {
	case <-cloudHits:
	case <-time.After(2 * time.Second):
		t.Fatal("云库端点从未被请求——lookupAsync 未触发")
	}
	_ = ws.SetReadDeadline(time.Now().Add(3 * time.Second))
	defer ws.SetReadDeadline(time.Time{})
	var seen []string
	for {
		m := recvLoop(t, ws, &seen)
		sug, ok := m.(*Suggestion)
		if !ok {
			seen = append(seen, fmt.Sprintf("%T", m))
			continue
		}
		seen = append(seen, sug.Source)
		if sug.Source == "cloud:"+srvURLHost(cloud.URL) {
			if sug.BestMove != "h2e2" {
				t.Fatalf("云库主选=%q, want h2e2", sug.BestMove)
			}
			return
		}
	}
}

// TestCloudToggleOff 运行时开关关闭后不再查询（PC 面板热切的语义保证：
// cfg.Enable=true 但原子位关 = 不打外网）。
func TestCloudToggleOff(t *testing.T) {
	cloudHits := make(chan struct{}, 8)
	cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cloudHits <- struct{}{}
		_, _ = w.Write([]byte("move:h2e2,score:25"))
	}))
	defer cloud.Close()

	fb := newFakeBridge(engine.StateReady)
	fb.onRequest = replayNormalSearch
	url, cleanup, srv := startTestServerWithBridgeS(t, fb)
	defer cleanup()
	srv.cfg.CloudBook = config.CloudBookConfig{Enable: true, Endpoint: cloud.URL}
	// 不 SetCloudBookEnable(true)：开关保持关

	ws := dialWS(t, url)
	defer ws.Close()
	if _, ok := recv(t, ws, "服务器 hello").(*Hello); !ok {
		t.Fatal("首帧应为 hello")
	}
	_ = ws.WriteJSON(Hello{frameBase: frame(TypeHello), Client: ClientName, Ver: "0.1.0", Caps: []string{CapV1}})
	_ = ws.WriteJSON(Auth{frameBase: frame(TypeAuth), Token: testToken})
	if _, ok := recv(t, ws, "welcome").(*Welcome); !ok {
		t.Fatal("期望 welcome")
	}

	fen := "rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR w - - 0 1"
	_ = ws.WriteJSON(Position{frameBase: frame(TypePosition), Fen: fen, RedGo: true})

	select {
	case <-cloudHits:
		t.Fatal("开关已关仍请求了云库端点")
	case <-time.After(800 * time.Millisecond):
	}
}

func srvURLHost(u string) string {
	p, err := url.Parse(u)
	if err != nil {
		return u
	}
	return p.Host
}

// recvLoop 同 recv，但失败时带回已见来源清单（诊断用）。
func recvLoop(t *testing.T, ws *websocket.Conn, seen *[]string) any {
	t.Helper()
	_ = ws.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, data, err := ws.ReadMessage()
	if err != nil {
		t.Fatalf("读取失败(已见 %v): %v", *seen, err)
	}
	msg, err := ParseFrame(data)
	if err != nil {
		t.Fatalf("解码失败(已见 %v): %v", *seen, data)
	}
	return msg
}
