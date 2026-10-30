package update

import (
	"fmt"
	"os"
	"path/filepath"
)

// 更新中间文件固定名（全部锚定 exe 目录——绿色软件零残留承诺，铁律见包注释）：
const (
	updateZipPart = "cabinet-update.zip.part" // 下载中的半截 zip
	updateZip     = "cabinet-update.zip"      // 下载完成的 zip（校验用，解出即删）
	exeNewSuffix  = ".new"                    // 校验过的新 exe（cabinet.exe.new）
	exeOldSuffix  = ".old"                    // 上一版备份（cabinet.exe.old）
	updateProbe   = "cabinet-update.probe"    // 目录可写探针（瞬时，即建即删）
)

// Writable 探测目录可写（下载与换入全在 exe 目录；绿色软件可能被放进只读
// 目录，此时退回"手动下载"而不是装到一半失败）。
func Writable(dir string) error {
	probe := filepath.Join(dir, updateProbe)
	if err := os.WriteFile(probe, []byte("ok"), 0o644); err != nil {
		return fmt.Errorf("目录不可写: %w", err)
	}
	_ = os.Remove(probe)
	return nil
}

// Apply 换入新 exe（调用方已优雅停止全部服务；运行中的 exe 允许改名、不允许
// 覆盖写，所以是"改名让位 → 新文件就位"两步）。两步任一失败立即回滚并删净
// 新文件——本进程映像已在内存，文件系统变化不影响继续运行旧版本。
// 成功后旧版本保留为 .old（新进程稳定运行后由 RemoveOldBackup 延迟删除，
// 防"新 exe 启动即崩"时失去回退手段）。
func Apply(dir, exeName string) error {
	cur := filepath.Join(dir, exeName)
	old := filepath.Join(dir, exeName+exeOldSuffix)
	neu := filepath.Join(dir, exeName+exeNewSuffix)
	if err := os.Rename(cur, old); err != nil {
		_ = os.Remove(neu)
		return fmt.Errorf("备份当前程序失败: %w", err)
	}
	if err := os.Rename(neu, cur); err != nil {
		// 回滚：老程序改回原名
		if rbErr := os.Rename(old, cur); rbErr != nil {
			return fmt.Errorf("换入失败且回滚也失败（%v）——下次启动将从 .old 恢复: %w", rbErr, err)
		}
		_ = os.Remove(neu)
		return fmt.Errorf("换入新程序失败（已回滚）: %w", err)
	}
	return nil
}

// CleanupOnStart 启动清理：换入中断自愈 + 残留删净。调用方在一切初始化之前
// 执行（单实例锁之前——上次更新中断的恢复与本次是否双开无关）。
//   - cabinet.exe 缺失而 .old 在（换到一半断电/杀软拦截）→ .old 改回原名；
//   - 其余中间文件（.new/.part/.zip/探针）无条件删除。
//
// .old 在 exe 完好时不删（由 RemoveOldBackup 延迟处理）。返回是否执行了恢复。
func CleanupOnStart(dir, exeName string) bool {
	cur := filepath.Join(dir, exeName)
	old := filepath.Join(dir, exeName+exeOldSuffix)
	if _, err := os.Stat(cur); os.IsNotExist(err) {
		if _, err := os.Stat(old); err == nil {
			if os.Rename(old, cur) == nil {
				// 恢复后顺手清掉其余残留
				cleanupResidue(dir, exeName)
				return true
			}
		}
	}
	cleanupResidue(dir, exeName)
	return false
}

// cleanupResidue 删除除 .old 外的全部更新残留。
func cleanupResidue(dir, exeName string) {
	for _, p := range []string{
		filepath.Join(dir, exeName+exeNewSuffix),
		filepath.Join(dir, updateZipPart),
		filepath.Join(dir, updateZip),
		filepath.Join(dir, updateProbe),
	} {
		_ = os.Remove(p)
	}
}

// RemoveOldBackup 删除上一版备份（新进程服务成功运行后延时调用；失败静默——
// 残留 .old 只占一份 exe 体积，下次启动再删）。
func RemoveOldBackup(dir, exeName string) {
	_ = os.Remove(filepath.Join(dir, exeName+exeOldSuffix))
}
