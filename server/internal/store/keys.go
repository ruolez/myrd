package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/ruolez/remotectl/server/internal/db"
)

// ---------- passkeys ----------

type Passkey struct {
	ID         string              `json:"id"`
	Name       string              `json:"name"`
	CreatedAt  time.Time           `json:"created_at"`
	LastUsedAt *time.Time          `json:"last_used_at"`
	Transports []string            `json:"transports"`
	UserID     string              `json:"-"`
	Credential webauthn.Credential `json:"-"`
}

func scanPasskeys(rows interface {
	Next() bool
	Scan(...any) error
	Err() error
	Close()
}) ([]Passkey, error) {
	defer rows.Close()
	out := []Passkey{}
	for rows.Next() {
		var p Passkey
		var raw []byte
		if err := rows.Scan(&p.ID, &p.UserID, &p.Name, &p.CreatedAt, &p.LastUsedAt, &p.Transports, &raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &p.Credential); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

const passkeyCols = `id, user_id, name, created_at, last_used_at, transports, credential`

func PasskeysByUser(ctx context.Context, q db.Querier, userID string) ([]Passkey, error) {
	rows, err := q.Query(ctx, `SELECT `+passkeyCols+` FROM webauthn_credentials WHERE user_id = $1 ORDER BY created_at`, userID)
	if err != nil {
		return nil, err
	}
	return scanPasskeys(rows)
}

func PasskeyByCredentialID(ctx context.Context, q db.Querier, credID []byte) (*Passkey, error) {
	rows, err := q.Query(ctx, `SELECT `+passkeyCols+` FROM webauthn_credentials WHERE credential_id = $1`, credID)
	if err != nil {
		return nil, err
	}
	ps, err := scanPasskeys(rows)
	if err != nil {
		return nil, err
	}
	if len(ps) == 0 {
		return nil, ErrNotFound
	}
	return &ps[0], nil
}

func CreatePasskey(ctx context.Context, q db.Querier, userID, name string, c *webauthn.Credential) (*Passkey, error) {
	raw, err := json.Marshal(c)
	if err != nil {
		return nil, err
	}
	tr := make([]string, 0, len(c.Transport))
	for _, t := range c.Transport {
		tr = append(tr, string(t))
	}
	var id string
	err = q.QueryRow(ctx, `INSERT INTO webauthn_credentials (user_id, credential_id, public_key, sign_count, transports, name, credential)
		VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id`,
		userID, c.ID, c.PublicKey, int64(c.Authenticator.SignCount), tr, name, raw).Scan(&id)
	if isUnique(err) {
		return nil, ErrConflict
	}
	if err != nil {
		return nil, err
	}
	rows, err := q.Query(ctx, `SELECT `+passkeyCols+` FROM webauthn_credentials WHERE id = $1`, id)
	if err != nil {
		return nil, err
	}
	ps, err := scanPasskeys(rows)
	if err != nil || len(ps) == 0 {
		return nil, err
	}
	return &ps[0], nil
}

func UpdatePasskeyUse(ctx context.Context, q db.Querier, id string, c *webauthn.Credential) error {
	raw, err := json.Marshal(c)
	if err != nil {
		return err
	}
	_, err = q.Exec(ctx, `UPDATE webauthn_credentials SET sign_count = $2, credential = $3, last_used_at = now() WHERE id = $1`,
		id, int64(c.Authenticator.SignCount), raw)
	return err
}

func DeletePasskey(ctx context.Context, q db.Querier, userID, id string) error {
	tag, err := q.Exec(ctx, `DELETE FROM webauthn_credentials WHERE id = $1 AND user_id = $2`, id, userID)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// ---------- client keys ----------

type ClientKey struct {
	ID         string     `json:"id"`
	Label      string     `json:"label"`
	PublicKey  []byte     `json:"public_key"` // marshals as base64 std
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
	RevokedAt  *time.Time `json:"revoked_at"`
	UserID     string     `json:"-"`
	SHA256     string     `json:"-"`
}

const clientKeyCols = `id, user_id, label, public_key, public_key_sha256, created_at, last_used_at, revoked_at`

func scanClientKey(row interface{ Scan(...any) error }) (*ClientKey, error) {
	var k ClientKey
	if err := row.Scan(&k.ID, &k.UserID, &k.Label, &k.PublicKey, &k.SHA256, &k.CreatedAt, &k.LastUsedAt, &k.RevokedAt); err != nil {
		return nil, notFound(err)
	}
	return &k, nil
}

func ClientKeyByID(ctx context.Context, q db.Querier, id string) (*ClientKey, error) {
	return scanClientKey(q.QueryRow(ctx, `SELECT `+clientKeyCols+` FROM client_keys WHERE id = $1`, id))
}

func ClientKeyByPublicKey(ctx context.Context, q db.Querier, pub []byte) (*ClientKey, error) {
	return scanClientKey(q.QueryRow(ctx, `SELECT `+clientKeyCols+` FROM client_keys WHERE public_key = $1`, pub))
}

func CreateClientKey(ctx context.Context, q db.Querier, userID string, pub []byte, sha, label string) (*ClientKey, error) {
	k, err := scanClientKey(q.QueryRow(ctx, `INSERT INTO client_keys (user_id, public_key, public_key_sha256, label)
		VALUES ($1, $2, $3, $4) RETURNING `+clientKeyCols, userID, pub, sha, label))
	if err != nil && isUnique(err) {
		return nil, ErrConflict
	}
	return k, err
}

func ListClientKeys(ctx context.Context, q db.Querier, userID string) ([]ClientKey, error) {
	rows, err := q.Query(ctx, `SELECT `+clientKeyCols+` FROM client_keys WHERE user_id = $1 ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ClientKey{}
	for rows.Next() {
		k, err := scanClientKey(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *k)
	}
	return out, rows.Err()
}

func RevokeClientKey(ctx context.Context, q db.Querier, userID, id string) (*ClientKey, error) {
	return scanClientKey(q.QueryRow(ctx, `UPDATE client_keys SET revoked_at = coalesce(revoked_at, now())
		WHERE id = $1 AND user_id = $2 RETURNING `+clientKeyCols, id, userID))
}

func TouchClientKey(ctx context.Context, q db.Querier, id string) error {
	_, err := q.Exec(ctx, `UPDATE client_keys SET last_used_at = now() WHERE id = $1`, id)
	return err
}

// RevokedClientKeys returns the SPKI DER of every revoked key of a user.
func RevokedClientKeys(ctx context.Context, q db.Querier, userID string) ([][]byte, error) {
	rows, err := q.Query(ctx, `SELECT public_key FROM client_keys WHERE user_id = $1 AND revoked_at IS NOT NULL`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := [][]byte{}
	for rows.Next() {
		var b []byte
		if err := rows.Scan(&b); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// ---------- enrollment tokens ----------

func CreateEnrollToken(ctx context.Context, q db.Querier, userID string, hash []byte, ttl time.Duration) (time.Time, error) {
	var exp time.Time
	err := q.QueryRow(ctx, `INSERT INTO enroll_tokens (user_id, token_hash, expires_at) VALUES ($1, $2, now() + $3::interval) RETURNING expires_at`,
		userID, hash, ttl.String()).Scan(&exp)
	return exp, err
}

// ConsumeEnrollToken marks an unexpired, unused token as used and returns its owner.
func ConsumeEnrollToken(ctx context.Context, q db.Querier, hash []byte) (tokenID, userID string, err error) {
	err = q.QueryRow(ctx, `UPDATE enroll_tokens SET used_at = now()
		WHERE token_hash = $1 AND used_at IS NULL AND expires_at > now() RETURNING id, user_id`, hash).Scan(&tokenID, &userID)
	return tokenID, userID, notFound(err)
}

func SetEnrollTokenDevice(ctx context.Context, q db.Querier, tokenID, deviceID string) error {
	_, err := q.Exec(ctx, `UPDATE enroll_tokens SET used_by_device_id = $2 WHERE id = $1`, tokenID, deviceID)
	return err
}
