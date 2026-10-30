// Package update 自动更新：检查 GitHub Release + 确认式下载安装。
//
// 形态约束：
//   - 手动"检查更新"按钮（控制台高级面板）+ 启动自检默认关；
//   - 异步、API 查询 3 秒超时、失败静默（只记日志，绝不弹窗打扰）；
//   - 用户确认后才下载安装，绝不静默自动；
//   - 版本比较用版本日期，不用 vc 提交数——提交数随历史重建会重置，前后
//     不可比；版本日期两种来源：旧制 stable-YYYYMMDD 直接取 tag 日期，
//     新制 vX.Y.Z（2026-10-30 起正式版定调语义化版本）取 Release 发布
//     时刻换算北京时间——tag 本身不含日期，发布日期总在构建之后，最多
//     相差一天，比较语义与旧制一致；
//   - 免 token 匿名查公开 API（仓库已公开，直查生效）；404（仓库不可见）、
//     403（共享代理出口匿名限流）等失败均静默；
//   - 代理失败直连兜底一次（代理节点抖动常见，直连可达时保成功率）；
//     失败文案归类为短句上屏，完整错误链只进日志；
//   - 手机端不做更新检查（APK 手动交付；api.github.com 过不了 GFW）。
//
// 绿色软件铁律：安装位置 = exe 自身目录；全部中间文件
// （.part / .zip / .new / .old / 可写探针）固定名锚定该目录——启动清理、失败回滚、零残留，
// 见 swap.go；不写注册表（仅读系统代理设置）、不留任何安装记录。
package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
)

// DefaultReleasesURL 仓库 Release 列表页兜底地址（"打开 Release 页"按钮在
// 尚未检查到具体 Release 时使用；与 New 的 ownerRepo 保持一致）。
const DefaultReleasesURL = "https://github.com/Silva6c/cabinet/releases"

// State 更新流程状态机（控制台面板据此渲染）。
type State string

const (
	StateIdle        State = "idle"             // 未检查
	StateChecking    State = "checking"         // 检查中
	StateUnknownVer  State = "unknown_ver"      // 本机版本无法解析（开发构建）
	StateUpToDate    State = "up_to_date"       // 已是最新
	StateUpdateAvail State = "update_available" // 有新版本（可下载安装）
	StateDownloading State = "downloading"      // 下载中
	StateReadyReboot State = "ready_reboot"     // 新 exe 已校验就位（cabinet.exe.new）
	StateFailed      State = "failed"           // 失败（message 为中文原因）
)

// Snapshot 面板渲染用只读快照（Checker 的内存态拷贝；HTTP 一律不在调用方线程）。
type Snapshot struct {
	State      State
	Message    string  // 中文状态/结果文案（始终非空）
	LatestTag  string  // 最新正式版 tag（vX.Y.Z；兼容旧制 stable-YYYYMMDD；检查前为空）
	ReleaseURL string  // Release 页地址（"打开 Release 页"按钮用）
	Progress   float64 // 下载进度 0–1（非下载态为 0）
	CanInstall bool    // 当前是否允许"下载并安装"（有新版本且资产带官方校验值）
}

// ghRelease / ghAsset 为 GitHub releases API 响应的裁剪结构（只取用到的字段，
// 未知字段宽容忽略——协议 §7 同款语义）。
type ghRelease struct {
	TagName     string    `json:"tag_name"`
	HTMLURL     string    `json:"html_url"`
	Draft       bool      `json:"draft"`
	Prerelease  bool      `json:"prerelease"`
	PublishedAt time.Time `json:"published_at"` // vX.Y.Z 标签无日期，版本日期由此换算
	Assets      []ghAsset `json:"assets"`
}

type ghAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Digest             string `json:"digest"` // 形如 "sha256:<hex>"；缺失 = 不可校验
	Size               int64  `json:"size"`
}

// errNoStable 发布列表里没有可识别的正式版 tag（无 draft/prerelease 噪声干扰）。
var errNoStable = errors.New("发布页暂无正式版")

var (
	stableTagRe = regexp.MustCompile(`^stable-(\d{8})$`)              // 旧制日期标签（兼容历史 Release）
	vTagRe      = regexp.MustCompile(`^v\d+\.\d+\.\d+$`)              // 新制语义化版本（v1.0.0 起）
	updateZipRe = regexp.MustCompile(`^Cabinet-Update-(\d{8})\.zip$`) // 升级包资产名（日期=构建日）
	buildVerRe  = regexp.MustCompile(`^vc\d+-(\d{8})$`)
)

// beijing 版本日期换算时区：构建日期（CI 的 TZ=Asia/Shanghai、本机 CST）与
// Release 的 published_at（UTC）必须同口径换算，否则跨零点会差一天。
var beijing = time.FixedZone("CST", 8*60*60)

// releaseDate 返回 Release 的版本日期（YYYYMMDD），用于与本机构建日期比较：
//   - 旧制 stable-YYYYMMDD：tag 自带日期，直接取用；
//   - 新制 vX.Y.Z：tag 不含日期，取发布时刻换算北京时间（发布必在构建之后，
//     最多相差一天，比较语义与旧制一致）。
//
// 认不出的 tag（含 assets 版权物 Release）返回空串，调用方跳过。
func releaseDate(r *ghRelease) string {
	if m := stableTagRe.FindStringSubmatch(r.TagName); m != nil {
		return m[1]
	}
	if vTagRe.MatchString(r.TagName) && !r.PublishedAt.IsZero() {
		return r.PublishedAt.In(beijing).Format("20060102")
	}
	return ""
}

// Checker 更新检查与安装编排：全部可变状态持锁，对外只暴露 Snapshot 只读拷贝
// （控制台 1.5s 刷新循环轮询；HTTP 全部发生在 goroutine 内，见 internal/console
// 的 FirewallStatus 同款禁令）。
type Checker struct {
	mu          sync.Mutex
	state       State
	message     string
	latestTag   string
	releaseURL  string
	assetURL    string
	assetDigest string
	assetSize   int64
	progress    float64

	ownerRepo    string
	apiBase      string // 默认 https://api.github.com；测试注入 httptest 地址
	version      string
	logger       *slog.Logger
	proxy        func(*http.Request) (*url.URL, error) // 代理选择（env→系统代理）；判别"曾走代理"
	client       *http.Client                          // API 查询（3 秒超时，拍板定值）
	directClient *http.Client                          // 直连兜底（代理失败后重试一次）
	dlClient     *http.Client                          // 下载（无总超时——大文件慢速经代理属正常；读空闲 30s 由 download.go 兜底）
	cancel       context.CancelFunc                    // 下载取消（Cancel 置发）
	idleExpired  bool                                  // 下载读空闲超时标志（定时器置位，错误分类时消费）
}

// New 创建检查器。ownerRepo 形如 "Silva6c/cabinet"；version 为 ldflags 注入的
// 本机版本（internal/server.Version，vc<N>-<日期> 格式）。
func New(ownerRepo, version string, logger *slog.Logger) *Checker {
	if logger == nil {
		logger = slog.Default()
	}
	proxy := proxyFunc()
	return &Checker{
		ownerRepo:    ownerRepo,
		apiBase:      "https://api.github.com",
		version:      version,
		state:        StateIdle,
		message:      "尚未检查",
		logger:       logger,
		proxy:        proxy,
		client:       newClient(3*time.Second, proxy),
		directClient: newClient(3*time.Second, nil),
		dlClient:     newClient(0, proxy),
	}
}

func newClient(timeout time.Duration, proxy func(*http.Request) (*url.URL, error)) *http.Client {
	return &http.Client{
		Timeout:   timeout,
		Transport: &http.Transport{Proxy: proxy},
	}
}

// Snapshot 返回当前状态快照（任意线程安全；只读内存，刷新循环可承受）。
func (c *Checker) Snapshot() Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	return Snapshot{
		State:      c.state,
		Message:    c.message,
		LatestTag:  c.latestTag,
		ReleaseURL: c.releaseURL,
		Progress:   c.progress,
		CanInstall: c.canInstallLocked(),
	}
}

// canInstallLocked 调用方须持锁。
func (c *Checker) canInstallLocked() bool {
	return c.state == StateUpdateAvail && c.assetURL != "" && c.assetDigest != ""
}

// CanCheck 返回能否发起检查（空串 = 可以；非空 = 中文原因，面板据此提示）。
func (c *Checker) CanCheck() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch c.state {
	case StateChecking:
		return "正在检查中"
	case StateDownloading:
		return "正在下载中（可先取消）"
	case StateReadyReboot:
		return "更新已就绪，即将重启"
	}
	return ""
}

// CanInstall 返回能否发起下载安装（语义同 Snapshot.CanInstall，供回调前置判断）。
func (c *Checker) CanInstall() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.canInstallLocked()
}

// Check 执行一次检查（同步；调用方包 goroutine）。结果写内存态，失败静默——
// 只记日志，不弹任何提示（拍板：失败静默）。
func (c *Checker) Check(ctx context.Context) {
	c.mu.Lock()
	if c.state == StateChecking || c.state == StateDownloading || c.state == StateReadyReboot {
		c.mu.Unlock() // 防重入：流程进行中忽略
		return
	}
	c.state = StateChecking
	c.message = "正在检查更新……"
	c.progress = 0
	c.mu.Unlock()

	rel, err := c.fetchReleases(ctx)
	c.mu.Lock()
	defer c.mu.Unlock()
	switch {
	case errors.Is(err, errNoStable):
		c.state = StateUpToDate
		c.message = errNoStable.Error()
		return
	case err != nil:
		c.state = StateFailed
		c.message = "检查失败：" + err.Error()
		c.logger.Info("检查更新失败（静默处理）", "err", err)
		return
	}
	c.latestTag = rel.TagName
	c.releaseURL = rel.HTMLURL
	local := parseBuildDate(c.version)
	if local == "" {
		c.state = StateUnknownVer
		c.message = "开发构建（无法确定构建日期），请到 Release 页对照"
		return
	}
	tagDate := releaseDate(rel)
	if tagDate == "" {
		// pickLatestStable 已按 releaseDate 过滤，走到这里属响应数据异常
		c.state = StateFailed
		c.message = "无法识别最新版本的发布日期"
		c.logger.Info("无法识别版本日期", "tag", rel.TagName)
		return
	}
	if local >= tagDate {
		c.state = StateUpToDate
		c.message = "已是最新（" + rel.TagName + "）"
		return
	}
	c.state = StateUpdateAvail
	c.message = "发现新版本 " + rel.TagName
	c.assetURL, c.assetDigest, c.assetSize = "", "", 0
	if name, url, digest, size := pickUpdateAsset(rel); name != "" {
		c.assetURL, c.assetDigest, c.assetSize = url, digest, size
	} else {
		c.message += "（无带校验值的升级包，请到 Release 页手动下载）"
	}
}

// fetchReleases 拉取 releases 列表并选出最新正式版。不用 releases/latest
// 端点：assets 版权物 Release 也是正式 Release，会污染 latest 语义——列表
// 过滤 + 日期取最大，双保险。
func (c *Checker) fetchReleases(ctx context.Context) (*ghRelease, error) {
	full := c.apiBase + "/repos/" + c.ownerRepo + "/releases?per_page=10"
	resp, err := c.apiGet(ctx, full, c.client)
	if err != nil && c.viaProxy(full) {
		// 代理路径失败：直连兜底一次——代理客户端节点抖动时（系统代理开着、
		// TUN 直连反而可达的组合很常见）多试一次；直连本就不通的场景多耗
		// 一次 3 秒超时，属可接受代价。
		c.logger.Info("代理请求失败，直连重试", "err", err)
		resp, err = c.apiGet(ctx, full, c.directClient)
	}
	if err != nil {
		c.logger.Info("网络请求失败", "err", err) // 全量错误链只进日志
		return nil, errors.New(failureMessage(err))
	}
	defer func() { _ = resp.Body.Close() }()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		// 匿名 404 = 仓库对匿名请求不可见（未公开/已改名）；共享代理出口 IP 的
		// 匿名限流 403 亦属已知真实场景（落下一分支）——两类失败均静默记日志
		return nil, fmt.Errorf("发布列表不可见（仓库未公开或暂无 Release）")
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("GitHub API 返回 %d", resp.StatusCode)
	}
	var rels []ghRelease
	if err := json.NewDecoder(resp.Body).Decode(&rels); err != nil {
		return nil, fmt.Errorf("解析响应失败: %w", err)
	}
	rel := pickLatestStable(rels)
	if rel == nil {
		return nil, errNoStable
	}
	return rel, nil
}

// viaProxy 判别该地址当前是否会经代理发出（直连兜底的触发条件：仅当本就
// 配置了代理时才值得多试一次直连）。
func (c *Checker) viaProxy(full string) bool {
	if c.proxy == nil {
		return false
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, full, nil)
	if err != nil {
		return false
	}
	u, _ := c.proxy(req)
	return u != nil
}

// apiGet 构造 GET 请求并经指定 client 发出（Accept/UA 统一在此设置，
// GitHub API 强制 UA）。
func (c *Checker) apiGet(ctx context.Context, full string, client *http.Client) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, full, nil)
	if err != nil {
		return nil, fmt.Errorf("构造请求失败: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "cabinet/"+c.version)
	return client.Do(req)
}

// failureMessage 把网络层错误归类为简短中文文案：上屏文案必须短——结果行
// 宽度有限，长错误串（含 URL 与 Go 错误链）会撑破面板（2026-10-28 用户
// 真机实录）；完整错误链已在调用方记日志。
func failureMessage(err error) string {
	s := strings.ToLower(err.Error())
	switch {
	case os.IsTimeout(err), strings.Contains(s, "client.timeout"), strings.Contains(s, "context deadline exceeded"):
		return "网络超时，可稍后重试"
	case strings.Contains(s, "eof"), strings.Contains(s, "connection reset"):
		return "连接被中断（代理或网络抖动），可重试"
	case strings.Contains(s, "connection refused"):
		return "无法连接代理或网络"
	case strings.Contains(s, "no such host"), strings.Contains(s, "proxyconnect"):
		return "代理或域名不可达，请检查网络"
	default:
		return "网络不可达，请检查代理设置"
	}
}

// pickLatestStable 从 release 列表过滤出最新正式版：tag 必须是 vX.Y.Z（新制）
// 或 stable-YYYYMMDD（旧制兼容），排除 draft 与 prerelease（assets 版权物
// Release 标了 prerelease，此过滤是第二道保险）。排序键 = releaseDate
// （8 位日期字典序 = 时间序）；同日并列取发布时刻更晚者，避免同日连发两版
// 时顺序不定。
func pickLatestStable(rels []ghRelease) *ghRelease {
	var best *ghRelease
	var bestDate string
	var bestAt time.Time
	for i := range rels {
		r := &rels[i]
		if r.Draft || r.Prerelease {
			continue
		}
		d := releaseDate(r)
		if d == "" {
			continue
		}
		if best == nil || d > bestDate || (d == bestDate && r.PublishedAt.After(bestAt)) {
			best, bestDate, bestAt = r, d, r.PublishedAt
		}
	}
	return best
}

// pickUpdateAsset 在 release 里找升级包资产（portable-pack 产出的
// Cabinet-Update-YYYYMMDD.zip，默认仅 cabinet.exe，-with-engine/-with-books
// 可附引擎与桔库）。资产名里的日期是构建日期：旧制与 tag 日期同日，新制
// vX.Y.Z 标签不含日期——故按类型匹配并取日期最大者，不再要求与 tag 同日。
// digest 为空（GitHub 未提供官方校验值）时不予下载安装——绿色软件不装不可验
// 的二进制。未命中返回零值。
func pickUpdateAsset(rel *ghRelease) (name, url, digest string, size int64) {
	bestDate := ""
	for _, a := range rel.Assets {
		m := updateZipRe.FindStringSubmatch(a.Name)
		if m == nil || a.BrowserDownloadURL == "" {
			continue
		}
		if m[1] > bestDate {
			name, url, digest, size = a.Name, a.BrowserDownloadURL, a.Digest, a.Size
			bestDate = m[1]
		}
	}
	return name, url, digest, size
}

// parseBuildDate 从 ldflags 注入的版本串提取构建日期（vc<N>-<YYYYMMDD>）。
// dev-0 / 0.1.0-dev 等无注入形态返回空串（调用方按"开发构建"处理）。
func parseBuildDate(ver string) string {
	m := buildVerRe.FindStringSubmatch(ver)
	if m == nil {
		return ""
	}
	return m[1]
}
