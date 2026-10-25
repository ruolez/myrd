package capproc

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ruolez/remotectl/agent/internal/platform"
	"github.com/ruolez/remotectl/agent/internal/platform/fake"
)

// TestMain doubles as a capture worker process for TestCommandRunsARealWorker.
func TestMain(m *testing.M) {
	if os.Getenv("CAPPROC_TEST_WORKER") == "1" {
		wire, err := TakeStdout()
		if err != nil {
			os.Exit(3)
		}
		os.Stdout.WriteString("stray output that must not reach the wire\n")
		os.Stderr.WriteString("worker started\n")
		fb := fake.New()
		if err := Serve(os.Stdin, wire, fb, WorkerOptions{Memory: func() uint64 { return 42 << 20 }}); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestWireRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		kind byte
		body []byte
	}{
		{"empty body", kindKeyframe, nil},
		{"control", kindStart, []byte(`{"seq":1}`)},
		{"large frame", kindFrame, bytes.Repeat([]byte{0xab}, 300_000)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var buf bytes.Buffer
			if err := writeMsg(&buf, c.kind, c.body); err != nil {
				t.Fatal(err)
			}
			kind, body, err := readMsg(&buf)
			if err != nil || kind != c.kind || !bytes.Equal(body, c.body) {
				t.Fatalf("got kind %d, %d bytes, err %v; want kind %d, %d bytes", kind, len(body), err, c.kind, len(c.body))
			}
		})
	}
}

func TestReadMsgRejectsOversizedMessages(t *testing.T) {
	var buf bytes.Buffer
	buf.Write([]byte{0xff, 0xff, 0xff, 0xff, kindFrame})
	if _, _, err := readMsg(&buf); err == nil {
		t.Fatal("a 4 GiB length was accepted")
	}
}

func TestErrorCodesRoundTrip(t *testing.T) {
	for _, want := range []error{nil, platform.ErrPermission, platform.ErrUnsupported, platform.ErrBusy,
		platform.ErrLocked, platform.ErrAsleep} {
		code, msg := errCode(want)
		if got := codeErr(code, msg); got != want {
			t.Errorf("%v came back as %v", want, got)
		}
	}
	code, msg := errCode(errors.New("native error -9"))
	if got := codeErr(code, msg); got == nil || got.Error() != "native error -9" {
		t.Errorf("other error came back as %v", got)
	}
}

func TestFrameRoundTrip(t *testing.T) {
	data := []byte{0, 0, 0, 1, 0x65, 0x88}
	body := frameBody(1234567, platform.FrameKey|platform.FrameMinor, data)
	pts, flags, got, ok := parseFrame(body)
	if !ok || pts != 1234567 || flags != platform.FrameKey|platform.FrameMinor || !bytes.Equal(got, data) {
		t.Fatalf("got pts %d flags %d data %x ok %v", pts, flags, got, ok)
	}
}

// recorder is a CaptureSink that keeps what it was given.
type recorder struct {
	mu     sync.Mutex
	frames []platform.FrameFlags
	shapes []uint32
	pos    int
}

func (r *recorder) Frame(_ []byte, _ int64, f platform.FrameFlags) {
	r.mu.Lock()
	r.frames = append(r.frames, f)
	r.mu.Unlock()
}

func (r *recorder) CursorShape(id uint32, _, _, _, _ uint16, _ []byte) {
	r.mu.Lock()
	r.shapes = append(r.shapes, id)
	r.mu.Unlock()
}

func (r *recorder) CursorPos(uint16, uint16, bool) {
	r.mu.Lock()
	r.pos++
	r.mu.Unlock()
}

func (r *recorder) counts() (frames, shapes, pos int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.frames), len(r.shapes), r.pos
}

func (r *recorder) firstFrame() platform.FrameFlags {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.frames) == 0 {
		return 0
	}
	return r.frames[0]
}

// harness runs workers in this process over pipes: each spawn gets a new
// fake backend, as a real worker process gets a new native backend.
type harness struct {
	// serve wraps a worker's fake backend; nil serves it as is.
	serve    func(*fake.Backend) platform.Backend
	mu       sync.Mutex
	workers  []*fake.Backend
	spawns   atomic.Int32
	failNext atomic.Bool
	retired  atomic.Int32
	kill     []func() // closes the pipes of a worker, as if it crashed
	mem      uint64
}

func (h *harness) spawn() (*Process, error) {
	h.spawns.Add(1)
	if h.failNext.Swap(false) {
		return nil, errors.New("exec: no such file")
	}
	fb := fake.New()
	fb.SetFullColor(true)
	toWorker, fromAgent := io.Pipe()
	toAgent, fromWorker := io.Pipe()
	done := make(chan struct{})
	var served platform.Backend = fb
	if h.serve != nil {
		served = h.serve(fb)
	}
	go func() {
		_ = Serve(toWorker, fromWorker, served, WorkerOptions{Memory: func() uint64 { return h.mem }, StatusEvery: 10 * time.Millisecond})
		fromWorker.Close()
		close(done)
	}()
	h.mu.Lock()
	h.workers = append(h.workers, fb)
	h.kill = append(h.kill, func() {
		toWorker.CloseWithError(errors.New("crashed"))
		toAgent.CloseWithError(errors.New("crashed"))
	})
	h.mu.Unlock()
	return &Process{R: toAgent, W: fromAgent, Wait: func() error { <-done; return nil },
		Kill: func() { toWorker.Close(); toAgent.Close() }}, nil
}

func (h *harness) worker(i int) *fake.Backend {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.workers[i]
}

func (h *harness) crash(i int) {
	h.mu.Lock()
	k := h.kill[i]
	h.mu.Unlock()
	k()
}

func newTestBackend(t *testing.T, linger time.Duration) (*Backend, *harness, *fake.Backend) {
	t.Helper()
	h := &harness{mem: 123 << 20}
	inproc := fake.New()
	b := New(inproc, Options{Spawn: h.spawn, Linger: linger, OnRetire: func() { h.retired.Add(1) },
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	t.Cleanup(b.Shutdown)
	return b, h, inproc
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

var cfgH264 = platform.CaptureConfig{FPS: 50, BitrateKbps: 2000, MaxWidth: 1280, MaxHeight: 720, Codec: platform.CodecH264}

func TestCaptureRunsInTheWorker(t *testing.T) {
	b, h, inproc := newTestBackend(t, time.Hour)
	var rec recorder
	if err := b.StartCapture(cfgH264, &rec); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "frames, a cursor shape and positions", func() bool {
		f, s, p := rec.counts()
		return f >= 3 && s >= 1 && p >= 3
	})
	if rec.firstFrame()&platform.FrameKey == 0 {
		t.Error("the first frame is not a keyframe")
	}
	if inproc.Capturing() || !h.worker(0).Capturing() {
		t.Fatalf("capturing in process %v, in the worker %v; want the worker only", inproc.Capturing(), h.worker(0).Capturing())
	}
	if got := h.worker(0).Starts(); len(got) != 1 || got[0] != cfgH264 {
		t.Fatalf("worker starts %+v, want [%+v]", got, cfgH264)
	}
}

func TestEncoderCommandsReachTheWorker(t *testing.T) {
	b, h, _ := newTestBackend(t, time.Hour)
	var rec recorder
	if err := b.StartCapture(cfgH264, &rec); err != nil {
		t.Fatal(err)
	}
	b.ConfigureEncoder(3500, 24)
	b.RequestKeyframe()
	if err := b.RefineEncoder(30); err != nil {
		t.Fatalf("refine: %v", err)
	}
	w := h.worker(0)
	waitFor(t, "configure and keyframe", func() bool {
		kbps, fps := w.Configured()
		return kbps == 3500 && fps == 24 && w.KeyframeRequests() == 1
	})
	if got := w.Refines(); len(got) != 1 || got[0] != 30 {
		t.Fatalf("refines %v, want [30]", got)
	}
	w.SetRefineUnsupported(true)
	if err := b.RefineEncoder(30); !errors.Is(err, platform.ErrUnsupported) {
		t.Fatalf("refine on an encoder without a quantizer cap: %v, want ErrUnsupported", err)
	}
}

func TestStartErrorsComeBackAsPlatformErrors(t *testing.T) {
	b, h, _ := newTestBackend(t, time.Hour)
	var rec recorder
	if err := b.StartCapture(cfgH264, &rec); err != nil {
		t.Fatal(err)
	}
	b.StopCapture()
	h.worker(0).SetFullColorFails(true)
	vp9 := cfgH264
	vp9.Codec = platform.CodecVP9
	if err := b.StartCapture(vp9, &rec); !errors.Is(err, platform.ErrUnsupported) {
		t.Fatalf("start: %v, want ErrUnsupported", err)
	}
	if err := b.StartCapture(cfgH264, &rec); err != nil {
		t.Fatalf("H.264 after a refused full-color start: %v", err)
	}
	if h.spawns.Load() != 1 {
		t.Fatalf("%d workers spawned, want 1: a refused start keeps the worker", h.spawns.Load())
	}
}

func TestNoCallbacksAfterStopCapture(t *testing.T) {
	b, _, _ := newTestBackend(t, time.Hour)
	var rec recorder
	if err := b.StartCapture(cfgH264, &rec); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "frames", func() bool { f, _, _ := rec.counts(); return f >= 2 })
	b.StopCapture()
	f, s, p := rec.counts()
	time.Sleep(100 * time.Millisecond)
	if f2, s2, p2 := rec.counts(); f2 != f || s2 != s || p2 != p {
		t.Fatalf("callbacks after StopCapture: frames %d→%d shapes %d→%d positions %d→%d", f, f2, s, s2, p, p2)
	}
}

func TestRestartsReuseTheWorkerUntilItLingers(t *testing.T) {
	b, h, _ := newTestBackend(t, 150*time.Millisecond)
	var rec recorder
	for range 3 {
		if err := b.StartCapture(cfgH264, &rec); err != nil {
			t.Fatal(err)
		}
		b.StopCapture()
	}
	if h.spawns.Load() != 1 {
		t.Fatalf("%d workers for back-to-back restarts, want 1", h.spawns.Load())
	}
	waitFor(t, "the idle worker to retire", func() bool { return h.retired.Load() == 1 })
	if b.Memory() != 0 {
		t.Fatalf("memory %d after the worker retired, want 0", b.Memory())
	}
	if err := b.StartCapture(cfgH264, &rec); err != nil {
		t.Fatal(err)
	}
	if h.spawns.Load() != 2 {
		t.Fatalf("%d workers after the idle one retired, want 2", h.spawns.Load())
	}
}

func TestLeavingFullColorReplacesTheWorker(t *testing.T) {
	b, h, _ := newTestBackend(t, time.Hour)
	var rec recorder
	vp9 := cfgH264
	vp9.Codec = platform.CodecVP9
	if err := b.StartCapture(vp9, &rec); err != nil {
		t.Fatal(err)
	}
	b.StopCapture()
	if err := b.StartCapture(vp9, &rec); err != nil {
		t.Fatal(err)
	}
	b.StopCapture()
	if h.spawns.Load() != 1 {
		t.Fatalf("%d workers for full color twice, want 1", h.spawns.Load())
	}
	if err := b.StartCapture(cfgH264, &rec); err != nil {
		t.Fatal(err)
	}
	if h.spawns.Load() != 2 || h.retired.Load() != 1 {
		t.Fatalf("spawns %d retired %d after leaving full color, want 2 and 1", h.spawns.Load(), h.retired.Load())
	}
}

func TestSpawnFailureCapturesInProcess(t *testing.T) {
	b, h, inproc := newTestBackend(t, time.Hour)
	h.failNext.Store(true)
	var rec recorder
	if err := b.StartCapture(cfgH264, &rec); err != nil {
		t.Fatal(err)
	}
	if !inproc.Capturing() {
		t.Fatal("not capturing in process after the worker failed to start")
	}
	b.ConfigureEncoder(900, 15)
	if kbps, fps := inproc.Configured(); kbps != 900 || fps != 15 {
		t.Fatalf("in-process encoder configured %d/%d, want 900/15", kbps, fps)
	}
	b.StopCapture()
	if inproc.Capturing() {
		t.Fatal("still capturing in process after StopCapture")
	}
}

func TestACrashedWorkerIsReplacedMidCapture(t *testing.T) {
	b, h, _ := newTestBackend(t, time.Hour)
	var rec recorder
	if err := b.StartCapture(cfgH264, &rec); err != nil {
		t.Fatal(err)
	}
	h.crash(0)
	waitFor(t, "a second worker", func() bool { return h.spawns.Load() == 2 })
	waitFor(t, "the second worker capturing", func() bool { return h.worker(1).Capturing() })
	if got := h.worker(1).Starts(); len(got) != 1 || got[0] != cfgH264 {
		t.Fatalf("replacement started with %+v, want [%+v]", got, cfgH264)
	}
	f, _, _ := rec.counts()
	waitFor(t, "frames from the replacement", func() bool { f2, _, _ := rec.counts(); return f2 > f+2 })
}

func TestSnapshotIsBusyWhileTheWorkerCaptures(t *testing.T) {
	b, _, _ := newTestBackend(t, time.Hour)
	var rec recorder
	if err := b.StartCapture(cfgH264, &rec); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Snapshot(0, 480, 480); !errors.Is(err, platform.ErrBusy) {
		t.Fatalf("snapshot during capture: %v, want ErrBusy", err)
	}
	b.StopCapture()
	if _, err := b.Snapshot(0, 480, 480); errors.Is(err, platform.ErrBusy) {
		t.Fatal("snapshot still busy after StopCapture")
	}
}

func TestWorkerReportsEncoderAndMemory(t *testing.T) {
	b, h, _ := newTestBackend(t, time.Hour)
	if b.Memory() != 0 {
		t.Fatalf("memory %d before any worker, want 0", b.Memory())
	}
	var rec recorder
	if err := b.StartCapture(cfgH264, &rec); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "a memory report", func() bool { return b.Memory() == h.mem })
	if got := b.EncoderName(); got != "fake" {
		t.Fatalf("encoder %q, want the worker's %q", got, "fake")
	}
}

func TestShutdownEndsTheWorker(t *testing.T) {
	b, h, _ := newTestBackend(t, time.Hour)
	var rec recorder
	if err := b.StartCapture(cfgH264, &rec); err != nil {
		t.Fatal(err)
	}
	b.Shutdown()
	if h.worker(0).Capturing() {
		t.Fatal("the worker still captures after Shutdown")
	}
	if h.retired.Load() != 1 {
		t.Fatalf("retired %d, want 1", h.retired.Load())
	}
}

func TestInProcessCaptureReleasesMemoryWhenIdle(t *testing.T) {
	b, h, _ := newTestBackend(t, 50*time.Millisecond)
	h.failNext.Store(true)
	var rec recorder
	if err := b.StartCapture(cfgH264, &rec); err != nil {
		t.Fatal(err)
	}
	b.StopCapture()
	waitFor(t, "memory released after the in-process capture", func() bool { return h.retired.Load() == 1 })
}

// syncBuffer is a log destination written from the stderr relay goroutine.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestCommandRunsARealWorker(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("CAPPROC_TEST_WORKER", "1")
	var logs syncBuffer
	log := slog.New(slog.NewTextHandler(&logs, nil))
	var retired atomic.Int32
	b := New(fake.New(), Options{Spawn: Command(exe, []string{"-test.run=^$"}, log), Linger: 50 * time.Millisecond,
		OnRetire: func() { retired.Add(1) }, Log: log})
	defer b.Shutdown()
	var rec recorder
	if err := b.StartCapture(cfgH264, &rec); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "frames from the worker process", func() bool { f, _, _ := rec.counts(); return f >= 3 })
	waitFor(t, "its memory report", func() bool { return b.Memory() == 42<<20 })
	b.StopCapture()
	waitFor(t, "the worker process to exit", func() bool { return retired.Load() == 1 })
	for _, want := range []string{"worker started", "stray output that must not reach the wire"} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("log lacks the worker's %q:\n%s", want, logs.String())
		}
	}
}

// screenDenied is a worker the OS does not let capture the screen.
type screenDenied struct{ *fake.Backend }

func (screenDenied) StartCapture(platform.CaptureConfig, platform.CaptureSink) error {
	return platform.ErrPermission
}

func TestWorkerWithoutTheAgentsScreenPermissionCapturesInProcess(t *testing.T) {
	b, h, inproc := newTestBackend(t, time.Hour)
	h.serve = func(fb *fake.Backend) platform.Backend { return screenDenied{fb} }
	inproc.Perms = platform.Permissions{Screen: true, Input: true}
	var rec recorder
	if err := b.StartCapture(cfgH264, &rec); err != nil {
		t.Fatalf("start: %v, want capture in process", err)
	}
	if !inproc.Capturing() {
		t.Fatal("not capturing in process")
	}
	b.StopCapture()
	if err := b.StartCapture(cfgH264, &rec); err != nil {
		t.Fatal(err)
	}
	if h.spawns.Load() != 1 || !inproc.Capturing() {
		t.Fatalf("spawns %d, in process %v; want no second worker", h.spawns.Load(), inproc.Capturing())
	}
}

func TestPermissionErrorPassesThroughWhenTheAgentLacksItToo(t *testing.T) {
	b, h, inproc := newTestBackend(t, time.Hour)
	h.serve = func(fb *fake.Backend) platform.Backend { return screenDenied{fb} }
	inproc.Perms = platform.Permissions{Input: true}
	var rec recorder
	if err := b.StartCapture(cfgH264, &rec); !errors.Is(err, platform.ErrPermission) {
		t.Fatalf("start: %v, want ErrPermission", err)
	}
	if inproc.Capturing() {
		t.Fatal("captured in process without permission")
	}
}
