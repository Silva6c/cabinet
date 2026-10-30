// portable-pack 绿色便携双轨打包工具（Go 实现，2026-10-07）。
//
// 取代早先的 bat 实现：cmd 批处理下 UTF-8 中文字节被 936 代码页解码错位会搅碎
// 整个脚本、shift 还会把 %0 一并移走使 %~dp0 退化——Go 侧无任何编码/参数坑。
// 入口 scripts/package-portable.bat 是纯 ASCII 薄壳：go build 本工具并运行。
//
// 产出（dist\ 下两个 zip，标准 Deflate，中文名自动带 UTF-8 标志，资源管理器
// Win10 1803+ 原生可解）：
//
//	Cabinet-Portable-YYYYMMDD-Full.zip   首装全量包：
//	  cabinet.exe + appdata\（configs 示例 + 引擎 + NNUE + 桔库）+ 使用说明.txt
//	  —— cert\data\logs 不打包，首次运行自动生成在 appdata\ 内。
//	Cabinet-Update-YYYYMMDD.zip          升级增量包：默认仅 cabinet.exe，
//	  -with-engine 附引擎+NNUE，-with-books 附桔库（保持 appdata\library\ 内部路径）。
//
// 红线：Pikafish.2026-09-06\ 与 library\books\ 源文件只读复制——打包时
// "改名 pikafish.exe"是复制产出，绝不修改/重命名源文件本体。
// 所有路径按项目根相对定位（bat 壳已 cd 到根；手动运行也请从根执行）。
package main

import (
	"archive/zip"
	"debug/pe"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/text/encoding/simplifiedchinese"
)

// 打包素材锚定路径（相对项目根）
const (
	engineSrc  = "Pikafish.2026-09-06/Pikafish-Windows-x86-64-universal.exe"
	engineName = "pikafish.exe" // 便携包内按约定改名（复制产出，源文件不动）
	nnueSrc    = "Pikafish.2026-09-06/pikafish.nnue"
	exampleSrc = "configs/cabinet.example.toml"
	readmeSrc  = "scripts/portable-readme.txt"
	readmeName = "使用说明.txt"
	exeName    = "cabinet.exe"

	// imageSubsystemWindowsGUI PE 可选头的 GUI 子系统值；交付构建必须是它，
	// 裸 go build 产物是 CUI=3，双击会弹黑框（2026-10-01 教训）
	imageSubsystemWindowsGUI = 2

	// GPL-3.0 分发合规（2026-10-17 #7）：包内捆绑 pikafish.exe + pikafish.nnue
	// 即构成 GPL-3.0 语义的"交付"——必须随包提供许可文本与源码获取途径。
	// 三份文件随引擎落 appdata\library\engine\licenses\；三方组件总清单落包根。
	pikafishLicenseDir = "Pikafish.2026-09-06"
	noticesSrc         = "THIRD-PARTY-NOTICES.md"
	noticesName        = "THIRD-PARTY-NOTICES.txt"
)

// pikafishLicenseDocs 随包交付的 GPL-3.0 义务文件（许可全文/NNUE 附加条款/
// 作者清单）——切片不能进 const 块，独立 var。
var pikafishLicenseDocs = []string{"Copying.txt", "NNUE-License.md", "AUTHORS"}

func main() {
	lite := flag.Bool("lite", false, "Full 包不含桔库（开局库功能退化为纯引擎搜索）")
	withEngine := flag.Bool("with-engine", false, "Update 包附带 appdata/library/engine+nnue")
	withBooks := flag.Bool("with-books", false, "Update 包附带 appdata/library/books")
	flag.Parse()

	log.SetFlags(0)
	log.SetOutput(out) // 控制台场景转 GBK 输出（见 out 注释），避免 936 控制台中文乱码
	if err := run(*lite, *withEngine, *withBooks); err != nil {
		log.Printf("[错误] %v", err)
		// CLI 收尾提示的 stdout 写失败无恢复语义，显式忽略
		_, _ = fmt.Fprintln(out)
		_, _ = fmt.Fprintln(out, "打包失败（见上方错误信息），已中止。")
		os.Exit(1)
	}
	_, _ = fmt.Fprintln(out)
	_, _ = fmt.Fprintln(out, "双轨包已生成在 dist\\ 下。")
}

// out 标准输出：cmd 控制台的代码页是 936，Go 直写 UTF-8 会乱码——检测到
// 字符设备（终端）时经 GBK 编码器输出；重定向到管道/文件时保持 UTF-8 原样。
var out io.Writer = func() io.Writer {
	if fi, err := os.Stdout.Stat(); err == nil && fi.Mode()&os.ModeCharDevice != 0 {
		return simplifiedchinese.GBK.NewEncoder().Writer(os.Stdout)
	}
	return os.Stdout
}()

func run(lite, withEngine, withBooks bool) error {
	stamp := time.Now().Format("20060102")

	// ── 源文件存在性预检（缺件早失败，免得 staging 组装一半才崩）──
	for _, p := range []string{engineSrc, nnueSrc, exampleSrc, readmeSrc} {
		if _, err := os.Stat(p); err != nil {
			return fmt.Errorf("缺少源文件 %s: %w", p, err)
		}
	}
	books, err := bookFiles() // *.obk 通配，文件名含中文（桔库），不逐个点名
	if err != nil {
		return err
	}
	if !lite && len(books) == 0 {
		return errors.New("缺少桔库文件 library/books/*.obk（若确要无库打包请加 -lite）")
	}

	// ── 第 1 步：构建 cabinet.exe（与 scripts/build-exe.bat 同逻辑：CGO+MinGW+windowsgui）──
	log.Printf("[1/4] 构建 %s ...", exeName)
	if err := buildExe(); err != nil {
		return err
	}

	// ── 第 2 步：组装 Full 包 staging ──
	log.Printf("[2/4] 组装 Full 包结构 ...")
	stg := filepath.Join("dist", "staging-full")
	if err := resetDir(stg); err != nil {
		return err
	}
	if err := stageFull(stg, lite, books); err != nil {
		return err
	}

	// ── 第 3 步：组装 Update 包 staging ──
	log.Printf("[3/4] 组装 Update 包结构 ...")
	stu := filepath.Join("dist", "staging-update")
	if err := resetDir(stu); err != nil {
		return err
	}
	if err := stageUpdate(stu, withEngine, withBooks, books); err != nil {
		return err
	}

	// ── 第 4 步：压缩产出（staging 根内容即 zip 根：解压直接得 exe + appdata + 说明）──
	log.Printf("[4/4] 压缩产出 ...")
	fullZip := filepath.Join("dist", fmt.Sprintf("Cabinet-Portable-%s-Full.zip", stamp))
	updateZip := filepath.Join("dist", fmt.Sprintf("Cabinet-Update-%s.zip", stamp))
	if err := zipDir(stg, fullZip); err != nil {
		return fmt.Errorf("压缩 full 包失败: %w", err)
	}
	if err := zipDir(stu, updateZip); err != nil {
		return fmt.Errorf("压缩 update 包失败: %w", err)
	}

	// staging 是中间产物，zip 落地后即清理（失败时保留供排查）
	if err := os.RemoveAll(stg); err != nil {
		log.Printf("[警告] 清理 %s 失败: %v", stg, err)
	}
	if err := os.RemoveAll(stu); err != nil {
		log.Printf("[警告] 清理 %s 失败: %v", stu, err)
	}

	fmt.Println()
	fmt.Println("完成。产物清单（dist\\）：")
	for _, z := range []string{fullZip, updateZip} {
		if info, err := os.Stat(z); err == nil {
			fmt.Printf("  %-42s %.1f MB\n", filepath.Base(z), float64(info.Size())/1048576)
		}
	}
	return nil
}

// buildExe 调 go build 产出 windowsgui 子系统的 cabinet.exe，并校验 PE 子系统。
// CGO 与 MinGW 由本函数注入子进程环境（bat 壳不设，保证从任意环境调用都成立）。
func buildExe() error {
	// 正在运行的旧实例会锁住 cabinet.exe，构建前先检查（不代杀进程）
	if f, err := os.OpenFile(exeName, os.O_WRONLY, 0); err == nil {
		_ = f.Close()
	} else if info, statErr := os.Stat(exeName); statErr == nil && !info.IsDir() {
		return fmt.Errorf("%s 正在运行，请先从托盘退出再打包（否则无法覆盖产物）", exeName)
	}

	// 版本注入与 scripts/build-exe.bat 同口径（2026-10-27）：git 可用时写
	// vc<提交数>-<HEAD日期>，非 git 环境留空回退 protocol.go 默认值——
	// 本地打包与 CI 打包的 exe 版本号保证一致可追溯
	ldflags := "-H windowsgui"
	if vc, err := exec.Command("git", "rev-list", "HEAD", "--count").Output(); err == nil {
		if bd, err := exec.Command("git", "log", "-1", "--format=%cd", "--date=format:%Y%m%d").Output(); err == nil {
			if v, d := strings.TrimSpace(string(vc)), strings.TrimSpace(string(bd)); v != "" && d != "" {
				ldflags += fmt.Sprintf(" -X cabinet/internal/server.Version=vc%s-%s", v, d)
			}
		}
	}

	// MinGW 解析顺序：仓内 .tools 副本（本机约定）优先，不在则信任环境 PATH
	// 里的 gcc（CI windows runner 自带 MinGW）——两处都落空才硬失败
	env := append(os.Environ(), "CGO_ENABLED=1")
	mingw, err := filepath.Abs(filepath.Join(".tools", "mingw64-extract", "mingw64", "bin"))
	if err == nil {
		if _, err := os.Stat(filepath.Join(mingw, "gcc.exe")); err == nil {
			// mingw 必须是绝对路径：go build 的 cgo 环节在临时构建目录下 spawn gcc，
			// PATH 里的相对条目按那个 CWD 解析必落空（实测教训）
			env = append(env, "PATH="+mingw+string(os.PathListSeparator)+os.Getenv("PATH"))
		} else if _, err := exec.LookPath("gcc"); err != nil {
			return errors.New("找不到 gcc：本机确认 .tools/mingw64-extract/mingw64 存在，CI 确认 MinGW 在 PATH")
		}
	}
	cmd := exec.Command("go", "build", "-ldflags", ldflags, "-o", exeName, "./cmd/cabinet")
	cmd.Env = env
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("构建失败: %w", err)
	}
	return checkPE(exeName)
}

// checkPE 校验 exe 的子系统为 Windows GUI（=2），不达标即中止。
// 此前经 objdump + findstr 校验，debug/pe 标准库直接读，零外部依赖。
// Subsystem 在可选头里（PE32/PE32+ 两种布局，Go Windows 产物是 PE32+）。
func checkPE(path string) error {
	f, err := pe.Open(path)
	if err != nil {
		return fmt.Errorf("PE 解析失败 %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	var subsystem uint16
	switch oh := f.OptionalHeader.(type) {
	case *pe.OptionalHeader32:
		subsystem = oh.Subsystem
	case *pe.OptionalHeader64:
		subsystem = oh.Subsystem
	default:
		return fmt.Errorf("PE 可选头类型未知: %T", f.OptionalHeader)
	}
	if subsystem != imageSubsystemWindowsGUI {
		return fmt.Errorf("PE 子系统校验未过：got %d want %d (Windows GUI)", subsystem, imageSubsystemWindowsGUI)
	}
	return nil
}

// stageFull 组装首装全量包：cabinet.exe + appdata 骨架 + 使用说明.txt。
// -lite 时跳过桔库（books 目录整个不建，避免出现空的 library\books）。
func stageFull(stg string, lite bool, books []string) error {
	if err := copyFile(exeName, filepath.Join(stg, exeName)); err != nil {
		return err
	}
	if err := copyFile(exampleSrc, filepath.Join(stg, "appdata", "configs", filepath.Base(exampleSrc))); err != nil {
		return err
	}
	if err := copyFile(engineSrc, filepath.Join(stg, "appdata", "library", "engine", engineName)); err != nil {
		return err
	}
	if err := copyFile(nnueSrc, filepath.Join(stg, "appdata", "library", "nnue", filepath.Base(nnueSrc))); err != nil {
		return err
	}
	if err := copyPikafishLicenses(stg); err != nil {
		return err
	}
	if !lite {
		for _, b := range books {
			if err := copyFile(b, filepath.Join(stg, "appdata", "library", "books", filepath.Base(b))); err != nil {
				return err
			}
		}
	}
	if err := copyFile(noticesSrc, filepath.Join(stg, noticesName)); err != nil {
		return err
	}
	return copyFile(readmeSrc, filepath.Join(stg, readmeName))
}

// stageUpdate 组装升级增量包：默认仅 cabinet.exe，按参数附引擎/桔库。
func stageUpdate(stu string, withEngine, withBooks bool, books []string) error {
	if err := copyFile(exeName, filepath.Join(stu, exeName)); err != nil {
		return err
	}
	if withEngine {
		if err := copyFile(engineSrc, filepath.Join(stu, "appdata", "library", "engine", engineName)); err != nil {
			return err
		}
		if err := copyFile(nnueSrc, filepath.Join(stu, "appdata", "library", "nnue", filepath.Base(nnueSrc))); err != nil {
			return err
		}
		if err := copyPikafishLicenses(stu); err != nil {
			return err
		}
	}
	if withBooks {
		for _, b := range books {
			if err := copyFile(b, filepath.Join(stu, "appdata", "library", "books", filepath.Base(b))); err != nil {
				return err
			}
		}
	}
	return nil
}

// copyPikafishLicenses 随引擎复制 GPL-3.0 义务文件（许可全文 + NNUE 附加条款
// + 作者清单）到包内 licenses\ 目录。引擎在则许可必在——分发合规的硬性要求，
// 缺源文件即报错中止打包（宁可打不出包，不可发不合规的包）。
func copyPikafishLicenses(stg string) error {
	for _, f := range pikafishLicenseDocs {
		src := filepath.Join(pikafishLicenseDir, f)
		dst := filepath.Join(stg, "appdata", "library", "engine", "licenses", f)
		if err := copyFile(src, dst); err != nil {
			return fmt.Errorf("皮卡鱼许可文件缺失（GPL 分发义务）: %w", err)
		}
	}
	return nil
}

// bookFiles 收集 library/books 下全部 *.obk（cmd 的 *.obk 通配不匹配 *.obk.zip，
// 此处保持同口径：只带 .obk 结尾的开局库）。
func bookFiles() ([]string, error) {
	entries, err := os.ReadDir(filepath.Join("library", "books"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读取桔库目录失败: %w", err)
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".obk") {
			out = append(out, filepath.Join("library", "books", e.Name()))
		}
	}
	return out, nil
}

// resetDir 删除并重建 staging 目录。
func resetDir(path string) error {
	if err := os.RemoveAll(path); err != nil {
		return fmt.Errorf("清理旧 staging 失败 %s: %w", path, err)
	}
	if err := os.MkdirAll(path, 0o755); err != nil {
		return fmt.Errorf("创建 staging 失败 %s: %w", path, err)
	}
	return nil
}

// copyFile 整文件复制（源只读；引擎改名 pikafish.exe 属复制产出）。
func copyFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("创建目录失败: %w", err)
	}
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("打开源文件失败 %s: %w", src, err)
	}
	defer func() { _ = in.Close() }()
	out, err := os.Create(dst)
	if err != nil {
		return fmt.Errorf("创建目标失败 %s: %w", dst, err)
	}
	defer func() { _ = out.Close() }()
	if _, err := io.Copy(out, in); err != nil {
		return fmt.Errorf("复制 %s → %s 失败: %w", src, dst, err)
	}
	return nil
}

// zipDir 把 staging 目录内容压缩为 zip：目录内相对路径即 zip 内路径，
// 所以 staging 根下的 exe/appdata/使用说明就是 zip 根（解压即用）。
// 中文名条目自动携带 UTF-8 标志（Go archive/zip 对非 ASCII 名默认置位）。
func zipDir(dir, zipPath string) error {
	out, err := os.Create(zipPath)
	if err != nil {
		return err
	}
	defer func() { _ = out.Close() }()
	w := zip.NewWriter(out)
	defer func() { _ = w.Close() }()
	return filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		src, err := os.Open(path)
		if err != nil {
			return err
		}
		defer func() { _ = src.Close() }()
		entry, err := w.Create(rel)
		if err != nil {
			return err
		}
		_, err = io.Copy(entry, src)
		return err
	})
}
