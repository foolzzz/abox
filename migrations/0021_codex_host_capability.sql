ALTER TABLE host_runtime_capabilities
    DROP CONSTRAINT host_runtime_capabilities_runtime_name_check,
    ADD CONSTRAINT host_runtime_capabilities_runtime_name_check
        CHECK (runtime_name IN ('omp', 'codex', 'claude', 'acp'));
