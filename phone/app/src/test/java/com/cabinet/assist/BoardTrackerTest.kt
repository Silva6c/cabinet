package com.cabinet.assist

import com.cabinet.gamelogic.Piece
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * BoardTracker 自愈与确认机制单测（纯 JVM）。
 * 重点回归:已确认局面过时后,子数跳变门限曾把真实新局面永久拒之门外(用户视角"卡住"),
 * 现依赖 unstableStreak>=12 的自我复位走出死区。
 */
class BoardTrackerTest {

    /**
     * 构造一个合法局面:开局基础上移除 n 个子(兵卒优先,最多 8 卒兵 + 4 马)。
     * 注意保留 col4 的兵/卒——否则帅将同列无遮挡触发"两将照面",局面非法,
     * 会走 isValid 拒绝路径,覆盖不到"子数跳变门限"这个被测逻辑。
     */
    private fun boardWithout(n: Int): RecognitionResult {
        val base = AssistBoard.canonicalStart()
        val candidates = mutableListOf<Pair<Int, Int>>()
        for (y in 0 until AssistBoard.H) for (x in 0 until AssistBoard.W) {
            val p = base[y][x]
            if ((p == Piece.WBING || p == Piece.BZU) && x != 4) candidates.add(y to x)
        }
        for (y in 0 until AssistBoard.H) for (x in 0 until AssistBoard.W) {
            val p = base[y][x]
            if (p == Piece.WMA || p == Piece.BMA) candidates.add(y to x)
        }
        for (k in 0 until minOf(n, candidates.size)) {
            val (y, x) = candidates[k]
            base[y][x] = Piece.EMPTY
        }
        return result(base)
    }

    private fun result(board: Array<IntArray>): RecognitionResult {
        val pieces = board.sumOf { row -> row.count { it != Piece.EMPTY } }
        return RecognitionResult(board, Orientation.STANDARD,
            AssistBoard.validate(board), pieces)
    }

    @Test
    fun `确认后连续3帧一致才确认新局面`() {
        val t = BoardTracker(3)
        val a = boardWithout(1)
        // 首次确认:3 帧
        assertEquals(BoardTracker.Event.UNSTABLE, t.onFrame(a))
        assertEquals(BoardTracker.Event.UNSTABLE, t.onFrame(a))
        assertEquals(BoardTracker.Event.NEW_BOARD, t.onFrame(a))
        assertEquals(a.canonical, t.confirmed!!.canonical)
    }

    @Test
    fun `子数超门限的局面被拒但不锁死_12帧后自愈重新确认`() {
        val t = BoardTracker(3)
        val start = AssistBoard.canonicalStart().let {
            RecognitionResult(it, Orientation.STANDARD, AssistBoard.validate(it), 32)
        }
        // 确认开局
        repeat(3) { t.onFrame(start) }
        assertEquals(start.canonical, t.confirmed!!.canonical)

        // 真实新局面:子数骤减 10(远超门限 max(2, 32/10)=3)——旧行为将永远 UNSTABLE
        val real = boardWithout(10)
        repeat(12) { assertEquals(BoardTracker.Event.UNSTABLE, t.onFrame(real)) }
        // 第 13 帧起自我复位(confirmed=null),重新走首次确认:3 帧后确认新局面
        assertEquals(BoardTracker.Event.UNSTABLE, t.onFrame(real))
        assertEquals(BoardTracker.Event.UNSTABLE, t.onFrame(real))
        val ev = t.onFrame(real)
        assertTrue("期望确认新局面,实际 $ev", ev == BoardTracker.Event.NEW_BOARD || ev == BoardTracker.Event.NEW_GAME)
        assertEquals(real.canonical, t.confirmed!!.canonical)
        assertEquals("自愈后不稳定计数应清零", 0, t.unstableStreak)
    }

    @Test
    fun `未满12帧不提前复位_门限内正常拒绝`() {
        val t = BoardTracker(3)
        val start = AssistBoard.canonicalStart().let {
            RecognitionResult(it, Orientation.STANDARD, AssistBoard.validate(it), 32)
        }
        repeat(3) { t.onFrame(start) }
        val big = boardWithout(10)
        // 少量(<12)被拒帧:不应自我复位,confirmed 保持
        repeat(6) { assertEquals(BoardTracker.Event.UNSTABLE, t.onFrame(big)) }
        assertEquals(start.canonical, t.confirmed!!.canonical)
    }

    @Test
    fun `SAME_BOARD清零不稳定计数`() {
        val t = BoardTracker(3)
        val a = boardWithout(1)
        repeat(3) { t.onFrame(a) }
        // 稳定后同局面帧
        assertEquals(BoardTracker.Event.SAME_BOARD, t.onFrame(a))
        assertEquals(0, t.unstableStreak)
    }

    @Test
    fun `候选累计中confirming为真_满帧确认后为假`() {
        val t = BoardTracker(3)
        val a = boardWithout(1)
        // 首次确认累计期:confirming 持续为真
        assertEquals(BoardTracker.Event.UNSTABLE, t.onFrame(a))
        assertTrue("首帧累计后应处于 confirming", t.confirming)
        assertEquals(BoardTracker.Event.UNSTABLE, t.onFrame(a))
        assertTrue(t.confirming)
        assertEquals(BoardTracker.Event.NEW_BOARD, t.onFrame(a))
        assertTrue("确认后不再 confirming", !t.confirming)
        // 稳态同局面帧:不 confirming
        assertEquals(BoardTracker.Event.SAME_BOARD, t.onFrame(a))
        assertTrue("SAME_BOARD 后不应 confirming", !t.confirming)
    }

    @Test
    fun `确认态下新局面单帧累计_重放补帧可完成确认`() {
        // 回归:点选式走子(无动画帧)时画面变化标志被单次识别消费,若 frameTick
        // 不把 confirming 当饥饿重放补帧,确认链将永久饿死(实测 2026-09-28)
        val t = BoardTracker(2)
        val start = AssistBoard.canonicalStart().let {
            RecognitionResult(it, Orientation.STANDARD, AssistBoard.validate(it), 32)
        }
        repeat(2) { t.onFrame(start) }
        assertEquals(start.canonical, t.confirmed!!.canonical)

        val next = boardWithout(1) // 子数 -1,门限内,真实走子形态
        assertEquals(BoardTracker.Event.UNSTABLE, t.onFrame(next))
        assertTrue("单帧新局面后应处于 confirming(等待重放补帧)", t.confirming)
        // frameTick 重放同一静止帧 → 第二帧即确认
        assertEquals(BoardTracker.Event.NEW_BOARD, t.onFrame(next))
        assertTrue("补帧确认后不再 confirming", !t.confirming)
        // v39 走子连贯性（248861c）：next=开局黑卒 (3,0) 挺进到 (4,0) 后识别
        // 漏检新位（单帧画面少画一枚卒）——确认时刻单步修复找到唯一解释
        // （黑卒前进，d=1 唯一）并把漏画的卒补到新位 (4,0)，confirmed 是
        // 修复后局面而非原始漏检帧 next（248861c 引入时本断言未同步）
        val repaired = AssistBoard.canonicalStart().also {
            it[4][0] = it[3][0]; it[3][0] = Piece.EMPTY
        }
        assertEquals(repaired, t.confirmed!!.canonical)
    }

    @Test
    fun `吃子步确认后走子方正确翻转`() {
        // 回归(2026-10-02):吃子步 movedSide 曾因"移除双色"返回 null,redGo 卡
        // 旧值,电脑端校验拒收(bad_position),用户被迫点"更新"。现只看新增子
        // 阵营,吃子步恒翻转。
        val t = BoardTracker(3)
        val start = AssistBoard.canonicalStart().let {
            RecognitionResult(it, Orientation.STANDARD, AssistBoard.validate(it), 32)
        }
        repeat(3) { t.onFrame(start) }
        assertTrue(t.redGo)
        // v39 走子连贯性（248861c）下本回归改用单步可解释的构造：红车
        // (9,0)→(4,0) 直线吃黑卒（伪走子枚举 d=0 唯一解释，走子方由 repair
        // 锁定为红）。原构造"红车瞬移 (9,0)→(5,1) 河界空位"不是单步可解释
        // 局面——修复枚举会命中黑卒同排覆盖类伪解把 confirmed 改写成伪局面
        //（实测 (4,1) 空化），该构造随 v39 失效。
        val capture = AssistBoard.canonicalStart().also {
            it[4][0] = it[9][0]; it[9][0] = Piece.EMPTY
        }.let { RecognitionResult(it, Orientation.STANDARD, AssistBoard.validate(it), 31) }
        repeat(2) { assertEquals(BoardTracker.Event.UNSTABLE, t.onFrame(capture)) }
        assertEquals(BoardTracker.Event.NEW_BOARD, t.onFrame(capture))
        assertTrue("红方刚吃子,下一步应是黑方", !t.redGo)
    }

    @Test
    fun `自愈后首确认开局重置红先`() {
        // 回归(2026-10-01):首次确认路径返回 NEW_GAME 时不重置 redGo——12 帧自愈
        // 后 redGo 残留上一局残局的旧值,新对局会以错误轮次上行
        val t = BoardTracker(3)
        val start = AssistBoard.canonicalStart().let {
            RecognitionResult(it, Orientation.STANDARD, AssistBoard.validate(it), 32)
        }
        repeat(2) { assertEquals(BoardTracker.Event.UNSTABLE, t.onFrame(start)) }
        assertEquals(BoardTracker.Event.NEW_GAME, t.onFrame(start))
        assertTrue(t.redGo)

        // 红方走一子(红兵 (6,0) 进到 (5,0),真实走子形态):movedSide=红 → redGo=false
        val next = AssistBoard.canonicalStart().also {
            it[5][0] = it[6][0]; it[6][0] = Piece.EMPTY
        }.let { RecognitionResult(it, Orientation.STANDARD, AssistBoard.validate(it), 32) }
        repeat(2) { assertEquals(BoardTracker.Event.UNSTABLE, t.onFrame(next)) }
        assertEquals(BoardTracker.Event.NEW_BOARD, t.onFrame(next))
        assertTrue("红方刚走子,下一步应是黑方", !t.redGo)

        // 残局后画面大变(子数跳变超门限)持续 12 帧 → 自愈复位,重新首确认开局
        val big = boardWithout(10)
        repeat(12) { assertEquals(BoardTracker.Event.UNSTABLE, t.onFrame(big)) }
        assertEquals(BoardTracker.Event.UNSTABLE, t.onFrame(start))
        assertEquals(BoardTracker.Event.UNSTABLE, t.onFrame(start))
        assertEquals(BoardTracker.Event.NEW_GAME, t.onFrame(start))
        assertTrue("自愈后首确认开局局面应重置红先 redGo=true", t.redGo)
    }

    // ---- B1 轮次恢复（2026-10-24）：进程重启后首次确认按持久化素材恢复走子方 ----

    @Test
    fun `恢复素材盘面全等时恢复持久化轮次`() {
        val t = BoardTracker(3)
        val p = boardWithout(2) // 中局面
        // 持久化素材：同盘面、轮黑（真实场景=重启前正轮黑走，FEN 带 b）
        t.setRestoreHint(AssistBoard.toFen(p.canonical, false))
        // 重启后识别到同一盘面：3 帧确认
        assertEquals(BoardTracker.Event.UNSTABLE, t.onFrame(p))
        assertEquals(BoardTracker.Event.UNSTABLE, t.onFrame(p))
        assertEquals(BoardTracker.Event.NEW_BOARD, t.onFrame(p))
        // 未修复前 redGo 恒 true（错轮次 → 建议 myTurn 门禁全灭）；恢复后应轮黑
        assertEquals(false, t.redGo)
    }

    @Test
    fun `恢复素材差1格时按走子推断轮次`() {
        val t = BoardTracker(3)
        val p = boardWithout(2) // 重启前局面（轮红）
        t.setRestoreHint(AssistBoard.toFen(p.canonical, true))
        // 停机期间红兵 (6,0)→(5,0) 走了一步：恢复素材是"上一手之前"的局面
        val n = AssistBoard.clone(p.canonical)
        n[6][0] = Piece.EMPTY
        n[5][0] = Piece.WBING
        val nr = result(n)
        assertEquals(BoardTracker.Event.UNSTABLE, t.onFrame(nr))
        assertEquals(BoardTracker.Event.UNSTABLE, t.onFrame(nr))
        assertEquals(BoardTracker.Event.NEW_BOARD, t.onFrame(nr))
        // 红已走 → 轮黑
        assertEquals(false, t.redGo)
    }

    @Test
    fun `恢复素材差2格以上无法推断时维持默认红先`() {
        val t = BoardTracker(3)
        val p = boardWithout(2)
        t.setRestoreHint(AssistBoard.toFen(p.canonical, false))
        // 停机期间走了多步（差 2 子，无新增 → movedSide 无法判定）：维持现状，
        // 错轮次由 bad_position 回环兜底
        val nr = boardWithout(4)
        assertEquals(BoardTracker.Event.UNSTABLE, t.onFrame(nr))
        assertEquals(BoardTracker.Event.UNSTABLE, t.onFrame(nr))
        assertEquals(BoardTracker.Event.NEW_BOARD, t.onFrame(nr))
        assertEquals(true, t.redGo)
    }

    @Test
    fun `开局局面优先走NEW_GAME不消费恢复素材`() {
        val t = BoardTracker(3)
        // 持久化素材是残局，但重启后画面是开局（已开新局）
        t.setRestoreHint(AssistBoard.toFen(boardWithout(2).canonical, false))
        val start = result(AssistBoard.canonicalStart())
        assertEquals(BoardTracker.Event.UNSTABLE, t.onFrame(start))
        assertEquals(BoardTracker.Event.UNSTABLE, t.onFrame(start))
        assertEquals(BoardTracker.Event.NEW_GAME, t.onFrame(start))
        assertEquals(true, t.redGo) // 新局红先，素材未被消费成错轮次
    }

    @Test
    fun `fromFenBoard往返一致且坏数据返回null`() {
        val p = boardWithout(2)
        val fen = AssistBoard.toFen(p.canonical, true)
        val back = AssistBoard.fromFenBoard(fen)
        assertTrue(back != null && AssistBoard.equal(p.canonical, back))
        assertEquals(null, AssistBoard.fromFenBoard("bad fen"))
        assertEquals(null, AssistBoard.fromFenBoard("9/9/9/9/9/9/9/9/9")) // 行数不足
    }

}
