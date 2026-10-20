package app

import (
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ruolez/remotectl/proto"
	"github.com/ruolez/remotectl/server/internal/security"
	"github.com/ruolez/remotectl/server/internal/store"
)

func decodeB64Any(s string) ([]byte, error) {
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(s); err == nil {
			return b, nil
		}
	}
	return nil, errors.New("invalid base64")
}

// ---------- client keys ----------

func (a *App) listClientKeys(w http.ResponseWriter, r *http.Request) error {
	ks, err := store.ListClientKeys(r.Context(), a.pool, userFrom(r).ID)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": ks})
	return nil
}

func (a *App) createClientKey(w http.ResponseWriter, r *http.Request) error {
	u := userFrom(r)
	var body struct {
		PublicKey string `json:"public_key"`
		Label     string `json:"label"`
	}
	if err := decode(r, &body); err != nil {
		return err
	}
	der, err := decodeB64Any(body.PublicKey)
	if err != nil {
		return errf(http.StatusBadRequest, "bad_request", "public_key must be base64 SPKI")
	}
	pub, err := x509.ParsePKIXPublicKey(der)
	ec, ok := pub.(*ecdsa.PublicKey)
	if err != nil || !ok || ec.Curve.Params().Name != "P-256" {
		return errf(http.StatusBadRequest, "bad_request", "public_key must be an ECDSA P-256 SPKI key")
	}
	ctx := r.Context()
	if existing, err := store.ClientKeyByPublicKey(ctx, a.pool, der); err == nil {
		if existing.UserID != u.ID {
			return errf(http.StatusConflict, "conflict", "key registered to another account")
		}
		if existing.RevokedAt != nil {
			return errf(http.StatusConflict, "conflict", "key was revoked; generate a new one")
		}
		writeJSON(w, http.StatusOK, map[string]any{"client_key": existing})
		return nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	sha, _ := proto.ClientKeyHash(base64.StdEncoding.EncodeToString(der))
	label := strings.TrimSpace(body.Label)
	if label == "" {
		label = "Browser"
	}
	k, err := store.CreateClientKey(ctx, a.pool, u.ID, der, sha, trunc(label, 128))
	if err != nil {
		return err
	}
	a.audit(ctx, store.Audit{Actor: "user", Event: "client_key.registered", UserID: u.ID, IP: a.clientIP(r),
		Meta: map[string]any{"client_key_id": k.ID, "label": k.Label}})
	writeJSON(w, http.StatusOK, map[string]any{"client_key": k})
	return nil
}

func (a *App) revokeClientKey(w http.ResponseWriter, r *http.Request) error {
	u := userFrom(r)
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	k, err := store.RevokeClientKey(r.Context(), a.pool, u.ID, id)
	if err != nil {
		return err
	}
	a.hub.PushKeyRevoked(u.ID, k.PublicKey)
	a.audit(r.Context(), store.Audit{Actor: "user", Event: "client_key.revoked", UserID: u.ID, IP: a.clientIP(r),
		Meta: map[string]any{"client_key_id": k.ID, "label": k.Label}})
	writeJSON(w, http.StatusOK, map[string]any{})
	return nil
}

// ---------- devices ----------

func (a *App) ownDevice(r *http.Request) (*store.Device, error) {
	id, err := pathID(r, "id")
	if err != nil {
		return nil, err
	}
	d, err := store.DeviceByID(r.Context(), a.pool, id)
	if err != nil {
		return nil, err
	}
	if d.UserID != userFrom(r).ID || d.Removed {
		return nil, errNotFound
	}
	a.hub.decorate(d)
	return d, nil
}

func (a *App) listDevices(w http.ResponseWriter, r *http.Request) error {
	ds, err := store.DevicesByUser(r.Context(), a.pool, userFrom(r).ID)
	if err != nil {
		return err
	}
	for i := range ds {
		a.hub.decorate(&ds[i])
		ds[i].OwnerEmail = ""
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": ds})
	return nil
}

func (a *App) getDevice(w http.ResponseWriter, r *http.Request) error {
	d, err := a.ownDevice(r)
	if err != nil {
		return err
	}
	d.OwnerEmail = ""
	writeJSON(w, http.StatusOK, map[string]any{"device": d})
	return nil
}

func (a *App) patchDevice(w http.ResponseWriter, r *http.Request) error {
	d, err := a.ownDevice(r)
	if err != nil {
		return err
	}
	var body struct {
		Name       *string `json:"name"`
		AutoAccept *bool   `json:"auto_accept"`
	}
	if err := decode(r, &body); err != nil {
		return err
	}
	if body.Name != nil {
		n := strings.TrimSpace(*body.Name)
		if n == "" || len(n) > 64 {
			return errf(http.StatusBadRequest, "bad_request", "name must be 1 to 64 characters")
		}
		body.Name = &n
	}
	ctx := r.Context()
	if err := store.UpdateDevice(ctx, a.pool, d.ID, body.Name, body.AutoAccept); err != nil {
		return err
	}
	u := userFrom(r)
	if body.Name != nil && *body.Name != d.Name {
		a.audit(ctx, store.Audit{Actor: "user", Event: "device.renamed", UserID: u.ID, DeviceID: d.ID, IP: a.clientIP(r),
			Meta: map[string]any{"from": d.Name, "to": *body.Name}})
	}
	if body.AutoAccept != nil && *body.AutoAccept != d.AutoAccept {
		a.audit(ctx, store.Audit{Actor: "user", Event: "device.updated", UserID: u.ID, DeviceID: d.ID, IP: a.clientIP(r),
			Meta: map[string]any{"auto_accept": *body.AutoAccept}})
	}
	nd, err := store.DeviceByID(ctx, a.pool, d.ID)
	if err != nil {
		return err
	}
	a.hub.decorate(nd)
	a.hub.PushDeviceUpdate(nd.ID, nd.Name, nd.AutoAccept)
	nd.OwnerEmail = ""
	writeJSON(w, http.StatusOK, map[string]any{"device": nd})
	return nil
}

func (a *App) removeDevice(r *http.Request, d *store.Device, actor, origin string) error {
	ctx := r.Context()
	if err := store.RemoveDevice(ctx, a.pool, d.ID); err != nil {
		return err
	}
	reason := proto.EndDeviceOffline
	if origin == fromAdmin {
		reason = proto.EndAdmin
	}
	a.hub.KickDevice(ctx, d.ID, reason, origin)
	a.hub.PushDeviceStatus(d.UserID, d.ID, "removed", nil)
	ev := store.Audit{Actor: actor, Event: "device.removed", UserID: d.UserID, DeviceID: d.ID, IP: a.clientIP(r), Meta: map[string]any{"name": d.Name}}
	if actor == "admin" {
		ev.Meta["admin_id"] = userFrom(r).ID
	}
	a.audit(ctx, ev)
	return nil
}

func (a *App) deleteDevice(w http.ResponseWriter, r *http.Request) error {
	d, err := a.ownDevice(r)
	if err != nil {
		return err
	}
	if err := a.removeDevice(r, d, "user", fromBrowser); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{})
	return nil
}

func (a *App) createEnrollToken(w http.ResponseWriter, r *http.Request) error {
	u := userFrom(r)
	tok := security.NewEnrollToken()
	norm, _ := security.NormalizeEnrollToken(tok)
	exp, err := store.CreateEnrollToken(r.Context(), a.pool, u.ID, security.SHA256(norm), enrollTTL)
	if err != nil {
		return err
	}
	a.audit(r.Context(), store.Audit{Actor: "user", Event: "enroll_token.created", UserID: u.ID, IP: a.clientIP(r)})
	writeJSON(w, http.StatusOK, map[string]any{"token": tok, "expires_at": exp.UTC()})
	return nil
}

func sessionCursor(s store.Session) string  { return s.CreatedAt.UTC().Format(time.RFC3339Nano) }
func auditCursor(e store.AuditEvent) string { return strconv.FormatInt(e.ID, 10) }

func (a *App) listSessions(w http.ResponseWriter, r *http.Request) error {
	deviceID, err := queryUUID(r, "device_id")
	if err != nil {
		return err
	}
	before, err := timeCursor(r)
	if err != nil {
		return err
	}
	limit := pageLimit(r)
	ss, err := store.ListSessions(r.Context(), a.pool, store.SessionFilter{UserID: userFrom(r).ID, DeviceID: deviceID, Limit: limit, Before: before})
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, page(ss, limit, sessionCursor))
	return nil
}

func auditBefore(r *http.Request) (int64, error) {
	v := r.URL.Query().Get("before")
	if v == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n <= 0 {
		return 0, errf(http.StatusBadRequest, "bad_request", "invalid cursor")
	}
	return n, nil
}

func (a *App) listAudit(w http.ResponseWriter, r *http.Request) error {
	deviceID, err := queryUUID(r, "device_id")
	if err != nil {
		return err
	}
	before, err := auditBefore(r)
	if err != nil {
		return err
	}
	limit := pageLimit(r)
	evs, err := store.ListAudit(r.Context(), a.pool, store.AuditFilter{OwnerID: userFrom(r).ID, DeviceID: deviceID, Limit: limit, Before: before})
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, page(evs, limit, auditCursor))
	return nil
}
