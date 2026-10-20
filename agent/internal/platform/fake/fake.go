// Package fake is an in-memory platform.Backend for tests: it emits a tiny
// fixed H.264 access unit at the configured fps and records injected input.
package fake

import (
	"sync"
	"time"

	"github.com/ruolez/remotectl/agent/internal/platform"
)

// A valid-enough Annex-B IDR access unit (SPS, PPS, IDR slice header bytes);
// the pion H.264 payloader only needs NAL boundaries.
var keyframeAU = []byte{
	0, 0, 0, 1, 0x67, 0x42, 0xe0, 0x1f, 0x8c, 0x8d, 0x40, 0x50, 0x1e, 0xd0, 0x0f, 0x08, 0x84, 0x6a,
	0, 0, 0, 1, 0x68, 0xce, 0x3c, 0x80,
	0, 0, 0, 1, 0x65, 0x88, 0x84, 0x00, 0x33, 0xff,
}
var deltaAU = []byte{0, 0, 0, 1, 0x41, 0x9a, 0x02, 0x04, 0x07}

type Backend struct {
	Perms       platform.Permissions
	MonitorList []platform.Monitor
	// Consent: when PromptFn is nil, ConsentPrompt returns ErrUnsupported.
	PromptFn func(title, message string) platform.Consent

	mu         sync.Mutex
	injected   []platform.InputEvent
	texts      []string
	clipboard  string
	indicator  bool
	keyReqs    int
	configured [2]int
	starts     []platform.CaptureConfig
	capturing  bool
	stop       chan struct{}
	wg         sync.WaitGroup
	sink       platform.CaptureSink
	forceKey   bool
}

func New() *Backend {
	return &Backend{
		Perms:       platform.Permissions{Screen: true, Input: true},
		MonitorList: []platform.Monitor{{ID: 1, Name: "Fake 1080p", Width: 1920, Height: 1080, Scale: 1, Primary: true}},
	}
}

func (b *Backend) Name() string                          { return "fake" }
func (b *Backend) Init() error                           { return nil }
func (b *Backend) Shutdown()                             { b.StopCapture() }
func (b *Backend) Monitors() ([]platform.Monitor, error) { return b.MonitorList, nil }
func (b *Backend) Permissions() platform.Permissions     { return b.Perms }
func (b *Backend) RequestPermission(bool)                {}
func (b *Backend) EncoderName() string                   { return "fake" }

func (b *Backend) StartCapture(cfg platform.CaptureConfig, sink platform.CaptureSink) error {
	b.mu.Lock()
	if b.capturing {
		b.mu.Unlock()
		return platform.ErrBusy
	}
	b.capturing, b.sink, b.stop, b.forceKey = true, sink, make(chan struct{}), true
	b.starts = append(b.starts, cfg)
	b.configured = [2]int{cfg.BitrateKbps, cfg.FPS}
	fps := max(cfg.FPS, 1)
	b.mu.Unlock()
	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		t := time.NewTicker(time.Second / time.Duration(fps))
		defer t.Stop()
		start := time.Now()
		n := 0
		sink.CursorShape(7, 2, 2, 0, 0, make([]byte, 16))
		for {
			select {
			case <-b.stop:
				return
			case <-t.C:
				b.mu.Lock()
				key := b.forceKey || n%(fps*2) == 0
				b.forceKey = false
				b.mu.Unlock()
				au := deltaAU
				if key {
					au = keyframeAU
				}
				sink.Frame(au, time.Since(start).Microseconds(), key)
				sink.CursorPos(uint16(n*100), 1000, true)
				n++
			}
		}
	}()
	return nil
}

func (b *Backend) StopCapture() {
	b.mu.Lock()
	if !b.capturing {
		b.mu.Unlock()
		return
	}
	b.capturing = false
	close(b.stop)
	b.mu.Unlock()
	b.wg.Wait()
}

func (b *Backend) ConfigureEncoder(kbps, fps int) {
	b.mu.Lock()
	b.configured = [2]int{kbps, fps}
	b.mu.Unlock()
}

func (b *Backend) RequestKeyframe() {
	b.mu.Lock()
	b.keyReqs++
	b.forceKey = true
	b.mu.Unlock()
}

func (b *Backend) Inject(ev platform.InputEvent) error {
	b.mu.Lock()
	b.injected = append(b.injected, ev)
	b.mu.Unlock()
	return nil
}

func (b *Backend) InjectText(s string) error {
	b.mu.Lock()
	b.texts = append(b.texts, s)
	b.mu.Unlock()
	return nil
}

func (b *Backend) ClipboardText() (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.clipboard, nil
}

func (b *Backend) SetClipboardText(s string) error {
	b.mu.Lock()
	b.clipboard = s
	b.mu.Unlock()
	return nil
}

func (b *Backend) Indicator(on bool) {
	b.mu.Lock()
	b.indicator = on
	b.mu.Unlock()
}

func (b *Backend) ConsentPrompt(title, message string, _ int) (platform.Consent, error) {
	if b.PromptFn == nil {
		return platform.ConsentDeny, platform.ErrUnsupported
	}
	return b.PromptFn(title, message), nil
}

// ---- test accessors ----

func (b *Backend) Injected() []platform.InputEvent {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]platform.InputEvent(nil), b.injected...)
}

func (b *Backend) Texts() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.texts...)
}

func (b *Backend) IndicatorOn() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.indicator
}

func (b *Backend) Capturing() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.capturing
}

// Starts lists every StartCapture config in order.
func (b *Backend) Starts() []platform.CaptureConfig {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]platform.CaptureConfig(nil), b.starts...)
}

// Configured is the encoder's current kbps and fps: the last ConfigureEncoder,
// or the start config's when none came after it.
func (b *Backend) Configured() (kbps, fps int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.configured[0], b.configured[1]
}

func (b *Backend) KeyframeRequests() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.keyReqs
}

var _ platform.Backend = (*Backend)(nil)
