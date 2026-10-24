package app

import (
	"net/http"

	"github.com/ruolez/remotectl/proto"
	"github.com/ruolez/remotectl/server/internal/store"
)

type virtualDisplayBody struct {
	Action string `json:"action"`
}

// requestVirtualDisplay asks an online Windows device to install or remove
// its virtual display driver; the outcome arrives in device.info.
func (a *App) requestVirtualDisplay(r *http.Request, d *store.Device) (string, error) {
	var body virtualDisplayBody
	if err := decodeOptional(r, &body); err != nil {
		return "", err
	}
	if body.Action != proto.VirtualDisplayInstall && body.Action != proto.VirtualDisplayRemove {
		return "", errf(http.StatusBadRequest, "bad_request", "action must be install or remove")
	}
	if d.OS != "windows" {
		return "", errf(http.StatusConflict, "unsupported", "a virtual display is only available on Windows")
	}
	if d.Disabled || !a.hub.IsOnline(d.ID) || !a.hub.PushVirtualDisplay(d.ID, body.Action) {
		return "", errf(http.StatusConflict, "device_offline", "device is offline")
	}
	return body.Action, nil
}

func (a *App) virtualDisplayDevice(w http.ResponseWriter, r *http.Request) error {
	d, err := a.ownDevice(r)
	if err != nil {
		return err
	}
	action, err := a.requestVirtualDisplay(r, d)
	if err != nil {
		return err
	}
	a.audit(r.Context(), store.Audit{Actor: "user", Event: "device.virtual_display.requested",
		UserID: d.UserID, DeviceID: d.ID, IP: a.clientIP(r), Meta: map[string]any{"action": action}})
	writeJSON(w, http.StatusAccepted, map[string]any{})
	return nil
}

func (a *App) adminVirtualDisplayDevice(w http.ResponseWriter, r *http.Request) error {
	d, err := a.adminDevice(r)
	if err != nil {
		return err
	}
	action, err := a.requestVirtualDisplay(r, d)
	if err != nil {
		return err
	}
	a.adminAudit(r, store.Audit{Event: "device.virtual_display.requested", UserID: d.UserID, DeviceID: d.ID,
		Meta: map[string]any{"action": action}})
	writeJSON(w, http.StatusAccepted, map[string]any{})
	return nil
}
