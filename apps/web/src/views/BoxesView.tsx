import { useEffect, useMemo, useState, type FormEvent } from "react";
import { api, errorMessage } from "../api/client";
import type { Agent, Box, Host, ResourceRole, Workspace } from "../api/types";
import { Icon } from "../components/Icon";
import { useToast } from "../components/Toast";
import { Button, EmptyState, ErrorState, InlineAlert, LoadingState, Modal, PageHeader, RefreshButton, StatusChip, cx } from "../components/ui";
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
}

export function BoxesView() {
  const { currentUser, meta } = useAccess();
  const { t } = useI18n();
  const location = useLocation();
  const resource = useResource<BoxListData>(async (signal) => {
    const [boxes, agents, hosts, workspaces] = await Promise.all([
      api.listBoxes(signal),
      api.listAgents(signal),
      api.listHosts(signal),
      api.listWorkspaces(signal)
    ]);
    return { boxes, agents, hosts, workspaces };
  }, []);
  const [query, setQuery] = useState("");
  const [status, setStatus] = useState("all");
  const canCreate = roleAtLeast(currentUser?.role, "operator");
  const createOpen = canCreate && new URLSearchParams(location.search).get("create") === "1";

  if (resource.loading) return <LoadingState label={t("boxes.loading")} />;
  if (resource.error && !resource.data) return <ErrorState error={resource.error} retry={resource.reload} />;

  const data = resource.data!;
  const enabledAgents = data.agents.filter((agent) => meta?.enabledRuntimes.includes(agent.runtimeType) ?? agent.runtimeType === "omp");
  const agentNames = new Map(data.agents.map((agent) => [agent.id, agent.name]));
  const hostNames = new Map(data.hosts.map((host) => [host.id, host.name]));
  const workspaceNames = new Map(data.workspaces.map((workspace) => [workspace.id, workspace.name]));
  const normalizedQuery = query.trim().toLowerCase();
  const filtered = data.boxes.filter((box) => {
    const matchesStatus = status === "all" || box.status === status;
    const text = `${box.name} ${agentNames.get(box.agentId) ?? ""} ${hostNames.get(box.hostId) ?? ""}`.toLowerCase();
    return matchesStatus && (!normalizedQuery || text.includes(normalizedQuery));
  });

  return (
    <div className="page">
      <PageHeader eyebrow={t("boxes.eyebrow")} title={t("boxes.title")} description={t("boxes.description")} actions={<><RefreshButton refreshing={resource.refreshing} onClick={resource.reload} />{canCreate ? <Button variant="primary" icon="plus" onClick={() => navigate("/boxes?create=1")}>{t("boxes.new")}</Button> : null}</>} />
      {resource.error ? <InlineAlert tone="warning">Box state could not be refreshed. Showing the last loaded snapshot.</InlineAlert> : null}
      {!canCreate ? <p className="permission-caption">Your {currentUser?.role ?? "viewer"} role can open shared boxes in read-only mode. Operator access is required to create one.</p> : null}
      {data.boxes.length ? (
        <>
          <div className="list-toolbar">
            <label className="search-field"><Icon name="search" /><span className="sr-only">{t("boxes.search")}</span><input value={query} onChange={(event) => setQuery(event.target.value)} placeholder={t("boxes.search")} /></label>
            <label className="filter-field"><span className="sr-only">Filter by status</span><select value={status} onChange={(event) => setStatus(event.target.value)}><option value="all">{t("boxes.allStatuses")}</option><option value="running">Running</option><option value="idle">Idle</option><option value="waiting_approval">Waiting approval</option><option value="hibernated">Hibernated</option><option value="error">Error</option><option value="terminated">Terminated</option></select></label>
          </div>
          {filtered.length ? (
            <section className="box-grid" aria-label="Agent boxes">
              {filtered.map((box) => (
                <Link className="box-card" to={`/boxes/${box.id}/agent-terminal`} key={box.id}>
                  <div className="box-card__top"><span className="resource-icon resource-icon--box"><Icon name="box" /></span><StatusChip status={box.status} compact /></div>
                  <div><h2>{box.name}</h2><p>{agentNames.get(box.agentId) ?? "Unknown agent"}</p></div>
                  <dl><div><dt>Host</dt><dd>{hostNames.get(box.hostId) ?? "Unknown"}</dd></div><div><dt>Workspace</dt><dd>{workspaceNames.get(box.workspaceId) ?? "Unknown"}</dd></div></dl>
                  <footer><span>{box.runtimeType?.toUpperCase() ?? "Runtime"}</span><span>Updated {formatDate(box.updatedAt)}</span><Icon name="arrow" /></footer>
                </Link>
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
  const [step, setStep] = useState(1);
  const [name, setName] = useState("");
  const [agentId, setAgentId] = useState("");
  const [hostId, setHostId] = useState("");
  const [workspaceId, setWorkspaceId] = useState("");
  const [projectPath, setProjectPath] = useState("");
  const [placementMode, setPlacementMode] = useState<"path" | "workspace">(organizationAdmin ? "path" : "workspace");
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string>();
  const [workspaceAccess, setWorkspaceAccess] = useState<ResourceRole>();
  const [checkingWorkspaceAccess, setCheckingWorkspaceAccess] = useState(false);
  const [workspaceAccessError, setWorkspaceAccessError] = useState<string>();

  useEffect(() => {
    if (!open) {
      setStep(1);
      setError(undefined);
      setPlacementMode(organizationAdmin ? "path" : "workspace");
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

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    if (step === 1) {
      setStep(2);
      return;
    }
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
      const box = await api.createBox({ name: name.trim(), agentId: selectedAgentId, hostId: selectedHostId, workspaceId: targetWorkspaceId });
      notify(`${box.name} was created.`);
      setName("");
      setAgentId("");
      setHostId("");
      setWorkspaceId("");
      setProjectPath("");
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

  return (
    <Modal open={open} onClose={onClose} title={t("boxes.createTitle")} description={t("boxes.createDescription")} size="large">
      <div className="stepper" aria-label="Creation progress"><span className={cx("stepper__step", step >= 1 && "stepper__step--active")}><b>1</b>{t("boxes.stepAgent")}</span><span className="stepper__line" /><span className={cx("stepper__step", step >= 2 && "stepper__step--active")}><b>2</b>{t("boxes.stepDirectory")}</span></div>
      <form className="form" onSubmit={submit}>
        {error ? <InlineAlert>{error}</InlineAlert> : null}
        {step === 1 ? (
          <>
            {agents.length === 0 ? <InlineAlert tone="warning">Create an agent definition before creating a box.</InlineAlert> : null}
            <label className="field"><span>{t("boxes.boxName")}</span><input autoFocus required maxLength={120} value={name} onChange={(event) => setName(event.target.value)} /><small>{t("boxes.boxNameHint")}</small></label>
            <label className="field"><span>{t("boxes.stepAgent")}</span><select required value={selectedAgentId} onChange={(event) => { setAgentId(event.target.value); setHostId(""); setWorkspaceId(""); }}><option value="">{t("boxes.selectAgent")}</option>{agents.map((agent) => <option value={agent.id} key={agent.id}>{agent.name} · {agent.runtimeType.toUpperCase()}</option>)}</select></label>
            {selectedAgent ? <div className="selection-summary"><Icon name="agent" /><div><strong>{selectedAgent.name}</strong><small>{selectedAgent.model || "Runtime default model"} · version {selectedAgent.version}</small></div><StatusChip status={selectedAgent.runtimeType} compact /></div> : null}
          </>
        ) : (
          <>
            {compatibleHosts.length === 0 ? <InlineAlert tone="warning">No online host supports {selectedAgent?.runtimeType.toUpperCase() ?? "this runtime"}.</InlineAlert> : null}
            <label className="field"><span>{t("boxes.host")}</span><select autoFocus required value={selectedHostId} onChange={(event) => { setHostId(event.target.value); setWorkspaceId(""); }}><option value="">{t("boxes.selectHost")}</option>{compatibleHosts.map((host) => <option value={host.id} key={host.id}>{host.name} · {host.systemHostname || [host.os, host.arch].filter(Boolean).join("/")} · {host.runtimes.join(", ")}</option>)}</select></label>
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
          </>
        )}
        <div className="modal__actions">
          {step === 1 ? <Button type="button" onClick={onClose}>{t("boxes.cancel")}</Button> : <Button type="button" icon="chevron" onClick={() => setStep(1)}>{t("boxes.back")}</Button>}
          <Button type="submit" variant="primary" icon={step === 1 ? "arrow" : "spark"} busy={submitting || checkingWorkspaceAccess} disabled={step === 1 ? !name.trim() || !selectedAgentId : !directoryReady}>{step === 1 ? t("boxes.chooseDirectory") : submitting && placementMode === "path" ? t("boxes.validatingDirectory") : t("boxes.create")}</Button>
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
