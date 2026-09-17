import WebSocket from "ws";

const base = process.env.AGENTBOX_WS_URL ?? "ws://127.0.0.1:8080";
const boxId = process.argv[2];
if (!boxId) throw new Error("usage: node scripts/e2e_agent_terminal.mjs <box-id>");

const decode = (value) => Buffer.from(value, "base64").toString("utf8");

function runTurn(prompt, expected, timeoutMs = 120_000) {
  const { promise, resolve, reject } = Promise.withResolvers();
  const socket = new WebSocket(`${base}/api/v1/boxes/${encodeURIComponent(boxId)}/agent-terminal`);
  let output = "";
  const timeout = setTimeout(() => {
    socket.terminate();
    reject(new Error(`timed out waiting for ${expected}: ${JSON.stringify(output.slice(-4000))}`));
  }, timeoutMs);
  socket.on("open", () => {
    setTimeout(() => socket.send(JSON.stringify({ type: "input", data: `${prompt}\r` })), 2_000);
  });
  socket.on("message", (raw) => {
    const message = JSON.parse(raw.toString());
    if (message.data) output += decode(message.data);
    if (message.error) {
      clearTimeout(timeout);
      socket.terminate();
      reject(new Error(message.error));
      return;
    }
    if (output.includes(expected)) {
      clearTimeout(timeout);
      socket.send(JSON.stringify({ type: "close" }));
      socket.close();
      resolve(output);
    }
  });
  socket.on("error", (error) => {
    clearTimeout(timeout);
    reject(error);
  });
  return promise;
}

const first = await runTurn("Calculate 12345 + 67890 and reply with the number only.", "80235");
const delay = Promise.withResolvers();
setTimeout(delay.resolve, 500);
await delay.promise;
const second = await runTurn("Calculate 22222 + 33333 and reply with the number only.", "55555");
console.log(JSON.stringify({
  boxId,
  firstObserved: first.includes("80235"),
  secondObserved: second.includes("55555")
}));
