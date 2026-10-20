package app

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/ruolez/remotectl/proto"
	"github.com/ruolez/remotectl/server/internal/security"
	"github.com/ruolez/remotectl/server/internal/store"
)

func (a *App) agentRateLimit(r *http.Request) error {
	if !a.agentLimits.Allow(a.clientIP(r)) {
		return errRateLimited
	}
	return nil
}

func (a *App) agentEnroll(w http.ResponseWriter, r *http.Request) error {
	if err := a.agentRateLimit(r); err != nil {
		return err
	}
	var body struct {
		Token     string `json:"token"`
		PublicKey string `json:"public_key"`
		Hostname  string `json:"hostname"`
		OS        string `json:"os"`
		Arch      string `json:"arch"`
		Version   string `json:"version"`
	}
	if err := decode(r, &body); err != nil {
		return err
	}
	norm, ok := security.NormalizeEnrollToken(body.Token)
	if !ok {
		return errf(http.StatusBadRequest, "bad_request", "malformed enrollment token")
	}
	pub, err := decodeB64Any(body.PublicKey)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return errf(http.StatusBadRequest, "bad_request", "public_key must be a base64 32-byte Ed25519 key")
	}
	ctx := r.Context()
	tx, err := a.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	tokenID, userID, err := store.ConsumeEnrollToken(ctx, tx, security.SHA256(norm))
	if errors.Is(err, store.ErrNotFound) {
		return errf(http.StatusUnauthorized, "unauthorized", "enrollment token is invalid, used or expired")
	}
	if err != nil {
		return err
	}
	name := strings.TrimSpace(body.Hostname)
	if name == "" {
		name = "New device"
	}
	deviceID, err := store.CreateDevice(ctx, tx, store.NewDevice{UserID: userID, Name: trunc(name, 64), Hostname: trunc(body.Hostname, 255),
		OS: trunc(body.OS, 32), Arch: trunc(body.Arch, 32), Version: trunc(body.Version, 64), PublicKey: pub,
		AutoAccept: a.Settings().DefaultAutoAccept})
	if errors.Is(err, store.ErrConflict) {
		return errf(http.StatusConflict, "conflict", "this device key is already enrolled")
	}
	if err != nil {
		return err
	}
	if err := store.SetEnrollTokenDevice(ctx, tx, tokenID, deviceID); err != nil {
		return err
	}
	if err := store.InsertAudit(ctx, tx, store.Audit{Actor: "agent", Event: "device.enrolled", UserID: userID, DeviceID: deviceID,
		IP: a.clientIP(r), Meta: map[string]any{"hostname": body.Hostname, "os": body.OS, "arch": body.Arch, "version": body.Version}}); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"device_id": deviceID, "name": trunc(name, 64)})
	return nil
}

func (a *App) deviceUsable(r *http.Request, id string) (*store.Device, error) {
	d, err := store.DeviceByID(r.Context(), a.pool, id)
	if errors.Is(err, store.ErrNotFound) || (err == nil && d.Removed) {
		return nil, errf(http.StatusForbidden, "device_removed", "device was removed")
	}
	if err != nil {
		return nil, err
	}
	if d.Disabled {
		return nil, errf(http.StatusForbidden, "device_disabled", "device is disabled")
	}
	return d, nil
}

func (a *App) agentChallenge(w http.ResponseWriter, r *http.Request) error {
	if err := a.agentRateLimit(r); err != nil {
		return err
	}
	var body struct {
		DeviceID string `json:"device_id"`
	}
	if err := decode(r, &body); err != nil {
		return err
	}
	if !validUUID(body.DeviceID) {
		return errf(http.StatusForbidden, "device_removed", "unknown device")
	}
	if _, err := a.deviceUsable(r, body.DeviceID); err != nil {
		return err
	}
	nonce := security.NewToken(32)
	a.nonces.Put(nonce, body.DeviceID)
	writeJSON(w, http.StatusOK, map[string]any{"nonce": nonce})
	return nil
}

func (a *App) agentAuth(w http.ResponseWriter, r *http.Request) error {
	if err := a.agentRateLimit(r); err != nil {
		return err
	}
	var body struct {
		DeviceID  string `json:"device_id"`
		Nonce     string `json:"nonce"`
		Signature string `json:"signature"`
	}
	if err := decode(r, &body); err != nil {
		return err
	}
	owner, ok := a.nonces.Take(body.Nonce)
	if !ok || owner != body.DeviceID {
		return errf(http.StatusUnauthorized, "unauthorized", "nonce invalid or expired")
	}
	d, err := a.deviceUsable(r, body.DeviceID)
	if err != nil {
		return err
	}
	if err := proto.VerifyDeviceSig(base64.StdEncoding.EncodeToString(d.PublicKey), body.Signature,
		proto.AuthChallengeMessage(body.DeviceID, body.Nonce)); err != nil {
		return errf(http.StatusUnauthorized, "unauthorized", "signature does not verify")
	}
	tok, exp, err := security.IssueAgentJWT(a.cfg.JWTSecret, d.ID, time.Now(), agentJWTTTL)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"token": tok, "expires_at": exp.UTC()})
	return nil
}

// agentDevice authenticates the Bearer JWT.
func (a *App) agentDevice(r *http.Request) (*store.Device, error) {
	id, err := security.ParseAgentJWT(a.cfg.JWTSecret, bearer(r))
	if err != nil {
		return nil, errf(http.StatusUnauthorized, "unauthorized", "invalid agent token")
	}
	return a.deviceUsable(r, id)
}

// agentUpdate offers the release pickRelease selects for this device; the
// cohort is applied here unless explicit=1.
func (a *App) agentUpdate(w http.ResponseWriter, r *http.Request) error {
	d, err := a.agentDevice(r)
	if err != nil {
		return err
	}
	q := r.URL.Query()
	os, arch, version := q.Get("os"), q.Get("arch"), q.Get("version")
	if os == "" {
		os = d.OS
	}
	if arch == "" {
		arch = d.Arch
	}
	if version == "" {
		version = d.AgentVersion
	}
	rels, err := store.ListReleases(r.Context(), a.pool)
	if err != nil {
		return err
	}
	explicit := q.Get("explicit") == "1" || q.Get("explicit") == "true"
	rel := pickRelease(rels, os, arch, version, proto.RolloutBucket(d.ID), explicit)
	if rel == nil {
		w.WriteHeader(http.StatusNoContent)
		return nil
	}
	writeJSON(w, http.StatusOK, map[string]any{"version": rel.Version, "os": rel.OS, "arch": rel.Arch, "url": rel.URL,
		"sha256": rel.SHA256, "size": rel.Size, "signature": rel.Signature})
	return nil
}

func (a *App) agentSelfDelete(w http.ResponseWriter, r *http.Request) error {
	d, err := a.agentDevice(r)
	if err != nil {
		return err
	}
	ctx := r.Context()
	if err := store.RemoveDevice(ctx, a.pool, d.ID); err != nil {
		return err
	}
	a.hub.KickDevice(ctx, d.ID, proto.EndDeviceOffline, fromAgent)
	a.hub.PushDeviceStatus(d.UserID, d.ID, "removed", nil)
	a.audit(ctx, store.Audit{Actor: "agent", Event: "device.removed", UserID: d.UserID, DeviceID: d.ID, IP: a.clientIP(r),
		Meta: map[string]any{"name": d.Name, "uninstall": true}})
	writeJSON(w, http.StatusOK, map[string]any{})
	return nil
}
