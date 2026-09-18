import { useState } from "react";
import { api, errorMessage } from "../api/client";
import type { Host } from "../api/types";
import { Icon } from "../components/Icon";
import { Button, EmptyState, ErrorState, InlineAlert, LoadingState, Modal, PageHeader, RefreshButton, StatusChip } from "../components/ui";
import { useResource } from "../hooks/useResource";
import { roleAtLeast, useAccess } from "../lib/access";
import { relativeTime } from "../lib/format";

export function HostsView() {
  const { currentUser } = useAccess();
  const canDelete = roleAtLeast(currentUser?.role, "admin");
  const resource = useResource((signal) => api.listHosts(signal), []);
  const [deletingHost, setDeletingHost] = useState<Host>();
  const [deleteBusy, setDeleteBusy] = useState(false);
  const [deleteError, setDeleteError] = useState<string>();

  if (resource.loading) return <LoadingState label="Loading host fleet" />;
  if (resource.error && !resource.data) return <ErrorState error={resource.error} retry={resource.reload} />;

  const hosts = resource.data ?? [];
  const online = hosts.filter((host) => host.status === "online").length;

  const openDelete = (host: Host) => {
    setDeleteError(undefined);
    setDeletingHost(host);
  };

  const closeDelete = () => {
    if (deleteBusy) return;
    setDeleteError(undefined);
    setDeletingHost(undefined);
  };

  const deleteHost = async () => {
    const host = deletingHost;
    if (!host) return;
    setDeleteBusy(true);
    setDeleteError(undefined);
    try {
      await api.deleteHost(host.id);
      resource.setData((current) => current?.filter((candidate) => candidate.id !== host.id));
      setDeletingHost(undefined);
    } catch (error) {
      setDeleteError(errorMessage(error));
    } finally {
      setDeleteBusy(false);
    }
  };

  return (
    <div className="page">
      <PageHeader eyebrow="Compute fleet" title="Hosts" description="Daemon connectivity, runtime support, and execution capacity." actions={<RefreshButton refreshing={resource.refreshing} onClick={resource.reload} />} />
      {resource.error ? <InlineAlert tone="warning">Host state could not be refreshed. Displayed presence may be stale.</InlineAlert> : null}
      {hosts.length ? (
        <>
          <div className="fleet-summary"><span className="fleet-summary__pulse" /><strong>{online} of {hosts.length}</strong><span>hosts online</span></div>
          <section className="card-grid" aria-label="Execution hosts">
            {hosts.map((host) => (
              <article className="resource-card host-card" key={host.id}>
                <div className="resource-card__top"><span className="resource-icon resource-icon--host"><Icon name="host" /></span><StatusChip status={host.status} /></div>
                <div className="resource-card__body"><h2>{host.name}</h2><p className="resource-card__subtitle mono">{host.systemHostname || "Hostname not reported"}</p><p className="resource-card__description">{[host.os, host.arch].filter(Boolean).join(" · ") || "Platform not reported"}</p></div>
                <div className="runtime-list" aria-label="Supported runtimes">{host.runtimes.length ? host.runtimes.map((runtime) => <span key={runtime}>{runtime}</span>) : <small>No runtimes reported</small>}</div>
                <dl className="resource-card__facts">
                  <div><dt>Daemon</dt><dd>{host.daemonVersion || "—"}</dd></div>
                  <div><dt>Last seen</dt><dd>{relativeTime(host.lastSeenAt)}</dd></div>
                  <div><dt>Capacity</dt><dd>{host.maxActiveBoxes ?? "—"} boxes</dd></div>
                </dl>
                {canDelete && host.status === "offline" ? <div className="resource-card__actions"><Button variant="danger" icon="trash" onClick={() => openDelete(host)}>Delete host</Button></div> : null}
              </article>
            ))}
          </section>
        </>
      ) : <EmptyState icon="host" title="No hosts enrolled" description="Start an AgentBox daemon to enroll an execution host." />}
      <Modal
        open={Boolean(deletingHost)}
        title={`Delete ${deletingHost?.name ?? "host"}?`}
        description="Revoke this offline execution host and remove it from the fleet."
        onClose={closeDelete}
        size="small"
      >
        <div className="confirm-dialog">
          {deleteError ? <InlineAlert>{deleteError}</InlineAlert> : null}
          <InlineAlert tone="warning">Hosts with active Boxes or non-archived Workspaces cannot be deleted. A revoked daemon credential cannot reconnect.</InlineAlert>
          <div className="modal__actions">
            <Button disabled={deleteBusy} onClick={closeDelete}>Cancel</Button>
            <Button variant="danger" icon="trash" busy={deleteBusy} onClick={() => void deleteHost()}>Delete host</Button>
          </div>
        </div>
      </Modal>
    </div>
  );
}
