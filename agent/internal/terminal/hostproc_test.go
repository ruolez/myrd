package terminal

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestMain doubles as a fake terminal child: TERM_FAKE_CHILD=echo upper-
// cases input and reports sizes, exiting 7 when its input ends;
// TERM_FAKE_CHILD=stubborn ignores the end of its input.
func TestMain(m *testing.M) {
	switch os.Getenv("TERM_FAKE_CHILD") {
	case "echo":
		_ = ServeFrames(os.Stdin, fakeConsole{})
		os.Exit(7)
	case "stubborn":
		_ = ServeFrames(os.Stdin, fakeConsole{})
		time.Sleep(time.Minute)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

type fakeConsole struct{}

func (fakeConsole) Write(p []byte) (int, error) {
	return os.Stdout.Write(bytes.ToUpper(p))
}

func (fakeConsole) Resize(cols, rows int) error {
	_, err := fmt.Fprintf(os.Stdout, "[%dx%d]", cols, rows)
	return err
}

func fakeChild(t *testing.T, mode string, grace time.Duration) Proc {
	t.Helper()
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), "TERM_FAKE_CHILD="+mode)
	p, err := StartChild(cmd, grace)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func readUntil(t *testing.T, r io.Reader, want string) {
	t.Helper()
	got := make(chan string, 1)
	go func() {
		var b strings.Builder
		buf := make([]byte, 64)
		for !strings.Contains(b.String(), want) {
			n, err := r.Read(buf)
			b.Write(buf[:n])
			if err != nil {
				break
			}
		}
		got <- b.String()
	}()
	select {
	case s := <-got:
		if !strings.Contains(s, want) {
			t.Fatalf("output %q, want %q", s, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("no %q from the child", want)
	}
}

func TestChildProcRelaysInputAndSize(t *testing.T) {
	p := fakeChild(t, "echo", time.Second)
	if _, err := p.Write([]byte("ls -la\r")); err != nil {
		t.Fatal(err)
	}
	readUntil(t, p, "LS -LA\r")
	if err := p.Resize(132, 43); err != nil {
		t.Fatal(err)
	}
	readUntil(t, p, "[132x43]")
	p.Hangup()
	_, _ = io.Copy(io.Discard, p)
	if code := p.Wait(); code != 7 {
		t.Fatalf("exit code %d, want 7", code)
	}
}

func TestChildProcLargeInputIsSplitIntoFrames(t *testing.T) {
	p := fakeChild(t, "echo", time.Second)
	big := strings.Repeat("ab", maxFramePayload) // two frames
	go func() { _, _ = p.Write([]byte(big)) }()
	readUntil(t, p, strings.ToUpper(big))
	p.Hangup()
	_, _ = io.Copy(io.Discard, p)
	p.Wait()
}

func TestChildProcHangupKillsAStubbornChild(t *testing.T) {
	p := fakeChild(t, "stubborn", 200*time.Millisecond)
	start := time.Now()
	p.Hangup()
	_, _ = io.Copy(io.Discard, p)
	if code := p.Wait(); code == 0 {
		t.Fatalf("exit code 0, want a kill")
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("kill took %v", d)
	}
}

func TestReadFrame(t *testing.T) {
	var b bytes.Buffer
	_ = WriteResize(&b, 80, 24)
	_ = WriteInput(&b, []byte("x"))
	_ = WriteOpen(&b, "shell:2")
	b.Write([]byte{'z', 0, 0})
	br := bufio.NewReader(&b)
	f, _ := ReadFrame(br)
	if c, r, ok := f.Size(); !ok || c != 80 || r != 24 {
		t.Fatalf("resize %d %d %v", c, r, ok)
	}
	if f, _ = ReadFrame(br); !f.IsInput() || string(f.Payload) != "x" {
		t.Fatalf("input %+v", f)
	}
	if f, _ = ReadFrame(br); !f.IsOpen() || string(f.Payload) != "shell:2" {
		t.Fatalf("open %+v", f)
	}
	if _, err := ReadFrame(br); err == nil {
		t.Fatal("unknown frame type accepted")
	}
	if err := WriteResize(&b, 0, 24); err == nil {
		t.Fatal("zero size accepted")
	}
}
