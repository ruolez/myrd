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
	if err := s.Pin(key, "Chrome on Mac", "u1", time.Unix(0, 0)); err != nil {
		t.Fatal(err)
	}
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if !s2.IsPinned(key) || len(s2.List()) != 1 || s2.List()[0].Label != "Chrome on Mac" || s2.List()[0].UserID != "u1" {
		t.Fatalf("pins after reopen: %+v", s2.List())
	}
}

func TestRevokeRemovesOnlyRevokedKeys(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "pins.json"))
	a, b := newSPKI(t), newSPKI(t)
	_ = s.Pin(a, "a", "u1", time.Now())
	_ = s.Pin(b, "b", "u1", time.Now())
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

func TestTrusted(t *testing.T) {
	const owner, other = "owner", "other"
	cases := []struct {
		name     string
		pinnedBy string // "" = a pin from before user ids were recorded
		pinSelf  bool   // the asking key itself is pinned
		asking   string
		want     bool
	}{
		{"pinned key", owner, true, owner, true},
		{"pinned key, server without user ids", owner, true, "", true},
		{"another browser of the account", owner, false, owner, true},
		{"another account", owner, false, other, false},
		{"server without user ids", owner, false, "", false},
		{"legacy pin trusts the device owner", "", false, other, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, _ := Open(filepath.Join(t.TempDir(), "pins.json"))
			self, pinned := newSPKI(t), newSPKI(t)
			if c.pinSelf {
				pinned = self
			}
			_ = s.Pin(pinned, "Chrome", c.pinnedBy, time.Now())
			if got := s.Trusted(self, c.asking); got != c.want {
				t.Fatalf("Trusted = %v, want %v", got, c.want)
			}
		})
	}
}

func TestTrustedRecordsTheAccountOnALegacyPin(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pins.json")
	s, _ := Open(path)
	key := newSPKI(t)
	_ = s.Pin(key, "Chrome", "", time.Now())
	if !s.Trusted(key, "owner") {
		t.Fatal("pinned key not trusted")
	}
	s2, _ := Open(path)
	if got := s2.List(); len(got) != 1 || got[0].UserID != "owner" {
		t.Fatalf("pins after reopen: %+v", got)
	}
	if s2.Trusted(newSPKI(t), "other") {
		t.Fatal("the pin still trusts every account after recording its own")
	}
}

func TestUnknownKeyNotTrusted(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "pins.json"))
	if s.Trusted(newSPKI(t), "owner") || s.Trusted("not base64!", "owner") {
		t.Fatal("unexpected trust")
	}
}
