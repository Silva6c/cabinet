// format.go 控制台数值/状态的显示格式化（纯函数，单测覆盖）。
package console

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"
)

// fmtState 引擎状态中文名。
func fmtState(state string) string {
	switch state {
	case "loading":
		return "加载中"
	case "ready":
		return "就绪"
	case "error":
		return "错误"
	case "offline":
		return "未启用"
	default:
		return orDash(state)
	}
}

// fmtInt 千分位整数（1,234,567）。
func fmtInt(v int64) string {
	s := fmt.Sprintf("%d", v)
	if len(s) <= 3 {
		return s
	}
	var sb strings.Builder
	pre := len(s) % 3
	if pre > 0 {
		sb.WriteString(s[:pre])
	}
	for i := pre; i < len(s); i += 3 {
		if sb.Len() > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString(s[i : i+3])
	}
	return sb.String()
}

// fmtNPS 搜索速率主数字（单位 "n/s" 由界面后缀小字显示，见 statBig）：
// >1M 显示 "8.52M"，>1K 显示 "923.4K"，其余整数。
func fmtNPS(nps int64) string {
	if nps <= 0 {
		return "—"
	}
	switch {
	case nps >= 1_000_000:
		return fmt.Sprintf("%.2fM", float64(nps)/1_000_000)
	case nps >= 1_000:
		return fmt.Sprintf("%.1fK", float64(nps)/1_000)
	default:
		return fmt.Sprintf("%d", nps)
	}
}

// fmtCompactN 大数紧凑显示（节点数）：≥100 万用 "12.4M"，≥1 千用 "923.4K"，
// 其余千分位整数——bento 半列宽内保持单行可读。
func fmtCompactN(v int64) string {
	switch {
	case v >= 1_000_000:
		return trimFloat(float64(v)/1_000_000) + "M"
	case v >= 1_000:
		return trimFloat(float64(v)/1_000) + "K"
	default:
		return fmtInt(v)
	}
}

// trimFloat 一位小数、去掉尾零（12.0 → "12"）。
func trimFloat(v float64) string {
	return strings.TrimSuffix(fmt.Sprintf("%.1f", v), ".0")
}

// fmtHashUsed 哈希占用 "128M/2G"（已用/配置；设计稿 §06 紧凑 kv 样式）。
// 已用 = HashMB × hashfull‰；配置缺失或尚未报告占用返回 "—"。
func fmtHashUsed(hashfull int, hashMB int) string {
	if hashMB <= 0 || hashfull <= 0 {
		return "—"
	}
	return fmtMBShort(int64(hashMB)*int64(hashfull)/1000) + "/" + fmtMBShort(int64(hashMB))
}

// fmtMBShort 容量短式：≥1G 且整除显示 "2G"，否则 "512M"。
func fmtMBShort(mb int64) string {
	if mb >= 1024 && mb%1024 == 0 {
		return fmt.Sprintf("%dG", mb/1024)
	}
	return fmt.Sprintf("%dM", mb)
}

// fpShort 证书指纹分组短式（设计稿 §06）：剔除常见前缀与分隔符后取前 12 hex
// 分 3 组 + 省略号 + 末 4 位——完整指纹生成二维码时原样使用，界面只作人工核对。
func fpShort(hex string) string {
	hex = strings.ToLower(strings.TrimSpace(hex))
	for _, pre := range []string{"sha256:", "sha1:", "sha:", "sha256 ", "sha1 ", "sha "} {
		if strings.HasPrefix(hex, pre) {
			hex = hex[len(pre):]
			break
		}
	}
	hex = strings.Map(func(r rune) rune {
		switch r {
		case ':', ' ', '-':
			return -1
		}
		return r
	}, hex)
	if len(hex) < 16 {
		return hex
	}
	return hex[0:4] + " " + hex[4:8] + " " + hex[8:12] + " … " + hex[len(hex)-4:]
}

// fmtThreads 线程配置显示；0 = 引擎自定。
func fmtThreads(n int) string {
	if n <= 0 {
		return "自动"
	}
	return fmt.Sprintf("%d", n)
}

// fmtRTT 往返延迟；-1 = 尚未测得。
func fmtRTT(ms int64) string {
	if ms < 0 {
		return "—"
	}
	return fmt.Sprintf("%d ms", ms)
}

// fmtScore 分数（红方视角）：杀棋 "M3"/"-M2"；空传 "—"/"+2.60"/"-1.10"。
func fmtScore(cp, mate *int) string {
	if mate != nil {
		if *mate > 0 {
			return fmt.Sprintf("M%d", *mate)
		}
		return fmt.Sprintf("-M%d", -*mate)
	}
	if cp != nil {
		return fmt.Sprintf("%+.2f", float64(*cp)/100.0)
	}
	return "—"
}

// ---------- 日志尾部紧凑渲染（2026-10-29 日志显示优化） ----------

// logEntry 一行日志的结构化解析结果（slog TextHandler 产出格式）。
type logEntry struct {
	time  string // HH:MM:SS（取自 ISO 时间戳时分秒段）；异构行（解析失败）为空
	level string // INFO / WARN / ERROR / DEBUG；异构行为空
	msg   string // 消息主体（带引号形态已反转义）
	rest  string // 附加属性原文（key=value 空格分隔），可为空
}

// logLineRe 解析 slog TextHandler 单行：
// time=<ts> level=<lv> msg=<"转义串" | 无空白串> [attrs...]。
// 带引号/不带引号两种形态由 group3 是否以引号开头区分（无引号串不含空白，
// 引号内可含任意转义字符）。
var logLineRe = regexp.MustCompile(`^time=(\S+) level=(\w+) msg=("((?:[^"\\]|\\.)*)"|(\S+))(?:\s+(.*))?$`)

// parseLogLine 解析一行日志；解析失败 ok=false（调用方原样显示兜底）。
func parseLogLine(line string) (logEntry, bool) {
	m := logLineRe.FindStringSubmatch(line)
	if m == nil {
		return logEntry{}, false
	}
	e := logEntry{level: m[2], rest: m[6]}
	if strings.HasPrefix(m[3], `"`) {
		// 带引号形态：strconv.AppendQuote 的逆过程（失败保原文，不做二次猜测）
		if u, err := strconv.Unquote(m[3]); err == nil {
			e.msg = u
		} else {
			e.msg = m[4]
		}
	} else {
		e.msg = m[5]
	}
	if len(m[1]) >= 19 && m[1][10] == 'T' {
		e.time = m[1][11:19] // ISO 时间戳取本地时分秒段
	} else {
		e.time = m[1]
	}
	return e, true
}

// isJunkLogLine 垃圾行判定（显示层兜底，剔除旧日志文件里的存量噪音；
// 源头根治见 tune.go 调参静默与 fynel10n.go 翻译补丁）：
// ① Fyne 翻译失败三连（主行 + Cause: + At:）；② Fyne 错误的缩进续行
// （真实 Fyne 错误保留主行可诊断）。
func isJunkLogLine(e logEntry) bool {
	return strings.Contains(e.msg, "Translation failure") ||
		strings.Contains(e.msg, "not found in language") ||
		strings.HasPrefix(e.msg, "  ")
}

// compactLogLines 批量解析 + 剔除垃圾行（UI 映射前的唯一纯函数入口，便于单测）。
// 解析失败的异构行原样透传（time/level 为空 = 渲染时不加前缀）。
func compactLogLines(lines []string) []logEntry {
	out := make([]logEntry, 0, len(lines))
	for _, ln := range lines {
		e, ok := parseLogLine(ln)
		if !ok {
			out = append(out, logEntry{msg: ln})
			continue
		}
		if isJunkLogLine(e) {
			continue
		}
		out = append(out, e)
	}
	return out
}

// logLineText 紧凑行文本：时间 + 级别（5 宽对齐）+ 消息 + 属性；异构行原样。
func logLineText(e logEntry) string {
	if e.time == "" && e.level == "" {
		return e.msg
	}
	line := e.time + " " + fmt.Sprintf("%-5s", e.level) + " " + e.msg
	if e.rest != "" {
		line += " " + e.rest
	}
	return line
}

// logLevelColor 级别 → 主题色名（WARN 琥珀 / ERROR 警示红，其余前景色）。
func logLevelColor(level string) fyne.ThemeColorName {
	switch level {
	case "WARN":
		return qiColorLogWarn
	case "ERROR":
		return qiColorLogError
	}
	return theme.ColorNameForeground
}

// fmtSearchElapsed 时间制仪表的本轮已搜用时：空闲 "—"，其余 "2.3s"（时间制
// 上限 500ms–20s，小数一位足够读）。
func fmtSearchElapsed(ms int64) string {
	if ms <= 0 {
		return "—"
	}
	return fmt.Sprintf("%.1fs", float64(ms)/1000.0)
}
