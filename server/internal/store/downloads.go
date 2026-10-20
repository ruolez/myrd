package store

import (
	"context"
	"time"

	"github.com/ruolez/remotectl/server/internal/db"
)

type AgentDownload struct {
	OS         string    `json:"os"`
	Filename   string    `json:"filename"`
	Size       int64     `json:"size"`
	SHA256     string    `json:"sha256"`
	UploadedAt time.Time `json:"uploaded_at"`
	ReleaseID  *string   `json:"release_id"` // set when the file is a release's (Admin → Releases)
}

func UpsertAgentDownload(ctx context.Context, q db.Querier, d AgentDownload, userID string) (*AgentDownload, error) {
	err := q.QueryRow(ctx, `INSERT INTO agent_downloads (os, filename, size, sha256, uploaded_by, release_id) VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (os) DO UPDATE SET filename = $2, size = $3, sha256 = $4, uploaded_by = $5, release_id = $6, uploaded_at = now()
		RETURNING uploaded_at`, d.OS, d.Filename, d.Size, d.SHA256, userID, d.ReleaseID).Scan(&d.UploadedAt)
	if err != nil {
		return nil, err
	}
	return &d, nil
}

func AgentDownloadByOS(ctx context.Context, q db.Querier, os string) (*AgentDownload, error) {
	var d AgentDownload
	err := q.QueryRow(ctx, `SELECT os, filename, size, sha256, uploaded_at, release_id FROM agent_downloads WHERE os = $1`, os).
		Scan(&d.OS, &d.Filename, &d.Size, &d.SHA256, &d.UploadedAt, &d.ReleaseID)
	if err != nil {
		return nil, notFound(err)
	}
	return &d, nil
}

func ListAgentDownloads(ctx context.Context, q db.Querier) (map[string]AgentDownload, error) {
	rows, err := q.Query(ctx, `SELECT os, filename, size, sha256, uploaded_at, release_id FROM agent_downloads`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]AgentDownload{}
	for rows.Next() {
		var d AgentDownload
		if err := rows.Scan(&d.OS, &d.Filename, &d.Size, &d.SHA256, &d.UploadedAt, &d.ReleaseID); err != nil {
			return nil, err
		}
		out[d.OS] = d
	}
	return out, rows.Err()
}

// DeleteAgentDownload reports whether a row existed.
func DeleteAgentDownload(ctx context.Context, q db.Querier, os string) (bool, error) {
	tag, err := q.Exec(ctx, `DELETE FROM agent_downloads WHERE os = $1`, os)
	return err == nil && tag.RowsAffected() == 1, err
}
