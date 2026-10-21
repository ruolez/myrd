package main

import (
	"flag"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ruolez/remotectl/agent/internal/native"
	"github.com/ruolez/remotectl/agent/internal/platform"
)

type selftestSink struct {
	start        time.Time
	frames, keys atomic.Int64
	bytes        atomic.Int64
	firstFrame   atomic.Int64 // ns after start
	shapes       atomic.Int64
	mu           sync.Mutex
	lastX, lastY uint16
	visible      bool
	positions    int
	firstAU      []byte
}

func (s *selftestSink) Frame(d []byte, _ int64, flags platform.FrameFlags) {
	if s.frames.Add(1) == 1 {
		s.firstFrame.Store(int64(time.Since(s.start)))
		s.mu.Lock()
		s.firstAU = append([]byte(nil), d[:min(len(d), 1<<20)]...)
		s.mu.Unlock()
	}
	s.bytes.Add(int64(len(d)))
	if flags&platform.FrameKey != 0 {
		s.keys.Add(1)
	}
}
func (s *selftestSink) CursorShape(uint32, uint16, uint16, uint16, uint16, []byte) { s.shapes.Add(1) }
func (s *selftestSink) CursorPos(x, y uint16, v bool) {
	s.mu.Lock()
	s.lastX, s.lastY, s.visible = x, y, v
	s.positions++
	s.mu.Unlock()
}

// nalTypes lists the H.264 NAL unit types in an Annex-B prefix.
func nalTypes(au []byte) []int {
	var out []int
	for i := 0; i+3 < len(au); i++ {
		if au[i] == 0 && au[i+1] == 0 && au[i+2] == 1 {
			out = append(out, int(au[i+3]&0x1f))
			i += 3
		}
	}
	return out
}

// vp9Frame describes a VP9 frame's uncompressed header: its profile (1 is
// 4:4:4) and whether it is a keyframe.
func vp9Frame(f []byte) string {
	if len(f) < 1 || f[0]>>6 != 2 {
		return "not a VP9 frame"
	}
	profile := (f[0]>>5)&1 | ((f[0]>>4)&1)<<1
	key := (f[0]>>3)&1 == 0 && (f[0]>>2)&1 == 0 // show_existing_frame=0, frame_type=0
	return fmt.Sprintf("VP9 profile %d, keyframe=%v", profile, key)
}

func cmdSelftest(args []string) error {
	fs := flag.NewFlagSet("selftest", flag.ExitOnError)
	secs := fs.Int("seconds", 3, "capture duration")
	fps := fs.Int("fps", 30, "target fps")
	fullColor := fs.Bool("full-color", false, "encode full color (VP9 4:4:4) instead of H.264")
	_ = fs.Parse(args)

	b := native.New()
	if err := b.Init(); err != nil {
		return fmt.Errorf("init %s backend: %w", b.Name(), err)
	}
	defer b.Shutdown()
	mons, err := b.Monitors()
	if err != nil {
		return err
	}
	p := b.Permissions()
	fmt.Printf("backend:     %s\nencoder:     %s\npermissions: screen=%v input=%v\nmonitors:\n", b.Name(), b.EncoderName(), p.Screen, p.Input)
	var mon platform.Monitor
	for _, m := range mons {
		fmt.Printf("  #%d %-24s %dx%d at %d,%d scale %.1f primary=%v\n", m.ID, m.Name, m.Width, m.Height, m.X, m.Y, m.Scale, m.Primary)
		if m.Primary {
			mon = m
		}
	}

	cfg := platform.CaptureConfig{MonitorID: mon.ID, FPS: *fps, BitrateKbps: 4000, KeyframeIntervalMS: 2000}
	if *fullColor {
		if !b.SupportsCodec(platform.CodecVP9) {
			return fmt.Errorf("this build cannot encode full color")
		}
		cfg.Codec = platform.CodecVP9
	}
	sink := &selftestSink{start: time.Now()}
	if err := b.StartCapture(cfg, sink); err != nil {
		return fmt.Errorf("capture: %w", err)
	}
	fmt.Printf("capturing:   %s\n", b.EncoderName())
	time.Sleep(500 * time.Millisecond)
	// Input readback: move to 25%/75% of the monitor and watch the cursor stream.
	const tx, ty = 16384, 49151
	injErr := b.Inject(platform.InputEvent{Type: platform.EvMouseMove, MonitorID: mon.ID, X: tx, Y: ty})
	time.Sleep(300 * time.Millisecond)
	b.RequestKeyframe()
	time.Sleep(time.Duration(*secs)*time.Second - 800*time.Millisecond)
	b.StopCapture()

	frames := sink.frames.Load()
	fmt.Printf("capture:     %d frames, %d keyframes, %d bytes in %ds (%.1f fps, %.0f kbps)\n", frames, sink.keys.Load(),
		sink.bytes.Load(), *secs, float64(frames)/float64(*secs), float64(sink.bytes.Load())*8/1000/float64(*secs))
	if *fullColor {
		fmt.Printf("first frame: %.1f ms after start; %s\n", float64(sink.firstFrame.Load())/1e6, vp9Frame(sink.firstAU))
	} else {
		fmt.Printf("first frame: %.1f ms after start; NAL types %v\n", float64(sink.firstFrame.Load())/1e6, nalTypes(sink.firstAU))
	}
	fmt.Printf("cursor:      %d shape(s), %d position updates\n", sink.shapes.Load(), sink.positions)
	sink.mu.Lock()
	dx, dy := int(sink.lastX)-tx, int(sink.lastY)-ty
	fmt.Printf("input:       inject err=%v; cursor now %d,%d (target %d,%d, Δ %d,%d)\n", injErr, sink.lastX, sink.lastY, tx, ty, dx, dy)
	sink.mu.Unlock()
	ok := frames > 0 && sink.keys.Load() > 0 && injErr == nil && abs(dx) < 200 && abs(dy) < 200
	if !ok {
		return fmt.Errorf("selftest FAILED")
	}
	fmt.Println("selftest OK")
	return nil
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
