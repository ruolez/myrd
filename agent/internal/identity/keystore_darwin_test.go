//go:build darwin

package identity

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"os"
	"testing"
)

// Touches the real login Keychain, so it only runs when asked:
//
//	RC_KEYCHAIN_TEST=1 go test ./internal/identity -run Keychain
func TestKeychainRoundTrip(t *testing.T) {
	if os.Getenv("RC_KEYCHAIN_TEST") != "1" {
		t.Skip("set RC_KEYCHAIN_TEST=1 to exercise the login Keychain")
	}
	ks := KeychainKeystore{Service: "com.remotectl.agent.test", Account: "device-key"}
	t.Cleanup(func() { _ = ks.Delete() })
	_ = ks.Delete()

	if _, err := ks.Load(); !errors.Is(err, ErrNotFound) {
		t.Fatalf("load before save: want ErrNotFound, got %v", err)
	}
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	if err := ks.Save(priv); err != nil {
		t.Fatal(err)
	}
	if err := ks.Save(priv); err != nil {
		t.Fatalf("overwrite: %v", err)
	}
	got, err := ks.Load()
	if err != nil || !got.Equal(priv) {
		t.Fatalf("load after save: %v", err)
	}
	if err := ks.Delete(); err != nil {
		t.Fatal(err)
	}
	if _, err := ks.Load(); !errors.Is(err, ErrNotFound) {
		t.Fatalf("load after delete: want ErrNotFound, got %v", err)
	}
}
