package com.cabinet.assist

import com.cabinet.BuildConfig
import okhttp3.Request
import okhttp3.Response
import okhttp3.WebSocket
import okhttp3.WebSocketListener
import okhttp3.mockwebserver.MockResponse
import okhttp3.mockwebserver.MockWebServer
import okhttp3.tls.HandshakeCertificates
import okhttp3.tls.HeldCertificate
import org.json.JSONObject
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import java.security.MessageDigest
import java.util.concurrent.ConcurrentLinkedQueue
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicInteger

/**
 * RemoteAnalysisClient 协议层 JVM 单测（MockWebServer 全链路，2026-10-27 挂账落地）。
 *
 * 覆盖（帧形态一律按 docs/protocol-v1.md 示例逐字构造，字段名对表锚点）：
 * - §3/§4.1 握手：PC 先发 hello → 手机按序回 hello（client/ver/caps/设备自报字段）
 *   与 auth（token）；
 * - §4.3/§4.5/§4.6/§4.8/§5 下行帧解析分发：welcome（引擎状态+算力同步）、
 *   suggestion（引擎/将杀/桔库/云库四种形态）、status（带指标/仅 state 收束）、
 *   error（bad_position 的 fen 回显与 turn_flip_legal、auth_failed 停止重连、
 *   not_ready 挂起重发标记）、ping→pong 原 ts 回显；
 * - §1 传输层证书指纹锁定：运行时自签证书（okhttp-tls）真 TLS 握手——指纹匹配
 *   可完成协议交换、不匹配在握手层拒绝且不上行任何帧；指纹归一化（大写/冒号
 *   分隔）与格式非法拒绝路径；
 * - §5/§6 重连退避：1s 起步翻倍至 5s 封顶、welcome 重置（反射直测，原因见下）。
 *
 * 线程模型与可测性说明（为什么这样测）：
 * - 客户端构造即创建 HandlerThread/Handler，纯 JVM 单测经 testOptions 的
 *   returnDefaultValues 退化为空操作（looper=null、postDelayed 恒 false）——
 *   本测试因此不存在真实定时器：重连/心跳不会被实际调度，无等待副作用；
 * - 异步推进全靠 OkHttp 自有线程池 + MockWebServer 线程，断言统一轮询等待
 *   （awaitCond，10s 超时兜底 CI 慢机）；
 * - 重连退避无法端到端观测（postDelayed 不执行），改为反射直测私有字段
 *   backoffMs 的演进与 welcome 重置点——算法本体（翻倍封顶）由此锁死；
 * - TLS 指纹锁定按实现方式（自定义 X509TrustManager 校验链首证书 SHA-256）
 *   做真握手验证，不 mock TLS 层。
 */
class RemoteAnalysisClientProtocolTest {

    /** 配对令牌（协议 §3 auth 帧载荷；测试值，与 PC 端无真实对应） */
    private val token = "test-token-1234"

    /** 标准开局 FEN，红先（协议 §4.4 position 示例同款局面） */
    private val fenRed = "rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR w - - 0 1"

    /** 同局面黑先（测 red_go=false 与 force/my_red 编解码） */
    private val fenBlack = fenRed.replace(" w ", " b ")

    /** PC hello（§4.1 服务器形态示例逐字：server=cabinet） */
    private fun pcHello(): String =
        """{"v":1,"t":"hello","server":"cabinet","ver":"0.1.0-test","caps":["v1"]}"""

    // ---- 每测试一服务端/一客户端；@After 统一清理（断言失败也不泄漏端口/线程） ----
    private var server: MockWebServer? = null
    private var client: RemoteAnalysisClient? = null

    @After
    fun tearDown() {
        client?.stop()
        server?.shutdown()
        client = null
        server = null
    }

    /**
     * 回调记录器：全部并发队列 + 计数器，测试经 awaitCond 轮询断言
     * （客户端回调发生在 OkHttp 线程，与测试线程无同步关系）。
     */
    private class Recorder : RemoteAnalysisClient.Listener {
        data class ServerCfg(val depth: Int, val hashMB: Int, val movetimeMS: Int?)
        data class Metric(val state: String, val depth: Int, val nodes: Long, val nps: Long, val hashfull: Int)
        data class Err(val code: String, val message: String, val turnFlipLegal: Boolean, val fen: String?)

        val states = ConcurrentLinkedQueue<RemoteAnalysisClient.ConnectionState>()
        val connectErrors = ConcurrentLinkedQueue<String>()
        val engineStates = ConcurrentLinkedQueue<String>()
        val serverCfgs = ConcurrentLinkedQueue<ServerCfg>()
        val suggestions = ConcurrentLinkedQueue<RemoteSuggestion>()
        val metrics = ConcurrentLinkedQueue<Metric>()
        val settledCount = AtomicInteger()
        val protoErrors = ConcurrentLinkedQueue<Err>()

        override fun onConnectionState(state: RemoteAnalysisClient.ConnectionState) { states.add(state) }
        override fun onConnectError(reason: String) { connectErrors.add(reason) }
        override fun onEngineState(state: String) { engineStates.add(state) }
        override fun onServerEngineConfig(depth: Int, hashMB: Int, movetimeMS: Int?) {
            serverCfgs.add(ServerCfg(depth, hashMB, movetimeMS))
        }
        override fun onSuggestion(sug: RemoteSuggestion) { suggestions.add(sug) }
        override fun onEngineMetrics(state: String, depth: Int, nodes: Long, nps: Long, hashfull: Int) {
            metrics.add(Metric(state, depth, nodes, nps, hashfull))
        }
        override fun onSearchSettled() { settledCount.incrementAndGet() }
        override fun onError(code: String, message: String, turnFlipLegal: Boolean, fen: String?) {
            protoErrors.add(Err(code, message, turnFlipLegal, fen))
        }
    }

    /**
     * MockWebServer 侧的协议脚本：onOpen 执行调用方动作（PC 先发 hello 等），
     * 收到的客户端帧按到达顺序记入 [received]（TCP 有序，顺序可信）。
     */
    private open class ServerScript(private val onOpenAction: WebSocket.() -> Unit = {}) : WebSocketListener() {
        val received = ConcurrentLinkedQueue<String>()
        /** 服务器侧 ws 升级完成的计数门（指纹反例用它断言"连接从未建立"） */
        val openLatch = java.util.concurrent.CountDownLatch(1)
        @Volatile private var ws: WebSocket? = null

        override fun onOpen(webSocket: WebSocket, response: Response) {
            ws = webSocket
            openLatch.countDown()
            webSocket.onOpenAction()
        }

        override fun onMessage(webSocket: WebSocket, text: String) { received.add(text) }

        /**
         * 对端（客户端 stop）发起关闭握手时必须回 close 完成关闭：OkHttp 语义下
         * 连接停在 CLOSING 状态直到本端 close——不回的话连接挂死，
         * MockWebServer.shutdown 等队列清空会超时（IOException "Gave up"）。
         */
        override fun onClosing(webSocket: WebSocket, code: Int, reason: String) {
            webSocket.close(1000, null)
        }

        fun send(text: String) {
            // 等服务器侧 ws 升级完成再发：测试主体在 start() 后立即 send 会早于
            // onOpen，此时 ws 还是 null 而静默丢帧（下行帧测试因此收不到任何回调）
            openLatch.await(10, TimeUnit.SECONDS)
            ws?.send(text)
        }
    }

    /** 起一个明文 ws:// 的 MockWebServer（单条 ws 升级响应；无需 TLS 的下行帧测试用） */
    private fun newPlainServer(onOpenAction: WebSocket.() -> Unit = {}): ServerScript {
        val script = ServerScript(onOpenAction)
        val s = MockWebServer()
        s.enqueue(MockResponse().withWebSocketUpgrade(script))
        s.start()
        server = s
        return script
    }

    /**
     * 起一个 wss://（自签 TLS）的 MockWebServer，返回服务器脚本与自签证书的
     * SHA-256 指纹（hex 小写）——模拟 PC 端控制台展示、经配对二维码带外传递的值。
     */
    private fun newTlsServer(onOpenAction: WebSocket.() -> Unit = {}): Pair<ServerScript, String> {
        val cert = HeldCertificate.Builder()
            .commonName("cabinet-pc")
            .addSubjectAlternativeName("localhost")
            .build()
        val certs = HandshakeCertificates.Builder().heldCertificate(cert).build()
        val script = ServerScript(onOpenAction)
        val s = MockWebServer()
        s.useHttps(certs.sslSocketFactory(), false)
        s.enqueue(MockResponse().withWebSocketUpgrade(script))
        s.start()
        server = s
        return script to sha256Hex(cert.certificate.encoded)
    }

    /** 与客户端同口径的链首证书 SHA-256 摘要（独立实现，作为"PC 侧计算"的对表端） */
    private fun sha256Hex(bytes: ByteArray): String =
        MessageDigest.getInstance("SHA-256").digest(bytes).joinToString("") { "%02x".format(it) }

    private fun wsUrl(s: MockWebServer): String =
        s.url("/ws").toString().replaceFirst("http://", "ws://")

    private fun wssUrl(s: MockWebServer): String =
        s.url("/ws").toString().replaceFirst("https://", "wss://")

    /** 轮询等待条件成立（20ms 步进；10s 超时兜底 CI 慢机，成功路径毫秒级返回）。cond 放尾参便于尾 lambda 调用 */
    private fun awaitCond(timeoutMs: Long = 10_000, cond: () -> Boolean): Boolean {
        val deadline = System.currentTimeMillis() + timeoutMs
        while (System.currentTimeMillis() < deadline) {
            if (cond()) return true
            Thread.sleep(20)
        }
        return cond()
    }

    /** 把服务器已收帧转成 JSONObject 列表（ConcurrentLinkedQueue 无随机访问） */
    private fun jsonFrames(script: ServerScript): List<JSONObject> =
        script.received.map { JSONObject(it) }

    // ==================== §3 握手：hello/auth ====================

    @Test
    fun `握手收到PC先发hello后按序回hello与auth_携带设备自报字段`() {
        // 指纹以冒号分隔+大写形态录入：顺带回归客户端归一化口径（与扫码录入一致）
        val (script, fp) = newTlsServer { send(pcHello()) }
        val rec = Recorder()
        val fpColon = fp.chunked(2).joinToString(":").uppercase()
        client = RemoteAnalysisClient(wssUrl(requireNotNull(server)), token, rec,
            certFingerprint = fpColon, deviceName = "测试机", deviceId = "dev-001").also { it.start() }

        assertTrue("10s 内未收到 hello+auth：${script.received}", awaitCond { script.received.size >= 2 })
        val frames = jsonFrames(script)
        // 顺序钉死：先 hello 后 auth（§3：本端收到 PC hello 后回 hello 并立即 auth）
        assertEquals("hello", frames[0].getString("t"))
        assertEquals("auth", frames[1].getString("t"))
        // hello 帧字段逐字对表（§4.1：client/ver/caps 必填；name/devid 可选自报）
        val hello = frames[0]
        assertEquals(1, hello.getInt("v"))
        assertEquals("cabinet-phone", hello.getString("client"))
        // ver 与 BuildConfig.VERSION_NAME 同源（gradle 由 git 标签推导，不手写常量）
        assertEquals(BuildConfig.VERSION_NAME, hello.getString("ver"))
        assertEquals(1, hello.getJSONArray("caps").length())
        assertEquals("v1", hello.getJSONArray("caps").getString(0))
        assertEquals("测试机", hello.getString("name"))
        assertEquals("dev-001", hello.getString("devid"))
        // auth 帧 token 透传
        assertEquals(1, frames[1].getInt("v"))
        assertEquals(token, frames[1].getString("token"))
    }

    @Test
    fun `联调模式无指纹wss信任自签证书完成握手_hello不携带可选设备字段`() {
        // debug 构建（单测运行变体）ENFORCE_TLS_FINGERPRINT=false：空指纹走 trust-all
        // 联调分支（等同 mockphone 的 InsecureSkipVerify）——自签证书也应握手成功
        val (script, _) = newTlsServer { send(pcHello()) }
        val rec = Recorder()
        client = RemoteAnalysisClient(wssUrl(requireNotNull(server)), token, rec).also { it.start() }

        assertTrue("10s 内未收到 hello+auth：${script.received}", awaitCond { script.received.size >= 2 })
        val hello = jsonFrames(script)[0]
        // §7 宽容语义：空值不携带（与旧端 hello 字节形态一致）
        assertFalse("空 deviceName 不应携带 name 字段", hello.has("name"))
        assertFalse("空 deviceId 不应携带 devid 字段", hello.has("devid"))
    }

    // ==================== §1 TLS 指纹锁定 ====================

    @Test
    fun `指纹锁定证书不匹配时握手层拒绝连接且不上行任何帧`() {
        // 造"另一台电脑"的证书：期望指纹与服务器实际证书不同——TrustManager 抛
        // CertificateException，TLS 握手失败，ws 升级从未发生
        val (script, _) = newTlsServer() // 服务器不发 hello：本测试到不了协议层
        val decoy = HeldCertificate.Builder().commonName("another-pc").build()
        val rec = Recorder()
        client = RemoteAnalysisClient(wssUrl(requireNotNull(server)), token, rec,
            certFingerprint = sha256Hex(decoy.certificate.encoded)).also { it.start() }

        assertTrue("10s 内未收到连接失败回调", awaitCond { rec.connectErrors.isNotEmpty() })
        // 连接从未建立：服务器侧 ws 未打开、未收到任何客户端帧（"拒绝"而非"降级连上"）
        assertFalse("服务器侧不应完成 ws 升级", script.openLatch.await(0, TimeUnit.MILLISECONDS))
        assertTrue("不应有任何上行帧：${script.received}", script.received.isEmpty())
        // 失败后走重连路径：状态回到 DISCONNECTED（定时器为空操作，不会真实重连）
        assertEquals(RemoteAnalysisClient.ConnectionState.DISCONNECTED, rec.states.last())
    }

    @Test
    fun `指纹格式非法时拒绝连接且不发起任何连接`() {
        // 非 64 位 hex：绝不静默降级为不校验（虚假安全感），配置错误直接明示
        val script = newPlainServer()
        val rec = Recorder()
        client = RemoteAnalysisClient(wsUrl(requireNotNull(server)), token, rec,
            certFingerprint = "zz:11").also { it.start() }

        assertTrue(awaitCond { rec.connectErrors.isNotEmpty() })
        assertTrue("错误文案应指明指纹格式非法：${rec.connectErrors.peek()}",
            rec.connectErrors.peek()!!.contains("证书指纹格式非法"))
        // 配置错误不重试、不发起连接（明文连接也一个请求都没有）
        assertEquals("不应发起任何连接", 0, requireNotNull(server).requestCount)
        assertTrue(script.received.isEmpty())
    }

    @Test
    fun `地址格式非法时报中文错误不闪退`() {
        // 手填 serverUrl 漏 scheme：OkHttp 抛 IllegalArgumentException——客户端必须
        // 接住给中文文案（配置错误不重试），不能在主线程/重连定时器里炸死
        val rec = Recorder()
        client = RemoteAnalysisClient("192.168.1.10:9527/ws", token, rec).also { it.start() }

        assertTrue(awaitCond { rec.connectErrors.isNotEmpty() })
        assertTrue("错误文案应引导 scheme：${rec.connectErrors.peek()}",
            rec.connectErrors.peek()!!.contains("电脑地址格式非法"))
        assertEquals(RemoteAnalysisClient.ConnectionState.DISCONNECTED, rec.states.last())
    }

    // ==================== §4.3 welcome ====================

    @Test
    fun `welcome帧解析引擎状态与算力同步并回调CONNECTED`() {
        val script = newPlainServer { send(pcHello()) }
        val rec = Recorder()
        client = RemoteAnalysisClient(wsUrl(requireNotNull(server)), token, rec).also { it.start() }
        assertTrue("握手未完成", awaitCond { script.received.size >= 2 })

        // §4.3 示例逐字：movetime_ms=0 是合法值（深度制），必须传到且不得变 null
        script.send("""{"v":1,"t":"welcome","session_id":"f3a9c2e81b7d4066","engine":{"state":"ready","depth":20,"hash_mb":2048,"movetime_ms":0},"books":[]}""")

        assertTrue(awaitCond { rec.serverCfgs.isNotEmpty() })
        assertEquals(RemoteAnalysisClient.ConnectionState.CONNECTED, rec.states.last())
        assertEquals("ready", rec.engineStates.last())
        val cfg = rec.serverCfgs.peek()!!
        assertEquals(20, cfg.depth)
        assertEquals(2048, cfg.hashMB)
        assertEquals("movetime_ms=0 必须传出（深度制语义）", Integer.valueOf(0), cfg.movetimeMS)
    }

    // ==================== §4.5 suggestion ====================

    @Test
    fun `suggestion引擎帧全字段解析_红方视角cp分数与wdl与fen回显`() {
        val script = newPlainServer()
        val rec = Recorder()
        client = RemoteAnalysisClient(wsUrl(requireNotNull(server)), token, rec).also { it.start() }
        // 下行帧解析不依赖握手状态：无需 welcome，服务器直接发目标帧
        script.send("""{"v":1,"t":"suggestion","source":"engine","bestmove":"h2e2","score":{"cp":120},"pv":["h2e2","b9c7","h0g2"],"depth":20,"fen":"$fenRed","wdl_w":62,"wdl_d":31,"wdl_l":7,"multipv":2}""")

        assertTrue(awaitCond { rec.suggestions.isNotEmpty() })
        val sug = rec.suggestions.peek()!!
        assertEquals("engine", sug.source)
        assertEquals("h2e2", sug.bestMove)
        assertEquals(120, sug.scoreCp!!)
        assertNull("cp/mate 二者只出现其一（§4.5）", sug.scoreMate)
        assertEquals(listOf("h2e2", "b9c7", "h0g2"), sug.pv)
        assertEquals(20, sug.depth)
        assertEquals(fenRed, sug.fen)
        assertEquals(62, sug.wdlW!!)
        assertEquals(31, sug.wdlD!!)
        assertEquals(7, sug.wdlL!!)
        assertEquals(2, sug.multipv!!)
        assertFalse(sug.cached)
        assertFalse(sug.fromBook)
        assertFalse(sug.fromCloud)
    }

    @Test
    fun `suggestion将杀帧mate解析_负值表示黑方将杀红方`() {
        val script = newPlainServer()
        val rec = Recorder()
        client = RemoteAnalysisClient(wsUrl(requireNotNull(server)), token, rec).also { it.start() }
        // mate 负值 = 黑方将杀红方（红方视角统一语义，§4.5）
        script.send("""{"v":1,"t":"suggestion","source":"engine","bestmove":"e9f9","score":{"mate":-3},"depth":30}""")

        assertTrue(awaitCond { rec.suggestions.isNotEmpty() })
        val sug = rec.suggestions.peek()!!
        assertNull(sug.scoreCp)
        assertEquals(-3, sug.scoreMate!!)
    }

    @Test
    fun `suggestion桔库帧极简形态字段兜底与缓存标记`() {
        val script = newPlainServer()
        val rec = Recorder()
        client = RemoteAnalysisClient(wsUrl(requireNotNull(server)), token, rec).also { it.start() }
        // §4.5 库命中形态逐字：无 score/pv/depth，cached=true（沉淀命中，omitempty 之外的可选标记）
        script.send("""{"v":1,"t":"suggestion","source":"book:桔库09.09.2023精修库","bestmove":"b2e2","cached":true}""")

        assertTrue(awaitCond { rec.suggestions.isNotEmpty() })
        val sug = rec.suggestions.peek()!!
        assertEquals("book:桔库09.09.2023精修库", sug.source)
        assertTrue(sug.fromBook)
        assertEquals("桔库09.09.2023精修库", sug.bookName)
        assertEquals("b2e2", sug.bestMove)
        assertNull("库命中无厘兵分", sug.scoreCp)
        assertNull(sug.scoreMate)
        assertTrue("库命中无 PV", sug.pv.isEmpty())
        assertEquals(0, sug.depth)
        assertNull("旧 PC 无 fen 回显字段时为 null", sug.fen)
        assertNull(sug.wdlW); assertNull(sug.wdlD); assertNull(sug.wdlL)
        assertNull(sug.multipv)
        assertTrue(sug.cached)
    }

    @Test
    fun `suggestion云库帧fromCloud解析_无分数且fen恒携带`() {
        val script = newPlainServer()
        val rec = Recorder()
        client = RemoteAnalysisClient(wsUrl(requireNotNull(server)), token, rec).also { it.start() }
        // §4.5 云库命中（2026-10-30 cloud_first 定稿）：multipv 恒 1、fen 恒携带、无分数
        script.send("""{"v":1,"t":"suggestion","source":"cloud:chessdb.cn","bestmove":"h2e2","fen":"$fenRed","multipv":1}""")

        assertTrue(awaitCond { rec.suggestions.isNotEmpty() })
        val sug = rec.suggestions.peek()!!
        assertTrue(sug.fromCloud)
        assertEquals("chessdb.cn", sug.cloudHost)
        assertNull(sug.scoreCp)
        assertEquals(fenRed, sug.fen)
        assertEquals(1, sug.multipv!!)
    }

    // ==================== §4.6 status ====================

    @Test
    fun `status帧带指标走onEngineMetrics_仅state走收束回调`() {
        val script = newPlainServer()
        val rec = Recorder()
        client = RemoteAnalysisClient(wsUrl(requireNotNull(server)), token, rec).also { it.start() }

        // §4.6 示例逐字：rtt_ms 是预留字段（PC 从不下发），宽容语义下收到也不炸
        script.send("""{"v":1,"t":"status","engine":{"state":"searching","depth":12,"nodes":123456,"nps":500000,"hashfull":30},"rtt_ms":12}""")
        assertTrue(awaitCond { rec.metrics.isNotEmpty() })
        val m = rec.metrics.peek()!!
        assertEquals("searching", m.state)
        assertEquals(12, m.depth)
        assertEquals(123456L, m.nodes)
        assertEquals(500000L, m.nps)
        assertEquals(30, m.hashfull)
        assertEquals("带指标帧不应触发收束", 0, rec.settledCount.get())

        // 收束帧：仅含 state（指标字段缺省）→ onSearchSettled
        script.send("""{"v":1,"t":"status","engine":{"state":"ready"}}""")
        assertTrue(awaitCond { rec.settledCount.get() >= 1 })
        assertEquals("ready", rec.engineStates.last())
    }

    // ==================== §4.7 心跳 ====================

    @Test
    fun `ping帧原样回pong保留ts`() {
        val script = newPlainServer()
        val rec = Recorder()
        client = RemoteAnalysisClient(wsUrl(requireNotNull(server)), token, rec).also { it.start() }

        script.send("""{"v":1,"t":"ping","ts":1757459200123}""")
        assertTrue(awaitCond {
            jsonFrames(script).any { it.optString("t") == "pong" }
        })
        val pong = jsonFrames(script).first { it.optString("t") == "pong" }
        assertEquals("PC 心跳必须原 ts 回 pong（§4.7）", 1757459200123L, pong.getLong("ts"))
        assertEquals(1, pong.getInt("v"))
    }

    // ==================== §4.8/§5 错误帧 ====================

    @Test
    fun `bad_position错误帧携带fen回显与turnFlipLegal_fen缺省时回调null`() {
        val script = newPlainServer()
        val rec = Recorder()
        client = RemoteAnalysisClient(wsUrl(requireNotNull(server)), token, rec).also { it.start() }

        // §4.8 示例逐字：bad_position 带 fen 回显 + turn_flip_legal（自动纠正走子方标记）
        script.send("""{"v":1,"t":"error","code":"bad_position","message":"局面识别异常（两将照面）","fen":"$fenRed","turn_flip_legal":true}""")
        assertTrue(awaitCond { rec.protoErrors.isNotEmpty() })
        val err = rec.protoErrors.peek()!!
        assertEquals("bad_position", err.code)
        assertEquals("局面识别异常（两将照面）", err.message)
        assertTrue(err.turnFlipLegal)
        assertEquals(fenRed, err.fen)

        // 无回显字段的错误（not_ready）：fen 回调 null、turnFlipLegal 兜底 false
        script.send("""{"v":1,"t":"error","code":"not_ready","message":"引擎加载中"}""")
        assertTrue(awaitCond { rec.protoErrors.size >= 2 })
        val err2 = rec.protoErrors.elementAt(1)
        assertEquals("not_ready", err2.code)
        assertFalse(err2.turnFlipLegal)
        assertNull("无 fen 字段时回调 null（不过滤）", err2.fen)
    }

    @Test
    fun `auth_failed错误后置DISCONNECTED且不再发起重连`() {
        // §5：令牌错误重连无意义——必须停机明示，区别于网络断连的自动重连
        val script = newPlainServer { send(pcHello()) }
        val rec = Recorder()
        client = RemoteAnalysisClient(wsUrl(requireNotNull(server)), token, rec).also { it.start() }
        assertTrue("握手未完成", awaitCond { script.received.size >= 2 })

        script.send("""{"v":1,"t":"error","code":"auth_failed","message":"配对令牌不正确"}""")
        assertTrue(awaitCond { rec.protoErrors.any { it.code == "auth_failed" } })
        assertEquals(RemoteAnalysisClient.ConnectionState.DISCONNECTED, rec.states.last())
        // 停止重连：1.2s 窗口内无第二次连接请求（自动重连 1s 起步，若有早就打了）
        Thread.sleep(1200)
        assertEquals("auth_failed 后不得重连", 1, requireNotNull(server).requestCount)
    }

    // ==================== §6 not_ready 恢复 ====================

    @Test
    fun `not_ready后首个ready自动重发最近局面且仅重发一次`() {
        val script = newPlainServer { send(pcHello()) }
        val rec = Recorder()
        client = RemoteAnalysisClient(wsUrl(requireNotNull(server)), token, rec).also { it.start() }
        assertTrue("握手未完成", awaitCond { script.received.size >= 2 })
        script.send("""{"v":1,"t":"welcome","engine":{"state":"ready","depth":20,"hash_mb":2048},"books":[]}""")
        assertTrue("welcome 未回调", awaitCond { rec.states.last() == RemoteAnalysisClient.ConnectionState.CONNECTED })

        // 上行局面（§4.4 示例形态：red_go 与 FEN 第 2 段一致，无可选字段）
        client!!.sendPosition(fenRed)
        assertTrue(awaitCond { script.received.size >= 3 })
        val pos1 = jsonFrames(script)[2]
        assertEquals("position", pos1.optString("t"))
        assertEquals(fenRed, pos1.getString("fen"))
        assertEquals(true, pos1.getBoolean("red_go"))
        assertEquals(0, pos1.getJSONArray("history").length())
        assertFalse("缺省 force 不得出现", pos1.has("force"))
        assertFalse("缺省 my_red 不得出现", pos1.has("my_red"))

        // not_ready 挂起重发标记 → status{state:"ready"} 自动补发一次
        script.send("""{"v":1,"t":"error","code":"not_ready","message":"引擎加载中"}""")
        assertTrue(awaitCond { rec.protoErrors.any { it.code == "not_ready" } })
        script.send("""{"v":1,"t":"status","engine":{"state":"ready"}}""")
        assertTrue("未自动补发 position", awaitCond { script.received.size >= 4 })
        assertEquals(fenRed, jsonFrames(script)[3].getString("fen"))

        // 只重发一次：§4.6 收束帧同样带 state:ready，据它再发会循环搜索（历史 bug 语义）
        val before = script.received.size
        Thread.sleep(400)
        assertEquals("收束 ready 不得再触发重发", before, script.received.size)
    }

    // ==================== §4.4 position 上行编解码 ====================

    @Test
    fun `position帧上行编解码force与my_red字段`() {
        val script = newPlainServer { send(pcHello()) }
        val rec = Recorder()
        client = RemoteAnalysisClient(wsUrl(requireNotNull(server)), token, rec).also { it.start() }
        assertTrue("握手未完成", awaitCond { script.received.size >= 2 })
        script.send("""{"v":1,"t":"welcome","engine":{"state":"ready"},"books":[]}""")
        assertTrue("welcome 未回调", awaitCond { rec.states.last() == RemoteAnalysisClient.ConnectionState.CONNECTED })

        // §4.4 示例 119 行形态：黑先 + force=true + my_red=false（执黑必须显式下发）
        client!!.sendPosition(fenBlack, force = true, myRed = false)
        assertTrue(awaitCond { script.received.size >= 3 })
        val pos = jsonFrames(script)[2]
        assertEquals(fenBlack, pos.getString("fen"))
        assertEquals("FEN 第 2 段为 b 时 red_go 必须 false", false, pos.getBoolean("red_go"))
        assertEquals(true, pos.getBoolean("force"))
        assertEquals(false, pos.getBoolean("my_red"))
        assertFalse(pos.has("new_game"))
        assertFalse("缺省 multi_pv_once 不得出现", pos.has("multi_pv_once"))
    }

    @Test
    fun `position帧上行编解码multi_pv_once单次多线字段`() {
        val script = newPlainServer { send(pcHello()) }
        val rec = Recorder()
        client = RemoteAnalysisClient(wsUrl(requireNotNull(server)), token, rec).also { it.start() }
        assertTrue("握手未完成", awaitCond { script.received.size >= 2 })
        script.send("""{"v":1,"t":"welcome","engine":{"state":"ready"},"books":[]}""")
        assertTrue("welcome 未回调", awaitCond { rec.states.last() == RemoteAnalysisClient.ConnectionState.CONNECTED })

        // 双击小棋盘"单次三变解析"（2026-11-09）：multi_pv_once=3 随 position 上行，
        // 旧 PC 忽略未知字段（§7）退化为普通重搜
        client!!.sendPosition(fenRed, multiPvOnce = 3)
        assertTrue(awaitCond { script.received.size >= 3 })
        val pos = jsonFrames(script)[2]
        assertEquals("multi_pv_once 必须按上行值编码", 3, pos.getInt("multi_pv_once"))
        assertFalse("单次多线不隐含 force 字段（PC 端自行合并语义）", pos.has("force"))
    }

    @Test
    fun `position帧manual标记仅true时出现`() {
        val script = newPlainServer { send(pcHello()) }
        val rec = Recorder()
        client = RemoteAnalysisClient(wsUrl(requireNotNull(server)), token, rec).also { it.start() }
        assertTrue("握手未完成", awaitCond { script.received.size >= 2 })
        script.send("""{"v":1,"t":"welcome","engine":{"state":"ready"},"books":[]}""")
        assertTrue("welcome 未回调", awaitCond { rec.states.last() == RemoteAnalysisClient.ConnectionState.CONNECTED })

        // 手动模式帧（2026-11-10 结构防线配套）：manual=true 编码上行，PC 端
        // 结构门跳过；普通帧不带（omitempty 语义，旧 PC 忽略 §7）
        client!!.sendPosition(fenRed, manual = true)
        assertTrue(awaitCond { script.received.size >= 3 })
        assertTrue(jsonFrames(script)[2].getBoolean("manual"))
        client!!.sendPosition(fenBlack)
        assertTrue(awaitCond { script.received.size >= 4 })
        assertFalse(jsonFrames(script)[3].has("manual"))
    }

    @Test
    fun `补发沿用multi_pv_once且普通发送清旧值`() {
        val script = newPlainServer { send(pcHello()) }
        val rec = Recorder()
        client = RemoteAnalysisClient(wsUrl(requireNotNull(server)), token, rec).also { it.start() }
        assertTrue("握手未完成", awaitCond { script.received.size >= 2 })
        script.send("""{"v":1,"t":"welcome","engine":{"state":"ready"},"books":[]}""")
        assertTrue("welcome 未回调", awaitCond { rec.states.last() == RemoteAnalysisClient.ConnectionState.CONNECTED })

        // 双击三变生产形态：force=true + multi_pv_once=3 同帧上行
        client!!.sendPosition(fenRed, force = true, multiPvOnce = 3)
        assertTrue(awaitCond { script.received.size >= 3 })
        assertEquals(3, jsonFrames(script)[2].getInt("multi_pv_once"))

        // 重连补发（welcome 处理器重放最近一条）：三变意图沿用不丢（与 force 同语义）
        script.send("""{"v":1,"t":"welcome","engine":{"state":"ready"},"books":[]}""")
        assertTrue("welcome 未补发", awaitCond { script.received.size >= 4 })
        val pos2 = jsonFrames(script)[3]
        assertEquals(fenRed, pos2.getString("fen"))
        assertEquals("补发应沿用 multi_pv_once", 3, pos2.getInt("multi_pv_once"))

        // 普通局面发送清旧值：此后补发不得再带三变意图（防旧值误补发）
        client!!.sendPosition(fenBlack)
        assertTrue(awaitCond { script.received.size >= 5 })
        script.send("""{"v":1,"t":"welcome","engine":{"state":"ready"},"books":[]}""")
        assertTrue("普通发送后的补发未到", awaitCond { script.received.size >= 6 })
        val pos3 = jsonFrames(script)[5]
        assertEquals(fenBlack, pos3.getString("fen"))
        assertFalse("普通发送应清旧值，补发不得再带 multi_pv_once", pos3.has("multi_pv_once"))
    }

    // ==================== §5/§6 重连退避 ====================

    @Test
    fun `重连退避1s起步翻倍至5s封顶_welcome后重置回1s`() {
        // 纯 JVM 单测中 netHandler 为空操作（postDelayed 不执行），无法端到端观测真实
        // 重连间隔——退避算法用反射直测私有字段 backoffMs：scheduleReconnect 每次失败
        // 消耗当前值并翻倍封顶（1s→2s→4s→5s 封顶，局域网直连快速跟上的设计取值）
        val rec = Recorder()
        client = RemoteAnalysisClient("ws://127.0.0.1:9/ws", token, rec)
        setPrivate("started", true)

        fun backoff(): Long = readLongPrivate("backoffMs")
        assertEquals("初始退避应为 1s", 1000L, backoff())
        invokePrivate("scheduleReconnect", emptyArray(), emptyArray())
        assertEquals(2000L, backoff())
        invokePrivate("scheduleReconnect", emptyArray(), emptyArray())
        assertEquals(4000L, backoff())
        invokePrivate("scheduleReconnect", emptyArray(), emptyArray())
        assertEquals("4s 之后封顶 5s", 5000L, backoff())
        invokePrivate("scheduleReconnect", emptyArray(), emptyArray())
        assertEquals("封顶后不再增长", 5000L, backoff())

        // welcome 到达 = 会话建立：退避重置回 1s，下一轮断线从 1s 重新起步（§6）
        val welcome = JSONObject("""{"v":1,"t":"welcome","engine":{"state":"ready","depth":20,"hash_mb":2048},"books":[]}""")
        invokePrivate("handleFrame", arrayOf(JSONObject::class.java, WebSocket::class.java),
            arrayOf(welcome, dummyWs))
        assertEquals("welcome 必须重置退避", 1000L, backoff())
        assertEquals("welcome 应回调 CONNECTED",
            RemoteAnalysisClient.ConnectionState.CONNECTED, rec.states.last())
        assertEquals("welcome 携带引擎状态", "ready", rec.engineStates.last())
    }

    // ---- 反射工具（仅测退避/welcome 内部状态；生产代码保持私有不动） ----

    private fun setPrivate(name: String, value: Any?) {
        RemoteAnalysisClient::class.java.getDeclaredField(name).apply { isAccessible = true }
            .set(client, value)
    }

    private fun readLongPrivate(name: String): Long =
        RemoteAnalysisClient::class.java.getDeclaredField(name).apply { isAccessible = true }
            .getLong(requireNotNull(client))

    private fun invokePrivate(name: String, parameterTypes: Array<Class<*>>, args: Array<Any?>) {
        val m = RemoteAnalysisClient::class.java.getDeclaredMethod(name, *parameterTypes)
        m.isAccessible = true
        m.invoke(requireNotNull(client), *args)
    }

    /** handleFrame 的 welcome 分支不使用 ws 参数，桩实现全部返回空值即可 */
    private val dummyWs = object : WebSocket {
        override fun request(): Request = Request.Builder().url("http://127.0.0.1/").build()
        override fun queueSize(): Long = 0L
        override fun send(text: String): Boolean = false
        override fun send(bytes: okio.ByteString): Boolean = false
        override fun close(code: Int, reason: String?): Boolean = false
        override fun cancel() {}
    }
}
