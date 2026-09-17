import { api } from "../api/client";
import type { Agent, Approval, Box, Host, Workspace } from "../api/types";
import { Icon, type IconName } from "../components/Icon";
import { EmptyState, ErrorState, LoadingState, PageHeader, RefreshButton, StatusChip } from "../components/ui";
import { useResource } from "../hooks/useResource";
import { formatDate } from "../lib/format";
import { Link } from "../lib/router";

interface DashboardData {
  agents: Agent[];
  hosts: Host[];
  workspaces: Workspace[];
  boxes: Box[];
  approvals: Approval[];
}

export function DashboardView() {
  const resource = useResource<DashboardData>(async (signal) => {
    const [agents, hosts, workspaces, boxes, approvals] = await Promise.all([
      api.listAgents(signal),
      api.listHosts(signal),
      api.listWorkspaces(signal),
      api.listBoxes(signal),
      api.listApprovals(signal)
    ]);
    return { agents, hosts, workspaces, boxes, approvals };
  }, []);

  if (resource.loading) return <LoadingState label="Loading operational overview" />;
  if (resource.error && !resource.data) return <ErrorState error={resource.error} retry={resource.reload} />;

  const data = resource.data!;
  const onlineHosts = data.hosts.filter((host) => host.status === "online").length;
  const activeBoxes = data.boxes.filter((box) => ["starting", "running", "waiting_approval"].includes(box.status)).length;
  const pendingApprovals = data.approvals.filter((approval) => approval.status === "pending").length;
  const readyWorkspaces = data.workspaces.filter((workspace) => workspace.status === "ready").length;
  const agentNames = new Map(data.agents.map((agent) => [agent.id, agent.name]));
  const hostNames = new Map(data.hosts.map((host) => [host.id, host.name]));
  const recentBoxes = [...data.boxes]
    .sort((left, right) => new Date(right.updatedAt ?? right.createdAt ?? 0).getTime() - new Date(left.updatedAt ?? left.createdAt ?? 0).getTime())
    .slice(0, 6);

  return (
    <div className="page">
      <PageHeader
        eyebrow="Command center"
        title="Good to see you."
        description="Live operational state across your agents and execution hosts."
        actions={
          <>
            <RefreshButton refreshing={resource.refreshing} onClick={resource.reload} />
            <Link className="button button--primary" to="/boxes?create=1"><Icon name="plus" /><span>New box</span></Link>
          </>
        }
      />

      {resource.error ? <div className="inline-alert inline-alert--warning">Some data may be stale. Refresh to reconnect.</div> : null}

      <section className="metric-grid" aria-label="System summary">
        <MetricCard icon="host" label="Hosts online" value={`${onlineHosts}/${data.hosts.length}`} detail={onlineHosts === data.hosts.length && data.hosts.length > 0 ? "All systems nominal" : `${data.hosts.length - onlineHosts} require attention`} tone={onlineHosts === data.hosts.length && data.hosts.length > 0 ? "positive" : "warning"} />
        <MetricCard icon="activity" label="Active boxes" value={String(activeBoxes)} detail={`${data.boxes.length} total boxes`} tone="active" />
        <MetricCard icon="approval" label="Pending approvals" value={String(pendingApprovals)} detail={pendingApprovals ? "Operator decision required" : "Queue is clear"} tone={pendingApprovals ? "warning" : "positive"} />
        <MetricCard icon="workspace" label="Ready workspaces" value={String(readyWorkspaces)} detail={`${data.agents.length} agent definitions`} tone="neutral" />
      </section>

      <div className="dashboard-grid">
        <section className="panel panel--wide">
          <div className="panel__header">
            <div><p className="eyebrow">Recent activity</p><h2>Boxes</h2></div>
            <Link className="text-link" to="/boxes">View all <Icon name="arrow" size={15} /></Link>
          </div>
          {recentBoxes.length === 0 ? (
            <EmptyState
              icon="box"
              title="No boxes yet"
              description="Create a box to start an agent in a workspace."
              action={<Link className="button button--primary" to="/boxes?create=1"><Icon name="plus" />Create box</Link>}
            />
          ) : (
            <div className="resource-list resource-list--compact">
              {recentBoxes.map((box) => (
                <Link className="resource-row resource-row--link" to={`/boxes/${box.id}`} key={box.id}>
                  <span className="resource-icon resource-icon--box"><Icon name="box" /></span>
                  <span className="resource-row__main">
                    <strong>{box.name}</strong>
                    <small>{agentNames.get(box.agentId) ?? "Unknown agent"} · {hostNames.get(box.hostId) ?? "Unknown host"}</small>
                  </span>
                  <span className="resource-row__meta"><StatusChip status={box.status} compact /><small>{formatDate(box.updatedAt)}</small></span>
                  <Icon name="chevron" className="resource-row__chevron" />
                </Link>
              ))}
            </div>
          )}
        </section>

        <aside className="panel quick-actions">
          <div className="panel__header"><div><p className="eyebrow">Launch</p><h2>Quick actions</h2></div></div>
          <Link className="quick-action" to="/boxes?create=1"><span><Icon name="box" /></span><div><strong>Create a box</strong><small>Start an agent in a workspace</small></div><Icon name="arrow" /></Link>
          <Link className="quick-action" to="/workspaces?create=1"><span><Icon name="workspace" /></span><div><strong>Add a workspace</strong><small>Register a host directory</small></div><Icon name="arrow" /></Link>
          <Link className="quick-action" to="/agents?create=1"><span><Icon name="agent" /></span><div><strong>Define an agent</strong><small>Set runtime and instructions</small></div><Icon name="arrow" /></Link>
          {pendingApprovals > 0 ? <Link className="approval-callout" to="/approvals"><Icon name="approval" /><div><strong>{pendingApprovals} decision{pendingApprovals === 1 ? "" : "s"} waiting</strong><small>Review before they expire</small></div><Icon name="arrow" /></Link> : null}
        </aside>
      </div>
    </div>
  );
}

function MetricCard({ icon, label, value, detail, tone }: { icon: IconName; label: string; value: string; detail: string; tone: "positive" | "active" | "warning" | "neutral" }) {
  return (
    <article className={`metric-card metric-card--${tone}`}>
      <span className="metric-card__icon"><Icon name={icon} /></span>
      <div><p>{label}</p><strong>{value}</strong><small>{detail}</small></div>
    </article>
  );
}
