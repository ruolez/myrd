package terminal

import "bytes"

// RingSize is how much recent output a kept shell holds for viewers that
// attach later.
const RingSize = 256 << 10

// ring keeps the last len(buf) bytes of a stream; positions are absolute
// stream offsets.
type ring struct {
	buf   []byte
	total int64 // bytes ever written
}

func newRing(size int) *ring { return &ring{buf: make([]byte, size)} }

func (r *ring) write(p []byte) {
	n := int64(len(r.buf))
	if int64(len(p)) > n {
		r.total += int64(len(p)) - n
		p = p[int64(len(p))-n:]
	}
	at := int(r.total % n)
	k := copy(r.buf[at:], p)
	copy(r.buf, p[k:])
	r.total += int64(len(p))
}

// start is the oldest offset still held.
func (r *ring) start() int64 {
	if r.total < int64(len(r.buf)) {
		return 0
	}
	return r.total - int64(len(r.buf))
}

// since returns the bytes from absolute offset from (clamped to what is held).
func (r *ring) since(from int64) []byte {
	if s := r.start(); from < s {
		from = s
	}
	if from >= r.total {
		return nil
	}
	n := int64(len(r.buf))
	out := make([]byte, 0, r.total-from)
	for p := from; p < r.total; {
		at := p % n
		end := n
		if rem := r.total - p; rem < n-at {
			end = at + rem
		}
		out = append(out, r.buf[at:end]...)
		p += end - at
	}
	return out
}

// replay is what a newly attached viewer receives: the held output (cut to
// a line start when the ring has wrapped, so it never begins inside an
// escape sequence or a character), with the alternate screen re-entered if
// its switch was lost, then the tracked modes.
func replay(r *ring, v *vtState) []byte {
	from := r.start()
	if v.alt && v.altAt >= from {
		// Only the full-screen program's own output; the program redraws on
		// the resize nudge anyway.
		from = v.altAt
	}
	body := r.since(from)
	if from > 0 && !(v.alt && from == v.altAt) {
		if i := bytes.IndexByte(body, '\n'); i >= 0 {
			body = body[i+1:]
		} else {
			body = nil
		}
	}
	var out []byte
	if v.alt && v.altAt < from {
		out = append(out, "\x1b[?1049h\x1b[H\x1b[2J"...)
	}
	out = append(out, body...)
	return append(out, v.restore()...)
}
