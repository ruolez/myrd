package terminal

import (
	"sort"
	"strconv"
	"strings"
)

// trackedModes are the DEC private modes a reattaching viewer must be put
// back into: they change how keys, paste and the mouse are encoded and which
// screen is shown, and their "set" sequence is usually long gone from the
// replay buffer.
var trackedModes = map[int]bool{
	1:    true, // DECCKM: application cursor keys
	25:   true, // DECTCEM: cursor visible (default on)
	1000: true, // mouse: press/release
	1002: true, // mouse: button motion
	1003: true, // mouse: any motion
	1005: true, // mouse: UTF-8 coordinates
	1006: true, // mouse: SGR coordinates
	2004: true, // bracketed paste
}

// altModes switch to the alternate screen (full-screen programs).
var altModes = map[int]bool{47: true, 1047: true, 1049: true}

const maxTitle = 256

// vtState follows a terminal's output just enough to restore a viewer:
// private modes, the alternate screen and the window title (OSC 0 / 2).
// Sequences may be split across feed calls.
type vtState struct {
	modes map[int]bool // on, other than the defaults
	alt   bool
	// altAt is the output offset where the alternate screen was entered.
	altAt int64
	title string

	pos   int64 // bytes fed so far
	st    int   // parser state
	buf   []byte
	osc   []byte
	inOSC bool
}

const (
	vtGround = iota
	vtEsc
	vtCSI
	vtOSC
	vtOSCEsc
)

func newVTState() *vtState { return &vtState{modes: map[int]bool{25: true}} }

// feed scans output. It reports whether the title changed.
func (v *vtState) feed(b []byte) (titleChanged bool) {
	for _, c := range b {
		v.pos++
		switch v.st {
		case vtGround:
			if c == 0x1b {
				v.st = vtEsc
			}
		case vtEsc:
			switch c {
			case '[':
				v.st, v.buf = vtCSI, v.buf[:0]
			case ']':
				v.st, v.osc = vtOSC, v.osc[:0]
			case 0x1b:
			default:
				v.st = vtGround
			}
		case vtCSI:
			switch {
			case c >= 0x40 && c <= 0x7e:
				v.csi(c)
				v.st = vtGround
			case c == 0x1b:
				v.st = vtEsc
			case len(v.buf) < 64:
				v.buf = append(v.buf, c)
			}
		case vtOSC:
			switch c {
			case 0x07:
				titleChanged = v.oscDone() || titleChanged
				v.st = vtGround
			case 0x1b:
				v.st = vtOSCEsc
			default:
				if len(v.osc) < maxTitle+8 {
					v.osc = append(v.osc, c)
				}
			}
		case vtOSCEsc:
			if c == '\\' {
				titleChanged = v.oscDone() || titleChanged
				v.st = vtGround
			} else {
				v.st = vtOSC
				if c == ']' {
					v.osc = v.osc[:0]
				}
			}
		}
	}
	return titleChanged
}

func (v *vtState) csi(final byte) {
	if final != 'h' && final != 'l' || len(v.buf) == 0 || v.buf[0] != '?' {
		return
	}
	on := final == 'h'
	for _, part := range strings.Split(string(v.buf[1:]), ";") {
		n, err := strconv.Atoi(part)
		if err != nil {
			continue
		}
		switch {
		case altModes[n]:
			if on && !v.alt {
				// The sequence started len(buf)+3 bytes back (ESC [ … final).
				v.altAt = v.pos - int64(len(v.buf)) - 3
			}
			v.alt = on
		case trackedModes[n]:
			if on {
				v.modes[n] = true
			} else {
				delete(v.modes, n)
			}
		}
	}
}

func (v *vtState) oscDone() bool {
	s := string(v.osc)
	i := strings.IndexByte(s, ';')
	if i < 0 {
		return false
	}
	if code := s[:i]; code != "0" && code != "2" {
		return false
	}
	t := sanitizeTitle(s[i+1:])
	if t == v.title {
		return false
	}
	v.title = t
	return true
}

// sanitizeTitle drops control characters and caps the length.
func sanitizeTitle(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			continue
		}
		b.WriteRune(r)
		if b.Len() >= maxTitle {
			break
		}
	}
	return strings.TrimSpace(b.String())
}

// restore returns the sequences that put a fresh terminal into the tracked
// modes; written after the replay, they win over whatever it contained.
func (v *vtState) restore() []byte {
	var b strings.Builder
	ns := make([]int, 0, len(v.modes))
	for n := range v.modes {
		if n != 25 {
			ns = append(ns, n)
		}
	}
	sort.Ints(ns)
	for _, n := range ns {
		b.WriteString("\x1b[?" + strconv.Itoa(n) + "h")
	}
	if !v.modes[25] {
		b.WriteString("\x1b[?25l")
	}
	return []byte(b.String())
}
