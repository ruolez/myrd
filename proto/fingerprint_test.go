package proto

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"testing"
)

const sampleSDP = "v=0\r\no=- 1 2 IN IP4 127.0.0.1\r\na=fingerprint:sha-256 ab:CD:01:23\r\na=setup:actpass\r\n"

func TestSDPFingerprint(t *testing.T) {
	got, err := SDPFingerprint(sampleSDP)
	if err != nil || got != "AB:CD:01:23" {
		t.Fatalf("got %q, %v", got, err)
	}
	if _, err := SDPFingerprint("v=0\r\n"); err == nil {
		t.Fatal("expected error for sdp without fingerprint")
	}
}

func TestFingerprintMessageCanonical(t *testing.T) {
	got := string(FingerprintMessage("s1", RoleOffer, "ab:cd"))
	if got != "remotectl-fp-v1\ns1\noffer\nAB:CD" {
		t.Fatalf("got %q", got)
	}
}

func TestVerifyBrowserSigRoundTrip(t *testing.T) {
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, _ := x509.MarshalPKIXPublicKey(&k.PublicKey)
	spki := base64.StdEncoding.EncodeToString(der)
	msg := FingerprintMessage("s1", RoleOffer, "AB:CD")
	h := sha256.Sum256(msg)
	r, s, _ := ecdsa.Sign(rand.Reader, k, h[:])
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	sigB64 := base64.StdEncoding.EncodeToString(sig)

	if err := VerifyBrowserSig(spki, sigB64, msg); err != nil {
		t.Fatalf("valid sig rejected: %v", err)
	}
	if err := VerifyBrowserSig(spki, sigB64, FingerprintMessage("s1", RoleAnswer, "AB:CD")); err == nil {
		t.Fatal("sig for offer accepted as answer")
	}
}

func TestVerifyDeviceSigRoundTrip(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	msg := FingerprintMessage("s1", RoleAnswer, "AB:CD")
	sig := base64.StdEncoding.EncodeToString(ed25519.Sign(priv, msg))
	pubB64 := base64.StdEncoding.EncodeToString(pub)
	if err := VerifyDeviceSig(pubB64, sig, msg); err != nil {
		t.Fatalf("valid sig rejected: %v", err)
	}
	if err := VerifyDeviceSig(pubB64, sig, FingerprintMessage("s2", RoleAnswer, "AB:CD")); err == nil {
		t.Fatal("sig bound to s1 accepted for s2")
	}
}

func TestEncodeStampsEnvelope(t *testing.T) {
	b, err := Encode(TypePing, Ping{TS: 5})
	if err != nil || string(b) != `{"ts":5,"type":"ping","v":1}` {
		t.Fatalf("got %s, %v", b, err)
	}
}
