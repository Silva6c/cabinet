package server

import (
	"crypto/tls"
	"database/sql"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cabinet/internal/book"
	"cabinet/internal/config"
	"cabinet/internal/engine"

	// sqlite 驱动（造测试桔库用；book 包测试同款）
	_ "modernc.org/sqlite"
)

// makeMultiCandBook 造含一个"三候选局面"的迷你桔库（schema 逐字对齐上游
// BHDatabase，构造方式与 book 包 TestMultiBookPriority 的 makeMiniBook 同源）：
// 光帅局面三条平移着法，vscore 30/20/10 —— SQL 按 vscore desc 排序，
// Query 应按 e9/f9/i9 顺序返回。
func makeMultiCandBook(t *testing.T, path string, vkey int64) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE bhobk([id] INTEGER PRIMARY KEY AUTOINCREMENT,
                   [vkey] INTEGER,
                   [vmove] INTEGER,
                   [vscore] INTEGER,
                   [vwin] INTEGER,
                   [vdraw] INTEGER,
                   [vlost] INTEGER,
                   [vvalid] INTEGER,
                   [vmemo] BLOB,
                   [vindex] INTEGER)`); err != nil {
		t.Fatal(err)
	}
	// vmove 位义（book.VmoveToUcci 冻结契约）：高 8 位=终点格子编码、低 8 位=起点，
	// 输出 UCCI = 起点在前。格子编码 = c90 表 0x33+x+y*16（zobrist_table.go），
	// 二路炮位（y=7）：h2=0xAA、g2=0xA9、e2=0xA7、b2=0xA4。
	// 三条候选着法按 vscore 30/20/10 —— SQL 按 vscore desc 排序，
	// Query 应按 h2e2/h2g2/b2e2 顺序返回（着法无需在盘面上真实可走：
	// Query 只按 vkey 查表，本测试验证的是多候选帧的下发链路）。
	// 格子编码 y 是 FEN 行号（0=黑底线），UCCI rank = 9-y——二路炮（rank 2）
	// 在 FEN 行 7：h2=0x33+7+7*16=0xAA、g2=0xA9、e2=0xA7、b2=0xA4。
	rows := []struct {
		ucci     string
		from, to int
		score    int
	}{
		{"h2e2", 0xAA, 0xA7, 30},
		{"h2g2", 0xAA, 0xA9, 20},
		{"b2e2", 0xA4, 0xA7, 10},
	}
	for _, r := range rows {
		vmove := r.to<<8 | r.from
		if _, err := db.Exec(
			"insert into bhobk(vkey, vmove, vscore, vwin, vdraw, vlost, vvalid) values(?,?,?,?,?,?,1)",
			vkey, vmove, r.score, 0.6, 3, 1); err != nil {
			t.Fatal(err)
		}
	}
}

// startTestServerWithS 带服务实例引用的起服（需要 SetBook 等注册回调时用）；
// 复用 startTestServerFull 的公共构造（此处仅多返回 srv）。
func startTestServerWithS(t *testing.T) (string, func(), *Server) {
	t.Helper()
	dir := t.TempDir()
	cert, err := EnsureCert(dir)
	if err != nil {
		t.Fatalf("测试证书生成失败: %v", err)
	}
	cfg := config.Default()
	cfg.Bind = "127.0.0.1:0"
	cfg.Token = testToken
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := New(cfg, cert, logger, nil)
	srv.authTimeout = 5 * time.Second
	srv.idleTimeout = 45 * time.Second
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	hs := &http.Server{
		Handler:   srv.Handler(),
		TLSConfig: &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12},
	}
	go hs.ServeTLS(ln, "", "")
	cleanup := func() { _ = hs.Close() }
	t.Cleanup(cleanup)
	return "wss://" + ln.Addr().String() + "/ws", cleanup, srv
}

// TestServer_BookMultiCandidates 桔库候选唤醒：命中局面 PC 逐条按 multipv
// 槽位下发全部候选（2026-10-17）——手机端聚合器按槽位聚合后，悬浮窗"变招"
// 按钮即可在桔库多候选局面循环切换。协议零改动（复用 multipv 线序号）。
// 用开局局面（合法可过 handlePosition 入口校验；光帅照面局面会被拒）。
func TestServer_BookMultiCandidates(t *testing.T) {
	fen := "rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR w - - 0 1"
	vkey, _, err := book.FENHashValidated(fen)
	if err != nil {
		t.Fatal(err)
	}
	bookPath := filepath.Join(t.TempDir(), "cand-book.obk")
	makeMultiCandBook(t, bookPath, vkey)
	store, err := book.OpenStore(slog.New(slog.NewTextHandler(os.Stderr, nil)), bookPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	url, cleanup, srv := startTestServerWithS(t)
	srv.SetBook(store)
	t.Cleanup(cleanup)

	ws := dialWS(t, url)
	authClient(t, ws)
	_ = ws.WriteJSON(Position{frameBase: frame(TypePosition), Fen: fen, RedGo: true})

	// 依次收 3 条 suggestion 帧：Multipv 1/2/3，着法按 vscore 降序
	want := []struct {
		move    string
		multipv int
	}{{"h2e2", 1}, {"h2g2", 2}, {"b2e2", 3}}
	for i, w := range want {
		var got *Suggestion
		deadline := time.Now().Add(3 * time.Second)
		for got == nil {
			if time.Now().After(deadline) {
				t.Fatalf("候选 %d 未下发", i+1)
			}
			_ = ws.SetReadDeadline(time.Now().Add(2 * time.Second))
			_, data, err := ws.ReadMessage()
			if err != nil {
				t.Fatalf("读帧失败（候选 %d）: %v", i+1, err)
			}
			if m, perr := ParseFrame(data); perr == nil {
				if s, ok := m.(*Suggestion); ok {
					got = s
				}
			}
		}
		if got.BestMove != w.move || got.Multipv != w.multipv {
			t.Fatalf("候选 %d 不符: got (%s,%d) want (%s,%d)",
				i+1, got.BestMove, got.Multipv, w.move, w.multipv)
		}
		if got.Source != "book:cand-book" || got.Fen != fen {
			t.Fatalf("候选 %d 来源/局面不符: %+v", i+1, got)
		}
		if got.Cached {
			t.Fatalf("桔库命中不是缓存，cached 应为 false: %+v", got)
		}
	}
}

// TestServer_SkipBooks 不走任何库（2026-10-30）：开关开启后建议链跳过全部库
// ——开局局面明明在桔库里，也必须走引擎直搜（fake 桥重放建议，source=engine、
// 引擎确实收到请求）；关闭后同局面重发，回归桔库直答（回归锚）。语义与拦截
// 位置见 handlePosition；沉淀写入行为与开关无关，不在本测覆盖。
func TestServer_SkipBooks(t *testing.T) {
	fb := newFakeBridge(engine.StateReady)
	engineRequests := 0
	fb.onRequest = func(fb *fakeBridge, fen string) {
		engineRequests++
		replayNormalSearch(fb, fen)
	}
	fen := "rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR w - - 0 1"
	vkey, _, err := book.FENHashValidated(fen)
	if err != nil {
		t.Fatal(err)
	}
	bookPath := filepath.Join(t.TempDir(), "skip-book.obk")
	makeMultiCandBook(t, bookPath, vkey)
	store, err := book.OpenStore(slog.New(slog.NewTextHandler(os.Stderr, nil)), bookPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	url, cleanup, srv := startTestServerWithBridgeS(t, fb)
	defer cleanup()
	srv.SetBook(store)
	srv.SetSkipBooks(true) // 开关开启：桔库/沉淀/云库全跳过

	ws := dialWS(t, url)
	defer ws.Close()
	handshakeClient(t, ws)

	_ = ws.WriteJSON(Position{frameBase: frame(TypePosition), Fen: fen, RedGo: true})

	// 开启态：第一个建议必须是引擎直搜（source=engine），book 来源帧绝不允许先到
	_ = ws.SetReadDeadline(time.Now().Add(3 * time.Second))
	defer ws.SetReadDeadline(time.Time{})
	for {
		m := recvLoop(t, ws, &[]string{})
		sug, ok := m.(*Suggestion)
		if !ok {
			continue
		}
		if sug.Source != "engine" {
			t.Fatalf("不走任何库开启时建议来源=%q, want engine（库应全部跳过）", sug.Source)
		}
		break
	}
	if engineRequests == 0 {
		t.Fatal("不走任何库开启时引擎应收到搜索请求")
	}

	// 回归锚：关闭开关后同局面重发（无同 FEN 短路，重新走完整链），桔库直答
	srv.SetSkipBooks(false)
	_ = ws.WriteJSON(Position{frameBase: frame(TypePosition), Fen: fen, RedGo: true})
	for {
		m := recvLoop(t, ws, &[]string{})
		sug, ok := m.(*Suggestion)
		if !ok {
			continue
		}
		if sug.Source == "engine" {
			continue // 关闭前在途的引擎建议（若有）不是本断言目标
		}
		if !strings.HasPrefix(sug.Source, "book:") {
			t.Fatalf("开关关闭后建议来源=%q, want book: 前缀（回归桔库直答）", sug.Source)
		}
		break
	}
}
