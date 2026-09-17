import { useState } from "react";
import { api, errorMessage } from "../api/client";
import type { Member, OrganizationRole, Team } from "../api/types";
import { Icon } from "../components/Icon";
import { useToast } from "../components/Toast";
import { EmptyState, ErrorState, InlineAlert, LoadingState, PageHeader, RefreshButton, StatusChip } from "../components/ui";
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
  const resource = useResource<OrganizationData>(async (signal) => {
    const [members, teams] = await Promise.all([api.listMembers(signal), api.listTeams(signal)]);
    return { members, teams };
  }, []);
  const [updatingMemberId, setUpdatingMemberId] = useState<string>();
  const [roleError, setRoleError] = useState<string>();

  if (resource.loading) return <LoadingState label="Loading organization" />;
  if (resource.error && !resource.data) return <ErrorState error={resource.error} retry={resource.reload} />;

  const data = resource.data!;
  const canManageMembers = roleAtLeast(currentUser?.role, "admin");
  const currentIsOwner = currentUser?.role === "owner";

  const updateRole = async (member: Member, role: OrganizationRole) => {
    if (member.role === role) return;
    setUpdatingMemberId(member.id);
    setRoleError(undefined);
    try {
      const updated = await api.updateMemberRole(member.id, { role });
      resource.setData((current) => current ? { ...current, members: current.members.map((candidate) => candidate.id === updated.id ? updated : candidate) } : current);
      notify(`${updated.displayName || updated.login} is now ${updated.role}.`);
    } catch (requestError) {
      setRoleError(errorMessage(requestError));
    } finally {
      setUpdatingMemberId(undefined);
    }
  };

  return (
    <div className="page">
      <PageHeader eyebrow="Organization" title="Members & teams" description="See who can use the console and how organization roles affect access." actions={<RefreshButton refreshing={resource.refreshing} onClick={resource.reload} />} />
      {resource.error ? <InlineAlert tone="warning">Organization data could not be refreshed. Showing the last loaded state.</InlineAlert> : null}
      {roleError ? <InlineAlert>{roleError}</InlineAlert> : null}

      <section className="current-user-panel panel" aria-label="Current user permissions">
        <span className="member-avatar">{initials(currentUser?.displayName || currentUser?.login || "You")}</span>
        <div><p className="eyebrow">Signed in</p><h2>{currentUser?.displayName || currentUser?.login || "Current user"}</h2><p>{currentUser?.login}</p></div>
        <div className="current-user-panel__permission"><StatusChip status={currentUser?.role ?? "viewer"} /><small>{roleDescription(currentUser?.role)}</small></div>
      </section>

      <section className="organization-section">
        <div className="section-heading"><div><p className="eyebrow">People</p><h2>Organization members</h2></div><span>{data.members.length} member{data.members.length === 1 ? "" : "s"}</span></div>
        <div className="table-panel">
          <div className="data-table data-table--members" role="table" aria-label="Organization members">
            <div className="data-table__header" role="row"><span role="columnheader">Member</span><span role="columnheader">Role</span><span role="columnheader">Joined</span></div>
            {data.members.map((member) => {
              const isCurrent = member.id === currentUser?.id;
              const canEdit = canManageMembers && !isCurrent && (currentIsOwner || member.role !== "owner");
              const options: OrganizationRole[] = currentIsOwner ? ["owner", "admin", "operator", "viewer"] : ["admin", "operator", "viewer"];
              return (
                <div className="data-table__row" role="row" key={member.id}>
                  <span role="cell" data-label="Member" className="member-cell"><span className="member-avatar member-avatar--small">{initials(member.displayName || member.login)}</span><span><strong>{member.displayName || member.login}{isCurrent ? " (you)" : ""}</strong><small>{member.login}</small></span></span>
                  <span role="cell" data-label="Role">{canEdit ? <label className="member-role-select"><span className="sr-only">Role for {member.displayName || member.login}</span><select value={member.role} disabled={updatingMemberId === member.id} onChange={(event) => void updateRole(member, event.target.value as OrganizationRole)}>{options.map((role) => <option value={role} key={role}>{role.charAt(0).toUpperCase()}{role.slice(1)}</option>)}</select>{updatingMemberId === member.id ? <span className="spinner spinner--small" /> : null}</label> : <StatusChip status={member.role} compact />}</span>
                  <span role="cell" data-label="Joined">{formatDate(member.createdAt)}</span>
                </div>
              );
            })}
          </div>
        </div>
        {!canManageMembers ? <p className="permission-caption">Only organization owners and admins can change member roles.</p> : null}
      </section>

      <section className="organization-section">
        <div className="section-heading"><div><p className="eyebrow">Groups</p><h2>Teams</h2></div><span>{data.teams.length} team{data.teams.length === 1 ? "" : "s"}</span></div>
        {data.teams.length ? <div className="team-grid">{data.teams.map((team) => <article className="team-card panel" key={team.id}><span className="resource-icon"><Icon name="team" /></span><div><h3>{team.name}</h3><p className="mono">{team.slug}</p><small>{team.description || "Use this team in workspace and box sharing rules."}</small></div></article>)}</div> : <EmptyState icon="team" title="No teams yet" description="Teams created for this organization will be available in sharing dialogs." />}
      </section>
    </div>
  );
}


function roleDescription(role: OrganizationRole | undefined): string {
  if (role === "owner") return "Full organization control, including member roles and every resource.";
  if (role === "admin") return "Manages agents, workspaces, sharing, and day-to-day operations.";
  if (role === "operator") return "Creates and operates boxes where resource access allows it.";
  return "Read-only by default; explicit resource access can broaden what is visible.";
}
