package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/ruolez/remotectl/server/internal/db"
)

type AuditEvent struct {
	ID         int64          `json:"id"`
	TS         time.Time      `json:"ts"`
	Actor      string         `json:"actor"`
	Event      string         `json:"event"`
	UserID     *string        `json:"user_id"`
	UserEmail  *string        `json:"user_email"`
	DeviceID   *string        `json:"device_id"`
	DeviceName *string        `json:"device_name"`
	SessionID  *string        `json:"session_id"`
	IP         *string        `json:"ip"`
	Meta       map[string]any `json:"meta"`
}

// Audit is an event to append.
type Audit struct {
	Actor     string // user | agent | admin | system
	Event     string
	UserID    string
	DeviceID  string
	SessionID string
	IP        string
	Meta      map[string]any
}

func InsertAudit(ctx context.Context, q db.Querier, a Audit) error {
	meta := a.Meta
	if meta == nil {
		meta = map[string]any{}
	}
	raw, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	_, err = q.Exec(ctx, `INSERT INTO audit_events (actor, event, user_id, device_id, session_id, ip, meta)
		VALUES ($1, $2, NULLIF($3, '')::uuid, NULLIF($4, '')::uuid, NULLIF($5, '')::uuid, NULLIF($6, '')::inet, $7)`,
		a.Actor, a.Event, a.UserID, a.DeviceID, a.SessionID, a.IP, raw)
	return err
}

type AuditFilter struct {
	UserID   string
	DeviceID string
	Event    string
	Actor    string
	Limit    int
	Before   int64 // id cursor, 0 = none
	// OwnerID restricts to events about this user or their devices (user-facing listing).
	OwnerID string
}

func ListAudit(ctx context.Context, q db.Querier, f AuditFilter) ([]AuditEvent, error) {
	rows, err := q.Query(ctx, `SELECT a.id, a.ts, a.actor, a.event, a.user_id, u.email, a.device_id, d.name, a.session_id, host(a.ip), a.meta
		FROM audit_events a LEFT JOIN users u ON u.id = a.user_id LEFT JOIN devices d ON d.id = a.device_id
		WHERE ($1 = '' OR a.user_id = NULLIF($1, '')::uuid) AND ($2 = '' OR a.device_id = NULLIF($2, '')::uuid) AND ($3 = '' OR a.event = $3)
		  AND ($4 = '' OR a.actor = $4) AND ($5 = 0 OR a.id < $5)
		  AND ($6 = '' OR a.user_id = NULLIF($6, '')::uuid OR a.device_id IN (SELECT id FROM devices WHERE user_id = NULLIF($6, '')::uuid))
		ORDER BY a.id DESC LIMIT $7`, f.UserID, f.DeviceID, f.Event, f.Actor, f.Before, f.OwnerID, f.Limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AuditEvent{}
	for rows.Next() {
		var e AuditEvent
		var meta []byte
		if err := rows.Scan(&e.ID, &e.TS, &e.Actor, &e.Event, &e.UserID, &e.UserEmail, &e.DeviceID, &e.DeviceName, &e.SessionID, &e.IP, &meta); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(meta, &e.Meta); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
