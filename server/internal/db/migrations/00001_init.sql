-- +goose Up
CREATE EXTENSION IF NOT EXISTS citext;

CREATE TABLE users (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    email            citext NOT NULL UNIQUE,
    password_hash    text,
    totp_secret_enc  bytea,           -- AES-256-GCM, key from TOTP_ENC_KEY
    totp_pending_enc bytea,           -- set by /totp/setup until enabled
    totp_enabled_at  timestamptz,
    is_admin         boolean NOT NULL DEFAULT false,
    disabled_at      timestamptz,
    last_login_at    timestamptz,
    created_at       timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE webauthn_credentials (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id       uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    credential_id bytea NOT NULL UNIQUE,
    public_key    bytea NOT NULL,
    sign_count    bigint NOT NULL DEFAULT 0,
    transports    text[] NOT NULL DEFAULT '{}',
    name          text NOT NULL DEFAULT 'Passkey',
    credential    jsonb NOT NULL,     -- full webauthn.Credential
    created_at    timestamptz NOT NULL DEFAULT now(),
    last_used_at  timestamptz
);
CREATE INDEX webauthn_credentials_user ON webauthn_credentials (user_id);

CREATE TABLE user_sessions (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id      uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash   bytea NOT NULL UNIQUE,
    ip           inet,
    user_agent   text NOT NULL DEFAULT '',
    created_at   timestamptz NOT NULL DEFAULT now(),
    expires_at   timestamptz NOT NULL,
    last_seen_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX user_sessions_user ON user_sessions (user_id);

CREATE TABLE client_keys (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id           uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    public_key        bytea NOT NULL UNIQUE,  -- SPKI DER, ECDSA P-256
    public_key_sha256 text NOT NULL,
    label             text NOT NULL DEFAULT '',
    created_at        timestamptz NOT NULL DEFAULT now(),
    last_used_at      timestamptz,
    revoked_at        timestamptz
);
CREATE INDEX client_keys_user ON client_keys (user_id);
CREATE INDEX client_keys_sha ON client_keys (public_key_sha256);

CREATE TABLE devices (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id       uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name          text NOT NULL,
    hostname      text NOT NULL DEFAULT '',
    os            text NOT NULL DEFAULT '',
    arch          text NOT NULL DEFAULT '',
    agent_version text NOT NULL DEFAULT '',
    public_key    bytea NOT NULL UNIQUE,       -- raw Ed25519
    status        text NOT NULL DEFAULT 'offline' CHECK (status IN ('online', 'offline', 'disabled')),
    auto_accept   boolean NOT NULL DEFAULT false,
    capabilities  jsonb NOT NULL DEFAULT '{}',
    permissions   jsonb NOT NULL DEFAULT '{}',
    monitors      jsonb NOT NULL DEFAULT '[]',
    encoder       text,
    last_ip       inet,
    last_seen_at  timestamptz,
    removed_at    timestamptz,
    created_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX devices_user ON devices (user_id);
CREATE INDEX devices_public_key ON devices (public_key);

CREATE TABLE enroll_tokens (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id           uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash        bytea NOT NULL UNIQUE,
    created_at        timestamptz NOT NULL DEFAULT now(),
    expires_at        timestamptz NOT NULL,
    used_at           timestamptz,
    used_by_device_id uuid REFERENCES devices(id) ON DELETE SET NULL
);
CREATE INDEX enroll_tokens_hash ON enroll_tokens (token_hash);

-- Informational mirror of the agent's authoritative local pin list.
CREATE TABLE device_pinned_clients (
    device_id         uuid NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
    public_key_sha256 text NOT NULL,
    client_key_id     uuid REFERENCES client_keys(id) ON DELETE SET NULL,
    label             text NOT NULL DEFAULT '',
    approved_at       timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (device_id, public_key_sha256)
);

CREATE TABLE sessions (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    device_id     uuid NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
    user_id       uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    client_key_id uuid REFERENCES client_keys(id) ON DELETE SET NULL,
    state         text NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'active', 'ended', 'failed')),
    created_at    timestamptz NOT NULL DEFAULT now(),
    connected_at  timestamptz,
    ended_at      timestamptz,
    end_reason    text,
    ice_path      text CHECK (ice_path IN ('host', 'srflx', 'relay')),
    bytes_sent    bigint NOT NULL DEFAULT 0,
    bytes_recv    bigint NOT NULL DEFAULT 0,
    client_ip     inet,
    agent_ip      inet
);
CREATE INDEX sessions_device_created ON sessions (device_id, created_at DESC);
CREATE INDEX sessions_user_created ON sessions (user_id, created_at DESC);
CREATE INDEX sessions_live ON sessions (state) WHERE state IN ('pending', 'active');

-- Append-only, range-partitioned by month. No FKs so rows outlive the users,
-- devices and sessions they describe.
CREATE TABLE audit_events (
    id         bigserial,
    ts         timestamptz NOT NULL DEFAULT now(),
    actor      text NOT NULL CHECK (actor IN ('user', 'agent', 'admin', 'system')),
    event      text NOT NULL,
    user_id    uuid,
    device_id  uuid,
    session_id uuid,
    ip         inet,
    meta       jsonb NOT NULL DEFAULT '{}',
    PRIMARY KEY (id, ts)
) PARTITION BY RANGE (ts);
CREATE INDEX audit_events_user_ts ON audit_events (user_id, ts DESC);
CREATE INDEX audit_events_device_ts ON audit_events (device_id, ts DESC);
CREATE INDEX audit_events_id ON audit_events (id DESC);
CREATE TABLE audit_events_default PARTITION OF audit_events DEFAULT;

-- +goose StatementBegin
CREATE FUNCTION audit_events_append_only() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'audit_events is append-only (% rejected)', TG_OP USING ERRCODE = 'insufficient_privilege';
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER audit_events_no_update BEFORE UPDATE OR DELETE ON audit_events
    FOR EACH ROW EXECUTE FUNCTION audit_events_append_only();

-- Creates the monthly partition containing month_start (idempotent).
-- +goose StatementBegin
CREATE FUNCTION ensure_audit_partition(month_start date) RETURNS void LANGUAGE plpgsql AS $$
DECLARE
    lo date := date_trunc('month', month_start)::date;
    hi date := (date_trunc('month', month_start) + interval '1 month')::date;
    part text := 'audit_events_' || to_char(lo, 'YYYYMM');
BEGIN
    IF to_regclass(part) IS NULL THEN
        EXECUTE format('CREATE TABLE %I PARTITION OF audit_events FOR VALUES FROM (%L) TO (%L)', part, lo, hi);
    END IF;
END;
$$;
-- +goose StatementEnd

CREATE TABLE agent_releases (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    version      text NOT NULL,
    os           text NOT NULL,
    arch         text NOT NULL,
    url          text NOT NULL,
    sha256       text NOT NULL,
    signature    text NOT NULL,
    rollout_pct  int NOT NULL DEFAULT 0 CHECK (rollout_pct BETWEEN 0 AND 100),
    published_at timestamptz,
    created_at   timestamptz NOT NULL DEFAULT now(),
    UNIQUE (version, os, arch)
);

CREATE TABLE app_settings (
    key   text PRIMARY KEY,
    value jsonb NOT NULL
);
INSERT INTO app_settings (key, value) VALUES
    ('signup_allowed', 'false'),
    ('default_auto_accept', 'false'),
    ('max_session_hours', '12'),
    ('download_macos_url', '""'),
    ('download_windows_url', '""');

-- +goose Down
DROP TABLE app_settings, agent_releases, audit_events, sessions, device_pinned_clients,
    enroll_tokens, devices, client_keys, user_sessions, webauthn_credentials, users CASCADE;
DROP FUNCTION IF EXISTS ensure_audit_partition(date);
DROP FUNCTION IF EXISTS audit_events_append_only();
