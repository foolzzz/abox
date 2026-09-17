import { useEffect, useMemo, useRef, useState, type FormEvent, type KeyboardEvent } from "react";
import { api, ApiError, errorMessage } from "../api/client";
import { streamBoxEvents, type StreamState } from "../api/sse";
import type { AccessControlEntry, Agent, AgentEvent, Approval, BoxSnapshot, BoxStatus, DeliveryMode, Host, Message, Workspace } from "../api/types";
import { AccessControlDialog } from "../components/AccessControlDialog";
import { Icon } from "../components/Icon";
import { useToast } from "../components/Toast";
import { Button, EmptyState, ErrorState, InlineAlert, LoadingState, Modal, StatusChip, cx } from "../components/ui";
import { useResource } from "../hooks/useResource";
import { roleAtLeast, useAccess } from "../lib/access";
import { isObjectRecord } from "../lib/data";
import { compactJson, extractText, formatTime, humanize, initials, messageText, relativeTime } from "../lib/format";
import { Link } from "../lib/router";
import { ApprovalCard } from "./ApprovalsView";

interface BoxDetailData {
  snapshot: BoxSnapshot;
  messages: Message[];
  approvals: Approval[];
  agents: Agent[];
  hosts: Host[];
  workspaces: Workspace[];
  acl: AccessControlEntry[];
}

interface LiveMessage {
  id: string;
  role: "user" | "assistant" | "system";
  actor: string;
  text: string;
  thinking: string;
  occurredAt: string;
  complete: boolean;
}

interface ActivityItem {
  id: string;
  kind: "tool" | "subagent" | "approval" | "todo" | "notice" | "run";
  title: string;
  detail?: string;
  status: string;
  occurredAt: string;
  actor: string;
  input?: unknown;
  output?: unknown;
  seq: number;
}

export function BoxDetailView({ boxId }: { boxId: string }) {
  const { currentUser } = useAccess();
  const { notify } = useToast();
  const resource = useResource<BoxDetailData>(async (signal) => {
    const [snapshot, messages, approvals, agents, hosts, workspaces, acl] = await Promise.all([
      api.getBox(boxId, signal),
      api.listMessages(boxId, signal),
      api.listApprovals(signal).catch((requestError: unknown) => requestError instanceof ApiError && requestError.status === 403 ? [] : Promise.reject(requestError)),
      api.listAgents(signal),
      api.listHosts(signal),
      api.listWorkspaces(signal),
      api.getBoxAcl(boxId, signal)
    ]);
    return { snapshot, messages, approvals, agents, hosts, workspaces, acl };
  }, [boxId]);
  const [events, setEvents] = useState<AgentEvent[]>([]);
  const [streamState, setStreamState] = useState<StreamState>("connecting");
  const [streamAttempt, setStreamAttempt] = useState(0);
  const [streamError, setStreamError] = useState<string>();
  const [composer, setComposer] = useState("");
  const [delivery, setDelivery] = useState<DeliveryMode>("prompt");
  const [sending, setSending] = useState(false);
  const [sendError, setSendError] = useState<string>();
  const [controlAction, setControlAction] = useState<"interrupt" | "stop" | "resume">();
  const [controlError, setControlError] = useState<string>();
  const [confirmStop, setConfirmStop] = useState(false);
  const [sharingOpen, setSharingOpen] = useState(false);
  const refreshTimer = useRef<number>();
  const conversationRef = useRef<HTMLDivElement>(null);
  const stickToBottom = useRef(true);

  useEffect(() => {
    setEvents([]);
    setStreamError(undefined);
    const controller = new AbortController();
    void streamBoxEvents(boxId, {
      signal: controller.signal,
      lastEventId: 0,
      onState: (state, attempt) => {
        setStreamState(state);
        setStreamAttempt(attempt);
      },
      onSnapshotRequired: async () => {
        const [snapshot, messages] = await Promise.all([
          api.getBox(boxId, controller.signal),
          api.listMessages(boxId, controller.signal)
        ]);
        setEvents([]);
        resource.setData((current) => current ? { ...current, snapshot, messages } : current);
        return snapshot.lastEventSeq;
      },
      onError: (error) => setStreamError(error.message),
      onEvent: (event) => {
        setStreamError(undefined);
        setEvents((current) => {
          if (current.some((candidate) => candidate.seq === event.seq || candidate.eventId === event.eventId)) return current;
          const next = [...current, event];
          return next.length > 2_000 ? next.slice(-1_500) : next;
        });
        if (/^(message\.completed|run\.|runtime\.|approval\.)/.test(event.type)) {
          window.clearTimeout(refreshTimer.current);
          refreshTimer.current = window.setTimeout(resource.reload, 350);
        }
      }
    });
    return () => {
      controller.abort();
      window.clearTimeout(refreshTimer.current);
    };
  }, [boxId, resource.reload]);

  const liveMessages = useMemo(() => deriveLiveMessages(events, new Set(resource.data?.messages.map((message) => message.id) ?? [])), [events, resource.data?.messages]);
  const activity = useMemo(() => deriveActivity(events), [events]);

  useEffect(() => {
    if (stickToBottom.current) conversationRef.current?.scrollTo({ top: conversationRef.current.scrollHeight, behavior: "smooth" });
  }, [resource.data?.messages.length, liveMessages]);

  if (resource.loading) return <LoadingState label="Loading box session" />;
  if (resource.error && !resource.data) return <ErrorState error={resource.error} retry={resource.reload} />;

  const data = resource.data!;
  const { snapshot } = data;
  const agent = data.agents.find((candidate) => candidate.id === snapshot.agentId);
  const host = data.hosts.find((candidate) => candidate.id === snapshot.hostId);
  const workspace = data.workspaces.find((candidate) => candidate.id === snapshot.workspaceId);
  const pendingApprovals = data.approvals.filter((approval) => approval.boxId === boxId && approval.status === "pending");
  const directAccess = data.acl.find((entry) => entry.userId === currentUser?.id)?.role;
  const ownsBox = snapshot.ownerUserId === currentUser?.id || directAccess === "owner";
  const hasOrganizationControl = roleAtLeast(currentUser?.role, "admin");
  const canOperateBox = hasOrganizationControl || ownsBox || directAccess === "operator";
  const effectiveAccess = hasOrganizationControl ? `${currentUser?.role} · organization-wide` : ownsBox ? "owner" : directAccess ?? "viewer";
  const recentEvents = events.filter((event) => event.seq > snapshot.lastEventSeq);
  const effectiveStatus = recentEvents.reduce<BoxStatus>((status, event) => {
    if (event.type === "run.started") return "running";
    if (event.type === "run.completed") return "idle";
    if (event.type === "run.failed") return "error";
    if (event.type === "approval.requested") return "waiting_approval";
    if (event.type === "approval.resolved" && status === "waiting_approval") return "running";
    return status;
  }, snapshot.status);
  const effectiveRunId = [...recentEvents].reverse().find((event) => event.type === "run.started")?.runId ?? snapshot.activeRunId;
  const canInterrupt = effectiveStatus === "running" || effectiveStatus === "waiting_approval";
  const canStop = !["terminated", "hibernated", "hibernating"].includes(effectiveStatus);
  const canResume = effectiveStatus === "hibernated";

  const send = async (event?: FormEvent) => {
    event?.preventDefault();
    const content = composer.trim();
    if (!canOperateBox || !content || sending) return;
    setSending(true);
    setSendError(undefined);
    try {
      const message = await api.sendMessage(boxId, { content, delivery }, crypto.randomUUID());
      resource.setData((current) => {
        if (!current || current.messages.some((candidate) => candidate.id === message.id)) return current;
        return { ...current, messages: [...current.messages, message] };
      });
      setComposer("");
      stickToBottom.current = true;
      notify(delivery === "prompt" ? "Prompt accepted." : delivery === "steer" ? "Steer accepted." : "Follow-up queued.");
    } catch (requestError) {
      setSendError(errorMessage(requestError));
    } finally {
      setSending(false);
    }
  };

  const handleComposerKey = (event: KeyboardEvent<HTMLTextAreaElement>) => {
    if (event.key === "Enter" && (event.metaKey || event.ctrlKey)) {
      event.preventDefault();
      void send();
    }
  };

  const control = async (action: "interrupt" | "stop" | "resume") => {
    if (!canOperateBox) return;
    setControlAction(action);
    setControlError(undefined);
    try {
      if (action === "interrupt") await api.interruptBox(boxId);
      else if (action === "stop") await api.stopBox(boxId);
      else await api.resumeBox(boxId);
      notify(action === "interrupt" ? "Interrupt requested." : action === "stop" ? "Stop requested." : "Resume requested.");
      setConfirmStop(false);
      window.setTimeout(resource.reload, 500);
    } catch (requestError) {
      setControlError(errorMessage(requestError));
    } finally {
      setControlAction(undefined);
    }
  };

  return (
    <div className="box-console">
      <header className="box-console__header">
        <div className="box-console__identity">
          <Link className="back-link" to="/boxes">Boxes</Link><span>/</span>
          <div><span className="resource-icon resource-icon--box"><Icon name="box" /></span><div><h1>{snapshot.name}</h1><p>{agent?.name ?? "Unknown agent"} · {workspace?.name ?? "Unknown workspace"}</p></div></div>
        </div>
        <div className="box-console__controls">
          <StatusChip status={effectiveStatus} />
          <Button icon="share" onClick={() => setSharingOpen(true)}>Sharing</Button>
          {canOperateBox && canInterrupt ? <Button icon="interrupt" busy={controlAction === "interrupt"} disabled={Boolean(controlAction)} onClick={() => void control("interrupt")}>Interrupt</Button> : null}
          {canOperateBox && canResume ? <Button icon="resume" variant="primary" busy={controlAction === "resume"} disabled={Boolean(controlAction)} onClick={() => void control("resume")}>Resume</Button> : null}
          {canOperateBox && canStop ? <Button icon="stop" variant="danger" disabled={Boolean(controlAction)} onClick={() => setConfirmStop(true)}>Stop</Button> : null}
        </div>
      </header>
      {resource.error ? <InlineAlert tone="warning">The latest snapshot could not be loaded. Live events remain connected.</InlineAlert> : null}
      {controlError ? <InlineAlert>{controlError}</InlineAlert> : null}

      <div className="console-layout">
        <section className="conversation-panel" aria-label="Conversation">
          <div className="conversation-panel__header">
            <div><p className="eyebrow">Conversation</p><h2>Agent thread</h2></div>
            <div className="stream-indicator" title={streamError}><span className={cx("stream-indicator__dot", `stream-indicator__dot--${streamState}`)} />{streamState === "open" ? "Live" : streamState === "retrying" ? `Reconnecting${streamAttempt ? ` · ${streamAttempt}` : ""}` : humanize(streamState)}</div>
          </div>
          {streamError && streamState === "retrying" ? <div className="stream-warning" role="status">Live updates paused: {streamError}. Retrying automatically.</div> : null}
          <div
            className="message-list"
            ref={conversationRef}
            onScroll={(event) => {
              const element = event.currentTarget;
              stickToBottom.current = element.scrollHeight - element.scrollTop - element.clientHeight < 160;
            }}
          >
            {data.messages.length === 0 && liveMessages.length === 0 ? (
              <EmptyState icon="spark" title={canOperateBox ? "Start the conversation" : "No messages yet"} description={canOperateBox ? "Send a prompt to begin a run in this box." : "Your access is read-only. An owner or operator can start the conversation."} />
            ) : (
              <>
                {data.messages.map((message) => <MessageBubble message={message} agentName={agent?.name} currentUserId={currentUser?.id} canCancel={canOperateBox} key={message.id} onCancelled={(cancelled) => resource.setData((current) => current ? { ...current, messages: current.messages.map((candidate) => candidate.id === cancelled.id ? cancelled : candidate) } : current)} />)}
                {liveMessages.map((message) => <LiveMessageBubble message={message} key={message.id} />)}
              </>
            )}
          </div>
          {canOperateBox ? (
            <form className="composer" onSubmit={send}>
              <fieldset className="delivery-selector">
                <legend className="sr-only">Delivery mode</legend>
                {(["prompt", "steer", "follow_up"] as DeliveryMode[]).map((mode) => (
                  <label className={cx(delivery === mode && "delivery-selector__option--active")} key={mode}>
                    <input type="radio" name="delivery" value={mode} checked={delivery === mode} onChange={() => setDelivery(mode)} />
                    {mode === "follow_up" ? "Follow-up" : humanize(mode)}
                  </label>
                ))}
              </fieldset>
              <div className="composer__input">
                <textarea aria-label="Message" rows={3} value={composer} onChange={(event) => setComposer(event.target.value)} onKeyDown={handleComposerKey} placeholder={delivery === "prompt" ? "Give the agent a task…" : delivery === "steer" ? "Redirect the active run…" : "Queue work after the active run…"} />
                <Button type="submit" variant="primary" icon="send" busy={sending} disabled={!composer.trim()} aria-label="Send message">Send</Button>
              </div>
              <div className="composer__footer"><span>{delivery === "prompt" ? "Starts a new run when idle." : delivery === "steer" ? "Adjusts the active run immediately." : "Runs after the current turn completes."}</span><span><kbd>⌘</kbd><span>+</span><kbd>Enter</kbd> to send</span></div>
              {sendError ? <InlineAlert>{sendError}</InlineAlert> : null}
            </form>
          ) : <div className="composer composer--read-only"><Icon name="approval" /><div><strong>Read-only conversation</strong><span>Your {effectiveAccess} access can view messages and live events, but cannot send or control this box.</span></div></div>}
        </section>

        <aside className="console-sidebar" aria-label="Session context">
          <section className="session-card">
            <div className="panel__header"><div><p className="eyebrow">Runtime</p><h2>Session</h2></div><Icon name="activity" /></div>
            <dl className="session-facts">
              <div><dt>Run</dt><dd className="mono" title={effectiveRunId ?? undefined}>{effectiveRunId ? effectiveRunId.slice(0, 8) : "Idle"}</dd></div>
              <div><dt>Runtime</dt><dd>{(snapshot.runtimeType ?? agent?.runtimeType ?? "—").toUpperCase()}</dd></div>
              <div><dt>Host</dt><dd><span className={cx("mini-dot", host?.status === "online" && "mini-dot--online")} />{host?.name ?? "Unknown"}</dd></div>
              <div><dt>Workspace</dt><dd title={workspace?.path}>{workspace?.name ?? "Unknown"}</dd></div>
              <div><dt>Access</dt><dd>{effectiveAccess}</dd></div>
              <div><dt>Event cursor</dt><dd className="mono">#{Math.max(snapshot.lastEventSeq, events.at(-1)?.seq ?? 0)}</dd></div>
            </dl>
          </section>

          {pendingApprovals.length ? (
            <section className="sidebar-section">
              <div className="panel__header"><div><p className="eyebrow">Decision needed</p><h2>Pending approval</h2></div><span className="nav-badge">{pendingApprovals.length}</span></div>
              {pendingApprovals.map((approval) => <ApprovalCard approval={approval} compact canDecide={canOperateBox} key={approval.id} onResolved={(resolved) => resource.setData((current) => current ? { ...current, approvals: current.approvals.map((item) => item.id === resolved.id ? resolved : item) } : current)} />)}
            </section>
          ) : null}

          <section className="activity-panel">
            <div className="panel__header"><div><p className="eyebrow">Live trace</p><h2>Activity</h2></div><span className="event-count">{activity.length}</span></div>
            {activity.length ? <ActivityTimeline items={activity} /> : <div className="activity-empty"><Icon name="terminal" /><p>No tool activity yet.</p><small>Runtime events will appear as the agent works.</small></div>}
          </section>
        </aside>
      </div>

      <Modal open={confirmStop} onClose={() => setConfirmStop(false)} title={`Stop ${snapshot.name}?`} description="The runtime will be asked to stop gracefully." size="small">
        <div className="confirm-dialog"><p>The box and its history remain available, but the active process will end.</p><div className="modal__actions"><Button onClick={() => setConfirmStop(false)}>Keep running</Button><Button variant="danger" icon="stop" busy={controlAction === "stop"} onClick={() => void control("stop")}>Stop box</Button></div></div>
      </Modal>
      <AccessControlDialog open={sharingOpen} resourceKind="box" resourceId={boxId} resourceName={snapshot.name} onClose={() => setSharingOpen(false)} onSaved={resource.reload} />
    </div>
  );
}

function MessageBubble({ message, agentName, currentUserId, canCancel, onCancelled }: { message: Message; agentName?: string; currentUserId?: string; canCancel: boolean; onCancelled: (message: Message) => void }) {
  const { notify } = useToast();
  const [cancelling, setCancelling] = useState(false);
  const [cancelError, setCancelError] = useState<string>();
  const text = messageText(message.content, message.plainText);
  const authoredByCurrentUser = Boolean(currentUserId && message.authorUserId === currentUserId);
  const authorLabel = authoredByCurrentUser
    ? `${message.authorName || "You"}${message.authorName ? " (you)" : ""}`
    : message.authorName || (message.role === "assistant" ? agentName || "Agent" : message.role === "system" ? "System" : message.authorUserId ? `User ${message.authorUserId.slice(0, 8)}` : "Organization member");
  const cancellable = canCancel && message.delivery === "follow_up" && message.status === "queued";

  const cancel = async () => {
    setCancelling(true);
    setCancelError(undefined);
    try {
      const cancelled = await api.cancelMessage(message.boxId, message.id);
      onCancelled(cancelled);
      notify("Queued follow-up cancelled.");
    } catch (requestError) {
      setCancelError(errorMessage(requestError));
    } finally {
      setCancelling(false);
    }
  };

  return (
    <article className={cx("message", `message--${message.role}`, message.status === "cancelled" && "message--cancelled")}>
      <div className="message__avatar" aria-hidden="true">{message.role === "user" ? initials(authorLabel.replace(" (you)", "")) : message.role === "assistant" ? <Icon name="agent" size={17} /> : <Icon name="activity" size={17} />}</div>
      <div className="message__body">
        <header><strong>{authorLabel}</strong><span>{message.role === "user" ? "User" : message.role === "assistant" ? "Agent" : "System"}</span>{message.delivery ? <span>{message.delivery === "follow_up" ? "Follow-up" : humanize(message.delivery)}</span> : null}{message.status !== "completed" ? <span className={`message-status message-status--${message.status}`}>{humanize(message.status)}</span> : null}<time dateTime={message.createdAt}>{formatTime(message.createdAt)}</time></header>
        <div className="message__content">{text || <em>No text content</em>}</div>
        {cancellable ? <div className="message__actions"><Button type="button" variant="ghost" icon="close" busy={cancelling} onClick={() => void cancel()}>Cancel queued message</Button></div> : null}
        {cancelError ? <p className="message__error" role="alert">{cancelError}</p> : null}
      </div>
    </article>
  );
}

function LiveMessageBubble({ message }: { message: LiveMessage }) {
  return (
    <article className={cx("message", `message--${message.role}`, "message--live")}>
      <div className="message__avatar" aria-hidden="true"><Icon name={message.role === "user" ? "terminal" : "agent"} size={17} /></div>
      <div className="message__body">
        <header><strong>{message.actor}</strong><span className="live-label"><i />{message.complete ? "Finalizing" : "Streaming"}</span><time dateTime={message.occurredAt}>{formatTime(message.occurredAt)}</time></header>
        {message.thinking ? <details className="thinking-block"><summary>Thinking</summary><div>{message.thinking}</div></details> : null}
        {message.text ? <div className="message__content">{message.text}</div> : <div className="typing-dots" aria-label="Agent is working"><i /><i /><i /></div>}
      </div>
    </article>
  );
}

function ActivityTimeline({ items }: { items: ActivityItem[] }) {
  return (
    <ol className="activity-timeline">
      {items.slice().reverse().map((item) => (
        <li className={`activity-item activity-item--${item.status}`} key={`${item.id}:${item.seq}`}>
          <span className="activity-item__rail"><i /></span>
          <div className="activity-item__content">
            <header><span>{item.kind === "tool" ? <Icon name="terminal" size={14} /> : item.kind === "approval" ? <Icon name="approval" size={14} /> : <Icon name="activity" size={14} />}{item.title}</span><time dateTime={item.occurredAt}>{formatTime(item.occurredAt)}</time></header>
            {item.detail ? <p>{item.detail}</p> : null}
            <div className="activity-item__meta"><StatusChip status={item.status} compact /><span>{item.actor}</span></div>
            {item.input !== undefined || item.output !== undefined ? <details className="payload-details payload-details--activity"><summary>Details</summary><pre>{compactJson(item.output ?? item.input)}</pre></details> : null}
          </div>
        </li>
      ))}
    </ol>
  );
}

function deriveLiveMessages(events: AgentEvent[], persistedIds: Set<string>): LiveMessage[] {
  const messages = new Map<string, LiveMessage>();
  for (const event of events) {
    if (!event.type.startsWith("message.")) continue;
    const payload = normalizedPayload(event.payload);
    const runtimeEvent = isObjectRecord(event.payload.runtimeEvent) ? event.payload.runtimeEvent : undefined;
    const runtimeMessage = runtimeEvent && isObjectRecord(runtimeEvent.message) ? runtimeEvent.message : undefined;
    const actorKind = event.actor?.kind ?? event.actorKind ?? "main_agent";
    const roleValue = firstString(payload.role, runtimeMessage?.role);
    const role: LiveMessage["role"] = roleValue === "user" || actorKind === "user" ? "user" : roleValue === "system" || actorKind === "system" ? "system" : "assistant";
    const messageId = firstString(payload.messageId, runtimeMessage?.id, runtimeEvent?.messageId) ?? `${event.runId ?? "runtime"}:${role}:${actorKind}`;
    if (persistedIds.has(messageId)) continue;
    const current = messages.get(messageId) ?? {
      id: messageId,
      role,
      actor: actorKind === "main_agent" ? "Agent" : humanize(actorKind),
      text: "",
      thinking: "",
      occurredAt: event.occurredAt,
      complete: false
    };
    if (event.type === "message.delta") {
      const channel = firstString(payload.channel);
      const delta = firstString(payload.text) || extractMessageDelta(event.payload, runtimeEvent);
      if (channel === "thinking") current.thinking += delta;
      else current.text += delta;
    }
    if (event.type === "message.completed") current.complete = true;
    messages.set(messageId, current);
  }
  return Array.from(messages.values()).filter((message) => !message.complete);
}

function deriveActivity(events: AgentEvent[]): ActivityItem[] {
  const items = new Map<string, ActivityItem>();
  for (const event of events) {
    const payload = normalizedPayload(event.payload);
    const actor = event.actor?.kind ?? event.actorKind ?? "runtime";
    if (event.type.startsWith("tool.")) {
      const id = firstString(payload.toolUseId, payload.toolCallId, payload.id, event.eventId) ?? event.eventId;
      const existing = items.get(`tool:${id}`);
      const status = event.type === "tool.started" ? "running" : event.type === "tool.progress" ? "running" : event.type === "tool.failed" ? "failed" : "completed";
      items.set(`tool:${id}`, {
        id,
        kind: "tool",
        title: firstString(payload.tool, payload.toolName, payload.name) ?? "Tool call",
        detail: firstString(payload.message, payload.status),
        status,
        occurredAt: event.occurredAt,
        actor: humanize(actor),
        input: existing?.input ?? payload.input ?? payload.arguments,
        output: payload.result ?? payload.output ?? existing?.output,
        seq: event.seq
      });
      continue;
    }
    if (event.type.startsWith("subagent.")) {
      const id = firstString(event.actor?.id, event.actorId, payload.parentToolUseId, event.eventId) ?? event.eventId;
      items.set(`subagent:${id}`, {
        id,
        kind: "subagent",
        title: "Subagent",
        detail: firstString(payload.message, payload.source),
        status: event.type.endsWith("completed") ? (payload.failed ? "failed" : "completed") : "running",
        occurredAt: event.occurredAt,
        actor: humanize(actor),
        output: payload.result,
        seq: event.seq
      });
      continue;
    }
    if (event.type === "approval.requested" || event.type === "approval.resolved") {
      items.set(`approval:${event.eventId}`, {
        id: event.eventId,
        kind: "approval",
        title: event.type === "approval.requested" ? "Approval requested" : "Approval resolved",
        detail: firstString(payload.tool, payload.toolName, payload.decision),
        status: event.type === "approval.requested" ? "pending" : "completed",
        occurredAt: event.occurredAt,
        actor: humanize(actor),
        input: payload.input,
        seq: event.seq
      });
      continue;
    }
    if (event.type === "todo.updated") {
      items.set(`todo:${event.eventId}`, { id: event.eventId, kind: "todo", title: "Plan updated", status: "completed", occurredAt: event.occurredAt, actor: humanize(actor), output: payload.todos ?? payload, seq: event.seq });
      continue;
    }
    if (event.type === "notice") {
      items.set(`notice:${event.eventId}`, { id: event.eventId, kind: "notice", title: firstString(payload.message) ?? "Runtime notice", status: "neutral", occurredAt: event.occurredAt, actor: humanize(actor), seq: event.seq });
      continue;
    }
    if (event.type.startsWith("run.")) {
      items.set(`run:${event.eventId}`, { id: event.eventId, kind: "run", title: humanize(event.type), detail: firstString(payload.reason, payload.error), status: event.type === "run.started" ? "running" : event.type === "run.failed" ? "failed" : "completed", occurredAt: event.occurredAt, actor: humanize(actor), seq: event.seq });
    }
  }
  return Array.from(items.values()).sort((left, right) => left.seq - right.seq);
}

function normalizedPayload(payload: Record<string, unknown>): Record<string, unknown> {
  if (!isObjectRecord(payload.runtimeEvent)) return payload;
  return { ...payload.runtimeEvent, ...payload };
}

function extractMessageDelta(payload: Record<string, unknown>, runtimeEvent?: Record<string, unknown>): string {
  if (isObjectRecord(payload.delta)) {
    const text = extractText(payload.delta);
    if (text) return text;
  }
  if (runtimeEvent && isObjectRecord(runtimeEvent.assistantMessageEvent)) {
    const text = extractText(runtimeEvent.assistantMessageEvent);
    if (text) return text;
  }
  if (runtimeEvent && isObjectRecord(runtimeEvent.delta)) return extractText(runtimeEvent.delta);
  return "";
}

function firstString(...values: unknown[]): string | undefined {
  for (const value of values) {
    if (typeof value === "string" && value.length > 0) return value;
  }
  return undefined;
}
