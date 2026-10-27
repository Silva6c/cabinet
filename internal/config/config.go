// Package config 负责内阁的 TOML 配置加载与持久化。
// 首次启动时若配置文件不存在，则以默认值生成（含随机令牌），让单二进制开箱即用。
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// Config 内阁服务端配置，对应 TOML 顶层结构。
//
// ⚠【长期约束】新增任何配置键必须带默认值（Go 零值不满足时，在 Default() 与
// Load 的缺省回填里显式赋默认），保证"升级只换 exe"对配置文件永远成立：
// 旧 toml 缺新键 → Default() 预解码兜底；旧 toml 显式写空 → Load 回填兜底。
// 路径类键的默认值一律写相对路径（运行期按资源根解析，见 root.go），
// 严禁把机器相关的绝对路径写进默认值或配置文件。
type Config struct {
	// Bind WSS 监听地址，如 ":9527" 监听所有网卡
	Bind string `toml:"bind"`
	// Token 配对令牌（64 hex 字符），手机端 auth 帧必须携带；为空则服务拒绝启动
	Token string `toml:"token"`
	// TLS 自签证书相关
	TLS TLSConfig `toml:"tls"`
	// Log 日志配置
	Log LogConfig `toml:"log"`
	// Engine 皮卡鱼引擎配置（M1a）
	Engine EngineConfig `toml:"engine"`
	// Library 桔库目录（热加载，M1c）
	Library LibraryConfig `toml:"library"`
	// Store 沉淀库（M1c）
	Store StoreConfig `toml:"store"`
}

// LibraryConfig 桔库目录配置。
type LibraryConfig struct {
	// BooksDir 库目录（*.obk 及 *.obk.zip；目录内文件增删自动热加载）
	BooksDir string `toml:"books_dir"`
}

// StoreConfig 沉淀库配置。
type StoreConfig struct {
	// Enable 是否开启建议沉淀
	Enable bool `toml:"enable"`
	// Path SQLite 文件路径
	Path string `toml:"path"`
}

// EngineConfig 引擎桥配置。
//
// ⚠【雷区·红线】算力参数（threads/hash_mb/multi_pv/depth）的默认值与部署值
// 由产品决策定死，严禁在代码里"顺手调优"——皮卡鱼与浏览器/对弈 App 共存一台机器，
// 算力互有挤占，任何自作主张的调整都直接改变用户可见的建议质量。
// 历次决策记录：2026-10-01 hash_mb 部署值 2048→10GB（产品档位封顶 10GB）；
// **2026-10-18 默认 hash_mb=256（最低档）**——用户跑大模型期间内存紧张，
// 且悬浮窗/面板切换算力时会落盘回写（SetTunePersist 链路），默认值只影响
// 首次部署与"切换前的起点"，用户改过一次后以落盘值为准。
type EngineConfig struct {
	// Enable 是否启动引擎桥；false 时服务器退化为 M0 echo 行为
	Enable bool `toml:"enable"`
	// Path 皮卡鱼可执行文件路径
	Path string `toml:"path"`
	// NNUE 权重文件路径（EvalFile；建议绝对路径，工作目录无关）
	NNUE string `toml:"nnue"`
	// Threads 引擎线程数；0 = 自动（逻辑处理器数的一半，留一半核给系统与共存程序）
	Threads int `toml:"threads"`
	// HashMB 引擎哈希表大小（MB）；0 = 自动（2GiB，仍为合法档位——控制台
	// "自动"选项落盘 0 后 Load 不回填、重启原样生效）。默认 256 见 DefaultEngine；
	// 悬浮窗/面板切换后由 SetTunePersist 链路落盘回写，重启保持用户的选择
	HashMB int `toml:"hash_mb"`
	// MultiPV 多路 PV 线数（>1 时算力分摊给候选着法，1 = 最强但无变招候选）
	MultiPV int `toml:"multi_pv"`
	// Depth 固定深度制搜索目标深度
	Depth int `toml:"depth"`
}

// DefaultEngine 默认引擎配置（路径相对项目根运行目录）。
// Threads 留 0 走自动档（逻辑核一半）；HashMB 2026-10-18 按用户要求钉 256MB
// （最低档，跑大模型期间内存紧张；悬浮窗/面板切换后落盘记住用户的选择）。
func DefaultEngine() EngineConfig {
	return EngineConfig{
		Enable:  true,
		Path:    "library/engine/pikafish.exe",
		NNUE:    "library/nnue/pikafish.nnue",
		Threads: 0,   // 自动 = 逻辑核一半
		HashMB:  256, // 默认最低档，切换后落盘生效
		MultiPV: 1,   // 2026-10-18 默认单线（MultiPV>1 分散搜索同深度棋力明显下降；变招功能由用户手动开启）
		Depth:   20,
	}
}

// TLSConfig 证书目录配置。
type TLSConfig struct {
	// CertDir 自签证书存放目录，证书一经生成绝不自动重建（见 internal/server/cert.go 铁律）
	CertDir string `toml:"cert_dir"`
}

// LogConfig 日志配置。
type LogConfig struct {
	// Level 日志级别：debug / info / warn / error
	Level string `toml:"level"`
	// File 日志文件路径；为空则只写控制台
	File string `toml:"file"`
	// MaxSizeMB 单个日志文件上限（MB），超过滚动
	MaxSizeMB int `toml:"max_size_mb"`
	// MaxBackups 保留的历史日志文件个数
	MaxBackups int `toml:"max_backups"`
}

// DefaultBind 默认 WSS 监听地址（单一定义点；allow-9527.bat 的防火墙规则
// 与本值联动，改端口须同步）。
const DefaultBind = ":9527"

// Default 返回默认配置。Token 留空由调用方生成随机值回填。
func Default() *Config {
	return &Config{
		Bind: DefaultBind,
		TLS:  TLSConfig{CertDir: "cert"},
		Log: LogConfig{
			Level:      "info",
			File:       "logs/cabinet.log",
			MaxSizeMB:  10,
			MaxBackups: 5,
		},
		Engine: DefaultEngine(),
		Library: LibraryConfig{
			BooksDir: "library/books",
		},
		Store: StoreConfig{
			Enable: true,
			Path:   "data/suggestions.db",
		},
	}
}

// Load 从 path 读取配置；文件不存在时生成默认配置（Token 由调用方回填）并写出文件。
// 返回的第二个值 isCreated 表示本次是否新建了配置文件（调用方据此打印提示）。
func Load(path string) (*Config, bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		cfg := Default()
		if err := cfg.Save(path); err != nil {
			return nil, false, fmt.Errorf("写入默认配置失败: %w", err)
		}
		return cfg, true, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("读取配置失败: %w", err)
	}
	cfg := Default()
	if _, err := toml.Decode(string(data), cfg); err != nil {
		return nil, false, fmt.Errorf("解析配置失败: %w", err)
	}
	// 缺省字段回填：显式留空的项恢复为默认值，避免 0 值引发运行期怪相
	if cfg.Bind == "" {
		cfg.Bind = DefaultBind
	}
	if cfg.TLS.CertDir == "" {
		cfg.TLS.CertDir = "cert"
	}
	if cfg.Log.Level == "" {
		cfg.Log.Level = "info"
	}
	if cfg.Log.MaxSizeMB <= 0 {
		cfg.Log.MaxSizeMB = 10
	}
	if cfg.Log.MaxBackups <= 0 {
		cfg.Log.MaxBackups = 5
	}
	// 路径类空值回填（零值不满足时显式赋默认，见 Config 头部长期约束）：
	// 旧 toml 显式写空字符串时恢复默认，避免空路径引发运行期怪相。
	// 注意 log.file 例外——空 = 只写控制台是既有特性，不回填。
	if cfg.Engine.Path == "" {
		cfg.Engine.Path = DefaultEngine().Path
	}
	if cfg.Engine.NNUE == "" {
		cfg.Engine.NNUE = DefaultEngine().NNUE
	}
	if cfg.Library.BooksDir == "" {
		cfg.Library.BooksDir = Default().Library.BooksDir
	}
	if cfg.Store.Path == "" {
		cfg.Store.Path = Default().Store.Path
	}
	return cfg, false, nil
}

// Save 将配置以 TOML 格式写回 path（不存在则创建目录）。
// 原子写（2026-10-18 加固）：先写 <path>.tmp 再 rename 覆盖——直接 Create 截断
// 原文件时若进程中途退出/两路并发写，会留下截断或交错的 toml，下次启动解析
// 失败直接起不来（config.Load 报错退出，不会静默回默认值）。tmp+rename 保证
// path 上的文件要么是完整旧版要么是完整新版。
func (c *Config) Save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("创建配置目录失败: %w", err)
	}
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return fmt.Errorf("创建临时配置文件失败: %w", err)
	}
	enc := toml.NewEncoder(f)
	encErr := enc.Encode(c)
	// Sync + Close 在 rename 前完成：确保落盘字节真正到介质（掉电也不留半截）
	if encErr == nil {
		encErr = f.Sync()
	}
	closeErr := f.Close()
	if encErr != nil {
		_ = os.Remove(tmp) // 清理半成品，不影响原配置
		return fmt.Errorf("序列化配置失败: %w", encErr)
	}
	if closeErr != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("关闭临时配置文件失败: %w", closeErr)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("替换配置文件失败: %w", err)
	}
	return nil
}
