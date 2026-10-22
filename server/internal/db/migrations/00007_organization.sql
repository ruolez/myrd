-- +goose Up
-- Per-owner organization of devices: a folder tree (one folder per device),
-- colored tags (many per device), favorites and notes. Devices have a single
-- owner, so these live on the device rows themselves.
CREATE TABLE device_folders (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    parent_id  uuid REFERENCES device_folders(id),
    name       text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 64),
    position   integer NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX device_folders_name ON device_folders (user_id, parent_id, lower(name)) NULLS NOT DISTINCT;
CREATE INDEX device_folders_parent ON device_folders (parent_id);

CREATE TABLE tags (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name       text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 32),
    color      smallint NOT NULL DEFAULT 0 CHECK (color BETWEEN 0 AND 11),
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX tags_name ON tags (user_id, lower(name));

CREATE TABLE device_tags (
    device_id uuid NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
    tag_id    uuid NOT NULL REFERENCES tags(id) ON DELETE CASCADE,
    PRIMARY KEY (device_id, tag_id)
);
CREATE INDEX device_tags_tag ON device_tags (tag_id);

ALTER TABLE devices
    ADD COLUMN folder_id uuid REFERENCES device_folders(id) ON DELETE SET NULL,
    ADD COLUMN favorite  boolean NOT NULL DEFAULT false,
    ADD COLUMN notes     text NOT NULL DEFAULT '' CHECK (char_length(notes) <= 4000);

-- Recents: the owner's latest connected session per device.
CREATE INDEX sessions_recent ON sessions (device_id, user_id, connected_at DESC);

-- +goose Down
DROP INDEX sessions_recent;
ALTER TABLE devices DROP COLUMN folder_id, DROP COLUMN favorite, DROP COLUMN notes;
DROP TABLE device_tags;
DROP TABLE tags;
DROP TABLE device_folders;
