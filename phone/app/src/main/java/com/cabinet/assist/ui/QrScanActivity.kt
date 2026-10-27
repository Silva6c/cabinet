package com.cabinet.assist.ui

import com.google.zxing.BarcodeFormat
import com.google.zxing.DecodeHintType
import com.journeyapps.barcodescanner.CaptureActivity
import com.journeyapps.barcodescanner.DecoratedBarcodeView
import com.journeyapps.barcodescanner.DefaultDecoderFactory
import com.journeyapps.barcodescanner.camera.CameraSettings.FocusMode

/**
 * 自定义扫码页（连接电脑配对用）：在 zxing 默认 CaptureActivity 基础上做三点提速——
 * ① 解码 hints 加 TRY_HARDER：电脑屏幕上的二维码常为远距/小尺寸/斜角，默认参数
 *    单帧识别率低，多等好几秒才出结果；TRY_HARDER 逐帧更彻底地尝试定位，总体
 *    "对准→出结果"时间显著缩短（单帧略慢，命中更早）。
 * ② 只扫 QR_CODE：POSSIBLE_FORMATS 限定后跳过一维码等无关格式的逐个尝试。
 * ③ 显式连续对焦：扫屏幕属于近距微距场景，焦距漂移是"看着清楚却识别不出"的主因。
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
        return view
    }
}
