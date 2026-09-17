ALTER TABLE messages
    ADD COLUMN IF NOT EXISTS presentation_context jsonb NOT NULL DEFAULT '{}'::jsonb;
