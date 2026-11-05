-- +goose Up
-- A session is a desktop (video + input) or a terminal (shell over the `term`
-- data channel, no video). PROTOCOL §3 session.create `mode`.
ALTER TABLE sessions ADD COLUMN mode text NOT NULL DEFAULT 'desktop'
    CHECK (mode IN ('desktop', 'terminal'));

-- +goose Down
ALTER TABLE sessions DROP COLUMN mode;
