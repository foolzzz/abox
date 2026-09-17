import { useEffect, useMemo, useState, type FormEvent } from "react";
import { api, errorMessage } from "../api/client";
import type { Agent, Box, Host, Workspace } from "../api/types";
import { Icon } from "../components/Icon";
import { useToast } from "../components/Toast";
import { Button, EmptyState, ErrorState, InlineAlert, LoadingState, Modal, PageHeader, RefreshButton, StatusChip, cx } from "../components/ui";
import { useResource } from "../hooks/useResource";
import { formatDate } from "../lib/format";
import { Link, navigate, useLocation } from "../lib/router";

interface BoxListData {
  boxes: Box[];
  agents: Agent[];
  hosts: Host[];
  workspaces: Workspace[];
}

export function BoxesView() {
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
  const createOpen = new URLSearchParams(location.search).get("create") === "1";

  if (resource.loading) return <LoadingState label="Loading boxes" />;
  if (resource.error && !resource.data) return <ErrorState error={resource.error} retry={resource.reload} />;

  const data = resource.data!;
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
      <PageHeader eyebrow="Live sessions" title="Boxes" description="Durable agent sessions bound to a host and workspace." actions={<><RefreshButton refreshing={resource.refreshing} onClick={resource.reload} /><Button variant="primary" icon="plus" onClick={() => navigate("/boxes?create=1")}>New box</Button></>} />
      {resource.error ? <InlineAlert tone="warning">Box state could not be refreshed. Showing the last loaded snapshot.</InlineAlert> : null}
      {data.boxes.length ? (
        <>
          <div className="list-toolbar">
            <label className="search-field"><Icon name="search" /><span className="sr-only">Search boxes</span><input value={query} onChange={(event) => setQuery(event.target.value)} placeholder="Search boxes" /></label>
            <label className="filter-field"><span className="sr-only">Filter by status</span><select value={status} onChange={(event) => setStatus(event.target.value)}><option value="all">All statuses</option><option value="running">Running</option><option value="idle">Idle</option><option value="waiting_approval">Waiting approval</option><option value="hibernated">Hibernated</option><option value="error">Error</option><option value="terminated">Terminated</option></select></label>
          </div>
          {filtered.length ? (
            <section className="box-grid" aria-label="Agent boxes">
              {filtered.map((box) => (
                <Link className="box-card" to={`/boxes/${box.id}`} key={box.id}>
                  <div className="box-card__top"><span className="resource-icon resource-icon--box"><Icon name="box" /></span><StatusChip status={box.status} compact /></div>
                  <div><h2>{box.name}</h2><p>{agentNames.get(box.agentId) ?? "Unknown agent"}</p></div>
                  <dl><div><dt>Host</dt><dd>{hostNames.get(box.hostId) ?? "Unknown"}</dd></div><div><dt>Workspace</dt><dd>{workspaceNames.get(box.workspaceId) ?? "Unknown"}</dd></div></dl>
                  <footer><span>{box.runtimeType?.toUpperCase() ?? "Runtime"}</span><span>Updated {formatDate(box.updatedAt)}</span><Icon name="arrow" /></footer>
                </Link>
              ))}
            </section>
          ) : <EmptyState icon="search" title="No boxes match" description="Adjust the search or status filter to see more boxes." />}
        </>
      ) : <EmptyState icon="box" title="No boxes yet" description="Create a box to pair an agent with a ready workspace." action={<Button variant="primary" icon="plus" onClick={() => navigate("/boxes?create=1")}>New box</Button>} />}
      <CreateBoxModal
        open={createOpen}
        agents={data.agents}
        hosts={data.hosts}
        workspaces={data.workspaces}
        onClose={() => navigate("/boxes", { replace: true })}
        onCreated={(box) => {
          resource.setData((current) => current ? { ...current, boxes: [box, ...current.boxes] } : current);
          navigate(`/boxes/${box.id}`, { replace: true });
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
  onClose,
  onCreated
}: {
  open: boolean;
  agents: Agent[];
  hosts: Host[];
  workspaces: Workspace[];
  onClose: () => void;
  onCreated: (box: Box) => void;
}) {
  const { notify } = useToast();
  const [step, setStep] = useState(1);
  const [name, setName] = useState("");
  const [agentId, setAgentId] = useState("");
  const [hostId, setHostId] = useState("");
  const [workspaceId, setWorkspaceId] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string>();

  useEffect(() => {
    if (!open) {
      setStep(1);
      setError(undefined);
    }
  }, [open]);

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

  useEffect(() => {
    if (hostId && !compatibleHosts.some((host) => host.id === hostId)) {
      setHostId("");
      setWorkspaceId("");
    }
  }, [agentId, compatibleHosts, hostId]);

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    if (step === 1) {
      setStep(2);
      return;
    }
    setSubmitting(true);
    setError(undefined);
    try {
      const box = await api.createBox({ name: name.trim(), agentId: selectedAgentId, hostId: selectedHostId, workspaceId: selectedWorkspaceId });
      notify(`${box.name} was created.`);
      setName("");
      setAgentId("");
      setHostId("");
      setWorkspaceId("");
      onCreated(box);
    } catch (requestError) {
      setError(errorMessage(requestError));
    } finally {
      setSubmitting(false);
    }
  };

  const hasPlacement = compatibleHosts.length > 0 && readyWorkspaces.length > 0;

  return (
    <Modal open={open} onClose={onClose} title="Create a box" description="Launch a durable agent session in two steps." size="large">
      <div className="stepper" aria-label="Creation progress"><span className={cx("stepper__step", step >= 1 && "stepper__step--active")}><b>1</b>Agent</span><span className="stepper__line" /><span className={cx("stepper__step", step >= 2 && "stepper__step--active")}><b>2</b>Placement</span></div>
      <form className="form" onSubmit={submit}>
        {error ? <InlineAlert>{error}</InlineAlert> : null}
        {step === 1 ? (
          <>
            {agents.length === 0 ? <InlineAlert tone="warning">Create an agent definition before creating a box.</InlineAlert> : null}
            <label className="field"><span>Box name</span><input autoFocus required maxLength={120} value={name} onChange={(event) => setName(event.target.value)} /><small>Use a name that describes the ongoing work.</small></label>
            <label className="field"><span>Agent</span><select required value={selectedAgentId} onChange={(event) => { setAgentId(event.target.value); setHostId(""); setWorkspaceId(""); }}><option value="">Select an agent</option>{agents.map((agent) => <option value={agent.id} key={agent.id}>{agent.name} · {agent.runtimeType.toUpperCase()}</option>)}</select></label>
            {selectedAgent ? <div className="selection-summary"><Icon name="agent" /><div><strong>{selectedAgent.name}</strong><small>{selectedAgent.model || "Runtime default model"} · version {selectedAgent.version}</small></div><StatusChip status={selectedAgent.runtimeType} compact /></div> : null}
          </>
        ) : (
          <>
            {compatibleHosts.length === 0 ? <InlineAlert tone="warning">No online host supports {selectedAgent?.runtimeType.toUpperCase() ?? "this runtime"}.</InlineAlert> : null}
            <label className="field"><span>Host</span><select autoFocus required value={selectedHostId} onChange={(event) => { setHostId(event.target.value); setWorkspaceId(""); }}><option value="">Select a compatible host</option>{compatibleHosts.map((host) => <option value={host.id} key={host.id}>{host.name} · {host.runtimes.join(", ")}</option>)}</select></label>
            <label className="field"><span>Workspace</span><select required value={selectedWorkspaceId} onChange={(event) => setWorkspaceId(event.target.value)} disabled={!selectedHostId}><option value="">Select a ready workspace</option>{readyWorkspaces.map((workspace) => <option value={workspace.id} key={workspace.id}>{workspace.name} · {workspace.path}</option>)}</select>{selectedHostId && readyWorkspaces.length === 0 ? <small className="field__error">This host has no ready workspaces.</small> : null}</label>
            {selectedHost && selectedWorkspace ? <div className="selection-summary"><Icon name="workspace" /><div><strong>{selectedWorkspace.name}</strong><small>{selectedHost.name} · {selectedWorkspace.path}</small></div><StatusChip status={selectedWorkspace.status} compact /></div> : null}
          </>
        )}
        <div className="modal__actions">
          {step === 1 ? <Button type="button" onClick={onClose}>Cancel</Button> : <Button type="button" icon="chevron" onClick={() => setStep(1)}>Back</Button>}
          <Button type="submit" variant="primary" icon={step === 1 ? "arrow" : "spark"} busy={submitting} disabled={step === 1 ? !name.trim() || !selectedAgentId : !selectedHostId || !selectedWorkspaceId || !hasPlacement}>{step === 1 ? "Choose placement" : "Create box"}</Button>
        </div>
      </form>
    </Modal>
  );
}
