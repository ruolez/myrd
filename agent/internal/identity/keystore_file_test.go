package identity

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestFileKeystoreRoundTrip(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "RemoteAgent")
	ks := FileKeystore{Path: filepath.Join(dir, "device.key")}
	if _, err := ks.Load(); !errors.Is(err, ErrNotFound) {
		t.Fatalf("load before save: want ErrNotFound, got %v", err)
	}
	if err := ks.Save(newKey(t)[:10]); err == nil {
		t.Fatal("short key saved")
	}

	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ks.Path, make([]byte, 64), 0o644); err != nil {
		t.Fatal(err)
	}
	key := newKey(t)
	if err := ks.Save(key); err != nil {
		t.Fatal(err)
	}
	got, err := ks.Load()
	if err != nil || !got.Equal(key) {
		t.Fatalf("load after save: %v", err)
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(ks.Path)
		if err != nil || fi.Mode().Perm() != 0o600 {
			t.Fatalf("key file mode = %v (%v), want 0600", fi.Mode().Perm(), err)
		}
	}
	if _, err := os.Stat(ks.Path + ".tmp"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temp file left behind: %v", err)
	}

	if err := ks.Delete(); err != nil {
		t.Fatal(err)
	}
	if _, err := ks.Load(); !errors.Is(err, ErrNotFound) {
		t.Fatalf("load after delete: want ErrNotFound, got %v", err)
	}
	if err := ks.Delete(); err != nil {
		t.Fatalf("delete when absent: %v", err)
	}
}

func TestFileKeystoreCreatesPrivateDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX modes")
	}
	dir := filepath.Join(t.TempDir(), "a", "RemoteAgent")
	if err := (FileKeystore{Path: filepath.Join(dir, "device.key")}).Save(newKey(t)); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(dir)
	if err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("data dir mode = %v (%v), want 0700", fi.Mode().Perm(), err)
	}
}
