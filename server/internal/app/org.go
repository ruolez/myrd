package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/ruolez/remotectl/server/internal/store"
)

const (
	maxFolders       = 500
	maxFolderDepth   = 8
	maxTags          = 200
	maxTagsPerDevice = 20
	maxNotes         = 4000
	maxBulkDevices   = 500
	tagColors        = 12
)

// optionalID decodes a JSON field that may be absent (set=false), null
// (set=true, id=nil) or a UUID string.
func optionalID(raw json.RawMessage, field string) (set bool, id *string, err error) {
	if raw == nil {
		return false, nil, nil
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return true, nil, nil
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return false, nil, errf(http.StatusBadRequest, "bad_request", field+" must be an id or null")
	}
	if _, err := uuid.Parse(s); err != nil {
		return false, nil, errf(http.StatusBadRequest, "bad_request", field+" is not a valid id")
	}
	return true, &s, nil
}

func validIDs(ids []string, field string) ([]string, error) {
	out := make([]string, 0, len(ids))
	seen := map[string]bool{}
	for _, id := range ids {
		if _, err := uuid.Parse(id); err != nil {
			return nil, errf(http.StatusBadRequest, "bad_request", field+" contains an invalid id")
		}
		id = strings.ToLower(id)
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out, nil
}

func cleanName(s string, max int, what string) (string, error) {
	n := strings.TrimSpace(s)
	if n == "" || utf8.RuneCountInString(n) > max || !utf8.ValidString(n) {
		return "", errf(http.StatusBadRequest, "bad_request", what+" must be 1 to "+strconv.Itoa(max)+" characters")
	}
	return n, nil
}

func (a *App) requireOwned(r *http.Request, table string, ids []string, what string) error {
	ok, err := store.OwnsAll(r.Context(), a.pool, table, userFrom(r).ID, ids)
	if err != nil {
		return err
	}
	if !ok {
		return errf(http.StatusNotFound, "not_found", what+" not found")
	}
	return nil
}

// ---------- folders ----------

func (a *App) listFolders(w http.ResponseWriter, r *http.Request) error {
	fs, err := store.ListFolders(r.Context(), a.pool, userFrom(r).ID)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": fs})
	return nil
}

func (a *App) ownFolder(r *http.Request) (*store.Folder, error) {
	id, err := pathID(r, "id")
	if err != nil {
		return nil, err
	}
	f, err := store.FolderByID(r.Context(), a.pool, id)
	if err != nil {
		return nil, err
	}
	if f.UserID != userFrom(r).ID {
		return nil, errNotFound
	}
	return f, nil
}

// checkParent validates moving a subtree of the given height under parent.
func (a *App) checkParent(r *http.Request, parent *string, height int) error {
	if parent == nil {
		if height > maxFolderDepth {
			return errf(http.StatusBadRequest, "bad_request", "folders nest at most "+strconv.Itoa(maxFolderDepth)+" levels")
		}
		return nil
	}
	if err := a.requireOwned(r, "device_folders", []string{*parent}, "parent folder"); err != nil {
		return err
	}
	depth, err := store.FolderDepth(r.Context(), a.pool, *parent)
	if err != nil {
		return err
	}
	if depth+height > maxFolderDepth {
		return errf(http.StatusBadRequest, "bad_request", "folders nest at most "+strconv.Itoa(maxFolderDepth)+" levels")
	}
	return nil
}

var errFolderExists = errf(http.StatusConflict, "conflict", "a folder with that name already exists there")

func (a *App) createFolder(w http.ResponseWriter, r *http.Request) error {
	u := userFrom(r)
	var body struct {
		Name     string          `json:"name"`
		ParentID json.RawMessage `json:"parent_id"`
	}
	if err := decode(r, &body); err != nil {
		return err
	}
	name, err := cleanName(body.Name, 64, "name")
	if err != nil {
		return err
	}
	_, parent, err := optionalID(body.ParentID, "parent_id")
	if err != nil {
		return err
	}
	ctx := r.Context()
	if n, err := store.CountFolders(ctx, a.pool, u.ID); err != nil {
		return err
	} else if n >= maxFolders {
		return errf(http.StatusBadRequest, "bad_request", "at most "+strconv.Itoa(maxFolders)+" folders")
	}
	if err := a.checkParent(r, parent, 1); err != nil {
		return err
	}
	f, err := store.CreateFolder(ctx, a.pool, u.ID, parent, name)
	if errors.Is(err, store.ErrConflict) {
		return errFolderExists
	}
	if err != nil {
		return err
	}
	a.hub.PushOrgChanged(u.ID, "folders", []string{f.ID})
	writeJSON(w, http.StatusOK, map[string]any{"folder": f})
	return nil
}

func (a *App) patchFolder(w http.ResponseWriter, r *http.Request) error {
	f, err := a.ownFolder(r)
	if err != nil {
		return err
	}
	var body struct {
		Name     *string         `json:"name"`
		ParentID json.RawMessage `json:"parent_id"`
		Position *int            `json:"position"`
	}
	if err := decode(r, &body); err != nil {
		return err
	}
	var p store.FolderPatch
	if body.Name != nil {
		n, err := cleanName(*body.Name, 64, "name")
		if err != nil {
			return err
		}
		p.Name = &n
	}
	if body.Position != nil {
		if *body.Position < 0 || *body.Position > 1_000_000 {
			return errf(http.StatusBadRequest, "bad_request", "position out of range")
		}
		p.Position = body.Position
	}
	if p.SetParent, p.ParentID, err = optionalID(body.ParentID, "parent_id"); err != nil {
		return err
	}
	ctx := r.Context()
	if p.SetParent {
		if p.ParentID != nil {
			cycle, err := store.IsFolderAncestor(ctx, a.pool, f.ID, *p.ParentID)
			if err != nil {
				return err
			}
			if cycle {
				return errf(http.StatusBadRequest, "bad_request", "a folder cannot move into itself or its subfolders")
			}
		}
		height, err := store.SubtreeHeight(ctx, a.pool, f.ID)
		if err != nil {
			return err
		}
		if err := a.checkParent(r, p.ParentID, height); err != nil {
			return err
		}
	}
	if err := store.UpdateFolder(ctx, a.pool, f.ID, p); errors.Is(err, store.ErrConflict) {
		return errFolderExists
	} else if err != nil {
		return err
	}
	nf, err := store.FolderByID(ctx, a.pool, f.ID)
	if err != nil {
		return err
	}
	a.hub.PushOrgChanged(f.UserID, "folders", []string{f.ID})
	writeJSON(w, http.StatusOK, map[string]any{"folder": nf})
	return nil
}

func (a *App) deleteFolder(w http.ResponseWriter, r *http.Request) error {
	f, err := a.ownFolder(r)
	if err != nil {
		return err
	}
	ctx := r.Context()
	tx, err := a.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := store.DeleteFolder(ctx, tx, f); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	a.hub.PushOrgChanged(f.UserID, "folders", []string{f.ID})
	writeJSON(w, http.StatusOK, map[string]any{})
	return nil
}

// ---------- tags ----------

func (a *App) listTags(w http.ResponseWriter, r *http.Request) error {
	ts, err := store.ListTags(r.Context(), a.pool, userFrom(r).ID)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": ts})
	return nil
}

func (a *App) ownTag(r *http.Request) (*store.Tag, error) {
	id, err := pathID(r, "id")
	if err != nil {
		return nil, err
	}
	t, err := store.TagByID(r.Context(), a.pool, id)
	if err != nil {
		return nil, err
	}
	if t.UserID != userFrom(r).ID {
		return nil, errNotFound
	}
	return t, nil
}

func validColor(c *int) error {
	if c != nil && (*c < 0 || *c >= tagColors) {
		return errf(http.StatusBadRequest, "bad_request", "color must be 0 to "+strconv.Itoa(tagColors-1))
	}
	return nil
}

var errTagExists = errf(http.StatusConflict, "conflict", "a tag with that name already exists")

func (a *App) createTag(w http.ResponseWriter, r *http.Request) error {
	u := userFrom(r)
	var body struct {
		Name  string `json:"name"`
		Color *int   `json:"color"`
	}
	if err := decode(r, &body); err != nil {
		return err
	}
	name, err := cleanName(body.Name, 32, "name")
	if err != nil {
		return err
	}
	if err := validColor(body.Color); err != nil {
		return err
	}
	color := 0
	if body.Color != nil {
		color = *body.Color
	}
	ctx := r.Context()
	if n, err := store.CountTags(ctx, a.pool, u.ID); err != nil {
		return err
	} else if n >= maxTags {
		return errf(http.StatusBadRequest, "bad_request", "at most "+strconv.Itoa(maxTags)+" tags")
	}
	t, err := store.CreateTag(ctx, a.pool, u.ID, name, color)
	if errors.Is(err, store.ErrConflict) {
		return errTagExists
	}
	if err != nil {
		return err
	}
	a.hub.PushOrgChanged(u.ID, "tags", []string{t.ID})
	writeJSON(w, http.StatusOK, map[string]any{"tag": t})
	return nil
}

func (a *App) patchTag(w http.ResponseWriter, r *http.Request) error {
	t, err := a.ownTag(r)
	if err != nil {
		return err
	}
	var body struct {
		Name  *string `json:"name"`
		Color *int    `json:"color"`
	}
	if err := decode(r, &body); err != nil {
		return err
	}
	if body.Name != nil {
		n, err := cleanName(*body.Name, 32, "name")
		if err != nil {
			return err
		}
		body.Name = &n
	}
	if err := validColor(body.Color); err != nil {
		return err
	}
	ctx := r.Context()
	if err := store.UpdateTag(ctx, a.pool, t.ID, body.Name, body.Color); errors.Is(err, store.ErrConflict) {
		return errTagExists
	} else if err != nil {
		return err
	}
	nt, err := store.TagByID(ctx, a.pool, t.ID)
	if err != nil {
		return err
	}
	a.hub.PushOrgChanged(t.UserID, "tags", []string{t.ID})
	writeJSON(w, http.StatusOK, map[string]any{"tag": nt})
	return nil
}

func (a *App) deleteTag(w http.ResponseWriter, r *http.Request) error {
	t, err := a.ownTag(r)
	if err != nil {
		return err
	}
	if err := store.DeleteTag(r.Context(), a.pool, t.ID); err != nil {
		return err
	}
	a.hub.PushOrgChanged(t.UserID, "tags", []string{t.ID})
	writeJSON(w, http.StatusOK, map[string]any{})
	return nil
}

// ---------- device organization ----------

type orgPatch struct {
	FolderID json.RawMessage `json:"folder_id"`
	Favorite *bool           `json:"favorite"`
	Notes    *string         `json:"notes"`
	TagIDs   *[]string       `json:"tag_ids"`
}

func (p orgPatch) empty() bool {
	return p.FolderID == nil && p.Favorite == nil && p.Notes == nil && p.TagIDs == nil
}

// deviceOrg validates an organization patch of one device.
func (a *App) deviceOrg(r *http.Request, p orgPatch) (store.DeviceOrg, error) {
	o := store.DeviceOrg{Favorite: p.Favorite}
	var err error
	if o.SetFolder, o.FolderID, err = optionalID(p.FolderID, "folder_id"); err != nil {
		return o, err
	}
	if o.FolderID != nil {
		if err := a.requireOwned(r, "device_folders", []string{*o.FolderID}, "folder"); err != nil {
			return o, err
		}
	}
	if p.Notes != nil {
		n := strings.TrimSpace(*p.Notes)
		if utf8.RuneCountInString(n) > maxNotes || !utf8.ValidString(n) {
			return o, errf(http.StatusBadRequest, "bad_request", "notes must be at most "+strconv.Itoa(maxNotes)+" characters")
		}
		o.Notes = &n
	}
	if p.TagIDs != nil {
		ids, err := validIDs(*p.TagIDs, "tag_ids")
		if err != nil {
			return o, err
		}
		if len(ids) > maxTagsPerDevice {
			return o, errf(http.StatusBadRequest, "bad_request", "at most "+strconv.Itoa(maxTagsPerDevice)+" tags per device")
		}
		if err := a.requireOwned(r, "tags", ids, "tag"); err != nil {
			return o, err
		}
		o.TagIDs = &ids
	}
	return o, nil
}

func (a *App) bulkDevices(w http.ResponseWriter, r *http.Request) error {
	u := userFrom(r)
	var body struct {
		DeviceIDs    []string        `json:"device_ids"`
		FolderID     json.RawMessage `json:"folder_id"`
		Favorite     *bool           `json:"favorite"`
		AddTagIDs    []string        `json:"add_tag_ids"`
		RemoveTagIDs []string        `json:"remove_tag_ids"`
	}
	if err := decode(r, &body); err != nil {
		return err
	}
	ids, err := validIDs(body.DeviceIDs, "device_ids")
	if err != nil {
		return err
	}
	if len(ids) == 0 || len(ids) > maxBulkDevices {
		return errf(http.StatusBadRequest, "bad_request", "device_ids must list 1 to "+strconv.Itoa(maxBulkDevices)+" devices")
	}
	b := store.BulkOrg{Favorite: body.Favorite}
	if b.SetFolder, b.FolderID, err = optionalID(body.FolderID, "folder_id"); err != nil {
		return err
	}
	if b.AddTagIDs, err = validIDs(body.AddTagIDs, "add_tag_ids"); err != nil {
		return err
	}
	if b.RemoveTagIDs, err = validIDs(body.RemoveTagIDs, "remove_tag_ids"); err != nil {
		return err
	}
	if err := a.requireOwned(r, "devices", ids, "device"); err != nil {
		return err
	}
	if b.FolderID != nil {
		if err := a.requireOwned(r, "device_folders", []string{*b.FolderID}, "folder"); err != nil {
			return err
		}
	}
	if err := a.requireOwned(r, "tags", append(append([]string{}, b.AddTagIDs...), b.RemoveTagIDs...), "tag"); err != nil {
		return err
	}
	ctx := r.Context()
	tx, err := a.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := store.BulkDeviceOrg(ctx, tx, ids, b); err != nil {
		return err
	}
	if _, over, err := store.TagCountOver(ctx, tx, ids, maxTagsPerDevice); err != nil {
		return err
	} else if over {
		return errf(http.StatusBadRequest, "bad_request", "at most "+strconv.Itoa(maxTagsPerDevice)+" tags per device")
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	ds, err := store.DevicesByIDs(ctx, a.pool, ids)
	if err != nil {
		return err
	}
	rels, err := store.ListReleases(ctx, a.pool)
	if err != nil {
		return err
	}
	for i := range ds {
		a.hub.decorate(&ds[i])
		applyLatest(&ds[i], rels)
		ds[i].OwnerEmail = ""
	}
	a.hub.PushOrgChanged(u.ID, "devices", ids)
	writeJSON(w, http.StatusOK, map[string]any{"items": ds})
	return nil
}
