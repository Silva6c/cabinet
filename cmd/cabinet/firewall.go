package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"cabinet/internal/firewall"
)

// firewallWatch 防火墙告警状态：启动时异步检测一次放行规则是否缺失，
// 一键放行后延时复查。"不再提示"按端口记录在状态文件（console-state.json，
// 不写 toml 主配置——ResolvePaths 之后再 cfg.Save 会固化绝对路径破坏便携性，
// 见 main.go 的 ResolvePaths 铁律）。
//
// 检测只做一次 + 放行后复查，不做周期轮询：netsh 是外部进程（几十到几百毫秒），
// 控制台 1.5s 刷新轮询会持续拉起子进程；规则被安全软件删除属于罕见事件，
// 下次启动重新检测即可覆盖。
type firewallWatch struct {
	mu        sync.Mutex
	port      int    // 监听端口（0 = 不适用告警，如回环 bind）
	ruleName  string // 放行规则名（空 = 不适用）
	missing   bool   // 规则缺失（待放行）
	available bool   // 检测已完成（未完成时控制台不显示告警也不显示正常态）
}

// consoleStateFile 状态文件的内存态与落盘格式；新增键向后再加字段，
// 旧文件缺字段零值即默认，天然向前兼容。
type consoleStateFile struct {
	// DismissedFirewallPorts 用户点过"不再提示"的端口集合（JSON 对象键，
	// 读取时逐个转 int，非法键忽略）
	DismissedFirewallPorts map[string]bool `json:"firewall_dismissed_ports"`
}

// startFirewallWatch 异步检测放行规则；applicable=false（回环 bind/解析失败）
// 时状态保持不适用，FirewallStatus 恒返回空。检测在 goroutine 里做：netsh
// 首查可能耗时数百毫秒，不拖慢启动。
func startFirewallWatch(bind string, statePath string, logger *slog.Logger) *firewallWatch {
	w := &firewallWatch{}
	port, rule, applicable := firewall.Plan(bind)
	if !applicable {
		return w
	}
	w.port, w.ruleName = port, rule
	go func() {
		exists, err := firewall.RuleExists(rule)
		w.mu.Lock()
		defer w.mu.Unlock()
		if err != nil {
			// 检测失败（netsh 不在/策略受限）按"不告警"处理，不阻塞用户；
			// 手机连不上时手机端文案仍指向防火墙
			logger.Warn("防火墙规则检测失败，跳过告警", "err", err)
			return
		}
		w.available = true
		w.missing = !exists
		if w.missing {
			logger.Warn("防火墙未放行监听端口（首次弹窗点过取消会静默拦截），控制台可一键放行",
				"port", port, "rule", rule)
		}
	}()
	return w
}

// Status 返回控制台横幅文案：空 = 不告警（规则在/不适用/已忽略/未完成检测）。
// "不再提示"读状态文件（低频操作，1.5s 刷新周期可承受；文件缺失视为未忽略）。
func (w *firewallWatch) Status(statePath string) string {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.available || !w.missing {
		return ""
	}
	dismissed, err := loadDismissedFirewallPorts(statePath)
	if err == nil && dismissed[w.port] {
		return ""
	}
	return fmt.Sprintf("Windows 防火墙未放行 TCP %d，手机将无法连接", w.port)
}

// Allow 一键放行：UAC 提权执行 netsh 加规则，2s 后复查规则是否落地并更新
// 告警态（UAC 是异步弹窗，无法直接拿到用户点"是/否"的结果）。返回给控制台
// 的反馈文案（空 = 已受理）。
func (w *firewallWatch) Allow(logger *slog.Logger) string {
	w.mu.Lock()
	port, rule := w.port, w.ruleName
	w.mu.Unlock()
	if rule == "" {
		return "防火墙检测不可用"
	}
	if err := firewall.Allow(rule, port); err != nil {
		logger.Warn("一键放行失败", "err", err)
		return err.Error()
	}
	logger.Info("一键放行已发起（UAC）", "port", port, "rule", rule)
	go func() {
		time.Sleep(2 * time.Second) // 等 UAC 确认 + netsh 执行
		exists, err := firewall.RuleExists(rule)
		w.mu.Lock()
		defer w.mu.Unlock()
		if err != nil {
			logger.Warn("放行后复查失败", "err", err)
			return
		}
		w.missing = !exists
		if !exists {
			logger.Info("放行后复查：规则仍未出现（UAC 可能被拒绝），告警保持")
		} else {
			logger.Info("放行成功，规则已生效", "rule", rule)
		}
	}()
	return ""
}

// DismissFirewall 把当前端口写入"不再提示"集合并落盘状态文件。
func (w *firewallWatch) DismissFirewall(statePath string, logger *slog.Logger) {
	w.mu.Lock()
	port := w.port
	w.mu.Unlock()
	if port == 0 {
		return
	}
	if err := saveDismissedFirewallPort(statePath, port); err != nil {
		logger.Warn("防火墙提示状态写入失败", "err", err)
		return
	}
	logger.Info("防火墙告警已按用户要求静默", "port", port)
}

// loadDismissedFirewallPorts 读状态文件的"不再提示"端口集合；文件不存在/
// 损坏返回空集合（不视为错误——状态文件只是提示去重，丢失仅导致再提示一次）。
func loadDismissedFirewallPorts(statePath string) (map[int]bool, error) {
	data, err := os.ReadFile(statePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var st consoleStateFile
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, err
	}
	out := make(map[int]bool, len(st.DismissedFirewallPorts))
	for k := range st.DismissedFirewallPorts {
		var p int
		if _, err := fmt.Sscanf(k, "%d", &p); err == nil {
			out[p] = true
		}
	}
	return out, nil
}

// saveDismissedFirewallPort 增量写一个端口进"不再提示"集合（读-改-写；
// 单实例锁保证无并发写者）。目录不存在时创建。
func saveDismissedFirewallPort(statePath string, port int) error {
	st := consoleStateFile{DismissedFirewallPorts: map[string]bool{}}
	if data, err := os.ReadFile(statePath); err == nil {
		_ = json.Unmarshal(data, &st)
		if st.DismissedFirewallPorts == nil {
			st.DismissedFirewallPorts = map[string]bool{}
		}
	}
	st.DismissedFirewallPorts[fmt.Sprint(port)] = true
	if err := os.MkdirAll(filepath.Dir(statePath), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(&st, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(statePath, data, 0o644)
}
