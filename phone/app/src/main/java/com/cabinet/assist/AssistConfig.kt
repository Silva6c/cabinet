package com.cabinet.assist

import android.content.Context
import android.os.Build
import java.util.UUID

/**
 * 连线功能的持久化配置（SharedPreferences）。
 * 分析在电脑端"内阁"完成：连接地址与令牌在此配置（连线页可编辑）。
 */
class AssistConfig(context: Context) {
    private val sp = context.getSharedPreferences("assist_settings", Context.MODE_PRIVATE)

    /** 电脑端 WSS 地址，如 wss://192.168.1.10:9527/ws */
    var serverUrl: String
        get() = sp.getString(KEY_SERVER_URL, "") ?: ""
        set(value) { sp.edit().putString(KEY_SERVER_URL, value.trim()).apply() }

    /** 配对令牌（PC configs/cabinet.toml 的 token 字段） */
    var serverToken: String
        get() = sp.getString(KEY_SERVER_TOKEN, "") ?: ""
        set(value) { sp.edit().putString(KEY_SERVER_TOKEN, value.trim()).apply() }

    /**
     * 电脑端证书 SHA-256 指纹（64 位 hex；扫码配对自动填入或手动录入）。
     * 非空时 TLS 握手校验服务器证书指纹，不匹配即断开（防中间人）；
     * 空 = 不校验（联调模式）。大小写与冒号分隔均容忍，统一归一化为小写无分隔。
     */
    var certFingerprint: String
        get() = sp.getString(KEY_CERT_FP, "") ?: ""
        set(value) { sp.edit().putString(KEY_CERT_FP, normalizeFingerprint(value)).apply() }

    /**
     * YOLO NNAPI 加速开关（默认关，走 CPU 4 线程）。
     * NNAPI 各机型实现差异大（风险清单 #3）：真机对比确认收益后再考虑默认开启。
     */
    var useNnapi: Boolean
        get() = sp.getBoolean(KEY_USE_NNAPI, false)
        set(value) { sp.edit().putBoolean(KEY_USE_NNAPI, value).apply() }

    /**
     * 棋盘识别引擎："onnx"（角点检测+全盘分类，对未收录皮肤泛化好，默认）
     * 或 "yolo"（检测式，onnx 加载失败时自动回退的目标）。
     */
    var recognizerEngine: String
        get() = sp.getString(KEY_RECOGNIZER_ENGINE, "onnx") ?: "onnx"
        set(value) { sp.edit().putString(KEY_RECOGNIZER_ENGINE, value).apply() }

    /** 最近一次识别时的帧尺寸（悬浮窗遮挡判断用：把归一化棋盘框换算回像素） */
    var frameSize: Pair<Int, Int>
        get() {
            val w = sp.getInt(KEY_FRAME_W, 0)
            val h = sp.getInt(KEY_FRAME_H, 0)
            return w to h
        }
        set(value) {
            sp.edit().putInt(KEY_FRAME_W, value.first).putInt(KEY_FRAME_H, value.second).apply()
        }

    /**
     * 稳定确认帧数（1..8）。默认 2：走子后的 burst 快速确认每省一帧即省一次
     * 推理耗时（真机 1~2s）；误确认风险由 validate（两将照面等结构校验）把关，
     * burst 期动画中间帧天然与其他帧不一致，连续 2 帧同类幻觉的概率远低于单帧。
     */
    var confirmCount: Int
        get() = sp.getInt(KEY_CONFIRM, 2).coerceIn(1, 8)
        set(value) { sp.edit().putInt(KEY_CONFIRM, value.coerceIn(1, 8)).apply() }

    /**
     * v39 走子连贯性开关（默认开）：确认时刻"单步走子匹配修正"——枚举旧局面全部
     * 伪合法走子，恰好差 1 格且唯一走子可解释则修正后确认（漏检主形态被规则修复）。
     * 设计与保守守门见 docs/走子连贯性设计与App域解剖-20261017.md §一。
     */
    var singleMoveRepair: Boolean
        get() = sp.getBoolean(KEY_SINGLE_MOVE_REPAIR, true)
        set(value) { sp.edit().putBoolean(KEY_SINGLE_MOVE_REPAIR, value).apply() }

    /**
     * 最近上行局面的持久化（2026-10-24 B1 轮次恢复）：进程重启后 BoardTracker
     * 新实例走子方恒红先，中局面稳态 PC 端校验拦不住——引擎按错轮次搜索后建议
     * 被端上 myTurn 门禁全部压掉，表现为"重启后永远没建议"。写点在
     * scheduleAnalysis（全部轮次变更路径的汇聚点），恢复点在 BoardTracker 首次
     * 确认分支（盘面全等恢复 / 差 1 格走子推断）。NEW_GAME 时由服务层清空。
     */
    var lastConfirmedFen: String
        get() = sp.getString(KEY_LAST_CONFIRMED_FEN, "") ?: ""
        set(value) { sp.edit().putString(KEY_LAST_CONFIRMED_FEN, value).apply() }

    /** 悬浮窗上次位置（左上角，归一化 0..1），未设置则默认 */
    var overlayPos: Pair<Float, Float>
        get() = sp.getFloat(KEY_OVERLAY_X, -1f) to sp.getFloat(KEY_OVERLAY_Y, -1f)
        set(value) {
            sp.edit().putFloat(KEY_OVERLAY_X, value.first).putFloat(KEY_OVERLAY_Y, value.second).apply()
        }

    /** 悬浮窗主题（overlay_dark 半透明深灰 / overlay_black 纯黑 / overlay_light 浅色） */
    var overlayTheme: String
        get() = sp.getString(KEY_OVERLAY_THEME, "overlay_dark") ?: "overlay_dark"
        set(value) { sp.edit().putString(KEY_OVERLAY_THEME, value).apply() }

    /** 详情窗（窗口2）上次位置（左上角，归一化 0..1），未设置则默认 */
    var overlayDetailPos: Pair<Float, Float>
        get() = sp.getFloat(KEY_OVERLAY_DX, -1f) to sp.getFloat(KEY_OVERLAY_DY, -1f)
        set(value) {
            sp.edit().putFloat(KEY_OVERLAY_DX, value.first).putFloat(KEY_OVERLAY_DY, value.second).apply()
        }

    /**
     * 识别引擎分段计时开关（默认关）。开启后 ONNX 引擎每次识别多打一条 timing 日志
     * （角点/透视/分类各段耗时），供性能对比实验与排查识别耗时用；平时关闭零开销。
     * 不放主页 UI：这是诊断开关，实验机用 adb 改 shared_prefs 即可。
     */
    var benchProfiling: Boolean
        get() = sp.getBoolean(KEY_BENCH_PROFILING, false)
        set(value) { sp.edit().putBoolean(KEY_BENCH_PROFILING, value).apply() }

    /** 引擎深度档位（set_engine 上行值；手机悬浮窗按钮循环用）。
     *  默认 20（2026-09-30 用户要求）：MultiPV 同步降为 1 后单步完整收敛预期约
     *  1.5~7s，首条建议仍百毫秒级（info 流渐进呈现）；曾因 MultiPV3+depth20 单步
     *  4s+ 常被打断而降到 16，现算法层面已改，深度回归 20 */
    var engineDepth: Int
        get() = sp.getInt(KEY_ENG_DEPTH, 20).coerceIn(1, 40)
        set(value) { sp.edit().putInt(KEY_ENG_DEPTH, value.coerceIn(1, 40)).apply() }

    /** 引擎 Hash 档位 MB（set_engine 上行值；0.25g–10g，2026-10-01 用户钉死上限 10GB。
     *  夹取上限必须 ≥ PC 部署值 10240，否则 welcome 同步来的 PC 实际值会被静默截断） */
    var engineHashMB: Int
        get() = sp.getInt(KEY_ENG_HASH, 256).coerceIn(32, 10240)
        set(value) { sp.edit().putInt(KEY_ENG_HASH, value.coerceIn(32, 10240)).apply() }

    /**
     * 引擎线程档（set_engine 上行 threads；0 = 跟随电脑端，不下发）。
     * 皮卡鱼搜索吞吐随线程近线性扩展（实测 13700H：1 线程 81 万 nps → 16 线程 675 万），
     * 电脑端默认只给逻辑核一半（留核给系统与模拟器），追求极限算力时在此调高。
     * 连接成功后若 >0 自动下发一次；悬浮窗与电脑端面板改动互不覆盖（只在改变时发送）。
     */
    var engineThreads: Int
        get() = sp.getInt(KEY_ENG_THREADS, 0).coerceIn(0, 64)
        set(value) { sp.edit().putInt(KEY_ENG_THREADS, value.coerceIn(0, 64)).apply() }

    /**
     * 变招路数档（set_engine 上行 multi_pv，悬浮窗"路数"按钮 1–3 循环；协议合法域
     * 1–5，产品钉死 1–3）。PC 端无逐次回传帧：此持久化值即悬浮窗显示值，点击后
     * PC 引擎热生效并自动重搜（PC 面板下拉 1.5s 轮询回显实际值）。默认 1（与
     * PC 部署 toml multi_pv=1 对齐）。
     */
    var engineMultiPv: Int
        get() = sp.getInt(KEY_ENG_MULTI_PV, 1).coerceIn(1, 3)
        set(value) { sp.edit().putInt(KEY_ENG_MULTI_PV, value.coerceIn(1, 3)).apply() }

    /**
     * 设备稳定标识（协议 §4.1 hello 可选自报字段 devid）：首启随机生成 32 hex
     * 并持久化，用于 PC 端跨重连区分设备。仅展示用途（PC 端不作为鉴权依据）；
     * 与配对信息同生命周期——重装/清数据 = 重新扫码 = 新身份，语义自洽。
     */
    val deviceId: String
        get() {
            sp.getString(KEY_DEVICE_ID, null)?.let { return it }
            val id = UUID.randomUUID().toString().replace("-", "")
            sp.edit().putString(KEY_DEVICE_ID, id).apply()
            return id
        }

    /**
     * 设备昵称（hello 可选自报字段 name，PC 控制台设备列表显示）：默认取
     * 机型（Build.MODEL，开箱即有意义），主页可编辑。PC 端会再做防御性规整
     * （trim/去控制字符/限长 24），此处同步限长减少无谓往返差异。
     */
    var deviceName: String
        get() = sp.getString(KEY_DEVICE_NAME, null)?.trim().takeUnless { it.isNullOrEmpty() }
            ?: Build.MODEL
        set(value) {
            sp.edit().putString(KEY_DEVICE_NAME, value.trim().take(24)).apply()
        }

    companion object {
        private const val KEY_SERVER_URL = "server_url"
        private const val KEY_SERVER_TOKEN = "server_token"
        private const val KEY_CERT_FP = "cert_fingerprint"
        private const val KEY_USE_NNAPI = "use_nnapi"
        private const val KEY_BENCH_PROFILING = "bench_profiling"
        private const val KEY_RECOGNIZER_ENGINE = "recognizer_engine"
        private const val KEY_FRAME_W = "frame_w"
        private const val KEY_FRAME_H = "frame_h"
        private const val KEY_CONFIRM = "confirm_count"
        private const val KEY_OVERLAY_X = "overlay_x"
        private const val KEY_OVERLAY_Y = "overlay_y"
        private const val KEY_OVERLAY_THEME = "overlay_theme"
        private const val KEY_OVERLAY_DX = "overlay_detail_x"
        private const val KEY_OVERLAY_DY = "overlay_detail_y"
        private const val KEY_ENG_DEPTH = "engine_depth"
        private const val KEY_ENG_HASH = "engine_hash_mb"
        private const val KEY_ENG_THREADS = "engine_threads"
        private const val KEY_ENG_MULTI_PV = "engine_multi_pv"
        private const val KEY_SINGLE_MOVE_REPAIR = "single_move_repair"
        private const val KEY_LAST_CONFIRMED_FEN = "last_confirmed_fen"
        private const val KEY_DEVICE_ID = "device_id"
        private const val KEY_DEVICE_NAME = "device_name"

        /**
         * 指纹归一化：去冒号/空格分隔、转小写，容忍 "AA:BB:.." / "aabb.." 两种写法；
         * 非 hex 字符残留则原样返回（校验端不会匹配到任意指纹，等于锁定失败安全侧）。
         */
        fun normalizeFingerprint(raw: String): String =
            raw.replace(":", "").replace(" ", "").trim().lowercase()
    }
}
