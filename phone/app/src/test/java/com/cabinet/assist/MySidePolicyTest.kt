package com.cabinet.assist

import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * 我方执色策略单测：纯随动判定（下端子色=我方，无任何覆盖机制）。
 */
class MySidePolicyTest {

    @Test
    fun `红在下STANDARD判我方执红`() {
        val p = MySidePolicy()
        assertTrue(p.mySideIsRed(orientationStandard = true))
    }

    @Test
    fun `红在上FLIPPED判我方执黑`() {
        val p = MySidePolicy()
        assertFalse(p.mySideIsRed(orientationStandard = false))
    }

    @Test
    fun `朝向翻转执色立即跟随_无残留`() {
        val p = MySidePolicy()
        // 棋盘物理翻转（对局 App 换边翻盘）：执色即刻跟随新朝向
        assertTrue(p.mySideIsRed(orientationStandard = true))
        assertFalse(p.mySideIsRed(orientationStandard = false))
        assertTrue(p.mySideIsRed(orientationStandard = true))
    }
}
