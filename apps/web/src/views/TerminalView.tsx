import { useEffect, useRef, useState, type KeyboardEvent } from "react";
import { Terminal } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import { SearchAddon } from "@xterm/addon-search";
import { Unicode11Addon } from "@xterm/addon-unicode11";
import { WebLinksAddon } from "@xterm/addon-web-links";
import "@xterm/xterm/css/xterm.css";
import { api, errorMessage } from "../api/client";
import type { RuntimeSessionAttachment, RuntimeType } from "../api/types";
import { Icon } from "../components/Icon";
import { useToast } from "../components/Toast";
import { Button, InlineAlert, Modal, StatusChip } from "../components/ui";
import { useResource } from "../hooks/useResource";
import { roleAtLeast, useAccess } from "../lib/access";
import { useI18n } from "../lib/i18n";
import { Link, navigate } from "../lib/router";

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
  const { currentUser, meta } = useAccess();
  const { notify } = useToast();
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
  const [deleteOpen, setDeleteOpen] = useState(false);
  const [deleteBusy, setDeleteBusy] = useState(false);
  const [deleteError, setDeleteError] = useState<string>();
  const [modelOpen, setModelOpen] = useState(false);
  const [modelValue, setModelValue] = useState("");
  const [modelBusy, setModelBusy] = useState(false);
  const [settingsError, setSettingsError] = useState<string>();
  const [visibilityBusy, setVisibilityBusy] = useState(false);
  const [sessionCopied, setSessionCopied] = useState(false);
  const [widescreen, setWidescreen] = useState(false);
  const [sessionActionBusy, setSessionActionBusy] = useState(false);
  const [sessionActionError, setSessionActionError] = useState<string>();
  const context = useResource(async (signal) => {
    const [box, hosts, workspaces] = await Promise.all([api.getBox(boxId, signal), api.listHosts(signal), api.listWorkspaces(signal)]);
    return {
      box,
      host: hosts.find((host) => host.id === box.hostId),
      workspace: workspaces.find((workspace) => workspace.id === box.workspaceId)
    };
  }, [boxId]);
  const attachments = useResource<RuntimeSessionAttachment[]>((signal) => api.listRuntimeSessionAttachments(boxId, signal), [boxId]);

  useEffect(() => {
    if (!container.current) return;
    setStatus("connecting");
    setError(undefined);
    const terminal = new Terminal({
      cursorBlink: false,
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
          terminal.write(Uint8Array.from(binary, (value) => value.charCodeAt(0)), () => terminal.refresh(0, terminal.rows - 1));
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

  useEffect(() => {
    document.body.classList.toggle("terminal-widescreen-active", widescreen);
    if (!widescreen) return;
    const exitOnEscape = (event: globalThis.KeyboardEvent) => {
      if (event.key !== "Escape") return;
      event.preventDefault();
      event.stopPropagation();
      setWidescreen(false);
    };
    window.addEventListener("keydown", exitOnEscape, true);
    return () => {
      document.body.classList.remove("terminal-widescreen-active");
      window.removeEventListener("keydown", exitOnEscape, true);
    };
  }, [widescreen]);

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

  const canManageBox = roleAtLeast(currentUser?.role, "admin") || context.data?.box.ownerUserId === currentUser?.id;
  const canDelete = canManageBox;
  const deleteBox = async () => {
    setDeleteBusy(true);
    setDeleteError(undefined);
    try {
      await api.deleteBox(boxId);
      notify(t("box.deleted"));
      navigate("/boxes", { replace: true });
    } catch (requestError) {
      setDeleteError(errorMessage(requestError));
      setDeleteBusy(false);
    }
  };
  const switchModel = async () => {
    setModelBusy(true);
    setSettingsError(undefined);
    try {
      const box = await api.updateBoxModel(boxId, modelValue.trim());
      context.setData((current) => current ? { ...current, box } : current);
      setModelOpen(false);
      setConnectionGeneration((value) => value + 1);
      notify(`Model switched to ${box.model || "runtime default"}.`);
    } catch (requestError) {
      setSettingsError(errorMessage(requestError));
    } finally {
      setModelBusy(false);
    }
  };
  const toggleVisibility = async () => {
    const current = context.data?.box.visibility ?? "private";
    setVisibilityBusy(true);
    setSettingsError(undefined);
    try {
      const box = await api.updateBoxVisibility(boxId, current === "private" ? "org" : "private");
      context.setData((value) => value ? { ...value, box } : value);
      notify(box.visibility === "org" ? "Box is visible to all organization users." : "Box is private to its owner.");
    } catch (requestError) {
      setSettingsError(errorMessage(requestError));
    } finally {
      setVisibilityBusy(false);
    }
  };
  const detachRuntimeSession = async () => {
    setSessionActionBusy(true);
    setSessionActionError(undefined);
    try {
      await api.detachRuntimeSession(boxId);
      context.setData((current) => current ? { ...current, box: { ...current.box, runtimeSessionMode: "new", runtimeSessionRef: undefined } } : current);
      attachments.reload();
      setConnectionGeneration((value) => value + 1);
      notify(t("terminal.sessionDetached"));
    } catch (requestError) {
      setSessionActionError(errorMessage(requestError));
    } finally {
      setSessionActionBusy(false);
    }
  };
  const stopRuntimeSession = async () => {
    setSessionActionBusy(true);
    setSessionActionError(undefined);
    try {
      await api.stopRuntimeSession(boxId);
      context.setData((current) => current ? { ...current, box: { ...current.box, runtimeSessionMode: "resume" } } : current);
      attachments.reload();
      setConnectionGeneration((value) => value + 1);
      notify(t("terminal.sessionStopped"));
    } catch (requestError) {
      setSessionActionError(errorMessage(requestError));
    } finally {
      setSessionActionBusy(false);
    }
  };
  const runtimeType = context.data?.box.runtimeType as RuntimeType | undefined;
  const modelSuggestions = runtimeType ? meta?.runtimeModels[runtimeType] ?? [] : [];
  const terminalSessionID = `abox-agent-${boxId}`;
  const copySessionID = async () => {
    await navigator.clipboard.writeText(terminalSessionID);
    setSessionCopied(true);
    window.setTimeout(() => setSessionCopied(false), 1500);
  };

  return (
    <div className={widescreen ? "page terminal-page terminal-page--wide" : "page terminal-page"}>
      <header className="page-heading terminal-heading">
        <div>
          <Link className="back-link" to="/boxes">{t("nav.boxes")}</Link>
          <p className="eyebrow">{t(isAgentTerminal ? "terminal.agentEyebrow" : "terminal.commandEyebrow")}</p>
          <h1>{t(isAgentTerminal ? "terminal.agentTitle" : "terminal.commandTitle")}</h1>
          <p>{t(isAgentTerminal ? "terminal.agentDescription" : "terminal.commandDescription")}</p>
          <dl className="terminal-context"><div><dt>{t("box.host")}</dt><dd>{context.data?.host ? `${context.data.host.name} · ${context.data.host.systemHostname || "hostname unavailable"}` : "—"}</dd></div><div><dt>{t("box.workspace")}</dt><dd className="mono" title={context.data?.workspace?.path}>{context.data?.workspace?.path ?? "—"}</dd></div></dl>
        </div>
        <div className="terminal-heading__actions">{isAgentTerminal ? <button className="terminal-session-id" type="button" onClick={() => void copySessionID()} title="Copy session_id"><span>session_id</span><code>{terminalSessionID}</code><Icon name="copy" size={14} />{sessionCopied ? <b>Copied</b> : null}</button> : null}<StatusChip status={status} />{context.data?.box.runtimeSessionMode && context.data.box.runtimeSessionMode !== "new" ? <span className="status-chip status-chip--neutral">{t("terminal.sharedSession", { count: attachments.data?.length ?? 0 })} · {t("terminal.sharedInput")}</span> : null}{isAgentTerminal && canManageBox ? <Button icon="edit" onClick={() => { setModelValue(context.data?.box.model ?? ""); setModelOpen(true); }}>Model: {context.data?.box.model || "default"}</Button> : null}{canManageBox ? <Button icon="share" busy={visibilityBusy} onClick={() => void toggleVisibility()}>{context.data?.box.visibility === "org" ? "Organization visible" : "Private"}</Button> : null}{context.data?.box.runtimeSessionMode && context.data.box.runtimeSessionMode !== "new" && canManageBox ? <Button busy={sessionActionBusy} onClick={() => void detachRuntimeSession()}>{t("terminal.detachSession")}</Button> : null}{context.data?.box.runtimeSessionMode && context.data.box.runtimeSessionMode !== "new" && roleAtLeast(currentUser?.role, "admin") ? <Button variant="danger" busy={sessionActionBusy} onClick={() => void stopRuntimeSession()}>{t("terminal.stopSharedSession")}</Button> : null}<Button icon="refresh" onClick={() => setConnectionGeneration((value) => value + 1)}>{t("terminal.reconnect")}</Button><Button icon="expand" onClick={() => void toggleFullscreen()}>{t("terminal.fullscreen")}</Button>{canDelete ? <Button variant="danger" icon="trash" onClick={() => setDeleteOpen(true)}>{t("box.delete")}</Button> : null}</div>
      </header>
      <nav className="box-panel-nav" aria-label={t("shell.boxOutput")}>
        <Link className={isAgentTerminal ? "box-panel-nav__item box-panel-nav__item--active" : "box-panel-nav__item"} to={`/boxes/${boxId}/agent-terminal`}><Icon name="terminal" /><span>{t("box.agentTerminal")}</span></Link>
        <Link className={!isAgentTerminal ? "box-panel-nav__item box-panel-nav__item--active" : "box-panel-nav__item"} to={`/boxes/${boxId}/command-terminal`}><Icon name="terminal" /><span>{t("box.commandTerminal")}</span></Link>
        <Link className="box-panel-nav__item" to={`/boxes/${boxId}/subagents`}><Icon name="agent" /><span>{t("box.subagents")}</span></Link>
        <Link className="box-panel-nav__item" to={`/boxes/${boxId}/todos`}><Icon name="todo" /><span>{t("box.todos")}</span></Link>
        <Link className="box-panel-nav__item" to={`/boxes/${boxId}/artifacts`}><Icon name="artifact" /><span>{t("box.artifacts")}</span></Link>
        <Link className="box-panel-nav__item" to={`/boxes/${boxId}/diff`}><Icon name="diff" /><span>{t("box.diff")}</span></Link>
      </nav>
      {error ? <div className="stream-warning" role="alert">{error}</div> : null}
      {deleteError ? <InlineAlert>{deleteError}</InlineAlert> : null}
      {settingsError ? <InlineAlert>{settingsError}</InlineAlert> : null}
      {sessionActionError ? <InlineAlert>{sessionActionError}</InlineAlert> : null}
      <section className="terminal-surface" aria-label={t(isAgentTerminal ? "terminal.agentAria" : "terminal.commandAria")} ref={surface}>
        <div className="terminal-toolbar"><label><Icon name="search" /><input value={search} onChange={(event) => setSearch(event.target.value)} onKeyDown={handleSearchKey} placeholder={t("terminal.search")} /></label><button type="button" onClick={searchTerminal}>{t("terminal.findNext")}</button><button type="button" aria-pressed={widescreen} onClick={() => setWidescreen((value) => !value)}><Icon name="expand" size={14} />{t(widescreen ? "terminal.exitWidescreen" : "terminal.widescreen")}</button></div>
        <div className="terminal-container" ref={container} />
        <div className="terminal-mobile-keys" aria-label={t("terminal.mobileKeys")}><button type="button" onClick={() => sendKey("\x02")}>Ctrl-B</button><button type="button" onClick={() => sendKey("\x03")}>Ctrl-C</button><button type="button" onClick={() => sendKey("\x1b")}>Esc</button><button type="button" onClick={() => sendKey("\t")}>Tab</button><button type="button" onClick={() => sendKey("\x1b[A")}>↑</button><button type="button" onClick={() => sendKey("\x1b[B")}>↓</button><button type="button" onClick={() => sendKey("\x1b[D")}>←</button><button type="button" onClick={() => sendKey("\x1b[C")}>→</button></div>
      </section>
      <Modal open={deleteOpen} title={t("box.deleteTitle", { name: context.data?.box.name ?? "Agent Box" })} description={t("box.deleteDescription")} onClose={() => !deleteBusy && setDeleteOpen(false)} size="small">
        <div className="confirm-dialog"><InlineAlert tone="warning">{t("box.deleteWarning")}</InlineAlert><div className="modal__actions"><Button disabled={deleteBusy} onClick={() => setDeleteOpen(false)}>{t("common.cancel")}</Button><Button variant="danger" icon="trash" busy={deleteBusy} onClick={() => void deleteBox()}>{t("box.deleteConfirm")}</Button></div></div>
      </Modal>
      <Modal open={modelOpen} title="Switch model" description="Changing the model restarts the persistent Agent Terminal and applies to future structured runs for this Box." onClose={() => !modelBusy && setModelOpen(false)} size="small">
        <div className="modal-form"><label className="field"><span>Model</span><input autoFocus list={`box-model-suggestions-${runtimeType ?? "runtime"}`} value={modelValue} onChange={(event) => setModelValue(event.target.value)} placeholder="Leave empty for Agent default" /><datalist id={`box-model-suggestions-${runtimeType ?? "runtime"}`}>{modelSuggestions.map((model) => <option value={model} key={model} />)}</datalist><small>Enter any model ID supported by {runtimeType?.toUpperCase() ?? "the runtime"}.</small></label><InlineAlert tone="warning">The current tmux Agent Terminal and active structured Runtime will be stopped before reconnecting.</InlineAlert><div className="modal__actions"><Button disabled={modelBusy} onClick={() => setModelOpen(false)}>{t("common.cancel")}</Button><Button variant="primary" busy={modelBusy} onClick={() => void switchModel()}>Switch model</Button></div></div>
      </Modal>
    </div>
  );
}
