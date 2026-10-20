package identity

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"testing"
)

type fakeKeystore struct {
	key                         ed25519.PrivateKey
	loadErr, saveErr, deleteErr error
	saves, deletes              int
}

func (f *fakeKeystore) Load() (ed25519.PrivateKey, error) {
	if f.loadErr != nil {
		return nil, f.loadErr
	}
	if f.key == nil {
		return nil, ErrNotFound
	}
	return f.key, nil
}

func (f *fakeKeystore) Save(k ed25519.PrivateKey) error {
	f.saves++
	if f.saveErr != nil {
		return f.saveErr
	}
	f.key = k
	return nil
}

func (f *fakeKeystore) Delete() error {
	f.deletes++
	if f.deleteErr != nil {
		return f.deleteErr
	}
	f.key = nil
	return nil
}

func newKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, k, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestMigratingKeystoreLoad(t *testing.T) {
	fileKey, legacyKey := newKey(t), newKey(t)
	errKeychain := errors.New("keychain read: OSStatus -128")
	errDisk := errors.New("disk full")

	cases := []struct {
		name                   string
		primary, legacy        fakeKeystore
		wantKey                ed25519.PrivateKey
		wantErr                error
		wantPrimary            ed25519.PrivateKey
		wantLegacy             ed25519.PrivateKey
		wantSaves, wantDeletes int
	}{
		{name: "file hit leaves legacy untouched",
			primary: fakeKeystore{key: fileKey}, legacy: fakeKeystore{key: legacyKey},
			wantKey: fileKey, wantPrimary: fileKey, wantLegacy: legacyKey},
		{name: "file miss migrates legacy",
			legacy:  fakeKeystore{key: legacyKey},
			wantKey: legacyKey, wantPrimary: legacyKey, wantSaves: 1, wantDeletes: 1},
		{name: "both miss",
			wantErr: ErrNotFound},
		{name: "legacy error is returned",
			legacy:  fakeKeystore{loadErr: errKeychain},
			wantErr: errKeychain},
		{name: "file read error skips legacy",
			primary: fakeKeystore{loadErr: errDisk}, legacy: fakeKeystore{key: legacyKey},
			wantErr: errDisk, wantLegacy: legacyKey},
		{name: "file write failure keeps legacy and returns key",
			primary: fakeKeystore{saveErr: errDisk}, legacy: fakeKeystore{key: legacyKey},
			wantKey: legacyKey, wantLegacy: legacyKey, wantSaves: 1},
		{name: "legacy delete failure still returns key",
			legacy:  fakeKeystore{key: legacyKey, deleteErr: errKeychain},
			wantKey: legacyKey, wantPrimary: legacyKey, wantLegacy: legacyKey, wantSaves: 1, wantDeletes: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := migratingKeystore{primary: &tc.primary, legacy: &tc.legacy}
			got, err := m.Load()
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if !got.Equal(tc.wantKey) {
				t.Fatal("returned key mismatch")
			}
			if !tc.primary.key.Equal(tc.wantPrimary) || !tc.legacy.key.Equal(tc.wantLegacy) {
				t.Fatalf("stores after load: primary set=%v legacy set=%v", tc.primary.key != nil, tc.legacy.key != nil)
			}
			if tc.primary.saves != tc.wantSaves || tc.legacy.saves != 0 || tc.legacy.deletes != tc.wantDeletes {
				t.Fatalf("primary saves=%d legacy saves=%d deletes=%d", tc.primary.saves, tc.legacy.saves, tc.legacy.deletes)
			}
		})
	}
}

func TestMigratingKeystoreSaveOnlyPrimary(t *testing.T) {
	key := newKey(t)
	var primary, legacy fakeKeystore
	if err := (migratingKeystore{primary: &primary, legacy: &legacy}).Save(key); err != nil {
		t.Fatal(err)
	}
	if !primary.key.Equal(key) || legacy.saves != 0 || legacy.deletes != 0 {
		t.Fatalf("primary set=%v, legacy saves=%d deletes=%d", primary.key != nil, legacy.saves, legacy.deletes)
	}
}

func TestMigratingKeystoreDeleteRemovesBoth(t *testing.T) {
	key := newKey(t)
	primary, legacy := fakeKeystore{key: key}, fakeKeystore{key: key}
	if err := (migratingKeystore{primary: &primary, legacy: &legacy}).Delete(); err != nil {
		t.Fatal(err)
	}
	if primary.key != nil || legacy.key != nil {
		t.Fatalf("after delete: primary set=%v legacy set=%v", primary.key != nil, legacy.key != nil)
	}

	errKeychain := errors.New("keychain delete: OSStatus -25244")
	primary, legacy = fakeKeystore{key: key}, fakeKeystore{key: key, deleteErr: errKeychain}
	err := (migratingKeystore{primary: &primary, legacy: &legacy}).Delete()
	if !errors.Is(err, errKeychain) || primary.key != nil {
		t.Fatalf("legacy delete failure: err=%v, primary removed=%v", err, primary.key == nil)
	}
}
