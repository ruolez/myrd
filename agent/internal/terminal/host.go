package terminal

import (
	"errors"
	"io"
)

var (
	ErrNotFound    = errors.New("no such terminal")
	ErrNoUser      = errors.New("nobody is signed in")
	ErrUnsupported = errors.New("not supported on this device")
	ErrTooMany     = errors.New("too many terminals")
)

// Host is where terminals come from: shells it starts and keeps, and
// terminals already open on the device.
type Host interface {
	// List returns every terminal the browser could take over or reopen.
	List() ([]Source, error)
	// Shells lists the programs New can start; the first is the default.
	Shells() []Shell
	// Admin reports whether New can start an elevated shell.
	Admin() bool
	// New starts a shell ("" = the default) and attaches to it.
	New(shell string, admin bool, cols, rows int) (Stream, Source, error)
	// Attach takes over the terminal with Source.ID id.
	Attach(id string, cols, rows int) (Stream, Source, error)
}

// Announcer shows a short message inside a terminal the person at the
// device has open (tmux's status line, screen's message line).
type Announcer interface {
	Announce(src Source, msg string)
}

// Stream is one viewer's attachment to a terminal.
type Stream interface {
	// Read returns output; io.EOF once the terminal ended or Detach ran.
	Read(p []byte) (int, error)
	// Write types p.
	Write(p []byte) (int, error)
	Resize(cols, rows int) error
	// Detach stops watching; the terminal keeps running.
	Detach()
	// Kill ends the program.
	Kill() error
	// Title is the terminal's current title.
	Title() string
	// ExitCode is the program's exit code once it ended (-1: killed by a
	// signal); nil while it runs or when it ended without one.
	ExitCode() *int
}

// Proc is a program running on a pseudo terminal.
type Proc interface {
	io.ReadWriter // output / input
	Resize(cols, rows int) error
	// Wait blocks until the program exits and returns its exit code
	// (-1 when it was killed by a signal).
	Wait() int
	// Hangup ends the program (SIGHUP to its process group, then a kill).
	Hangup()
	// Close releases the terminal after Wait returned.
	Close() error
}

// SpawnSpec describes a program to start on a new pseudo terminal.
type SpawnSpec struct {
	Path string
	Args []string // argv[0] included
	Dir  string
	Env  []string
	Cols int
	Rows int
}

// Spawner starts programs on pseudo terminals.
type Spawner func(SpawnSpec) (Proc, error)

func errCode(err error) string {
	switch {
	case errors.Is(err, ErrNotFound):
		return CodeNotFound
	case errors.Is(err, ErrNoUser):
		return CodeNoUser
	case errors.Is(err, ErrUnsupported):
		return CodeUnsupported
	case errors.Is(err, ErrTooMany):
		return CodeTooMany
	default:
		return CodeSpawn
	}
}
