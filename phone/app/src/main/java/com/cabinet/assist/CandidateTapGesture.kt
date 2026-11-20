package com.cabinet.assist

/**
 * 小棋盘连点手势状态机（纯 JVM 可单测；2026-11-15 用户定稿"连点 3 次"，
 * 2026-11-17 体验修复改逐击续窗）。
 *
 * 语义与击数一致：连点三下 = 触发"单次三变解析"（本轮搜索 multi_pv=3）。
 * 前两击照常立即上报换候选（点击零延迟优先，不加去抖等待），第三击改报
 * 触发且不再换候选。
 *
 * 窗口为**逐击续链**（2026-11-17 用户拍板）：每击与上一击间隔 ≤窗口即续链，
 * 不再从首击死算总长——原口径下节奏稍慢（如 600ms/击 ×3 总长 1.2s）永远
 * 打不出触发，且失败的末击伪装成"换了一下候选"（A/B 实测"点了没反应"）。
 * 链长天然 ≤3（第三击即触发并清零），总长 ≤2×窗口，不存在无界链。
 * 2026-11-20 窗口 1s→0.7s（用户拍板）：收紧连击窗，把"浏览候选"的常用节奏
 * （约 1s/击）从误触发区挪出——快速连点三变仍可达，慢节奏浏览不再误发。
 *
 * ⚠已知语义（说明书同口径）：以 <0.7s 节奏快速连点浏览候选同样会在第三击
 * 触发三变——第 2 击有"再点 1 次出三变"提示可停手；浏览候选请用"变招"
 * 按钮或放慢节奏。变招按钮按下/面板拖动等无关输入调 [reset] 清窗，防止
 * 把不相邻的点按误判成连点序列。
 */
class CandidateTapGesture(private val windowMs: Long = CANDIDATE_TAP_WINDOW_MS) {

    /** 单击动作：CYCLE=照常换候选；ONE_SHOT=三击到齐，触发单次三变解析 */
    enum class Action { CYCLE, ONE_SHOT }

    /** 序列内已击次数（CYCLE 返回时为 1..2，供面板进度提示；触发 ONE_SHOT 前已随 reset 清零） */
    var taps: Int = 0
        private set

    /** 上一击时刻（0 = 无在途序列） */
    private var lastTapAt = 0L

    /** 记录一击并返回本击动作 */
    fun onTap(now: Long): Action {
        if (lastTapAt == 0L || now - lastTapAt > windowMs) {
            // 超窗：本击成为新序列首击，照常换候选
            lastTapAt = now
            taps = 1
            return Action.CYCLE
        }
        lastTapAt = now
        taps++
        if (taps >= 3) {
            reset()
            return Action.ONE_SHOT
        }
        return Action.CYCLE
    }

    /** 清窗（变招按钮按下/面板拖动：与连点序列无关的输入打断） */
    fun reset() {
        lastTapAt = 0L
        taps = 0
    }

    companion object {
        /** 击间续链窗口：每击间隔 ≤0.7 秒（2026-11-17 改逐击续窗替换"距首击 1 秒"；
         *  2026-11-20 1s→0.7s 收紧，降低浏览候选误触发） */
        const val CANDIDATE_TAP_WINDOW_MS = 700L
    }
}
