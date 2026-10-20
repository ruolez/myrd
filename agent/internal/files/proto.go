// Package files implements the `file` data channel (docs/PROTOCOL.md §5):
// browsing the device's file system and streaming uploads and downloads of
// any size, flow-controlled, as the device's signed-in user. The package
// has no WebRTC dependency; the session hands it a Channel.
package files

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
)

const (
	TypeData = 0x40
	MaxChunk = 16384
	// Window is the credit a sender may run ahead of the receiver's last ack.
	Window = 4 << 20
	// AckEvery is the most a receiver writes between two acks.
	AckEvery = 256 << 10

	dataHeader = 5
)

// Error codes of the `error` frame.
const (
	CodeNotFound         = "not_found"
	CodeDenied           = "denied"
	CodeOSDenied         = "os_denied"
	CodeNotDir           = "not_dir"
	CodeNotFile          = "not_file"
	CodeBadName          = "bad_name"
	CodeBadPath          = "bad_path"
	CodeNoSpace          = "no_space"
	CodeIO               = "io"
	CodeChanged          = "changed"
	CodeChecksum         = "checksum"
	CodeProtocol         = "protocol"
	CodeTooMany          = "too_many"
	CodeNoUser           = "no_user"
	CodeAuditUnavailable = "audit_unavailable"
	CodeStalled          = "stalled"
	CodeCancelled        = "cancelled"
)

var (
	ErrDataType   = errors.New("not a file data frame")
	ErrDataLength = errors.New("bad file data length")
)

// EncodeData builds a 0x40 FileData frame. payload must be 1..MaxChunk bytes.
func EncodeData(xfer uint32, payload []byte) []byte {
	b := make([]byte, dataHeader+len(payload))
	b[0] = TypeData
	binary.LittleEndian.PutUint32(b[1:], xfer)
	copy(b[dataHeader:], payload)
	return b
}

// DecodeData parses a FileData frame; payload aliases b.
func DecodeData(b []byte) (xfer uint32, payload []byte, err error) {
	if len(b) == 0 || b[0] != TypeData {
		return 0, nil, ErrDataType
	}
	if len(b) <= dataHeader || len(b) > dataHeader+MaxChunk {
		return 0, nil, ErrDataLength
	}
	return binary.LittleEndian.Uint32(b[1:]), b[dataHeader:], nil
}

// Msg is a decoded browser → agent JSON frame. Only the fields of T are set.
type Msg struct {
	T      string `json:"t"`
	ID     uint32 `json:"id"`
	Xfer   uint32 `json:"xfer"`
	Path   string `json:"path"`
	Offset int    `json:"offset"`
	Hidden bool   `json:"hidden"`
	Dir    string `json:"dir"`
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
}

// required lists the keys each browser → agent frame must carry.
var required = map[string][]string{
	"roots":  {"id"},
	"list":   {"id", "path"},
	"get":    {"xfer", "path"},
	"put":    {"xfer", "name", "size"},
	"ack":    {"xfer", "bytes"},
	"end":    {"xfer", "sha256"},
	"cancel": {"xfer"},
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
	switch {
	case m.Offset < 0:
		return Msg{}, errors.New("negative offset")
	case m.Size < 0 || m.Bytes < 0:
		return Msg{}, errors.New("negative size")
	case m.T == "end" && !isSHA256Hex(m.SHA256):
		return Msg{}, errors.New("sha256 must be 64 lowercase hex digits")
	}
	return m, nil
}

func isSHA256Hex(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// ---------- agent → browser frames ----------

type Root struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Kind string `json:"kind"` // home | downloads | desktop | documents | volume | drive
}

type rootsMsg struct {
	T          string `json:"t"`
	ID         uint32 `json:"id"`
	Roots      []Root `json:"roots"`
	DefaultDir string `json:"default_dir"`
	Sep        string `json:"sep"`
}

type listingMsg struct {
	T          string  `json:"t"`
	ID         uint32  `json:"id"`
	Path       string  `json:"path"`
	Parent     string  `json:"parent"`
	Entries    []Entry `json:"entries"`
	Offset     int     `json:"offset"`
	NextOffset *int    `json:"next_offset"`
	Total      int     `json:"total"`
	Truncated  bool    `json:"truncated"`
}

type beginMsg struct {
	T      string `json:"t"`
	Xfer   uint32 `json:"xfer"`
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	MTime  int64  `json:"mtime"`
	Window int64  `json:"window"`
}

type readyMsg struct {
	T      string `json:"t"`
	Xfer   uint32 `json:"xfer"`
	Name   string `json:"name"`
	Path   string `json:"path"`
	Window int64  `json:"window"`
}

type ackMsg struct {
	T     string `json:"t"`
	Xfer  uint32 `json:"xfer"`
	Bytes int64  `json:"bytes"`
}

type endMsg struct {
	T      string `json:"t"`
	Xfer   uint32 `json:"xfer"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type doneMsg struct {
	T      string `json:"t"`
	Xfer   uint32 `json:"xfer"`
	Name   string `json:"name"`
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type errorMsg struct {
	T      string  `json:"t"`
	ID     *uint32 `json:"id,omitempty"`
	Xfer   *uint32 `json:"xfer,omitempty"`
	Code   string  `json:"code"`
	Detail string  `json:"detail"`
}
