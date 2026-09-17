CREATE TABLE schedules (
    id UUID PRIMARY KEY,
    organization_id UUID NOT NULL,
    name TEXT NOT NULL,
    agent_id UUID NOT NULL,
    agent_version_id UUID NOT NULL,
    host_id UUID NOT NULL,
    workspace_id UUID NOT NULL,
    cron_expression TEXT NOT NULL,
    timezone TEXT NOT NULL,
    prompt_template TEXT NOT NULL,
    concurrency_policy TEXT NOT NULL CHECK (concurrency_policy IN ('skip', 'queue', 'replace')),
    status TEXT NOT NULL CHECK (status IN ('active', 'paused', 'deleted')),
    next_run_at TIMESTAMPTZ,
    last_run_at TIMESTAMPTZ,
    created_by_user_id UUID NOT NULL,
    version BIGINT NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (organization_id, id),
    FOREIGN KEY (organization_id, agent_id, agent_version_id)
        REFERENCES agent_versions(organization_id, agent_id, id),
    FOREIGN KEY (organization_id, host_id, workspace_id)
        REFERENCES workspaces(organization_id, host_id, id),
    FOREIGN KEY (organization_id, created_by_user_id)
        REFERENCES organization_members(organization_id, user_id),
    CHECK ((status = 'active') = (next_run_at IS NOT NULL))
);

CREATE TABLE schedule_executions (
    id UUID PRIMARY KEY,
    organization_id UUID NOT NULL,
    schedule_id UUID NOT NULL,
    scheduled_for TIMESTAMPTZ NOT NULL,
    box_id UUID,
    run_id UUID,
    status TEXT NOT NULL CHECK (status IN ('claimed', 'skipped', 'dispatched', 'completed', 'failed')),
    reason TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (organization_id, id),
    UNIQUE (schedule_id, scheduled_for),
    FOREIGN KEY (organization_id, schedule_id) REFERENCES schedules(organization_id, id),
    FOREIGN KEY (organization_id, box_id) REFERENCES boxes(organization_id, id),
    FOREIGN KEY (organization_id, box_id, run_id) REFERENCES runs(organization_id, box_id, id),
    CHECK (run_id IS NULL OR box_id IS NOT NULL)
);
