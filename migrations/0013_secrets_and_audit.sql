CREATE TABLE secret_refs (
    id UUID PRIMARY KEY,
    organization_id UUID NOT NULL,
    name CITEXT NOT NULL,
    provider TEXT NOT NULL CHECK (provider IN ('host_env', 'server_encrypted', 'external')),
    locator TEXT NOT NULL,
    host_id UUID,
    status TEXT NOT NULL CHECK (status IN ('active', 'revoked')),
    created_by_user_id UUID NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    revoked_at TIMESTAMPTZ,
    UNIQUE (organization_id, id),
    UNIQUE (organization_id, name),
    FOREIGN KEY (organization_id, host_id) REFERENCES hosts(organization_id, id),
    FOREIGN KEY (organization_id, created_by_user_id)
        REFERENCES organization_members(organization_id, user_id),
    CHECK (provider <> 'host_env' OR locator ~ '^[A-Za-z_][A-Za-z0-9_]*$')
);

CREATE TABLE box_secret_bindings (
    organization_id UUID NOT NULL,
    box_id UUID NOT NULL,
    secret_ref_id UUID NOT NULL,
    env_name CITEXT NOT NULL,
    created_by_user_id UUID NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (box_id, secret_ref_id),
    UNIQUE (box_id, env_name),
    FOREIGN KEY (organization_id, box_id) REFERENCES boxes(organization_id, id),
    FOREIGN KEY (organization_id, secret_ref_id) REFERENCES secret_refs(organization_id, id),
    FOREIGN KEY (organization_id, created_by_user_id)
        REFERENCES organization_members(organization_id, user_id),
    CHECK (env_name ~ '^[A-Za-z_][A-Za-z0-9_]*$')
);

CREATE TABLE audit_logs (
    id UUID PRIMARY KEY,
    organization_id UUID NOT NULL REFERENCES organizations(id),
    actor_type TEXT NOT NULL CHECK (actor_type IN ('user', 'system', 'daemon')),
    actor_user_id UUID,
    actor_host_id UUID,
    action TEXT NOT NULL,
    resource_type TEXT NOT NULL,
    resource_id UUID,
    request_id TEXT,
    tailscale_node_id TEXT,
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    occurred_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    FOREIGN KEY (organization_id, actor_user_id)
        REFERENCES organization_members(organization_id, user_id),
    FOREIGN KEY (organization_id, actor_host_id) REFERENCES hosts(organization_id, id),
    CHECK (
        (actor_type = 'user' AND actor_user_id IS NOT NULL AND actor_host_id IS NULL) OR
        (actor_type = 'daemon' AND actor_host_id IS NOT NULL AND actor_user_id IS NULL) OR
        (actor_type = 'system' AND actor_user_id IS NULL AND actor_host_id IS NULL)
    )
);
