-- +goose Up
-- Saved sign-in (PROTOCOL §4.2): a username and password a browser sealed to
-- a device's unlock key and signed with its client key. Only the device can
-- open it; the server keeps the username in clear for the form.
CREATE TABLE device_credentials (
    device_id     uuid NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
    user_id       uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    client_key_id uuid NOT NULL REFERENCES client_keys(id) ON DELETE CASCADE,
    username      text NOT NULL,
    sealed        jsonb NOT NULL,
    updated_at    timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (device_id, user_id)
);
CREATE INDEX device_credentials_client_key ON device_credentials (client_key_id);

-- +goose Down
DROP TABLE device_credentials;
