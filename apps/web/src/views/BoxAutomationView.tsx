import { useEffect, useState } from "react";
import { api, errorMessage } from "../api/client";
import type { Artifact, PendingWorkspaceDiff, SubagentInstance, TodoItem, WorkspaceDiff, WorkspaceDiffFile } from "../api/types";
import { Icon } from "../components/Icon";
import { useToast } from "../components/Toast";
import { Button, EmptyState, ErrorState, LoadingState, RefreshButton, StatusChip, cx } from "../components/ui";
import { useResource } from "../hooks/useResource";
import { compactJson, formatDate, humanize, relativeTime } from "../lib/format";
import { Link } from "../lib/router";

export type BoxAutomationPanel = "subagents" | "todos" | "artifacts" | "diff";

const PANELS: Array<{ id: BoxAutomationPanel; label: string; icon: "agent" | "todo" | "artifact" | "diff" }> = [
  { id: "subagents", label: "Subagents", icon: "agent" },
  { id: "todos", label: "Todos", icon: "todo" },
  { id: "artifacts", label: "Artifacts", icon: "artifact" },
  { id: "diff", label: "Workspace diff", icon: "diff" }
];

export function BoxAutomationView({ boxId, panel }: { boxId: string; panel: BoxAutomationPanel }) {
  const box = useResource((signal) => api.getBox(boxId, signal), [boxId]);
  if (box.loading) return <LoadingState label={`Loading ${panel}`} />;
  if (box.error && !box.data) return <ErrorState error={box.error} retry={box.reload} />;
  const snapshot = box.data!;
  return (
    <div className="page automation-page">
      <header className="automation-page__header">
        <div><div className="automation-page__crumbs"><Link to="/boxes">Boxes</Link><span>/</span><Link to={`/boxes/${boxId}`}>{snapshot.name}</Link><span>/</span><strong>{PANELS.find((item) => item.id === panel)?.label}</strong></div><h1>{snapshot.name}</h1><p>Durable output and operational state from this box.</p></div>
        <div><StatusChip status={snapshot.status} /><Link className="button button--secondary" to={`/boxes/${boxId}`}><Icon name="terminal" />Conversation</Link></div>
      </header>
      <nav className="box-panel-nav" aria-label="Box output">
        {PANELS.map((item) => <Link className={cx("box-panel-nav__item", panel === item.id && "box-panel-nav__item--active")} aria-current={panel === item.id ? "page" : undefined} to={`/boxes/${boxId}/${item.id}`} key={item.id}><Icon name={item.icon} /><span>{item.label}</span></Link>)}
      </nav>
      <section className="automation-surface">
        {panel === "subagents" ? <SubagentsPanel boxId={boxId} /> : null}
        {panel === "todos" ? <TodosPanel boxId={boxId} /> : null}
        {panel === "artifacts" ? <ArtifactsPanel boxId={boxId} /> : null}
        {panel === "diff" ? <DiffPanel boxId={boxId} /> : null}
      </section>
    </div>
  );
}

function SurfaceHeader({ eyebrow, title, description, count, refreshing, onRefresh }: { eyebrow: string; title: string; description: string; count?: number; refreshing?: boolean; onRefresh: () => void }) {
  return <div className="automation-surface__header"><div><p className="eyebrow">{eyebrow}</p><h2>{title}{count !== undefined ? <span>{count}</span> : null}</h2><p>{description}</p></div><RefreshButton refreshing={refreshing} onClick={onRefresh} /></div>;
}

function SubagentsPanel({ boxId }: { boxId: string }) {
  const resource = useResource((signal) => api.listBoxSubagents(boxId, signal), [boxId]);
  if (resource.loading) return <LoadingState label="Loading subagents" />;
  if (resource.error && !resource.data) return <ErrorState error={resource.error} retry={resource.reload} />;
  const agents = resource.data ?? [];
  const children = new Map<string | null, SubagentInstance[]>();
  const ids = new Set(agents.map((agent) => agent.externalAgentId));
  for (const agent of agents) {
    const parent = agent.parentExternalAgentId && ids.has(agent.parentExternalAgentId) ? agent.parentExternalAgentId : null;
    children.set(parent, [...(children.get(parent) ?? []), agent]);
  }
  for (const list of children.values()) list.sort((left, right) => new Date(left.startedAt).getTime() - new Date(right.startedAt).getTime());
  return (
    <>
      <SurfaceHeader eyebrow="Delegated work" title="Subagent tree" description="Parent-child execution, lifecycle state, and runtime metadata projected from agent events." count={agents.length} refreshing={resource.refreshing} onRefresh={resource.reload} />
      {agents.length ? <ol className="subagent-tree subagent-tree--root">{(children.get(null) ?? []).map((agent) => <SubagentNode agent={agent} children={children} key={agent.id} />)}</ol> : <EmptyState icon="agent" title="No subagents" description="Delegated agents will appear here when the runtime starts them." />}
    </>
  );
}

function SubagentNode({ agent, children }: { agent: SubagentInstance; children: Map<string | null, SubagentInstance[]> }) {
  const nested = children.get(agent.externalAgentId) ?? [];
  return (
    <li className="subagent-node">
      <article>
        <span className="subagent-node__branch" aria-hidden="true" />
        <span className="resource-icon resource-icon--agent"><Icon name="agent" /></span>
        <div className="subagent-node__main"><header><strong>{agent.label || agent.agentType || `Subagent ${agent.externalAgentId.slice(0, 8)}`}</strong><StatusChip status={agent.status} compact /></header><p>{agent.agentType ? humanize(agent.agentType) : "Runtime subagent"} · started {relativeTime(agent.startedAt)}{agent.finishedAt ? ` · finished ${relativeTime(agent.finishedAt)}` : ""}</p><code title={agent.externalAgentId}>{agent.externalAgentId}</code>{agent.metadata && Object.keys(agent.metadata).length ? <details className="payload-details"><summary>Runtime metadata</summary><pre>{compactJson(agent.metadata)}</pre></details> : null}</div>
      </article>
      {nested.length ? <ol>{nested.map((child) => <SubagentNode agent={child} children={children} key={child.id} />)}</ol> : null}
    </li>
  );
}

function TodosPanel({ boxId }: { boxId: string }) {
  const resource = useResource((signal) => api.listBoxTodos(boxId, signal), [boxId]);
  if (resource.loading) return <LoadingState label="Loading todos" />;
  if (resource.error && !resource.data) return <ErrorState error={resource.error} retry={resource.reload} />;
  const todos = [...(resource.data ?? [])].sort((left, right) => left.position - right.position || new Date(left.updatedAt).getTime() - new Date(right.updatedAt).getTime());
  const phases = new Map<string, TodoItem[]>();
  for (const todo of todos) {
    const phase = todo.phaseName || "Current plan";
    phases.set(phase, [...(phases.get(phase) ?? []), todo]);
  }
  const complete = todos.filter((todo) => todo.status === "completed").length;
  return (
    <>
      <SurfaceHeader eyebrow="Execution plan" title="Todos" description="The latest durable plan reported by the runtime, including blocked and abandoned work." count={todos.length} refreshing={resource.refreshing} onRefresh={resource.reload} />
      {todos.length ? <><div className="todo-progress"><div><span style={{ width: `${todos.length ? (complete / todos.length) * 100 : 0}%` }} /></div><small>{complete} of {todos.length} complete</small></div><div className="todo-phases">{Array.from(phases.entries()).map(([phase, items]) => <section className="todo-phase" key={phase}><header><h3>{phase}</h3><span>{items.length}</span></header><ol>{items.map((todo) => <li className={`todo-item todo-item--${todo.status}`} key={todo.id}><span className="todo-item__check">{todo.status === "completed" ? <Icon name="check" /> : todo.status === "in_progress" ? <span className="spinner spinner--small" /> : todo.position + 1}</span><div><header><strong>{todo.content}</strong><StatusChip status={todo.status} compact /></header>{todo.blockReason ? <p>{todo.blockReason}</p> : null}<small>Updated {relativeTime(todo.updatedAt)}</small></div></li>)}</ol></section>)}</div></> : <EmptyState icon="todo" title="No todo plan" description="Todo items will appear when the agent publishes or updates its plan." />}
    </>
  );
}

function ArtifactsPanel({ boxId }: { boxId: string }) {
  const resource = useResource((signal) => api.listBoxArtifacts(boxId, signal), [boxId]);
  if (resource.loading) return <LoadingState label="Loading artifacts" />;
  if (resource.error && !resource.data) return <ErrorState error={resource.error} retry={resource.reload} />;
  const artifacts = resource.data ?? [];
  return (
    <>
      <SurfaceHeader eyebrow="Automation output" title="Artifacts" description="Files, reports, images, archives, and logs captured from runs in this box." count={artifacts.length} refreshing={resource.refreshing} onRefresh={resource.reload} />
      {artifacts.length ? <div className="artifact-list">{artifacts.map((artifact) => <ArtifactRow artifact={artifact} boxId={boxId} key={artifact.id} />)}</div> : <EmptyState icon="artifact" title="No artifacts" description="Files emitted by the runtime will be available for download here." />}
    </>
  );
}

function ArtifactRow({ artifact, boxId }: { artifact: Artifact; boxId: string }) {
  const { notify } = useToast();
  const [downloading, setDownloading] = useState(false);
  const [downloadError, setDownloadError] = useState<string>();
  const available = artifact.status === "ready" && (!artifact.expiresAt || new Date(artifact.expiresAt).getTime() > Date.now());
  const startDownload = async () => {
    setDownloading(true);
    setDownloadError(undefined);
    try {
      const blob = await api.downloadArtifact(boxId, artifact.id);
      const url = URL.createObjectURL(blob);
      const anchor = document.createElement("a");
      anchor.href = url;
      anchor.download = artifact.name;
      document.body.append(anchor);
      anchor.click();
      anchor.remove();
      window.setTimeout(() => URL.revokeObjectURL(url), 1_000);
      notify(`${artifact.name} downloaded.`);
    } catch (requestError) {
      setDownloadError(errorMessage(requestError));
    } finally {
      setDownloading(false);
    }
  };
  return <article className="artifact-row"><span className="artifact-row__kind"><Icon name={artifact.kind === "image" ? "spark" : artifact.kind === "archive" ? "box" : "artifact"} /></span><div><header><strong>{artifact.name}</strong><StatusChip status={available ? "ready" : artifact.status === "deleted" ? "deleted" : "expired"} compact /></header><p>{humanize(artifact.kind)} · {artifact.mimeType || "Unknown type"} · {formatBytes(artifact.sizeBytes)}</p><small>Created {formatDate(artifact.createdAt)}{artifact.expiresAt ? ` · expires ${relativeTime(artifact.expiresAt)}` : ""}</small><code title={artifact.sha256}>SHA-256 {artifact.sha256.slice(0, 16)}…</code>{downloadError ? <span className="artifact-row__error" role="alert">{downloadError}</span> : null}</div>{available ? <Button icon="download" busy={downloading} onClick={() => void startDownload()}>Download</Button> : <span className="artifact-row__unavailable">Unavailable</span>}</article>;
}

function DiffPanel({ boxId }: { boxId: string }) {
  const resource = useResource((signal) => api.getBoxDiff(boxId, signal), [boxId]);
  const pendingDiff = isPendingDiff(resource.data) ? resource.data : undefined;
  const pending = pendingDiff !== undefined;
  useEffect(() => {
    if (!pending || resource.error) return;
    const timer = window.setTimeout(resource.reload, 2_000);
    return () => window.clearTimeout(timer);
  }, [pending, resource.error, resource.reload]);
  if (resource.loading) return <LoadingState label="Requesting workspace diff" />;
  if (resource.error && (!resource.data || pending)) return <ErrorState error={resource.error} retry={resource.reload} />;
  if (pendingDiff) return <div className="diff-pending"><span className="spinner" /><h2>Generating workspace diff</h2><p>The host is preparing a durable snapshot. This view refreshes automatically.</p><code>{pendingDiff.requestId}</code></div>;
  if (!resource.data) return <EmptyState icon="diff" title="No diff available" description="The workspace diff has not been generated." />;
  const diff = resource.data as WorkspaceDiff;
  const additions = diff.files.reduce((sum, file) => sum + file.additions, 0);
  const deletions = diff.files.reduce((sum, file) => sum + file.deletions, 0);
  return (
    <>
      <SurfaceHeader eyebrow="Workspace changes" title="Diff viewer" description={`${diff.baseRef || "Base"} → ${diff.headRef || "working tree"} · generated ${relativeTime(diff.generatedAt)}`} count={diff.files.length} refreshing={resource.refreshing} onRefresh={resource.reload} />
      {diff.files.length ? <><div className="diff-summary"><span>{diff.files.length} file{diff.files.length === 1 ? "" : "s"}</span><strong>+{additions}</strong><em>−{deletions}</em></div><div className="diff-files">{diff.files.map((file) => <DiffFile file={file} key={`${file.oldPath ?? ""}:${file.path}`} />)}</div></> : <EmptyState icon="diff" title="Workspace is clean" description="There are no changes from the selected base revision." />}
    </>
  );
}

function DiffFile({ file }: { file: WorkspaceDiffFile }) {
  return <details className="diff-file" open><summary><span className={`diff-file__status diff-file__status--${file.status}`}>{file.status.slice(0, 1).toUpperCase()}</span><code>{file.oldPath ? `${file.oldPath} → ` : ""}{file.path}</code><span><strong>+{file.additions}</strong><em>−{file.deletions}</em></span><Icon name="chevron" /></summary>{file.patch ? <pre className="diff-patch">{file.patch.split("\n").map((line, index) => <span className={line.startsWith("+") && !line.startsWith("+++") ? "diff-line--added" : line.startsWith("-") && !line.startsWith("---") ? "diff-line--deleted" : line.startsWith("@@") ? "diff-line--hunk" : undefined} key={`${index}:${line}`}><i>{index + 1}</i><code>{line || " "}</code></span>)}</pre> : <div className="diff-file__empty">Patch content is not available for this file.</div>}</details>;
}

function isPendingDiff(value: WorkspaceDiff | PendingWorkspaceDiff | undefined): value is PendingWorkspaceDiff {
  return Boolean(value && "status" in value && value.status === "pending");
}

function formatBytes(value: number): string {
  if (value < 1_024) return `${value} B`;
  if (value < 1_048_576) return `${(value / 1_024).toFixed(1)} KB`;
  if (value < 1_073_741_824) return `${(value / 1_048_576).toFixed(1)} MB`;
  return `${(value / 1_073_741_824).toFixed(1)} GB`;
}
