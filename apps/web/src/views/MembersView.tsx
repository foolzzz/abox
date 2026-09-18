import { useState, type FormEvent } from "react";
import { api, errorMessage } from "../api/client";
import type { Member, OrganizationRole, Team } from "../api/types";
import { Icon } from "../components/Icon";
import { useToast } from "../components/Toast";
import { Button, EmptyState, ErrorState, InlineAlert, LoadingState, Modal, PageHeader, RefreshButton, StatusChip } from "../components/ui";
import { useResource } from "../hooks/useResource";
import { roleAtLeast, useAccess } from "../lib/access";
import { formatDate, initials } from "../lib/format";

interface OrganizationData {
  members: Member[];
  teams: Team[];
}

export function MembersView() {
  const { currentUser } = useAccess();
  const { notify } = useToast();
  const canManageAccounts = roleAtLeast(currentUser?.role, "admin");
  const resource = useResource<OrganizationData>(async (signal) => {
    const [members, teams] = await Promise.all([api.listMembers(signal), api.listTeams(signal)]);
    return { members, teams };
  }, []);
  const [createOpen, setCreateOpen] = useState(false);
  const [resetMember, setResetMember] = useState<Member>();
  const [updatingMemberId, setUpdatingMemberId] = useState<string>();
  const [requestError, setRequestError] = useState<string>();

  if (resource.loading) return <LoadingState label="Loading accounts" />;
  if (resource.error && !resource.data) return <ErrorState error={resource.error} retry={resource.reload} />;
  if (!canManageAccounts) return <div className="page"><PageHeader eyebrow="Administration" title="Accounts" description="Account management is restricted to administrators." /><EmptyState icon="members" title="Administrator access required" description="Your user account can operate shared resources but cannot manage other accounts." /></div>;

  const data = resource.data!;
  const replaceMember = (updated: Member) => resource.setData((current) => current ? { ...current, members: current.members.map((member) => member.id === updated.id ? updated : member) } : current);

  const updateAccount = async (member: Member, update: { role?: OrganizationRole; status?: "active" | "disabled" }) => {
    setUpdatingMemberId(member.id);
    setRequestError(undefined);
    try {
      const updated = await api.updateAccount(member.id, update);
      replaceMember(updated);
      notify(`${updated.displayName || updated.login} was updated.`);
    } catch (error) {
      setRequestError(errorMessage(error));
    } finally {
      setUpdatingMemberId(undefined);
    }
  };

  return (
    <div className="page">
      <PageHeader eyebrow="Administration" title="Accounts & access" description="Create local accounts and assign either administrator or user access." actions={<><RefreshButton refreshing={resource.refreshing} onClick={resource.reload} /><Button variant="primary" icon="plus" onClick={() => setCreateOpen(true)}>New account</Button></>} />
      {resource.error ? <InlineAlert tone="warning">Account data could not be refreshed. Showing the last loaded state.</InlineAlert> : null}
      {requestError ? <InlineAlert>{requestError}</InlineAlert> : null}

      <section className="current-user-panel panel" aria-label="Current account">
        <span className="member-avatar">{initials(currentUser?.displayName || currentUser?.login || "You")}</span>
        <div><p className="eyebrow">Signed in</p><h2>{currentUser?.displayName || currentUser?.login}</h2><p>{currentUser?.login}</p></div>
        <div className="current-user-panel__permission"><StatusChip status={currentUser?.role ?? "user"} /><small>{roleDescription(currentUser?.role)}</small></div>
      </section>

      <section className="organization-section">
        <div className="section-heading"><div><p className="eyebrow">Local accounts</p><h2>Accounts</h2></div><span>{data.members.length} total</span></div>
        <div className="table-panel">
          <div className="data-table data-table--members" role="table" aria-label="Accounts">
            <div className="data-table__header" role="row"><span role="columnheader">Account</span><span role="columnheader">Role</span><span role="columnheader">Status</span><span role="columnheader">Actions</span></div>
            {data.members.map((member) => {
              const isCurrent = member.id === currentUser?.id;
              const busy = updatingMemberId === member.id;
              return (
                <div className="data-table__row" role="row" key={member.id}>
                  <span role="cell" data-label="Account" className="member-cell"><span className="member-avatar member-avatar--small">{initials(member.displayName || member.login)}</span><span><strong>{member.displayName || member.login}{isCurrent ? " (you)" : ""}</strong><small>{member.login} · {member.hasPassword ? "password account" : "legacy identity (disabled)"} · joined {formatDate(member.createdAt)}</small></span></span>
                  <span role="cell" data-label="Role">{isCurrent ? <StatusChip status={member.role} compact /> : <label className="member-role-select"><span className="sr-only">Role for {member.login}</span><select value={member.role} disabled={busy || !member.hasPassword} onChange={(event) => void updateAccount(member, { role: event.target.value as OrganizationRole })}><option value="admin">Administrator</option><option value="user">User</option></select></label>}</span>
                  <span role="cell" data-label="Status">{isCurrent ? <StatusChip status={member.status} compact /> : <label className="member-role-select"><span className="sr-only">Status for {member.login}</span><select value={member.status} disabled={busy || !member.hasPassword} onChange={(event) => void updateAccount(member, { status: event.target.value as "active" | "disabled" })}><option value="active">Active</option><option value="disabled">Disabled</option></select></label>}</span>
                  <span role="cell" data-label="Actions">{member.hasPassword && !isCurrent ? <Button variant="ghost" onClick={() => setResetMember(member)}>Reset password</Button> : <span className="permission-caption">{busy ? "Updating…" : isCurrent ? "Current session" : "No local password"}</span>}</span>
                </div>
              );
            })}
          </div>
        </div>
      </section>

      <section className="organization-section">
        <div className="section-heading"><div><p className="eyebrow">Resource groups</p><h2>Teams</h2></div><span>{data.teams.length} team{data.teams.length === 1 ? "" : "s"}</span></div>
        {data.teams.length ? <div className="team-grid">{data.teams.map((team) => <article className="team-card panel" key={team.id}><span className="resource-icon"><Icon name="team" /></span><div><h3>{team.name}</h3><p className="mono">{team.slug}</p><small>{team.description || "Use this team in workspace and box sharing rules."}</small></div></article>)}</div> : <EmptyState icon="team" title="No teams yet" description="Teams created for this organization will be available in sharing dialogs." />}
      </section>

      <CreateAccountModal open={createOpen} onClose={() => setCreateOpen(false)} onCreated={(member) => { resource.setData((current) => current ? { ...current, members: [...current.members, member] } : current); setCreateOpen(false); notify(`${member.login} was created.`); }} />
      <ResetPasswordModal member={resetMember} onClose={() => setResetMember(undefined)} onReset={(member) => { replaceMember(member); setResetMember(undefined); notify(`${member.login} must change the new temporary password at next sign-in.`); }} />
    </div>
  );
}

function CreateAccountModal({ open, onClose, onCreated }: { open: boolean; onClose: () => void; onCreated: (member: Member) => void }) {
  const [username, setUsername] = useState("");
  const [displayName, setDisplayName] = useState("");
  const [role, setRole] = useState<OrganizationRole>("user");
  const [password, setPassword] = useState("");
  const [error, setError] = useState<string>();
  const [submitting, setSubmitting] = useState(false);

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    setSubmitting(true);
    setError(undefined);
    try {
      onCreated(await api.createAccount({ username, displayName, role, password }));
      setUsername(""); setDisplayName(""); setRole("user"); setPassword("");
    } catch (requestError) {
      setError(errorMessage(requestError));
    } finally {
      setSubmitting(false);
    }
  };

  return <Modal open={open} title="Create account" description="New accounts receive a temporary password that must be changed at first sign-in." onClose={onClose} size="small"><form className="modal-form" onSubmit={(event) => void submit(event)}>{error ? <InlineAlert>{error}</InlineAlert> : null}<label className="field"><span>Username</span><input value={username} onChange={(event) => setUsername(event.target.value)} minLength={3} maxLength={64} required /></label><label className="field"><span>Display name</span><input value={displayName} onChange={(event) => setDisplayName(event.target.value)} maxLength={128} required /></label><label className="field"><span>Role</span><select value={role} onChange={(event) => setRole(event.target.value as OrganizationRole)}><option value="user">User</option><option value="admin">Administrator</option></select></label><label className="field"><span>Temporary password</span><input type="password" value={password} onChange={(event) => setPassword(event.target.value)} minLength={8} maxLength={72} required /></label><div className="modal-actions"><Button type="button" variant="ghost" onClick={onClose}>Cancel</Button><Button type="submit" variant="primary" busy={submitting}>Create account</Button></div></form></Modal>;
}

function ResetPasswordModal({ member, onClose, onReset }: { member?: Member; onClose: () => void; onReset: (member: Member) => void }) {
  const [password, setPassword] = useState("");
  const [error, setError] = useState<string>();
  const [submitting, setSubmitting] = useState(false);

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    if (!member) return;
    setSubmitting(true);
    setError(undefined);
    try {
      onReset(await api.resetAccountPassword(member.id, { password }));
      setPassword("");
    } catch (requestError) {
      setError(errorMessage(requestError));
    } finally {
      setSubmitting(false);
    }
  };

  return <Modal open={Boolean(member)} title={`Reset password${member ? ` for ${member.login}` : ""}`} description="All active sessions will be revoked. The account must change this temporary password at next sign-in." onClose={onClose} size="small"><form className="modal-form" onSubmit={(event) => void submit(event)}>{error ? <InlineAlert>{error}</InlineAlert> : null}<label className="field"><span>Temporary password</span><input type="password" value={password} onChange={(event) => setPassword(event.target.value)} minLength={8} maxLength={72} required /></label><div className="modal-actions"><Button type="button" variant="ghost" onClick={onClose}>Cancel</Button><Button type="submit" variant="primary" busy={submitting}>Reset password</Button></div></form></Modal>;
}

function roleDescription(role: OrganizationRole | undefined): string {
  return role === "admin" ? "Full account and infrastructure administration." : "Operates resources granted through organization and resource access rules.";
}
