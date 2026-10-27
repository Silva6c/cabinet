package server

import (
	"bytes"
	"encoding/json"
	"testing"
)

// 本文件是协议帧的逐字节兼容锚：suggestion 帧此前没有任何字节级 golden
//（hello 与收束 status 各有一条），未来任何编码路径改动（手写编码器、
// 字段调整、omitempty 语义变化）都必须以这里的 golden 为准绳。
// golden 是"当前输出钉死"，不是规范文本——协议文档在 docs/protocol-v1.md。

// benchSuggestion 构造真实形态的 suggestion 帧：90B FEN + 12 步 PV +
// 三指针 WDL + 线序（MultiPV>1 时手机端按槽位聚合的真实输入）。
func benchSuggestion() Suggestion {
	return Suggestion{
		frameBase: frame(TypeSuggestion),
		Source:    "engine",
		Fen:       "r1ba1k2r/9/1c2c4/p1p1p1p1p/9/2P1P3P/P8/1CN2C2N/9/R1BAKAB1R w - - 0 1",
		BestMove:  "h2e2",
		Score:     &Score{CP: intPtr(-34)},
		PV: []string{
			"h2e2", "h9g7", "e2e6", "b7c7", "c4e4", "c7c4",
			"e4c4", "c6e5", "c4c5", "e5c4", "c5c4", "a6c5",
		},
		Depth:   24,
		WdlW:    intPtr(40),
		WdlD:    intPtr(35),
		WdlL:    intPtr(25),
		Multipv: 1,
	}
}

func intPtr(v int) *int { return &v }

// suggestionGolden 钉死字段序与 omitempty 语义：frameBase(v,t) 展开最前，
// Source 恒输出，Fen 非空，Score/PV/WDL*/Depth/Multipv 均非零值全输出。
const suggestionGolden = `{"v":1,"t":"suggestion","source":"engine",` +
	`"fen":"r1ba1k2r/9/1c2c4/p1p1p1p1p/9/2P1P3P/P8/1CN2C2N/9/R1BAKAB1R w - - 0 1",` +
	`"bestmove":"h2e2","score":{"cp":-34},` +
	`"pv":["h2e2","h9g7","e2e6","b7c7","c4e4","c7c4","e4c4","c6e5","c4c5","e5c4","c5c4","a6c5"],` +
	`"depth":24,"wdl_w":40,"wdl_d":35,"wdl_l":25,"multipv":1}`

// TestSuggestionGoldenByteExact suggestion 帧逐字节兼容锚。
func TestSuggestionGoldenByteExact(t *testing.T) {
	got, err := json.Marshal(benchSuggestion())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, []byte(suggestionGolden)) {
		t.Fatalf("suggestion 帧字节兼容被破坏:\n got: %s\nwant: %s", got, suggestionGolden)
	}
}

// TestSuggestionCachedField cached 字段的序列化形态（2026-10-17 沉淀命中标记）：
// true 时输出 "cached":true 且位于 multipv 之后；false 时 omitempty 整体省略
// （该形态已由 golden 锚钉死——benchSuggestion 的 Cached 恒为零值）。
// 单列本用例是为钉死新字段的编码形态，避免扰动既有逐字节锚。
func TestSuggestionCachedField(t *testing.T) {
	sug := benchSuggestion()
	sug.Cached = true
	got, err := json.Marshal(sug)
	if err != nil {
		t.Fatal(err)
	}
	wantSuffix := `"multipv":1,"cached":true}`
	if !bytes.HasSuffix(got, []byte(wantSuffix)) {
		t.Fatalf("cached=true 应输出在 multipv 之后:\n got: %s\nwant尾部: %s", got, wantSuffix)
	}
}

func BenchmarkSuggestionMarshal(b *testing.B) {
	msg := benchSuggestion()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := json.Marshal(msg); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkStatusMarshal 带引擎指标的 status 帧（广播路径 500ms 节流后仍有
// 每秒数次），与收束 status 的既有 golden（protocol_test.go）互为补充。
func BenchmarkStatusMarshal(b *testing.B) {
	msg := Status{
		frameBase: frame(TypeStatus),
		Engine: EngineStatus{
			State: "searching", Depth: 18, Nodes: 9_400_000,
			NPS: 1_600_000, Hashfull: 96,
		},
	}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := json.Marshal(msg); err != nil {
			b.Fatal(err)
		}
	}
}
