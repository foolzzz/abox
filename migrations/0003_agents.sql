CREATE TABLE agents (
    id UUID PRIMARY KEY,
    organization_id UUID NOT NULL REFERENCES organizations(id),
    slug CITEXT NOT NULL,
    name TEXT NOT NULL,
    description TEXT,
    status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'archived')),
    published_version_id UUID,
    created_by_user_id UUID NOT NULL,
    version BIGINT NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    archived_at TIMESTAMPTZ,
    UNIQUE (organization_id, id),
    UNIQUE (organization_id, slug),
    FOREIGN KEY (organization_id, created_by_user_id)
        REFERENCES organization_members(organization_id, user_id)
);

CREATE TABLE agent_versions (
    id UUID PRIMARY KEY,
    organization_id UUID NOT NULL,
    agent_id UUID NOT NULL,
    version_no INTEGER NOT NULL CHECK (version_no > 0),
    lifecycle_status TEXT NOT NULL CHECK (lifecycle_status IN ('draft', 'published', 'retired')),
    runtime_type TEXT NOT NULL CHECK (runtime_type IN ('omp', 'claude', 'acp')),
    model TEXT,
    thinking_level TEXT,
    prompt_mode TEXT NOT NULL DEFAULT 'append' CHECK (prompt_mode IN ('append', 'replace')),
    system_prompt TEXT NOT NULL DEFAULT '',
    tool_policy JSONB NOT NULL DEFAULT '{}'::jsonb,
    skill_policy JSONB NOT NULL DEFAULT '{}'::jsonb,
    approval_policy JSONB NOT NULL DEFAULT '{}'::jsonb,
    runtime_config JSONB NOT NULL DEFAULT '{}'::jsonb,
    idle_timeout_seconds INTEGER NOT NULL DEFAULT 1800 CHECK (idle_timeout_seconds BETWEEN 60 AND 604800),
    created_by_user_id UUID NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    published_at TIMESTAMPTZ,
    UNIQUE (organization_id, id),
    UNIQUE (agent_id, version_no),
    UNIQUE (agent_id, id),
    UNIQUE (organization_id, agent_id, id),
    FOREIGN KEY (organization_id, agent_id) REFERENCES agents(organization_id, id),
    FOREIGN KEY (organization_id, created_by_user_id)
        REFERENCES organization_members(organization_id, user_id)
);

ALTER TABLE agents
    ADD CONSTRAINT agents_published_version_fk
    FOREIGN KEY (id, published_version_id) REFERENCES agent_versions(agent_id, id);

CREATE FUNCTION enforce_agent_version_immutability() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.lifecycle_status = 'published' AND NEW.lifecycle_status NOT IN ('published', 'retired') THEN
        RAISE EXCEPTION 'published agent version cannot return to %', NEW.lifecycle_status USING ERRCODE = '23514';
    END IF;
    IF OLD.lifecycle_status = 'retired' AND NEW.lifecycle_status <> 'retired' THEN
        RAISE EXCEPTION 'retired agent version is immutable' USING ERRCODE = '23514';
    END IF;
    IF OLD.lifecycle_status <> 'draft' AND ROW(
        NEW.organization_id, NEW.agent_id, NEW.version_no, NEW.runtime_type, NEW.model,
        NEW.thinking_level, NEW.prompt_mode, NEW.system_prompt, NEW.tool_policy,
        NEW.skill_policy, NEW.approval_policy, NEW.runtime_config,
        NEW.idle_timeout_seconds, NEW.created_by_user_id, NEW.created_at, NEW.published_at
    ) IS DISTINCT FROM ROW(
        OLD.organization_id, OLD.agent_id, OLD.version_no, OLD.runtime_type, OLD.model,
        OLD.thinking_level, OLD.prompt_mode, OLD.system_prompt, OLD.tool_policy,
        OLD.skill_policy, OLD.approval_policy, OLD.runtime_config,
        OLD.idle_timeout_seconds, OLD.created_by_user_id, OLD.created_at, OLD.published_at
    ) THEN
        RAISE EXCEPTION 'published agent version configuration is immutable' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER agent_versions_immutable_after_publish
BEFORE UPDATE ON agent_versions
FOR EACH ROW EXECUTE FUNCTION enforce_agent_version_immutability();
