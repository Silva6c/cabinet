package com.cabinet.assist

import org.junit.Assert.assertEquals
import org.junit.Test

/** 悬浮窗档位循环纯函数（深度/Hash/变招路数三按钮共用） */
class OverlayStepCycleTest {

    @Test
    fun `档内循环且尾档回首档`() {
        val steps = intArrayOf(1, 2, 3)
        assertEquals(2, ScreenAssistService.nextStepValue(steps, 1))
        assertEquals(3, ScreenAssistService.nextStepValue(steps, 2))
        assertEquals(1, ScreenAssistService.nextStepValue(steps, 3))
    }

    @Test
    fun `深度档在档内走下一档`() {
        val steps = intArrayOf(12, 16, 20, 24, 28, 32, 36, 40)
        assertEquals(24, ScreenAssistService.nextStepValue(steps, 20))
        assertEquals(40, ScreenAssistService.nextStepValue(steps, 36))
        assertEquals(12, ScreenAssistService.nextStepValue(steps, 40))
    }

    @Test
    fun `旧存档值不在档内时迁移到更大的档`() {
        // 旧版本存过 13/21 这类非档值：13 跳到第一个更大的档 16；
        // 21 已大于全部档位 → 回到最低档循环
        assertEquals(16, ScreenAssistService.nextStepValue(intArrayOf(12, 16, 20), 13))
        assertEquals(12, ScreenAssistService.nextStepValue(intArrayOf(12, 16, 20), 21))
    }

    @Test
    fun `档外值小于最小档或大于最大档的兜底`() {
        // 比最小档还小 → 迁移到第一个更大的档（即最小档）
        assertEquals(12, ScreenAssistService.nextStepValue(intArrayOf(12, 16, 20), 11))
        // 比最大档还大 → 回到最低档循环回来
        assertEquals(12, ScreenAssistService.nextStepValue(intArrayOf(12, 16, 20), 41))
    }
}

/** 思考单芯片（2026-10-28 重设计：短按模式内循环档位 + 长按模式切换） */
class OverlayThinkChipTest {

    private val timeSteps = intArrayOf(0, 1000, 3000, 5000, 10000, 20000)
    private val depthSteps = intArrayOf(12, 16, 20, 24, 28, 32, 36, 40)

    @Test
    fun `时间制内短按循环且尾档回首档`() {
        assertEquals(1000, ScreenAssistService.nextStepValue(timeSteps, 0))
        assertEquals(5000, ScreenAssistService.nextStepValue(timeSteps, 3000))
        assertEquals(0, ScreenAssistService.nextStepValue(timeSteps, 20000))
    }

    @Test
    fun `深度制内短按循环不变`() {
        assertEquals(24, ScreenAssistService.nextStepValue(depthSteps, 20))
        assertEquals(12, ScreenAssistService.nextStepValue(depthSteps, 40))
    }

    @Test
    fun `旧存档非档值迁移`() {
        assertEquals(1000, ScreenAssistService.nextStepValue(timeSteps, 800))
        assertEquals(3000, ScreenAssistService.nextStepValue(timeSteps, 2000))
    }

    @Test
    fun `芯片文案按现行模式自明`() {
        assertEquals("不限", ScreenAssistService.moveTimeChipText(0))
        assertEquals("3秒", ScreenAssistService.moveTimeChipText(3000))
        assertEquals("时间3秒", ScreenAssistService.thinkChipText(true, 20, 3000))
        assertEquals("深度20", ScreenAssistService.thinkChipText(false, 20, 0))
    }
}
