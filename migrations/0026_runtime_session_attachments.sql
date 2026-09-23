ALTER TABLE boxes
    DROP CONSTRAINT boxes_runtime_session_mode_check,
    DROP CONSTRAINT boxes_runtime_session_shape_check;

ALTER TABLE boxes
    ADD CONSTRAINT boxes_runtime_session_mode_check
    CHECK (runtime_session_mode IN ('new', 'resume', 'attach')),
    ADD CONSTRAINT boxes_runtime_session_shape_check
    CHECK (
        (runtime_session_mode = 'new' AND runtime_session_ref IS NULL)
        OR
        (runtime_session_mode IN ('resume', 'attach') AND runtime_session_ref IS NOT NULL)
    );

CREATE TABLE runtime_session_attachments (
    id UUID PRIMARY KEY,
    organization_id UUID NOT NULL,
    host_id UUID NOT NULL,
    box_id UUID NOT NULL,
    runtime_type TEXT NOT NULL,
    session_ref TEXT NOT NULL,
    attach_mode TEXT NOT NULL CHECK (attach_mode IN ('resume', 'attach')),
    input_policy TEXT NOT NULL DEFAULT 'shared' CHECK (input_policy IN ('shared', 'single_writer')),
    status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'detached')),
    created_by_user_id UUID NOT NULL,
    attached_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    detached_at TIMESTAMPTZ,
    UNIQUE (organization_id, id),
    FOREIGN KEY (organization_id, host_id) REFERENCES hosts(organization_id, id),
    FOREIGN KEY (organization_id, box_id) REFERENCES boxes(organization_id, id),
    FOREIGN KEY (organization_id, created_by_user_id) REFERENCES organization_members(organization_id, user_id)
);

CREATE UNIQUE INDEX runtime_session_attachments_one_active_per_box
    ON runtime_session_attachments(box_id)
    WHERE status = 'active';

CREATE INDEX runtime_session_attachments_session_idx
    ON runtime_session_attachments(organization_id, host_id, runtime_type, session_ref, status);

INSERT INTO runtime_session_attachments(
    id, organization_id, host_id, box_id, runtime_type, session_ref,
    attach_mode, status, created_by_user_id
)
SELECT gen_random_uuid(), organization_id, host_id, id, runtime_type, runtime_session_ref,
       runtime_session_mode, 'active', owner_user_id
FROM boxes
WHERE runtime_session_mode = 'resume' AND runtime_session_ref IS NOT NULL
  AND status <> 'terminated';
