CREATE TABLE messages (
    id UUID PRIMARY KEY,
    organization_id UUID NOT NULL,
    box_id UUID NOT NULL,
    run_id UUID,
    box_seq BIGINT NOT NULL CHECK (box_seq > 0),
    author_type TEXT NOT NULL CHECK (author_type IN ('user', 'agent', 'system', 'scheduler', 'webhook')),
    author_user_id UUID,
    role TEXT NOT NULL CHECK (role IN ('user', 'assistant', 'system')),
    delivery TEXT CHECK (delivery IS NULL OR delivery IN ('prompt', 'steer', 'follow_up')),
    status TEXT NOT NULL CHECK (status IN ('accepted', 'queued', 'dispatched', 'applied', 'rejected', 'cancelled')),
    content JSONB NOT NULL,
    plain_text TEXT,
    idempotency_key TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    applied_at TIMESTAMPTZ,
    UNIQUE (organization_id, id),
    UNIQUE (organization_id, box_id, id),
    UNIQUE (box_id, box_seq),
    FOREIGN KEY (organization_id, box_id) REFERENCES boxes(organization_id, id),
    FOREIGN KEY (organization_id, box_id, run_id) REFERENCES runs(organization_id, box_id, id),
    FOREIGN KEY (organization_id, author_user_id)
        REFERENCES organization_members(organization_id, user_id),
    CHECK ((author_type = 'user') = (author_user_id IS NOT NULL))
);

ALTER TABLE runs
    ADD CONSTRAINT runs_trigger_message_fk
    FOREIGN KEY (organization_id, box_id, trigger_message_id)
    REFERENCES messages(organization_id, box_id, id)
    DEFERRABLE INITIALLY DEFERRED;
