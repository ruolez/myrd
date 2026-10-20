package files

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// upload receives a file into dir: written to a hidden part file, verified
// against the browser's SHA-256 and only then renamed to its final name,
// never replacing an existing file.
func (t *transfer) upload(m Msg) (code, detail string) {
	s := t.s
	t.setMeta(truncUTF8(m.Name, maxNameBytes), truncUTF8(m.Dir, 4096), m.Size)
	dir := m.Dir
	if dir == "" {
		_, def, err := s.c.Host.Roots()
		if err != nil {
			return fail(err)
		}
		dir = def
	}
	p, err := CheckPath(dir, s.c.goos)
	if err != nil {
		return fail(err)
	}
	t.setMeta(truncUTF8(m.Name, maxNameBytes), p, m.Size)
	name, err := SanitizeName(m.Name)
	if err != nil {
		return fail(err)
	}

	var f *os.File
	var real, final string
	err = s.c.Host.Do(func() error {
		var err error
		if real, err = s.resolve(p); err != nil {
			return err
		}
		if st, err := os.Stat(real); err != nil {
			return err
		} else if !st.IsDir() {
			return errNotDir
		}
		if final, err = s.reserveName(real, name); err != nil {
			return err
		}
		part := filepath.Join(real, PartName(final, t.xfer))
		f, err = createPart(part)
		if errors.Is(err, fs.ErrExist) {
			// Left by an agent that died mid-upload: xfer ids restart with
			// every browser session, and this service runs each id once.
			if os.Remove(part) == nil {
				f, err = createPart(part)
			}
		}
		if err != nil {
			s.release(real, final)
		}
		return err
	})
	if err != nil {
		return fail(err)
	}
	part := filepath.Join(real, PartName(final, t.xfer))
	committed := false
	defer func() {
		if !committed {
			_ = f.Close()
			_ = s.c.Host.Do(func() error { return os.Remove(part) })
		}
		s.release(real, final)
	}()
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.sweepStale(real)
	}()

	t.setMeta(final, p, m.Size)
	if err := s.sendJSON(readyMsg{T: "ready", Xfer: t.xfer, Name: final, Path: JoinPath(p, final, s.c.goos), Window: s.c.window}); err != nil {
		return CodeIO, err.Error()
	}
	t.show()

	h := sha256.New()
	for {
		bufs, end, sha, err := t.take()
		if err != nil {
			return CodeCancelled, ""
		}
		for _, b := range bufs {
			if _, err := f.Write(b); err != nil {
				return fail(err)
			}
			h.Write(b)
			if v, due := t.wrote(len(b)); due {
				_ = s.sendJSON(ackMsg{T: "ack", Xfer: t.xfer, Bytes: v})
			}
		}
		t.activity(StateActive, "", false)
		if !end {
			continue
		}
		written := t.ackAll()
		if written != m.Size {
			return CodeProtocol, fmt.Sprintf("received %d of %d bytes", written, m.Size)
		}
		got := hex.EncodeToString(h.Sum(nil))
		if got != sha {
			return CodeChecksum, "sha256 mismatch"
		}
		if err := f.Sync(); err != nil {
			return fail(err)
		}
		if err := f.Close(); err != nil {
			return fail(err)
		}
		_ = s.sendJSON(ackMsg{T: "ack", Xfer: t.xfer, Bytes: written})
		if !t.markFinal(got) {
			return CodeCancelled, ""
		}
		err = s.c.Host.Do(func() error {
			for i := 0; ; i++ {
				err := renameNoReplace(part, filepath.Join(real, final))
				if err == nil || !errors.Is(err, fs.ErrExist) || i == 4 {
					return err
				}
				// Someone created the name since `ready`: take the next free one.
				next, rerr := s.reserveName(real, name)
				if rerr != nil {
					return rerr
				}
				s.release(real, final)
				final = next
			}
		})
		if err != nil {
			return fail(err)
		}
		committed = true
		t.setMeta(final, p, m.Size)
		_ = s.sendJSON(doneMsg{T: "done", Xfer: t.xfer, Name: final, Path: JoinPath(p, final, s.c.goos), Size: written, SHA256: got})
		return "", ""
	}
}

func createPart(p string) (*os.File, error) {
	return os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
}

// push queues a data frame for the writer, enforcing the credit window and
// the declared size. Called from Handle; never blocks.
func (t *transfer) push(p []byte) {
	t.mu.Lock()
	if t.final || t.abortCode != "" {
		t.mu.Unlock()
		return
	}
	problem := ""
	switch {
	case t.endSeen:
		problem = "data after end"
	case t.rw.receive(len(p)) != nil:
		problem = "flow-control window exceeded"
	case t.rw.received > t.size:
		problem = "more data than the declared size"
	}
	if problem != "" {
		t.mu.Unlock()
		t.abort(CodeProtocol, problem)
		return
	}
	if n := len(t.queue); n > 0 && len(t.queue[n-1])+len(p) <= coalesce {
		t.queue[n-1] = append(t.queue[n-1], p...)
	} else {
		b := make([]byte, len(p), max(coalesce, len(p)))
		copy(b, p)
		t.queue = append(t.queue, b)
	}
	t.mu.Unlock()
	t.signal()
}

func (t *transfer) onEnd(sha string) {
	t.mu.Lock()
	dup := t.endSeen
	t.endSeen, t.endSHA = true, sha
	t.mu.Unlock()
	if dup {
		t.abort(CodeProtocol, "duplicate end")
		return
	}
	t.signal()
}

// take waits for queued data and/or the end marker.
func (t *transfer) take() (bufs [][]byte, end bool, sha string, err error) {
	for {
		if err := t.ctx.Err(); err != nil {
			return nil, false, "", err
		}
		t.mu.Lock()
		if len(t.queue) > 0 || t.endSeen {
			bufs, t.queue = t.queue, nil
			end, sha = t.endSeen, t.endSHA
			t.mu.Unlock()
			return bufs, end, sha, nil
		}
		t.mu.Unlock()
		select {
		case <-t.kick:
		case <-t.ctx.Done():
		}
	}
}

// wrote accounts bytes written to disk; v is the ack to send when due.
func (t *transfer) wrote(n int) (v int64, due bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	due = t.rw.wrote(n)
	t.bytes = t.rw.written
	t.progress = t.s.c.Now()
	if due {
		v = t.rw.acked()
	}
	return v, due
}

// ackAll marks everything written as acked and returns the byte count.
func (t *transfer) ackAll() int64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.rw.acked()
}
