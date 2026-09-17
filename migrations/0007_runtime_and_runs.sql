CREATE TABLE runtime_instances (
    id UUID PRIMARY KEY,
    organization_id UUID NOT NULL,
    box_id UUID NOT NULL,
    host_id UUID NOT NULL,
    runtime_type TEXT NOT NULL CHECK (runtime_type IN ('omp', 'claude', 'acp')),
    runtime_version TEXT,
    daemon_instance_id UUID NOT NULL,
    process_id INTEGER,
    process_group_id INTEGER,
    session_ref TEXT,
    status TEXT NOT NULL CHECK (status IN ('starting', 'ready', 'busy', 'stopping', 'exited')),
    capabilities JSONB NOT NULL DEFAULT '{}'::jsonb,
    config_snapshot JSONB NOT NULL DEFAULT '{}'::jsonb,
    started_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    ready_at TIMESTAMPTZ,
    last_event_at TIMESTAMPTZ,
    stopped_at TIMESTAMPTZ,
    exit_code INTEGER,
    exit_signal TEXT,
    terminal_reason TEXT,
    version BIGINT NOT NULL DEFAULT 1 CHECK (version > 0),
    UNIQUE (organization_id, box_id, id),
    FOREIGN KEY (organization_id, box_id, host_id)
        REFERENCES boxes(organization_id, id, host_id)
);

CREATE TABLE runs (
    id UUID PRIMARY KEY,
    organization_id UUID NOT NULL,
    box_id UUID NOT NULL,
    runtime_instance_id UUID,
    trigger_message_id UUID NOT NULL,
    source_type TEXT NOT NULL CHECK (source_type IN ('interactive', 'schedule', 'webhook', 'system')),
    source_ref_id UUID,
    status TEXT NOT NULL CHECK (status IN (
        'queued', 'dispatching', 'running', 'waiting_approval', 'interrupting',
        'disconnected', 'succeeded', 'failed', 'aborted', 'lost', 'cancelled'
    )),
    priority SMALLINT NOT NULL DEFAULT 100,
    queued_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    dispatch_started_at TIMESTAMPTZ,
    started_at TIMESTAMPTZ,
    finished_at TIMESTAMPTZ,
    terminal_reason TEXT,
    error_code TEXT,
    error_message TEXT,
    usage JSONB NOT NULL DEFAULT '{}'::jsonb,
    version BIGINT NOT NULL DEFAULT 1 CHECK (version > 0),
    UNIQUE (organization_id, box_id, id),
    FOREIGN KEY (organization_id, box_id) REFERENCES boxes(organization_id, id),
    FOREIGN KEY (organization_id, box_id, runtime_instance_id)
        REFERENCES runtime_instances(organization_id, box_id, id)
);
