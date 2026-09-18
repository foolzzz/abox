ALTER TABLE users
    ADD COLUMN username CITEXT,
    ADD COLUMN password_hash TEXT,
    ADD COLUMN must_change_password BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN password_changed_at TIMESTAMPTZ;

ALTER TABLE users ALTER COLUMN tailscale_login DROP NOT NULL;

CREATE UNIQUE INDEX users_username_unique
    ON users(username);

ALTER TABLE organization_members
    DROP CONSTRAINT IF EXISTS organization_members_role_check;

UPDATE organization_members
SET role = CASE
    WHEN role IN ('owner', 'admin') THEN 'admin'
    ELSE 'user'
END;

UPDATE users
SET status = 'disabled'
WHERE username IS NULL;

ALTER TABLE organization_members
    ADD CONSTRAINT organization_members_role_check
    CHECK (role IN ('admin', 'user'));

CREATE TABLE user_sessions (
    id UUID PRIMARY KEY,
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash BYTEA NOT NULL UNIQUE CHECK (octet_length(token_hash) = 32),
    expires_at TIMESTAMPTZ NOT NULL,
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (expires_at > created_at)
);

CREATE INDEX user_sessions_user_expires_idx
    ON user_sessions(user_id, expires_at);
