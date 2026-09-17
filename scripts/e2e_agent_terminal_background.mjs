import { execFileSync } from "node:child_process";
import WebSocket from "ws";

const base = process.env.AGENTBOX_WS_URL ?? "ws://127.0.0.1:8080";
const boxId = process.argv[2];
const marker = process.argv[3] ?? "BACKGROUND_DONE";
if (!boxId) throw new Error("usage: node scripts/e2e_agent_terminal_background.mjs <box-id> [marker]");

const wait = (milliseconds) => {
  const { promise, resolve } = Promise.withResolvers();
  setTimeout(resolve, milliseconds);
  return promise;
};

const { promise: opened, resolve: resolveOpened, reject: rejectOpened } = Promise.withResolvers();
const socket = new WebSocket(`${base}/api/v1/boxes/${encodeURIComponent(boxId)}/agent-terminal`);
socket.on("open", resolveOpened);
socket.on("error", rejectOpened);
await opened;
await wait(2_000);
socket.send(JSON.stringify({ type: "input", data: `!sleep 5; echo ${marker}\r` }));
await wait(500);
socket.send(JSON.stringify({ type: "close" }));
socket.close();
await wait(7_000);
const sessionName = `abox-agent-${boxId}`;
const transcript = execFileSync("tmux", ["capture-pane", "-p", "-t", sessionName, "-S", "-120"], { encoding: "utf8" });
if (!transcript.includes(marker)) {
  throw new Error(`background command did not continue after detach: ${transcript.slice(-2000)}`);
}
console.log(JSON.stringify({ boxId, sessionName, marker, backgroundContinued: true }));
