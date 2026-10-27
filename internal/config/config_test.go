package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoad_CreatesDefaultWhenMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "cabinet.toml")
	cfg, created, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("文件不存在时应报告 created=true")
	}
	if cfg.Bind != ":9527" || cfg.TLS.CertDir != "cert" || cfg.Log.Level != "info" {
		t.Fatalf("默认值不符: %+v", cfg)
	}
	// HashMB 默认 256（默认最低档+切换落盘；0 仍是合法自动档，
	// 经控制台"自动"选项落盘——Load 不回填 HashMB，0 原样生效 2GiB）
	if cfg.Engine.HashMB != 256 {
		t.Fatalf("默认 HashMB 应为 256: %d", cfg.Engine.HashMB)
	}
	// 配置文件应已写出（供人工检查/修改 token）
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("默认配置文件未写出: %v", err)
	}
}

func TestLoad_ParsesExistingAndFillsDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cabinet.toml")
	content := `
bind = ":9600"
token = "abc"

[tls]
cert_dir = "mycert"

[log]
level = "debug"
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, created, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Fatal("已存在的文件不应报告 created=true")
	}
	if cfg.Bind != ":9600" || cfg.Token != "abc" || cfg.TLS.CertDir != "mycert" || cfg.Log.Level != "debug" {
		t.Fatalf("解析结果不符: %+v", cfg)
	}
	// 未写出的日志字段应回填默认值，不能是 0
	if cfg.Log.MaxSizeMB != 10 || cfg.Log.MaxBackups != 5 {
		t.Fatalf("缺省日志字段应回填默认值: %+v", cfg.Log)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cabinet.toml")
	in := Default()
	in.Bind = ":12345"
	in.Token = "tok123"
	in.Log.Level = "warn"
	if err := in.Save(path); err != nil {
		t.Fatal(err)
	}
	out, _, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if out.Bind != in.Bind || out.Token != in.Token || out.Log.Level != in.Log.Level || out.TLS.CertDir != in.TLS.CertDir {
		t.Fatalf("往返不一致:\n in:  %+v\n out: %+v", in, out)
	}
}
