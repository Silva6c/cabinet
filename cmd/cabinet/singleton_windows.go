//go:build windows

// 单实例锁（Windows）：双开时第二个实例会走完全套初始化后因端口占用静默退出
// （windowsgui 子系统无控制台，stderr 不可见、日志也来不及写），用户观感是
// "点开没反应"。用命名互斥体在一切初始化之前判定，命中即 MessageBox 明确提示。
// 锁名按配置文件绝对路径派生（2026-10-01 第二轮审查 P2-2）：不同配置可并行
// 第二实例（测试/联调各自 -config 互不干扰），同配置仍互斥。
package main

import (
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

var (
	modkernel32Singleton = syscall.NewLazyDLL("kernel32.dll")
	procCreateMutexW     = modkernel32Singleton.NewProc("CreateMutexW")
	moduser32            = syscall.NewLazyDLL("user32.dll")
	procMessageBoxW      = moduser32.NewProc("MessageBoxW")
)

const (
	errnoAlreadyExists = 0x000000B7 // ERROR_ALREADY_EXISTS
	mbIconWarning      = 0x30
	mbOkOnly           = 0x0
)

// singletonMutexName 由配置文件绝对路径派生的互斥体名：绝对路径 + 忽略大小写
// 过 fnv32（Windows 路径大小写不敏感），避免路径里的非法字符进内核对象名。
// 同一配置的多实例共享一把锁（互斥），不同配置各一把（可并行）。
func singletonMutexName(configPath string) string {
	abs, err := filepath.Abs(configPath)
	if err != nil {
		abs = configPath
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(strings.ToLower(abs)))
	return fmt.Sprintf("Local\\cabinet-singleton-%08x", h.Sum32())
}

// acquireSingleton 尝试持有单实例互斥体；已有实例在运行时弹窗提示并返回 false
// （调用方应立即退出）。成功持有后句柄故意不释放——随进程存活到退出，锁自动失效。
// 互斥体创建/名字构造失败时放行（极端环境保可用，端口占用仍是最后兜底）。
func acquireSingleton(silent bool, configPath string) bool {
	namePtr, err := syscall.UTF16PtrFromString(singletonMutexName(configPath))
	if err != nil {
		return true
	}
	h, _, callErr := procCreateMutexW.Call(0, 0, uintptr(unsafe.Pointer(namePtr)))
	if h == 0 {
		return true
	}
	if errno, ok := callErr.(syscall.Errno); !ok || errno != errnoAlreadyExists {
		return true // 全新互斥体：本实例持有锁，继续启动
	}
	// 更新重启接力：旧实例正在优雅退出（正常远快于 6s 服务 + 8s 引擎上限），
	// 先短重试再放弃，避免新旧进程交接竞态弹"已在运行"误窗。仅在更新拉起的
	// 进程内启用（CABINET_UPDATE_RELAUNCH=1）；正常双开行为完全不变。
	if os.Getenv("CABINET_UPDATE_RELAUNCH") == "1" {
		for i := 0; i < 10; i++ {
			time.Sleep(500 * time.Millisecond)
			h2, _, callErr2 := procCreateMutexW.Call(0, 0, uintptr(unsafe.Pointer(namePtr)))
			if h2 == 0 {
				return true
			}
			if errno2, ok := callErr2.(syscall.Errno); !ok || errno2 != errnoAlreadyExists {
				return true // 旧实例已退出，本实例接力持有锁
			}
			// 仍被占用：指向已存在互斥体的句柄泄漏到进程退出为止（即将退出，无碍）
		}
		// 重试耗尽仍被占用：旧实例未退干净，落入下方正常双开提示
	}
	// 已有实例：无窗口模式（-no-tray/服务）不弹窗（避免卡住无人值守脚本），
	// 但 stderr 可用时给出提示——此前静默 exit 0，联调者会以为启动成功
	if silent {
		fmt.Fprintln(os.Stderr, "cabinet 已有实例在运行（同一配置文件），本实例退出")
		return false
	}
	msg, _ := syscall.UTF16PtrFromString("内阁 cabinet 已在运行。\n\n请查看任务栏托盘图标（可能在溢出区 ^），右键可打开面板或退出。")
	caption, _ := syscall.UTF16PtrFromString("内阁 cabinet")
	// 提示框调用无错误恢复语义（用户已可见弹窗或流程注定退出），显式忽略返回
	_, _, _ = procMessageBoxW.Call(0, uintptr(unsafe.Pointer(msg)),
		uintptr(unsafe.Pointer(caption)), mbIconWarning|mbOkOnly)
	return false
}
