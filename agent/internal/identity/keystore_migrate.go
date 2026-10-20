package identity

import (
	"crypto/ed25519"
	"errors"
)

// migratingKeystore reads and writes primary only; on a primary miss it moves
// a key found in legacy into primary. A failed move still returns the key and
// leaves legacy intact, so the next Load tries again.
type migratingKeystore struct{ primary, legacy Keystore }

func (m migratingKeystore) Load() (ed25519.PrivateKey, error) {
	key, err := m.primary.Load()
	if !errors.Is(err, ErrNotFound) {
		return key, err
	}
	key, err = m.legacy.Load()
	if err != nil {
		return nil, err
	}
	if m.primary.Save(key) == nil {
		_ = m.legacy.Delete()
	}
	return key, nil
}

func (m migratingKeystore) Save(key ed25519.PrivateKey) error { return m.primary.Save(key) }

func (m migratingKeystore) Delete() error {
	return errors.Join(m.primary.Delete(), m.legacy.Delete())
}
