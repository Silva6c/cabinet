# 手机端接入指导意见：WDL 胜率显示与算力新字段

电脑端（cabinet.exe）已完成"下一代"引擎升级，协议 v1 在 §7「新增可选字段旧端忽略」规则内扩展，**旧版手机 APK 无需任何改动即可继续使用**。本文档说明手机端如何接入新能力，供后续实施参考。

## 一、电脑端已就绪的新能力（无需手机端配合）

| 能力 | 状态 | 说明 |
|---|---|---|
| 引擎线程自动档 | ✅ 已生效 | `threads=0`（自动 = 逻辑处理器数的一半，本机 10 线程）；追求极限可在面板/手机端手动调高 |
| 置换表自动档 | ✅ 已生效 | `hash_mb=0`（自动 = 2GiB） |
| 进程高优先级 | ✅ 已生效 | 皮卡鱼进程提升为 High priority class |
| PC 面板调参区 | ✅ 已生效 | 线程数/变招路数/置换表/搜索深度四下拉 + 清空置换表，与手机端 `set_engine` 同一条路、双向同步 |
| suggestion 新增 WDL | ✅ 已下发 | `wdl_w`/`wdl_d`/`wdl_l`（红方视角百分比），**手机端解析后即可显示** |
| set_engine 新增字段 | ✅ 已支持 | `threads`、`multi_pv`（手机端可发可不发） |

## 二、suggestion 帧新字段（协议 §4.5）

PC 每条引擎建议现在可能携带：

```json
{"v":1,"t":"suggestion","source":"engine","bestmove":"h2e2",
 "score":{"cp":120},"pv":["h2e2","b9c7"],"depth":20,
 "wdl_w":62,"wdl_d":31,"wdl_l":7}
```

- `wdl_w` / `wdl_d` / `wdl_l`：**红方视角**胜/和/负概率，百分比（0–100，三者合计约 100，PC 端已从皮卡鱼走子方视角千分比换算并四舍五入）
- 可选字段：库命中建议、引擎未输出 WDL 时**缺省**（`wdl_w` 为 null 或整个字段不出现——Kotlin 里 `optInt` 兜底即可）
- 杀棋局面（score.mate 存在）时 WDL 可能极端（如 1000/0/0），显示时无需特判

## 三、手机端改动清单（3 个文件，约 30 行）

### 1. `RemoteAnalysisClient.kt` — 解析 WDL 字段

`RemoteSuggestion` data class（约 33 行附近）加三个可空字段：

```kotlin
/** 红方视角胜/和/负概率（百分比 0-100；引擎未输出时为 null） */
val wdlW: Int? = null,
val wdlD: Int? = null,
val wdlL: Int? = null,
```

suggestion 的 JSON 解析处（`onMessage` 内 suggestion 分支，约 233–249 行）补：

```kotlin
// WDL 胜/和/负（红方视角百分比；PC 端可选下发，缺省 null）
wdlW = obj.optInt("wdl_w").takeIf { obj.has("wdl_w") },
wdlD = obj.optInt("wdl_d").takeIf { obj.has("wdl_d") },
wdlL = obj.optInt("wdl_l").takeIf { obj.has("wdl_l") },
```

（若解析用 kotlinx.serialization，改为 `val wdlW: Int? = null` + `@SerialName("wdl_w")`，旧帧自动 null。）

### 2. `ScreenAssistService.kt` — 透传到详情窗

`lastMetrics`（EngMetrics，约 1227 行）与详情窗数据组装处，把最近一条 `lastSuggestion?.wdlW/D/L` 一并带给 `OverlayDetailView`。建议做法：详情窗刷新时直接读 `lastSuggestion`，不必改 EngMetrics。

### 3. `OverlayDetailView.kt` — 显示胜率行

指标区（`detail_metrics`，现有"深度/节点/速率/哈希"行之后）加一行：

```kotlin
// 胜率行：红方视角 胜/和/负（引擎未输出 WDL 时隐藏此行）
val wdl: String? = wdlW?.takeIf { wdlD != null && wdlL != null }
    ?.let { "胜 $it% · 和 $wdlD% · 负 $wdlL%" }
```

样式沿用 `detail_metrics` 现有色（`OverlayTheme.statusTextColor()`，琥珀/主题色高亮行）。

### 4.（可选，不建议本期做）悬浮窗算力按钮扩展

`suggestion` 之外，`set_engine` 帧已支持 `threads`（0=自动/1–64）与 `multi_pv`（1–5）。悬浮窗面板刚定稿单列 6 按钮，**本期不要加按钮**；PC 面板已可调这两项，手机端无需重复。

## 四、验证方法

1. 电脑端新版 cabinet.exe 运行中（托盘状态行"引擎:就绪"），模拟器/真机连线
2. 装新 APK 后点开悬浮窗"详情"：引擎建议出现后详情窗应显示"胜 x% · 和 y% · 负 z%"行
3. 旧 APK 回归：不装新 APK 直接连线，一切照旧（PC 不因手机旧版而少发字段）
4. 边界：将杀局面（score.mate）时胜率应显示极端值；库命中建议无 WDL，胜率行应隐藏

## 五、遗留备忘

- 皮卡鱼 2026-09 版**没有** Skill/UCI_Elo 棋力档选项，"AI 棋力模拟"只能靠深度档位近似
- WDL 是模型自对弈估计值（基于 Fishtest LTC 60+0.6s），中残局参考意义大、纯残局偏乐观
- 大页内存（官方再 +5~10% nps）需 Windows「锁定内存页」特权+重启，列为后续可选项，未启用
