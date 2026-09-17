import { useState, type FormEvent } from "react";
import { api, errorMessage } from "../api/client";
import type { Agent, RuntimeType } from "../api/types";
import { Icon } from "../components/Icon";
import { useToast } from "../components/Toast";
import { Button, EmptyState, ErrorState, InlineAlert, LoadingState, Modal, PageHeader, RefreshButton, StatusChip } from "../components/ui";
import { useResource } from "../hooks/useResource";
import { formatDate } from "../lib/format";
import { navigate, useLocation } from "../lib/router";

export function AgentsView() {
  const location = useLocation();
  const resource = useResource((signal) => api.listAgents(signal), []);
  const createOpen = new URLSearchParams(location.search).get("create") === "1";

  if (resource.loading) return <LoadingState label="Loading agent definitions" />;
  if (resource.error && !resource.data) return <ErrorState error={resource.error} retry={resource.reload} />;

  return (
    <div className="page">
      <PageHeader
        eyebrow="Definitions"
        title="Agents"
        description="Versioned runtime profiles that define how work is performed."
        actions={<><RefreshButton refreshing={resource.refreshing} onClick={resource.reload} /><Button variant="primary" icon="plus" onClick={() => navigate("/agents?create=1")}>New agent</Button></>}
      />
      {resource.error ? <InlineAlert tone="warning">The list could not be refreshed. Showing the last loaded data.</InlineAlert> : null}
      {resource.data?.length ? (
        <section className="card-grid" aria-label="Agent definitions">
          {resource.data.map((agent) => <AgentCard agent={agent} key={agent.id} />)}
        </section>
      ) : (
        <EmptyState icon="agent" title="No agents defined" description="Define the instructions and runtime for your first agent." action={<Button variant="primary" icon="plus" onClick={() => navigate("/agents?create=1")}>New agent</Button>} />
      )}
      <CreateAgentModal
        open={createOpen}
        onClose={() => navigate("/agents", { replace: true })}
        onCreated={(agent) => {
          resource.setData((current) => current ? [agent, ...current] : [agent]);
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

function CreateAgentModal({ open, onClose, onCreated }: { open: boolean; onClose: () => void; onCreated: (agent: Agent) => void }) {
  const { notify } = useToast();
  const [name, setName] = useState("");
  const [runtimeType, setRuntimeType] = useState<RuntimeType>("omp");
  const [model, setModel] = useState("");
  const [systemPrompt, setSystemPrompt] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string>();

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
    <Modal open={open} onClose={onClose} title="Define an agent" description="Create a versioned profile for a supported runtime." size="large">
      <form className="form" onSubmit={submit}>
        {error ? <InlineAlert>{error}</InlineAlert> : null}
        <div className="form-grid form-grid--two">
          <label className="field"><span>Name</span><input autoFocus required maxLength={120} value={name} onChange={(event) => setName(event.target.value)} /><small>A clear name operators will recognize.</small></label>
          <label className="field"><span>Runtime</span><select value={runtimeType} onChange={(event) => setRuntimeType(event.target.value as RuntimeType)}><option value="omp">OMP</option><option value="claude">Claude</option><option value="acp">ACP</option></select><small>The host must advertise this runtime.</small></label>
        </div>
        <label className="field"><span>Model <em>optional</em></span><input value={model} onChange={(event) => setModel(event.target.value)} /><small>Leave empty to use the runtime default.</small></label>
        <label className="field"><span>System instructions</span><textarea required rows={9} value={systemPrompt} onChange={(event) => setSystemPrompt(event.target.value)} /><small>Persistent behavior and operating boundaries for every box.</small></label>
        <div className="modal__actions"><Button type="button" onClick={onClose}>Cancel</Button><Button type="submit" variant="primary" icon="check" busy={submitting} disabled={!name.trim() || !systemPrompt.trim()}>Create agent</Button></div>
      </form>
    </Modal>
  );
}
