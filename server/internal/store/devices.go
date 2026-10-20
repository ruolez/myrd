package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/ruolez/remotectl/server/internal/db"
)

type PinnedClientRow struct {
	ClientKeyID *string   `json:"client_key_id"`
	Label       string    `json:"label"`
	ApprovedAt  time.Time `json:"approved_at"`
}

type Device struct {
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	Hostname      string            `json:"hostname"`
	OS            string            `json:"os"`
	Arch          string            `json:"arch"`
	AgentVersion  string            `json:"agent_version"`
	PublicKey     []byte            `json:"public_key"`
	Status        string            `json:"status"` // online | offline | disabled (presence applied by the API)
	Online        bool              `json:"online"`
	Busy          bool              `json:"busy"`
	AutoAccept    bool              `json:"auto_accept"`
	LastSeenAt    *time.Time        `json:"last_seen_at"`
	CreatedAt     time.Time         `json:"created_at"`
	Monitors      json.RawMessage   `json:"monitors"`
	Permissions   json.RawMessage   `json:"permissions"`
	Encoder       *string           `json:"encoder"`
	PinnedClients []PinnedClientRow `json:"pinned_clients"`
	OwnerEmail    string            `json:"owner_email,omitempty"`

	UserID   string `json:"-"`
	Disabled bool   `json:"-"`
	Removed  bool   `json:"-"`
}

const deviceCols = `d.id, d.user_id, d.name, d.hostname, d.os, d.arch, d.agent_version, d.public_key,
	d.status = 'disabled', d.removed_at IS NOT NULL, d.auto_accept, d.last_seen_at, d.created_at,
	d.monitors, d.permissions, d.encoder,
	EXISTS (SELECT 1 FROM sessions s WHERE s.device_id = d.id AND s.state IN ('pending', 'active')),
	coalesce((SELECT json_agg(json_build_object('client_key_id', p.client_key_id, 'label', p.label, 'approved_at', p.approved_at) ORDER BY p.approved_at)
		FROM device_pinned_clients p WHERE p.device_id = d.id), '[]'),
	u.email`

func scanDevice(row interface{ Scan(...any) error }) (*Device, error) {
	var d Device
	var pins []byte
	err := row.Scan(&d.ID, &d.UserID, &d.Name, &d.Hostname, &d.OS, &d.Arch, &d.AgentVersion, &d.PublicKey,
		&d.Disabled, &d.Removed, &d.AutoAccept, &d.LastSeenAt, &d.CreatedAt, &d.Monitors, &d.Permissions, &d.Encoder,
		&d.Busy, &pins, &d.OwnerEmail)
	if err != nil {
		return nil, notFound(err)
	}
	if err := json.Unmarshal(pins, &d.PinnedClients); err != nil {
		return nil, err
	}
	d.Status = "offline"
	if d.Disabled {
		d.Status = "disabled"
	}
	return &d, nil
}

func DeviceByID(ctx context.Context, q db.Querier, id string) (*Device, error) {
	return scanDevice(q.QueryRow(ctx, `SELECT `+deviceCols+` FROM devices d JOIN users u ON u.id = d.user_id WHERE d.id = $1`, id))
}

func DevicesByUser(ctx context.Context, q db.Querier, userID string) ([]Device, error) {
	return queryDevices(ctx, q, `SELECT `+deviceCols+` FROM devices d JOIN users u ON u.id = d.user_id
		WHERE d.user_id = $1 AND d.removed_at IS NULL ORDER BY d.name, d.created_at`, userID)
}

// ListAllDevices is the admin listing (cursor = created_at). status filters on
// the stored column; presence-based online filtering is done by the caller.
func ListAllDevices(ctx context.Context, q db.Querier, search string, disabledOnly bool, limit int, before *time.Time) ([]Device, error) {
	return queryDevices(ctx, q, `SELECT `+deviceCols+` FROM devices d JOIN users u ON u.id = d.user_id
		WHERE d.removed_at IS NULL
		  AND ($1 = '' OR d.name ILIKE '%' || $1 || '%' OR d.hostname ILIKE '%' || $1 || '%' OR u.email ILIKE '%' || $1 || '%')
		  AND (NOT $2 OR d.status = 'disabled')
		  AND ($3::timestamptz IS NULL OR d.created_at < $3)
		ORDER BY d.created_at DESC LIMIT $4`, search, disabledOnly, before, limit)
}

func queryDevices(ctx context.Context, q db.Querier, sql string, args ...any) ([]Device, error) {
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Device{}
	for rows.Next() {
		d, err := scanDevice(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *d)
	}
	return out, rows.Err()
}

type NewDevice struct {
	UserID, Name, Hostname, OS, Arch, Version string
	PublicKey                                 []byte
	AutoAccept                                bool
}

func CreateDevice(ctx context.Context, q db.Querier, n NewDevice) (string, error) {
	var id string
	err := q.QueryRow(ctx, `INSERT INTO devices (user_id, name, hostname, os, arch, agent_version, public_key, auto_accept)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING id`,
		n.UserID, n.Name, n.Hostname, n.OS, n.Arch, n.Version, n.PublicKey, n.AutoAccept).Scan(&id)
	if isUnique(err) {
		return "", ErrConflict
	}
	return id, err
}

type DeviceHello struct {
	Hostname, OS, Arch, Version, Encoder, IP string
	Capabilities, Permissions, Monitors      []byte
}

func ApplyHello(ctx context.Context, q db.Querier, id string, h DeviceHello) error {
	_, err := q.Exec(ctx, `UPDATE devices SET hostname = $2, os = $3, arch = $4, agent_version = $5,
		capabilities = $6, permissions = $7, monitors = $8, encoder = NULLIF($9, ''), last_ip = NULLIF($10, '')::inet,
		last_seen_at = now(), status = CASE WHEN status = 'disabled' THEN status ELSE 'online' END
		WHERE id = $1`, id, h.Hostname, h.OS, h.Arch, h.Version, h.Capabilities, h.Permissions, h.Monitors, h.Encoder, h.IP)
	return err
}

func SetDevicePermissions(ctx context.Context, q db.Querier, id string, perms []byte) error {
	_, err := q.Exec(ctx, `UPDATE devices SET permissions = $2 WHERE id = $1`, id, perms)
	return err
}

func SetDeviceOffline(ctx context.Context, q db.Querier, id string) error {
	_, err := q.Exec(ctx, `UPDATE devices SET last_seen_at = now(), status = CASE WHEN status = 'disabled' THEN status ELSE 'offline' END WHERE id = $1`, id)
	return err
}

// MarkAllOffline runs at boot: presence is in memory, so nothing is online yet.
func MarkAllOffline(ctx context.Context, q db.Querier) error {
	_, err := q.Exec(ctx, `UPDATE devices SET status = 'offline' WHERE status = 'online'`)
	return err
}

func TouchDevicesSeen(ctx context.Context, q db.Querier, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	_, err := q.Exec(ctx, `UPDATE devices SET last_seen_at = now() WHERE id = ANY($1::uuid[])`, ids)
	return err
}

func UpdateDevice(ctx context.Context, q db.Querier, id string, name *string, autoAccept *bool) error {
	_, err := q.Exec(ctx, `UPDATE devices SET name = coalesce($2, name), auto_accept = coalesce($3, auto_accept) WHERE id = $1`, id, name, autoAccept)
	return err
}

func SetDeviceDisabled(ctx context.Context, q db.Querier, id string, disabled bool) error {
	_, err := q.Exec(ctx, `UPDATE devices SET status = CASE WHEN $2 THEN 'disabled' ELSE 'offline' END WHERE id = $1`, id, disabled)
	return err
}

func RemoveDevice(ctx context.Context, q db.Querier, id string) error {
	_, err := q.Exec(ctx, `UPDATE devices SET removed_at = coalesce(removed_at, now()), status = CASE WHEN status = 'disabled' THEN status ELSE 'offline' END WHERE id = $1`, id)
	return err
}

// ReplacePins mirrors the agent's pin list.
func ReplacePins(ctx context.Context, q db.Querier, deviceID string, pins []PinInput) error {
	if _, err := q.Exec(ctx, `DELETE FROM device_pinned_clients WHERE device_id = $1`, deviceID); err != nil {
		return err
	}
	for _, p := range pins {
		if _, err := q.Exec(ctx, `INSERT INTO device_pinned_clients (device_id, public_key_sha256, client_key_id, label, approved_at)
			VALUES ($1, $2, (SELECT k.id FROM client_keys k JOIN devices d ON d.user_id = k.user_id
			                 WHERE d.id = $1 AND k.public_key_sha256 = $2 LIMIT 1), $3, coalesce($4::timestamptz, now()))
			ON CONFLICT (device_id, public_key_sha256) DO NOTHING`, deviceID, p.SHA256, p.Label, p.ApprovedAt); err != nil {
			return err
		}
	}
	return nil
}

type PinInput struct {
	SHA256     string
	Label      string
	ApprovedAt *time.Time
}
