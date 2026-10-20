-- +goose Up
-- Agent installers uploaded by admins; the file itself lives in DOWNLOADS_DIR/<os>.
CREATE TABLE agent_downloads (
    os          text PRIMARY KEY CHECK (os IN ('macos', 'windows')),
    filename    text NOT NULL,
    size        bigint NOT NULL,
    sha256      text NOT NULL,
    uploaded_by uuid REFERENCES users(id) ON DELETE SET NULL,
    uploaded_at timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE agent_downloads;
