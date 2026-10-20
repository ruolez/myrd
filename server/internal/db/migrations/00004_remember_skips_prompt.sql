-- +goose Up
-- "Allow and remember" on the device now means no prompt next time: browsers
-- the owner pinned connect directly unless the device asks every time.
ALTER TABLE devices ALTER COLUMN auto_accept SET DEFAULT true;
UPDATE devices SET auto_accept = true;
UPDATE app_settings SET value = 'true' WHERE key = 'default_auto_accept';

-- +goose Down
ALTER TABLE devices ALTER COLUMN auto_accept SET DEFAULT false;
UPDATE app_settings SET value = 'false' WHERE key = 'default_auto_accept';
