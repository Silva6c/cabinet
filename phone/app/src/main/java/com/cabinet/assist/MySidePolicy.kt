package com.cabinet.assist

/**
 * 我方执色判定策略（纯 JVM，无 Android 依赖）：
 *
 * 纯随动——"屏幕下方阵营=我方"（识别朝向 STANDARD=红在下=我方执红，FLIPPED=我方
 * 执黑），识别层 orientation 本就按双王位置判定下端子色，随动即时刻以画面为准。
 *
 * 历史：曾有悬浮窗『换边』按钮做手动覆盖（对局 App"换边不翻棋盘"场景的兜底），
 * 但覆盖一旦触发就残留、误触即翻转执色且不自愈（2026-09-30 用户实测反馈"换边按钮
 * 没有任何意义"），已移除按钮与覆盖机制。取舍：换边不翻棋盘的 App 执色会判错，
 * 无手动纠错入口（『更新』仍可纠单步轮次）。
 */
class MySidePolicy {

    /** 我方是否执红：恒等于识别朝向（红在下方=执红） */
    fun mySideIsRed(orientationStandard: Boolean): Boolean = orientationStandard
}
