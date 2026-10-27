package com.cabinet.assist

import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * SuggestionAggregator（变招端上聚合）单测（纯 JVM）。
 *
 * 槽位制语义（产品决策"候选严格等于 MultiPV 数"）：PC 引擎建议带线序号
 * multipv，端上每个序号一个槽位——同序号新着法覆盖旧着法（掉出当前前 N 名即淘汰），
 * 候选总数恒等于 PC 已报出的线数；候选按序号升序（实力排序）。
 * 无序号建议（桔库/沉淀/旧 PC）按槽位 1 处理；depth 明显回落=新一轮搜索、全部重建。
 */
class SuggestionAggregatorTest {

    /** 造一条建议：默认引擎来源、无 WDL，bestMove 即 PV 首步 */
    private fun sug(
        bestMove: String,
        scoreCp: Int? = -100,
        depth: Int = 16,
        source: String = "engine",
        multipv: Int? = null
    ): RemoteSuggestion =
        RemoteSuggestion(
            source = source, bestMove = bestMove, scoreCp = scoreCp, scoreMate = null,
            pv = listOf(bestMove, "h1g1"), depth = depth, multipv = multipv
        )

    @Test
    fun `单条建议形成一个候选`() {
        val agg = SuggestionAggregator()
        agg.onSuggestion(sug("b2b1"))
        assertEquals(listOf("b2b1"), agg.candidates.map { it.bestMove })
    }

    @Test
    fun `空bestMove被忽略不影响候选`() {
        val agg = SuggestionAggregator()
        agg.onSuggestion(sug("b2b1"))
        agg.onSuggestion(sug("", scoreCp = null))
        assertEquals(listOf("b2b1"), agg.candidates.map { it.bestMove })
    }

    @Test
    fun `候选按线序号升序排列_与到达顺序无关`() {
        val agg = SuggestionAggregator()
        // 乱序到达（3→1→2）：输出仍按排名 1/2/3
        agg.onSuggestion(sug("c2e2", multipv = 3))
        agg.onSuggestion(sug("c2c1", multipv = 1))
        agg.onSuggestion(sug("f0e1", multipv = 2))
        assertEquals(listOf("c2c1", "f0e1", "c2e2"), agg.candidates.map { it.bestMove })
    }

    @Test
    fun `同线序号新着法覆盖旧着法_掉线即淘汰不增项`() {
        val agg = SuggestionAggregator()
        agg.onSuggestion(sug("c2c1", multipv = 1))
        agg.onSuggestion(sug("f0e1", multipv = 2))
        agg.onSuggestion(sug("c2e2", multipv = 3))
        assertEquals(3, agg.candidates.size)
        // 搜索推进：线 2 的着法从 f0e1 变为 d0e1（f0e1 掉出前三名）
        agg.onSuggestion(sug("d0e1", multipv = 2))
        // 候选总数恒 3（MultiPV 数），不缓存已掉线着法
        assertEquals(3, agg.candidates.size)
        assertEquals(listOf("c2c1", "d0e1", "c2e2"), agg.candidates.map { it.bestMove })
        // 被淘汰着法的时间戳清零（变招循环不再点到它）
        assertEquals(0L, agg.arrivedAtOf("f0e1"))
    }

    @Test
    fun `三线排名互换不增项_分数与排名取最新`() {
        val agg = SuggestionAggregator()
        agg.onSuggestion(sug("aaaa", multipv = 1, scoreCp = -50))
        agg.onSuggestion(sug("bbbb", multipv = 2, scoreCp = -70))
        agg.onSuggestion(sug("cccc", multipv = 3, scoreCp = -90))
        // 下一深度：b 升到第 1，a 降到第 2（皮卡鱼线间排名互换）
        agg.onSuggestion(sug("bbbb", multipv = 1, scoreCp = -45))
        agg.onSuggestion(sug("aaaa", multipv = 2, scoreCp = -60))
        assertEquals(3, agg.candidates.size)
        assertEquals(listOf("bbbb", "aaaa", "cccc"), agg.candidates.map { it.bestMove })
        assertEquals(-45, agg.candidates[0].scoreCp) // 分数取最新
        assertEquals(-60, agg.candidates[1].scoreCp)
    }

    @Test
    fun `同着法同线重复到达原位刷新不增项`() {
        val agg = SuggestionAggregator()
        agg.onSuggestion(sug("c2c1", scoreCp = -80, depth = 8, multipv = 1))
        agg.onSuggestion(sug("c2e2", multipv = 2))
        agg.onSuggestion(sug("c2c1", scoreCp = -136, depth = 14, multipv = 1)) // 搜索推进后的新终值
        assertEquals(2, agg.candidates.size)
        assertEquals("c2c1", agg.candidates[0].bestMove) // 槽位不动，变招循环稳定
        assertEquals(-136, agg.candidates[0].scoreCp)    // 分数/深度取最新
        assertEquals(14, agg.candidates[0].depth)
    }

    @Test
    fun `无序号建议按槽1处理_互相覆盖只留最新`() {
        val agg = SuggestionAggregator()
        // 旧 PC / 桔库建议无 multipv 字段：都占槽位 1
        agg.onSuggestion(sug("b2b1"))
        agg.onSuggestion(sug("c2e2"))
        assertEquals(listOf("c2e2"), agg.candidates.map { it.bestMove })
    }

    @Test
    fun `引擎线到齐后逐槽取代无序号建议`() {
        val agg = SuggestionAggregator()
        agg.onSuggestion(sug("b2b1")) // 无序号（如桔库命中）→ 槽 1
        agg.onSuggestion(sug("c3c4", multipv = 2))
        assertEquals(listOf("b2b1", "c3c4"), agg.candidates.map { it.bestMove })
        agg.onSuggestion(sug("h0g2", multipv = 1)) // 引擎线 1 取代槽 1
        assertEquals(listOf("h0g2", "c3c4"), agg.candidates.map { it.bestMove })
    }

    @Test
    fun `超上限的新序号被忽略_防御异常帧`() {
        val agg = SuggestionAggregator(maxCandidates = 5)
        for (r in 1..5) agg.onSuggestion(sug("m$r", multipv = r))
        agg.onSuggestion(sug("m9", multipv = 9)) // 正常 1-5 档不会发生
        assertEquals(5, agg.candidates.size)
        assertEquals(listOf("m1", "m2", "m3", "m4", "m5"), agg.candidates.map { it.bestMove })
    }

    @Test
    fun `深度明显回落即新一轮搜索_旧槽位全部作废`() {
        val agg = SuggestionAggregator()
        // 上一轮搜索推进到 depth 16，三条线到齐
        agg.onSuggestion(sug("h2e2", depth = 4, multipv = 1))
        agg.onSuggestion(sug("h2d2", depth = 10, multipv = 2))
        agg.onSuggestion(sug("c3c4", depth = 16, multipv = 3))
        assertEquals(3, agg.candidates.size)
        // 新一轮搜索从头爬升：depth 回落到 2（≤3）= 旧一代作废
        agg.onSuggestion(sug("b0c2", depth = 2, multipv = 1))
        assertEquals(listOf("b0c2"), agg.candidates.map { it.bestMove })
    }

    @Test
    fun `深度微降一不触发重建_容忍线间交错`() {
        val agg = SuggestionAggregator()
        agg.onSuggestion(sug("h2e2", depth = 16, multipv = 1))
        agg.onSuggestion(sug("h2d2", depth = 15, multipv = 2)) // 15 不在 1..3，视为同层交错
        assertEquals(listOf("h2e2", "h2d2"), agg.candidates.map { it.bestMove })
    }

    @Test
    fun `reset清空候选`() {
        val agg = SuggestionAggregator()
        agg.onSuggestion(sug("b2b1", multipv = 1))
        agg.onSuggestion(sug("c2e2", multipv = 2))
        agg.reset()
        assertTrue(agg.candidates.isEmpty())
        assertEquals(0L, agg.arrivedAtOf("b2b1"))
    }

    @Test
    fun `reset同时清除搜索代跟踪_新局面首条不误判回落`() {
        val agg = SuggestionAggregator()
        agg.onSuggestion(sug("h2e2", depth = 20, multipv = 1))
        agg.reset()
        // 换局面后首条建议 depth 12：若搜索代未清会误清一次（无害但脏）
        agg.onSuggestion(sug("c3c4", depth = 12, multipv = 1))
        assertEquals(listOf("c3c4"), agg.candidates.map { it.bestMove })
    }

    @Test
    fun `候选到达时间戳按槽位记录且同槽刷新`() {
        val agg = SuggestionAggregator()
        agg.onSuggestion(sug("h2e2", depth = 10, multipv = 1))
        val t1 = agg.arrivedAtOf("h2e2")
        assertTrue(t1 > 0)
        assertTrue(agg.arrivedAtOf("不存在") == 0L)
        // 同槽刷新（毫秒粒度可能同值，单调不减）
        agg.onSuggestion(sug("h2e2", depth = 12, multipv = 1))
        assertTrue(agg.arrivedAtOf("h2e2") >= t1)
        // 新槽到达
        agg.onSuggestion(sug("c3c4", depth = 12, multipv = 2))
        assertTrue(agg.arrivedAtOf("c3c4") >= t1)
    }
}
