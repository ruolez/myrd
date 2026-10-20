// Package identity owns the device's Ed25519 key. The private key never
// leaves the OS secret store: Keychain (macOS), DPAPI LocalMachine (Windows),
// a 0600 file in the data dir (Linux demo device).
package identity

import (
	"crypto/ed25519"
	"errors"
)

var ErrNotFound = errors.New("device key not found")

type Keystore interface {
	// Load returns ErrNotFound when no key has been stored yet.
	Load() (ed25519.PrivateKey, error)
	Save(ed25519.PrivateKey) error
	Delete() error
}

// Default returns the platform keystore. dataDir is used by stores that keep
// a file (Linux, Windows DPAPI blob).
func Default(dataDir string) Keystore { return platformKeystore(dataDir) }
