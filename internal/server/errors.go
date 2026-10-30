package server

import (
	"errors"
	"fmt"
)

// errorFrameFor 将解码错误映射为协议错误帧（协议 §5 错误码）。
func errorFrameFor(err error) ErrorFrame {
	switch {
	case errors.Is(err, ErrBadJSON):
		return ErrorFrame{frameBase: frame(TypeError), Code: CodeBadJSON, Message: "帧不是合法 JSON"}
	case errors.Is(err, ErrBadFrame):
		return ErrorFrame{frameBase: frame(TypeError), Code: CodeBadFrame, Message: "帧结构不合格（缺少 t 字段）"}
	case errors.Is(err, ErrUnsupportedVer):
		return ErrorFrame{frameBase: frame(TypeError), Code: CodeUnsupportedVer, Message: "协议版本不受支持，请升级客户端"}
	default:
		return ErrorFrame{frameBase: frame(TypeError), Code: CodeInternal, Message: "服务器内部错误"}
	}
}

// frameTypeName 返回消息结构的协议类型名（仅用于日志）。
func frameTypeName(msg any) string {
	switch m := msg.(type) {
	case *Hello:
		return TypeHello
	case *Auth:
		return TypeAuth
	case *Welcome:
		return TypeWelcome
	case *Position:
		return TypePosition
	case *Suggestion:
		return TypeSuggestion
	case *Status:
		return TypeStatus
	case *Ping:
		return TypePing
	case *Pong:
		return TypePong
	case *ErrorFrame:
		return TypeError
	case *Bye:
		return TypeBye
	case *Log:
		return TypeLog
	default:
		return fmt.Sprintf("%T", m)
	}
}

// truncateForLog 日志用帧截断，避免超长帧刷屏。
func truncateForLog(data []byte) string {
	const max = 200
	if len(data) <= max {
		return string(data)
	}
	return string(data[:max]) + "…"
}
