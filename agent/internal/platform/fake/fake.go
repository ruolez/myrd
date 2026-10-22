// Package fake is an in-memory platform.Backend for tests: it emits a tiny
// fixed H.264 access unit (VP9 frame with CodecVP9) at the configured fps (or, with SetStatic, only the
// first frame, keyframes and refinement passes) and records injected input.
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

// The uncompressed headers of a real VP9 profile 1 (4:4:4) 16x16 key and
// delta frame (libvpx); enough for pion's VP9 payloader.
var vp9Key = []byte{0xa2, 0x49, 0x83, 0x42, 0x40, 0x00, 0x1e, 0x00, 0x1e, 0xc0, 0x47, 0x04, 0x83, 0x83, 0x02, 0x00,
	0x00, 0x04, 0x40, 0x00, 0x69, 0x43, 0xff, 0xff}
var vp9Delta = []byte{0xa6, 0x00, 0x40, 0x92, 0x9c, 0x58, 0x51, 0xe3, 0x30, 0x10, 0x1c, 0x06, 0x03, 0x00, 0x82, 0x77}

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
	static     bool
	refinePend bool // a refinement pass is pending
	refines    []int
	noRefine   bool
	vp9        bool // SupportsCodec(CodecVP9)
	vp9Fails   bool // StartCapture refuses CodecVP9

	canHideWallpaper bool
	wallpaperHidden  bool
	wallpaperCalls   []bool
	locked           bool
	snapshot         platform.Image
	snapshotErr      error
	snapshots        int
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

func (b *Backend) SupportsCodec(codec int) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return codec == platform.CodecH264 || (codec == platform.CodecVP9 && b.vp9)
}

// SetFullColor lets captures use CodecVP9.
func (b *Backend) SetFullColor(on bool) {
	b.mu.Lock()
	b.vp9 = on
	b.mu.Unlock()
}

// SetFullColorFails makes a CodecVP9 StartCapture fail (not recorded in Starts).
func (b *Backend) SetFullColorFails(on bool) {
	b.mu.Lock()
	b.vp9Fails = on
	b.mu.Unlock()
}

func (b *Backend) StartCapture(cfg platform.CaptureConfig, sink platform.CaptureSink) error {
	b.mu.Lock()
	if b.capturing {
		b.mu.Unlock()
		return platform.ErrBusy
	}
	if cfg.Codec == platform.CodecVP9 && b.vp9Fails {
		b.mu.Unlock()
		return platform.ErrUnsupported
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
				key := b.forceKey || (!b.static && n%(fps*2) == 0)
				refine := b.refinePend
				emit := key || refine || !b.static
				b.forceKey, b.refinePend = false, false
				b.mu.Unlock()
				au := deltaAU
				switch {
				case cfg.Codec == platform.CodecVP9 && key:
					au = vp9Key
				case cfg.Codec == platform.CodecVP9:
					au = vp9Delta
				case key:
					au = keyframeAU
				}
				if emit {
					var fl platform.FrameFlags
					if key {
						fl |= platform.FrameKey
					} else if refine {
						fl |= platform.FrameStatic
					}
					sink.Frame(au, time.Since(start).Microseconds(), fl)
				}
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

func (b *Backend) RefineEncoder(maxQP int) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.refines = append(b.refines, maxQP)
	if b.noRefine && maxQP > 0 {
		return platform.ErrUnsupported
	}
	b.refinePend = true
	return nil
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

func (b *Backend) HideWallpaper(hide bool) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.wallpaperCalls = append(b.wallpaperCalls, hide)
	if !b.canHideWallpaper {
		return platform.ErrUnsupported
	}
	b.wallpaperHidden = hide
	return nil
}

func (b *Backend) ConsentPrompt(title, message string, _ int) (platform.Consent, error) {
	if b.PromptFn == nil {
		return platform.ConsentDeny, platform.ErrUnsupported
	}
	return b.PromptFn(title, message), nil
}

// ---- test accessors ----

// SetStatic makes the screen static: after the first frame only keyframes
// and refinement passes are emitted.
func (b *Backend) SetStatic(on bool) {
	b.mu.Lock()
	b.static = on
	b.mu.Unlock()
}

// SetRefineUnsupported makes RefineEncoder refuse a bounded quantizer
// (repeat passes, maxQP 0, still work).
func (b *Backend) SetRefineUnsupported(on bool) {
	b.mu.Lock()
	b.noRefine = on
	b.mu.Unlock()
}

// Refines lists every RefineEncoder quantizer in order, refused ones included.
func (b *Backend) Refines() []int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]int(nil), b.refines...)
}

func (b *Backend) ScreenLocked() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.locked
}

// Snapshot returns the picture set by SetSnapshot (a grey 160×90 by
// default), ErrBusy while capturing and ErrLocked on the unlock screen.
func (b *Backend) Snapshot(_ uint32, maxW, maxH int) (platform.Image, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.snapshots++
	switch {
	case b.snapshotErr != nil:
		return platform.Image{}, b.snapshotErr
	case b.capturing:
		return platform.Image{}, platform.ErrBusy
	case b.locked:
		return platform.Image{}, platform.ErrLocked
	}
	img := b.snapshot
	if img.Pix == nil {
		w, h := min(160, maxW), min(90, maxH)
		img = platform.Image{Pix: make([]byte, w*h*4), Width: w, Height: h, Stride: w * 4}
		for i := range img.Pix {
			img.Pix[i] = 0x80
		}
	}
	return img, nil
}

// SetSnapshot sets the picture (or the error) Snapshot returns.
func (b *Backend) SetSnapshot(img platform.Image, err error) {
	b.mu.Lock()
	b.snapshot, b.snapshotErr = img, err
	b.mu.Unlock()
}

// Snapshots counts Snapshot calls.
func (b *Backend) Snapshots() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.snapshots
}

// SetLocked puts the fake device on its unlock screen or takes it off.
func (b *Backend) SetLocked(locked bool) {
	b.mu.Lock()
	b.locked = locked
	b.mu.Unlock()
}

// SetCanHideWallpaper makes HideWallpaper work; by default it reports
// ErrUnsupported, like the Linux demo device.
func (b *Backend) SetCanHideWallpaper(ok bool) {
	b.mu.Lock()
	b.canHideWallpaper = ok
	b.mu.Unlock()
}

func (b *Backend) WallpaperHidden() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.wallpaperHidden
}

// WallpaperCalls lists every HideWallpaper argument in order.
func (b *Backend) WallpaperCalls() []bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]bool(nil), b.wallpaperCalls...)
}

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
