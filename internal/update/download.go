package update

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// 下载阶段常量：读空闲超时（30s 无数据判定链路卡死）与解压体积上限
// （zip bomb 防护；exe 实际 ~56MB，512MB 余量足够宽容）。
const (
	downloadIdleTimeout = 30 * time.Second
	extractSizeLimit    = 512 << 20
)

// DownloadInstall 确认式下载安装外壳（用户点"下载并安装"即确认，绝不静默
// 调用）：定位当前程序目录并探测可写，全流程见 downloadInstallTo。
func (c *Checker) DownloadInstall(ctx context.Context) {
	dir, exeName, err := ExeSelf()
	if err == nil {
		err = Writable(dir)
	}
	if err != nil {
		c.mu.Lock()
		c.state = StateFailed
		c.message = "程序目录不可写，无法自动安装，请到 Release 页手动下载"
		c.logger.Info("更新安装前置检查失败", "err", err)
		c.mu.Unlock()
		return
	}
	c.downloadInstallTo(ctx, dir, exeName)
}

// downloadInstallTo 下载安装内核（dir/exeName 参数化，测试注入临时目录）：
// 下载 zip（.part→改名）→ sha256 官方值校验 → 解出新 exe（.new，验 MZ 头）→
// 删 zip → 状态 ready_reboot。真正的换入（rename 替换）在服务全部停止后由
// swap.Apply 执行——运行中 exe 只允许改名、不允许覆盖写。
// 任一步失败：删净中间文件、状态回退/置败，绝不留残缺现场（绿色软件铁律）。
func (c *Checker) downloadInstallTo(ctx context.Context, dir, exeName string) {
	c.mu.Lock()
	if !c.canInstallLocked() {
		c.mu.Unlock() // 防重入 + 防越权：状态不对直接忽略
		return
	}
	c.state = StateDownloading
	c.message = "正在下载更新包……"
	c.progress = 0
	dlCtx, cancel := context.WithCancel(ctx)
	c.cancel = cancel
	assetURL, digest, size := c.assetURL, c.assetDigest, c.assetSize
	c.mu.Unlock()
	defer cancel()

	zipPath := filepath.Join(dir, updateZip)
	err := c.doDownload(dlCtx, cancel, assetURL, zipPath, size)
	if err == nil {
		err = verifyFileSHA256(zipPath, digest)
	}
	if err == nil {
		err = extractExe(zipPath, dir, exeName)
	}
	// zip 无论成败，走完即无存在价值（校验失败/解压失败同样删——垃圾零残留）
	_ = os.Remove(zipPath)

	// 错误分类：空闲超时（读 30s 无数据）→ failed；用户取消 → 回退可重试；
	// 其余 → failed。idleExpired 标志在定时器里置位，这里消费一次。
	idle := c.takeIdleExpired()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cancel = nil
	switch {
	case idle:
		_ = os.Remove(filepath.Join(dir, updateZipPart))
		_ = os.Remove(filepath.Join(dir, exeName+exeNewSuffix))
		c.state = StateFailed
		c.message = "下载超时：网络长时间无响应"
		c.progress = 0
	case errors.Is(err, context.Canceled):
		// 用户取消：回退到"可再点安装"，删半截文件
		_ = os.Remove(filepath.Join(dir, updateZipPart))
		c.state = StateUpdateAvail
		c.message = "已取消下载"
		c.progress = 0
	case err != nil:
		_ = os.Remove(filepath.Join(dir, updateZipPart))
		_ = os.Remove(filepath.Join(dir, exeName+exeNewSuffix))
		c.state = StateFailed
		c.message = "更新失败：" + err.Error()
		c.logger.Info("更新下载/校验失败（静默处理）", "err", err)
		c.progress = 0
	default:
		c.state = StateReadyReboot
		c.message = "更新已就绪，正在重启应用……"
	}
}

// doDownload 流式下载资产到 dest：先写 .part（半截文件绝不冒充成品），完成后
// 原子改名。进度按 Content-Length（缺省回退资产 size 字段）回写状态机。
// 读空闲超时：每次 Read 前重置定时器，30s 无数据 → 置 idleExpired 标志并
// cancel——大文件慢速经代理属正常，不能用总超时一刀切。
func (c *Checker) doDownload(ctx context.Context, cancel context.CancelFunc, assetURL, dest string, sizeHint int64) error {
	partPath := dest + ".part"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, assetURL, nil)
	if err != nil {
		return fmt.Errorf("构造下载请求失败: %w", err)
	}
	req.Header.Set("User-Agent", "cabinet/"+c.version)
	timer := time.AfterFunc(downloadIdleTimeout, func() {
		c.mu.Lock()
		c.idleExpired = true
		c.mu.Unlock()
		cancel()
	})
	defer timer.Stop()

	resp, err := c.dlClient.Do(req)
	if err != nil {
		return fmt.Errorf("下载失败: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("下载返回 %d", resp.StatusCode)
	}
	total := resp.ContentLength
	if total <= 0 {
		total = sizeHint
	}

	f, err := os.Create(partPath)
	if err != nil {
		return fmt.Errorf("创建下载临时文件失败: %w", err)
	}
	defer func() { _ = os.Remove(partPath) }() // 成功路径已改名（Remove 报不存在，无碍）
	defer func() { _ = f.Close() }()

	buf := make([]byte, 64*1024)
	var written int64
	for {
		timer.Reset(downloadIdleTimeout)
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				return fmt.Errorf("写入下载文件失败: %w", werr)
			}
			written += int64(n)
			if total > 0 {
				c.mu.Lock()
				c.progress = float64(written) / float64(total)
				c.mu.Unlock()
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return fmt.Errorf("下载中断: %w", rerr)
		}
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("关闭下载文件失败: %w", err)
	}
	if err := os.Rename(partPath, dest); err != nil {
		return fmt.Errorf("下载文件改名失败: %w", err)
	}
	return nil
}

// takeIdleExpired 消费空闲超时标志（置位一次即生效）。
func (c *Checker) takeIdleExpired() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	v := c.idleExpired
	c.idleExpired = false
	return v
}

// verifyFileSHA256 对照官方 digest（"sha256:<hex>"）校验文件完整性。
// 无 digest 的资产在 pickUpdateAsset 阶段已被排除，走到这里 digest 必有。
func verifyFileSHA256(path, digest string) error {
	const prefix = "sha256:"
	if !strings.HasPrefix(digest, prefix) {
		return fmt.Errorf("校验值格式异常: %q", digest)
	}
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("打开升级包失败: %w", err)
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return fmt.Errorf("读取升级包失败: %w", err)
	}
	if !strings.EqualFold(hex.EncodeToString(h.Sum(nil)), strings.TrimPrefix(digest, prefix)) {
		return fmt.Errorf("sha256 校验不匹配（升级包可能损坏）")
	}
	return nil
}

// extractExe 从升级 zip 解出 cabinet.exe 写成 <exeName>.new（目标文件名固定，
// 不按 zip 内路径落盘——zip slip 无落脚点）。校验非空 + MZ 头（PE 标识）。
// zip 内按 basename 匹配（portable-pack 打包可能是根路径也可能带目录前缀）。
func extractExe(zipPath, dir, exeName string) error {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return fmt.Errorf("打开升级包失败: %w", err)
	}
	defer func() { _ = r.Close() }()
	for _, f := range r.File {
		if f.FileInfo().IsDir() || filepath.Base(f.Name) != exeName {
			continue
		}
		if f.UncompressedSize64 > extractSizeLimit {
			return fmt.Errorf("升级包内文件异常过大（%d 字节）", f.UncompressedSize64)
		}
		rc, err := f.Open()
		if err != nil {
			return fmt.Errorf("读取升级包内 %s 失败: %w", f.Name, err)
		}
		defer func() { _ = rc.Close() }()
		newPath := filepath.Join(dir, exeName+exeNewSuffix)
		out, err := os.Create(newPath)
		if err != nil {
			return fmt.Errorf("写入新程序失败: %w", err)
		}
		n, cerr := io.Copy(out, io.LimitReader(rc, extractSizeLimit))
		closeErr := out.Close()
		if cerr != nil {
			_ = os.Remove(newPath)
			return fmt.Errorf("解压新程序失败: %w", cerr)
		}
		if closeErr != nil {
			_ = os.Remove(newPath)
			return fmt.Errorf("保存新程序失败: %w", closeErr)
		}
		if n == 0 {
			_ = os.Remove(newPath)
			return fmt.Errorf("升级包内 %s 为空", f.Name)
		}
		if err := verifyMZ(newPath); err != nil {
			_ = os.Remove(newPath)
			return err
		}
		return nil
	}
	return fmt.Errorf("升级包内未找到 %s", exeName)
}

// verifyMZ 校验文件以 PE 头（MZ）开始——防解压出错装上残缺二进制。
func verifyMZ(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	var magic [2]byte
	if _, err := io.ReadFull(f, magic[:]); err != nil {
		return fmt.Errorf("读取新程序头失败: %w", err)
	}
	if magic[0] != 'M' || magic[1] != 'Z' {
		return fmt.Errorf("新程序缺少 PE 头（文件损坏）")
	}
	return nil
}

// ExeSelf 定位当前程序（dir, exeName）：绿色软件的"安装路径"——换入、备份、
// 下载中间文件全部锚定这里，与 CWD 无关。
func ExeSelf() (dir, exeName string, err error) {
	p, err := os.Executable()
	if err != nil {
		return "", "", fmt.Errorf("无法定位当前程序: %w", err)
	}
	return filepath.Dir(p), filepath.Base(p), nil
}

// Cancel 取消进行中的下载（无进行中下载时空操作）。
func (c *Checker) Cancel() {
	c.mu.Lock()
	cancel := c.cancel
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}
