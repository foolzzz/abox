import { useEffect, useState, type FormEvent } from "react";
import { api, errorMessage } from "../api/client";
import type { Agent, Host, RuntimeType } from "../api/types";
import { Icon } from "../components/Icon";
import { useToast } from "../components/Toast";
import { Button, EmptyState, ErrorState, InlineAlert, LoadingState, Modal, PageHeader, RefreshButton, StatusChip, cx } from "../components/ui";
import { useResource } from "../hooks/useResource";
import { roleAtLeast, useAccess } from "../lib/access";
import { formatDate } from "../lib/format";
import { useI18n } from "../lib/i18n";
import { navigate, useLocation } from "../lib/router";

const DEFAULT_RUNTIME_TYPES: RuntimeType[] = ["omp"];
const RUNTIME_LABELS: Record<RuntimeType, string> = { omp: "OMP", codex: "Codex", claude: "Claude", acp: "ACP" };

interface AgentListData {
  agents: Agent[];
  hosts: Host[];
}

export function AgentsView() {
  const { currentUser, meta } = useAccess();
  const { t } = useI18n();
  const location = useLocation();
  const resource = useResource<AgentListData>(async (signal) => {
    const [agents, hosts] = await Promise.all([api.listAgents(signal), api.listHosts(signal)]);
    return { agents, hosts };
  }, []);
  const canCreate = roleAtLeast(currentUser?.role, "admin");
  const createOpen = canCreate && new URLSearchParams(location.search).get("create") === "1";
  const runtimeTypes = meta ? meta.enabledRuntimes : DEFAULT_RUNTIME_TYPES;

  if (resource.loading) return <LoadingState label={t("agents.load")} />;
  if (resource.error && !resource.data) return <ErrorState error={resource.error} retry={resource.reload} />;

  const data = resource.data!;
  const visibleAgents = data.agents.filter((agent) => runtimeTypes.includes(agent.runtimeType));
  return (
    <div className="page">
      <PageHeader
        eyebrow={t("agents.eyebrow")}
        title={t("agents.title")}
        description={t(runtimeTypes.length === 1 && runtimeTypes[0] === "omp" ? "agents.ompDescription" : "agents.multiDescription")}
        actions={<><RefreshButton refreshing={resource.refreshing} onClick={resource.reload} />{canCreate ? <Button variant="primary" icon="plus" onClick={() => navigate("/agents?create=1")}>{t("agents.new")}</Button> : null}</>}
      />
      {resource.error ? <InlineAlert tone="warning">{t("agents.stale")}</InlineAlert> : null}
      {!canCreate ? <p className="permission-caption">{t("agents.readOnly", { role: currentUser?.role ?? "viewer" })}</p> : null}
      {visibleAgents.length ? (
        <section className="card-grid" aria-label="Agent definitions">
          {visibleAgents.map((agent) => <AgentCard agent={agent} key={agent.id} />)}
        </section>
      ) : (
        <EmptyState icon="agent" title={t("agents.empty")} description={t(canCreate ? "agents.emptyAdmin" : "agents.emptyViewer")} action={canCreate ? <Button variant="primary" icon="plus" onClick={() => navigate("/agents?create=1")}>{t("agents.new")}</Button> : undefined} />
      )}
      <CreateAgentModal
        open={createOpen}
        hosts={data.hosts}
        runtimeTypes={runtimeTypes}
        runtimeModels={meta?.runtimeModels ?? {}}
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
  const { t } = useI18n();
  return (
    <article className="resource-card">
      <div className="resource-card__top">
        <span className="resource-icon resource-icon--agent"><Icon name="agent" /></span>
        <StatusChip status={agent.runtimeType} compact />
      </div>
      <div className="resource-card__body">
        <h2>{agent.name}</h2>
        <p className="resource-card__subtitle">{agent.model || t("agents.defaultModel")}</p>
        <p className="resource-card__description">{agent.systemPrompt}</p>
      </div>
      <dl className="resource-card__facts">
        <div><dt>{t("agents.version")}</dt><dd>v{agent.version}</dd></div>
        <div><dt>{t("agents.updated")}</dt><dd>{formatDate(agent.updatedAt ?? agent.createdAt)}</dd></div>
      </dl>
    </article>
  );
}

function CreateAgentModal({ open, hosts, runtimeTypes, runtimeModels, onClose, onCreated }: { open: boolean; hosts: Host[]; runtimeTypes: RuntimeType[]; runtimeModels: Partial<Record<RuntimeType, string[]>>; onClose: () => void; onCreated: (agent: Agent) => void }) {
  const { notify } = useToast();
  const { t } = useI18n();
  const [name, setName] = useState("");
  const [runtimeType, setRuntimeType] = useState<RuntimeType>(runtimeTypes[0] ?? "omp");
  const [model, setModel] = useState("");
  const [systemPrompt, setSystemPrompt] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string>();
  const runtimeHosts: Record<RuntimeType, number> = { omp: 0, codex: 0, claude: 0, acp: 0 };
  for (const host of hosts) {
    if (host.status !== "online") continue;
    for (const runtime of host.runtimes) {
      if (runtime in runtimeHosts) runtimeHosts[runtime as RuntimeType] += 1;
    }
  }
  const selectedRuntimeHosts = runtimeHosts[runtimeType];
  const modelSuggestions = runtimeModels[runtimeType] ?? [];

  useEffect(() => {
    if (!runtimeTypes.includes(runtimeType) && runtimeTypes[0]) setRuntimeType(runtimeTypes[0]);
  }, [runtimeType, runtimeTypes]);

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    setSubmitting(true);
    setError(undefined);
    try {
      const agent = await api.createAgent({ name: name.trim(), runtimeType, model: model.trim() || null, systemPrompt: systemPrompt.trim() });
      notify(t("agents.created", { name: agent.name }));
      setName("");
      setModel("");
      setSystemPrompt("");
      setRuntimeType(runtimeTypes[0] ?? "omp");
      onCreated(agent);
    } catch (requestError) {
      setError(errorMessage(requestError));
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Modal open={open} onClose={onClose} title={t("agents.define")} description={t("agents.defineDescription")} size="large">
      <form className="form" onSubmit={submit}>
        {error ? <InlineAlert>{error}</InlineAlert> : null}
        <label className="field"><span>{t("agents.name")}</span><input autoFocus required maxLength={120} value={name} onChange={(event) => setName(event.target.value)} /><small>{t("agents.nameHint")}</small></label>
        <fieldset className="runtime-picker">
          <legend>{t("agents.runtime")}</legend>
          {runtimeTypes.map((runtime) => {
            const availableHosts = runtimeHosts[runtime];
            return <label className={cx("runtime-option", runtimeType === runtime && "runtime-option--selected")} key={runtime}><input type="radio" name="runtime" value={runtime} checked={runtimeType === runtime} onChange={() => setRuntimeType(runtime)} /><span className="resource-icon resource-icon--agent"><Icon name="terminal" /></span><span><strong>{RUNTIME_LABELS[runtime]}</strong><small>{availableHosts ? t("agents.onlineHosts", { count: availableHosts }) : t("agents.noHost")}</small></span><StatusChip status={availableHosts ? "available" : "unavailable"} compact /></label>;
          })}
        </fieldset>
        {selectedRuntimeHosts === 0 ? <InlineAlert tone="warning">{t("agents.unavailable", { runtime: RUNTIME_LABELS[runtimeType] })}</InlineAlert> : null}
        <label className="field"><span>{t("agents.model")} <em>{t("common.optional")}</em></span><input list={`agent-model-suggestions-${runtimeType}`} value={model} onChange={(event) => setModel(event.target.value)} /><datalist id={`agent-model-suggestions-${runtimeType}`}>{modelSuggestions.map((suggestion) => <option key={suggestion} value={suggestion} />)}</datalist><small>{t("agents.modelHint", { runtime: RUNTIME_LABELS[runtimeType] })}</small></label>
        <label className="field"><span>{t("agents.instructions")}</span><textarea required rows={9} value={systemPrompt} onChange={(event) => setSystemPrompt(event.target.value)} /><small>{t("agents.instructionsHint")}</small></label>
        <div className="modal__actions"><Button type="button" onClick={onClose}>{t("common.cancel")}</Button><Button type="submit" variant="primary" icon="check" busy={submitting} disabled={!name.trim() || !systemPrompt.trim()}>{t("agents.new")}</Button></div>
      </form>
    </Modal>
  );
}
