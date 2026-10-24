// Package app is the HTTP API and signaling hub of remotectl-api.
package app

import (
	"context"
	"log/slog"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/ruolez/remotectl/proto"
	"github.com/ruolez/remotectl/server/internal/config"
	"github.com/ruolez/remotectl/server/internal/db"
	"github.com/ruolez/remotectl/server/internal/ratelimit"
	"github.com/ruolez/remotectl/server/internal/security"
	"github.com/ruolez/remotectl/server/internal/store"
)

// serverFeatures lists the optional APIs this server offers (GET /v1/config).
var serverFeatures = []string{"organization", "device_info", "previews"}

const (
	Version     = "0.1.0"
	agentJWTTTL = 10 * time.Minute
	enrollTTL   = 15 * time.Minute
)

type App struct {
	cfg     *config.Config
	pool    *pgxpool.Pool
	log     *slog.Logger
	hub     *Hub
	metrics *metrics
	wa      *webauthn.WebAuthn
	argon   security.ArgonParams
	started time.Time

	ctx    context.Context // cancelled at shutdown; parent of every socket
	cancel context.CancelFunc

	settingsMu sync.RWMutex
	settings   store.Settings

	loginFails  *ratelimit.Failures
	agentLimits *ratelimit.Buckets
	signupLimit *ratelimit.Buckets

	totpTickets *ttlMap[string]                // ticket → user id (password ok, TOTP pending)
	ceremonies  *ttlMap[*webauthn.SessionData] // ticket → WebAuthn ceremony
	nonces      *ttlMap[string]                // nonce → device id
}

type Options struct {
	Argon *security.ArgonParams // tests use cheap parameters
}

func New(ctx context.Context, cfg *config.Config, pool *pgxpool.Pool, log *slog.Logger, opts Options) (*App, error) {
	wa, err := webauthn.New(&webauthn.Config{
		RPID: cfg.RPID, RPDisplayName: "my RemoteDesktop", RPOrigins: []string{cfg.PublicOrigin},
	})
	if err != nil {
		return nil, err
	}
	actx, cancel := context.WithCancel(ctx)
	a := &App{cfg: cfg, pool: pool, log: log, wa: wa, argon: security.DefaultArgon, started: time.Now(),
		ctx: actx, cancel: cancel,
		loginFails:  ratelimit.NewFailures(10, 15*time.Minute),
		agentLimits: ratelimit.NewBuckets(1, 30),
		signupLimit: ratelimit.NewBuckets(0.05, 5),
		totpTickets: newTTLMap[string](5 * time.Minute),
		ceremonies:  newTTLMap[*webauthn.SessionData](5 * time.Minute),
		nonces:      newTTLMap[string](60 * time.Second),
	}
	if opts.Argon != nil {
		a.argon = *opts.Argon
	}
	a.hub = newHub(a)
	a.metrics = newMetrics(a.hub, pool)
	if err := a.reloadSettings(ctx); err != nil {
		cancel()
		return nil, err
	}
	if err := a.bootstrapAdmin(ctx); err != nil {
		cancel()
		return nil, err
	}
	if err := a.hub.BootSweep(ctx); err != nil {
		cancel()
		return nil, err
	}
	return a, nil
}

func (a *App) Hub() *Hub { return a.hub }

// Close cancels every socket context and pending update pushes.
func (a *App) Close() {
	a.cancel()
	a.hub.CancelUpdateChecks("")
}

func (a *App) bootstrapAdmin(ctx context.Context) error {
	n, err := store.CountUsers(ctx, a.pool)
	if err != nil || n > 0 {
		return err
	}
	if a.cfg.AdminEmail == "" || a.cfg.AdminInitialPassword == "" {
		a.log.Warn("no users and no ADMIN_EMAIL/ADMIN_INITIAL_PASSWORD; nobody can sign in")
		return nil
	}
	hash, err := security.HashPassword(a.cfg.AdminInitialPassword, a.argon)
	if err != nil {
		return err
	}
	u, err := store.CreateUser(ctx, a.pool, strings.ToLower(a.cfg.AdminEmail), hash, true)
	if err != nil {
		return err
	}
	a.audit(ctx, store.Audit{Actor: "system", Event: "user.created", UserID: u.ID, Meta: map[string]any{"bootstrap": true, "is_admin": true}})
	a.log.Info("bootstrap admin created", "email", u.Email)
	return nil
}

func (a *App) reloadSettings(ctx context.Context) error {
	s, err := store.LoadSettings(ctx, a.pool)
	if err != nil {
		return err
	}
	a.settingsMu.Lock()
	a.settings = s
	a.settingsMu.Unlock()
	return nil
}

func (a *App) Settings() store.Settings {
	a.settingsMu.RLock()
	defer a.settingsMu.RUnlock()
	return a.settings
}

// iceServers mints coturn REST credentials for one side of a session. STUN
// URLs become a separate credential-less entry.
func (a *App) iceServers(sessionID, side string, urls []string) []proto.ICEServer {
	var stun, turn []string
	for _, u := range urls {
		if strings.HasPrefix(u, "stun:") || strings.HasPrefix(u, "stuns:") {
			stun = append(stun, u)
		} else {
			turn = append(turn, u)
		}
	}
	out := []proto.ICEServer{}
	if len(stun) > 0 {
		out = append(out, proto.ICEServer{URLs: stun})
	}
	if len(turn) > 0 {
		user, cred := security.TURNCredential(a.cfg.TURNSecret, sessionID, side, time.Now().Add(a.cfg.TURNTTL))
		out = append(out, proto.ICEServer{URLs: turn, Username: user, Credential: cred})
	}
	return out
}

// Run starts background loops (sweeper, last_seen flush, partitions) until ctx ends.
func (a *App) Run(ctx context.Context) {
	sweep := time.NewTicker(5 * time.Second)
	flush := time.NewTicker(time.Minute)
	prune := time.NewTicker(time.Hour)
	defer prune.Stop()
	parts := time.NewTicker(12 * time.Hour)
	defer sweep.Stop()
	defer flush.Stop()
	defer parts.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-sweep.C:
			a.hub.sweep()
			a.hub.previewTick()
		case <-prune.C:
			c, cancel := dbctx()
			if err := store.PrunePreviews(c, a.pool, time.Now().Add(-previewKeep)); err != nil {
				a.log.Error("prune previews", "err", err)
			}
			cancel()
		case <-flush.C:
			a.hub.flushSeen()
		case <-parts.C:
			c, cancel := dbctx()
			if err := db.EnsurePartitions(c, a.pool, time.Now()); err != nil {
				a.log.Error("audit partitions", "err", err)
			}
			cancel()
		}
	}
}

func chiParam(r *http.Request, name string) string { return chi.URLParam(r, name) }

// MetricsHandler serves Prometheus text format (internal listener only).
func (a *App) MetricsHandler() http.Handler {
	return promhttp.HandlerFor(a.metrics.reg, promhttp.HandlerOpts{})
}

func (a *App) Router() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.Recoverer)
	r.Use(a.requestLog)
	r.Use(a.originGuard)
	r.Use(a.loadUser)

	health := a.h(a.healthz)
	r.Get("/healthz", health)

	r.Route("/v1", func(r chi.Router) {
		r.Get("/healthz", health)
		r.Get("/config", a.h(a.getConfig))
		r.Get("/downloads/{os}", a.h(a.serveDownload))
		r.Head("/downloads/{os}", a.h(a.serveDownload))
		r.Get("/releases/{id}/file", a.h(a.serveReleaseFile))
		r.Head("/releases/{id}/file", a.h(a.serveReleaseFile))

		r.Post("/auth/signup", a.h(a.signup))
		r.Post("/auth/login", a.h(a.login))
		r.Post("/auth/login/totp", a.h(a.loginTOTP))
		r.Post("/auth/passkey/begin", a.h(a.passkeyLoginBegin))
		r.Post("/auth/passkey/finish", a.h(a.passkeyLoginFinish))
		r.Post("/auth/logout", a.h(a.logout))
		r.Get("/auth/me", a.h(a.me))

		r.Get("/ws", a.hub.ServeBrowser)

		r.Post("/agent/enroll", a.h(a.agentEnroll))
		r.Post("/agent/challenge", a.h(a.agentChallenge))
		r.Post("/agent/auth", a.h(a.agentAuth))
		r.Get("/agent/ws", a.hub.ServeAgent)
		r.Get("/agent/update", a.h(a.agentUpdate))
		r.Delete("/agent/self", a.h(a.agentSelfDelete))

		r.Group(func(r chi.Router) {
			r.Use(requireUser)
			r.Post("/account/password", a.h(a.changePassword))
			r.Get("/account/passkeys", a.h(a.listPasskeys))
			r.Post("/account/passkeys/begin", a.h(a.passkeyRegisterBegin))
			r.Post("/account/passkeys/finish", a.h(a.passkeyRegisterFinish))
			r.Delete("/account/passkeys/{id}", a.h(a.deletePasskey))
			r.Post("/account/totp/setup", a.h(a.totpSetup))
			r.Post("/account/totp/enable", a.h(a.totpEnable))
			r.Post("/account/totp/disable", a.h(a.totpDisable))
			r.Get("/account/sessions", a.h(a.listLoginSessions))
			r.Delete("/account/sessions/{id}", a.h(a.deleteLoginSession))

			r.Get("/client-keys", a.h(a.listClientKeys))
			r.Post("/client-keys", a.h(a.createClientKey))
			r.Delete("/client-keys/{id}", a.h(a.revokeClientKey))
			r.Put("/client-keys/{id}/preview-key", a.h(a.putPreviewKey))
			r.Get("/previews", a.h(a.getPreviews))

			r.Get("/devices", a.h(a.listDevices))
			r.Get("/devices/{id}", a.h(a.getDevice))
			r.Patch("/devices/{id}", a.h(a.patchDevice))
			r.Delete("/devices/{id}", a.h(a.deleteDevice))
			r.Get("/devices/{id}/credential", a.h(a.getCredential))
			r.Put("/devices/{id}/credential", a.h(a.putCredential))
			r.Delete("/devices/{id}/credential", a.h(a.deleteCredential))
			r.Put("/devices/{id}/unlock-key", a.h(a.putUnlockKey))
			r.Post("/devices/{id}/update", a.h(a.updateDevice))
			r.Post("/devices/{id}/virtual-display", a.h(a.virtualDisplayDevice))
			r.Post("/devices/bulk", a.h(a.bulkDevices))
			r.Get("/folders", a.h(a.listFolders))
			r.Post("/folders", a.h(a.createFolder))
			r.Patch("/folders/{id}", a.h(a.patchFolder))
			r.Delete("/folders/{id}", a.h(a.deleteFolder))
			r.Get("/tags", a.h(a.listTags))
			r.Post("/tags", a.h(a.createTag))
			r.Patch("/tags/{id}", a.h(a.patchTag))
			r.Delete("/tags/{id}", a.h(a.deleteTag))
			r.Post("/enroll-tokens", a.h(a.createEnrollToken))
			r.Get("/sessions", a.h(a.listSessions))
			r.Get("/audit", a.h(a.listAudit))
		})

		r.Route("/admin", func(r chi.Router) {
			r.Use(requireAdmin)
			r.Get("/overview", a.h(a.adminOverview))
			r.Get("/users", a.h(a.adminListUsers))
			r.Post("/users", a.h(a.adminCreateUser))
			r.Patch("/users/{id}", a.h(a.adminPatchUser))
			r.Post("/users/{id}/password", a.h(a.adminSetPassword))
			r.Post("/users/{id}/reset-mfa", a.h(a.adminResetMFA))
			r.Post("/users/{id}/logout", a.h(a.adminLogoutUser))
			r.Delete("/users/{id}", a.h(a.adminDeleteUser))
			r.Get("/devices", a.h(a.adminListDevices))
			r.Patch("/devices/{id}", a.h(a.adminPatchDevice))
			r.Delete("/devices/{id}", a.h(a.adminDeleteDevice))
			r.Post("/devices/{id}/update", a.h(a.adminUpdateDevice))
			r.Post("/devices/{id}/virtual-display", a.h(a.adminVirtualDisplayDevice))
			r.Post("/updates/push", a.h(a.adminPushUpdates))
			r.Get("/sessions", a.h(a.adminListSessions))
			r.Post("/sessions/{id}/end", a.h(a.adminEndSession))
			r.Get("/audit", a.h(a.adminListAudit))
			r.Get("/releases", a.h(a.adminListReleases))
			r.Post("/releases", a.h(a.adminUploadRelease))
			r.Patch("/releases/{id}", a.h(a.adminPatchRelease))
			r.Post("/releases/{id}/installer", a.h(a.adminSetReleaseInstaller))
			r.Delete("/releases/{id}", a.h(a.adminDeleteRelease))
			r.Get("/settings", a.h(a.adminGetSettings))
			r.Put("/settings", a.h(a.adminPutSettings))
			r.Put("/downloads/{os}", a.h(a.adminUploadDownload))
			r.Delete("/downloads/{os}", a.h(a.adminDeleteDownload))
		})
	})
	r.NotFound(a.h(func(w http.ResponseWriter, r *http.Request) error { return errNotFound }))
	return r
}

func (a *App) requestLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r)
		if strings.HasSuffix(r.URL.Path, "/ws") {
			return // long-lived; logged by the hub
		}
		a.log.Debug("http", append(logAttrs(r), "status", ww.Status(), "ms", time.Since(start).Milliseconds())...)
	})
}

func (a *App) healthz(w http.ResponseWriter, r *http.Request) error {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	ok := a.pool.Ping(ctx) == nil
	status := http.StatusOK
	if !ok {
		status = http.StatusServiceUnavailable
	}
	writeJSON(w, status, map[string]any{"ok": ok, "db": ok})
	return nil
}

func (a *App) getConfig(w http.ResponseWriter, r *http.Request) error {
	s := a.Settings()
	downloads, err := a.downloadLinks(r, s)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"product_name":    "my RemoteDesktop",
		"version":         Version,
		"signup_allowed":  s.SignupAllowed,
		"webauthn":        map[string]string{"rp_id": a.cfg.RPID, "origin": a.cfg.PublicOrigin},
		"downloads":       downloads,
		"features":        serverFeatures,
		"screen_previews": s.ScreenPreviews,
	})
	return nil
}

func goVersion() string { return runtime.Version() }
