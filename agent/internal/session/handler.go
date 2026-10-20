package session

// Outbound sends one signaling message (msgType from proto.Type*, msg is a
// proto struct) towards the server.
type Outbound func(msgType string, msg any) error

// Handler is the seam between the signaling socket and whatever runs the
// media session. In single-process agents (macOS, Linux) it is *Manager. On
// Windows the LocalSystem service holds the socket and forwards these calls
// over a named pipe to agent-session.exe in the console session, which runs
// the real *Manager.
type Handler interface {
	// HandleSignal receives session.offer, ice.candidate and session.end as
	// raw JSON (already validated as v=1).
	HandleSignal(msgType string, raw []byte)
	// ActiveSessions lists session ids with a live peer connection (for hello).
	ActiveSessions() []string
	// EndAll ends every session with reason (kill switch, shutdown).
	EndAll(reason string)
}
