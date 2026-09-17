import { useEffect, useRef, useState, type KeyboardEvent } from "react";
import { Terminal } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import { SearchAddon } from "@xterm/addon-search";
import { Unicode11Addon } from "@xterm/addon-unicode11";
import { WebLinksAddon } from "@xterm/addon-web-links";
import "@xterm/xterm/css/xterm.css";
import { api } from "../api/client";
import { Icon } from "../components/Icon";
import { Button, StatusChip } from "../components/ui";
import { useResource } from "../hooks/useResource";
import { useI18n } from "../lib/i18n";
import { Link } from "../lib/router";

interface TerminalServerMessage {
  type: "data" | "error";
  data?: string;
  stream?: "stdout" | "stderr";
  closed?: boolean;
  exitCode?: number;
  error?: string;
}

export function TerminalView({ boxId, mode }: { boxId: string; mode: "agent" | "command" }) {
  const { t } = useI18n();
  const isAgentTerminal = mode === "agent";
  const container = useRef<HTMLDivElement>(null);
  const surface = useRef<HTMLElement>(null);
  const socketRef = useRef<WebSocket>();
  const terminalRef = useRef<Terminal>();
  const searchAddonRef = useRef<SearchAddon>();
  const [status, setStatus] = useState<"connecting" | "connected" | "closed" | "error">("connecting");
  const [error, setError] = useState<string>();
  const [search, setSearch] = useState("");
  const [connectionGeneration, setConnectionGeneration] = useState(0);
  const context = useResource(async (signal) => {
    const [box, hosts, workspaces] = await Promise.all([api.getBox(boxId, signal), api.listHosts(signal), api.listWorkspaces(signal)]);
    return {
      box,
      host: hosts.find((host) => host.id === box.hostId),
      workspace: workspaces.find((workspace) => workspace.id === box.workspaceId)
    };
  }, [boxId]);

  useEffect(() => {
    if (!container.current) return;
    setStatus("connecting");
    setError(undefined);
    const terminal = new Terminal({
      cursorBlink: true,
      convertEol: true,
      fontFamily: "JetBrains Mono, SFMono-Regular, Consolas, monospace",
      fontSize: 13,
      scrollback: 10_000,
      allowProposedApi: true,
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
    const searchAddon = new SearchAddon();
    terminal.loadAddon(fit);
    terminal.loadAddon(searchAddon);
    terminal.loadAddon(new WebLinksAddon());
    terminal.loadAddon(new Unicode11Addon());
    terminal.unicode.activeVersion = "11";
    terminal.open(container.current);
    fit.fit();
    terminalRef.current = terminal;
    searchAddonRef.current = searchAddon;

    const protocol = window.location.protocol === "https:" ? "wss:" : "ws:";
    const terminalPath = isAgentTerminal ? "agent-terminal" : "command-terminal";
    const socket = new WebSocket(`${protocol}//${window.location.host}/api/v1/boxes/${encodeURIComponent(boxId)}/${terminalPath}`);
    socketRef.current = socket;
    const sendResize = () => {
      if (socket.readyState === WebSocket.OPEN) socket.send(JSON.stringify({ type: "resize", columns: terminal.cols, rows: terminal.rows }));
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
          setError(message.error || t("terminal.error"));
          setStatus("error");
          terminal.writeln(`\r\n\x1b[31m${message.error || t("terminal.error")}\x1b[0m`);
          return;
        }
        if (message.data) {
          const binary = atob(message.data);
          terminal.write(Uint8Array.from(binary, (value) => value.charCodeAt(0)));
        }
        if (message.error) terminal.writeln(`\r\n\x1b[31m${message.error}\x1b[0m`);
        if (message.closed) {
          terminal.writeln(`\r\n[${t("terminal.closed")}${message.exitCode ? `: ${message.exitCode}` : ""}]`);
          setStatus("closed");
        }
      } catch (messageError) {
        setError(messageError instanceof Error ? messageError.message : t("terminal.invalidResponse"));
        setStatus("error");
      }
    });
    socket.addEventListener("close", () => setStatus((current) => current === "error" ? current : "closed"));
    socket.addEventListener("error", () => {
      setError(t("terminal.connectionFailed"));
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
      if (socketRef.current === socket) socketRef.current = undefined;
      if (terminalRef.current === terminal) terminalRef.current = undefined;
      if (searchAddonRef.current === searchAddon) searchAddonRef.current = undefined;
    };
  }, [boxId, connectionGeneration, isAgentTerminal, t]);

  const sendKey = (data: string) => {
    const socket = socketRef.current;
    if (socket?.readyState === WebSocket.OPEN) {
      socket.send(JSON.stringify({ type: "input", data }));
      terminalRef.current?.focus();
    }
  };
  const searchTerminal = () => {
    if (search) searchAddonRef.current?.findNext(search, { incremental: true });
    terminalRef.current?.focus();
  };
  const handleSearchKey = (event: KeyboardEvent<HTMLInputElement>) => {
    if (event.key === "Enter") searchTerminal();
  };
  const toggleFullscreen = async () => {
    if (document.fullscreenElement) await document.exitFullscreen();
    else await surface.current?.requestFullscreen();
  };

  return (
    <div className="page terminal-page">
      <header className="page-heading terminal-heading">
        <div>
          <Link className="back-link" to={`/boxes/${boxId}`}>{t("shell.boxSession")}</Link>
          <p className="eyebrow">{t(isAgentTerminal ? "terminal.agentEyebrow" : "terminal.commandEyebrow")}</p>
          <h1>{t(isAgentTerminal ? "terminal.agentTitle" : "terminal.commandTitle")}</h1>
          <p>{t(isAgentTerminal ? "terminal.agentDescription" : "terminal.commandDescription")}</p>
          <dl className="terminal-context"><div><dt>{t("box.host")}</dt><dd>{context.data?.host ? `${context.data.host.name} · ${context.data.host.systemHostname || "hostname unavailable"}` : "—"}</dd></div><div><dt>{t("box.workspace")}</dt><dd className="mono" title={context.data?.workspace?.path}>{context.data?.workspace?.path ?? "—"}</dd></div>{isAgentTerminal ? <div><dt>Session</dt><dd className="mono">abox-agent-{boxId.slice(0, 8)}</dd></div> : null}</dl>
        </div>
        <div className="terminal-heading__actions"><StatusChip status={status} /><Button icon="refresh" onClick={() => setConnectionGeneration((value) => value + 1)}>{t("terminal.reconnect")}</Button><Button icon="expand" onClick={() => void toggleFullscreen()}>{t("terminal.fullscreen")}</Button></div>
      </header>
      <nav className="box-panel-nav box-panel-nav--console" aria-label={t("shell.boxOutput")}>
        <Link className="box-panel-nav__item" to={`/boxes/${boxId}`}><Icon name="activity" /><span>{t("box.agentConsole")}</span></Link>
        <Link className={isAgentTerminal ? "box-panel-nav__item box-panel-nav__item--active" : "box-panel-nav__item"} to={`/boxes/${boxId}/agent-terminal`}><Icon name="terminal" /><span>{t("box.agentTerminal")}</span></Link>
        <Link className={!isAgentTerminal ? "box-panel-nav__item box-panel-nav__item--active" : "box-panel-nav__item"} to={`/boxes/${boxId}/command-terminal`}><Icon name="terminal" /><span>{t("box.commandTerminal")}</span></Link>
      </nav>
      {error ? <div className="stream-warning" role="alert">{error}</div> : null}
      <section className="terminal-surface" aria-label={t(isAgentTerminal ? "terminal.agentAria" : "terminal.commandAria")} ref={surface}>
        <div className="terminal-toolbar"><label><Icon name="search" /><input value={search} onChange={(event) => setSearch(event.target.value)} onKeyDown={handleSearchKey} placeholder={t("terminal.search")} /></label><button type="button" onClick={searchTerminal}>{t("terminal.findNext")}</button></div>
        <div className="terminal-container" ref={container} />
        <div className="terminal-mobile-keys" aria-label={t("terminal.mobileKeys")}><button type="button" onClick={() => sendKey("\x02")}>Ctrl-B</button><button type="button" onClick={() => sendKey("\x03")}>Ctrl-C</button><button type="button" onClick={() => sendKey("\x1b")}>Esc</button><button type="button" onClick={() => sendKey("\t")}>Tab</button><button type="button" onClick={() => sendKey("\x1b[A")}>↑</button><button type="button" onClick={() => sendKey("\x1b[B")}>↓</button><button type="button" onClick={() => sendKey("\x1b[D")}>←</button><button type="button" onClick={() => sendKey("\x1b[C")}>→</button></div>
      </section>
    </div>
  );
}
