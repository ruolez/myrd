package proto

// Screen previews (PROTOCOL §3 "Screen previews", §4.1): small JPEG
// snapshots the agent encrypts to one browser's preview key and signs with
// its device key. The server relays and caches them but can neither read
// nor forge them, nor pass one device's preview off as another's or an old
// one as new.

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/hkdf"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"strconv"
	"strings"
)

const (
	TypePreviewWatch       = "preview.watch"       // B→S
	TypePreviewRequest     = "preview.request"     // S→A
	TypePreview            = "preview"             // A→S→B
	TypePreviewUnavailable = "preview.unavailable" // A→S→B

	FeaturePreview = "preview"

	PreviewMime = "image/jpeg"
	// MaxPreviewCiphertext keeps a preview message (base64 plus fields)
	// under MaxMessageBytes.
	MaxPreviewCiphertext = 46080
	MaxPreviewRecipients = 5
	MaxPreviewWatch      = 100
)

// Reasons a preview is not available (preview.unavailable.reason).
const (
	PreviewOff           = "previews_off"   // the device's own switch
	PreviewPaused        = "paused"         // remote access paused on the device
	PreviewUpdating      = "updating"       // the agent is installing an update
	PreviewUntrusted     = "untrusted"      // this browser key is not pinned on the device
	PreviewBadKey        = "bad_key"        // the preview key's signature does not verify
	PreviewAskEveryTime  = "ask_every_time" // the owner wants a prompt for every session
	PreviewNoPermission  = "no_permission"  // no screen recording permission
	PreviewInSession     = "in_session"     // a session is capturing the screen
	PreviewLocked        = "locked"         // lock screen or secure desktop
	PreviewDisplayOff    = "display_off"    // the display sleeps
	PreviewCaptureFailed = "capture_failed"
	PreviewUnsupported   = "unsupported"
	PreviewNoConsole     = "no_console" // Windows: no session helper running
	PreviewTooSoon       = "too_soon"   // asked again within 30 s; retry_after_s says when (not shown)
	PreviewDisabled      = "disabled"   // server: admin or owner turned previews off
	PreviewOffline       = "offline"    // server
	PreviewAgentOutdated = "agent_outdated"
)

type PreviewWatch struct {
	V           int      `json:"v"`
	Type        string   `json:"type"`
	ClientKeyID string   `json:"client_key_id"`
	DeviceIDs   []string `json:"device_ids"`
}

type PreviewRecipient struct {
	ClientKey     ClientKeyInfo `json:"client_key"`
	PreviewKey    string        `json:"preview_key"`     // base64 SPKI DER, ECDH P-256
	PreviewKeySig string        `json:"preview_key_sig"` // P1363 by the client key over PreviewKeyMessage
	User          UserInfo      `json:"user"`
}

type PreviewRequest struct {
	V          int                `json:"v"`
	Type       string             `json:"type"`
	Req        string             `json:"req"`
	Recipients []PreviewRecipient `json:"recipients"`
}

type Preview struct {
	V            int    `json:"v"`
	Type         string `json:"type"`
	Req          string `json:"req,omitempty"`
	DeviceID     string `json:"device_id,omitempty"` // added by the server
	ClientKeyID  string `json:"client_key_id"`
	CapturedAtMS int64  `json:"captured_at_ms"`
	MonitorID    uint32 `json:"monitor_id"`
	Width        int    `json:"width"`
	Height       int    `json:"height"`
	Mime         string `json:"mime"`
	EphPub       string `json:"eph_pub"`    // base64, 65-byte uncompressed P-256 point
	IV           string `json:"iv"`         // base64, 12 bytes
	Ciphertext   string `json:"ciphertext"` // base64, AES-256-GCM ciphertext ‖ tag
	Sig          string `json:"sig"`        // base64 Ed25519 by the device key
	ReceivedAt   string `json:"received_at,omitempty"`
}

type PreviewUnavailable struct {
	V           int    `json:"v"`
	Type        string `json:"type"`
	Req         string `json:"req,omitempty"`
	DeviceID    string `json:"device_id,omitempty"`     // added by the server
	ClientKeyID string `json:"client_key_id,omitempty"` // empty: every recipient
	Reason      string `json:"reason"`
	RetryAfterS int    `json:"retry_after_s,omitempty"`
	At          string `json:"at,omitempty"`
}

// SPKIHash is the lowercase hex SHA-256 of a DER public key.
func SPKIHash(der []byte) string {
	h := sha256.Sum256(der)
	return hex.EncodeToString(h[:])
}

// PreviewKeyMessage is what a browser's client key signs to vouch for its
// preview key:
//
//	"remotectl-preview-key-v1\n" + ck_sha + "\n" + pk_sha
//
// ck_sha and pk_sha are SPKIHash of the client key and of the preview key.
func PreviewKeyMessage(ckSHA, pkSHA string) []byte {
	return []byte("remotectl-preview-key-v1\n" + ckSHA + "\n" + pkSHA)
}

// PreviewMeta is everything about a preview that is signed besides the
// ciphertext.
type PreviewMeta struct {
	DeviceID     string // lowercase UUID
	CKSHA        string // recipient client key, SPKIHash
	PKSHA        string // recipient preview key, SPKIHash
	CapturedAtMS int64
	MonitorID    uint32
	Width        int
	Height       int
	Mime         string
}

// PreviewHeader is the AES-GCM additional data and the start of the signed
// message, one value per line (no trailing newline):
//
//	remotectl-preview-v1, device_id, ck_sha, pk_sha, captured_at_ms,
//	monitor_id, width, height, mime, base64(eph_pub), base64(iv)
func PreviewHeader(m PreviewMeta, ephPub, iv []byte) []byte {
	return []byte(strings.Join([]string{
		"remotectl-preview-v1", strings.ToLower(m.DeviceID), m.CKSHA, m.PKSHA,
		strconv.FormatInt(m.CapturedAtMS, 10), strconv.FormatUint(uint64(m.MonitorID), 10),
		strconv.Itoa(m.Width), strconv.Itoa(m.Height), m.Mime,
		base64.StdEncoding.EncodeToString(ephPub), base64.StdEncoding.EncodeToString(iv),
	}, "\n"))
}

// PreviewSigMessage is what the device key signs: header + "\n" + hex(sha256(ciphertext)).
func PreviewSigMessage(header, ct []byte) []byte {
	h := sha256.Sum256(ct)
	return append(append(append([]byte{}, header...), '\n'), hex.EncodeToString(h[:])...)
}

// previewKey derives the AES-256 key: HKDF-SHA256 over the ECDH shared
// secret (the X coordinate, as WebCrypto deriveBits returns it), salted with
// the ephemeral public key and bound to the device and recipient keys.
func previewKey(shared, ephPub []byte, m PreviewMeta) ([]byte, error) {
	info := "remotectl-preview-v1\n" + strings.ToLower(m.DeviceID) + "\n" + m.CKSHA + "\n" + m.PKSHA
	return hkdf.Key(sha256.New, shared, ephPub, info, 32)
}

// ParsePreviewKey parses a browser's preview key (SPKI DER of a P-256 key;
// WebCrypto exports ECDH keys with the id-ecPublicKey OID).
func ParsePreviewKey(der []byte) (*ecdh.PublicKey, error) {
	pub, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return nil, err
	}
	ec, ok := pub.(*ecdsa.PublicKey)
	if !ok || ec.Curve.Params().Name != "P-256" {
		return nil, errors.New("preview key is not a P-256 key")
	}
	return ec.ECDH()
}

type SealedPreview struct {
	EphPub, IV, Ciphertext, Sig []byte
}

// SealPreview encrypts a picture to one browser's preview key and signs it.
func SealPreview(rnd io.Reader, devKey ed25519.PrivateKey, previewSPKI []byte, m PreviewMeta, plaintext []byte) (*SealedPreview, error) {
	eph, err := ecdh.P256().GenerateKey(rnd)
	if err != nil {
		return nil, err
	}
	iv := make([]byte, 12)
	if _, err := io.ReadFull(rnd, iv); err != nil {
		return nil, err
	}
	return sealPreview(eph, iv, devKey, previewSPKI, m, plaintext)
}

func sealPreview(eph *ecdh.PrivateKey, iv []byte, devKey ed25519.PrivateKey, previewSPKI []byte, m PreviewMeta, plaintext []byte) (*SealedPreview, error) {
	pub, err := ParsePreviewKey(previewSPKI)
	if err != nil {
		return nil, err
	}
	shared, err := eph.ECDH(pub)
	if err != nil {
		return nil, err
	}
	ephPub := eph.PublicKey().Bytes()
	key, err := previewKey(shared, ephPub, m)
	if err != nil {
		return nil, err
	}
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	header := PreviewHeader(m, ephPub, iv)
	ct := gcm.Seal(nil, iv, plaintext, header)
	return &SealedPreview{EphPub: ephPub, IV: iv, Ciphertext: ct,
		Sig: ed25519.Sign(devKey, PreviewSigMessage(header, ct))}, nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// VerifyPreviewSig checks the device's signature over a preview.
func VerifyPreviewSig(devPub ed25519.PublicKey, m PreviewMeta, s *SealedPreview) error {
	if len(devPub) != ed25519.PublicKeySize || len(s.EphPub) != 65 || len(s.IV) != 12 {
		return errors.New("malformed preview")
	}
	if !ed25519.Verify(devPub, PreviewSigMessage(PreviewHeader(m, s.EphPub, s.IV), s.Ciphertext), s.Sig) {
		return errors.New("preview signature mismatch")
	}
	return nil
}

// OpenPreview verifies a preview against the device key, then decrypts it
// with the browser's preview key (the browser does the same in WebCrypto).
func OpenPreview(devPub ed25519.PublicKey, priv *ecdh.PrivateKey, m PreviewMeta, s *SealedPreview) ([]byte, error) {
	if err := VerifyPreviewSig(devPub, m, s); err != nil {
		return nil, err
	}
	eph, err := ecdh.P256().NewPublicKey(s.EphPub)
	if err != nil {
		return nil, err
	}
	shared, err := priv.ECDH(eph)
	if err != nil {
		return nil, err
	}
	key, err := previewKey(shared, s.EphPub, m)
	if err != nil {
		return nil, err
	}
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	return gcm.Open(nil, s.IV, s.Ciphertext, PreviewHeader(m, s.EphPub, s.IV))
}

// Sealed decodes a preview message's base64 fields.
func (p Preview) Sealed() (*SealedPreview, error) {
	var s SealedPreview
	var err error
	dec := func(v string) []byte {
		b, e := base64.StdEncoding.DecodeString(v)
		if e != nil && err == nil {
			err = e
		}
		return b
	}
	s.EphPub, s.IV, s.Ciphertext, s.Sig = dec(p.EphPub), dec(p.IV), dec(p.Ciphertext), dec(p.Sig)
	return &s, err
}

// Meta is the signed metadata of a preview for the given device and keys.
func (p Preview) Meta(deviceID, ckSHA, pkSHA string) PreviewMeta {
	return PreviewMeta{DeviceID: deviceID, CKSHA: ckSHA, PKSHA: pkSHA, CapturedAtMS: p.CapturedAtMS,
		MonitorID: p.MonitorID, Width: p.Width, Height: p.Height, Mime: p.Mime}
}
