CREATE TABLE artifacts (
    id UUID PRIMARY KEY,
    organization_id UUID NOT NULL,
    box_id UUID NOT NULL,
    run_id UUID,
    host_id UUID NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('file', 'image', 'report', 'archive', 'log', 'other')),
    name TEXT NOT NULL,
    mime_type TEXT,
    storage_backend TEXT NOT NULL CHECK (storage_backend IN ('host', 'object')),
    host_path TEXT,
    object_key TEXT,
    size_bytes BIGINT NOT NULL CHECK (size_bytes >= 0),
    sha256 TEXT NOT NULL CHECK (sha256 ~ '^[0-9a-f]{64}$'),
    status TEXT NOT NULL CHECK (status IN ('ready', 'deleted')),
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at TIMESTAMPTZ,
    deleted_at TIMESTAMPTZ,
    UNIQUE (organization_id, id),
    FOREIGN KEY (organization_id, box_id, host_id)
        REFERENCES boxes(organization_id, id, host_id),
    FOREIGN KEY (organization_id, box_id, run_id) REFERENCES runs(organization_id, box_id, id),
    CHECK (
        (storage_backend = 'host' AND host_path IS NOT NULL AND object_key IS NULL) OR
        (storage_backend = 'object' AND object_key IS NOT NULL AND host_path IS NULL)
    )
);

CREATE TABLE message_artifacts (
    organization_id UUID NOT NULL,
    message_id UUID NOT NULL,
    artifact_id UUID NOT NULL,
    position INTEGER NOT NULL CHECK (position >= 0),
    PRIMARY KEY (message_id, artifact_id),
    UNIQUE (message_id, position),
    FOREIGN KEY (organization_id, message_id) REFERENCES messages(organization_id, id),
    FOREIGN KEY (organization_id, artifact_id) REFERENCES artifacts(organization_id, id)
);
