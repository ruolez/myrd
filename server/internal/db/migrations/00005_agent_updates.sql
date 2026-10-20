-- +goose Up
-- v1 releases pointed at external URLs with a signature that agents never
-- verified successfully; none could be installed. v2 releases are uploaded
-- files stored as DOWNLOADS_DIR/releases/<id>.<ext>.
DELETE FROM agent_releases;
ALTER TABLE agent_releases
    DROP COLUMN url,
    ADD COLUMN filename text NOT NULL,
    ADD COLUMN size bigint NOT NULL CHECK (size > 0),
    ADD COLUMN key_id text NOT NULL,
    ADD COLUMN uploaded_by uuid REFERENCES users(id) ON DELETE SET NULL,
    ALTER COLUMN rollout_pct SET DEFAULT 100,
    ADD CONSTRAINT agent_releases_os_check CHECK (os IN ('darwin', 'windows')),
    ADD CONSTRAINT agent_releases_arch_check CHECK (arch IN ('amd64', 'arm64', 'universal'));

-- The Add-device installer can be a release's file (hard link).
ALTER TABLE agent_downloads ADD COLUMN release_id uuid REFERENCES agent_releases(id) ON DELETE SET NULL;

-- Last update.status reported by the agent (or derived by the server).
ALTER TABLE devices
    ADD COLUMN update_state text NOT NULL DEFAULT '' CHECK (update_state IN
        ('', 'checking', 'downloading', 'waiting_idle', 'installing', 'up_to_date', 'failed', 'rolled_back')),
    ADD COLUMN update_from text NOT NULL DEFAULT '',
    ADD COLUMN update_version text NOT NULL DEFAULT '',
    ADD COLUMN update_error text NOT NULL DEFAULT '',
    ADD COLUMN update_at timestamptz;

-- +goose Down
ALTER TABLE devices DROP COLUMN update_state, DROP COLUMN update_from, DROP COLUMN update_version,
    DROP COLUMN update_error, DROP COLUMN update_at;
ALTER TABLE agent_downloads DROP COLUMN release_id;
DELETE FROM agent_releases;
ALTER TABLE agent_releases
    DROP CONSTRAINT agent_releases_os_check,
    DROP CONSTRAINT agent_releases_arch_check,
    ALTER COLUMN rollout_pct SET DEFAULT 0,
    DROP COLUMN uploaded_by,
    DROP COLUMN key_id,
    DROP COLUMN size,
    DROP COLUMN filename,
    ADD COLUMN url text NOT NULL DEFAULT '';
ALTER TABLE agent_releases ALTER COLUMN url DROP DEFAULT;
