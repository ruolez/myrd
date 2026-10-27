package store

import (
	"context"
	"time"

	"github.com/ruolez/remotectl/server/internal/db"
)

type InstallLink struct {
	CodeSealed []byte
	CreatedAt  time.Time
	LastUsedAt *time.Time
	UseCount   int
}

const installLinkCols = `code_sealed, created_at, last_used_at, use_count`

func scanInstallLink(row interface{ Scan(...any) error }) (*InstallLink, error) {
	var l InstallLink
	if err := row.Scan(&l.CodeSealed, &l.CreatedAt, &l.LastUsedAt, &l.UseCount); err != nil {
		return nil, notFound(err)
	}
	return &l, nil
}

// PutInstallLink gives the user a new link, replacing any previous one.
func PutInstallLink(ctx context.Context, q db.Querier, userID string, hash, sealed []byte) (*InstallLink, error) {
	return scanInstallLink(q.QueryRow(ctx, `INSERT INTO install_links (user_id, code_hash, code_sealed) VALUES ($1, $2, $3)
		ON CONFLICT (user_id) DO UPDATE SET code_hash = $2, code_sealed = $3, created_at = now(), last_used_at = NULL, use_count = 0
		RETURNING `+installLinkCols, userID, hash, sealed))
}

func InstallLinkByUser(ctx context.Context, q db.Querier, userID string) (*InstallLink, error) {
	return scanInstallLink(q.QueryRow(ctx, `SELECT `+installLinkCols+` FROM install_links WHERE user_id = $1`, userID))
}

// DeleteInstallLink reports whether the user had a link.
func DeleteInstallLink(ctx context.Context, q db.Querier, userID string) (bool, error) {
	tag, err := q.Exec(ctx, `DELETE FROM install_links WHERE user_id = $1`, userID)
	return err == nil && tag.RowsAffected() == 1, err
}

// InstallLinkLive reports whether hash is the link of an enabled user.
func InstallLinkLive(ctx context.Context, q db.Querier, hash []byte) (bool, error) {
	var ok bool
	err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM install_links l JOIN users u ON u.id = l.user_id
		WHERE l.code_hash = $1 AND u.disabled_at IS NULL)`, hash).Scan(&ok)
	return ok, err
}

// UseInstallLink counts an enrollment through the link and returns its owner.
func UseInstallLink(ctx context.Context, q db.Querier, hash []byte) (userID string, err error) {
	err = q.QueryRow(ctx, `UPDATE install_links l SET use_count = use_count + 1, last_used_at = now()
		FROM users u WHERE l.code_hash = $1 AND u.id = l.user_id AND u.disabled_at IS NULL
		RETURNING l.user_id`, hash).Scan(&userID)
	return userID, notFound(err)
}
