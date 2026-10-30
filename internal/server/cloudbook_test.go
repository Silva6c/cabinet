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

// handshakeClient 完整 hello/auth/welcome 握手（云库优先三测试共用）。
func handshakeClient(t *testing.T, ws *websocket.Conn) {
	t.Helper()
	if _, ok := recv(t, ws, "服务器 hello").(*Hello); !ok {
		t.Fatal("首帧应为 hello")
	}
	_ = ws.WriteJSON(Hello{frameBase: frame(TypeHello), Client: ClientName, Ver: "0.1.0", Caps: []string{CapV1}})
	_ = ws.WriteJSON(Auth{frameBase: frame(TypeAuth), Token: testToken})
	if _, ok := recv(t, ws, "welcome").(*Welcome); !ok {
		t.Fatal("期望 welcome")
	}
}

// TestCloudFirstHit "默认走云库"命中路径（2026-10-30）：cloud_first 开启后
// 云库命中即终——下发 cloud 建议且**引擎完全不被请求**（与桔库命中"命中即
// return"同构，省算力正是该功能的收益；语义矩阵见 cloudLookupFirst）。
func TestCloudFirstHit(t *testing.T) {
	cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("move:h2e2,score:25"))
	}))
	defer cloud.Close()

	fb := newFakeBridge(engine.StateReady)
	engineRequests := 0
	fb.onRequest = func(fb *fakeBridge, fen string) {
		engineRequests++
		replayNormalSearch(fb, fen)
	}
	url, cleanup, srv := startTestServerWithBridgeS(t, fb)
	defer cleanup()
	srv.cfg.CloudBook = config.CloudBookConfig{Enable: true, Endpoint: cloud.URL}
	srv.SetCloudBookEnable(true)
	srv.SetCloudFirst(true)

	ws := dialWS(t, url)
	defer ws.Close()
	handshakeClient(t, ws)

	fen := "rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR w - - 0 1"
	_ = ws.WriteJSON(Position{frameBase: frame(TypePosition), Fen: fen, RedGo: true})

	// 等云库建议到达
	_ = ws.SetReadDeadline(time.Now().Add(3 * time.Second))
	defer ws.SetReadDeadline(time.Time{})
	for {
		m := recvLoop(t, ws, &[]string{})
		sug, ok := m.(*Suggestion)
		if !ok {
			continue
		}
		if sug.Source == "cloud:"+srvURLHost(cloud.URL) {
			if sug.BestMove != "h2e2" {
				t.Fatalf("云库主选=%q, want h2e2", sug.BestMove)
			}
			break
		}
		t.Fatalf("cloud_first 命中时不应有其他来源建议先到：%s", sug.Source)
	}
	// 引擎必须零请求：云库命中即终，常规链（含引擎）未走
	time.Sleep(800 * time.Millisecond)
	if engineRequests != 0 {
		t.Fatalf("云库命中后引擎不应被请求，实际 %d 次", engineRequests)
	}
}

// TestCloudFirstMiss "默认走云库"未命中路径：云库查询照发，未命中自动回落
// 常规链——引擎建议照常产出（零缺口，用户无感知断流）。
func TestCloudFirstMiss(t *testing.T) {
	cloudHits := make(chan struct{}, 8)
	cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cloudHits <- struct{}{}
		_, _ = w.Write([]byte("unknown:unknown"))
	}))
	defer cloud.Close()

	fb := newFakeBridge(engine.StateReady)
	fb.onRequest = replayNormalSearch
	url, cleanup, srv := startTestServerWithBridgeS(t, fb)
	defer cleanup()
	srv.cfg.CloudBook = config.CloudBookConfig{Enable: true, Endpoint: cloud.URL}
	srv.SetCloudBookEnable(true)
	srv.SetCloudFirst(true)

	ws := dialWS(t, url)
	defer ws.Close()
	handshakeClient(t, ws)

	fen := "rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR w - - 0 1"
	_ = ws.WriteJSON(Position{frameBase: frame(TypePosition), Fen: fen, RedGo: true})

	select {
	case <-cloudHits:
	case <-time.After(2 * time.Second):
		t.Fatal("云库端点从未被请求——云库优先路径未触发")
	}
	// 回落常规链：引擎建议最终到达（cloud 未命中不产生建议帧）
	_ = ws.SetReadDeadline(time.Now().Add(5 * time.Second))
	defer ws.SetReadDeadline(time.Time{})
	for {
		m := recvLoop(t, ws, &[]string{})
		sug, ok := m.(*Suggestion)
		if !ok {
			continue
		}
		if sug.Source == "engine" {
			return // 常规链引擎建议已到，回落成功
		}
		t.Fatalf("cloud 未命中时不应有其他来源建议：%s", sug.Source)
	}
}

// TestCloudFirstOff cloud_first 关闭 = 完全现状行为：双未命中异步查云库补充 +
// 引擎照跑——云库建议先到、引擎建议随后覆盖（"最新覆盖"收束语义，2026-10-28
// 定稿的原始行为，回归锚）。
func TestCloudFirstOff(t *testing.T) {
	cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("move:h2e2,score:25"))
	}))
	defer cloud.Close()

	fb := newFakeBridge(engine.StateReady)
	fb.onRequest = replayNormalSearch
	url, cleanup, srv := startTestServerWithBridgeS(t, fb)
	defer cleanup()
	srv.cfg.CloudBook = config.CloudBookConfig{Enable: true, Endpoint: cloud.URL}
	srv.SetCloudBookEnable(true)
	// 不 SetCloudFirst(true)：保持默认关

	ws := dialWS(t, url)
	defer ws.Close()
	handshakeClient(t, ws)

	fen := "rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR w - - 0 1"
	_ = ws.WriteJSON(Position{frameBase: frame(TypePosition), Fen: fen, RedGo: true})

	seenCloud, seenEngine := false, false
	_ = ws.SetReadDeadline(time.Now().Add(5 * time.Second))
	defer ws.SetReadDeadline(time.Time{})
	for !seenCloud || !seenEngine {
		m := recvLoop(t, ws, &[]string{})
		sug, ok := m.(*Suggestion)
		if !ok {
			continue
		}
		switch sug.Source {
		case "cloud:" + srvURLHost(cloud.URL):
			seenCloud = true
		case "engine":
			seenEngine = true
		}
	}
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
