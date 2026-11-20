package com.cabinet.assist

import com.cabinet.gamelogic.Piece
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Test

/** 回合结构防线判据单测（2026-11-10 优势崩塌归因，真实故障形状回放；
 *  与 Go 端 TestPlausiblePair 同源对表——手机端为无侧并集判据） */
class AssistBoardPlausiblePairTest {

    private fun base(): Array<IntArray> = AssistBoard.canonicalStart()

    @Test
    fun `吃自家子形状被拒_幻影帧穿帮点`() {
        // 黑车 (3,1) 消失、黑炮位 (3,3) 变黑车：隐含走子=黑车吃自家炮 → 拒
        val old = base()
        old[1][3] = Piece.BJU
        old[3][3] = Piece.BPAO
        val new = AssistBoard.clone(old)
        new[1][3] = Piece.EMPTY
        new[3][3] = Piece.BJU
        assertNotNull(AssistBoard.plausiblePair(old, new))
    }

    @Test
    fun `非吃子与吃子与悔棋还原全部通过`() {
        // 非吃子：黑车 (3,1)→(3,2)（空格）
        val old = base()
        old[1][3] = Piece.BJU
        var new = AssistBoard.clone(old)
        new[1][3] = Piece.EMPTY
        new[2][3] = Piece.BJU
        assertNull(AssistBoard.plausiblePair(old, new))
        // 吃子：黑车 (3,1)→(3,3) 吃红兵
        val old2 = base()
        old2[1][3] = Piece.BJU
        old2[3][3] = Piece.WBING
        val new2 = AssistBoard.clone(old2)
        new2[1][3] = Piece.EMPTY
        new2[3][3] = Piece.BJU
        assertNull(AssistBoard.plausiblePair(old2, new2))
        // 悔棋还原：红兵回到被吃的格子（出现+换色反向形态）
        val old3 = base()
        old3[1][3] = Piece.EMPTY
        old3[3][3] = Piece.BJU
        val new3 = AssistBoard.clone(old3)
        new3[1][3] = Piece.BJU
        new3[3][3] = Piece.WBING
        assertNull(AssistBoard.plausiblePair(old3, new3))
    }

    @Test
    fun `多子无落点蒸发被拒_真实崩塌形状`() {
        // 三子同时消失无落点（局3 01:52:59 帧形状）：子数差 -3 超出 3 格上限
        val old = base()
        val new = AssistBoard.clone(old)
        new[0][0] = Piece.EMPTY  // 黑车
        new[3][0] = Piece.EMPTY  // 黑卒
        new[9][0] = Piece.EMPTY  // 红车
        assertNotNull(AssistBoard.plausiblePair(old, new))
    }

    @Test
    fun `合法两步合并通过_真实FEN回放`() {
        // PC:41454 真实帧：车吃马 + 炮移位（cells=4、子数差 -1）
        val mergePrev = AssistBoard.fromFenBoard(
            "1r2kab2/4a4/4br1cn/p1nRp1p2/8p/2c1P1P2/PC6P/4C4/N8/2BAKABR1 w - - 0 1")
        val mergeCur = AssistBoard.fromFenBoard(
            "1r2kab2/4a4/4br1cn/p1R1p1p2/2c5p/4P1P2/PC6P/4C4/N8/2BAKABR1 w - - 0 1")
        assertNotNull(mergePrev)
        assertNotNull(mergeCur)
        assertNull(AssistBoard.plausiblePair(mergePrev!!, mergeCur!!))
    }

    @Test
    fun `兑子合并帧通过_真实FEN回放`() {
        // 11-10 局3 真实物理结果（cells=3、子数差 -2）：vc47 首版上界错一档
        // 曾整批误拒，必须通过（与 Go TestPlausiblePair 同源对表）
        val prev = AssistBoard.fromFenBoard(
            "3rkab2/4a2R1/4bc3/p1R1C4/1c3rpnp/9/P4C2P/2N1B4/4A4/2BAK4 w - - 0 1")
        val cur = AssistBoard.fromFenBoard(
            "3rkab2/4a2R1/4br3/p1R1C4/1c4pnp/9/P7P/2N1B4/4A4/2BAK4 w - - 0 1")
        assertNotNull(prev)
        assertNotNull(cur)
        assertNull(AssistBoard.plausiblePair(prev!!, cur!!))
    }

    @Test
    fun `同型兑子合并帧通过_同色双消失被拒`() {
        // 兑车（cells=2 异色双消失，接触格 old==new 无 diff）——复审 P1-1 补钉
        val old = base()
        old[0][0] = Piece.BJU
        old[4][0] = Piece.BJU   // 黑车进到红车格（红车已提前走开的语义省略为同格）
        old[9][0] = Piece.WJU
        old[4][0] = Piece.WJU
        // 重排：old = 黑车(0,0)+红车(4,0)接触前；new = 两车均消失（接触格
        // 恢复为黑车原始占据形态……此处直接构造双消失异色形状）
        val o = base()
        o[0][0] = Piece.BJU     // 黑车原位
        o[4][0] = Piece.EMPTY
        val n = AssistBoard.clone(o)
        n[0][0] = Piece.EMPTY   // 黑车回吃后离开（接触格净变化为 0 的简化形状）
        // ↑ 该构造 cells=1；真正的同型兑子 cells=2 形状=双消失异色，直接构造：
        val o2 = base()
        o2[0][0] = Piece.BJU    // 黑车出发格
        o2[9][0] = Piece.WJU    // 红车出发格
        val n2 = AssistBoard.clone(o2)
        n2[0][0] = Piece.EMPTY  // 黑车消失（回吃后接触格恢复黑车原占据 = 无 diff）
        n2[9][0] = Piece.EMPTY  // 红车消失（被兑）
        assertNull(AssistBoard.plausiblePair(o2, n2))
        // 同色双消失（两黑车纯蒸发，无兑子语义）：拒
        val o3 = base()
        o3[0][0] = Piece.BJU
        o3[0][1] = Piece.BJU
        val n3 = AssistBoard.clone(o3)
        n3[0][0] = Piece.EMPTY
        n3[0][1] = Piece.EMPTY
        assertNotNull(AssistBoard.plausiblePair(o3, n3))
    }

    @Test
    fun `还原同色子被拒`() {
        // prev = 红炮已进到 e2、h2 空出（canonicalStart 的 h2 本就有红炮，
        // 直接改会因新旧同值而构不成 2 格差）
        val old = base()
        old[7][4] = Piece.WPAO
        old[7][7] = Piece.EMPTY
        val new = AssistBoard.clone(old)
        new[7][4] = Piece.WBING // "复原子"是红兵 = 与移动方同色（非悔棋形状）
        new[7][7] = Piece.WPAO  // 红炮回到原位（填充格）
        assertNotNull(AssistBoard.plausiblePair(old, new))
    }

    @Test
    fun `undoRestoreSide还原帧推导轮次_非还原返回null`() {
        // 还原形态：红车回到原位 (4,5)，黑卒复原子回到 (4,4) → 撤销后红先行
        val old = base()
        old[4][4] = Piece.WJU   // 红车坐在被吃格
        val new = AssistBoard.clone(old)
        new[4][4] = Piece.BZU   // 黑卒复原
        new[4][5] = Piece.WJU   // 红车回到原位
        assertEquals(true, AssistBoard.undoRestoreSide(old, new))
        // 非还原（含消失格）：null
        val old2 = base()
        val new2 = AssistBoard.clone(old2)
        new2[3][0] = Piece.EMPTY
        assertNull(AssistBoard.undoRestoreSide(old2, new2))
    }

    @Test
    fun `单格残差与子数增加边界`() {
        // 单格消失（目的地漏检/幽灵清理）：放行
        val old = base()
        val new = AssistBoard.clone(old)
        new[3][0] = Piece.EMPTY
        assertNull(AssistBoard.plausiblePair(old, new))
        // 子数增加（同侧多格无吃复原）：拒
        val old2 = base()
        val new2 = AssistBoard.clone(old2)
        new2[4][0] = Piece.WBING
        new2[4][2] = Piece.WBING
        new2[4][4] = Piece.WBING
        new2[4][6] = Piece.WBING
        assertNotNull(AssistBoard.plausiblePair(old2, new2))
    }
}
