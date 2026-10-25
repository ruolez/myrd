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
	unlockNoPrompt      = "no_prompt"
)

const (
	usageA      = 0x04
	usageEnter  = 0x28
	usageLShift = 0xE1
	usageLGUI   = 0xE3
)

// unlockPlan is how a password reaches the unlock screen of one OS. Typing
// starts as soon as the backend reports the password field (UnlockPrompt),
// never into a lock curtain: a half-typed password plus Enter counts as a
// failed sign-in.
type unlockPlan struct {
	// sas raises the Windows sign-in box (Ctrl+Alt+Delete); raise is the
	// chord tapped instead elsewhere (Shift wakes the macOS password field).
	sas   bool
	raise []uint16
	// alwaysRaise: a ready prompt does not mean a focused password field
	// (macOS only knows that the display is awake).
	alwaysRaise bool
	// selectAll replaces a half-typed password instead of extending it.
	selectAll []uint16
	// click: the field still missing this long after the raise, click the
	// middle of the primary screen (lifts the Windows lock curtain when
	// Ctrl+Alt+Delete did not). 0 never clicks.
	click time.Duration
	// settle: from the raise, or the field appearing, to the first key.
	settle time.Duration
	// blind: when the backend cannot tell, typing starts this long after the
	// last raise or click.
	blind time.Duration
}

func unlockPlanFor(goos string) unlockPlan {
	switch goos {
	case "windows":
		return unlockPlan{sas: true, selectAll: []uint16{usageLCtrl, usageA}, click: 700 * time.Millisecond,
			settle: 100 * time.Millisecond, blind: 2 * time.Second}
	case "darwin":
		return unlockPlan{raise: []uint16{usageLShift}, alwaysRaise: true, selectAll: []uint16{usageLGUI, usageA},
			settle: 300 * time.Millisecond, blind: time.Second}
	}
	return unlockPlan{selectAll: []uint16{usageLCtrl, usageA}}
}

const (
	// promptPoll is how often the password field is looked for.
	promptPoll = 100 * time.Millisecond
	// promptWait is how long it may take to appear.
	promptWait = 6 * time.Second
	// raiseFresh: a raise this recent (the session opening) is not repeated;
	// a second Ctrl+Alt+Delete could close what the first one opened.
	raiseFresh = 3 * time.Second
)

var (
	errUnlockNotLocked = errors.New("the screen is not locked")
	errUnlockNoPrompt  = errors.New("the password field did not appear")
)

// keyGap paces typed characters: password fields drop keys sent in a burst.
const keyGap = 20 * time.Millisecond

// unlocker types a password on the lock screen. sas raises the sign-in box
// on Windows; nil taps Ctrl instead. sleep returns false when the session
// ends.
type unlocker struct {
	b     platform.Backend
	km    keymap.Table
	sas   func()
	sleep func(time.Duration) bool
	now   func() time.Time
}

// unlockTrace is how an unlock went, for the log.
type unlockTrace struct {
	raisedAt time.Time // the last raise, this unlock's or the session opening's
	raised   bool      // this unlock raised the field
	clicked  bool
	prompt   platform.UnlockPrompt // last seen
	focus    string                // what had the keyboard focus last (Windows)
	waited   time.Duration         // from the request to the first key
}

func (u *unlocker) chord(usages []uint16) error {
	var err error
	for _, x := range usages {
		if e, ok := u.km[x]; ok {
			err = errors.Join(err, u.b.Inject(platform.InputEvent{Type: platform.EvKey, Keycode: e.Code, Flags: e.Flags, Down: true}))
		}
	}
	for i := len(usages) - 1; i >= 0; i-- {
		if e, ok := u.km[usages[i]]; ok {
			err = errors.Join(err, u.b.Inject(platform.InputEvent{Type: platform.EvKey, Keycode: e.Code, Flags: e.Flags, Down: false}))
		}
	}
	return err
}

// raise brings up the password field.
func (u *unlocker) raise(p unlockPlan) error {
	switch {
	case p.sas && u.sas != nil:
		u.sas()
		return nil
	case p.sas:
		return u.chord([]uint16{usageLCtrl})
	}
	return u.chord(p.raise)
}

// clickCenter clicks the middle of the primary monitor.
func (u *unlocker) clickCenter() error {
	mons, err := u.b.Monitors()
	if err != nil || len(mons) == 0 {
		return err
	}
	id := mons[0].ID
	for _, m := range mons {
		if m.Primary {
			id = m.ID
		}
	}
	return errors.Join(
		u.b.Inject(platform.InputEvent{Type: platform.EvMouseMove, MonitorID: id, X: 32768, Y: 32768}),
		u.b.Inject(platform.InputEvent{Type: platform.EvMouseButton, Button: platform.BtnLeft, Down: true}),
		u.b.Inject(platform.InputEvent{Type: platform.EvMouseButton, Button: platform.BtnLeft, Down: false}))
}

func (u *unlocker) sleepUntil(t time.Time) bool {
	if d := t.Sub(u.now()); d > 0 {
		return u.sleep(d)
	}
	return true
}

// awaitPrompt raises the password field unless it is up or was raised at
// raisedAt, and returns once it takes keys.
func (u *unlocker) awaitPrompt(p unlockPlan, raisedAt time.Time, tr *unlockTrace) error {
	start := u.now()
	prev, _ := u.b.UnlockPrompt()
	tr.raisedAt = raisedAt
	if (prev != platform.PromptReady || p.alwaysRaise) && (raisedAt.IsZero() || start.Sub(raisedAt) > raiseFresh) {
		if err := u.raise(p); err != nil {
			return err
		}
		tr.raisedAt, tr.raised = u.now(), true
	}
	if tr.raisedAt.IsZero() {
		tr.raisedAt = start
	}
	last := tr.raisedAt // the last raise or click
	for {
		if !u.b.ScreenLocked() {
			return errUnlockNotLocked
		}
		now := u.now()
		state, focus := u.b.UnlockPrompt()
		tr.prompt, tr.focus = state, focus
		switch {
		case state == platform.PromptReady:
			at := last.Add(p.settle)
			if prev != platform.PromptReady && now.Add(p.settle).After(at) {
				at = now.Add(p.settle)
			}
			if !u.sleepUntil(at) {
				return context.Canceled
			}
			return nil
		case state == platform.PromptUnknown && !now.Before(last.Add(p.blind)):
			return nil
		case now.Sub(start) >= promptWait:
			return errUnlockNoPrompt
		}
		prev = state
		if p.click > 0 && !tr.clicked && now.Sub(tr.raisedAt) >= p.click {
			if err := u.clickCenter(); err != nil {
				return err
			}
			tr.clicked, last = true, u.now()
		}
		if !u.sleep(promptPoll) {
			return context.Canceled
		}
	}
}

// run types password on the lock screen once its field is up.
func (u *unlocker) run(p unlockPlan, password string, raisedAt time.Time) (unlockTrace, error) {
	var tr unlockTrace
	start := u.now()
	if err := u.awaitPrompt(p, raisedAt, &tr); err != nil {
		return tr, err
	}
	if !u.b.ScreenLocked() {
		return tr, errUnlockNotLocked
	}
	tr.waited = u.now().Sub(start)
	if err := u.chord(p.selectAll); err != nil {
		return tr, err
	}
	for _, r := range password {
		if err := u.b.InjectText(string(r)); err != nil {
			return tr, err
		}
		if !u.sleep(keyGap) {
			return tr, context.Canceled
		}
	}
	return tr, u.chord([]uint16{usageEnter})
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
		} else if s.media != nil {
			go s.media.watchUnlock(s.ctx)
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
	m.signIn.Lock()
	u := m.unlocker(s.ctx)
	tr, err := u.run(unlockPlanFor(m.o.UnlockOS), c.Password, m.raisedAt)
	if tr.raised {
		m.raisedAt = tr.raisedAt
	}
	m.signIn.Unlock()
	log := s.log.With("waited_ms", tr.waited.Milliseconds(), "raised", tr.raised, "clicked", tr.clicked,
		"prompt", tr.prompt.String(), "focus", tr.focus)
	switch {
	case errors.Is(err, errUnlockNotLocked):
		return unlockNotLocked
	case errors.Is(err, errUnlockNoPrompt):
		log.Warn("unlock: the password field did not appear, nothing typed")
		return unlockNoPrompt
	case err != nil:
		log.Warn("unlock: input failed", "err", err)
		return unlockInputFailed
	}
	log.Info("unlock: password typed on the lock screen")
	return ""
}

func (m *Manager) unlocker(ctx context.Context) *unlocker {
	return &unlocker{b: m.o.Backend, km: m.o.Keymap, sas: m.o.SecureAttention, now: m.now,
		sleep: func(d time.Duration) bool { return m.sleep(ctx, d) }}
}

// raiseSignIn brings up the password field as soon as a session opens on
// the lock screen, so it is up by the time the browser sends the saved
// password (and for typing it by hand).
func (m *Manager) raiseSignIn() {
	if m.unlockKey == nil || !m.o.Backend.ScreenLocked() {
		return
	}
	p := unlockPlanFor(m.o.UnlockOS)
	m.signIn.Lock()
	defer m.signIn.Unlock()
	if state, _ := m.o.Backend.UnlockPrompt(); state == platform.PromptReady && !p.alwaysRaise {
		return
	}
	if !m.raisedAt.IsZero() && m.now().Sub(m.raisedAt) <= raiseFresh {
		return
	}
	if err := m.unlocker(context.Background()).raise(p); err != nil {
		m.o.Log.Warn("unlock: raising the password field failed", "err", err)
		return
	}
	m.raisedAt = m.now()
	m.o.Log.Info("unlock: raised the password field for the new session")
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
