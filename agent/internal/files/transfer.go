package files

import (
	"context"
	"sync"
	"time"

	"github.com/ruolez/remotectl/proto"
)

// transfer is one upload or download. Its goroutine (run) owns the file;
// Handle feeds it through push/onEnd/onAck and stops it through abort.
type transfer struct {
	s       *Service
	xfer    uint32
	kind    string
	ctx     context.Context
	cancel  context.CancelFunc
	started time.Time
	kick    chan struct{} // cap 1: data, end or ack arrived

	mu          sync.Mutex
	abortCode   string
	abortDetail string
	final       bool // committed (upload) or fully acked (download); aborts no longer apply
	name, dir   string
	size, bytes int64
	sha         string
	replaced    bool
	progress    time.Time
	shown       bool
	lastShown   time.Time

	rw      recvWindow // upload
	queue   [][]byte
	endSeen bool
	endSHA  string

	sw sendWindow // download
}

func (t *transfer) signal() {
	select {
	case t.kick <- struct{}{}:
	default:
	}
}

// abort stops the transfer with code unless it already finished.
func (t *transfer) abort(code, detail string) {
	t.mu.Lock()
	if t.final || t.abortCode != "" {
		t.mu.Unlock()
		return
	}
	t.abortCode, t.abortDetail = code, detail
	t.mu.Unlock()
	t.cancel()
}

// markFinal commits the outcome; false if an abort got there first.
func (t *transfer) markFinal(sha string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.abortCode != "" {
		return false
	}
	t.final, t.sha = true, sha
	return true
}

func (t *transfer) setMeta(name, dir string, size int64) {
	t.mu.Lock()
	t.name, t.dir, t.size = name, dir, size
	t.mu.Unlock()
}

func (t *transfer) setReplaced() {
	t.mu.Lock()
	t.replaced = true
	t.mu.Unlock()
}

func fail(err error) (code, detail string) { return errCode(err), err.Error() }

func (t *transfer) run(m Msg) {
	defer t.s.wg.Done()
	var code, detail string
	if t.kind == DirUpload {
		code, detail = t.upload(m)
	} else {
		code, detail = t.download(m)
	}
	t.finish(code, detail)
}

func (t *transfer) finish(code, detail string) {
	s := t.s
	t.cancel()
	t.mu.Lock()
	if t.abortCode != "" {
		code, detail = t.abortCode, t.abortDetail
	}
	rec := proto.FileRecord{Xfer: t.xfer, Direction: t.kind, Name: t.name, Dir: t.dir, Size: t.size, Bytes: t.bytes,
		Result: StateOK, Error: code, DurationMS: s.c.Now().Sub(t.started).Milliseconds()}
	switch code {
	case "":
		rec.SHA256, rec.Replaced = t.sha, t.replaced
	case CodeCancelled:
		rec.Result = StateCancelled
	default:
		rec.Result = StateError
	}
	shown := t.shown
	t.mu.Unlock()
	s.remove(t)
	if code != "" && code != CodeCancelled && !s.isClosed() {
		x := t.xfer
		s.sendError(nil, &x, code, detail)
	}
	if s.c.Record != nil {
		s.c.Record(rec)
	}
	if shown {
		t.activity(rec.Result, code, true)
	}
	s.c.Log.Info("file transfer finished", "xfer", t.xfer, "direction", t.kind, "name", rec.Name, "dir", rec.Dir,
		"bytes", rec.Bytes, "result", rec.Result, "error", code, "detail", detail)
}

// show puts the transfer on the device surfaces.
func (t *transfer) show() {
	t.mu.Lock()
	t.shown = true
	t.mu.Unlock()
	t.activity(StateActive, "", true)
}

// activity emits progress, at most once a second unless force.
func (t *transfer) activity(state, code string, force bool) {
	fn := t.s.c.Activity
	if fn == nil {
		return
	}
	now := t.s.c.Now()
	t.mu.Lock()
	if !t.shown || !force && now.Sub(t.lastShown) < time.Second {
		t.mu.Unlock()
		return
	}
	t.lastShown = now
	a := Activity{Xfer: t.xfer, Direction: t.kind, Name: t.name, Dir: t.dir, Size: t.size, Bytes: t.bytes,
		State: state, Error: code, Started: t.started, Updated: now}
	t.mu.Unlock()
	fn(a)
}
