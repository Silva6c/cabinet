package server

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
)

// TokenBytes 令牌随机字节数；编码为 hex 后即协议约定的 64 字符令牌。
const TokenBytes = 32

// GenerateToken 生成随机配对令牌（64 hex 字符）。M0 用于首次启动写入配置；
// M1 配对流程改为服务器只存哈希、令牌经二维码分发，生成本身不变。
func GenerateToken() (string, error) {
	b := make([]byte, TokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("读取随机源失败: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// ValidateToken 恒时比较客户端提供的令牌与期望令牌，防时序侧信道。
func ValidateToken(provided, expected string) bool {
	if len(provided) == 0 || len(expected) == 0 {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) == 1
}
