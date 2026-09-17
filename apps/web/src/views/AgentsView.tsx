import { useState, type FormEvent } from "react";
import { api, errorMessage } from "../api/client";
import type { Agent, Host, RuntimeType } from "../api/types";
import { Icon } from "../components/Icon";
import { useToast } from "../components/Toast";
import { Button, EmptyState, ErrorState, InlineAlert, LoadingState, Modal, PageHeader, RefreshButton, StatusChip, cx } from "../components/ui";
import { useResource } from "../hooks/useResource";
import { roleAtLeast, useAccess } from "../lib/access";
import { formatDate } from "../lib/format";
import { navigate, useLocation } from "../lib/router";

const RUNTIME_TYPES: RuntimeType[] = ["omp", "claude", "acp"];
const RUNTIME_LABELS: Record<RuntimeType, string> = { omp: "OMP", claude: "Claude", acp: "ACP" };

interface AgentListData {
  agents: Agent[];
  hosts: Host[];
}

export function AgentsView() {
  const { currentUser } = useAccess();
  const location = useLocation();
  const resource = useResource<AgentListData>(async (signal) => {
    const [agents, hosts] = await Promise.all([api.listAgents(signal), api.listHosts(signal)]);
    return { agents, hosts };
  }, []);
  const canCreate = roleAtLeast(currentUser?.role, "admin");
  const createOpen = canCreate && new URLSearchParams(location.search).get("create") === "1";

  if (resource.loading) return <LoadingState label="Loading agent definitions" />;
  if (resource.error && !resource.data) return <ErrorState error={resource.error} retry={resource.reload} />;

  const data = resource.data!;
  return (
    <div className="page">
      <PageHeader
        eyebrow="Definitions"
        title="Agents"
        description="Versioned runtime profiles for OMP, Claude, and other advertised runtimes. Every runtime uses the same box conversation UI."
        actions={<><RefreshButton refreshing={resource.refreshing} onClick={resource.reload} />{canCreate ? <Button variant="primary" icon="plus" onClick={() => navigate("/agents?create=1")}>New agent</Button> : null}</>}
      />
      {resource.error ? <InlineAlert tone="warning">The list could not be refreshed. Showing the last loaded data.</InlineAlert> : null}
      {!canCreate ? <p className="permission-caption">Your {currentUser?.role ?? "viewer"} role can use existing agents. An organization admin is required to define one.</p> : null}
      {data.agents.length ? (
        <section className="card-grid" aria-label="Agent definitions">
          {data.agents.map((agent) => <AgentCard agent={agent} key={agent.id} />)}
        </section>
      ) : (
        <EmptyState icon="agent" title="No agents defined" description={canCreate ? "Define the instructions and runtime for your first agent." : "An organization admin must define an agent before boxes can be created."} action={canCreate ? <Button variant="primary" icon="plus" onClick={() => navigate("/agents?create=1")}>New agent</Button> : undefined} />
      )}
      <CreateAgentModal
        open={createOpen}
        hosts={data.hosts}
        onClose={() => navigate("/agents", { replace: true })}
        onCreated={(agent) => {
          resource.setData((current) => current ? { ...current, agents: [agent, ...current.agents] } : { agents: [agent], hosts: data.hosts });
          navigate("/agents", { replace: true });
        }}
      />
    </div>
  );
}

function AgentCard({ agent }: { agent: Agent }) {
  return (
    <article className="resource-card">
      <div className="resource-card__top">
        <span className="resource-icon resource-icon--agent"><Icon name="agent" /></span>
        <StatusChip status={agent.runtimeType} compact />
      </div>
      <div className="resource-card__body">
        <h2>{agent.name}</h2>
        <p className="resource-card__subtitle">{agent.model || "Runtime default model"}</p>
        <p className="resource-card__description">{agent.systemPrompt}</p>
      </div>
      <dl className="resource-card__facts">
        <div><dt>Version</dt><dd>v{agent.version}</dd></div>
        <div><dt>Updated</dt><dd>{formatDate(agent.updatedAt ?? agent.createdAt)}</dd></div>
      </dl>
    </article>
  );
}

function CreateAgentModal({ open, hosts, onClose, onCreated }: { open: boolean; hosts: Host[]; onClose: () => void; onCreated: (agent: Agent) => void }) {
  const { notify } = useToast();
  const [name, setName] = useState("");
  const [runtimeType, setRuntimeType] = useState<RuntimeType>("omp");
  const [model, setModel] = useState("");
  const [systemPrompt, setSystemPrompt] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string>();
  const runtimeHosts: Record<RuntimeType, number> = { omp: 0, claude: 0, acp: 0 };
  for (const host of hosts) {
    if (host.status !== "online") continue;
    for (const runtime of host.runtimes) {
      if (runtime in runtimeHosts) runtimeHosts[runtime as RuntimeType] += 1;
    }
  }
  const selectedRuntimeHosts = runtimeHosts[runtimeType];

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    setSubmitting(true);
    setError(undefined);
    try {
      const agent = await api.createAgent({ name: name.trim(), runtimeType, model: model.trim() || null, systemPrompt: systemPrompt.trim() });
      notify(`${agent.name} is ready to use.`);
      setName("");
      setModel("");
      setSystemPrompt("");
      setRuntimeType("omp");
      onCreated(agent);
    } catch (requestError) {
      setError(errorMessage(requestError));
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Modal open={open} onClose={onClose} title="Define an agent" description="Choose a runtime, model, and persistent instructions." size="large">
      <form className="form" onSubmit={submit}>
        {error ? <InlineAlert>{error}</InlineAlert> : null}
        <label className="field"><span>Name</span><input autoFocus required maxLength={120} value={name} onChange={(event) => setName(event.target.value)} /><small>A clear name operators will recognize.</small></label>
        <fieldset className="runtime-picker">
          <legend>Runtime</legend>
          {RUNTIME_TYPES.map((runtime) => {
            const availableHosts = runtimeHosts[runtime];
            return <label className={cx("runtime-option", runtimeType === runtime && "runtime-option--selected")} key={runtime}><input type="radio" name="runtime" value={runtime} checked={runtimeType === runtime} onChange={() => setRuntimeType(runtime)} /><span className="resource-icon resource-icon--agent"><Icon name="terminal" /></span><span><strong>{RUNTIME_LABELS[runtime]}</strong><small>{availableHosts ? `${availableHosts} online host${availableHosts === 1 ? "" : "s"} available` : "No online host currently advertises this runtime"}</small></span><StatusChip status={availableHosts ? "available" : "unavailable"} compact /></label>;
          })}
        </fieldset>
        {selectedRuntimeHosts === 0 ? <InlineAlert tone="warning">You can save this definition, but a box cannot use {RUNTIME_LABELS[runtimeType]} until an online host advertises it.</InlineAlert> : null}
        <label className="field"><span>Model <em>optional</em></span><input value={model} onChange={(event) => setModel(event.target.value)} /><small>Leave empty to use the {RUNTIME_LABELS[runtimeType]} default.</small></label>
        <label className="field"><span>System instructions</span><textarea required rows={9} value={systemPrompt} onChange={(event) => setSystemPrompt(event.target.value)} /><small>Persistent behavior and operating boundaries for every box.</small></label>
        <div className="modal__actions"><Button type="button" onClick={onClose}>Cancel</Button><Button type="submit" variant="primary" icon="check" busy={submitting} disabled={!name.trim() || !systemPrompt.trim()}>Create agent</Button></div>
      </form>
    </Modal>
  );
}
