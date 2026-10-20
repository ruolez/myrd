package pins

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"path/filepath"
	"testing"
	"time"
)

func newSPKI(t *testing.T) string {
	t.Helper()
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, _ := x509.MarshalPKIXPublicKey(&k.PublicKey)
	return base64.StdEncoding.EncodeToString(der)
}

func TestPinPersistsAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pins.json")
	s, _ := Open(path)
	key := newSPKI(t)
	if err := s.Pin(key, "Chrome on Mac", time.Unix(0, 0)); err != nil {
		t.Fatal(err)
	}
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if !s2.IsPinned(key) || len(s2.List()) != 1 || s2.List()[0].Label != "Chrome on Mac" {
		t.Fatalf("pins after reopen: %+v", s2.List())
	}
}

func TestRevokeRemovesOnlyRevokedKeys(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "pins.json"))
	a, b := newSPKI(t), newSPKI(t)
	_ = s.Pin(a, "a", time.Now())
	_ = s.Pin(b, "b", time.Now())
	n, err := s.Revoke([]string{a, newSPKI(t)})
	if err != nil || n != 1 {
		t.Fatalf("revoked %d, %v", n, err)
	}
	if s.IsPinned(a) || !s.IsPinned(b) {
		t.Fatal("wrong key revoked")
	}
}

func TestUnknownKeyNotPinned(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "pins.json"))
	if s.IsPinned(newSPKI(t)) || s.IsPinned("not base64!") {
		t.Fatal("unexpected pin")
	}
}
