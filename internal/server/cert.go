package server

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
// 铁律：cert/ 目录下的证书一经生成，后续任何启动都绝不自动重建。
//
// 手机端（M2 起）将固定本证书的指纹作为服务器校验依据，重建证书 =
// 全部已配对手机断联，必须重新配对。因此：
//   - 证书文件存在：直接加载复用，哪怕 SAN 缺本机新 IP、哪怕临近过期；
//   - 证书文件损坏：报错退出，由人工删除 cert/ 下文件后重签；
//   - 确需重签：人工删除 cert/server.crt 与 cert/server.key，并让所有手机重新配对。
// ─────────────────────────────────────────────────────────────────────────────

const (
	certFileName = "server.crt"
	keyFileName  = "server.key"
	// certValidityDays 自签证书有效期 10 年
	certValidityDays = 3650
)

// EnsureCert 返回服务所用证书：certDir 下已有证书则加载（绝不重建），
// 没有则生成自签证书并持久化。
func EnsureCert(certDir string) (tls.Certificate, error) {
	certPath := filepath.Join(certDir, certFileName)
	keyPath := filepath.Join(certDir, keyFileName)

	if fileExists(certPath) || fileExists(keyPath) {
		// 任一文件存在即走加载路径：绝不静默重建缺一半的证书对
		cert, err := tls.LoadX509KeyPair(certPath, keyPath)
		if err != nil {
			return tls.Certificate{}, fmt.Errorf(
				"加载已有证书失败（绝不自动重建；如需重签请人工删除 %s 与 %s，并让所有手机重新配对）: %w",
				certPath, keyPath, err)
		}
		return cert, nil
	}

	if err := os.MkdirAll(certDir, 0o755); err != nil {
		return tls.Certificate{}, fmt.Errorf("创建证书目录失败: %w", err)
	}
	if err := generateSelfSigned(certPath, keyPath); err != nil {
		return tls.Certificate{}, err
	}
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("加载新生成的证书失败: %w", err)
	}
	return cert, nil
}

// generateSelfSigned 生成 ECDSA P-256 自签证书并写盘。
// SAN 覆盖：localhost、cabinet.local 与本机所有非环回 IPv4（局域网手机按
// IP 连接）——**不含主机名**（2026-11-08 用户拍板，业界主流形态：SAN 与
// 部署环境解耦）。历史教训：曾把 os.Hostname() 原样写进 DNS SAN，中文电脑
// 名（外部用户实测"无序"）在 x509.CreateCertificate 处报 IA5String 编码
// 失败、首次启动即静默退出——SAN 里的主机名从第一天起就是零功能（手机按
// IP 直连、靠二维码指纹配对），固定服务名让任何电脑名永不踩坑；将来若做
// mDNS 自动发现，cabinet.local 即刻成为有真实消费方的名字。
func generateSelfSigned(certPath, keyPath string) error {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("生成私钥失败: %w", err)
	}

	serial, err := rand.Int(rand.Reader, serialLimit)
	if err != nil {
		return fmt.Errorf("生成序列号失败: %w", err)
	}

	notBefore := time.Now().Add(-time.Hour) // 时钟略回拨，避免刚生成即"未生效"
	tmpl := x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   ServerName,
			Organization: []string{"cabinet"},
		},
		NotBefore:             notBefore,
		NotAfter:              notBefore.AddDate(0, 0, certValidityDays),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              sanHostnames(),
		IPAddresses:           sanIPs(),
	}

	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		return fmt.Errorf("签发证书失败: %w", err)
	}

	if err := writePEM(certPath, "CERTIFICATE", der, 0o644); err != nil {
		return err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return fmt.Errorf("序列化私钥失败: %w", err)
	}
	return writePEM(keyPath, "EC PRIVATE KEY", keyDER, 0o600)
}

// serialLimit 证书序列号上限：2^128 内随机正整数（RFC 5280 建议 ≤20 字节）
var serialLimit = new(big.Int).Lsh(big.NewInt(1), 128)

// CertificateFingerprint 计算证书叶子的 SHA-256 指纹（64 位小写 hex）。
// 口径：对证书 DER 全文（x509 标准口径，openssl/certutil 显示的一致），
// 手机端 TLS 握手按同一摘要校验；二维码 f 参数与控制台展示均用此值。
func CertificateFingerprint(cert tls.Certificate) (string, error) {
	if len(cert.Certificate) == 0 {
		return "", fmt.Errorf("证书链为空，无法计算指纹")
	}
	// Leaf 为链首 DER（LoadX509KeyPair 时即证书文件内容）
	sum := sha256.Sum256(cert.Certificate[0])
	return hex.EncodeToString(sum[:]), nil
}

// fileExists 报告路径是否存在（存在即 true，不区分文件/目录）。
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// certServiceDNS 证书 DNS SAN 的固定服务名：mDNS 风格（RFC 6762/6763），
// 为将来 mDNS 自动发现预留有真实消费方的名字；当前仅作稳定标识。
const certServiceDNS = "cabinet.local"

// sanHostnames 证书的 DNS SAN：固定 [localhost, cabinet.local]，与部署环境
// （主机名/语言/字符集）完全解耦——2026-11-08 用户拍板的业界主流形态，
// 任何电脑名永不触发 IA5String 编码失败。注意：IP 一律放 IP SAN。
func sanHostnames() []string {
	return []string{"localhost", certServiceDNS}
}

// sanIPs 证书的 IP SAN：环回地址 + 本机所有非环回 IPv4。
func sanIPs() []net.IP {
	ips := []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return ips
	}
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok || ipnet.IP.IsLoopback() || ipnet.IP.To4() == nil {
			continue
		}
		ips = append(ips, ipnet.IP)
	}
	return ips
}

// writePEM 按 PEM 编码写文件，权限仅作用于类 Unix 平台（Windows 忽略）。
func writePEM(path, blockType string, der []byte, perm os.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return fmt.Errorf("创建 %s 失败: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	return pem.Encode(f, &pem.Block{Type: blockType, Bytes: der})
}
