package terminal

import (
	"errors"
	"io"
	"os/exec"
	"sync"
	"time"
)

// childProc is a terminal program run by a child process that speaks the
// frame protocol on its stdin and writes the terminal's output to stdout.
type childProc struct {
	cmd   *exec.Cmd
	in    io.WriteCloser
	out   io.ReadCloser
	grace time.Duration

	wmu  sync.Mutex
	once sync.Once
	done chan struct{}
	code int
}

// StartChild starts cmd (its Stdin and Stdout must be unset) as a Proc.
// Hangup closes its stdin and kills it if it is still running after grace.
func StartChild(cmd *exec.Cmd, grace time.Duration) (Proc, error) {
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &childProc{cmd: cmd, in: in, out: out, grace: grace, done: make(chan struct{})}, nil
}

func (p *childProc) Read(b []byte) (int, error) { return p.out.Read(b) }

func (p *childProc) Write(b []byte) (int, error) {
	p.wmu.Lock()
	defer p.wmu.Unlock()
	if err := WriteInput(p.in, b); err != nil {
		return 0, err
	}
	return len(b), nil
}

func (p *childProc) Resize(cols, rows int) error {
	p.wmu.Lock()
	defer p.wmu.Unlock()
	return WriteResize(p.in, cols, rows)
}

func (p *childProc) Wait() int {
	p.once.Do(func() {
		err := p.cmd.Wait()
		p.code = 0
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			p.code = ee.ExitCode()
		} else if err != nil {
			p.code = -1
		}
		close(p.done)
	})
	return p.code
}

func (p *childProc) Hangup() {
	p.wmu.Lock()
	_ = p.in.Close()
	p.wmu.Unlock()
	go func() {
		select {
		case <-p.done:
		case <-time.After(p.grace):
			_ = p.cmd.Process.Kill()
		}
	}()
}

func (p *childProc) Close() error { return nil } // Wait closed the pipes
