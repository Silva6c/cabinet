// mockphone 模拟手机端瘦客户端，按协议 v1 走完整链路，用于 M0 服务器联调：
// 收 PC hello → 发 hello → 发 auth → 收 welcome → 发 position → 收 status
// → 发 ping → 收 pong（算 RTT）→ 发 bye → 收 bye。
//
// 用法：
//
//	go run ./tools/mockphone -url wss://127.0.0.1:9527/ws -token <令牌>
package main

import (
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

const protoV1 = 1

// 帧结构镜像 internal/server 的定义（工具独立于 internal 包，按协议文档手写，字段逐字一致）
type frameBase struct {
	V int    `json:"v"`
	T string `json:"t"`
}

type Hello struct {
	frameBase
	Client string   `json:"client,omitempty"`
	Server string   `json:"server,omitempty"`
	Ver    string   `json:"ver"`
	Caps   []string `json:"caps"`
}

type Auth struct {
	frameBase
	Token string `json:"token"`
}

type Welcome struct {
	frameBase
	SessionID string `json:"session_id"`
	Engine    struct {
		State string `json:"state"`
	} `json:"engine"`
	Books []string `json:"books"`
}

type Position struct {
	frameBase
	Fen     string   `json:"fen"`
	RedGo   bool     `json:"red_go"`
	History []string `json:"history,omitempty"`
}

type Status struct {
	frameBase
	Engine struct {
		State    string `json:"state"`
		Depth    int    `json:"depth,omitempty"`
		Nodes    int64  `json:"nodes,omitempty"`
		NPS      int64  `json:"nps,omitempty"`
		Hashfull int    `json:"hashfull,omitempty"`
	} `json:"engine"`
	RTTMs *int64 `json:"rtt_ms,omitempty"`
}

type Suggestion struct {
	frameBase
	Source   string `json:"source"`
	BestMove string `json:"bestmove"`
	Score    *struct {
		CP   *int `json:"cp,omitempty"`
		Mate *int `json:"mate,omitempty"`
	} `json:"score,omitempty"`
	PV    []string `json:"pv,omitempty"`
	Depth int      `json:"depth,omitempty"`
}

type ErrorFrame struct {
	frameBase
	Code    string `json:"code"`
	Message string `json:"message"`
}

type Ping struct {
	frameBase
	Ts int64 `json:"ts"`
}

type Pong struct {
	frameBase
	Ts int64 `json:"ts"`
}

type Bye struct {
	frameBase
}

// readFrame 限时读一帧原始 JSON。
func readFrame(ws *websocket.Conn) ([]byte, error) {
	_ = ws.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, data, err := ws.ReadMessage()
	return data, err
}

// send 序列化并发送一帧，同时打印发送日志；序列化/发送任一失败直接 Fatal
// （联调工具错即停，不做重试）。
func send(ws *websocket.Conn, msg any) {
	b, err := json.Marshal(msg)
	if err != nil {
		log.Fatalf("序列化失败: %v", err)
	}
	if err := ws.WriteMessage(websocket.TextMessage, b); err != nil {
		log.Fatalf("发送失败: %v", err)
	}
	log.Printf("[send] %s", b)
}

func main() {
	url := flag.String("url", "wss://127.0.0.1:9527/ws", "内阁 WSS 地址")
	token := flag.String("token", "", "配对令牌（配置文件 token 字段）")
	fen := flag.String("fen", "rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR w - - 0 1", "测试局面 FEN")
	window := flag.Duration("window", 3*time.Second, "position 后收帧窗口（搜索收束观测）")
	hold := flag.Duration("hold", 0, "收束后到 bye 前保持连接的时长（控制台截图/多连接观测用）")
	flag.Parse()
	if *token == "" {
		log.Fatal("缺少 -token（取 cabinet 配置文件的 token 字段）")
	}

	// 自签证书：联调工具明确跳过校验（真机端 M2 起改为证书指纹校验）
	d := websocket.Dialer{
		TLSClientConfig:  &tls.Config{InsecureSkipVerify: true},
		HandshakeTimeout: 5 * time.Second,
	}
	ws, resp, err := d.Dial(*url, nil)
	if err != nil {
		log.Fatalf("连接失败: %v (http=%v)", err, resp)
	}
	defer func() { _ = ws.Close() }()
	ws.SetReadLimit(64 << 10)
	log.Printf("已连接 %s", *url)

	// 1. 协议钉死：连接建立后 PC 先发 hello
	data, err := readFrame(ws)
	if err != nil {
		log.Fatalf("读服务器 hello 失败: %v", err)
	}
	var sh Hello
	if err := json.Unmarshal(data, &sh); err != nil || sh.T != "hello" || sh.Server == "" {
		log.Fatalf("期望 PC 的 hello 帧, got: %s", data)
	}
	log.Printf("[recv] %s", data)

	// 2. 客户端 hello → 3. auth
	send(ws, Hello{frameBase: frameBase{protoV1, "hello"}, Client: "cabinet-phone", Ver: "0.1.0", Caps: []string{"v1"}})
	send(ws, Auth{frameBase: frameBase{protoV1, "auth"}, Token: *token})

	// 4. welcome
	data, err = readFrame(ws)
	if err != nil {
		log.Fatalf("读 welcome 失败: %v", err)
	}
	var wl Welcome
	if err := json.Unmarshal(data, &wl); err != nil || wl.T != "welcome" {
		log.Fatalf("期望 welcome 帧, got: %s", data)
	}
	log.Printf("[recv] %s", data)
	log.Printf("会话建立 session_id=%s engine=%s books=%v", wl.SessionID, wl.Engine.State, wl.Books)

	// 5. position → 收帧窗口：搜索期会推送多条 status/suggestion（协议 §4.5/§4.6），
	// 滚动记录最新一条；收到 bestmove 后的指标复位 status（depth=0）即搜索收束。
	// 注意：不能靠"读超时"结束窗口——gorilla 连接读超时后即不可恢复，
	// 会破坏后续 ping/pong；收束信号提前 break 时 deadline 停在未来，连接无恙。
	send(ws, Position{frameBase: frameBase{protoV1, "position"}, Fen: *fen, RedGo: true, History: []string{}})
	windowEnd := time.Now().Add(*window)
	var best *Suggestion
	var lastStatus string
	settled := false
	for {
		remain := time.Until(windowEnd)
		if remain <= 0 {
			break
		}
		_ = ws.SetReadDeadline(time.Now().Add(remain))
		_, data, err = ws.ReadMessage()
		if err != nil {
			log.Printf("[warn] 窗口读中断: %v（后续 ping 可能失败）", err)
			break
		}
		var head struct {
			T string `json:"t"`
		}
		if err := json.Unmarshal(data, &head); err != nil {
			log.Fatalf("坏帧: %s", data)
		}
		switch head.T {
		case "status":
			var st Status
			_ = json.Unmarshal(data, &st)
			lastStatus = fmt.Sprintf("state=%s depth=%d nodes=%d nps=%d hashfull=%d",
				st.Engine.State, st.Engine.Depth, st.Engine.Nodes, st.Engine.NPS, st.Engine.Hashfull)
			log.Printf("[recv] status %s", lastStatus)
			// 收束信号：bestmove 后服务器广播指标复位 status（depth=0 & nodes=0）
			if st.Engine.Depth == 0 && st.Engine.Nodes == 0 && best != nil {
				settled = true
				windowEnd = time.Now() // 正常收束，提前结束窗口（deadline 停在未来，连接无恙）
			}
		case "suggestion":
			var sug Suggestion
			if err := json.Unmarshal(data, &sug); err != nil {
				log.Fatalf("坏 suggestion 帧: %s", data)
			}
			score := "-"
			if sug.Score != nil {
				switch {
				case sug.Score.CP != nil:
					score = fmt.Sprintf("cp %d（红方视角）", *sug.Score.CP)
				case sug.Score.Mate != nil:
					score = fmt.Sprintf("mate %d", *sug.Score.Mate)
				}
			}
			log.Printf("[recv] suggestion source=%s bestmove=%s score=%s pv=%v depth=%d",
				sug.Source, sug.BestMove, score, sug.PV, sug.Depth)
			if sug.Source == "engine" {
				best = &sug // 滚动更新，窗口结束时即终值
			}
			if strings.HasPrefix(sug.Source, "book:") {
				// 桔库命中：确定性建议即刻终态，无搜索期也无指标复位帧
				best = &sug
				settled = true
				windowEnd = time.Now()
			}
		case "error":
			var ef ErrorFrame
			_ = json.Unmarshal(data, &ef)
			log.Printf("[recv] %s", data) // 原样打印（观察 turn_flip_legal 等扩展字段）
			log.Fatalf("收到 error 帧: %s %s", ef.Code, ef.Message)
		default:
			log.Printf("[recv] %s", data)
		}
	}
	if best == nil {
		log.Fatal("窗口内未收到 suggestion")
	}
	if !settled {
		log.Printf("[warn] 未收到收束信号（窗口耗尽），以最后一条 suggestion 为终值")
	}
	_ = ws.SetReadDeadline(time.Time{})
	log.Printf("终值 suggestion: bestmove=%s depth=%d（引擎指标: %s）", best.BestMove, best.Depth, lastStatus)

	// 6. ping → pong，算 RTT（穿插的 status 跳过）
	ts := time.Now().UnixMilli()
	send(ws, Ping{frameBase: frameBase{protoV1, "ping"}, Ts: ts})
	var pg Pong
	for {
		data, err = readFrame(ws)
		if err != nil {
			log.Fatalf("读 pong 失败: %v", err)
		}
		if err := json.Unmarshal(data, &pg); err == nil && pg.T == "pong" {
			break
		}
		log.Printf("[recv] %s（非 pong，跳过）", data)
	}
	log.Printf("[recv] %s", data)
	log.Printf("心跳 RTT = %d ms", time.Now().UnixMilli()-pg.Ts)

	// 保持连接（可选）：控制台截图/多连接观测时让"连接区"有活数据可拍
	if *hold > 0 {
		log.Printf("保持连接 %s ...", *hold)
		time.Sleep(*hold)
	}

	// 7. bye → bye（穿插帧跳过）
	send(ws, Bye{frameBase: frameBase{protoV1, "bye"}})
	var by Bye
	for {
		data, err = readFrame(ws)
		if err != nil {
			log.Fatalf("读 bye 失败: %v", err)
		}
		if err := json.Unmarshal(data, &by); err == nil && by.T == "bye" {
			break
		}
		log.Printf("[recv] %s（非 bye，跳过）", data)
	}
	log.Printf("[recv] %s", data)
	log.Printf("链路全通：hello → auth → welcome → position → status/suggestion（真实引擎）→ ping → pong → bye")
}
