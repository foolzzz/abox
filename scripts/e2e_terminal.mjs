import WebSocket from "ws";

const base = process.env.AGENTBOX_WS_URL ?? "ws://127.0.0.1:8080";
const boxId = process.argv[2];
if (!boxId) throw new Error("usage: node scripts/e2e_terminal.mjs <box-id>");

const socket = new WebSocket(`${base}/api/v1/boxes/${encodeURIComponent(boxId)}/terminal`);
let output = "";
const timeout = setTimeout(() => {
  console.error(output);
  socket.terminate();
  process.exit(1);
}, 30_000);

socket.on("open", () => {
  setTimeout(() => socket.send(JSON.stringify({ type: "input", data: "printf '\\x54\\x45\\x52\\x4d\\x49\\x4e\\x41\\x4c\\x5f\\x4f\\x4b\\n'\n" })), 1_000);
});
socket.on("message", (raw) => {
  const message = JSON.parse(raw.toString());
  if (message.data) output += Buffer.from(message.data, "base64").toString("utf8");
  if (message.error) {
    clearTimeout(timeout);
    console.error(message.error);
    process.exit(1);
  }
  if (output.includes("TERMINAL_OK")) {
    clearTimeout(timeout);
    socket.send(JSON.stringify({ type: "close" }));
    console.log(JSON.stringify({ boxId, output }));
    socket.close();
  }
});
socket.on("unexpected-response", (_request, response) => {
  clearTimeout(timeout);
  let body = "";
  response.on("data", (chunk) => { body += chunk.toString(); });
  response.on("end", () => {
    console.error(`unexpected websocket response: ${response.statusCode} ${body}`);
    process.exit(1);
  });
  response.resume();
});
socket.on("error", (error) => {
  clearTimeout(timeout);
  console.error(error);
  process.exit(1);
});
