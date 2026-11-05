package terminal

import (
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

type sharedHost struct{ h *fakeHost }

func (s sharedHost) NewShared(cols, rows int) (Stream, Source, error) {
	return s.h.shells.Start(SpawnSpec{Path: "/bin/sh", Cols: cols, Rows: rows}, Meta{Kind: "shell", Name: "sh", Local: true})
}
func (s sharedHost) JoinShared(id string, cols, rows int) (Stream, Source, error) {
	return s.h.shells.AttachShared(id, cols, rows)
}
func (s sharedHost) SharedList() []Source { return s.h.shells.ListShared() }

// dialShared connects a local viewer through an in-memory pipe.
func dialShared(h SharedHost, allowed bool, id string) net.Conn {
	srv, cli := net.Pipe()
	go func() { _ = serveShared(srv, h, func() bool { return allowed }) }()
	go func() {
		_ = WriteResize(cli, 100, 30)
		_ = WriteOpen(cli, id)
	}()
	_ = cli.SetDeadline(time.Now().Add(5 * time.Second))
	return cli
}

func TestSharedNewShellAndJoin(t *testing.T) {
	h := newFakeHost()
	sh := sharedHost{h}
	c := dialShared(sh, true, "")
	defer c.Close()
	waitFor(t, func() bool { h.mu.Lock(); defer h.mu.Unlock(); return len(h.procs) == 1 })
	if spec := h.specs[0]; spec.Cols != 100 || spec.Rows != 30 {
		t.Fatalf("shell size %dx%d", spec.Cols, spec.Rows)
	}
	p := h.proc(0)
	go func() { _, _ = p.outW.Write([]byte("local$ ")) }()
	readUntil(t, c, "local$ ")
	go func() { _ = WriteInput(c, []byte("ls\r")) }()
	waitFor(t, func() bool { return p.input() == "ls\r" })

	got, _ := io.ReadAll(dialShared(sh, true, ListShared))
	if string(got) != "shell:1\tsh\t1 attached\n" {
		t.Fatalf("list %q", got)
	}
	join := dialShared(sh, true, "shell:1")
	defer join.Close()
	readUntil(t, join, "local$ ") // the replay
}

func TestSharedRefusals(t *testing.T) {
	h := newFakeHost()
	sh := sharedHost{h}
	if got, _ := io.ReadAll(dialShared(sh, false, "")); !strings.Contains(string(got), "turned off") {
		t.Fatalf("off: %q", got)
	}
	// A shell the person at the device may not join (an administrator's).
	if _, _, err := h.shells.Start(SpawnSpec{Path: "/bin/sh", Cols: 80, Rows: 24}, Meta{Kind: "shell", Name: "root", Admin: true}); err != nil {
		t.Fatal(err)
	}
	if got, _ := io.ReadAll(dialShared(sh, true, "shell:1")); !strings.Contains(string(got), "no such terminal") {
		t.Fatalf("join admin shell: %q", got)
	}
	if l := h.shells.ListShared(); len(l) != 0 {
		t.Fatalf("shared list %+v", l)
	}
}
