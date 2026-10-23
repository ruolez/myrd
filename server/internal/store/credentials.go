package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ruolez/remotectl/server/internal/db"
)

// DeviceCredential is a saved sign-in for a device and user (proto.SealedCredential
// JSON in Sealed); the server cannot read the password.
type DeviceCredential struct {
	ClientKeyID string          `json:"client_key_id"`
	Username    string          `json:"username"`
	Sealed      json.RawMessage `json:"sealed"`
	UpdatedAt   time.Time       `json:"updated_at"`
}

func UpsertDeviceCredential(ctx context.Context, q db.Querier, deviceID, userID string, c DeviceCredential) (*DeviceCredential, error) {
	err := q.QueryRow(ctx, `INSERT INTO device_credentials (device_id, user_id, client_key_id, username, sealed)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (device_id, user_id) DO UPDATE SET client_key_id = EXCLUDED.client_key_id,
			username = EXCLUDED.username, sealed = EXCLUDED.sealed, updated_at = now()
		RETURNING updated_at`, deviceID, userID, c.ClientKeyID, c.Username, c.Sealed).Scan(&c.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// DeviceCredentialFor returns nil when nothing is saved.
func DeviceCredentialFor(ctx context.Context, q db.Querier, deviceID, userID string) (*DeviceCredential, error) {
	var c DeviceCredential
	err := q.QueryRow(ctx, `SELECT client_key_id, username, sealed, updated_at FROM device_credentials
		WHERE device_id = $1 AND user_id = $2`, deviceID, userID).Scan(&c.ClientKeyID, &c.Username, &c.Sealed, &c.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// DeleteDeviceCredential reports whether a credential was saved.
func DeleteDeviceCredential(ctx context.Context, q db.Querier, deviceID, userID string) (bool, error) {
	tag, err := q.Exec(ctx, `DELETE FROM device_credentials WHERE device_id = $1 AND user_id = $2`, deviceID, userID)
	return tag.RowsAffected() > 0, err
}

// DeleteCredentialsOfClientKey drops what a revoked browser saved: devices
// refuse its credentials anyway.
func DeleteCredentialsOfClientKey(ctx context.Context, q db.Querier, clientKeyID string) error {
	_, err := q.Exec(ctx, `DELETE FROM device_credentials WHERE client_key_id = $1`, clientKeyID)
	return err
}
