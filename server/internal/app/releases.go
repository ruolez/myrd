package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/ruolez/remotectl/proto"
	"github.com/ruolez/remotectl/server/internal/security"
	"github.com/ruolez/remotectl/server/internal/store"
)

const maxReleaseBytes = maxDownloadBytes

// pickRelease is the server-side selection of docs/PROTOCOL.md §7: the
// highest published release for os newer than current whose arch is arch or
// universal (exact arch wins at the same version) and whose cohort includes
// bucket, unless explicit.
func pickRelease(rels []store.Release, os, arch, current string, bucket int, explicit bool) *store.Release {
	var best *store.Release
	for i := range rels {
		r := &rels[i]
		switch {
		case r.PublishedAt == nil || r.OS != os || (r.Arch != arch && r.Arch != "universal"):
			continue
		case proto.CompareVersions(r.Version, current) <= 0:
			continue
		case !explicit && bucket >= r.RolloutPct:
			continue
		}
		if best == nil {
			best = r
			continue
		}
		if c := proto.CompareVersions(r.Version, best.Version); c > 0 || (c == 0 && r.Arch == arch && best.Arch != arch) {
			best = r
		}
	}
	return best
}

// applyLatest sets latest_version (cohort ignored) and update_available.
func applyLatest(d *store.Device, rels []store.Release) {
	d.LatestVersion, d.UpdateAvailable = nil, false
	if r := pickRelease(rels, d.OS, d.Arch, "", 0, true); r != nil {
		v := r.Version
		d.LatestVersion = &v
		d.UpdateAvailable = proto.CompareVersions(v, d.AgentVersion) > 0
	}
}

func (a *App) withLatest(ctx context.Context, d *store.Device) error {
	rels, err := store.ListReleases(ctx, a.pool)
	if err != nil {
		return err
	}
	applyLatest(d, rels)
	return nil
}

// downloadOS is the Add-device download slot of a release os.
func downloadOS(goos string) string {
	switch goos {
	case "darwin":
		return "macos"
	case "windows":
		return "windows"
	}
	return ""
}

func (a *App) releasesDir() string { return filepath.Join(a.cfg.DownloadsDir, "releases") }

func (a *App) releasePath(r *store.Release) string {
	return filepath.Join(a.releasesDir(), r.ID+proto.ExtForOS(r.OS))
}

func (a *App) adminListReleases(w http.ResponseWriter, r *http.Request) error {
	rels, err := store.ListReleases(r.Context(), a.pool)
	if err != nil {
		return err
	}
	slices.SortStableFunc(rels, func(x, y store.Release) int { return proto.CompareVersions(y.Version, x.Version) })
	writeJSON(w, http.StatusOK, map[string]any{"items": rels})
	return nil
}

// adminUploadRelease streams a multipart upload. The sidecar (part "sig")
// must come first so a bad signature is refused before the installer is read.
func (a *App) adminUploadRelease(w http.ResponseWriter, r *http.Request) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxReleaseBytes+1<<20)
	mr, err := r.MultipartReader()
	if err != nil {
		return errf(http.StatusBadRequest, "bad_request", "expected multipart/form-data")
	}
	ctx := r.Context()
	rollout := 100
	var sc *proto.ReleaseSidecar
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			return errf(http.StatusBadRequest, "bad_request", `missing part "file"`)
		}
		if err != nil {
			return uploadErr(err)
		}
		switch part.FormName() {
		case "rollout_pct":
			b, err := io.ReadAll(io.LimitReader(part, 8))
			n, perr := strconv.Atoi(strings.TrimSpace(string(b)))
			if err != nil || perr != nil || n < 0 || n > 100 {
				return errf(http.StatusBadRequest, "bad_request", "rollout_pct must be 0..100")
			}
			rollout = n
		case "sig":
			b, err := io.ReadAll(io.LimitReader(part, proto.MaxSidecarBytes+1))
			if err != nil {
				return uploadErr(err)
			}
			s, err := proto.ParseSidecar(b)
			if err == nil {
				err = proto.VerifySidecar(a.cfg.ReleasePublicKey, s)
			}
			if err != nil {
				return errf(http.StatusBadRequest, "bad_signature", err.Error())
			}
			if s.Size > maxReleaseBytes {
				return errf(http.StatusRequestEntityTooLarge, "too_large", "installers are limited to 512 MB")
			}
			if exists, err := store.ReleaseExists(ctx, a.pool, s.Version, s.OS, s.Arch); err != nil {
				return err
			} else if exists {
				return errConflictRelease
			}
			sc = &s
		case "file":
			if sc == nil {
				return errf(http.StatusBadRequest, "bad_request", `part "sig" must come before part "file"`)
			}
			return a.createRelease(w, r, part, *sc, rollout)
		}
	}
}

var errConflictRelease = errf(http.StatusConflict, "conflict", "a release with this version/os/arch exists")

func uploadErr(err error) error {
	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) {
		return errf(http.StatusRequestEntityTooLarge, "too_large", "installers are limited to 512 MB")
	}
	return errf(http.StatusBadRequest, "bad_request", "upload interrupted")
}

// createRelease stores the installer part as DOWNLOADS_DIR/releases/<id><ext>
// once its size and SHA-256 match the verified sidecar, then inserts the row.
func (a *App) createRelease(w http.ResponseWriter, r *http.Request, part *multipart.Part, sc proto.ReleaseSidecar, rollout int) error {
	ext := proto.ExtForOS(sc.OS)
	if got := strings.ToLower(filepath.Ext(part.FileName())); got != ext {
		return errf(http.StatusBadRequest, "mismatch", fmt.Sprintf("expected a %s installer for %s", ext, sc.OS))
	}
	dir := a.releasesDir()
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".upload-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op after the rename
	h := sha256.New()
	n, rerr := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(part, sc.Size+1))
	if err := tmp.Sync(); rerr == nil {
		rerr = err
	}
	if err := tmp.Close(); rerr == nil {
		rerr = err
	}
	switch sum := hex.EncodeToString(h.Sum(nil)); {
	case rerr != nil:
		return uploadErr(rerr)
	case n != sc.Size:
		return errf(http.StatusBadRequest, "mismatch", fmt.Sprintf("the file has %d bytes, the signature file says %d", n, sc.Size))
	case sum != sc.SHA256:
		return errf(http.StatusBadRequest, "mismatch", "the file's SHA-256 does not match the signature file")
	}
	rel := store.Release{ID: uuid.NewString(), Version: sc.Version, OS: sc.OS, Arch: sc.Arch, Filename: sc.Filename,
		Size: sc.Size, SHA256: sc.SHA256, Signature: sc.Signature, KeyID: sc.KeyID, RolloutPct: rollout}
	path := a.releasePath(&rel)
	if err := os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	u := userFrom(r)
	out, err := store.CreateRelease(r.Context(), a.pool, rel, u.ID)
	if err != nil {
		os.Remove(path)
		if errors.Is(err, store.ErrConflict) {
			return errConflictRelease
		}
		return err
	}
	a.adminAudit(r, store.Audit{Event: "release.uploaded", Meta: map[string]any{"release_id": out.ID, "version": out.Version,
		"os": out.OS, "arch": out.Arch, "filename": out.Filename, "size": out.Size, "sha256": out.SHA256, "rollout_pct": out.RolloutPct}})
	writeJSON(w, http.StatusOK, map[string]any{"release": out})
	return nil
}

func (a *App) adminPatchRelease(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	var body struct {
		RolloutPct *int  `json:"rollout_pct"`
		Published  *bool `json:"published"`
	}
	if err := decode(r, &body); err != nil {
		return err
	}
	if body.RolloutPct != nil && (*body.RolloutPct < 0 || *body.RolloutPct > 100) {
		return errf(http.StatusBadRequest, "bad_request", "rollout_pct must be 0..100")
	}
	rel, err := store.UpdateRelease(r.Context(), a.pool, id, body.RolloutPct, body.Published)
	if err != nil {
		return err
	}
	event := "release.updated"
	if body.Published != nil && *body.Published {
		event = "release.published"
	}
	a.adminAudit(r, store.Audit{Event: event, Meta: map[string]any{"release_id": rel.ID, "version": rel.Version,
		"rollout_pct": rel.RolloutPct, "published": rel.PublishedAt != nil}})
	writeJSON(w, http.StatusOK, map[string]any{"release": rel})
	return nil
}

func (a *App) adminDeleteRelease(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	ctx := r.Context()
	rel, err := store.ReleaseByID(ctx, a.pool, id)
	if err != nil {
		return err
	}
	if err := store.DeleteRelease(ctx, a.pool, id); err != nil {
		return err
	}
	if err := os.Remove(a.releasePath(rel)); err != nil && !errors.Is(err, os.ErrNotExist) {
		a.log.Error("remove release file", "release", id, "err", err)
	}
	a.adminAudit(r, store.Audit{Event: "release.deleted", Meta: map[string]any{"release_id": id, "version": rel.Version,
		"os": rel.OS, "arch": rel.Arch}})
	writeJSON(w, http.StatusOK, map[string]any{})
	return nil
}

// adminSetReleaseInstaller makes a release's file the Add-device download
// for its os.
func (a *App) adminSetReleaseInstaller(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	ctx := r.Context()
	rel, err := store.ReleaseByID(ctx, a.pool, id)
	if err != nil {
		return err
	}
	osName := downloadOS(rel.OS)
	if err := linkOrCopy(a.releasePath(rel), a.downloadPath(osName)); errors.Is(err, os.ErrNotExist) {
		return errf(http.StatusNotFound, "not_found", "the release file is missing")
	} else if err != nil {
		return err
	}
	u := userFrom(r)
	if _, err := store.UpsertAgentDownload(ctx, a.pool, store.AgentDownload{OS: osName, Filename: rel.Filename, Size: rel.Size,
		SHA256: rel.SHA256, ReleaseID: &rel.ID}, u.ID); err != nil {
		return err
	}
	a.adminAudit(r, store.Audit{Event: "release.installer_set", Meta: map[string]any{"release_id": rel.ID, "version": rel.Version,
		"os": osName, "filename": rel.Filename}})
	if rel, err = store.ReleaseByID(ctx, a.pool, id); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"release": rel})
	return nil
}

// linkOrCopy atomically replaces dst with src: a hard link when both are on
// one filesystem, otherwise a copy.
func linkOrCopy(src, dst string) error {
	tmp := filepath.Join(filepath.Dir(dst), "."+filepath.Base(dst)+".link-"+security.NewToken(8))
	if err := os.Link(src, tmp); err != nil {
		if _, serr := os.Stat(src); serr != nil {
			return serr
		}
		if err := copyFile(src, tmp); err != nil {
			os.Remove(tmp)
			return err
		}
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if err == nil {
		err = out.Sync()
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	return err
}

// serveReleaseFile is public for published releases (agents download
// without a session); admins may also fetch unpublished ones.
func (a *App) serveReleaseFile(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	rel, err := store.ReleaseByID(r.Context(), a.pool, id)
	if err != nil {
		return err
	}
	if u := userFrom(r); rel.PublishedAt == nil && (u == nil || !u.IsAdmin) {
		return errNotFound
	}
	f, err := os.Open(a.releasePath(rel))
	if errors.Is(err, os.ErrNotExist) {
		return errNotFound
	}
	if err != nil {
		return err
	}
	defer f.Close()
	hdr := w.Header()
	hdr.Set("Content-Type", "application/octet-stream")
	hdr.Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": rel.Filename}))
	hdr.Set("Cache-Control", "no-cache")
	hdr.Set("ETag", `"`+rel.SHA256+`"`)
	hdr.Set("X-Content-SHA256", rel.SHA256)
	http.ServeContent(w, r, "", rel.CreatedAt, f)
	return nil
}
