// Package inputproto encodes and decodes the binary data-channel protocol in
// docs/PROTOCOL.md §5 (input + cursor channels). Every decoder range-checks
// its fields; anything malformed returns an error so the session can count it
// toward the abuse threshold.
package inputproto

import (
	"encoding/binary"
	"errors"
	"unicode/utf8"
)

const (
	TypeMouseMove      = 0x01
	TypeMouseButton    = 0x02
	TypeMouseWheel     = 0x03
	TypeKey            = 0x04
	TypeText           = 0x05
	TypeMouseMoveRel   = 0x06
	TypeClipboardBegin = 0x10
	TypeClipboardData  = 0x11
	TypeControl        = 0x20

	TypeCursorShape = 0x30
	TypeCursorPos   = 0x31
)

const (
	OpSelectMonitor   = 1
	OpRequestKeyframe = 2
	OpSetQuality      = 3
	OpReleaseAll      = 4
)

const (
	MimeText = 1
	MimePNG  = 2
)

const (
	MaxText          = 1024
	MaxClipboard     = 1 << 20
	MaxClipboardData = 16384
	MaxButton        = 4
	MaxQualityTier   = 2
)

var (
	ErrEmpty       = errors.New("empty message")
	ErrUnknownType = errors.New("unknown message type")
	ErrLength      = errors.New("bad message length")
	ErrRange       = errors.New("field out of range")
	ErrUTF8        = errors.New("invalid utf-8")
)

// Event is one decoded input-channel message. Only the fields relevant to
// Type are set.
type Event struct {
	Type    uint8
	X, Y    uint16
	Monitor uint8
	DX, DY  int16
	Button  uint8
	Down    bool
	Usage   uint16
	Mods    uint8
	Text    string
	Xfer    uint16
	Mime    uint8
	Total   uint32
	Data    []byte // ClipboardData payload (aliases the input buffer)
	Op      uint8
	Arg     uint8
}

var le = binary.LittleEndian

// Decode parses one input-channel message.
func Decode(b []byte) (Event, error) {
	if len(b) == 0 {
		return Event{}, ErrEmpty
	}
	ev := Event{Type: b[0]}
	p := b[1:]
	switch ev.Type {
	case TypeMouseMove:
		if len(p) != 5 {
			return ev, ErrLength
		}
		ev.X, ev.Y, ev.Monitor = le.Uint16(p), le.Uint16(p[2:]), p[4]
	case TypeMouseMoveRel, TypeMouseWheel:
		if len(p) != 4 {
			return ev, ErrLength
		}
		ev.DX, ev.DY = int16(le.Uint16(p)), int16(le.Uint16(p[2:]))
	case TypeMouseButton:
		if len(p) != 2 {
			return ev, ErrLength
		}
		if p[0] > MaxButton || p[1] > 1 {
			return ev, ErrRange
		}
		ev.Button, ev.Down = p[0], p[1] == 1
	case TypeKey:
		if len(p) != 4 {
			return ev, ErrLength
		}
		if p[2] > 1 {
			return ev, ErrRange
		}
		ev.Usage, ev.Down, ev.Mods = le.Uint16(p), p[2] == 1, p[3]
	case TypeText:
		if len(p) < 1 || len(p) > MaxText {
			return ev, ErrLength
		}
		if !utf8.Valid(p) {
			return ev, ErrUTF8
		}
		ev.Text = string(p)
	case TypeClipboardBegin:
		if len(p) != 7 {
			return ev, ErrLength
		}
		ev.Xfer, ev.Mime, ev.Total = le.Uint16(p), p[2], le.Uint32(p[3:])
		if (ev.Mime != MimeText && ev.Mime != MimePNG) || ev.Total > MaxClipboard {
			return ev, ErrRange
		}
	case TypeClipboardData:
		if len(p) < 3 || len(p) > 2+MaxClipboardData {
			return ev, ErrLength
		}
		ev.Xfer, ev.Data = le.Uint16(p), p[2:]
	case TypeControl:
		if len(p) != 2 {
			return ev, ErrLength
		}
		ev.Op, ev.Arg = p[0], p[1]
		if ev.Op < OpSelectMonitor || ev.Op > OpReleaseAll {
			return ev, ErrRange
		}
		if ev.Op == OpSetQuality && ev.Arg > MaxQualityTier {
			return ev, ErrRange
		}
	default:
		return ev, ErrUnknownType
	}
	return ev, nil
}

// Encode serializes an input-channel event (used by tests and the Go test
// controller; the browser has its own TypeScript encoder).
func Encode(ev Event) []byte {
	switch ev.Type {
	case TypeMouseMove:
		b := []byte{ev.Type, 0, 0, 0, 0, ev.Monitor}
		le.PutUint16(b[1:], ev.X)
		le.PutUint16(b[3:], ev.Y)
		return b
	case TypeMouseMoveRel, TypeMouseWheel:
		b := []byte{ev.Type, 0, 0, 0, 0}
		le.PutUint16(b[1:], uint16(ev.DX))
		le.PutUint16(b[3:], uint16(ev.DY))
		return b
	case TypeMouseButton:
		return []byte{ev.Type, ev.Button, b2u(ev.Down)}
	case TypeKey:
		b := []byte{ev.Type, 0, 0, b2u(ev.Down), ev.Mods}
		le.PutUint16(b[1:], ev.Usage)
		return b
	case TypeText:
		return append([]byte{ev.Type}, ev.Text...)
	case TypeClipboardBegin:
		b := []byte{ev.Type, 0, 0, ev.Mime, 0, 0, 0, 0}
		le.PutUint16(b[1:], ev.Xfer)
		le.PutUint32(b[4:], ev.Total)
		return b
	case TypeClipboardData:
		b := []byte{ev.Type, 0, 0}
		le.PutUint16(b[1:], ev.Xfer)
		return append(b, ev.Data...)
	case TypeControl:
		return []byte{ev.Type, ev.Op, ev.Arg}
	}
	return nil
}

func b2u(v bool) uint8 {
	if v {
		return 1
	}
	return 0
}

// EncodeCursorPos builds a 0x31 CursorPos message.
func EncodeCursorPos(x, y uint16, visible bool) []byte {
	b := []byte{TypeCursorPos, 0, 0, 0, 0, b2u(visible)}
	le.PutUint16(b[1:], x)
	le.PutUint16(b[3:], y)
	return b
}

// EncodeCursorShape builds a 0x30 CursorShape message. An empty png means
// "use the cached shape with this id".
func EncodeCursorShape(id uint32, w, h, hotX, hotY uint16, png []byte) []byte {
	b := make([]byte, 13, 13+len(png))
	b[0] = TypeCursorShape
	le.PutUint32(b[1:], id)
	le.PutUint16(b[5:], w)
	le.PutUint16(b[7:], h)
	le.PutUint16(b[9:], hotX)
	le.PutUint16(b[11:], hotY)
	return append(b, png...)
}

// CursorShape is a decoded 0x30 message.
type CursorShape struct {
	ID         uint32
	W, H       uint16
	HotX, HotY uint16
	PNG        []byte
}

// DecodeCursor parses a cursor-channel message; exactly one of the results is
// meaningful depending on the returned type byte.
func DecodeCursor(b []byte) (typ uint8, shape CursorShape, x, y uint16, visible bool, err error) {
	if len(b) == 0 {
		return 0, shape, 0, 0, false, ErrEmpty
	}
	switch b[0] {
	case TypeCursorPos:
		if len(b) != 6 {
			return b[0], shape, 0, 0, false, ErrLength
		}
		return b[0], shape, le.Uint16(b[1:]), le.Uint16(b[3:]), b[5] == 1, nil
	case TypeCursorShape:
		if len(b) < 13 {
			return b[0], shape, 0, 0, false, ErrLength
		}
		shape = CursorShape{ID: le.Uint32(b[1:]), W: le.Uint16(b[5:]), H: le.Uint16(b[7:]),
			HotX: le.Uint16(b[9:]), HotY: le.Uint16(b[11:]), PNG: b[13:]}
		return b[0], shape, 0, 0, false, nil
	}
	return b[0], shape, 0, 0, false, ErrUnknownType
}

// ClipboardChunks splits a clipboard payload into Begin + Data frames.
func ClipboardChunks(xfer uint16, mime uint8, data []byte) [][]byte {
	out := [][]byte{Encode(Event{Type: TypeClipboardBegin, Xfer: xfer, Mime: mime, Total: uint32(len(data))})}
	for off := 0; off < len(data); off += MaxClipboardData {
		end := min(off+MaxClipboardData, len(data))
		out = append(out, Encode(Event{Type: TypeClipboardData, Xfer: xfer, Data: data[off:end]}))
	}
	return out
}

// ClipboardAssembler reassembles chunked clipboard transfers.
type ClipboardAssembler struct {
	xfer   uint16
	mime   uint8
	total  uint32
	buf    []byte
	active bool
}

// Feed consumes a decoded Begin or Data event. It returns done=true with the
// full payload when a transfer completes; an error for a Data frame with no
// matching Begin or one that overflows the announced total.
func (a *ClipboardAssembler) Feed(ev Event) (done bool, mime uint8, data []byte, err error) {
	switch ev.Type {
	case TypeClipboardBegin:
		a.xfer, a.mime, a.total, a.active = ev.Xfer, ev.Mime, ev.Total, true
		a.buf = make([]byte, 0, ev.Total)
		if ev.Total == 0 {
			a.active = false
			return true, a.mime, nil, nil
		}
		return false, 0, nil, nil
	case TypeClipboardData:
		if !a.active || ev.Xfer != a.xfer {
			return false, 0, nil, ErrRange
		}
		if uint32(len(a.buf)+len(ev.Data)) > a.total {
			a.active = false
			return false, 0, nil, ErrRange
		}
		a.buf = append(a.buf, ev.Data...)
		if uint32(len(a.buf)) == a.total {
			a.active = false
			return true, a.mime, a.buf, nil
		}
		return false, 0, nil, nil
	}
	return false, 0, nil, ErrUnknownType
}
