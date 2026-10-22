package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/ruolez/remotectl/server/internal/db"
)

// StoredPreview is a sealed preview (proto.Preview JSON) the server keeps for
// a device and browser key; it cannot read it.
type StoredPreview struct {
	DeviceID    string
	ClientKeyID string
	PreviewSHA  string
	Payload     json.RawMessage
	CapturedAt  time.Time
	ReceivedAt  time.Time
}

func UpsertPreview(ctx context.Context, q db.Querier, p StoredPreview) error {
	_, err := q.Exec(ctx, `INSERT INTO device_previews (device_id, client_key_id, preview_key_sha256, payload, captured_at, received_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (device_id, client_key_id) DO UPDATE SET preview_key_sha256 = EXCLUDED.preview_key_sha256,
			payload = EXCLUDED.payload, captured_at = EXCLUDED.captured_at, received_at = EXCLUDED.received_at`,
		p.DeviceID, p.ClientKeyID, p.PreviewSHA, p.Payload, p.CapturedAt, p.ReceivedAt)
	return err
}

// PreviewsFor returns the stored previews of the given devices for a browser
// key, sealed to its current preview key.
func PreviewsFor(ctx context.Context, q db.Querier, clientKeyID, previewSHA string, deviceIDs []string) ([]StoredPreview, error) {
	rows, err := q.Query(ctx, `SELECT device_id, client_key_id, preview_key_sha256, payload, captured_at, received_at
		FROM device_previews WHERE client_key_id = $1 AND preview_key_sha256 = $2 AND device_id = ANY($3::uuid[])`,
		clientKeyID, previewSHA, deviceIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StoredPreview
	for rows.Next() {
		var p StoredPreview
		if err := rows.Scan(&p.DeviceID, &p.ClientKeyID, &p.PreviewSHA, &p.Payload, &p.CapturedAt, &p.ReceivedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// DeletePreviews drops stored previews: of one device, or all ("").
func DeletePreviews(ctx context.Context, q db.Querier, deviceID string) error {
	_, err := q.Exec(ctx, `DELETE FROM device_previews WHERE $1 = '' OR device_id = NULLIF($1, '')::uuid`, deviceID)
	return err
}

func PrunePreviews(ctx context.Context, q db.Querier, olderThan time.Time) error {
	_, err := q.Exec(ctx, `DELETE FROM device_previews WHERE received_at < $1`, olderThan)
	return err
}

func SetDevicePreviews(ctx context.Context, q db.Querier, id string, on bool) error {
	_, err := q.Exec(ctx, `UPDATE devices SET previews = $2 WHERE id = $1`, id, on)
	return err
}
