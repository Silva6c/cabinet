package com.cabinet.assist

import com.cabinet.gamelogic.Piece
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
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
        // (0,9)→(0,8) 相邻格吃黑车。⚠canonicalStart 下红方没有任何合法吃子
        //（开局棋理），2026-11-10 结构门会正确拒绝"车越己兵"类瞬移构造——
        // prev 预先把黑车进到 (0,8)（红车正上相邻格，吃子路径无中间格）。
        // 2026-11-12 滑行防线后原 (0,4) 5 格长滑构造会被距离限制拦下（动画
        // 切片风险），改相邻格短滑吃子，走子方翻转语义不变。
        val prevBoard = AssistBoard.canonicalStart().also {
            it[0][0] = Piece.EMPTY   // 黑车离开原位
            it[8][0] = Piece.BJU     // 黑车进到 (0,8)——红车正上相邻格
        }
        val t2 = BoardTracker(3)
        val prevRes = RecognitionResult(prevBoard, Orientation.STANDARD,
            AssistBoard.validate(prevBoard), 31)
        // prev 非开局形态：首确认走 NEW_BOARD（3 帧）
        repeat(2) { assertEquals(BoardTracker.Event.UNSTABLE, t2.onFrame(prevRes)) }
        assertEquals(BoardTracker.Event.NEW_BOARD, t2.onFrame(prevRes))
        assertTrue(t2.redGo)
        // 吃子候选：红车 (0,9)→(0,8) 吃黑车（2 格消失+换色，伪合法）
        val capture = AssistBoard.clone(prevBoard).also {
            it[9][0] = Piece.EMPTY
            it[8][0] = Piece.WJU
        }.let { RecognitionResult(it, Orientation.STANDARD, AssistBoard.validate(it), 30) }
        // 单步几何提前确认（2026-11-09）：合法单步吃子 1 帧即确认，走子方由
        // repair 锁定——与 movedSide 主路径的吃子步翻转同源
        assertEquals(BoardTracker.Event.NEW_BOARD, t2.onFrame(capture))
        assertTrue("红方刚吃子,下一步应是黑方", !t2.redGo)
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
        // （单步几何提前确认 2026-11-09：d==0 唯一单步解释 → 1 帧即确认）
        val next = AssistBoard.canonicalStart().also {
            it[5][0] = it[6][0]; it[6][0] = Piece.EMPTY
        }.let { RecognitionResult(it, Orientation.STANDARD, AssistBoard.validate(it), 32) }
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

    @Test
    fun `提前确认吃自家子被结构门拒_redGo不改写`() {
        // 复审 P2-2 回归钉：pseudoMoves 含自家阻挡格目标，repair 可枚举出
        // "吃自家子"的 d==0 解释——提前确认路径必须有结构门，幻影帧不得
        // 借道确认上行；拒绝不改写 redGo、候选保留等真实帧治愈
        val t = BoardTracker(3)
        val start = result(AssistBoard.canonicalStart())
        repeat(2) { assertEquals(BoardTracker.Event.UNSTABLE, t.onFrame(start)) }
        assertEquals(BoardTracker.Event.NEW_GAME, t.onFrame(start))
        // 红车 (0,9)→(1,9) "吃"自家红马：pseudoMoves 含自家阻挡格目标（repair
        // 可枚举），plausiblePair 按被吃子同色判拒。相邻格构造——2026-11-12
        // 滑行防线后原 (0,6) 3 格长滑构造先被距离限制拦下，触不到结构门
        val ownCapture = AssistBoard.clone(start.canonical).also {
            it[9][0] = Piece.EMPTY
            it[9][1] = Piece.WJU
        }.let { result(it) }
        assertEquals(BoardTracker.Event.UNSTABLE, t.onFrame(ownCapture))
        assertEquals(start.canonical, t.confirmed!!.canonical)
        assertTrue(t.redGo)
        assertNotNull(t.lastGateReject)
        // 真实单步帧到达：候选覆盖治愈
        val real = AssistBoard.clone(start.canonical).also {
            it[5][0] = Piece.WBING; it[6][0] = Piece.EMPTY  // 红兵 (0,6)→(0,5)
        }.let { result(it) }
        assertEquals(BoardTracker.Event.NEW_BOARD, t.onFrame(real))
        assertNull(t.lastGateReject)
    }

    @Test
    fun `单步几何提前确认_候选1帧即确认不等confirmCount`() {
        // 2026-11-09"停在上一帧"治理：对手回手击穿 confirmCount=2 确认窗口的
        // 两步合并，靠 d==0 唯一单步解释提前确认收口（确认窗口 ~1.6s→~0.5s）
        val t = BoardTracker(3) // confirmCount=3，提前确认仍应在 1 帧生效
        val start = result(AssistBoard.canonicalStart())
        repeat(2) { assertEquals(BoardTracker.Event.UNSTABLE, t.onFrame(start)) }
        assertEquals(BoardTracker.Event.NEW_GAME, t.onFrame(start)) // 首确认开局
        // 真实单步走子（红炮 h2→g2 相邻格横移，伪走子枚举唯一解释）。
        // 2026-11-12 滑行防线后原 h2→e2 3 格横移构造被距离限制拦下（动画切片
        // 风险），改相邻格 1 格横移，提前确认加速语义不变
        val moved = AssistBoard.clone(start.canonical).also {
            it[7][6] = it[7][7]; it[7][7] = Piece.EMPTY
        }.let { result(it) }
        assertEquals(BoardTracker.Event.NEW_BOARD, t.onFrame(moved))
        assertEquals(moved.canonical, t.confirmed!!.canonical)
        assertTrue("红方刚走子,下一步应是黑方", !t.redGo)
    }

    @Test
    fun `提前确认关闭或非单步解释时仍按confirmCount确认`() {
        // 开关关（adb 诊断通道）：同一步走子仍需 confirmCount 帧确认
        val t = BoardTracker(3, singleMoveRepair = true, singleMoveEarlyConfirm = false)
        val start = result(AssistBoard.canonicalStart())
        repeat(2) { assertEquals(BoardTracker.Event.UNSTABLE, t.onFrame(start)) }
        assertEquals(BoardTracker.Event.NEW_GAME, t.onFrame(start)) // 首确认开局
        val moved = AssistBoard.clone(start.canonical).also {
            it[7][4] = it[7][7]; it[7][7] = Piece.EMPTY
        }.let { result(it) }
        assertEquals(BoardTracker.Event.UNSTABLE, t.onFrame(moved))
        assertEquals(BoardTracker.Event.UNSTABLE, t.onFrame(moved))
        assertEquals(BoardTracker.Event.NEW_BOARD, t.onFrame(moved))

        // 开关开但非单步解释（同帧消失两子，无 from→to 结构）：不提前
        val t2 = BoardTracker(3)
        val start2 = boardWithout(1) // 中途局面（非开局形态）：首确认走 NEW_BOARD
        repeat(2) { assertEquals(BoardTracker.Event.UNSTABLE, t2.onFrame(start2)) }
        assertEquals(BoardTracker.Event.NEW_BOARD, t2.onFrame(start2))
        val twoGone = boardWithout(3) // 相对 start2 再少两子，识别抖动形态
        assertEquals(BoardTracker.Event.UNSTABLE, t2.onFrame(twoGone))
    }

    @Test
    fun `提前确认错侧走子不触发_回落confirmCount主路径`() {
        // 2026-11-10 加强：移动子必须与预期行棋方（redGo）一致才提前——不满足
        // 轮次语义的 2 格跳变更可能是识别错位幻觉；错侧真走子（prev 侧卡旧值
        // 形态）回落 confirmCount 主路径，repair 锁侧照常纠正
        val t = BoardTracker(3)
        val start = result(AssistBoard.canonicalStart())
        repeat(2) { assertEquals(BoardTracker.Event.UNSTABLE, t.onFrame(start)) }
        assertEquals(BoardTracker.Event.NEW_GAME, t.onFrame(start))
        assertTrue(t.redGo) // 红先：预期移动方=红
        // 黑走子（黑卒 (3,0)→(4,0)）：单步可解释但移动子 ≠ 预期行棋方 → 不提前
        val blackMove = AssistBoard.clone(start.canonical).also {
            it[4][0] = it[3][0]; it[3][0] = Piece.EMPTY
        }.let { result(it) }
        assertEquals(BoardTracker.Event.UNSTABLE, t.onFrame(blackMove))
        assertEquals(BoardTracker.Event.UNSTABLE, t.onFrame(blackMove))
        assertEquals(BoardTracker.Event.NEW_BOARD, t.onFrame(blackMove))
        assertTrue("黑方刚走子,下一步应是红方", t.redGo)
    }

    @Test
    fun `车直线滑行2格以上的过路帧不提前确认`() {
        // 回归 2026-11-12 案例 A（13:24:40 黑车 4 格长滑被切成两帧确认）：滑行
        // ≥2 格的过路帧对上一确认局面恰构成唯一单步解释（d==0），提前确认会把
        // 一个动画切成两个确认局面——幻影 prev 进 PC 且引擎照算，真实终帧变成
        // "又一次同侧走子"撞结构门。直线滑行 ≥2 格必须回落 confirmCount 主路径
        val t = BoardTracker(3)
        val start = result(AssistBoard.canonicalStart())
        repeat(3) { t.onFrame(start) }
        assertEquals(start.canonical, t.confirmed!!.canonical)

        // 红炮 b2(y7,x1)→f2(y7,x5) 横移 4 格（y7 行 x2–x5 全空，真空道）的过路
        // 帧 d2(y7,x3)：对 start 恰构成唯一单步（炮平移 2 格，路径 x2 空）、移动
        // 方=红=预期行棋方——防线前全部条件满足提前确认，滑行距离检查必须拦下
        val mid = AssistBoard.clone(start.canonical).also {
            it[7][1] = Piece.EMPTY
            it[7][3] = Piece.WPAO
        }.let { result(it) }
        assertEquals(BoardTracker.Event.UNSTABLE, t.onFrame(mid))
        assertEquals("滑行过路帧不得成为确认局面", start.canonical, t.confirmed!!.canonical)

        // 停稳帧 f2：仍 2 格差，走 confirmCount 主路径正常确认（3 帧）
        val fin = AssistBoard.clone(start.canonical).also {
            it[7][1] = Piece.EMPTY
            it[7][5] = Piece.WPAO
        }.let { result(it) }
        assertEquals(BoardTracker.Event.UNSTABLE, t.onFrame(fin))
        assertEquals(BoardTracker.Event.UNSTABLE, t.onFrame(fin))
        assertEquals(BoardTracker.Event.NEW_BOARD, t.onFrame(fin))
        assertEquals(fin.canonical, t.confirmed!!.canonical)
        assertEquals("长滑终帧应走主路径确认", "main", t.lastConfirmPath)
    }

    @Test
    fun `相邻格直行与马跳仍可提前确认`() {
        // 滑行防线只拦直线 ≥2 格：相邻格单步（无滑行中间帧）不受扰，vc45 的
        // "停在上一帧"加速语义保留
        val t = BoardTracker(3)
        val start = result(AssistBoard.canonicalStart())
        repeat(3) { t.onFrame(start) }

        // 红兵 g7(y6)→g6(y5) 相邻 1 格直行，移动方=红=预期行棋方
        val step = AssistBoard.clone(start.canonical).also {
            it[6][6] = Piece.EMPTY
            it[5][6] = Piece.WBING
        }.let { result(it) }
        assertEquals(BoardTracker.Event.NEW_BOARD, t.onFrame(step))
        assertEquals(step.canonical, t.confirmed!!.canonical)
        assertEquals("相邻格单步应走提前确认", "early", t.lastConfirmPath)
        assertTrue("红方刚走子,下一步应是黑方", !t.redGo)
    }

    @Test
    fun `纯fill漏检复现帧不翻轮次_031743回归`() {
        // 回归 2026-11-13 03:17:43：红兵 (4,4) 识别漏检 ≥2 帧 → 缺兵盘面被确认
        // → 兵复现成 1 格纯 fill，movedSide 曾按"新增子=红"翻 b（错轮次持续
        // 95.5s 到手动更新）。纯 fill 守卫：真实走子必含出发格消失，"空→有子"
        // 且无任何消失格的变化不构成走子证据（棋子不会凭空出现，物理 100%
        // 非走子）；悔棋还原（fill+swap）由 undoRestoreSide 先行分流不受扰
        val t = BoardTracker(3)
        // 已确认缺兵盘面（识别漏检：兵+马双漏，非开局形态防重开检测抢先），
        // 轮次=该红走（w，与事故日志 44543 帧一致）
        val missing = AssistBoard.clone(AssistBoard.canonicalStart()).also {
            it[6][6] = Piece.EMPTY
            it[9][1] = Piece.EMPTY
        }
        t.restore(result(missing), redGoSide = true)
        assertTrue(t.redGo)
        // 兵复现（1 格纯 fill，马仍缺=非开局）：修复前 movedSide 判红翻 b；修复后保持
        val restored = AssistBoard.clone(missing).also {
            it[6][6] = Piece.WBING
        }.let { result(it) }
        assertEquals(BoardTracker.Event.UNSTABLE, t.onFrame(restored))
        assertEquals(BoardTracker.Event.UNSTABLE, t.onFrame(restored))
        assertEquals(BoardTracker.Event.NEW_BOARD, t.onFrame(restored))
        assertEquals(restored.canonical, t.confirmed!!.canonical)
        assertTrue("纯 fill 复现帧不得翻轮次（漏检恢复非走子）", t.redGo)
    }

    @Test
    fun `门拒连拒3次弃confirmed走首次确认_发呆期逃生口`() {
        // 2026-11-13 发呆期治理：同一 prev 连续 3 次被结构门拒（合并帧场景实测
        // 12.5s 走满 12 帧通用自愈，phone-20261113-025645-局2.log:590-640）——
        // 第 3 次拒直接丢弃 confirmed 走首次确认路径（候选与 redGo 保留），
        // 真盘面 confirmCount 帧即确认。幻影提前上行无害：PC 结构门对同一
        // prev 连续第 2 次拒即按识别自愈放行，不比现状更不安全
        val t = BoardTracker(2)
        val start = result(AssistBoard.canonicalStart())
        repeat(2) { t.onFrame(start) }
        // 构造门拒帧：红车"吃"自家马（伪合法形状，两端结构门都拒）
        val ghost = AssistBoard.clone(start.canonical).also {
            it[9][0] = Piece.EMPTY
            it[9][1] = Piece.WJU
        }.let { result(it) }
        assertEquals(BoardTracker.Event.UNSTABLE, t.onFrame(ghost))
        assertEquals(BoardTracker.Event.UNSTABLE, t.onFrame(ghost))
        assertEquals(BoardTracker.Event.UNSTABLE, t.onFrame(ghost))
        assertNull("第 3 次连拒应触发逃生口丢弃 confirmed", t.confirmed)
        assertTrue("逃生口保留 redGo（轮次未被坏帧污染）", t.redGo)
        // 真盘面走首次确认路径（prev==null）confirmCount 帧确认
        val next = AssistBoard.clone(start.canonical).also {
            it[6][0] = Piece.EMPTY
            it[5][0] = Piece.WBING
        }.let { result(it) }
        assertEquals(BoardTracker.Event.UNSTABLE, t.onFrame(next))
        assertEquals(BoardTracker.Event.NEW_BOARD, t.onFrame(next))
        assertEquals(next.canonical, t.confirmed!!.canonical)
        assertEquals("逃生口后应走首次确认路径", "first", t.lastConfirmPath)
    }

    @Test
    fun `正常走子不受逃生口与纯fill守卫影响`() {
        // 回归守卫：movedSide 守卫与门拒逃生口都不得影响正常确认链
        val t = BoardTracker(3)
        val start = result(AssistBoard.canonicalStart())
        repeat(3) { t.onFrame(start) }
        // 正常单步（黑卒 (3,0)→(4,0)）：错侧走主路径 2 帧确认，movedSide 判黑
        val blackMove = AssistBoard.clone(start.canonical).also {
            it[4][0] = it[3][0]; it[3][0] = Piece.EMPTY
        }.let { result(it) }
        repeat(2) { assertEquals(BoardTracker.Event.UNSTABLE, t.onFrame(blackMove)) }
        assertEquals(BoardTracker.Event.NEW_BOARD, t.onFrame(blackMove))
        assertTrue("黑方刚走子,下一步应是红方", t.redGo)
        assertEquals("main", t.lastConfirmPath)
    }
}
