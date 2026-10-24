package session

import (
	"encoding/json"
	"errors"
	"os"
	"slices"
	"sync"
	"time"

	"github.com/ruolez/remotectl/agent/internal/platform"
)

// Remote screen resolution (control `display`): the controller switches the
// device's monitor to another display mode. The change is for the login
// session only and is undone a little after the session ends, or by the next
// agent process when this one dies first (Options.DisplayMarker).

// DisplayMarkerName is the Options.DisplayMarker file in the agent's data
// directory.
const DisplayMarkerName = "display-changed"

// displayRestoreDelay lets a reconnecting browser take over a changed mode
// instead of seeing it restored and switched again.
const displayRestoreDelay = 10 * time.Second

// Reply codes of a refused or failed display request.
const (
	displayUnsupported = "unsupported"
	displayUnknownMode = "unknown_mode"
	displayFailed      = "failed"
)

// displayState is the device's display changes, shared by sessions.
type displayState struct {
	mu       sync.Mutex
	original map[uint32]platform.DisplayMode // monitor id → mode before the first change; nil = unchanged
	timer    *time.Timer                     // pending restore after a session ended
}

// setDisplayMode switches a monitor (capture stopped) and remembers its mode
// before the first change.
func (m *Manager) setDisplayMode(monitorID, w, h uint32, cur platform.DisplayMode) error {
	d := &m.display
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := m.o.Backend.SetDisplayMode(monitorID, w, h); err != nil {
		return err
	}
	if d.original == nil {
		d.original = map[uint32]platform.DisplayMode{}
		if m.o.DisplayMarker != "" {
			if err := os.WriteFile(m.o.DisplayMarker, nil, 0o600); err != nil {
				m.o.Log.Warn("display: marker not written", "err", err)
			}
		}
	}
	if _, ok := d.original[monitorID]; !ok {
		d.original[monitorID] = cur
	}
	return nil
}

// originalMode is a changed monitor's mode before the change.
func (m *Manager) originalMode(monitorID uint32) (platform.DisplayMode, bool) {
	m.display.mu.Lock()
	defer m.display.mu.Unlock()
	o, ok := m.display.original[monitorID]
	return o, ok
}

// restoreDisplays brings back the saved modes when a session changed one.
func (m *Manager) restoreDisplays() error {
	d := &m.display
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.timer != nil {
		d.timer.Stop()
		d.timer = nil
	}
	if d.original == nil {
		return nil
	}
	err := m.o.Backend.RestoreDisplays()
	if err != nil {
		m.o.Log.Warn("display: restore failed", "err", err)
		return err
	}
	d.original = nil
	m.removeDisplayMarker()
	return nil
}

func (m *Manager) removeDisplayMarker() {
	if m.o.DisplayMarker == "" {
		return
	}
	if err := os.Remove(m.o.DisplayMarker); err != nil && !errors.Is(err, os.ErrNotExist) {
		m.o.Log.Warn("display: marker not removed", "err", err)
	}
}

// recoverDisplays restores what a previous agent process left changed.
func (m *Manager) recoverDisplays() {
	if m.o.DisplayMarker == "" {
		return
	}
	if _, err := os.Stat(m.o.DisplayMarker); err != nil {
		return
	}
	if err := m.o.Backend.RestoreDisplays(); err != nil && !errors.Is(err, platform.ErrUnsupported) {
		m.o.Log.Warn("display: restore after a restart failed", "err", err)
		return
	}
	m.o.Log.Info("display: restored the modes a previous run changed")
	m.removeDisplayMarker()
}

// displayAcquire: a session starts and takes over a mode the last one left
// changed (a pending restore is cancelled; one in progress finishes first).
func (m *Manager) displayAcquire() {
	d := &m.display
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.timer != nil {
		d.timer.Stop()
		d.timer = nil
	}
}

// displayRelease: a session ended; a changed mode is restored after the
// grace period unless the next session takes it over.
func (m *Manager) displayRelease() {
	d := &m.display
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.original == nil || d.timer != nil {
		return
	}
	d.timer = time.AfterFunc(m.displayDelay, func() { _ = m.restoreDisplays() })
}

// ---- media pipe ----

type displayMode struct {
	Width  uint32  `json:"width"`
	Height uint32  `json:"height"`
	Scale  float32 `json:"scale"`
}

func toDisplayMode(d platform.DisplayMode) displayMode {
	return displayMode{d.Width, d.Height, d.Scale}
}

// DisplayInfo is the stream state's `display`: the active monitor's modes,
// its current mode and, while a session changed it, the mode it had before.
type DisplayInfo struct {
	Modes    []displayMode `json:"modes"`
	Current  displayMode   `json:"current"`
	Original *displayMode  `json:"original"`
}

// displayCache is the active monitor's modes, read when it changes.
type displayCache struct {
	modes   []platform.DisplayMode
	current platform.DisplayMode
}

// readDisplayLocked lists the active monitor's modes; nil when they cannot
// change (or only one exists). Caller holds mu.
func (p *mediaPipe) readDisplayLocked() *displayCache {
	if len(p.monitors) == 0 {
		return nil
	}
	modes, cur, err := p.b.DisplayModes(p.monitors[p.active].ID)
	if err != nil || len(modes) < 2 {
		return nil
	}
	return &displayCache{modes, cur}
}

func (p *mediaPipe) displayInfoLocked() *DisplayInfo {
	c := p.display
	if c == nil {
		return nil
	}
	info := &DisplayInfo{Current: toDisplayMode(c.current)}
	for _, md := range c.modes {
		info.Modes = append(info.Modes, toDisplayMode(md))
	}
	if o, ok := p.s.m.originalMode(p.monitors[p.active].ID); ok {
		om := toDisplayMode(o)
		info.Original = &om
	}
	return info
}

type displayRequest struct {
	ID      int64  `json:"id"`
	Width   uint32 `json:"width"`
	Height  uint32 `json:"height"`
	Restore bool   `json:"restore"`
}

// onDisplay answers control `display`: switches the active monitor to a mode
// it listed (or restores the saved modes), restarting capture at the new size.
func (p *mediaPipe) onDisplay(raw []byte) {
	var req displayRequest
	_ = json.Unmarshal(raw, &req)
	code := p.applyDisplay(req)
	msg := map[string]any{"t": "display", "id": req.ID, "ok": code == ""}
	if code != "" {
		msg["code"] = code
	}
	p.sendControl(msg)
	p.sendStreamState(false)
}

func (p *mediaPipe) applyDisplay(req displayRequest) string {
	p.capMu.Lock()
	defer p.capMu.Unlock()
	p.mu.Lock()
	c, stopped := p.display, p.stopped
	var id uint32
	if len(p.monitors) > 0 {
		id = p.monitors[p.active].ID
	}
	p.mu.Unlock()
	if stopped || c == nil {
		return displayUnsupported
	}
	_, changed := p.s.m.originalMode(id)
	if req.Restore && !changed {
		return ""
	}
	if !req.Restore {
		i := slices.IndexFunc(c.modes, func(m platform.DisplayMode) bool { return m.Width == req.Width && m.Height == req.Height })
		if i < 0 {
			return displayUnknownMode
		}
		if c.modes[i].Width == c.current.Width && c.modes[i].Height == c.current.Height {
			return ""
		}
	}

	p.mu.Lock()
	running := p.running
	p.running = false
	p.mu.Unlock()
	if running {
		p.b.StopCapture()
	}
	var err error
	if req.Restore {
		err = p.s.m.restoreDisplays()
	} else {
		err = p.s.m.setDisplayMode(id, req.Width, req.Height, c.current)
	}
	if err != nil {
		p.s.log.Error("display: mode change failed", "restore", req.Restore, "width", req.Width, "height", req.Height, "err", err)
	} else {
		p.s.log.Info("display: mode changed", "restore", req.Restore, "width", req.Width, "height", req.Height)
	}
	p.refreshMonitorsLocked(id)
	var startErr error
	if running {
		startErr = p.startCaptureLocked()
	}
	p.syncWallpaperLocked(running && startErr == nil)
	if startErr != nil {
		p.s.log.Error("capture restart failed", "err", startErr)
		p.sendControl(map[string]any{"t": "error", "code": "capture_failed", "detail": startErr.Error()})
	}
	p.mu.Lock()
	w, h, active := p.monitors[p.active].Width, p.monitors[p.active].Height, p.active
	p.mu.Unlock()
	p.sendControl(map[string]any{"t": "monitor", "active": active, "width": w, "height": h})
	if err != nil {
		return displayFailed
	}
	return ""
}

// refreshMonitorsLocked lists the monitors again after a mode change, keeps
// monitor id active and reads its modes; caller holds capMu.
func (p *mediaPipe) refreshMonitorsLocked(id uint32) {
	mons, err := p.b.Monitors()
	p.mu.Lock()
	defer p.mu.Unlock()
	if err == nil && len(mons) > 0 {
		p.monitors = mons
		p.active = 0
		for i, m := range mons {
			if m.ID == id {
				p.active = i
			}
		}
	}
	p.display = p.readDisplayLocked()
	p.resetAutoLocked()
}
