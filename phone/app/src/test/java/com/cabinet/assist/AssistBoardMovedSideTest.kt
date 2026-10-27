package com.cabinet.assist

import com.cabinet.gamelogic.Piece
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test

/**
 * movedSide 走子方推断单测（纯 JVM）。
 * 回归(2026-10-02):旧逻辑把"移除双色棋子"当异常返回 null——而吃子步必然移除
 * 两色棋子(移动方起点子+被吃方子),是否翻车还取决于格子遍历顺序(纯运气),
 * 实测电脑端 10/10 次拒收皆此因。新规则:只看"哪里多了子"。
 */
class AssistBoardMovedSideTest {

    /** 开局局面复本 */
    private fun start(): Array<IntArray> = Array(AssistBoard.H) { y ->
        IntArray(AssistBoard.W) { x -> AssistBoard.canonicalStart()[y][x] }
    }

    @Test
    fun `吃子步_目标格先被遍历_判定移动方`() {
        // 复现 2026-10-02 10:07 实例:红车 (9,0)→(5,1) 吃黑车。目标格 y=1 先于
        // 起点 y=9 被遍历——旧代码先记 removed=黑、再遇起点红冲突返回 null;
        // 新代码只看新增(红车)恒判红。
        val old = start()
        val new = start()
        new[5][1] = Piece.WJU   // 红车落到黑车格(吃子)
        new[9][0] = Piece.EMPTY // 起点清空
        assertEquals(1, AssistBoard.movedSide(old, new))
    }

    @Test
    fun `吃子步_起点先被遍历_判定移动方`() {
        // 黑车 (0,4)→(6,4) 吃红兵:起点 y=0 先被遍历,目标格在后
        val old = start()
        val new = start()
        new[6][4] = Piece.BJU   // 黑车落到红兵格(吃子)
        new[0][4] = Piece.EMPTY // 起点清空
        assertEquals(0, AssistBoard.movedSide(old, new))
    }

    @Test
    fun `吃子步_目标深入敌阵_判定移动方`() {
        // 深入红阵的黑车 (8,0) 被红车 (9,0) 吃掉:移除双色冲突在先,新增在最后
        val old = start()
        old[8][0] = Piece.BJU
        val new = start()
        new[8][0] = Piece.WJU
        new[9][0] = Piece.EMPTY
        assertEquals(1, AssistBoard.movedSide(old, new))
    }

    @Test
    fun `非吃子步_单色新增单色移除_判定移动方`() {
        // 红兵 (6,0) 进到 (5,0)
        val old = start()
        val new = start()
        new[5][0] = Piece.WBING
        new[6][0] = Piece.EMPTY
        assertEquals(1, AssistBoard.movedSide(old, new))
    }

    @Test
    fun `红黑同时新增_识别噪声_返回null`() {
        // 双方同时多子只可能是识别噪声,放弃判定(redGo 保持旧值,由自动纠错兜底)
        val old = start()
        val new = start()
        new[4][0] = Piece.WJU   // 红多一车
        new[4][8] = Piece.BJU   // 黑多一车
        assertNull(AssistBoard.movedSide(old, new))
    }

    @Test
    fun `无新增_单色移除_按移除方补判`() {
        // 目标格新子被误识别为空时只剩移除信息:移除单色黑马 → 判黑(保守兜底,
        // 与旧逻辑一致)
        val old = start()
        val new = start()
        new[2][1] = Piece.EMPTY
        assertEquals(0, AssistBoard.movedSide(old, new))
    }

    @Test
    fun `无新增_双色移除_返回null`() {
        // 全无新增且红黑各少一子:无法定位移动方(与旧逻辑一致)
        val old = start()
        val new = start()
        new[2][1] = Piece.EMPTY
        new[7][7] = Piece.EMPTY
        assertNull(AssistBoard.movedSide(old, new))
    }

    @Test
    fun `局面无变化_返回null`() {
        val b = start()
        assertNull(AssistBoard.movedSide(b, b))
    }

    // ---- 2026-10-02 二次精化：同色换型不是走子证据 ----
    // 真实走子的目标格必然"空→有子"或"变色（吃子）"，己方子吃不到己方子上；
    // 同色值变（炮→马）只能是识别误读。旧逻辑把它算作该色"新增"，与真实走子方
    // 的"新增"冲突返回 null（轮次卡旧值、悬浮窗无箭头），或单发时误判该色走子。

    @Test
    fun `同色换型误读伴随真实走子_判定真实走子方`() {
        // 黑马 (0,1)→(1,1) 真实走子 + 红炮 (7,7) 被误读成红马（同色值变）：
        // 旧逻辑红黑"同时新增"返回 null；新逻辑过滤同色误读，恒判黑
        val old = start()
        val new = start()
        new[1][1] = Piece.BMA   // 黑马真实落点
        new[0][1] = Piece.EMPTY // 黑马起点清空
        new[7][7] = Piece.WMA   // 红炮位误读为红马（颜色未变，非走子证据）
        assertEquals(0, AssistBoard.movedSide(old, new))
    }

    @Test
    fun `同色换型单独发生_无走子_返回null`() {
        // 仅红炮 (7,7) 被误读成红马：无真实走子。旧逻辑误判"红方走子"污染轮次；
        // 新逻辑过滤后无证据，返回 null（redGo 保持旧值，保守）
        val old = start()
        val new = start()
        new[7][7] = Piece.WMA
        assertNull(AssistBoard.movedSide(old, new))
    }

    @Test
    fun `吃子目标格同色换型误读_按起点移除补判`() {
        // 黑马 (0,1) 吃红兵 (6,4)，落点被误读成红马（同色换型）：真实移动方是黑。
        // 新增证据被同色过滤后，起点"黑马→空"是唯一真移除 → 补判黑。
        // 旧逻辑把落点算作红方"新增"，误判红走
        val old = start()
        val new = start()
        new[6][4] = Piece.WMA   // 红兵位落黑马后误读为红马
        new[0][1] = Piece.EMPTY // 黑马起点清空
        assertEquals(0, AssistBoard.movedSide(old, new))
    }

    @Test
    fun `吃子目标格误读为空_双色真移除_返回null`() {
        // 黑马 (0,1) 吃红兵 (6,4)，落点被误读为空：起点黑移除+落点红移除，
        // 双色真移除无法定位移动方，返回 null（与旧逻辑一致，保守兜底）
        val old = start()
        val new = start()
        new[6][4] = Piece.EMPTY
        new[0][1] = Piece.EMPTY
        assertNull(AssistBoard.movedSide(old, new))
    }
}
