package files

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"os"
)

// download streams a regular file within the browser's credit window,
// holding back while the channel buffers too much and on relayed paths.
func (t *transfer) download(m Msg) (code, detail string) {
	s := t.s
	t.setMeta(truncUTF8(m.Path, maxNameBytes), "", 0)
	p, err := CheckPath(m.Path, s.c.goos)
	if err != nil {
		return fail(err)
	}
	name, dir := BaseName(p, s.c.goos), ParentDir(p, s.c.goos)
	t.setMeta(name, dir, 0)
	var f *os.File
	var info fs.FileInfo
	err = s.c.Host.Do(func() error {
		real, err := s.resolve(p)
		if err != nil {
			return err
		}
		if f, err = openRead(real); err != nil {
			return err
		}
		if info, err = f.Stat(); err != nil {
			f.Close()
			return err
		}
		if !info.Mode().IsRegular() {
			f.Close()
			return errNotFile
		}
		return nil
	})
	if err != nil {
		return fail(err)
	}
	defer f.Close()
	size := info.Size()
	t.setMeta(name, dir, size)
	if err := s.sendJSON(beginMsg{T: "begin", Xfer: t.xfer, Name: name, Size: size, MTime: info.ModTime().UnixMilli(), Window: s.c.window}); err != nil {
		return CodeIO, err.Error()
	}
	t.show()

	h := sha256.New()
	buf := make([]byte, MaxChunk)
	var sent int64
	for {
		n, rerr := f.Read(buf)
		if n > 0 {
			if sent+int64(n) > size {
				return CodeChanged, "the file grew while it was read"
			}
			if err := t.waitCredit(n); err != nil {
				return CodeCancelled, ""
			}
			if err := s.waitBuffered(t.ctx); err != nil {
				return CodeIO, err.Error()
			}
			if err := s.pace(t.ctx, n); err != nil {
				return CodeIO, err.Error()
			}
			// Accounted before sending: the ack can arrive before Send returns.
			t.mu.Lock()
			t.sw.add(n)
			t.mu.Unlock()
			if err := s.channel().Send(EncodeData(t.xfer, buf[:n])); err != nil {
				return CodeIO, err.Error()
			}
			h.Write(buf[:n])
			sent += int64(n)
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return fail(rerr)
		}
	}
	if sent != size {
		return CodeChanged, "the file shrank while it was read"
	}
	sha := hex.EncodeToString(h.Sum(nil))
	if err := s.sendJSON(endMsg{T: "end", Xfer: t.xfer, Size: size, SHA256: sha}); err != nil {
		return CodeIO, err.Error()
	}
	if err := t.waitAcked(size); err != nil {
		return CodeCancelled, ""
	}
	if !t.markFinal(sha) {
		return CodeCancelled, ""
	}
	return "", ""
}

// onAck applies the browser's cumulative ack. Called from Handle.
func (t *transfer) onAck(bytes int64) {
	t.mu.Lock()
	if err := t.sw.ack(bytes); err != nil {
		t.mu.Unlock()
		t.abort(CodeProtocol, "ack outside the sent range")
		return
	}
	t.bytes = bytes
	t.progress = t.s.c.Now()
	t.mu.Unlock()
	t.signal()
	t.activity(StateActive, "", false)
}

func (t *transfer) waitCredit(n int) error {
	for {
		t.mu.Lock()
		ok := t.sw.canSend(n)
		t.mu.Unlock()
		if ok {
			return nil
		}
		select {
		case <-t.kick:
		case <-t.ctx.Done():
			return t.ctx.Err()
		}
	}
}

func (t *transfer) waitAcked(size int64) error {
	for {
		t.mu.Lock()
		ok := t.sw.acked >= size
		t.mu.Unlock()
		if ok {
			return nil
		}
		select {
		case <-t.kick:
		case <-t.ctx.Done():
			return t.ctx.Err()
		}
	}
}
