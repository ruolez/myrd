package terminal

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ruolez/remotectl/proto"
)

// ---------- fakes ----------

type fakeProc struct {
	outR *io.PipeReader
	outW *io.PipeWriter

	mu    sync.Mutex
	in    bytes.Buffer
	sizes [][2]int
	hung  bool
	once  sync.Once
	exit  chan int
}

func newFakeProc() *fakeProc {
	r, w := io.Pipe()
	return &fakeProc{outR: r, outW: w, exit: make(chan int, 1)}
}

func (p *fakeProc) Read(b []byte) (int, error) { return p.outR.Read(b) }

func (p *fakeProc) Write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.in.Write(b)
}

func (p *fakeProc) Resize(c, r int) error {
	p.mu.Lock()
	p.sizes = append(p.sizes, [2]int{c, r})
	p.mu.Unlock()
	return nil
}

func (p *fakeProc) Wait() int {
	c := <-p.exit
	p.exit <- c
	return c
}

func (p *fakeProc) Hangup() {
	p.mu.Lock()
	p.hung = true
	p.mu.Unlock()
	p.finish(-1)
}

func (p *fakeProc) Close() error { return nil }

func (p *fakeProc) finish(code int) {
	p.once.Do(func() {
		_ = p.outW.Close()
		p.exit <- code
	})
}

func (p *fakeProc) input() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.in.String()
}

// fakeHost is a Host over Shells with fake programs.
type fakeHost struct {
	shells *Shells
	mu     sync.Mutex
	procs  []*fakeProc
	specs  []SpawnSpec
}

func newFakeHost() *fakeHost {
	h := &fakeHost{}
	h.shells = NewShells(func(spec SpawnSpec) (Proc, error) {
		p := newFakeProc()
		h.mu.Lock()
		h.procs = append(h.procs, p)
		h.specs = append(h.specs, spec)
		h.mu.Unlock()
		return p, nil
	}, slog.New(slog.DiscardHandler))
	return h
}

func (h *fakeHost) proc(i int) *fakeProc {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.procs[i]
}

func (h *fakeHost) List() ([]Source, error) { return h.shells.List(), nil }
func (h *fakeHost) Shells() []Shell         { return []Shell{{ID: "/bin/sh", Name: "sh", Path: "/bin/sh"}} }
func (h *fakeHost) Admin() bool             { return false }
func (h *fakeHost) New(sh string, admin bool, c, r int) (Stream, Source, error) {
	if admin {
		return nil, Source{}, ErrUnsupported
	}
	return h.shells.Start(SpawnSpec{Path: "/bin/sh", Args: []string{"-sh"}, Cols: c, Rows: r}, Meta{Kind: proto.TermSourceShell, Name: "sh", Detail: "/bin/sh"})
}
func (h *fakeHost) Attach(id string, c, r int) (Stream, Source, error) {
	return h.shells.Attach(id, c, r)
}

type fakeChan struct {
	mu    sync.Mutex
	texts []string
	data  [][]byte
	note  chan struct{}
}

func newFakeChan() *fakeChan { return &fakeChan{note: make(chan struct{}, 1000)} }

func (c *fakeChan) Send(b []byte) error {
	c.mu.Lock()
	c.data = append(c.data, append([]byte(nil), b...))
	c.mu.Unlock()
	c.note <- struct{}{}
	return nil
}

func (c *fakeChan) SendText(s string) error {
	c.mu.Lock()
	c.texts = append(c.texts, s)
	c.mu.Unlock()
	c.note <- struct{}{}
	return nil
}

func (c *fakeChan) BufferedAmount() uint64               { return 0 }
func (c *fakeChan) SetBufferedAmountLowThreshold(uint64) {}
func (c *fakeChan) OnBufferedAmountLow(func())           {}

// waitText returns the first JSON frame of type typ (and matching pred).
func (c *fakeChan) waitText(t *testing.T, typ string, pred func(map[string]any) bool) map[string]any {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		c.mu.Lock()
		for _, s := range c.texts {
			var m map[string]any
			_ = json.Unmarshal([]byte(s), &m)
			if m["t"] == typ && (pred == nil || pred(m)) {
				c.mu.Unlock()
				return m
			}
		}
		c.mu.Unlock()
		select {
		case <-c.note:
		case <-deadline:
			c.mu.Lock()
			defer c.mu.Unlock()
			t.Fatalf("no %q frame; got %v", typ, c.texts)
		}
	}
}

// output concatenates the output frames of term.
func (c *fakeChan) output(t *testing.T, term uint16) string {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	var b strings.Builder
	for _, d := range c.data {
		id, p, err := DecodeData(TypeOutput, d)
		if err != nil {
			t.Fatalf("bad output frame %x: %v", d, err)
		}
		if id == term {
			b.Write(p)
		}
	}
	return b.String()
}

func (c *fakeChan) waitOutput(t *testing.T, term uint16, want string) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for !strings.Contains(c.output(t, term), want) {
		select {
		case <-c.note:
		case <-deadline:
			t.Fatalf("output of term %d = %q, want it to contain %q", term, c.output(t, term), want)
		}
	}
}

type recorder struct {
	mu   sync.Mutex
	recs []proto.TerminalRecord
}

func (r *recorder) record(rec proto.TerminalRecord) {
	r.mu.Lock()
	r.recs = append(r.recs, rec)
	r.mu.Unlock()
}

func (r *recorder) all() []proto.TerminalRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]proto.TerminalRecord(nil), r.recs...)
}

func newTestService(h Host, window int64) (*Service, *fakeChan, *recorder) {
	rec := &recorder{}
	s := NewService(Config{Host: h, Record: rec.record, Log: slog.New(slog.DiscardHandler), window: window})
	ch := newFakeChan()
	s.Attach(ch)
	return s, ch, rec
}

func send(s *Service, format string, a ...any) { s.Handle(true, []byte(fmt.Sprintf(format, a...))) }

func termOf(m map[string]any) uint16 { return uint16(m["term"].(float64)) }

// ---------- tests ----------

func TestOpenTypeAndExit(t *testing.T) {
	h := newFakeHost()
	s, ch, rec := newTestService(h, Window)
	send(s, `{"t":"open","id":1,"cols":80,"rows":24}`)
	opened := ch.waitText(t, "opened", nil)
	term := termOf(opened)
	if src := opened["source"].(map[string]any); src["kind"] != "shell" || src["id"] != "shell:1" {
		t.Fatalf("opened source %v", src)
	}
	p := h.proc(0)
	_, _ = p.outW.Write([]byte("$ "))
	ch.waitOutput(t, term, "$ ")

	s.Handle(false, EncodeData(TypeInput, term, []byte("ls\r")))
	waitFor(t, func() bool { return p.input() == "ls\r" })

	p.finish(3)
	closed := ch.waitText(t, "closed", nil)
	if closed["reason"] != ReasonExited || closed["exit_code"] != float64(3) {
		t.Fatalf("closed %v", closed)
	}
	s.Close("test")
	recs := rec.all()
	if len(recs) != 2 || recs[0].Action != proto.TermOpened || recs[1].Action != proto.TermClosed ||
		recs[1].Reason != ReasonExited || *recs[1].ExitCode != 3 || recs[1].BytesIn != 3 || recs[1].BytesOut != 2 {
		t.Fatalf("records %+v", recs)
	}
}

func TestCreditWindowHoldsOutput(t *testing.T) {
	const window = 1000
	h := newFakeHost()
	s, ch, _ := newTestService(h, window)
	defer s.Close("test")
	send(s, `{"t":"open","id":1,"cols":80,"rows":24}`)
	term := termOf(ch.waitText(t, "opened", nil))
	p := h.proc(0)
	go func() { _, _ = p.outW.Write(bytes.Repeat([]byte("x"), 3*window)) }()

	ch.waitOutput(t, term, strings.Repeat("x", window))
	time.Sleep(50 * time.Millisecond)
	if n := len(ch.output(t, term)); n != window {
		t.Fatalf("sent %d bytes without an ack, window %d", n, window)
	}
	send(s, `{"t":"ack","term":%d,"bytes":%d}`, term, window)
	ch.waitOutput(t, term, strings.Repeat("x", 2*window))
}

func TestAckBeyondSentIsAProtocolError(t *testing.T) {
	h := newFakeHost()
	s, ch, _ := newTestService(h, Window)
	defer s.Close("test")
	send(s, `{"t":"open","id":1,"cols":80,"rows":24}`)
	term := termOf(ch.waitText(t, "opened", nil))
	send(s, `{"t":"ack","term":%d,"bytes":5}`, term)
	e := ch.waitText(t, "error", func(m map[string]any) bool { return m["term"] != nil })
	if e["code"] != CodeProtocol {
		t.Fatalf("error %v", e)
	}
}

func TestDetachKeepsShellAndReattachReplays(t *testing.T) {
	h := newFakeHost()
	s, ch, _ := newTestService(h, Window)
	send(s, `{"t":"open","id":1,"cols":80,"rows":24}`)
	term := termOf(ch.waitText(t, "opened", nil))
	p := h.proc(0)
	_, _ = p.outW.Write([]byte("hello\r\n\x1b[?2004h$ "))
	ch.waitOutput(t, term, "$ ")
	send(s, `{"t":"detach","term":%d}`, term)
	if c := ch.waitText(t, "closed", nil); c["reason"] != ReasonDetached || c["exit_code"] != nil {
		t.Fatalf("closed %v", c)
	}
	s.Close("first session")
	if p.hung {
		t.Fatal("a kept shell was hung up on detach")
	}

	// A new session (a reconnect) lists and reattaches it.
	s2, ch2, rec2 := newTestService(h, Window)
	defer s2.Close("test")
	send(s2, `{"t":"list","id":7}`)
	list := ch2.waitText(t, "sources", nil)
	srcs := list["sources"].([]any)
	if len(srcs) != 1 || srcs[0].(map[string]any)["id"] != "shell:1" || srcs[0].(map[string]any)["attached"] != float64(0) {
		t.Fatalf("sources %v", srcs)
	}
	send(s2, `{"t":"attach","id":8,"source":"shell:1","cols":100,"rows":30}`)
	term2 := termOf(ch2.waitText(t, "opened", nil))
	ch2.waitOutput(t, term2, "hello\r\n\x1b[?2004h$ \x1b[?2004h")
	waitFor(t, func() bool {
		p.mu.Lock()
		defer p.mu.Unlock()
		return len(p.sizes) > 0 && p.sizes[len(p.sizes)-1] == [2]int{100, 30}
	})
	if r := rec2.all(); len(r) != 1 || r[0].Action != proto.TermAttached || r[0].Source != "shell" {
		t.Fatalf("records %+v", r)
	}
}

func TestKillReportsKilled(t *testing.T) {
	h := newFakeHost()
	s, ch, _ := newTestService(h, Window)
	defer s.Close("test")
	send(s, `{"t":"open","id":1,"cols":80,"rows":24}`)
	term := termOf(ch.waitText(t, "opened", nil))
	send(s, `{"t":"kill","term":%d}`, term)
	c := ch.waitText(t, "closed", nil)
	if c["reason"] != ReasonKilled || c["exit_code"] != float64(-1) || !h.proc(0).hung {
		t.Fatalf("closed %v hung %v", c, h.proc(0).hung)
	}
}

func TestTwoViewersShareOutput(t *testing.T) {
	h := newFakeHost()
	s, ch, _ := newTestService(h, Window)
	defer s.Close("test")
	send(s, `{"t":"open","id":1,"cols":80,"rows":24}`)
	t1 := termOf(ch.waitText(t, "opened", nil))
	send(s, `{"t":"attach","id":2,"source":"shell:1","cols":80,"rows":24}`)
	t2 := termOf(ch.waitText(t, "opened", func(m map[string]any) bool { return m["id"] == float64(2) }))
	if t1 == t2 {
		t.Fatal("same term id twice")
	}
	_, _ = h.proc(0).outW.Write([]byte("both"))
	ch.waitOutput(t, t1, "both")
	ch.waitOutput(t, t2, "both")
}

func TestErrors(t *testing.T) {
	h := newFakeHost()
	s, ch, rec := newTestService(h, Window)
	defer s.Close("test")
	send(s, `{"t":"attach","id":3,"source":"shell:99","cols":80,"rows":24}`)
	if e := ch.waitText(t, "error", nil); e["code"] != CodeNotFound || e["id"] != float64(3) {
		t.Fatalf("error %v", e)
	}
	send(s, `{"t":"open","id":4,"cols":0,"rows":24}`)
	ch.waitText(t, "error", func(m map[string]any) bool { return m["id"] == float64(4) && m["code"] == CodeProtocol })
	send(s, `{"t":"open","id":5,"cols":80,"rows":24,"admin":true}`)
	ch.waitText(t, "error", func(m map[string]any) bool { return m["id"] == float64(5) && m["code"] == CodeUnsupported })
	if r := rec.all(); len(r) != 2 || r[0].Result != "error" || r[0].Error != CodeNotFound || r[1].Error != CodeUnsupported {
		t.Fatalf("records %+v", r)
	}
}

func TestAuditUnavailableRefusesOpen(t *testing.T) {
	h := newFakeHost()
	s := NewService(Config{Host: h, AuditOK: func() bool { return false }, Log: slog.New(slog.DiscardHandler)})
	ch := newFakeChan()
	s.Attach(ch)
	defer s.Close("test")
	send(s, `{"t":"open","id":1,"cols":80,"rows":24}`)
	if e := ch.waitText(t, "error", nil); e["code"] != CodeAuditUnavailable {
		t.Fatalf("error %v", e)
	}
	if len(h.procs) != 0 {
		t.Fatal("a shell started without an audit log")
	}
}

func TestTooManyTerms(t *testing.T) {
	h := newFakeHost()
	s, ch, _ := newTestService(h, Window)
	defer s.Close("test")
	for i := 1; i <= MaxTerms; i++ {
		send(s, `{"t":"open","id":%d,"cols":80,"rows":24}`, i)
		id := float64(i)
		ch.waitText(t, "opened", func(m map[string]any) bool { return m["id"] == id })
	}
	send(s, `{"t":"open","id":99,"cols":80,"rows":24}`)
	ch.waitText(t, "error", func(m map[string]any) bool { return m["id"] == float64(99) && m["code"] == CodeTooMany })
}

func TestCloseDetachesEverything(t *testing.T) {
	h := newFakeHost()
	s, ch, rec := newTestService(h, Window)
	send(s, `{"t":"open","id":1,"cols":80,"rows":24}`)
	ch.waitText(t, "opened", nil)
	s.Close("session ended")
	r := rec.all()
	if len(r) != 2 || r[1].Reason != ReasonSessionEnded {
		t.Fatalf("records %+v", r)
	}
	if got := h.shells.List(); len(got) != 1 || got[0].Attached != 0 {
		t.Fatalf("kept shells %+v", got)
	}
}

func TestEphemeralClientEndsWithLastViewer(t *testing.T) {
	h := newFakeHost()
	st, _, err := h.shells.Start(SpawnSpec{Path: "tmux", Cols: 80, Rows: 24}, Meta{Kind: "tmux", Name: "main", Ephemeral: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := h.shells.List(); len(got) != 0 {
		t.Fatalf("ephemeral client listed: %+v", got)
	}
	st.Detach()
	if !h.proc(0).hung {
		t.Fatal("tmux client kept running after its viewer left")
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatal("condition not reached")
}

func TestCwd(t *testing.T) {
	h := newFakeHost()
	s, ch, _ := newTestService(h, Window)
	defer s.Close("test")
	send(s, `{"t":"open","id":1,"cols":80,"rows":24}`)
	term := termOf(ch.waitText(t, "opened", nil))
	send(s, `{"t":"cwd","id":2,"term":%d}`, term)
	if e := ch.waitText(t, "error", func(m map[string]any) bool { return m["id"] == float64(2) }); e["code"] != CodeUnsupported {
		t.Fatalf("cwd of a program that does not say: %v", e)
	}
	send(s, `{"t":"cwd","id":3,"term":99}`)
	if e := ch.waitText(t, "error", func(m map[string]any) bool { return m["id"] == float64(3) }); e["code"] != CodeNotFound {
		t.Fatalf("cwd of no terminal: %v", e)
	}
	st, _, err := h.shells.Start(SpawnSpec{Path: "tmux", Cols: 80, Rows: 24},
		Meta{Kind: "tmux", Name: "main", Ephemeral: true, Cwd: func() (string, error) { return "/srv/app", nil }})
	if err != nil {
		t.Fatal(err)
	}
	if p, err := st.(Cwder).Cwd(); err != nil || p != "/srv/app" {
		t.Fatalf("meta cwd %q %v", p, err)
	}
}

// permHost is a host whose app terminals wait for the device's permission.
type permHost struct {
	*fakeHost
	mu      sync.Mutex
	asked   bool
	blocked []Blocked
}

func (h *permHost) Blocked() []Blocked {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.blocked
}

func (h *permHost) AskAgain() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.asked = true
	h.blocked = []Blocked{{App: "Terminal", Reason: BlockedAsking}}
}

func TestListReportsBlockedAppsAndAsksAgain(t *testing.T) {
	h := &permHost{fakeHost: newFakeHost(), blocked: []Blocked{{App: "Terminal", Reason: BlockedDenied}}}
	s, ch, _ := newTestService(h, Window)
	defer s.Close("test")

	send(s, `{"t":"list","id":1}`)
	first := ch.waitText(t, "sources", func(m map[string]any) bool { return m["id"] == float64(1) })
	send(s, `{"t":"list","id":2,"ask":true}`)
	again := ch.waitText(t, "sources", func(m map[string]any) bool { return m["id"] == float64(2) })

	got := []any{first["blocked"], again["blocked"]}
	want := []any{
		[]any{map[string]any{"app": "Terminal", "reason": "denied"}},
		[]any{map[string]any{"app": "Terminal", "reason": "asking"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("blocked = %v, want %v", got, want)
	}
}

func TestListOmitsBlockedWhenNothingIs(t *testing.T) {
	s, ch, _ := newTestService(newFakeHost(), Window)
	defer s.Close("test")
	send(s, `{"t":"list","id":1,"ask":true}`)
	if m := ch.waitText(t, "sources", nil); m["blocked"] != nil {
		t.Fatalf("blocked = %v, want it absent", m["blocked"])
	}
}
