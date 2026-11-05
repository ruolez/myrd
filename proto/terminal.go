package proto

// Remote terminal (docs/PROTOCOL.md §5 "`term`"). Terminal bytes travel
// end to end on the `term` data channel; the server only sees the session's
// mode and the audit records below (never what was typed or shown).

// Session modes (session.create / session.offer `mode`). A terminal session
// has no video: the agent starts no capture and needs no screen permission.
// The mode is a hint for the agent and the audit log, not a permission: the
// `term` channel works in both modes.
const (
	ModeDesktop  = "desktop"
	ModeTerminal = "terminal"
)

// ValidMode reports whether m is a session mode ("" means desktop).
func ValidMode(m string) bool { return m == "" || m == ModeDesktop || m == ModeTerminal }

// FeatureTerminal is advertised by the agent (control hello, and
// hello.capabilities.features so the dashboard offers Terminal) and by the
// server in hello_ack.features when it accepts session.terminal audit
// records and the organization allows terminals. An agent serves the `term`
// channel only while the server advertises it.
const FeatureTerminal = "terminal"

// TypeSessionTerminal carries terminal audit records (A→S, never relayed).
const TypeSessionTerminal = "session.terminal"

// ErrTerminalDisabled refuses a terminal session the organization turned off.
const ErrTerminalDisabled = "terminal_disabled"

// MaxTerminalRecords caps the records in one SessionTerminal message.
const MaxTerminalRecords = 50

// Terminal record actions and sources.
const (
	TermOpened   = "opened"   // a new shell started
	TermAttached = "attached" // an existing terminal was taken over
	TermClosed   = "closed"   // the browser stopped watching it (Reason says why)

	TermSourceShell   = "shell"   // a shell the agent keeps
	TermSourceTmux    = "tmux"    // a tmux session
	TermSourceScreen  = "screen"  // a GNU screen session
	TermSourceConsole = "console" // a Windows console window
	TermSourceApp     = "app"     // a Terminal.app / iTerm2 tab
)

// TerminalRecord describes one terminal event of a session. Content is
// never recorded.
type TerminalRecord struct {
	Term       uint16 `json:"term"`
	Action     string `json:"action"` // opened | attached | closed
	Source     string `json:"source"` // shell | tmux | screen | console | app
	Title      string `json:"title"`  // shell path, tmux session name, window title
	Admin      bool   `json:"admin,omitempty"`
	Result     string `json:"result"`           // "ok" | "error"
	Error      string `json:"error,omitempty"`  // error code when Result is "error"
	Reason     string `json:"reason,omitempty"` // closed: detached | exited | killed | session_ended
	ExitCode   *int   `json:"exit_code,omitempty"`
	BytesIn    int64  `json:"bytes_in,omitempty"`  // typed (browser → device)
	BytesOut   int64  `json:"bytes_out,omitempty"` // shown (device → browser)
	DurationMS int64  `json:"duration_ms,omitempty"`
}

type SessionTerminal struct {
	V         int              `json:"v"`
	Type      string           `json:"type"`
	SessionID string           `json:"session_id"`
	Terminals []TerminalRecord `json:"terminals"`
}
