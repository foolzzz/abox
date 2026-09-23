import { useEffect, useMemo, useState, type FormEvent } from "react";
import { api, errorMessage } from "../api/client";
import type { Agent, Box, Host, Member, ResourceRole, RuntimeSession, Workspace } from "../api/types";
import { Icon } from "../components/Icon";
import { useToast } from "../components/Toast";
import { Button, EmptyState, ErrorState, InlineAlert, LoadingState, Modal, PageHeader, RefreshButton, StatusChip } from "../components/ui";
import { useResource } from "../hooks/useResource";
import { roleAtLeast, useAccess } from "../lib/access";
import { formatDate } from "../lib/format";
import { useI18n } from "../lib/i18n";
import { Link, navigate, useLocation } from "../lib/router";

interface BoxListData {
  boxes: Box[];
  agents: Agent[];
  hosts: Host[];
  workspaces: Workspace[];
  members: Member[];
}

export function BoxesView() {
  const { currentUser, meta } = useAccess();
  const { t } = useI18n();
  const location = useLocation();
  const resource = useResource<BoxListData>(async (signal) => {
    const [boxes, agents, hosts, workspaces, members] = await Promise.all([
      api.listBoxes(signal),
      api.listAgents(signal),
      api.listHosts(signal),
      api.listWorkspaces(signal),
      api.listMembers(signal)
    ]);
    return { boxes, agents, hosts, workspaces, members };
  }, []);
  const [query, setQuery] = useState("");
  const [status, setStatus] = useState("all");
  const canCreate = roleAtLeast(currentUser?.role, "user");
  const [ownerFilter, setOwnerFilter] = useState("me");
  const createOpen = canCreate && new URLSearchParams(location.search).get("create") === "1";
  const { notify } = useToast();
  const [deletingBox, setDeletingBox] = useState<Box>();
  const [deleteBusy, setDeleteBusy] = useState(false);
  const [deleteError, setDeleteError] = useState<string>();
  const [selectedBoxIds, setSelectedBoxIds] = useState<Set<string>>(() => new Set());
  const [batchDeletingBoxes, setBatchDeletingBoxes] = useState<Box[]>([]);
  const [batchDeleteBusy, setBatchDeleteBusy] = useState(false);
  const [batchDeleteErrors, setBatchDeleteErrors] = useState<string[]>([]);

  if (resource.loading) return <LoadingState label={t("boxes.loading")} />;
  if (resource.error && !resource.data) return <ErrorState error={resource.error} retry={resource.reload} />;

  const data = resource.data!;
  const memberNames = new Map(data.members.map((member) => [member.id, member.displayName || member.login]));
  const enabledAgents = data.agents.filter((agent) => meta?.enabledRuntimes.includes(agent.runtimeType) ?? agent.runtimeType === "omp");
  const agentNames = new Map(data.agents.map((agent) => [agent.id, agent.name]));
  const hostNames = new Map(data.hosts.map((host) => [host.id, host.name]));
  const workspaceNames = new Map(data.workspaces.map((workspace) => [workspace.id, workspace.name]));
  const normalizedQuery = query.trim().toLowerCase();
  const filtered = data.boxes.filter((box) => {
    const matchesStatus = status === "all" || box.status === status;
    const ownerID = ownerFilter === "me" ? currentUser?.id : ownerFilter;
    const matchesOwner = ownerFilter === "all" || box.ownerUserId === ownerID;
    const text = `${box.name} ${agentNames.get(box.agentId) ?? ""} ${hostNames.get(box.hostId) ?? ""} ${memberNames.get(box.ownerUserId ?? "") ?? ""}`.toLowerCase();
    return matchesStatus && matchesOwner && (!normalizedQuery || text.includes(normalizedQuery));
  });
  const canDeleteBox = (box: Box) => roleAtLeast(currentUser?.role, "admin") || box.ownerUserId === currentUser?.id;
  const selectableBoxes = filtered.filter(canDeleteBox);
  const selectedBoxes = data.boxes.filter((box) => selectedBoxIds.has(box.id) && canDeleteBox(box));
  const allVisibleSelected = selectableBoxes.length > 0 && selectableBoxes.every((box) => selectedBoxIds.has(box.id));
  const toggleBoxSelection = (boxId: string) => setSelectedBoxIds((current) => {
    const next = new Set(current);
    if (next.has(boxId)) next.delete(boxId);
    else next.add(boxId);
    return next;
  });
  const toggleVisibleSelection = () => setSelectedBoxIds((current) => {
    const next = new Set(current);
    for (const box of selectableBoxes) {
      if (allVisibleSelected) next.delete(box.id);
      else next.add(box.id);
    }
    return next;
  });
  const deleteBox = async () => {
    if (!deletingBox) return;
    setDeleteBusy(true);
    setDeleteError(undefined);
    try {
      await api.deleteBox(deletingBox.id);
      resource.setData((current) => current ? { ...current, boxes: current.boxes.filter((box) => box.id !== deletingBox.id) } : current);
      setSelectedBoxIds((current) => { const next = new Set(current); next.delete(deletingBox.id); return next; });
      notify(t("box.deleted"));
      setDeletingBox(undefined);
    } catch (requestError) {
      setDeleteError(errorMessage(requestError));
    } finally {
      setDeleteBusy(false);
    }
  };

  const openBatchDelete = () => {
    if (selectedBoxes.length === 0) return;
    setBatchDeleteErrors([]);
    setBatchDeletingBoxes(selectedBoxes);
  };
  const closeBatchDelete = () => {
    if (batchDeleteBusy) return;
    setBatchDeleteErrors([]);
    setBatchDeletingBoxes([]);
  };
  const batchDeleteBoxes = async () => {
    const targets = batchDeletingBoxes;
    if (targets.length === 0) return;
    setBatchDeleteBusy(true);
    setBatchDeleteErrors([]);
    const outcomes = await Promise.all(targets.map(async (box) => {
      try {
        await api.deleteBox(box.id);
        return { box, error: undefined };
      } catch (requestError) {
        return { box, error: errorMessage(requestError) };
      }
    }));
    const deleted = outcomes.filter((outcome) => !outcome.error).map((outcome) => outcome.box);
    const failed = outcomes.filter((outcome): outcome is { box: Box; error: string } => Boolean(outcome.error));
    const deletedIds = new Set(deleted.map((box) => box.id));
    if (deletedIds.size > 0) {
      resource.setData((current) => current ? { ...current, boxes: current.boxes.filter((box) => !deletedIds.has(box.id)) } : current);
      notify(t("boxes.bulkDeleted", { count: deletedIds.size }));
    }
    setSelectedBoxIds(new Set(failed.map((outcome) => outcome.box.id)));
    setBatchDeleteBusy(false);
    if (failed.length === 0) {
      setBatchDeletingBoxes([]);
      return;
    }
    setBatchDeletingBoxes(failed.map((outcome) => outcome.box));
    setBatchDeleteErrors(failed.map((outcome) => `${outcome.box.name}: ${outcome.error}`));
  };
  return (
    <div className="page">
      <PageHeader eyebrow={t("boxes.eyebrow")} title={t("boxes.title")} description={t("boxes.description")} actions={<><RefreshButton refreshing={resource.refreshing} onClick={resource.reload} />{canCreate ? <Button variant="primary" icon="plus" onClick={() => navigate("/boxes?create=1")}>{t("boxes.new")}</Button> : null}</>} />
      {resource.error ? <InlineAlert tone="warning">Box state could not be refreshed. Showing the last loaded snapshot.</InlineAlert> : null}
      {!canCreate ? <p className="permission-caption">An active user or administrator account is required to create a box.</p> : null}
      {data.boxes.length ? (
        <>
          <div className="list-toolbar">
            <label className="search-field"><Icon name="search" /><span className="sr-only">{t("boxes.search")}</span><input value={query} onChange={(event) => setQuery(event.target.value)} placeholder={t("boxes.search")} /></label>
            <label className="filter-field"><span className="sr-only">{t("boxes.ownerFilter")}</span><select value={ownerFilter} onChange={(event) => setOwnerFilter(event.target.value)} aria-label={t("boxes.ownerFilter")}><option value="me">{t("boxes.myBoxes")}</option><option value="all">{t("boxes.allOwners")}</option>{data.members.filter((member) => member.status === "active" && member.id !== currentUser?.id && data.boxes.some((box) => box.ownerUserId === member.id)).map((member) => <option value={member.id} key={member.id}>{member.displayName || member.login}</option>)}</select></label>
            <label className="filter-field"><span className="sr-only">Filter by status</span><select value={status} onChange={(event) => setStatus(event.target.value)}><option value="all">{t("boxes.allStatuses")}</option><option value="running">Running</option><option value="idle">Idle</option><option value="waiting_approval">Waiting approval</option><option value="hibernated">Hibernated</option><option value="error">Error</option><option value="terminated">Terminated</option></select></label>
            {selectableBoxes.length ? <label className="box-bulk-select"><input type="checkbox" checked={allVisibleSelected} onChange={toggleVisibleSelection} aria-label={t("boxes.selectAll")} /><span>{t("boxes.selected", { count: selectedBoxes.length })}</span></label> : null}
            {selectedBoxes.length ? <Button variant="danger" icon="trash" onClick={openBatchDelete}>{t("boxes.deleteSelected", { count: selectedBoxes.length })}</Button> : null}
          </div>
          {filtered.length ? (
            <section className="box-grid" aria-label="Agent boxes">
              {filtered.map((box) => (
                <article className={selectedBoxIds.has(box.id) ? "box-card box-card--selected" : "box-card"} key={box.id}>
                  <div className="box-card__top"><span className="resource-icon resource-icon--box"><Icon name="box" /></span><span className="box-card__controls">{canDeleteBox(box) ? <label className="box-card__select"><input type="checkbox" checked={selectedBoxIds.has(box.id)} onChange={() => toggleBoxSelection(box.id)} aria-label={t("boxes.select", { name: box.name })} /></label> : null}<StatusChip status={box.status} compact />{canDeleteBox(box) ? <button className="box-card__delete" type="button" title={t("box.delete")} aria-label={`Delete ${box.name}`} onClick={() => { setDeleteError(undefined); setDeletingBox(box); }}><Icon name="trash" size={16} /></button> : null}</span></div>
                  <Link className="box-card__main" to={`/boxes/${box.id}/agent-terminal`}>
                    <div><h2>{box.name}</h2><p>{agentNames.get(box.agentId) ?? "Unknown agent"}</p></div>
                    <dl><div><dt>Host</dt><dd>{hostNames.get(box.hostId) ?? "Unknown"}</dd></div><div><dt>Workspace</dt><dd>{workspaceNames.get(box.workspaceId) ?? "Unknown"}</dd></div></dl>
                    <footer><span>{box.runtimeType?.toUpperCase() ?? "Runtime"}</span><span>Updated {formatDate(box.updatedAt)}</span><Icon name="arrow" /></footer>
                  </Link>
                </article>
              ))}
            </section>
          ) : <EmptyState icon="search" title="No boxes match" description="Adjust the search or status filter to see more boxes." />}
        </>
      ) : <EmptyState icon="box" title="No boxes yet" description={canCreate ? "Create a box to pair an agent with a ready workspace." : "An operator must create a box and share it with you."} action={canCreate ? <Button variant="primary" icon="plus" onClick={() => navigate("/boxes?create=1")}>New box</Button> : undefined} />}
      <CreateBoxModal
        open={createOpen}
        agents={enabledAgents}
        hosts={data.hosts}
        workspaces={data.workspaces}
        currentUserId={currentUser?.id}
        organizationAdmin={roleAtLeast(currentUser?.role, "admin")}
        onWorkspaceCreated={(workspace) => resource.setData((current) => current ? { ...current, workspaces: [workspace, ...current.workspaces.filter((item) => item.id !== workspace.id)] } : current)}
        onClose={() => navigate("/boxes", { replace: true })}
        onCreated={(box) => {
          resource.setData((current) => current ? { ...current, boxes: [box, ...current.boxes] } : current);
          navigate(`/boxes/${box.id}/agent-terminal`, { replace: true });
        }}
      />
      <Modal open={Boolean(deletingBox)} title={t("box.deleteTitle", { name: deletingBox?.name ?? "Agent Box" })} description={t("box.deleteDescription")} onClose={() => !deleteBusy && setDeletingBox(undefined)} size="small">
        <div className="confirm-dialog">{deleteError ? <InlineAlert>{deleteError}</InlineAlert> : null}<InlineAlert tone="warning">{t("box.deleteWarning")}</InlineAlert><div className="modal__actions"><Button disabled={deleteBusy} onClick={() => setDeletingBox(undefined)}>{t("common.cancel")}</Button><Button variant="danger" icon="trash" busy={deleteBusy} onClick={() => void deleteBox()}>{t("box.deleteConfirm")}</Button></div></div>
      </Modal>
      <Modal open={batchDeletingBoxes.length > 0} title={t("boxes.bulkDeleteTitle", { count: batchDeletingBoxes.length })} description={t("boxes.bulkDeleteDescription")} onClose={closeBatchDelete} size="small">
        <div className="confirm-dialog">
          {batchDeleteErrors.map((message, index) => <InlineAlert key={`${index}:${message}`}>{message}</InlineAlert>)}
          <InlineAlert tone="warning">{t("boxes.bulkDeleteWarning")}</InlineAlert>
          <ul className="box-bulk-delete-list">{batchDeletingBoxes.map((box) => <li key={box.id}>{box.name}</li>)}</ul>
          <div className="modal__actions"><Button disabled={batchDeleteBusy} onClick={closeBatchDelete}>{t("common.cancel")}</Button><Button variant="danger" icon="trash" busy={batchDeleteBusy} onClick={() => void batchDeleteBoxes()}>{t("boxes.bulkDeleteConfirm", { count: batchDeletingBoxes.length })}</Button></div>
        </div>
      </Modal>
    </div>
  );
}

function CreateBoxModal({
  open,
  agents,
  hosts,
  workspaces,
  currentUserId,
  organizationAdmin,
  onWorkspaceCreated,
  onClose,
  onCreated
}: {
  open: boolean;
  agents: Agent[];
  hosts: Host[];
  workspaces: Workspace[];
  currentUserId?: string;
  organizationAdmin: boolean;
  onWorkspaceCreated: (workspace: Workspace) => void;
  onClose: () => void;
  onCreated: (box: Box) => void;
}) {
  const { notify } = useToast();
  const { t } = useI18n();
  const [name, setName] = useState("");
  const [agentId, setAgentId] = useState("");
  const [hostId, setHostId] = useState("");
  const [workspaceId, setWorkspaceId] = useState("");
  const [projectPath, setProjectPath] = useState("");
  const [placementMode, setPlacementMode] = useState<"path" | "workspace">(organizationAdmin ? "path" : "workspace");
  const [runtimeSessionMode, setRuntimeSessionMode] = useState<"new" | "resume" | "attach">("new");
  const [runtimeSessionRef, setRuntimeSessionRef] = useState("");
  const [runtimeSessions, setRuntimeSessions] = useState<RuntimeSession[]>([]);
  const [sessionSearch, setSessionSearch] = useState("");
  const [sessionDiscoveryBusy, setSessionDiscoveryBusy] = useState(false);
  const [sessionDiscoveryError, setSessionDiscoveryError] = useState<string>();
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string>();
  const [workspaceAccess, setWorkspaceAccess] = useState<ResourceRole>();
  const [checkingWorkspaceAccess, setCheckingWorkspaceAccess] = useState(false);
  const [workspaceAccessError, setWorkspaceAccessError] = useState<string>();

  useEffect(() => {
    if (!open) {
      setError(undefined);
      setPlacementMode(organizationAdmin ? "path" : "workspace");
      setRuntimeSessionMode("new");
      setRuntimeSessionRef("");
      setSessionSearch("");
      setRuntimeSessions([]);
      setSessionDiscoveryError(undefined);
    }
  }, [open, organizationAdmin]);

  const selectedAgentId = agentId || (agents.length === 1 ? agents[0]?.id ?? "" : "");
  const selectedAgent = agents.find((agent) => agent.id === selectedAgentId);
  const compatibleHosts = useMemo(
    () => hosts.filter((host) => host.status === "online" && (!selectedAgent || host.runtimes.includes(selectedAgent.runtimeType))),
    [hosts, selectedAgent]
  );
  const selectedHostId = hostId || (compatibleHosts.length === 1 ? compatibleHosts[0]?.id ?? "" : "");
  const readyWorkspaces = workspaces.filter((workspace) => workspace.hostId === selectedHostId && workspace.status === "ready");
  const selectedWorkspaceId = workspaceId || (readyWorkspaces.length === 1 ? readyWorkspaces[0]?.id ?? "" : "");
  const selectedHost = hosts.find((host) => host.id === selectedHostId);
  const selectedWorkspace = workspaces.find((workspace) => workspace.id === selectedWorkspaceId);
  const normalizedProjectPath = projectPath.trim().replace(/\/+$/, "") || "/";

  useEffect(() => {
    if (selectedAgent?.runtimeType === "claude") return;
    setRuntimeSessionMode("new");
    setRuntimeSessionRef("");
  }, [selectedAgent?.runtimeType]);

  useEffect(() => {
    if (hostId && !compatibleHosts.some((host) => host.id === hostId)) {
      setHostId("");
      setWorkspaceId("");
    }
  }, [agentId, compatibleHosts, hostId]);

  useEffect(() => {
    setWorkspaceAccessError(undefined);
    if (!open || placementMode === "path" || !selectedWorkspaceId) {
      setWorkspaceAccess(placementMode === "path" && organizationAdmin ? "owner" : undefined);
      setCheckingWorkspaceAccess(false);
      return;
    }
    if (organizationAdmin) {
      setWorkspaceAccess("owner");
      setCheckingWorkspaceAccess(false);
      return;
    }
    const controller = new AbortController();
    setCheckingWorkspaceAccess(true);
    api.getWorkspaceAcl(selectedWorkspaceId, controller.signal).then(
      (entries) => {
        if (controller.signal.aborted) return;
        setWorkspaceAccess(entries.find((entry) => entry.userId === currentUserId)?.role ?? "viewer");
        setCheckingWorkspaceAccess(false);
      },
      (requestError: unknown) => {
        if (controller.signal.aborted) return;
        setWorkspaceAccess(undefined);
        setWorkspaceAccessError(errorMessage(requestError));
        setCheckingWorkspaceAccess(false);
      }
    );
    return () => controller.abort();
  }, [currentUserId, open, organizationAdmin, placementMode, selectedWorkspaceId]);

  useEffect(() => {
    setSessionDiscoveryError(undefined);
    if (!open || !organizationAdmin || runtimeSessionMode === "new" || !selectedHostId || selectedAgent?.runtimeType !== "claude") {
      setRuntimeSessions([]);
      setSessionDiscoveryBusy(false);
      return;
    }
    const controller = new AbortController();
    setSessionDiscoveryBusy(true);
    api.listRuntimeSessions(selectedHostId, "claude", controller.signal).then(
      (sessions) => {
        if (!controller.signal.aborted) {
          setRuntimeSessions(sessions);
          setSessionDiscoveryBusy(false);
        }
      },
      (requestError: unknown) => {
        if (!controller.signal.aborted) {
          setSessionDiscoveryError(errorMessage(requestError));
          setRuntimeSessions([]);
          setSessionDiscoveryBusy(false);
        }
      }
    );
    return () => controller.abort();
  }, [open, organizationAdmin, runtimeSessionMode, selectedHostId, selectedAgent?.runtimeType]);

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    if (placementMode === "workspace" && !organizationAdmin && workspaceAccess !== "owner" && workspaceAccess !== "operator") return;
    setSubmitting(true);
    setError(undefined);
    try {
      let targetWorkspaceId = selectedWorkspaceId;
      if (placementMode === "path") {
        const existing = workspaces.find((workspace) => workspace.hostId === selectedHostId && (workspace.path.replace(/\/+$/, "") || "/") === normalizedProjectPath);
        let workspace = existing;
        if (!workspace || workspace.status === "error") {
          const pathSegments = normalizedProjectPath.split("/").filter(Boolean);
          workspace = await api.createWorkspace({
            hostId: selectedHostId,
            name: pathSegments.at(-1) || "Project",
            path: normalizedProjectPath
          });
        }
        if (workspace.status === "provisioning") workspace = await waitForWorkspaceReady(workspace.id);
        onWorkspaceCreated(workspace);
        targetWorkspaceId = workspace.id;
      }
      const box = await api.createBox({ name: name.trim(), agentId: selectedAgentId, hostId: selectedHostId, workspaceId: targetWorkspaceId, runtimeSessionMode, runtimeSessionRef: runtimeSessionMode === "resume" ? runtimeSessionRef.trim() : undefined });
      notify(`${box.name} was created.`);
      setName("");
      setAgentId("");
      setHostId("");
      setWorkspaceId("");
      setProjectPath("");
      setRuntimeSessionMode("new");
      setRuntimeSessionRef("");
      onCreated(box);
    } catch (requestError) {
      setError(errorMessage(requestError));
    } finally {
      setSubmitting(false);
    }
  };

  const canUseSelectedWorkspace = organizationAdmin || workspaceAccess === "owner" || workspaceAccess === "operator";
  const directoryReady = placementMode === "path"
    ? organizationAdmin && selectedHostId !== "" && projectPath.trim().startsWith("/")
    : selectedHostId !== "" && selectedWorkspaceId !== "" && canUseSelectedWorkspace;
  const runtimeSessionReady = runtimeSessionMode === "new" || runtimeSessionRef.trim() !== "";
  const normalizedSessionSearch = sessionSearch.trim().toLowerCase();
  const filteredRuntimeSessions = runtimeSessions.filter((session) => !normalizedSessionSearch || `${session.name} ${session.workspace ?? ""} ${session.sessionRef}`.toLowerCase().includes(normalizedSessionSearch));
  const chooseRuntimeSession = (session: RuntimeSession) => {
    setRuntimeSessionRef(session.sessionRef);
    setRuntimeSessionMode(session.running ? "attach" : "resume");
    if (!session.workspace) return;
    const matchingWorkspace = workspaces.find((workspace) => workspace.hostId === selectedHostId && workspace.status === "ready" && (workspace.path.replace(/\/+$/, "") || "/") === (session.workspace?.replace(/\/+$/, "") || "/"));
    if (organizationAdmin) {
      setPlacementMode("path");
      setProjectPath(session.workspace);
    } else if (matchingWorkspace) {
      setPlacementMode("workspace");
      setWorkspaceId(matchingWorkspace.id);
    }
  };

  return (
    <Modal open={open} onClose={onClose} title={t("boxes.createTitle")} description={t("boxes.createDescription")} size="large">
      <form className="form" onSubmit={submit}>
        {error ? <InlineAlert>{error}</InlineAlert> : null}
        {agents.length === 0 ? <InlineAlert tone="warning">Create an agent definition before creating a box.</InlineAlert> : null}
        <label className="field"><span>{t("boxes.boxName")}</span><input autoFocus required maxLength={120} value={name} onChange={(event) => setName(event.target.value)} /><small>{t("boxes.boxNameHint")}</small></label>
        <label className="field"><span>{t("boxes.stepAgent")}</span><select required value={selectedAgentId} onChange={(event) => { setAgentId(event.target.value); setHostId(""); setWorkspaceId(""); }}><option value="">{t("boxes.selectAgent")}</option>{agents.map((agent) => <option value={agent.id} key={agent.id}>{agent.name} · {agent.runtimeType.toUpperCase()}</option>)}</select></label>
        {selectedAgent ? <div className="selection-summary"><Icon name="agent" /><div><strong>{selectedAgent.name}</strong><small>{selectedAgent.model || "Runtime default model"} · version {selectedAgent.version}</small></div><StatusChip status={selectedAgent.runtimeType} compact /></div> : null}
        {selectedAgent && compatibleHosts.length === 0 ? <InlineAlert tone="warning">No online host supports {selectedAgent.runtimeType.toUpperCase()}.</InlineAlert> : null}
        <label className="field"><span>{t("boxes.host")}</span><select required value={selectedHostId} onChange={(event) => { setHostId(event.target.value); setWorkspaceId(""); }} disabled={!selectedAgentId}><option value="">{t("boxes.selectHost")}</option>{compatibleHosts.map((host) => <option value={host.id} key={host.id}>{host.name} · {host.systemHostname || [host.os, host.arch].filter(Boolean).join("/")} · {host.runtimes.join(", ")}</option>)}</select></label>
        {selectedAgent?.runtimeType === "claude" ? <fieldset className="segmented-field"><legend>{t("boxes.sessionSource")}</legend><label><input type="radio" name="runtimeSessionMode" checked={runtimeSessionMode === "new"} onChange={() => { setRuntimeSessionMode("new"); setRuntimeSessionRef(""); }} />{t("boxes.sessionNew")}</label><label><input type="radio" name="runtimeSessionMode" checked={runtimeSessionMode === "resume"} onChange={() => setRuntimeSessionMode("resume")} />{t("boxes.sessionResume")}</label><label><input type="radio" name="runtimeSessionMode" checked={runtimeSessionMode === "attach"} onChange={() => setRuntimeSessionMode("attach")} />{t("boxes.sessionAttach")}</label></fieldset> : null}
        {selectedAgent?.runtimeType === "claude" && runtimeSessionMode !== "new" ? <><label className="field"><span>{t("boxes.sessionRef")}</span><input className="mono" required maxLength={256} value={runtimeSessionRef} onChange={(event) => setRuntimeSessionRef(event.target.value)} placeholder="xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx" aria-label={t("boxes.sessionRef")} /><small>{t("boxes.sessionRefHint")}</small></label>{organizationAdmin ? <div className="session-picker"><label className="field"><span>{t("boxes.sessionSearch")}</span><input value={sessionSearch} onChange={(event) => setSessionSearch(event.target.value)} placeholder={t("boxes.sessionSearchPlaceholder")} /></label>{sessionDiscoveryBusy ? <span className="spinner spinner--small" /> : null}{sessionDiscoveryError ? <InlineAlert tone="warning">{t("boxes.sessionDiscoveryFallback")}: {sessionDiscoveryError}</InlineAlert> : null}{!sessionDiscoveryBusy && !sessionDiscoveryError && filteredRuntimeSessions.length === 0 ? <small>{t("boxes.sessionNone")}</small> : null}<div className="session-picker__list">{filteredRuntimeSessions.map((session) => <button type="button" className={runtimeSessionRef === session.sessionRef ? "session-picker__item session-picker__item--selected" : "session-picker__item"} onClick={() => chooseRuntimeSession(session)} key={session.sessionRef} aria-label={`${t("boxes.sessionChoose")} ${session.name}`}><span><strong>{session.name}</strong><small className="mono">{session.workspace || session.sessionRef}</small></span><StatusChip status={session.running ? "running" : "stopped"} compact /></button>)}</div></div> : null}<InlineAlert tone="warning">{t(runtimeSessionMode === "attach" ? "boxes.sessionAttachWarning" : "boxes.sessionResumeWarning")}</InlineAlert></> : null}
        {organizationAdmin ? <fieldset className="segmented-field"><legend>{t("boxes.stepDirectory")}</legend><label><input type="radio" name="placementMode" checked={placementMode === "path"} onChange={() => setPlacementMode("path")} />{t("boxes.pathMode")}</label><label><input type="radio" name="placementMode" checked={placementMode === "workspace"} onChange={() => setPlacementMode("workspace")} />{t("boxes.workspaceMode")}</label></fieldset> : null}
        {placementMode === "path" ? (
          <label className="field"><span>{t("boxes.projectPath")}</span><input className="mono" required value={projectPath} onChange={(event) => setProjectPath(event.target.value)} placeholder="/Users/name/projects/my-project" /><small>{t("boxes.projectPathHint")}</small></label>
        ) : (
          <label className="field"><span>{t("boxes.registeredWorkspace")}</span><select required value={selectedWorkspaceId} onChange={(event) => setWorkspaceId(event.target.value)} disabled={!selectedHostId}><option value="">{t("boxes.selectWorkspace")}</option>{readyWorkspaces.map((workspace) => <option value={workspace.id} key={workspace.id}>{workspace.name} · {workspace.path}</option>)}</select>{selectedHostId && readyWorkspaces.length === 0 ? <small className="field__error">{t("boxes.noWorkspace")}</small> : null}</label>
        )}
        {selectedHost && placementMode === "path" && projectPath.trim() ? <div className="selection-summary"><Icon name="workspace" /><div><strong>{normalizedProjectPath}</strong><small>{selectedHost.name} · {selectedHost.systemHostname || "hostname unavailable"}</small></div><StatusChip status="validation" compact /></div> : null}
        {selectedHost && placementMode === "workspace" && selectedWorkspace ? <div className="selection-summary"><Icon name="workspace" /><div><strong>{selectedWorkspace.name}</strong><small>{selectedHost.name} · {selectedHost.systemHostname || "hostname unavailable"} · {selectedWorkspace.path}</small></div>{checkingWorkspaceAccess ? <span className="spinner spinner--small" /> : <StatusChip status={workspaceAccess ?? selectedWorkspace.status} compact />}</div> : null}
        {workspaceAccessError ? <InlineAlert>{workspaceAccessError}</InlineAlert> : null}
        {placementMode === "workspace" && selectedWorkspace && !checkingWorkspaceAccess && !workspaceAccessError && !canUseSelectedWorkspace ? <InlineAlert tone="warning">{t("boxes.viewerWorkspace")}</InlineAlert> : null}
        <div className="modal__actions">
          <Button type="button" onClick={onClose}>{t("boxes.cancel")}</Button>
          <Button type="submit" variant="primary" icon="spark" busy={submitting || checkingWorkspaceAccess} disabled={!name.trim() || !selectedAgentId || !directoryReady || !runtimeSessionReady}>{submitting && placementMode === "path" ? t("boxes.validatingDirectory") : t("boxes.create")}</Button>
        </div>
      </form>
    </Modal>
  );
}

async function waitForWorkspaceReady(workspaceId: string): Promise<Workspace> {
  const deadline = Date.now() + 15_000;
  let workspace = await api.getWorkspace(workspaceId);
  while (workspace.status === "provisioning" && Date.now() < deadline) {
    const { promise, resolve } = Promise.withResolvers<void>();
    window.setTimeout(resolve, 250);
    await promise;
    workspace = await api.getWorkspace(workspaceId);
  }
  if (workspace.status !== "ready") {
    throw new Error(workspace.status === "error" ? "The host rejected this project directory." : "Project directory validation timed out.");
  }
  return workspace;
}
