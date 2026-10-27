package com.cabinet.assist.ui

import android.content.Context
import android.graphics.drawable.Drawable
import android.graphics.drawable.GradientDrawable
import android.graphics.drawable.LayerDrawable
import android.graphics.drawable.StateListDrawable

/**
 * 棋韵浮窗调色板（docs/美学设计研究.md §4.2 / §5.2）：
 * 三档主题各持一套"语义色 + 材质"（底/描边/芯片/按钮），视图侧只按语义取色、
 * 不出现字面色值——换肤即换一整块语义。
 *
 * 伪玻璃三件套：半透明底（72%）＋ 1dp 高光描边 ＋ 顶部微渐变。
 * 真背景模糊在物理上不可得：Android 悬浮窗（FLAG_SECURE ＋ 覆盖他人应用）
 * 无法采样身后内容，这里不做假。
 */
class QiPalette(
    /** 面板玻璃底（含透明度） */
    val panel: Int,
    val panelStroke: Int,
    /** 顶部微渐变起始白（0 = 不加高光，纯黑档不发光） */
    val panelTopHighlight: Int,
    /** 文字三级：主 / 次级 / 弱化 */
    val fg: Int,
    val muted: Int,
    val dim: Int,
    /** 主状态大字（琥珀系，浅色档自动加深） */
    val headline: Int,
    /** 优势玉字与状态点 */
    val jade: Int,
    val jadeDot: Int,
    val danger: Int,
    val amber: Int,
    /** 值芯片（深度/哈希）：无描边、6% 白底 */
    val chipBg: Int,
    val chipBgPressed: Int,
    /** 动作按钮：1dp 描边半透明底 */
    val actionBg: Int,
    val actionBgPressed: Int,
    val actionStroke: Int,
    val actionStrokePressed: Int,
    /** 停止按钮的琥珀警示描边 */
    val stopStroke: Int,
    val stopStrokePressed: Int,
    /** 细分割线 */
    val divider: Int,
)

/** 悬浮窗主题（"更换背景"设置，连线页选择）：深色玻璃 / 纯黑 OLED / 浅色纸感 */
enum class OverlayTheme(val key: String, val palette: QiPalette) {

    /** 深色玻璃（默认）：通用档，半透明 72% + 暖白高光 */
    DARK("overlay_dark", QiPalette(
        panel = 0xB81B1E24.toInt(),
        panelStroke = 0x24FFFFFF, panelTopHighlight = 0x12FFFFFF,
        fg = 0xFFE8E6E1.toInt(), muted = 0xFF9A9EA6.toInt(), dim = 0xFF565B64.toInt(),
        headline = 0xFFFFE082.toInt(), jade = 0xFF81C784.toInt(), jadeDot = 0xFF4CAF50.toInt(),
        danger = 0xFFFF8A80.toInt(), amber = 0xFFFFC107.toInt(),
        chipBg = 0x0FFFFFFF, chipBgPressed = 0x1CFFFFFF,
        actionBg = 0x1AFFFFFF, actionBgPressed = 0x29FFFFFF,
        actionStroke = 0x29FFFFFF, actionStrokePressed = 0x47FFFFFF,
        stopStroke = 0xB3FFC107.toInt(), stopStrokePressed = 0xFFFFC107.toInt(),
        divider = 0x1AFFFFFF,
    )),

    /** 纯黑 OLED：夜间省电档，黑底不发光（无高光渐变） */
    BLACK("overlay_black", QiPalette(
        panel = 0xF2000000.toInt(),
        panelStroke = 0x17FFFFFF, panelTopHighlight = 0,
        fg = 0xFFE8E6E1.toInt(), muted = 0xFF9A9EA6.toInt(), dim = 0xFF565B64.toInt(),
        headline = 0xFFFFE082.toInt(), jade = 0xFF81C784.toInt(), jadeDot = 0xFF4CAF50.toInt(),
        danger = 0xFFFF8A80.toInt(), amber = 0xFFFFC107.toInt(),
        chipBg = 0x0FFFFFFF, chipBgPressed = 0x1CFFFFFF,
        actionBg = 0x14FFFFFF, actionBgPressed = 0x24FFFFFF,
        actionStroke = 0x24FFFFFF, actionStrokePressed = 0x3DFFFFFF,
        stopStroke = 0xB3FFC107.toInt(), stopStrokePressed = 0xFFFFC107.toInt(),
        divider = 0x17FFFFFF,
    )),

    /** 浅色纸感：强光环境护眼档，文字整体加深保证对比 */
    LIGHT("overlay_light", QiPalette(
        panel = 0xE6ECEFF1.toInt(),
        panelStroke = 0x1F000000, panelTopHighlight = 0x1AFFFFFF,
        fg = 0xFF263238.toInt(), muted = 0xFF546E7A.toInt(), dim = 0xFF78909C.toInt(),
        headline = 0xFF9A5B00.toInt(), jade = 0xFF1B6B32.toInt(), jadeDot = 0xFF2E7D32.toInt(),
        danger = 0xFFC62828.toInt(), amber = 0xFF9A5B00.toInt(),
        chipBg = 0x12000000, chipBgPressed = 0x1F000000,
        actionBg = 0x14000000, actionBgPressed = 0x20000000,
        actionStroke = 0x24000000, actionStrokePressed = 0x38000000,
        stopStroke = 0x999A5B00.toInt(), stopStrokePressed = 0xFF9A5B00.toInt(),
        divider = 0x1F000000,
    ));

    /** 面板/详情窗背景：玻璃底 + 上沿描边 +（深色/浅色档）顶部微渐变 */
    fun background(ctx: Context): Drawable {
        val r = 20f * ctx.resources.displayMetrics.density
        val base = GradientDrawable().apply {
            shape = GradientDrawable.RECTANGLE
            cornerRadius = r
            setColor(palette.panel)
            setStroke(dp(ctx, 1f), palette.panelStroke)
        }
        if (palette.panelTopHighlight == 0) return base
        // 三段停靠：高光在前 50% 渐隐（近似视觉稿的 42% 渐隐点）
        val top = GradientDrawable(
            GradientDrawable.Orientation.TOP_BOTTOM,
            intArrayOf(palette.panelTopHighlight, 0x00FFFFFF, 0x00FFFFFF),
        ).apply { cornerRadius = r }
        return LayerDrawable(arrayOf(base, top))
    }

    /** 值芯片底（深度/哈希）：每个 view 各取一份实例，禁止共享 Drawable */
    fun chipBackground(ctx: Context): Drawable = pressable(
        shape(ctx, 6f, palette.chipBg, 0, 0),
        shape(ctx, 6f, palette.chipBgPressed, 0, 0),
    )

    /** 动作按钮底：默认描边或指定描边（停止按钮传 stopStroke 得到琥珀警示边） */
    fun actionBackground(
        ctx: Context,
        stroke: Int = palette.actionStroke,
        strokePressed: Int = palette.actionStrokePressed,
    ): Drawable = pressable(
        shape(ctx, 6f, palette.actionBg, stroke, dp(ctx, 1f)),
        shape(ctx, 6f, palette.actionBgPressed, strokePressed, dp(ctx, 1f)),
    )

    private fun shape(ctx: Context, radiusDp: Float, fill: Int, stroke: Int, strokePx: Int): GradientDrawable =
        GradientDrawable().apply {
            shape = GradientDrawable.RECTANGLE
            cornerRadius = radiusDp * ctx.resources.displayMetrics.density
            setColor(fill)
            if (strokePx > 0) setStroke(strokePx, stroke)
        }

    private fun pressable(normal: Drawable, pressed: Drawable): Drawable = StateListDrawable().apply {
        addState(intArrayOf(android.R.attr.state_pressed), pressed)
        addState(intArrayOf(), normal)
    }

    private fun dp(ctx: Context, v: Float): Int = (v * ctx.resources.displayMetrics.density).toInt()

    companion object {
        fun fromKey(key: String): OverlayTheme =
            entries.firstOrNull { it.key == key } ?: DARK
    }
}
