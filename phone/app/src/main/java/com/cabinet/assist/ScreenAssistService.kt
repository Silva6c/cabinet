package com.cabinet.assist

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.app.Service
import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.content.IntentFilter
import android.graphics.PixelFormat
import android.hardware.display.DisplayManager
import android.hardware.display.VirtualDisplay
import android.media.Image
import android.media.ImageReader
import android.media.projection.MediaProjection
import android.media.projection.MediaProjectionManager
import android.net.ConnectivityManager
import android.net.NetworkCapabilities
import android.os.Binder
import android.os.Build
import android.os.Handler
import android.os.HandlerThread
import android.os.IBinder
import android.os.SystemClock
import android.util.DisplayMetrics
import android.view.Gravity
import android.view.WindowManager
import android.widget.Toast
import androidx.core.app.ServiceCompat
import androidx.core.content.ContextCompat
import com.cabinet.R
import com.cabinet.assist.ui.DetailModel
import com.cabinet.assist.ui.OverlayAction
import com.cabinet.assist.ui.OverlayDetailView
import com.cabinet.assist.ui.OverlayModel
import com.cabinet.assist.ui.OverlayPanelView
import com.cabinet.assist.ui.OverlayStateKind
import com.cabinet.assist.ui.OverlayTheme
import com.cabinet.assist.ui.QiTone
import com.cabinet.gamelogic.Piece
import java.io.File
import kotlin.math.max
import kotlin.math.min

/**
 * 连线服务：录制屏幕（MediaProjection）→ YOLO 识别棋盘 → 悬浮窗提示（仅提示，无任何自动点击）。
 *
 * 合规说明（红线，改动前先读）：
 * - 仅使用 MediaProjection（录屏级授权，用户主动确认）与悬浮窗权限；
 * - 不注册无障碍服务、不注入触摸、不读写其他应用数据；
 * - 识别为只读，走子由玩家手动完成——任何"替玩家落子"的功能都越线。
 *
 * ⚠【雷区·线程模型】本服务两条线程纪律，改代码前先认清自己在哪条线程：
 * - workerHandler（HandlerThread "assist-worker"）：全部识别/帧处理/跟踪逻辑，
 *   【唯一 worker】——任何 Throwable 都要就地捕获（见 onImageAvailable），漏出
 *   会杀死线程使识别管线永久停摆（pendingEngine 切引擎也随之失效）；
 * - mainHandler（主线程）：悬浮窗 UI 全部操作 + postRender，别在 worker 直接
 *   碰 View；worker 与主线程之间传递状态一律 @Volatile 或 post，别加锁长临界区。
 */
class ScreenAssistService : Service() {

    companion object {
        private const val TAG = "AssistService"
        const val ACTION_START = "com.cabinet.assist.START"
        const val EXTRA_RESULT_CODE = "result_code"
        const val EXTRA_RESULT_DATA = "result_data"
        private const val CHANNEL_ID = "assist_foreground"
        private const val NOTIF_ID = 1001
        private const val MIN_FRAME_INTERVAL_MS = 220L
        /** 慢路径（棋盘未定位 sessionGrid==null 或跟踪不稳定）抓帧间隔：
         *  全图定位推理很重，按 1.4s 节流防烧机。⚠旧注释"遮挡才放慢/抓帧前隐藏面板"
         *  描述的是已删除的隐身闪烁机制，勿据旧注释恢复隐藏抓帧（FLAG_SECURE 下
         *  无需隐藏）。MIN_FRAME_INTERVAL_MS=220：快速路径只做帧差检测不推理，
         *  是走子检测延迟与采样开销的折中 */
        private const val OVERLAY_FRAME_INTERVAL_MS = 1400L
        /** 建议成熟阈值：距最后一条建议到达的静默时长，超过即视为"定着"（绿箭头） */
        private const val SUGGEST_IDLE_MS = 800L
        /**
         * 建议渲染节流：info 流（depth/bestmove 变化即推）高频到达时合并悬浮窗重绘——
         * 每条全量重绘会刷爆主线程（2026-09-30 用户反馈卡顿），节流窗口内只安排一次补绘
         */
        private const val SUGGEST_RENDER_THROTTLE_MS = 120L
        /**
         * 单帧最少棋子数：低于该值视为"未见棋盘"（回全屏重定位）。
         * 残局可低至 3 子（帅仕 vs 将），不能沿用固定 10 子门槛——那会把合法残局
         * 永远拒之门外，反而让幻觉出 ≥10 检测的坏帧乘虚而入；合法性由 validate() 把关。
         */
        const val MIN_RECOGNIZED_PIECES = 3
        /** 帧差检测采样步长（像素）：棋子/走子痕迹远大于步长，移动必被检出 */
        private const val FRAME_DIFF_STEP = 16
        /** 帧差"有变化"的占比阈值（变化采样点比例，与历史 1/500 行为一致） */
        private const val CHANGE_RATIO_MIN = 0.002f
        /** 切页级大变化占比阈值：全屏重绘（切 App/弹大窗）远超该值，走子动画/局部
         *  刷新远低于该值——只有这类"用户主动到达的新页面"才清识别退避立即全速定位 */
        private const val CHANGE_RATIO_PAGE = 0.25f
        /** 慢路径兜底探测间隔：棋盘外画面静止时也周期性抓帧一次（省电与漏检之间的折中） */
        private const val SLOW_PROBE_INTERVAL_MS = 6000L
        /** setStatus 瞬时提示的覆盖时长：TTL 内所有重渲染（含 elapsedTicker 每秒 tick）
         *  都持续顶住主状态大字，过期回常规文案（旧实现只渲染一次，1s 用时 tick 即冲掉） */
        private const val STATUS_OVERRIDE_TTL_MS = 4000L
        /** 帧循环兜底 tick 周期：画面静止时回调停摆，靠它推进多帧确认/重开检测 */
        private const val FRAME_TICK_MS = 700L
        /** 画面停稳判定：最后一次帧差变化后静默满该时长，才值得花整轮推理——
         *  走子动画的过渡帧（悬空子）推理出来的是错误局面，白烧 ~0.5s 还污染确认 */
        private const val MOTION_SETTLE_MS = 200L
        /** 停稳等待的硬上限：若棋盘区内存在常驻闪烁源导致永远等不到停稳，
         *  超时后照常识别（退化为旧行为），识别永不饿死 */
        private const val MOTION_SETTLE_MAX_MS = 800L
        const val ENGINE_ONNX = "onnx"
        const val ENGINE_YOLO = "yolo"

        /**
         * 档位循环（悬浮窗深度/Hash/变招路数三按钮共用）：当前值在档内→下一档
         * （尾档循环回首档）；不在档内（旧存档迁移）→跳到第一个更大的档，全部
         * 档都更小则回最低档。纯函数抽出便于单测。
         */
        internal fun nextStepValue(steps: IntArray, current: Int): Int {
            val idx = steps.indexOf(current)
            if (idx >= 0) return steps[(idx + 1) % steps.size]
            return steps.firstOrNull { it > current } ?: steps[0]
        }
    }

    /** 供 Activity 绑定调用的接口 */
    inner class CastBinder : Binder() {
        fun service(): ScreenAssistService = this@ScreenAssistService
    }

    fun isCapturing(): Boolean = mediaProjection != null && virtualDisplay != null

    /**
     * 重配对热生效：按最新连接配置（serverUrl/token/指纹）重建 WSS 客户端，
     * 录屏与悬浮窗全保留。任意线程可调（内部 post 到 workerHandler）。
     * 录屏未启动时无需调用——startCapture 会按当时的配置创建客户端；
     * 重连成功后 welcome 补发机制自动恢复最近局面的建议链。
     */
    fun applyConnection() {
        workerHandler.post {
            remote?.stop()
            remote = null
            if (mediaProjection == null) return@post // 录屏未启动：服务未运行，无需重建
            if (config.serverUrl.isBlank() || config.serverToken.isBlank()) {
                setStatus("未配置电脑地址/令牌\n请在连线页填写后重试")
                return@post
            }
            remote = RemoteAnalysisClient(config.serverUrl, config.serverToken, remoteListener,
                certFingerprint = config.certFingerprint)
                .also { it.start() }
            RLog.i(TAG, "连接配置已热更新: ${config.serverUrl}")
        }
    }

    /** 电脑端连接态（连线页卡片 chip 展示用；未连接/未配置时 false） */
    fun isRemoteConnected(): Boolean = remoteConnected

    /** 最近一次状态文本（供连线页显示） */
    fun getStatusText(): String = lastStatusText

    /**
     * 延迟初始化：服务构造时 Context 尚未 attach（mBase==null），
     * 立即访问 getSharedPreferences 会崩溃；首次访问发生在 onCreate 及之后，安全。
     */
    private val config by lazy { AssistConfig(this) }

    private var mediaProjection: MediaProjection? = null
    private var virtualDisplay: VirtualDisplay? = null
    private var imageReader: ImageReader? = null
    private val projectionManager by lazy {
        getSystemService(Context.MEDIA_PROJECTION_SERVICE) as MediaProjectionManager
    }

    private val workerThread = HandlerThread("assist-worker").apply { start() }
    private val workerHandler = Handler(workerThread.looper)
    private val mainHandler = Handler(android.os.Looper.getMainLooper())

    private var lastFrameTs = 0L
    private var pendingFrame = false

    @Volatile private var paused = false
    /** 暂停基准局面（canonical 快照）：暂停期间低频识别，局面与之不同即自动恢复 */
    @Volatile private var pausedBoard: Array<IntArray>? = null
    /** 暂停期间上次恢复检查时间戳（低频节流） */
    @Volatile private var lastPauseCheckTs = 0L
    /** 最近一帧的引用（framePixels 复用缓冲）：静止画面确认饿死时由 tick 重放推进多帧确认 */
    @Volatile private var lastFrame: Frame? = null
    /**
     * 最近一次上行分析的局面 FEN（含走子方字段）：同局面重复确认不再重发。
     * 伪变化帧反复"确认"同一棋盘、或用户连按"更新棋局"时，PC 端会对同一局面
     * 重复查库/重搜并重推建议——面板状态与悬浮窗建议都被反复重置，是 PC 主机端
     * 显示状态抖动的直接来源。真变化（走子/翻转轮次/悔棋）FEN 必不同，不受影响；
     * 重新开始识别/切换引擎时清空，允许重发一次让 PC 补推当前局面建议。
     */
    @Volatile private var lastSentFen: String? = null
    /** 新局标记（复盘闭环 2026-10-17）：NEW_GAME 确认后置位，随首条上行 position
     *  带 new_game 字段发出后清零。
     *  【为何一次性消费】NEW_GAME 只在"确认瞬间"产生一次语义——该局后续的重发/
     *  补发（welcome 重连、not_ready 恢复、forceFullRelocate）都不带标记，否则
     *  PC 端会把同一局拆成多个 game_id、按局复盘全乱。若首条上行因断线静默
     *  失败，标记已随 sendPosition 记录但帧未送达——PC 漏一次局边界，该局记录
     *  落空 game_id（不参与按局折叠，宁缺勿错），不做重试：补发路径无法区分
     *  "首帧"与"重发"，重试必然引入重复标记。
     *  @Volatile：置位在 worker 确认链、消费在 scheduleAnalysis 的同队列 post，
     *  顺序有保证，volatile 只防可见性撕裂 */
    @Volatile private var pendingNewGameFlag = false
    /**
     * 自上次识别消费以来画面是否出现过变化（累积标志，非瞬时值）。
     * 决定 frameTick 的兜底方式：无变化（false）→ 重放 lastFrame 合法（其内容就是
     * 当前画面）；有变化（true）→ 必须主动抓新帧。曾用瞬时帧差值：切换 App 的变化帧
     * 在退避期被闸口拦截，随后悬浮窗自身刷新的帧（窗外静止）把标志刷回 false，
     * 闸口开窗时重放旧帧抢跑——"切到对局界面后识别永不恢复"的死循环。
     * 置位：可见帧差有变化；清位：识别完成（markEnd 消费）或主动抓帧确认无新内容。
     */
    @Volatile private var screenChangedSinceRec = true
    /**
     * 最近一次画面运动（可见帧差变化）时刻：识别等画面停稳（见 MOTION_SETTLE_MS）。
     * 走子动画期间每个合成帧都会刷新它；停稳后由 frameTick 抓稳态帧识别。
     */
    @Volatile private var lastMotionTs = 0L
    /** 停稳等待开始时刻（常驻闪烁源导致等不到停稳时，按 MOTION_SETTLE_MAX_MS 兜底放行） */
    private var motionWaitStart = 0L
    /** 灭屏标志：灭屏后录屏可能只剩黑帧（识别不到会退全屏推理空转发热），取帧直接休眠 */
    @Volatile private var screenOff = false
    /**
     * 帧差签名（稀疏采样值）：悬浮窗已由 FLAG_SECURE 从录屏剔除（见 [boardOccluded]
     * 注释），录屏帧永远不含悬浮窗，全帧单一签名槽即可。悬浮窗自身刷新（状态文字/
     * 指标）也不会出现在录屏里，静止时零误触发。首帧/采样区尺寸变化时重建
     * （全 0 与实际像素比对自动判"有变化"）。
     */
    private var frameSig: IntArray? = null
    /** 上次慢路径兜底探测时间（窗口外静止时按此周期强制检查被挡区域） */
    private var lastSlowProbeTs = 0L
    /** imageToFrame 像素缓冲复用（onImageAvailable 与 handleFrame 同线程串行，无踩踏） */
    private var framePixels: IntArray? = null
    /**
     * 紧急手动模式：识别连续失效/产出幽灵局面时，暂停帧识别，由用户在小棋盘上
     * 点选"起点→终点"录入实战着法（自动翻转走子方）、再点一次已选中子将其删除。
     */
    @Volatile private var manualMode = false
    /** 手动模式下小棋盘当前选中的格（canonical x,y） */
    @Volatile private var manualSelected: Pair<Int, Int>? = null
    /** 用户已点击“关闭”：不再重建悬浮窗，直到重新启动服务 */
    @Volatile private var overlayClosed = false
    /** 用户正在拖动主面板（拖动中该窗的遮挡判定豁免、识别取帧降频；2026-10-01 起按窗拆分） */
    @Volatile private var panelDragging = false
    /** 用户正在拖动详情窗（只豁免详情窗自身的遮挡判定，主面板提示不受影响） */
    @Volatile private var detailDragging = false
    /** 任一窗正在拖动：识别管线取帧抑制共用判据（拖动中帧内几何不稳，保守降频） */
    private val anyDragging get() = panelDragging || detailDragging

    private var tracker = BoardTracker(3)
    private var yoloDetector: YoloBoardDetector? = null
    /** 连续识别失败计数（ONNX/YOLO 两引擎共用，名字是历史遗留）：transient 判定
     *  与"连续失败回全屏重定位"都靠它；成功识别与 switchEngine 时清零 */
    @Volatile private var yoloMissStreak = 0

    /** ONNX 两步式识别引擎（角点+全盘分类）；engineName=onnx 时使用，加载失败自动回退 YOLO */
    @Volatile private var onnxEngine: OnnxBoardRecognizer? = null
    /** 当前识别引擎（主页可切换；worker 帧循环读写） */
    @Volatile private var engineName = ENGINE_YOLO
    /** 主页请求切换的目标引擎（worker 帧循环里应用，避免跨线程重建竞争） */
    @Volatile private var pendingEngine: String? = null
    /**
     * 识别节拍器（按引擎各一）：所有驱动源（真实回调/frameTick 兜底/暂停检查/慢路径
     * 隐藏抓帧）统一过闸。间隔 = max(下限, 上次实际成本×2) × 失败退避，上限 8s——
     * 真机单帧 1~2s，固定间隔等于背靠背满载发热；失败/非法指数退避避免无棋盘画面烧死。
     */
    private val onnxPacer = RecognizePacer(minFloorMs = 1000L)
    private val yoloPacer = RecognizePacer(minFloorMs = 500L)

    /** 当前引擎对应的节拍器（frameTick/慢路径/暂停检查等引擎无关的闸口用） */
    private fun recognizePacer(): RecognizePacer =
        if (engineName == ENGINE_ONNX) onnxPacer else yoloPacer

    /** 识别已成功消费一次输入（结果可用且合法），清除画面变化累积标志。
     *  失败/非法不清——识别吃到的可能是过渡乱帧，"当前画面仍未被可信识别"
     *  这一事实成立，兜底路径必须继续抓帧直到成功（失败退避负责限流） */
    private fun consumeScreenChange() { screenChangedSinceRec = false }
    /** 裁剪推理下连续不稳定的帧数（棋盘挪动后残框自锁时回退全屏重定位） */
    @Volatile private var cropUnstableStreak = 0
    /** 连续"结果非法"帧数（issues 非空/含未知格）：ONNX 角点缓存失效判据，确认等待期不计入 */
    @Volatile private var invalidStreak = 0
    /** 本次会话 YOLO 检出的棋盘网格（用于悬浮窗遮挡判断与裁剪推理），识别失败时清空自愈 */
    @Volatile private var sessionGrid: BoardGrid? = null

    /**
     * 远程分析客户端：分析全部在电脑端"内阁"完成（开局库优先 → 皮卡鱼），
     * 手机端只上行局面、下行建议。本地引擎/开局库已彻底移除（瘦客户端）。
     */
    private var remote: RemoteAnalysisClient? = null
    @Volatile private var remoteConnected = false
    @Volatile private var pcEngineReady = false
    /** PC 引擎原始状态（offline/loading/ready/error，welcome/status 携带）：
     *  "电脑引擎准备中…"此前不分状态——PC 未启用引擎（offline）或引擎反复崩溃
     *  （error）时用户看到的是永远"准备中"，无法区分正常冷启动与真故障 */
    @Volatile private var pcEngineState = "offline"
    /** 最近一次连接/协议错误（显示于状态栏；连接恢复时清除） */
    @Volatile private var remoteErrorText: String? = null
    /** 采集启动时刻：悬浮窗状态行"用时 x.xs"（0 = 未启动/已释放） */
    @Volatile private var captureStartedAtMs = 0L

    /** 用时行秒级随动 tick：渲染是事件驱动的（识别/建议/拖动才重绘），平时用时行
     *  静止——每秒驱动一次全量重渲染（纯 setText 无布局变化，主面板与详情窗同刷）。
     *  capture 停止时移除（releaseCapture），用时清零回 modeText。 */
    private val elapsedTicker = object : Runnable {
        override fun run() {
            if (captureStartedAtMs > 0 && !overlayClosed && overlayPanel != null) {
                postRender()
            }
            // 无条件自续期（体内守卫只做空转短路）：tick 链永不断——capture 停止由
            // releaseCapture 摘除；面板暂缺（overlayClosed/未创建）时保持每秒空转
            // 等待复活。勿把本行挪进守卫内：面板一旦短暂消失 tick 链即断，用时行
            // 永久冻结
            mainHandler.postDelayed(this, 1000L)
        }
    }

    /** 建议成熟度跟踪：距最后一条建议到达静默 SUGGEST_IDLE_MS（或搜索已收束）=> 定着（绿箭头）。
     *  旧设计"best 连续 N ms 未变"在 MultiPV 多线振荡下会被无限重置、永久卡暂定，已废 */
    @Volatile private var lastSuggestionAt = 0L
    /** 上一条建议的深度（服务层同步跟踪，depth 回落 = 新一轮搜索，收束态作废回暂定） */
    @Volatile private var lastSugDepth = 0
    @Volatile private var searchSettled = false

    /** 建议渲染节流状态（WSS 线程写、主线程读写，@Volatile 兜底） */
    @Volatile private var lastSuggestionRenderAt = 0L
    @Volatile private var suggestionRenderPending = false
    /** 引擎指标帧渲染节流（同 onSuggestion 的 120ms 节流+补绘排队；2026-10-02 闪烁治理） */
    @Volatile private var lastMetricsRenderAt = 0L
    @Volatile private var metricsRenderPending = false

    /** 成熟补绘排队标志（防重复排队；WSS 线程写、主线程读写） */
    @Volatile private var matureRecheckPending = false

    @Volatile private var currentFen = ""
    /**
     * 最近一次走子方纠正所针对的 FEN（"更新"按钮与 bad_position 自动纠正共用记录）。
     * 同一 FEN 只自动纠正一次：纠正重发仍被拒说明盘面本身有错（遮挡漏识别等），
     * 再翻回去只会来回抖，停手交回提示/走子自愈。WSS 线程读、主线程写。
     */
    @Volatile private var lastAutoFlipFen: String? = null
    /**
     * 最近一次"自愈重探"所针对的 FEN（2026-10-02）：bad_position 翻转无效/不可用
     * 时回全屏重新定位+识别一次（瞬态误读自愈），同 FEN 只探一次——识别结果若
     * 确定性复现同一非法局面，二次仍拒就落错误提示（防识别↔重探死循环）。
     * 每次新局面确认时重新武装（onBoardConfirmed）。
     */
    @Volatile private var lastReprobeFen: String? = null
    /** canonical 布局 90 子（红恒在 y=9；迷你棋盘视图按 flipped 自行做屏幕朝向映射） */
    @Volatile private var currentPieces: IntArray? = null
    /** 当前确认局面的 canonical 棋盘（手动模式种子用） */
    @Volatile private var currentCanonical: Array<IntArray>? = null
    /** 屏幕朝向（下一帧即权威：worker 逐帧刷新、主线程渲染读，故 @Volatile） */
    @Volatile
    private var currentOrientation = Orientation.STANDARD
    /** 最近一次 PC 建议（局面变化时由 resetSuggestionState 置空）。
     *  A2 线程安全（2026-10-27）：OkHttp 线程写（onSuggestion）、worker 写
     *  （resetSuggestionState）、主线程读（renderOverlay）——@Volatile 防引用
     *  撕裂/可见性滞后（与相邻 lastSuggestionAt/lastSugDepth 等字段对齐） */
    @Volatile private var lastSuggestion: RemoteSuggestion? = null
    /** 变招循环选中候选下标：主线程读写（CYCLE_CANDIDATE/renderOverlay）、
     *  worker 写（resetSuggestionState 经 onBoardConfirmed），@Volatile 兜底 */
    @Volatile private var selectedCandidate = 0
    /**
     * 当前局面的多路候选聚合器（引擎 MultiPV 各线建议聚合，变招按钮据此循环）。
     * 聚合/上限/线程模型见 SuggestionAggregator 注释。
     */
    private val suggestionAggregator = SuggestionAggregator()

    /** 已确认局面历史（悔棋用）：每次确认入栈，回退时弹出当前、显示上一条。
     *  上限 60≈一整局步数。B1/B2 修复（2026-10-27）：无锁 ArrayDeque 的全部读写
     *  （确认链 addLast、悔棋出栈、手动编辑入栈）已收敛到 workerHandler 单线程
     *  串行——勿再从主线程直接操作 history，新入口一律 workerHandler.post */
    private class HistoryEntry(val result: RecognitionResult, val redGo: Boolean, val fen: String)
    private val history = ArrayDeque<HistoryEntry>()

    private var windowManager: WindowManager? = null
    private var overlayPanel: OverlayPanelView? = null
    private var overlayParams: WindowManager.LayoutParams? = null

    /**
     * 悬浮窗遮挡棋盘状态（worker 线程计算，2026-10-01 起按窗拆分）。
     * panelOccluded / detailOccluded 独立计算、独立拖动豁免，聚合读 [boardOccluded]
     * 供坏帧保险丝；提示行按责任窗给文案（"悬浮窗/详情窗挡住棋盘"）——此前合并
     * 判定不分责任窗：cnvcs 全屏棋盘下详情窗默认落位（棋盘上下放不下退 60% 屏高）
     * 正压棋盘，用户移开主面板清不掉黄字，拖任意一窗又全局豁免（"点拖短暂消失、
     * 松手弹回"），根因链详见 d58860a 注释留痕。
     * 驱动主面板一条**独立提示行**（琥珀小字）——不挤占状态行（首版把
     * "运行中 · 用时"整个顶掉被判异常），也不走隐身闪烁（机制已删）。
     * 第二用途：afterRecognition 坏帧保险丝——个别 ROM 若不把 secure 层剔出
     * 录屏，面板像素入镜会拼出垃圾局面，遮挡期间无效帧直接丢弃。
     * 重算时机（[recomputeOcclusion]）：onImageAvailable 入口每真实帧/frameTick
     * 每 700ms（守卫外，paused/manualMode 也持续重算，修掉"tick 停摆值冻结照
     * 显示"）/拖动结束/详情窗开关——松手即得准确状态，不等 ≤700ms 的下一 tick。
     *
     * 前提假设（⚠【雷区】改动抓帧/遮挡逻辑前先读）：两窗均置 FLAG_SECURE，
     * secure 层在录屏/截屏合成时被剔除，MediaProjection 虚拟屏里永远没有悬浮窗，
     * 识别看到的是完整棋盘——所以遮挡**不影响识别**，抓帧无需"隐身"（2026-10-02
     * 起隐身闪烁机制已整体删除，勿再往回加）。副作用（可接受）：用户手动截屏时
     * 悬浮窗同样不入镜。
     * ⚠【雷区】调试时别误判：secure 层也会让模拟器的 `adb shell screencap`
     * 整体失败（RC=1 输出 0 字节，2026-10-02 实测）——这是 secure 语义使然
     * 不是 bug，MediaProjection 识别管线不受影响；别为"修好 screencap"删掉
     * 这两个 FLAG_SECURE（删了悬浮窗会入镜污染识别），调试抓屏用真机截图或临时去掉。
     * 若个别 ROM 不剔除 secure overlay 层（罕见）：遮挡期间识别结果无效帧在
     * afterRecognition 保险丝丢弃（垃圾局面不上行），提示引导用户移开面板。
     */
    @Volatile private var panelOccluded = false
    /** 详情窗是否几何遮挡已定位棋盘（与主面板独立计算、独立豁免拖动） */
    @Volatile private var detailOccluded = false
    /** 聚合遮挡（坏帧保险丝等"有无遮挡"判据用；提示文案按 panel/detail 细分） */
    private val boardOccluded get() = panelOccluded || detailOccluded

    // ---- 详情窗（窗口2）：红黑优大字 + 引擎状态/实时指标（M3） ----

    /** 最近一次带指标的 status 帧（窗口2 显示；收束帧不更新） */
    private class EngMetrics(
        val state: String, val depth: Int, val nodes: Long, val nps: Long, val hashfull: Int)
    @Volatile private var lastMetrics: EngMetrics? = null
    private var detailView: OverlayDetailView? = null
    private var detailParams: WindowManager.LayoutParams? = null
    /** 详情窗显隐（主面板"详情"按钮切换；默认显示） */
    @Volatile private var detailVisible = true

    /**
     * 开局不自动弹详情窗：服务启动后先只有主面板，首次棋盘确认（有可信局面）后
     * 才由 renderOverlay 自动弹出——开局阶段识别尚未稳定，详情窗也没有内容可展示
     * （红黑优/引擎指标全空，弹出来是个空壳）；且详情窗默认悬在棋盘上方，开局就弹
     * 会让确认期与稳态走子都因遮挡而进入"隐藏抓帧"路径（闪烁源）。
     * 用户手动开关（详情按钮）不受此限。
     */
    @Volatile private var detailAutoShown = false

    /** 引擎深度档位（悬浮窗按钮循环；set_engine 下行）12–40 步进 4 */
    private val depthSteps = intArrayOf(12, 16, 20, 24, 28, 32, 36, 40)
    /** 引擎 Hash 档位 MB（0.25g–10g，2026-10-01 用户钉死上限 10GB；皮卡鱼官方
     *  Hash 上限 33554432MB，取产品档位）。10240 必须在表内：PC 曾部署 10GB，
     *  从 10g 循环下去后要能循环回来。
     *  2026-10-18 起默认 256M（最低档，跑大模型期间内存紧张）且**切换落盘**——
     *  PC 端 SetTunePersist 把值写回 cabinet.toml，重启保持用户上次的选择；
     *  手机本地 engineHashMB（AssistConfig）只是显示起点，以 PC 落盘值为准 */
    private val hashSteps = intArrayOf(256, 512, 1024, 2048, 3072, 4096, 6144, 8192, 10240)
    /** 变招路数档位（悬浮窗按钮循环；PC 引擎 MultiPV 热改，产品钉死 1–3。
     *  协议 §4.10 校验 1–5，手机端只露 1–3——放宽产品档位改这里即可，
     *  上限受 PC 端 handleSetEngine 的 5 约束 */
    private val multiPvSteps = intArrayOf(1, 2, 3)

    /**
     * ⚠实验临时开关（2026-10-04 识别引擎对比实验专用，勿提交为 true）：
     * true 时两窗均不置 FLAG_SECURE——secure 层会连 adb screencap 一起拦掉
     * （RC=1 输出 0 字节），实验需要 screencap 存真值底片；代价是悬浮窗会入镜
     * （悬浮窗在屏时识别结果可能被污染，实验采集时把悬浮窗全部收起/移出棋盘）。
     * 实验结束必须改回 false 重编译，并验证 secure 语义（screencap RC=1+0 字节）。
     */
    private val BENCH_DISABLE_SECURE = false

    private val binder = CastBinder()

    override fun onCreate() {
        super.onCreate()
        tracker = BoardTracker(config.confirmCount, config.singleMoveRepair)
        // B1 轮次恢复：注入进程重启前最后上行的局面（2026-10-24）。中局面稳态
        // 错轮次 PC 拦不住（错轮次局面合法），靠端上持久化恢复走子方
        config.lastConfirmedFen.takeIf { it.isNotEmpty() }?.let { tracker.setRestoreHint(it) }
        // YOLO 棋子检测：直接检测 14 类棋子 + 棋盘框，免校准、跨 App 泛化。
        // NNAPI delegate 由配置开关控制（默认关，走 CPU；机型差异风险，真机对比后再定默认值）。
        // 初始化失败（含 JVM 测试环境无 TFLite 原生库的 UnsatisfiedLinkError）只提示，不阻塞服务
        val yoloInitStart = SystemClock.elapsedRealtime()
        yoloDetector = try {
            YoloBoardDetector(this, useNnapi = config.useNnapi)
        } catch (t: Throwable) {
            RLog.w(TAG, "yolo detector init failed", t)
            null
        }
        // ⚠【现状标明 2026-10-01】YOLO 是独立回退引擎：默认 engineName=onnx 不触达
        // 此推理路径，仅主页手动切换或 ONNX 加载失败回退（switchEngine）时启用——
        // 默认链路上 YOLO 被架空（研究报告：docs/yolo-识别管线研究报告.md）。
        // 但本初始化无条件把 tflite 加载进内存：将来摘除 YOLO 需一并改这里。
        // 初始化耗时入日志（2026-10-03 对比实验）：tflite 解释器构建 + 模型 mmap 是
        // 冷启动成本的一部分，与 ONNX 的 "onnx models loaded in Xms" 同口径可比
        val yoloInitMs = SystemClock.elapsedRealtime() - yoloInitStart
        RLog.i(TAG, "yolo detector: ${if (yoloDetector != null) "ready" else "unavailable"} " +
            "(nnapi=${config.useNnapi}) init=${yoloInitMs}ms")
        // 识别引擎按配置初始化（onnx 模型加载重，放后台线程，期间帧走跳过逻辑）
        initEngine(config.recognizerEngine)
        ContextCompat.registerReceiver(
            this, screenStateReceiver,
            IntentFilter(Intent.ACTION_SCREEN_OFF).apply { addAction(Intent.ACTION_SCREEN_ON) },
            ContextCompat.RECEIVER_NOT_EXPORTED
        )
    }

    override fun onBind(intent: Intent?): IBinder = binder

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        if (intent?.action != ACTION_START) {
            // STICKY 重启（进程被杀后系统拉起）：录屏授权不可复用，直接结束，
            // 等用户在连线页重新走 MediaProjection 授权（Android 14 每次会话授权）
            stopSelf()
            return START_NOT_STICKY
        }
        val resultCode = intent.getIntExtra(EXTRA_RESULT_CODE, 0)
        @Suppress("DEPRECATION")
        val data = if (Build.VERSION.SDK_INT >= 33)
            intent.getParcelableExtra(EXTRA_RESULT_DATA, Intent::class.java)
        else intent.getParcelableExtra(EXTRA_RESULT_DATA)
        startForegroundInternal()
        startCapture(resultCode, data)
        return START_NOT_STICKY
    }

    private fun startForegroundInternal() {
        createChannel()
        val notification = buildNotification()
        val type = if (Build.VERSION.SDK_INT >= 29)
            android.content.pm.ServiceInfo.FOREGROUND_SERVICE_TYPE_MEDIA_PROJECTION else 0
        ServiceCompat.startForeground(this, NOTIF_ID, notification, type)
    }

    private fun createChannel() {
        val nm = getSystemService(NotificationManager::class.java)
        val ch = NotificationChannel(CHANNEL_ID, "连线·屏幕识别", NotificationManager.IMPORTANCE_LOW)
        ch.description = "屏幕识别与悬浮窗指导运行中"
        nm.createNotificationChannel(ch)
    }

    private fun buildNotification(contentText: String? = null): Notification {
        val pi = PendingIntent.getActivity(
            this, 0,
            Intent(this, com.cabinet.assist.ui.AssistActivity::class.java),
            PendingIntent.FLAG_IMMUTABLE
        )
        return Notification.Builder(this, CHANNEL_ID)
            .setContentTitle("内阁·连线识别运行中")
            .setContentText(contentText ?: "仅识别局面并给出走法建议，不会自动走子")
            .setSmallIcon(R.drawable.ic_launcher)
            .setOngoing(true)
            .setContentIntent(pi)
            .build()
    }

    /** 投影状态回调（Android 14+ 必须注册）：系统侧停止/授权撤销时释放并收摊。
     *  回调投递在 worker（registerCallback 指定 workerHandler），窗体移除/状态
     *  复位必须 mainHandler.post（removeView 有主线程限制）。overlayClosed 守卫：
     *  用户已点 CLOSE 时收摊已完成，直接返回防重复 setStatus/stopSelf
     *  （releaseCapture 本身判空幂等） */
    private val projectionCallback = object : MediaProjection.Callback() {
        override fun onStop() {
            mainHandler.post {
                if (overlayClosed) return@post
                // Toast 先于收摊动作：overlayClosed 置位后 setStatus 的渲染与通知
                // 都会被守卫拦截（文案送达对象已被移除=用户看到的是凭空消失），
                // Toast 是唯一不受该守卫管辖的送达通道
                Toast.makeText(applicationContext, "录屏已停止，可在内阁连线页重新授权启动", Toast.LENGTH_LONG).show()
                overlayClosed = true
                paused = true
                overlayPanel?.let { p -> runCatching { windowManager?.removeView(p) } }
                overlayPanel = null
                removeDetailWindow()
                releaseCapture()
                setStatus("录屏已停止\n可在连线页重新授权启动")
                stopSelf()
            }
        }
    }

    /** 灭屏监听：识别管线随屏幕休眠/恢复（亮屏后首帧与旧签名比对，画面没变则同样跳过推理） */
    private val screenStateReceiver = object : BroadcastReceiver() {
        override fun onReceive(context: Context?, intent: Intent?) {
            screenOff = intent?.action == Intent.ACTION_SCREEN_OFF
        }
    }

    // ==================== 截屏 ====================

    private fun startCapture(resultCode: Int, data: Intent?) {
        if (mediaProjection != null || data == null) return
        // 重新启动时允许再次显示悬浮窗（详情窗仍等首次棋盘确认后才自动弹出）
        overlayClosed = false
        paused = false
        captureStartedAtMs = System.currentTimeMillis() // 状态行"用时"计时起点
        // 用时随秒跳动（重启动防双 tick：先摘再挂）
        mainHandler.removeCallbacks(elapsedTicker)
        mainHandler.postDelayed(elapsedTicker, 1000L)
        lastMetrics = null
        detailVisible = true
        detailAutoShown = false
        // 允许重发当前局面一次：重新识别后悬浮窗建议状态已重置，
        // 需让 PC 端补推当前局面的建议
        lastSentFen = null
        val metrics = screenMetrics()
        val w = metrics.widthPixels
        val h = metrics.heightPixels
        val dpi = metrics.densityDpi

        mediaProjection = projectionManager.getMediaProjection(resultCode, data)
        // Android 14+ 硬要求：createVirtualDisplay 之前必须注册回调，
        // 以响应投影状态变化（用户从系统面板停止 / 授权被撤销）释放资源
        mediaProjection?.registerCallback(projectionCallback, workerHandler)
        // 记录帧尺寸：悬浮窗遮挡判断把归一化棋盘框换算回像素时需要
        config.frameSize = w to h
        // maxImages=2：acquireLatestImage 语义的最小够用配置——持帧解码期间仍留 1 槽接新帧；
        // 调大只会积压陈旧帧徒增内存
        val reader = ImageReader.newInstance(w, h, PixelFormat.RGBA_8888, 2)
        reader.setOnImageAvailableListener(::onImageAvailable, workerHandler)
        imageReader = reader
        virtualDisplay = mediaProjection?.createVirtualDisplay(
            "assist-capture", w, h, dpi,
            DisplayManager.VIRTUAL_DISPLAY_FLAG_AUTO_MIRROR,
            reader.surface, null, workerHandler
        )
        RLog.i(TAG, "capture started: ${w}x$h")

        // 连接电脑端"内阁"（后台准备，不阻塞识别）；未配置地址时状态栏提示
        if (config.serverUrl.isBlank() || config.serverToken.isBlank()) {
            setStatus("未配置电脑地址/令牌\n请在连线页填写后重试")
        } else {
            remote = RemoteAnalysisClient(config.serverUrl, config.serverToken, remoteListener,
                certFingerprint = config.certFingerprint)
                .also { it.start() }
            // B3 录屏重启补发（2026-10-24）：录屏重启保留 tracker/currentFen 但
            // remote 是全新实例（lastPositionFen=null），且画面未变恒 SAME_BOARD
            // ——frameTick 兜底、帧差门、确认链三道闸集体静默，上方清 lastSentFen
            // 成"死承诺"。这里主动补发一次（去重闸已放行）：sendPosition 先记
            // lastPositionFen 再发送，即使握手未完成也有 welcome 补发兜底（双
            // 保险）。首启场景 currentFen 为空天然跳过；不带 force（等同重连语义，
            // PC 正常查桔库）
            if (currentFen.isNotEmpty()) scheduleAnalysis()
        }
        // 帧循环兜底 tick（见 startFrameTickLoop 注释；releaseCapture 递增代号退役）
        onnxPacer.reset()
        yoloPacer.reset()
        startFrameTickLoop()
        postRender()
    }

    /**
     * 录屏会话代次（A3 竞态修复的锁扣，2026-10-27）：releaseCapture 递增，帧 tick
     * 的续期消息闭包捕获本代代号、run 开头先验代号——本代已退役即不再执行也不续期。
     * @Volatile：主线程写（releaseCapture）、worker 读（tick run）。
     */
    @Volatile private var captureGen = 0L
    /** 当前代的帧 tick 消息（仅主线程读写：startCapture/releaseCapture 都在主线程） */
    private var frameTickRunner: Runnable? = null

    /**
     * 帧循环兜底 tick:识别循环纯回调驱动,画面完全静止时 SurfaceFlinger 不再合成
     * 新帧、onImageAvailable 停发——多帧确认/新局重开检测会饿死在半途(悬浮窗永远
     * "正在识别棋盘…",用户视角即"对局中途卡住不动";cnvcs 新皮肤实测暴露,授权后
     * 画面静止连首次识别都不会发生)。此 tick 周期主动驱动一轮帧处理(无新帧时
     * acquireLatestImage 返回 null,安全空跑);确认饥饿时:有历史帧则重放(静止画面
     * 内容不变,重放识别结果与候选一致,恰好补齐确认所需帧),从未识别过则主动隐藏
     * 抓一帧。连续非法结果(主菜单等无棋盘画面)熔断重放,等真实变化帧,避免空转耗电。
     *
     * A3 竞态修复（2026-10-27）：原单例 Runnable 尾部无条件自续期，与主线程
     * releaseCapture 的 removeCallbacks 存在竞态——run 执行中途 remove 摘不到
     * 已取出的消息，run 尾部再挂一条即 tick 链泄漏，复启后与新一代 tick 双链
     * 并行。修法：每次 startCapture 经本函数新建 Runnable（闭包捕获本代
     * captureGen），run 开头先验代号，本代已退役直接 return（不执行也不续期）
     * ——releaseCapture 期间在途的 run 至多在尾部多挂一条逃逸消息，它下次 run
     * 开头即自检退出，链干净终止；startCapture 复启每代新链，旧代残余消息同样
     * 被代号拦下，无双链。单例对象 + "run 尾部读代号比对"不够：逃逸消息无从
     * 得知自己属于旧代，唯有消息内嵌代号（闭包捕获）可识别。
     */
    private fun startFrameTickLoop() {
        val gen = captureGen
        val tick = object : Runnable {
            var tickHeartbeat = 0L
            override fun run() {
                // 本代已退役（releaseCapture 已递增代号）：立即断链，勿执行勿续期
                if (gen != captureGen) return
                // 遮挡重算放守卫外：纯几何计算不依赖识别管线，paused/manualMode/灭屏下
                // 也持续更新（此前 tick 停摆时 boardOccluded 冻结在旧值、提示行照显示）
                runCatching { recomputeOcclusion() }
                val reader = imageReader
                if (reader != null && !screenOff && !paused && !manualMode && !overlayClosed) {
                    runCatching { onImageAvailable(reader) }
                    val starved = (tracker.confirmed == null || tracker.unstableStreak > 0 ||
                        tracker.confirming) && invalidStreak < 5
                    // confirming=候选累计中同样算饥饿：点选式走子（无动画帧）时画面变化
                    // 标志会被单次识别消费，若不重放补帧且画面静止，确认链将永久饿死
                    // 已确认稳态但有"未被可信识别消费"的画面变化（如切回对局的过渡帧
                    // 恰好识别失败、随后队列空无新帧）：同样需要兜底抓帧，否则直到下
                    // 一个真实新帧前识别永不恢复
                    // 变化帧已检出（flag=true）→ 必须兜底识别：此处不可用 invalidStreak
                    // 熔断——快速路径改为"只检测不识别"后，识别全靠本路径推进，熔断会把
                    // "面板拖动/切对局"后的首次识别永久锁死（模拟器实测：flag=true 且
                    // invalid=5 时永不恢复）；限流交给停稳门槛 + pacer 退避（失败间隔
                    // 指数拉长），比一刀切断更平滑
                    val needsRecapture = screenChangedSinceRec
                    // 心跳诊断(每 100 tick 一条,稳态约 70s):确认链意外静默时,
                    // 一条日志看全状态(starved/标志/退避/停稳年龄)
                    if (++tickHeartbeat % 100 == 0L) {
                        RLog.d(TAG, "tick: starved=$starved need=$needsRecapture flag=$screenChangedSinceRec " +
                            "unstable=${tracker.unstableStreak} invalid=$invalidStreak pending=$pendingFrame " +
                            "pacer=${recognizePacer().canRecognizeNow()} grid=${sessionGrid != null} " +
                            "motionAge=${System.currentTimeMillis() - lastMotionTs}")
                    }
                    // 重放也过识别节拍器：此前 700ms 重放只被固定间隔拦一半（700<800），
                    // 另一半白做一轮完整处理；退避期更不该重放
                    if (!pendingFrame && (starved || needsRecapture) &&
                        recognizePacer().canRecognizeNow()) {
                        val f = lastFrame
                        if (f != null && !screenChangedSinceRec) {
                            // 画面无变化：lastFrame 即当前屏幕内容，重放补齐多帧确认
                            pendingFrame = true
                            workerHandler.post { pendingFrame = false; runCatching { handleFrame(f) } }
                        } else {
                            // 画面有变化：重放旧帧毫无意义（曾因此永远识别启动时的桌面帧、
                            // 失败退避又把真实抓帧锁死在闸外），主动抓当前帧
                            runCatching { forceCaptureOnce(reader) }
                        }
                    }
                }
                // burst 快速确认期用 150ms 细粒度：间隔下限 300ms 时 tick 相位损失减半，
                // 走子→确认链每一环都省一截；稳态仍 700ms（闸口反正会拦,粒度只影响响应延迟）
                // 续期保持无条件（paused/manualMode/灭屏下仅靠体内守卫空转），是否退役
                // 由下次 run 开头的代号检查裁决；主线程 releaseCapture 的 removeCallbacks
                // 仍先摘队内消息，双保险。勿把本行挪进守卫内——守卫暂假时兜底驱动与
                // 遮挡重算全部停摆
                workerHandler.postDelayed(this, if (recognizePacer().isBursting()) 150L else FRAME_TICK_MS)
            }
        }
        frameTickRunner = tick
        workerHandler.post(tick)
    }

    /** 主动抓帧解码送识别（未识别过/重放不可用时的兜底） */
    private fun forceCaptureOnce(reader: ImageReader) {
        // FLAG_SECURE 悬浮窗已从录屏剔除（恒定前提，见 boardOccluded 注释）：
        // 画面里永远没有悬浮窗，直接抓帧无需任何隐藏——走子变化帧由此零闪烁完成识别
        // 闸口未放行时不解码（整屏拷贝不便宜）：标志保持置位，放行后下个 tick 再抓
        if (!recognizePacer().canRecognizeNow(System.currentTimeMillis())) return
        // 停稳门槛：画面刚运动过（走子动画/切页过渡）时先等 MOTION_SETTLE_MS
        // 静默再识别——过渡帧推理出来的是错误局面，白烧一轮还污染确认；
        // 常驻闪烁源导致永远等不到停稳时按 MOTION_SETTLE_MAX_MS 兜底放行
        val nowTs = System.currentTimeMillis()
        if (nowTs - lastMotionTs < MOTION_SETTLE_MS) {
            if (motionWaitStart == 0L) motionWaitStart = nowTs
            if (nowTs - motionWaitStart < MOTION_SETTLE_MAX_MS) return
        } else {
            motionWaitStart = 0L
        }
        RLog.d(TAG, "forceCapture: direct")
        // 录屏帧"取走即焚"：快速路径取到的帧若被闸口拦下，帧内容已随 close 消失，
        // 且画面静止后合成器不再产帧——acquire 永远为 null。此时重放 lastFrame
        // （handleFrame 入口保存的被拦帧）：已确认态下棋盘未动，其内容即当前屏幕，
        // 多帧确认由此补齐，走子后不会永远停在"正在识别棋盘…"。
        val img = runCatching { reader.acquireLatestImage() }.getOrNull()
        val frame = if (img != null) {
            img.use { imageToFrame(it) }
        } else {
            lastFrame ?: run { RLog.d(TAG, "forceCapture: nothing to replay"); return }
        }
        pendingFrame = true
        workerHandler.post {
            pendingFrame = false
            runCatching { handleFrame(frame) }
        }
    }

    @Suppress("DEPRECATION")
    private fun screenMetrics(): DisplayMetrics {
        val dm = DisplayMetrics()
        val wm = getSystemService(Context.WINDOW_SERVICE) as WindowManager
        @Suppress("DEPRECATION")
        wm.defaultDisplay.getRealMetrics(dm)
        return dm
    }

    private fun onImageAvailable(reader: ImageReader) {
        val now = System.currentTimeMillis()
        // 遮挡几何判定：逐窗计算（按窗豁免拖动、沿变化即时重绘提示行），供主面板
        // 独立提示行（文案指明责任窗）+ afterRecognition 坏帧保险丝（聚合读）。
        // FLAG_SECURE 剔除成立时遮挡不影响识别（boardOccluded KDoc 雷区说明）
        recomputeOcclusion()
        // 慢路径（低频抓帧省电）触发条件：
        // - 棋盘尚未定位（sessionGrid==null）：全图定位推理很重；
        // - 识别未确认（unstableStreak>0）：确认机制需要连续多帧推理。
        // （"窗口遮挡棋盘"条件已删——隐身闪烁机制的遗迹：FLAG_SECURE 下面板不入帧，
        // 遮挡与识别零关系；全屏棋盘恒遮挡时它把抓帧永久压在慢路径，白损走子检测延迟）
        val trackingUnstable = tracker.unstableStreak > 0
        val slowPath = !anyDragging &&
            (sessionGrid == null || trackingUnstable)
        val interval = if (slowPath) OVERLAY_FRAME_INTERVAL_MS else MIN_FRAME_INTERVAL_MS
        if (now - lastFrameTs < interval) {
            // 节流窗口内的帧取走后仍做轻量帧差（稀疏采样，毫秒级）：真变化才置位。
            // 录屏帧不含悬浮窗（FLAG_SECURE 剔除），全帧采样即可
            var kept: Frame? = null
            val ratio = runCatching {
                reader.acquireLatestImage()?.use {
                    val r = frameChangeRatio(it)
                    // 变化帧像素立即入 lastFrame 缓冲（keepFrame 由调用方赋引用）：
                    // 录屏帧"取走即焚"，快速路径只检测不识别；若变化帧不在此保存，
                    // 停稳后兜底抓帧 acquire 为 null 只能重放更旧的 lastFrame——
                    // 走子/切页画面永远进不了识别管线（实测：走子→SAME_BOARD→冻结）
                    if (r >= CHANGE_RATIO_MIN) kept = imageToFrame(it)
                    r
                }
            }.getOrNull() ?: 0f
            if (kept != null) lastFrame = kept
            if (ratio >= CHANGE_RATIO_MIN) {
                lastMotionTs = now
                screenChangedSinceRec = true
                recognizePacer().requestBurst()
                // 切页级大变化（用户主动切 App/弹窗）：清退避，新页面立即全速定位
                if (ratio >= CHANGE_RATIO_PAGE) recognizePacer().clearBackoff()
            }
            return
        }
        lastFrameTs = now
        // 灭屏/手动模式：识别管线休眠，只把帧取走丢弃（整屏拷贝与推理全部跳过）。
        // paused 不在此列：暂停（悔棋/手动暂停）后仍要低频识别，检测到棋盘变化自动恢复
        if (screenOff || manualMode) {
            runCatching { reader.acquireLatestImage()?.close() }
            return
        }
        try {
            // 先判 pendingFrame：上一帧还在识别队列时本帧注定被覆盖，直接丢弃。
            if (pendingFrame) {
                // 同节流丢弃：轻量帧差，真变化才置位
                var kept: Frame? = null
                val ratio = runCatching {
                    reader.acquireLatestImage()?.use {
                        val r = frameChangeRatio(it)
                        // 变化帧像素入 lastFrame：本帧被丢弃、像素若不保存，
                        // 兜底重放拿到的永远是更旧的画面（走子后冻结的根源）
                        if (r >= CHANGE_RATIO_MIN) kept = imageToFrame(it)
                        r
                    }
                }.getOrNull() ?: 0f
                if (kept != null) lastFrame = kept
                if (ratio >= CHANGE_RATIO_MIN) {
                    lastMotionTs = now
                    screenChangedSinceRec = true
                    recognizePacer().requestBurst()
                    // 切页级大变化（用户主动切 App/弹窗）：清退避，新页面立即全速定位
                    if (ratio >= CHANGE_RATIO_PAGE) recognizePacer().clearBackoff()
                }
                return
            }
            // 第一道帧差（全帧采样：录屏帧不含悬浮窗，FLAG_SECURE 剔除）：窗口外
            // 背景静止即整帧丢弃，省下整屏解码与推理。
            // 例外：识别未确认期每帧都要推理（连续帧确认），不可短路。
            var visibleKept: Frame? = null
            val visibleRatio = reader.acquireLatestImage()?.use {
                val r = frameChangeRatio(it)
                // 变化帧像素立即入 lastFrame 缓冲：本帧随后多半被"无变化短路"丢弃
                // （快速路径只检测不识别），不保存则走子画面进不了兜底重放
                if (r >= CHANGE_RATIO_MIN) visibleKept = imageToFrame(it)
                r
            } ?: return
            if (visibleKept != null) lastFrame = visibleKept
            // 累积维护"画面是否变化"给 frameTick：变化帧哪怕被本帧节流/闸口拦截，
            // 标志也保持置位，兜底路径会抓新帧而不是重放旧帧。
            // 同时请求 burst 快速节奏：棋局一变即满负荷识别确认，尽早转发引擎
            if (visibleRatio >= CHANGE_RATIO_MIN) {
                lastMotionTs = now
                screenChangedSinceRec = true
                recognizePacer().requestBurst()
                // 切页级大变化（用户主动切 App/弹窗）：清退避，新页面立即全速定位
                if (visibleRatio >= CHANGE_RATIO_PAGE) recognizePacer().clearBackoff()
            }
            val probeDue = now - lastSlowProbeTs >= SLOW_PROBE_INTERVAL_MS
            if (visibleRatio < CHANGE_RATIO_MIN && !(slowPath && (trackingUnstable || probeDue))) return

            var frame: Frame?
            if (slowPath) {
                // 确认等待期（未定位/未确认）过识别节拍器：未放行时不抓帧、不解码
                // ——全图定位推理很重，真机上无棋盘画面（桌面/主菜单）也无限推理
                // 直至热节流拖垮全系统；已确认态的 6s 兜底 probe 不受影响（probe
                // 抓帧后的实际推理仍由 handleFrame 入口闸口把关）
                if ((sessionGrid == null || trackingUnstable) &&
                    !recognizePacer().canRecognizeNow(now)) {
                    runCatching { reader.acquireLatestImage()?.close() }
                    return
                }
                // FLAG_SECURE：悬浮窗已从录屏剔除——画面里永远没有悬浮窗，直接
                // 取帧全程零闪烁；全帧参与变化检测（窗口矩形露出的是真实棋盘）
                frame = reader.acquireLatestImage()?.use {
                    if (trackingUnstable || frameChanged(it)) imageToFrame(it) else null
                }
                lastSlowProbeTs = now
                if (frame == null) return
            } else {
                // 快速路径只做"变化检测"（上方 visibleChanged 分支已置位+burst）：
                // 画面正在动（本帧与上次采样不同），此刻解码推理大概率吃到走子动画
                // 的过渡帧（悬空子），烧一整轮 0.5s 推理还产出错误局面污染确认——
                // 识别等画面停稳后由 frameTick 兜底抓稳态帧完成（见 forceCaptureOnce
                // 的停稳门槛），延迟更低且不吃动画帧
                return
            }
            pendingFrame = true
            val f = frame
            workerHandler.post { pendingFrame = false; runCatching { handleFrame(f) } }
        } catch (t: Throwable) {
            // Throwable 而非 Exception：OOM 等 Error 若漏出会杀死唯一 worker 线程，
            // 识别管线永久停摆（切引擎的 pendingEngine 也随之失效）
            RLog.w(TAG, "onImageAvailable failed", t)
        }
    }

    private var frameRowBytes: ByteArray? = null

    /**
     * 帧差跳过：对（棋盘裁剪或全屏）区域做步长 16px 的稀疏采样，与上帧逐点比较。
     * 录屏为合成器直出，静止画面逐位一致，差异占比 >0.2% 才算"有变化"（只滤零星闪烁）。
     * 采样区域与推理 cropHint 一致。录屏帧不含悬浮窗（FLAG_SECURE 剔除，见
     * boardOccluded 注释），全帧单一签名槽全帧采样。
     *
     * 返回 false 表示画面静止，调用方直接丢帧（省下整屏解码与 YOLO 推理）。
     */
    private fun frameChanged(image: Image): Boolean =
        frameChangeRatio(image) >= CHANGE_RATIO_MIN

    /**
     * 帧差变化占比（变化采样点 / 参与比较的采样点，0~1）。签名槽维护与 [frameChanged]
     * 相同（比较即更新）。
     * 除布尔"有变化"外还区分变化幅度：切页/大弹窗是全屏重绘（占比远超走子动画/局部
     * 刷新）——只有切页级大变化才值得清识别退避（用户主动到达的新页面需要立即全速
     * 定位，而不是被此前无棋盘页面攒下的退避拖慢数秒）。
     */
    private fun frameChangeRatio(image: Image): Float {
        val plane = image.planes[0]
        val rowStride = plane.rowStride
        val pixelStride = plane.pixelStride
        val buffer = plane.buffer
        val w = image.width
        val h = image.height
        val crop = cropHint(w, h)
        val x0 = if (crop != null) max(0, crop[0].toInt()) else 0
        val y0 = if (crop != null) max(0, crop[1].toInt()) else 0
        val x1 = if (crop != null) min(w, crop[2].toInt()) else w
        val y1 = if (crop != null) min(h, crop[3].toInt()) else h
        val cols = ((x1 - x0) / FRAME_DIFF_STEP).coerceAtLeast(1)
        val rows = ((y1 - y0) / FRAME_DIFF_STEP).coerceAtLeast(1)
        // 每帧路径零分配：签名槽直接读写成员
        val cur = frameSig?.takeIf { it.size == cols * rows }
            ?: IntArray(cols * rows).also { frameSig = it }
        var diff = 0
        var idx = 0
        if (pixelStride == 4) {
            // 快路径：逐行批量 JNI 读（复用 frameRowBytes），行内稀疏采样
            var row = frameRowBytes
            if (row == null || row.size < w * 4) row = ByteArray(w * 4)
            frameRowBytes = row
            for (ry in 0 until rows) {
                val y = min(h - 1, y0 + ry * FRAME_DIFF_STEP)
                buffer.position(y * rowStride)
                buffer.get(row, 0, w * 4)
                for (cx in 0 until cols) {
                    val x = min(w - 1, x0 + cx * FRAME_DIFF_STEP)
                    val off = x * 4
                    val v = (0xFF shl 24) or
                        ((row[off].toInt() and 0xFF) shl 16) or
                        ((row[off + 1].toInt() and 0xFF) shl 8) or
                        (row[off + 2].toInt() and 0xFF)
                    if (v != cur[idx]) diff++
                    cur[idx] = v
                    idx++
                }
            }
        } else {
            // 兜底：非 4 字节步长的逐点读（采样点稀疏，开销可接受）
            for (ry in 0 until rows) {
                val y = min(h - 1, y0 + ry * FRAME_DIFF_STEP)
                for (cx in 0 until cols) {
                    val x = min(w - 1, x0 + cx * FRAME_DIFF_STEP)
                    val off = y * rowStride + x * pixelStride
                    val v = (0xFF shl 24) or
                        ((buffer.get(off).toInt() and 0xFF) shl 16) or
                        ((buffer.get(off + 1).toInt() and 0xFF) shl 8) or
                        (buffer.get(off + 2).toInt() and 0xFF)
                    if (v != cur[idx]) diff++
                    cur[idx] = v
                    idx++
                }
            }
        }
        // 差异占比超过 0.2%（1/500）视为画面变化；新建签名的全 0 初值与实际像素
        // 比对必然超限，首帧/采样区尺寸变化自动判"有变化"
        return diff.toFloat() / (cols * rows).coerceAtLeast(1)
    }

    private fun imageToFrame(image: Image): Frame {
        val plane = image.planes[0]
        val rowStride = plane.rowStride
        val pixelStride = plane.pixelStride
        val buffer = plane.buffer
        val w = image.width
        val h = image.height
        // 像素缓冲复用：每帧新分配 10MB 级 IntArray 会造成持续 GC 压力（同线程串行无踩踏）
        val px = framePixels?.takeIf { it.size == w * h } ?: IntArray(w * h).also { framePixels = it }
        if (pixelStride == 4) {
            // 快路径：整行一次批量 JNI 读，再在 JVM 内解 RGBA（避免每像素 4 次单字节 JNI 调用）
            var row = frameRowBytes
            if (row == null || row.size < w * 4) row = ByteArray(w * 4)
            frameRowBytes = row
            var p = 0
            for (y in 0 until h) {
                buffer.position(y * rowStride)
                buffer.get(row, 0, w * 4)
                var i = 0
                while (i < w * 4) {
                    px[p++] = (0xFF shl 24) or
                        ((row[i].toInt() and 0xFF) shl 16) or
                        ((row[i + 1].toInt() and 0xFF) shl 8) or
                        (row[i + 2].toInt() and 0xFF)
                    i += 4
                }
            }
        } else {
            for (y in 0 until h) {
                val rowStart = y * rowStride
                for (x in 0 until w) {
                    val off = rowStart + x * pixelStride
                    val r = buffer.get(off).toInt() and 0xFF
                    val g = buffer.get(off + 1).toInt() and 0xFF
                    val b = buffer.get(off + 2).toInt() and 0xFF
                    px[y * w + x] = (0xFF shl 24) or (r shl 16) or (g shl 8) or b
                }
            }
        }
        return Frame(w, h, px)
    }

    /**
     * 调试开关：当 /sdcard/assist_debug_save 存在时，把服务实际收到的帧原始字节存到
     * /sdcard/assist_frame.raw（magic=ASFR, 大端 int w/h, 之后 RGB 三字节每像素）。
     */
    private fun debugSaveFrameIfRequested(frame: Frame) {
        if (!com.cabinet.BuildConfig.DEBUG) return
        val trigger = java.io.File("/sdcard/assist_debug_save")
        if (!trigger.exists()) return
        trigger.delete()
        try {
            val dir = getExternalFilesDir(null) ?: filesDir
            val outPath = java.io.File(dir, "assist_frame.raw").absolutePath
            // BufferedOutputStream：DataOutputStream 直写 FileOutputStream 每字节一次
            // syscall，7MB 帧在模拟器上要写几十秒（曾误判为"写入中断"）
            java.io.DataOutputStream(java.io.BufferedOutputStream(
                java.io.FileOutputStream(outPath), 1 shl 16)).use { out ->
                out.writeInt(0x41534652) // 'ASFR'
                out.writeInt(frame.width)
                out.writeInt(frame.height)
                for (p in frame.argb) {
                    out.writeByte((p shr 16) and 0xFF)
                    out.writeByte((p shr 8) and 0xFF)
                    out.writeByte(p and 0xFF)
                }
            }
            RLog.i(TAG, "debug frame saved $outPath")
        } catch (e: Exception) {
            RLog.w(TAG, "debug frame save failed", e)
        }
    }

    // ==================== 识别与分析 ====================

    /**
     * 裁剪推理提示：已知棋盘位置时只把棋盘外扩约 1.2 格的区域送入模型，
     * 减少整屏缩放开销并提高棋子有效分辨率；尺寸向下取 16 的倍数以稳定裁剪位图复用。
     * 返回 null 表示整帧。
     */
    private fun cropHint(frameW: Int, frameH: Int): DoubleArray? {
        val g = sessionGrid ?: return null
        val x0 = g.nx0 * frameW
        val y0 = g.ny0 * frameH
        val x1 = g.nx1 * frameW
        val y1 = g.ny1 * frameH
        val mx = (x1 - x0) / 8.0 * 1.2
        val my = (y1 - y0) / 9.0 * 1.2
        val cx0 = max(0.0, x0 - mx)
        val cy0 = max(0.0, y0 - my)
        val cx1 = min(frameW.toDouble(), x1 + mx)
        val cy1 = min(frameH.toDouble(), y1 + my)
        val w = ((cx1 - cx0).toInt() / 16) * 16
        val h = ((cy1 - cy0).toInt() / 16) * 16
        if (w < 64 || h < 64) return null
        return doubleArrayOf(cx0, cy0, cx0 + w, cy0 + h)
    }

    private fun handleFrame(frame: Frame) {
        lastFrame = frame // framePixels 为复用缓冲，静止时内容即当前屏幕，供确认饿死重放
        if (manualMode) return
        if (paused) {
            // 悔棋/暂停后的自动恢复：曾出现"暂停后永不复位"被用户当成卡死。
            // 低频识别比对——局面与暂停基准不同（用户已改棋盘）即解除暂停并让本帧
            // 走正常流程；相同则维持暂停（保住"悔棋期间不被屏幕局面覆盖"的原意）。
            val now = System.currentTimeMillis()
            if (now - lastPauseCheckTs < OVERLAY_FRAME_INTERVAL_MS) return
            lastPauseCheckTs = now
            val base = pausedBoard
            if (base == null) {
                paused = false
            } else {
                val cur = recognizeOnceForPauseCheck(frame) ?: return
                if (AssistBoard.equal(cur, base)) return
                paused = false
                pausedBoard = null
                RLog.i(TAG, "pause auto-resume: board changed")
            }
        }
        debugSaveFrameIfRequested(frame)
        // 引擎切换请求在帧循环里应用（单线程重建，避免跨线程竞争）
        pendingEngine?.let { want ->
            pendingEngine = null
            if (want != engineName) switchEngine(want)
        }
        if (engineName == ENGINE_ONNX) { handleFrameOnnx(frame); return }
        val yolo = yoloDetector ?: run {
            yoloMissStreak++
            if (yoloMissStreak == 1 || yoloMissStreak % 20 == 0) {
                setStatus("识别引擎初始化失败：请尝试重启应用（重装可修复）")
            }
            return
        }
        val now = System.currentTimeMillis()
        // 节拍器闸口：成本自适应间隔 + 失败退避（此前固定 250ms 在画面持续变化时
        // 背靠背整屏推理，真机热节流拖垮全系统）
        if (!yoloPacer.canRecognizeNow(now)) return
        val crop = cropHint(frame.width, frame.height)
        val t0 = System.currentTimeMillis()
        val mapped = try {
            yolo.detect(frame, crop)
        } catch (e: Exception) {
            RLog.w(TAG, "yolo detect failed", e)
            null
        }
        val cost = System.currentTimeMillis() - t0
        if (mapped == null || mapped.pieceCount < MIN_RECOGNIZED_PIECES) {
            // 同 ONNX：对局中已定位棋盘的首次失败视为走子动画过渡帧——不退避不烧
            // burst、保留会话网格；连续 3 次失败才回全屏重定位（自愈）
            val transient = sessionGrid != null && yoloMissStreak == 0
            yoloPacer.markEnd(now = System.currentTimeMillis(), costMs = cost,
                ok = false, transient = transient)
            yoloMissStreak++
            if (yoloMissStreak >= 3) sessionGrid = null
            if (!transient && (yoloMissStreak == 1 || yoloMissStreak % 10 == 0)) {
                setStatus("正在识别棋盘…\n请让棋盘完整无遮挡")
            }
            return
        }
        yoloPacer.markEnd(now = System.currentTimeMillis(), costMs = cost,
            ok = mapped.grid != null, transient = mapped.grid != null || sessionGrid != null)
        if (mapped.grid != null) consumeScreenChange()
        yoloMissStreak = 0
        // 悬浮窗遮挡判断沿用网格语义：用 YOLO 棋盘框更新会话网格（同时作为裁剪推理提示）
        if (mapped.grid != null) sessionGrid = mapped.grid
        val result = RecognitionResult(
            mapped.canonical, mapped.orientation,
            AssistBoard.validate(mapped.canonical), mapped.pieceCount)
        // 与 rec(onnx) 字段对齐（fen + 整帧耗时）：两引擎性能对比实验（2026-10-03）
        // 依赖这两列——此前 YOLO 行只有检出数，识别出的局面与耗时全无记录，无法算准确率
        RLog.d(TAG, "rec(yolo): pieces=${mapped.pieceCount} orient=${mapped.orientation} " +
            "issue=${result.issues.size} dropped=${mapped.dropped} " +
            "fen=${AssistBoard.toFen(mapped.canonical, true).take(40)} ${cost}ms")
        afterRecognition(result, crop != null)
        // 同 ONNX：成功但仍未确认时续 burst，保持 300ms 级确认节奏（见 handleFrameOnnx）
        if (mapped.grid != null && tracker.confirmed == null) yoloPacer.requestBurst(4)
    }

    /**
     * 识别结果入跟踪器（YOLO 与 ONNX 两条引擎路径共用）：
     * 确认新局后上行分析；不稳定时处理裁剪框自锁回退与状态提示。
     */
    private fun afterRecognition(result: RecognitionResult, hadCrop: Boolean) {
        // 遮挡期保险丝（2026-10-02）：悬浮窗几何遮挡棋盘且识别结果本身无效时丢弃
        // ——个别 ROM 若不把 secure 层剔出录屏，面板像素入镜会拼出垃圾局面，绝不能
        // 上行（PC 会拒收报"识别异常"）。FLAG_SECURE 正常生效时结果有效，不受影响
        if (boardOccluded && !result.isValid()) {
            RLog.d(TAG, "遮挡期间识别结果无效，丢弃（防垃圾局面上行）")
            return
        }
        // 屏幕朝向下一帧即权威（chess-dike 式逐帧跟随）：只采结构合法帧。
        // 旧行为只在确认新局面时锁存一次，SAME_BOARD 帧永不刷新——过渡帧一旦
        // 污染朝向便永久卡死（2026-10-01 模拟器实测：我方执色/小地图/轮次全错）。
        // 真实翻转棋盘也由此在下一帧（约 1s）随动，无需等新局面确认。
        if (result.issues.isEmpty()) currentOrientation = result.orientation
        when (tracker.onFrame(result)) {
            BoardTracker.Event.NEW_BOARD -> {
                cropUnstableStreak = 0
                invalidStreak = 0
                onBoardConfirmed()
            }
            BoardTracker.Event.NEW_GAME -> {
                cropUnstableStreak = 0
                invalidStreak = 0
                onBoardConfirmed(isNewGame = true)
            }
            BoardTracker.Event.UNSTABLE -> {
                // 裁剪框内长时间不稳定（棋盘挪动后残框自锁）：回退全屏重新定位
                if (hadCrop) {
                    cropUnstableStreak++
                    if (cropUnstableStreak >= 10) {
                        cropUnstableStreak = 0
                        sessionGrid = null
                    }
                } else {
                    cropUnstableStreak = 0
                }
                // 注意不能用 tracker.unstableStreak 判角点缓存失效：首次确认等待期
                // （连续 confirmCount 帧累计）它也递增，会把刚建的缓存反复清掉。
                // 只看"结果本身非法"（issues 非空/含未知格）是否连续出现——棋盘真挪走
                // 时透视变换采到的是别处画面，分类结果必然连续非法。
                if (!result.isValid()) invalidStreak++ else invalidStreak = 0
                if (engineName == ENGINE_ONNX && invalidStreak >= 3) {
                    onnxEngine?.invalidateCorners()
                }
                if (tracker.unstableStreak >= 15) {
                    setStatus("识别不稳定，稳定后自动恢复\n请让棋盘完整无遮挡")
                }
            }
            BoardTracker.Event.SAME_BOARD -> { invalidStreak = 0 }
        }
    }

    /** ONNX 引擎识别一帧：角点定位+全盘分类，产出后走与 YOLO 相同的确认/上行链路 */
    private fun handleFrameOnnx(frame: Frame) {
        val eng = onnxEngine
        if (eng == null || !eng.loaded) return // 模型加载中/已回退：跳帧
        val now = System.currentTimeMillis()
        // 节拍器闸口：间隔=max(1s, 上次成本×2)×退避。真机单帧 1~2s 时自动拉长到
        // 2~4s，杜绝背靠背推理；固定 800ms 节流在真机上形同虚设
        if (!onnxPacer.canRecognizeNow(now)) return
        val t0 = System.currentTimeMillis()
        val r = try {
            eng.recognize(frame)
        } catch (t: Throwable) {
            RLog.w(TAG, "onnx recognize error", t)
            null
        }
        val cost = System.currentTimeMillis() - t0
        if (r == null) {
            RLog.d(TAG, "onnx recognize -> null (cost=$cost)")
            // 已定位棋盘下的失败多为走子动画过渡帧（悬空子/计数抖动）：不退避不烧
            // burst、保留会话网格（棋盘没动，角点缓存与遮挡判断继续有效——否则一次
            // 过渡帧失败就会触发下一帧隐藏抓屏+全图定位，白等 ~1s）；连续 3 次失败
            // 才判定棋盘真挪走/切了 App，回全屏重定位（自愈）
            val transient = sessionGrid != null && yoloMissStreak == 0
            onnxPacer.markEnd(now = System.currentTimeMillis(), costMs = cost,
                ok = false, transient = transient)
            yoloMissStreak++
            if (yoloMissStreak >= 3) {
                eng.invalidateCorners()
                sessionGrid = null
            }
            if (!transient && (yoloMissStreak == 1 || yoloMissStreak % 10 == 0)) {
                setStatus("正在识别棋盘…\n请让棋盘完整无遮挡")
            }
            return
        }
        yoloMissStreak = 0
        // 角点包围盒换算会话网格（悬浮窗遮挡判断与下一帧裁剪提示复用）。
        // 非法结果（issues 非空）不可信——尤其不能用它更新遮挡网格：曾因切换 App 后
        // 旧网格残留，遮挡判断失准走快路径不隐藏悬浮窗，识别持续吃到悬浮窗入镜的
        // 残缺画面形成死循环。置 null 强制回慢路径（隐藏抓帧）。
        val grid = try {
            BoardGrid.fromPxCorners(
                r.boxPx[0].coerceIn(0.0, frame.width.toDouble()),
                r.boxPx[1].coerceIn(0.0, frame.height.toDouble()),
                r.boxPx[2].coerceIn(0.0, frame.width.toDouble()),
                r.boxPx[3].coerceIn(0.0, frame.height.toDouble()),
                frame.width, frame.height)
        } catch (e: Exception) { null }
        val result = RecognitionResult(
            r.canonical, r.orientation,
            AssistBoard.validate(r.canonical), r.pieceCount)
        // 过渡帧也可能产出结构非法局面（悬空子被错读）：对局中已定位棋盘的首次非法
        // 按瞬时失败记账（不退避不烧 burst），并保留会话网格；连续非法由 invalidStreak
        // 熔断（frameTick 的 needsRecapture gate），棋盘真挪走由 r==null 路径自愈
        val issuesEmpty = result.issues.isEmpty()
        onnxPacer.markEnd(now = System.currentTimeMillis(), costMs = cost,
            ok = issuesEmpty, transient = issuesEmpty || (sessionGrid != null && yoloMissStreak == 0))
        if (issuesEmpty) {
            consumeScreenChange()
            sessionGrid = grid
        }
        RLog.d(TAG, "rec(onnx): pieces=${r.pieceCount} orient=${r.orientation} " +
            "issue=${result.issues.size} fen=${AssistBoard.toFen(r.canonical, true).take(40)} " +
            "${r.elapsedMs}ms")
        if (System.currentTimeMillis() - lastHexLogTs > 20_000) {
            // 诊断：屏幕坐标原始识别 vs canonical 归一化对照（丢子/错位时定位丢在哪层）
            lastHexLogTs = System.currentTimeMillis()
            RLog.d(TAG, "rec hex raw=${boardHex(r.screenRaw)}")
            RLog.d(TAG, "rec hex can=${boardHex(r.canonical)}")
        }
        afterRecognition(result, sessionGrid != null)
        // 成功但仍未确认（candidate 累积中）：续 burst 保持 300ms 级确认节奏——
        // 角点缓存复用帧很便宜（实测 ~180ms），pacer 常规下限 1s 会把开局首次确认
        // 拖到 2s+（模拟器实测确认耗时 3.2s）；确认完成（confirmed!=null）后不再续，
        // burst 逐帧耗尽自然回归稳态，无空转风险
        if (issuesEmpty && tracker.confirmed == null) onnxPacer.requestBurst(4)
    }

    /** 主页切换识别引擎（worker 帧循环里应用；切换后局面重新确认） */
    fun setRecognizerEngine(name: String) { pendingEngine = name }

    /** 按配置初始化识别引擎；onnx 模型加载重，放后台线程，期间帧走跳过逻辑 */
    private fun initEngine(name: String) {
        engineName = name
        if (name != ENGINE_ONNX) return
        Thread {
            // 分段计时开关走配置（bench_profiling，默认关）：性能对比实验/排查耗时用，
            // 打开后每次识别多一条 timing 日志（稳态 ~1s/条），平时不产生额外开销
            val eng = OnnxBoardRecognizer(assets, profiling = config.benchProfiling)
            val ok = try { eng.load() } catch (t: Throwable) {
                RLog.w(TAG, "onnx load threw", t); false
            }
            if (engineName != ENGINE_ONNX) { eng.release(); return@Thread } // 期间已被切换走
            onnxEngine = if (ok) eng else null
            if (!ok) {
                engineName = ENGINE_YOLO
                setStatus("ONNX 引擎加载失败\n已回退 YOLO 识别")
            }
            RLog.i(TAG, "onnx engine: ${if (ok) "ready" else "load failed, fallback to yolo"}")
        }.start()
    }

    /** 切换引擎：释放旧 ONNX 会话、重置跟踪状态（两引擎输出细节有差异，重新确认更稳） */
    private fun switchEngine(want: String) {
        RLog.i(TAG, "switch engine: $engineName -> $want")
        tracker.reset()
        resetSuggestionState()
        // 重新确认后允许重发当前局面一次（悬浮窗建议已重置，需 PC 补推）
        lastSentFen = null
        onnxEngine?.release()
        onnxEngine = null
        sessionGrid = null
        yoloMissStreak = 0
        invalidStreak = 0
        onnxPacer.reset()
        yoloPacer.reset()
        engineName = want
        if (want == ENGINE_ONNX) initEngine(want)
    }

    /**
     * 确认链终点（worker 调用：afterRecognition ← handleFrame/handleFrameOnnx），
     * 把"连续帧置信"转成全局状态的唯一入口。顺序即契约：刷四个快照
     * （canonical/pieces/orientation/FEN，渲染与上行都读）→ 重新武装
     * lastAutoFlipFen/lastReprobeFen（每局面一次的自动纠正/自愈配额）→
     * 清建议状态 → 入悔棋历史 → 渲染并上行。悔棋（undoOneStep）与手动编辑
     * 的 history 操作同样收敛到本线程串行（B1/B2），但快照更新各自完成、
     * 不经过这里。
     */
    private fun onBoardConfirmed(isNewGame: Boolean = false) {
        val confirmed = tracker.confirmed ?: return
        currentCanonical = confirmed.canonical
        currentPieces = flatten(confirmed.canonical)
        currentOrientation = confirmed.orientation
        currentFen = AssistBoard.toFen(confirmed.canonical, tracker.redGo)
        // 自愈配额按盘面值记账（2026-10-24 残局震荡 7 秒 3 连发成因治理）：此前
        // 每次确认无条件清零——识别在两个错读之间抖动时 flip↔重探↔确认可循环；
        // 现在只有确认出新盘面才重新武装，同一盘面（含仅轮次字段不同的重读）
        // 终身一次自愈。盘面对比用 FEN 首段（轮次字段不参与，flip 只改轮次）
        val confirmedBoard = currentFen.substringBefore(' ')
        if (lastReprobeFen?.substringBefore(' ') != confirmedBoard) lastReprobeFen = null
        if (lastAutoFlipFen?.substringBefore(' ') != confirmedBoard) lastAutoFlipFen = null
        resetSuggestionState()
        history.addLast(HistoryEntry(confirmed, tracker.redGo, currentFen))
        if (history.size > 60) history.removeFirst()
        if (isNewGame) {
            // 单局日志分界（协议 §4.11）：PC 落盘检测到此行即切换下一局日志文件
            // phone-<开始时刻>-局<N>.log（2026-10-01 用户提议：单日单文件混多局
            // 检索困难，一局一文件）。特征串与 PC 端 phoneGameSplitMarker 保持一致
            RLog.i(TAG, "===== 新局开始 fen=$currentFen =====")
            // 复盘闭环（2026-10-17）：NEW_GAME 后的首条上行带 new_game 标记——
            // PC 端据此生成对局 ID，建议沉淀按局归组。标志在 scheduleAnalysis
            // 消费（worker 串行，置位→消费有序）
            pendingNewGameFlag = true
            // B1 双保险：新局红先，旧持久化素材作废（开局 isStartLike 路径本身
            // 也已不消费素材，此处显式清防"确认后上行前进程再死"的窗口）
            config.lastConfirmedFen = ""
        }
        RLog.i(TAG, "board confirmed: $currentFen")
        postRender()
        scheduleAnalysis()
    }

    /** 诊断用 90 格紧凑编码：每格 1 字符（0=空，1-e=Piece 值十六进制），行序 y0..y9 */
    private fun boardHex(b: Array<IntArray>): String {
        val sb = StringBuilder(92)
        for (y in 0..9) for (x in 0..8) {
            val p = b[y][x]
            sb.append(if (p in 1..14) "0123456789abcde"[p] else '0')
        }
        return sb.toString()
    }

    @Volatile private var lastHexLogTs = 0L

    private fun flatten(p: Array<IntArray>): IntArray {
        val out = IntArray(90)
        for (y in 0 until 10) for (x in 0 until 9) out[y * 9 + x] = p[y][x]
        return out
    }

    /** 重置建议状态：局面/轮次变化后，旧的分析结果与成熟度跟踪全部作废 */
    private fun resetSuggestionState() {
        lastSuggestion = null
        selectedCandidate = 0
        suggestionAggregator.reset()
        lastSuggestionAt = 0L
        lastSugDepth = 0
        searchSettled = false
    }

    /**
     * 成熟补绘：静默成熟判据是时间函数（静默 SUGGEST_IDLE_MS 即定着），渲染却是
     * 事件驱动——"单条建议后 PC 沉默"（沉淀命中：不起搜索即无收束帧，桔库命中
     * 另有强制 ✓ 分支）时，建议到达那刻渲染必画（暂定），之后没有任何事件再触发
     * 重绘，✓ 永远画不出来（2026-10-01 真机日志实锤：复现局走出桔库范围后步步
     * 卡暂定，需人为点"更新"恢复）。每次建议到达后排一次静默期补绘兜底；
     * 到点若静默未满（期间又有新建议刷新了计时）按剩余时间续排，链路不断。
     */
    private val matureRecheck = Runnable {
        matureRecheckPending = false
        if (overlayClosed || lastSuggestion == null) return@Runnable
        val idle = System.currentTimeMillis() - lastSuggestionAt
        if (idle >= SUGGEST_IDLE_MS) {
            postRender() // 静默已满：重算成熟度翻 ✓（searchSettled 场景幂等无害）
        } else {
            scheduleMatureRecheck(SUGGEST_IDLE_MS - idle + 50)
        }
    }

    private fun scheduleMatureRecheck(delayMs: Long) {
        if (matureRecheckPending) return
        matureRecheckPending = true
        mainHandler.postDelayed(matureRecheck, delayMs)
    }

    /** MoveChinese.describe 容错版：解析失败时原样返回 UCCI */
    private fun describeSafe(fen: String, ucci: String): String =
        try { MoveChinese.describe(fen, ucci) } catch (e: Exception) { ucci }

    /**
     * 分析调度：把确认局面经 WSS 上行给电脑端"内阁"。
     * 查开局库/驱动皮卡鱼/结果按 FEN"最新覆盖"过滤全部在 PC 端完成；
     * 我方回合与对方回合都上行。
     * 线程：任意线程可调（体内 post 到 workerHandler 串行执行）——主线程按钮
     * （悔棋/更新/手动编辑）与 worker 确认链共用本入口。lastSentFen 在 remote
     * 非空后、发送前置位：断线静默失败也视为已发，恢复依赖 welcome 补发（见下）；
     * remote 为空（服务未初始化/已销毁）时不得置位——此时 sendPosition 没跑、
     * lastPositionFen 没记，welcome 补发链无从兜底，置位等于永久丢弃该局面
     * （此后局面不变 SameBoard 就再也不发，2026-10-24 排查 B2 缺口）。
     */
    private fun scheduleAnalysis(force: Boolean = false) {
        workerHandler.post {
            val fen = currentFen
            if (fen.isEmpty()) return@post
            // 同局面同轮次去重：PC 端"最新覆盖"虽幂等，但重复 position 会重复查库/
            // 重搜并重推建议，两侧 UI 状态都被抖动（详见 lastSentFen 注释）。
            // force（"更新"按钮）跳过去重：显式要求 PC 重新分析
            if (!force && fen == lastSentFen) return@post
            val r = remote ?: run {
                mainHandler.post { postRender() }
                return@post
            }
            lastSentFen = fen
            // B1 轮次持久化（2026-10-24）：本函数是全部轮次变更路径（确认链/更新
            // 按钮/自动纠正/手动编辑/悔棋重发）的汇聚点，此处 FEN 已是最终态——
            // 进程重启后 BoardTracker 首次确认分支据此恢复走子方
            config.lastConfirmedFen = fen
            // 断线时也要调用：sendPosition 先记录局面后发送（断线时发送静默失败），
            // 重连成功后 welcome 会自动补发最新局面——否则断线期间确认的每步棋
            // 建议全部漏发，且状态栏仍显示"已连接"，用户看到的就是"卡住不动"
            // new_game 标记一次性消费（置位后随首条上行发出即清零——重发/补发
            // 不带标记，防 PC 把同一局拆成多局；完整语义见 sendPosition KDoc
            // 与 pendingNewGameFlag 字段注释）
            val isNewGameFrame = pendingNewGameFlag
            pendingNewGameFlag = false
            r.sendPosition(fen, force, isNewGameFrame)
            if (remoteConnected && tracker.redGo == mySideIsRed()) {
                mainHandler.post { setStatus("电脑分析中…") }
            }
        }
    }

    /**
     * 远程建议回调（OkHttp 线程 → mainHandler 落 UI）。
     * 绿箭头语义（协议 §4.5 + 产品约定）：
     *   - source 为 book:* 到达即定着（开局库命中，确定性建议，零等待绿 ✓）；
     *   - engine 建议按成熟度跟踪：bestmove 稳定 1.2s → 绿；
     *   - 收到仅含 state 的收束 status → onSearchSettled → 立即定着。
     */
    private val remoteListener = object : RemoteAnalysisClient.Listener {
        override fun onConnectionState(state: RemoteAnalysisClient.ConnectionState) {
            remoteConnected = state == RemoteAnalysisClient.ConnectionState.CONNECTED
            mainHandler.post {
                when (state) {
                    RemoteAnalysisClient.ConnectionState.CONNECTING -> setStatus("连接电脑中…")
                    RemoteAnalysisClient.ConnectionState.CONNECTED -> {
                        remoteErrorText = null
                        setStatus("已连接电脑")
                        // 连线页设置的线程档（>0 才下发，0=跟随电脑端）：连接即应用，
                        // 释放皮卡鱼算力（PC 默认只给逻辑核一半，追求极限在此调高）
                        if (config.engineThreads > 0) {
                            remote?.sendSetEngine(null, null, threads = config.engineThreads)
                        }
                    }
                    RemoteAnalysisClient.ConnectionState.DISCONNECTED ->
                        setStatus(remoteErrorText ?: "电脑连接断开，自动重连中…")
                }
                postRender()
            }
        }

        override fun onEngineState(s: String) {
            pcEngineReady = s == "ready"
            pcEngineState = s
            mainHandler.post { postRender() }
        }

        override fun onServerEngineConfig(depth: Int, hashMB: Int) {
            // PC 实际算力同步（welcome 携带，2026-10-02）：按钮显示与循环起点改为
            // PC 真实值——此前显示手机本地默认档，出现"手机 0.25g / 电脑 2048"错位。
            // 只在 PC 报了有效值时覆盖对应档（depth/hash 各自独立，缺省侧保持原样）
            mainHandler.post {
                if (depth > 0 && config.engineDepth != depth) {
                    RLog.i(TAG, "同步 PC 实际深度: ${config.engineDepth} -> $depth")
                    config.engineDepth = depth
                }
                if (hashMB > 0 && config.engineHashMB != hashMB) {
                    RLog.i(TAG, "同步 PC 实际 Hash: ${config.engineHashMB} -> $hashMB")
                    config.engineHashMB = hashMB
                }
                postRender()
            }
        }

        override fun onEngineMetrics(state: String, depth: Int, nodes: Long, nps: Long, hashfull: Int) {
            lastMetrics = EngMetrics(state, depth, nodes, nps, hashfull)
            // PC 搜索期每条指标帧都全量重绘会造成面板持续跳动（2026-10-02 闪烁
            // 治理）：与 onSuggestion 同款 120ms 节流 + 补绘排队
            val now = System.currentTimeMillis()
            if (now - lastMetricsRenderAt >= SUGGEST_RENDER_THROTTLE_MS) {
                lastMetricsRenderAt = now
                mainHandler.post { postRender() }
            } else if (!metricsRenderPending) {
                metricsRenderPending = true
                val delay = SUGGEST_RENDER_THROTTLE_MS - (now - lastMetricsRenderAt)
                mainHandler.postDelayed({
                    metricsRenderPending = false
                    lastMetricsRenderAt = System.currentTimeMillis()
                    postRender()
                }, delay)
            }
        }

        override fun onSuggestion(sug: RemoteSuggestion) {
            // 迟到旧建议过滤（2026-10-02）：PC 回显建议所属局面，与本机当前局面
            // 不符说明是"最新覆盖"赛跑中的在途旧帧——丢弃，不渲染。旧建议对着新
            // 棋盘会误报"轮次可能识别有误"并压掉箭头（旧 PC 不带 fen 时为 null，
            // 不过滤）。局面在途更新时新建议随后必到，无需任何补救。
            if (sug.fen != null && currentFen.isNotEmpty() && sug.fen != currentFen) {
                RLog.i(TAG, "丢弃迟到建议（fen 已过期）: ${sug.bestMove}")
                return
            }
            // 开局库建议不倒退覆盖引擎多线（2026-10-01 用户实测"电脑变招手机没有
            // 跟上"）：同一局面内 PC 因重发被桔库命中短路时，回的单条 book 建议会让
            // 聚合好的 MultiPV 各线候选从界面上消失。局面变化会走 resetSuggestionState
            // 清场，这里的"已有引擎候选"必然属于当前局面——book 只在无引擎候选时显示
            if (sug.fromBook && lastSuggestion != null && !lastSuggestion!!.fromBook &&
                suggestionAggregator.candidates.isNotEmpty()
            ) {
                RLog.i(TAG, "忽略开局库建议（不倒退覆盖引擎多线候选）: ${sug.bestMove}")
                return
            }
            // 当前局面的建议正常到达 = 分析链路恢复：清除错误状态（否则"连接异常：
            // 局面识别异常"会在自愈成功后一直挂着到重连）
            if (remoteErrorText != null) {
                remoteErrorText = null
                setStatus("识别已恢复")
            }
            lastSuggestion = sug
            // 多路候选聚合（见 SuggestionAggregator 注释）：按 bestMove 去重，
            // 新着法追加、已有着法用新建议刷新（分数/深度取最新）
            suggestionAggregator.onSuggestion(sug)
            // 成熟度跟踪（静默窗口）：距最后一条建议到达超过 SUGGEST_IDLE_MS 即定着——
            // best 变化不再重置计时（旧设计在 MultiPV 多线振荡下会无限重置、永久卡暂定）；
            // depth 回到浅层 = PC 开始新一轮搜索，收束态作废回暂定（诚实反映"正在算"）
            val d = sug.depth
            if (lastSugDepth > 3 && d in 1..3) searchSettled = false
            if (d > 0) lastSugDepth = d
            lastSuggestionAt = System.currentTimeMillis()
            // 节流渲染：窗口内到达的后续建议只安排一次补绘（见常量注释）
            val now = System.currentTimeMillis()
            if (now - lastSuggestionRenderAt >= SUGGEST_RENDER_THROTTLE_MS) {
                lastSuggestionRenderAt = now
                mainHandler.post { postRender() }
            } else if (!suggestionRenderPending) {
                suggestionRenderPending = true
                val delay = SUGGEST_RENDER_THROTTLE_MS - (now - lastSuggestionRenderAt)
                mainHandler.postDelayed({
                    suggestionRenderPending = false
                    lastSuggestionRenderAt = System.currentTimeMillis()
                    postRender()
                }, delay)
            }
            // 静默期成熟补绘兜底（见 matureRecheck 注释）：单条建议后 PC 沉默的场景
            scheduleMatureRecheck(SUGGEST_IDLE_MS + 200)
        }

        override fun onSearchSettled() {
            searchSettled = true
            mainHandler.post { postRender() }
        }

        override fun onError(code: String, message: String, turnFlipLegal: Boolean, fen: String?) {
            // 迟到旧错误过滤（2026-10-02）：PC 错误帧回显触发局面，与本机当前局面
            // 不符说明是"最新覆盖"赛跑中的在途旧帧——丢弃，绝不作用于新局面（此前
            // 旧局面错误会让新局面错翻轮次或误挂"识别异常"）。旧 PC 不带 fen 时为
            // null，不过滤。局面更新后如仍异常，PC 会针对新局面再回错误帧。
            if (fen != null && currentFen.isNotEmpty() && fen != currentFen) {
                RLog.i(TAG, "丢弃迟到错误帧（fen 已过期）: $code")
                return
            }
            // 自愈瀑布（2026-10-24 扩容）：bad_position（吃子步轮次卡错，PC 已验证
            // 翻转合法则先翻）/ no_moves（无着可走——真绝杀，或残局漏子拼出的假
            // 局面）/ bad_frame（将帅缺一等结构非法，比 bad_position 更强的"盘面
            // 拼错"信号，此前零自愈）三类共用重探配额；手动模式全跳过（用户接管）
            if (!manualMode && (code == "bad_position" || code == "no_moves" || code == "bad_frame")) {
                mainHandler.post {
                    val cur = currentFen
                    if (cur.isEmpty()) return@post
                    if (code == "bad_position" && turnFlipLegal && cur != lastAutoFlipFen) {
                        RLog.w(TAG, "bad_position: PC 已验证翻转轮次合法，自动纠正走子方重发")
                        flipTurnAndResend()
                        setStatus("已自动纠正走子方\n现${if (tracker.redGo == mySideIsRed()) "轮到我方走" else "轮到对方走"}")
                    } else if (cur != lastReprobeFen) {
                        // 自愈重探（每盘面一次）：翻转无效/已翻过仍被拒、无着可走、
                        // 结构非法——多半是识别瞬态误读（列偏/漏子拼出不可能局面）
                        // ——回全屏重新定位+识别。同盘面二次仍异常（识别确定性复现
                        // 或真绝杀）才落错误提示，配额记账防震荡风暴（见 onBoardConfirmed）
                        RLog.w(TAG, "$code: 触发自愈重探（重新定位+识别）fen=$cur")
                        lastReprobeFen = cur
                        setStatus("局面识别异常\n正在重新定位识别…")
                        workerHandler.post {
                            forceFullRelocate()
                        }
                    } else {
                        // 配额用尽：不再自动循环，把手动出口顶到用户眼前
                        // （真绝杀时核对棋盘即知；识别误读时点『手动』改盘）
                        showRemoteError("识别自愈未成功——请核对棋盘，或点『手动』修正")
                    }
                }
                return
            }
            showRemoteError(message)
        }

        /** 普通错误提示：状态条截断展示（40 字符内），供非自动纠正路径复用 */
        private fun showRemoteError(message: String) {
            RLog.w(TAG, "remote error: $message")
            if (overlayClosed) return
            val short = if (message.length > 40) message.take(40) + "…" else message
            remoteErrorText = short
            mainHandler.post { setStatus(short) }
        }

        override fun onConnectError(reason: String) {
            // "连接被拒绝"补记当前网络类型（2026-10-24 诊断增强）：实测该错误在
            // PC 正常监听期间出现过 ×3（8 秒窗口），代码侧无嫌疑——嫌疑是手机
            // 网络面切换（WiFi↔蜂窝）后私网路由不可达，下次发生可直接定位
            val full = if (reason.contains("拒绝")) "$reason（当前网络：${currentNetworkBrief()}）" else reason
            RLog.w(TAG, "connect error: $full")
            if (overlayClosed) return
            remoteErrorText = "连接失败：$full"
            mainHandler.post { setStatus("连接电脑失败：$full\n自动重试中…") }
        }

        /** 诊断用当前网络简述（WiFi/蜂窝/无网络/未知） */
        private fun currentNetworkBrief(): String {
            val cm = getSystemService(ConnectivityManager::class.java) ?: return "未知"
            val caps = cm.activeNetwork?.let { cm.getNetworkCapabilities(it) } ?: return "无网络"
            return when {
                caps.hasTransport(NetworkCapabilities.TRANSPORT_WIFI) -> "WiFi"
                caps.hasTransport(NetworkCapabilities.TRANSPORT_CELLULAR) -> "蜂窝"
                else -> "其他"
            }
        }
    }

    // ==================== 悬浮窗 ====================

    /**
     * 主线程专用（唯一调用方 renderOverlay；addView/animate 必须主线程）。
     * 幂等：已建直接返回。无悬浮窗权限返回 null——renderOverlay 在此整体静默：
     * HUD 停摆但识别/上行照跑（服务存活、数据仍新鲜），授权恢复后下一次
     * 渲染自动重建。
     */
    private fun ensureOverlay(): OverlayPanelView? {
        if (overlayPanel != null) return overlayPanel
        if (!android.provider.Settings.canDrawOverlays(this)) return null
        val wm = getSystemService(Context.WINDOW_SERVICE) as WindowManager
        val panel = OverlayPanelView(this)
        val dm = screenMetrics()
        // 面板固定尺寸（关闭自适应）：总宽 392dp（6+114+4+171+2+90+6），
        // 高约 197dp（棋盘 185 主导）；三列比例与 346dp 基准一致（114:171:90 = 100:150:78）
        val panelW = panel.dp(392)
        val panelH = panel.dp(197)
        val (px0, py0) = config.overlayPos
        // 默认位置：屏幕底部中央的空白区（多数象棋 App 棋盘下方有空余区域；
        // 不遮挡棋盘 => 平时无需为抓帧隐藏面板，完全无闪烁）
        // 持久化位置按旧面板宽度归一化，面板加宽/加高后可能整体出屏 => 钳回屏内
        val x = (if (px0 < 0) (dm.widthPixels - panelW) / 2 else (px0 * dm.widthPixels).toInt())
            .coerceIn(0, max(0, dm.widthPixels - panelW))
        val y = (if (py0 < 0) dm.heightPixels - panelH else (py0 * dm.heightPixels).toInt())
            .coerceIn(0, max(0, dm.heightPixels - panelH))
        val params = WindowManager.LayoutParams(
            WindowManager.LayoutParams.WRAP_CONTENT,
            WindowManager.LayoutParams.WRAP_CONTENT,
            WindowManager.LayoutParams.TYPE_APPLICATION_OVERLAY,
            WindowManager.LayoutParams.FLAG_NOT_FOCUSABLE or
                WindowManager.LayoutParams.FLAG_LAYOUT_NO_LIMITS or
                (if (!BENCH_DISABLE_SECURE) WindowManager.LayoutParams.FLAG_SECURE else 0),
                PixelFormat.TRANSLUCENT
        ).apply {
            gravity = Gravity.TOP or Gravity.START
            this.x = x
            this.y = y
        }
        // FLAG_SECURE：secure 层从录屏合成中剔除——悬浮窗永不入镜，抓帧零闪烁，
        // 识别看到完整棋盘（详见 boardOccluded 字段注释的雷区说明）
        panel.onAction = ::onOverlayAction
        panel.onCellTap = ::onManualCellTap
        panel.onDragStateChange = { dragging ->
            panelDragging = dragging
            // 松手即时重算遮挡（否则要等 ≤700ms 下一 tick）：拖动结束瞬间提示
            // 就给出准确的责任窗状态
            if (!dragging) workerHandler.post { recomputeOcclusion() }
        }
        panel.applyTheme(OverlayTheme.fromKey(config.overlayTheme))
        panel.setDragHandle(params, wm) {
            config.overlayPos = params.x.toFloat() / dm.widthPixels to params.y.toFloat() / dm.heightPixels
        }
        wm.addView(panel, params)
        // 入场动效：淡入 + 1.02→1 缩放（150ms；只做出现提示，禁持续动画/循环光效）
        panel.alpha = 0f
        panel.scaleX = 1.02f
        panel.scaleY = 1.02f
        panel.animate().alpha(1f).scaleX(1f).scaleY(1f).setDuration(150).start()
        // attach 后再应用一次主题（同详情窗：attach 前设置在悬浮窗上不生效）
        panel.post { panel.applyTheme(OverlayTheme.fromKey(config.overlayTheme)) }
        overlayPanel = panel
        overlayParams = params
        windowManager = wm
        return panel
    }

    /** 悬浮窗面板矩形与已定位棋盘矩形是否重叠（重叠时抓帧前才需短暂隐藏面板） */
    private fun overlayIntersectsBoard(): Boolean =
        windowIntersectsBoard(overlayParams) { (overlayPanel?.width ?: 0) to (overlayPanel?.height ?: 0) }

    /** 详情窗（窗口2）与棋盘是否重叠（同主面板语义） */
    private fun detailIntersectsBoard(): Boolean =
        windowIntersectsBoard(detailParams) { (detailView?.width ?: 0) to (detailView?.height ?: 0) }

    /**
     * 遮挡几何重算（worker 线程）：逐窗计算与已定位棋盘的相交，沿变化才 postRender
     * （渲染事件驱动）。拖动只豁免被拖的那一窗——拖主面板时详情窗若仍压棋盘，
     * 提示继续显示并指明责任窗。调用点：onImageAvailable 入口（每真实帧）、
     * frameTick 周期（守卫外，paused/manualMode 也重算）、拖动结束与详情窗开关
     * （post 到 worker：松手即刷新，不等 ≤700ms 的下一 tick）。
     */
    private fun recomputeOcclusion() {
        val gridReady = sessionGrid != null
        val pOcc = gridReady && !panelDragging && overlayIntersectsBoard()
        val dOcc = gridReady && !detailDragging && detailIntersectsBoard()
        if (panelOccluded != pOcc || detailOccluded != dOcc) {
            panelOccluded = pOcc
            detailOccluded = dOcc
            RLog.i(TAG, "board occlusion: panel=$pOcc detail=$dOcc")
            postRender() // 渲染是事件驱动的：遮挡沿变化即时重绘提示行
        }
    }

    /** 窗口矩形（params 位置 + view 尺寸）与已定位棋盘矩形是否重叠 */
    private inline fun windowIntersectsBoard(
        params: WindowManager.LayoutParams?,
        size: () -> Pair<Int, Int>,
    ): Boolean {
        val grid = sessionGrid ?: return false
        val p = params ?: return false
        val (w, h) = size()
        if (w <= 0 || h <= 0) return false
        val (fw, fh) = config.frameSize
        if (fw <= 0 || fh <= 0) return false
        val bx0 = grid.nx0 * fw
        val by0 = grid.ny0 * fh
        val bx1 = grid.nx1 * fw
        val by1 = grid.ny1 * fh
        val px1 = p.x + w
        val py1 = p.y + h
        return p.x < bx1 && px1 > bx0 && p.y < by1 && py1 > by0
    }

    /**
     * 悬浮窗按钮入口（主线程：View 点击回调）。undo/flip/manual 修改的 worker
     * 侧状态均为 @Volatile（单布尔/计数原子）；history 的读写已全部收敛到
     * workerHandler 串行执行（B1/B2 修复，见 undoOneStep/pushManualHistory），
     * flipTurnAndResend 只写 @Volatile 的 tracker.redGo——改这三个入口前先
     * 确认线程归属仍成立。
     */
    private fun onOverlayAction(action: OverlayAction) {
        when (action) {
            OverlayAction.CYCLE_DEPTH -> {
                // 远程调算力（协议 §4.10）：循环深度档位并上行 set_engine。
                // PC 端 TuneEngine 应用后自动落盘回写 cabinet.toml（切换记住
                // 选择 2026-10-18）——本机 config.engineDepth 只是显示起点
                val next = nextStepValue(depthSteps, config.engineDepth)
                config.engineDepth = next
                remote?.sendSetEngine(next, null)
                setStatus("电脑引擎深度 → $next")
                postRender()
            }
            OverlayAction.CYCLE_HASH -> {
                // PC 端 TuneEngine 应用后自动落盘（重启保持用户档位，2026-10-18）；
                // 本机 engineHashMB 只是显示起点，以 PC 落盘值为准（档位注释见 hashSteps）。
                // 2026-10-20 起 PC 空闲时也立即冲刷 setoption（此前无搜索可搭车时
                // 引擎毫无动作），全场景即时生效
                val next = nextStepValue(hashSteps, config.engineHashMB)
                config.engineHashMB = next
                remote?.sendSetEngine(null, next)
                setStatus("电脑引擎 Hash → ${hashShort(next)}\n（已生效）")
                postRender()
            }
            OverlayAction.CYCLE_MULTI_PV -> {
                // 变招路数 1–3 循环（协议 §4.10 set_engine.multi_pv）：PC 引擎热
                // 生效并自动以最近局面重搜（空闲时立即冲刷，2026-10-20 起）；PC
                // 面板下拉 1.5s 轮询自动回显实际值。PC 无逐次回传帧，悬浮窗显示
                // 本地持久化值——PC 端若手动调过路数，手机点一次即以手机为准。
                val next = nextStepValue(multiPvSteps, config.engineMultiPv)
                config.engineMultiPv = next
                remote?.sendSetEngine(null, null, multiPV = next)
                setStatus("变招路数 → $next\n（已生效）")
                postRender()
            }
            OverlayAction.TOGGLE_DETAIL -> {
                detailVisible = !detailVisible
                if (detailVisible) {
                    // 手动打开即视为用户要详情窗：解除"开局未确认不自动弹"的门槛，
                    // 否则窗口被 updateDetailWindow 的 gate 挡住不刷新内容
                    detailAutoShown = true
                    ensureDetailWindow()
                } else removeDetailWindow()
                postRender()
            }
            OverlayAction.PAUSE -> {
                if (paused) {
                    paused = false
                    pausedBoard = null
                    setStatus("继续指导")
                } else {
                    pauseRecognition()
                    setStatus("已停止指导\n改动棋盘后自动恢复")
                }
            }
            OverlayAction.CLOSE -> {
                // 关闭悬浮窗：停止录屏、移除面板、停止指导（与录制耦合，之后可在连线页重新授权启动）
                overlayClosed = true
                overlayPanel?.let { p -> runCatching { windowManager?.removeView(p) } }
                overlayPanel = null
                removeDetailWindow()
                paused = true
                releaseCapture()
                setStatus("已关闭（录屏已停止）\n可在连线页重新启动")
                stopSelf()
            }
            OverlayAction.CYCLE_CANDIDATE -> {
                // 变招：在聚合出的多路候选间循环（见 SuggestionAggregator 注释）。
                // 仅当引擎搜索聚合出 ≥2 个着法时可切；单候选（开局库命中/沉淀/
                // 变招路数=1）保持提示指引——协议 v1 逐条下发，候选靠端上聚合。
                val cands = suggestionAggregator.candidates
                if (cands.size > 1) {
                    selectedCandidate = (selectedCandidate + 1) % cands.size
                    postRender()
                } else {
                    setStatus(
                        if (remoteConnected) "当前只有单一建议\n电脑端『变招路数』需 ≥2"
                        else "未连接电脑，暂无建议"
                    )
                }
            }
            OverlayAction.UNDO -> undoOneStep()
            OverlayAction.MANUAL -> toggleManualMode()
            OverlayAction.FLIP_TURN -> {
                // 更新棋局（原"轮次"）：识别差分偶发失败会让 redGo 卡在旧值（显示"轮到对方走"
                // 而实际轮到我方），一键翻转并按新轮次重建 FEN、重新上行分析。
                flipTurnAndResend()
                setStatus("棋局已更新\n现${if (tracker.redGo == mySideIsRed()) "轮到我方走" else "轮到对方走"}")
            }
        }
    }

    /**
     * 翻转走子方并按新轮次重建 FEN、force 重上行分析（"更新"按钮与 bad_position
     * 自动纠正共用）。force=true 让 PC 跳过桔库/沉淀直接引擎重搜——否则同局面
     * 重发被桔库命中短路回单条库建议，多路变招候选拿不回来（2026-10-01 实测）。
     * 同时记录 lastAutoFlipFen：这条 FEN 已纠正过，自动纠正不再重复触发。
     */
    private fun flipTurnAndResend() {
        tracker.redGo = !tracker.redGo
        resetSuggestionState()
        val canonical = currentCanonical
        if (canonical != null) {
            currentFen = AssistBoard.toFen(canonical, tracker.redGo)
        }
        lastAutoFlipFen = currentFen
        postRender()
        scheduleAnalysis(force = true)
    }

    /**
     * 自愈重探（bad_position 翻转无效时，2026-10-02）：回全屏重新定位+识别——
     * 清角点缓存（ONNX）与会话网格，清 lastSentFen（重读结果即使 FEN 相同也要
     * 重发对账），请求 burst 加速。worker 线程调用（管线状态只在 worker 改）。
     */
    private fun forceFullRelocate() {
        if (engineName == ENGINE_ONNX) onnxEngine?.invalidateCorners()
        sessionGrid = null
        lastSentFen = null
        recognizePacer().requestBurst()
    }

    /**
     * 悔棋：回退到上一个已确认局面并重新分析（上一手之前的建议）。
     * 纯本地状态回退，不操作游戏 App；回退后暂停实时同步，点『继续』恢复。
     * B1/B2 修复（2026-10-27）：整个回退体收敛到 workerHandler 串行执行——原先
     * 主线程 removeLast/last + tracker.restore 与 worker 确认链的 addLast/onFrame
     * 并发（tracker 内部 candidate/candidateHits 非 volatile，restore 清它们是
     * 跨线程写），且 pauseRecognition 在出栈后才置位、拦不住已在途的确认帧；
     * 同线程化后三类竞态一并消除（在途确认帧要么先于本消息完成、其结果被弹栈
     * 弹掉，要么后于本消息执行、此时已暂停不会覆盖）。setStatus/postRender/
     * scheduleAnalysis 本就经 mainHandler/workerHandler post，跨线程调用安全；
     * 连点悔棋在 worker 队列逐条执行，逐步回退语义不变。
     */
    private fun undoOneStep() {
        workerHandler.post {
            if (history.size <= 1) {
                setStatus("没有可悔的局面（会随对弈自动积累）")
                return@post
            }
            history.removeLast()
            val prev = history.last()
            tracker.restore(prev.result, prev.redGo)
            currentCanonical = prev.result.canonical
            currentPieces = flatten(prev.result.canonical)
            currentOrientation = prev.result.orientation
            currentFen = prev.fen
            resetSuggestionState()
            // 暂停实时同步：否则屏幕上的当前局面会在下一帧覆盖悔棋结果。
            // 暂停期间低频识别比对，用户在棋盘 App 改动局面后自动恢复（曾因永不复位被当成卡死）
            // （手动模式下帧识别本就被 manualMode 拦截，退出后无需再点"继续"）
            if (!manualMode) pauseRecognition()
            postRender()
            scheduleAnalysis()
            setStatus("已悔棋一步\n改动棋盘后自动恢复")
        }
    }

    /** 进入暂停：记录当前局面作比对基准（自动恢复判据用） */
    private fun pauseRecognition() {
        pausedBoard = currentCanonical?.map { it.copyOf() }?.toTypedArray()
        paused = true
    }

    /** 暂停期间的单次识别（按当前引擎），返回 canonical 或 null——仅供自动恢复比对。
     *  同样过识别节拍器：暂停检查是完整推理，不能游离在负载约束之外 */
    private fun recognizeOnceForPauseCheck(frame: Frame): Array<IntArray>? {
        val pacer = recognizePacer()
        if (!pacer.canRecognizeNow()) return null
        val t0 = System.currentTimeMillis()
        val canonical: Array<IntArray>? = try {
            if (engineName == ENGINE_ONNX) onnxEngine?.recognize(frame)?.canonical
            else yoloDetector?.detect(frame, null)?.canonical
        } catch (t: Throwable) {
            RLog.w(TAG, "pause check recognize failed", t)
            null
        }
        pacer.markEnd(now = System.currentTimeMillis(),
            costMs = System.currentTimeMillis() - t0, ok = canonical != null)
        return canonical
    }

    /**
     * 紧急手动模式开关：识别彻底失效（如某些残局界面）或产出幽灵局面时，
     * 暂停帧识别，由用户在小棋盘上手工维护局面让引擎继续指导。
     * 种子局面取最近一次确认局面；识别从未成功时从起始局面开始录入。
     */
    private fun toggleManualMode() {
        manualMode = !manualMode
        manualSelected = null
        if (manualMode) {
            if (currentCanonical == null) {
                val start = AssistBoard.canonicalStart()
                currentCanonical = start
                currentPieces = flatten(start)
                currentOrientation = Orientation.STANDARD
                currentFen = AssistBoard.toFen(start, tracker.redGo)
            }
            // 快照种子局面（悔棋的锚点；与已确认局面相同则不重复入栈）。
            // B1：history 读写收敛到 worker（见 undoOneStep），闭包内重读
            // manualMode 防排队瞬间模式已翻转的极小窗口
            val canonical = currentCanonical
            if (canonical != null) {
                workerHandler.post {
                    if (manualMode && history.lastOrNull()?.fen != currentFen) {
                        pushManualHistory(canonical, tracker.redGo)
                    }
                }
            }
            setStatus("手动模式（再点退出）\n点棋选子 → 再点走子/删子")
        } else {
            setStatus("已退出手动模式\n恢复自动识别")
        }
        postRender()
    }

    /** 手动模式：小棋盘点格（canonical x,y）——选子 / 走子 / 删子 */
    private fun onManualCellTap(x: Int, y: Int) {
        if (!manualMode) return
        val board = currentCanonical ?: return
        val sel = manualSelected
        if (sel == null) {
            // 未选中：点有子的格则选中，空格无操作
            if (board[y][x] != Piece.EMPTY) {
                manualSelected = x to y
                postRender()
            }
            return
        }
        if (sel == x to y) {
            // 再点一次已选中的子：删除（帅/将必须保留，否则局面非法、引擎无法分析）
            val p = board[y][x]
            manualSelected = null
            if (p == Piece.WSHUAI || p == Piece.BJIANG) {
                setStatus("手动模式\n帅/将不可删除")
                postRender()
                return
            }
            board[y][x] = Piece.EMPTY
            applyManualBoard(board, flipTurn = false)
            return
        }
        // 走子：起点必须有子；目标格可为空格或对方子（吃子），按实战着法录入并翻转走子方
        val moving = board[sel.second][sel.first]
        manualSelected = null
        if (moving == Piece.EMPTY) {
            postRender()
            return
        }
        board[sel.second][sel.first] = Piece.EMPTY
        board[y][x] = moving
        applyManualBoard(board, flipTurn = true)
    }

    /** 手动局面快照入历史栈（复用悔棋：可逐步撤销手工编辑）。
     *  ⚠仅 worker 线程调用（B1：history 单线程纪律，调用点见 applyManualBoard/
     *  toggleManualMode 的 workerHandler.post） */
    private fun pushManualHistory(board: Array<IntArray>, redGo: Boolean) {
        val result = RecognitionResult(
            AssistBoard.clone(board), currentOrientation,
            emptyList(), countPieces(board))
        history.addLast(HistoryEntry(result, redGo, AssistBoard.toFen(board, redGo)))
        if (history.size > 60) history.removeFirst()
    }

    private fun countPieces(board: Array<IntArray>): Int =
        board.sumOf { row -> row.count { it != Piece.EMPTY } }

    /** 手动编辑生效：入栈新局面（history.last 恒等于当前显示局面，悔棋可逐步回退），重建缓存并重新上行分析 */
    private fun applyManualBoard(board: Array<IntArray>, flipTurn: Boolean) {
        if (flipTurn) tracker.redGo = !tracker.redGo
        currentCanonical = board
        currentPieces = flatten(board)
        currentFen = AssistBoard.toFen(board, tracker.redGo)
        // B1：手动编辑入栈也收敛到 worker（history 单线程纪律，见 undoOneStep）；
        // 本 post 先于 scheduleAnalysis 内部的 post 入队，worker 串行保证"入栈→上行"顺序
        workerHandler.post { pushManualHistory(board, tracker.redGo) }
        resetSuggestionState()
        postRender()
        scheduleAnalysis()
    }

    /**
     * 我方执色判定：纯随动"屏幕下方阵营=我方"（识别朝向 STANDARD=红在下=执红）。
     * 曾有的『换边』手动覆盖已随按钮一并移除（2026-09-30 用户反馈），策略见 [MySidePolicy]。
     */
    private val mySidePolicy = MySidePolicy()

    private fun mySideIsRed(): Boolean =
        mySidePolicy.mySideIsRed(currentOrientation == Orientation.STANDARD)

    private fun setStatus(text: String) {
        lastStatusText = text
        // 瞬时提示改为 TTL 覆盖态：elapsedTicker 每秒重渲染后提示不再被立刻冲掉
        statusOverrideText = text
        statusOverrideUntilMs = System.currentTimeMillis() + STATUS_OVERRIDE_TTL_MS
        mainHandler.post {
            renderOverlay()
            // 关闭悬浮窗后服务可能在绑定下短暂存活：不再往通知栏贴"运行中"通知
            // （stopForeground 已移除的那条不能复活，否则残留一条滑不掉的僵尸通知）
            if (overlayClosed) return@post
            runCatching {
                val nm = getSystemService(NotificationManager::class.java)
                nm.notify(NOTIF_ID, buildNotification(text))
            }
        }
    }

    /**
     * 连线页线程档变化后立即下发（协议 §4.10 set_engine.threads）。
     * 0 档（跟随电脑端）也要显式下发 threads=0：PC 端语义为"自动=逻辑核一半"，
     * 否则此前下发过的线程数会残留在 PC 端，按钮显示与实际算力不符。
     * 未连接时静默丢弃（连接成功回调会对非 0 档补发）。
     */
    fun applyEngineThreads() {
        val t = config.engineThreads
        RLog.i(TAG, "applyEngineThreads: $t")
        remote?.sendSetEngine(null, null, threads = t)
    }

    /** 主题变更即时应用（连线页切换"悬浮窗背景"后两个窗口立即换肤） */
    fun applyOverlayTheme() {
        val theme = OverlayTheme.fromKey(config.overlayTheme)
        RLog.i(TAG, "applyOverlayTheme: $theme panel=${overlayPanel != null} detail=${detailView != null}")
        mainHandler.post {
            overlayPanel?.applyTheme(theme)
            detailView?.applyTheme(theme)
        }
    }

    @Volatile private var lastStatusText = "未启动"

    /** setStatus 瞬时提示（TTL 覆盖态）：TTL 内所有渲染持续顶住主状态大字，
     *  过期由常规运行文案接管（elapsedTicker 每秒重渲染也不冲掉） */
    @Volatile private var statusOverrideText: String? = null
    @Volatile private var statusOverrideUntilMs = 0L

    private fun postRender() {
        mainHandler.post { renderOverlay() }
    }

    private fun renderOverlay() {
        // 用户点“关闭”后不重建（直到重新启动服务）
        if (overlayClosed) return
        val panel = ensureOverlay() ?: return
        val fen = currentFen
        val myRed = mySideIsRed()
        val sug = lastSuggestion
        // 选中候选（变招循环）：主面板与详情窗统一展示同一线的分数/胜率/来源——
        // 详情窗若固定取最新建议（最优线），变招切到候选 2/3 后两窗各说各话
        val cands = suggestionAggregator.candidates
        val selSug = if (selectedCandidate in cands.indices) cands[selectedCandidate] else sug
        // 遮挡提醒行（独立小行，不挤占状态行；手动模式局面自维护不提示）
        // 遮挡提示按责任窗给文案：合并判定时代只说"挡住棋盘"，用户移开主面板
        // 清不掉详情窗造成的黄字（2026-10-01 重构）。两窗都挡时主面板文案优先，
        // 挪开后提示自动切到详情窗——引导逐个清理
        val occludedHint = when {
            manualMode -> null // 手动模式局面自维护，不提示
            panelOccluded -> "悬浮窗挡住棋盘，建议移开"
            detailOccluded -> "详情窗挡住棋盘，建议移开"
            else -> null
        }

        // ---- 结构化 HUD 槽位（棋韵 §5.2：大字一眼可读，其余降噪）----
        // 顶部状态行：运行模式 · 用时（用时随秒跳动，elapsedTicker 每秒驱动重渲染）
        val modeText = when {
            manualMode -> "手动模式"
            paused -> "已停止"
            remoteErrorText != null -> "连接异常"
            remote == null -> "未连接"
            !remoteConnected -> "连接中"
            !pcEngineReady -> when (pcEngineState) {
                "error" -> "引擎异常"
                "offline" -> "未启用引擎"
                else -> "引擎准备中"
            }
            else -> "运行中"
        }
        val elapsed = elapsedText()
        val stateText = when {
            paused -> "已停止"
            elapsed.isEmpty() -> modeText
            else -> "$modeText · $elapsed"
        }
        val stateKind = when {
            remoteErrorText != null -> OverlayStateKind.ERROR
            manualMode -> OverlayStateKind.MANUAL
            paused -> OverlayStateKind.IDLE
            !remoteConnected -> OverlayStateKind.WAITING
            else -> OverlayStateKind.RUNNING
        }

        // 主状态大字：setStatus 覆盖文案（TTL 内的瞬时提示）优先，其次按运行状态
        val turnRed = tracker.redGo
        val myTurn = if (fen.isEmpty()) false else (turnRed == myRed)
        val activeOverride = statusOverrideText
            ?.takeIf { System.currentTimeMillis() < statusOverrideUntilMs }
        val headline = activeOverride ?: when {
            manualMode -> "手动模式"
            paused -> "已停止指导\n点『继续』恢复"
            remoteErrorText != null -> remoteErrorText ?: "连接异常"
            !remoteConnected -> if (remote == null) "未配置电脑地址" else "连接电脑中…"
            !pcEngineReady -> when (pcEngineState) {
                "error" -> "电脑引擎异常，重试中…"
                "offline" -> "电脑端未启用引擎"
                else -> "电脑引擎准备中…"
            }
            fen.isEmpty() -> "识别中"
            myTurn -> "轮到我方走"
            else -> "轮到对方走"
        }

        // 优势行（红方统一视角 → 玩家视角着色；无建议/无分数时隐藏；随选中候选走）
        var advantageText: String? = null
        var advantageTone = QiTone.NONE
        advantageLabel(selSug, brief = false)?.let { (label, redGood) ->
            advantageText = label
            advantageTone = if (redGood == myRed) QiTone.GOOD else QiTone.BAD
        }

        var candidateText: String? = null
        var candidateTone = QiTone.NONE
        var candIndex = 0
        var candCount = 0
        var arrowFrom: Pair<Int, Int>? = null
        var arrowTo: Pair<Int, Int>? = null
        var suggestMature = false
        // 识别中断/持续不稳定时，旧建议所依据的局面可能已失效——不再展示，避免误导
        // （手动模式局面由用户维护，不受识别健康度影响）
        val recHealthy = manualMode || (yoloMissStreak == 0 && tracker.unstableStreak < 5)

        // 送子防线：着法起点必须是我方棋子——轮次错乱/幽灵 FEN 时宁可不显示
        fun fromIsOurs(ucci: String): Boolean {
            val arr = currentPieces ?: return false
            if (ucci.length < 4) return false
            val fx = ucci[0] - 'a'
            val fy = 9 - (ucci[1] - '0')
            if (fx !in 0..8 || fy !in 0..9) return false
            val p = arr[fy * 9 + fx]
            return p in 1..14 && Piece.isRed(p) == myRed
        }

        // 建议区文案三分支（顺序即优先级，新建议形态往表里加分支）：
        //   ①开局库命中（fromBook，含桔库多候选时间线）→ 绿 ✓ 确定性建议
        //   ②引擎建议（含沉淀缓存帧：sel.cached 显示"· 缓存"且到达即成熟）
        //   ③引擎未就绪（remoteConnected 但 pcEngineReady=false 的兜底文案）
        // 各分支共同守卫：recHealthy（识别不健康不显示，手动模式豁免）+
        // fromIsOurs（着法起点必须是我方子，幽灵 FEN 宁可不显示）。
        when {
            myTurn && recHealthy && sug != null && sug.fromBook -> {
                // 开局库命中（PC 端查库回传）：确定性建议，到达即定着绿 ✓（协议语义）。
                // 桔库候选唤醒（2026-10-17）：PC 逐条按 multipv 槽位下发多候选，
                // 聚合器照常落槽——渲染跟随"变招"循环选中项（lastSuggestion 是
                // 最后到达帧=最弱候选，直接用它箭头会指错）；单候选时 selSug 兜底
                // 回 sug，与旧行为等价。
                val sel = if (selectedCandidate in cands.indices) cands[selectedCandidate] else sug
                val ucci = sel.bestMove
                if (fromIsOurs(ucci)) {
                    suggestMature = true
                    val arrow = ucciToCells(ucci)
                    arrowFrom = arrow.first
                    arrowTo = arrow.second
                    val chs = describeSafe(fen, ucci)
                    // 缓存标记独立于来源：沉淀记录的 source 可能是 "book:<名>"，
                    // 命中缓存时即使走开局库文案分支也要标出（PC 端同款约定）
                    val cacheTag = if (sel.cached) "（缓存）" else ""
                    // 多候选时库名省略（详情窗有来源行），格式向引擎候选文案对齐
                    candidateText = if (cands.size > 1) "候选 ${selectedCandidate + 1}/${cands.size} · $chs ✓$cacheTag"
                                    else if (sel.bookName.isNotEmpty()) "开局库·${sel.bookName} $chs ✓$cacheTag"
                                    else "开局库 $chs ✓$cacheTag"
                    candidateTone = QiTone.GOOD
                } else {
                    candidateText = "轮次可能识别有误\n走一步后自动纠正"
                    candidateTone = QiTone.WARN
                }
            }
            myTurn && recHealthy && sug != null -> {
                // 我方回合引擎建议：显示选中候选（变招循环，见 SuggestionAggregator）；
                // 无多候选/未切换时即最新建议。成熟度 = 搜索收束 或 所选候选建议静默
                // 超 SUGGEST_IDLE_MS（按候选自身到达时刻，切换候选看到的稳定度不再张冠李戴）
                val sel = selSug ?: sug
                val ucci = sel.bestMove.ifEmpty { sel.pv.firstOrNull() } ?: ""
                if (ucci.isNotEmpty() && fromIsOurs(ucci)) {
                    val selArrived = suggestionAggregator.arrivedAtOf(sel.bestMove)
                        .takeIf { it > 0 } ?: lastSuggestionAt
                    // 沉淀缓存终值（sel.cached）无搜索过程、无收束帧，本身就是历史
                    // 成熟建议——到达即视为成熟，不该显示"暂定"误导用户
                    suggestMature = searchSettled || sel.cached ||
                        System.currentTimeMillis() - selArrived >= SUGGEST_IDLE_MS
                    val chs = describeSafe(fen, ucci)
                    val arrow = ucciToCells(ucci)
                    arrowFrom = arrow.first
                    arrowTo = arrow.second
                    var suffix = if (suggestMature) " ✓" else "（暂定）"
                    // 缓存标记：告诉用户这条建议是沉淀库秒回（历史终值），不是现算
                    if (sel.cached) suffix += " · 缓存"
                    if (cands.size > 1) {
                        candIndex = selectedCandidate + 1
                        candCount = cands.size
                        candidateText = "候选 $candIndex/$candCount · $chs$suffix"
                    } else {
                        candidateText = "建议 $chs$suffix"
                    }
                    candidateTone = if (suggestMature) QiTone.GOOD else QiTone.NONE
                } else if (ucci.isNotEmpty()) {
                    candidateText = "轮次可能识别有误\n走一步后自动纠正"
                    candidateTone = QiTone.WARN
                }
            }
            myTurn && remoteConnected && !pcEngineReady && fen.isNotEmpty() -> {
                candidateText = when (pcEngineState) {
                    "error" -> "电脑引擎异常，重试中…"
                    "offline" -> "电脑端未启用引擎"
                    else -> "电脑引擎准备中…"
                }
            }
        }

        panel.render(OverlayModel(
            stateText = stateText,
            stateKind = stateKind,
            occludedText = occludedHint,
            headlineText = headline,
            advantageText = advantageText,
            advantageTone = advantageTone,
            candidateText = candidateText,
            candidateTone = candidateTone,
            candidateIndex = candIndex,
            candidateCount = candCount,
            pieces = currentPieces,
            arrowFrom = arrowFrom,
            arrowTo = arrowTo,
            flipped = currentOrientation == Orientation.FLIPPED,
            mySideIsRed = myRed,
            mature = suggestMature,
            paused = paused,
            manualMode = manualMode,
            selectedCell = manualSelected,
            depthText = depthLabel(),
            hashText = hashLabel(),
            multiPvText = config.engineMultiPv.toString(),
        ))
        // 窗口2（详情）：首次棋盘确认后才自动弹出（开局不预弹空壳窗，见字段注释）；
        // 之后与主面板同节奏刷新（红黑优/引擎指标/来源）
        if (!detailAutoShown && tracker.confirmed != null) detailAutoShown = true
        updateDetailWindow(myRed, selSug)
    }

    // ==================== 详情窗（窗口2） ====================

    /** 深度档芯片值（"20"；未配置/未连接返回 null → 芯片显示占位） */
    private fun depthLabel(): String? =
        if (remote == null) null else config.engineDepth.toString()

    /** 状态行"用时"：整数档（2026-10-03 用户要求去小数点）——60s 内显秒、1h 内显分、以上显时；未启动返回空串省略用时 */
    private fun elapsedText(): String {
        val t0 = captureStartedAtMs
        if (t0 <= 0L) return ""
        val s = (System.currentTimeMillis() - t0) / 1000
        return when {
            s < 60 -> "${s}s"
            s < 3600 -> "${s / 60}m"
            else -> "${s / 3600}h"
        }
    }

    /** Hash 档按钮文本（紧凑格式：0.25g/0.5g/1g/2g/3g/4g/6g/8g/10g；档外值显示 xxxm 兜底） */
    private fun hashLabel(): String? =
        if (remote == null) null else hashShort(config.engineHashMB)

    private fun hashShort(mb: Int): String = when {
        // 1024 的整数倍一律 g 显示（welcome 同步来的 PC 实际值可能不在档位表内）
        mb == 256 -> "0.25g"
        mb == 512 -> "0.5g"
        mb >= 1024 && mb % 1024 == 0 -> "${mb / 1024}g"
        else -> "${mb}m"
    }

    /** 引擎状态中文（详情窗显示） */
    private fun engineStateText(s: String): String = when (s) {
        "ready" -> "就绪"; "loading" -> "加载中"; "offline" -> "待机"
        "error" -> "异常"; "searching" -> "搜索中"
        else -> s
    }

    /**
     * 优势标签（红方统一视角换算为玩家视角）：返回 (文本, 红方占优)。
     * brief=true 详情窗大字（"红优 +0.8"）；brief=false 面板优势行（"红方优势 +0.8"）。
     * 无分数（库命中无 score）返回 null。
     */
    private fun advantageLabel(sug: RemoteSuggestion?, brief: Boolean = true): Pair<String, Boolean>? {
        sug ?: return null
        sug.scoreMate?.let { mate ->
            return if (mate > 0) "红方 ${mate} 步杀" to true
            else "黑方 ${-mate} 步杀" to false
        }
        val cp = sug.scoreCp ?: return null
        return if (brief) {
            if (cp >= 0) "红优 +%.1f".format(cp / 100.0) to true
            else "黑优 %.1f".format(-cp / 100.0) to false
        } else {
            if (cp >= 0) "红方优势 +%.1f".format(cp / 100.0) to true
            else "黑方优势 %.1f".format(-cp / 100.0) to false
        }
    }

    /** 创建或显示详情窗（首次创建带拖动与主题；位置持久化归一化存储） */
    private fun ensureDetailWindow(): OverlayDetailView? {
        if (!android.provider.Settings.canDrawOverlays(this)) return null
        val wm = getSystemService(Context.WINDOW_SERVICE) as WindowManager
        val view = detailView ?: OverlayDetailView(this).also { v ->
            val dm = screenMetrics()
            // 窗口估算值（仅默认落位/钳制用，窗口本体 WRAP_CONTENT）：
            // 宽 = 锚定行 260dp + 根 padding 28dp；高 = 三行结构堆叠（2026-10-18
            // 详情窗重构"美观且占地少"：大字/状态同行、指标单行、来源/胜率同行）
            val w = v.dp(288)
            val h = v.dp(96)
            val (nx, ny) = config.overlayDetailPos
            // 默认位置（用户从未拖过，nx<0）：详情窗创建必在首次棋盘确认后
            // （detailAutoShown 门槛），此时 sessionGrid 可用——优先把窗落在
            // 已确认棋盘矩形之外（先试棋盘正下方、与主面板不挤压；不行再试
            // 棋盘正上方），都放不下退回 60% 屏高。窗口不遮棋盘 => 识别走
            // 快速路径，稳态走子零闪烁；用户拖动后位置持久化，永不再自动挪动
            // ⚠【问题标明 2026-10-01】cnvcs 等全屏自绘棋盘下棋盘上下都放不下 →
            // 退回 60% 屏高（defY 兜底）正好压在棋盘中部 → detailIntersectsBoard()
            // 恒 true → 遮挡黄字常驻；提示文案又不指明责任窗，用户误以为挪主面板
            // 能清掉——"移开后黄字不消失"的主因（根因链详见 boardOccluded KDoc）
            var defX = (dm.widthPixels - w) / 2
            var defY = (dm.heightPixels * 3) / 5
            val grid = sessionGrid
            if (nx < 0 && grid != null) {
                val (fw, fh) = config.frameSize
                if (fw > 0 && fh > 0) {
                    val belowY = (grid.ny1 * fh).toInt() + 16
                    val aboveY = (grid.ny0 * fh).toInt() - h - 16
                    val panelTop = overlayParams?.y ?: dm.heightPixels
                    when {
                        belowY + h <= dm.heightPixels && belowY + h <= panelTop + 20 -> defY = belowY
                        aboveY >= 0 -> defY = aboveY
                    }
                }
            }
            val x = (if (nx < 0) defX else (nx * dm.widthPixels).toInt())
                .coerceIn(0, max(0, dm.widthPixels - w))
            val y = (if (ny < 0) defY else (ny * dm.heightPixels).toInt())
                .coerceIn(0, max(0, dm.heightPixels - h))
            val p = WindowManager.LayoutParams(
                WindowManager.LayoutParams.WRAP_CONTENT,
                WindowManager.LayoutParams.WRAP_CONTENT,
                WindowManager.LayoutParams.TYPE_APPLICATION_OVERLAY,
                WindowManager.LayoutParams.FLAG_NOT_FOCUSABLE or
                    (if (!BENCH_DISABLE_SECURE) WindowManager.LayoutParams.FLAG_SECURE else 0),
                PixelFormat.TRANSLUCENT
            ).apply {
                gravity = Gravity.TOP or Gravity.START
                this.x = x
                this.y = y
            }
            v.applyTheme(OverlayTheme.fromKey(config.overlayTheme))
            v.setDragHandle(p, wm) {
                val d = screenMetrics()
                config.overlayDetailPos = p.x.toFloat() / d.widthPixels to p.y.toFloat() / d.heightPixels
            }
            // 拖动详情窗期间同样抑制识别管线取帧（窗口在移动，帧内位置不稳定）
            v.onDragStateChange = { dragging ->
                detailDragging = dragging
                if (!dragging) workerHandler.post { recomputeOcclusion() }
            }
            wm.addView(v, p)
            // 与主面板同款入场动效（150ms 淡入 + 微缩放）
            v.alpha = 0f
            v.scaleX = 1.02f
            v.scaleY = 1.02f
            v.animate().alpha(1f).scaleX(1f).scaleY(1f).setDuration(150).start()
            // attach 后再应用一次主题：实测 attach 前设置的背景/文字色在部分
            // 悬浮窗路径上不生效（XML inflate 色残留），attach 后设置才可靠
            v.post { v.applyTheme(OverlayTheme.fromKey(config.overlayTheme)) }
            detailParams = p
            detailView = v
            // 详情窗入屏可能新增/解除遮挡：即时重算，不等下一 tick
            workerHandler.post { recomputeOcclusion() }
        }
        return view
    }

    /** 隐藏详情窗（保留实例以便再次显示） */
    private fun removeDetailWindow() {
        detailView?.let { v -> runCatching { windowManager?.removeView(v) } }
        detailView = null
        detailParams = null
        // 详情窗出屏可能解除遮挡：即时重算，不等下一 tick
        workerHandler.post { recomputeOcclusion() }
    }

    /** 详情窗内容刷新（renderOverlay 尾部调用；隐藏态/开局未确认棋盘前不创建） */
    private fun updateDetailWindow(myRed: Boolean, sug: RemoteSuggestion?) {
        if (!detailVisible || overlayClosed || !detailAutoShown) return
        val view = ensureDetailWindow() ?: return
        val (label, redGood) = advantageLabel(sug) ?: (null to false)
        // 我方视角着色：我方占优玉、我方劣势警示、无分数灰
        val tone = when {
            label == null -> QiTone.NONE
            redGood == myRed -> QiTone.GOOD
            else -> QiTone.BAD
        }
        val m = lastMetrics
        val state = m?.state ?: if (remoteConnected) if (pcEngineReady) "ready" else "…"
        else "未连接"
        val stateKind = when {
            !remoteConnected -> OverlayStateKind.WAITING
            m?.state == "error" -> OverlayStateKind.ERROR
            m != null && m.depth > 0 -> OverlayStateKind.RUNNING
            else -> OverlayStateKind.IDLE
        }
        val elapsed = elapsedText()
        val stateText = buildString {
            append("引擎").append(engineStateText(state))
            if (elapsed.isNotEmpty()) append(" · ").append(elapsed)
        }
        // 指标四项：深度 = 实际/目标（目标取本机请求档）+ 节点/速率 + 哈希档
        val hasMetrics = m != null && m.depth > 0
        val depthV = if (hasMetrics) {
            val target = config.engineDepth
            if (target > 0) "${m!!.depth}/$target" else m!!.depth.toString()
        } else "—"
        val nodesV = if (hasMetrics) fmtCount(m!!.nodes) else "—"
        val npsV = if (hasMetrics) "${fmtCount(m!!.nps)}/s" else "—"
        val hashV = hashShort(config.engineHashMB).uppercase()
        // 胜率行（协议字段恒为红方视角，2026-10-18 实测修复）：展示层换算成
        // 我方视角——否则我方执黑时"胜 x%"显示的是对手（红方）胜率，与大字
        // 颜色（我方视角着色）自相矛盾，用户读感即"胜率算错"。三字段不全则
        // 隐藏。皮卡鱼 WDL 是自对弈模型估计（和棋主导），与 sigmoid(cp) 不可
        // 互推，数值本身并非计算错误。
        val wdlText = sug?.let { s ->
            s.wdlW?.takeIf { s.wdlD != null && s.wdlL != null }?.let {
                val (w, l) = if (myRed) it to s.wdlL!! else s.wdlL!! to it
                "胜 $w% · 和 ${s.wdlD}% · 负 $l%"
            }
        }
        val src = when {
            sug == null -> "来源 —"
            sug.fromBook -> "来源 开局库·${sug.bookName.ifEmpty { "桔库" }}"
            else -> "来源 引擎（深度${sug.depth}）"
        }
        view.render(DetailModel(
            score = label,
            scoreTone = tone,
            stateKind = stateKind,
            stateText = stateText,
            depth = depthV,
            nodes = nodesV,
            nps = npsV,
            hash = hashV,
            wdl = wdlText,
            source = src,
        ))
    }

    /** 数量格式化：M/K 单位缩写（节点/速率显示用，与电脑端控制台同口径） */
    private fun fmtCount(n: Long): String = when {
        n >= 10_000_000 -> "%.0fM".format(n / 1_000_000.0)
        n >= 1_000_000 -> "%.1fM".format(n / 1_000_000.0)
        n >= 1_000 -> "%.0fK".format(n / 1_000.0)
        n > 0 -> n.toString()
        else -> "—"
    }

    /** ucci(4字符) -> 内部 (x,y) 起止 */
    private fun ucciToCells(ucci: String): Pair<Pair<Int, Int>, Pair<Int, Int>> {
        if (ucci.length < 4) return Pair(0 to 0, 0 to 0)
        val fx = ucci[0] - 'a'
        val fy = 9 - (ucci[1] - '0')
        val tx = ucci[2] - 'a'
        val ty = 9 - (ucci[3] - '0')
        return Pair(fx to fy, tx to ty)
    }

    /**
     * 清理顺序契约：① releaseCapture 最先（摘 frameTick/elapsedTicker、停投影、
     * 断 WSS、撤通知；CLOSE/投影 onStop/本函数三处调用，判空保证重复调用幂等）；
     * ② 引擎 native 资源；③ 移两窗；④ 注销广播；⑤ workerThread.quitSafely()
     * 收尾——允许在途 handleFrame 自然结束、丢弃未到期消息。remote?.stop() 与
     * releaseCapture 内的停止重复是有意幂等（绑定续命下 onDestroy 可能不经
     * CLOSE 单独发生）。
     */
    override fun onDestroy() {
        RLog.i(TAG, "service destroyed")
        releaseCapture()
        yoloDetector?.close()
        yoloDetector = null
        onnxEngine?.release()
        onnxEngine = null
        remote?.stop()
        remote = null
        overlayPanel?.let { p ->
            windowManager?.removeView(p)
        }
        overlayPanel = null
        removeDetailWindow()
        runCatching { unregisterReceiver(screenStateReceiver) }
        workerThread.quitSafely()
        super.onDestroy()
    }

    private fun releaseCapture() {
        // A3：先递增代号（在途 run 尾部续期后、逃逸消息下次 run 时自检退出），
        // 再摘当前代 tick 的排队消息，两步合围保证旧代链干净终止（见 startFrameTickLoop）
        captureGen++
        frameTickRunner?.let { workerHandler.removeCallbacks(it) }
        frameTickRunner = null
        mainHandler.removeCallbacks(elapsedTicker)
        // matureRecheck 刻意不在此摘：pending 标志+体内早退自清（overlayClosed 时
        // 至多空转一次）。若对称地加 removeCallbacks(matureRecheck)，必须同时置
        // matureRecheckPending=false，否则下次启动 scheduleMatureRecheck 因残留
        // pending 永不再排队，建议成熟 ✓ 永不渲染
        captureStartedAtMs = 0L // 用时清零：状态行回 modeText，tick 停摆
        // 先摘回调再 stop，避免 onStop 递归触发收摊逻辑
        try { mediaProjection?.unregisterCallback(projectionCallback) } catch (e: Exception) { }
        try { virtualDisplay?.release() } catch (e: Exception) { }
        virtualDisplay = null
        try { imageReader?.close() } catch (e: Exception) { }
        imageReader = null
        try { mediaProjection?.stop() } catch (e: Exception) { }
        mediaProjection = null
        // 录屏停了连线即断：主页 Activity 的绑定会续命服务（stopSelf 后 onDestroy
        // 不一定执行），必须在此显式断开，否则 WSS 心跳与自动重连继续、PC 端仍显示在线
        remote?.stop()
        remote = null
        stopForeground(STOP_FOREGROUND_REMOVE)
    }

    private fun OverlayPanelView.dp(v: Int): Int =
        (v * resources.displayMetrics.density).toInt()

    private fun OverlayDetailView.dp(v: Int): Int =
        (v * resources.displayMetrics.density).toInt()
}
