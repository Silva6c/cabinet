package com.cabinet.assist

import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * 证书指纹归一化单测：录入端容忍大写与冒号/空格分隔，统一为 64 位小写 hex；
 * 与 RemoteAnalysisClient 的锁定口径（同一归一化后逐字节比对）保持一致。
 */
class AssistConfigFingerprintTest {

    @Test
    fun `大写与冒号分隔归一化为小写无分隔`() {
        // 用真实证书指纹（cert/server.crt 的 SHA-256），冒号串程序化构造避免手抄错位
        val expected = "32d5073e7b9d05c5da9140391ed2ed800b3c0f31774c57fd6fe8559289982ff4"
        val raw = expected.chunked(2).joinToString(":").uppercase()
        assertEquals(expected, AssistConfig.normalizeFingerprint(raw))
    }

    @Test
    fun `小写无分隔原样保留`() {
        val raw = "a".repeat(64)
        assertEquals(raw, AssistConfig.normalizeFingerprint(raw))
    }

    @Test
    fun `空格分隔也容忍`() {
        val raw = "AA BB CC 00 11"
        assertEquals("aabbcc0011", AssistConfig.normalizeFingerprint(raw))
    }

    @Test
    fun `空串归一化仍为空`() {
        assertEquals("", AssistConfig.normalizeFingerprint("   "))
    }

    @Test
    fun `非法字符不被吞掉——交给连接端拒绝而非静默匹配`() {
        val out = AssistConfig.normalizeFingerprint("zz:11")
        // 归一化只做大小写与分隔符处理，不校验 hex；格式校验在 RemoteAnalysisClient，
        // 非法指纹在连接端被明确拒绝（绝不静默降级为不校验）。
        assertTrue(out.startsWith("zz"))
    }
}
