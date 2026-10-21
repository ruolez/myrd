// Package proto holds the wire types shared by the server and the agent:
// signaling messages (WebSocket, JSON, v=1) and the fingerprint-signing
// canonical form. docs/PROTOCOL.md is the human-readable spec; this file is
// the source of truth for field names.
package proto

import "encoding/json"

const Version = 1

// MaxMessageBytes caps every signaling message (doc: 64 KB).
const MaxMessageBytes = 64 * 1024

// Message types. Direction noted as B=browser, S=server, A=agent.
const (
	// agent <-> server
	TypeHello         = "hello"              // A→S
	TypeHelloAck      = "hello_ack"          // S→A
	TypeTokenRenew    = "token.renew"        // A→S
	TypeToken         = "token"              // S→A
	TypePermissions   = "permissions"        // A→S
	TypePinsSync      = "pins.sync"          // A→S
	TypeDeviceUpdate  = "device.update"      // S→A
	TypeKeyRevoked    = "client_key.revoked" // S→A
	TypeSessionStatus = "session.status"     // A→S→B
	TypeSessionState  = "session.state"      // A→S→B
	TypeSessionFile   = "session.file"       // A→S, audit only, never relayed
	TypeUpdateCheck   = "update.check"       // S→A
	TypeUpdateStatus  = "update.status"      // A→S

	// browser <-> server
	TypeSessionCreate      = "session.create"       // B→S
	TypeSessionCreated     = "session.created"      // S→B
	TypeDeviceStatus       = "device.status"        // S→B
	TypeDeviceUpdateStatus = "device.update_status" // S→B

	// relayed both ways
	TypeSessionOffer  = "session.offer"  // B→S→A
	TypeSessionAnswer = "session.answer" // A→S→B
	TypeICECandidate  = "ice.candidate"  // B↔S↔A
	TypeSessionEnd    = "session.end"    // any→S→other

	TypePing  = "ping"
	TypePong  = "pong"
	TypeError = "error" // S→B / S→A
)

// Envelope is decoded first to route a message by Type.
type Envelope struct {
	V    int    `json:"v"`
	Type string `json:"type"`
}

// ---------- shared value types ----------

type Monitor struct {
	ID      uint32  `json:"id"`
	Name    string  `json:"name"`
	X       int32   `json:"x"`
	Y       int32   `json:"y"`
	Width   uint32  `json:"width"`
	Height  uint32  `json:"height"`
	Scale   float32 `json:"scale"`
	Primary bool    `json:"primary"`
}

type Capabilities struct {
	Monitors []Monitor `json:"monitors"`
	Codecs   []string  `json:"codecs"`  // ["h264"]
	MaxFPS   int       `json:"max_fps"` // 60
	Encoder  string    `json:"encoder"` // "videotoolbox" | "mf-hw" | "x264" ...
}

type Permissions struct {
	Screen bool `json:"screen"`
	Input  bool `json:"input"`
}

type ICEServer struct {
	URLs       []string `json:"urls"`
	Username   string   `json:"username,omitempty"`
	Credential string   `json:"credential,omitempty"`
}

// ICECandidate mirrors RTCIceCandidateInit. A nil candidate means end-of-candidates.
type ICECandidate struct {
	Candidate        string  `json:"candidate"`
	SDPMid           *string `json:"sdpMid,omitempty"`
	SDPMLineIndex    *uint16 `json:"sdpMLineIndex,omitempty"`
	UsernameFragment *string `json:"usernameFragment,omitempty"`
}

// ClientKeyInfo is the browser key the server claims made an offer. The agent
// trusts it only if the key is in its local pinned list or the owner approves
// it on the device screen.
type ClientKeyInfo struct {
	ID        string `json:"id"`
	PublicKey string `json:"public_key"` // base64 std, SPKI DER, ECDSA P-256
	Label     string `json:"label"`
	CreatedAt string `json:"created_at"` // RFC3339
}

type PinnedClient struct {
	PublicKeySHA256 string `json:"public_key_sha256"` // hex sha256 of SPKI DER
	Label           string `json:"label"`
	ApprovedAt      string `json:"approved_at"`       // RFC3339
	UserID          string `json:"user_id,omitempty"` // account that approved it; empty on pins from before 0.3.9
}

type UserInfo struct {
	ID    string `json:"id,omitempty"`
	Email string `json:"email"`
}

// ---------- agent <-> server ----------

type Hello struct {
	V                int            `json:"v"`
	Type             string         `json:"type"`
	AgentVersion     string         `json:"agent_version"`
	OS               string         `json:"os"`   // "darwin" | "windows" | "linux"
	Arch             string         `json:"arch"` // "arm64" | "amd64"
	Hostname         string         `json:"hostname"`
	Capabilities     Capabilities   `json:"capabilities"`
	Permissions      Permissions    `json:"permissions"`
	ActiveSessions   []string       `json:"active_sessions"`
	PinnedClientKeys []PinnedClient `json:"pinned_client_keys"`
}

type DeviceInfo struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	AutoAccept bool   `json:"auto_accept"`
}

type UpdateHint struct {
	Available bool   `json:"available"`
	Version   string `json:"version,omitempty"`
}

type HelloAck struct {
	V                 int        `json:"v"`
	Type              string     `json:"type"`
	ServerTime        string     `json:"server_time"` // RFC3339Nano
	Device            DeviceInfo `json:"device"`
	Update            UpdateHint `json:"update"`
	RevokedClientKeys []string   `json:"revoked_client_keys"` // SPKI base64
}

type Token struct {
	V         int    `json:"v"`
	Type      string `json:"type"`
	Token     string `json:"token"`
	ExpiresAt string `json:"expires_at"`
}

type PermissionsMsg struct {
	V           int         `json:"v"`
	Type        string      `json:"type"`
	Permissions Permissions `json:"permissions"`
}

type PinsSync struct {
	V                int            `json:"v"`
	Type             string         `json:"type"`
	PinnedClientKeys []PinnedClient `json:"pinned_client_keys"`
}

type DeviceUpdate struct {
	V          int    `json:"v"`
	Type       string `json:"type"`
	Name       string `json:"name"`
	AutoAccept bool   `json:"auto_accept"`
}

// UpdateCheck asks the agent to look for a release now. Explicit skips the
// rollout cohort and the agent's skip-list of failed versions; Force ends
// active sessions instead of waiting for the device to be idle.
type UpdateCheck struct {
	V        int    `json:"v"`
	Type     string `json:"type"`
	Explicit bool   `json:"explicit,omitempty"`
	Force    bool   `json:"force,omitempty"`
}

// Update states reported in update.status and stored on the device.
const (
	UpdateChecking    = "checking"
	UpdateDownloading = "downloading"
	UpdateWaitingIdle = "waiting_idle"
	UpdateInstalling  = "installing"
	UpdateUpToDate    = "up_to_date"
	UpdateFailed      = "failed"
	UpdateRolledBack  = "rolled_back"
)

// UpdateStates lists every valid update.status state.
var UpdateStates = []string{UpdateChecking, UpdateDownloading, UpdateWaitingIdle, UpdateInstalling,
	UpdateUpToDate, UpdateFailed, UpdateRolledBack}

const (
	MaxUpdateVersionLen = 64
	MaxUpdateErrorLen   = 512
)

// UpdateStatus reports update progress (sent on state changes only). From is
// the version running when the attempt started, Version the target.
type UpdateStatus struct {
	V       int    `json:"v"`
	Type    string `json:"type"`
	State   string `json:"state"`
	From    string `json:"from,omitempty"`
	Version string `json:"version,omitempty"`
	Error   string `json:"error,omitempty"`
}

type KeyRevoked struct {
	V         int    `json:"v"`
	Type      string `json:"type"`
	PublicKey string `json:"public_key"` // SPKI base64
}

// ---------- browser <-> server ----------

type SessionCreate struct {
	V           int    `json:"v"`
	Type        string `json:"type"`
	Req         string `json:"req"`
	DeviceID    string `json:"device_id"`
	ClientKeyID string `json:"client_key_id"`
	ForceRelay  bool   `json:"force_relay,omitempty"`
}

type SessionDevice struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	PublicKey string `json:"public_key"` // base64 std, raw 32-byte Ed25519
}

type SessionCreated struct {
	V          int           `json:"v"`
	Type       string        `json:"type"`
	Req        string        `json:"req"`
	SessionID  string        `json:"session_id"`
	ICEServers []ICEServer   `json:"ice_servers"`
	Device     SessionDevice `json:"device"`
	ForceRelay bool          `json:"force_relay,omitempty"` // echoed so the browser sets iceTransportPolicy:"relay"
}

type DeviceStatus struct {
	V          int     `json:"v"`
	Type       string  `json:"type"`
	DeviceID   string  `json:"device_id"`
	Online     bool    `json:"online"`
	Status     string  `json:"status"` // online | offline | disabled | removed
	LastSeenAt *string `json:"last_seen_at,omitempty"`
}

// DeviceUpdateStatus relays a device's update progress to its owner's browsers.
type DeviceUpdateStatus struct {
	V        int    `json:"v"`
	Type     string `json:"type"`
	DeviceID string `json:"device_id"`
	State    string `json:"state"`
	From     string `json:"from,omitempty"`
	Version  string `json:"version,omitempty"`
	Error    string `json:"error,omitempty"`
	At       string `json:"at"` // RFC3339
}

// ---------- relayed ----------

// SessionOffer: browser sends {session_id, sdp, fp_sig}; the server adds
// client_key, user, ice_servers and force_relay before forwarding to the agent.
type SessionOffer struct {
	V          int            `json:"v"`
	Type       string         `json:"type"`
	SessionID  string         `json:"session_id"`
	SDP        string         `json:"sdp"`
	FPSig      string         `json:"fp_sig"` // base64 std, P1363 r||s (64 bytes)
	ClientKey  *ClientKeyInfo `json:"client_key,omitempty"`
	User       *UserInfo      `json:"user,omitempty"`
	ICEServers []ICEServer    `json:"ice_servers,omitempty"`
}

type SessionAnswer struct {
	V         int    `json:"v"`
	Type      string `json:"type"`
	SessionID string `json:"session_id"`
	SDP       string `json:"sdp"`
	FPSig     string `json:"fp_sig"` // base64 std, Ed25519 (64 bytes)
}

type ICECandidateMsg struct {
	V         int           `json:"v"`
	Type      string        `json:"type"`
	SessionID string        `json:"session_id"`
	Candidate *ICECandidate `json:"candidate"`
}

// Session status values sent by the agent while the offer is pending.
const (
	StatusAwaitingApproval = "awaiting_approval"
	StatusApproved         = "approved"
)

type SessionStatus struct {
	V         int    `json:"v"`
	Type      string `json:"type"`
	SessionID string `json:"session_id"`
	Status    string `json:"status"`
}

// ICE path values for session.state and the sessions.ice_path column.
const (
	PathHost  = "host"
	PathSrflx = "srflx"
	PathRelay = "relay"
)

type SessionState struct {
	V         int    `json:"v"`
	Type      string `json:"type"`
	SessionID string `json:"session_id"`
	Connected bool   `json:"connected"`
	Path      string `json:"path"`
	// Selected candidate pair, informational (audit meta).
	LocalCandidate  string `json:"local_candidate,omitempty"`
	RemoteCandidate string `json:"remote_candidate,omitempty"`
}

// End reasons.
const (
	EndUserHangup    = "user_hangup"
	EndAgentKill     = "agent_kill_switch"
	EndNotApproved   = "not_approved"
	EndTimeout       = "timeout"
	EndICEFailed     = "ice_failed"
	EndError         = "error"
	EndPeerGone      = "peer_disconnected"
	EndAdmin         = "admin_terminated"
	EndDeviceOffline = "device_offline"
	EndServerRestart = "server_restart"
	EndBadSignature  = "bad_signature"
	EndPermission    = "permission_missing"
	EndAbuse         = "input_abuse"
	EndAgentUpdate   = "agent_update" // the owner or an admin forced an update
)

type SessionEnd struct {
	V         int    `json:"v"`
	Type      string `json:"type"`
	SessionID string `json:"session_id"`
	Reason    string `json:"reason"`
	BytesSent int64  `json:"bytes_sent,omitempty"` // agent only
	BytesRecv int64  `json:"bytes_recv,omitempty"` // agent only
}

type Ping struct {
	V    int    `json:"v"`
	Type string `json:"type"`
	TS   int64  `json:"ts,omitempty"`
}

// Error codes.
const (
	ErrDeviceOffline    = "device_offline"
	ErrDeviceBusy       = "device_busy"
	ErrDeviceDisabled   = "device_disabled"
	ErrNotFound         = "not_found"
	ErrForbidden        = "forbidden"
	ErrRateLimited      = "rate_limited"
	ErrClientKeyInvalid = "client_key_invalid"
	ErrBadRequest       = "bad_request"
	ErrTooLarge         = "too_large"
	ErrInternal         = "internal"
)

type Error struct {
	V         int    `json:"v"`
	Type      string `json:"type"`
	Req       string `json:"req,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	Code      string `json:"code"`
	Message   string `json:"message"`
}

// Encode stamps v and type and marshals.
func Encode(typ string, msg any) ([]byte, error) {
	b, err := json.Marshal(msg)
	if err != nil {
		return nil, err
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	m["v"] = json.RawMessage(`1`)
	t, _ := json.Marshal(typ)
	m["type"] = t
	return json.Marshal(m)
}

// ---------- file transfer audit (A→S) ----------

// MaxFileRecords caps the records in one SessionFile message.
const MaxFileRecords = 50

// FileRecord describes one finished file transfer of a session.
type FileRecord struct {
	Xfer       uint32 `json:"xfer"`
	Direction  string `json:"direction"` // "upload" (browser → device) | "download"
	Name       string `json:"name"`
	Dir        string `json:"dir"`
	Size       int64  `json:"size"`
	Bytes      int64  `json:"bytes"`
	SHA256     string `json:"sha256,omitempty"`
	Result     string `json:"result"` // "ok" | "cancelled" | "error"
	Error      string `json:"error,omitempty"`
	DurationMS int64  `json:"duration_ms"`
}

type SessionFile struct {
	V         int          `json:"v"`
	Type      string       `json:"type"`
	SessionID string       `json:"session_id"`
	Files     []FileRecord `json:"files"`
}
