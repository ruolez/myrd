// Package winsvc implements the Windows process model: a LocalSystem service
// ("RemoteAgent") that owns enrollment, updates and the signaling socket, and
// a helper (`remote-agent.exe helper`, the doc's agent-session.exe) spawned
// into the active console session that owns capture, encode, input, the
// tray and the consent prompt. They talk over two one-way named pipes
// carrying length-prefixed JSON frames (synchronous pipe handles serialize
// reads and writes, so one duplex pipe would deadlock).
//
// frame.go, proxy.go and link.go are OS-independent and unit-tested on every
// host; the *_windows.go files hold the pipes, process spawning and service.
package winsvc

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/ruolez/remotectl/proto"
)

// MaxFrameBytes bounds one frame (signaling messages are ≤ 64 KB).
const MaxFrameBytes = 1 << 20

// Frame kinds.
const (
	// service → helper
	KindSignal     = "signal"      // Type = proto msg type, Data = raw signaling JSON
	KindEndAll     = "end_all"     // Data = reason string
	KindRevokedSet = "revoked_set" // Data = []string (SPKI b64)
	KindRevokedAdd = "revoked_add" // Data = string (SPKI b64)
	KindDevice     = "device"      // Data = proto.DeviceInfo
	KindStatus     = "status"      // Data = ServiceStatus
	KindShutdown   = "shutdown"    // helper exits cleanly

	// helper → service
	KindOut    = "out"    // Type = proto msg type, Data = message JSON for the server
	KindCaps   = "caps"   // Data = HelperCaps
	KindActive = "active" // Data = []string session ids with a live peer connection
	KindQuit   = "quit"   // owner chose Quit in the tray: stop the service
)

type Frame struct {
	Kind string          `json:"k"`
	Type string          `json:"t,omitempty"`
	Data json.RawMessage `json:"d,omitempty"`
}

// HelperCaps is what the helper reports for hello / permissions.
type HelperCaps struct {
	Capabilities proto.Capabilities `json:"capabilities"`
	Permissions  proto.Permissions  `json:"permissions"`
	SessionID    uint32             `json:"session_id"` // Windows console session it runs in
	DeviceKey    string             `json:"device_key"` // Ed25519 public key (base64) the helper signs with
}

// ServiceStatus drives the helper's tray.
type ServiceStatus struct {
	Enrolled bool   `json:"enrolled"`
	Online   bool   `json:"online"`
	Status   string `json:"status"`
}

func newFrame(kind, typ string, v any) (Frame, error) {
	f := Frame{Kind: kind, Type: typ}
	if v == nil {
		return f, nil
	}
	if raw, ok := v.(json.RawMessage); ok {
		f.Data = raw
		return f, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return f, err
	}
	f.Data = b
	return f, nil
}

// WriteFrame writes a 4-byte little-endian length followed by the JSON frame.
func WriteFrame(w io.Writer, f Frame) error {
	b, err := json.Marshal(f)
	if err != nil {
		return err
	}
	if len(b) > MaxFrameBytes {
		return fmt.Errorf("winsvc: frame of %d bytes exceeds limit", len(b))
	}
	buf := make([]byte, 4+len(b))
	binary.LittleEndian.PutUint32(buf, uint32(len(b)))
	copy(buf[4:], b)
	_, err = w.Write(buf)
	return err
}

var ErrFrameTooLarge = errors.New("winsvc: frame exceeds limit")

// ReadFrame reads one frame written by WriteFrame.
func ReadFrame(r io.Reader) (Frame, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return Frame{}, err
	}
	n := binary.LittleEndian.Uint32(hdr[:])
	if n > MaxFrameBytes {
		return Frame{}, ErrFrameTooLarge
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(r, b); err != nil {
		return Frame{}, err
	}
	var f Frame
	if err := json.Unmarshal(b, &f); err != nil {
		return Frame{}, err
	}
	return f, nil
}
