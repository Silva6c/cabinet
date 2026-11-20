package com.cabinet.assist

import com.cabinet.gamelogic.Piece
import org.junit.Assert.assertTrue
import org.junit.Assert.assertFalse
import org.junit.Test

/**
 * validate 兵可达性（2026-11-14 红队第二轮，与 PC fenlegal.go 双端同步）与
 * isStartLike 红车错位容错死分支修正的回归钉。
 */
class AssistBoardValidateTest {

    /** 开局局面复本 */
    private fun start(): Array<IntArray> = Array(AssistBoard.H) { y ->
        IntArray(AssistBoard.W) { x -> AssistBoard.canonicalStart()[y][x] }
    }

    // ── 兵可达性 ──

    @Test
    fun `开局与真实过河兵不报可达性`() {
        // 开局本身恒合法（红兵 y=6 偶列、黑卒 y=3 偶列）
        assertTrue(AssistBoard.validate(start()).isEmpty())
        // 红兵过河任意列（y≤4）：开局红兵 y=6 x=4 前进到 y=4
        val crossed = start()
        crossed[6][4] = Piece.EMPTY
        crossed[4][4] = Piece.WBING
        assertTrue(AssistBoard.validate(crossed).none { it.contains("不可达") })
    }

    @Test
    fun `红兵倒摆起始线之后_不可达`() {
        val b = start()
        b[6][4] = Piece.EMPTY
        b[8][4] = Piece.WBING // y=8：起始线 y=6 之后（皮卡鱼实测 CRITICAL 形态）
        assertTrue(AssistBoard.validate(b).any { it.contains("红兵位置不可达") })
    }

    @Test
    fun `红兵未过河奇数列_不可达`() {
        val b = start()
        b[6][4] = Piece.EMPTY
        b[5][3] = Piece.WBING // y=5 未过河、x=3 奇列
        assertTrue(AssistBoard.validate(b).any { it.contains("红兵位置不可达") })
    }

    @Test
    fun `黑卒镜像对称_不可达`() {
        val b = start()
        b[3][0] = Piece.EMPTY
        b[1][0] = Piece.BZU // y=1：黑卒起始线 y=3 之后（y≤2）
        assertTrue(AssistBoard.validate(b).any { it.contains("黑卒位置不可达") })
        val b2 = start()
        b2[3][0] = Piece.EMPTY
        b2[4][1] = Piece.BZU // y=4 未过河、x=1 奇列
        assertTrue(AssistBoard.validate(b2).any { it.contains("黑卒位置不可达") })
    }

    // ── isStartLike 红车错位容错（死分支修正回归） ──

    @Test
    fun `红车格被识别成红马_判新局`() {
        // 历史遗留错位模式：红车格（y=9 的 x=8/x=0）被识别成红马——旧判据写死
        // 差异格 x=7 且要求该格为红马，与"该格有差异"恒矛盾（死分支），容错
        // 从未生效过
        val b = start()
        b[9][8] = Piece.WMA
        assertTrue(AssistBoard.isStartLike(b))
        val b2 = start()
        b2[9][0] = Piece.WMA
        assertTrue(AssistBoard.isStartLike(b2))
    }

    @Test
    fun `红车平移吃自己兵位_不判新局`() {
        // 两格差（车离位+兵被覆盖），计数 30：非"恰一格差异"形态
        val b = start()
        b[9][8] = Piece.EMPTY
        b[6][8] = Piece.WJU
        assertFalse(AssistBoard.isStartLike(b))
    }

    @Test
    fun `黑马格被识别成红车_不判新局`() {
        // 差异格是红马位（x=7）：不在容错白名单（历史错位模式只有红车→红马）
        val b = start()
        b[9][7] = Piece.WJU
        assertFalse(AssistBoard.isStartLike(b))
    }
}
