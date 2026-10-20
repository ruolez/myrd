package store

import (
	"context"
	"time"

	"github.com/ruolez/remotectl/server/internal/db"
)

type Session struct {
	ID             string     `json:"id"`
	DeviceID       string     `json:"device_id"`
	DeviceName     string     `json:"device_name"`
	UserID         string     `json:"user_id"`
	UserEmail      string     `json:"user_email"`
	ClientKeyID    *string    `json:"client_key_id"`
	ClientKeyLabel *string    `json:"client_key_label"`
	State          string     `json:"state"`
	CreatedAt      time.Time  `json:"created_at"`
	ConnectedAt    *time.Time `json:"connected_at"`
	EndedAt        *time.Time `json:"ended_at"`
	EndReason      *string    `json:"end_reason"`
	ICEPath        *string    `json:"ice_path"`
	BytesSent      int64      `json:"bytes_sent"`
	BytesRecv      int64      `json:"bytes_recv"`
	ClientIP       *string    `json:"client_ip"`
	AgentIP        *string    `json:"agent_ip"`
	DurationS      *int       `json:"duration_s"`
}

const sessionCols = `s.id, s.device_id, d.name, s.user_id, u.email, s.client_key_id, k.label, s.state, s.created_at,
	s.connected_at, s.ended_at, s.end_reason, s.ice_path, s.bytes_sent, s.bytes_recv, host(s.client_ip), host(s.agent_ip),
	CASE WHEN s.connected_at IS NOT NULL THEN extract(epoch FROM coalesce(s.ended_at, now()) - s.connected_at)::int END`

const sessionFrom = ` FROM sessions s JOIN devices d ON d.id = s.device_id JOIN users u ON u.id = s.user_id
	LEFT JOIN client_keys k ON k.id = s.client_key_id `

func scanSession(row interface{ Scan(...any) error }) (*Session, error) {
	var s Session
	err := row.Scan(&s.ID, &s.DeviceID, &s.DeviceName, &s.UserID, &s.UserEmail, &s.ClientKeyID, &s.ClientKeyLabel, &s.State,
		&s.CreatedAt, &s.ConnectedAt, &s.EndedAt, &s.EndReason, &s.ICEPath, &s.BytesSent, &s.BytesRecv, &s.ClientIP, &s.AgentIP, &s.DurationS)
	if err != nil {
		return nil, notFound(err)
	}
	return &s, nil
}

func SessionByID(ctx context.Context, q db.Querier, id string) (*Session, error) {
	return scanSession(q.QueryRow(ctx, `SELECT `+sessionCols+sessionFrom+`WHERE s.id = $1`, id))
}

func CreateSession(ctx context.Context, q db.Querier, deviceID, userID, clientKeyID, clientIP string) (string, error) {
	var id string
	err := q.QueryRow(ctx, `INSERT INTO sessions (device_id, user_id, client_key_id, client_ip)
		VALUES ($1, $2, $3, NULLIF($4, '')::inet) RETURNING id`, deviceID, userID, clientKeyID, clientIP).Scan(&id)
	return id, err
}

func DeviceBusy(ctx context.Context, q db.Querier, deviceID string) (bool, error) {
	var b bool
	err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM sessions WHERE device_id = $1 AND state IN ('pending', 'active'))`, deviceID).Scan(&b)
	return b, err
}

// MarkSessionActive transitions pending → active. ok=false when the session
// was not pending (duplicate or late state message).
func MarkSessionActive(ctx context.Context, q db.Querier, id, path, agentIP string) (ok bool, err error) {
	tag, err := q.Exec(ctx, `UPDATE sessions SET state = 'active', connected_at = now(), ice_path = NULLIF($2, ''),
		agent_ip = NULLIF($3, '')::inet WHERE id = $1 AND state = 'pending'`, id, path, agentIP)
	return err == nil && tag.RowsAffected() == 1, err
}

func SetSessionPath(ctx context.Context, q db.Querier, id, path string) error {
	_, err := q.Exec(ctx, `UPDATE sessions SET ice_path = NULLIF($2, '') WHERE id = $1 AND state = 'active'`, id, path)
	return err
}

// EndSession closes a live session: pending → failed, active → ended.
// ok=false when it was already closed.
func EndSession(ctx context.Context, q db.Querier, id, reason string, sent, recv int64) (s *Session, ok bool, err error) {
	tag, err := q.Exec(ctx, `UPDATE sessions SET state = CASE WHEN state = 'pending' THEN 'failed' ELSE 'ended' END,
		ended_at = now(), end_reason = $2, bytes_sent = greatest(bytes_sent, $3), bytes_recv = greatest(bytes_recv, $4)
		WHERE id = $1 AND state IN ('pending', 'active')`, id, reason, sent, recv)
	if err != nil {
		return nil, false, err
	}
	if tag.RowsAffected() == 0 && (sent > 0 || recv > 0) {
		// The other side ended first; the agent's own session.end still carries
		// the transport byte counters.
		if _, err := q.Exec(ctx, `UPDATE sessions SET bytes_sent = greatest(bytes_sent, $2), bytes_recv = greatest(bytes_recv, $3)
			WHERE id = $1 AND state IN ('ended', 'failed')`, id, sent, recv); err != nil {
			return nil, false, err
		}
	}
	s, err = SessionByID(ctx, q, id)
	return s, tag.RowsAffected() == 1, err
}

// LiveSessionIDs returns ids of pending/active sessions, optionally per device or state.
func LiveSessionIDs(ctx context.Context, q db.Querier, deviceID, state string) ([]string, error) {
	rows, err := q.Query(ctx, `SELECT id FROM sessions WHERE state IN ('pending', 'active')
		AND ($1 = '' OR device_id = NULLIF($1, '')::uuid) AND ($2 = '' OR state = $2)`, deviceID, state)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func LiveSessionIDsByUser(ctx context.Context, q db.Querier, userID string) ([]string, error) {
	rows, err := q.Query(ctx, `SELECT id FROM sessions WHERE state IN ('pending', 'active') AND user_id = $1`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

type SessionFilter struct {
	UserID   string
	DeviceID string
	State    string
	Limit    int
	Before   *time.Time
}

func ListSessions(ctx context.Context, q db.Querier, f SessionFilter) ([]Session, error) {
	rows, err := q.Query(ctx, `SELECT `+sessionCols+sessionFrom+`
		WHERE ($1 = '' OR s.user_id = NULLIF($1, '')::uuid) AND ($2 = '' OR s.device_id = NULLIF($2, '')::uuid) AND ($3 = '' OR s.state = $3)
		  AND ($4::timestamptz IS NULL OR s.created_at < $4)
		ORDER BY s.created_at DESC LIMIT $5`, f.UserID, f.DeviceID, f.State, f.Before, f.Limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Session{}
	for rows.Next() {
		s, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *s)
	}
	return out, rows.Err()
}
