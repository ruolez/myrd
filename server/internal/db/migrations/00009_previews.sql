-- +goose Up
-- Screen previews (PROTOCOL §3, §4.1). Each browser registers an ECDH
-- preview key vouched for by its client key; agents seal previews to it.
-- The server keeps the latest sealed preview per device and browser key for
-- the dashboard's first paint, and can read none of them.
ALTER TABLE client_keys
    ADD COLUMN preview_key        bytea,
    ADD COLUMN preview_key_sha256 text,
    ADD COLUMN preview_key_sig    bytea;

ALTER TABLE devices ADD COLUMN previews boolean NOT NULL DEFAULT true;

CREATE TABLE device_previews (
    device_id          uuid NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
    client_key_id      uuid NOT NULL REFERENCES client_keys(id) ON DELETE CASCADE,
    preview_key_sha256 text NOT NULL,
    payload            jsonb NOT NULL,
    captured_at        timestamptz NOT NULL,
    received_at        timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (device_id, client_key_id)
);
CREATE INDEX device_previews_received ON device_previews (received_at);

-- +goose Down
DROP TABLE device_previews;
ALTER TABLE devices DROP COLUMN previews;
ALTER TABLE client_keys DROP COLUMN preview_key, DROP COLUMN preview_key_sha256, DROP COLUMN preview_key_sig;
