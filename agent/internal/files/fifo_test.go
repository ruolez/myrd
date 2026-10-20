//go:build !windows

package files

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestFIFOListedAsOtherAndNotDownloadable(t *testing.T) {
	h := newHarness(t, nil)
	p := filepath.Join(h.home, "pipe")
	if err := syscall.Mkfifo(p, 0o644); err != nil {
		t.Skip("mkfifo:", err)
	}
	es, _, err := readDir(h.home, scanCap)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range es {
		if e.Name == "pipe" && e.Kind != KindOther {
			t.Fatalf("fifo kind %s", e.Kind)
		}
	}
	h.send(`{"t":"get","xfer":1,"path":%q}`, p)
	if e := h.ch.wait(t, "error", 1); e["code"] != CodeNotFile {
		t.Fatalf("error %v", e)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatal(err)
	}
}
