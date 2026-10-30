// 内阁手机端 app 模块：瘦客户端（录屏识别 + WSS 远程分析 + 双悬浮窗）。
// 本地引擎/开局库已移除（分析全部由电脑端"内阁"完成，见 RemoteAnalysisClient）。
// import 须在 plugins 块之前（Kotlin 脚本规则）；java.time 全限定写法会被
// Gradle 的 java 扩展名遮蔽（Unresolved reference），必须显式 import。
import java.time.LocalDate
import java.time.format.DateTimeFormatter

plugins {
    // 版本集中在 phone/gradle/libs.versions.toml（version catalog）。
    // ktlint 插件暂缓接入：ktlint 12.1.0 内嵌解析器与本库源码/Gradle KTS 兼容
    // 问题实测翻车（parse failure + 全仓重排噪音），挂账待 Kotlin 2.x 升级后评估
    alias(libs.plugins.android.application)
    alias(libs.plugins.kotlin.android)
}

// versionCode = git 提交数（交付惯例：Cabinet_日期_versionCode_变体.apk）；git 不可用时回退 1。
val gitCommitCount: Int = runCatching {
    providers.exec {
        commandLine("git", "rev-list", "HEAD", "--count")
        workingDir = rootDir
    }.standardOutput.asText.get().trim().toInt()
}.getOrDefault(1)

// versionName = 最近可达 v 标签（新制语义化版本，2026-10-30 起正式版标签为
// vX.Y.Z）的 git describe 结果，去掉前导 v：release 提交上是 "1.0.0"，标签之后
// 的开发提交是 "1.0.0-3-gabc1234"（一眼看出是开发构建）。不手写常量——版本号
// 只由 git 标签派生，避免发布版与包内版本各写各的（2026-10-30 用户点出
// versionName 仍是 0.1.0 与 v1.0.0 发布不一致的教训）。无 git/无标签回退占位串。
val gitVersionName: String = runCatching {
    providers.exec {
        commandLine("git", "describe", "--tags", "--match", "v*")
        workingDir = rootDir
    }.standardOutput.asText.get().trim().removePrefix("v")
}.getOrDefault("0.1.0-dev")

android {
    namespace = "com.cabinet"
    compileSdk = 34

    defaultConfig {
        applicationId = "com.cabinet"
        minSdk = 26
        targetSdk = 34
        versionCode = gitCommitCount
        versionName = gitVersionName
    }

    // 交付变体通道：对齐主工程 Cabinet 交付惯例（assembleArmv8DotprodRelease）。
    // 本端为纯瘦客户端、不含引擎二进制，变体不收窄 ABI（保留 x86_64 便于模拟器联调）。
    flavorDimensions += "channel"
    productFlavors {
        create("Armv8Dotprod") { dimension = "channel" }
    }

    // ABI 分包（2026-10-28 用户定）：ORT 1.31.1 so 翻倍后 4 ABI 全带让 APK 膨胀到
    // 184MB——只发有真实运行环境的包：arm64-v8a（全部真机）+ x86_64（模拟器联调）。
    // 32 位 armeabi-v7a/x86 无对应运行场景，直接排除不构建。各包同 versionCode
    // （自分发不经 Play，不存在分包 versionCode 区分要求）。
    splits {
        abi {
            isEnable = true
            reset()
            include("arm64-v8a", "x86_64")
            isUniversalApk = false
        }
    }

    buildTypes {
        release {
            isMinifyEnabled = false
            // 无正式 keystore：交付包用 debug 签名保证可直接安装（与模拟器调试安装升级兼容）
            signingConfig = signingConfigs.getByName("debug")
            // TLS 收口（2026-10-27 规范轮）：release 禁止无指纹 trust-all 联调
            // 模式——防指纹为空时静默信任所有证书（RemoteAnalysisClient 消费）
            buildConfigField("boolean", "ENFORCE_TLS_FINGERPRINT", "true")
        }
        debug {
            buildConfigField("boolean", "ENFORCE_TLS_FINGERPRINT", "false")
        }
    }
    buildFeatures {
        buildConfig = true
    }
    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
    kotlinOptions {
        jvmTarget = "17"
    }
    packaging {
        resources.excludes.addAll(listOf("/META-INF/{AL2.0,LGPL2.1}"))
    }
}

dependencies {
    implementation(libs.androidx.core.ktx)
    implementation(libs.androidx.appcompat)
    implementation(libs.androidx.activity.ktx)
    // WSS 远程分析客户端（协议 v1，docs/protocol-v1.md）
    implementation(libs.okhttp)
    // 应用内二维码扫码连接（Apache-2.0，自带相机页与权限处理，无需 Google 服务）
    implementation(libs.zxing.android.embedded)
    // YOLO 棋子检测（CPU 路径；NNAPI delegate 为配置开关，默认关）
    implementation(libs.tensorflow.lite)
    // ONNX 两步式识别引擎（RTMPose 角点 + Swin 分类，模型文件另行放置不进 git）
    implementation(libs.onnxruntime.android)
    // 单元测试（模板匹配等纯 JVM 逻辑）
    testImplementation(libs.junit)
}

// APK 归档：构建产物按交付命名惯例（cabinet_<日期>_vc<提交数>_<abi>.apk）复制到
// 项目根 apk/ 目录（全局 gitignore，不入 git）——APK 交付的唯一固定落点，免得在
// build/outputs 深坑与各处散落文件里翻找。用 rename 把 gradle 默认输出名
// 换成惯例名；日期取本机当天（CI 侧 TZ 已统一北京时区，见 release-build.yml）。
// 用法：./gradlew :app:archiveArmv8DotprodReleaseApk（自动先构建再归档）。
val apkArchiveDate: String =
    LocalDate.now().format(DateTimeFormatter.ofPattern("yyyyMMdd"))
tasks.register<Copy>("archiveArmv8DotprodReleaseApk") {
    dependsOn("assembleArmv8DotprodRelease")
    from("build/outputs/apk/Armv8Dotprod/release")
    include("*-release.apk")
    into(rootDir.parentFile.resolve("apk"))
    rename { fileName ->
        Regex("""app-Armv8Dotprod-(arm64-v8a|x86_64)-release\.apk""").find(fileName)?.let { m ->
            val abi = if (m.groupValues[1] == "arm64-v8a") "arm64" else "x86_64"
            "cabinet_${apkArchiveDate}_vc${gitCommitCount}_${abi}.apk"
        } ?: fileName
    }
}
