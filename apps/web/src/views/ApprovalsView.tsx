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

  return (
    <div className="page">
      <PageHeader eyebrow="Human in the loop" title="Approvals" description="Review sensitive tool requests before they continue." actions={<RefreshButton refreshing={resource.refreshing} onClick={resource.reload} />} />
      {resource.error ? <InlineAlert tone="warning">The queue could not be refreshed. Decisions may have changed.</InlineAlert> : null}
      {pending.length ? (
        <section className="approval-list" aria-label="Pending approvals">
          <div className="queue-summary"><span><Icon name="clock" />{pending.length} pending</span><small>Oldest requests should be reviewed first.</small></div>
          {[...pending].sort((left, right) => new Date(left.expiresAt).getTime() - new Date(right.expiresAt).getTime()).map((approval) => (
            <ApprovalCard
              approval={approval}
              key={approval.id}
              onResolved={(resolved) => resource.setData((current) => current?.map((item) => item.id === resolved.id ? resolved : item))}
            />
          ))}
        </section>
      ) : <EmptyState icon="approval" title="Approval queue is clear" description="New tool requests requiring a decision will appear here." />}
    </div>
  );
}

export function ApprovalCard({ approval, onResolved, compact = false, canDecide = true }: { approval: Approval; onResolved: (approval: Approval) => void; compact?: boolean; canDecide?: boolean }) {
  const { notify } = useToast();
  const [deciding, setDeciding] = useState<"approved" | "denied">();
  const [error, setError] = useState<string>();
  const expired = new Date(approval.expiresAt).getTime() <= Date.now();

  const decide = async (decision: "approved" | "denied") => {
    setDeciding(decision);
    setError(undefined);
    try {
      const resolved = await api.decideApproval(approval.id, { decision });
      onResolved(resolved);
      notify(`${approval.toolName} was ${decision}.`, decision === "approved" ? "success" : "error");
    } catch (requestError) {
      setError(errorMessage(requestError));
    } finally {
      setDeciding(undefined);
    }
  };

  return (
    <article className={`approval-card${compact ? " approval-card--compact" : ""}`}>
      <div className="approval-card__risk"><span className={`risk-badge risk-badge--${approval.riskLevel}`}>{approval.riskLevel}</span><span>Expires {relativeTime(approval.expiresAt)}</span></div>
      <div className="approval-card__main">
        <span className="resource-icon resource-icon--approval"><Icon name="terminal" /></span>
        <div><p className="eyebrow">Tool request</p><h2>{approval.toolName}</h2><Link to={`/boxes/${approval.boxId}`}>Open box <Icon name="arrow" size={14} /></Link></div>
      </div>
      {approval.payload && Object.keys(approval.payload).length ? <details className="payload-details"><summary>Request payload</summary><pre>{compactJson(approval.payload)}</pre></details> : null}
      {error ? <InlineAlert>{error}</InlineAlert> : null}
      <div className="approval-card__actions">
        {canDecide ? <><Button icon="deny" variant="danger" busy={deciding === "denied"} disabled={Boolean(deciding) || expired} onClick={() => void decide("denied")}>Deny</Button><Button icon="check" variant="primary" busy={deciding === "approved"} disabled={Boolean(deciding) || expired} onClick={() => void decide("approved")}>Approve</Button></> : <StatusChip status="read_only" compact />}
        {expired ? <StatusChip status="expired" compact /> : null}
      </div>
    </article>
  );
}
