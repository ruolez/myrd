package terminal

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// A terminal program in another process (`remote-agent term-host` on
// Windows) or a local viewer (`remote-agent attach`) is driven over a byte
// stream pair: output flows raw, input and size changes travel the other
// way as frames of a type byte, a big-endian u16 length and the payload.
const (
	frameInput  = 'i'
	frameResize = 'r' // u16 cols, u16 rows
	// frameOpen is the first frame of a local viewer: the Source.ID to
	// join, or "" for a new shell.
	frameOpen = 'o'

	maxFramePayload = 0xffff
)

var errBadFrame = errors.New("terminal: bad frame")

func writeFrame(w io.Writer, typ byte, p []byte) error {
	if len(p) > maxFramePayload {
		return errBadFrame
	}
	b := make([]byte, 3+len(p))
	b[0] = typ
	binary.BigEndian.PutUint16(b[1:], uint16(len(p)))
	copy(b[3:], p)
	_, err := w.Write(b)
	return err
}

// WriteInput sends p as input frames.
func WriteInput(w io.Writer, p []byte) error {
	for len(p) > 0 {
		n := min(len(p), maxFramePayload)
		if err := writeFrame(w, frameInput, p[:n]); err != nil {
			return err
		}
		p = p[n:]
	}
	return nil
}

// WriteResize sends a size change.
func WriteResize(w io.Writer, cols, rows int) error {
	if cols < 1 || rows < 1 || cols > 0xffff || rows > 0xffff {
		return errBadFrame
	}
	var b [4]byte
	binary.BigEndian.PutUint16(b[0:], uint16(cols))
	binary.BigEndian.PutUint16(b[2:], uint16(rows))
	return writeFrame(w, frameResize, b[:])
}

// WriteOpen asks a local terminal server for the terminal id ("" = a new
// shell); it is the first frame a local viewer sends, after its size.
func WriteOpen(w io.Writer, id string) error { return writeFrame(w, frameOpen, []byte(id)) }

// Frame is one frame read by ReadFrame.
type Frame struct {
	Type    byte
	Payload []byte
}

// Size decodes a resize frame.
func (f Frame) Size() (cols, rows int, ok bool) {
	if f.Type != frameResize || len(f.Payload) != 4 {
		return 0, 0, false
	}
	cols = int(binary.BigEndian.Uint16(f.Payload[0:]))
	rows = int(binary.BigEndian.Uint16(f.Payload[2:]))
	return cols, rows, cols > 0 && rows > 0
}

func (f Frame) IsInput() bool { return f.Type == frameInput }
func (f Frame) IsOpen() bool  { return f.Type == frameOpen }

// ReadFrame reads the next frame.
func ReadFrame(r *bufio.Reader) (Frame, error) {
	var h [3]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		return Frame{}, err
	}
	n := int(binary.BigEndian.Uint16(h[1:]))
	p := make([]byte, n)
	if _, err := io.ReadFull(r, p); err != nil {
		if errors.Is(err, io.EOF) {
			err = io.ErrUnexpectedEOF
		}
		return Frame{}, err
	}
	switch h[0] {
	case frameInput, frameResize, frameOpen:
	default:
		return Frame{}, fmt.Errorf("%w: type %#x", errBadFrame, h[0])
	}
	return Frame{Type: h[0], Payload: p}, nil
}

// Console is the end of a frame stream that applies the frames.
type Console interface {
	Write(p []byte) (int, error)
	Resize(cols, rows int) error
}

// ServeFrames applies input and resize frames from r to c until r ends
// (nil) or fails.
func ServeFrames(r io.Reader, c Console) error {
	br := bufio.NewReader(r)
	for {
		f, err := ReadFrame(br)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		switch {
		case f.IsInput():
			if _, err := c.Write(f.Payload); err != nil {
				return err
			}
		default:
			if cols, rows, ok := f.Size(); ok {
				_ = c.Resize(cols, rows)
			}
		}
	}
}
