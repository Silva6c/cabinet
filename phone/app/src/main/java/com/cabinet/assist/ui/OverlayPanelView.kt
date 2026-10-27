package com.cabinet.assist.ui

import android.content.Context
import android.graphics.drawable.GradientDrawable
import android.text.SpannableStringBuilder
import android.text.style.ForegroundColorSpan
import android.util.AttributeSet
import android.view.LayoutInflater
import android.view.MotionEvent
import android.view.View
import android.view.ViewConfiguration
import android.view.WindowManager
import android.widget.LinearLayout
import android.widget.TextView
import com.cabinet.R
import kotlin.math.abs

/** 状态语义档（颜色由当前主题调色板解析，见 QiPalette）：
 *  运行=玉 / 等待=琥珀 / 停止=灰 / 异常=警示红 / 手动=琥珀 */
enum class OverlayStateKind { RUNNING, WAITING, IDLE, ERROR, MANUAL }

/** 文本语义色调（优势行 / 候选行）：GOOD=玉、BAD=警示、WARN=琥珀、NONE=主文字 */
enum class QiTone { NONE, GOOD, BAD, WARN }

/**
 * 悬浮窗显示模型（结构化槽位，"一眼原则"）：
 * 顶部状态行（运行模式 · 用时）→ 主状态大字（轮到我方走/已停止/连接异常）
 * → 优势 → 候选/预案（尾部点阵），每格独立排版，不再是一整段多行小字。
 */
class OverlayModel(
    /** 顶部状态行：运行中 · 用时 1.2s（次级小字） */
    val stateText: String = "",
    val stateKind: OverlayStateKind = OverlayStateKind.IDLE,
    /** 遮挡提醒行：悬浮窗盖住已定位棋盘时显示；null 隐藏（独立小行，不挤占状态行） */
    val occludedText: String? = null,
    /** 主状态大字：轮到我方走 / 已停止指导 / 连接异常…（含 setStatus 覆盖文案） */
    val headlineText: String = "识别中",
    /** 优势行：红方优势 +0.8；null 隐藏 */
    val advantageText: String? = null,
    val advantageTone: QiTone = QiTone.NONE,
    /** 候选/预案行：候选 1/3 · 马三进二 ✓；null 隐藏 */
    val candidateText: String? = null,
    val candidateTone: QiTone = QiTone.NONE,
    /** 候选点阵（1 基序号与总数；总数 ≤ 1 不显示），折进候选行尾部 */
    val candidateIndex: Int = 0,
    val candidateCount: Int = 0,
    /** canonical 布局 90 子（红恒在 y=9），null 表示尚无局面 */
    val pieces: IntArray? = null,
    /** 推荐走法起止（canonical 内部坐标 x,y） */
    val arrowFrom: Pair<Int, Int>? = null,
    val arrowTo: Pair<Int, Int>? = null,
    /** 被识别屏幕红方在上（迷你棋盘随动：同样红在上显示） */
    val flipped: Boolean = false,
    val mySideIsRed: Boolean = true,
    /** 建议已"定着"（成熟）：箭头用绿色；false 为暂定（蓝箭头） */
    val mature: Boolean = false,
    /** 已停止指导（按钮显示"继续"） */
    val paused: Boolean = false,
    /** 紧急手动模式：小棋盘点按用于选子/走子/删子，而非切换候选 */
    val manualMode: Boolean = false,
    /** 手动模式下当前选中的格（canonical x,y），迷你棋盘绘制高亮 */
    val selectedCell: Pair<Int, Int>? = null,
    /** 引擎深度档值（"20"）；null = 未启用/未连接 */
    val depthText: String? = null,
    /** 引擎 Hash 档值（"0.25g"） */
    val hashText: String? = null,
    /** 变招路数档值（"2"；悬浮窗"路数"按钮文案，PC 端无回显——本地持久化值为准） */
    val multiPvText: String? = null,
)

enum class OverlayAction {
    /** 停止/继续指导 */
    PAUSE,
    /** 关闭连线（停止录取+移除面板） */
    CLOSE,
    /** 切换候选着法（变招） */
    CYCLE_CANDIDATE,
    /** 悔棋：回退到上一个已确认局面 */
    UNDO,
    /** 更新棋局（原"轮次"）：翻转"我方/对方"走子方并按新轮次重建/重分析 */
    FLIP_TURN,
    /** 紧急手动模式开关：识别失效时在小棋盘上手工维护局面 */
    MANUAL,
    /** 引擎深度档循环（set_engine 远程调算力，协议 §4.10） */
    CYCLE_DEPTH,
    /** 引擎 Hash 档循环 */
    CYCLE_HASH,
    /** 变招路数（引擎 MultiPV）1–3 循环：上行 set_engine 热改 PC 引擎，下一轮搜索生效 */
    CYCLE_MULTI_PV,
    /** 详情窗（窗口2）显隐切换 */
    TOGGLE_DETAIL,
}

class OverlayPanelView @JvmOverloads constructor(
    context: Context,
    attrs: AttributeSet? = null,
) : LinearLayout(context, attrs) {

    private val chess: OverlayChessView
    private val stateDot: View
    private val state: TextView
    private val occluded: TextView
    private val status: TextView
    private val advantage: TextView
    private val divider: View
    private val candidate: TextView
    private val btnPause: TextView
    private val btnClose: TextView
    private val btnDepth: TextView
    private val btnHash: TextView
    private val btnChange: TextView
    private val btnUndo: TextView
    private val btnManual: TextView
    private val btnTurn: TextView
    private val btnDetail: TextView
    private val btnMultiPv: TextView

    /** 真正的背景层 = XML 里的 overlay_root：悬浮窗 root 的 background 运行期
     *  替换不生效（首次之后静默失效，实测 TYPE_APPLICATION_OVERLAY 窗口行为），
     *  子 View 的背景可正常替换——换肤一律改这一层，root 自身保持透明 */
    private lateinit var bgLayer: View

    /** 当前主题调色板（applyTheme 注入；render 只按语义取色，不出现字面色值） */
    private var palette: QiPalette = OverlayTheme.DARK.palette

    /** 最近一次渲染模型：主题切换后按新调色板重绘动态色（点/点阵/芯片值） */
    private var lastModel: OverlayModel? = null

    var onAction: ((OverlayAction) -> Unit)? = null
    /** 手动模式：小棋盘点格（canonical x,y），由服务端处理选子/走子/删子 */
    var onCellTap: ((x: Int, y: Int) -> Unit)? = null
    /** 拖动开始/结束（用于抑制取帧时的面板隐藏动作） */
    var onDragStateChange: ((Boolean) -> Unit)? = null

    // 关闭二次确认：误触风险大，首次点按进入待确认态，3 秒内再点才真正关闭
    private var closeArmed = false
    private var closeNormalColor = 0
    private val closeReset = Runnable { resetCloseState() }

    private fun resetCloseState() {
        closeArmed = false
        btnClose.text = "关闭"
        btnClose.setTextColor(closeNormalColor)
    }

    private var wm: WindowManager? = null
    private var params: WindowManager.LayoutParams? = null
    private var onMove: (() -> Unit)? = null

    private val touchSlop = ViewConfiguration.get(context).scaledTouchSlop * 1.5f
    private var downX = 0f
    private var downY = 0f
    private var startX = 0
    private var startY = 0
    private var dragging = false

    init {
        LayoutInflater.from(context).inflate(R.layout.overlay_panel, this, true)
        bgLayer = findViewById(R.id.overlay_root)
        chess = findViewById(R.id.overlay_chess)
        stateDot = findViewById(R.id.overlay_state_dot)
        state = findViewById(R.id.overlay_state)
        occluded = findViewById(R.id.overlay_occluded)
        status = findViewById(R.id.overlay_status)
        advantage = findViewById(R.id.overlay_advantage)
        divider = findViewById(R.id.overlay_divider)
        candidate = findViewById(R.id.overlay_candidate)
        btnPause = findViewById(R.id.overlay_btn_pause)
        btnClose = findViewById(R.id.overlay_btn_close)
        btnDepth = findViewById(R.id.overlay_btn_strength)
        btnHash = findViewById(R.id.overlay_btn_hash)
        btnChange = findViewById(R.id.overlay_btn_change)
        btnUndo = findViewById(R.id.overlay_btn_undo)
        btnManual = findViewById(R.id.overlay_btn_manual)
        btnTurn = findViewById(R.id.overlay_btn_turn)
        btnDetail = findViewById(R.id.overlay_btn_detail)
        btnMultiPv = findViewById(R.id.overlay_btn_multipv)
        closeNormalColor = btnClose.currentTextColor

        btnPause.setOnClickListener { onAction?.invoke(OverlayAction.PAUSE) }
        btnChange.setOnClickListener { onAction?.invoke(OverlayAction.CYCLE_CANDIDATE) }
        btnUndo.setOnClickListener { onAction?.invoke(OverlayAction.UNDO) }
        btnManual.setOnClickListener { onAction?.invoke(OverlayAction.MANUAL) }
        btnTurn.setOnClickListener { onAction?.invoke(OverlayAction.FLIP_TURN) }
        btnDepth.setOnClickListener { onAction?.invoke(OverlayAction.CYCLE_DEPTH) }
        btnHash.setOnClickListener { onAction?.invoke(OverlayAction.CYCLE_HASH) }
        btnMultiPv.setOnClickListener { onAction?.invoke(OverlayAction.CYCLE_MULTI_PV) }
        btnDetail.setOnClickListener { onAction?.invoke(OverlayAction.TOGGLE_DETAIL) }
        btnClose.setOnClickListener {
            if (!closeArmed) {
                closeArmed = true
                btnClose.text = "确认?"  // 网格按钮 40dp 宽，确认文案需紧凑
                btnClose.setTextColor(palette.danger)
                postDelayed(closeReset, 3000)
            } else {
                removeCallbacks(closeReset)
                closeReset.run()
                onAction?.invoke(OverlayAction.CLOSE)
            }
        }
        chess.onNextCandidate = { onAction?.invoke(OverlayAction.CYCLE_CANDIDATE) }
        chess.onCellTap = { x, y -> onCellTap?.invoke(x, y) }
    }

    fun render(model: OverlayModel) {
        lastModel = model

        // 状态行：语义点 + 次级小字
        stateDot.background = dotDrawable(dotColor(model.stateKind), hollow = model.stateKind == OverlayStateKind.IDLE)
        state.text = model.stateText
        state.setTextColor(palette.muted)

        // 遮挡提醒行（琥珀小字，独立于状态行——移开棋盘即消失）
        val occ = model.occludedText
        if (occ.isNullOrEmpty()) {
            occluded.visibility = View.GONE
        } else {
            occluded.visibility = View.VISIBLE
            occluded.text = occ
            occluded.setTextColor(palette.amber)
        }

        // 主状态大字
        status.text = model.headlineText
        status.setTextColor(palette.headline)

        // 优势行
        val adv = model.advantageText
        if (adv.isNullOrEmpty()) {
            advantage.visibility = View.GONE
        } else {
            advantage.visibility = View.VISIBLE
            advantage.text = adv
            advantage.setTextColor(toneColor(model.advantageTone, palette.jade))
        }

        // 候选/预案行（点阵折进尾部：当前候选=琥珀实点，其余=弱化点）
        val cand = model.candidateText
        if (cand.isNullOrEmpty()) {
            candidate.visibility = View.GONE
        } else {
            candidate.visibility = View.VISIBLE
            val sb = SpannableStringBuilder(cand)
            sb.setSpan(
                ForegroundColorSpan(toneColor(model.candidateTone, palette.fg)),
                0, sb.length, 0,
            )
            if (model.candidateCount in 2..9) {
                sb.append("  ")
                for (i in 1..model.candidateCount) {
                    val start = sb.length
                    sb.append("●")
                    sb.setSpan(
                        ForegroundColorSpan(if (i == model.candidateIndex) palette.amber else palette.dim),
                        start, sb.length, 0,
                    )
                    if (i != model.candidateCount) sb.append(" ")
                }
            }
            candidate.text = sb
        }

        // 值芯片：标签灰 + 值琥珀（等宽由 tnum 保证）
        btnDepth.text = chipText("深度", model.depthText)
        btnHash.text = chipText("哈希", model.hashText)
        btnMultiPv.text = chipText("路数", model.multiPvText)
        btnPause.text = if (model.paused) "继续" else "停止"

        chess.pieces = model.pieces
        chess.arrowFrom = model.arrowFrom
        chess.arrowTo = model.arrowTo
        chess.flipped = model.flipped
        chess.mySideIsRed = model.mySideIsRed
        chess.mature = model.mature
        chess.manualMode = model.manualMode
        chess.selectedCell = model.selectedCell
    }

    /**
     * 全区域拖动：按住面板任意位置移动即可拖动（按钮点击不受影响——小于滑动阈值的
     * 点按会正常分发为点击）。[onMove] 在拖动结束时调用（用于持久化位置）。
     */
    fun setDragHandle(params: WindowManager.LayoutParams, wm: WindowManager, onMove: () -> Unit) {
        this.params = params
        this.wm = wm
        this.onMove = onMove
    }

    /**
     * 应用背景主题（"更换背景"设置）：背景层是 XML 的 overlay_root 子 View——悬浮窗
     * root 的 background 运行时替换不生效，子 View 背景可正常替换。
     * 换肤 = 换一整块语义：底/描边/芯片/按钮材质 + 全部语义文字色，最后按新调色板
     * 重绘一遍当前模型（动态色如状态点、点阵、芯片值随主题同步）。
     */
    fun applyTheme(theme: OverlayTheme) {
        palette = theme.palette
        bgLayer.background = theme.background(context)

        state.setTextColor(palette.muted)
        occluded.setTextColor(palette.amber)
        status.setTextColor(palette.headline)
        candidate.setTextColor(palette.fg)
        advantage.setTextColor(palette.jade) // render 会按语义覆盖
        divider.setBackgroundColor(palette.divider)

        // 值芯片（每个 view 各取一份 Drawable 实例，禁止共享）
        btnDepth.background = theme.chipBackground(context)
        btnHash.background = theme.chipBackground(context)

        // 动作按钮：停止 = 琥珀警示描边（唯一带语义描边的动作）
        btnPause.background = theme.actionBackground(context, palette.stopStroke, palette.stopStrokePressed)
        for (v in listOf(btnChange, btnUndo, btnManual, btnMultiPv, btnTurn, btnClose, btnDetail)) {
            v.background = theme.actionBackground(context)
            v.setTextColor(palette.fg)
        }

        // 关闭二次确认可能正处于待确认态：按新主题保留状态
        closeNormalColor = palette.fg
        if (closeArmed) btnClose.setTextColor(palette.danger) else btnClose.setTextColor(closeNormalColor)

        lastModel?.let { render(it) }
    }

    /** 语义色调 → 当前主题色（NONE = 主文字） */
    private fun toneColor(tone: QiTone, noneColor: Int): Int = when (tone) {
        QiTone.GOOD -> palette.jade
        QiTone.BAD -> palette.danger
        QiTone.WARN -> palette.amber
        QiTone.NONE -> noneColor
    }

    private fun dotColor(kind: OverlayStateKind): Int = when (kind) {
        OverlayStateKind.RUNNING -> palette.jadeDot
        OverlayStateKind.WAITING, OverlayStateKind.MANUAL -> palette.amber
        OverlayStateKind.ERROR -> palette.danger
        OverlayStateKind.IDLE -> palette.dim
    }

    /** 状态点：实心或空心圆（空心 = 停止/未启动的弱化语义） */
    private fun dotDrawable(color: Int, hollow: Boolean): GradientDrawable {
        val d = GradientDrawable()
        d.shape = GradientDrawable.OVAL
        if (hollow) {
            d.setColor(0x00000000)
            d.setStroke((1.5f * resources.displayMetrics.density).toInt(), color)
        } else {
            d.setColor(color)
        }
        return d
    }

    /** 值芯片双色文本："深度" 灰 + 值 琥珀（未连接时值弱化） */
    private fun chipText(label: String, value: String?): CharSequence {
        val sb = SpannableStringBuilder(label)
        sb.setSpan(ForegroundColorSpan(palette.muted), 0, sb.length, 0)
        sb.append(" ")
        val vs = sb.length
        sb.append(value ?: "—")
        sb.setSpan(
            ForegroundColorSpan(if (value != null) palette.amber else palette.dim),
            vs, sb.length, 0,
        )
        return sb
    }

    /** MOVE 超过阈值后拦截事件，交给 onTouchEvent 移动整个窗口。
     *  拦截链契约：DOWN 必须返回 false 放行，否则按钮/小棋盘收不到事件；MOVE
     *  超 slop 抢一次（true），此后逐帧返回 dragging 维持拦截，子 View 收到
     *  CANCEL 自然不会误触发点击，未超阈值的点按仍正常走点击（拖动与点击
     *  共存的关键）。位移一律用 rawX/rawY——startX/startY 记的是窗口
     *  params.x/y（屏幕坐标），与视图本地 ev.x 基准不同，混用会漂移。
     *  为何必须拦截而非 onTouchEvent 直收：见 OverlayDetailView 同函数的实测注释。 */
    override fun onInterceptTouchEvent(ev: MotionEvent): Boolean {
        when (ev.actionMasked) {
            MotionEvent.ACTION_DOWN -> {
                downX = ev.rawX
                downY = ev.rawY
                startX = params?.x ?: 0
                startY = params?.y ?: 0
                dragging = false
                return false
            }
            MotionEvent.ACTION_MOVE -> {
                if (!dragging && (abs(ev.rawX - downX) > touchSlop || abs(ev.rawY - downY) > touchSlop)) {
                    dragging = true
                    onDragStateChange?.invoke(true)
                    return true
                }
                return dragging
            }
        }
        return false
    }

    @Suppress("ClickableViewAccessibility")
    override fun onTouchEvent(ev: MotionEvent): Boolean {
        if (ev.actionMasked == MotionEvent.ACTION_MOVE) {
            val p = params
            if (dragging && p != null) {
                p.x = startX + (ev.rawX - downX).toInt()
                p.y = startY + (ev.rawY - downY).toInt()
                wm?.updateViewLayout(this, p)
                return true
            }
        } else if (ev.actionMasked == MotionEvent.ACTION_UP || ev.actionMasked == MotionEvent.ACTION_CANCEL) {
            if (dragging) {
                dragging = false
                onDragStateChange?.invoke(false)
                onMove?.invoke()
                return true
            }
        }
        return super.onTouchEvent(ev)
    }
}
