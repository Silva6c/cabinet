// 会话保活 mock：连上后只回 pong 维持连接，不发 position（供控制台"断开会话"UI 实测）。
// 自签证书走 NODE_TLS_REJECT_UNAUTHORIZED=0（同 mock-phone.mjs）。
// 用法: NODE_TLS_REJECT_UNAUTHORIZED=0 node tools/mock-idle.mjs <wss url> <token>
const url = process.argv[2];
const token = process.argv[3];
if (!url || !token) {
  console.error("用法: node tools/mock-idle.mjs <wss url> <token>");
  process.exit(1);
}
const ws = new WebSocket(url);
ws.addEventListener("open", () => console.log("已连接，保持空闲（只回 pong）"));
ws.addEventListener("message", (ev) => {
  const m = JSON.parse(ev.data.toString());
  switch (m.t) {
    case "hello":
      ws.send(JSON.stringify({ v: 1, t: "hello", client: "cabinet-phone", ver: "0.1.0", caps: ["v1"] }));
      ws.send(JSON.stringify({ v: 1, t: "auth", token }));
      break;
    case "ping":
      ws.send(JSON.stringify({ v: 1, t: "pong", ts: m.ts }));
      break;
    case "bye":
      console.log("=== 收到 PC 的 bye 帧（主动断开通知） ===");
      break;
    default:
      console.log("[recv]", m.t, m.session_id ?? "");
  }
});
ws.addEventListener("close", (ev) => {
  console.log(`=== 连接已关闭 code=${ev.code} reason=${ev.reason || "(空)"} ===`);
  process.exit(0);
});
ws.addEventListener("error", () => {
  console.log("连接错误");
  process.exit(1);
});
// 挂住进程直到被断开
setInterval(() => {}, 1 << 30);
