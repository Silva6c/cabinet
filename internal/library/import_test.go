package library

import (
	"os"
	"path/filepath"
	"testing"
)

// TestPrepareImport 导入预检三态：合法 .obk 直通返回目标名；zip 自动解出
// .obk；schema 不符的假库 / 非法后缀被拒（坏库不进库目录的第一道闸）。
func TestPrepareImport(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "我的库.obk")
	makeBook(t, good)

	// 合法 .obk：直通（无临时文件，cleanup 空操作），目标名 = <名>.obk
	obk, target, cleanup, err := PrepareImport(good, nil)
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		t.Fatalf("合法 .obk 应通过: %v", err)
	}
	if obk != good || target != "我的库.obk" {
		t.Fatalf("直通路径/目标名不符: obk=%q target=%q", obk, target)
	}

	// zip 包裹：解出 .obk，目标名取条目名
	zipPath := filepath.Join(dir, "打包库.obk.zip")
	makeZip(t, zipPath, map[string][]byte{"内库.obk": []byte("placeholder")})
	// zip 内是占位内容——解出后过不了 OpenBook 校验，先只验证"解压+目标名"
	// 路径正确（schema 校验由下一用例覆盖），故本段预期报 schema 错而非解压错
	_, _, cleanup2, err := PrepareImport(zipPath, nil)
	if cleanup2 != nil {
		defer cleanup2()
	}
	if err == nil {
		t.Fatal("占位内容 zip 应在 OpenBook 校验失败")
	}

	// 合法库打成 zip：完整通过（解压 → 校验）
	realData, err := os.ReadFile(good)
	if err != nil {
		t.Fatal(err)
	}
	zipPath2 := filepath.Join(dir, "真库.obk.zip")
	makeZip(t, zipPath2, map[string][]byte{"真库.obk": realData})
	obk2, target2, cleanup3, err := PrepareImport(zipPath2, nil)
	if cleanup3 != nil {
		defer cleanup3()
	}
	if err != nil || target2 != "真库.obk" {
		t.Fatalf("合法 zip 应通过: err=%v target=%q", err, target2)
	}
	if obk2 == zipPath2 {
		t.Fatal("zip 路径应被替换为解出的临时 .obk")
	}

	// 非法后缀拒绝
	if _, _, _, err := PrepareImport(filepath.Join(dir, "x.txt"), nil); err == nil {
		t.Fatal("非法后缀应被拒绝")
	}

	// 坏库（缺 bhobk 表）拒绝：错误经 book.OpenBook 冒出，内容可读
	bad := filepath.Join(dir, "bad.obk")
	if err := os.WriteFile(bad, []byte("not a sqlite"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, cleanup4, err := PrepareImport(bad, nil); err == nil {
		t.Fatal("坏库应被拒绝")
	} else if cleanup4 != nil {
		cleanup4()
	}
}
