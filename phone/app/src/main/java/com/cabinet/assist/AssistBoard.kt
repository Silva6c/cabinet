package com.cabinet.assist

import com.cabinet.gamelogic.Piece

/**
 * 纯 JVM 的中国象棋棋盘抽象。
 * 约定与项目 gamelogic.Board 一致：piece[y][x]，y=0 为黑方底线，y=9 为红方底线（红在下即标准视角）。
 * 本文件不依赖任何 Android 类，便于单元测试。
 */
object AssistBoard {
    const val W = 9
    const val H = 10

    /** 标准开局局面，piece[y][x]，红在 y=9 底部 */
    fun canonicalStart(): Array<IntArray> {
        val p = Array(H) { IntArray(W) }
        for (x in 0 until W) {
            p[0][x] = when (x) {
                0, 8 -> Piece.BJU
                1, 7 -> Piece.BMA
                2, 6 -> Piece.BXIANG
                3, 5 -> Piece.BSHI
                else -> Piece.BJIANG
            }
        }
        p[2][1] = Piece.BPAO
        p[2][7] = Piece.BPAO
        for (x in 0 until W step 2) p[3][x] = Piece.BZU
        for (x in 0 until W step 2) p[6][x] = Piece.WBING
        p[7][1] = Piece.WPAO
        p[7][7] = Piece.WPAO
        for (x in 0 until W) {
            p[9][x] = when (x) {
                0, 8 -> Piece.WJU
                1, 7 -> Piece.WMA
                2, 6 -> Piece.WXIANG
                3, 5 -> Piece.WSHI
                else -> Piece.WSHUAI
            }
        }
        return p
    }

    fun clone(p: Array<IntArray>): Array<IntArray> = Array(H) { p[it].clone() }

    fun equal(a: Array<IntArray>, b: Array<IntArray>): Boolean {
        for (y in 0 until H) for (x in 0 until W) if (a[y][x] != b[y][x]) return false
        return true
    }

    fun countPieces(p: Array<IntArray>): Int {
        var n = 0
        for (y in 0 until H) for (x in 0 until W) if (p[y][x] != Piece.EMPTY) n++
        return n
    }

    /** piece[y][x] -> xqbase FEN 盘面 + 走子方 */
    fun toFen(pieces: Array<IntArray>, redGo: Boolean): String {
        val sb = StringBuilder()
        for (y in 0 until H) {
            var zeros = 0
            for (x in 0 until W) {
                val p = pieces[y][x]
                if (p == Piece.EMPTY) zeros++
                else {
                    if (zeros > 0) { sb.append(zeros); zeros = 0 }
                    sb.append(Piece.pieceCharMap[p])
                }
            }
            if (zeros > 0) sb.append(zeros)
            if (y < H - 1) sb.append('/')
        }
        return "$sb ${if (redGo) "w" else "b"} - - 0 1"
    }

    /**
     * FEN 盘面段（首段，"rnbak.../9/..."）→ canonical。轮次持久化恢复用——
     * 只需盘面对比（全等/差 1 走子推断），不校验合法性：来源是自家持久化数据，
     * 坏数据返回 null 让恢复链优雅放弃，绝不抛异常打断识别管线。
     * 轮次字段由调用方另行解析（split 第二段）。
     */
    fun fromFenBoard(fen: String): Array<IntArray>? {
        val rows = fen.substringBefore(' ').split('/')
        if (rows.size != H) return null
        val p = Array(H) { IntArray(W) }
        for (y in 0 until H) {
            var x = 0
            for (ch in rows[y]) {
                if (ch.isDigit()) {
                    x += ch - '0'
                } else {
                    val v = Piece.pieceValueMap[ch]
                    if (v == null || x >= W) return null
                    p[y][x] = v
                    x++
                }
            }
            if (x != W) return null
        }
        return p
    }

    /**
     * 推断"刚走子的一方"：对比旧/新局面。返回红(1)、黑(0)或 null(无法确定)。
     * 规则：只看"哪里多了子"——新增棋子（目标格）所属阵营即移动方。吃子步必然
     * 同时移除两色棋子（移动方起点子 + 被吃方子），旧逻辑把"移除双色"当异常返回
     * null，走子方就此卡在旧值（是否触发还取决于格子遍历顺序，纯运气；
     * 2026-10-02 实测 10/10 次电脑端拒收皆此因）。移除情况现完全不影响判定，
     * 仅红黑同时新增（识别噪声）或全无新增（目标格新子被误识别为空）才放弃/
     * 保守兜底。
     *
     * 2026-10-02 二次精化：**同色换型不是走子证据**。真实走子的目标格必然
     * "空→有子"或"变色（吃子）"——己方子吃不到己方子上；同色值变（炮→马）
     * 只能是识别误读。此前它被算作该色"新增"，与真实走子方的"新增"冲突返回
     * null，轮次卡旧值→悬浮窗一直显示"轮到对方走"且无箭头（用户被迫点"更新"）。
     * 移除兜底同理只数"真移除"（有子→空），目标格误读成另一色子的格不算移除。
     */
    /**
     * 回合结构防线（2026-11-10 优势崩塌归因，与 Go 端 xq.PlausiblePair 同源对表）：
     * 上一确认局面到候选局面必须能被棋理解释——实测崩塌帧：兑子两回合被合成一帧
     * 但已离场子残留（2 格差="吃自家子"伪非法形状）、三子无落点蒸发（3 格差、
     * 子数 -3）被确认上行后电脑照常出招。返回 null=通过，非空=拒绝原因。
     * 无侧并集判据（确认时轮次未定，Go 端为带侧严格版）：
     *   0 格=同盘（调用方先行相等短路）；1 格=单格残差（目的地漏检/幽灵清理）
     *   放行；2 格=三形态之一（非吃子 消失+出现 / 吃子 消失+换色 / 悔棋还原
     *   出现+换色，pseudoMoves 验证——吃自家子在此穿帮）+同型兑子合并帧（双
     *   消失异色——吃子方+回吃方出发子，接触格 old==new 不产生 diff，兑车最高
     *   频合法形态）；≥3 格=子数差 ≤0 且 ≤格数-1（c 个被吃子对应 c 个互异出发
     *   格+至少 1 个共享落子格），子数增加不可能。
     */
    fun plausiblePair(old: Array<IntArray>, new: Array<IntArray>): String? {
        val vanish = mutableListOf<Pair<Int, Int>>()
        val fill = mutableListOf<Pair<Int, Int>>()
        val swap = mutableListOf<Pair<Int, Int>>()
        for (y in 0 until H) for (x in 0 until W) {
            val o = old[y][x]
            val n = new[y][x]
            if (o == n) continue
            when {
                o != Piece.EMPTY && n == Piece.EMPTY -> vanish.add(x to y)
                o == Piece.EMPTY && n != Piece.EMPTY -> fill.add(x to y)
                else -> swap.add(x to y)
            }
        }
        val cells = vanish.size + fill.size + swap.size
        if (cells >= 3) {
            val delta = fill.size - vanish.size // 子数差（负=净减少）
            if (delta > 0) return "子数 +$delta 不可能（子不会凭空复原）"
            // 每个被吃子对应一个互异的移动方出发格，落子格可共享（兑子回吃全落
            // 同一格）→ cells ≥ 被吃子数 + 1。⚠上界是 cells-1 而非 cells-2：
            // 兑子合并帧（cells=3、子数差 -2）是最高频合法形态，上界错一档会
            // 整批误拒（与 Go 端 PlausiblePair 同源对表）
            if (-delta > cells - 1) return "子数差 $delta 超出 $cells 格变化的上限"
            return null
        }
        if (cells == 2) {
            // 同型兑子合并帧（Go 端带侧严格版同侧 2 格放行形态的无侧镜像）：
            // 双消失异色=吃子方+回吃方出发子（接触格 old==new 不产生 diff）
            if (vanish.size == 2 && fill.isEmpty() && swap.isEmpty() &&
                Piece.isRed(old[vanish[0].second][vanish[0].first]) !=
                Piece.isRed(old[vanish[1].second][vanish[1].first])) {
                return null
            }
            val from: Pair<Int, Int>
            val to: Pair<Int, Int>
            when {
                vanish.size == 1 && fill.size == 1 -> { from = vanish[0]; to = fill[0] } // 非吃子
                vanish.size == 1 && swap.size == 1 -> { from = vanish[0]; to = swap[0] } // 吃子
                fill.size == 1 && swap.size == 1 -> { from = swap[0]; to = fill[0] }     // 悔棋还原
                else -> return "2 格差非走子形状（消${vanish.size} 增${fill.size} 换${swap.size}）"
            }
            // 子型同一性：出发格消失的子必须与落子格出现的子同型（车消失马出现
            // = 子型置换幻影）
            if (new[to.second][to.first] != old[from.second][from.first]) {
                return "2 格差子型置换（出发子与落子不一致）"
            }
            // 隐含走子必须伪合法。⚠pseudoMoves 会把阻挡格也列为目标（含自家子），
            // 吃自家子=幻影帧穿帮点（实测崩塌帧），必须显式验证被吃/还原子与移动方异色
            if (pseudoMoves(old, from.first, from.second).none { it == to }) {
                return "2 格差不构成伪合法走子（疑似幻影/错位）"
            }
            val moverRed = Piece.isRed(old[from.second][from.first])
            val victim: Int = when {
                vanish.size == 1 && swap.size == 1 -> old[to.second][to.first]   // 吃子：被吃子
                fill.size == 1 && swap.size == 1 -> new[swap[0].second][swap[0].first] // 还原：复原子
                else -> Piece.EMPTY                                              // 非吃子：无被吃子
            }
            if (victim != Piece.EMPTY && Piece.isRed(victim) == moverRed) {
                return "吃/还原同色子（幻影帧穿帮形状）"
            }
            return null
        }
        return null // cells == 1 / 0：单格残差与同盘放行（既有语义，与 PC 门对齐）
    }

    /**
     * 应用侧悔棋/还原帧的轮次推导（2026-11-10 结构防线配套）：2 格差恰为
     * "出现+换色、无消失"= 撤销形态（真实走子必含出发格消失）——撤销后由
     * 回撤子重新行棋，轮次=回撤子颜色。非还原形态返回 null（走 movedSide
     * 既有推导）。应用内悔棋是真实用户流：此前还原帧轮次卡旧值（红黑同时
     * 新增 → movedSide null），上行同侧被 PC 结构门拒，重探落 SAME_BOARD
     * 不重发，建议悬置到下一手。
     */
    fun undoRestoreSide(old: Array<IntArray>, new: Array<IntArray>): Boolean? {
        var fill: Pair<Int, Int>? = null
        var swap: Pair<Int, Int>? = null
        for (y in 0 until H) for (x in 0 until W) {
            val o = old[y][x]
            val n = new[y][x]
            if (o == n) continue
            when {
                o == Piece.EMPTY && n != Piece.EMPTY ->
                    if (fill != null) return null else fill = x to y
                o != Piece.EMPTY && n != Piece.EMPTY && o != n ->
                    if (swap != null) return null else swap = x to y
                else -> return null // 任何消失格=非纯还原形态
            }
        }
        if (fill == null || swap == null) return null
        return Piece.isRed(old[swap.second][swap.first])
    }

    fun movedSide(old: Array<IntArray>, new: Array<IntArray>): Int? {
        // 纯 fill 守卫（2026-11-13 轮次事故 03:17:43 根治）：真实走子必含出发格
        // 消失——"空→有子"且无任何消失格的变化（识别漏检子复现）不构成走子证据，
        // 按新增子颜色翻轮次会把漏检恢复误判成"对方走子"（实测：缺兵盘面被确认后
        // 兵复现成 1 格纯 fill，movedSide 判红翻 b，错轮次持续 95.5s 到手动更新）。
        // fill+swap 的悔棋还原形态已在调用点由 undoRestoreSide 先行分流；手动增删
        // 走 manual 显式翻轮次，均不受扰。
        var hasFill = false
        var hasVanish = false
        for (y in 0 until H) for (x in 0 until W) {
            val o = old[y][x]
            val n = new[y][x]
            if (o == Piece.EMPTY && n != Piece.EMPTY) hasFill = true
            if (o != Piece.EMPTY && n == Piece.EMPTY) hasVanish = true
        }
        if (hasFill && !hasVanish) return null
        var addedSide: Int? = null
        for (y in 0 until H) for (x in 0 until W) {
            val o = old[y][x]
            val n = new[y][x]
            if (n == Piece.EMPTY || n == o) continue
            if (o != Piece.EMPTY && Piece.isRed(o) == Piece.isRed(n)) continue
            val s = if (Piece.isRed(n)) 1 else 0
            if (addedSide != null && addedSide != s) return null
            addedSide = s
        }
        if (addedSide != null) return addedSide
        // 无新增子兜底：只数"真移除"（有子→空）。单色真移除按移除方补判，
        // 双色真移除无法定位移动方，放弃（保守卡旧值，交给 PC 校验/下一手自愈）
        var removedSide: Int? = null
        for (y in 0 until H) for (x in 0 until W) {
            val o = old[y][x]
            if (o == Piece.EMPTY || new[y][x] != Piece.EMPTY) continue
            val s = if (Piece.isRed(o)) 1 else 0
            if (removedSide != null && removedSide != s) return null
            removedSide = s
        }
        return removedSide
    }

    /**
     * 单格 (fromX,fromY) 棋子的伪合法落点枚举（仅走法几何：不管将军/照面/胜负，
     * 识别修正不需要棋规级；照面校验由 validate 独立兜底）。
     * 走法规则与渲染器/训练域一致：车四向滑动、马八跳蹩腿、炮平移隔一吃、
     * 帅/将九宫直行一步、仕/士九宫斜一步、相/象田字不过河塞象眼、兵/卒过河前
     * 只进过河可横。
     */
    fun pseudoMoves(p: Array<IntArray>, fx: Int, fy: Int): List<Pair<Int, Int>> {
        val piece = p[fy][fx]
        if (piece == Piece.EMPTY) return emptyList()
        val red = Piece.isRed(piece)
        val out = mutableListOf<Pair<Int, Int>>()
        fun push(x: Int, y: Int) {
            if (x in 0 until W && y in 0 until H) out.add(x to y)
        }
        when (piece) {
            Piece.WJU, Piece.BJU -> {
                for (d in intArrayOf(1, -1)) {
                    var x = fx + d
                    while (x in 0 until W) { push(x, fy); if (p[fy][x] != Piece.EMPTY) break; x += d }
                    var y = fy + d
                    while (y in 0 until H) { push(fx, y); if (p[y][fx] != Piece.EMPTY) break; y += d }
                }
            }
            Piece.WMA, Piece.BMA -> {
                // 八向跳 + 蹩马腿（腿 = 直向一格处）
                val jumps = arrayOf(2 to 1, 2 to -1, -2 to 1, -2 to -1, 1 to 2, 1 to -2, -1 to 2, -1 to -2)
                for ((dx, dy) in jumps) {
                    val legX = fx + if (kotlin.math.abs(dx) == 2) dx / 2 else 0
                    val legY = fy + if (kotlin.math.abs(dy) == 2) dy / 2 else 0
                    if (legX !in 0 until W || legY !in 0 until H) continue
                    if (p[legY][legX] != Piece.EMPTY) continue
                    push(fx + dx, fy + dy)
                }
            }
            Piece.WPAO, Piece.BPAO -> {
                for (d in intArrayOf(1, -1)) {
                    var x = fx + d
                    var jumped = false
                    while (x in 0 until W) {
                        if (p[fy][x] != Piece.EMPTY) {
                            if (!jumped) jumped = true
                            else { push(x, fy); break }   // 隔一子吃
                        } else if (!jumped) push(x, fy)   // 平移
                        x += d
                    }
                    var y = fy + d
                    jumped = false
                    while (y in 0 until H) {
                        if (p[y][fx] != Piece.EMPTY) {
                            if (!jumped) jumped = true
                            else { push(fx, y); break }
                        } else if (!jumped) push(fx, y)
                        y += d
                    }
                }
            }
            Piece.WSHUAI, Piece.BJIANG -> {
                // 九宫直行一步（x 3~5；帅 y 7~9，将 y 0~2）
                for (d in intArrayOf(1, -1)) {
                    val nx = fx + d
                    if (nx in 3..5) {
                        if (red && fy in 7..9) out.add(nx to fy)
                        if (!red && fy in 0..2) out.add(nx to fy)
                    }
                    val ny = fy + d
                    if ((red && ny in 7..9) || (!red && ny in 0..2)) out.add(fx to ny)
                }
            }
            Piece.WSHI, Piece.BSHI -> {
                for (dx in intArrayOf(1, -1)) for (dy in intArrayOf(1, -1)) {
                    val nx = fx + dx; val ny = fy + dy
                    if (nx in 3..5 && ((red && ny in 7..9) || (!red && ny in 0..2))) out.add(nx to ny)
                }
            }
            Piece.WXIANG, Piece.BXIANG -> {
                // 田字不过河（红 y≥5，黑 y≤4）+ 塞象眼
                for (dx in intArrayOf(2, -2)) for (dy in intArrayOf(2, -2)) {
                    val nx = fx + dx; val ny = fy + dy
                    if (nx !in 0 until W || ny !in 0 until H) continue
                    if ((red && ny < 5) || (!red && ny > 4)) continue
                    if (p[fy + dy / 2][fx + dx / 2] != Piece.EMPTY) continue
                    out.add(nx to ny)
                }
            }
            Piece.WBING, Piece.BZU -> {
                val dy = if (red) -1 else 1
                push(fx, fy + dy)
                val crossed = if (red) fy <= 4 else fy >= 5
                if (crossed) { push(fx - 1, fy); push(fx + 1, fy) }
            }
        }
        return out
    }

    /**
     * 单步走子匹配修正（v39，docs/走子连贯性设计与App域解剖-20261017.md §一）：
     * 枚举 old 的全部伪合法走子，取结果与 new 的逐格汉明距离最小者。
     * 返回 Triple(修正后局面, 走子方(1红/0黑), 差异格数)，无 ≤1 差异或歧义返回 null。
     *
     * 保守守门：只认 d==0（识别与某单步结果完全一致，锁定走子方）与 d==1 且
     * **唯一** 达最小距离（恰好 1 格识别错、且只有一步走子能解释——马跳走原地
     * 残留等漏检主形态）。d≥2 或多步等价歧义一律返回 null，调用方维持现状。
     */
    fun repairBySingleMove(old: Array<IntArray>, new: Array<IntArray>): Triple<Array<IntArray>, Int?, Int>? {
        var best = Int.MAX_VALUE
        var bestBoard: Array<IntArray>? = null
        var bestSide: Int? = null
        var bestCount = 0
        for (fy in 0 until H) for (fx in 0 until W) {
            val piece = old[fy][fx]
            if (piece == Piece.EMPTY) continue
            for ((tx, ty) in pseudoMoves(old, fx, fy)) {
                val next = Array(H) { old[it].clone() }
                next[ty][tx] = piece
                next[fy][fx] = Piece.EMPTY
                var d = 0
                loop@ for (y in 0 until H) for (x in 0 until W) {
                    if (next[y][x] != new[y][x]) {
                        d++
                        if (d > best) break@loop   // 剪枝：已劣于当前最优
                    }
                }
                if (d > 1 || d > best) continue
                val side = if (Piece.isRed(piece)) 1 else 0
                if (d < best) {
                    best = d; bestBoard = next; bestSide = side; bestCount = 1
                } else {
                    bestCount++
                    if (bestSide != null && bestSide != side) bestSide = null  // 走子方歧义
                }
            }
        }
        if (bestBoard == null || best > 1) return null
        if (best == 1 && bestCount > 1) return null   // 修正仅在唯一解释时执行
        return Triple(bestBoard, bestSide, best)
    }

    /** 各棋子类型的数量上限（piece 值 → 上限；validate 每次识别都调，Map 提为常量免重复构建+装箱） */
    private val PIECE_LIMITS = mapOf(
        2 to 2, 3 to 2, 4 to 2, 5 to 2, 6 to 2, 7 to 5, 9 to 2, 10 to 2, 11 to 2, 12 to 2, 13 to 2, 14 to 5)

    /** 硬合法性校验：每个阵营的棋子数量必须在合法范围内，帅/将各一。返回问题列表，空表示通过。
     *  完整校验清单（任一不过即整帧作废，交 tracker 走不稳定自愈）：
     *  ① 帅/将各恰 1 ② 各子型不超上限（兵/卒 5，其余每型 2，见 PIECE_LIMITS）
     *  ③ 全盘总子数 ≤ 32 ④ 帅将同列且中间无子即"照面" */
    fun validate(pieces: Array<IntArray>): List<String> {
        val issues = mutableListOf<String>()
        val counts = IntArray(Piece.BZU + 1)
        for (y in 0 until H) for (x in 0 until W) {
            val p = pieces[y][x]
            if (p != Piece.EMPTY) counts[p]++
        }
        if (counts[Piece.WSHUAI] != 1) issues.add("红帅数量异常:${counts[Piece.WSHUAI]}")
        if (counts[Piece.BJIANG] != 1) issues.add("黑将数量异常:${counts[Piece.BJIANG]}")
        for ((k, v) in PIECE_LIMITS) {
            if (counts[k] > v) issues.add("${Piece.getNameByValue(k)}数量超限:${counts[k]}")
        }
        var total = 0
        for (c in counts) total += c
        if (total > 32) issues.add("总子数超限:$total")
        // 兵/卒可达性（2026-11-14 红队第二轮，与 PC fenlegal.go 双端同步）：皮卡鱼
        // 对不可达兵位直接 CRITICAL 退出进程（实测红兵 y=8 秒崩→看门狗重启循环）。
        // 触发面：识别把象/车误判成兵（兵数≤5 过数量门）、手动模式倒摆兵。真实
        // 对局恒满足，零误伤。
        // 红兵起始线 y=6 只进不退：y≥7 不可达；未过河（y=5..6）只能站起始偶数列
        //（横移仅过河后才可能）。黑卒镜像：起始线 y=3；y≤2 不可达；y=3..4 非偶列。
        for (y in 0 until H) for (x in 0 until W) {
            when (pieces[y][x]) {
                Piece.WBING -> if (y >= 7 || (y >= 5 && x % 2 != 0)) {
                    issues.add("红兵位置不可达:($x,$y)")
                }
                Piece.BZU -> if (y <= 2 || (y <= 4 && x % 2 != 0)) {
                    issues.add("黑卒位置不可达:($x,$y)")
                }
            }
        }
        // 两将(帅)照面:同列且中间无子——象棋根本规则。误识别常产出此类非法局面
        // (如把主菜单按钮认成将/帅),此前只被 PC 端拒收,本地却已确认+上行,
        // 状态栏反而报"连接异常"。本地先拦,让 tracker 走不稳定自愈。
        if (counts[Piece.WSHUAI] == 1 && counts[Piece.BJIANG] == 1) {
            var rx = -1; var ry = -1; var bx = -1; var by = -1
            for (y in 0 until H) for (x in 0 until W) {
                when (pieces[y][x]) {
                    Piece.WSHUAI -> { rx = x; ry = y }
                    Piece.BJIANG -> { bx = x; by = y }
                }
            }
            if (rx == bx) {
                var blocked = false
                for (y in minOf(ry, by) + 1 until maxOf(ry, by)) {
                    if (pieces[y][rx] != Piece.EMPTY) { blocked = true; break }
                }
                if (!blocked) issues.add("两将照面(同列 $rx 无隔子)")
            }
        }
        return issues
    }

    /** 与开局对照一致的子数（用于"开局检测"）；开局局面只读缓存，避免每帧重建 */
    private val startBoard = canonicalStart()

    fun matchStartCount(pieces: Array<IntArray>): Int {
        var n = 0
        for (y in 0 until H) for (x in 0 until W) {
            if (pieces[y][x] != Piece.EMPTY && pieces[y][x] == startBoard[y][x]) n++
        }
        return n
    }

    /**
     * NEW_GAME 的开局相似判定（BoardTracker 专用）：完美开局（32/32 逐格一致），
     * 或恰一格差异且该格是开局红车位（y=9 的 x=0/x=8）被识别成红马——覆盖识别
     * 引擎历史遗留的"红车 R→N 错位"模式。2026-11-14 红队第二轮修正：旧判据写死
     * 差异格 (x=7,y=9) 并要求该格为红马——开局该格本就是红马，"该格有差异"与
     * "该格为红马"恒不可同时成立，条件恒假（死分支），承诺的容错从未生效过；
     * 真实错位格是红车位。
     *
     * ⚠不能放宽成"允许任意一格差异"（count==31 && total==32）：单格置换
     * （有子格被另一有子格覆盖，如"车平移吃自己的兵位"）与红车错位在
     * 计数上不可区分，BoardTrackerTest 吃子步用例实证会误判新局。精准钉死
     * 差异格位置与子型是唯一安全的容错方式；错位格被训练修复后应整体删除
     * 此特例。
     */
    fun isStartLike(pieces: Array<IntArray>): Boolean {
        val count = matchStartCount(pieces)
        if (count >= 32) return true
        if (count != 31) return false
        for (y in 0 until H) for (x in 0 until W) {
            if (pieces[y][x] != startBoard[y][x]) {
                return y == 9 && (x == 0 || x == 8) && pieces[y][x] == Piece.WMA
            }
        }
        return false
    }

}
