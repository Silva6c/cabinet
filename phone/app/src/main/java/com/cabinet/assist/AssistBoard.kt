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
    fun movedSide(old: Array<IntArray>, new: Array<IntArray>): Int? {
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
     * 或恰一格差异且该格恰是开局红车位 (9,7) 被识别成红马——后者覆盖当前
     * 识别引擎的顽固 (9,7) R→N 错位（历史遗留错位模式）。
     *
     * ⚠不能放宽成"允许任意一格差异"（count==31 && total==32）：单格置换
     * （有子格被另一有子格覆盖，如"车平移吃自己的兵位"）与 (9,7) 错位在
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
                return y == 9 && x == 7 && pieces[y][x] == Piece.WMA
            }
        }
        return false
    }

}
