package files

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/ruolez/remotectl/proto"
)

const (
	MaxActive    = 4 // concurrent transfers per session
	MaxRequests  = 2 // concurrent roots/list/mkdir/rename/delete requests per session
	StallTimeout = 60 * time.Second
	RelayRate    = 4 << 20 // file bytes/s on relayed sessions

	bufferHigh = 2 << 20 // sender waits while the channel buffers more
	bufferLow  = 1 << 20 // OnBufferedAmountLow threshold
	closeWait  = 5 * time.Second
	coalesce   = 64 << 10
)

// Channel is the subset of *webrtc.DataChannel the service uses.
type Channel interface {
	Send(data []byte) error
	SendText(s string) error
	BufferedAmount() uint64
	SetBufferedAmountLowThreshold(th uint64)
	OnBufferedAmountLow(f func())
}

const (
	DirUpload   = "upload" // browser → device
	DirDownload = "download"

	StateActive    = "active"
	StateOK        = "ok"
	StateCancelled = "cancelled"
	StateError     = "error"
)

// Activity is a transfer's progress, for the device-local surfaces (tray,
// device console). It is emitted when a transfer starts, at most once a
// second while it runs, and when it finishes.
type Activity struct {
	Xfer      uint32    `json:"xfer"`
	Direction string    `json:"direction"` // upload | download
	Name      string    `json:"name"`
	Dir       string    `json:"dir"`
	Size      int64     `json:"size"`
	Bytes     int64     `json:"bytes"`
	State     string    `json:"state"` // active | ok | cancelled | error
	Error     string    `json:"error,omitempty"`
	Started   time.Time `json:"started"`
	Updated   time.Time `json:"updated"`
}

// Config wires a Service to its session. Every func may be nil.
type Config struct {
	Host     Host
	Policy   Policy
	Path     func() string          // current ICE path; "relay" paces file data
	Record   func(proto.FileRecord) // one audit record per finished transfer
	Activity func(Activity)         // must not block
	AuditOK  func() bool            // false refuses new transfers (audit_unavailable)
	Log      *slog.Logger
	Now      func() time.Time

	goos         string
	stallTimeout time.Duration
	relayRate    int
	window       int64
}

// Service serves one session's `file` channel.
type Service struct {
	c     Config
	deny  Policy
	pacer *rate.Limiter
	stop  chan struct{}
	wg    sync.WaitGroup

	chMu sync.Mutex
	ch   Channel
	low  chan struct{} // closed and replaced on OnBufferedAmountLow

	mu     sync.Mutex
	closed bool
	xfers  map[uint32]*transfer
	reqs   int

	resMu    sync.Mutex // reservations and sweeps; may be held across file-system calls
	reserved map[string]bool
	swept    map[string]bool
}

func New(c Config) *Service {
	if c.Host == nil {
		c.Host = LocalHost{}
	}
	if c.Log == nil {
		c.Log = slog.Default()
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.goos == "" {
		c.goos = runtime.GOOS
	}
	if c.stallTimeout == 0 {
		c.stallTimeout = StallTimeout
	}
	if c.relayRate == 0 {
		c.relayRate = RelayRate
	}
	if c.window == 0 {
		c.window = Window
	}
	s := &Service{c: c, stop: make(chan struct{}), low: make(chan struct{}),
		pacer: rate.NewLimiter(rate.Limit(c.relayRate), 4*MaxChunk),
		xfers: map[uint32]*transfer{}, reserved: map[string]bool{}, swept: map[string]bool{}}
	for _, d := range c.Policy.Deny {
		s.deny.Deny = append(s.deny.Deny, d)
		if r, err := filepath.EvalSymlinks(d); err == nil && r != d {
			s.deny.Deny = append(s.deny.Deny, r)
		}
	}
	go s.watchStalls()
	return s
}

// Attach binds the open data channel.
func (s *Service) Attach(ch Channel) {
	ch.SetBufferedAmountLowThreshold(bufferLow)
	ch.OnBufferedAmountLow(s.onLow)
	s.chMu.Lock()
	s.ch = ch
	s.chMu.Unlock()
}

// Handle processes one data-channel message. It never blocks on disk or
// network I/O, so it is safe to call from the channel's read loop.
func (s *Service) Handle(isString bool, data []byte) {
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return
	}
	if !isString {
		xfer, payload, err := DecodeData(data)
		if err != nil {
			s.sendError(nil, nil, CodeProtocol, err.Error())
			return
		}
		// Frames for unknown transfers are expected in flight after a cancel.
		if t := s.transfer(xfer); t != nil {
			if t.kind != DirUpload {
				t.abort(CodeProtocol, "data for a download")
				return
			}
			t.push(payload)
		}
		return
	}
	m, err := DecodeMsg(data)
	if err != nil {
		var ref struct {
			ID   *uint32 `json:"id"`
			Xfer *uint32 `json:"xfer"`
		}
		_ = json.Unmarshal(data, &ref)
		if ref.Xfer != nil {
			if t := s.transfer(*ref.Xfer); t != nil {
				t.abort(CodeProtocol, err.Error())
				return
			}
		}
		s.sendError(ref.ID, ref.Xfer, CodeProtocol, err.Error())
		return
	}
	switch m.T {
	case "roots":
		s.request(m.ID, func() any { return s.roots(m.ID) })
	case "list":
		s.request(m.ID, func() any { return s.list(m) })
	case "get":
		s.start(m, DirDownload)
	case "put":
		s.start(m, DirUpload)
	case OpMkdir:
		s.request(m.ID, func() any { return s.fileOp(m, s.mkdir) })
	case OpRename:
		s.request(m.ID, func() any { return s.fileOp(m, s.rename) })
	case OpDelete:
		s.request(m.ID, func() any { return s.fileOp(m, s.deleteEntry) })
	case "ack":
		if t := s.transfer(m.Xfer); t != nil {
			if t.kind != DirDownload {
				t.abort(CodeProtocol, "ack for an upload")
				return
			}
			t.onAck(m.Bytes)
		}
	case "end":
		if t := s.transfer(m.Xfer); t != nil {
			if t.kind != DirUpload {
				t.abort(CodeProtocol, "end for a download")
				return
			}
			t.onEnd(m.SHA256)
		}
	case "cancel":
		if t := s.transfer(m.Xfer); t != nil {
			t.abort(CodeCancelled, "cancelled by the browser")
		}
	}
}

// Close cancels every transfer (each records `cancelled` and removes its
// partial file) and waits briefly for them to finish. Nothing is sent.
// Every call waits, so a caller never overtakes a Close in progress.
func (s *Service) Close(reason string) {
	s.mu.Lock()
	first := !s.closed
	s.closed = true
	ts := make([]*transfer, 0, len(s.xfers))
	for _, t := range s.xfers {
		ts = append(ts, t)
	}
	s.mu.Unlock()
	if first {
		close(s.stop)
		for _, t := range ts {
			t.abort(CodeCancelled, reason)
		}
	}
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(closeWait):
		s.c.Log.Warn("file transfers still finishing after close", "reason", reason)
	}
}

func (s *Service) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

func (s *Service) transfer(xfer uint32) *transfer {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.xfers[xfer]
}

// ---------- sending ----------

func (s *Service) channel() Channel {
	s.chMu.Lock()
	defer s.chMu.Unlock()
	return s.ch
}

func (s *Service) sendJSON(v any) error {
	ch := s.channel()
	if ch == nil {
		return errors.New("file channel not attached")
	}
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return ch.SendText(string(b))
}

func (s *Service) sendError(id, xfer *uint32, code, detail string) {
	_ = s.sendJSON(errorMsg{T: "error", ID: id, Xfer: xfer, Code: code, Detail: detail})
}

func (s *Service) onLow() {
	s.chMu.Lock()
	close(s.low)
	s.low = make(chan struct{})
	s.chMu.Unlock()
}

// waitBuffered blocks while the channel holds more than bufferHigh bytes.
func (s *Service) waitBuffered(ctx context.Context) error {
	for {
		ch := s.channel()
		if ch == nil {
			return errors.New("file channel not attached")
		}
		if ch.BufferedAmount() <= bufferHigh {
			return nil
		}
		s.chMu.Lock()
		low := s.low
		s.chMu.Unlock()
		if ch.BufferedAmount() <= bufferHigh {
			return nil
		}
		t := time.NewTimer(250 * time.Millisecond) // in case a low event is missed
		select {
		case <-low:
		case <-t.C:
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		}
		t.Stop()
	}
}

// pace limits file data on relayed sessions; coturn's per-allocation cap
// is shared with the video.
func (s *Service) pace(ctx context.Context, n int) error {
	if s.c.Path == nil || s.c.Path() != proto.PathRelay {
		return nil
	}
	return s.pacer.WaitN(ctx, n)
}

// ---------- requests (roots, list) ----------

func (s *Service) request(id uint32, fn func() any) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	if s.reqs >= MaxRequests {
		s.mu.Unlock()
		s.sendError(&id, nil, CodeTooMany, "too many requests")
		return
	}
	s.reqs++
	s.wg.Add(1)
	s.mu.Unlock()
	go func() {
		defer s.wg.Done()
		reply := fn()
		s.mu.Lock()
		s.reqs--
		s.mu.Unlock()
		_ = s.sendJSON(reply)
	}()
}

func reqError(id uint32, err error) errorMsg {
	return errorMsg{T: "error", ID: &id, Code: errCode(err), Detail: err.Error()}
}

func (s *Service) sep() string {
	if s.c.goos == "windows" {
		return `\`
	}
	return "/"
}

func (s *Service) roots(id uint32) any {
	roots, def, err := s.c.Host.Roots()
	if err != nil {
		return reqError(id, err)
	}
	if roots == nil {
		roots = []Root{}
	}
	return rootsMsg{T: "roots", ID: id, Roots: roots, DefaultDir: def, Sep: s.sep()}
}

var (
	errNotDir  = errors.New("not a directory")
	errNotFile = errors.New("not a regular file")
)

func (s *Service) list(m Msg) any {
	p, err := CheckPath(m.Path, s.c.goos)
	if err != nil {
		return reqError(m.ID, err)
	}
	var es []Entry
	var truncated bool
	err = s.c.Host.Do(func() error {
		real, err := s.resolve(p)
		if err != nil {
			return err
		}
		if st, err := os.Stat(real); err != nil {
			return err
		} else if !st.IsDir() {
			return errNotDir
		}
		es, truncated, err = readDir(real, scanCap)
		return err
	})
	if err != nil {
		return reqError(m.ID, err)
	}
	es = visible(es, m.Hidden)
	sortEntries(es)
	pg, next := page(es, m.Offset)
	return listingMsg{T: "listing", ID: m.ID, Path: p, Parent: ParentDir(p, s.c.goos), Entries: pg,
		Offset: m.Offset, NextOffset: next, Total: len(es), Truncated: truncated}
}

// resolve follows symlinks and applies the deny list. Call inside Host.Do.
func (s *Service) resolve(p string) (string, error) {
	real, err := filepath.EvalSymlinks(p)
	if err != nil {
		return "", err
	}
	if s.deny.Denied(p, s.c.goos) || s.deny.Denied(real, s.c.goos) {
		return "", ErrDenied
	}
	return real, nil
}

// ---------- transfers ----------

func (s *Service) start(m Msg, kind string) {
	x := m.Xfer
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	code, detail := "", ""
	switch {
	case s.xfers[x] != nil:
		code, detail = CodeProtocol, "xfer already in use"
	case len(s.xfers) >= MaxActive:
		code, detail = CodeTooMany, "too many transfers"
	case s.c.AuditOK != nil && !s.c.AuditOK():
		code, detail = CodeAuditUnavailable, "the audit log is not reachable"
	}
	if code != "" {
		s.mu.Unlock()
		s.sendError(nil, &x, code, detail)
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	now := s.c.Now()
	t := &transfer{s: s, xfer: x, kind: kind, ctx: ctx, cancel: cancel, started: now, progress: now, size: m.Size,
		kick: make(chan struct{}, 1), rw: recvWindow{size: s.c.window}, sw: sendWindow{size: s.c.window}}
	s.xfers[x] = t
	s.wg.Add(1)
	s.mu.Unlock()
	go t.run(m)
}

func (s *Service) remove(t *transfer) {
	s.mu.Lock()
	if s.xfers[t.xfer] == t {
		delete(s.xfers, t.xfer)
	}
	s.mu.Unlock()
}

func (s *Service) watchStalls() {
	tick := time.NewTicker(s.c.stallTimeout / 6)
	defer tick.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-tick.C:
		}
		now := s.c.Now()
		s.mu.Lock()
		ts := make([]*transfer, 0, len(s.xfers))
		for _, t := range s.xfers {
			ts = append(ts, t)
		}
		s.mu.Unlock()
		for _, t := range ts {
			t.mu.Lock()
			idle := now.Sub(t.progress)
			t.mu.Unlock()
			if idle > s.c.stallTimeout {
				t.abort(CodeStalled, "no progress for "+s.c.stallTimeout.String())
			}
		}
	}
}

// fold is the reservation key: case-insensitive where the file system
// usually is.
func (s *Service) fold(p string) string {
	if s.c.goos == "linux" {
		return p
	}
	return strings.ToLower(p)
}

// reserveName picks a free final name in dir (on disk and among running
// uploads) and holds it until release. With replace, name itself counts as
// free while it is a regular file. Call inside Host.Do.
func (s *Service) reserveName(dir, name string, replace bool) (string, error) {
	s.resMu.Lock()
	defer s.resMu.Unlock()
	final, err := UniqueName(name, func(c string) bool {
		full := filepath.Join(dir, c)
		if s.reserved[s.fold(full)] {
			return true
		}
		st, err := os.Lstat(full)
		if replace && c == name && err == nil && st.Mode().IsRegular() {
			return false
		}
		return !errors.Is(err, os.ErrNotExist)
	})
	if err != nil {
		return "", err
	}
	s.reserved[s.fold(filepath.Join(dir, final))] = true
	return final, nil
}

func (s *Service) release(dir, name string) {
	s.resMu.Lock()
	delete(s.reserved, s.fold(filepath.Join(dir, name)))
	s.resMu.Unlock()
}

// sweepStale deletes abandoned upload parts older than StalePartAge in dir,
// once per directory per session.
func (s *Service) sweepStale(dir string) {
	s.resMu.Lock()
	done := s.swept[s.fold(dir)]
	s.swept[s.fold(dir)] = true
	s.resMu.Unlock()
	if done {
		return
	}
	now := s.c.Now()
	_ = s.c.Host.Do(func() error {
		f, err := os.Open(dir)
		if err != nil {
			return err
		}
		defer f.Close()
		for {
			des, err := f.ReadDir(256)
			for _, de := range des {
				if !IsPartName(de.Name()) {
					continue
				}
				if info, err := de.Info(); err == nil && info.Mode().IsRegular() && IsStalePart(de.Name(), info.ModTime(), now) {
					if os.Remove(filepath.Join(dir, de.Name())) == nil {
						s.c.Log.Info("removed stale upload part", "dir", dir, "name", de.Name())
					}
				}
			}
			if err == io.EOF || len(des) == 0 {
				return nil
			}
			if err != nil {
				return err
			}
		}
	})
}
