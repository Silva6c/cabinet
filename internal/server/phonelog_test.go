package server

import (
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
