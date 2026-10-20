-- +goose Up
-- Last accepted TOTP time step (unix/30); a code is only accepted for a later step.
ALTER TABLE users ADD COLUMN totp_last_step bigint;

-- +goose Down
ALTER TABLE users DROP COLUMN totp_last_step;
