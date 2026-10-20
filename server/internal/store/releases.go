package store

import (
	"context"
	"time"

	"github.com/ruolez/remotectl/server/internal/db"
)

type Release struct {
	ID          string     `json:"id"`
	Version     string     `json:"version"`
	OS          string     `json:"os"`
	Arch        string     `json:"arch"`
	URL         string     `json:"url"`
	Filename    string     `json:"filename"`
	Size        int64      `json:"size"`
	SHA256      string     `json:"sha256"`
	Signature   string     `json:"signature"`
	KeyID       string     `json:"key_id"`
	RolloutPct  int        `json:"rollout_pct"`
	PublishedAt *time.Time `json:"published_at"`
	CreatedAt   time.Time  `json:"created_at"`
	Installer   bool       `json:"installer"`
	Devices     int        `json:"devices"`
}

const releaseCols = `r.id, r.version, r.os, r.arch, '/v1/releases/' || r.id || '/file', r.filename, r.size, r.sha256,
	r.signature, r.key_id, r.rollout_pct, r.published_at, r.created_at,
	EXISTS (SELECT 1 FROM agent_downloads ad WHERE ad.release_id = r.id),
	(SELECT count(*) FROM devices d WHERE d.removed_at IS NULL AND d.os = r.os AND d.agent_version = r.version)`

func scanRelease(row interface{ Scan(...any) error }) (*Release, error) {
	var r Release
	if err := row.Scan(&r.ID, &r.Version, &r.OS, &r.Arch, &r.URL, &r.Filename, &r.Size, &r.SHA256, &r.Signature, &r.KeyID,
		&r.RolloutPct, &r.PublishedAt, &r.CreatedAt, &r.Installer, &r.Devices); err != nil {
		return nil, notFound(err)
	}
	return &r, nil
}

func ListReleases(ctx context.Context, q db.Querier) ([]Release, error) {
	rows, err := q.Query(ctx, `SELECT `+releaseCols+` FROM agent_releases r ORDER BY r.created_at DESC`)
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

func ReleaseByID(ctx context.Context, q db.Querier, id string) (*Release, error) {
	return scanRelease(q.QueryRow(ctx, `SELECT `+releaseCols+` FROM agent_releases r WHERE r.id = $1`, id))
}

func ReleaseExists(ctx context.Context, q db.Querier, version, os, arch string) (bool, error) {
	var ok bool
	err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM agent_releases WHERE version = $1 AND os = $2 AND arch = $3)`,
		version, os, arch).Scan(&ok)
	return ok, err
}

// CreateRelease inserts an unpublished release under r.ID.
func CreateRelease(ctx context.Context, q db.Querier, r Release, uploadedBy string) (*Release, error) {
	if _, err := q.Exec(ctx, `INSERT INTO agent_releases (id, version, os, arch, filename, size, sha256, signature, key_id, rollout_pct, uploaded_by)
		VALUES ($1, $2, $3, $4, $5, $6, lower($7), $8, $9, $10, $11)`,
		r.ID, r.Version, r.OS, r.Arch, r.Filename, r.Size, r.SHA256, r.Signature, r.KeyID, r.RolloutPct, uploadedBy); err != nil {
		if isUnique(err) {
			return nil, ErrConflict
		}
		return nil, err
	}
	return ReleaseByID(ctx, q, r.ID)
}

func UpdateRelease(ctx context.Context, q db.Querier, id string, rollout *int, published *bool) (*Release, error) {
	tag, err := q.Exec(ctx, `UPDATE agent_releases SET rollout_pct = coalesce($2, rollout_pct),
		published_at = CASE WHEN $3::bool IS NULL THEN published_at WHEN $3 THEN coalesce(published_at, now()) ELSE NULL END
		WHERE id = $1`, id, rollout, published)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrNotFound
	}
	return ReleaseByID(ctx, q, id)
}

func DeleteRelease(ctx context.Context, q db.Querier, id string) error {
	tag, err := q.Exec(ctx, `DELETE FROM agent_releases WHERE id = $1`, id)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}
