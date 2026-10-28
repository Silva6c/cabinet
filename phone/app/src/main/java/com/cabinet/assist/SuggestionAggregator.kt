package com.cabinet.assist

/**
 * 当前局面的多路候选聚合器（引擎 MultiPV 各线建议，端上聚合）。
 *
 * 槽位制（产品决策：候选严格等于 MultiPV 线数）：PC 引擎建议带线序号
 * multipv（1=当前排名最优），每个序号一个槽位——同序号新着法覆盖旧着法（掉出当前
 * 前 N 名即被淘汰），候选总数恒等于 PC 本轮已报出的线数，不缓存已掉线着法。
 * 无序号的建议（沉淀命中/旧 PC）按槽位 1 处理（引擎各线到齐后逐槽取代）。
 * **桔库候选唤醒（2026-10-17）**：桔库多候选局面 PC 逐条带序号下发（1..N），
 * 本聚合器零特判直接落槽——"变招"按钮因此对桔库多候选局面天然可用。
 *
 * 搜索代检测：PC 每轮搜索 depth 从小爬升、单调不减，一旦新到建议的 depth 明显回落
 * 即视为新一轮搜索开始，旧一代槽位全部清空重建（2026-09-30 反馈：不缓存旧着法）。
 * 换局面时调用 [reset] 重建。
 *
 * 线程模型：建议回调来自 WSS 线程，候选读取来自主线程（渲染/变招点击）——内部用
 * synchronized 保护读写；[candidates] 为槽位快照（整体替换 + @Volatile），读侧免锁；
 * 到达时刻一律经 [arrivedAtOf] 查询（带锁），与候选快照同锁更新，无快照窗口。
 */
class SuggestionAggregator(private val maxCandidates: Int = 5) {

    /** 聚合后的候选（按线序号升序=实力排序，最多 [maxCandidates] 槽）；只整体替换，读侧免锁 */
    @Volatile
    var candidates: List<RemoteSuggestion> = emptyList()
        private set

    /** 槽位表：线序号 → (最新建议, 最后到达时刻)；输出时按序号升序重建快照 */
    private val slots = HashMap<Int, Pair<RemoteSuggestion, Long>>()

    /** 各候选最后到达时刻（与 [candidates] 同锁成对更新，同序） */
    private var arrivedList: List<Long> = emptyList()

    /** 搜索代跟踪：上一条建议的深度 */
    private var lastDepth = 0

    /** 收到一条建议：新搜索代（depth 回到浅层）先清场重建；同序号覆盖，新序号追加 */
    fun onSuggestion(sug: RemoteSuggestion) {
        if (sug.bestMove.isEmpty()) return
        val now = System.currentTimeMillis()
        synchronized(this) {
            val d = sug.depth
            // 搜索代检测：PC 每轮搜索 depth 从 1 附近重新爬升，而 MultiPV 各线在同层
            // 高位交错（如 14/15/16 波动）——只有"曾到深层后又回到 ≤3"才算新一轮，
            // 回落阈值判据会被线间交错误触发
            if (lastDepth > 3 && d in 1..3) {
                slots.clear()
            }
            // d==0（开局库/沉淀建议无深度，协议缺省 0）不更新基线：若清成 0，紧随
            // 其后的新一轮搜索（depth 1~3）会因 lastDepth<=3 漏判清场，上一代掉线
            // 槽位残留到被逐槽覆盖为止。服务层 lastSugDepth 同款守卫同理
            if (d > 0) lastDepth = d
            // 序号缺省（桔库/沉淀/旧 PC）按槽位 1 处理
            val rank = sug.multipv ?: 1
            if (rank !in slots && slots.size >= maxCandidates) {
                return // 防御：序号超出上限且槽已满（正常 1-5 档不会发生），忽略
            }
            // 同槽覆盖=旧着法掉出当前排名即淘汰；时间戳随新帧刷新
            slots[rank] = sug to now
            val sorted = slots.entries.sortedBy { it.key }
            candidates = sorted.map { it.value.first }
            arrivedList = sorted.map { it.value.second }
        }
    }

    /** 指定着法候选的最后到达时刻；不在候选中返回 0（调用方回退到最近一条建议的时刻） */
    fun arrivedAtOf(bestMove: String): Long {
        synchronized(this) {
            val idx = candidates.indexOfFirst { it.bestMove == bestMove }
            return if (idx >= 0) arrivedList[idx] else 0L
        }
    }

    /** 换局面/重置：清空候选与搜索代跟踪（变招下标由服务层一并归零） */
    fun reset() {
        synchronized(this) {
            slots.clear()
            candidates = emptyList()
            arrivedList = emptyList()
            lastDepth = 0
        }
    }
}
