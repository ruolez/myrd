// Package capproc runs screen capture and encoding in a child process, one
// per session: when the session ends the worker exits and the OS takes back
// everything the encoder held (a 4K full-color encoder alone is ~800 MB),
// including what an allocator or a graphics driver would otherwise keep.
//
// The agent and the worker talk over the worker's stdin and stdout. Each
// message is a 4-byte big-endian length, a kind byte and a body; control
// bodies are JSON, frames and cursor updates are binary.
package capproc

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/ruolez/remotectl/agent/internal/platform"
)

// Agent → worker.
const (
	kindStart     byte = 1 // ctl{Seq, Config}
	kindStop      byte = 2 // ctl{Seq}
	kindConfigure byte = 3 // ctl{Kbps, FPS}
	kindKeyframe  byte = 4 // empty
	kindRefine    byte = 5 // ctl{Seq, QP}
)

// Worker → agent.
const (
	kindReply  byte = 16 // ctl{Seq, Code, Msg, Encoder}
	kindFrame  byte = 17 // pts int64, flags uint8, access unit
	kindShape  byte = 18 // id uint32, w, h, hotX, hotY uint16, RGBA
	kindPos    byte = 19 // x, y uint16, visible uint8
	kindStatus byte = 20 // ctl{Encoder, Mem}
)

// maxMsg bounds one message: a keyframe of an 8K screen at the top bitrate
// stays far below it.
const maxMsg = 64 << 20

type ctl struct {
	Seq     uint32                  `json:"seq,omitempty"`
	Config  *platform.CaptureConfig `json:"cfg,omitempty"`
	Kbps    int                     `json:"kbps,omitempty"`
	FPS     int                     `json:"fps,omitempty"`
	QP      int                     `json:"qp,omitempty"`
	Code    int                     `json:"code,omitempty"`
	Msg     string                  `json:"msg,omitempty"`
	Encoder string                  `json:"encoder,omitempty"`
	Mem     uint64                  `json:"mem,omitempty"`
}

func writeMsg(w io.Writer, kind byte, body []byte) error {
	var hdr [5]byte
	binary.BigEndian.PutUint32(hdr[:4], uint32(len(body)))
	hdr[4] = kind
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	_, err := w.Write(body)
	return err
}

func writeCtl(w io.Writer, kind byte, c ctl) error {
	body, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return writeMsg(w, kind, body)
}

func readMsg(r io.Reader) (byte, []byte, error) {
	var hdr [5]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return 0, nil, err
	}
	n := binary.BigEndian.Uint32(hdr[:4])
	if n > maxMsg {
		return 0, nil, fmt.Errorf("capture worker message of %d bytes", n)
	}
	if n == 0 {
		return hdr[4], nil, nil
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		return 0, nil, err
	}
	return hdr[4], body, nil
}

func frameBody(pts int64, flags platform.FrameFlags, data []byte) []byte {
	b := make([]byte, 9+len(data))
	binary.BigEndian.PutUint64(b, uint64(pts))
	b[8] = byte(flags)
	copy(b[9:], data)
	return b
}

func parseFrame(b []byte) (pts int64, flags platform.FrameFlags, data []byte, ok bool) {
	if len(b) < 9 {
		return 0, 0, nil, false
	}
	return int64(binary.BigEndian.Uint64(b)), platform.FrameFlags(b[8]), b[9:], true
}

func shapeBody(id uint32, w, h, hotX, hotY uint16, rgba []byte) []byte {
	b := make([]byte, 12+len(rgba))
	binary.BigEndian.PutUint32(b, id)
	binary.BigEndian.PutUint16(b[4:], w)
	binary.BigEndian.PutUint16(b[6:], h)
	binary.BigEndian.PutUint16(b[8:], hotX)
	binary.BigEndian.PutUint16(b[10:], hotY)
	copy(b[12:], rgba)
	return b
}

func posBody(x, y uint16, visible bool) []byte {
	b := make([]byte, 5)
	binary.BigEndian.PutUint16(b, x)
	binary.BigEndian.PutUint16(b[2:], y)
	if visible {
		b[4] = 1
	}
	return b
}

// wireErrs are the platform errors a reply carries by index; code -1 is any
// other error, with its text.
var wireErrs = []error{nil, platform.ErrPermission, platform.ErrUnsupported, platform.ErrBusy, platform.ErrLocked,
	platform.ErrAsleep}

func errCode(err error) (int, string) {
	for i, e := range wireErrs {
		if err == e || (e != nil && errors.Is(err, e)) {
			return i, ""
		}
	}
	return -1, err.Error()
}

func codeErr(code int, msg string) error {
	if code >= 0 && code < len(wireErrs) {
		return wireErrs[code]
	}
	return errors.New(msg)
}
