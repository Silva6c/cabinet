// format.go 控制台数值/状态的显示格式化（纯函数，单测覆盖）。
package console

import (
	"fmt"
	"strings"
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
