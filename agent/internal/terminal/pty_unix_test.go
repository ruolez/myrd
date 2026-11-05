//go:build !windows

package terminal

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A real shell on a real pseudo terminal: typed input runs, the window size
// reaches the program, and the exit code comes back.
func TestSpawnPTYRunsShell(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("no /bin/sh")
	}
	p, err := spawnPTY(SpawnSpec{Path: "/bin/sh", Args: []string{"sh"}, Env: buildEnv(nil, nil), Cols: 91, Rows: 17})
	if err != nil {
		t.Fatal(err)
	}
	out := make(chan []byte, 100)
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := p.Read(buf)
			if n > 0 {
				out <- append([]byte(nil), buf[:n]...)
			}
			if err != nil {
				close(out)
				return
			}
		}
	}()
	if _, err := p.Write([]byte("stty size; echo marker-$TERM; exit 7\n")); err != nil {
		t.Fatal(err)
	}
	var got bytes.Buffer
	timeout := time.After(5 * time.Second)
	for done := false; !done; {
		select {
		case b, ok := <-out:
			if !ok {
				done = true
			}
			got.Write(b)
		case <-timeout:
			t.Fatalf("no exit; output %q", got.String())
		}
	}
	if code := p.Wait(); code != 7 {
		t.Fatalf("exit code %d, want 7", code)
	}
	_ = p.Close()
	s := got.String()
	if !strings.Contains(s, "17 91") || !strings.Contains(s, "marker-xterm-256color") {
		t.Fatalf("output %q lacks size or TERM", s)
	}
}

func TestHangupEndsProgram(t *testing.T) {
	p, err := spawnPTY(SpawnSpec{Path: "/bin/sh", Args: []string{"sh", "-c", "sleep 30"}, Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		buf := make([]byte, 1024)
		for {
			if _, err := p.Read(buf); err != nil {
				return
			}
		}
	}()
	p.Hangup()
	done := make(chan int)
	go func() { done <- p.Wait() }()
	select {
	case code := <-done:
		if code != -1 {
			t.Fatalf("exit code %d, want -1 (signal)", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("program still running after hangup")
	}
}

func TestSpawnPTYCwd(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("no /bin/sh")
	}
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	p, err := spawnPTY(SpawnSpec{Path: "/bin/sh", Args: []string{"sh"}, Dir: dir, Env: buildEnv(nil, nil), Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		p.Hangup()
		p.Wait()
		_ = p.Close()
	}()
	go func() { _, _ = io.Copy(io.Discard, p) }()
	got, err := p.(Cwder).Cwd()
	if err != nil || got != dir {
		t.Fatalf("cwd %q %v, want %q", got, err, dir)
	}
	_, _ = p.Write([]byte("cd /\n"))
	waitFor(t, func() bool { c, _ := p.(Cwder).Cwd(); return c == "/" })
}
