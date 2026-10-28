package com.cabinet.assist.ui

import android.content.Context
import android.graphics.drawable.GradientDrawable
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

/**
 * 详情窗显示模型：三行高密度结构（overlay_detail.xml 2026-10-18 重构）——
 * 行1 优势大字（左）+ 引擎状态·用时（右）；行2 指标四项一行均布
 * （深度/节点/速率/哈希）；行3 建议来源 + 可选胜率 WDL（右对齐）。
 * 色由主题调色板解析。
 */
class DetailModel(
    /** 优势大字（红优 +0.8 / 黑方 3 步杀）；null = 无分数显示占位灰 */
    val score: String? = null,
    val scoreTone: QiTone = QiTone.NONE,
    val stateKind: OverlayStateKind = OverlayStateKind.IDLE,
    /** 状态行：引擎运行中 · 用时 1.2s */
    val stateText: String = "",
    val depth: String = "—",
    val nodes: String = "—",
    val nps: String = "—",
    val hash: String = "—",
    /** 胜率行（红方视角 胜/和/负）；null 隐藏 */
    val wdl: String? = null,
    val source: String = "来源 —",
)

/**
 * 悬浮窗窗口2（详情）：优势提示大字（红优/黑优，按我方视角着色）
 * + 电脑引擎状态与实时指标（status 帧）+ 建议来源。全区域可拖动。
 *
 * 数据同步契约：本窗无独立数据通道，每次主面板刷新（renderOverlay 尾部的
 * updateDetailWindow）都从服务端同一份快照（fen/我方色/最新建议/引擎指标）
 * 全量派生 DetailModel 重渲染；视图内不得缓存跨帧业务状态——lastModel
 * 仅用于换肤重绘，不是数据源。
 */
class OverlayDetailView @JvmOverloads constructor(
    context: Context,
    attrs: AttributeSet? = null,
) : LinearLayout(context, attrs) {

    private val score: TextView
    private val dot: View
    private val state: TextView
    private val labels: List<TextView>
    private val values: List<TextView>
    private val wdl: TextView
    private val source: TextView

    /** 真正的背景层 = XML 里的 detail_root（悬浮窗 root 背景运行期替换不生效，见
     *  OverlayPanelView.bgLayer 注释） */
    private lateinit var bgLayer: View

    /** 当前主题调色板（applyTheme 注入）与最近一次模型（换肤后重绘） */
    private var palette: QiPalette = OverlayTheme.DARK.palette
    private var lastModel: DetailModel? = null

    private var wm: WindowManager? = null
    private var params: WindowManager.LayoutParams? = null
    private var onMove: (() -> Unit)? = null
    /** 拖动状态回调（服务端据此抑制识别管线取帧，避免拖动中悬浮窗被隐藏闪烁） */
    var onDragStateChange: ((Boolean) -> Unit)? = null
    private var downX = 0f
    private var downY = 0f
    private var startX = 0
    private var startY = 0
    private var dragging = false
    private val touchSlop = ViewConfiguration.get(context).scaledTouchSlop

    init {
        LayoutInflater.from(context).inflate(R.layout.overlay_detail, this, true)
        bgLayer = findViewById(R.id.detail_root)
        score = findViewById(R.id.detail_score)
        dot = findViewById(R.id.detail_dot)
        state = findViewById(R.id.detail_state)
        labels = listOf(
            findViewById(R.id.detail_l_depth),
            findViewById(R.id.detail_l_nodes),
            findViewById(R.id.detail_l_nps),
            findViewById(R.id.detail_l_hash),
        )
        values = listOf(
            findViewById(R.id.detail_v_depth),
            findViewById(R.id.detail_v_nodes),
            findViewById(R.id.detail_v_nps),
            findViewById(R.id.detail_v_hash),
        )
        wdl = findViewById(R.id.detail_wdl)
        source = findViewById(R.id.detail_source)
    }

    fun render(model: DetailModel) {
        lastModel = model

        score.text = model.score ?: "—"
        score.setTextColor(
            when (model.scoreTone) {
                QiTone.GOOD -> palette.jade
                QiTone.BAD -> palette.danger
                else -> palette.muted
            }
        )

        dot.background = dotDrawable(dotColor(model.stateKind))
        state.text = model.stateText
        state.setTextColor(palette.muted)

        values[0].text = model.depth
        values[1].text = model.nodes
        values[2].text = model.nps
        values[3].text = model.hash
        for (v in values) v.setTextColor(palette.amber)

        val w = model.wdl
        if (w.isNullOrEmpty()) {
            wdl.visibility = View.GONE
        } else {
            wdl.visibility = View.VISIBLE
            wdl.text = w
            wdl.setTextColor(palette.muted)
        }
        source.text = model.source
        source.setTextColor(palette.muted)
    }

    /** 拖动支持：与主面板同款（拖动结束回调 onMove 持久化位置） */
    fun setDragHandle(params: WindowManager.LayoutParams, wm: WindowManager, onMove: () -> Unit) {
        this.params = params
        this.wm = wm
        this.onMove = onMove
    }

    /** 应用背景主题（背景层子 View detail_root，原因见 OverlayPanelView.bgLayer）：
     *  静态标签色也随主题走，最后按当前模型重绘一遍动态色（大字/点/值）。 */
    fun applyTheme(theme: OverlayTheme) {
        palette = theme.palette
        bgLayer.background = theme.background(context)
        for (l in labels) l.setTextColor(palette.muted)
        state.setTextColor(palette.muted)
        source.setTextColor(palette.muted)
        wdl.setTextColor(palette.muted)
        for (v in values) v.setTextColor(palette.amber)
        lastModel?.let { render(it) }
    }

    private fun dotColor(kind: OverlayStateKind): Int = when (kind) {
        OverlayStateKind.RUNNING -> palette.jadeDot
        OverlayStateKind.WAITING, OverlayStateKind.MANUAL -> palette.amber
        OverlayStateKind.ERROR -> palette.danger
        OverlayStateKind.IDLE -> palette.dim
    }

    private fun dotDrawable(color: Int): GradientDrawable =
        GradientDrawable().apply {
            shape = GradientDrawable.OVAL
            setColor(color)
        }

    /** MOVE 超过阈值后拦截事件，交给 onTouchEvent 移动整个窗口。
     *  必须拦截：布局根节点 clickable=true 会消费全部触摸，父容器 onTouchEvent
     *  永远收不到 DOWN 之后的序列（实测拖不动的根因），与主面板同款机制。 */
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
