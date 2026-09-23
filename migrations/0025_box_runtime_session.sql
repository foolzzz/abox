ALTER TABLE boxes
    ADD COLUMN runtime_session_mode TEXT NOT NULL DEFAULT 'new',
    ADD COLUMN runtime_session_ref TEXT;

ALTER TABLE boxes
    ADD CONSTRAINT boxes_runtime_session_mode_check
    CHECK (runtime_session_mode IN ('new', 'resume')),
    ADD CONSTRAINT boxes_runtime_session_ref_length_check
    CHECK (runtime_session_ref IS NULL OR char_length(runtime_session_ref) BETWEEN 1 AND 256),
    ADD CONSTRAINT boxes_runtime_session_shape_check
    CHECK (
        (runtime_session_mode = 'new' AND runtime_session_ref IS NULL)
        OR
        (runtime_session_mode = 'resume' AND runtime_session_ref IS NOT NULL)
    );
