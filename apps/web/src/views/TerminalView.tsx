import { useEffect, useRef, useState } from "react";
import { Terminal } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import "@xterm/xterm/css/xterm.css";
import { Link } from "../lib/router";
import { StatusChip } from "../components/ui";

interface TerminalServerMessage {
  type: "data" | "error";
  data?: string;
  stream?: "stdout" | "stderr";
  closed?: boolean;
  exitCode?: number;
  error?: string;
}

export function TerminalView({ boxId }: { boxId: string }) {
  const container = useRef<HTMLDivElement>(null);
  const [status, setStatus] = useState<"connecting" | "connected" | "closed" | "error">("connecting");
  const [error, setError] = useState<string>();

  useEffect(() => {
    if (!container.current) return;
    const terminal = new Terminal({
      cursorBlink: true,
      convertEol: true,
      fontFamily: "JetBrains Mono, SFMono-Regular, Consolas, monospace",
      fontSize: 13,
      theme: {
        background: "#090d14",
        foreground: "#d9e1ec",
        cursor: "#63dfc6",
        selectionBackground: "#315f59",
        black: "#101722",
        brightBlack: "#5c6b7d",
        green: "#63dfc6",
        brightGreen: "#86f4dc",
        red: "#ff7f91",
        brightRed: "#ff9aaa",
        yellow: "#efc36b",
        blue: "#80a8ff",
        magenta: "#be93ff",
        cyan: "#77d5e8",
        white: "#d9e1ec"
      }
    });
    const fit = new FitAddon();
    terminal.loadAddon(fit);
    terminal.open(container.current);
    fit.fit();

    const protocol = window.location.protocol === "https:" ? "wss:" : "ws:";
    const socket = new WebSocket(`${protocol}//${window.location.host}/api/v1/boxes/${encodeURIComponent(boxId)}/terminal`);
    const sendResize = () => {
      if (socket.readyState === WebSocket.OPEN) {
        socket.send(JSON.stringify({ type: "resize", columns: terminal.cols, rows: terminal.rows }));
      }
    };
    socket.addEventListener("open", () => {
      setStatus("connected");
      terminal.focus();
      sendResize();
    });
    socket.addEventListener("message", (event) => {
      try {
        const message = JSON.parse(String(event.data)) as TerminalServerMessage;
        if (message.type === "error") {
          setError(message.error || "Terminal error");
          setStatus("error");
          terminal.writeln(`\r\n\x1b[31m${message.error || "Terminal error"}\x1b[0m`);
          return;
        }
        if (message.data) {
          const binary = atob(message.data);
          const bytes = Uint8Array.from(binary, (value) => value.charCodeAt(0));
          terminal.write(bytes);
        }
        if (message.error) terminal.writeln(`\r\n\x1b[31m${message.error}\x1b[0m`);
        if (message.closed) {
          terminal.writeln(`\r\n[session closed${message.exitCode ? `: ${message.exitCode}` : ""}]`);
          setStatus("closed");
        }
      } catch (messageError) {
        setError(messageError instanceof Error ? messageError.message : "Invalid terminal response");
        setStatus("error");
      }
    });
    socket.addEventListener("close", () => setStatus((current) => current === "error" ? current : "closed"));
    socket.addEventListener("error", () => {
      setError("Terminal connection failed");
      setStatus("error");
    });
    const dataSubscription = terminal.onData((data) => {
      if (socket.readyState === WebSocket.OPEN) socket.send(JSON.stringify({ type: "input", data }));
    });
    const resizeSubscription = terminal.onResize(sendResize);
    const observer = new ResizeObserver(() => {
      fit.fit();
      sendResize();
    });
    observer.observe(container.current);

    return () => {
      observer.disconnect();
      dataSubscription.dispose();
      resizeSubscription.dispose();
      if (socket.readyState === WebSocket.OPEN) socket.send(JSON.stringify({ type: "close" }));
      socket.close();
      terminal.dispose();
    };
  }, [boxId]);

  return (
    <div className="page terminal-page">
      <header className="page-heading">
        <div>
          <Link className="back-link" to={`/boxes/${boxId}`}>Box session</Link>
          <p className="eyebrow">Interactive shell</p>
          <h1>Terminal</h1>
          <p>Commands execute in this box&apos;s workspace on its assigned host.</p>
        </div>
        <StatusChip status={status} />
      </header>
      {error ? <div className="stream-warning" role="alert">{error}</div> : null}
      <section className="terminal-surface" aria-label="Interactive terminal">
        <div className="terminal-container" ref={container} />
      </section>
    </div>
  );
}
