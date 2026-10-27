// root.go 统一资源根解析（绿色便携化改造 2026-10-07）。
//
// 此前 cabinet 的全部资源（configs/library/cert/data/logs）按进程 CWD 相对解析，
// 从任务计划/开机自启/其他目录的命令行启动时找不到资源，核心功能静默失效。
// 本文件确立与 CWD 无关的锚定语义，规则如下（单测钉死，见 root_test.go）：
//
//   - 资源根 = exe 所在目录；若 exe 目录下存在 appdata\ 目录，则资源根 = appdata\。
//     便携包布局（exe + appdata\ + 使用说明.txt）→ 数据全落 appdata，顶层只见
//     "一个程序 + 一个数据夹"；开发目录 <仓库根>（configs/library/cert
//     直接在仓库根、无 appdata）→ 回落 exe 目录本身，零迁移成本继续可跑。
//   - 配置里的路径：绝对路径（含盘符）原样使用；相对路径一律按资源根解析（而非 CWD）。
//     旧 cabinet.toml 的相对路径写法在新语义下解析结果与旧行为一致——开发目录 exe
//     就在仓库根，资源根 = 仓库根 = 原 CWD；这就是"升级只换 exe、旧配置零改动"的根基。
package config

import (
	"os"
	"path/filepath"
	"strings"
)

// AppdataDirName 便携包数据目录名：资源根锚定的布局标记（exe 目录下存在该目录
// 即认定便携布局）。与打包脚本 scripts/package-portable.bat 的 staging 结构约定一致。
const AppdataDirName = "appdata"

// ExeDir 返回当前进程可执行文件的真实所在目录（解析 symlink 后取目录）。
// 失败时返回错误，由调用方决定回落策略（DiscoverResourceRoot 回落 CWD）。
func ExeDir() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	// os.Executable 可能给间接路径（symlink/快捷方式启动等），解析到真实文件再取目录
	if resolved, lerr := filepath.EvalSymlinks(exe); lerr == nil {
		exe = resolved
	}
	return filepath.Dir(exe), nil
}

// DiscoverResourceRoot 按便携锚定规则解析资源根（规则见文件头注释）。
// exe 目录不可得时回落 "."（CWD）——等价改造前的行为，极端环境不比从前更糟。
func DiscoverResourceRoot() string {
	exeDir, err := ExeDir()
	if err != nil {
		return "."
	}
	return resolveResourceRoot(exeDir)
}

// resolveResourceRoot DiscoverResourceRoot 之上的纯逻辑：注入 exeDir 便于单测
// appdata 存在/不存在两个分支，不必真去改进程 exe 的位置。
func resolveResourceRoot(exeDir string) string {
	appdata := filepath.Join(exeDir, AppdataDirName)
	if st, err := os.Stat(appdata); err == nil && st.IsDir() {
		return appdata
	}
	return exeDir
}

// ResolvePath 按资源根语义解析单个路径（规则见文件头注释）：
// 绝对路径（含盘符）原样透传；相对路径按资源根拼接；空串返回空串——
// "日志只写控制台"等空值语义由各字段自行处理，此处不代劳。
func ResolvePath(root, p string) string {
	if p == "" {
		return ""
	}
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Join(root, p)
}

// ResolvePaths 将配置内全部路径字段按资源根就地解析为绝对路径。
//
// ⚠ 调用时机铁律：必须在最后一次 cfg.Save() 之后（main.run 的 token 回写之后、
// logging.Setup 之前）。Save 会把内存态整体写回 toml——若先解析后保存，机器相关
// 的绝对路径会被固化进配置文件，破坏"整包拷走即可用"的便携性（搬机/换盘符即失效）。
// 配置文件里永远保持相对路径写法，仅运行期内存态为绝对路径。
// 【运行期也要写配置的场景（算力参数落盘 2026-10-18）】不要直接 Save 内存态——
// 先拷贝快照再 UnresolvePaths 反解回相对形态（见彼处注释），main.go 的
// SetTunePersist 实现是标准范例。
//
// ⚠ 字段完备性：新增 Config 路径字段必须在此追加一行——漏加的字段保持相对
// 路径、随进程 CWD 漂移，等于便携化改造要修的老 bug 单点复发。评审时按
// "Config 有几个路径字段、这里几行"逐一核对；UnresolvePaths 同步加行，
// TestUnresolvePaths_MatchesResolveFields 对账测试会拦单向遗漏。
func (c *Config) ResolvePaths(root string) {
	c.TLS.CertDir = ResolvePath(root, c.TLS.CertDir)
	c.Log.File = ResolvePath(root, c.Log.File)
	c.Engine.Path = ResolvePath(root, c.Engine.Path)
	c.Engine.NNUE = ResolvePath(root, c.Engine.NNUE)
	c.Library.BooksDir = ResolvePath(root, c.Library.BooksDir)
	c.Store.Path = ResolvePath(root, c.Store.Path)
}

// relativize ResolvePath 的逆操作：路径在资源根下则反解为相对形态（写回 toml
// 的便携安全形态），不在（用户自配的绝对路径指向资源根之外，如另一块盘的
// NNUE）或空串则原样保留——不猜测用户意图，保留什么写回什么。
func relativize(root, p string) string {
	if p == "" {
		return ""
	}
	rel, err := filepath.Rel(root, p)
	if err != nil || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return filepath.Clean(p) // 逃出资源根/解析失败：保留原样（含绝对路径语义）
	}
	// 归一化分隔符为正斜杠（toml 里的既有写法，跨盘符拷贝可读）
	return filepath.ToSlash(rel)
}

// UnresolvePaths ResolvePaths 的逆操作：路径字段按资源根反解回相对形态，
// 用于**运行期**写回配置文件（算力参数落盘）。直接 Save 内存态会把绝对路径
// 固化进 toml 破坏便携性—— ResolvePaths 头注释的调用时机铁律即为此设；
// 本方法让"运行期也要写配置"的场景（切换算力记住选择）绕开该雷。
// 字段清单必须与 ResolvePaths 一一对应（新增路径字段两处同步加行）。
func (c *Config) UnresolvePaths(root string) {
	c.TLS.CertDir = relativize(root, c.TLS.CertDir)
	c.Log.File = relativize(root, c.Log.File)
	c.Engine.Path = relativize(root, c.Engine.Path)
	c.Engine.NNUE = relativize(root, c.Engine.NNUE)
	c.Library.BooksDir = relativize(root, c.Library.BooksDir)
	c.Store.Path = relativize(root, c.Store.Path)
}
