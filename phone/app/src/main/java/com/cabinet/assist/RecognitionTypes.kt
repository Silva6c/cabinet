package com.cabinet.assist

import kotlin.math.abs

/**
 * 识别管线的共享类型（纯 JVM，无 Android 依赖）：
 * 帧输入、朝向、单帧识别结果、稳定帧跟踪器。
 */
class Frame(val width: Int, val height: Int, val argb: IntArray,
            /** 源录屏帧时间戳（Image.timestamp，纳秒；0 = 测试/重放构造）：
             *  走子事件帧判重用——同一合成帧不重复整屏解码 */
            val srcTs: Long = 0L) {
    init {
        // 像素数必须严格等于 w*h：Frame 由 ImageReader 逐行拷贝组装，一旦行跨度/
        // 尺寸传错，识别层只会看到"整体错位"的画面而无声出错——这里 fail-fast，
        // 宁可当帧抛异常也不让坏帧流进管线（warp/letterbox 均假定像素紧凑排列）
        require(argb.size == width * height) { "frame pixel count mismatch" }
    }
}

enum class Orientation {
    /** 红方在下（标准朝向，屏幕下方阵营=红方） */
    STANDARD,
    /** 红方在上（换边/翻转朝向，屏幕下方阵营=黑方） */
    FLIPPED;
}

/**
 * 一帧识别结果。canonical 为项目内部约定（红恒定在 y=9），orientation 表示屏幕朝向。
 */
class RecognitionResult(
    val canonical: Array<IntArray>,
    val orientation: Orientation,
    val issues: List<String>,
    val recognizedPieces: Int,
) {
    fun isValid(): Boolean = issues.isEmpty()
}

/**
 * 稳定帧跟踪器：连续 confirmCount 帧出现同一局面才确认，并推断轮次/检测重开。
 * @param singleMoveRepair v39 走子连贯性开关（设计见姊妹仓 xiangqi-recognition 的
 *   docs/走子连贯性设计与App域解剖-20261017.md）：
 *   确认时刻做"单步走子匹配"——枚举旧局面的全部伪合法走子，d==0 直接锁定走子方，
 *   恰好差 1 格且唯一走子可解释则修正后确认（漏检主形态"马跳走原地残留"被规则修复）；
 *   d≥2 或歧义维持现状，12 帧自愈继续兜底。默认开。
 * @param singleMoveEarlyConfirm 单步几何提前确认（2026-11-09"停在上一帧"治理，
 *   默认开）：候选仅 1 帧且与上一确认局面构成"唯一单步走子且识别逐格完全一致"
 *   （repairBySingleMove d==0）即确认，不等 confirmCount——对手回手击穿确认窗口
 *   导致的两步合并（我方候选被覆盖、中间局面永失）从窗口上收口。
 */
class BoardTracker(
    private val confirmCount: Int = 3,
    private val singleMoveRepair: Boolean = true,
    private val singleMoveEarlyConfirm: Boolean = true,
) {

    enum class Event {
        /** 稳定新局确认 */
        NEW_BOARD,
        /** 与当前确认局面相同（未变化） */
        SAME_BOARD,
        /** 检测到新对局（回到开局） */
        NEW_GAME,
        /** 帧不稳定/不合法，等待后续 */
        UNSTABLE,
    }

    /** 当前确认局面 */
    var confirmed: RecognitionResult? = null
        private set

    /** 红方走子方（true=红方走）；worker 写、主线程读（悔棋/更新棋局按钮） */
    @Volatile var redGo = true

    /** 连续未确认帧数（用于提示）；worker 写、主线程读 */
    @Volatile var unstableStreak = 0
        private set

    /**
     * 候选累计中：已识别到与确认局面不同的新局面、但尚未满 confirmCount 连续帧。
     * frameTick 据此重放补帧——点选式走子（无动画帧）时画面变化标志会被单次识别
     * 消费，若漏判此状态且画面静止，确认链将永久饿死（实测：对弈 App 即时落子后
     * 识别停在旧局面、两次识别恰好夹着对方回手，各只累计 1 帧）。
     * worker 写、主线程读。
     */
    @Volatile var confirming: Boolean = false
    /**
     * 最近一次回合结构门拒绝原因（2026-11-10 结构防线，null=无在途拒绝）：
     * 服务层据此在变化时留痕——结构门拒绝是静默的（等治愈/自愈重探），没有
     * 这行日志就无法区分"识别抖动"与"门拦住了坏盘面"。确认/同盘时清空。
     */
    @Volatile var lastGateReject: String? = null
        private set

    /**
     * 最近一次确认所走的确认路径（排障用，2026-11-12 确认链决策日志）：early=
     * 单步几何提前确认 / main=confirmCount 主路径 / first=首次确认（启动或 12 帧
     * 自愈后）/ newgame=重开检测。服务层在 board confirmed 日志里带上此值——
     * 此前确认链决策无日志，幻影 prev 归因（滑行动画切片 vs 识别振荡 vs 上游
     * 丢帧）全程只能靠逐帧反推，排障成本过高。worker 线程写读，@Volatile 与
     * 相邻跨线程可读字段对齐。
     */
    @Volatile var lastConfirmPath: String? = null
        private set

    /**
     * 滑行候选累计中（2026-11-13"发呆期"体感治理）：车/炮长滑 ≥2 格的候选被
     * 滑行防线拦下等待停稳帧期间置位（候选被覆盖/确认/回 SAME_BOARD 清零）——
     * 悬浮窗据此显示"对方走子中…"消除视觉发呆。⚠不可复用 confirming：门拒期
     * 候选保留且 hits 可能 ≥confirmCount，confirming 恒 false。worker 写读，
     * @Volatile 供主线程渲染。
     */
    @Volatile var slidingCandidate: Boolean = false
        private set

    /** 门拒逃生口状态：同一 prev 连续被结构门拒的计数与锚定盘面（见 noteGateReject） */
    private var gateRejectStreak = 0
    private var gateRejectPrev: Array<IntArray>? = null

    /**
     * 12 帧自愈丢弃旧确认的标志：自愈丢弃 confirmed 重新走首次确认流程后，
     * 若重新确认出的局面与自愈前相同，FEN 不变会被服务层 lastSentFen 去重闸
     * 拦死上行——PC 不重推建议，悬浮窗建议区持续空白。服务层在确认链终点
     * （onBoardConfirmed）消费本标志并清 lastSentFen，与 forceFullRelocate
     * 同款"重读结果即使 FEN 相同也要重发对账"语义。置位（onFrameInner）与
     * 消费均在 worker 线程串行，无并发窗口；@Volatile 与其余跨线程可读字段对齐
     * （主线程诊断读取不受线程限制）。
     */
    @Volatile var staleConfirmationDiscarded = false
        private set

    private var candidate: RecognitionResult? = null
    private var candidateHits = 0

    /** 消费自愈标志：置位即返回 true 并清零（服务层据此清 lastSentFen 对账重发） */
    fun consumeStaleConfirmationDiscarded(): Boolean {
        val v = staleConfirmationDiscarded
        staleConfirmationDiscarded = false
        return v
    }

    /**
     * 门拒记账+逃生口（2026-11-13 发呆期治理）：同一 prev 连续 3 次被结构门拒
     * = confirmed 大概率是识别坏盘面（漏检/错位），继续等只会走满 12 帧通用自愈
     * （实测合并帧场景发呆 12.5s，phone-20261113-025645-局2.log:590-640）——
     * 直接丢弃 confirmed 走首次确认路径（候选与 redGo 保留，真盘面 confirmCount
     * 帧即确认）。幻影提前上行无害：PC 端结构门对同一 prev 连续第 2 次拒即按
     * 识别自愈放行（2026-11-12），不比现状更不安全。返回 true=已触发逃生
     * （confirmed 已置 null，下一帧走 prev==null 分支）。
     */
    private fun noteGateReject(prev: RecognitionResult): Boolean {
        val anchored = gateRejectPrev
        val same = anchored != null && AssistBoard.equal(prev.canonical, anchored)
        gateRejectStreak = if (same) gateRejectStreak + 1 else 1
        gateRejectPrev = prev.canonical
        if (gateRejectStreak < 3) return false
        confirmed = null
        staleConfirmationDiscarded = true
        gateRejectStreak = 0
        gateRejectPrev = null
        unstableStreak = 0
        slidingCandidate = false
        return true
    }

    /** 门拒计数复位：确认成功/回同盘/跟踪状态重建时调用（拒绝链不跨局面延续） */
    private fun clearGateRejectState() {
        gateRejectStreak = 0
        gateRejectPrev = null
    }

    // ---- B1 轮次恢复素材（2026-10-24）：进程重启前最后上行的局面 ----
    // 纯盘面 + 轮次，首次确认消费后即清；不持久化在 tracker 内（由服务层从
    // AssistConfig 注入，tracker 保持纯 JVM 无 Android 依赖）。
    private var restoreBoard: Array<IntArray>? = null
    private var restoreRedGo = true

    /**
     * 注入持久化恢复素材（进程启动时调用一次）：中局面首次确认时用于恢复走子方
     * ——重启后 redGo 恒 true，稳态局面 PC 校验拦不住，错轮次会让建议被端上
     * myTurn 门禁全部压掉（"重启后永远没建议"）。FEN 解析失败静默放弃。
     */
    fun setRestoreHint(fen: String) {
        val board = AssistBoard.fromFenBoard(fen) ?: return
        val parts = fen.split(" ")
        restoreBoard = board
        restoreRedGo = parts.size > 1 && parts[1] == "w"
    }

    fun reset(redGoFirst: Boolean = true) {
        lastGateReject = null
        confirmed = null
        candidate = null
        candidateHits = 0
        unstableStreak = 0
        confirming = false
        clearGateRejectState()
        slidingCandidate = false
        // 外部重置（引擎切换）= 跟踪状态整体重建，悬置的自愈标志一并作废——
        // 否则标志会在后续无关的确认里多触发一次重发（幂等无害，但状态不自洽）
        staleConfirmationDiscarded = false
        redGo = redGoFirst
    }

    /** 悔棋：恢复到历史局面（以给定结果与轮次重建跟踪状态，后续帧可正常重新确认） */
    fun restore(result: RecognitionResult, redGoSide: Boolean) {
        lastGateReject = null
        confirmed = result
        redGo = redGoSide
        candidate = null
        candidateHits = 0
        unstableStreak = 0
        confirming = false
        clearGateRejectState()
        slidingCandidate = false
        staleConfirmationDiscarded = false // 同 reset：悔棋重建跟踪，自愈历史作废
    }

    fun onFrame(res: RecognitionResult): Event {
        val e = onFrameInner(res)
        confirming = candidate != null && candidateHits < confirmCount
        return e
    }

    private fun onFrameInner(res: RecognitionResult): Event {
        // 长期不稳定自愈：已确认局面过时后（换局/识别漂移/幽灵局面被误确认），
        // 子数跳变门限会把真实新局面永久拒之门外——每帧 candidate 被清、UNSTABLE
        // 无限持续、建议被 recHealthy 抑制，用户看到的就是"卡住不动"。
        // 连续 12 帧（慢路径约 15 秒）未确认即丢弃旧确认，重新走首次确认流程
        // （仍需连续 confirmCount 帧一致，瞬时坏帧/幽灵帧难以蒙混）。
        if (unstableStreak >= 12) {
            confirmed = null
            candidate = null
            candidateHits = 0
            unstableStreak = 0 // 同步清零:只复位一次,否则下一帧再次清掉正在累计的候选
            // 旧确认已丢弃：服务层确认后需清 lastSentFen 对账重发（见字段注）
            staleConfirmationDiscarded = true
        }
        val prev = confirmed
        // 坏帧不清候选：isValid=false 直接返回，candidate/candidateHits 原样保留——
        // "连续 confirmCount 帧"只对有效帧计数，瞬时遮挡/动画坏帧不打断好帧的累计；
        // 只有"有效但内容不同"的帧才会重置候选。若在此清候选，偶发一帧坏帧就会
        // 让走子确认永远差一口气。
        if (!res.isValid()) {
            unstableStreak++
            return Event.UNSTABLE
        }

        if (prev == null) {
            // 首次确认：连续 confirmCount 帧完全一致才确认（瞬时坏帧/幻觉帧很难
            // 连续多帧逐格一致）；确认时与开局完全一致才视为全新对局
            if (candidate != null && AssistBoard.equal(res.canonical, candidate!!.canonical)) {
                candidateHits++
            } else {
                candidate = res
                candidateHits = 1
            }
            if (candidateHits >= confirmCount) {
                confirmed = candidate!!
                lastConfirmPath = "first"
                val first = candidate!!
                candidate = null
                candidateHits = 0
                unstableStreak = 0
                slidingCandidate = false
                clearGateRejectState()
                // 容错：(9,7) 顽固错位与吃子局面的区分见 AssistBoard.isStartLike
                val isNewGame = AssistBoard.isStartLike(first.canonical)
                if (isNewGame) {
                    // 与下方"重开检测"路径对齐：开局=新对局红先。此路径多发生在
                    // 12 帧自愈之后，redGo 若保留上一局残局的旧值，新局会以错误
                    // 轮次上行（2026-10-01 实测链路的一环）
                    redGo = true
                    restoreBoard = null // 开局天然红先，恢复素材作废
                } else {
                    // B1 轮次恢复（2026-10-24）：中局面首次确认时按持久化素材恢复
                    // 走子方——盘面全等直接恢复；差 1 格（停机期间走了一步）用
                    // movedSide 推断（持久化局面是上一手之前，移动方已走 → 轮对方，
                    // 与确认链 NEW_BOARD 分支的差分推断同构）；差 ≥2（停机期间走了多步/换局）
                    // 或素材异常维持现状，bad_position 回环兜底。一次性消费——
                    // 12 帧自愈走回首次确认时素材已清，redGo 保留（自愈不清轮次）
                    restoreBoard?.let { hintBoard ->
                        restoreBoard = null
                        val moved = AssistBoard.movedSide(hintBoard, first.canonical)
                        when {
                            AssistBoard.equal(first.canonical, hintBoard) -> redGo = restoreRedGo
                            moved != null -> redGo = moved != 1
                            // else：无法推断，保持默认
                        }
                    }
                }
                lastGateReject = null
                return if (isNewGame) Event.NEW_GAME else Event.NEW_BOARD
            }
            unstableStreak++
            return Event.UNSTABLE
        }

        // 重开检测：与开局完全一致（32 子）视为新对局（换局可能伴随大幅子数变化，
        // 因此放在子数跳变门限之前）
        // 重开检测：开局相似（(9,7) 错位容错，依据同上）且非当前确认局面
        // （换局可能伴随大幅子数变化，因此放在子数跳变门限之前）
        if (AssistBoard.isStartLike(res.canonical) && !AssistBoard.equal(res.canonical, prev.canonical)) {
            confirmed = res
            lastConfirmPath = "newgame"
            redGo = true
            candidate = null
            candidateHits = 0
            unstableStreak = 0
            lastGateReject = null
            clearGateRejectState()
            slidingCandidate = false
            return Event.NEW_GAME
        }

        // 子数跳变门限：真实走子只 ±1（吃子 -1），检测噪声通常 ±2~3；与已确认局面
        // 相差过大只可能是局部遮挡/动画/幻觉帧，拒绝（换局走上面的重开检测）
        val tol = maxOf(2, prev.recognizedPieces / 10)
        if (abs(res.recognizedPieces - prev.recognizedPieces) > tol) {
            candidate = null
            candidateHits = 0
            unstableStreak++
            return Event.UNSTABLE
        }

        val same = AssistBoard.equal(res.canonical, prev.canonical)
        if (same) {
            candidate = null
            candidateHits = 0
            unstableStreak = 0
            lastGateReject = null
            clearGateRejectState()
            slidingCandidate = false
            return Event.SAME_BOARD
        }

        // 与候选一致则累计
        if (candidate != null && AssistBoard.equal(res.canonical, candidate!!.canonical)) {
            candidateHits++
        } else {
            candidate = res
            candidateHits = 1
            slidingCandidate = false // 候选被新内容覆盖（长滑候选由下方检测重新置位）
        }
        // 单步几何提前确认（2026-11-09"停在上一帧"治理，见 singleMoveEarlyConfirm
        // KDoc）：候选仅 1 帧、与上一确认局面构成唯一单步且逐格完全一致（d==0）
        // → 立即确认。repairBySingleMove 的唯一性守门（多步等价歧义返回 null）+
        // burst 节拍只采停稳帧，幻觉帧恰好构成唯一合法单步的概率≈0；d==1 残差
        // 不走此径（单帧+残差误确认风险大于收益）。确认后的走子方锁定与
        // candidateHits>=confirmCount 主路径同源（repairBySingleMove 的 side）
        if (singleMoveEarlyConfirm && candidateHits == 1 && confirmCount > 1) {
            val rep = AssistBoard.repairBySingleMove(prev.canonical, candidate!!.canonical)
            if (rep != null && rep.third == 0) {
                // 滑行动画切片防线（2026-11-12 归因，实测 13:24:40 黑车 4 格长滑被
                // 切成两帧确认）：车/炮直线滑行 ≥2 格时 burst 可能在动画中途采到
                // 过路帧——过路帧对上一确认局面恰构成唯一单步解释（d==0）被 1 帧
                // 提前确认，一个动画就此切成两个"确认局面"：幻影 prev 进 PC 且
                // 引擎照算（低质量指挥），真实终帧变成"又一次同侧走子"撞结构门，
                // 断供到手动干预。直线滑行 ≥2 格禁用提前确认回落主路径（等停稳
                // 帧）；无滑行中间帧的走子（相邻格直行/马跳/仕相斜步）不受扰。
                // d==0 保证差异恰 2 格（一消一现/一消一换），from/to 按 diff 顺序
                // 取即可，曼哈顿距离对称无需区分方向。
                var fx = -1; var fy = -1; var tx = -1; var ty = -1
                var diffCells = 0
                for (y in 0 until AssistBoard.H) for (x in 0 until AssistBoard.W) {
                    if (prev.canonical[y][x] != candidate!!.canonical[y][x]) {
                        if (diffCells == 0) { fx = x; fy = y } else { tx = x; ty = y }
                        diffCells++
                    }
                }
                val dx = abs(tx - fx)
                val dy = abs(ty - fy)
                val longSlide = diffCells == 2 && ((dx == 0) != (dy == 0)) && maxOf(dx, dy) >= 2
                if (longSlide) {
                    // 长滑候选等待停稳帧：悬浮窗显示"对方走子中…"（发呆期体感治理）
                    slidingCandidate = true
                } else {
                    val (_, side, _) = rep
                    // 移动子必须与预期行棋方（redGo）一致才提前确认：不满足轮次语义
                    // 的 2 格跳变更可能是识别错位幻觉（单帧幻觉恰好构成唯一合法单步
                    // 的残余风险由此收窄）；错侧真走子（prev 侧卡旧值形态）回落
                    // confirmCount 主路径，repair 锁侧照常纠正，仅少一次加速
                    if (side != null && (side == 1) == redGo) {
                        // 回合结构门同样适用（⚠pseudoMoves 会把阻挡格列为目标，吃
                        // 自家子可被 repair 枚举成 d==0 解释——幻影帧由此漏进提前
                        // 确认；拒绝路径不改写 redGo）
                        val gateReject = AssistBoard.plausiblePair(prev.canonical, candidate!!.canonical)
                        if (gateReject != null) {
                            lastGateReject = gateReject
                            noteGateReject(prev)
                            unstableStreak++
                            return Event.UNSTABLE
                        }
                        lastGateReject = null
                        redGo = side != 1 // 移动方是红 => 下一手是黑；移动方是黑 => 下一手是红
                        confirmed = candidate!!
                        lastConfirmPath = "early"
                        candidate = null
                        candidateHits = 0
                        unstableStreak = 0
                        slidingCandidate = false
                        clearGateRejectState()
                        return Event.NEW_BOARD
                    }
                }
            }
        }
        if (candidateHits >= confirmCount) {
            val newBoard = candidate!!
            var toConfirm = newBoard
            // v39 走子连贯性：确认时刻做单步走子匹配。d==0（识别与唯一单步结果完全
            // 一致）锁定走子方；恰好差 1 格且唯一走子可解释 → 修正后确认（漏检帧
            // "马跳走原地残留"等被规则直接修复）。d≥2/歧义/开关关 = 行为与旧版一致。
            var repairedSide: Int? = null
            if (singleMoveRepair) {
                val rep = AssistBoard.repairBySingleMove(prev.canonical, newBoard.canonical)
                if (rep != null) {
                    val (fixed, side, d) = rep
                    if (d == 1) {
                        toConfirm = RecognitionResult(
                            fixed, newBoard.orientation, newBoard.issues,
                            AssistBoard.countPieces(fixed))
                    }
                    repairedSide = side
                }
            }
            // 回合结构防线（2026-11-10 优势崩塌归因，与 Go 端 xq.PlausiblePair
            // 同源对表，判据见 AssistBoard.plausiblePair）：不可能局面（幻影/
            // 错位/多子蒸发）不确认——等下一真实帧覆盖治愈或 12 帧自愈（自愈
            // 路径不过门：遮挡恢复的大跳变靠它，行为不变）；确定性误读由服务
            // 层结构门回 bad_frame 触发自愈重探。拒绝不重置候选：同一坏帧持续
            // 到来自然滑向自愈，真实帧到达则候选被覆盖治愈。⚠门在侧锁之前：
            // 拒绝路径不得改写 redGo（未确认=状态未变）。
            val gateReject = AssistBoard.plausiblePair(prev.canonical, toConfirm.canonical)
            if (gateReject != null) {
                lastGateReject = gateReject
                noteGateReject(prev)
                unstableStreak++
                return Event.UNSTABLE
            }
            lastGateReject = null
            // 应用侧悔棋/还原帧优先（确定性推导，与 repair 无关）；其余走
            // repair 锁侧 + movedSide 既有链
            val undoSide = AssistBoard.undoRestoreSide(prev.canonical, toConfirm.canonical)
            if (undoSide != null) {
                redGo = undoSide
            } else {
                if (repairedSide != null) {
                    redGo = repairedSide != 1
                }
                val moved = AssistBoard.movedSide(prev.canonical, toConfirm.canonical)
                if (moved != null) {
                    redGo = moved != 1 // 移动方是红 => 下一手是黑；移动方是黑 => 下一手是红
                }
            }
            confirmed = toConfirm
            lastConfirmPath = "main"
            candidate = null
            candidateHits = 0
            unstableStreak = 0
            slidingCandidate = false
            clearGateRejectState()
            return Event.NEW_BOARD
        }
        unstableStreak++
        return Event.UNSTABLE
    }
}
