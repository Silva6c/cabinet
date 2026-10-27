# THIRD-PARTY NOTICES — 内阁（Cabinet）第三方组件清单

本软件（cabinet.exe / 内阁手机端 APK / 便携分发包）包含下列第三方组件。
按"复制即交付"的 GPL 义务，本文件随分发包一并提供；各组件的完整许可文本
见包内对应目录或下文链接。

## 1. Pikafish（皮卡鱼引擎）— GPL-3.0

- 用途：UCI 象棋引擎（分析后端），以独立子进程方式被 cabinet.exe 调用
- 版本：Pikafish 2026-09-06 官方构建（Windows x86-64 universal）
- 来源：https://github.com/official-pikafish/Pikafish
- 许可：GNU General Public License v3.0（全文见
  `appdata\library\engine\licenses\Copying.txt`）
- 源码获取：上述官方仓库按包内引擎版本对应 tag/commit 提供完整源码；
  依据 GPL-3.0 §6，你也可以向我们索取对应版本源码
- 分发义务清单（GPL-3.0）：
  1. 随包提供 GPL-3.0 许可全文（✓ licenses\Copying.txt）
  2. 提供对应源码或书面承诺（✓ 官方公开仓库 + 版本对应说明）
  3. 保留版权与许可声明（✓ licenses\AUTHORS）
- NNUE 权重文件（pikafish.nnue）附加条款（见 licenses\NNUE-License.md）：
  仅限合法使用；未经许可不得商用。**使用本软件即视为已同意该条款。**

## 2. 识别模型权重（nano / RTMPose 角点）— MIT

- 用途：手机端识别链现役模型（assets/models/nano_v3-0319.onnx 为其 INT8 量化版、
  4_v6-0301.onnx 逐字节原版）
- 原作者发布：https://huggingface.co/spaces/yolo12138/Chinese_Chess_Recognition
- 许可：MIT License（义务=随分发物保留其版权与许可声明；2026-10-27 考古对账：
  量化版契约/输出与 fp32 原版逐位一致）
- 历史经上游生态流通，来源归属以上述原作者仓为准

## 3. VinXiangQi — GPL-3.0

- 用途：YOLO 回退识别引擎——模型 yolov5n_xq_fp16.tflite（APK assets 现役）与
  解码策略（YoloPostprocessor.kt / DetectionBoardMapper.kt 移植自其
  GetBoardFromPrediction，头注释保留出处）
- 来源：https://github.com/Vincentzyx/VinXiangQi
- 许可：GNU GPL-3.0；其作者 Vincentzyx 亦为 Pikafish 创始作者之一
- 义务提示：APK 含上述模型与移植代码，**对外分发 APK 时需按 GPL-3.0 提供对应
  源码**（自装自用不分发不触发）

## 3b. DroidFish（国际象棋 Android 应用）— GPL-3.0

- 用途：手机端棋规引擎基础——Piece/Position/Board/Move/Zobrist 数据结构与
  走子/照将判定骨架改自其国际象棋实现并适配中国象棋规则（保留原文件头版权
  声明）；tools/vector-gen 的向量一致性测试工具为其同源拷贝，参与构建与测试
- 原作：DroidFish by Peter Österlund
- 来源：https://github.com/peterosterlund2/droidfish
- 许可：GNU GPL-3.0；本仓以 GPL-3.0 整体授权，修改后的源码随本仓库公开，
  义务满足

## 3c. 象棋迪克 与 zfdang/chinese-chess-android — GPL-3.0

- 用途：手机端棋盘/棋子位图资源与部分棋规代码思路源自上述两个 GPL 项目
- 来源：象棋迪克（com.xqdk.chess，Android 应用，资源以其内嵌版权声明为准）；
  https://github.com/zfdang/chinese-chess-android
- 许可：GNU GPL-3.0；位图资源随 APK 分发时按 GPL-3.0 提供对应源码
  （本仓整体 GPL-3.0 授权，义务满足）

## 4. onnxruntime-android — MIT

- 用途：手机端识别引擎（ONNX 推理运行时）
- 版本：1.31.1；许可：MIT License
- 来源：https://github.com/microsoft/onnxruntime

## 5. Android 端其他依赖（build.gradle.kts）

androidx.core:core-ktx、androidx.appcompat、androidx.activity-ktx（Apache-2.0，
AOSP/Google）；com.squareup.okhttp3:okhttp 4.12.0（Apache-2.0，Square）；
com.journeyapps:zxing-android-embedded 4.3.0（Apache-2.0，journeyapps）；
org.tensorflow:tensorflow-lite 2.14.0（Apache-2.0）——均为各自仓库发布的开源许可，
完整清单见 gradle 依赖树。

## 6. Fyne（Go GUI 工具包）— BSD-3-Clause

- 用途：PC 控制台图形界面（fyne.io/fyne/v2）
- 许可：BSD 3-Clause；来源：https://github.com/fyne-io/fyne

## 7. 其他 Go 依赖

gorilla/websocket、BurntSushi/toml、fsnotify、skip2/go-qrcode、
natefinch/lumberjack、modernc.org/sqlite、fyne.io/systray（托盘图标，
internal/tray 在用）、golang.org/x/sys、golang.org/x/text——均为各自仓库
发布的开源许可（MIT/BSD/Apache-2.0 类），完整清单见 go.mod / go.sum。

## 8. 自研组件（无外部许可义务）

- GridHead 自研识别链：自研训练产物，已从产品 assets 移除（2026-10-27 产品
  回退第三方原版），演进与训练归档见 xiangqi-recognition 仓
- 桔库查询层（internal/book）：对上游格式的读取实现；桔库数据本体（*.obk）
  来源：象棋鱼 [zfdang/chinese-chess-fish-android](https://github.com/zfdang/chinese-chess-fish-android)
  （MIT），由部署者自行提供，不在本包分发范围；testdata/ju-book-*.json 为其
  衍生向量（MIT 义务=保留版权与许可声明）

## 9. 参考但不分发

- 作者先前象棋项目（GPL）：仅作只读参考，不进入本软件编译产物，不在分发与
  开源仓库范围
