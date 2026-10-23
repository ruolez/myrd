package app

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"unicode/utf8"

	"github.com/ruolez/remotectl/proto"
	"github.com/ruolez/remotectl/server/internal/store"
)

// ---------- saved sign-in (PROTOCOL §4.2) ----------

func (a *App) getCredential(w http.ResponseWriter, r *http.Request) error {
	d, err := a.ownDevice(r)
	if err != nil {
		return err
	}
	c, err := store.DeviceCredentialFor(r.Context(), a.pool, d.ID, userFrom(r).ID)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"credential": c})
	return nil
}

// checkSealedCredential checks the shape of a sealed credential; only the
// device can tell whether it opens.
func checkSealedCredential(s proto.SealedCredential) error {
	dec := base64.StdEncoding.DecodeString
	ephPub, err1 := dec(s.EphPub)
	iv, err2 := dec(s.IV)
	ct, err3 := dec(s.Ciphertext)
	sig, err4 := dec(s.Sig)
	uk, err5 := hex.DecodeString(s.UnlockKey)
	if errors.Join(err1, err2, err3, err4, err5) != nil || len(ephPub) != 65 || len(iv) != 12 || len(sig) != 64 ||
		len(uk) != 32 || len(ct) <= 16 || len(ct) > proto.MaxCredentialCiphertext {
		return errf(http.StatusBadRequest, "bad_request", "malformed sealed credential")
	}
	return nil
}

func (a *App) putCredential(w http.ResponseWriter, r *http.Request) error {
	d, err := a.ownDevice(r)
	if err != nil {
		return err
	}
	u := userFrom(r)
	var body struct {
		Username string                 `json:"username"`
		Sealed   proto.SealedCredential `json:"sealed"`
	}
	if err := decode(r, &body); err != nil {
		return err
	}
	if !utf8.ValidString(body.Username) || utf8.RuneCountInString(body.Username) > proto.MaxCredentialField {
		return errf(http.StatusBadRequest, "bad_request", "username must be at most 256 characters")
	}
	if err := checkSealedCredential(body.Sealed); err != nil {
		return err
	}
	ctx := r.Context()
	der, _ := base64.StdEncoding.DecodeString(body.Sealed.ClientKey)
	k, err := store.ClientKeyByPublicKey(ctx, a.pool, der)
	if errors.Is(err, store.ErrNotFound) || (err == nil && (k.UserID != u.ID || k.RevokedAt != nil)) {
		return errf(http.StatusBadRequest, "bad_request", "sealed by a browser key this account does not hold")
	}
	if err != nil {
		return err
	}
	sealed, _ := json.Marshal(body.Sealed)
	c, err := store.UpsertDeviceCredential(ctx, a.pool, d.ID, u.ID, store.DeviceCredential{ClientKeyID: k.ID, Username: body.Username, Sealed: sealed})
	if err != nil {
		return err
	}
	a.audit(ctx, store.Audit{Actor: "user", Event: "device.credential.saved", UserID: u.ID, DeviceID: d.ID, IP: a.clientIP(r),
		Meta: map[string]any{"client_key_id": k.ID, "label": k.Label}})
	writeJSON(w, http.StatusOK, map[string]any{"credential": c})
	return nil
}

func (a *App) deleteCredential(w http.ResponseWriter, r *http.Request) error {
	d, err := a.ownDevice(r)
	if err != nil {
		return err
	}
	u := userFrom(r)
	had, err := store.DeleteDeviceCredential(r.Context(), a.pool, d.ID, u.ID)
	if err != nil {
		return err
	}
	if had {
		a.audit(r.Context(), store.Audit{Actor: "user", Event: "device.credential.removed", UserID: u.ID, DeviceID: d.ID, IP: a.clientIP(r)})
	}
	writeJSON(w, http.StatusOK, map[string]any{})
	return nil
}
