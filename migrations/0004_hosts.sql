CREATE TABLE hosts (
    id UUID PRIMARY KEY,
    organization_id UUID NOT NULL REFERENCES organizations(id),
    slug CITEXT NOT NULL,
    name TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'enrolling' CHECK (status IN ('enrolling', 'online', 'draining', 'offline', 'revoked')),
    os TEXT,
    arch TEXT,
    daemon_version TEXT,
    current_daemon_instance_id UUID,
    last_acked_host_seq BIGINT NOT NULL DEFAULT 0 CHECK (last_acked_host_seq >= 0),
    tailscale_node_id TEXT,
    tailscale_dns_name TEXT,
    labels JSONB NOT NULL DEFAULT '{}'::jsonb,
    max_active_boxes INTEGER NOT NULL DEFAULT 4 CHECK (max_active_boxes BETWEEN 1 AND 256),
    last_seen_at TIMESTAMPTZ,
    drain_started_at TIMESTAMPTZ,
    created_by_user_id UUID NOT NULL,
    version BIGINT NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    revoked_at TIMESTAMPTZ,
    UNIQUE (organization_id, id),
    UNIQUE (organization_id, slug),
    FOREIGN KEY (organization_id, created_by_user_id)
        REFERENCES organization_members(organization_id, user_id)
);

CREATE TABLE host_credentials (
    id UUID PRIMARY KEY,
    organization_id UUID NOT NULL,
    host_id UUID NOT NULL,
    credential_hash BYTEA NOT NULL,
    key_id TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('active', 'rotating', 'revoked', 'expired')),
    issued_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at TIMESTAMPTZ,
    last_used_at TIMESTAMPTZ,
    revoked_at TIMESTAMPTZ,
    UNIQUE (organization_id, id),
    UNIQUE (host_id, key_id),
    FOREIGN KEY (organization_id, host_id) REFERENCES hosts(organization_id, id)
);

CREATE TABLE host_runtime_capabilities (
    organization_id UUID NOT NULL,
    host_id UUID NOT NULL,
    runtime_name TEXT NOT NULL CHECK (runtime_name IN ('omp', 'claude', 'acp')),
    runtime_version TEXT,
    binary_path TEXT,
    capabilities JSONB NOT NULL DEFAULT '{}'::jsonb,
    status TEXT NOT NULL CHECK (status IN ('available', 'unavailable', 'degraded')),
    discovered_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_checked_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    error_message TEXT,
    PRIMARY KEY (host_id, runtime_name),
    FOREIGN KEY (organization_id, host_id) REFERENCES hosts(organization_id, id)
);

CREATE TABLE host_workspace_roots (
    id UUID PRIMARY KEY,
    organization_id UUID NOT NULL,
    host_id UUID NOT NULL,
    display_path TEXT NOT NULL,
    real_path TEXT NOT NULL,
    mode TEXT NOT NULL CHECK (mode IN ('existing', 'managed', 'both')),
    enabled BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (host_id, real_path),
    UNIQUE (organization_id, host_id, id),
    FOREIGN KEY (organization_id, host_id) REFERENCES hosts(organization_id, id)
);
