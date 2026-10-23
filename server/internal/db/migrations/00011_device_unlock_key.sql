-- +goose Up
-- The device's unlock key (PROTOCOL §4.2) as a browser received it in a
-- session, signed by the device key, so sign-ins can be saved outside a
-- session. The server checks the signature; browsers check it again against
-- the device key they pinned.
ALTER TABLE devices
    ADD COLUMN unlock_key     bytea,
    ADD COLUMN unlock_key_sig bytea;

-- +goose Down
ALTER TABLE devices DROP COLUMN unlock_key, DROP COLUMN unlock_key_sig;
