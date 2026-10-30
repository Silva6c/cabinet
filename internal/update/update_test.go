package update

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newTestChecker 走 New 构造（保证初始状态与客户端齐全）后注入 apiBase。
func newTestChecker(t *testing.T, apiBase, version string) *Checker {
	t.Helper()
	c := New("Silva6c/cabinet", version, slog.New(slog.NewTextHandler(io.Discard, nil)))
	c.apiBase = apiBase
	return c
}

// setupUpdateServer 起 httptest：/releases 返回 stable-20261101（带升级包资产，
// 下载地址按请求 Host 回填），/upd.zip 提供 zip 内容（zipDelay>0 时延迟响应，
// 供取消测试）。资产 digest 为 zipBytes 的 sha256（模拟 GitHub 官方值）。
func setupUpdateServer(t *testing.T, zipBytes []byte, zipDelay time.Duration) *httptest.Server {
	t.Helper()
	sum := sha256.Sum256(zipBytes)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/Silva6c/cabinet/releases", func(w http.ResponseWriter, r *http.Request) {
		rels := []ghRelease{{
			TagName: "stable-20261101",
			HTMLURL: "https://example.com/releases/tag/stable-20261101",
			Assets: []ghAsset{{
				Name:               "Cabinet-Update-20261101.zip",
				BrowserDownloadURL: "http://" + r.Host + "/upd.zip",
				Digest:             digest,
				Size:               int64(len(zipBytes)),
			}},
		}}
		_ = json.NewEncoder(w).Encode(rels)
	})
	mux.HandleFunc("/upd.zip", func(w http.ResponseWriter, r *http.Request) {
		if zipDelay > 0 {
			// 监听请求上下文：客户端取消时立即返回，服务端不干等（Close 不阻塞）
			select {
			case <-r.Context().Done():
				return
			case <-time.After(zipDelay):
			}
		}
		_, _ = w.Write(zipBytes)
	})
	return httptest.NewServer(mux)
}

// TestParseBuildDate 版本串 → 构建日期。
func TestParseBuildDate(t *testing.T) {
	cases := []struct{ ver, want string }{
		{"vc26-20261028", "20261028"},
		{"vc1-20261101", "20261101"},
		{"dev-0", ""},
		{"0.1.0-dev", ""},
		{"", ""},
		{"vc26-20261028-x", ""},
	}
	for _, c := range cases {
		if got := parseBuildDate(c.ver); got != c.want {
			t.Errorf("parseBuildDate(%q) = %q, want %q", c.ver, got, c.want)
		}
	}
}

// TestReleaseDate 版本日期两种来源：旧制取 tag 日期，新制取发布日期换算北京时间。
func TestReleaseDate(t *testing.T) {
	cases := []struct {
		rel  ghRelease
		want string
	}{
		{ghRelease{TagName: "stable-20261028"}, "20261028"},
		// UTC 01:00 发布 = 北京 09:00，同日
		{ghRelease{TagName: "v1.0.0", PublishedAt: time.Date(2026, 10, 30, 1, 0, 0, 0, time.UTC)}, "20261030"},
		// UTC 17:00 发布 = 北京次日 01:00，必须按北京时区换算
		{ghRelease{TagName: "v1.0.1", PublishedAt: time.Date(2026, 10, 30, 17, 0, 0, 0, time.UTC)}, "20261031"},
		{ghRelease{TagName: "v1.0.0"}, ""},     // 缺 published_at
		{ghRelease{TagName: "assets"}, ""},     // 版权物 Release 不参与
		{ghRelease{TagName: "v1.0"}, ""},       // 非三段式不算正式版
		{ghRelease{TagName: "v1.0.0-rc1"}, ""}, // 预发布形态不算
	}
	for _, c := range cases {
		if got := releaseDate(&c.rel); got != c.want {
			t.Errorf("releaseDate(%q) = %q, want %q", c.rel.TagName, got, c.want)
		}
	}
}

// TestPickLatestStable draft/prerelease/非正式版 tag 全过滤，乱序取日期最大；
// 新制 vX.Y.Z 与旧制 stable-YYYYMMDD 混排时按版本日期统一比较。
func TestPickLatestStable(t *testing.T) {
	rels := []ghRelease{
		{TagName: "assets"},                            // 非正式版 tag
		{TagName: "stable-20261001", Draft: true},      // draft
		{TagName: "stable-20261015", Prerelease: true}, // prerelease
		{TagName: "stable-20261020"},
		{TagName: "stable-20261028"},
		{TagName: "stable-bad-date"}, // 格式不对
	}
	if got := pickLatestStable(rels); got == nil || got.TagName != "stable-20261028" {
		t.Fatalf("pickLatestStable = %+v, want stable-20261028", got)
	}
	if pickLatestStable(nil) != nil {
		t.Fatal("空列表应返回 nil")
	}
	onlyPre := []ghRelease{{TagName: "stable-20261020"}, {TagName: "stable-20261028", Prerelease: true}}
	if got := pickLatestStable(onlyPre); got == nil || got.TagName != "stable-20261020" {
		t.Fatalf("prerelease 应被排除, got %+v", got)
	}
	// v 标签按发布日期胜过更早的日期标签（迁移期两制并存）
	withV := []ghRelease{
		{TagName: "stable-20261028", PublishedAt: time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)},
		{TagName: "v1.0.0", PublishedAt: time.Date(2026, 10, 30, 1, 0, 0, 0, time.UTC)},
	}
	if got := pickLatestStable(withV); got == nil || got.TagName != "v1.0.0" {
		t.Fatalf("v 标签应胜出, got %+v", got)
	}
	// 同日并列取发布时刻更晚者
	sameDay := []ghRelease{
		{TagName: "stable-20261030", PublishedAt: time.Date(2026, 10, 30, 1, 0, 0, 0, time.UTC)},
		{TagName: "v1.0.0", PublishedAt: time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)},
	}
	if got := pickLatestStable(sameDay); got == nil || got.TagName != "v1.0.0" {
		t.Fatalf("同日应取发布更晚者, got %+v", got)
	}
	if pickLatestStable([]ghRelease{{TagName: "v2.0.0"}}) != nil {
		t.Fatal("v 标签缺 published_at 应跳过")
	}
}

// TestPickUpdateAsset 按"升级包类型 + 日期最大"匹配：v 标签不含日期，资产名
// 日期是构建日，不能再要求与 tag 同日；便携包/APK 不混入。
func TestPickUpdateAsset(t *testing.T) {
	rel := &ghRelease{
		TagName: "v1.0.0",
		Assets: []ghAsset{
			{Name: "Cabinet-Portable-20261030-Full.zip", BrowserDownloadURL: "http://x/full.zip", Digest: "sha256:aa"},
			{Name: "Cabinet-Update-20261028.zip", BrowserDownloadURL: "http://x/old.zip", Digest: "sha256:cc"},
			{Name: "Cabinet-Update-20261030.zip", BrowserDownloadURL: "http://x/upd.zip", Digest: "sha256:bb", Size: 100},
			{Name: "cabinet_20261030_vc4_arm64.apk", BrowserDownloadURL: "http://x/a.apk", Digest: "sha256:dd"},
		},
	}
	name, url, digest, size := pickUpdateAsset(rel)
	if name != "Cabinet-Update-20261030.zip" || url != "http://x/upd.zip" || digest != "sha256:bb" || size != 100 {
		t.Fatalf("pickUpdateAsset = %q %q %q %d", name, url, digest, size)
	}
	if name, _, _, _ := pickUpdateAsset(&ghRelease{TagName: "v1.0.0"}); name != "" {
		t.Fatalf("无升级包应返回空, got %q", name)
	}
}

// TestCheck_Flow 检查链路五分支：有新版 / 已最新 / 404 静默失败 / 无稳定版 /
// 开发构建无法比对。
func TestCheck_Flow(t *testing.T) {
	zipBytes := makeTestZip(t, []byte("MZfake-exe"))
	digest := "sha256:" + hex.EncodeToString(func() []byte { sum := sha256.Sum256(zipBytes); return sum[:] }())

	mkSrv := func(body string, code int) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(code)
			_, _ = w.Write([]byte(body))
		}))
	}

	t.Run("发现新版本", func(t *testing.T) {
		var srvURL string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			rels := []ghRelease{{
				TagName: "stable-20261101",
				HTMLURL: "https://example.com/r",
				Assets: []ghAsset{{
					Name:               "Cabinet-Update-20261101.zip",
					BrowserDownloadURL: srvURL + "/upd.zip",
					Digest:             digest,
					Size:               int64(len(zipBytes)),
				}},
			}}
			_ = json.NewEncoder(w).Encode(rels)
		}))
		defer srv.Close()
		srvURL = srv.URL

		c := newTestChecker(t, srv.URL, "vc5-20261028")
		c.Check(context.Background())
		s := c.Snapshot()
		if s.State != StateUpdateAvail {
			t.Fatalf("state = %s (%s), want update_available", s.State, s.Message)
		}
		if !s.CanInstall || s.LatestTag != "stable-20261101" {
			t.Fatalf("CanInstall=%v LatestTag=%s", s.CanInstall, s.LatestTag)
		}
	})

	t.Run("已是最新", func(t *testing.T) {
		srv := mkSrv(`[{"tag_name":"stable-20261001","html_url":"https://example.com/r"}]`, http.StatusOK)
		defer srv.Close()
		c := newTestChecker(t, srv.URL, "vc9-20261201")
		c.Check(context.Background())
		if s := c.Snapshot(); s.State != StateUpToDate || !strings.Contains(s.Message, "已是最新") {
			t.Fatalf("state = %s (%s)", s.State, s.Message)
		}
	})

	t.Run("私有仓404静默失败", func(t *testing.T) {
		srv := mkSrv("Not Found", http.StatusNotFound)
		defer srv.Close()
		c := newTestChecker(t, srv.URL, "vc5-20261028")
		c.Check(context.Background())
		if s := c.Snapshot(); s.State != StateFailed || s.Message == "" {
			t.Fatalf("state = %s (%s)", s.State, s.Message)
		}
	})

	t.Run("无稳定版按最新处理", func(t *testing.T) {
		srv := mkSrv(`[]`, http.StatusOK)
		defer srv.Close()
		c := newTestChecker(t, srv.URL, "vc5-20261028")
		c.Check(context.Background())
		if s := c.Snapshot(); s.State != StateUpToDate {
			t.Fatalf("state = %s (%s)", s.State, s.Message)
		}
	})

	t.Run("开发构建无法比对", func(t *testing.T) {
		srv := mkSrv(`[{"tag_name":"stable-20261101"}]`, http.StatusOK)
		defer srv.Close()
		c := newTestChecker(t, srv.URL, "0.1.0-dev")
		c.Check(context.Background())
		if s := c.Snapshot(); s.State != StateUnknownVer {
			t.Fatalf("state = %s (%s)", s.State, s.Message)
		}
	})
}

// TestDownloadInstall_Flow 下载安装全链路：成功 / digest 不符 / 用户取消。
func TestDownloadInstall_Flow(t *testing.T) {
	payload := append([]byte("MZ"), []byte(strings.Repeat("A", 4096))...)
	zipBytes := makeTestZip(t, payload)

	t.Run("成功就绪", func(t *testing.T) {
		srv := setupUpdateServer(t, zipBytes, 0)
		defer srv.Close()
		c := newTestChecker(t, srv.URL, "vc5-20261028")
		c.Check(context.Background())
		if !c.CanInstall() {
			t.Fatalf("前置: CanInstall=false")
		}
		dir := t.TempDir()
		c.downloadInstallTo(context.Background(), dir, "app.exe")

		s := c.Snapshot()
		if s.State != StateReadyReboot {
			t.Fatalf("state = %s (%s)", s.State, s.Message)
		}
		got, err := os.ReadFile(filepath.Join(dir, "app.exe.new"))
		if err != nil || string(got) != string(payload) {
			t.Fatalf("新 exe 内容不符: %v %d 字节", err, len(got))
		}
		for _, residue := range []string{"cabinet-update.zip", "cabinet-update.zip.part"} {
			if _, err := os.Stat(filepath.Join(dir, residue)); !os.IsNotExist(err) {
				t.Fatalf("残留 %s 未清理", residue)
			}
		}
	})

	t.Run("digest不符删净回退", func(t *testing.T) {
		srv := setupUpdateServer(t, zipBytes, 0)
		defer srv.Close()
		c := newTestChecker(t, srv.URL, "vc5-20261028")
		c.Check(context.Background())
		c.mu.Lock()
		c.assetDigest = "sha256:" + strings.Repeat("ab", 32) // 篡改为错误值
		c.mu.Unlock()

		dir := t.TempDir()
		c.downloadInstallTo(context.Background(), dir, "app.exe")
		if s := c.Snapshot(); s.State != StateFailed {
			t.Fatalf("state = %s (%s)", s.State, s.Message)
		}
		if _, err := os.Stat(filepath.Join(dir, "cabinet-update.zip")); !os.IsNotExist(err) {
			t.Fatal("坏 zip 未清理")
		}
		if _, err := os.Stat(filepath.Join(dir, "app.exe.new")); !os.IsNotExist(err) {
			t.Fatal("坏 .new 未清理")
		}
	})

	t.Run("用户取消回退可重试", func(t *testing.T) {
		srv := setupUpdateServer(t, zipBytes, 10*time.Second) // 响应挂 10s，制造下载中窗口
		defer srv.Close()
		c := newTestChecker(t, srv.URL, "vc5-20261028")
		c.Check(context.Background())

		dir := t.TempDir()
		done := make(chan struct{})
		go func() {
			c.downloadInstallTo(context.Background(), dir, "app.exe")
			close(done)
		}()
		time.Sleep(300 * time.Millisecond)
		c.Cancel()
		<-done

		if s := c.Snapshot(); s.State != StateUpdateAvail || !strings.Contains(s.Message, "取消") {
			t.Fatalf("state = %s (%s)", s.State, s.Message)
		}
		if _, err := os.Stat(filepath.Join(dir, "cabinet-update.zip.part")); !os.IsNotExist(err) {
			t.Fatal("半截下载未清理")
		}
		// 取消后可重新发起（状态机回退正确）
		if !c.CanInstall() {
			t.Fatal("取消后应可重新安装")
		}
	})

	t.Run("状态不对直接忽略", func(t *testing.T) {
		c := newTestChecker(t, "http://127.0.0.1:1", "vc5-20261028") // 未 Check，idle
		dir := t.TempDir()
		c.downloadInstallTo(context.Background(), dir, "app.exe")
		if s := c.Snapshot(); s.State != StateIdle {
			t.Fatalf("idle 态不应有任何动作, got %s", s.State)
		}
	})
}

// makeTestZip 内存构造升级包（entry 带目录前缀，验证 basename 匹配）。
func makeTestZip(t *testing.T, payload []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("cabinet/app.exe")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// TestCheck_DirectFallback 代理路径失败时直连兜底：系统代理开着但节点抖动
// （TUN 用户常见组合）时，直连可达即保成功率——2026-10-28 真机实录落地。
func TestCheck_DirectFallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[{"tag_name":"stable-20261001","html_url":"https://example.com/r"}]`))
	}))
	defer srv.Close()

	c := newTestChecker(t, srv.URL, "vc9-20261201")
	c.proxy = func(*http.Request) (*url.URL, error) {
		return &url.URL{Scheme: "http", Host: "127.0.0.1:1"}, nil // 恒指向死端口
	}
	c.client = newClient(time.Second, c.proxy)
	c.directClient = newClient(time.Second, nil)
	c.Check(context.Background())
	if s := c.Snapshot(); s.State != StateUpToDate || !strings.Contains(s.Message, "已是最新") {
		t.Fatalf("state = %s (%s), want 直连兜底后 up_to_date", s.State, s.Message)
	}
}

// TestCheck_BothPathsFail 代理与直连全败：上屏文案为分类短句——不得含 URL
// 或 Go 错误链（长串撑破面板的实录），全量细节只进日志。
func TestCheck_BothPathsFail(t *testing.T) {
	c := newTestChecker(t, "https://127.0.0.1:1", "vc1-20260101") // 端口 1 恒拒连
	c.Check(context.Background())
	s := c.Snapshot()
	if s.State != StateFailed {
		t.Fatalf("state = %s (%s), want failed", s.State, s.Message)
	}
	if strings.Contains(s.Message, "http") || len([]rune(s.Message)) > 40 {
		t.Fatalf("文案应为无 URL 短句, got %q", s.Message)
	}
}

// TestFailureMessage 分类表：超时/中断/拒连三类归位为短句，且一律不带 URL。
func TestFailureMessage(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"超时", &url.Error{Op: "Get", URL: "https://api.github.com/x", Err: context.DeadlineExceeded}, "网络超时"},
		{"中断", &url.Error{Op: "Get", URL: "https://api.github.com/x", Err: io.EOF}, "中断"},
		{"拒连", &url.Error{Op: "Get", URL: "https://api.github.com/x",
			Err: errors.New("dial tcp 127.0.0.1:7897: connect: connection refused")}, "无法连接"},
	}
	for _, tc := range cases {
		got := failureMessage(tc.err)
		if !strings.Contains(got, tc.want) || strings.Contains(got, "http") || len([]rune(got)) > 40 {
			t.Fatalf("%s: got %q, want 含 %q 的短句", tc.name, got, tc.want)
		}
	}
}
