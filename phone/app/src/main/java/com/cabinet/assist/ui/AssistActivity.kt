package com.cabinet.assist.ui

import android.Manifest
import android.app.Activity
import android.content.ComponentName
import android.content.Context
import android.content.Intent
import android.content.ServiceConnection
import android.content.pm.PackageManager
import android.graphics.Bitmap
import android.graphics.BitmapFactory
import android.graphics.Canvas
import android.graphics.LinearGradient
import android.graphics.Paint
import android.graphics.Shader
import android.graphics.drawable.BitmapDrawable
import android.graphics.drawable.GradientDrawable
import android.media.projection.MediaProjectionManager
import android.net.Uri
import android.os.Build
import android.os.Bundle
import android.os.IBinder
import android.provider.Settings
import android.text.Editable
import android.view.View
import android.text.TextWatcher
import android.widget.Button
import android.widget.EditText
import android.widget.RadioButton
import android.widget.RadioGroup
import android.widget.ScrollView
import android.widget.TextView
import android.widget.Toast
import androidx.activity.result.contract.ActivityResultContracts
import androidx.appcompat.app.AlertDialog
import androidx.appcompat.app.AppCompatActivity
import androidx.core.content.ContextCompat
import com.journeyapps.barcodescanner.ScanContract
import com.journeyapps.barcodescanner.ScanOptions
import com.cabinet.BuildConfig
import com.cabinet.R
import com.cabinet.assist.AssistConfig
import com.cabinet.assist.RemoteAnalysisClient
import com.cabinet.assist.RemoteSuggestion
import com.cabinet.assist.ScreenAssistService
import java.io.File

/**
 * 连线功能主界面：免责声明 → 连接参数（手填或应用内扫码）→ 授权（悬浮窗/通知/录屏）→ 启动识别服务。
 * 棋盘识别全自动（YOLO 检测，免校准）。说明性小字统一收进"关于"对话框。
 */
class AssistActivity : AppCompatActivity() {

    companion object {
        /** 自定义主页背景文件（filesDir 下；存在即启用，删除即恢复默认） */
        private const val BG_FILE = "home_background.jpg"
    }

    private var captureIntentCode = -1
    private var captureIntentData: Intent? = null

    private lateinit var rowOverlayPerm: View
    private lateinit var rowNotifyPerm: View
    private lateinit var btnStartCapture: Button
    private lateinit var tvOverlayAction: TextView
    private lateinit var tvNotifyAction: TextView
    private lateinit var numOverlay: TextView
    private lateinit var numNotify: TextView
    private lateinit var numCapture: TextView
    private lateinit var vStateDot: View
    private lateinit var tvStateHint: TextView
    private lateinit var tvChipConn: TextView
    private lateinit var tvChipPerm: TextView
    private lateinit var tvChipBg: TextView
    private lateinit var tvStatus: TextView
    private lateinit var etServerUrl: EditText
    private lateinit var etServerToken: EditText
    private lateinit var etServerFingerprint: EditText
    private lateinit var bgScroll: ScrollView

    /** 卡片状态 chip 语义档（描边与文字色成对切换） */
    private enum class ChipTone { DIM, AMBER, JADE }

    private val statusHandler = android.os.Handler(android.os.Looper.getMainLooper())
    private val statusRunnable = object : Runnable {
        override fun run() {
            refreshStatus()
            statusHandler.postDelayed(this, 1000)
        }
    }

    private var service: ScreenAssistService? = null
    /** 测试连接的临时客户端（验证完即弃；与运行中识别服务的 client 完全独立） */
    private var testClient: RemoteAnalysisClient? = null
    private var btnTestConnect: Button? = null
    private var bound = false

    private val connection = object : ServiceConnection {
        override fun onServiceConnected(name: ComponentName?, binder: IBinder?) {
            val cb = binder as? ScreenAssistService.CastBinder
            service = cb?.service()
            bound = true
            refreshStatus()
        }

        override fun onServiceDisconnected(name: ComponentName?) {
            service = null
            bound = false
        }
    }

    private val projectionLauncher = registerForActivityResult(
        ActivityResultContracts.StartActivityForResult()
    ) { result ->
        if (result.resultCode == Activity.RESULT_OK && result.data != null) {
            captureIntentCode = result.resultCode
            captureIntentData = result.data
            startCaptureService()
            Toast.makeText(this, "录屏授权成功，识别服务已启动", Toast.LENGTH_SHORT).show()
        } else {
            Toast.makeText(this, "未授予录屏权限，无法识别", Toast.LENGTH_SHORT).show()
        }
    }

    private val notificationLauncher = registerForActivityResult(
        ActivityResultContracts.RequestPermission()
    ) { refreshStatus() }

    /** 主页背景自定义：系统图库选图（无需存储权限，系统文件选择） */
    private val pickBackgroundLauncher = registerForActivityResult(
        ActivityResultContracts.GetContent()
    ) { uri ->
        if (uri == null) return@registerForActivityResult
        try {
            saveScaledBackground(uri)
            applyBackground()
            Toast.makeText(this, "主页背景已更新", Toast.LENGTH_SHORT).show()
        } catch (t: Throwable) {
            android.util.Log.w("AssistActivity", "bg pick failed uri=$uri", t)
            Toast.makeText(this, "背景设置失败：${t.javaClass.simpleName}: ${t.message}", Toast.LENGTH_LONG).show()
        }
    }

    /** 应用内扫码连接：扫电脑端"连接二维码"（cabinet://connect?u=…&k=…&f=…）自动填入连接参数 */
    private val scanLauncher = registerForActivityResult(ScanContract()) { result ->
        val text = result.contents?.trim().orEmpty()
        if (text.isEmpty()) return@registerForActivityResult  // 用户取消扫码
        val params = parseConnectPayload(text)
        if (params == null) {
            Toast.makeText(this, "不是有效的内阁连接二维码", Toast.LENGTH_LONG).show()
            return@registerForActivityResult
        }
        applyConnectParams(params)
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setContentView(R.layout.assist_activity)

        rowOverlayPerm = findViewById(R.id.btn_overlay_permission)
        rowNotifyPerm = findViewById(R.id.btn_notification_permission)
        btnStartCapture = findViewById(R.id.btn_start_capture)
        tvOverlayAction = findViewById(R.id.tv_overlay_action)
        tvNotifyAction = findViewById(R.id.tv_notify_action)
        numOverlay = findViewById(R.id.tv_step1_num)
        numNotify = findViewById(R.id.tv_step2_num)
        numCapture = findViewById(R.id.tv_step3_num)
        vStateDot = findViewById(R.id.v_state_dot)
        tvStateHint = findViewById(R.id.tv_state_hint)
        tvChipConn = findViewById(R.id.tv_chip_conn)
        tvChipPerm = findViewById(R.id.tv_chip_perm)
        tvChipBg = findViewById(R.id.tv_chip_bg)
        tvStatus = findViewById(R.id.tv_auto_status)
        bgScroll = findViewById(R.id.bg_scroll)

        // 证书指纹（可选）：默认收起为文本级入口，扫码导入含指纹时自动展开
        val fpToggle = findViewById<TextView>(R.id.tv_fp_toggle)
        fpToggle.setOnClickListener {
            val show = etServerFingerprint.visibility != View.VISIBLE
            etServerFingerprint.visibility = if (show) View.VISIBLE else View.GONE
            fpToggle.text = if (show) "证书指纹（可选，扫码自动填入）⌄"
                           else "证书指纹（可选，扫码自动填入）›"
        }

        // 主页背景自定义：图库选图 / 恢复默认（应用内私有文件持久化，无需存储权限）
        findViewById<Button>(R.id.btn_pick_background).setOnClickListener {
            pickBackgroundLauncher.launch("image/*")
        }
        findViewById<Button>(R.id.btn_reset_background).setOnClickListener {
            File(filesDir, BG_FILE).delete()
            applyBackground()
            Toast.makeText(this, "已恢复默认背景", Toast.LENGTH_SHORT).show()
        }
        applyBackground()

        // 电脑连接设置：输入即持久化（识别服务启动时读取并建立 WSS）
        val cfg = AssistConfig(this)
        etServerUrl = findViewById(R.id.et_server_url)
        etServerToken = findViewById(R.id.et_server_token)
        etServerFingerprint = findViewById(R.id.et_server_fingerprint)
        btnTestConnect = findViewById(R.id.btn_test_connect)
        btnTestConnect?.setOnClickListener { testConnection() }
        etServerUrl.setText(cfg.serverUrl)
        etServerToken.setText(cfg.serverToken)
        etServerFingerprint.setText(cfg.certFingerprint)
        etServerUrl.addTextChangedListener(saveOnChanged { cfg.serverUrl = it })
        etServerToken.addTextChangedListener(saveOnChanged { cfg.serverToken = it })
        etServerFingerprint.addTextChangedListener(saveOnChanged { cfg.certFingerprint = it })

        // 悬浮窗背景主题：三选一，切换即时应用到运行中的悬浮窗
        val rgTheme = findViewById<RadioGroup>(R.id.rg_overlay_theme)
        val current = com.cabinet.assist.ui.OverlayTheme.fromKey(cfg.overlayTheme)
        findViewById<RadioButton>(when (current) {
            com.cabinet.assist.ui.OverlayTheme.BLACK -> R.id.rb_theme_black
            com.cabinet.assist.ui.OverlayTheme.LIGHT -> R.id.rb_theme_light
            else -> R.id.rb_theme_dark
        }).isChecked = true
        rgTheme.setOnCheckedChangeListener { _, id ->
            cfg.overlayTheme = when (id) {
                R.id.rb_theme_black -> com.cabinet.assist.ui.OverlayTheme.BLACK.key
                R.id.rb_theme_light -> com.cabinet.assist.ui.OverlayTheme.LIGHT.key
                else -> com.cabinet.assist.ui.OverlayTheme.DARK.key
            }
            service?.applyOverlayTheme()
        }

        // 识别引擎：ONNX 角点式（默认，对未收录皮肤泛化好）/ YOLO 检测式；运行中即时切换
        val rgEngine = findViewById<RadioGroup>(R.id.rg_recognizer_engine)
        findViewById<RadioButton>(
            if (cfg.recognizerEngine == ScreenAssistService.ENGINE_YOLO) R.id.rb_engine_yolo
            else R.id.rb_engine_onnx
        ).isChecked = true
        rgEngine.setOnCheckedChangeListener { _, id ->
            cfg.recognizerEngine = if (id == R.id.rb_engine_yolo)
                ScreenAssistService.ENGINE_YOLO else ScreenAssistService.ENGINE_ONNX
            service?.setRecognizerEngine(cfg.recognizerEngine)
        }

        // 引擎线程档：点按循环 0(跟随电脑端)→4→8→12→16→20→24→32→0。
        // 皮卡鱼吞吐随线程近线性扩展（13700H 实测：1 线程 81 万 nps → 16 线程 675 万），
        // 电脑端默认只给逻辑核一半；>0 时连接成功后由服务自动下发，已连接时立即生效
        val threadSteps = intArrayOf(0, 4, 8, 12, 16, 20, 24, 32)
        val btnThreads = findViewById<Button>(R.id.btn_engine_threads)
        fun renderThreads() {
            val t = cfg.engineThreads
            btnThreads.text = if (t == 0) "跟随电脑端" else "$t 线程"
        }
        btnThreads.setOnClickListener {
            val idx = threadSteps.indexOf(cfg.engineThreads)
            cfg.engineThreads = threadSteps[(idx + 1).mod(threadSteps.size)]
            renderThreads()
            service?.applyEngineThreads()
        }
        renderThreads()

        rowOverlayPerm.setOnClickListener {
            val intent = Intent(
                Settings.ACTION_MANAGE_OVERLAY_PERMISSION,
                Uri.parse("package:$packageName")
            )
            startActivity(intent)
        }
        rowNotifyPerm.setOnClickListener {
            if (Build.VERSION.SDK_INT >= 33) {
                notificationLauncher.launch(Manifest.permission.POST_NOTIFICATIONS)
            }
        }
        btnStartCapture.setOnClickListener {
            val mpm = getSystemService(Context.MEDIA_PROJECTION_SERVICE) as MediaProjectionManager
            projectionLauncher.launch(mpm.createScreenCaptureIntent())
        }

        // 应用内扫码连接：打开相机扫描电脑端控制台显示的连接二维码
        findViewById<Button>(R.id.btn_scan_connect).setOnClickListener {
            val opts = ScanOptions()
            opts.setDesiredBarcodeFormats(ScanOptions.QR_CODE)
            opts.setPrompt("对准电脑端控制台显示的连接二维码")
            opts.setBeepEnabled(false)
            opts.setOrientationLocked(true)
            // 自定义扫码页：TRY_HARDER + 仅 QR + 连续对焦——扫电脑屏幕二维码
            // （远距/小码/斜角）默认参数单帧识别率低，识别明显偏慢
            opts.setCaptureActivity(QrScanActivity::class.java)
            scanLauncher.launch(opts)
        }

        // 关于：功能说明/连接提示/使用步骤/合规声明（原主页小字收纳于此）
        findViewById<TextView>(R.id.btn_about).setOnClickListener { showAbout() }

        bindService(Intent(this, ScreenAssistService::class.java), connection, Context.BIND_AUTO_CREATE)

        // 扫码配对：系统相机扫 PC 端"连接二维码"后点链接（cabinet:// 深链）进入本页
        maybeHandleConnectIntent(intent)

        // 每次启动先展示免责声明，同意后才能使用
        showDisclaimer()
    }

    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        setIntent(intent)
        maybeHandleConnectIntent(intent)
    }

    /**
     * 扫码配对深链入口（cabinet://connect?u=<wss地址>&k=<令牌>&f=<证书指纹可选>）：
     * 系统相机扫 PC 端二维码后点链接进入本页，解析成功自动填入连接参数。
     */
    private fun maybeHandleConnectIntent(intent: Intent?) {
        if (intent?.action != Intent.ACTION_VIEW) return
        val data = intent.data ?: return
        if (data.scheme != "cabinet" || data.host != "connect") return
        val url = data.getQueryParameter("u")?.trim().orEmpty()
        val token = data.getQueryParameter("k")?.trim().orEmpty()
        if (url.isEmpty() || token.isEmpty()) {
            Toast.makeText(this, "二维码缺少地址或令牌参数", Toast.LENGTH_LONG).show()
            return
        }
        // 二维码是配对权威来源：不带 f = PC 端未启用锁定，清空本地旧指纹
        applyConnectParams(ConnectParams(url, token, data.getQueryParameter("f")?.trim().orEmpty()))
    }

    /** 连接二维码解析结果：地址 + 令牌 + 证书指纹（空 = 不锁定）。 */
    private data class ConnectParams(val url: String, val token: String, val fingerprint: String)

    /** 校验并解析连接二维码内容（cabinet://connect?u=…&k=…[&f=指纹]），非法返回 null。 */
    private fun parseConnectPayload(text: String): ConnectParams? {
        if (!text.startsWith("cabinet://connect")) return null
        val uri = Uri.parse(text)
        if (uri.scheme != "cabinet" || uri.host != "connect") return null
        val url = uri.getQueryParameter("u")?.trim().orEmpty()
        val token = uri.getQueryParameter("k")?.trim().orEmpty()
        if (url.isEmpty() || token.isEmpty()) return null
        return ConnectParams(url, token, uri.getQueryParameter("f")?.trim().orEmpty())
    }

    /** 填入连接参数（输入即持久化的 TextWatcher 负责保存）；fingerprint 为空=不锁定。 */
    private fun applyConnectParams(p: ConnectParams) {
        etServerUrl.setText(p.url)
        etServerToken.setText(p.token)
        etServerFingerprint.setText(p.fingerprint)
        // 二维码里带指纹（=电脑端启用了锁定）时展开指纹行，否则保持收起
        if (p.fingerprint.isNotEmpty()) etServerFingerprint.visibility = View.VISIBLE
        Toast.makeText(this, "已导入电脑连接参数：${p.url}", Toast.LENGTH_LONG).show()
        // 运行中的识别服务热生效（停旧 client 按新参数重建，录屏/悬浮窗全保留）；
        // 服务未运行时为 null 安全跳过——startCapture 会按当前配置创建
        service?.applyConnection()
        testConnection()
    }

    /**
     * 测试连接：临时实例化协议客户端，验证"电脑地址/令牌/指纹/防火墙"是否全部
     * 正确。此前这些参数要等三步授权全做完才知道对不对（客户端只在录屏启动时
     * 创建），配对失败的用户会把三步授权全白做一遍。收到 welcome 即判成功并
     * 显示 PC 引擎状态；验证完立即 stop，与运行中识别服务的连接完全独立。
     * 三种终态：welcome（成功）/ onConnectError（原因中文可读）/ 10s 超时。
     */
    private fun testConnection() {
        if (testClient != null) return // 防重入：测试中再点无效
        val url = etServerUrl.text.toString().trim()
        val token = etServerToken.text.toString().trim()
        if (url.isEmpty() || token.isEmpty()) {
            Toast.makeText(this, "请先填写电脑地址与配对令牌", Toast.LENGTH_SHORT).show()
            return
        }
        btnTestConnect?.isEnabled = false
        btnTestConnect?.text = "测试中…"
        val fingerprint = etServerFingerprint.text.toString().trim()
        val listener = object : RemoteAnalysisClient.Listener {
            override fun onConnectionState(state: RemoteAnalysisClient.ConnectionState) {}
            override fun onConnectError(reason: String) {
                finishTest(reason, ok = false)
            }
            override fun onEngineState(state: String) {
                // welcome 到手 = TCP/TLS/令牌/指纹全部验证通过
                val eng = when (state) {
                    "ready" -> "电脑引擎就绪"
                    "loading" -> "电脑引擎加载中"
                    "error" -> "⚠电脑引擎异常（查看电脑端日志）"
                    else -> "电脑端未启用引擎"
                }
                finishTest("连接成功！$eng", ok = true)
            }
            override fun onServerEngineConfig(depth: Int, hashMB: Int) {}
            override fun onSuggestion(sug: RemoteSuggestion) {}
            override fun onEngineMetrics(state: String, depth: Int, nodes: Long, nps: Long, hashfull: Int) {}
            override fun onSearchSettled() {}
            override fun onError(code: String, message: String, turnFlipLegal: Boolean, fen: String?) {
                finishTest("电脑端返回错误：$message", ok = false)
            }
        }
        testClient = RemoteAnalysisClient(url, token, listener, certFingerprint = fingerprint)
            .also { it.start() }
        statusHandler.postDelayed(testTimeoutRunnable, 10_000)
    }

    /** 测试结束收尾（幂等：testClient 置空后，迟到的回调/超时自动忽略） */
    private fun finishTest(message: String, ok: Boolean) {
        val client = testClient ?: return
        testClient = null
        statusHandler.removeCallbacks(testTimeoutRunnable)
        runCatching { client.stop() }
        runOnUiThread {
            btnTestConnect?.isEnabled = true
            btnTestConnect?.text = "测试连接（无需开启录屏）"
            Toast.makeText(this, (if (ok) "✓ " else "✗ ") + message, Toast.LENGTH_LONG).show()
        }
    }

    private val testTimeoutRunnable = Runnable { finishTest(
        "连接超时：电脑 IP 不可达，或电脑端防火墙未放行 9527（电脑端运行 allow-9527.bat 放行）",
        ok = false) }

    // ==================== 免责声明 / 关于 ====================

    /**
     * 免责声明：每次启动必弹（无可跳过），明确"仅供学习研究、禁止用于对局平台"等
     * 使用边界；点"同意并继续"进入主页，点"不同意并退出"直接结束应用。
     */
    private fun showDisclaimer() {
        AlertDialog.Builder(this)
            .setTitle("免责声明")
            .setMessage(
                "本软件（内阁）为象棋学习辅助工具，仅供个人学习、复盘研究与技术交流使用。请在使用前仔细阅读以下条款：\n\n" +
                    "一、本软件仅通过系统录屏授权识别画面并以悬浮窗显示走法建议，不包含自动走子、触摸注入、无障碍服务、Root/Xposed 等功能，所有着法均由玩家手动操作。\n\n" +
                    "二、严禁将本软件用于任何正式比赛、等级分对局、网络对局平台或赌博等违反平台规则与法律法规的场景；由此产生的一切后果由使用者本人承担。\n\n" +
                    "三、本软件仅供学习研究，请于获取后 24 小时内自行删除。\n\n" +
                    "四、本软件按\"现状\"提供，不对软件的可用性与准确性作任何保证，不对因使用本软件造成的任何直接或间接损失承担责任。\n\n" +
                    "点击\"同意并继续\"即表示您已阅读、理解并同意以上全部条款。"
            )
            .setCancelable(false)
            .setPositiveButton("同意并继续") { d, _ -> d.dismiss() }
            .setNegativeButton("不同意并退出") { _, _ -> finishAffinity() }
            .show()
    }

    /** 关于：收纳主页说明小字（功能说明/连接提示/使用步骤/合规声明）+ 版本号。 */
    private fun showAbout() {
        AlertDialog.Builder(this)
            .setTitle("关于内阁")
            .setMessage(
                "内阁（手机端）v${BuildConfig.VERSION_NAME}\n\n" +
                    "【功能说明】识别其他象棋 App 的画面，分析在电脑端\"内阁\"完成，悬浮窗给出走法建议。本功能只识别、只提示，不会自动走子（无无障碍服务、无点击注入）。\n\n" +
                    "【连接提示】推荐点击\"扫二维码连接电脑\"，扫描电脑端控制台『②连接』区（或托盘菜单\"连接二维码\"）显示的二维码，自动填入电脑地址与配对令牌。\n\n" +
                    "【使用步骤】① 授予悬浮窗权限 → ② 允许通知 → ③ 授权录屏并启动 → ④ 切到任意象棋 App 对局界面即可——自动找棋盘、自动识别方向，悬浮窗实时给出我方建议。\n\n" +
                    "【合规声明】本功能仅通过系统录屏授权做画面识别并显示走法建议，属于象棋学习辅助工具，仅供学习研究，请于获取后 24 小时内自行删除。不包含自动走子、触摸注入、无障碍服务、Root/Xposed 或读取其他应用数据；对局中的所有着法均由玩家手动操作。"
            )
            .setPositiveButton("关闭", null)
            .show()
    }

    // ==================== 主页背景自定义 ====================

    /**
     * 应用主页背景：filesDir 下存在自定义图则用之（统一压暗保证可读），
     * 否则用默认"墨底 + 26dp 细网格"（QiGridDrawable，自绘零图片资产）。
     * 图已按屏幕比例预裁剪（保存时做），这里直接拉伸即可铺满不失真。
     */
    private fun applyBackground() {
        val f = File(filesDir, BG_FILE)
        val bmp = if (f.exists()) BitmapFactory.decodeFile(f.absolutePath) else null
        if (bmp != null) {
            bgScroll.background = BitmapDrawable(resources, withScrim(bmp))
            setChip(tvChipBg, "自定义图", ChipTone.JADE)
        } else {
            bgScroll.background = QiGridDrawable(this)
            setChip(tvChipBg, "默认棋韵", ChipTone.DIM)
        }
    }

    /**
     * 自定义背景统一压暗：纵向 80% → 93%（棋韵 §5.1）。
     * 玻璃卡片是半透明的，需要底层足够暗才能保证文字对比；高饱和插图
     * （如内置竹叶图）压暗后即融入墨色体系。
     */
    private fun withScrim(src: Bitmap): Bitmap {
        val out = Bitmap.createBitmap(src.width, src.height, Bitmap.Config.ARGB_8888)
        val c = Canvas(out)
        c.drawBitmap(src, 0f, 0f, null)
        val p = Paint()
        p.shader = LinearGradient(
            0f, 0f, 0f, src.height.toFloat(),
            0xCC080A0E.toInt(), 0xED080A0E.toInt(), Shader.TileMode.CLAMP
        )
        c.drawRect(0f, 0f, src.width.toFloat(), src.height.toFloat(), p)
        return out
    }

    /**
     * 保存所选图片为主页背景：下采样（宽≤1440）→ 居中裁剪到屏幕宽高比（防止
     * 拉伸变形）→ JPEG 写入应用私有目录（content uri 会过期，必须落盘持久化）。
     */
    private fun saveScaledBackground(uri: Uri) {
        val resolver = contentResolver
        // 第一遍只读尺寸
        val bounds = BitmapFactory.Options().apply { inJustDecodeBounds = true }
        val ins0 = resolver.openInputStream(uri)
        if (ins0 == null) throw IllegalStateException("openInputStream 为 null：$uri")
        ins0.use { BitmapFactory.decodeStream(it, null, bounds) }
        if (bounds.outWidth <= 0 || bounds.outHeight <= 0) {
            throw IllegalStateException("不是有效的图片文件")
        }
        // 下采样：目标宽 ≤1440（背景图不需要原图分辨率，控制内存与磁盘）
        var sample = 1
        while (bounds.outWidth / (sample * 2) >= 1440) sample *= 2
        val src = resolver.openInputStream(uri)?.use {
            BitmapFactory.decodeStream(it, null, BitmapFactory.Options().apply { inSampleSize = sample })
        } ?: throw IllegalStateException("图片解码失败")

        // 居中裁剪到屏幕宽高比
        val dm = resources.displayMetrics
        val targetRatio = dm.widthPixels.toFloat() / dm.heightPixels
        val w = src.width
        val h = src.height
        val cw: Int
        val ch: Int
        if (w.toFloat() / h > targetRatio) {
            ch = h; cw = (h * targetRatio).toInt().coerceAtMost(w)
        } else {
            cw = w; ch = (w / targetRatio).toInt().coerceAtMost(h)
        }
        val cropped = Bitmap.createBitmap(src, (w - cw) / 2, (h - ch) / 2, cw, ch)
        File(filesDir, BG_FILE).outputStream().use { out ->
            cropped.compress(Bitmap.CompressFormat.JPEG, 88, out)
        }
        if (cropped !== src) src.recycle()
    }

    override fun onResume() {
        super.onResume()
        refreshStatus()
        statusHandler.removeCallbacks(statusRunnable)
        statusHandler.postDelayed(statusRunnable, 1000)
    }

    override fun onPause() {
        super.onPause()
        statusHandler.removeCallbacks(statusRunnable)
    }

    override fun onDestroy() {
        if (bound) unbindService(connection)
        super.onDestroy()
        // 服务继续运行（前台服务），仅解绑
    }

    /** 输入即保存（每次按键写 SharedPreferences，量小无性能问题） */
    private fun saveOnChanged(save: (String) -> Unit): TextWatcher = object : TextWatcher {
        override fun beforeTextChanged(s: CharSequence?, a: Int, b: Int, c: Int) {}
        override fun onTextChanged(s: CharSequence?, a: Int, b: Int, c: Int) {}
        override fun afterTextChanged(s: Editable?) { save(s?.toString()?.trim() ?: "") }
    }

    private fun startCaptureService() {
        if (captureIntentData == null) return
        val intent = Intent(this, ScreenAssistService::class.java)
            .setAction(ScreenAssistService.ACTION_START)
            .putExtra(ScreenAssistService.EXTRA_RESULT_CODE, captureIntentCode)
            .putExtra(ScreenAssistService.EXTRA_RESULT_DATA, captureIntentData)
        ContextCompat.startForegroundService(this, intent)
    }

    private fun refreshStatus() {
        val overlayOk = Settings.canDrawOverlays(this)
        val notifOk = Build.VERSION.SDK_INT < 33 ||
                ContextCompat.checkSelfPermission(this, Manifest.permission.POST_NOTIFICATIONS) ==
                PackageManager.PERMISSION_GRANTED
        val svc = service
        val running = svc?.isCapturing() == true

        // 步骤行：完成=玉 ✓ / 待办=朱砂数字，动作 chip 文案同步
        setStep(numOverlay, tvOverlayAction, overlayOk, "1", "已授权 ✓")
        setStep(numNotify, tvNotifyAction, notifOk, "2", "已就绪 ✓")
        rowNotifyPerm.isEnabled = !notifOk && Build.VERSION.SDK_INT >= 33
        if (running) {
            numCapture.text = "✓"
            numCapture.setBackgroundResource(R.drawable.step_num_done)
            numCapture.setTextColor(colorRes(R.color.qi_jade_text))
        } else {
            numCapture.text = "3"
            numCapture.setBackgroundResource(R.drawable.step_num_idle)
            numCapture.setTextColor(colorRes(R.color.qi_muted))
        }
        btnStartCapture.text = if (running) "屏幕识别运行中 ✓" else "开始屏幕识别"

        // 玻璃状态条：状态点（运行=玉实心 / 未启动=灰空心）+ 文案 + 提示
        vStateDot.background = dotDrawable(
            colorRes(if (running) R.color.qi_jade_dot else R.color.qi_dim),
            hollow = !running
        )
        tvStatus.text = when {
            !running -> "未启动，按下方步骤授权后开始"
            else -> svc!!.getStatusText()
        }
        tvStateHint.text = if (running) "" else "先扫码配对电脑"

        // 卡片 chip：连接态 / 授权态（连接态优先取服务端真实状态）
        val hasConn = etServerUrl.text.isNotBlank() && etServerToken.text.isNotBlank()
        when {
            !hasConn -> setChip(tvChipConn, "未配置", ChipTone.DIM)
            svc?.isRemoteConnected() == true -> setChip(tvChipConn, "已连接", ChipTone.JADE)
            else -> setChip(tvChipConn, "未连接", ChipTone.AMBER)
        }
        if (overlayOk && notifOk) setChip(tvChipPerm, "已就绪", ChipTone.JADE)
        else setChip(tvChipPerm, "待授权", ChipTone.AMBER)
    }

    // ==================== 棋韵小件（chip / 步骤 / 状态点） ====================

    private fun colorRes(resId: Int): Int = ContextCompat.getColor(this, resId)

    /** 状态 chip：描边与文字色按语义档成对切换（玉=就绪 / 琥珀=等待 / 灰=未配置） */
    private fun setChip(tv: TextView, text: String, tone: ChipTone) {
        tv.text = text
        when (tone) {
            ChipTone.JADE -> {
                tv.setBackgroundResource(R.drawable.chip_jade)
                tv.setTextColor(colorRes(R.color.qi_jade_text))
            }
            ChipTone.AMBER -> {
                tv.setBackgroundResource(R.drawable.chip_amber)
                tv.setTextColor(colorRes(R.color.qi_amber_text))
            }
            ChipTone.DIM -> {
                tv.setBackgroundResource(R.drawable.chip_dim)
                tv.setTextColor(colorRes(R.color.qi_muted))
            }
        }
    }

    /** 步骤行状态回填：完成（玉 ✓）或待办（朱砂几何序号），动作 chip 文案同步 */
    private fun setStep(num: TextView, action: TextView, done: Boolean, n: String, doneText: String) {
        if (done) {
            num.text = "✓"
            num.setBackgroundResource(R.drawable.step_num_done)
            num.setTextColor(colorRes(R.color.qi_jade_text))
            action.text = doneText
            action.setTextColor(colorRes(R.color.qi_jade_text))
        } else {
            num.text = n
            num.setBackgroundResource(R.drawable.step_num_todo)
            num.setTextColor(colorRes(R.color.qi_zhu))
            action.text = "去授予"
            action.setTextColor(colorRes(R.color.qi_amber_text))
        }
    }

    /** 状态点：实心或空心圆（空心 = 未启动的弱化语义） */
    private fun dotDrawable(color: Int, hollow: Boolean): GradientDrawable {
        val d = GradientDrawable()
        d.shape = GradientDrawable.OVAL
        if (hollow) {
            d.setColor(0x00000000)
            d.setStroke((1.5f * resources.displayMetrics.density).toInt(), color)
        } else {
            d.setColor(color)
        }
        return d
    }
}
