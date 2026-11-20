package com.cabinet.assist.ui

import android.view.Gravity
import android.widget.Button
import android.widget.FrameLayout
import android.widget.TextView
import com.google.zxing.BarcodeFormat
import com.google.zxing.DecodeHintType
import com.journeyapps.barcodescanner.CaptureActivity
import com.journeyapps.barcodescanner.DecoratedBarcodeView
import com.journeyapps.barcodescanner.DefaultDecoderFactory
import com.journeyapps.barcodescanner.ViewfinderView
import com.journeyapps.barcodescanner.camera.CameraSettings.FocusMode

/**
 * 自定义扫码页（连接电脑配对用）：在 zxing 默认 CaptureActivity 基础上做五点优化——
 * ① 解码 hints 加 TRY_HARDER：电脑屏幕上的二维码常为远距/小尺寸/斜角，默认参数
 *    单帧识别率低，多等好几秒才出结果；TRY_HARDER 逐帧更彻底地尝试定位，总体
 *    "对准→出结果"时间显著缩短（单帧略慢，命中更早）。
 * ② 只扫 QR_CODE：POSSIBLE_FORMATS 限定后跳过一维码等无关格式的逐个尝试。
 * ③ 显式连续对焦：扫屏幕属于近距微距场景，焦距漂移是"看着清楚却识别不出"的主因。
 * ④ 手电筒按钮（2026-11-17 扫码五件套）：zxing 默认布局无此按钮，暗光房间扫
 *    亮屏幕反而需要背光辅助压反光——无开关时用户只能干等。
 * ⑤ 界面精修（2026-11-18 用户拍板"样式精修"）：取景框三色与状态文字胶囊，
 *    见 styleChrome。
 * 经 ScanOptions.setCaptureActivity(QrScanActivity::class.java) 启用。
 */
class QrScanActivity : CaptureActivity() {

    override fun initializeContent(): DecoratedBarcodeView {
        val view = super.initializeContent()
        val barcode = view.getBarcodeView()
        // 仅 QR + TRY_HARDER：hints 经 DefaultDecoderFactory 注入解码线程
        barcode.setDecoderFactory(
            DefaultDecoderFactory(
                listOf(BarcodeFormat.QR_CODE),
                mapOf(DecodeHintType.TRY_HARDER to true),
                null, 0
            )
        )
        val settings = barcode.cameraSettings
        settings.isContinuousFocusEnabled = true
        settings.focusMode = FocusMode.CONTINUOUS
        barcode.cameraSettings = settings
        styleChrome(view)
        addTorchButton(view)
        return view
    }

    /** 界面精修（2026-11-18 用户拍板：不动 zxing 布局结构，只调视觉两处）——
     *  ①取景框 mask 压暗聚焦：默认 #60000000 → #8C000000，四周更暗、取景区更
     *    突出（frame 描边 4.3.0 无公开 setter，javap 实查仅 setMaskColor/
     *    setLaserVisibility 两个色值方法——frame 保持库默认，mask 压暗后取景框
     *    自然突出）；laser 扫描线保持库默认。
     *  ②状态文字（setPrompt 提示文案显示处，经 getStatusView() 取引用）白字+
     *    半透明黑底胶囊，与手电筒按钮同一套半透明黑底视觉。 */
    private fun styleChrome(view: DecoratedBarcodeView) {
        val density = resources.displayMetrics.density
        view.viewFinder.setMaskColor(0x8C000000.toInt())
        view.statusView?.apply {
            setTextColor(0xFFFFFFFF.toInt())
            val pad = (10 * density).toInt()
            setPadding(pad, pad / 2, pad, pad / 2)
            background = android.graphics.drawable.GradientDrawable().apply {
                cornerRadius = 12f * density
                setColor(0x88000000.toInt())
            }
        }
    }

    /** 手电筒悬浮按钮：addContentView 叠在库默认布局之上（不动 zxing 布局结构，
     *  CaptureManager 生命周期不受影响；切窗/熄屏由库在 onPause 统一关灯）。
     *  2026-11-17 用户反馈定稿：放**顶部水平居中**——zxing 默认 status_view（含
     *  setPrompt 提示文案）贴底居中，原左下角按钮与两行长提示文字水平重叠；
     *  顶部无任何库组件，既居中显眼又零冲突。白描边高亮（棋韵描边分层思路，
     *  半透明黑底 + 2dp 白边）保证取景框亮暗两种背景下按钮轮廓都清晰。 */
    private fun addTorchButton(view: DecoratedBarcodeView) {
        val btn = Button(this)
        btn.text = "💡 手电筒"
        btn.isAllCaps = false
        val density = resources.displayMetrics.density
        val pad = (14 * density).toInt()
        btn.setPadding(pad, pad / 2, pad, pad / 2)
        btn.background = android.graphics.drawable.GradientDrawable().apply {
            cornerRadius = 12f * density
            setColor(0x88000000.toInt()) // 半透明黑底（值超 Int 上限需显式 toInt），任何主题下可读
            setStroke((2 * density).toInt(), 0xFFFFFFFF.toInt()) // 高亮白描边
        }
        btn.setTextColor(0xFFFFFFFF.toInt())
        var torchOn = false
        btn.setOnClickListener {
            torchOn = !torchOn
            // journeyapps 4.3.0 无 setTorch(boolean)，开关是独立方法对
            if (torchOn) view.setTorchOn() else view.setTorchOff()
            btn.text = if (torchOn) "💡 关灯" else "💡 手电筒"
        }
        val lp = FrameLayout.LayoutParams(
            FrameLayout.LayoutParams.WRAP_CONTENT,
            FrameLayout.LayoutParams.WRAP_CONTENT,
            Gravity.TOP or Gravity.CENTER_HORIZONTAL
        )
        lp.topMargin = (24 * density).toInt()
        addContentView(btn, lp)
    }
}
