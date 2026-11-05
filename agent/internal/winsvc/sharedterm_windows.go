package winsvc

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"syscall"
	"time"

	"golang.org/x/sys/windows"

	"github.com/ruolez/remotectl/agent/internal/terminal"
)

// SharedPipe is the pipe `remote-agent attach` reaches the helper of
// Windows session sid by.
func SharedPipe(sid uint32) string { return fmt.Sprintf(`\\.\pipe\remotectl-term-%d`, sid) }

var _ terminal.SharedHost = (*TermHost)(nil)

func (t *TermHost) NewShared(cols, rows int) (terminal.Stream, terminal.Source, error) {
	if !t.userSignedIn() {
		return nil, terminal.Source{}, terminal.ErrNoUser
	}
	return t.New("", false, cols, rows)
}

func (t *TermHost) JoinShared(id string, cols, rows int) (terminal.Stream, terminal.Source, error) {
	return t.shells.AttachShared(id, cols, rows)
}

func (t *TermHost) SharedList() []terminal.Source { return t.shells.ListShared() }

// ServeShared serves `remote-agent attach` run by the user signed in to
// the session, until ctx ends. Only that user (and SYSTEM) may open the
// pipe, and only from this session.
func (t *TermHost) ServeShared(ctx context.Context, allowed func() bool) {
	accept := func() (io.ReadWriteCloser, error) {
		for {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			f, err := t.acceptShared(ctx)
			if err == nil {
				return f, nil
			}
			if !errors.Is(err, errNoSessionUser) {
				t.Log.Debug("winsvc: shared terminal pipe", "err", err)
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(5 * time.Second):
			}
		}
	}
	_ = terminal.ServeShared(accept, t, allowed, t.Log)
}

var errNoSessionUser = errors.New("nobody is signed in")

func (t *TermHost) acceptShared(ctx context.Context) (io.ReadWriteCloser, error) {
	var tok windows.Token
	if err := windows.WTSQueryUserToken(t.SessionID, &tok); err != nil {
		return nil, errNoSessionUser
	}
	u, err := tok.GetTokenUser()
	tok.Close()
	if err != nil {
		return nil, err
	}
	sd, err := windows.SecurityDescriptorFromString("D:P(A;;GA;;;SY)(A;;GRGW;;;" + u.User.Sid.String() + ")")
	if err != nil {
		return nil, err
	}
	sa := &windows.SecurityAttributes{SecurityDescriptor: sd}
	sa.Length = uint32(unsafeSizeofSA)
	name := SharedPipe(t.SessionID)
	n, _ := windows.UTF16PtrFromString(name)
	h, err := windows.CreateNamedPipe(n, windows.PIPE_ACCESS_DUPLEX|windows.FILE_FLAG_OVERLAPPED,
		windows.PIPE_TYPE_BYTE|windows.PIPE_WAIT|windows.PIPE_REJECT_REMOTE_CLIENTS, windows.PIPE_UNLIMITED_INSTANCES,
		pipeBuf, pipeBuf, 0, sa)
	if err != nil {
		return nil, err
	}
	ev, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		windows.CloseHandle(h)
		return nil, err
	}
	defer windows.CloseHandle(ev)
	ov := windows.Overlapped{HEvent: ev}
	err = windows.ConnectNamedPipe(h, &ov)
	if errors.Is(err, windows.ERROR_IO_PENDING) {
		stop := context.AfterFunc(ctx, func() { _ = windows.CancelIoEx(h, &ov) })
		var n uint32
		err = windows.GetOverlappedResult(h, &ov, &n, true)
		stop()
	}
	if err != nil && !errors.Is(err, windows.ERROR_PIPE_CONNECTED) {
		windows.CloseHandle(h)
		return nil, err
	}
	var pid, sid uint32
	if windows.GetNamedPipeClientProcessId(h, &pid) != nil || windows.ProcessIdToSessionId(pid, &sid) != nil || sid != t.SessionID {
		windows.CloseHandle(h)
		return nil, errors.New("client from another session")
	}
	return os.NewFile(uintptr(h), name), nil // overlapped: reads and writes run side by side
}

// OpenShared opens a console window as the signed-in user running
// `remote-agent attach` (tray: Open shared terminal).
func (t *TermHost) OpenShared() error {
	var tok windows.Token
	if err := windows.WTSQueryUserToken(t.SessionID, &tok); err != nil {
		return err
	}
	defer tok.Close()
	cmd := exec.Command(t.Exe, "attach")
	cmd.SysProcAttr = &syscall.SysProcAttr{Token: syscall.Token(tok), CreationFlags: windows.CREATE_NEW_CONSOLE}
	if env, err := tok.Environ(false); err == nil {
		cmd.Env = env
	}
	if dir, err := tok.GetUserProfileDirectory(); err == nil {
		cmd.Dir = dir
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}
