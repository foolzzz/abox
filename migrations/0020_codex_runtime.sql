ALTER TABLE agent_versions
    DROP CONSTRAINT agent_versions_runtime_type_check,
    ADD CONSTRAINT agent_versions_runtime_type_check
        CHECK (runtime_type IN ('omp', 'codex', 'claude', 'acp'));

ALTER TABLE boxes
    DROP CONSTRAINT boxes_runtime_type_check,
    ADD CONSTRAINT boxes_runtime_type_check
        CHECK (runtime_type IN ('omp', 'codex', 'claude', 'acp'));

ALTER TABLE runtime_instances
    DROP CONSTRAINT runtime_instances_runtime_type_check,
    ADD CONSTRAINT runtime_instances_runtime_type_check
        CHECK (runtime_type IN ('omp', 'codex', 'claude', 'acp'));
