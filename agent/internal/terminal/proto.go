// Package terminal implements the `term` data channel (docs/PROTOCOL.md §5):
// shells the agent starts and keeps, and terminals already open on the
// device (tmux, screen) that the browser takes over. Every source is a VT
// byte stream, flow-controlled per terminal. The package has no WebRTC
// dependency; the session hands it a Channel.
package terminal

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
)

const (
	TypeOutput = 0x50 // agent → browser: terminal output
	TypeInput  = 0x51 // browser → agent: typed bytes

	MaxChunk = 16384
	// Window is the output a terminal may run ahead of the browser's last
	// ack. Small on purpose: a `cat` of a huge file blocks the program on
	// the device instead of flooding a phone.
	Window = 256 << 10
	// AckEvery is the most a browser renders between two acks.
	AckEvery = 32 << 10

	MaxTerms = 8 // terminals open at once on one channel
	MaxCols  = 1000
	MaxRows  = 500

	dataHeader = 3
)

// Error codes of the `error` frame.
const (
	CodeNotFound         = "not_found"
	CodeDenied           = "denied"
	CodeNoUser           = "no_user"
	CodeUnsupported      = "unsupported"
	CodeTooMany          = "too_many"
	CodeAuditUnavailable = "audit_unavailable"
	CodeProtocol         = "protocol"
	CodeSpawn            = "spawn_failed"
)

// Reasons of the `closed` frame.
const (
	ReasonDetached     = "detached"
	ReasonExited       = "exited"
	ReasonKilled       = "killed"
	ReasonSessionEnded = "session_ended"
)

var (
	ErrDataType   = errors.New("not a terminal data frame")
	ErrDataLength = errors.New("bad terminal data length")
	ErrCredit     = errors.New("flow-control violation")
)

// EncodeData builds a data frame: typ | term u16 LE | payload (1..MaxChunk).
func EncodeData(typ byte, term uint16, payload []byte) []byte {
	b := make([]byte, dataHeader+len(payload))
	b[0] = typ
	binary.LittleEndian.PutUint16(b[1:], term)
	copy(b[dataHeader:], payload)
	return b
}

// DecodeData parses a data frame of type typ; payload aliases b.
func DecodeData(typ byte, b []byte) (term uint16, payload []byte, err error) {
	if len(b) == 0 || b[0] != typ {
		return 0, nil, ErrDataType
	}
	if len(b) <= dataHeader || len(b) > dataHeader+MaxChunk {
		return 0, nil, ErrDataLength
	}
	return binary.LittleEndian.Uint16(b[1:]), b[dataHeader:], nil
}

// Msg is a decoded browser → agent JSON frame. Only the fields of T are set.
type Msg struct {
	T      string `json:"t"`
	ID     uint32 `json:"id"`
	Term   uint16 `json:"term"`
	Source string `json:"source"`
	Shell  string `json:"shell"`
	Admin  bool   `json:"admin"`
	Cols   int    `json:"cols"`
	Rows   int    `json:"rows"`
	Bytes  int64  `json:"bytes"`
}

// required lists the keys each browser → agent frame must carry.
var required = map[string][]string{
	"list":   {"id"},
	"open":   {"id", "cols", "rows"},
	"attach": {"id", "source", "cols", "rows"},
	"resize": {"term", "cols", "rows"},
	"ack":    {"term", "bytes"},
	"detach": {"term"},
	"kill":   {"term"},
}

// DecodeMsg parses and range-checks a browser → agent JSON frame.
func DecodeMsg(b []byte) (Msg, error) {
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(b, &keys); err != nil {
		return Msg{}, err
	}
	var m Msg
	if err := json.Unmarshal(b, &m); err != nil {
		return Msg{}, err
	}
	req, ok := required[m.T]
	if !ok {
		return Msg{}, fmt.Errorf("unknown message %q", m.T)
	}
	for _, k := range req {
		if v, ok := keys[k]; !ok || string(v) == "null" {
			return Msg{}, fmt.Errorf("%s: missing %s", m.T, k)
		}
	}
	if _, sized := keys["cols"]; sized && (m.Cols < 1 || m.Cols > MaxCols || m.Rows < 1 || m.Rows > MaxRows) {
		return Msg{}, fmt.Errorf("%s: size %dx%d out of range", m.T, m.Cols, m.Rows)
	}
	if m.Bytes < 0 {
		return Msg{}, errors.New("negative bytes")
	}
	return m, nil
}

// ---------- agent → browser frames ----------

// Source is a terminal the browser can open or take over.
type Source struct {
	ID       string `json:"id"`   // "shell:3", "tmux:main", "screen:1234.work"
	Kind     string `json:"kind"` // proto.TermSource*
	Title    string `json:"title"`
	Detail   string `json:"detail,omitempty"`  // working directory, window count…
	Attached int    `json:"attached"`          // viewers right now (browsers and local clients)
	Admin    bool   `json:"admin,omitempty"`   // runs elevated
	Fidelity string `json:"fidelity"`          // "full" | "mirror"
	Created  int64  `json:"created,omitempty"` // unix ms
}

// Shell is a program a new terminal can start.
type Shell struct {
	ID   string `json:"id"`   // passed back as open.shell
	Name string `json:"name"` // "zsh", "PowerShell"
	Path string `json:"path"`
}

type sourcesMsg struct {
	T       string   `json:"t"`
	ID      uint32   `json:"id"`
	Sources []Source `json:"sources"`
	Shells  []Shell  `json:"shells"`
	Admin   bool     `json:"admin"` // open.admin is possible
}

type openedMsg struct {
	T      string `json:"t"`
	ID     uint32 `json:"id"`
	Term   uint16 `json:"term"`
	Source Source `json:"source"`
	Window int64  `json:"window"`
}

type closedMsg struct {
	T        string `json:"t"`
	Term     uint16 `json:"term"`
	Reason   string `json:"reason"`
	ExitCode *int   `json:"exit_code,omitempty"`
}

type titleMsg struct {
	T     string `json:"t"`
	Term  uint16 `json:"term"`
	Title string `json:"title"`
}

type errorMsg struct {
	T      string  `json:"t"`
	ID     *uint32 `json:"id,omitempty"`
	Term   *uint16 `json:"term,omitempty"`
	Code   string  `json:"code"`
	Detail string  `json:"detail"`
}
