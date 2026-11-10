package app

import (
	"errors"
	"net/http"

	"github.com/ruolez/remotectl/proto"
	"github.com/ruolez/remotectl/server/internal/store"
)

// getDeviceSession answers who holds the device: its pending or active
// session, or null when it is free.
func (a *App) getDeviceSession(w http.ResponseWriter, r *http.Request) error {
	d, err := a.ownDevice(r)
	if err != nil {
		return err
	}
	s, err := store.LiveSessionByDevice(r.Context(), a.pool, d.ID)
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusOK, map[string]any{"session": nil})
		return nil
	}
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"session": s})
	return nil
}

type endDeviceSessionBody struct {
	Takeover bool `json:"takeover"`
}

// endDeviceSession ends whatever session holds the device, so the owner can
// connect from another browser. Both ends hear why: `taken_over` when the
// caller connects next, `ended_remotely` otherwise.
func (a *App) endDeviceSession(w http.ResponseWriter, r *http.Request) error {
	d, err := a.ownDevice(r)
	if err != nil {
		return err
	}
	var body endDeviceSessionBody
	if err := decodeOptional(r, &body); err != nil {
		return err
	}
	ctx := r.Context()
	ids, err := store.LiveSessionIDs(ctx, a.pool, d.ID, "")
	if err != nil {
		return err
	}
	reason, event := proto.EndEndedRemotely, "session.terminated"
	if body.Takeover {
		reason, event = proto.EndTakenOver, "session.taken_over"
	}
	for _, id := range ids {
		a.audit(ctx, store.Audit{Actor: "user", Event: event, UserID: d.UserID, DeviceID: d.ID, SessionID: id, IP: a.clientIP(r)})
		a.hub.EndSession(id, reason, fromOwner, 0, 0)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ended": len(ids)})
	return nil
}
