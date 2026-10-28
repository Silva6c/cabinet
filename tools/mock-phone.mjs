#!/usr/bin/env node
/**
 * mock-phone.mjs 模拟手机端瘦客户端，按协议 v1 走完整链路（与 Go 版 tools/mockphone 等价）：
 * 收 PC hello → 发 hello → 发 auth → 收 welcome → 发 position → 收 status
 * → 发 ping → 收 pong（算 RTT）→ 发 bye → 收 bye。
 *
 * 依赖：Node ≥ 22 的内置全局 WebSocket，无第三方包。
 *
 * 自签证书的校验关闭必须通过环境变量注入（不要写死进任何脚本）：
 *   Git Bash:    NODE_TLS_REJECT_UNAUTHORIZED=0 node tools/mock-phone.mjs [url] [token]
 *   PowerShell:  $env:NODE_TLS_REJECT_UNAUTHORIZED="0"; node tools/mock-phone.mjs [url] [token]
 *
 * 注意：若本脚本配合环境变量仍报证书校验失败（部分 Node 版本的 undici WebSocket
 * 不吃 NODE_TLS_REJECT_UNAUTHORIZED），请改用 Go 版：go run ./tools/mockphone
 */

const url = process.argv[2] ?? "wss://127.0.0.1:9527/ws";
const token = process.argv[3];

if (!token) {
  console.error("用法: node tools/mock-phone.mjs [url] [token]");
  console.error("token 取 cabinet 配置文件的 token 字段（cabinet -gen-token 可生成）");
  process.exit(1);
}

const V1 = 1;
const log = (...a) => console.log(new Date().toISOString(), ...a);

function frame(t, extra = {}) {
  return JSON.stringify({ v: V1, t, ...extra });
}

/** 限时等待指定类型帧；ts 模式下返回原始对象供回传校验 */
function expect(ws, type, timeoutMs = 5000) {
  return new Promise((resolve, reject) => {
    const timer = setTimeout(() => {
      ws.removeEventListener("message", onMsg);
      ws.removeEventListener("close", onClose);
      reject(new Error(`等待 ${type} 超时`));
    }, timeoutMs);
    const cleanup = () => {
      clearTimeout(timer);
      ws.removeEventListener("message", onMsg);
      ws.removeEventListener("close", onClose);
    };
    function onMsg(ev) {
      let msg;
      try {
        msg = JSON.parse(ev.data);
      } catch {
        cleanup();
        reject(new Error(`非 JSON 帧: ${ev.data}`));
        return;
      }
      log("[recv]", ev.data);
      if (msg.t !== type) {
        if (msg.t === "error") {
          cleanup();
          reject(new Error(`收到 error 帧: ${msg.code} ${msg.message}`));
        }
        return; // 其他帧继续等（未知类型按协议忽略）
      }
      cleanup();
      resolve(msg);
    }
    function onClose() {
      cleanup();
      reject(new Error(`等待 ${type} 期间连接被关闭`));
    }
    ws.addEventListener("message", onMsg);
    ws.addEventListener("close", onClose);
  });
}

const ws = new WebSocket(url);

ws.addEventListener("open", async () => {
  try {
    log("已连接", url);

    // 1. 协议钉死：连接建立后 PC 先发 hello
    const hello = await expect(ws, "hello");
    if (!hello.server) throw new Error("PC 的 hello 帧缺少 server 字段");

    // 2. 客户端 hello → 3. auth
    const clientHello = frame("hello", { client: "cabinet-phone", ver: "0.1.0", caps: ["v1"] });
    log("[send]", clientHello);
    ws.send(clientHello);
    const auth = frame("auth", { token });
    log("[send]", auth);
    ws.send(auth);

    // 4. welcome
    const welcome = await expect(ws, "welcome");
    log(`会话建立 session_id=${welcome.session_id} engine=${welcome.engine?.state} books=`, welcome.books);

    // 5. position → status（M0 echo 基准）
    const fen = "rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR w - - 0 1";
    const pos = frame("position", { fen, red_go: true, history: [] });
    log("[send]", pos);
    ws.send(pos);
    await expect(ws, "status");

    // 6. ping → pong（ts 原样回传），算 RTT
    const ts = Date.now();
    const ping = frame("ping", { ts });
    log("[send]", ping);
    ws.send(ping);
    const pong = await expect(ws, "pong");
    if (pong.ts !== ts) throw new Error(`pong.ts 应原样回传: got ${pong.ts}`);
    log(`心跳 RTT = ${Date.now() - pong.ts} ms`);

    // 7. bye → bye
    log("[send]", frame("bye"));
    ws.send(frame("bye"));
    await expect(ws, "bye");

    log("链路全通：hello → auth → welcome → position → status → ping → pong → bye");
    ws.close();
    process.exit(0);
  } catch (err) {
    console.error("联调失败:", err.message);
    process.exit(1);
  }
});

ws.addEventListener("error", (err) => {
  console.error("连接错误:", err.message ?? err);
  console.error("提示：自签证书需以环境变量方式关闭校验，见脚本头部说明；或改用 Go 版 tools/mockphone");
  process.exit(1);
});
