// 本文件管 UCI 输出行的解析与分数视角换算，全部纯函数、无进程状态；
// 进程生命周期/握手/搜索循环在 engine.go。

package engine

import (
	"sort"
	"strconv"
	"strings"
)

// parseInfo 解析 UCI "info" 行为 Info。无评分（score cp/mate 均缺）时返回 ok=false，
// 与手机端 AnalysisEngine.parseInfo 的过滤口径一致（NNUE 加载等叙述行不带分数）。
func parseInfo(tokens []string) (*Info, bool) {
	info := &Info{MultiPV: 1}
	hasScore := false
	i := 1
	for i < len(tokens) {
		switch tokens[i] {
		case "depth":
			if i+1 < len(tokens) {
				info.Depth, _ = strconv.Atoi(tokens[i+1])
				i++
			}
		case "multipv":
			if i+1 < len(tokens) {
				info.MultiPV, _ = strconv.Atoi(tokens[i+1])
				i++
			}
		case "score":
			if i+2 < len(tokens) {
				switch tokens[i+1] {
				case "cp":
					info.ScoreCP, _ = strconv.Atoi(tokens[i+2])
					hasScore = true
					i += 2
				case "mate":
					// UCI 契约：mate N 正 = 走子方 N 步内将杀对方，负 = 走子方
					// N 步内被将杀。原样保留带符号值，红黑视角换算统一延迟到
					// ToRedScore（黑方行棋时取负）——勿在此取 abs 或提前翻转。
					info.Mate, _ = strconv.Atoi(tokens[i+2])
					info.HasMate = true
					hasScore = true
					i += 2
				}
			}
		case "nodes":
			if i+1 < len(tokens) {
				info.Nodes, _ = strconv.ParseInt(tokens[i+1], 10, 64)
				i++
			}
		case "nps":
			if i+1 < len(tokens) {
				info.NPS, _ = strconv.ParseInt(tokens[i+1], 10, 64)
				i++
			}
		case "hashfull":
			if i+1 < len(tokens) {
				info.Hashfull, _ = strconv.Atoi(tokens[i+1])
				i++
			}
		case "wdl":
			// wdl W D L：胜/和/负概率，走子方视角千分比（UCI_ShowWDL 开启时）
			if i+3 < len(tokens) {
				info.WdlW, _ = strconv.Atoi(tokens[i+1])
				info.WdlD, _ = strconv.Atoi(tokens[i+2])
				info.WdlL, _ = strconv.Atoi(tokens[i+3])
				info.HasWdl = true
				i += 3
			}
		case "pv":
			// pv 之后全是着法（4 字符 UCCI），取完即止
			for _, m := range tokens[i+1:] {
				if len(m) == 4 {
					info.PV = append(info.PV, m)
				}
			}
			return info, hasScore
		case "currmove", "string", "seldepth":
			// 不关心的字段：string 说明行直接终止解析
			if tokens[i] == "string" {
				return nil, false
			}
		}
		i++
	}
	return info, hasScore
}

// sortedLines 把各 MultiPV 线按序号升序整理。
func sortedLines(lines map[int]*Info) []Info {
	keys := make([]int, 0, len(lines))
	for k := range lines {
		keys = append(keys, k)
	}
	sort.Ints(keys)
	out := make([]Info, 0, len(keys))
	for _, k := range keys {
		out = append(out, *lines[k])
	}
	return out
}

// ToRedScore 把走子方视角的原生分换算为红方视角（协议 §4.5：正=红优/红方将杀）。
// redGo 表示该局面下红方行棋（fen 第 2 段 != "b"）。
func ToRedScore(info Info, redGo bool) (cp int, mate int, hasMate bool) {
	if redGo {
		return info.ScoreCP, info.Mate, info.HasMate
	}
	// 黑方视角取负：原生"走子方(黑)优 x" → 红方 -x；原生 mate 正=黑方将杀 → 红方被杀取负
	return -info.ScoreCP, -info.Mate, info.HasMate
}

// FEN 行棋方是否红方（w 或 r 均视为红，上游 restoreFromFEN 同口径）。
// strings.Fields 按任意空白分段：上游 FEN 可能经录屏/日志链路混入多空格/
// 制表符，必须容忍而非按单空格 split；段缺失（len<2）默认红方，纯防御
// （正常路径 FEN 为本端拼装不缺段）。
func RedGoOf(fen string) bool {
	parts := strings.Fields(fen)
	if len(parts) < 2 {
		return true
	}
	return parts[1] != "b"
}

// ToRedWdl 把走子方视角的 WDL（千分比）换算为红方视角：红方行棋原样返回，
// 黑方行棋则胜/负互换（走子方"胜"即红方"负"）。供 suggestion 帧换算使用。
func ToRedWdl(info Info, redGo bool) (w, d, l int) {
	if !info.HasWdl {
		return 0, 0, 0
	}
	if redGo {
		return info.WdlW, info.WdlD, info.WdlL
	}
	return info.WdlL, info.WdlD, info.WdlW
}
