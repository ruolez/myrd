package files

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestRenameNoReplace(t *testing.T) {
	for name, rename := range map[string]func(string, string) error{"native": renameNoReplace, "link fallback": linkRename} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			from, to := filepath.Join(dir, "from"), filepath.Join(dir, "to")
			_ = os.WriteFile(from, []byte("new"), 0o644)
			_ = os.WriteFile(to, []byte("old"), 0o644)
			if err := rename(from, to); !errors.Is(err, fs.ErrExist) {
				t.Fatalf("rename onto an existing file: %v", err)
			}
			if b, _ := os.ReadFile(to); string(b) != "old" {
				t.Fatalf("target replaced: %q", b)
			}
			free := filepath.Join(dir, "free")
			if err := rename(from, free); err != nil {
				t.Fatal(err)
			}
			if b, _ := os.ReadFile(free); string(b) != "new" {
				t.Fatalf("renamed content %q", b)
			}
			if _, err := os.Stat(from); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("source still there: %v", err)
			}
		})
	}
}
