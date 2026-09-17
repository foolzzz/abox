import { useState } from "react";
import { api, errorMessage } from "../api/client";
import type { Approval } from "../api/types";
import { Icon } from "../components/Icon";
import { useToast } from "../components/Toast";
import { Button, EmptyState, ErrorState, InlineAlert, LoadingState, PageHeader, RefreshButton, StatusChip } from "../components/ui";
import { useResource } from "../hooks/useResource";
import { roleAtLeast, useAccess } from "../lib/access";
import { compactJson, relativeTime } from "../lib/format";
import { Link } from "../lib/router";

export function ApprovalsView() {
  const { currentUser, loading: accessLoading } = useAccess();
  const canReview = roleAtLeast(currentUser?.role, "operator");
  const resource = useResource((signal) => canReview ? api.listApprovals(signal) : Promise.resolve([]), [canReview]);

  if (accessLoading || resource.loading) return <LoadingState label="Loading approval queue" />;
  if (!canReview) return <div className="page"><PageHeader eyebrow="Human in the loop" title="Approvals" description="Sensitive tool requests require an operator decision." /><EmptyState icon="approval" title="Operator access required" description={`Your ${currentUser?.role ?? "viewer"} role is read-only. An operator, admin, or owner can review approvals.`} /></div>;
  if (resource.error && !resource.data) return <ErrorState error={resource.error} retry={resource.reload} />;

  const approvals = resource.data ?? [];
  const pending = approvals.filter((approval) => approval.status === "pending");
  const resolved = approvals.filter((approval) => approval.status !== "pending").sort((left, right) => new Date(right.resolvedAt ?? right.expiresAt).getTime() - new Date(left.resolvedAt ?? left.expiresAt).getTime());

  return (
    <div className="page">
      <PageHeader eyebrow="Human in the loop" title="Approvals" description="Review the full remote request, expiry, and final operator decision for sensitive OMP tools." actions={<RefreshButton refreshing={resource.refreshing} onClick={resource.reload} />} />
      {resource.error ? <InlineAlert tone="warning">The queue could not be refreshed. Decisions may have changed.</InlineAlert> : null}
      {pending.length ? (
        <section className="approval-list" aria-label="Pending approvals">
          <div className="queue-summary"><span><Icon name="clock" />{pending.length} pending</span><small>Oldest requests should be reviewed first.</small></div>
          {[...pending].sort((left, right) => new Date(left.expiresAt).getTime() - new Date(right.expiresAt).getTime()).map((approval) => (
            <ApprovalCard approval={approval} key={approval.id} onResolved={(updated) => resource.setData((current) => current?.map((item) => item.id === updated.id ? updated : item))} />
          ))}
        </section>
      ) : <EmptyState icon="approval" title="Approval queue is clear" description="New OMP tool requests requiring a remote decision will appear here." />}
      {resolved.length ? <section className="approval-history"><div className="section-heading"><div><p className="eyebrow">Decision log</p><h2>Recent requests</h2></div><span>{resolved.length} resolved</span></div><div className="approval-list approval-list--history">{resolved.map((approval) => <ApprovalCard approval={approval} canDecide={false} key={approval.id} onResolved={() => undefined} />)}</div></section> : null}
    </div>
  );
}

export function ApprovalCard({ approval, onResolved, compact = false, canDecide = true }: { approval: Approval; onResolved: (approval: Approval) => void; compact?: boolean; canDecide?: boolean }) {
  const { notify } = useToast();
  const [deciding, setDeciding] = useState<"approved" | "denied">();
  const [error, setError] = useState<string>();
  const expired = approval.status === "expired" || new Date(approval.expiresAt).getTime() <= Date.now();

  const decide = async (decision: "approved" | "denied") => {
    setDeciding(decision);
    setError(undefined);
    try {
      const resolved = await api.decideApproval(approval.id, { decision });
      onResolved(resolved);
      window.dispatchEvent(new Event("agentbox:approvals-changed"));
      notify(`${approval.toolName} was ${decision}.`, decision === "approved" ? "success" : "error");
    } catch (requestError) {
      setError(errorMessage(requestError));
    } finally {
      setDeciding(undefined);
    }
  };

  return (
    <article className={`approval-card${compact ? " approval-card--compact" : ""}${approval.status !== "pending" ? " approval-card--resolved" : ""}`}>
      <div className="approval-card__risk"><span className={`risk-badge risk-badge--${approval.riskLevel}`}>{approval.riskLevel}</span><span>{approval.status === "pending" ? `Expires ${relativeTime(approval.expiresAt)}` : approval.status === "expired" ? `Expired ${relativeTime(approval.expiresAt)}` : `Resolved ${relativeTime(approval.resolvedAt ?? approval.expiresAt)}`}</span><StatusChip status={approval.status} compact /></div>
      <div className="approval-card__main">
        <span className="resource-icon resource-icon--approval"><Icon name="terminal" /></span>
        <div><p className="eyebrow">Remote tool request</p><h2>{approval.toolName}</h2><Link to={`/boxes/${approval.boxId}`}>Open box <Icon name="arrow" size={14} /></Link></div>
      </div>
      <dl className="approval-card__facts"><div><dt>Requested</dt><dd>{approval.requestedAt ? relativeTime(approval.requestedAt) : "—"}</dd></div><div><dt>Expires</dt><dd title={approval.expiresAt}>{relativeTime(approval.expiresAt)}</dd></div><div><dt>Run</dt><dd className="mono" title={approval.runId}>{approval.runId.slice(0, 8)}</dd></div>{approval.resolvedAt ? <div><dt>Resolved</dt><dd>{relativeTime(approval.resolvedAt)}</dd></div> : null}</dl>
      {approval.payload && Object.keys(approval.payload).length ? <details className="payload-details"><summary>Request payload</summary><pre>{compactJson(approval.payload)}</pre></details> : null}
      {approval.decisionPayload && Object.keys(approval.decisionPayload).length ? <details className="payload-details"><summary>Decision details</summary><pre>{compactJson(approval.decisionPayload)}</pre></details> : null}
      {error ? <InlineAlert>{error}</InlineAlert> : null}
      <div className="approval-card__actions">
        {canDecide && approval.status === "pending" ? <><Button icon="deny" variant="danger" busy={deciding === "denied"} disabled={Boolean(deciding) || expired} onClick={() => void decide("denied")}>Deny</Button><Button icon="check" variant="primary" busy={deciding === "approved"} disabled={Boolean(deciding) || expired} onClick={() => void decide("approved")}>Approve</Button></> : null}
        {approval.resolvedByUserId ? <span className="approval-card__resolver">Decision by <code>{approval.resolvedByUserId.slice(0, 8)}</code></span> : null}
        {expired && approval.status === "pending" ? <StatusChip status="expired" compact /> : null}
      </div>
    </article>
  );
}
