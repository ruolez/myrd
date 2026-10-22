package app

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ruolez/remotectl/proto"
	"github.com/ruolez/remotectl/server/internal/security"
	"github.com/ruolez/remotectl/server/internal/store"
)

func validUUID(s string) bool {
	_, err := uuid.Parse(s)
	return err == nil
}

func (a *App) adminAudit(r *http.Request, ev store.Audit) {
	ev.Actor = "admin"
	ev.IP = a.clientIP(r)
	if ev.Meta == nil {
		ev.Meta = map[string]any{}
	}
	admin := userFrom(r)
	ev.Meta["admin_id"] = admin.ID
	ev.Meta["admin_email"] = admin.Email
	a.audit(r.Context(), ev)
}

func (a *App) adminOverview(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	o, err := store.LoadOverview(ctx, a.pool)
	if err != nil {
		return err
	}
	agents, browsers := a.hub.Counts()
	o.Devices.Online = agents
	recent, err := store.ListAudit(ctx, a.pool, store.AuditFilter{Limit: 15})
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"server": map[string]any{"version": Version, "started_at": a.started.UTC(), "uptime_s": int(time.Since(a.started).Seconds()),
			"go_version": goVersion(), "db_ok": a.pool.Ping(ctx) == nil},
		"users":        o.Users,
		"devices":      o.Devices,
		"sessions":     o.Sessions,
		"ws":           map[string]any{"agents": agents, "browsers": browsers, "reconnects_per_min": a.hub.ReconnectsPerMin()},
		"series":       o.Series,
		"recent_audit": recent,
	})
	return nil
}

// ---------- users ----------

func userCursor(u store.User) string { return u.CreatedAt.UTC().Format(time.RFC3339Nano) }

func (a *App) adminListUsers(w http.ResponseWriter, r *http.Request) error {
	before, err := timeCursor(r)
	if err != nil {
		return err
	}
	limit := pageLimit(r)
	us, err := store.ListUsers(r.Context(), a.pool, strings.TrimSpace(r.URL.Query().Get("q")), limit, before)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, page(us, limit, userCursor))
	return nil
}

func (a *App) adminCreateUser(w http.ResponseWriter, r *http.Request) error {
	var body struct {
		Email    string `json:"email"`
		Password string `json:"password"`
		IsAdmin  bool   `json:"is_admin"`
	}
	if err := decode(r, &body); err != nil {
		return err
	}
	email, ok := normalizeEmail(body.Email)
	if !ok {
		return errf(http.StatusBadRequest, "bad_request", "invalid email")
	}
	if err := validPassword(body.Password); err != nil {
		return err
	}
	hash, err := security.HashPassword(body.Password, a.argon)
	if err != nil {
		return err
	}
	u, err := store.CreateUser(r.Context(), a.pool, email, hash, body.IsAdmin)
	if errors.Is(err, store.ErrConflict) {
		return errf(http.StatusConflict, "conflict", "an account with this email exists")
	}
	if err != nil {
		return err
	}
	a.adminAudit(r, store.Audit{Event: "user.created", UserID: u.ID, Meta: map[string]any{"email": u.Email, "is_admin": u.IsAdmin}})
	writeJSON(w, http.StatusOK, map[string]any{"user": u})
	return nil
}

func (a *App) adminTarget(r *http.Request) (*store.User, error) {
	id, err := pathID(r, "id")
	if err != nil {
		return nil, err
	}
	return store.UserByID(r.Context(), a.pool, id)
}

func (a *App) adminPatchUser(w http.ResponseWriter, r *http.Request) error {
	t, err := a.adminTarget(r)
	if err != nil {
		return err
	}
	var body struct {
		IsAdmin  *bool `json:"is_admin"`
		Disabled *bool `json:"disabled"`
	}
	if err := decode(r, &body); err != nil {
		return err
	}
	self := t.ID == userFrom(r).ID
	if self && ((body.IsAdmin != nil && !*body.IsAdmin) || (body.Disabled != nil && *body.Disabled)) {
		return errf(http.StatusBadRequest, "bad_request", "you cannot demote or disable yourself")
	}
	ctx := r.Context()
	if body.IsAdmin != nil && *body.IsAdmin != t.IsAdmin {
		if err := store.SetUserAdmin(ctx, a.pool, t.ID, *body.IsAdmin); err != nil {
			return err
		}
		a.adminAudit(r, store.Audit{Event: "admin.user_updated", UserID: t.ID, Meta: map[string]any{"is_admin": *body.IsAdmin}})
	}
	if body.Disabled != nil && *body.Disabled != t.Disabled {
		if err := store.SetUserDisabled(ctx, a.pool, t.ID, *body.Disabled); err != nil {
			return err
		}
		if *body.Disabled {
			if err := store.DeleteUserLoginSessions(ctx, a.pool, t.ID); err != nil {
				return err
			}
			a.hub.EndUserActivity(ctx, t.ID, proto.EndAdmin)
		}
		a.adminAudit(r, store.Audit{Event: "admin.user_updated", UserID: t.ID, Meta: map[string]any{"disabled": *body.Disabled}})
	}
	u, err := store.UserByID(ctx, a.pool, t.ID)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"user": u})
	return nil
}

func (a *App) adminSetPassword(w http.ResponseWriter, r *http.Request) error {
	t, err := a.adminTarget(r)
	if err != nil {
		return err
	}
	var body struct{ Password string }
	if err := decode(r, &body); err != nil {
		return err
	}
	if err := validPassword(body.Password); err != nil {
		return err
	}
	hash, err := security.HashPassword(body.Password, a.argon)
	if err != nil {
		return err
	}
	if err := store.SetPassword(r.Context(), a.pool, t.ID, hash); err != nil {
		return err
	}
	a.adminAudit(r, store.Audit{Event: "user.password_changed", UserID: t.ID, Meta: map[string]any{"reset_by_admin": true}})
	writeJSON(w, http.StatusOK, map[string]any{})
	return nil
}

func (a *App) adminResetMFA(w http.ResponseWriter, r *http.Request) error {
	t, err := a.adminTarget(r)
	if err != nil {
		return err
	}
	if err := store.DisableTOTP(r.Context(), a.pool, t.ID); err != nil {
		return err
	}
	a.adminAudit(r, store.Audit{Event: "user.totp_disabled", UserID: t.ID, Meta: map[string]any{"reset_by_admin": true}})
	writeJSON(w, http.StatusOK, map[string]any{})
	return nil
}

func (a *App) adminLogoutUser(w http.ResponseWriter, r *http.Request) error {
	t, err := a.adminTarget(r)
	if err != nil {
		return err
	}
	ctx := r.Context()
	if err := store.DeleteUserLoginSessions(ctx, a.pool, t.ID); err != nil {
		return err
	}
	for _, b := range a.hub.browsersOf(t.ID) {
		b.close(closeUnauthorized, "signed out by admin")
	}
	a.adminAudit(r, store.Audit{Event: "admin.user_updated", UserID: t.ID, Meta: map[string]any{"logout": true}})
	writeJSON(w, http.StatusOK, map[string]any{})
	return nil
}

func (a *App) adminDeleteUser(w http.ResponseWriter, r *http.Request) error {
	t, err := a.adminTarget(r)
	if err != nil {
		return err
	}
	if t.ID == userFrom(r).ID {
		return errf(http.StatusBadRequest, "bad_request", "you cannot delete yourself")
	}
	ctx := r.Context()
	a.hub.EndUserActivity(ctx, t.ID, proto.EndAdmin)
	devices, err := store.DevicesByUser(ctx, a.pool, t.ID)
	if err != nil {
		return err
	}
	for _, d := range devices {
		a.hub.KickDevice(ctx, d.ID, proto.EndAdmin, fromAdmin)
	}
	if err := store.DeleteUser(ctx, a.pool, t.ID); err != nil {
		return err
	}
	a.adminAudit(r, store.Audit{Event: "admin.user_deleted", UserID: t.ID, Meta: map[string]any{"email": t.Email}})
	writeJSON(w, http.StatusOK, map[string]any{})
	return nil
}

// ---------- devices ----------

func deviceCursor(d store.Device) string { return d.CreatedAt.UTC().Format(time.RFC3339Nano) }

func (a *App) adminListDevices(w http.ResponseWriter, r *http.Request) error {
	before, err := timeCursor(r)
	if err != nil {
		return err
	}
	limit := pageLimit(r)
	status := r.URL.Query().Get("status")
	// Presence filters are applied after the query, so fetch a wider window.
	fetch := limit
	if status == "online" || status == "offline" {
		fetch = 1000
	}
	ctx := r.Context()
	ds, err := store.ListAllDevices(ctx, a.pool, strings.TrimSpace(r.URL.Query().Get("q")), status == "disabled", fetch, before)
	if err != nil {
		return err
	}
	rels, err := store.ListReleases(ctx, a.pool)
	if err != nil {
		return err
	}
	out := ds[:0]
	for i := range ds {
		a.hub.decorate(&ds[i])
		applyLatest(&ds[i], rels)
		if (status == "online" || status == "offline") && ds[i].Status != status {
			continue
		}
		out = append(out, ds[i])
	}
	if len(out) > limit {
		out = out[:limit]
	}
	writeJSON(w, http.StatusOK, page(out, limit, deviceCursor))
	return nil
}

func (a *App) adminDevice(r *http.Request) (*store.Device, error) {
	id, err := pathID(r, "id")
	if err != nil {
		return nil, err
	}
	d, err := store.DeviceByID(r.Context(), a.pool, id)
	if err != nil {
		return nil, err
	}
	if d.Removed {
		return nil, errNotFound
	}
	return d, nil
}

func (a *App) adminPatchDevice(w http.ResponseWriter, r *http.Request) error {
	d, err := a.adminDevice(r)
	if err != nil {
		return err
	}
	var body struct {
		Disabled *bool `json:"disabled"`
	}
	if err := decode(r, &body); err != nil {
		return err
	}
	ctx := r.Context()
	if body.Disabled != nil && *body.Disabled != d.Disabled {
		if err := store.SetDeviceDisabled(ctx, a.pool, d.ID, *body.Disabled); err != nil {
			return err
		}
		event := "device.enabled"
		status := "offline"
		if *body.Disabled {
			event, status = "device.disabled", "disabled"
			a.hub.KickDevice(ctx, d.ID, proto.EndAdmin, fromAdmin)
		}
		a.hub.PushDeviceStatus(d.UserID, d.ID, status, d.LastSeenAt)
		a.adminAudit(r, store.Audit{Event: event, UserID: d.UserID, DeviceID: d.ID, Meta: map[string]any{"name": d.Name}})
	}
	nd, err := store.DeviceByID(ctx, a.pool, d.ID)
	if err != nil {
		return err
	}
	a.hub.decorate(nd)
	if err := a.withLatest(ctx, nd); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"device": nd})
	return nil
}

func (a *App) adminDeleteDevice(w http.ResponseWriter, r *http.Request) error {
	d, err := a.adminDevice(r)
	if err != nil {
		return err
	}
	if err := a.removeDevice(r, d, "admin", fromAdmin); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{})
	return nil
}

// ---------- sessions + audit ----------

func (a *App) adminListSessions(w http.ResponseWriter, r *http.Request) error {
	before, err := timeCursor(r)
	if err != nil {
		return err
	}
	state := r.URL.Query().Get("state")
	switch state {
	case "", "pending", "active", "ended", "failed":
	default:
		return errf(http.StatusBadRequest, "bad_request", "invalid state")
	}
	limit := pageLimit(r)
	ss, err := store.ListSessions(r.Context(), a.pool, store.SessionFilter{State: state, Limit: limit, Before: before})
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, page(ss, limit, sessionCursor))
	return nil
}

func (a *App) adminEndSession(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	s, err := store.SessionByID(r.Context(), a.pool, id)
	if err != nil {
		return err
	}
	if s.State == "pending" || s.State == "active" {
		a.hub.EndSession(id, proto.EndAdmin, fromAdmin, 0, 0)
		a.adminAudit(r, store.Audit{Event: "admin.session_terminated", UserID: s.UserID, DeviceID: s.DeviceID, SessionID: s.ID})
	}
	writeJSON(w, http.StatusOK, map[string]any{})
	return nil
}

func (a *App) adminListAudit(w http.ResponseWriter, r *http.Request) error {
	userID, err := queryUUID(r, "user_id")
	if err != nil {
		return err
	}
	deviceID, err := queryUUID(r, "device_id")
	if err != nil {
		return err
	}
	before, err := auditBefore(r)
	if err != nil {
		return err
	}
	q := r.URL.Query()
	limit := pageLimit(r)
	evs, err := store.ListAudit(r.Context(), a.pool, store.AuditFilter{UserID: userID, DeviceID: deviceID,
		Event: q.Get("event"), Actor: q.Get("actor"), Limit: limit, Before: before})
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, page(evs, limit, auditCursor))
	return nil
}

// ---------- settings ----------

func (a *App) adminGetSettings(w http.ResponseWriter, r *http.Request) error {
	writeJSON(w, http.StatusOK, a.Settings())
	return nil
}

func (a *App) adminPutSettings(w http.ResponseWriter, r *http.Request) error {
	s := a.Settings()
	if err := decode(r, &s); err != nil {
		return err
	}
	if s.MaxSessionHours < 1 || s.MaxSessionHours > 168 {
		return errf(http.StatusBadRequest, "bad_request", "max_session_hours must be 1..168")
	}
	for _, u := range []string{s.DownloadMacOSURL, s.DownloadWindowsURL} {
		if u != "" && !strings.HasPrefix(u, "https://") && !strings.HasPrefix(u, "http://") {
			return errf(http.StatusBadRequest, "bad_request", "download URLs must be http(s)")
		}
	}
	ctx := r.Context()
	previewsWereOn := a.Settings().ScreenPreviews
	if err := store.SaveSettings(ctx, a.pool, s); err != nil {
		return err
	}
	if previewsWereOn && !s.ScreenPreviews {
		a.hub.DropPreviews("")
	}
	if err := a.reloadSettings(ctx); err != nil {
		return err
	}
	a.adminAudit(r, store.Audit{Event: "settings.updated", Meta: map[string]any{"signup_allowed": s.SignupAllowed,
		"default_auto_accept": s.DefaultAutoAccept, "max_session_hours": s.MaxSessionHours, "screen_previews": s.ScreenPreviews}})
	writeJSON(w, http.StatusOK, a.Settings())
	return nil
}
