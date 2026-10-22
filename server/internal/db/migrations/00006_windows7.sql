-- +goose Up
-- The Windows 7 build of the agent is a release arch of its own with its own
-- Add-device installer.
ALTER TABLE agent_releases
    DROP CONSTRAINT agent_releases_arch_check,
    ADD CONSTRAINT agent_releases_arch_check CHECK (arch IN ('amd64', 'arm64', 'universal', 'amd64-win7'));
ALTER TABLE agent_downloads
    DROP CONSTRAINT agent_downloads_os_check,
    ADD CONSTRAINT agent_downloads_os_check CHECK (os IN ('macos', 'windows', 'windows7'));

-- +goose Down
DELETE FROM agent_downloads WHERE os = 'windows7';
DELETE FROM agent_releases WHERE arch = 'amd64-win7';
ALTER TABLE agent_downloads
    DROP CONSTRAINT agent_downloads_os_check,
    ADD CONSTRAINT agent_downloads_os_check CHECK (os IN ('macos', 'windows'));
ALTER TABLE agent_releases
    DROP CONSTRAINT agent_releases_arch_check,
    ADD CONSTRAINT agent_releases_arch_check CHECK (arch IN ('amd64', 'arm64', 'universal'));
