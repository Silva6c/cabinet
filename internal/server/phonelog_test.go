package server

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 手机日志单局分卷（协议 §4.11）：手机端识别到新对局上行"===== 新局开始"
// 分界行，PC 落盘检测到即切换 phone-<开始时刻>-局<N>.log——单日单文件混
// 多局检索困难（2026-10-01 用户反馈），一局一文件按局定位。

func TestPhoneLogHasGameSplit(t *testing.T) {
	cases := []struct {
		name  string
		lines []string
		want  bool
	}{
		{"空批", nil, false},
		{"普通日志行", []string{"09:44:12 I/AssistService: board confirmed: rnbakabnr"}, false},
		{"分界行_带手机行前缀", []string{
			"09:44:12 I/AssistService: board confirmed: rnbakabnr",
			"09:44:13 I/AssistService: ===== 新局开始 fen=rnbakabnr/9/9 1",
		}, true},
		{"分界行_嵌在中间", []string{
			"x", "10:00:00.000 D/AssistService: ===== 新局开始 fen=abc", "y",
		}, true},
	}
	for _, tc := range cases {
		if got := phoneLogHasGameSplit(tc.lines); got != tc.want {
			t.Errorf("%s: phoneLogHasGameSplit = %v, 期望 %v", tc.name, got, tc.want)
		}
	}
}

func TestPhoneLogFileName(t *testing.T) {
	ts := time.Date(2026, 10, 1, 14, 30, 5, 0, time.Local)
	got := phoneLogFileName(ts, 3)
	wantPrefix := "phone-20261001-143005-局3.log"
	if got != wantPrefix {
		t.Errorf("phoneLogFileName = %q, 期望 %q", got, wantPrefix)
	}
	if !strings.HasSuffix(got, ".log") {
		t.Errorf("文件名应以 .log 结尾: %q", got)
	}
}

// TestSanitizePhoneLogLine 行清洗（2026-11-14 红队第二轮）：行内 \r\n 与控制
// 字符剥离防伪造行，\t 保留（无法伪造行边界）。
func TestSanitizePhoneLogLine(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"普通行", "board confirmed: rnbakabnr", "board confirmed: rnbakabnr"},
		{"行内换行剥离", "fake\r\nline2", "fakeline2"},
		{"裸 LF 剥离", "a\nb", "ab"},
		{"制表符保留", "a\tb", "a\tb"},
		{"NUL 与 DEL 剥离", "a\x00b\x7fc", "abc"},
	}
	for _, tc := range cases {
		if got := sanitizePhoneLogLine(tc.in); got != tc.want {
			t.Errorf("%s: sanitizePhoneLogLine(%q) = %q, want %q", tc.name, tc.in, got, tc.want)
		}
	}
}

// TestHandleLogFrameQuotaAndSanitize 每帧行数配额 + 行清洗端到端：1000 行
// （行内嵌 \r\n）上行，落盘恰 400 行且无控制字符。
func TestHandleLogFrameQuotaAndSanitize(t *testing.T) {
	url, cleanup, srv := startTestServerWithS(t)
	defer cleanup()
	ws := dialWS(t, url)
	defer ws.Close()
	authClient(t, ws)

	lines := make([]string, 0, 1000)
	for i := 0; i < 1000; i++ {
		lines = append(lines, fmt.Sprintf("line-%d\r\ninjected", i))
	}
	_ = ws.WriteJSON(Log{frameBase: frame(TypeLog), Lines: lines})

	dir := filepath.Join(filepath.Dir(srv.cfg.Log.File), "phone")
	var content []byte
	deadline := time.Now().Add(2 * time.Second)
	for {
		entries, _ := os.ReadDir(dir)
		if len(entries) > 0 {
			data, err := os.ReadFile(filepath.Join(dir, entries[0].Name()))
			if err == nil && len(data) > 0 {
				content = data
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("手机日志未落盘")
		}
		time.Sleep(20 * time.Millisecond)
	}
	got := string(content)
	if n := strings.Count(got, "\n"); n != phoneLogMaxLinesPerFrame {
		t.Fatalf("落盘行数=%d, want %d（配额封顶）", n, phoneLogMaxLinesPerFrame)
	}
	if strings.ContainsRune(got, '\r') {
		t.Fatalf("行内控制字符未剥离: %q", got[:min(120, len(got))])
	}
}

// TestEnforcePhoneLogBudget 目录容量兜底（2026-11-14 红队第二轮）：超预算时
// 按修改时间从旧到新删除直到回落。
func TestEnforcePhoneLogBudget(t *testing.T) {
	dir := t.TempDir()
	for i, name := range []string{"phone-a.log", "phone-b.log", "phone-c.log"} {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, bytes.Repeat([]byte("x"), 100), 0o644); err != nil {
			t.Fatal(err)
		}
		if i == 0 { // a 最旧
			old := time.Now().Add(-time.Hour)
			if err := os.Chtimes(path, old, old); err != nil {
				t.Fatal(err)
			}
		}
	}
	enforcePhoneLogBudget(dir, 250)
	if _, err := os.Stat(filepath.Join(dir, "phone-a.log")); !os.IsNotExist(err) {
		t.Fatal("最旧文件应被预算删除")
	}
	if _, err := os.Stat(filepath.Join(dir, "phone-c.log")); err != nil {
		t.Fatal("预算内文件不应被删")
	}
}
