package server

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestEnsureCert_GeneratesNew(t *testing.T) {
	dir := t.TempDir()
	cert, err := EnsureCert(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(cert.Certificate) == 0 {
		t.Fatal("证书链为空")
	}
	for _, f := range []string{certFileName, keyFileName} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Fatalf("证书文件未生成: %s: %v", f, err)
		}
	}
	// 证书必须能被标准 TLS 栈解析且在有效期内
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatalf("证书解析失败: %v", err)
	}
	if !time.Now().Before(leaf.NotAfter) || !time.Now().After(leaf.NotBefore) {
		t.Fatalf("证书有效期异常: %s ~ %s", leaf.NotBefore, leaf.NotAfter)
	}
	if len(leaf.DNSNames) == 0 || leaf.DNSNames[0] != "localhost" {
		t.Fatalf("DNS SAN 应包含 localhost: %v", leaf.DNSNames)
	}
	if len(leaf.IPAddresses) == 0 {
		t.Fatal("IP SAN 不应为空（至少含环回地址）")
	}
}

// TestEnsureCert_DoesNotOverwrite 铁律测试：cert/ 已有证书时再启动，文件绝不被覆盖。
// 手机端将固定证书指纹，重建 = 全部手机断联（见 cert.go 顶部铁律注释）。
func TestEnsureCert_DoesNotOverwrite(t *testing.T) {
	dir := t.TempDir()
	if _, err := EnsureCert(dir); err != nil {
		t.Fatal(err)
	}
	crtBefore, err := os.ReadFile(filepath.Join(dir, certFileName))
	if err != nil {
		t.Fatal(err)
	}
	keyBefore, err := os.ReadFile(filepath.Join(dir, keyFileName))
	if err != nil {
		t.Fatal(err)
	}

	// 再次启动（多次），文件内容必须逐字节不变
	for i := 0; i < 3; i++ {
		if _, err := EnsureCert(dir); err != nil {
			t.Fatalf("第 %d 次复用已有证书失败: %v", i+1, err)
		}
	}
	crtAfter, err := os.ReadFile(filepath.Join(dir, certFileName))
	if err != nil {
		t.Fatal(err)
	}
	keyAfter, err := os.ReadFile(filepath.Join(dir, keyFileName))
	if err != nil {
		t.Fatal(err)
	}
	if string(crtBefore) != string(crtAfter) || string(keyBefore) != string(keyAfter) {
		t.Fatal("已有证书被覆盖！违反 cert.go 铁律：证书一经生成绝不自动重建")
	}
}

// TestEnsureCert_CorruptCertFails 证书文件损坏时必须报错退出，
// 绝不静默重建（静默重建会让客户端指纹校验悄悄失效）。
func TestEnsureCert_CorruptCertFails(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, certFileName), []byte("garbage"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureCert(dir); err == nil {
		t.Fatal("损坏的证书应返回错误，而不是重建或静默通过")
	}
	// 报错后原文件保持原样
	b, err := os.ReadFile(filepath.Join(dir, certFileName))
	if err != nil || string(b) != "garbage" {
		t.Fatalf("报错后不应改动损坏的证书文件: %v %q", err, b)
	}
}

// TestEnsureCert_HalfPairFails 只有一半证书对时也应报错（绝不静默重建缺失的一半）。
func TestEnsureCert_HalfPairFails(t *testing.T) {
	dir := t.TempDir()
	if _, err := EnsureCert(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, keyFileName)); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureCert(dir); err == nil {
		t.Fatal("证书对缺失一半应报错（人工删除后重签），而不是自动重建")
	}
}

// TestCertificateFingerprint 指纹口径：64 位小写 hex，且与叶子证书 DER 的
// SHA-256 摘要一致（手机端按同一摘要校验；两次计算必须稳定）。
func TestCertificateFingerprint(t *testing.T) {
	cert, err := EnsureCert(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	fp, err := CertificateFingerprint(cert)
	if err != nil {
		t.Fatal(err)
	}
	if len(fp) != 64 {
		t.Fatalf("指纹应为 64 位 hex: %s", fp)
	}
	for _, r := range fp {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			t.Fatalf("指纹应全为小写 hex: %s", fp)
		}
	}
	// 与标准库直接摘要比对（DER 口径）
	sum := sha256.Sum256(cert.Certificate[0])
	if fp != hex.EncodeToString(sum[:]) {
		t.Fatalf("指纹与 DER SHA-256 不符: %s", fp)
	}
	// 稳定性：重算一致
	if fp2, _ := CertificateFingerprint(cert); fp2 != fp {
		t.Fatalf("同证书两次指纹不一致: %s vs %s", fp, fp2)
	}
	// 空证书链报错
	if _, err := CertificateFingerprint(tls.Certificate{}); err == nil {
		t.Fatal("空证书链应报错")
	}
}
