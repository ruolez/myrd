-- +goose Up
-- One reusable install link per user (PROTOCOL §2 "Install links"): the
-- installer downloaded through it enrolls into that user's account without a
-- token. The code is looked up by hash and kept sealed (TOTP_ENC_KEY) so the
-- owner can copy the link again.
CREATE TABLE install_links (
    user_id      uuid PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    code_hash    bytea NOT NULL UNIQUE,
    code_sealed  bytea NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    last_used_at timestamptz,
    use_count    integer NOT NULL DEFAULT 0
);

-- +goose Down
DROP TABLE install_links;
