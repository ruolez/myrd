package capproc

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ruolez/remotectl/agent/internal/platform"
)

// Process is a running capture worker.
type Process struct {
	R    io.Reader      // worker → agent
	W    io.WriteCloser // agent → worker; closing it asks the worker to exit
	Wait func() error   // returns once the worker has exited
	Kill func()
}

// Options configures a Backend.
type Options struct {
	// Spawn starts a worker. When it fails, capture runs in this process.
	Spawn func() (*Process, error)
	// Linger keeps an idle worker after StopCapture, so a restart (a new
	// size, another monitor) reuses it. Default 5 s.
	Linger time.Duration
	// OnRetire runs after a worker has exited: the agent gives back the
	// memory its own side of the session used.
	OnRetire func()
	Log      *slog.Logger
}

const (
	callTimeout  = 30 * time.Second // a start can wait for the GPU, an encoder probe or a permission check
	exitTimeout  = 3 * time.Second
	maxRecovers  = 3 // replacements for crashed workers per recoverWindow
	recoverEvery = time.Minute
)

var errWorkerGone = errors.New("capture worker exited")

// Backend runs capture and encoding in a worker process and everything else
// (input, clipboard, displays, previews, prompts) on the wrapped backend.
type Backend struct {
	platform.Backend
	o Options

	op         sync.Mutex // orders Start/Stop/recover/retire
	mu         sync.Mutex
	w          *worker
	capturing  bool
	inproc     bool // this capture runs on the wrapped backend
	cfg        platform.CaptureConfig
	sink       platform.CaptureSink
	linger     *time.Timer
	recoveries []time.Time
	// inProcessOnly: a worker was refused the screen this process may
	// capture (macOS judging the child apart from the app); no more workers.
	inProcessOnly bool
}

// New wraps b; b must already be initialized by the caller as usual.
func New(b platform.Backend, o Options) *Backend {
	if o.Linger <= 0 {
		o.Linger = 5 * time.Second
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	return &Backend{Backend: b, o: o}
}

// Memory is the running worker's last reported memory in bytes; 0 when no
// worker runs.
func (b *Backend) Memory() uint64 {
	b.mu.Lock()
	w := b.w
	b.mu.Unlock()
	if w == nil {
		return 0
	}
	return w.mem.Load()
}

func (b *Backend) StartCapture(cfg platform.CaptureConfig, sink platform.CaptureSink) error {
	b.op.Lock()
	defer b.op.Unlock()
	b.mu.Lock()
	if b.capturing {
		b.mu.Unlock()
		return platform.ErrBusy
	}
	if b.linger != nil {
		b.linger.Stop()
		b.linger = nil
	}
	w, inProcessOnly := b.w, b.inProcessOnly
	b.mu.Unlock()
	if inProcessOnly {
		return b.startInProcess(cfg, sink)
	}
	// A worker that ran full color keeps that encoder's memory: start fresh.
	if w != nil && (w.gone() || (w.fullColor && cfg.Codec != platform.CodecVP9)) {
		b.retire(w)
		w = nil
	}
	if w == nil {
		var err error
		if w, err = b.spawn(); err != nil {
			b.o.Log.Warn("capture worker unavailable, capturing in this process", "err", err)
			return b.startInProcess(cfg, sink)
		}
	}
	err := w.start(cfg, sink)
	if errors.Is(err, errWorkerGone) {
		b.o.Log.Warn("capture worker failed to start capture, capturing in this process")
		b.retire(w)
		return b.startInProcess(cfg, sink)
	}
	if errors.Is(err, platform.ErrPermission) && b.Backend.Permissions().Screen {
		b.o.Log.Warn("capture worker may not record the screen; capturing in this process from now on")
		b.mu.Lock()
		b.inProcessOnly = true
		b.mu.Unlock()
		b.retire(w)
		return b.startInProcess(cfg, sink)
	}
	if err != nil {
		return err
	}
	b.mu.Lock()
	b.capturing, b.cfg, b.sink = true, cfg, sink
	b.mu.Unlock()
	return nil
}

func (b *Backend) startInProcess(cfg platform.CaptureConfig, sink platform.CaptureSink) error {
	if err := b.Backend.StartCapture(cfg, sink); err != nil {
		return err
	}
	b.mu.Lock()
	b.capturing, b.inproc, b.cfg, b.sink = true, true, cfg, sink
	b.mu.Unlock()
	return nil
}

func (b *Backend) StopCapture() {
	b.op.Lock()
	defer b.op.Unlock()
	b.mu.Lock()
	if !b.capturing {
		b.mu.Unlock()
		return
	}
	inproc, w := b.inproc, b.w
	b.capturing, b.inproc, b.sink = false, false, nil
	b.mu.Unlock()
	if inproc {
		b.Backend.StopCapture()
		b.mu.Lock()
		b.linger = time.AfterFunc(b.o.Linger, b.releaseIdle)
		b.mu.Unlock()
		return
	}
	if w == nil {
		return
	}
	if err := w.stop(); err != nil {
		b.retire(w)
		return
	}
	b.mu.Lock()
	b.linger = time.AfterFunc(b.o.Linger, func() { b.retireIdle(w) })
	b.mu.Unlock()
}

// retireIdle ends w unless a capture started on it meanwhile.
func (b *Backend) retireIdle(w *worker) {
	b.op.Lock()
	defer b.op.Unlock()
	b.mu.Lock()
	idle := b.w == w && !b.capturing
	b.mu.Unlock()
	if idle {
		b.retire(w)
	}
}

// releaseIdle gives back memory after an in-process capture, unless another
// one started meanwhile.
func (b *Backend) releaseIdle() {
	b.mu.Lock()
	idle := !b.capturing
	b.mu.Unlock()
	if idle && b.o.OnRetire != nil {
		b.o.OnRetire()
	}
}

func (b *Backend) ConfigureEncoder(kbps, fps int) {
	if w, inproc := b.current(); inproc {
		b.Backend.ConfigureEncoder(kbps, fps)
	} else if w != nil {
		w.send(kindConfigure, ctl{Kbps: kbps, FPS: fps})
	}
}

func (b *Backend) RequestKeyframe() {
	if w, inproc := b.current(); inproc {
		b.Backend.RequestKeyframe()
	} else if w != nil {
		w.send(kindKeyframe, ctl{})
	}
}

func (b *Backend) RefineEncoder(maxQP int) error {
	w, inproc := b.current()
	switch {
	case inproc:
		return b.Backend.RefineEncoder(maxQP)
	case w == nil:
		return errors.New("not capturing")
	}
	return w.call(kindRefine, ctl{QP: maxQP})
}

// EncoderName is the worker's encoder while it captures: the native side
// can fall back to another one mid-stream.
func (b *Backend) EncoderName() string {
	if w, _ := b.current(); w != nil {
		if name := w.encoderName(); name != "" {
			return name
		}
	}
	return b.Backend.EncoderName()
}

// Snapshot keeps the in-process meaning of ErrBusy: no pictures while a
// session captures.
func (b *Backend) Snapshot(monitorID uint32, maxW, maxH int) (platform.Image, error) {
	if w, _ := b.current(); w != nil {
		return platform.Image{}, platform.ErrBusy
	}
	return b.Backend.Snapshot(monitorID, maxW, maxH)
}

func (b *Backend) Shutdown() {
	b.StopCapture()
	b.op.Lock()
	b.mu.Lock()
	w := b.w
	if b.linger != nil {
		b.linger.Stop()
		b.linger = nil
	}
	b.mu.Unlock()
	if w != nil {
		b.retire(w)
	}
	b.op.Unlock()
	b.Backend.Shutdown()
}

// current is the worker capturing now (nil when none is) and whether the
// capture runs in this process instead.
func (b *Backend) current() (*worker, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.capturing || b.inproc {
		return nil, b.capturing && b.inproc
	}
	return b.w, false
}

// spawn starts a worker and makes it current; caller holds op.
func (b *Backend) spawn() (*worker, error) {
	p, err := b.o.Spawn()
	if err != nil {
		return nil, err
	}
	w := &worker{p: p, calls: map[uint32]chan ctl{}, done: make(chan struct{}), out: bufio.NewWriterSize(p.W, 64<<10)}
	b.mu.Lock()
	b.w = w
	b.mu.Unlock()
	go func() {
		w.read()
		b.lost(w)
	}()
	return w, nil
}

// retire ends w and waits for it; caller holds op.
func (b *Backend) retire(w *worker) {
	b.mu.Lock()
	if b.w == w {
		b.w = nil
	}
	b.mu.Unlock()
	w.retiring.Store(true)
	_ = w.p.W.Close()
	exited := make(chan struct{})
	go func() {
		_ = w.p.Wait()
		close(exited)
	}()
	select {
	case <-exited:
	case <-time.After(exitTimeout):
		b.o.Log.Warn("capture worker did not exit; killing it")
		w.p.Kill()
		<-exited
	}
	if b.o.OnRetire != nil {
		b.o.OnRetire()
	}
}

// lost runs when w's output ends. A worker that dies mid-capture (a crash
// in a driver or an encoder) is replaced and capture resumes.
func (b *Backend) lost(w *worker) {
	if w.retiring.Load() {
		return
	}
	b.op.Lock()
	defer b.op.Unlock()
	b.mu.Lock()
	if b.w != w {
		b.mu.Unlock()
		return
	}
	b.w = nil
	capturing, cfg, sink := b.capturing && !b.inproc, b.cfg, b.sink
	now := time.Now()
	recent := b.recoveries[:0]
	for _, t := range b.recoveries {
		if now.Sub(t) < recoverEvery {
			recent = append(recent, t)
		}
	}
	b.recoveries = recent
	tooMany := len(recent) >= maxRecovers
	if capturing && !tooMany {
		b.recoveries = append(b.recoveries, now)
	}
	if capturing && tooMany {
		b.capturing = false
	}
	b.mu.Unlock()
	w.retiring.Store(true)
	_ = w.p.W.Close()
	_ = w.p.Wait()
	if !capturing {
		return
	}
	if tooMany {
		b.o.Log.Error("capture worker keeps exiting; capture stopped")
		return
	}
	b.o.Log.Error("capture worker exited during capture; starting a new one")
	nw, err := b.spawn()
	if err == nil {
		if err = nw.start(cfg, sink); err == nil {
			return
		}
	}
	b.o.Log.Error("capture could not resume", "err", err)
	b.mu.Lock()
	b.capturing = false
	b.mu.Unlock()
}

// worker is the agent's side of one worker process.
type worker struct {
	p         *Process
	fullColor bool // set by start; read under Backend.op
	retiring  atomic.Bool
	mem       atomic.Uint64

	wmu sync.Mutex
	out *bufio.Writer

	mu      sync.Mutex
	seq     uint32
	calls   map[uint32]chan ctl
	sink    platform.CaptureSink
	encoder string
	done    chan struct{}
}

func (w *worker) gone() bool {
	select {
	case <-w.done:
		return true
	default:
		return false
	}
}

func (w *worker) encoderName() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.encoder
}

func (w *worker) start(cfg platform.CaptureConfig, sink platform.CaptureSink) error {
	w.mu.Lock()
	w.sink = sink // frames may arrive before the reply
	w.mu.Unlock()
	err := w.call(kindStart, ctl{Config: &cfg})
	if err != nil {
		w.mu.Lock()
		w.sink = nil
		w.mu.Unlock()
		return err
	}
	if cfg.Codec == platform.CodecVP9 {
		w.fullColor = true
	}
	return nil
}

// stop returns after the worker stopped capture: every frame it sent before
// has been delivered, and none follow.
func (w *worker) stop() error {
	err := w.call(kindStop, ctl{})
	w.mu.Lock()
	w.sink = nil
	w.mu.Unlock()
	return err
}

func (w *worker) send(kind byte, c ctl) error {
	body, err := json.Marshal(c)
	if err != nil {
		return err
	}
	w.wmu.Lock()
	defer w.wmu.Unlock()
	if err := writeMsg(w.out, kind, body); err != nil {
		return errWorkerGone
	}
	if w.out.Flush() != nil {
		return errWorkerGone
	}
	return nil
}

// call sends a request and waits for its reply. Errors from the backend come
// back as platform errors; a dead or stuck worker is errWorkerGone.
func (w *worker) call(kind byte, c ctl) error {
	ch := make(chan ctl, 1)
	w.mu.Lock()
	w.seq++
	c.Seq = w.seq
	w.calls[c.Seq] = ch
	w.mu.Unlock()
	defer func() {
		w.mu.Lock()
		delete(w.calls, c.Seq)
		w.mu.Unlock()
	}()
	if err := w.send(kind, c); err != nil {
		return err
	}
	select {
	case r := <-ch:
		if r.Encoder != "" {
			w.mu.Lock()
			w.encoder = r.Encoder
			w.mu.Unlock()
		}
		return codeErr(r.Code, r.Msg)
	case <-w.done:
		return errWorkerGone
	case <-time.After(callTimeout):
		w.p.Kill()
		return errWorkerGone
	}
}

// read delivers the worker's messages until its output ends.
func (w *worker) read() {
	defer close(w.done)
	r := bufio.NewReaderSize(w.p.R, 256<<10)
	for {
		kind, body, err := readMsg(r)
		if err != nil {
			return
		}
		switch kind {
		case kindFrame:
			if pts, flags, data, ok := parseFrame(body); ok && len(data) > 0 {
				if s := w.currentSink(); s != nil {
					s.Frame(data, pts, flags)
				}
			}
		case kindShape:
			if len(body) > 12 {
				if s := w.currentSink(); s != nil {
					s.CursorShape(be32(body), be16(body[4:]), be16(body[6:]), be16(body[8:]), be16(body[10:]), body[12:])
				}
			}
		case kindPos:
			if len(body) == 5 {
				if s := w.currentSink(); s != nil {
					s.CursorPos(be16(body), be16(body[2:]), body[4] != 0)
				}
			}
		case kindReply, kindStatus:
			var c ctl
			if json.Unmarshal(body, &c) != nil {
				continue
			}
			if kind == kindStatus {
				w.status(c)
				continue
			}
			w.mu.Lock()
			ch := w.calls[c.Seq]
			w.mu.Unlock()
			if ch != nil {
				ch <- c
			}
		}
	}
}

func (w *worker) status(c ctl) {
	if c.Mem != 0 {
		w.mem.Store(c.Mem)
	}
	if c.Encoder != "" {
		w.mu.Lock()
		w.encoder = c.Encoder
		w.mu.Unlock()
	}
}

func (w *worker) currentSink() platform.CaptureSink {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.sink
}

func be16(b []byte) uint16 { return uint16(b[0])<<8 | uint16(b[1]) }
func be32(b []byte) uint32 { return uint32(be16(b))<<16 | uint32(be16(b[2:])) }
