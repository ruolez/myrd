// Package store holds the hand-written pgx queries. Every helper takes a
// db.Querier so it works on the pool or inside a transaction. Row types carry
// the JSON tags of the API objects in docs/PROTOCOL.md.
package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/ruolez/remotectl/server/internal/db"
)

var (
	ErrNotFound = errors.New("not found")
	ErrConflict = errors.New("conflict")
)

func notFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

func isUnique(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && pe.Code == "23505"
}

type User struct {
	ID           string     `json:"id"`
	Email        string     `json:"email"`
	IsAdmin      bool       `json:"is_admin"`
	Disabled     bool       `json:"disabled"`
	TOTPEnabled  bool       `json:"totp_enabled"`
	HasPassword  bool       `json:"has_password"`
	PasskeyCount int        `json:"passkey_count"`
	CreatedAt    time.Time  `json:"created_at"`
	LastLoginAt  *time.Time `json:"last_login_at"`
	DeviceCount  *int       `json:"device_count,omitempty"`

	PasswordHash  string `json:"-"`
	TOTPSecretEnc []byte `json:"-"`
	TOTPPending   []byte `json:"-"`
}

const userCols = `u.id, u.email, u.is_admin, u.disabled_at IS NOT NULL, u.totp_enabled_at IS NOT NULL,
	u.password_hash IS NOT NULL, (SELECT count(*) FROM webauthn_credentials w WHERE w.user_id = u.id),
	u.created_at, u.last_login_at, coalesce(u.password_hash, ''), u.totp_secret_enc, u.totp_pending_enc`

func scanUser(row pgx.Row) (*User, error) {
	var u User
	err := row.Scan(&u.ID, &u.Email, &u.IsAdmin, &u.Disabled, &u.TOTPEnabled, &u.HasPassword,
		&u.PasskeyCount, &u.CreatedAt, &u.LastLoginAt, &u.PasswordHash, &u.TOTPSecretEnc, &u.TOTPPending)
	if err != nil {
		return nil, notFound(err)
	}
	return &u, nil
}

func UserByID(ctx context.Context, q db.Querier, id string) (*User, error) {
	return scanUser(q.QueryRow(ctx, `SELECT `+userCols+` FROM users u WHERE u.id = $1`, id))
}

func UserByEmail(ctx context.Context, q db.Querier, email string) (*User, error) {
	return scanUser(q.QueryRow(ctx, `SELECT `+userCols+` FROM users u WHERE u.email = $1`, email))
}

func CountUsers(ctx context.Context, q db.Querier) (int, error) {
	var n int
	err := q.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&n)
	return n, err
}

// CreateUser inserts a user; passwordHash may be empty for passkey-only.
func CreateUser(ctx context.Context, q db.Querier, email, passwordHash string, isAdmin bool) (*User, error) {
	var id string
	err := q.QueryRow(ctx, `INSERT INTO users (email, password_hash, is_admin) VALUES ($1, NULLIF($2, ''), $3) RETURNING id`,
		email, passwordHash, isAdmin).Scan(&id)
	if isUnique(err) {
		return nil, ErrConflict
	}
	if err != nil {
		return nil, err
	}
	return UserByID(ctx, q, id)
}

func SetPassword(ctx context.Context, q db.Querier, userID, hash string) error {
	_, err := q.Exec(ctx, `UPDATE users SET password_hash = $2 WHERE id = $1`, userID, hash)
	return err
}

func SetTOTPPending(ctx context.Context, q db.Querier, userID string, sealed []byte) error {
	_, err := q.Exec(ctx, `UPDATE users SET totp_pending_enc = $2 WHERE id = $1`, userID, sealed)
	return err
}

func EnableTOTP(ctx context.Context, q db.Querier, userID string, sealed []byte) error {
	_, err := q.Exec(ctx, `UPDATE users SET totp_secret_enc = $2, totp_pending_enc = NULL, totp_enabled_at = now(), totp_last_step = NULL WHERE id = $1`, userID, sealed)
	return err
}

func DisableTOTP(ctx context.Context, q db.Querier, userID string) error {
	_, err := q.Exec(ctx, `UPDATE users SET totp_secret_enc = NULL, totp_pending_enc = NULL, totp_enabled_at = NULL, totp_last_step = NULL WHERE id = $1`, userID)
	return err
}

// ClaimTOTPStep records step as used. ok=false when that step or a later one
// was already used (a replayed code).
func ClaimTOTPStep(ctx context.Context, q db.Querier, userID string, step int64) (ok bool, err error) {
	tag, err := q.Exec(ctx, `UPDATE users SET totp_last_step = $2 WHERE id = $1 AND (totp_last_step IS NULL OR totp_last_step < $2)`, userID, step)
	return err == nil && tag.RowsAffected() == 1, err
}

func TouchLogin(ctx context.Context, q db.Querier, userID string) error {
	_, err := q.Exec(ctx, `UPDATE users SET last_login_at = now() WHERE id = $1`, userID)
	return err
}

func SetUserAdmin(ctx context.Context, q db.Querier, userID string, admin bool) error {
	_, err := q.Exec(ctx, `UPDATE users SET is_admin = $2 WHERE id = $1`, userID, admin)
	return err
}

func SetUserDisabled(ctx context.Context, q db.Querier, userID string, disabled bool) error {
	_, err := q.Exec(ctx, `UPDATE users SET disabled_at = CASE WHEN $2 THEN coalesce(disabled_at, now()) ELSE NULL END WHERE id = $1`, userID, disabled)
	return err
}

func DeleteUser(ctx context.Context, q db.Querier, userID string) error {
	tag, err := q.Exec(ctx, `DELETE FROM users WHERE id = $1`, userID)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// ListUsers pages users newest first (cursor = created_at RFC3339Nano).
func ListUsers(ctx context.Context, q db.Querier, search string, limit int, before *time.Time) ([]User, error) {
	rows, err := q.Query(ctx, `SELECT `+userCols+`, (SELECT count(*) FROM devices d WHERE d.user_id = u.id AND d.removed_at IS NULL)
		FROM users u
		WHERE ($1 = '' OR u.email ILIKE '%' || $1 || '%') AND ($2::timestamptz IS NULL OR u.created_at < $2)
		ORDER BY u.created_at DESC LIMIT $3`, search, before, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []User{}
	for rows.Next() {
		var u User
		var dc int
		if err := rows.Scan(&u.ID, &u.Email, &u.IsAdmin, &u.Disabled, &u.TOTPEnabled, &u.HasPassword,
			&u.PasskeyCount, &u.CreatedAt, &u.LastLoginAt, &u.PasswordHash, &u.TOTPSecretEnc, &u.TOTPPending, &dc); err != nil {
			return nil, err
		}
		u.DeviceCount = &dc
		out = append(out, u)
	}
	return out, rows.Err()
}

func CountAdmins(ctx context.Context, q db.Querier) (int, error) {
	var n int
	err := q.QueryRow(ctx, `SELECT count(*) FROM users WHERE is_admin AND disabled_at IS NULL`).Scan(&n)
	return n, err
}

// ---------- login sessions ----------

type LoginSession struct {
	ID         string    `json:"id"`
	IP         *string   `json:"ip"`
	UserAgent  string    `json:"user_agent"`
	CreatedAt  time.Time `json:"created_at"`
	LastSeenAt time.Time `json:"last_seen_at"`
	ExpiresAt  time.Time `json:"expires_at"`
	Current    bool      `json:"current"`
	UserID     string    `json:"-"`
}

func CreateLoginSession(ctx context.Context, q db.Querier, userID string, tokenHash []byte, ip, ua string, ttl time.Duration) (string, error) {
	var id string
	err := q.QueryRow(ctx, `INSERT INTO user_sessions (user_id, token_hash, ip, user_agent, expires_at)
		VALUES ($1, $2, NULLIF($3, '')::inet, $4, now() + $5::interval) RETURNING id`,
		userID, tokenHash, ip, ua, ttl.String()).Scan(&id)
	return id, err
}

// LoginSessionByHash returns the session and its (enabled) user.
func LoginSessionByHash(ctx context.Context, q db.Querier, tokenHash []byte) (*LoginSession, *User, error) {
	var s LoginSession
	err := q.QueryRow(ctx, `SELECT s.id, s.user_id, host(s.ip), s.user_agent, s.created_at, s.last_seen_at, s.expires_at
		FROM user_sessions s JOIN users u ON u.id = s.user_id
		WHERE s.token_hash = $1 AND s.expires_at > now() AND u.disabled_at IS NULL`, tokenHash).
		Scan(&s.ID, &s.UserID, &s.IP, &s.UserAgent, &s.CreatedAt, &s.LastSeenAt, &s.ExpiresAt)
	if err != nil {
		return nil, nil, notFound(err)
	}
	u, err := UserByID(ctx, q, s.UserID)
	return &s, u, err
}

func SlideLoginSession(ctx context.Context, q db.Querier, id string, ttl time.Duration) error {
	_, err := q.Exec(ctx, `UPDATE user_sessions SET last_seen_at = now(), expires_at = now() + $2::interval WHERE id = $1`, id, ttl.String())
	return err
}

func ListLoginSessions(ctx context.Context, q db.Querier, userID string) ([]LoginSession, error) {
	rows, err := q.Query(ctx, `SELECT id, user_id, host(ip), user_agent, created_at, last_seen_at, expires_at
		FROM user_sessions WHERE user_id = $1 AND expires_at > now() ORDER BY last_seen_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []LoginSession{}
	for rows.Next() {
		var s LoginSession
		if err := rows.Scan(&s.ID, &s.UserID, &s.IP, &s.UserAgent, &s.CreatedAt, &s.LastSeenAt, &s.ExpiresAt); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func DeleteLoginSession(ctx context.Context, q db.Querier, userID, id string) error {
	tag, err := q.Exec(ctx, `DELETE FROM user_sessions WHERE id = $1 AND user_id = $2`, id, userID)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

func DeleteLoginSessionByHash(ctx context.Context, q db.Querier, tokenHash []byte) error {
	_, err := q.Exec(ctx, `DELETE FROM user_sessions WHERE token_hash = $1`, tokenHash)
	return err
}

func DeleteUserLoginSessions(ctx context.Context, q db.Querier, userID string) error {
	_, err := q.Exec(ctx, `DELETE FROM user_sessions WHERE user_id = $1`, userID)
	return err
}
