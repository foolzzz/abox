ALTER TABLE boxes
    ADD COLUMN model_override TEXT;

ALTER TABLE boxes
    ADD CONSTRAINT boxes_model_override_length_check
    CHECK (model_override IS NULL OR char_length(model_override) BETWEEN 1 AND 128);
