package engine

import (
	"strings"
	"testing"
)

// 本文件为桥层热路径提供量化基线（全仓此前 0 个 Benchmark）：实测 parseInfo
// 全链 ~1.5µs/行、~0.01% 单核（50 行/秒峰值），防止未来有人以"每秒数十条"
// 为由对解析路径做重构式"优化"——池化 Info/PV 会破坏 server 侧对切片的
// 异步共享（server.go sug.PV = info.PV），Scanner 替换 ReadString 有 64KB
// 截断断流风险。任何微优化先跑这里的基线，收益不足一个百分点就不做。

// benchInfoLine 真实形态的 MultiPV info 行（depth 爬升期每秒数十条）。
const benchInfoLine = "info depth 24 seldepth 31 multipv 1 score cp 34 nodes 12400000 " +
	"nps 1800000 hashfull 128 tbhits 0 time 6800 pv h2e2 h9g7 e2e6 b7c7 c4e4 " +
	"c7c4 e4c4 c6e5 c5c4 e5c4 c4c5 a6c5"

func BenchmarkParseInfoLine(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		tokens := strings.Fields(benchInfoLine)
		if _, ok := parseInfo(tokens); !ok {
			b.Fatal("parseInfo 未识别真实 info 行")
		}
	}
}

func BenchmarkRedGoOf(b *testing.B) {
	fen := "r1ba1k2r/9/1c2c4/p1p1p1p1p/9/2P1P3P/P8/1CN2C2N/9/R1BAKAB1R w - - 0 1"
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		RedGoOf(fen)
	}
}
