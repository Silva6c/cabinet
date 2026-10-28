package server

import (
	"cabinet/internal/engine"
	"errors"
	"fmt"
)

func (s *Server) handleSetEngine(c *Conn, m *SetEngine) {
	type kv struct {
		kind string
		v    int
	}
	var fields []kv
	if m.Depth != nil {
		fields = append(fields, kv{"depth", *m.Depth})
	}
	if m.HashMB != nil {
		fields = append(fields, kv{"hash", *m.HashMB})
	}
	if m.Threads != nil {
		fields = append(fields, kv{"threads", *m.Threads})
	}
	if m.MultiPV != nil {
		fields = append(fields, kv{"multipv", *m.MultiPV})
	}
	if len(fields) == 0 {
		_ = c.Send(ErrorFrame{frameBase: frame(TypeError), Code: CodeBadFrame,
			Message: "set_engine 未包含任何字段（depth / hash_mb / threads / multi_pv）"})
		return
	}
	if s.eng == nil {
		_ = c.Send(ErrorFrame{frameBase: frame(TypeError), Code: CodeNotReady,
			Message: "当前服务未启用引擎，无法调节算力"})
		return
	}
	if _, ok := s.eng.(EngineTuner); !ok {
		_ = c.Send(ErrorFrame{frameBase: frame(TypeError), Code: CodeNotReady,
			Message: "当前引擎桥不支持远程调节"})
		return
	}
	// 先整体校验（任一越界整帧拒绝，不部分应用）再逐字段应用
	for _, f := range fields {
		if msg := s.validateTune(f.kind, f.v); msg != "" {
			_ = c.Send(ErrorFrame{frameBase: frame(TypeError), Code: CodeBadFrame, Message: msg})
			return
		}
	}
	for _, f := range fields {
		s.applyTune(f.kind, f.v)
		// 落盘与 TuneEngine（面板链路）同权：此前手机链路只 applyTune 不触发
		// 回调，悬浮窗调的参数重启即被 toml 旧值打回——用户用手机救回的
		// 算力选择（如 hash 调回 10240）没被记住，棋力"反复变弱"的直接推手
		if s.onTunePersist != nil {
			s.onTunePersist(f.kind, f.v)
		}
	}
	c.log.Info("远程调节引擎算力", "fields", len(fields),
		"req_depth", m.Depth, "req_hash_mb", m.HashMB,
		"req_threads", m.Threads, "req_multi_pv", m.MultiPV)
	s.reSearchLatest()
}

// TuneEngine 桌面面板调节引擎算力：与协议 §4.10 完全同一条路（校验→应用→
// 以最近局面重搜），参数改动即时生效且手机端感知一致。
// kind 取 "depth" / "hash" / "threads" / "multipv"；返回错误消息（空串 = 成功）。
func (s *Server) TuneEngine(kind string, v int) error {
	if msg := s.validateTune(kind, v); msg != "" {
		return errors.New(msg)
	}
	s.applyTune(kind, v)
	s.reSearchLatest()
	// 落盘记住用户的选择（2026-10-18）：面板链路在此回调；手机悬浮窗链路
	// （handleSetEngine）在逐字段应用处触发同一回调——重启后以落盘值为准
	// （否则每次启动回配置默认值，用户切一次丢一次）
	if s.onTunePersist != nil {
		s.onTunePersist(kind, v)
	}
	return nil
}

// ClearHash 桌面面板"清空置换表"：置位后于下一轮搜索开始前执行。
func (s *Server) ClearHash() error {
	tuner, ok := s.eng.(EngineTuner)
	if !ok {
		return errors.New("当前引擎桥不支持该操作")
	}
	tuner.ClearHash()
	return nil
}

// validateTune 单字段范围校验（协议 §4.10；threads/hash 的 0 是自动档合法值）。
// 返回错误消息，空串 = 合法。
func (s *Server) validateTune(kind string, v int) string {
	switch kind {
	case "depth":
		if v < 1 || v > 40 {
			return fmt.Sprintf("depth=%d 超出范围（1–40）", v)
		}
	case "hash":
		if v != 0 && (v < 32 || v > 32768) {
			return fmt.Sprintf("hash_mb=%d 超出范围（0=自动，或 32–32768）", v)
		}
	case "threads":
		if v != 0 && (v < 1 || v > 64) {
			return fmt.Sprintf("threads=%d 超出范围（0=自动，或 1–64）", v)
		}
	case "multipv":
		if v < 1 || v > 5 {
			return fmt.Sprintf("multi_pv=%d 超出范围（1–5）", v)
		}
	default:
		return "未知的调节字段 " + kind
	}
	return ""
}

// applyTune 单字段应用（engine 侧 setter 返回值仅兜底防御）。
func (s *Server) applyTune(kind string, v int) {
	tuner, ok := s.eng.(EngineTuner)
	if !ok {
		return
	}
	switch kind {
	case "depth":
		tuner.SetDepth(v)
	case "hash":
		tuner.SetHashMB(v)
	case "threads":
		tuner.SetThreads(v)
	case "multipv":
		tuner.SetMultiPV(v)
	}
}

// reSearchLatest 引擎就绪且有最近局面时，以最近局面重新请求（新算力立即体现：
// 参数在下一轮搜索开始前应用）。重放前再过一次引擎安全校验：set_engine 调参
// 是"坏局面重放再崩"的入口（2026-10-01 第二轮审查实测：坏局面进了 lastFen
// 后每次调参必崩）——消费侧拦截让调参永不成为崩溃放大器。
func (s *Server) reSearchLatest() {
	want, _ := s.lastFen.Load().(string)
	if want == "" || s.EngineState() != engine.StateReady {
		return
	}
	if !engine.PositionLegal(want) {
		s.log.Warn("最近局面引擎不安全，跳过重放（等新局面覆盖）", "fen", want)
		return
	}
	s.eng.Request(want)
}

// recordSuggestion 沉淀回调（nil 安全）。
