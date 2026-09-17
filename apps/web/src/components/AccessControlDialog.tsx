import { useEffect, useMemo, useState } from "react";
import { api, errorMessage } from "../api/client";
import type { AccessControlEntry, AccessControlInput, Member, ResourceRole, Team } from "../api/types";
import { roleAtLeast, useAccess } from "../lib/access";
import { Button, InlineAlert, Modal, StatusChip } from "./ui";
import { Icon } from "./Icon";
import { useToast } from "./Toast";

type ResourceKind = "workspace" | "box";

interface AccessControlDialogProps {
  open: boolean;
  resourceKind: ResourceKind;
  resourceId: string;
  resourceName: string;
  onClose: () => void;
  onSaved?: () => void;
}

interface SubjectOption {
  key: string;
  kind: "user" | "team";
  id: string;
  label: string;
  detail: string;
}

export function AccessControlDialog({
  open,
  resourceKind,
  resourceId,
  resourceName,
  onClose,
  onSaved
}: AccessControlDialogProps) {
  const { currentUser } = useAccess();
  const { notify } = useToast();
  const [members, setMembers] = useState<Member[]>([]);
  const [teams, setTeams] = useState<Team[]>([]);
  const [loadedEntries, setLoadedEntries] = useState<AccessControlEntry[]>([]);
  const [entries, setEntries] = useState<AccessControlInput[]>([]);
  const [subjectKey, setSubjectKey] = useState("");
  const [loading, setLoading] = useState(false);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string>();

  useEffect(() => {
    if (!open) return;
    const controller = new AbortController();
    setLoading(true);
    setError(undefined);
    const aclRequest = resourceKind === "workspace"
      ? api.getWorkspaceAcl(resourceId, controller.signal)
      : api.getBoxAcl(resourceId, controller.signal);
    Promise.all([api.listMembers(controller.signal), api.listTeams(controller.signal), aclRequest]).then(
      ([nextMembers, nextTeams, nextEntries]) => {
        if (controller.signal.aborted) return;
        setMembers(nextMembers);
        setTeams(nextTeams);
        setLoadedEntries(nextEntries);
        setEntries(nextEntries.map((entry) => ({ userId: entry.userId, teamId: entry.teamId, role: entry.role })));
        setSubjectKey("");
        setLoading(false);
      },
      (requestError: unknown) => {
        if (controller.signal.aborted) return;
        setError(errorMessage(requestError));
        setLoading(false);
      }
    );
    return () => controller.abort();
  }, [open, resourceId, resourceKind]);

  const memberNames = useMemo(() => new Map(members.map((member) => [member.id, member])), [members]);
  const teamNames = useMemo(() => new Map(teams.map((team) => [team.id, team])), [teams]);
  const selectedSubjects = new Set(entries.map((entry) => entry.userId ? `user:${entry.userId}` : `team:${entry.teamId}`));
  const subjectOptions: SubjectOption[] = [
    ...members.map((member) => ({
      key: `user:${member.id}`,
      kind: "user" as const,
      id: member.id,
      label: member.displayName || member.login,
      detail: member.login
    })),
    ...teams.map((team) => ({
      key: `team:${team.id}`,
      kind: "team" as const,
      id: team.id,
      label: team.name,
      detail: `Team · ${team.slug}`
    }))
  ].filter((subject) => !selectedSubjects.has(subject.key));

  const directRole = loadedEntries.find((entry) => entry.userId === currentUser?.id)?.role;
  const canManage = roleAtLeast(currentUser?.role, "admin") || directRole === "owner";
  const hasOwner = entries.some((entry) => entry.role === "owner");

  const addSubject = () => {
    const subject = subjectOptions.find((candidate) => candidate.key === subjectKey);
    if (!subject) return;
    setEntries((current) => [
      ...current,
      subject.kind === "user" ? { userId: subject.id, role: "viewer" } : { teamId: subject.id, role: "viewer" }
    ]);
    setSubjectKey("");
  };

  const save = async () => {
    if (!canManage || !hasOwner) return;
    setSaving(true);
    setError(undefined);
    try {
      const nextEntries = resourceKind === "workspace"
        ? await api.replaceWorkspaceAcl(resourceId, { entries })
        : await api.replaceBoxAcl(resourceId, { entries });
      setLoadedEntries(nextEntries);
      setEntries(nextEntries.map((entry) => ({ userId: entry.userId, teamId: entry.teamId, role: entry.role })));
      notify(`Sharing for ${resourceName} was updated.`);
      onSaved?.();
      onClose();
    } catch (requestError) {
      setError(errorMessage(requestError));
    } finally {
      setSaving(false);
    }
  };

  return (
    <Modal
      open={open}
      onClose={onClose}
      title={`Share ${resourceName}`}
      description={`Give organization members and teams access to this ${resourceKind}.`}
      size="large"
    >
      <div className="acl-dialog">
        {error ? <InlineAlert>{error}</InlineAlert> : null}
        {loading ? (
          <div className="acl-dialog__loading"><span className="spinner" /><span>Loading access rules</span></div>
        ) : (
          <>
            <div className="acl-dialog__summary">
              <div><p className="eyebrow">Your permission</p><strong>{roleAtLeast(currentUser?.role, "admin") ? `${currentUser?.role} · organization-wide` : directRole ?? "viewer"}</strong></div>
              <StatusChip status={canManage ? "owner" : "read_only"} compact />
            </div>
            {!canManage ? <InlineAlert tone="warning">Sharing is read-only for you. An organization admin or resource owner can change these rules.</InlineAlert> : null}
            <div className="acl-list" role="list" aria-label="Access rules">
              {entries.map((entry, index) => {
                const member = entry.userId ? memberNames.get(entry.userId) : undefined;
                const team = entry.teamId ? teamNames.get(entry.teamId) : undefined;
                const label = member?.displayName || member?.login || team?.name || "Unknown subject";
                const detail = member ? member.login : team ? `Team · ${team.slug}` : entry.userId || entry.teamId;
                return (
                  <div className="acl-entry" role="listitem" key={entry.userId ? `user:${entry.userId}` : `team:${entry.teamId}`}>
                    <span className="acl-entry__icon"><Icon name={entry.userId ? "members" : "team"} /></span>
                    <span className="acl-entry__subject"><strong>{label}</strong><small>{detail}</small></span>
                    <label className="acl-entry__role"><span className="sr-only">Role for {label}</span><select value={entry.role} disabled={!canManage} onChange={(event) => setEntries((current) => current.map((candidate, candidateIndex) => candidateIndex === index ? { ...candidate, role: event.target.value as ResourceRole } : candidate))}><option value="owner">Owner</option><option value="operator">Operator</option><option value="viewer">Viewer</option></select></label>
                    {canManage ? <button className="icon-button" type="button" aria-label={`Remove ${label}`} title={`Remove ${label}`} onClick={() => setEntries((current) => current.filter((_, candidateIndex) => candidateIndex !== index))}><Icon name="close" /></button> : null}
                  </div>
                );
              })}
              {entries.length === 0 ? <p className="acl-list__empty">No explicit access rules.</p> : null}
            </div>
            {canManage ? (
              <div className="acl-add">
                <label className="field"><span>Add a member or team</span><select value={subjectKey} onChange={(event) => setSubjectKey(event.target.value)}><option value="">Select a subject</option><optgroup label="Members">{subjectOptions.filter((subject) => subject.kind === "user").map((subject) => <option value={subject.key} key={subject.key}>{subject.label} · {subject.detail}</option>)}</optgroup><optgroup label="Teams">{subjectOptions.filter((subject) => subject.kind === "team").map((subject) => <option value={subject.key} key={subject.key}>{subject.label} · {subject.detail}</option>)}</optgroup></select></label>
                <Button type="button" icon="plus" disabled={!subjectKey} onClick={addSubject}>Add</Button>
              </div>
            ) : null}
            {!hasOwner && canManage ? <InlineAlert tone="warning">At least one owner is required before these access rules can be saved.</InlineAlert> : null}
            <div className="modal__actions"><Button type="button" onClick={onClose}>{canManage ? "Cancel" : "Close"}</Button>{canManage ? <Button type="button" variant="primary" icon="check" busy={saving} disabled={!hasOwner} onClick={() => void save()}>Save sharing</Button> : null}</div>
          </>
        )}
      </div>
    </Modal>
  );
}
