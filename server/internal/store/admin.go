package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/ruolez/remotectl/server/internal/db"
)

// ---------- releases ----------

type Release struct {
	ID          string     `json:"id"`
	Version     string     `json:"version"`
	OS          string     `json:"os"`
	Arch        string     `json:"arch"`
	URL         string     `json:"url"`
	SHA256      string     `json:"sha256"`
	Signature   string     `json:"signature"`
	RolloutPct  int        `json:"rollout_pct"`
	PublishedAt *time.Time `json:"published_at"`
	CreatedAt   time.Time  `json:"created_at"`
}

const releaseCols = `id, version, os, arch, url, sha256, signature, rollout_pct, published_at, created_at`

func scanRelease(row interface{ Scan(...any) error }) (*Release, error) {
	var r Release
	if err := row.Scan(&r.ID, &r.Version, &r.OS, &r.Arch, &r.URL, &r.SHA256, &r.Signature, &r.RolloutPct, &r.PublishedAt, &r.CreatedAt); err != nil {
		return nil, notFound(err)
	}
	return &r, nil
}

func ListReleases(ctx context.Context, q db.Querier, os, arch string, publishedOnly bool) ([]Release, error) {
	rows, err := q.Query(ctx, `SELECT `+releaseCols+` FROM agent_releases
		WHERE ($1 = '' OR os = $1) AND ($2 = '' OR arch = $2) AND (NOT $3 OR published_at IS NOT NULL)
		ORDER BY created_at DESC`, os, arch, publishedOnly)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Release{}
	for rows.Next() {
		r, err := scanRelease(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

func CreateRelease(ctx context.Context, q db.Querier, r Release) (*Release, error) {
	out, err := scanRelease(q.QueryRow(ctx, `INSERT INTO agent_releases (version, os, arch, url, sha256, signature, rollout_pct)
		VALUES ($1, $2, $3, $4, lower($5), $6, $7) RETURNING `+releaseCols,
		r.Version, r.OS, r.Arch, r.URL, r.SHA256, r.Signature, r.RolloutPct))
	if err != nil && isUnique(err) {
		return nil, ErrConflict
	}
	return out, err
}

func UpdateRelease(ctx context.Context, q db.Querier, id string, rollout *int, published *bool) (*Release, error) {
	return scanRelease(q.QueryRow(ctx, `UPDATE agent_releases SET rollout_pct = coalesce($2, rollout_pct),
		published_at = CASE WHEN $3::bool IS NULL THEN published_at WHEN $3 THEN coalesce(published_at, now()) ELSE NULL END
		WHERE id = $1 RETURNING `+releaseCols, id, rollout, published))
}

func DeleteRelease(ctx context.Context, q db.Querier, id string) error {
	tag, err := q.Exec(ctx, `DELETE FROM agent_releases WHERE id = $1`, id)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// ---------- settings ----------

type Settings struct {
	SignupAllowed      bool   `json:"signup_allowed"`
	DefaultAutoAccept  bool   `json:"default_auto_accept"`
	MaxSessionHours    int    `json:"max_session_hours"`
	DownloadMacOSURL   string `json:"download_macos_url"`
	DownloadWindowsURL string `json:"download_windows_url"`
}

func LoadSettings(ctx context.Context, q db.Querier) (Settings, error) {
	s := Settings{MaxSessionHours: 12}
	rows, err := q.Query(ctx, `SELECT key, value FROM app_settings`)
	if err != nil {
		return s, err
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		var v []byte
		if err := rows.Scan(&k, &v); err != nil {
			return s, err
		}
		var dst any
		switch k {
		case "signup_allowed":
			dst = &s.SignupAllowed
		case "default_auto_accept":
			dst = &s.DefaultAutoAccept
		case "max_session_hours":
			dst = &s.MaxSessionHours
		case "download_macos_url":
			dst = &s.DownloadMacOSURL
		case "download_windows_url":
			dst = &s.DownloadWindowsURL
		default:
			continue
		}
		if err := json.Unmarshal(v, dst); err != nil {
			return s, err
		}
	}
	return s, rows.Err()
}

func SaveSettings(ctx context.Context, q db.Querier, s Settings) error {
	vals := map[string]any{
		"signup_allowed":       s.SignupAllowed,
		"default_auto_accept":  s.DefaultAutoAccept,
		"max_session_hours":    s.MaxSessionHours,
		"download_macos_url":   s.DownloadMacOSURL,
		"download_windows_url": s.DownloadWindowsURL,
	}
	for k, v := range vals {
		raw, _ := json.Marshal(v)
		if _, err := q.Exec(ctx, `INSERT INTO app_settings (key, value) VALUES ($1, $2)
			ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, k, raw); err != nil {
			return err
		}
	}
	return nil
}

// ---------- overview ----------

type Overview struct {
	Users struct {
		Total    int `json:"total"`
		Admins   int `json:"admins"`
		Disabled int `json:"disabled"`
	} `json:"users"`
	Devices struct {
		Total    int            `json:"total"`
		Online   int            `json:"online"`
		Disabled int            `json:"disabled"`
		ByOS     map[string]int `json:"by_os"`
	} `json:"devices"`
	Sessions struct {
		Active          int            `json:"active"`
		Pending         int            `json:"pending"`
		Today           int            `json:"today"`
		RelayShare24h   float64        `json:"relay_share_24h"`
		AvgDurationS24h int            `json:"avg_duration_s_24h"`
		ByPath24h       map[string]int `json:"by_path_24h"`
		Failed24h       int            `json:"failed_24h"`
	} `json:"sessions"`
	Series []SeriesPoint `json:"series"`
}

type SeriesPoint struct {
	Hour     time.Time `json:"hour"`
	Sessions int       `json:"sessions"`
	Relay    int       `json:"relay"`
	Failed   int       `json:"failed"`
}

func LoadOverview(ctx context.Context, q db.Querier) (*Overview, error) {
	o := &Overview{}
	if err := q.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE is_admin), count(*) FILTER (WHERE disabled_at IS NOT NULL) FROM users`).
		Scan(&o.Users.Total, &o.Users.Admins, &o.Users.Disabled); err != nil {
		return nil, err
	}
	if err := q.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE status = 'disabled') FROM devices WHERE removed_at IS NULL`).
		Scan(&o.Devices.Total, &o.Devices.Disabled); err != nil {
		return nil, err
	}
	o.Devices.ByOS = map[string]int{"darwin": 0, "windows": 0, "linux": 0}
	rows, err := q.Query(ctx, `SELECT os, count(*) FROM devices WHERE removed_at IS NULL AND os <> '' GROUP BY os`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var k string
		var n int
		if err := rows.Scan(&k, &n); err != nil {
			rows.Close()
			return nil, err
		}
		o.Devices.ByOS[k] = n
	}
	rows.Close()

	var relay, withPath int
	var avg *float64
	s := &o.Sessions
	if err := q.QueryRow(ctx, `SELECT
			count(*) FILTER (WHERE state = 'active'),
			count(*) FILTER (WHERE state = 'pending'),
			count(*) FILTER (WHERE created_at >= date_trunc('day', now())),
			count(*) FILTER (WHERE created_at > now() - interval '24 hours' AND ice_path = 'relay'),
			count(*) FILTER (WHERE created_at > now() - interval '24 hours' AND ice_path IS NOT NULL),
			avg(extract(epoch FROM ended_at - connected_at)) FILTER (WHERE created_at > now() - interval '24 hours' AND state = 'ended' AND connected_at IS NOT NULL),
			count(*) FILTER (WHERE created_at > now() - interval '24 hours' AND state = 'failed'),
			count(*) FILTER (WHERE created_at > now() - interval '24 hours' AND ice_path = 'host'),
			count(*) FILTER (WHERE created_at > now() - interval '24 hours' AND ice_path = 'srflx')
		FROM sessions`).Scan(&s.Active, &s.Pending, &s.Today, &relay, &withPath, &avg, &s.Failed24h, new(int), new(int)); err != nil {
		return nil, err
	}
	if withPath > 0 {
		s.RelayShare24h = float64(relay) / float64(withPath)
	}
	if avg != nil {
		s.AvgDurationS24h = int(*avg)
	}
	s.ByPath24h = map[string]int{"host": 0, "srflx": 0, "relay": 0}
	prow, err := q.Query(ctx, `SELECT ice_path, count(*) FROM sessions WHERE created_at > now() - interval '24 hours' AND ice_path IS NOT NULL GROUP BY ice_path`)
	if err != nil {
		return nil, err
	}
	for prow.Next() {
		var k string
		var n int
		if err := prow.Scan(&k, &n); err != nil {
			prow.Close()
			return nil, err
		}
		s.ByPath24h[k] = n
	}
	prow.Close()

	srows, err := q.Query(ctx, `SELECT h.hour, count(s.id), count(s.id) FILTER (WHERE s.ice_path = 'relay'), count(s.id) FILTER (WHERE s.state = 'failed')
		FROM generate_series(date_trunc('hour', now()) - interval '23 hours', date_trunc('hour', now()), interval '1 hour') AS h(hour)
		LEFT JOIN sessions s ON s.created_at >= h.hour AND s.created_at < h.hour + interval '1 hour'
		GROUP BY h.hour ORDER BY h.hour`)
	if err != nil {
		return nil, err
	}
	defer srows.Close()
	o.Series = []SeriesPoint{}
	for srows.Next() {
		var p SeriesPoint
		if err := srows.Scan(&p.Hour, &p.Sessions, &p.Relay, &p.Failed); err != nil {
			return nil, err
		}
		o.Series = append(o.Series, p)
	}
	return o, srows.Err()
}
