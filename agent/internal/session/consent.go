package session

import (
	"context"
	"errors"
	"fmt"

	"github.com/ruolez/remotectl/agent/internal/platform"
)

// ConsentRequest describes a pending session for an on-device prompt.
type ConsentRequest struct {
	SessionID string
	Title     string
	Message   string
	Label     string // browser client key label
	Email     string
	FirstSeen string // client key created_at
	NewKey    bool   // true = untrusted browser (offer "Allow and remember")
}

// Prompter asks the owner on the device's own screen. The Linux demo device
// implements it in internal/devconsole; macOS/Windows use the native
// Backend.ConsentPrompt and only fall back to a Prompter when that returns
// platform.ErrUnsupported.
type Prompter interface {
	Prompt(ctx context.Context, req ConsentRequest) (platform.Consent, error)
}

type consentMode int

const (
	consentAuto   consentMode = iota // trusted + auto_accept: no prompt
	consentLight                     // trusted, auto_accept off: "Allow session from <label>?"
	consentNewKey                    // untrusted browser: full TOFU prompt
)

// decideConsent: trusted is pins.Store.Trusted (pinned key or remembered account).
func decideConsent(trusted, autoAccept bool) consentMode {
	switch {
	case trusted && autoAccept:
		return consentAuto
	case trusted:
		return consentLight
	default:
		return consentNewKey
	}
}

func consentText(mode consentMode, label, email, firstSeen string) (title, message string) {
	if label == "" {
		label = "unnamed browser"
	}
	if mode == consentNewKey {
		return "Allow this new browser?",
			fmt.Sprintf("Allow this new browser: %s, first seen %s? (user %s)\n\n"+
				"Allow and remember also lets every other browser signed in as %s connect without asking.", label, firstSeen, email, email)
	}
	return "Remote session request", fmt.Sprintf("Allow session from %s? (%s)", label, email)
}

// askConsent tries the native prompt, then the fallback prompter.
func askConsent(ctx context.Context, b platform.Backend, fallback Prompter, req ConsentRequest, timeoutSec int) (platform.Consent, error) {
	type result struct {
		c   platform.Consent
		err error
	}
	ch := make(chan result, 1)
	go func() {
		c, err := b.ConsentPrompt(req.Title, req.Message, timeoutSec)
		ch <- result{c, err}
	}()
	select {
	case <-ctx.Done():
		return platform.ConsentDeny, ctx.Err()
	case r := <-ch:
		if !errors.Is(r.err, platform.ErrUnsupported) {
			return r.c, r.err
		}
	}
	if fallback == nil {
		return platform.ConsentDeny, errors.New("no consent prompter available")
	}
	return fallback.Prompt(ctx, req)
}
