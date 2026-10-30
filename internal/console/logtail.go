// logtail.go 日志文件尾部读取（控制台日志区）。
// 只读文件末尾小块，不加载全文件（lumberjack 滚动下单文件可达 MaxSizeMB）。
package console

import (
	"bytes"
	"io"
	"os"
)

// tailLines 取日志文件最后 n 行；文件不存在/读取失败返回空切片（UI 显示占位）。
func tailLines(path string, n int) []string {
	if path == "" || n <= 0 {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()

	const chunk = 16 << 10 // 每次最多回看 16KB，足够 40 行长日志（日志面板取 logTailLines=40 行）
	st, err := f.Stat()
	if err != nil {
		return nil
	}
	size := st.Size()
	readLen := int64(chunk)
	if size < readLen {
		readLen = size
	}
	buf := make([]byte, readLen)
	if _, err := f.ReadAt(buf, size-readLen); err != nil && err != io.EOF {
		return nil
	}
	// 半行开头丢弃（回看窗口可能切在行中间）
	if readLen == chunk {
		if i := bytes.IndexByte(buf, '\n'); i >= 0 {
			buf = buf[i+1:]
		}
	}
	text := trimTrailingNewline(string(buf))
	lines := splitLines(text)
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines
}

// trimTrailingNewline 去掉末尾换行，避免产生空尾行。
func trimTrailingNewline(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}

// splitLines 按 \n 切行并过滤空行。
func splitLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			line := s[start:i]
			line = trimCR(line)
			if line != "" {
				out = append(out, line)
			}
			start = i + 1
		}
	}
	if start < len(s) {
		if line := trimCR(s[start:]); line != "" {
			out = append(out, line)
		}
	}
	return out
}

func trimCR(s string) string {
	if len(s) > 0 && s[len(s)-1] == '\r' {
		return s[:len(s)-1]
	}
	return s
}
