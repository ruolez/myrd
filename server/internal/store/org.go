package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ruolez/remotectl/server/internal/db"
)

// ---------- folders ----------

type Folder struct {
	ID        string    `json:"id"`
	ParentID  *string   `json:"parent_id"`
	Name      string    `json:"name"`
	Position  int       `json:"position"`
	CreatedAt time.Time `json:"created_at"`
	UserID    string    `json:"-"`
}

const folderCols = `id, user_id, parent_id, name, position, created_at`

func scanFolder(row interface{ Scan(...any) error }) (*Folder, error) {
	var f Folder
	if err := row.Scan(&f.ID, &f.UserID, &f.ParentID, &f.Name, &f.Position, &f.CreatedAt); err != nil {
		return nil, notFound(err)
	}
	return &f, nil
}

func ListFolders(ctx context.Context, q db.Querier, userID string) ([]Folder, error) {
	rows, err := q.Query(ctx, `SELECT `+folderCols+` FROM device_folders WHERE user_id = $1 ORDER BY position, lower(name)`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Folder{}
	for rows.Next() {
		f, err := scanFolder(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *f)
	}
	return out, rows.Err()
}

func FolderByID(ctx context.Context, q db.Querier, id string) (*Folder, error) {
	return scanFolder(q.QueryRow(ctx, `SELECT `+folderCols+` FROM device_folders WHERE id = $1`, id))
}

func CountFolders(ctx context.Context, q db.Querier, userID string) (int, error) {
	var n int
	err := q.QueryRow(ctx, `SELECT count(*) FROM device_folders WHERE user_id = $1`, userID).Scan(&n)
	return n, err
}

// FolderDepth is the number of folders from the root down to id (a top-level folder is 1).
func FolderDepth(ctx context.Context, q db.Querier, id string) (int, error) {
	var n int
	err := q.QueryRow(ctx, `WITH RECURSIVE up AS (
			SELECT id, parent_id, 1 AS depth FROM device_folders WHERE id = $1
			UNION ALL
			SELECT f.id, f.parent_id, up.depth + 1 FROM device_folders f JOIN up ON f.id = up.parent_id WHERE up.depth < 64)
		SELECT coalesce(max(depth), 0) FROM up`, id).Scan(&n)
	return n, err
}

// SubtreeHeight is the number of levels in id's subtree, id included.
func SubtreeHeight(ctx context.Context, q db.Querier, id string) (int, error) {
	var n int
	err := q.QueryRow(ctx, `WITH RECURSIVE down AS (
			SELECT id, 1 AS height FROM device_folders WHERE id = $1
			UNION ALL
			SELECT f.id, down.height + 1 FROM device_folders f JOIN down ON f.parent_id = down.id WHERE down.height < 64)
		SELECT coalesce(max(height), 0) FROM down`, id).Scan(&n)
	return n, err
}

// IsFolderAncestor reports whether ancestor is folder id or one of its parents.
func IsFolderAncestor(ctx context.Context, q db.Querier, ancestor, id string) (bool, error) {
	var ok bool
	err := q.QueryRow(ctx, `WITH RECURSIVE up AS (
			SELECT id, parent_id, 1 AS depth FROM device_folders WHERE id = $2
			UNION ALL
			SELECT f.id, f.parent_id, up.depth + 1 FROM device_folders f JOIN up ON f.id = up.parent_id WHERE up.depth < 64)
		SELECT EXISTS (SELECT 1 FROM up WHERE id = $1)`, ancestor, id).Scan(&ok)
	return ok, err
}

func CreateFolder(ctx context.Context, q db.Querier, userID string, parentID *string, name string) (*Folder, error) {
	f, err := scanFolder(q.QueryRow(ctx, `INSERT INTO device_folders (user_id, parent_id, name, position)
		VALUES ($1, $2, $3, (SELECT coalesce(max(position), -1) + 1 FROM device_folders
		                     WHERE user_id = $1 AND parent_id IS NOT DISTINCT FROM $2))
		RETURNING `+folderCols, userID, parentID, name))
	if err != nil && isUnique(err) {
		return nil, ErrConflict
	}
	return f, err
}

type FolderPatch struct {
	Name      *string
	SetParent bool
	ParentID  *string
	Position  *int
}

func UpdateFolder(ctx context.Context, q db.Querier, id string, p FolderPatch) error {
	_, err := q.Exec(ctx, `UPDATE device_folders SET name = coalesce($2, name),
		parent_id = CASE WHEN $3 THEN $4::uuid ELSE parent_id END, position = coalesce($5, position)
		WHERE id = $1`, id, p.Name, p.SetParent, p.ParentID, p.Position)
	if err != nil && isUnique(err) {
		return ErrConflict
	}
	return err
}

// DeleteFolder moves the folder's subfolders and devices to its parent, then
// deletes it. A subfolder whose name is taken in the parent gets a suffix.
// Run it in a transaction.
func DeleteFolder(ctx context.Context, q db.Querier, f *Folder) error {
	children, err := childFolders(ctx, q, f.ID)
	if err != nil {
		return err
	}
	taken, err := siblingNames(ctx, q, f.UserID, f.ParentID, f.ID)
	if err != nil {
		return err
	}
	for _, c := range children {
		name := freeName(c.Name, taken)
		taken[strings.ToLower(name)] = true
		if _, err := q.Exec(ctx, `UPDATE device_folders SET parent_id = $2, name = $3 WHERE id = $1`, c.ID, f.ParentID, name); err != nil {
			return err
		}
	}
	if _, err := q.Exec(ctx, `UPDATE devices SET folder_id = $2 WHERE folder_id = $1`, f.ID, f.ParentID); err != nil {
		return err
	}
	_, err = q.Exec(ctx, `DELETE FROM device_folders WHERE id = $1`, f.ID)
	return err
}

func childFolders(ctx context.Context, q db.Querier, id string) ([]Folder, error) {
	rows, err := q.Query(ctx, `SELECT `+folderCols+` FROM device_folders WHERE parent_id = $1 ORDER BY position`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Folder
	for rows.Next() {
		f, err := scanFolder(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *f)
	}
	return out, rows.Err()
}

func siblingNames(ctx context.Context, q db.Querier, userID string, parentID *string, except string) (map[string]bool, error) {
	rows, err := q.Query(ctx, `SELECT lower(name) FROM device_folders
		WHERE user_id = $1 AND parent_id IS NOT DISTINCT FROM $2 AND id <> $3`, userID, parentID, except)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out[n] = true
	}
	return out, rows.Err()
}

// freeName returns name, or "name (2)", "name (3)", … whichever is not taken
// (case-insensitively), kept within the 64-character limit.
func freeName(name string, taken map[string]bool) string {
	if !taken[strings.ToLower(name)] {
		return name
	}
	for i := 2; ; i++ {
		suffix := fmt.Sprintf(" (%d)", i)
		base := []rune(name)
		if len(base)+len([]rune(suffix)) > 64 {
			base = base[:64-len([]rune(suffix))]
		}
		cand := string(base) + suffix
		if !taken[strings.ToLower(cand)] {
			return cand
		}
	}
}

// ---------- tags ----------

type Tag struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Color     int       `json:"color"`
	CreatedAt time.Time `json:"created_at"`
	UserID    string    `json:"-"`
}

const tagCols = `id, user_id, name, color, created_at`

func scanTag(row interface{ Scan(...any) error }) (*Tag, error) {
	var t Tag
	if err := row.Scan(&t.ID, &t.UserID, &t.Name, &t.Color, &t.CreatedAt); err != nil {
		return nil, notFound(err)
	}
	return &t, nil
}

func ListTags(ctx context.Context, q db.Querier, userID string) ([]Tag, error) {
	rows, err := q.Query(ctx, `SELECT `+tagCols+` FROM tags WHERE user_id = $1 ORDER BY lower(name)`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Tag{}
	for rows.Next() {
		t, err := scanTag(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

func TagByID(ctx context.Context, q db.Querier, id string) (*Tag, error) {
	return scanTag(q.QueryRow(ctx, `SELECT `+tagCols+` FROM tags WHERE id = $1`, id))
}

func CountTags(ctx context.Context, q db.Querier, userID string) (int, error) {
	var n int
	err := q.QueryRow(ctx, `SELECT count(*) FROM tags WHERE user_id = $1`, userID).Scan(&n)
	return n, err
}

func CreateTag(ctx context.Context, q db.Querier, userID, name string, color int) (*Tag, error) {
	t, err := scanTag(q.QueryRow(ctx, `INSERT INTO tags (user_id, name, color) VALUES ($1, $2, $3) RETURNING `+tagCols, userID, name, color))
	if err != nil && isUnique(err) {
		return nil, ErrConflict
	}
	return t, err
}

func UpdateTag(ctx context.Context, q db.Querier, id string, name *string, color *int) error {
	_, err := q.Exec(ctx, `UPDATE tags SET name = coalesce($2, name), color = coalesce($3, color) WHERE id = $1`, id, name, color)
	if err != nil && isUnique(err) {
		return ErrConflict
	}
	return err
}

func DeleteTag(ctx context.Context, q db.Querier, id string) error {
	_, err := q.Exec(ctx, `DELETE FROM tags WHERE id = $1`, id)
	return err
}

// ---------- ownership checks ----------

// OwnsAll reports whether every id names a row of table owned by userID
// (devices that were removed do not count).
func OwnsAll(ctx context.Context, q db.Querier, table, userID string, ids []string) (bool, error) {
	if len(ids) == 0 {
		return true, nil
	}
	extra := ""
	switch table {
	case "devices":
		extra = " AND removed_at IS NULL"
	case "device_folders", "tags":
	default:
		return false, fmt.Errorf("OwnsAll: unknown table %q", table)
	}
	var n int
	err := q.QueryRow(ctx, `SELECT count(DISTINCT id) FROM `+table+` WHERE id = ANY($2::uuid[]) AND user_id = $1`+extra, userID, ids).Scan(&n)
	return n == len(dedupe(ids)), err
}

func dedupe(ids []string) []string {
	seen := map[string]bool{}
	out := ids[:0:0]
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// ---------- device organization ----------

type DeviceOrg struct {
	SetFolder bool
	FolderID  *string
	Favorite  *bool
	Notes     *string
	TagIDs    *[]string // full replacement
}

func SetDeviceOrg(ctx context.Context, q db.Querier, id string, o DeviceOrg) error {
	if _, err := q.Exec(ctx, `UPDATE devices SET folder_id = CASE WHEN $2 THEN $3::uuid ELSE folder_id END,
		favorite = coalesce($4, favorite), notes = coalesce($5, notes) WHERE id = $1`,
		id, o.SetFolder, o.FolderID, o.Favorite, o.Notes); err != nil {
		return err
	}
	if o.TagIDs == nil {
		return nil
	}
	if _, err := q.Exec(ctx, `DELETE FROM device_tags WHERE device_id = $1 AND NOT (tag_id = ANY($2::uuid[]))`, id, *o.TagIDs); err != nil {
		return err
	}
	_, err := q.Exec(ctx, `INSERT INTO device_tags (device_id, tag_id) SELECT $1, unnest($2::uuid[]) ON CONFLICT DO NOTHING`, id, *o.TagIDs)
	return err
}

type BulkOrg struct {
	SetFolder    bool
	FolderID     *string
	Favorite     *bool
	AddTagIDs    []string
	RemoveTagIDs []string
}

func BulkDeviceOrg(ctx context.Context, q db.Querier, ids []string, b BulkOrg) error {
	if _, err := q.Exec(ctx, `UPDATE devices SET folder_id = CASE WHEN $2 THEN $3::uuid ELSE folder_id END,
		favorite = coalesce($4, favorite) WHERE id = ANY($1::uuid[])`, ids, b.SetFolder, b.FolderID, b.Favorite); err != nil {
		return err
	}
	if len(b.RemoveTagIDs) > 0 {
		if _, err := q.Exec(ctx, `DELETE FROM device_tags WHERE device_id = ANY($1::uuid[]) AND tag_id = ANY($2::uuid[])`, ids, b.RemoveTagIDs); err != nil {
			return err
		}
	}
	if len(b.AddTagIDs) > 0 {
		if _, err := q.Exec(ctx, `INSERT INTO device_tags (device_id, tag_id)
			SELECT d, t FROM unnest($1::uuid[]) d CROSS JOIN unnest($2::uuid[]) t ON CONFLICT DO NOTHING`, ids, b.AddTagIDs); err != nil {
			return err
		}
	}
	return nil
}

// TagCountOver returns a device among ids that would carry more than max tags.
func TagCountOver(ctx context.Context, q db.Querier, ids []string, max int) (string, bool, error) {
	var id string
	err := q.QueryRow(ctx, `SELECT device_id FROM device_tags WHERE device_id = ANY($1::uuid[])
		GROUP BY device_id HAVING count(*) > $2 LIMIT 1`, ids, max).Scan(&id)
	if err != nil {
		if notFound(err) == ErrNotFound {
			return "", false, nil
		}
		return "", false, err
	}
	return id, true, nil
}
