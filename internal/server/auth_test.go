package server

import (
	"encoding/hex"
	"testing"
)

func TestGenerateToken(t *testing.T) {
	tok, err := GenerateToken()
	if err != nil {
		t.Fatal(err)
	}
	if len(tok) != TokenBytes*2 {
		t.Fatalf("令牌长度应为 %d hex 字符, got %d", TokenBytes*2, len(tok))
	}
	if _, err := hex.DecodeString(tok); err != nil {
		t.Fatalf("令牌应为合法 hex: %v", err)
	}

	// 随机性抽查：两次生成不重复
	tok2, _ := GenerateToken()
	if tok == tok2 {
		t.Fatal("两次生成的令牌不应相同")
	}
}

func TestValidateToken(t *testing.T) {
	const tok = "a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90"
	if !ValidateToken(tok, tok) {
		t.Fatal("相同令牌应通过校验")
	}
	if ValidateToken(tok, tok+"0") {
		t.Fatal("不同长度令牌应拒绝")
	}
	if ValidateToken(tok+" ", tok) {
		t.Fatal("带尾随空格应拒绝")
	}
	if ValidateToken("", tok) || ValidateToken(tok, "") {
		t.Fatal("空令牌应拒绝")
	}
}
