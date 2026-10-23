package session

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"

	"github.com/ruolez/remotectl/agent/internal/keymap"
	"github.com/ruolez/remotectl/agent/internal/platform"
	"github.com/ruolez/remotectl/proto"
)

// Saved sign-in (PROTOCOL §4.2 and control `unlock`): the browser hands over a
// credential sealed to this device's unlock key; the agent opens it and types
// the password on the lock screen, never anywhere else.

// unlockSpacing is the least time between two unlock attempts in a session,
// so a wrong saved password cannot lock the account out in a loop.
const unlockSpacing = 5 * time.Second

// Reply codes of a refused or failed unlock.
const (
	unlockUnsupported   = "unsupported"
	unlockBusy          = "busy"
	unlockTooSoon       = "too_soon"
	unlockNotLocked     = "not_locked"
	unlockUntrusted     = "untrusted"
	unlockStaleKey      = "stale_key"
	unlockBadCredential = "bad_credential"
	unlockInputFailed   = "input_failed"
)

const (
	usageA      = 0x04
	usageEnter  = 0x28
	usageLShift = 0xE1
	usageLGUI   = 0xE3
)

// unlockStep is one action of the unlock sequence: a key chord (pressed in
// order, released in reverse), the secure attention sequence, text typed one
// character at a time, a pause, or a check that the screen is still locked.
type unlockStep struct {
	keys        []uint16
	sas         bool
	text        string
	wait        time.Duration
	checkLocked bool
}

// unlockSteps is how a password reaches the unlock screen of goos. Windows
// needs Ctrl+Alt+Delete (or any key) to raise the sign-in box from the lock
// curtain; macOS wakes its password field with any key. Select-all first so
// a half-typed password is replaced, not extended.
func unlockSteps(goos, password string) []unlockStep {
	switch goos {
	case "windows":
		return []unlockStep{{sas: true}, {wait: 2 * time.Second}, {checkLocked: true},
			{keys: []uint16{usageLCtrl, usageA}}, {text: password}, {keys: []uint16{usageEnter}}}
	case "darwin":
		return []unlockStep{{keys: []uint16{usageLShift}}, {wait: time.Second}, {checkLocked: true},
			{keys: []uint16{usageLGUI, usageA}}, {text: password}, {keys: []uint16{usageEnter}}}
	}
	return []unlockStep{{checkLocked: true}, {keys: []uint16{usageLCtrl, usageA}}, {text: password},
		{keys: []uint16{usageEnter}}}
}

var errUnlockNotLocked = errors.New("the screen is not locked")

// keyGap paces typed characters: password fields drop keys sent in a burst.
const keyGap = 20 * time.Millisecond

// runUnlock performs steps. sas raises the sign-in box on Windows; nil taps
// Ctrl instead. sleep returns false when the session ends.
func runUnlock(b platform.Backend, km keymap.Table, sas func(), steps []unlockStep, sleep func(time.Duration) bool) error {
	chord := func(usages []uint16) error {
		var err error
		for _, u := range usages {
			if e, ok := km[u]; ok {
				err = errors.Join(err, b.Inject(platform.InputEvent{Type: platform.EvKey, Keycode: e.Code, Flags: e.Flags, Down: true}))
			}
		}
		for i := len(usages) - 1; i >= 0; i-- {
			if e, ok := km[usages[i]]; ok {
				err = errors.Join(err, b.Inject(platform.InputEvent{Type: platform.EvKey, Keycode: e.Code, Flags: e.Flags, Down: false}))
			}
		}
		return err
	}
	for _, st := range steps {
		switch {
		case st.sas && sas != nil:
			sas()
		case st.sas:
			if err := chord([]uint16{usageLCtrl}); err != nil {
				return err
			}
		case st.wait > 0:
			if !sleep(st.wait) {
				return context.Canceled
			}
		case st.checkLocked:
			if !b.ScreenLocked() {
				return errUnlockNotLocked
			}
		case st.text != "":
			for _, r := range st.text {
				if err := b.InjectText(string(r)); err != nil {
					return err
				}
				if !sleep(keyGap) {
					return context.Canceled
				}
			}
		default:
			if err := chord(st.keys); err != nil {
				return err
			}
		}
	}
	return nil
}

type unlockRequest struct {
	ID         int64                   `json:"id"`
	Credential *proto.SealedCredential `json:"credential"`
}

// unlockGate holds a session's unlock attempts.
type unlockGate struct {
	busy bool
	last time.Time
}

// helloUnlock adds the unlock key to the control hello when the device can
// unlock itself: the browser checks the signature against the device key it
// pinned, then seals credentials to it.
func (m *Manager) helloUnlock(hello map[string]any) {
	if m.unlockSPKI == nil {
		return
	}
	hello["unlock_key"] = base64.StdEncoding.EncodeToString(m.unlockSPKI)
	hello["unlock_key_sig"] = m.unlockSig
}

// onUnlock answers control `unlock` from the browser.
func (s *sess) onUnlock(raw []byte) {
	var req unlockRequest
	_ = json.Unmarshal(raw, &req)
	reply := func(code string) {
		msg := map[string]any{"t": "unlock", "id": req.ID, "ok": code == ""}
		if code != "" {
			msg["code"] = code
		}
		s.media.sendControl(msg)
	}
	m := s.m
	if m.unlockKey == nil {
		reply(unlockUnsupported)
		return
	}
	if req.Credential == nil {
		reply(unlockBadCredential)
		return
	}
	now := time.Now()
	s.mu.Lock()
	code := ""
	switch {
	case s.unlock.busy:
		code = unlockBusy
	case !s.unlock.last.IsZero() && now.Sub(s.unlock.last) < unlockSpacing:
		code = unlockTooSoon
	default:
		s.unlock.busy, s.unlock.last = true, now
	}
	s.mu.Unlock()
	if code != "" {
		reply(code)
		return
	}
	go func() {
		code := s.unlock1(*req.Credential)
		s.mu.Lock()
		s.unlock.busy = false
		s.mu.Unlock()
		if code != "" {
			s.log.Warn("unlock refused", "code", code)
		} else {
			s.log.Info("unlock: password typed on the lock screen")
		}
		reply(code)
	}()
}

func (s *sess) unlock1(sealed proto.SealedCredential) string {
	m := s.m
	if !m.o.Backend.ScreenLocked() {
		return unlockNotLocked
	}
	if !m.o.Pins.PinnedFor(sealed.ClientKey, s.userID) || m.isRevoked(sealed.ClientKey) {
		return unlockUntrusted
	}
	c, err := proto.OpenCredential(m.unlockKey, s.userID, sealed)
	switch {
	case errors.Is(err, proto.ErrCredentialStaleKey):
		return unlockStaleKey
	case err != nil:
		return unlockBadCredential
	}
	err = runUnlock(m.o.Backend, m.o.Keymap, m.o.SecureAttention, unlockSteps(m.o.UnlockOS, c.Password), func(d time.Duration) bool {
		return m.sleep(s.ctx, d)
	})
	switch {
	case errors.Is(err, errUnlockNotLocked):
		return unlockNotLocked
	case err != nil:
		return unlockInputFailed
	}
	return ""
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}
