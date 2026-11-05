//go:build !windows

package terminal

import (
	"errors"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
)

// hangupGrace is how long a hung-up program has before it is killed.
const hangupGrace = 3 * time.Second

type unixProc struct {
	cmd  *exec.Cmd
	f    *os.File
	once sync.Once
	done chan struct{}
	code int
}

// spawnPTY starts spec on a new pseudo terminal in its own session, the
// terminal as its controlling terminal.
func spawnPTY(spec SpawnSpec) (Proc, error) {
	cmd := &exec.Cmd{Path: spec.Path, Args: spec.Args, Dir: spec.Dir, Env: spec.Env}
	if len(cmd.Args) == 0 {
		cmd.Args = []string{spec.Path}
	}
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(spec.Cols), Rows: uint16(spec.Rows)})
	if err != nil {
		return nil, err
	}
	return &unixProc{cmd: cmd, f: f, done: make(chan struct{})}, nil
}

func (p *unixProc) Read(b []byte) (int, error) {
	n, err := p.f.Read(b)
	if err != nil && errors.Is(err, syscall.EIO) {
		err = os.ErrClosed // Linux: the last process holding the terminal exited
	}
	return n, err
}

func (p *unixProc) Write(b []byte) (int, error) { return p.f.Write(b) }

func (p *unixProc) Resize(cols, rows int) error {
	return pty.Setsize(p.f, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
}

func (p *unixProc) Wait() int {
	p.once.Do(func() {
		err := p.cmd.Wait()
		p.code = 0
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			p.code = ee.ExitCode() // -1 when a signal ended it
		} else if err != nil {
			p.code = -1
		}
		close(p.done)
	})
	return p.code
}

// Hangup sends SIGHUP to the program's process group, like closing a
// terminal window, and kills the group if it is still there after a grace.
func (p *unixProc) Hangup() {
	pid := p.cmd.Process.Pid
	_ = syscall.Kill(-pid, syscall.SIGHUP)
	_ = syscall.Kill(pid, syscall.SIGHUP)
	go func() {
		select {
		case <-p.done:
		case <-time.After(hangupGrace):
			_ = syscall.Kill(-pid, syscall.SIGKILL)
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	}()
}

func (p *unixProc) Close() error { return p.f.Close() }

// Cwd is the shell's working directory.
func (p *unixProc) Cwd() (string, error) { return processCwd(p.cmd.Process.Pid) }

func processCwd(pid int) (string, error) {
	if runtime.GOOS == "linux" {
		return os.Readlink("/proc/" + strconv.Itoa(pid) + "/cwd")
	}
	out, err := runTool(nil, "/usr/sbin/lsof", "-a", "-p", strconv.Itoa(pid), "-d", "cwd", "-Fn")
	if err != nil {
		return "", err
	}
	for _, l := range strings.Split(out, "\n") {
		if p, ok := strings.CutPrefix(l, "n/"); ok {
			return "/" + p, nil
		}
	}
	return "", ErrUnsupported
}
