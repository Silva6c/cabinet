# AGENTS.md — AI 协作入口

内阁（Cabinet）：手机做"眼睛"（录屏识别+悬浮窗），电脑做"脑子"（皮卡鱼+开局库）。
本文件是任何 AI agent（Claude Code / Codex / ZCode / 子代理等）接手本仓库时的
冷启动入口——读完即可干活，不依赖任何历史会话。

## 冷启动顺序

1. **README.md** —— 项目定位、快速开始、构建入口、许可边界。
2. **docs/technical-overview.md** —— 双端架构、模块详解、安全模型、测试与部署。
3. **docs/protocol-v1.md** —— WSS 协议 v1 定稿：帧结构、字段名、错误码全部钉死，
   **实现照协议抄，改协议先改文档**。
4. 改 UI 前读 **docs/美学设计研究.md**（色板 token 与布局依据）。

## 目录地图

| 路径 | 内容 |
|---|---|
| `cmd/ internal/` | Go 电脑端（WSS 服务、皮卡鱼引擎桥、开局库、控制台、托盘） |
| `phone/` | Android 手机端（识别管线、RemoteAnalysisClient、双悬浮窗） |
| `docs/` | 全部文档（地图见 docs/README.md） |
| `scripts/ testdata/ tools/` | 构建脚本、协议测试向量（Java/Go 双端一致性）、基准工具 |
| `library/ cert/ configs/` | 本机部署位（引擎/开局库/证书/配置，均不入 git，见 README 部署表） |

## 硬纪律（碰了就是事故）

1. **合规红线**：任何代码不得含自动走子、点击注入、无障碍服务、Root 能力——
   本项目只读屏幕、只做提示（MediaProjection + 悬浮窗）。
2. **协议兼容**：`docs/protocol-v1.md` 是唯一权威；新增字段必须走
   "旧端忽略未知字段"的宽容语义（§7），破坏性变更加协议版本号。
3. **`cert/` 证书一经生成绝不重建**：重建 = 全部已配对手机断联，需全部重新扫码。
4. **版权物不进 git**：皮卡鱼引擎/NNUE/开局库/识别模型全部 gitignore，
   由使用者自行获取部署（README 有获取指引）。
5. 对话、注释、文档、错误信息一律**简体中文**；代码标识符保持英文。

## 工程门禁

- Go 端：`make test`（全量测试）+ `golangci-lint run`（**0 issues** 才能提交）；
  配置见 `.golangci.yml`，新启用 linter 必须先清零。
- Android 端：`./gradlew :app:testArmv8DotprodDebugUnitTest`。
- 协议改动：跑 `internal/server` 的 golden 锚测试
  （`protocol_bench_test.go`）确认帧形态不破。
- 识别引擎的训练/评测演进在姊妹仓 **xiangqi-recognition**（私有），本仓不含训练代码。

## 提交约定

- 提交信息简体中文，一笔一事（功能 / 收官登记分开）。
- 版本号：Go 端由 `build-exe.bat`/Makefile 经 ldflags 注入
  `vc<git提交数>-<日期>`；Android 端 versionCode = git 提交数。
