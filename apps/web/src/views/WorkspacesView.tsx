import { useState, type FormEvent } from "react";
import { api, errorMessage } from "../api/client";
import type { Workspace } from "../api/types";
import { AccessControlDialog } from "../components/AccessControlDialog";
import { Icon } from "../components/Icon";
import { useToast } from "../components/Toast";
import { Button, EmptyState, ErrorState, InlineAlert, LoadingState, Modal, PageHeader, RefreshButton, StatusChip } from "../components/ui";
import { useResource } from "../hooks/useResource";
import { roleAtLeast, useAccess } from "../lib/access";
import { formatDate } from "../lib/format";
import { navigate, useLocation } from "../lib/router";

export function WorkspacesView() {
  const { currentUser } = useAccess();
  const location = useLocation();
  const resources = useResource(async (signal) => {
    const [workspaces, hosts] = await Promise.all([api.listWorkspaces(signal), api.listHosts(signal)]);
    return { workspaces, hosts };
  }, []);
  const [sharingWorkspace, setSharingWorkspace] = useState<Workspace>();
  const [deletingWorkspace, setDeletingWorkspace] = useState<Workspace>();
  const [deleteBusy, setDeleteBusy] = useState(false);
  const [deleteError, setDeleteError] = useState<string>();
  const canCreate = roleAtLeast(currentUser?.role, "admin");
  const createOpen = canCreate && new URLSearchParams(location.search).get("create") === "1";

  if (resources.loading) return <LoadingState label="Loading workspaces" />;
  if (resources.error && !resources.data) return <ErrorState error={resources.error} retry={resources.reload} />;

  const data = resources.data!;
  const hostNames = new Map(data.hosts.map((host) => [host.id, host.name]));

  const openDelete = (workspace: Workspace) => {
    setDeleteError(undefined);
    setDeletingWorkspace(workspace);
  };

  const closeDelete = () => {
    if (deleteBusy) return;
    setDeleteError(undefined);
    setDeletingWorkspace(undefined);
  };

  const deleteWorkspace = async () => {
    const workspace = deletingWorkspace;
    if (!workspace) return;
    setDeleteBusy(true);
    setDeleteError(undefined);
    try {
      await api.deleteWorkspace(workspace.id);
      resources.setData((current) => current ? {
        ...current,
        workspaces: current.workspaces.filter((candidate) => candidate.id !== workspace.id)
      } : current);
      setDeletingWorkspace(undefined);
    } catch (error) {
      setDeleteError(errorMessage(error));
    } finally {
      setDeleteBusy(false);
    }
  };

  return (
    <div className="page">
      <PageHeader eyebrow="Execution context" title="Workspaces" description="Host directories made available to agent boxes, with explicit owner, operator, and viewer access." actions={<><RefreshButton refreshing={resources.refreshing} onClick={resources.reload} />{canCreate ? <Button variant="primary" icon="plus" onClick={() => navigate("/workspaces?create=1")}>New workspace</Button> : null}</>} />
      {resources.error ? <InlineAlert tone="warning">The list could not be refreshed. Showing the last loaded data.</InlineAlert> : null}
      {!canCreate ? <p className="permission-caption">Users can access shared workspaces; only administrators can register one.</p> : null}
      {data.workspaces.length ? (
        <section className="table-panel" aria-label="Registered workspaces">
          <div className="data-table data-table--workspaces" role="table">
            <div className="data-table__header" role="row"><span role="columnheader">Workspace</span><span role="columnheader">Host</span><span role="columnheader">Path</span><span role="columnheader">State</span><span role="columnheader">Created</span><span role="columnheader">Actions</span></div>
            {data.workspaces.map((workspace) => (
              <div className="data-table__row" role="row" key={workspace.id}>
                <span role="cell" data-label="Workspace"><span className="table-primary"><span className="resource-icon resource-icon--workspace"><Icon name="workspace" /></span><strong>{workspace.name}</strong></span></span>
                <span role="cell" data-label="Host">{hostNames.get(workspace.hostId) ?? "Unknown host"}</span>
                <span role="cell" data-label="Path" className="mono table-path">{workspace.path}</span>
                <span role="cell" data-label="State"><StatusChip status={workspace.status} compact /></span>
                <span role="cell" data-label="Created">{formatDate(workspace.createdAt)}</span>
                <span role="cell" data-label="Actions"><div className="table-actions"><button className="icon-button" type="button" title={`Sharing for ${workspace.name}`} aria-label={`Open sharing for ${workspace.name}`} onClick={() => setSharingWorkspace(workspace)}><Icon name="share" /></button>{canCreate ? <button className="icon-button icon-button--danger" type="button" title={`Delete ${workspace.name}`} aria-label={`Delete ${workspace.name}`} onClick={() => openDelete(workspace)}><Icon name="trash" /></button> : null}</div></span>
              </div>
            ))}
          </div>
        </section>
      ) : <EmptyState icon="workspace" title="No workspaces registered" description={canCreate ? "Register a directory on an online host before creating a box." : "An organization admin must register a directory before boxes can be created."} action={canCreate ? <Button variant="primary" icon="plus" onClick={() => navigate("/workspaces?create=1")}>New workspace</Button> : undefined} />}
      <CreateWorkspaceModal
        open={createOpen}
        hosts={data.hosts}
        onClose={() => navigate("/workspaces", { replace: true })}
        onCreated={(workspace) => {
          resources.setData((current) => current ? { ...current, workspaces: [workspace, ...current.workspaces] } : current);
          navigate("/workspaces", { replace: true });
        }}
      />
      {sharingWorkspace ? <AccessControlDialog open resourceKind="workspace" resourceId={sharingWorkspace.id} resourceName={sharingWorkspace.name} onClose={() => setSharingWorkspace(undefined)} onSaved={resources.reload} /> : null}
      <Modal
        open={Boolean(deletingWorkspace)}
        title={`Delete ${deletingWorkspace?.name ?? "workspace"}?`}
        description="Archive this workspace registration without deleting the directory on the Host."
        onClose={closeDelete}
        size="small"
      >
        <div className="confirm-dialog">
          {deleteError ? <InlineAlert>{deleteError}</InlineAlert> : null}
          <InlineAlert tone="warning">A workspace used by active Boxes cannot be deleted.</InlineAlert>
          <div className="modal__actions">
            <Button disabled={deleteBusy} onClick={closeDelete}>Cancel</Button>
            <Button variant="danger" icon="trash" busy={deleteBusy} onClick={() => void deleteWorkspace()}>Delete workspace</Button>
          </div>
        </div>
      </Modal>
    </div>
  );
}

function CreateWorkspaceModal({ open, hosts, onClose, onCreated }: { open: boolean; hosts: Array<{ id: string; name: string; status: string }>; onClose: () => void; onCreated: (workspace: Workspace) => void }) {
  const { notify } = useToast();
  const [hostId, setHostId] = useState("");
  const [name, setName] = useState("");
  const [path, setPath] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string>();
  const onlineHosts = hosts.filter((host) => host.status === "online");
  const selectedHost = hostId || (onlineHosts.length === 1 ? onlineHosts[0]?.id ?? "" : "");

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    setSubmitting(true);
    setError(undefined);
    try {
      const workspace = await api.createWorkspace({ hostId: selectedHost, name: name.trim(), path: path.trim() });
      notify(`${workspace.name} is being provisioned.`);
      setHostId("");
      setName("");
      setPath("");
      onCreated(workspace);
    } catch (requestError) {
      setError(errorMessage(requestError));
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Modal open={open} onClose={onClose} title="Register a workspace" description="Expose an existing directory on an online host.">
      <form className="form" onSubmit={submit}>
        {error ? <InlineAlert>{error}</InlineAlert> : null}
        {onlineHosts.length === 0 ? <InlineAlert tone="warning">No online hosts are available. Bring a daemon online before registering a workspace.</InlineAlert> : null}
        <label className="field"><span>Host</span><select autoFocus required value={selectedHost} onChange={(event) => setHostId(event.target.value)}><option value="">Select a host</option>{onlineHosts.map((host) => <option value={host.id} key={host.id}>{host.name}</option>)}</select></label>
        <label className="field"><span>Name</span><input required maxLength={120} value={name} onChange={(event) => setName(event.target.value)} /></label>
        <label className="field"><span>Absolute path</span><input className="mono" required value={path} onChange={(event) => setPath(event.target.value)} /><small>The daemon must be able to access this directory.</small></label>
        <div className="modal__actions"><Button type="button" onClick={onClose}>Cancel</Button><Button type="submit" variant="primary" icon="check" busy={submitting} disabled={!selectedHost || !name.trim() || !path.trim()}>Register workspace</Button></div>
      </form>
    </Modal>
  );
}
