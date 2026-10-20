package security

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

var fastArgon = ArgonParams{Memory: 1024, Time: 1, Threads: 1}

func TestPasswordRoundTrip(t *testing.T) {
	h, err := HashPassword("correct horse", fastArgon)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$v=19$m=1024,t=1,p=1$") {
		t.Fatalf("unexpected encoding %q", h)
	}
	if !VerifyPassword("correct horse", h) || VerifyPassword("wrong horse", h) || VerifyPassword("x", "garbage") {
		t.Fatal("verify mismatch")
	}
}

func TestEnrollTokenFormatAndNormalize(t *testing.T) {
	for i := 0; i < 200; i++ {
		tok := NewEnrollToken()
		if len(tok) != 9 || tok[4] != '-' {
			t.Fatalf("bad format %q", tok)
		}
		norm, ok := NormalizeEnrollToken(strings.ToLower(tok))
		if !ok || norm != strings.ReplaceAll(tok, "-", "") {
			t.Fatalf("normalize %q → %q %v", tok, norm, ok)
		}
	}
	for _, bad := range []string{"ABCD-EFG", "ABCD-EFGI", "0BCD-EFGH", ""} { // I, 0 not in charset
		if _, ok := NormalizeEnrollToken(bad); ok {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestSealOpen(t *testing.T) {
	key := make([]byte, 32)
	s, err := Seal(key, []byte("JBSWY3DPEHPK3PXP"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := Open(key, s)
	if err != nil || string(p) != "JBSWY3DPEHPK3PXP" {
		t.Fatalf("got %q %v", p, err)
	}
	s[len(s)-1] ^= 1
	if _, err := Open(key, s); err == nil {
		t.Fatal("tampered ciphertext opened")
	}
}

func TestTURNCredentialMatchesCoturnScheme(t *testing.T) {
	exp := time.Unix(1700000000, 0)
	user, cred := TURNCredential("s3cret", "sess", "b", exp)
	if user != "1700000000:sess:b" {
		t.Fatalf("user %q", user)
	}
	m := hmac.New(sha1.New, []byte("s3cret"))
	m.Write([]byte("1700000000:sess:b"))
	if cred != base64.StdEncoding.EncodeToString(m.Sum(nil)) {
		t.Fatal("credential is not base64(HMAC-SHA1(secret, username))")
	}
}

func TestAgentJWT(t *testing.T) {
	secret := []byte("0123456789abcdef")
	now := time.Now()
	tok, exp, err := IssueAgentJWT(secret, "dev-1", now, 10*time.Minute)
	if err != nil || !exp.Equal(now.Add(10*time.Minute)) {
		t.Fatal(err)
	}
	if id, err := ParseAgentJWT(secret, tok); err != nil || id != "dev-1" {
		t.Fatalf("parse: %q %v", id, err)
	}
	if _, err := ParseAgentJWT([]byte("other-secret-123"), tok); err == nil {
		t.Fatal("wrong secret accepted")
	}
	old, _, _ := IssueAgentJWT(secret, "dev-1", now.Add(-time.Hour), 10*time.Minute)
	if _, err := ParseAgentJWT(secret, old); err == nil {
		t.Fatal("expired token accepted")
	}
}

func TestTOTPStep(t *testing.T) {
	// RFC 6238 appendix B SHA-1 seed "12345678901234567890", truncated to 6 digits.
	const secret = "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"
	cases := []struct {
		name     string
		code     string
		now      int64
		wantStep int64
		wantOK   bool
	}{
		{"current step", "287082", 59, 1, true},
		{"one step late", "287082", 89, 1, true},
		{"one step early", "287082", 29, 1, true},
		{"two steps late", "287082", 119, 0, false},
		{"rfc vector", "081804", 1111111109, 37037036, true},
		{"wrong code", "287083", 59, 0, false},
		{"wrong length", "28708", 59, 0, false},
	}
	for _, c := range cases {
		step, ok := TOTPStep(c.code, secret, time.Unix(c.now, 0))
		if step != c.wantStep || ok != c.wantOK {
			t.Errorf("%s: got (%d, %v), want (%d, %v)", c.name, step, ok, c.wantStep, c.wantOK)
		}
	}
}
