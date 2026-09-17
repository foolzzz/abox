CREATE TABLE approvals (
    id UUID PRIMARY KEY,
    organization_id UUID NOT NULL,
    box_id UUID NOT NULL,
    run_id UUID NOT NULL,
    runtime_instance_id UUID NOT NULL,
    runtime_request_id TEXT NOT NULL,
    tool_name TEXT NOT NULL,
    risk_level TEXT NOT NULL CHECK (risk_level IN ('low', 'medium', 'high', 'critical')),
    status TEXT NOT NULL CHECK (status IN ('pending', 'approved', 'denied', 'expired', 'cancelled')),
    requested_payload JSONB NOT NULL,
    decision_payload JSONB,
    requested_at TIMESTAMPTZ NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    resolved_at TIMESTAMPTZ,
    resolved_by_user_id UUID,
    version BIGINT NOT NULL DEFAULT 1 CHECK (version > 0),
    UNIQUE (runtime_instance_id, runtime_request_id),
    FOREIGN KEY (organization_id, box_id) REFERENCES boxes(organization_id, id),
    FOREIGN KEY (organization_id, box_id, run_id) REFERENCES runs(organization_id, box_id, id),
    FOREIGN KEY (organization_id, box_id, runtime_instance_id)
        REFERENCES runtime_instances(organization_id, box_id, id),
    FOREIGN KEY (organization_id, resolved_by_user_id)
        REFERENCES organization_members(organization_id, user_id)
);

CREATE TABLE subagent_instances (
    id UUID PRIMARY KEY,
    organization_id UUID NOT NULL,
    box_id UUID NOT NULL,
    run_id UUID NOT NULL,
    runtime_instance_id UUID NOT NULL,
    external_agent_id TEXT NOT NULL,
    parent_external_agent_id TEXT,
    agent_type TEXT,
    label TEXT,
    status TEXT NOT NULL CHECK (status IN ('running', 'idle', 'completed', 'failed', 'cancelled', 'parked')),
    session_ref TEXT,
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    started_at TIMESTAMPTZ NOT NULL,
    finished_at TIMESTAMPTZ,
    UNIQUE (runtime_instance_id, external_agent_id),
    FOREIGN KEY (organization_id, box_id, run_id) REFERENCES runs(organization_id, box_id, id),
    FOREIGN KEY (organization_id, box_id, runtime_instance_id)
        REFERENCES runtime_instances(organization_id, box_id, id)
);

CREATE TABLE todo_items (
    id UUID PRIMARY KEY,
    organization_id UUID NOT NULL,
    box_id UUID NOT NULL,
    run_id UUID,
    runtime_instance_id UUID NOT NULL,
    external_todo_id TEXT NOT NULL,
    phase_name TEXT,
    content TEXT NOT NULL,
    position INTEGER NOT NULL CHECK (position >= 0),
    status TEXT NOT NULL CHECK (status IN ('pending', 'in_progress', 'completed', 'blocked', 'abandoned')),
    block_reason TEXT,
    updated_at TIMESTAMPTZ NOT NULL,
    UNIQUE (runtime_instance_id, external_todo_id),
    FOREIGN KEY (organization_id, box_id) REFERENCES boxes(organization_id, id),
    FOREIGN KEY (organization_id, box_id, run_id) REFERENCES runs(organization_id, box_id, id),
    FOREIGN KEY (organization_id, box_id, runtime_instance_id)
        REFERENCES runtime_instances(organization_id, box_id, id)
);
