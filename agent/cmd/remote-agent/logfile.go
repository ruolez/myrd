package main

import (
	"io"
	"os"
	"path/filepath"
	"runtime"
)

const maxLogSize = 10 << 20

// defaultLogFile is where an interactive run also logs: a Finder-launched
// macOS app has no visible stderr. Empty means stderr only.
func defaultLogFile() string {
	if runtime.GOOS != "darwin" {
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, "Library", "Logs", "RemoteAgent.log")
}

// openLogFile appends to path, first moving it to path.1 once it has grown
// past maxSize.
func openLogFile(path string, maxSize int64) (io.WriteCloser, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if fi, err := os.Stat(path); err == nil && fi.Size() > maxSize {
		os.Rename(path, path+".1")
	}
	return os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
}
