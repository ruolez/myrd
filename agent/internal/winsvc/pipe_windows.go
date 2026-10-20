package winsvc

import (
	"context"
	"errors"
	"os"
	"time"

	"golang.org/x/sys/windows"
)

// Two one-way pipes; only SYSTEM may open them (the service and the helper
// both run as LocalSystem).
const (
	pipeToHelper   = `\\.\pipe\remotectl-agent-s2h`
	pipeFromHelper = `\\.\pipe\remotectl-agent-h2s`
	pipeSDDL       = "D:P(A;;GA;;;SY)"
	pipeBuf        = 64 * 1024
)

func pipeSA() (*windows.SecurityAttributes, error) {
	sd, err := windows.SecurityDescriptorFromString(pipeSDDL)
	if err != nil {
		return nil, err
	}
	sa := &windows.SecurityAttributes{SecurityDescriptor: sd}
	sa.Length = uint32(unsafeSizeofSA)
	return sa, nil
}

// pipeServer is one instance of the pipe pair waiting for a helper.
type pipeServer struct {
	out windows.Handle // service writes → helper
	in  windows.Handle // helper writes → service
}

func newPipeServer() (*pipeServer, error) {
	sa, err := pipeSA()
	if err != nil {
		return nil, err
	}
	mode := uint32(windows.PIPE_TYPE_BYTE | windows.PIPE_WAIT | windows.PIPE_REJECT_REMOTE_CLIENTS)
	mk := func(name string, access uint32) (windows.Handle, error) {
		n, _ := windows.UTF16PtrFromString(name)
		// FIRST_PIPE_INSTANCE: fail instead of joining a pipe someone else created.
		return windows.CreateNamedPipe(n, access|windows.FILE_FLAG_FIRST_PIPE_INSTANCE, mode, 1, pipeBuf, pipeBuf, 0, sa)
	}
	out, err := mk(pipeToHelper, windows.PIPE_ACCESS_OUTBOUND)
	if err != nil {
		return nil, err
	}
	in, err := mk(pipeFromHelper, windows.PIPE_ACCESS_INBOUND)
	if err != nil {
		windows.CloseHandle(out)
		return nil, err
	}
	return &pipeServer{out: out, in: in}, nil
}

// accept waits for the helper to open both ends. On timeout or ctx end it
// connects to itself to unblock ConnectNamedPipe and returns an error.
func (s *pipeServer) accept(ctx context.Context, timeout time.Duration) (w, r *os.File, err error) {
	done := make(chan error, 1)
	go func() {
		for _, h := range []windows.Handle{s.out, s.in} {
			if err := windows.ConnectNamedPipe(h, nil); err != nil && !errors.Is(err, windows.ERROR_PIPE_CONNECTED) {
				done <- err
				return
			}
		}
		done <- nil
	}()
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case err := <-done:
		if err != nil {
			s.close()
			return nil, nil, err
		}
		return os.NewFile(uintptr(s.out), pipeToHelper), os.NewFile(uintptr(s.in), pipeFromHelper), nil
	case <-ctx.Done():
		err = ctx.Err()
	case <-t.C:
		err = errors.New("winsvc: helper did not connect")
	}
	unblockPipe(pipeToHelper, windows.GENERIC_READ)
	unblockPipe(pipeFromHelper, windows.GENERIC_WRITE)
	<-done
	s.close()
	return nil, nil, err
}

func (s *pipeServer) close() {
	if s.out != 0 {
		windows.CloseHandle(s.out)
		s.out = 0
	}
	if s.in != 0 {
		windows.CloseHandle(s.in)
		s.in = 0
	}
}

func unblockPipe(name string, access uint32) {
	if h, err := openPipe(name, access); err == nil {
		windows.CloseHandle(h)
	}
}

func openPipe(name string, access uint32) (windows.Handle, error) {
	n, _ := windows.UTF16PtrFromString(name)
	return windows.CreateFile(n, access, 0, nil, windows.OPEN_EXISTING, 0, 0)
}

// dialService opens the helper ends of the pipe pair, retrying while the
// service is still creating them.
func dialService(ctx context.Context) (r, w *os.File, err error) {
	deadline := time.Now().Add(15 * time.Second)
	for {
		var in, out windows.Handle
		in, err = openPipe(pipeToHelper, windows.GENERIC_READ)
		if err == nil {
			out, err = openPipe(pipeFromHelper, windows.GENERIC_WRITE)
			if err == nil {
				return os.NewFile(uintptr(in), pipeToHelper), os.NewFile(uintptr(out), pipeFromHelper), nil
			}
			windows.CloseHandle(in)
		}
		if time.Now().After(deadline) {
			return nil, nil, err
		}
		select {
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}
