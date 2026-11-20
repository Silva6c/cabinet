package com.cabinet.assist

import org.junit.Assert.assertEquals
import org.junit.Test

/**
 * 小棋盘连点手势状态机单测（纯 JVM，2026-11-15 用户定稿"连点 3 次"；
 * 2026-11-17 体验修复改**逐击续窗**：每击间隔 ≤窗口续链，不再从首击死算总长
 * ——原口径下 600ms/击 ×3（总长 1.2s）永远打不出，失败的末击伪装成换候选；
 * 2026-11-20 窗口 1s→0.7s 收紧，降低浏览候选误触发）。
 *
 * 语义契约：前两击照常上报换候选（点击零延迟优先），第三击改报触发且不再
 * 换候选；与上一击间隔超 0.7s 重新起算；变招按钮/拖动 reset 打断序列。
 */
class CandidateTapGestureTest {

    private val g = CandidateTapGesture()

    /** 手工推进的时刻轴（模拟 elapsedRealtime 单调时钟） */
    private var now = 10_000L

    private fun tap(): CandidateTapGesture.Action = g.onTap(now)

    @Test
    fun `三次连点触发单次三变_前两击照常换候选`() {
        assertEquals(CandidateTapGesture.Action.CYCLE, tap())
        now += 200
        assertEquals(CandidateTapGesture.Action.CYCLE, tap())
        now += 200
        assertEquals(CandidateTapGesture.Action.ONE_SHOT, tap())
    }

    @Test
    fun `逐击续链_总时长超1秒仍触发`() {
        // 600ms/击的稍慢节奏（击间隔 ≤700ms 逐击续链，总长 1.2s > 旧口径
        // "距首击 1 秒"总窗）：逐击续窗下照常触发
        assertEquals(CandidateTapGesture.Action.CYCLE, tap())
        now += 600
        assertEquals(CandidateTapGesture.Action.CYCLE, tap())
        now += 600
        assertEquals(CandidateTapGesture.Action.ONE_SHOT, tap())
    }

    @Test
    fun `触发后序列归零_重新三击才能再触发`() {
        tap(); now += 100
        tap(); now += 100
        assertEquals(CandidateTapGesture.Action.ONE_SHOT, tap())
        // 第 4 击 = 新序列首击（照常换候选）
        now += 100
        assertEquals(CandidateTapGesture.Action.CYCLE, tap())
        now += 100
        assertEquals(CandidateTapGesture.Action.CYCLE, tap())
        now += 100
        assertEquals(CandidateTapGesture.Action.ONE_SHOT, tap())
    }

    @Test
    fun `与上一击间隔超窗_重新起算不触发`() {
        assertEquals(CandidateTapGesture.Action.CYCLE, tap())
        now += 500
        assertEquals(CandidateTapGesture.Action.CYCLE, tap())
        now += 1100 // 距上一击 1100ms > 700ms：第三击成为新序列首击
        assertEquals(CandidateTapGesture.Action.CYCLE, tap())
    }

    @Test
    fun `窗口边界_恰好0点7秒内算同序列`() {
        assertEquals(CandidateTapGesture.Action.CYCLE, tap())
        now += 500
        assertEquals(CandidateTapGesture.Action.CYCLE, tap())
        now += 699 // 距上一击 699ms ≤ 700ms：仍在序列内
        assertEquals(CandidateTapGesture.Action.ONE_SHOT, tap())
    }

    @Test
    fun `reset打断连点序列_变招按钮与拖动清窗`() {
        tap(); now += 300
        g.reset()
        // reset 后下一击是新序列首击
        assertEquals(CandidateTapGesture.Action.CYCLE, tap())
        now += 100
        assertEquals(CandidateTapGesture.Action.CYCLE, tap())
        now += 100
        assertEquals(CandidateTapGesture.Action.ONE_SHOT, tap())
    }

    @Test
    fun `击数暴露_供面板进度提示`() {
        assertEquals(CandidateTapGesture.Action.CYCLE, tap())
        assertEquals(1, g.taps)
        now += 300
        assertEquals(CandidateTapGesture.Action.CYCLE, tap())
        assertEquals(2, g.taps)
        now += 300
        assertEquals(CandidateTapGesture.Action.ONE_SHOT, tap())
        // 触发即清零（ONE_SHOT 返回时序列已重置）
        assertEquals(0, g.taps)
    }
}
