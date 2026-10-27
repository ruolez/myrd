// Package security holds the pure crypto helpers: password hashing, opaque
// tokens, enrollment tokens, TOTP secret sealing, TURN credentials and agent
// JWTs.
package security

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
	"golang.org/x/crypto/argon2"
)

// ---------- passwords (argon2id) ----------

type ArgonParams struct {
	Memory  uint32 // KiB
	Time    uint32
	Threads uint8
}

var DefaultArgon = ArgonParams{Memory: 64 * 1024, Time: 2, Threads: 2}

func HashPassword(pw string, p ArgonParams) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	h := argon2.IDKey([]byte(pw), salt, p.Time, p.Memory, p.Threads, 32)
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s", p.Memory, p.Time, p.Threads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(h)), nil
}

func VerifyPassword(pw, encoded string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}
	var p ArgonParams
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.Memory, &p.Time, &p.Threads); err != nil {
		return false
	}
	salt, err1 := base64.RawStdEncoding.DecodeString(parts[4])
	want, err2 := base64.RawStdEncoding.DecodeString(parts[5])
	if err1 != nil || err2 != nil {
		return false
	}
	got := argon2.IDKey([]byte(pw), salt, p.Time, p.Memory, p.Threads, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

// ---------- opaque tokens ----------

// NewToken returns n random bytes base64url-encoded (no padding).
func NewToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func SHA256(s string) []byte {
	h := sha256.Sum256([]byte(s))
	return h[:]
}

// ---------- enrollment tokens ----------

const EnrollCharset = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

// NewEnrollToken returns "XXXX-XXXX" drawn uniformly from EnrollCharset.
func NewEnrollToken() string { return newCode(2) }

// NewInstallCode returns an install link code "XXXX-XXXX-XXXX": unlike a
// token it does not expire, so it is longer.
func NewInstallCode() string { return newCode(3) }

func newCode(groups int) string {
	out := make([]byte, 0, groups*5-1)
	buf := make([]byte, 1)
	for len(out) < cap(out) {
		if len(out)%5 == 4 {
			out = append(out, '-')
			continue
		}
		if _, err := rand.Read(buf); err != nil {
			panic(err)
		}
		// 256 is a multiple of 32: no modulo bias.
		out = append(out, EnrollCharset[int(buf[0])%len(EnrollCharset)])
	}
	return string(out)
}

// NormalizeEnrollToken uppercases, strips separators/whitespace and validates.
func NormalizeEnrollToken(s string) (string, bool) { return normalizeCode(s, 8) }

// NormalizeInstallCode is NormalizeEnrollToken for install link codes.
func NormalizeInstallCode(s string) (string, bool) { return normalizeCode(s, 12) }

func normalizeCode(s string, n int) (string, bool) {
	var b strings.Builder
	for _, r := range strings.ToUpper(s) {
		if r == '-' || r == ' ' || r == '\t' {
			continue
		}
		if !strings.ContainsRune(EnrollCharset, r) {
			return "", false
		}
		b.WriteRune(r)
	}
	if b.Len() != n {
		return "", false
	}
	return b.String(), true
}

// ---------- AES-GCM sealing (TOTP secrets) ----------

func Seal(key, plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, plaintext, nil), nil
}

func Open(key, sealed []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(sealed) < gcm.NonceSize() {
		return nil, errors.New("sealed value too short")
	}
	return gcm.Open(nil, sealed[:gcm.NonceSize()], sealed[gcm.NonceSize():], nil)
}

// ---------- TOTP ----------

const totpPeriod = 30

// TOTPStep returns the time step (unix/30) whose code matches, accepting one
// step of clock skew either way. Callers record the step to make codes single-use.
func TOTPStep(code, secret string, now time.Time) (int64, bool) {
	if len(code) != 6 {
		return 0, false
	}
	cur := now.Unix() / totpPeriod
	for step := cur - 1; step <= cur+1; step++ {
		want, err := totp.GenerateCodeCustom(secret, time.Unix(step*totpPeriod, 0), totp.ValidateOpts{
			Period: totpPeriod, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1})
		if err == nil && subtle.ConstantTimeCompare([]byte(want), []byte(code)) == 1 {
			return step, true
		}
	}
	return 0, false
}

// ---------- TURN (coturn REST API credentials) ----------

// TURNCredential returns username "<expiry>:<sessionID>:<side>" and
// base64(HMAC-SHA1(secret, username)).
func TURNCredential(secret, sessionID, side string, expiry time.Time) (user, cred string) {
	user = strconv.FormatInt(expiry.Unix(), 10) + ":" + sessionID + ":" + side
	m := hmac.New(sha1.New, []byte(secret))
	m.Write([]byte(user))
	return user, base64.StdEncoding.EncodeToString(m.Sum(nil))
}

// ---------- agent JWT ----------

type AgentClaims struct {
	Typ string `json:"typ"`
	jwt.RegisteredClaims
}

func IssueAgentJWT(secret []byte, deviceID string, now time.Time, ttl time.Duration) (string, time.Time, error) {
	exp := now.Add(ttl)
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, AgentClaims{
		Typ: "agent",
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   deviceID,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(exp),
		},
	})
	s, err := tok.SignedString(secret)
	return s, exp, err
}

// ParseAgentJWT returns the device id of a valid, unexpired agent token.
func ParseAgentJWT(secret []byte, token string) (string, error) {
	var c AgentClaims
	_, err := jwt.ParseWithClaims(token, &c, func(t *jwt.Token) (any, error) { return secret, nil },
		jwt.WithValidMethods([]string{"HS256"}), jwt.WithExpirationRequired())
	if err != nil {
		return "", err
	}
	if c.Typ != "agent" || c.Subject == "" {
		return "", errors.New("not an agent token")
	}
	return c.Subject, nil
}
