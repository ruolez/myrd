package proto

// Saved sign-in credentials (PROTOCOL §4.2): a browser seals a username and
// password to a device's unlock key and signs the result with its client key.
// The server stores the sealed credential for the account; only the device
// can open it, and it types the password only on its own lock screen.

import (
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/hkdf"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
)

const (
	FeatureUnlock = "unlock"

	// MaxCredentialField bounds the username and the password (characters).
	MaxCredentialField = 256
	// MaxCredentialCiphertext keeps a sealed credential small (JSON of two
	// fields at their limit, 4 bytes per character, plus the tag).
	MaxCredentialCiphertext = 2*MaxCredentialField*4 + 64
)

// Errors from OpenCredential; the agent reports them as unlock codes.
var (
	ErrCredentialMalformed = errors.New("malformed credential")
	ErrCredentialStaleKey  = errors.New("credential sealed to another unlock key")
	ErrCredentialSignature = errors.New("credential signature mismatch")
	ErrCredentialDecrypt   = errors.New("credential does not decrypt")
)

// SealedCredential is what the browser stores on the server and later hands
// to the device in a session (control `unlock`).
type SealedCredential struct {
	ClientKey  string `json:"client_key"` // base64 SPKI DER of the signing client key
	UnlockKey  string `json:"unlock_key"` // SPKIHash of the device unlock key it was sealed to
	EphPub     string `json:"eph_pub"`    // base64, 65-byte uncompressed P-256 point
	IV         string `json:"iv"`         // base64, 12 bytes
	Ciphertext string `json:"ciphertext"` // base64, AES-256-GCM ciphertext ‖ tag
	Sig        string `json:"sig"`        // base64 P1363 ECDSA by ClientKey
}

// Credential is the sealed plaintext.
type Credential struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// DeriveUnlockKey derives the device's ECDH P-256 unlock key from its
// Ed25519 key, so it needs no storage of its own and changes with the device
// identity.
func DeriveUnlockKey(devKey ed25519.PrivateKey) (*ecdh.PrivateKey, error) {
	for i := 0; i < 16; i++ {
		info := "remotectl-unlock-key-v1"
		if i > 0 {
			info += "\n" + strconv.Itoa(i)
		}
		d, err := hkdf.Key(sha256.New, devKey.Seed(), nil, info, 32)
		if err != nil {
			return nil, err
		}
		if k, err := ecdh.P256().NewPrivateKey(d); err == nil {
			return k, nil
		}
	}
	return nil, errors.New("no valid unlock key")
}

// UnlockKeySPKI is the DER SPKI of an unlock key, as browsers import it.
func UnlockKeySPKI(k *ecdh.PrivateKey) ([]byte, error) {
	return x509.MarshalPKIXPublicKey(k.PublicKey())
}

// UnlockKeyMessage is what the device key signs to vouch for its unlock key:
//
//	"remotectl-unlock-key-v1\n" + uk_sha
func UnlockKeyMessage(ukSHA string) []byte {
	return []byte("remotectl-unlock-key-v1\n" + ukSHA)
}

// CredentialHeader is the AES-GCM additional data and the start of the
// signed message, one value per line (no trailing newline):
//
//	remotectl-unlock-v1, user_id, ck_sha, uk_sha, base64(eph_pub), base64(iv)
func CredentialHeader(userID, ckSHA, ukSHA string, ephPub, iv []byte) []byte {
	return []byte(strings.Join([]string{
		"remotectl-unlock-v1", strings.ToLower(userID), ckSHA, ukSHA,
		base64.StdEncoding.EncodeToString(ephPub), base64.StdEncoding.EncodeToString(iv),
	}, "\n"))
}

// credentialKey derives the AES-256 key, bound to the user and both keys.
func credentialKey(shared, ephPub []byte, userID, ckSHA, ukSHA string) ([]byte, error) {
	info := "remotectl-unlock-v1\n" + strings.ToLower(userID) + "\n" + ckSHA + "\n" + ukSHA
	return hkdf.Key(sha256.New, shared, ephPub, info, 32)
}

// credentialSigMessage is what the client key signs: header + "\n" + hex(sha256(ciphertext)).
func credentialSigMessage(header, ct []byte) []byte {
	h := sha256.Sum256(ct)
	return append(append(append([]byte{}, header...), '\n'), hex.EncodeToString(h[:])...)
}

// SealCredential is the browser's half, used by tests and test vectors.
// sign returns the P1363 signature of a message with the client key.
func SealCredential(rnd io.Reader, unlockSPKI, clientSPKI []byte, userID string, c Credential, sign func([]byte) ([]byte, error)) (*SealedCredential, error) {
	eph, err := ecdh.P256().GenerateKey(rnd)
	if err != nil {
		return nil, err
	}
	iv := make([]byte, 12)
	if _, err := io.ReadFull(rnd, iv); err != nil {
		return nil, err
	}
	pt, err := json.Marshal(c)
	if err != nil {
		return nil, err
	}
	return sealCredential(eph, iv, unlockSPKI, clientSPKI, userID, pt, sign)
}

func sealCredential(eph *ecdh.PrivateKey, iv, unlockSPKI, clientSPKI []byte, userID string, pt []byte, sign func([]byte) ([]byte, error)) (*SealedCredential, error) {
	pub, err := ParsePreviewKey(unlockSPKI)
	if err != nil {
		return nil, err
	}
	shared, err := eph.ECDH(pub)
	if err != nil {
		return nil, err
	}
	ephPub := eph.PublicKey().Bytes()
	ckSHA, ukSHA := SPKIHash(clientSPKI), SPKIHash(unlockSPKI)
	key, err := credentialKey(shared, ephPub, userID, ckSHA, ukSHA)
	if err != nil {
		return nil, err
	}
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	header := CredentialHeader(userID, ckSHA, ukSHA, ephPub, iv)
	ct := gcm.Seal(nil, iv, pt, header)
	sig, err := sign(credentialSigMessage(header, ct))
	if err != nil {
		return nil, err
	}
	b64 := base64.StdEncoding.EncodeToString
	return &SealedCredential{ClientKey: b64(clientSPKI), UnlockKey: ukSHA, EphPub: b64(ephPub), IV: b64(iv),
		Ciphertext: b64(ct), Sig: b64(sig)}, nil
}

// OpenCredential checks that s was sealed to priv for userID and signed by
// its client key, then decrypts it. Whether that client key may unlock the
// device is the caller's decision.
func OpenCredential(priv *ecdh.PrivateKey, userID string, s SealedCredential) (Credential, error) {
	var c Credential
	spki, err := UnlockKeySPKI(priv)
	if err != nil {
		return c, err
	}
	ukSHA := SPKIHash(spki)
	if s.UnlockKey != ukSHA {
		return c, ErrCredentialStaleKey
	}
	dec := base64.StdEncoding.DecodeString
	clientSPKI, err1 := dec(s.ClientKey)
	ephPub, err2 := dec(s.EphPub)
	iv, err3 := dec(s.IV)
	ct, err4 := dec(s.Ciphertext)
	if err := errors.Join(err1, err2, err3, err4); err != nil || len(ephPub) != 65 || len(iv) != 12 ||
		len(ct) < 16 || len(ct) > MaxCredentialCiphertext {
		return c, ErrCredentialMalformed
	}
	ckSHA := SPKIHash(clientSPKI)
	header := CredentialHeader(userID, ckSHA, ukSHA, ephPub, iv)
	if VerifyBrowserSig(s.ClientKey, s.Sig, credentialSigMessage(header, ct)) != nil {
		return c, ErrCredentialSignature
	}
	eph, err := ecdh.P256().NewPublicKey(ephPub)
	if err != nil {
		return c, ErrCredentialMalformed
	}
	shared, err := priv.ECDH(eph)
	if err != nil {
		return c, ErrCredentialMalformed
	}
	key, err := credentialKey(shared, ephPub, userID, ckSHA, ukSHA)
	if err != nil {
		return c, err
	}
	gcm, err := newGCM(key)
	if err != nil {
		return c, err
	}
	pt, err := gcm.Open(nil, iv, ct, header)
	if err != nil {
		return c, ErrCredentialDecrypt
	}
	if json.Unmarshal(pt, &c) != nil || c.Password == "" ||
		len([]rune(c.Username)) > MaxCredentialField || len([]rune(c.Password)) > MaxCredentialField {
		return Credential{}, ErrCredentialMalformed
	}
	return c, nil
}
