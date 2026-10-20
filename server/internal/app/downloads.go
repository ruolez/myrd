package app

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/ruolez/remotectl/server/internal/store"
)

const maxDownloadBytes = 512 << 20

// downloadExts are the installer formats accepted per OS.
var downloadExts = map[string][]string{
	"macos":   {".pkg", ".dmg", ".zip"},
	"windows": {".msi", ".exe", ".zip"},
}

var downloadNameChars = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._ -]{0,127}$`)

// downloadName validates the file name an admin uploads for an OS and returns
// its base name, which becomes the Content-Disposition filename.
func downloadName(osName, raw string) (string, error) {
	exts, ok := downloadExts[osName]
	if !ok {
		return "", errf(http.StatusNotFound, "not_found", "unknown platform")
	}
	name := path.Base(strings.ReplaceAll(raw, `\`, "/"))
	if !downloadNameChars.MatchString(name) {
		return "", errf(http.StatusBadRequest, "bad_request", "file name may use letters, digits, spaces, '.', '_' and '-'")
	}
	ext := strings.ToLower(filepath.Ext(name))
	for _, e := range exts {
		if ext == e {
			return name, nil
		}
	}
	return "", errf(http.StatusBadRequest, "bad_request", "expected "+strings.Join(exts, ", ")+" for "+osName)
}

func (a *App) downloadPath(osName string) string { return filepath.Join(a.cfg.DownloadsDir, osName) }

// adminUploadDownload stores the request body as the installer for {os}.
// PUT /v1/admin/downloads/{os}?filename=RemoteAgent.pkg
func (a *App) adminUploadDownload(w http.ResponseWriter, r *http.Request) error {
	osName := chi.URLParam(r, "os")
	name, err := downloadName(osName, r.URL.Query().Get("filename"))
	if err != nil {
		return err
	}
	if err := os.MkdirAll(a.cfg.DownloadsDir, 0o750); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(a.cfg.DownloadsDir, "."+osName+".upload-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op after the rename
	h := sha256.New()
	size, err := io.Copy(io.MultiWriter(tmp, h), http.MaxBytesReader(w, r.Body, maxDownloadBytes))
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	var tooBig *http.MaxBytesError
	switch {
	case errors.As(err, &tooBig):
		return errf(http.StatusRequestEntityTooLarge, "too_large", "installers are limited to 512 MB")
	case err != nil:
		return err
	case size == 0:
		return errf(http.StatusBadRequest, "bad_request", "empty file")
	}
	if err := os.Rename(tmp.Name(), a.downloadPath(osName)); err != nil {
		return err
	}
	u := userFrom(r)
	d, err := store.UpsertAgentDownload(r.Context(), a.pool, store.AgentDownload{
		OS: osName, Filename: name, Size: size, SHA256: hex.EncodeToString(h.Sum(nil))}, u.ID)
	if err != nil {
		return err
	}
	a.audit(r.Context(), store.Audit{Actor: "admin", Event: "download.uploaded", UserID: u.ID, IP: a.clientIP(r),
		Meta: map[string]any{"os": osName, "filename": name, "size": size, "sha256": d.SHA256}})
	writeJSON(w, http.StatusOK, d)
	return nil
}

func (a *App) adminDeleteDownload(w http.ResponseWriter, r *http.Request) error {
	osName := chi.URLParam(r, "os")
	if _, ok := downloadExts[osName]; !ok {
		return errNotFound
	}
	existed, err := store.DeleteAgentDownload(r.Context(), a.pool, osName)
	if err != nil {
		return err
	}
	if !existed {
		return errNotFound
	}
	if err := os.Remove(a.downloadPath(osName)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	u := userFrom(r)
	a.audit(r.Context(), store.Audit{Actor: "admin", Event: "download.deleted", UserID: u.ID, IP: a.clientIP(r),
		Meta: map[string]any{"os": osName}})
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// serveDownload is public: the installer is opened on the machine being
// enrolled, often before anyone signs in there. Enrolling still needs a token.
func (a *App) serveDownload(w http.ResponseWriter, r *http.Request) error {
	osName := chi.URLParam(r, "os")
	if _, ok := downloadExts[osName]; !ok {
		return errNotFound
	}
	d, err := store.AgentDownloadByOS(r.Context(), a.pool, osName)
	if errors.Is(err, store.ErrNotFound) {
		return errNotFound
	}
	if err != nil {
		return err
	}
	f, err := os.Open(a.downloadPath(osName))
	if errors.Is(err, os.ErrNotExist) {
		return errNotFound
	}
	if err != nil {
		return err
	}
	defer f.Close()
	hdr := w.Header()
	hdr.Set("Content-Type", "application/octet-stream")
	hdr.Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": d.Filename}))
	hdr.Set("Cache-Control", "no-cache")
	hdr.Set("ETag", `"`+d.SHA256+`"`)
	hdr.Set("X-Content-SHA256", d.SHA256)
	http.ServeContent(w, r, "", d.UploadedAt, f)
	return nil
}

// downloadLinks is the "downloads" block of /v1/config: an uploaded installer
// wins over the external URL from Admin → System.
func (a *App) downloadLinks(r *http.Request, s store.Settings) (map[string]any, error) {
	files, err := store.ListAgentDownloads(r.Context(), a.pool)
	if err != nil {
		return nil, err
	}
	out := map[string]any{}
	for osName, external := range map[string]string{"macos": s.DownloadMacOSURL, "windows": s.DownloadWindowsURL} {
		switch d, ok := files[osName]; {
		case ok:
			out[osName] = map[string]any{"url": "/v1/downloads/" + osName, "file": d}
		case external != "":
			out[osName] = map[string]any{"url": external, "file": nil}
		default:
			out[osName] = nil
		}
	}
	return out, nil
}
