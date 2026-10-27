package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"cabinet/internal/engine"
)

// sendJSONForTest 仅序列化消息帧（不经过连接），供往返测试使用。
func sendJSONForTest(msg any) ([]byte, error) { return json.Marshal(msg) }

func TestHelloRoundTrip(t *testing.T) {
	in := Hello{frameBase: frame(TypeHello), Server: ServerName, Ver: Version, Caps: []string{CapV1}}
	out := roundTrip[Hello](t, in)
	if out.Server != ServerName || out.Ver != Version || len(out.Caps) != 1 || out.Caps[0] != CapV1 {
		t.Fatalf("字段不匹配: %+v", out)
	}
	if out.Client != "" {
		t.Fatalf("server hello 不应带 client 字段: %q", out.Client)
	}
}

func TestAuthRoundTrip(t *testing.T) {
	in := Auth{frameBase: frame(TypeAuth), Token: "a1b2c3"}
	out := roundTrip[Auth](t, in)
	if out.Token != "a1b2c3" {
		t.Fatalf("token 不匹配: %q", out.Token)
	}
}

func TestPositionRoundTrip(t *testing.T) {
	in := Position{frameBase: frame(TypePosition), Fen: "rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR w - - 0 1", RedGo: true, History: []string{"h2e2"}}
	out := roundTrip[Position](t, in)
	if out.Fen != in.Fen || !out.RedGo || len(out.History) != 1 || out.History[0] != "h2e2" {
		t.Fatalf("字段不匹配: %+v", out)
	}
}

func TestSuggestionScorePerspective(t *testing.T) {
	// score 红方统一视角：cp 正=红优；mate 正=红将杀黑方、负=黑将杀红方（协议 §4.5）
	cp, mate := 120, -3
	in := Suggestion{frameBase: frame(TypeSuggestion), Source: "engine", BestMove: "h2e2",
		Score: &Score{CP: &cp}, PV: []string{"h2e2", "b9c7"}, Depth: 20}
	out := roundTrip[Suggestion](t, in)
	if out.Score == nil || out.Score.CP == nil || *out.Score.CP != 120 || out.Score.Mate != nil {
		t.Fatalf("score 字段不匹配: %+v", out.Score)
	}

	in2 := Suggestion{frameBase: frame(TypeSuggestion), Source: "engine", BestMove: "h2e2", Score: &Score{Mate: &mate}}
	out2 := roundTrip[Suggestion](t, in2)
	if out2.Score == nil || out2.Score.Mate == nil || *out2.Score.Mate != -3 || out2.Score.CP != nil {
		t.Fatalf("mate 字段不匹配: %+v", out2.Score)
	}
}

func TestPingPongTsRoundTrip(t *testing.T) {
	in := Ping{frameBase: frame(TypePing), Ts: 1757459200123}
	out := roundTrip[Ping](t, in)
	if out.Ts != 1757459200123 {
		t.Fatalf("ts 不匹配: %d", out.Ts)
	}
}

func TestEncodeFieldOrder(t *testing.T) {
	// v/t 必须在最外层且优先输出（协议 §2 帧结构示例）
	b, err := sendJSONForTest(Hello{frameBase: frame(TypeHello), Server: ServerName, Ver: Version, Caps: []string{CapV1}})
	if err != nil {
		t.Fatal(err)
	}
	// ver 值跟随 Version 变量（正式构建经 ldflags 注入），本测试锚的是字段顺序
	want := fmt.Sprintf(`{"v":1,"t":"hello","server":"cabinet","ver":%q,"caps":["v1"]}`, Version)
	if string(b) != want {
		t.Fatalf("编码输出不符期望\n got: %s\nwant: %s", b, want)
	}
}

func TestDecodeFrame_BadJSON(t *testing.T) {
	_, _, err := DecodeFrame([]byte("{not json"))
	if !errors.Is(err, ErrBadJSON) {
		t.Fatalf("期望 ErrBadJSON, got %v", err)
	}
}

func TestDecodeFrame_MissingT(t *testing.T) {
	_, _, err := DecodeFrame([]byte(`{"v":1}`))
	if !errors.Is(err, ErrBadFrame) {
		t.Fatalf("期望 ErrBadFrame, got %v", err)
	}
}

func TestDecodeFrame_MissingV(t *testing.T) {
	_, _, err := DecodeFrame([]byte(`{"t":"ping"}`))
	if !errors.Is(err, ErrUnsupportedVer) {
		t.Fatalf("期望 ErrUnsupportedVer, got %v", err)
	}
}

func TestDecodeFrame_FutureVersion(t *testing.T) {
	_, _, err := DecodeFrame([]byte(`{"v":2,"t":"ping"}`))
	if !errors.Is(err, ErrUnsupportedVer) {
		t.Fatalf("期望 ErrUnsupportedVer, got %v", err)
	}
}

func TestParseFrame_UnknownTypeIgnored(t *testing.T) {
	// 未知类型按协议静默忽略：返回 ErrUnknownType，不视为 bad_frame
	_, err := ParseFrame([]byte(`{"v":1,"t":"teleport","x":1}`))
	if !errors.Is(err, ErrUnknownType) {
		t.Fatalf("期望 ErrUnknownType, got %v", err)
	}
}

func TestErrorFrameFor_MapsCodes(t *testing.T) {
	cases := []struct {
		err  error
		code string
	}{
		{ErrBadJSON, CodeBadJSON},
		{ErrBadFrame, CodeBadFrame},
		{ErrUnsupportedVer, CodeUnsupportedVer},
	}
	for _, tc := range cases {
		f := errorFrameFor(tc.err)
		if f.Code != tc.code {
			t.Errorf("错误码映射错误: %v → %s, 期望 %s", tc.err, f.Code, tc.code)
		}
	}
}

// TestStatusSettledFrameShape 搜索收束帧形态（协议 §4.6 定稿）：
// 仅含 state，指标字段（depth/nodes/nps/hashfull）必须缺省——
// 指标仅搜索中才有，客户端以该帧作为本轮建议收束信号。
func TestStatusSettledFrameShape(t *testing.T) {
	b, err := json.Marshal(Status{frameBase: frame(TypeStatus), Engine: EngineStatus{State: engine.StateReady}})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"v":1,"t":"status","engine":{"state":"ready"}}`
	if string(b) != want {
		t.Fatalf("收束帧形态不符\n got: %s\nwant: %s", b, want)
	}
	if bytes.Contains(b, []byte("depth")) || bytes.Contains(b, []byte("nodes")) {
		t.Fatalf("收束帧不应含指标字段: %s", b)
	}
}

// roundTrip 编码后经 ParseFrame 解码，断言类型并返回。
func roundTrip[T any](t *testing.T, in any) T {
	t.Helper()
	b, err := sendJSONForTest(in)
	if err != nil {
		t.Fatal(err)
	}
	out, err := ParseFrame(b)
	if err != nil {
		t.Fatalf("解码失败: %v (帧: %s)", err, b)
	}
	typed, ok := out.(*T)
	if !ok {
		t.Fatalf("解码类型错误: %T", out)
	}
	return *typed
}

// TestSetEnginePointerSemantics set_engine 指针字段：区分"未传"与零值
// （threads=0/hash_mb=0 是合法的自动档），未传字段解码后为 nil（协议 §4.10）。
func TestSetEnginePointerSemantics(t *testing.T) {
	depth := 26
	th0 := 0
	in := SetEngine{frameBase: frame(TypeSetEngine), Depth: &depth, Threads: &th0}
	out := roundTrip[SetEngine](t, in)
	if out.Depth == nil || *out.Depth != 26 {
		t.Fatalf("depth 应为 26: %+v", out.Depth)
	}
	if out.Threads == nil || *out.Threads != 0 {
		t.Fatalf("threads=0（自动档）必须与未传区分: %+v", out.Threads)
	}
	if out.HashMB != nil || out.MultiPV != nil {
		t.Fatalf("未传字段应为 nil: hash=%v multi_pv=%v", out.HashMB, out.MultiPV)
	}
}

// TestSuggestionWDLRoundTrip suggestion 帧的 wdl_w/d/l 可选字段编解码往返。
func TestSuggestionWDLRoundTrip(t *testing.T) {
	w, d, l := 62, 31, 7
	in := Suggestion{frameBase: frame(TypeSuggestion), Source: "engine", BestMove: "h2e2",
		WdlW: &w, WdlD: &d, WdlL: &l}
	out := roundTrip[Suggestion](t, in)
	if out.WdlW == nil || *out.WdlW != 62 || out.WdlD == nil || *out.WdlD != 31 || out.WdlL == nil || *out.WdlL != 7 {
		t.Fatalf("wdl 字段不匹配: %v/%v/%v", out.WdlW, out.WdlD, out.WdlL)
	}
	// 无 wdl 时缺省（omitempty），旧端忽略新字段
	in2 := Suggestion{frameBase: frame(TypeSuggestion), Source: "engine", BestMove: "h2e2"}
	out2 := roundTrip[Suggestion](t, in2)
	if out2.WdlW != nil || out2.WdlD != nil || out2.WdlL != nil {
		t.Fatalf("无 wdl 时应缺省: %v/%v/%v", out2.WdlW, out2.WdlD, out2.WdlL)
	}
}
