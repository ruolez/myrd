package app

import (
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/ruolez/remotectl/proto"
	"github.com/ruolez/remotectl/server/internal/security"
	"github.com/ruolez/remotectl/server/internal/store"
)

type installLinkView struct {
	Code       string     `json:"code"`
	URL        string     `json:"url"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
	UseCount   int        `json:"use_count"`
}

func (a *App) installLinkView(l *store.InstallLink) (*installLinkView, error) {
	code, err := security.Open(a.cfg.TOTPEncKey, l.CodeSealed)
	if err != nil {
		return nil, err
	}
	return &installLinkView{Code: string(code), URL: a.cfg.PublicOrigin + "/i/" + string(code),
		CreatedAt: l.CreatedAt.UTC(), LastUsedAt: l.LastUsedAt, UseCount: l.UseCount}, nil
}

func (a *App) getInstallLink(w http.ResponseWriter, r *http.Request) error {
	l, err := store.InstallLinkByUser(r.Context(), a.pool, userFrom(r).ID)
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusOK, map[string]any{"link": nil})
		return nil
	}
	if err != nil {
		return err
	}
	v, err := a.installLinkView(l)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"link": v})
	return nil
}

// createInstallLink gives the caller a new link; the previous one stops working.
func (a *App) createInstallLink(w http.ResponseWriter, r *http.Request) error {
	u := userFrom(r)
	code := security.NewInstallCode()
	norm, _ := security.NormalizeInstallCode(code)
	sealed, err := security.Seal(a.cfg.TOTPEncKey, []byte(code))
	if err != nil {
		return err
	}
	l, err := store.PutInstallLink(r.Context(), a.pool, u.ID, security.SHA256(norm), sealed)
	if err != nil {
		return err
	}
	v, err := a.installLinkView(l)
	if err != nil {
		return err
	}
	a.audit(r.Context(), store.Audit{Actor: "user", Event: "install_link.created", UserID: u.ID, IP: a.clientIP(r)})
	writeJSON(w, http.StatusOK, map[string]any{"link": v})
	return nil
}

func (a *App) deleteInstallLink(w http.ResponseWriter, r *http.Request) error {
	u := userFrom(r)
	existed, err := store.DeleteInstallLink(r.Context(), a.pool, u.ID)
	if err != nil {
		return err
	}
	if !existed {
		return errNotFound
	}
	a.audit(r.Context(), store.Audit{Actor: "user", Event: "install_link.revoked", UserID: u.ID, IP: a.clientIP(r)})
	writeJSON(w, http.StatusOK, map[string]any{})
	return nil
}

// liveInstallCode returns the {code} of a public install request as
// "XXXX-XXXX-XXXX", or 404 unless it is a live link.
func (a *App) liveInstallCode(r *http.Request) (string, error) {
	if err := a.agentRateLimit(r); err != nil {
		return "", err
	}
	norm, ok := security.NormalizeInstallCode(chi.URLParam(r, "code"))
	if !ok {
		return "", errNotFound
	}
	live, err := store.InstallLinkLive(r.Context(), a.pool, security.SHA256(norm))
	if err != nil {
		return "", err
	}
	if !live {
		return "", errNotFound
	}
	return norm[:4] + "-" + norm[4:8] + "-" + norm[8:], nil
}

// installInfo is public: the landing page of an install link lists the
// installers it hands out, each named for the link.
func (a *App) installInfo(w http.ResponseWriter, r *http.Request) error {
	code, err := a.liveInstallCode(r)
	if err != nil {
		return err
	}
	files, err := store.ListAgentDownloads(r.Context(), a.pool)
	if err != nil {
		return err
	}
	out := map[string]any{}
	for osName := range downloadExts {
		out[osName] = nil
		if d, ok := files[osName]; ok {
			out[osName] = map[string]any{"filename": proto.InstallLinkFilename(d.Filename, code), "size": d.Size}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"downloads": out})
	return nil
}

// installDownload is public: the uploaded installer, named so that it
// enrolls with the link's code (proto.InstallCodeFromPath).
func (a *App) installDownload(w http.ResponseWriter, r *http.Request) error {
	code, err := a.liveInstallCode(r)
	if err != nil {
		return err
	}
	return a.sendDownload(w, r, chi.URLParam(r, "os"), func(name string) string {
		return proto.InstallLinkFilename(name, code)
	})
}
