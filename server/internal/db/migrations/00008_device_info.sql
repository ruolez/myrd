-- +goose Up
-- The latest device.info report (proto.HostInfo): OS version, console user,
-- idle time, uptime, CPU, memory and disks.
ALTER TABLE devices
    ADD COLUMN info    jsonb,
    ADD COLUMN info_at timestamptz;

-- +goose Down
ALTER TABLE devices DROP COLUMN info, DROP COLUMN info_at;
