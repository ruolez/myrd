package terminal

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/ruolez/remotectl/proto"
)

const (
	MaxRequests = 4 // list/open/attach requests in flight per channel

	bufferHigh = 1 << 20 // the pump waits while the channel buffers more
	bufferLow  = 256 << 10
	closeWait  = 3 * time.Second
)

// Channel is the subset of *webrtc.DataChannel the service uses.
type Channel interface {
	Send(data []byte) error
	SendText(s string) error
	BufferedAmount() uint64
	SetBufferedAmountLowThreshold(th uint64)
	OnBufferedAmountLow(f func())
}

// Config wires a Service to its session. Record, AuditOK, Log and Now may
// be nil.
type Config struct {
	Host    Host
	Record  func(proto.TerminalRecord) // audit: one record per open/attach and per close
	AuditOK func() bool                // false refuses new terminals (audit_unavailable)
	// OnChange reports the terminals this channel has open (tray, console).
	OnChange func(open []Source)
	Log      *slog.Logger
	Now      func() time.Time

	window int64
}

// Service serves one session's `term` channel.
type Service struct {
	c Config

	chMu sync.Mutex
	ch   Channel
	low  chan struct{}

	mu     sync.Mutex
	closed bool
	terms  map[uint16]*term
	next   uint16
	reqs   int
	wg     sync.WaitGroup
}

func NewService(c Config) *Service {
	if c.Log == nil {
		c.Log = slog.Default()
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.window == 0 {
		c.window = Window
	}
	return &Service{c: c, low: make(chan struct{}), terms: map[uint16]*term{}}
}

type term struct {
	s       *Service
	id      uint16
	st      Stream
	src     Source
	action  string
	started time.Time
	in      *byteQueue

	mu      sync.Mutex
	cond    *sync.Cond
	sent    int64
	acked   int64
	reason  string // why it is closing, when this side decided
	stopped bool
	title   string
	bytesIn int64
}

// Attach binds the open data channel.
func (s *Service) Attach(ch Channel) {
	ch.SetBufferedAmountLowThreshold(bufferLow)
	ch.OnBufferedAmountLow(s.onLow)
	s.chMu.Lock()
	s.ch = ch
	s.chMu.Unlock()
}

// Handle processes one data-channel message without blocking on terminal
// I/O, so it is safe to call from the channel's read loop.
func (s *Service) Handle(isString bool, data []byte) {
	if s.isClosed() {
		return
	}
	if !isString {
		id, payload, err := DecodeData(TypeInput, data)
		if err != nil {
			s.sendError(nil, nil, CodeProtocol, err.Error())
			return
		}
		if t := s.term(id); t != nil { // input in flight after a close is expected
			t.input(payload)
		}
		return
	}
	m, err := DecodeMsg(data)
	if err != nil {
		var ref struct {
			ID   *uint32 `json:"id"`
			Term *uint16 `json:"term"`
		}
		_ = json.Unmarshal(data, &ref)
		s.sendError(ref.ID, ref.Term, CodeProtocol, err.Error())
		return
	}
	switch m.T {
	case "list":
		s.request(m.ID, func() any { return s.list(m.ID) })
	case "open", "attach":
		s.request(m.ID, func() any { return s.open(m) })
	case "ack":
		if t := s.term(m.Term); t != nil {
			if err := t.ack(m.Bytes); err != nil {
				t.stop(CodeProtocol)
			}
		}
	case "resize":
		if t := s.term(m.Term); t != nil {
			_ = t.st.Resize(m.Cols, m.Rows)
		}
	case "detach":
		if t := s.term(m.Term); t != nil {
			t.stop(ReasonDetached)
		}
	case "kill":
		if t := s.term(m.Term); t != nil {
			t.mu.Lock()
			if t.reason == "" {
				t.reason = ReasonKilled
			}
			t.mu.Unlock()
			_ = t.st.Kill()
		}
	}
}

// Close detaches every terminal (shells keep running on the device) and
// waits briefly for their records. Every call waits.
func (s *Service) Close(reason string) {
	s.mu.Lock()
	first := !s.closed
	s.closed = true
	ts := make([]*term, 0, len(s.terms))
	for _, t := range s.terms {
		ts = append(ts, t)
	}
	s.mu.Unlock()
	if first {
		for _, t := range ts {
			t.stop(ReasonSessionEnded)
		}
		s.onLow()
	}
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(closeWait):
		s.c.Log.Warn("terminals still closing", "reason", reason)
	}
}

// Open lists the terminals this channel shows (for the tray).
func (s *Service) Open() []Source {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Source, 0, len(s.terms))
	for _, t := range s.terms {
		out = append(out, t.src)
	}
	sortSources(out)
	return out
}

func (s *Service) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

func (s *Service) term(id uint16) *term {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.terms[id]
}

func (s *Service) changed() {
	if s.c.OnChange != nil {
		s.c.OnChange(s.Open())
	}
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
		return errors.New("term channel not attached")
	}
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return ch.SendText(string(b))
}

func (s *Service) sendError(id *uint32, term *uint16, code, detail string) {
	_ = s.sendJSON(errorMsg{T: "error", ID: id, Term: term, Code: code, Detail: detail})
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
			return errors.New("term channel not attached")
		}
		s.chMu.Lock()
		low := s.low
		s.chMu.Unlock()
		if ch.BufferedAmount() <= bufferHigh {
			return nil
		}
		t := time.NewTimer(250 * time.Millisecond)
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

// ---------- requests ----------

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
		if reply != nil {
			_ = s.sendJSON(reply)
		}
	}()
}

func reqError(id uint32, code string, err error) errorMsg {
	return errorMsg{T: "error", ID: &id, Code: code, Detail: err.Error()}
}

func (s *Service) list(id uint32) any {
	srcs, err := s.c.Host.List()
	if err != nil {
		return reqError(id, errCode(err), err)
	}
	if srcs == nil {
		srcs = []Source{}
	}
	shells := s.c.Host.Shells()
	if shells == nil {
		shells = []Shell{}
	}
	return sourcesMsg{T: "sources", ID: id, Sources: srcs, Shells: shells, Admin: s.c.Host.Admin()}
}

func (s *Service) open(m Msg) any {
	action := proto.TermOpened
	if m.T == "attach" {
		action = proto.TermAttached
	}
	fail := func(code string, err error, kind, title string) any {
		s.record(proto.TerminalRecord{Action: action, Source: kind, Title: title, Admin: m.Admin, Result: "error", Error: code})
		return reqError(m.ID, code, err)
	}
	kind, title := proto.TermSourceShell, m.Shell
	if action == proto.TermAttached {
		kind, title = sourceKind(m.Source), m.Source
	}
	s.mu.Lock()
	n := len(s.terms)
	s.mu.Unlock()
	if n >= MaxTerms {
		return fail(CodeTooMany, ErrTooMany, kind, title)
	}
	if s.c.AuditOK != nil && !s.c.AuditOK() {
		return reqError(m.ID, CodeAuditUnavailable, errors.New("the audit log is not reachable"))
	}
	var st Stream
	var src Source
	var err error
	if action == proto.TermAttached {
		st, src, err = s.c.Host.Attach(m.Source, m.Cols, m.Rows)
	} else {
		st, src, err = s.c.Host.New(m.Shell, m.Admin, m.Cols, m.Rows)
	}
	if err != nil {
		s.c.Log.Warn("terminal not opened", "action", action, "source", m.Source, "err", err)
		return fail(errCode(err), err, kind, title)
	}
	s.mu.Lock()
	if s.closed || len(s.terms) >= MaxTerms {
		s.mu.Unlock()
		st.Detach()
		return fail(CodeTooMany, ErrTooMany, kind, title)
	}
	id := s.allocLocked()
	t := &term{s: s, id: id, st: st, src: src, action: action, started: s.c.Now(), in: newByteQueue(), title: st.Title()}
	t.cond = sync.NewCond(&t.mu)
	s.terms[id] = t
	s.wg.Add(2)
	s.mu.Unlock()
	_ = s.sendJSON(openedMsg{T: "opened", ID: m.ID, Term: id, Source: src, Window: s.c.window})
	s.record(proto.TerminalRecord{Term: id, Action: action, Source: src.Kind, Title: src.Title, Admin: src.Admin, Result: "ok"})
	go t.pump()
	go t.writer()
	s.changed()
	return nil
}

func sourceKind(id string) string {
	for k := range kindOrder {
		if len(id) > len(k) && id[:len(k)] == k && id[len(k)] == ':' {
			return k
		}
	}
	return "unknown"
}

// allocLocked picks a free terminal id (1..65535).
func (s *Service) allocLocked() uint16 {
	for {
		s.next++
		if s.next == 0 {
			s.next = 1
		}
		if s.terms[s.next] == nil {
			return s.next
		}
	}
}

func (s *Service) record(r proto.TerminalRecord) {
	if s.c.Record != nil {
		s.c.Record(r)
	}
}

// ---------- one terminal ----------

func (t *term) input(p []byte) {
	t.mu.Lock()
	t.bytesIn += int64(len(p))
	t.mu.Unlock()
	if !t.in.push(append([]byte(nil), p...)) {
		return
	}
}

// writer types queued input, so a program that does not read its input
// never blocks the channel.
func (t *term) writer() {
	defer t.s.wg.Done()
	buf := make([]byte, 4096)
	for {
		n, err := t.in.read(buf)
		if err != nil {
			return
		}
		if _, err := t.st.Write(buf[:n]); err != nil {
			t.in.drop()
			return
		}
	}
}

func (t *term) ack(bytes int64) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if bytes < t.acked || bytes > t.sent {
		return ErrCredit
	}
	t.acked = bytes
	t.cond.Broadcast()
	return nil
}

// stop ends this viewer's attachment; reason goes to the browser.
func (t *term) stop(reason string) {
	t.mu.Lock()
	if t.reason == "" {
		t.reason = reason
	}
	t.stopped = true
	t.cond.Broadcast()
	t.mu.Unlock()
	t.st.Detach()
}

// credit waits until output may be sent and returns how much (0: stopped).
func (t *term) credit() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	for !t.stopped && t.sent-t.acked >= t.s.c.window {
		t.cond.Wait()
	}
	if t.stopped {
		return 0
	}
	return int(min(int64(MaxChunk), t.s.c.window-(t.sent-t.acked)))
}

// pump sends the terminal's output until it ends or this viewer leaves.
func (t *term) pump() {
	defer t.s.wg.Done()
	s := t.s
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	buf := make([]byte, MaxChunk)
	for {
		n := t.credit()
		if n == 0 {
			break
		}
		k, err := t.st.Read(buf[:n])
		if k > 0 {
			if s.waitBuffered(ctx) != nil {
				break
			}
			if ch := s.channel(); ch == nil || ch.Send(EncodeData(TypeOutput, t.id, buf[:k])) != nil {
				t.stop(ReasonSessionEnded)
				break
			}
			t.mu.Lock()
			t.sent += int64(k)
			t.mu.Unlock()
			if title := t.st.Title(); title != t.title {
				t.title = title
				_ = s.sendJSON(titleMsg{T: "title", Term: t.id, Title: title})
			}
		}
		if err != nil {
			break
		}
	}
	t.in.drop()
	t.st.Detach()
	t.mu.Lock()
	reason := t.reason
	sent, in := t.sent, t.bytesIn
	t.mu.Unlock()
	var code *int
	if reason == "" || reason == ReasonKilled {
		code = t.st.ExitCode()
		if reason == "" {
			reason = ReasonExited
		}
	}
	s.mu.Lock()
	if s.terms[t.id] == t {
		delete(s.terms, t.id)
	}
	s.mu.Unlock()
	if reason != CodeProtocol {
		_ = s.sendJSON(closedMsg{T: "closed", Term: t.id, Reason: reason, ExitCode: code})
	} else {
		id := t.id
		s.sendError(nil, &id, CodeProtocol, "flow-control violation")
		reason = ReasonDetached
	}
	s.record(proto.TerminalRecord{Term: t.id, Action: proto.TermClosed, Source: t.src.Kind, Title: t.src.Title,
		Admin: t.src.Admin, Result: "ok", Reason: reason, ExitCode: code, BytesIn: in, BytesOut: sent,
		DurationMS: s.c.Now().Sub(t.started).Milliseconds()})
	s.changed()
}
