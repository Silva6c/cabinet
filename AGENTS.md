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
5. **识别引擎的训练/评测/权重演进在姊妹仓 `D:\AI-works\xiangqi-recognition`**
   （独立 GPLv3 项目）：冷启动读其 **AGENTS.md**——当前状态在其
   `docs/训练工作台账.md`，方法史在其 `docs/训练方法史-v1到v34.md`；
   本仓 `phone/tools/recog` 已冻结为历史基线，严禁在此起训练。

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
- 识别引擎的训练/评测演进在姊妹仓 **xiangqi-recognition**（独立 GPLv3 开源项目
  `github.com/Silva6c/xiangqi-recognition`），本仓不含训练代码。

### 跨平台雷区（CI 是 ubuntu，本机多为 Windows——2026-10-27 六笔 CI 修复实录沉淀）

- 平台专用代码走 `_windows.go` + `//go:build !windows` 存根对。**存根签名必须与
  Windows 实现逐字对表**：本机 build/vet/test/lint 全部排除存根文件，签名错
  本机全绿、只在 CI 炸。改存根后用临时模块双 GOOS 验证（拷存根 + 复刻真实
  调用形态，`GOOS=linux` 与默认各 `go build` 一次，双双通过才算数）。
- 测试对"启动监听后立即写文件"的链路（fsnotify 等），必须等就绪信号再写：
  Linux inotify **不回放 `w.Add` 之前的事件**，写早了变更永久丢失（Windows 本机
  调度快掩盖竞争）。参照 `Manager.watchReady` 模式（sync.Once + close(channel)）。
- gofmt 歧义区：闭包一行 `return A{...}, B{{...}}`（多个跨行复合字面量）的续行
  缩进，Windows 与 Linux 的 gofmt 判定可能**互为镜像**且无法本机复现——拆成
  独立赋值 + 简单 return，缩进由括号深度唯一决定，两边都无歧义。
- golangci-lint 钉 v2（action 用 **v7**）；action v6 只认 v1 会装错版本直接挂。
- CI 日志自助获取（不必等截图）：`printf "protocol=https\nhost=github.com\n" |
  git credential fill` 取凭据 → curl GitHub API（`/actions/runs` → `/jobs` →
  job 的 `/logs`）。
- 本机 `go vet ./...` 会扫 `_windows.go`（CI ubuntu 不扫）：`LazyProc.Call` 参数里
  的 `uintptr(unsafe.Pointer(...))`（如 `firewall_windows.go` 的 ShellExecuteW）
  是官方豁免的 syscall 惯用法，vet 原生命令不识别 LazyProc 场景会报 misuse——
  已注释 + `//nolint:govet` 双豁免，属已知误报，勿为消警改写 API 调用形态。

## 提交约定

- 提交信息简体中文，一笔一事（功能 / 收官登记分开）。
- 版本号：Go 端由 `build-exe.bat`/Makefile 经 ldflags 注入
  `vc<git提交数>-<日期>`；Android 端 versionCode = git 提交数。

## 版本发布纪律（2026-10-27 用户定）

- **GitHub 只收稳定版本**：日常开发 = 本地提交；push 到 GitHub 只在用户
  人工确认稳定版之后——AI 不得自行判定"稳定"并推送，用户没说推送就不推。
- 稳定版发布动作：确认代理已开 → `git push` → 打 tag `stable-YYYYMMDD`
  推送，tag 自动触发 `.github/workflows/release-build.yml` 构建 APK +
  便携双轨 zip 并挂到该 tag 的 GitHub Release；`workflow_dispatch` 可手动
  补包重跑（只出 artifact 不挂 Release）。
- CI 产出完整包的前提：版权物（模型 onnx/tflite、皮卡鱼引擎包 zip、桔库
  zip）已一次性上传到本仓 Release 的 **assets 标签**（版权红线不进 git，
  构建时 `gh release download` 注入）；缺资产时构建失败提示上传，不产残废包。
- 本地日常构建照旧不受影响：APK `:app:assembleArmv8DotprodRelease`
  （输出名 `cabinet_日期_vc<提交数>_变体.apk`，versionCode 下限 275）；
  exe/便携 zip `scripts/package-portable.bat`（版本注入已与 build-exe.bat
  对齐，CI windows runner 自动回退自带 MinGW）。
