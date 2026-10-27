package com.cabinet.assist

import android.os.Handler
import android.os.HandlerThread
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.Response
import okhttp3.WebSocket
import okhttp3.WebSocketListener
import okio.ByteString
import org.json.JSONObject
import java.security.SecureRandom
import java.security.cert.X509Certificate
import java.util.concurrent.TimeUnit
import javax.net.ssl.SSLContext
import javax.net.ssl.TrustManager
import javax.net.ssl.X509TrustManager

/**
 * 远程分析客户端（协议 v1，逐字对应 docs/protocol-v1.md）：
 * 手机端作为瘦客户端，把确认局面经 WSS 上行给电脑端"内阁"，
 * 由 PC 并行查开局库/驱动皮卡鱼，回 suggestion（红方视角分数）与 status。
 *
 * 协议要点（§3 握手钉死，PC 先发 hello）：
 *   连接建立 → PC hello → 手机 hello{client:"cabinet-phone",ver,caps} → auth{token}
 *   → PC welcome{session_id,engine:{state},books} → 业务自由流动。
 *   10s 内未完成握手 PC 踢出。
 *
 * 可靠性（§5/§6）：
 *   - 重连指数退避 1s→2s→…→5s 封顶（局域网直连，PC 恢复后平均 3s 内跟上）；
 *     重连成功（welcome）后重发最近 position，
 *     PC 端"最新覆盖"幂等；
 *   - 收 error{not_ready} 后挂起重发标记，待 status{state:"ready"} 自动重发；
 *   - 收 error{auth_failed} 判定令牌错误，停止重连（重连也无意义）。
 *
 * 心跳（§4.7）：PC 每 15s 发 ping，本端必须以原 ts 回 pong；本端每 20s 发一次
 * ping 保活 NAT 并顺带测 RTT。45s 无任何帧 PC 会判定死链断开。
 *
 * TLS（§1 传输层）：LAN 自签证书 + 证书指纹锁定。certFingerprint 非空时校验服务器
 * 链首证书的 SHA-256 摘要，不匹配即握手失败（防中间人替换证书）；为空时信任所有
 * 证书（联调模式，等同 mockphone 的 InsecureSkipVerify）。指纹经配对二维码 f 参数
 * 带外传递（PC 控制台"②连接"区可见）；PC 证书一经生成绝不重建（cert/ 铁律），
 * 重建 = 全部手机需重新配对。
 *
 * 线程模型：OkHttp 回调在其自有线程池，协议帧解析与状态维护经串行 net 线程，
 * 对外回调不做线程保证（调用方 ScreenAssistService 自行 mainHandler 切换）。
 */
class RemoteAnalysisClient(
    private val serverUrl: String,
    private val token: String,
    private val listener: Listener,
    /** 证书 SHA-256 指纹（64 位 hex）；非空 = 锁定校验，空 = 联调模式信任所有证书 */
    private val certFingerprint: String = "",
) {
    interface Listener {
        /** 连接状态变化（状态栏显示） */
        fun onConnectionState(state: ConnectionState)
        /**
         * 连接失败原因（onFailure 分类，中文可读；重连期间每次失败都会回调）。
         * 真机排障关键：超时≈IP 不可达/防火墙拦截，拒绝≈电脑未启动或端口错。
         */
        fun onConnectError(reason: String)
        /** PC 引擎状态（welcome / status 携带）：offline/loading/ready/error */
        fun onEngineState(state: String)
        /**
         * PC 引擎实际算力（welcome 携带，协议 §4.3）：depth/hashMB 均 >0 时回调。
         * 手机端算力按钮的显示与循环起点据此与 PC 对齐（此前显示本地默认档，
         * 出现"手机 0.25g / 电脑 2048"的脱节——2026-10-02 用户反馈）。
         */
        fun onServerEngineConfig(depth: Int, hashMB: Int)
        /** 收到建议（source="book:库名" 或 "engine"；分数为红方视角，可为空=库命中） */
        fun onSuggestion(sug: RemoteSuggestion)
        /** 引擎实时指标（带指标的 status 帧，搜索中 500ms 节流） */
        fun onEngineMetrics(state: String, depth: Int, nodes: Long, nps: Long, hashfull: Int)
        /** 搜索收束：收到仅含 state 的 status 帧（协议 §4.6 定稿语义） */
        fun onSearchSettled()
        /**
         * 协议错误帧（not_ready/auth_failed/bad_position/bad_frame 等），中文可读。
         * code 供端上分类处置（如 bad_position 自动纠正走子方）；fen 为 PC 回显的
         * 触发局面（2026-10-02 起）——调用方必须丢弃 fen 与当前局面不符的迟到旧
         * 错误（否则会错翻新局面轮次）；旧 PC 端不带 fen 时为 null（不过滤）。
         * turnFlipLegal 仅 bad_position 使用：PC 端已验证"翻转走子方后局面合法"，
         * 翻转即正确答案。
         */
        fun onError(code: String, message: String, turnFlipLegal: Boolean, fen: String?)
    }

    enum class ConnectionState { DISCONNECTED, CONNECTING, CONNECTED }

    companion object {
        private const val TAG = "RemoteAnalysis"

        /** 默认 WSS 端口提示（错误文案单点，改端口只改这里） */
        private const val PORT_HINT = "9527"
        /** 协议 v1 能力集与客户端标识（§3 逐字定死，勿改） */
        private const val PROTO_VERSION = 1
        private const val CLIENT_NAME = "cabinet-phone"
        private const val CLIENT_VER = "0.1.0"
        private const val CAP_V1 = "v1"
        /** 客户端保活心跳间隔（协议建议 15–30s） */
        private const val PING_INTERVAL_MS = 20_000L
        /** 重连退避上限（局域网直连重试成本低，5s 封顶保证 PC 恢复后快速跟上） */
        private const val MAX_BACKOFF_MS = 5_000L
        /** 手机诊断日志批量上行间隔（协议 §4.11）：2s 一批，量级每步棋约 10 行 */
        private const val LOG_FLUSH_MS = 2_000L

        /**
         * A5 泄漏修复（2026-10-27）：OkHttpClient 按 (serverUrl, 锁定指纹) 全局
         * 共享。此前每个实例 buildClient() 独建——重配对/重启录屏反复重建客户端
         * 时，旧 client 的连接池与 dispatcher 线程随实例弃用而堆积。client 的
         * 全部 per-实例差异只有 serverUrl（wss 判定）与指纹（TLS 锁定配置），
         * 两者都进 key：key 未变复用同一实例（无状态污染——token 走 auth 帧、
         * listener 在实例侧，均不进 client；超时/ping 间隔是常量）；key 变化
         * （改地址/重新配对切换指纹模式）才重建，旧实例 stop 已 close ws，旧
         * client 连接断开后由 GC 兜底回收（不做 dispatcher.shutdown——若 key
         * 又变回旧值会复用到已 shutdown 的 client，后患大于收益）。
         */
        @Volatile private var sharedClient: OkHttpClient? = null
        @Volatile private var sharedClientKey: String? = null

        private fun sharedClient(serverUrl: String, lockedFp: String): OkHttpClient {
            val key = "$serverUrl\n$lockedFp"
            synchronized(this) {
                if (sharedClient == null || sharedClientKey != key) {
                    sharedClientKey = key
                    sharedClient = buildClient(serverUrl, lockedFp)
                }
                return sharedClient!!
            }
        }

        /**
         * 构建 OkHttpClient（A5 起仅在 companion 内由 sharedClient 按 key 调用，
         * 实例侧不再各自持有——参数化的 serverUrl/lockedFp 即 key 的两个组成项）。
         */
        private fun buildClient(serverUrl: String, lockedFp: String): OkHttpClient {
            val builder = OkHttpClient.Builder()
                // 连接超时 2s：PC 不在时每次尝试快速失败（默认 8s 会把重连周期拖到
                // 退避间隔+8s，PC 恢复后要干等）；局域网直连 refused 本就立即 RST，
                // 2s 只兜住丢包场景
                .connectTimeout(2, TimeUnit.SECONDS)
                .readTimeout(0, TimeUnit.MILLISECONDS) // WebSocket 长连接不设读超时
                // 传输层心跳 10s（2026-10-24）：PC 半死/链路假活时 OkHttp 协议层 ping
                // 10-20s 内感知断开触发重连——此前无任何自主探测，PC 侧 45s 判死关连接
                // 成了手机感知半开的唯一机制（等 TCP 层超时要数分钟）。协议层 pong 由
                // TCP 栈自动应答、不经应用层帧，PC 的 45s 应用层判定零干扰
                .pingInterval(10, TimeUnit.SECONDS)
            if (serverUrl.startsWith("wss") && lockedFp.isEmpty() &&
                com.cabinet.BuildConfig.ENFORCE_TLS_FINGERPRINT
            ) {
                // release 交付包禁止无指纹 trust-all（2026-10-27 规范轮收口）：
                // 不安装信任所有证书的 TrustManager，连接走标准 TLS 校验——
                // 自签证书校验失败 → OkHttp 报错 → 既有 onConnectError 错误链
                // 给出指引。联调模式（trust-all）仅限 debug 构建；正式使用必须
                // 先完成扫码配对写入指纹（走下方指纹锁定分支）
                RLog.w(TAG, "release 构建拒绝无指纹 trust-all 联调模式，请先扫码配对写入指纹")
            }
            if (serverUrl.startsWith("wss")) {
                if (lockedFp.isEmpty()) {
                    // 联调模式（未配指纹）：信任所有 + 主机名不校验，等同 mockphone 的 InsecureSkipVerify
                    // （release 构建已被上方 ENFORCE 分支拦截告警，不会真正建立不安全连接）
                    val tm = object : X509TrustManager {
                        override fun checkClientTrusted(chain: Array<X509Certificate>, authType: String) {}
                        override fun checkServerTrusted(chain: Array<X509Certificate>, authType: String) {}
                        override fun getAcceptedIssuers(): Array<X509Certificate> = arrayOf()
                    }
                    val ssl = SSLContext.getInstance("TLS").apply {
                        init(null, arrayOf<TrustManager>(tm), SecureRandom())
                    }
                    builder.sslSocketFactory(ssl.socketFactory, tm)
                        .hostnameVerifier { _, _ -> true }
                } else {
                    // 指纹锁定：只认链首证书 SHA-256 摘要匹配的证书（自签链仅一张，主机名校验无意义）
                    val fp = lockedFp
                    val tm = object : X509TrustManager {
                        override fun checkClientTrusted(chain: Array<X509Certificate>, authType: String) {}
                        override fun checkServerTrusted(chain: Array<X509Certificate>, authType: String) {
                            if (chain.isEmpty()) {
                                throw java.security.cert.CertificateException("服务器未提供证书")
                            }
                            val got = sha256Hex(chain[0].encoded)
                            if (got != fp) {
                                throw java.security.cert.CertificateException(
                                    "证书指纹不匹配（期望 $fp，实际 $got）——连接可能被劫持，或电脑端重签过证书")
                            }
                        }
                        override fun getAcceptedIssuers(): Array<X509Certificate> = arrayOf()
                    }
                    val ssl = SSLContext.getInstance("TLS").apply {
                        init(null, arrayOf<TrustManager>(tm), SecureRandom())
                    }
                    builder.sslSocketFactory(ssl.socketFactory, tm)
                        .hostnameVerifier { _, _ -> true } // 指纹已锁定证书本体，IP 直连不校验主机名
                }
            }
            return builder.build()
        }

        private fun sha256Hex(bytes: ByteArray): String =
            java.security.MessageDigest.getInstance("SHA-256").digest(bytes)
                .joinToString("") { "%02x".format(it) }
    }

    /** 最近一次上行局面（重连/就绪后重发） */
    @Volatile private var lastPositionFen: String? = null
    /** 最后一条 position 的 force 语义（重连/not_ready 恢复补发时保持） */
    @Volatile private var lastPositionForce = false
    /** 收到 error{not_ready} 后置位；status{state:"ready"} 到达时重发一次并清零。
     *  注意 PC 的搜索收束帧同样带 state:"ready"（§4.6），不能据此重发（会循环搜索）。 */
    @Volatile private var awaitingReady = false

    private val netThread = HandlerThread("remote-analysis").apply { start() }
    private val netHandler = Handler(netThread.looper)

    /**
     * 归一化后的锁定指纹（空 = 联调模式不校验）；格式非法时 fpFormatValid=false 由 connect() 拒绝连接。
     * 必须声明在 client 之前：client 初始化经 sharedClient(serverUrl, lockedFp)
     * 读取本属性（A5 起按此 key 复用 OkHttpClient），而 Kotlin 属性初始化按声明
     * 顺序执行，若放在后面，构造 client 时读到的是未初始化的 null（曾因此启动
     * 即 NPE 闪退）。
     */
    private val lockedFp: String = run {
        val s = certFingerprint.replace(":", "").replace(" ", "").trim().lowercase()
        if (s.isEmpty()) "" else if (s.length == 64 && s.all { it in '0'..'9' || it in 'a'..'f' }) s else ""
    }
    private val fpFormatValid = certFingerprint.isBlank() || lockedFp.isNotEmpty()

    private val client: OkHttpClient = sharedClient(serverUrl, lockedFp)
    /** 当前 WebSocket（B4：OkHttp 回调线程写 connect/scheduleReconnect、net 线程与
     *  stop 调用线程读写，@Volatile 与 state 等跨线程字段对齐） */
    @Volatile private var ws: WebSocket? = null
    @Volatile private var state = ConnectionState.DISCONNECTED
    @Volatile private var started = false
    @Volatile private var authFailed = false
    /** 重连退避间隔（B4：welcome 重置与 scheduleReconnect 读写均在 OkHttp 回调
     *  线程，但与 state 同类跨线程可见性要求，@Volatile 对齐） */
    @Volatile private var backoffMs = 1000L

    private val pingRunnable = object : Runnable {
        override fun run() {
            send(PingFrame(ts = System.currentTimeMillis()))
            netHandler.postDelayed(this, PING_INTERVAL_MS)
        }
    }

    fun start() {
        if (started) return
        started = true
        authFailed = false
        connect()
        scheduleLogFlush()
    }

    /**
     * 手机诊断日志批量上行（协议 §4.11）：每 [LOG_FLUSH_MS] 取走 RLog 积攒的行，
     * 连接就绪才发送——断连期间日志留在环形队列（满则丢最旧），重连后随下一批补上。
     * 纯诊断辅助：失败静默（send 内部已记 logcat），绝不影响对局链路。
     */
    private fun scheduleLogFlush() {
        netHandler.postDelayed({
            if (!started) return@postDelayed
            if (state == ConnectionState.CONNECTED && ws != null) {
                val lines = RLog.drain().toMutableList()
                val dropped = RLog.takeDropped()
                if (dropped > 0) lines.add("…（断连期间队列满，丢弃 $dropped 行）")
                if (lines.isNotEmpty()) send(LogFrame(lines))
            }
            scheduleLogFlush()
        }, LOG_FLUSH_MS)
    }

    /**
     * 停止并释放（服务销毁/重配对重建客户端/连线页测试收尾时调用；不再重连）。
     * ⚠A5：stop 后实例不可复用——net 线程在此退出（quitSafely），此后 start()
     * 与一切 netHandler.post 都会静默失败。现有调用点（ScreenAssistService 的
     * applyConnection/releaseCapture/onDestroy、AssistActivity 测试收尾）均为
     * "stop 后置 null 弃实例"，满足该前提；新增调用方必须遵守。
     */
    fun stop() {
        started = false
        netHandler.removeCallbacksAndMessages(null)
        runCatching { ws?.close(1000, "bye") }
        ws = null
        // A5 泄漏修复（2026-10-27）：HandlerThread 此前启动后永不退出，重配对/
        // 重启录屏每重建一次客户端就泄漏一条线程。quitSafely 允许在途消息跑完再
        // 退出；此后 OkHttp 迟到回调里 scheduleReconnect 的 postDelayed 静默失败
        // （looper 已退出，post 返回 false），started=false 双保险拦截重连
        netThread.quitSafely()
    }

    /**
     * 上行局面（"最新覆盖"：任何时刻只有最新局面有意义）。未连接时仅记录，重连后自动补发。
     * force=true（"更新"按钮）：PC 跳过桔库/沉淀直接引擎重搜（协议 §4.4）——同局面
     * 重发否则被桔库命中短路，多路变招候选拿不回来。重连补发不带 force（PC 重启后
     * 正常查库）。补发时保持最后一条的 force 语义：force 的局面在 lastPositionForce 记忆
     *
     * newGame=true（复盘闭环 2026-10-17）：NEW_GAME 确认后的首条上行带 new_game
     * 标记——PC 端据此生成对局 ID、建议沉淀按局归组（协议 §4.4）。**只该在标记
     * 时刻置一次**：重发/补发（welcome 后、not_ready 恢复）不带标记，否则 PC 会
     * 把同一局拆成多局。旧 PC 忽略未知字段（协议 §7），PC 端不自行猜开局
     * （识别错位容忍语义只在手机端有），本标记是唯一的局边界上行信号。
     */
    fun sendPosition(fen: String, force: Boolean = false, newGame: Boolean = false) {
        lastPositionFen = fen
        lastPositionForce = force
        // 转发完成时刻（真机基准：与 board confirmed、suggestion received 同在
        // logcat 时间轴上，端到端"走子→转发→建议"无需跨设备时钟比对）
        RLog.i(TAG, "position sent: $fen${if (force) " (force)" else ""}${if (newGame) " (new_game)" else ""}")
        send(PositionFrame(fen = fen, redGo = fenRedGo(fen), history = emptyList(), force = force, newGame = newGame))
    }

    /**
     * 上行引擎算力调节（协议 §4.10 set_engine）：只传要改的字段（null = 不改）。
     * PC 端逐字段校验应用；越界回 error{bad_frame}。未连接时静默丢弃（不排队）。
     * threads=0 是合法值（自动档）；multiPV 范围 1–5（协议 §4.10）。
     */
    fun sendSetEngine(depth: Int?, hashMB: Int?, threads: Int? = null, multiPV: Int? = null) {
        send(SetEngineFrame(depth = depth, hashMB = hashMB, threads = threads, multiPV = multiPV))
    }

    // ==================== 连接管理 ====================

    private fun connect() {
        if (!started || authFailed) return
        // 配了指纹但格式非法：拒绝连接并明示（绝不静默降级为不校验——那是虚假安全感）
        if (!fpFormatValid) {
            setState(ConnectionState.DISCONNECTED)
            listener.onConnectError("证书指纹格式非法（应为 64 位 hex，冒号分隔可）——请清空或重新扫码填入")
            return
        }
        setState(ConnectionState.CONNECTING)
        val req = Request.Builder().url(serverUrl).build()
        ws = client.newWebSocket(req, object : WebSocketListener() {
            override fun onOpen(webSocket: WebSocket, response: Response) {
                RLog.i(TAG, "ws open，等待 PC hello（协议 §3）")
            }

            override fun onMessage(webSocket: WebSocket, text: String) {
                try {
                    handleFrame(JSONObject(text), webSocket)
                } catch (t: Throwable) {
                    RLog.w(TAG, "帧处理异常", t)
                }
            }

            override fun onMessage(webSocket: WebSocket, bytes: ByteString) = Unit

            override fun onFailure(webSocket: WebSocket, t: Throwable, response: Response?) {
                RLog.w(TAG, "ws failure: ${t.message}")
                listener.onConnectError(describeConnectFailure(t))
                scheduleReconnect()
            }

            override fun onClosed(webSocket: WebSocket, code: Int, reason: String) {
                RLog.i(TAG, "ws closed: $code $reason")
                scheduleReconnect()
            }
        })
    }

    private fun scheduleReconnect() {
        ws = null
        if (!started || authFailed) {
            if (authFailed) setState(ConnectionState.DISCONNECTED)
            return
        }
        setState(ConnectionState.DISCONNECTED)
        val delay = backoffMs
        backoffMs = (backoffMs * 2).coerceAtMost(MAX_BACKOFF_MS)
        RLog.i(TAG, "将在 ${delay}ms 后重连")
        netHandler.postDelayed({ connect() }, delay)
    }

    private fun setState(s: ConnectionState) {
        if (state != s) {
            state = s
            listener.onConnectionState(s)
        }
    }

    /**
     * 连接失败原因分类（中文可读，真机排障用）。
     * 注意 when 顺序：ConnectException 是 SocketException 子类，必须在前。
     */
    private fun describeConnectFailure(t: Throwable): String = when (t) {
        is java.net.SocketTimeoutException ->
            "连接超时（IP 不可达，或电脑防火墙未放行 $PORT_HINT）"
        is java.net.ConnectException ->
            "连接被拒绝（电脑端未启动，或地址/端口不对）"
        // SSLPeerUnverifiedException 是 SSLException 子类，必须在前：
        // 指纹锁定校验失败时 OkHttp 抛此异常，要给出区别于一般 TLS 错误的指引
        is javax.net.ssl.SSLPeerUnverifiedException ->
            if (t.message?.contains("指纹") == true)
                "证书指纹不匹配（连接可能被劫持，或电脑端重签过证书）——核对电脑端\"②连接\"区指纹，或在电脑端重新扫码"
            else
                "TLS 对端校验失败（${t.message ?: "未知错误"}）"
        is javax.net.ssl.SSLException ->
            "TLS 握手失败（${t.message ?: "证书异常"}）"
        is java.net.UnknownHostException ->
            "地址无法解析（检查电脑地址格式）"
        is java.net.SocketException ->
            "网络不可达（手机与电脑是否在同一 WiFi 网段？）"
        else -> "连接失败（${t.javaClass.simpleName}: ${t.message ?: "未知错误"}）"
    }

    // ==================== 协议帧 ====================

    private fun handleFrame(m: JSONObject, webSocket: WebSocket) {
        when (m.optString("t")) {
            // §3：连接建立后 PC 先发 hello；本端回 hello 并立即 auth（10s 窗口内）
            "hello" -> {
                val hello = JSONObject().apply {
                    put("v", PROTO_VERSION); put("t", "hello")
                    put("client", CLIENT_NAME); put("ver", CLIENT_VER)
                    put("caps", org.json.JSONArray().put(CAP_V1))
                }
                webSocket.send(hello.toString())
                send(AuthFrame(token = token))
            }
            // §3：welcome 到达 = 鉴权通过、会话建立；重连后重发最近 position（最新覆盖幂等）
            "welcome" -> {
                backoffMs = 1000L
                setState(ConnectionState.CONNECTED)
                listener.onEngineState(m.optJSONObject("engine")?.optString("state") ?: "offline")
                // PC 实际算力同步（协议 §4.3，2026-10-02 起）：手机按钮显示与循环
                // 起点改为 PC 真实值；字段缺省（旧 PC）时为 0，调用方按"无效"忽略
                m.optJSONObject("engine")?.let { eng ->
                    val d = eng.optInt("depth")
                    val h = eng.optInt("hash_mb")
                    if (d > 0 || h > 0) listener.onServerEngineConfig(d, h)
                }
                // 心跳是自续链 Runnable：重连成功必须先摘旧链再挂，否则 ping 频率随重连次数成倍增长
                netHandler.removeCallbacks(pingRunnable)
                netHandler.post(pingRunnable)
                lastPositionFen?.let { sendPosition(it, lastPositionForce) }
            }
            "suggestion" -> {
                // 建议回包时刻（真机基准脚本用它对齐"走子→建议刷新"的端到端时间差）
                RLog.i(TAG, "suggestion received: source=${m.optString("source")} move=${m.optString("bestmove")}")
                val score = m.optJSONObject("score")
                val mate = score?.optInt("mate", Int.MIN_VALUE)?.takeIf { it != Int.MIN_VALUE }
                val cp = score?.optInt("cp", Int.MIN_VALUE)?.takeIf { it != Int.MIN_VALUE }
                val pv = mutableListOf<String>()
                m.optJSONArray("pv")?.let { arr ->
                    for (i in 0 until arr.length()) pv.add(arr.optString(i))
                }
                listener.onSuggestion(RemoteSuggestion(
                    source = m.optString("source", "engine"),
                    bestMove = m.optString("bestmove"),
                    scoreCp = cp,
                    scoreMate = mate,
                    pv = pv,
                    depth = m.optInt("depth", 0),
                    fen = m.optString("fen", "").ifEmpty { null },
                    // WDL 胜/和/负（红方视角百分比；PC 端可选下发——字段缺省或 JSON null 均兜底为 null）
                    wdlW = m.optInt("wdl_w").takeIf { m.has("wdl_w") && !m.isNull("wdl_w") },
                    wdlD = m.optInt("wdl_d").takeIf { m.has("wdl_d") && !m.isNull("wdl_d") },
                    wdlL = m.optInt("wdl_l").takeIf { m.has("wdl_l") && !m.isNull("wdl_l") },
                    multipv = m.optInt("multipv").takeIf { m.has("multipv") && !m.isNull("multipv") },
                    // 沉淀库缓存命中标记（协议 §4.5，PC 可选下发）：悬浮窗据此显示
                    // "· 缓存"弱标记且到达即成熟（历史终值无搜索过程）；PC 端
                    // omitempty——字段缺省/旧 PC 不发均 optBoolean 兜底 false
                    cached = m.optBoolean("cached", false),
                ))
            }
            "status" -> {
                val eng = m.optJSONObject("engine")
                val st = eng?.optString("state") ?: "offline"
                listener.onEngineState(st)
                if (eng != null && !eng.has("depth") && !eng.has("nodes")) {
                    // §4.6 收束帧仅含 state（指标字段缺省）→ 通知"搜索已收束"
                    listener.onSearchSettled()
                } else if (eng != null) {
                    listener.onEngineMetrics(
                        st,
                        eng.optInt("depth"),
                        eng.optLong("nodes"),
                        eng.optLong("nps"),
                        eng.optInt("hashfull"),
                    )
                }
                // §6 not_ready 恢复：仅"not_ready 之后的首个 ready"触发一次重发
                if (eng?.optString("state") == "ready" && awaitingReady) {
                    awaitingReady = false
                    lastPositionFen?.let { sendPosition(it, lastPositionForce) }
                }
            }
            // §4.7：PC 心跳必须原样回 pong
            "ping" -> send(PongFrame(ts = m.optLong("ts")))
            // §5：错误码枚举
            "error" -> {
                val code = m.optString("code")
                val msg = m.optString("message", code)
                RLog.w(TAG, "server error: $code $msg")
                if (code == "auth_failed") {
                    // 令牌错误：重连无意义，停止并提示（区分于网络断连）
                    authFailed = true
                    setState(ConnectionState.DISCONNECTED)
                }
                if (code == "not_ready") {
                    // §6：PC 引擎未就绪，挂起重发标记；status{state:"ready"} 到达时自动补发
                    awaitingReady = true
                }
                listener.onError(code, msg.ifEmpty { "服务器错误：$code" },
                    m.optBoolean("turn_flip_legal", false),
                    // fen 回显（2026-10-02 起）：调用方据此丢弃迟到旧错误
                    m.optString("fen", "").ifEmpty { null })
            }
            "bye" -> RLog.i(TAG, "对端 bye")
            else -> { /* 未知类型按协议忽略 */ }
        }
    }

    private fun send(any: Any) {
        val socket = ws ?: return
        if (!started) return
        // 业务帧等 welcome 再放行：TCP 已 open 但握手（hello+auth+welcome）未完成时，
        // PC 对 position/set_engine 回 error{auth_required}（2026-10-27 局3 实录：服务
        // 重启瞬间恢复的 pending position 在 ws open 后、welcome 前发出，控制台/悬浮窗
        // 闪一次"请先完成握手"）。welcome 处理器会用 lastPositionFen 自动补发，丢弃无损
        if ((any is PositionFrame || any is SetEngineFrame) && state != ConnectionState.CONNECTED) return
        if (!socket.send(toJson(any).toString())) {
            RLog.w(TAG, "发送失败（连接已断），等待重连")
        }
    }

    private fun toJson(any: Any): JSONObject = when (any) {
        is PingFrame -> JSONObject().put("v", PROTO_VERSION).put("t", "ping").put("ts", any.ts)
        is PongFrame -> JSONObject().put("v", PROTO_VERSION).put("t", "pong").put("ts", any.ts)
        is AuthFrame -> JSONObject().put("v", PROTO_VERSION).put("t", "auth").put("token", any.token)
        is PositionFrame -> JSONObject()
            .put("v", PROTO_VERSION).put("t", "position")
            .put("fen", any.fen).put("red_go", any.redGo)
            .put("history", org.json.JSONArray(any.history))
            .apply {
                if (any.force) put("force", true)
                // 新局标记仅 true 时出现（omitempty 语义，与 PC 端解码对齐；
                // 旧 PC 忽略未知字段，协议 §7）
                if (any.newGame) put("new_game", true)
            }
        // 手机诊断日志上行（协议 §4.11）：lines 为端上预格式化行，PC 原样落盘
        is LogFrame -> JSONObject().put("v", PROTO_VERSION).put("t", "log")
            .put("lines", org.json.JSONArray(any.lines))
        is SetEngineFrame -> JSONObject().apply {
            put("v", PROTO_VERSION); put("t", "set_engine")
            if (any.depth != null) put("depth", any.depth)
            if (any.hashMB != null) put("hash_mb", any.hashMB)
            // 线程/MultiPV（协议 §4.10，PC 端校验 threads 0/1–64、multi_pv 1–5）；
            // threads=0 合法（自动档）。旧 PC 端忽略新增字段（协议 §7）
            if (any.threads != null) put("threads", any.threads)
            if (any.multiPV != null) put("multi_pv", any.multiPV)
        }
        else -> throw IllegalArgumentException("未知帧类型")
    }

    private fun fenRedGo(fen: String): Boolean =
        fen.split(" ").getOrNull(1)?.let { it != "b" } ?: true

    // ==================== TLS（指纹锁定 / 联调模式） ====================
    // A5 起 buildClient/sha256Hex 迁入 companion（sharedClient keyed 单例复用），
    // 本段仅余指纹归一化（lockedFp，见字段声明处）与格式校验（fpFormatValid）。
}

/** PC → 手机 suggestion（§4.5）。score 为红方视角（PC 已换算）：cp 正=红优，mate 正=红将杀。 */
class RemoteSuggestion(
    val source: String,          // "book:<库名>" 或 "engine"
    val bestMove: String,        // UCCI 4 字符
    val scoreCp: Int?,
    val scoreMate: Int?,
    val pv: List<String>,
    val depth: Int,
    /** 本建议对应的局面（2026-10-02 起 PC 回显；旧 PC 无此字段时为 null）——
     *  调用方据此丢弃迟到旧建议，防止旧建议对着新棋盘误报"轮次可能识别有误" */
    val fen: String? = null,
    /** 红方视角胜/和/负概率（百分比 0-100；引擎未输出 WDL 时为 null） */
    val wdlW: Int? = null,
    val wdlD: Int? = null,
    val wdlL: Int? = null,
    /** MultiPV 线序号（1=最优线；桔库/沉淀命中或旧 PC 无此字段时为 null，按槽位 1 处理） */
    val multipv: Int? = null,
    /** 沉淀库缓存命中（同局面历史终值直接回放，未进引擎现算）；旧 PC 无此字段时为 false。
     *  注意与 fromBook 独立：沉淀记录的 source 可能是 "book:<名>"，不能靠 fromBook 判缓存 */
    val cached: Boolean = false,
) {
    val fromBook: Boolean get() = source.startsWith("book:")
    val bookName: String get() = if (fromBook) source.removePrefix("book:") else ""
}

// 协议帧（仅本端发送的类型）
private data class PingFrame(val ts: Long)
private data class PongFrame(val ts: Long)
private data class AuthFrame(val token: String)
private data class LogFrame(val lines: List<String>)
private data class PositionFrame(
    val fen: String, val redGo: Boolean, val history: List<String>,
    val force: Boolean = false,
    /** 新局标记（2026-10-17 复盘闭环）：NEW_GAME 确认后的首帧置位——PC 据此
     *  开启新对局 ID，建议沉淀按局归组。旧 PC 忽略未知字段（协议 §7） */
    val newGame: Boolean = false)
private data class SetEngineFrame(
    val depth: Int?, val hashMB: Int?,
    val threads: Int? = null, val multiPV: Int? = null)
