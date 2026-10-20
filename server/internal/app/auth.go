package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image/png"
	"net/http"
	"net/mail"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/google/uuid"
	"github.com/pquerna/otp/totp"

	"github.com/ruolez/remotectl/server/internal/security"
	"github.com/ruolez/remotectl/server/internal/store"
)

var errInvalidCreds = errf(http.StatusUnauthorized, "invalid_credentials", "invalid email or password")
var errRateLimited = errf(http.StatusTooManyRequests, "rate_limited", "too many attempts, try again later")

// dummyHash keeps password-check timing similar for unknown emails.
var dummyHash, _ = security.HashPassword("remotectl-dummy-password", security.ArgonParams{Memory: 64 * 1024, Time: 2, Threads: 2})

func normalizeEmail(s string) (string, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	if len(s) > 254 {
		return "", false
	}
	addr, err := mail.ParseAddress(s)
	if err != nil || addr.Address != s {
		return "", false
	}
	return s, true
}

func validPassword(pw string) error {
	if len(pw) < 10 || len(pw) > 1024 {
		return errf(http.StatusBadRequest, "bad_request", "password must be 10 to 1024 characters")
	}
	return nil
}

// completeLogin issues the cookie and records the login.
func (a *App) completeLogin(w http.ResponseWriter, r *http.Request, u *store.User, method string) error {
	ctx := r.Context()
	token := security.NewToken(32)
	ip := a.clientIP(r)
	if _, err := store.CreateLoginSession(ctx, a.pool, u.ID, security.SHA256(token), ip, trunc(r.UserAgent(), 512), loginSessionTTL); err != nil {
		return err
	}
	if err := store.TouchLogin(ctx, a.pool, u.ID); err != nil {
		return err
	}
	a.loginFails.Reset("email:" + u.Email)
	a.audit(ctx, store.Audit{Actor: "user", Event: "user.login", UserID: u.ID, IP: ip,
		Meta: map[string]any{"method": method, "user_agent": trunc(r.UserAgent(), 256)}})
	a.setSessionCookie(w, token)
	fresh, err := store.UserByID(ctx, a.pool, u.ID)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"user": fresh})
	return nil
}

func (a *App) loginFailed(ctx context.Context, ip, email, userID, reason string) {
	a.loginFails.Fail("ip:"+ip, "email:"+email)
	a.audit(ctx, store.Audit{Actor: "user", Event: "user.login_failed", UserID: userID, IP: ip,
		Meta: map[string]any{"email": email, "reason": reason}})
}

func (a *App) signup(w http.ResponseWriter, r *http.Request) error {
	if !a.Settings().SignupAllowed {
		return errf(http.StatusForbidden, "forbidden", "sign-up is disabled")
	}
	if !a.signupLimit.Allow(a.clientIP(r)) {
		return errRateLimited
	}
	var body struct{ Email, Password string }
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
	u, err := store.CreateUser(r.Context(), a.pool, email, hash, false)
	if errors.Is(err, store.ErrConflict) {
		return errf(http.StatusConflict, "conflict", "an account with this email exists")
	}
	if err != nil {
		return err
	}
	a.audit(r.Context(), store.Audit{Actor: "user", Event: "user.created", UserID: u.ID, IP: a.clientIP(r), Meta: map[string]any{"signup": true}})
	return a.completeLogin(w, r, u, "password")
}

func (a *App) login(w http.ResponseWriter, r *http.Request) error {
	var body struct{ Email, Password string }
	if err := decode(r, &body); err != nil {
		return err
	}
	ip := a.clientIP(r)
	email := strings.ToLower(strings.TrimSpace(body.Email))
	if a.loginFails.Blocked("ip:"+ip, "email:"+email) {
		return errRateLimited
	}
	ctx := r.Context()
	u, err := store.UserByEmail(ctx, a.pool, email)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	if u == nil || u.PasswordHash == "" {
		security.VerifyPassword(body.Password, dummyHash)
		uid := ""
		if u != nil {
			uid = u.ID
		}
		a.loginFailed(ctx, ip, email, uid, "bad_credentials")
		return errInvalidCreds
	}
	if !security.VerifyPassword(body.Password, u.PasswordHash) {
		a.loginFailed(ctx, ip, email, u.ID, "bad_credentials")
		return errInvalidCreds
	}
	if u.Disabled {
		a.loginFailed(ctx, ip, email, u.ID, "disabled")
		return errf(http.StatusForbidden, "forbidden", "account disabled")
	}
	if u.TOTPEnabled {
		ticket := security.NewToken(24)
		a.totpTickets.Put(ticket, u.ID)
		writeJSON(w, http.StatusOK, map[string]any{"totp_required": true, "ticket": ticket})
		return nil
	}
	return a.completeLogin(w, r, u, "password")
}

// checkTOTP accepts each code once: its time step is claimed atomically.
func (a *App) checkTOTP(ctx context.Context, u *store.User, code string) bool {
	if u.TOTPSecretEnc == nil {
		return false
	}
	secret, err := security.Open(a.cfg.TOTPEncKey, u.TOTPSecretEnc)
	if err != nil {
		a.log.Error("open totp secret", "user", u.ID, "err", err)
		return false
	}
	step, ok := security.TOTPStep(strings.TrimSpace(code), string(secret), time.Now())
	if !ok {
		return false
	}
	claimed, err := store.ClaimTOTPStep(ctx, a.pool, u.ID, step)
	if err != nil {
		a.log.Error("claim totp step", "user", u.ID, "err", err)
	}
	return claimed
}

func (a *App) loginTOTP(w http.ResponseWriter, r *http.Request) error {
	var body struct{ Ticket, Code string }
	if err := decode(r, &body); err != nil {
		return err
	}
	userID, ok := a.totpTickets.Take(body.Ticket)
	if !ok {
		return errf(http.StatusUnauthorized, "unauthorized", "sign-in expired, start again")
	}
	ctx := r.Context()
	u, err := store.UserByID(ctx, a.pool, userID)
	if err != nil {
		return err
	}
	ip := a.clientIP(r)
	if a.loginFails.Blocked("ip:"+ip, "email:"+u.Email) {
		return errRateLimited
	}
	if u.Disabled {
		return errf(http.StatusForbidden, "forbidden", "account disabled")
	}
	if !a.checkTOTP(ctx, u, body.Code) {
		a.loginFailed(ctx, ip, u.Email, u.ID, "bad_totp")
		a.totpTickets.Put(body.Ticket, userID) // allow a retry within the ticket TTL
		return errf(http.StatusUnauthorized, "invalid_credentials", "invalid code")
	}
	return a.completeLogin(w, r, u, "password+totp")
}

func (a *App) logout(w http.ResponseWriter, r *http.Request) error {
	if c, err := r.Cookie(cookieName); err == nil && c.Value != "" {
		if err := store.DeleteLoginSessionByHash(r.Context(), a.pool, security.SHA256(c.Value)); err != nil {
			return err
		}
	}
	if u := userFrom(r); u != nil {
		a.audit(r.Context(), store.Audit{Actor: "user", Event: "user.logout", UserID: u.ID, IP: a.clientIP(r)})
	}
	a.clearSessionCookie(w)
	writeJSON(w, http.StatusOK, map[string]any{})
	return nil
}

func (a *App) me(w http.ResponseWriter, r *http.Request) error {
	u := userFrom(r)
	if u == nil {
		return errUnauthorized
	}
	writeJSON(w, http.StatusOK, map[string]any{"user": u})
	return nil
}

// ---------- passkeys ----------

type waUser struct {
	u     *store.User
	creds []webauthn.Credential
}

func (w waUser) WebAuthnID() []byte {
	id := uuid.MustParse(w.u.ID)
	return id[:]
}
func (w waUser) WebAuthnName() string                       { return w.u.Email }
func (w waUser) WebAuthnDisplayName() string                { return w.u.Email }
func (w waUser) WebAuthnCredentials() []webauthn.Credential { return w.creds }

func (a *App) loadWAUser(ctx context.Context, u *store.User) (waUser, []store.Passkey, error) {
	pks, err := store.PasskeysByUser(ctx, a.pool, u.ID)
	if err != nil {
		return waUser{}, nil, err
	}
	wu := waUser{u: u}
	for _, p := range pks {
		wu.creds = append(wu.creds, p.Credential)
	}
	return wu, pks, nil
}

func (a *App) passkeyLoginBegin(w http.ResponseWriter, r *http.Request) error {
	if a.loginFails.Blocked("ip:" + a.clientIP(r)) {
		return errRateLimited
	}
	assertion, session, err := a.wa.BeginDiscoverableLogin(webauthn.WithUserVerification(protocol.VerificationPreferred))
	if err != nil {
		return err
	}
	ticket := security.NewToken(24)
	a.ceremonies.Put("login:"+ticket, session)
	writeJSON(w, http.StatusOK, map[string]any{"ticket": ticket, "publicKey": assertion.Response})
	return nil
}

func (a *App) passkeyLoginFinish(w http.ResponseWriter, r *http.Request) error {
	var body struct {
		Ticket     string          `json:"ticket"`
		Credential json.RawMessage `json:"credential"`
	}
	if err := decode(r, &body); err != nil {
		return err
	}
	ctx := r.Context()
	ip := a.clientIP(r)
	if a.loginFails.Blocked("ip:" + ip) {
		return errRateLimited
	}
	session, ok := a.ceremonies.Take("login:" + body.Ticket)
	if !ok {
		return errf(http.StatusUnauthorized, "unauthorized", "sign-in expired, start again")
	}
	parsed, err := protocol.ParseCredentialRequestResponseBytes(body.Credential)
	if err != nil {
		a.loginFails.Fail("ip:" + ip)
		return errf(http.StatusBadRequest, "bad_request", "invalid passkey response")
	}
	var found *store.Passkey
	handler := func(rawID, userHandle []byte) (webauthn.User, error) {
		pk, err := store.PasskeyByCredentialID(ctx, a.pool, rawID)
		if err != nil {
			return nil, err
		}
		uid, err := uuid.FromBytes(userHandle)
		if err != nil || uid.String() != pk.UserID {
			return nil, errors.New("user handle mismatch")
		}
		u, err := store.UserByID(ctx, a.pool, pk.UserID)
		if err != nil {
			return nil, err
		}
		found = pk
		wu, _, err := a.loadWAUser(ctx, u)
		return wu, err
	}
	wu, cred, err := a.wa.ValidatePasskeyLogin(handler, *session, parsed)
	if err != nil || found == nil {
		a.loginFails.Fail("ip:" + ip)
		a.audit(ctx, store.Audit{Actor: "user", Event: "user.login_failed", IP: ip, Meta: map[string]any{"reason": "bad_passkey"}})
		return errf(http.StatusUnauthorized, "invalid_credentials", "passkey not recognized")
	}
	u := wu.(waUser).u
	if cred.Authenticator.CloneWarning {
		a.audit(ctx, store.Audit{Actor: "system", Event: "passkey.clone_warning", UserID: u.ID, IP: ip,
			Meta: map[string]any{"passkey_id": found.ID, "passkey_name": found.Name}})
		if err := store.DeleteUserLoginSessions(ctx, a.pool, u.ID); err != nil {
			return err
		}
		a.hub.EndUserActivity(ctx, u.ID, "admin_terminated")
		return errf(http.StatusUnauthorized, "invalid_credentials", "passkey sign counter went backwards; possible cloned authenticator. All sessions were revoked.")
	}
	if err := store.UpdatePasskeyUse(ctx, a.pool, found.ID, cred); err != nil {
		return err
	}
	if u.Disabled {
		return errf(http.StatusForbidden, "forbidden", "account disabled")
	}
	return a.completeLogin(w, r, u, "passkey")
}

func (a *App) listPasskeys(w http.ResponseWriter, r *http.Request) error {
	pks, err := store.PasskeysByUser(r.Context(), a.pool, userFrom(r).ID)
	if err != nil {
		return err
	}
	for i := range pks {
		if pks[i].Transports == nil {
			pks[i].Transports = []string{}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": pks})
	return nil
}

func (a *App) passkeyRegisterBegin(w http.ResponseWriter, r *http.Request) error {
	u := userFrom(r)
	wu, _, err := a.loadWAUser(r.Context(), u)
	if err != nil {
		return err
	}
	excl := make([]protocol.CredentialDescriptor, 0, len(wu.creds))
	for i := range wu.creds {
		excl = append(excl, wu.creds[i].Descriptor())
	}
	creation, session, err := a.wa.BeginRegistration(wu,
		webauthn.WithResidentKeyRequirement(protocol.ResidentKeyRequirementRequired),
		webauthn.WithAuthenticatorSelection(protocol.AuthenticatorSelection{
			ResidentKey: protocol.ResidentKeyRequirementRequired, UserVerification: protocol.VerificationPreferred,
		}),
		webauthn.WithExclusions(excl))
	if err != nil {
		return err
	}
	ticket := security.NewToken(24)
	a.ceremonies.Put("reg:"+u.ID+":"+ticket, session)
	writeJSON(w, http.StatusOK, map[string]any{"ticket": ticket, "publicKey": creation.Response})
	return nil
}

func (a *App) passkeyRegisterFinish(w http.ResponseWriter, r *http.Request) error {
	u := userFrom(r)
	var body struct {
		Ticket     string          `json:"ticket"`
		Name       string          `json:"name"`
		Credential json.RawMessage `json:"credential"`
	}
	if err := decode(r, &body); err != nil {
		return err
	}
	session, ok := a.ceremonies.Take("reg:" + u.ID + ":" + body.Ticket)
	if !ok {
		return errf(http.StatusBadRequest, "bad_request", "registration expired, start again")
	}
	parsed, err := protocol.ParseCredentialCreationResponseBytes(body.Credential)
	if err != nil {
		return errf(http.StatusBadRequest, "bad_request", "invalid passkey response")
	}
	ctx := r.Context()
	wu, _, err := a.loadWAUser(ctx, u)
	if err != nil {
		return err
	}
	cred, err := a.wa.CreateCredential(wu, *session, parsed)
	if err != nil {
		return errf(http.StatusBadRequest, "bad_request", "passkey verification failed")
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		name = "Passkey"
	}
	pk, err := store.CreatePasskey(ctx, a.pool, u.ID, trunc(name, 64), cred)
	if err != nil {
		return err
	}
	if pk.Transports == nil {
		pk.Transports = []string{}
	}
	a.audit(ctx, store.Audit{Actor: "user", Event: "passkey.added", UserID: u.ID, IP: a.clientIP(r), Meta: map[string]any{"passkey_id": pk.ID, "name": pk.Name}})
	writeJSON(w, http.StatusOK, map[string]any{"passkey": pk})
	return nil
}

func (a *App) deletePasskey(w http.ResponseWriter, r *http.Request) error {
	u := userFrom(r)
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	if !u.HasPassword && u.PasskeyCount <= 1 {
		return errf(http.StatusConflict, "conflict", "set a password before removing your last passkey")
	}
	if err := store.DeletePasskey(r.Context(), a.pool, u.ID, id); err != nil {
		return err
	}
	a.audit(r.Context(), store.Audit{Actor: "user", Event: "passkey.removed", UserID: u.ID, IP: a.clientIP(r), Meta: map[string]any{"passkey_id": id}})
	writeJSON(w, http.StatusOK, map[string]any{})
	return nil
}

// ---------- password + TOTP ----------

func (a *App) changePassword(w http.ResponseWriter, r *http.Request) error {
	u := userFrom(r)
	var body struct {
		Current *string `json:"current_password"`
		New     string  `json:"new_password"`
	}
	if err := decode(r, &body); err != nil {
		return err
	}
	if err := validPassword(body.New); err != nil {
		return err
	}
	if u.HasPassword {
		key := "pwchange:" + u.ID
		if a.loginFails.Blocked(key) {
			return errRateLimited
		}
		if body.Current == nil || !security.VerifyPassword(*body.Current, u.PasswordHash) {
			a.loginFails.Fail(key)
			return errf(http.StatusUnauthorized, "invalid_credentials", "current password is wrong")
		}
	}
	hash, err := security.HashPassword(body.New, a.argon)
	if err != nil {
		return err
	}
	ctx := r.Context()
	if err := store.SetPassword(ctx, a.pool, u.ID, hash); err != nil {
		return err
	}
	// Sign out every other browser.
	if cur := loginSessionFrom(r); cur != nil {
		sessions, err := store.ListLoginSessions(ctx, a.pool, u.ID)
		if err != nil {
			return err
		}
		for _, s := range sessions {
			if s.ID != cur.ID {
				_ = store.DeleteLoginSession(ctx, a.pool, u.ID, s.ID)
			}
		}
	}
	a.audit(ctx, store.Audit{Actor: "user", Event: "user.password_changed", UserID: u.ID, IP: a.clientIP(r)})
	writeJSON(w, http.StatusOK, map[string]any{})
	return nil
}

func (a *App) totpSetup(w http.ResponseWriter, r *http.Request) error {
	u := userFrom(r)
	if u.TOTPEnabled {
		return errf(http.StatusConflict, "conflict", "two-factor is already enabled")
	}
	key, err := totp.Generate(totp.GenerateOpts{Issuer: "my RemoteDesktop", AccountName: u.Email})
	if err != nil {
		return err
	}
	sealed, err := security.Seal(a.cfg.TOTPEncKey, []byte(key.Secret()))
	if err != nil {
		return err
	}
	if err := store.SetTOTPPending(r.Context(), a.pool, u.ID, sealed); err != nil {
		return err
	}
	img, err := key.Image(240, 240)
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"secret": key.Secret(), "otpauth_url": key.URL(),
		"qr_png": "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes()),
	})
	return nil
}

func (a *App) totpEnable(w http.ResponseWriter, r *http.Request) error {
	u := userFrom(r)
	var body struct{ Code string }
	if err := decode(r, &body); err != nil {
		return err
	}
	if u.TOTPEnabled {
		return errf(http.StatusConflict, "conflict", "two-factor is already enabled")
	}
	if u.TOTPPending == nil {
		return errf(http.StatusBadRequest, "bad_request", "run setup first")
	}
	secret, err := security.Open(a.cfg.TOTPEncKey, u.TOTPPending)
	if err != nil {
		return err
	}
	if !totp.Validate(strings.TrimSpace(body.Code), string(secret)) {
		return errf(http.StatusBadRequest, "invalid_credentials", "invalid code")
	}
	if err := store.EnableTOTP(r.Context(), a.pool, u.ID, u.TOTPPending); err != nil {
		return err
	}
	a.audit(r.Context(), store.Audit{Actor: "user", Event: "user.totp_enabled", UserID: u.ID, IP: a.clientIP(r)})
	writeJSON(w, http.StatusOK, map[string]any{})
	return nil
}

func (a *App) totpDisable(w http.ResponseWriter, r *http.Request) error {
	u := userFrom(r)
	var body struct{ Code string }
	if err := decode(r, &body); err != nil {
		return err
	}
	if !u.TOTPEnabled {
		return errf(http.StatusBadRequest, "bad_request", "two-factor is not enabled")
	}
	if !a.checkTOTP(r.Context(), u, body.Code) {
		return errf(http.StatusBadRequest, "invalid_credentials", "invalid code")
	}
	if err := store.DisableTOTP(r.Context(), a.pool, u.ID); err != nil {
		return err
	}
	a.audit(r.Context(), store.Audit{Actor: "user", Event: "user.totp_disabled", UserID: u.ID, IP: a.clientIP(r)})
	writeJSON(w, http.StatusOK, map[string]any{})
	return nil
}

func (a *App) listLoginSessions(w http.ResponseWriter, r *http.Request) error {
	ss, err := store.ListLoginSessions(r.Context(), a.pool, userFrom(r).ID)
	if err != nil {
		return err
	}
	if cur := loginSessionFrom(r); cur != nil {
		for i := range ss {
			ss[i].Current = ss[i].ID == cur.ID
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": ss})
	return nil
}

func (a *App) deleteLoginSession(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	if err := store.DeleteLoginSession(r.Context(), a.pool, userFrom(r).ID, id); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{})
	return nil
}
