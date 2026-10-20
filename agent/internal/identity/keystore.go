// Package identity owns the device's Ed25519 key. The private key never
// leaves its store: a DPAPI LocalMachine blob (Windows) or a 0600 file
// (macOS, Linux) in the data dir. On macOS a key left in the login Keychain by
// older builds moves to the file on first Load.
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

// Default returns the platform keystore, which keeps its file in dataDir.
func Default(dataDir string) Keystore { return platformKeystore(dataDir) }
