package app

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/ruolez/remotectl/proto"
	"github.com/ruolez/remotectl/server/internal/store"
)

const (
	checkSpread     = 3 * time.Second // per device in a bulk push
	maxCheckWindow  = 10 * time.Minute
	installDeadline = 15 * time.Minute
)

// spreadWindow is how long a push of update.check to n devices is spread over.
func spreadWindow(n int, per time.Duration) time.Duration {
	return min(time.Duration(max(n, 0))*per, maxCheckWindow)
}

// spreadDelays gives device i a random moment in the i-th of n equal slots
// of window; rnd(n) returns a value in [0, n).
func spreadDelays(n int, window time.Duration, rnd func(int64) int64) []time.Duration {
	out := make([]time.Duration, max(n, 0))
	if n <= 0 {
		return out
	}
	slot := window / time.Duration(n)
	for i := range out {
		out[i] = time.Duration(i) * slot
		if slot > 0 {
			out[i] += time.Duration(rnd(int64(slot)))
		}
	}
	return out
}

// auditEventFor is the audit event of a reported update state, if any.
func auditEventFor(prevState string, next proto.UpdateStatus) (string, bool) {
	switch next.State {
	case proto.UpdateInstalling:
		if prevState != proto.UpdateInstalling {
			return "agent.update.started", true
		}
	case proto.UpdateFailed:
		return "agent.update.failed", true
	case proto.UpdateRolledBack:
		return "agent.update.rolled_back", true
	}
	return "", false
}

// helloOutcome derives the end of an install from the version a device
// reports in hello: the target means success, the old version long after
// the install started means it failed without saying so.
func helloOutcome(state, from, version string, at *time.Time, running string, now time.Time) (proto.UpdateStatus, string, bool) {
	if state != proto.UpdateInstalling || version == "" {
		return proto.UpdateStatus{}, "", false
	}
	switch {
	case running == version:
		return proto.UpdateStatus{State: proto.UpdateUpToDate, From: from, Version: version}, "agent.update.succeeded", true
	case running == from && at != nil && now.Sub(*at) > installDeadline:
		return proto.UpdateStatus{State: proto.UpdateFailed, From: from, Version: version,
			Error: "still on " + from + " after install"}, "agent.update.failed", true
	}
	return proto.UpdateStatus{}, "", false
}

// updateTargets are the devices to send update.check to: those pickRelease
// offers a release (releaseID's only, when set), minus the ones whose last
// attempt at that version failed or rolled back.
func updateTargets(devs []store.Device, rels []store.Release, explicit bool, releaseID string) []string {
	ids := []string{}
	for _, d := range devs {
		rel := pickRelease(rels, d.OS, d.Arch, d.AgentVersion, proto.RolloutBucket(d.ID), explicit)
		switch {
		case rel == nil || (releaseID != "" && rel.ID != releaseID):
		case rel.Version == d.UpdateVersion && (d.UpdateState == proto.UpdateFailed || d.UpdateState == proto.UpdateRolledBack):
		default:
			ids = append(ids, d.ID)
		}
	}
	return ids
}

// cleanText truncates agent-reported text to n bytes of valid UTF-8.
func cleanText(s string, n int) string { return strings.ToValidUTF8(trunc(s, n), "") }

func releaseChecksKey(id string) string { return "release:" + id }

// requestUpdate is "Update now": an explicit update.check to an online device
// that has a newer release.
func (a *App) requestUpdate(ctx context.Context, d *store.Device, force bool) (string, error) {
	if !slices.Contains(proto.ReleaseOSes, d.OS) {
		return "", errf(http.StatusConflict, "unsupported", "updates are not available for this device")
	}
	if d.Disabled || !a.hub.IsOnline(d.ID) {
		return "", errf(http.StatusConflict, "device_offline", "device is offline")
	}
	rels, err := store.ListReleases(ctx, a.pool)
	if err != nil {
		return "", err
	}
	rel := pickRelease(rels, d.OS, d.Arch, d.AgentVersion, proto.RolloutBucket(d.ID), true)
	if rel == nil {
		return "", errf(http.StatusConflict, "up_to_date", "no newer release for this device")
	}
	prev := proto.UpdateStatus{State: d.UpdateState, From: d.UpdateFrom, Version: d.UpdateVersion, Error: d.UpdateError}
	next := proto.UpdateStatus{State: proto.UpdateChecking, From: d.AgentVersion, Version: rel.Version}
	if err := a.hub.SetUpdateStatus(ctx, d.UserID, d.ID, next); err != nil {
		return "", err
	}
	if !a.hub.PushUpdateCheck(d.ID, true, force) {
		if err := a.hub.SetUpdateStatus(ctx, d.UserID, d.ID, prev); err != nil {
			a.log.Error("restore update state", "device", d.ID, "err", err)
		}
		return "", errf(http.StatusConflict, "device_offline", "device is offline")
	}
	return rel.Version, nil
}

type updateBody struct {
	Force bool `json:"force"`
}

func (a *App) updateDevice(w http.ResponseWriter, r *http.Request) error {
	d, err := a.ownDevice(r)
	if err != nil {
		return err
	}
	var body updateBody
	if err := decodeOptional(r, &body); err != nil {
		return err
	}
	ctx := r.Context()
	version, err := a.requestUpdate(ctx, d, body.Force)
	if err != nil {
		return err
	}
	a.audit(ctx, store.Audit{Actor: "user", Event: "agent.update.requested", UserID: d.UserID, DeviceID: d.ID, IP: a.clientIP(r),
		Meta: map[string]any{"version": version, "force": body.Force}})
	writeJSON(w, http.StatusAccepted, map[string]any{"version": version})
	return nil
}

func (a *App) adminUpdateDevice(w http.ResponseWriter, r *http.Request) error {
	d, err := a.adminDevice(r)
	if err != nil {
		return err
	}
	var body updateBody
	if err := decodeOptional(r, &body); err != nil {
		return err
	}
	version, err := a.requestUpdate(r.Context(), d, body.Force)
	if err != nil {
		return err
	}
	a.adminAudit(r, store.Audit{Event: "agent.update.requested", UserID: d.UserID, DeviceID: d.ID,
		Meta: map[string]any{"version": version, "force": body.Force}})
	writeJSON(w, http.StatusAccepted, map[string]any{"version": version})
	return nil
}

// onlineDevices loads the connected, enabled devices (of os, when set).
func (a *App) onlineDevices(ctx context.Context, os string) ([]store.Device, error) {
	ds, err := store.DevicesByIDs(ctx, a.pool, a.hub.OnlineDeviceIDs())
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(ds, func(d store.Device) bool { return d.Disabled || (os != "" && d.OS != os) }), nil
}

// adminPushUpdates is "Update all outdated": explicit, never forced.
func (a *App) adminPushUpdates(w http.ResponseWriter, r *http.Request) error {
	var body struct {
		OS string `json:"os"`
	}
	if err := decodeOptional(r, &body); err != nil {
		return err
	}
	if body.OS != "" && !slices.Contains(proto.ReleaseOSes, body.OS) {
		return errf(http.StatusBadRequest, "bad_request", "os must be darwin or windows")
	}
	ctx := r.Context()
	devs, err := a.onlineDevices(ctx, body.OS)
	if err != nil {
		return err
	}
	rels, err := store.ListReleases(ctx, a.pool)
	if err != nil {
		return err
	}
	ids := updateTargets(devs, rels, true, "")
	a.hub.ScheduleUpdateChecks("push:"+body.OS, ids, true)
	a.adminAudit(r, store.Audit{Event: "agent.update.pushed", Meta: map[string]any{"os": body.OS, "queued": len(ids)}})
	writeJSON(w, http.StatusOK, map[string]any{"queued": len(ids)})
	return nil
}

// scheduleReleaseChecks tells the online devices a just published (or
// widened) release now applies to.
func (a *App) scheduleReleaseChecks(ctx context.Context, rel *store.Release) error {
	devs, err := a.onlineDevices(ctx, rel.OS)
	if err != nil {
		return err
	}
	rels, err := store.ListReleases(ctx, a.pool)
	if err != nil {
		return err
	}
	a.hub.ScheduleUpdateChecks(releaseChecksKey(rel.ID), updateTargets(devs, rels, false, rel.ID), false)
	return nil
}
