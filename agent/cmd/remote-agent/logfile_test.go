package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOpenLogFileRotatesPastMaxSize(t *testing.T) {
	const maxSize = 16
	cases := []struct {
		name     string
		existing string
		wantLog  string
		wantOld  string
	}{
		{"appends below the limit", "short\n", "short\nnew\n", ""},
		{"rotates past the limit", "a line longer than 16 bytes\n", "new\n", "a line longer than 16 bytes\n"},
	}
	for _, c := range cases {
		path := filepath.Join(t.TempDir(), "logs", "agent.log")
		os.MkdirAll(filepath.Dir(path), 0o700)
		os.WriteFile(path, []byte(c.existing), 0o600)
		f, err := openLogFile(path, maxSize)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		f.Write([]byte("new\n"))
		f.Close()
		gotLog, _ := os.ReadFile(path)
		gotOld, _ := os.ReadFile(path + ".1")
		if string(gotLog) != c.wantLog || string(gotOld) != c.wantOld {
			t.Errorf("%s: log %q old %q, want %q %q", c.name, gotLog, gotOld, c.wantLog, c.wantOld)
		}
	}
}

func TestOpenLogFileCreatesDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a", "b", "agent.log")
	f, err := openLogFile(path, maxLogSize)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("log file: %v %v", fi, err)
	}
}
