ALTER TABLE host_commands
    DROP CONSTRAINT host_commands_command_type_check;

ALTER TABLE host_commands
    ADD CONSTRAINT host_commands_command_type_check CHECK (command_type IN (
        'runtime.start', 'runtime.prompt', 'runtime.steer', 'runtime.follow_up',
        'runtime.interrupt', 'runtime.approval_response', 'runtime.stop', 'runtime.inspect',
        'workspace.git_diff'
    )),
    ADD COLUMN result_json JSONB;

ALTER TABLE schedule_executions
    ADD COLUMN trigger_type TEXT NOT NULL DEFAULT 'schedule'
        CHECK (trigger_type IN ('schedule', 'webhook')),
    ADD COLUMN trigger_key TEXT,
    ADD COLUMN finished_at TIMESTAMPTZ;

CREATE UNIQUE INDEX schedule_executions_trigger_uk
    ON schedule_executions(schedule_id, trigger_type, trigger_key)
    WHERE trigger_key IS NOT NULL;

CREATE TABLE webhook_deliveries (
    id UUID PRIMARY KEY,
    organization_id UUID NOT NULL,
    schedule_id UUID NOT NULL,
    execution_id UUID NOT NULL,
    idempotency_key TEXT NOT NULL,
    body_sha256 TEXT NOT NULL CHECK (body_sha256 ~ '^[0-9a-f]{64}$'),
    received_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (schedule_id, idempotency_key),
    FOREIGN KEY (organization_id, schedule_id) REFERENCES schedules(organization_id, id),
    FOREIGN KEY (organization_id, execution_id) REFERENCES schedule_executions(organization_id, id)
);

CREATE TABLE notifications (
    id UUID PRIMARY KEY,
    organization_id UUID NOT NULL,
    user_id UUID NOT NULL,
    type TEXT NOT NULL,
    title TEXT NOT NULL,
    body TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'unread' CHECK (status IN ('unread', 'read')),
    box_id UUID,
    run_id UUID,
    schedule_id UUID,
    approval_id UUID,
    dedupe_key TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    read_at TIMESTAMPTZ,
    UNIQUE (organization_id, id),
    UNIQUE (organization_id, user_id, dedupe_key),
    FOREIGN KEY (organization_id, user_id)
        REFERENCES organization_members(organization_id, user_id),
    FOREIGN KEY (organization_id, box_id) REFERENCES boxes(organization_id, id),
    FOREIGN KEY (organization_id, box_id, run_id) REFERENCES runs(organization_id, box_id, id),
    FOREIGN KEY (organization_id, schedule_id) REFERENCES schedules(organization_id, id),
    FOREIGN KEY (approval_id) REFERENCES approvals(id),
    CHECK (run_id IS NULL OR box_id IS NOT NULL)
);

CREATE INDEX notifications_user_status_created_idx
    ON notifications(organization_id, user_id, status, created_at DESC);

CREATE TABLE workspace_diff_requests (
    id UUID PRIMARY KEY,
    organization_id UUID NOT NULL,
    box_id UUID NOT NULL,
    workspace_id UUID NOT NULL,
    host_id UUID NOT NULL,
    command_id UUID NOT NULL UNIQUE,
    requested_by_user_id UUID NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'completed', 'failed')),
    base_ref TEXT,
    head_ref TEXT,
    result JSONB,
    error_code TEXT,
    error_message TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    generated_at TIMESTAMPTZ,
    UNIQUE (organization_id, id),
    FOREIGN KEY (organization_id, box_id, host_id)
        REFERENCES boxes(organization_id, id, host_id),
    FOREIGN KEY (organization_id, host_id, workspace_id)
        REFERENCES workspaces(organization_id, host_id, id),
    FOREIGN KEY (command_id) REFERENCES host_commands(id),
    FOREIGN KEY (organization_id, requested_by_user_id)
        REFERENCES organization_members(organization_id, user_id),
    CHECK ((status = 'completed') = (result IS NOT NULL))
);

CREATE INDEX workspace_diff_requests_box_created_idx
    ON workspace_diff_requests(box_id, created_at DESC);
