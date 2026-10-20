//go:build darwin

package identity

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultKeepsKeyInDataDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "RemoteAgent")
	key := newKey(t)
	if err := Default(dir).Save(key); err != nil {
		t.Fatal(err)
	}
	got, err := (FileKeystore{Path: filepath.Join(dir, "device.key")}).Load()
	if err != nil || !got.Equal(key) {
		t.Fatalf("device.key: %v", err)
	}
	if got, err := Default(dir).Load(); err != nil || !got.Equal(key) {
		t.Fatalf("Default load: %v", err)
	}
}

// Touches the real login Keychain, so it only runs when asked:
//
//	RC_KEYCHAIN_TEST=1 go test ./internal/identity -run Keychain
func TestKeychainMigratesToFile(t *testing.T) {
	if os.Getenv("RC_KEYCHAIN_TEST") != "1" {
		t.Skip("set RC_KEYCHAIN_TEST=1 to exercise the login Keychain")
	}
	legacy := KeychainKeystore{Service: "com.remotectl.agent.test", Account: "device-key"}
	t.Cleanup(func() { _ = legacy.Delete() })
	key := newKey(t)
	if err := legacy.Save(key); err != nil {
		t.Fatal(err)
	}
	file := FileKeystore{Path: filepath.Join(t.TempDir(), "device.key")}
	ks := migratingKeystore{primary: file, legacy: legacy}
	if got, err := ks.Load(); err != nil || !got.Equal(key) {
		t.Fatalf("migrating load: %v", err)
	}
	if got, err := file.Load(); err != nil || !got.Equal(key) {
		t.Fatalf("file after migration: %v", err)
	}
	if _, err := legacy.Load(); !errors.Is(err, ErrNotFound) {
		t.Fatalf("keychain after migration: want ErrNotFound, got %v", err)
	}
}

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
