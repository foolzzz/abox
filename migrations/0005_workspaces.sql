CREATE TABLE workspaces (
    id UUID PRIMARY KEY,
    organization_id UUID NOT NULL REFERENCES organizations(id),
    host_id UUID NOT NULL,
    workspace_root_id UUID NOT NULL,
    name TEXT NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('existing', 'managed', 'git_worktree')),
    display_path TEXT NOT NULL,
    real_path TEXT NOT NULL,
    repository_url TEXT,
    git_branch TEXT,
    status TEXT NOT NULL CHECK (status IN ('provisioning', 'ready', 'error', 'archived')),
    created_by_user_id UUID NOT NULL,
    version BIGINT NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    archived_at TIMESTAMPTZ,
    UNIQUE (organization_id, id),
    UNIQUE (organization_id, host_id, id),
    UNIQUE (host_id, real_path),
    FOREIGN KEY (organization_id, host_id) REFERENCES hosts(organization_id, id),
    FOREIGN KEY (organization_id, host_id, workspace_root_id)
        REFERENCES host_workspace_roots(organization_id, host_id, id),
    FOREIGN KEY (organization_id, created_by_user_id)
        REFERENCES organization_members(organization_id, user_id)
);

CREATE TABLE workspace_acl (
    id UUID PRIMARY KEY,
    organization_id UUID NOT NULL,
    workspace_id UUID NOT NULL,
    user_id UUID,
    team_id UUID,
    role TEXT NOT NULL CHECK (role IN ('owner', 'operator', 'viewer')),
    created_by_user_id UUID NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    FOREIGN KEY (organization_id, workspace_id) REFERENCES workspaces(organization_id, id),
    FOREIGN KEY (organization_id, user_id)
        REFERENCES organization_members(organization_id, user_id),
    FOREIGN KEY (organization_id, team_id) REFERENCES teams(organization_id, id),
    FOREIGN KEY (organization_id, created_by_user_id)
        REFERENCES organization_members(organization_id, user_id),
    CHECK (num_nonnulls(user_id, team_id) = 1)
);
