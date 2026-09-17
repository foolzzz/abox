ALTER TABLE host_commands
    DROP CONSTRAINT host_commands_command_type_check;

ALTER TABLE host_commands
    ADD CONSTRAINT host_commands_command_type_check CHECK (command_type IN (
        'runtime.start', 'runtime.prompt', 'runtime.steer', 'runtime.follow_up',
        'runtime.interrupt', 'runtime.approval_response', 'runtime.stop', 'runtime.inspect',
        'workspace.validate', 'workspace.git_diff'
    ));
