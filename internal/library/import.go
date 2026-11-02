// import.go 库文件导入预检（2026-11-01 控制台"导入库文件"）：把用户选中的
// .obk / .zip 规范成单个可校验的 .obk 临时文件。校验 = book.OpenBook 试开
// （bhobk 表 + 列名/列序 schema 核对），把"假桔库/坏库"拦在拷贝进库目录之前
// ——过检后调用方自行拷贝，fsnotify 热加载自动接管。
package library

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"cabinet/internal/book"
)

// PrepareImport 预检待导入文件并返回可直接入库的 .obk 路径与目标文件名。
//   - src 后缀 .zip（含 .obk.zip）：解压到临时目录，取其中第一个 .obk 条目
//   - src 后缀 .obk：直接使用原路径（无临时文件）
//
// 返回 cleanup 无论成功失败都必须调用（清临时目录；.obk 直通时为空操作）。
// 目标文件名恒为 "<库名>.obk"（库名 = 文件名去 .obk 后缀，与 BookInfo.Name
// 同口径）；同名覆盖由调用方（UI 侧 Confirm）决定。
func PrepareImport(src string, logger *slog.Logger) (obkPath, targetName string, cleanup func(), err error) {
	cleanup = func() {}
	if strings.EqualFold(filepath.Ext(src), ".zip") {
		tmp, e := os.MkdirTemp("", "cabinet-book-")
		if e != nil {
			return "", "", cleanup, fmt.Errorf("创建临时目录失败: %w", e)
		}
		cleanup = func() { _ = os.RemoveAll(tmp) }
		if e := unzip(src, tmp); e != nil {
			return "", "", cleanup, fmt.Errorf("zip 解压失败: %w", e)
		}
		// 枚举 + EqualFold 过滤（Glob 模式全平台大小写敏感，zip 内的 X.OBK
		// 会被 "*.obk" 漏掉误报"未找到"）
		ents, e := os.ReadDir(tmp)
		if e != nil {
			return "", "", cleanup, fmt.Errorf("zip 解压目录读取失败: %w", e)
		}
		var matches []string
		for _, ent := range ents {
			if !ent.IsDir() && strings.EqualFold(filepath.Ext(ent.Name()), ".obk") {
				matches = append(matches, filepath.Join(tmp, ent.Name()))
			}
		}
		if len(matches) == 0 {
			return "", "", cleanup, fmt.Errorf("zip 中未找到 .obk 库文件")
		}
		if len(matches) > 1 {
			// 多条目 zip 取首个：解压语义按"一个 zip 一本书"约定（上游 zip
			// 交付形态）；多余条目不静默丢弃提示用户
			if logger != nil {
				logger.Warn("zip 内含多个 .obk，仅导入第一个", "count", len(matches))
			}
		}
		src = matches[0]
	} else if !strings.EqualFold(filepath.Ext(src), ".obk") {
		return "", "", cleanup, fmt.Errorf("不支持的文件类型（仅 .obk 或 .zip）: %s", filepath.Ext(src))
	}

	b, e := book.OpenBook(src, nil)
	if e != nil {
		return "", "", cleanup, e // OpenBook 错误已含路径与具体校验原因
	}
	_ = b.Close()
	// 后缀剥离走 bookNameOf（EqualFold——大小写敏感的 TrimSuffix 会把 X.OBK
	// 剥成双扩展名 X.OBK.obk 落盘）
	return src, bookNameOf(src) + ".obk", cleanup, nil
}
