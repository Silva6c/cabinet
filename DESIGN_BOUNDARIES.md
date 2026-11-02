# 设计边界（Design Boundaries）

> 本文件声明内阁的**客观能力边界**：它是什么、不是什么、如何自行验证。
> 边界由代码的实际能力决定，不由本文件的声明决定——每条结论都给出可
> 独立核对的证据入口（源码路径 / 协议文档 / 构建配置），欢迎按第 8 节
> 自行审计。

## 1. 一句话定位

内阁 = 手机做"眼睛"（录屏识别棋盘局面），电脑做"脑子"（本地引擎分析）。
**软件止于"建议"**：分析结果只以悬浮窗呈现给人眼，走子永远由使用者手动
完成——从设计上不存在"识别 → 操作"的自动化回路。

## 2. 只读输入

- 屏幕信息**仅**来自使用者显式授权的 MediaProjection 录屏：系统弹窗
  授权、运行中通知栏常驻可见、随时可停。
- 不读取其他应用的进程内存，不注入、不 hook、不抓包改包。
- 不注册无障碍服务（AccessibilityService），不模拟触摸/手势，不要求
  Root/Xposed。
- 一切分析仅基于屏幕上**使用者本人可见**的画面：不获取屏幕之外的任何
  信息——不读内存、不解析对局通信、不触碰对局刻意隐藏的数据。

## 3. 输出面向人眼

- 唯一输出形态是悬浮窗：迷你棋盘回显 + 建议箭头 + 评分文本。
- 识别结果回显到迷你棋盘，使用者可随时核对软件的"理解"是否正确。
- **不提供任何机器可读的识别结果接口**：无广播、无 ContentProvider、
  无本地监听端口、无文件协议导出——外部自动化脚本无法把本应用当作
  "感知模块"消费。手机端唯一 `exported` 的组件是主入口 Activity
  （应用启动 + `cabinet://connect` 扫码配对深链，仅接收连接配置），见
  [AndroidManifest.xml](phone/app/src/main/AndroidManifest.xml)。

## 4. 权限清单（全部，共 7 项）

| 权限 | 实际用途 |
|---|---|
| `INTERNET` | 与使用者自己扫码配对的电脑端建立 WSS 连接 |
| `ACCESS_NETWORK_STATE` | 悬浮窗状态区显示当前网络类型（WiFi/蜂窝） |
| `FOREGROUND_SERVICE`、`FOREGROUND_SERVICE_MEDIA_PROJECTION` | 录屏识别运行期的前台服务与常驻通知（可见、可停） |
| `POST_NOTIFICATIONS` | 展示上述前台通知 |
| `SYSTEM_ALERT_WINDOW` | 悬浮窗建议展示 |
| `CAMERA` | 仅用于扫描电脑端配对二维码 |

不申请 `QUERY_ALL_PACKAGES`、定位、录音、通讯录等任何与功能无关的
权限；`allowBackup="false"`。全部声明见
[AndroidManifest.xml](phone/app/src/main/AndroidManifest.xml)。

## 5. 通信边界

- **手机端**只"连出"不"监听"：唯一出站连接是使用者扫码配对的自己的
  电脑（WSS：自签证书 + 令牌 + 证书指纹锁定；联调模式可不锁定，见
  协议 §1）。
- **电脑端**对外请求仅两类：①云库查询（开局库与残局库，两个独立开关
  均默认关；开启即表示同意把对局局面 FEN 发往所配端点，端点默认预设
  chessdb.cn、可自行替换，见
  [cabinet.example.toml](configs/cabinet.example.toml) 内隐私口径注释）；
  ②"检查更新"的 API 查询与升级包下载（GitHub Releases /
  api.github.com；手动按钮 + 可选启动自检，自检默认关）。此外电脑端
  运行 WSS 服务供配对手机连入（全仓唯一监听点），无其他监听。无遥测、
  无统计上报。
- 协议 v1 共 12 种消息；电脑 → 手机下行除握手帧 `hello` 与心跳帧
  `ping` 外，业务帧只有 `welcome` / `suggestion` / `status` / `pong` /
  `error` / `bye`——全部是握手、心跳、展示与状态信息，**不存在任何
  "执行指令"类帧**（无走子、无点击、无 UI 控制）。完整定义见
  [协议 v1](docs/protocol-v1.md) §4，代码权威表在
  [protocol.go](internal/server/protocol.go)。
- 电脑端无任何输入模拟能力（无键鼠注入类依赖，[go.mod](go.mod) 全量
  可查）。

## 6. 永远不做

以下为本项目硬边界，不接受以任何理由加入：

- 自动点击 / 自动走子 / 自动对弈 / 自动确认
- 无障碍模拟、触摸注入、Root/Xposed/注入框架
- 读进程内存、改网络包、绕过反作弊、反检测 / 隐藏进程 / 伪装
- 识别验证码、绕过人机检测
- 提供机器可读的识别输出接口给外部自动化
- 后台静默录屏、与功能无关的敏感权限
- 为"改成自动走子 / 绕过检测"提供指导（贡献与 Issue 治理同理）

加入上述任何能力的修改版、分支或重打包，均**非本项目官方发布物**。
官方 APK 仅经本仓 GitHub Releases 分发，以 [README](README.md)「签名
与安全获取」所列证书指纹为准，可自行核验。

## 7. 使用边界（授权前提）

- 预设场景：**单机复盘、离线分析、个人学习**，或使用者拥有合法使用权
  / 已获运营方授权的环境。
- 不鼓励、不支持将本项目用于未授权的在线竞技对局；使用者需自行确认
  目标平台规则，并对自己的账号行为负责。
- 本文件描述设计事实，不构成法律意见。

## 8. 如何自行验证

1. **从源码构建**：见 [README](README.md)「从源码构建」。
2. **审权限**：[AndroidManifest.xml](phone/app/src/main/AndroidManifest.xml)
   全部 `uses-permission` 共 7 项；在 `phone/app/src/main/java` 源码检索
   `AccessibilityService` / `dispatchGesture` / `injectInputEvent`
   应零功能命中（命中 `ClickableViewAccessibility` 之类的 lint 注解、
   或检索 `Root/Xposed` 命中应用内合规声明文案，均与本边界无关）。
3. **审协议**：[protocol-v1.md](docs/protocol-v1.md) §4 消息表与
   [protocol.go](internal/server/protocol.go) 常量表逐帧对照，核对下行
   消息不含执行语义。
4. **审输出接口**：在 `phone/app/src/main/java` 源码检索
   `sendBroadcast` / `ContentProvider` / `ServerSocket` 应零命中；
   悬浮窗实现位于
   `phone/app/src/main/java/com/cabinet/assist/ScreenAssistService.kt`
   （建议展示，无对外动作）。
5. **验签名**：正式 APK 证书指纹与核验命令见
   [README](README.md)「签名与安全获取」。
6. **查构建**：GitHub Actions 工作流（go.yml / codeql.yml /
   release-build.yml）公开可查。
7. **附注**：`internal/server/telemetry.go` 虽名含 telemetry，实为
   本地建议沉淀落库（无任何网络外发），与"无遥测"声称不冲突。

## 9. 版本边界留痕

每个 Release 的发布说明写清能力变化。权限、协议消息面、对外接口的任何
扩张都只会在源码与协议文档中发生、随提交可审计，不做静默增加。若某版本
出现与本文件冲突的能力，以源码为准并视为缺陷，欢迎提 Issue。风险升高
时优先收窄分发（源码优先、限制二进制、归档旧版本），处置过程如实留痕。
