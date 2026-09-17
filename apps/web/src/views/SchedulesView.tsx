import { useEffect, useMemo, useState, type FormEvent } from "react";
import { api, errorMessage } from "../api/client";
import type { Agent, ConcurrencyPolicy, CreateScheduleRequest, Host, Schedule, ScheduleExecution, Workspace } from "../api/types";
import { Icon } from "../components/Icon";
import { useToast } from "../components/Toast";
import { Button, EmptyState, ErrorState, InlineAlert, LoadingState, Modal, PageHeader, RefreshButton, StatusChip, cx } from "../components/ui";
import { useResource } from "../hooks/useResource";
import { roleAtLeast, useAccess } from "../lib/access";
import { formatDate, relativeTime } from "../lib/format";
import { Link } from "../lib/router";

interface ScheduleData {
  schedules: Schedule[];
  agents: Agent[];
  hosts: Host[];
  workspaces: Workspace[];
}

export function SchedulesView() {
  const { currentUser, meta } = useAccess();
  const { notify } = useToast();
  const canManage = roleAtLeast(currentUser?.role, "operator");
  const resource = useResource<ScheduleData>(async (signal) => {
    const [schedules, agents, hosts, workspaces] = await Promise.all([
      api.listSchedules(signal),
      api.listAgents(signal),
      api.listHosts(signal),
      api.listWorkspaces(signal)
    ]);
    return { schedules, agents, hosts, workspaces };
  }, []);
  const [editor, setEditor] = useState<Schedule | "new">();
  const [selectedId, setSelectedId] = useState<string>();
  const [deleting, setDeleting] = useState<Schedule>();
  const [deleteBusy, setDeleteBusy] = useState(false);
  const [deleteError, setDeleteError] = useState<string>();
  const [filter, setFilter] = useState("all");

  if (resource.loading) return <LoadingState label="Loading schedules" />;
  if (resource.error && !resource.data) return <ErrorState error={resource.error} retry={resource.reload} />;

  const data = resource.data!;
  const enabledAgents = data.agents.filter((agent) => meta?.enabledRuntimes.includes(agent.runtimeType) ?? agent.runtimeType === "omp");
  const agentNames = new Map(data.agents.map((agent) => [agent.id, agent.name]));
  const hostNames = new Map(data.hosts.map((host) => [host.id, host.name]));
  const workspaceNames = new Map(data.workspaces.map((workspace) => [workspace.id, workspace.name]));
  const schedules = data.schedules.filter((schedule) => schedule.status !== "deleted" && (filter === "all" || schedule.status === filter));
  const selected = schedules.find((schedule) => schedule.id === selectedId) ?? schedules[0];

  const remove = async () => {
    if (!deleting) return;
    setDeleteBusy(true);
    setDeleteError(undefined);
    try {
      await api.deleteSchedule(deleting.id);
      resource.setData((current) => current ? { ...current, schedules: current.schedules.filter((item) => item.id !== deleting.id) } : current);
      if (selectedId === deleting.id) setSelectedId(undefined);
      notify(`${deleting.name} was deleted.`);
      setDeleting(undefined);
    } catch (requestError) {
      setDeleteError(errorMessage(requestError));
    } finally {
      setDeleteBusy(false);
    }
  };

  return (
    <div className="page">
      <PageHeader
        eyebrow="Unattended automation"
        title="Schedules"
        description="Run agent prompts on a cron schedule with explicit placement, concurrency control, and a durable execution history."
        actions={<><RefreshButton refreshing={resource.refreshing} onClick={resource.reload} />{canManage ? <Button variant="primary" icon="plus" onClick={() => setEditor("new")}>New schedule</Button> : null}</>}
      />
      {resource.error ? <InlineAlert tone="warning">Schedules could not be refreshed. Showing the last loaded state.</InlineAlert> : null}
      {!canManage ? <p className="permission-caption">Your {currentUser?.role ?? "viewer"} role can inspect automation history. Operator access is required to create or change schedules.</p> : null}
      {data.schedules.some((schedule) => schedule.status !== "deleted") ? (
        <>
          <div className="list-toolbar schedule-toolbar">
            <label className="filter-field"><span className="sr-only">Filter schedules</span><select value={filter} onChange={(event) => setFilter(event.target.value)}><option value="all">All schedules</option><option value="active">Active</option><option value="paused">Paused</option></select></label>
            <span>{schedules.length} automation{schedules.length === 1 ? "" : "s"}</span>
          </div>
          <div className="automation-layout">
            <section className="schedule-list" aria-label="Schedules">
              {schedules.length ? schedules.map((schedule) => (
                <article className={cx("schedule-card", selected?.id === schedule.id && "schedule-card--selected")} key={schedule.id}>
                  <button className="schedule-card__open" type="button" onClick={() => setSelectedId(schedule.id)} aria-pressed={selected?.id === schedule.id}>
                    <span className="resource-icon resource-icon--schedule"><Icon name="schedule" /></span>
                    <span className="schedule-card__body"><span><strong>{schedule.name}</strong><StatusChip status={schedule.status} compact /></span><small>{agentNames.get(schedule.agentId) ?? "Unknown agent"} · {workspaceNames.get(schedule.workspaceId) ?? "Unknown workspace"}</small><code>{schedule.cronExpression} · {schedule.timezone}</code></span>
                    <Icon name="chevron" />
                  </button>
                  <div className="schedule-card__facts"><span><small>Next run</small><strong>{schedule.nextRunAt ? relativeTime(schedule.nextRunAt) : "Paused"}</strong></span><span><small>Last run</small><strong>{schedule.lastRunAt ? relativeTime(schedule.lastRunAt) : "Never"}</strong></span><span><small>Host</small><strong>{hostNames.get(schedule.hostId) ?? "Unknown"}</strong></span></div>
                  {canManage ? <footer><Button variant="ghost" icon="edit" onClick={() => setEditor(schedule)}>Edit</Button><Button variant="ghost" icon="trash" onClick={() => setDeleting(schedule)}>Delete</Button></footer> : null}
                </article>
              )) : <EmptyState icon="schedule" title="No schedules match" description="Change the filter to see other automation." />}
            </section>
            <aside className="execution-panel panel">
              {selected ? <ExecutionHistory schedule={selected} /> : <EmptyState icon="activity" title="Select a schedule" description="Choose an automation to inspect its unattended run history." />}
            </aside>
          </div>
        </>
      ) : <EmptyState icon="schedule" title="No schedules yet" description={canManage ? "Create an unattended run with a prompt, placement, and concurrency policy." : "An operator can create scheduled automation for the organization."} action={canManage ? <Button variant="primary" icon="plus" onClick={() => setEditor("new")}>New schedule</Button> : undefined} />}

      <ScheduleEditor
        open={Boolean(editor)}
        schedule={editor === "new" ? undefined : editor}
        agents={enabledAgents}
        hosts={data.hosts}
        workspaces={data.workspaces}
        onClose={() => setEditor(undefined)}
        onSaved={(schedule) => {
          resource.setData((current) => current ? { ...current, schedules: current.schedules.some((item) => item.id === schedule.id) ? current.schedules.map((item) => item.id === schedule.id ? schedule : item) : [schedule, ...current.schedules] } : current);
          setSelectedId(schedule.id);
          setEditor(undefined);
        }}
      />
      <Modal open={Boolean(deleting)} onClose={() => { if (!deleteBusy) setDeleting(undefined); }} title={`Delete ${deleting?.name ?? "schedule"}?`} description="This stops future unattended runs. Existing boxes and execution history remain available." size="small">
        <div className="confirm-dialog">{deleteError ? <InlineAlert>{deleteError}</InlineAlert> : null}<p>This action cannot be undone.</p><div className="modal__actions"><Button onClick={() => setDeleting(undefined)} disabled={deleteBusy}>Cancel</Button><Button variant="danger" icon="trash" busy={deleteBusy} onClick={() => void remove()}>Delete schedule</Button></div></div>
      </Modal>
    </div>
  );
}

function ExecutionHistory({ schedule }: { schedule: Schedule }) {
  const resource = useResource((signal) => api.listScheduleExecutions(schedule.id, signal), [schedule.id]);
  if (resource.loading) return <LoadingState label="Loading run history" />;
  if (resource.error && !resource.data) return <ErrorState error={resource.error} retry={resource.reload} />;
  const executions = [...(resource.data ?? [])].sort((left, right) => new Date(right.scheduledFor).getTime() - new Date(left.scheduledFor).getTime());
  return (
    <>
      <div className="panel__header execution-panel__header"><div><p className="eyebrow">Unattended runs</p><h2>{schedule.name}</h2></div><RefreshButton refreshing={resource.refreshing} onClick={resource.reload} /></div>
      {resource.error ? <InlineAlert tone="warning">History may be stale.</InlineAlert> : null}
      {executions.length ? <ol className="execution-list">{executions.map((execution) => <ExecutionRow execution={execution} key={execution.id} />)}</ol> : <EmptyState icon="clock" title="No runs yet" description={schedule.status === "active" ? `The next run is ${schedule.nextRunAt ? relativeTime(schedule.nextRunAt) : "being calculated"}.` : "Resume this schedule to create new runs."} />}
    </>
  );
}

function ExecutionRow({ execution }: { execution: ScheduleExecution }) {
  return (
    <li>
      <span className="execution-list__rail"><i /></span>
      <div><header><time dateTime={execution.scheduledFor}>{formatDate(execution.scheduledFor)}</time><StatusChip status={execution.status} compact /></header><p>{execution.reason || (execution.status === "completed" ? "Unattended run completed." : execution.status === "dispatched" ? "Run dispatched to its box." : `Execution ${execution.status}.`)}</p>{execution.boxId ? <Link className="text-link" to={`/boxes/${execution.boxId}`}>Open output <Icon name="arrow" size={14} /></Link> : null}</div>
    </li>
  );
}

function ScheduleEditor({ open, schedule, agents, hosts, workspaces, onClose, onSaved }: { open: boolean; schedule?: Schedule; agents: Agent[]; hosts: Host[]; workspaces: Workspace[]; onClose: () => void; onSaved: (schedule: Schedule) => void }) {
  const { notify } = useToast();
  const initial = useMemo<CreateScheduleRequest>(() => ({
    name: schedule?.name ?? "",
    agentId: schedule?.agentId ?? "",
    hostId: schedule?.hostId ?? "",
    workspaceId: schedule?.workspaceId ?? "",
    cronExpression: schedule?.cronExpression ?? "0 9 * * 1-5",
    timezone: schedule?.timezone ?? Intl.DateTimeFormat().resolvedOptions().timeZone,
    promptTemplate: schedule?.promptTemplate ?? "",
    concurrencyPolicy: schedule?.concurrencyPolicy ?? "skip",
    status: schedule?.status === "paused" ? "paused" : "active"
  }), [schedule]);
  const [form, setForm] = useState<CreateScheduleRequest>(initial);
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string>();
  useEffect(() => {
    if (!open) return;
    setForm(initial);
    setError(undefined);
  }, [initial, open]);
  const agent = agents.find((item) => item.id === form.agentId);
  const compatibleHosts = hosts.filter((host) => (host.status === "online" || host.id === form.hostId) && (!agent || host.runtimes.includes(agent.runtimeType)));
  const readyWorkspaces = workspaces.filter((workspace) => workspace.hostId === form.hostId && (workspace.status === "ready" || workspace.id === form.workspaceId));
  const set = <K extends keyof CreateScheduleRequest>(key: K, value: CreateScheduleRequest[K]) => setForm((current) => ({ ...current, [key]: value }));
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    setSubmitting(true);
    setError(undefined);
    try {
      const saved = schedule ? await api.updateSchedule(schedule.id, form) : await api.createSchedule(form);
      notify(`${saved.name} was ${schedule ? "updated" : "created"}.`);
      onSaved(saved);
    } catch (requestError) {
      setError(errorMessage(requestError));
    } finally {
      setSubmitting(false);
    }
  };
  return (
    <Modal open={open} onClose={onClose} title={schedule ? `Edit ${schedule.name}` : "Create a schedule"} description="Configure an unattended prompt and where it runs." size="large">
      <form className="form" onSubmit={submit}>
        {error ? <InlineAlert>{error}</InlineAlert> : null}
        <div className="form-grid form-grid--two"><label className="field"><span>Name</span><input required maxLength={120} value={form.name} onChange={(event) => set("name", event.target.value)} /></label><label className="field"><span>Status</span><select value={form.status} onChange={(event) => set("status", event.target.value as "active" | "paused")}><option value="active">Active</option><option value="paused">Paused</option></select></label></div>
        <label className="field"><span>Agent</span><select required value={form.agentId} onChange={(event) => setForm((current) => ({ ...current, agentId: event.target.value, hostId: "", workspaceId: "" }))}><option value="">Select an agent</option>{agents.map((item) => <option value={item.id} key={item.id}>{item.name} · {item.runtimeType.toUpperCase()}</option>)}</select></label>
        <div className="form-grid form-grid--two"><label className="field"><span>Host</span><select required value={form.hostId} onChange={(event) => setForm((current) => ({ ...current, hostId: event.target.value, workspaceId: "" }))}><option value="">Select an online host</option>{compatibleHosts.map((host) => <option value={host.id} key={host.id}>{host.name}</option>)}</select></label><label className="field"><span>Workspace</span><select required value={form.workspaceId} disabled={!form.hostId} onChange={(event) => set("workspaceId", event.target.value)}><option value="">Select a ready workspace</option>{readyWorkspaces.map((workspace) => <option value={workspace.id} key={workspace.id}>{workspace.name} · {workspace.path}</option>)}</select></label></div>
        <div className="form-grid form-grid--two"><label className="field"><span>Cron expression</span><input required className="mono" value={form.cronExpression} onChange={(event) => set("cronExpression", event.target.value)} /><small>Five-field cron, for example <code>0 9 * * 1-5</code>.</small></label><label className="field"><span>Timezone</span><input required value={form.timezone} onChange={(event) => set("timezone", event.target.value)} /><small>Use an IANA timezone such as America/Los_Angeles.</small></label></div>
        <label className="field"><span>Concurrency policy</span><select value={form.concurrencyPolicy} onChange={(event) => set("concurrencyPolicy", event.target.value as ConcurrencyPolicy)}><option value="skip">Skip if already running</option><option value="queue">Queue after active run</option><option value="replace">Stop active run and replace</option></select></label>
        <label className="field"><span>Prompt</span><textarea required rows={7} value={form.promptTemplate} onChange={(event) => set("promptTemplate", event.target.value)} placeholder="Describe the work this unattended run should complete…" /></label>
        <div className="modal__actions"><Button type="button" onClick={onClose}>Cancel</Button><Button type="submit" variant="primary" icon="schedule" busy={submitting} disabled={!form.name.trim() || !form.agentId || !form.hostId || !form.workspaceId || !form.cronExpression.trim() || !form.timezone.trim() || !form.promptTemplate.trim()}>{schedule ? "Save changes" : "Create schedule"}</Button></div>
      </form>
    </Modal>
  );
}
