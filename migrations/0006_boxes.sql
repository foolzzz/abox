CREATE TABLE boxes (
    id UUID PRIMARY KEY,
    organization_id UUID NOT NULL REFERENCES organizations(id),
    name TEXT NOT NULL,
    agent_id UUID NOT NULL,
    agent_version_id UUID NOT NULL,
    host_id UUID NOT NULL,
    workspace_id UUID NOT NULL,
    owner_user_id UUID NOT NULL,
    visibility TEXT NOT NULL DEFAULT 'private' CHECK (visibility IN ('private', 'org')),
    status TEXT NOT NULL DEFAULT 'created' CHECK (status IN (
        'created', 'starting', 'idle', 'running', 'waiting_approval',
        'hibernating', 'hibernated', 'error', 'terminated'
    )),
    runtime_type TEXT NOT NULL CHECK (runtime_type IN ('omp', 'claude', 'acp')),
    idle_timeout_seconds INTEGER NOT NULL DEFAULT 1800 CHECK (idle_timeout_seconds BETWEEN 60 AND 604800),
    next_message_seq BIGINT NOT NULL DEFAULT 1 CHECK (next_message_seq > 0),
    next_event_seq BIGINT NOT NULL DEFAULT 1 CHECK (next_event_seq > 0),
    version BIGINT NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_activity_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    terminated_at TIMESTAMPTZ,
    UNIQUE (organization_id, id),
    UNIQUE (organization_id, id, host_id),
    FOREIGN KEY (organization_id, agent_id) REFERENCES agents(organization_id, id),
    FOREIGN KEY (organization_id, agent_id, agent_version_id)
        REFERENCES agent_versions(organization_id, agent_id, id),
    FOREIGN KEY (organization_id, host_id) REFERENCES hosts(organization_id, id),
    FOREIGN KEY (organization_id, host_id, workspace_id)
        REFERENCES workspaces(organization_id, host_id, id),
    FOREIGN KEY (organization_id, owner_user_id)
        REFERENCES organization_members(organization_id, user_id)
);

CREATE TABLE box_acl (
    id UUID PRIMARY KEY,
    organization_id UUID NOT NULL,
    box_id UUID NOT NULL,
    user_id UUID,
    team_id UUID,
    role TEXT NOT NULL CHECK (role IN ('owner', 'operator', 'viewer')),
    created_by_user_id UUID NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    FOREIGN KEY (organization_id, box_id) REFERENCES boxes(organization_id, id),
    FOREIGN KEY (organization_id, user_id)
        REFERENCES organization_members(organization_id, user_id),
    FOREIGN KEY (organization_id, team_id) REFERENCES teams(organization_id, id),
    FOREIGN KEY (organization_id, created_by_user_id)
        REFERENCES organization_members(organization_id, user_id),
    CHECK (num_nonnulls(user_id, team_id) = 1)
);
