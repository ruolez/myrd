package terminal

import (
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"
)

// MaxShells caps the programs the agent keeps running on pseudo terminals.
const MaxShells = 16

// nudgeDelay separates the two resizes that make a full-screen program
// redraw for a viewer who just attached.
const nudgeDelay = 40 * time.Millisecond

// Shells starts programs on pseudo terminals and keeps them running while
// no browser watches: shells survive a dropped connection and can be
// reopened from any session. Clients of tmux and screen sessions are
// ephemeral instead: they end with their last viewer (the session itself
// lives on in tmux / screen).
type Shells struct {
	spawn Spawner
	log   *slog.Logger
	now   func() time.Time

	mu     sync.Mutex
	next   int
	shells map[int]*shell
	closed bool
}

func NewShells(spawn Spawner, log *slog.Logger) *Shells {
	if log == nil {
		log = slog.Default()
	}
	return &Shells{spawn: spawn, log: log, now: time.Now, shells: map[int]*shell{}}
}

type shell struct {
	h         *Shells
	id        int
	kind      string // proto.TermSource*
	name      string // title until the program sets one
	detail    string
	ephemeral bool
	created   time.Time
	proc      Proc
	done      chan struct{}

	mu     sync.Mutex
	ring   *ring
	vt     *vtState
	views  map[*view]struct{}
	cols   int
	rows   int
	ended  bool
	code   *int
	killed bool
}

// Start runs spec and attaches the first viewer.
func (h *Shells) Start(spec SpawnSpec, kind, name, detail string, ephemeral bool) (Stream, Source, error) {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return nil, Source{}, ErrUnsupported
	}
	if len(h.shells) >= MaxShells {
		h.mu.Unlock()
		return nil, Source{}, ErrTooMany
	}
	h.next++
	id := h.next
	h.mu.Unlock()

	p, err := h.spawn(spec)
	if err != nil {
		return nil, Source{}, fmt.Errorf("start %s: %w", spec.Path, err)
	}
	s := &shell{h: h, id: id, kind: kind, name: name, detail: detail, ephemeral: ephemeral, created: h.now(),
		proc: p, done: make(chan struct{}), ring: newRing(RingSize), vt: newVTState(),
		views: map[*view]struct{}{}, cols: spec.Cols, rows: spec.Rows}
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		p.Hangup()
		go func() { p.Wait(); _ = p.Close() }()
		return nil, Source{}, ErrUnsupported
	}
	h.shells[id] = s
	h.mu.Unlock()
	v, src := s.attach(spec.Cols, spec.Rows)
	go s.run()
	h.log.Info("terminal started", "id", id, "kind", kind, "name", name)
	return v, src, nil
}

// Attach adds a viewer to a kept shell (Source.ID "shell:N").
func (h *Shells) Attach(id string, cols, rows int) (Stream, Source, error) {
	n, ok := strings.CutPrefix(id, "shell:")
	if !ok {
		return nil, Source{}, ErrNotFound
	}
	i, err := strconv.Atoi(n)
	if err != nil {
		return nil, Source{}, ErrNotFound
	}
	h.mu.Lock()
	s := h.shells[i]
	h.mu.Unlock()
	if s == nil || s.ephemeral {
		return nil, Source{}, ErrNotFound
	}
	v, src := s.attach(cols, rows)
	if v == nil {
		return nil, Source{}, ErrNotFound
	}
	return v, src, nil
}

// List describes the kept shells, oldest first.
func (h *Shells) List() []Source {
	h.mu.Lock()
	ss := make([]*shell, 0, len(h.shells))
	for _, s := range h.shells {
		if !s.ephemeral {
			ss = append(ss, s)
		}
	}
	h.mu.Unlock()
	out := make([]Source, 0, len(ss))
	for _, s := range ss {
		s.mu.Lock()
		if !s.ended {
			out = append(out, s.sourceLocked())
		}
		s.mu.Unlock()
	}
	sortSources(out)
	return out
}

// Close hangs up every program (the agent is stopping).
func (h *Shells) Close() {
	h.mu.Lock()
	h.closed = true
	ss := make([]*shell, 0, len(h.shells))
	for _, s := range h.shells {
		ss = append(ss, s)
	}
	h.mu.Unlock()
	for _, s := range ss {
		s.proc.Hangup()
	}
	for _, s := range ss {
		select {
		case <-s.done:
		case <-time.After(3 * time.Second):
		}
	}
}

func (h *Shells) remove(s *shell) {
	h.mu.Lock()
	if h.shells[s.id] == s {
		delete(h.shells, s.id)
	}
	h.mu.Unlock()
}

func (s *shell) sourceLocked() Source {
	title := s.vt.title
	if title == "" {
		title = s.name
	}
	return Source{ID: "shell:" + strconv.Itoa(s.id), Kind: s.kind, Title: title, Detail: s.detail,
		Attached: len(s.views), Fidelity: "full", Created: s.created.UnixMilli()}
}

func (s *shell) attach(cols, rows int) (*view, Source) {
	s.mu.Lock()
	if s.ended {
		s.mu.Unlock()
		return nil, Source{}
	}
	v := &view{s: s, q: newByteQueue()}
	v.q.data = replay(s.ring, s.vt)
	s.views[v] = struct{}{}
	src := s.sourceLocked()
	same := cols == s.cols && rows == s.rows
	alt := s.vt.alt
	s.mu.Unlock()
	if same && alt && rows > 1 {
		// A full-screen program redraws on SIGWINCH, which only a real size
		// change sends.
		_ = s.resize(cols, rows-1)
		go func() {
			time.Sleep(nudgeDelay)
			_ = s.resize(cols, rows)
		}()
	} else if !same {
		_ = s.resize(cols, rows)
	}
	return v, src
}

func (s *shell) resize(cols, rows int) error {
	s.mu.Lock()
	ended := s.ended
	s.cols, s.rows = cols, rows
	s.mu.Unlock()
	if ended {
		return nil
	}
	return s.proc.Resize(cols, rows)
}

// run copies the program's output to the ring and every viewer until it
// exits.
func (s *shell) run() {
	buf := make([]byte, 32<<10)
	for {
		n, err := s.proc.Read(buf)
		if n > 0 {
			chunk := append([]byte(nil), buf[:n]...)
			s.mu.Lock()
			s.ring.write(chunk)
			s.vt.feed(chunk)
			vs := make([]*view, 0, len(s.views))
			for v := range s.views {
				vs = append(vs, v)
			}
			s.mu.Unlock()
			for _, v := range vs {
				v.q.push(chunk) // waits while that viewer is behind
			}
		}
		if err != nil {
			break
		}
	}
	code := s.proc.Wait()
	_ = s.proc.Close()
	s.mu.Lock()
	s.ended = true
	s.code = &code
	vs := s.views
	s.views = map[*view]struct{}{}
	s.mu.Unlock()
	for v := range vs {
		v.q.close()
	}
	s.h.remove(s)
	close(s.done)
	s.h.log.Info("terminal ended", "id", s.id, "kind", s.kind, "exit_code", code)
}

// view is one viewer's attachment.
type view struct {
	s    *shell
	q    *byteQueue
	once sync.Once
}

var _ Stream = (*view)(nil)

func (v *view) Read(p []byte) (int, error) { return v.q.read(p) }

func (v *view) Write(p []byte) (int, error) {
	v.s.mu.Lock()
	ended := v.s.ended
	v.s.mu.Unlock()
	if ended {
		return 0, io.ErrClosedPipe
	}
	return v.s.proc.Write(p)
}

func (v *view) Resize(cols, rows int) error { return v.s.resize(cols, rows) }

func (v *view) Detach() {
	v.once.Do(func() {
		s := v.s
		s.mu.Lock()
		delete(s.views, v)
		last := len(s.views) == 0 && !s.ended
		s.mu.Unlock()
		v.q.drop()
		if last && s.ephemeral {
			s.proc.Hangup()
		}
	})
}

func (v *view) Kill() error {
	v.s.mu.Lock()
	v.s.killed = true
	v.s.mu.Unlock()
	v.s.proc.Hangup()
	return nil
}

func (v *view) Title() string {
	v.s.mu.Lock()
	defer v.s.mu.Unlock()
	if v.s.vt.title != "" {
		return v.s.vt.title
	}
	return v.s.name
}

func (v *view) ExitCode() *int {
	v.s.mu.Lock()
	defer v.s.mu.Unlock()
	return v.s.code
}
