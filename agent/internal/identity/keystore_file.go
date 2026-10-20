package identity

import (
	"crypto/ed25519"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// FileKeystore stores the raw 64-byte private key in a 0600 file.
type FileKeystore struct{ Path string }

func (f FileKeystore) Load() (ed25519.PrivateKey, error) {
	b, err := os.ReadFile(f.Path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if len(b) != ed25519.PrivateKeySize {
		return nil, errors.New("corrupt device key file")
	}
	return ed25519.PrivateKey(b), nil
}

func (f FileKeystore) Save(k ed25519.PrivateKey) error {
	if len(k) != ed25519.PrivateKeySize {
		return errors.New("invalid ed25519 private key")
	}
	if err := os.MkdirAll(filepath.Dir(f.Path), 0o700); err != nil {
		return err
	}
	tmp := f.Path + ".tmp"
	_ = os.Remove(tmp)
	if err := os.WriteFile(tmp, k, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, f.Path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

func (f FileKeystore) Delete() error {
	err := os.Remove(f.Path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}
